package main

// The evidence CLI, task 085.
//
// A thin HTTP adapter, so what is asserted is the adaptation: the finding
// reaches the route as a query string, --json forwards the server's own body
// untouched, the human rendering states the difference between "nothing
// happened" and "nothing is known", and a missing required flag is usage rather
// than an API call.

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

const behaviorsBody = `{"version":"1",` +
	`"finding":{"reference_run_id":"run-ref","candidate_run_id":"run-cand","check":"added_behaviors"},` +
	`"status":"resolved","side":"candidate","recorded_count":"2",` +
	`"behaviors":[` +
	`{"fingerprint_id":"fp-export","presence":"added",` +
	`"behavior":{"operation_category":"tool","operation_name":"export_customer","target_name":"export.localhost"},` +
	`"reference_count":"0","candidate_count":"2"}],` +
	`"history":{"state":"complete","retained_count":"4","complete":true},` +
	`"next_after":"fp-export","extra":"kept"}`

const observationsBody = `{"version":"1",` +
	`"finding":{"reference_run_id":"run-ref","candidate_run_id":"run-cand","check":"block_decisions"},` +
	`"status":"resolved","side":"candidate","recorded_count":"2","exhaustive":true,` +
	`"observations":[` +
	`{"sequence":"2","event_id":"c2","timestamp":"2026-03-04T05:06:07Z","fingerprint_id":"fp-export",` +
	`"decision":"block","risk_level":"critical",` +
	`"behavior":{"operation_category":"tool","operation_name":"export_customer","target_name":"export.localhost"}}],` +
	`"history":{"state":"complete","retained_count":"4","complete":true},"extra":[1,2]}`

// The finding travels as a query string, which is what makes the route's URL
// the citable link.
func TestEvidenceCommandsSendTheFindingAsQuery(t *testing.T) {
	for _, tc := range []struct {
		name      string
		args      []string
		body      string
		wantPath  string
		wantQuery map[string]string
	}{
		{
			name: "behaviors by check",
			args: []string{"evidence", "behaviors",
				"--reference-run", "run-ref", "--candidate-run", "run-cand",
				"--check", "added_behaviors"},
			body:     behaviorsBody,
			wantPath: "/v1/evidence/behaviors",
			wantQuery: map[string]string{
				"reference_run_id": "run-ref",
				"candidate_run_id": "run-cand",
				"check":            "added_behaviors",
			},
		},
		{
			name: "observations by behavior and side",
			args: []string{"evidence", "observations",
				"--reference-run", "run-ref", "--candidate-run", "run-cand",
				"--behavior", "fp-shared", "--side", "reference",
				"--after", "7", "--limit", "10"},
			body:     observationsBody,
			wantPath: "/v1/evidence/observations",
			wantQuery: map[string]string{
				"reference_run_id": "run-ref",
				"candidate_run_id": "run-cand",
				"behavior":         "fp-shared",
				"side":             "reference",
				"after":            "7",
				"limit":            "10",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(200, tc.body)

			result := runPlatformCLI(t, append(tc.args, "--api-url", api.url())...)
			result.mustExit(t, exitOK, tc.name)

			request := api.only()
			if request.method != "GET" {
				t.Errorf("method = %s, want GET", request.method)
			}
			if request.escapedPath != tc.wantPath {
				t.Errorf("path = %s, want %s", request.escapedPath, tc.wantPath)
			}
			query, err := url.ParseQuery(request.rawQuery)
			if err != nil {
				t.Fatalf("parse query %q: %v", request.rawQuery, err)
			}
			for key, want := range tc.wantQuery {
				if got := query.Get(key); got != want {
					t.Errorf("query %s = %q, want %q", key, got, want)
				}
			}
			// Nothing the caller did not ask for.
			if len(query) != len(tc.wantQuery) {
				t.Errorf("query = %v, want exactly %v", query, tc.wantQuery)
			}
		})
	}
}

// --json forwards the server's body verbatim, including fields this build does
// not know about.
func TestEvidenceJSONForwardsTheServerBody(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, observationsBody)

	result := runPlatformCLI(t, "evidence", "observations",
		"--reference-run", "run-ref", "--candidate-run", "run-cand",
		"--check", "block_decisions", "--api-url", api.url(), "--json")
	result.mustExit(t, exitOK, "json")

	var got map[string]any
	if err := json.Unmarshal([]byte(result.stdout), &got); err != nil {
		t.Fatalf("stdout is not the server's JSON: %v (%s)", err, result.stdout)
	}
	if _, ok := got["extra"]; !ok {
		t.Error("an unknown top-level field was stripped; --json must forward the " +
			"server's response as it arrived")
	}
	if got["status"] != "resolved" {
		t.Errorf("status = %v, want resolved", got["status"])
	}
}

// The rendering states what an empty answer means, because the payload does not
// look different.
func TestEvidenceRenderingDistinguishesEmptyFromUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		state    string
		complete bool
		wantAny  []string
	}{
		{
			name: "complete history", status: "none_found",
			state: "complete", complete: true,
			wantAny: []string{"none exists"},
		},
		{
			name: "partial history", status: "indeterminate",
			state: "partial", complete: false,
			wantAny: []string{"absence establishes nothing", "partial"},
		},
		{
			name: "unavailable history", status: "indeterminate",
			state: "unavailable", complete: false,
			wantAny: []string{"absence establishes nothing", "unavailable"},
		},
		{
			name: "aggregate only", status: "aggregate_only",
			state: "complete", complete: true,
			wantAny: []string{"no per-observation"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			body, err := json.Marshal(map[string]any{
				"version": "1", "status": tc.status, "side": "candidate",
				"recorded_count": "5", "exhaustive": tc.complete,
				"observations": []any{},
				"history": map[string]any{
					"state": tc.state, "retained_count": "0", "complete": tc.complete,
				},
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			api.reply(200, string(body))

			result := runPlatformCLI(t, "evidence", "observations",
				"--reference-run", "run-ref", "--candidate-run", "run-cand",
				"--check", "block_decisions", "--api-url", api.url())
			result.mustExit(t, exitOK, tc.name)

			for _, want := range tc.wantAny {
				if !strings.Contains(result.stdout, want) {
					t.Errorf("output does not mention %q:\n%s", want, result.stdout)
				}
			}
			// The recorded count is shown even with no rows, so a reader sees
			// that the evidence claims five and shows none.
			if !strings.Contains(result.stdout, "5") {
				t.Errorf("output omits the recorded count:\n%s", result.stdout)
			}
		})
	}
}

// Exhaustive is rendered, because it is the difference between "these are the
// matches" and "these are some of them".
func TestEvidenceRenderingShowsExhaustive(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, observationsBody)

	result := runPlatformCLI(t, "evidence", "observations",
		"--reference-run", "run-ref", "--candidate-run", "run-cand",
		"--check", "block_decisions", "--api-url", api.url())
	result.mustExit(t, exitOK, "render")

	if !strings.Contains(result.stdout, "EXHAUSTIVE true") {
		t.Errorf("output does not state exhaustiveness:\n%s", result.stdout)
	}
	if !strings.Contains(result.stdout, "export_customer") {
		t.Errorf("output does not describe the observation:\n%s", result.stdout)
	}
}

// A missing required flag is usage, and reaches no API.
func TestEvidenceUsageFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no subcommand", []string{"evidence"}},
		{"unknown subcommand", []string{"evidence", "explain"}},
		{"no reference run", []string{"evidence", "behaviors",
			"--candidate-run", "run-cand", "--check", "added_behaviors"}},
		{"no candidate run", []string{"evidence", "observations",
			"--reference-run", "run-ref", "--check", "block_decisions"}},
		{"unknown flag", []string{"evidence", "observations",
			"--reference-run", "run-ref", "--candidate-run", "run-cand", "--invented", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(t)
			result := runPlatformCLI(t, append(tc.args, "--api-url", api.url())...)
			result.mustExit(t, exitUsage, tc.name)
			if got := api.captured(); len(got) != 0 {
				t.Errorf("a usage failure sent %d requests", len(got))
			}
		})
	}
}

// An API failure is the API exit code, and the server's error envelope is what
// the caller sees under --json.
func TestEvidenceAPIFailure(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(400, `{"version":"1","error":{"code":"invalid_request",`+
		`"message":"this comparison holds no behavior fp-nope"}}`)

	result := runPlatformCLI(t, "evidence", "observations",
		"--reference-run", "run-ref", "--candidate-run", "run-cand",
		"--behavior", "fp-nope", "--api-url", api.url(), "--json")
	result.mustExit(t, exitOperational, "invalid finding")

	if !strings.Contains(result.stdout+result.stderr, "invalid_request") {
		t.Errorf("the server's error code did not reach the caller:\n%s\n%s",
			result.stdout, result.stderr)
	}
}
