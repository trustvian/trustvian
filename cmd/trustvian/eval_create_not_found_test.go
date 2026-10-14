package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// eval create against a missing environment said only "not_found: the
// requested resource does not exist", which names nothing (finding F17 of
// #175). The control plane's message is deliberately generic, so the CLI
// reads the references it sent and names the one that is missing. --json still
// forwards the server's own envelope.
func TestEvalCreateNamesTheMissingReference(t *testing.T) {
	const notFound = `{"version":"1","error":{"code":"not_found","message":"the requested resource does not exist"}}`
	tests := []struct {
		name      string
		candidate bool // GET /v1/candidates/c1 answers
		env       bool // GET /v1/projects/p1/environments/staging answers
		want      string
	}{
		{"missing environment", true, false, `environment "staging" does not exist in project "p1"`},
		{"missing candidate", false, true, `candidate "c1" does not exist`},
		{"both exist", true, true, "the requested resource does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.serve(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/candidates/c1" && tt.candidate:
					fmt.Fprint(w, `{"version":"1","id":"c1","agent_id":"a1","metadata":{}}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/agents/a1":
					fmt.Fprint(w, `{"version":"1","id":"a1","project_id":"p1","name":"a"}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/projects/p1/environments/staging" && tt.env:
					fmt.Fprint(w, `{"version":"1","project_id":"p1","ref":"staging"}`)
				default:
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, notFound)
				}
			})
			result := runPlatformCLI(t, "eval", "create", "--id", "r1", "--candidate-id", "c1",
				"--environment", "staging", "--behavioral-profile", "p", "--api-url", api.url())
			result.mustExit(t, exitOperational, tt.name)
			if !strings.Contains(result.stderr, tt.want) {
				t.Errorf("stderr %q does not contain %q", result.stderr, tt.want)
			}

			json := runPlatformCLI(t, "eval", "create", "--id", "r1", "--candidate-id", "c1",
				"--environment", "staging", "--behavioral-profile", "p", "--api-url", api.url(), "--json")
			json.mustExit(t, exitOperational, tt.name+" --json")
			if strings.TrimSpace(json.stderr) != notFound {
				t.Errorf("--json printed %q, want the server's envelope", json.stderr)
			}
		})
	}
}
