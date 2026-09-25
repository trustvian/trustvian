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
	"reflect"
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

// ---------------------------------------------------------------------
// Environment commands (task 065)
// ---------------------------------------------------------------------

// TestEnvironmentCommandRouting pins method, path, query and body for each
// leaf, because each alone is the kind of thing that stays "nearly right".
func TestEnvironmentCommandRouting(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantMethod string
		wantPath   string
		wantQuery  string
		wantBody   map[string]any
	}{
		{
			name:       "create",
			args:       []string{"env", "create", "--project-id", "proj-1", "--ref", "staging", "--name", "Staging"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/environments",
			wantBody:   map[string]any{"project_id": "proj-1", "ref": "staging", "name": "Staging"},
		},
		{
			name: "create with a rank",
			args: []string{"env", "create", "--project-id", "proj-1", "--ref", "prod",
				"--name", "Production", "--rank", "20"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/environments",
			wantBody: map[string]any{
				"project_id": "proj-1", "ref": "prod", "name": "Production", "rank": float64(20)},
		},
		{
			name:       "get",
			args:       []string{"env", "get", "--project-id", "proj-1", "--ref", "staging"},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/projects/proj-1/environments/staging",
		},
		{
			name:       "set name",
			args:       []string{"env", "set", "--project-id", "proj-1", "--ref", "staging", "--revision", "3", "--name", "Staging EU"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/projects/proj-1/environments/staging/configure",
			wantBody:   map[string]any{"revision": float64(3), "name": "Staging EU"},
		},
		{
			name:       "set rank",
			args:       []string{"env", "set", "--project-id", "proj-1", "--ref", "staging", "--revision", "3", "--rank", "0"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/projects/proj-1/environments/staging/configure",
			wantBody:   map[string]any{"revision": float64(3), "rank": float64(0)},
		},
		{
			name:       "clear rank",
			args:       []string{"env", "set", "--project-id", "proj-1", "--ref", "staging", "--revision", "3", "--clear-rank"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/projects/proj-1/environments/staging/configure",
			wantBody:   map[string]any{"revision": float64(3), "clear_rank": true},
		},
		{
			name:       "archive",
			args:       []string{"env", "archive", "--project-id", "proj-1", "--ref", "staging", "--revision", "2"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/projects/proj-1/environments/staging/archive",
			wantBody:   map[string]any{"revision": float64(2)},
		},
		{
			name:       "activate",
			args:       []string{"env", "activate", "--project-id", "proj-1", "--ref", "staging", "--revision", "2"},
			wantMethod: http.MethodPost,
			wantPath:   "/v1/projects/proj-1/environments/staging/activate",
			wantBody:   map[string]any{"revision": float64(2)},
		},
		{
			name:       "list",
			args:       []string{"env", "list", "--project-id", "proj-1"},
			wantMethod: http.MethodGet,
			wantPath:   "/v1/projects/proj-1/environments",
			wantQuery:  "limit=64",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.reply(http.StatusOK, `{"version":"1","project_id":"proj-1","ref":"staging",`+
				`"name":"Staging","status":"active","revision":1,"environments":[]}`)

			result := runPlatformCLI(t, append(tt.args, "--api-url", api.url())...)
			result.mustExit(t, 0, tt.name)

			got := api.only()
			if got.method != tt.wantMethod || got.escapedPath != tt.wantPath {
				t.Errorf("%s %s, want %s %s", got.method, got.escapedPath, tt.wantMethod, tt.wantPath)
			}
			if tt.wantQuery != "" && got.rawQuery != tt.wantQuery {
				t.Errorf("query = %q, want %q", got.rawQuery, tt.wantQuery)
			}
			if tt.wantBody == nil {
				return
			}
			var body map[string]any
			if err := json.Unmarshal(got.body, &body); err != nil {
				t.Fatalf("request body is not JSON: %v (%s)", err, got.body)
			}
			if !reflect.DeepEqual(body, tt.wantBody) {
				t.Errorf("body = %v, want %v", body, tt.wantBody)
			}
		})
	}
}

// env list follows next_after until it is absent, so a project bigger than one
// page still lists completely.
func TestEnvironmentListFollowsEveryPage(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after") {
		case "":
			fmt.Fprint(w, `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1},
				{"project_id":"proj-1","ref":"b","name":"B","rank":20,"status":"active","revision":1}
			],"next_after":"b"}`)
		case "b":
			fmt.Fprint(w, `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"c","name":"C","rank":10,"status":"archived","revision":2}
			]}`)
		default:
			t.Errorf("unexpected cursor %q", r.URL.Query().Get("after"))
		}
	})

	result := runPlatformCLI(t, "env", "list", "--project-id", "proj-1", "--api-url", api.url())
	result.mustExit(t, 0, "env list")

	if got := len(api.captured()); got != 2 {
		t.Errorf("made %d requests, want 2 — the second page was not followed", got)
	}
	for _, ref := range []string{"a", "b", "c"} {
		if !strings.Contains(result.stdout, ref) {
			t.Errorf("output is missing %q:\n%s", ref, result.stdout)
		}
	}
	// Ranked first in rank order, unranked last — display ordering over the
	// rank the server supplied, not a promotion decision.
	if want := []string{"c", "b", "a"}; !inOrder(result.stdout, want) {
		t.Errorf("rows are not in rank order, want %v:\n%s", want, result.stdout)
	}
}

// --json over a paged listing forwards the server's own field names, with
// every row exactly as it arrived — including a field this build does not
// know about.
func TestEnvironmentListJSONPreservesUnknownFields(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("after") {
		case "":
			fmt.Fprint(w, `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1,
				 "future_field":"kept"}
			],"next_after":"a"}`)
		default:
			fmt.Fprint(w, `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"b","name":"B","status":"active","revision":1}
			]}`)
		}
	})

	result := runPlatformCLI(t, "env", "list", "--project-id", "proj-1",
		"--api-url", api.url(), "--json")
	result.mustExit(t, 0, "env list --json")

	var body struct {
		Version      string           `json:"version"`
		ProjectID    string           `json:"project_id"`
		Environments []map[string]any `json:"environments"`
		NextAfter    string           `json:"next_after"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &body); err != nil {
		t.Fatalf("--json output is not JSON: %v (%s)", err, result.stdout)
	}
	if body.Version != "1" || body.ProjectID != "proj-1" {
		t.Errorf("envelope = %+v, want the route's own shape", body)
	}
	if len(body.Environments) != 2 {
		t.Fatalf("aggregated %d rows, want 2", len(body.Environments))
	}
	if body.Environments[0]["future_field"] != "kept" {
		t.Error("a field this build does not know about was dropped from --json output")
	}
	if body.NextAfter != "" {
		t.Errorf("next_after = %q, want it absent: the traversal finished", body.NextAfter)
	}
}

// TestEnvironmentListHasNoTraversalCeiling is the regression for a fixed
// 256-page bound this command used to carry.
//
// The creation cap is 64 per project, but the number of environments a
// *migrated* project holds is whatever its history referenced — task 065 is
// explicit that migration preserves everything and caps nothing. A traversal
// ceiling would therefore turn a legitimately large project into a truncated
// answer that looked complete, which is the worst shape a wrong answer can
// take. Three hundred pages here, well past the old limit, and every row must
// arrive.
func TestEnvironmentListHasNoTraversalCeiling(t *testing.T) {
	const pages = 300

	// One row per page keeps this cheap: what is being tested is the number
	// of continuations followed, not the number of rows carried.
	ref := func(page int) string { return fmt.Sprintf("env-%04d", page) }

	api := newFakeAPI(t)
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		page := 0
		if after != "" {
			var n int
			if _, err := fmt.Sscanf(after, "env-%04d", &n); err != nil {
				t.Errorf("unexpected cursor %q", after)
				return
			}
			page = n + 1
		}
		w.Header().Set("Content-Type", "application/json")
		next := ""
		if page < pages-1 {
			next = fmt.Sprintf(`,"next_after":%q`, ref(page))
		}
		fmt.Fprintf(w, `{"version":"1","project_id":"proj-1","environments":[
			{"project_id":"proj-1","ref":%q,"name":"E","status":"active","revision":1}
		]%s}`, ref(page), next)
	})

	result := runPlatformCLI(t, "env", "list", "--project-id", "proj-1",
		"--api-url", api.url(), "--json")
	result.mustExit(t, 0, "env list across 300 pages")

	if got := len(api.captured()); got != pages {
		t.Errorf("made %d requests, want %d — the traversal stopped early", got, pages)
	}

	var body struct {
		Environments []map[string]any `json:"environments"`
		NextAfter    string           `json:"next_after"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &body); err != nil {
		t.Fatalf("--json output is not JSON: %v", err)
	}
	if len(body.Environments) != pages {
		t.Fatalf("aggregated %d rows, want %d", len(body.Environments), pages)
	}
	if body.NextAfter != "" {
		t.Errorf("next_after = %q, want it absent", body.NextAfter)
	}
	// Every row exactly once, in the order the server sent them.
	for i, row := range body.Environments {
		if row["ref"] != ref(i) {
			t.Fatalf("row %d = %v, want %s", i, row["ref"], ref(i))
		}
	}
}

// A pagination stream that cannot make progress is reported, not followed.
//
// Removing the page ceiling means the loop is bounded by cursor progress
// alone, so each way a cursor can fail to progress has to terminate on its
// own. None of these is reachable from a correct server; all of them loop
// forever if believed.
func TestEnvironmentListRefusesNonProgressingPagination(t *testing.T) {
	tests := []struct {
		name    string
		first   string
		second  string
		wantErr string
	}{
		{
			name: "cursor repeats itself",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"b","name":"B","status":"active","revision":1}
			],"next_after":"b"}`,
			second: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"b","name":"B","status":"active","revision":1}
			],"next_after":"b"}`,
			wantErr: "did not advance",
		},
		{
			name: "cursor moves backwards",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"m","name":"M","status":"active","revision":1}
			],"next_after":"m"}`,
			second: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"a"}`,
			wantErr: "did not advance",
		},
		{
			name: "continuation on an empty page",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"a"}`,
			second:  `{"version":"1","project_id":"proj-1","environments":[],"next_after":"zz"}`,
			wantErr: "empty page",
		},
		{
			name: "cursor is not the page's last row",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"zzz"}`,
			second:  `{"version":"1","project_id":"proj-1","environments":[]}`,
			wantErr: "is not the page's last environment",
		},
		{
			name: "project changes mid-traversal",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"a"}`,
			second: `{"version":"1","project_id":"proj-2","environments":[
				{"project_id":"proj-2","ref":"b","name":"B","status":"active","revision":1}
			]}`,
			wantErr: "changed project mid-traversal",
		},
		{
			name: "version changes mid-traversal",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"a"}`,
			second: `{"version":"2","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"b","name":"B","status":"active","revision":1}
			]}`,
			wantErr: "changed version mid-traversal",
		},
		{
			name: "a page is not JSON",
			first: `{"version":"1","project_id":"proj-1","environments":[
				{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
			],"next_after":"a"}`,
			second:  `not json at all`,
			wantErr: "not valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.serve(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Query().Get("after") == "" {
					fmt.Fprint(w, tt.first)
					return
				}
				fmt.Fprint(w, tt.second)
			})

			result := runPlatformCLI(t, "env", "list", "--project-id", "proj-1",
				"--api-url", api.url(), "--json")
			result.mustExit(t, exitOperational, tt.name)
			if !strings.Contains(result.stderr, tt.wantErr) {
				t.Errorf("stderr = %q, want it to mention %q", result.stderr, tt.wantErr)
			}
			if result.stdout != "" {
				t.Errorf("a broken traversal still printed output:\n%s", result.stdout)
			}
		})
	}
}

// The aggregated envelope keeps what it does not understand.
//
// `env list --json` is the CLI's one synthesized document, so a field a newer
// server adds to the *envelope* has to survive the synthesis the same way a
// field added to a row does. Only "environments" and "next_after" are the
// aggregation's to own.
func TestEnvironmentListJSONPreservesUnknownEnvelopeFields(t *testing.T) {
	api := newFakeAPI(t)
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("after") == "" {
			fmt.Fprint(w, `{"version":"1","project_id":"proj-1",
				"future_total":131,"future_note":{"nested":true},
				"environments":[
					{"project_id":"proj-1","ref":"a","name":"A","status":"active","revision":1}
				],"next_after":"a"}`)
			return
		}
		fmt.Fprint(w, `{"version":"1","project_id":"proj-1","environments":[
			{"project_id":"proj-1","ref":"b","name":"B","status":"active","revision":1}
		]}`)
	})

	result := runPlatformCLI(t, "env", "list", "--project-id", "proj-1",
		"--api-url", api.url(), "--json")
	result.mustExit(t, 0, "env list --json")

	// Exactly one JSON document, not one per page.
	decoder := json.NewDecoder(strings.NewReader(result.stdout))
	var envelope map[string]any
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("--json output is not JSON: %v (%s)", err, result.stdout)
	}
	if decoder.More() {
		t.Error("--json emitted more than one JSON document; the traversal must " +
			"produce one completed collection")
	}

	if envelope["future_total"] != float64(131) {
		t.Errorf("future_total = %v, want the server's value preserved", envelope["future_total"])
	}
	nested, ok := envelope["future_note"].(map[string]any)
	if !ok || nested["nested"] != true {
		t.Errorf("future_note = %v, want the server's object preserved", envelope["future_note"])
	}
	if _, present := envelope["next_after"]; present {
		t.Error("next_after survived a completed traversal")
	}
	rows, ok := envelope["environments"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("environments = %v, want both rows", envelope["environments"])
	}
}

func TestEnvironmentCommandUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no subcommand", []string{"env"}},
		{"unknown subcommand", []string{"env", "promote"}},
		{"create without a project", []string{"env", "create", "--ref", "r", "--name", "N"}},
		{"create without a ref", []string{"env", "create", "--project-id", "p", "--name", "N"}},
		{"create with a rank above the bound", []string{"env", "create",
			"--project-id", "p", "--ref", "r", "--name", "N", "--rank", "10000"}},
		{"create with a negative rank", []string{"env", "create",
			"--project-id", "p", "--ref", "r", "--name", "N", "--rank", "-1"}},
		{"set without a revision", []string{"env", "set",
			"--project-id", "p", "--ref", "r", "--name", "N"}},
		{"set with nothing to change", []string{"env", "set",
			"--project-id", "p", "--ref", "r", "--revision", "1"}},
		{"set with rank and clear-rank", []string{"env", "set", "--project-id", "p",
			"--ref", "r", "--revision", "1", "--rank", "1", "--clear-rank"}},
		{"archive without a revision", []string{"env", "archive", "--project-id", "p", "--ref", "r"}},
		{"activate without a revision", []string{"env", "activate", "--project-id", "p", "--ref", "r"}},
		{"list without a project", []string{"env", "list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeAPI(t)
			result := runPlatformCLI(t, append(tt.args, "--api-url", api.url())...)
			result.mustExit(t, exitUsage, tt.name)
			if len(api.captured()) != 0 {
				t.Error("a usage error still reached the API")
			}
		})
	}
}

// A server-side refusal is operational, and the server's own error envelope
// is what --json forwards.
func TestEnvironmentCommandServerErrors(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(http.StatusConflict,
		`{"version":"1","error":{"code":"conflict","message":"project has reached its environment limit"}}`)

	result := runPlatformCLI(t, "env", "create", "--project-id", "p", "--ref", "r",
		"--name", "N", "--api-url", api.url())
	result.mustExit(t, exitOperational, "create at the cap")
	if !strings.Contains(result.stderr, "environment limit") {
		t.Errorf("stderr does not carry the server's message:\n%s", result.stderr)
	}

	jsonResult := runPlatformCLI(t, "env", "create", "--project-id", "p", "--ref", "r",
		"--name", "N", "--api-url", api.url(), "--json")
	jsonResult.mustExit(t, exitOperational, "create at the cap --json")
	if !strings.Contains(jsonResult.stderr, `"code":"conflict"`) {
		t.Errorf("--json did not forward the server's envelope:\n%s", jsonResult.stderr)
	}
}

// inOrder reports whether each want appears after the previous one.
func inOrder(text string, want []string) bool {
	at := 0
	for _, token := range want {
		index := strings.Index(text[at:], token)
		if index < 0 {
			return false
		}
		at += index + len(token)
	}
	return true
}
