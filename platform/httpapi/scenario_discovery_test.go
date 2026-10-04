package httpapi_test

// Task 102: discovering recorded scenario executions and checking whether one
// may be reused as a reference, over HTTP.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScenarioExecutionsAreListedAndReferenceEligibilityIsTheServers(t *testing.T) {
	a := newAPI(t)

	// e1: completed, its reference runs completed with evidence — usable.
	a.beginExecution("e1", 1, nil, 201)
	a.completeIsolatedRun("e1-reference-1", []string{"read"})
	a.completeIsolatedRun("e1-candidate-1", []string{"read", "export"})
	a.completeExecution("e1", []string{"e1-reference-1"}, []string{"e1-candidate-1"}, 200)
	// e2: still running — never a reference.
	a.beginExecution("e2", 1, nil, 201)
	// e3: failed — never a reference.
	a.beginExecution("e3", 1, nil, 201)
	a.mustStatus(a.do("POST", "/v1/scenario-executions/e3/fail", map[string]any{}), 200, "fail e3")

	response := a.do("GET", "/v1/projects/proj-1/scenario-executions", nil)
	a.mustStatus(response, 200, "list")
	var list struct {
		Order      string `json:"order"`
		NextAfter  string `json:"next_after"`
		Executions []struct {
			ID           string `json:"id"`
			ScenarioName string `json:"scenario_name"`
			Status       string `json:"status"`
			Verdict      string `json:"verdict"`
			AgentID      string `json:"agent_id"`
			Environment  string `json:"environment"`
			StartedAt    string `json:"started_at"`
			Runs         int    `json:"runs"`
		} `json:"executions"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Order != "started_at_desc" || len(list.Executions) != 3 {
		t.Fatalf("list = %+v", list)
	}
	// A fixed clock: equal start keys, so identifier descending.
	if list.Executions[0].ID != "e3" || list.Executions[2].ID != "e1" {
		t.Errorf("order = %v", list.Executions)
	}
	if e := list.Executions[2]; e.Status != "completed" || e.Verdict != "fail" || e.Runs != 1 ||
		e.ScenarioName != "support" || e.AgentID != "agent-1" || e.StartedAt == "" {
		t.Errorf("completed entry = %+v", e)
	}
	if strings.Contains(response.Body.String(), `"repetitions"`) {
		t.Error("a list entry carries run associations; the by-id route keeps them")
	}

	filtered := a.do("GET", "/v1/projects/proj-1/scenario-executions?scenario=other&limit=1", nil)
	if !strings.Contains(filtered.Body.String(), `"executions":[]`) {
		t.Errorf("a filter matching nothing = %s", filtered.Body.String())
	}
	paged := a.do("GET", "/v1/projects/proj-1/scenario-executions?limit=1", nil)
	var first struct {
		NextAfter string `json:"next_after"`
	}
	_ = json.Unmarshal(paged.Body.Bytes(), &first)
	if first.NextAfter == "" {
		t.Error("a full page with more following published no next_after")
	}
	next := a.do("GET", "/v1/projects/proj-1/scenario-executions?limit=1&after="+first.NextAfter, nil)
	if !strings.Contains(next.Body.String(), `"id":"e2"`) {
		t.Errorf("continuation = %s", next.Body.String())
	}

	check := func(id string) (usable bool, reason string, runs int) {
		t.Helper()
		r := a.do("GET", "/v1/scenario-executions/"+id+"/reference-check", nil)
		a.mustStatus(r, 200, "check "+id)
		var body struct {
			Usable      bool   `json:"usable"`
			Reason      string `json:"reason"`
			Runs        int    `json:"runs"`
			ProjectID   string `json:"project_id"`
			Environment string `json:"environment"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.ProjectID != "proj-1" || body.Environment != testEnvironment {
			t.Errorf("%s check states conditions %+v", id, body)
		}
		return body.Usable, body.Reason, body.Runs
	}
	if usable, reason, runs := check("e1"); !usable || reason != "" || runs != 1 {
		t.Errorf("e1 = usable %v reason %q runs %d; a completed execution with completed reference runs is usable",
			usable, reason, runs)
	}
	for _, id := range []string{"e2", "e3"} {
		if usable, reason, _ := check(id); usable || !strings.Contains(reason, "only a completed execution can be a reference") {
			t.Errorf("%s = usable %v reason %q; the server's own reason must be relayed", id, usable, reason)
		}
	}
	a.mustStatus(a.do("GET", "/v1/scenario-executions/nope/reference-check", nil), 404, "missing execution")
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/scenario-executions?after=e1", nil), 400, "bad cursor")
	a.mustStatus(a.do("GET", "/v1/projects/proj-1/scenario-executions?limit=0", nil), 400, "bad limit")
}
