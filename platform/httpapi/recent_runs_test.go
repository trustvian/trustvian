package httpapi_test

// The /v1 recency-ordered run collection, task 101.
//
// The handler under test runs on a fixed clock, so every run here shares one
// creation key: the order these tests see is the identifier tie-break, which
// is exactly the part of the contract a clock cannot exercise. Time ordering
// itself is asserted on both backends by the platform conformance suite.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

type recentRunsBody struct {
	Version     string `json:"version"`
	ProjectID   string `json:"project_id"`
	AgentID     string `json:"agent_id"`
	CandidateID string `json:"candidate_id"`
	Order       string `json:"order"`
	NextAfter   string `json:"next_after"`
	Runs        []struct {
		ID          string `json:"id"`
		CandidateID string `json:"candidate_id"`
		Status      string `json:"status"`
		CreatedAt   string `json:"created_at"`
	} `json:"evaluation_runs"`
}

func decodeRecent(t *testing.T, r *httptest.ResponseRecorder) recentRunsBody {
	t.Helper()
	var body recentRunsBody
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode recent runs: %v (%s)", err, r.Body.String())
	}
	return body
}

func (a *api) seedRecentRuns() {
	a.t.Helper()
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
		"id": "cand-2", "agent_id": "agent-1", "metadata": map[string]string{},
	}), 201, "create second candidate")
	a.mustStatus(a.do("POST", "/v1/projects", map[string]string{"id": "proj-2", "name": "Other"}), 201, "project 2")
	a.mustStatus(a.do("POST", "/v1/agents", map[string]string{
		"id": "agent-x", "project_id": "proj-2", "name": "Other agent",
	}), 201, "agent x")
	for _, run := range []struct{ id, candidate string }{
		{"run-1", "cand-1"}, {"run-2", "cand-2"}, {"run-3", "cand-1"},
	} {
		a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
			"id": run.id, "candidate_id": run.candidate,
			"environment": testEnvironment, "behavioral_profile": testProfile,
		}), 201, "create "+run.id)
	}
}

func TestRecentRunsRouteListsNewestFirstAndSaysSo(t *testing.T) {
	a := newAPI(t)
	a.seedRecentRuns()

	response := a.do("GET", "/v1/projects/proj-1/evaluation-runs/recent", nil)
	a.mustStatus(response, 200, "recent runs")
	body := decodeRecent(t, response)
	if body.Version != "1" || body.ProjectID != "proj-1" || body.Order != "created_at_desc" {
		t.Errorf("envelope = %+v", body)
	}
	if body.AgentID != "" || body.CandidateID != "" || strings.Contains(response.Body.String(), `"agent_id"`) {
		t.Errorf("a narrowing nobody sent is echoed: %s", response.Body.String())
	}
	var ids []string
	for _, run := range body.Runs {
		ids = append(ids, run.ID)
	}
	if strings.Join(ids, ",") != "run-3,run-2,run-1" {
		t.Errorf("runs = %v, want [run-3 run-2 run-1] (equal keys, identifier descending)", ids)
	}

	narrowed := decodeRecent(t, a.do("GET",
		"/v1/projects/proj-1/evaluation-runs/recent?agent_id=agent-1&candidate_id=cand-2", nil))
	if len(narrowed.Runs) != 1 || narrowed.Runs[0].ID != "run-2" || narrowed.CandidateID != "cand-2" {
		t.Errorf("candidate narrowing = %+v", narrowed)
	}
	if empty := decodeRecent(t, a.do("GET", "/v1/projects/proj-2/evaluation-runs/recent", nil)); len(empty.Runs) != 0 ||
		!strings.Contains(a.do("GET", "/v1/projects/proj-2/evaluation-runs/recent", nil).Body.String(), `"evaluation_runs":[]`) {
		t.Errorf("a project with no runs must publish an empty list, not null: %+v", empty)
	}
}

func TestRecentRunsRoutePagesExactly(t *testing.T) {
	a := newAPI(t)
	a.seedRecentRuns()

	var seen []string
	path := "/v1/projects/proj-1/evaluation-runs/recent?limit=2"
	for range 5 {
		response := a.do("GET", path, nil)
		a.mustStatus(response, 200, "page")
		page := decodeRecent(t, response)
		for _, run := range page.Runs {
			seen = append(seen, run.ID)
		}
		if page.NextAfter == "" {
			break
		}
		path = "/v1/projects/proj-1/evaluation-runs/recent?limit=2&after=" + page.NextAfter
	}
	if strings.Join(seen, ",") != "run-3,run-2,run-1" {
		t.Errorf("paged runs = %v, want each once, newest first", seen)
	}
	full := decodeRecent(t, a.do("GET", "/v1/projects/proj-1/evaluation-runs/recent?limit=3", nil))
	if len(full.Runs) != 3 || full.NextAfter != "" {
		t.Errorf("an exactly-full last page published next_after %q", full.NextAfter)
	}
}

func TestRecentRunsRouteRefusesInvalidScopeAndInput(t *testing.T) {
	a := newAPI(t)
	a.seedRecentRuns()

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/v1/projects/nope/evaluation-runs/recent", 404},
		{"/v1/projects/proj-1/evaluation-runs/recent?agent_id=nope", 404},
		{"/v1/projects/proj-1/evaluation-runs/recent?candidate_id=nope", 404},
		{"/v1/projects/proj-1/evaluation-runs/recent?agent_id=agent-x", 400},
		{"/v1/projects/proj-2/evaluation-runs/recent?candidate_id=cand-1", 400},
		{"/v1/projects/proj-1/evaluation-runs/recent?limit=0", 400},
		{"/v1/projects/proj-1/evaluation-runs/recent?limit=65", 400},
		{"/v1/projects/proj-1/evaluation-runs/recent?after=run-1", 400},
		{"/v1/projects/proj-1/evaluation-runs/recent?after=00000000000000000001.", 400},
	} {
		a.mustStatus(a.do("GET", tc.path, nil), tc.want, tc.path)
	}
}
