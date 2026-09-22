package main

// Task 060: the control-plane CLI.
//
// Every test here drives a real httptest.Server. The CLI's whole job is to
// speak HTTP correctly, so a mocked transport would verify the part that isn't
// interesting and skip the part that is.
//
// No test needs a database. Task 058 owns server-side semantics; these prove
// the client sends the right request, reads the response without damaging it,
// and picks the documented exit code.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testTimeout keeps a hung fake server from stalling the suite. Injected
// through runPlatform rather than a CLI flag: a public --timeout would be a
// new operational surface that exists only for tests.
const testTimeout = 5 * time.Second

// capturedRequest is what the fake API saw.
type capturedRequest struct {
	method      string
	escapedPath string
	rawQuery    string
	body        []byte
	contentType string
	accept      string
	authHeaders []string
}

// fakeAPI is a control plane that records what it was asked.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server

	mu       sync.Mutex
	requests []capturedRequest

	// status and body are the canned reply; handler overrides both when set.
	status  int
	body    string
	handler http.HandlerFunc
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{t: t, status: http.StatusOK, body: `{"version":"1"}`}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAll(r)
		api.mu.Lock()
		api.requests = append(api.requests, capturedRequest{
			method:      r.Method,
			escapedPath: r.URL.EscapedPath(),
			rawQuery:    r.URL.RawQuery,
			body:        body,
			contentType: r.Header.Get("Content-Type"),
			accept:      r.Header.Get("Accept"),
			authHeaders: r.Header.Values("Authorization"),
		})
		handler, status, replyBody := api.handler, api.status, api.body
		api.mu.Unlock()

		if handler != nil {
			handler(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, replyBody)
	}))
	t.Cleanup(api.server.Close)
	return api
}

func readAll(r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	if r.Body == nil {
		return nil, nil
	}
	_, err := buf.ReadFrom(r.Body)
	return buf.Bytes(), err
}

func (a *fakeAPI) reply(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.body, a.handler = status, body, nil
}

func (a *fakeAPI) serve(handler http.HandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.handler = handler
}

func (a *fakeAPI) captured() []capturedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]capturedRequest(nil), a.requests...)
}

// only asserts exactly one request arrived and returns it.
func (a *fakeAPI) only() capturedRequest {
	a.t.Helper()
	got := a.captured()
	if len(got) != 1 {
		a.t.Fatalf("request count = %d, want exactly 1", len(got))
	}
	return got[0]
}

func (a *fakeAPI) url() string { return a.server.URL }

// cliResult is one command's full observable outcome.
type cliResult struct {
	stdout string
	stderr string
	code   int
}

// runPlatformCLI drives a control-plane command with captured streams.
func runPlatformCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	return runPlatformCLITimeout(t, testTimeout, args...)
}

func runPlatformCLITimeout(t *testing.T, timeout time.Duration, args ...string) cliResult {
	t.Helper()
	var out, errOut bytes.Buffer
	code, handled := runPlatform(streams{out: &out, err: &errOut}, args, timeout)
	if !handled {
		t.Fatalf("runPlatform did not handle %q", args)
	}
	return cliResult{stdout: out.String(), stderr: errOut.String(), code: code}
}

func (r cliResult) mustExit(t *testing.T, want int, context string) {
	t.Helper()
	if r.code != want {
		t.Fatalf("%s: exit = %d, want %d\nstdout: %s\nstderr: %s",
			context, r.code, want, r.stdout, r.stderr)
	}
}

// ---------------------------------------------------------------------
// Routing: method, path and body for every leaf command
// ---------------------------------------------------------------------

// TestPlatformCommandRouting pins the wire shape of all sixteen leaves.
//
// Route, method and request body together, because each alone is the kind of
// thing that stays "nearly right": a correct body sent to the wrong path fails
// in production, not in a test that only checked the body.
func TestPlatformCommandRouting(t *testing.T) {
	recordPath := writeRecordFile(t, `{"event_id":"evt-1","fingerprint_id":"fp-1"}`)

	tests := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantBody   map[string]any
		// noBody marks the lifecycle posts that legitimately send nothing.
		noBody bool
	}{
		{
			name:       "project create",
			args:       []string{"project", "create", "--id", "proj-1", "--name", "Checkout"},
			wantMethod: "POST", wantPath: "/v1/projects",
			wantBody: map[string]any{"id": "proj-1", "name": "Checkout"},
		},
		{
			name:       "project get",
			args:       []string{"project", "get", "--id", "proj-1"},
			wantMethod: "GET", wantPath: "/v1/projects/proj-1", noBody: true,
		},
		{
			name: "agent create",
			args: []string{"agent", "create", "--id", "agent-1",
				"--project-id", "proj-1", "--name", "Checkout agent"},
			wantMethod: "POST", wantPath: "/v1/agents",
			wantBody: map[string]any{
				"id": "agent-1", "project_id": "proj-1", "name": "Checkout agent"},
		},
		{
			name:       "agent get",
			args:       []string{"agent", "get", "--id", "agent-1"},
			wantMethod: "GET", wantPath: "/v1/agents/agent-1", noBody: true,
		},
		{
			name: "candidate create",
			args: []string{"candidate", "create", "--id", "cand-1", "--agent-id", "agent-1",
				"--label", "v2", "--source-ref", "abc123", "--model", "m-1"},
			wantMethod: "POST", wantPath: "/v1/candidates",
			wantBody: map[string]any{
				"id": "cand-1", "agent_id": "agent-1",
				"metadata": map[string]any{"label": "v2", "source_ref": "abc123", "model": "m-1"},
			},
		},
		{
			name:       "candidate get",
			args:       []string{"candidate", "get", "--id", "cand-1"},
			wantMethod: "GET", wantPath: "/v1/candidates/cand-1", noBody: true,
		},
		{
			name: "eval create",
			args: []string{"eval", "create", "--id", "run-42", "--candidate-id", "cand-1",
				"--environment", "local", "--behavioral-profile", "checkout-agent"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs",
			wantBody: map[string]any{
				"id": "run-42", "candidate_id": "cand-1",
				"environment": "local", "behavioral_profile": "checkout-agent"},
		},
		{
			name:       "eval get",
			args:       []string{"eval", "get", "--id", "run-42"},
			wantMethod: "GET", wantPath: "/v1/evaluation-runs/run-42", noBody: true,
		},
		{
			name:       "eval start",
			args:       []string{"eval", "start", "--id", "run-42"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/start", noBody: true,
		},
		{
			name:       "eval complete",
			args:       []string{"eval", "complete", "--id", "run-42"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/complete", noBody: true,
		},
		{
			name:       "eval cancel",
			args:       []string{"eval", "cancel", "--id", "run-42"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/cancel", noBody: true,
		},
		{
			name:       "eval fail",
			args:       []string{"eval", "fail", "--id", "run-42", "--reason", "crashed"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/fail",
			wantBody: map[string]any{"reason": "crashed"},
		},
		{
			// Empty is allowed: the API accepts it, and inventing a reason
			// would put CLI-authored text into durable state.
			name:       "eval fail without a reason",
			args:       []string{"eval", "fail", "--id", "run-42"},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/fail",
			wantBody: map[string]any{"reason": ""},
		},
		{
			name:       "eval progress",
			args:       []string{"eval", "progress", "--id", "run-42"},
			wantMethod: "GET", wantPath: "/v1/evaluation-runs/run-42/progress", noBody: true,
		},
		{
			name:       "eval ingest-state",
			args:       []string{"eval", "ingest-state", "--id", "run-42"},
			wantMethod: "GET", wantPath: "/v1/evaluation-runs/run-42/ingest-state", noBody: true,
		},
		{
			name: "eval ingest",
			args: []string{"eval", "ingest", "--id", "run-42", "--sequence", "7",
				"--behavioral-profile", "checkout-agent", "--record", recordPath},
			wantMethod: "POST", wantPath: "/v1/evaluation-runs/run-42/records",
			wantBody: map[string]any{
				"version": "1", "sequence": "7", "behavioral_profile": "checkout-agent",
				"record": map[string]any{"event_id": "evt-1", "fingerprint_id": "fp-1"},
			},
		},
		{
			name: "eval compare",
			args: []string{"eval", "compare", "--reference-run", "ref", "--candidate-run", "cand",
				"--max-added-behaviors", "0", "--max-block-decisions", "0",
				"--max-critical-risk-observations", "0"},
			wantMethod: "POST", wantPath: "/v1/evaluations/compare",
			wantBody: map[string]any{
				"reference_run_id": "ref", "candidate_run_id": "cand",
				"gate_limits": map[string]any{
					"max_added_behaviors": "0", "max_block_decisions": "0",
					"max_critical_risk_observations": "0"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			if strings.HasPrefix(tt.name, "eval compare") {
				api.reply(200, passingComparison(0, "0"))
			}

			result := runPlatformCLI(t, append(tt.args, "--api-url", api.url())...)
			result.mustExit(t, exitOK, tt.name)

			got := api.only()
			if got.method != tt.wantMethod {
				t.Errorf("method = %s, want %s", got.method, tt.wantMethod)
			}
			if got.escapedPath != tt.wantPath {
				t.Errorf("path = %s, want %s", got.escapedPath, tt.wantPath)
			}
			if got.rawQuery != "" {
				t.Errorf("query = %q, want none", got.rawQuery)
			}
			if got.accept != "application/json" {
				t.Errorf("Accept = %q, want application/json", got.accept)
			}
			// Task 070 owns authentication; nothing here may claim it.
			if len(got.authHeaders) != 0 {
				t.Errorf("Authorization headers = %v, want none", got.authHeaders)
			}

			if tt.noBody {
				if len(got.body) != 0 {
					t.Errorf("body = %s, want none", got.body)
				}
				return
			}
			if got.contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got.contentType)
			}
			var decoded map[string]any
			if err := json.Unmarshal(got.body, &decoded); err != nil {
				t.Fatalf("request body is not JSON: %v (%s)", err, got.body)
			}
			if !jsonEqual(decoded, tt.wantBody) {
				t.Errorf("body =\n%s\nwant\n%s", mustJSON(decoded), mustJSON(tt.wantBody))
			}
		})
	}
}

func jsonEqual(a, b any) bool { return mustJSON(a) == mustJSON(b) }

func mustJSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("<unencodable: %v>", err)
	}
	return string(encoded)
}

func writeRecordFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// passingComparison builds a comparison body with a chosen verdict.
//
// addedCount and maximum are free parameters so a test can make the evidence
// and the verdict disagree — see TestCompareTrustsTheServersVerdict.
func passingComparison(addedCount int, maximum string) string {
	return comparisonBody(gateVerdictPass, addedCount, maximum)
}

func comparisonBody(verdict string, addedCount int, maximum string) string {
	return fmt.Sprintf(`{
	  "version": "1",
	  "reference_run_id": "ref",
	  "candidate_run_id": "cand",
	  "behavior_diff": {"added_count": %d, "removed_count": 1, "shared_count": 9},
	  "gate": {
	    "reference_evidence": {"actual":"10","minimum":"1","passed":true},
	    "candidate_evidence": {"actual":"12","minimum":"1","passed":true},
	    "added_behaviors": {"actual":"%d","maximum":"%s","passed":true},
	    "block_decisions": {"actual":"0","maximum":"0","passed":true},
	    "critical_risk_observations": {"actual":"0","maximum":"0","passed":true},
	    "verdict": "%s"
	  }
	}`, addedCount, addedCount, maximum, verdict)
}
