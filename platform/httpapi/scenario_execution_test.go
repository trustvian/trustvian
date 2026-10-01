package httpapi_test

// /v1/scenario-executions over HTTP (task 078, ADR 0054).

import (
	"encoding/json"
	"fmt"
	"testing"
)

type executionBody struct {
	ID                   string `json:"id"`
	ScenarioName         string `json:"scenario_name"`
	ProjectID            string `json:"project_id"`
	AgentID              string `json:"agent_id"`
	Environment          string `json:"environment"`
	Runs                 int    `json:"runs"`
	ReferenceExecutionID string `json:"reference_execution_id"`
	Status               string `json:"status"`
	StartedAt            string `json:"started_at"`
	FinishedAt           string `json:"finished_at"`
	CompletionSequence   string `json:"completion_sequence"`
	Verdict              string `json:"verdict"`
	Repetitions          []struct {
		Side              string `json:"side"`
		Index             int    `json:"index"`
		RunID             string `json:"run_id"`
		BehavioralProfile string `json:"behavioral_profile"`
	} `json:"repetitions"`
}

func beginBody(id string, runs int, reference map[string]string) map[string]any {
	body := map[string]any{
		"id": id, "scenario_name": "support", "runs": runs,
		"project_id": "proj-1", "agent_id": "agent-1", "environment": testEnvironment,
	}
	if reference != nil {
		body["reference"] = reference
	}
	return body
}

func (a *api) beginExecution(id string, runs int, reference map[string]string, want int) []byte {
	a.t.Helper()
	a.seedHierarchy()
	r := a.do("POST", "/v1/scenario-executions", beginBody(id, runs, reference))
	a.mustStatus(r, want, "begin "+id)
	return r.Body.Bytes()
}

func (a *api) completeExecution(id string, refs, cands []string, want int) []byte {
	a.t.Helper()
	body := map[string]any{"candidate_run_ids": cands, "gate_limits": repeatedLimitsBody("1", "0")}
	if refs != nil {
		body["reference_run_ids"] = refs
	}
	r := a.do("POST", "/v1/scenario-executions/"+id+"/complete", body)
	a.mustStatus(r, want, "complete "+id)
	return r.Body.Bytes()
}

func TestScenarioExecutionsOverHTTP(t *testing.T) {
	a := newAPI(t)

	// Self-contained: begin, run both sides, complete.
	var begun struct {
		Execution          executionBody  `json:"execution"`
		ReferenceExecution *executionBody `json:"reference_execution"`
	}
	if err := json.Unmarshal(a.beginExecution("e1", 1, nil, 201), &begun); err != nil {
		t.Fatal(err)
	}
	if begun.Execution.Status != "running" || begun.ReferenceExecution != nil ||
		begun.Execution.Repetitions == nil || len(begun.Execution.Repetitions) != 0 {
		t.Errorf("begun = %+v", begun)
	}
	a.completeIsolatedRun("e1-reference-1", []string{"read"})
	a.completeIsolatedRun("e1-candidate-1", []string{"read", "export"})
	var completed struct {
		Execution  executionBody `json:"execution"`
		Comparison repeatedBody  `json:"comparison"`
	}
	if err := json.Unmarshal(a.completeExecution("e1",
		[]string{"e1-reference-1"}, []string{"e1-candidate-1"}, 200), &completed); err != nil {
		t.Fatal(err)
	}
	e := completed.Execution
	if e.Status != "completed" || e.Verdict != "fail" || e.CompletionSequence != "1" ||
		len(e.Repetitions) != 2 || e.Repetitions[0].RunID != "e1-reference-1" || e.FinishedAt == "" {
		t.Errorf("completed execution = %+v", e)
	}
	if c := completed.Comparison; c.Version == "" || len(c.Gate.Checks) != 6 || c.Gate.Verdict != "fail" ||
		c.Producer.ControlPlaneVersion == "" {
		t.Errorf("comparison = %+v; want compare-repeated's shape", c)
	}

	// Recorded: last resolves e1, and the server supplies the reference runs.
	var recorded struct {
		Execution          executionBody  `json:"execution"`
		ReferenceExecution *executionBody `json:"reference_execution"`
	}
	if err := json.Unmarshal(a.beginExecution("e2", 1, map[string]string{"mode": "last"}, 201),
		&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.ReferenceExecution == nil || recorded.ReferenceExecution.ID != "e1" ||
		recorded.Execution.ReferenceExecutionID != "e1" {
		t.Fatalf("recorded begin = %+v", recorded)
	}
	a.completeIsolatedRun("e2-candidate-1", []string{"read"})
	a.completeExecution("e2", []string{"e1-reference-1"}, []string{"e2-candidate-1"}, 400)
	if err := json.Unmarshal(a.completeExecution("e2", nil, []string{"e2-candidate-1"}, 200),
		&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Comparison.Gate.Verdict != "pass" || completed.Execution.Repetitions[0].RunID != "e1-reference-1" ||
		completed.Execution.CompletionSequence != "2" {
		t.Errorf("recorded completion = %+v", completed)
	}

	// GET, and the lifecycle conflicts.
	r := a.do("GET", "/v1/scenario-executions/e2", nil)
	a.mustStatus(r, 200, "get e2")
	a.mustStatus(a.do("GET", "/v1/scenario-executions/no-such", nil), 404, "get missing")
	a.completeExecution("e2", nil, []string{"e2-candidate-1"}, 409)
	a.mustStatus(a.do("POST", "/v1/scenario-executions/e2/fail", map[string]any{}), 409, "fail completed")
	a.beginExecution("e1", 1, nil, 409)

	a.beginExecution("e3", 1, nil, 201)
	for i := 0; i < 2; i++ {
		r := a.do("POST", "/v1/scenario-executions/e3/fail", map[string]any{})
		a.mustStatus(r, 200, fmt.Sprintf("fail e3 #%d", i+1))
		var failed struct {
			Execution executionBody `json:"execution"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &failed); err != nil || failed.Execution.Status != "failed" {
			t.Errorf("failed e3 = %+v, %v", failed, err)
		}
	}
}

func TestScenarioExecutionReferenceErrorsOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.beginExecution("e1", 1, nil, 201)
	a.completeIsolatedRun("e1-reference-1", []string{"read"})
	a.completeIsolatedRun("e1-candidate-1", []string{"read"})
	a.completeExecution("e1", []string{"e1-reference-1"}, []string{"e1-candidate-1"}, 200)

	for name, tc := range map[string]struct {
		body   map[string]any
		status int
		code   string
	}{
		"missing explicit reference": {beginBody("x1", 1, map[string]string{"mode": "execution", "execution_id": "no-such"}), 404, "not_found"},
		"last with no matching history": {func() map[string]any {
			b := beginBody("x2", 1, map[string]string{"mode": "last"})
			b["scenario_name"] = "billing"
			return b
		}(), 404, "not_found"},
		"mismatched runs":           {beginBody("x3", 2, map[string]string{"mode": "last"}), 409, "conflict"},
		"unknown mode":              {beginBody("x4", 1, map[string]string{"mode": "newest"}), 400, "invalid_request"},
		"execution mode without id": {beginBody("x5", 1, map[string]string{"mode": "execution"}), 400, "invalid_request"},
		"last mode with an id":      {beginBody("x6", 1, map[string]string{"mode": "last", "execution_id": "e1"}), 400, "invalid_request"},
		"runs out of range":         {beginBody("x7", 65, nil), 400, "invalid_request"},
	} {
		t.Run(name, func(t *testing.T) {
			r := a.do("POST", "/v1/scenario-executions", tc.body)
			a.mustStatus(r, tc.status, name)
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &envelope); err != nil || envelope.Error.Code != tc.code {
				t.Errorf("code = %q, %v; want %s", envelope.Error.Code, err, tc.code)
			}
		})
	}
	// A refused begin records nothing.
	a.mustStatus(a.do("GET", "/v1/scenario-executions/x3", nil), 404, "refused begin")
}
