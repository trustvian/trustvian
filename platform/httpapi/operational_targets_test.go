package httpapi_test

// GET /v1/evaluations/operational over /v1 (task 087).

import (
	"encoding/json"
	"net/url"
	"testing"

	"github.com/trustvian/trustvian/event"
)

type operationalTargetsBody struct {
	Version         string   `json:"version"`
	ReferenceRunIDs []string `json:"reference_run_ids"`
	CandidateRunIDs []string `json:"candidate_run_ids"`
	Targets         []struct {
		TargetName            string `json:"target_name"`
		TargetCategory        string `json:"target_category"`
		ReferenceObservations string `json:"reference_observations"`
		CandidateObservations string `json:"candidate_observations"`
		Errors                struct {
			Comparable bool   `json:"comparable"`
			Reason     string `json:"reason"`
			Candidate  struct {
				RunsWithEvidence string `json:"runs_with_evidence"`
				HTTP429          string `json:"http_429"`
			} `json:"candidate"`
			Delta *struct {
				HTTP429 string `json:"http_429"`
			} `json:"delta"`
		} `json:"errors"`
	} `json:"targets"`
	NextAfter string `json:"next_after"`
}

func (a *api) operationalTargets(query url.Values, want int) operationalTargetsBody {
	a.t.Helper()
	r := a.do("GET", "/v1/evaluations/operational?"+query.Encode(), nil)
	a.mustStatus(r, want, "operational targets")
	var body operationalTargetsBody
	if want == 200 {
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			a.t.Fatal(err)
		}
	}
	return body
}

func ok200(code string) map[string]string { return map[string]string{"http_status_code": code} }

// TestOperationalTargetsArePerTargetAndPaged: one row per target, in target
// order, paged with the collections' cursor, and a target the candidate began
// rate-limiting shows its 429s with a delta.
func TestOperationalTargetsArePerTargetAndPaged(t *testing.T) {
	a := newAPI(t)
	a.completeOperationalRun("run-ref", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: ok200("200"), target: "api.model.example"},
		{op: "search", duration: "1000", status: event.StatusOK, fields: ok200("200"), target: "kb.internal"},
	})
	a.completeOperationalRun("run-can", "cand-2", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusError, fields: ok200("429"), target: "api.model.example"},
		{op: "chat", duration: "1000", status: event.StatusError, fields: ok200("429"), target: "api.model.example"},
		{op: "search", duration: "1000", status: event.StatusOK, fields: ok200("200"), target: "kb.internal"},
	})
	query := url.Values{"reference_run_id": {"run-ref"}, "candidate_run_id": {"run-can"}, "limit": {"1"}}

	first := a.operationalTargets(query, 200)
	if len(first.Targets) != 1 || first.Targets[0].TargetName != "api.model.example" ||
		first.NextAfter != "external/api.model.example" {
		t.Fatalf("first page = %+v", first)
	}
	row := first.Targets[0]
	if row.ReferenceObservations != "1" || row.CandidateObservations != "2" ||
		!row.Errors.Comparable || row.Errors.Candidate.HTTP429 != "2" ||
		row.Errors.Delta == nil || row.Errors.Delta.HTTP429 != "2" {
		t.Fatalf("model target row = %+v", row)
	}

	query.Set("after", first.NextAfter)
	second := a.operationalTargets(query, 200)
	if len(second.Targets) != 1 || second.Targets[0].TargetName != "kb.internal" || second.NextAfter != "" {
		t.Fatalf("second page = %+v", second)
	}
}

// TestOperationalTargetsSumRepeatedRuns: N runs a side are summed, and
// runs_with_evidence counts the runs a target's evidence came from.
func TestOperationalTargetsSumRepeatedRuns(t *testing.T) {
	a := newAPI(t)
	a.completeOperationalRun("ref-1", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: ok200("200")}})
	a.completeOperationalRun("ref-2", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: ok200("200")}})
	a.completeOperationalRun("can-1", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusOK, fields: ok200("429")}})
	a.completeOperationalRun("can-2", "cand-1", []operationalStep{
		{op: "chat", duration: "1000", status: event.StatusUnavailable}})
	body := a.operationalTargets(url.Values{
		"reference_run_id": {"ref-1", "ref-2"}, "candidate_run_id": {"can-1", "can-2"},
	}, 200)
	if len(body.Targets) != 1 {
		t.Fatalf("targets = %+v", body.Targets)
	}
	row := body.Targets[0]
	if row.ReferenceObservations != "2" || row.CandidateObservations != "2" ||
		row.Errors.Candidate.RunsWithEvidence != "1" || row.Errors.Candidate.HTTP429 != "1" {
		t.Fatalf("summed row = %+v", row)
	}
}

func TestOperationalTargetsRefusals(t *testing.T) {
	a := newAPI(t)
	a.completeOperationalRun("run-ref", "cand-1", nil)
	a.completeOperationalRun("run-can", "cand-2", nil)
	a.seedRunning("run-live")
	tests := []struct {
		name  string
		query url.Values
		want  int
	}{
		{"no runs", url.Values{}, 400},
		{"unequal sides", url.Values{"reference_run_id": {"run-ref"}}, 400},
		{"a run named twice on one side", url.Values{"reference_run_id": {"run-ref", "run-ref"},
			"candidate_run_id": {"run-can", "run-can"}}, 400},
		{"a cursor this server did not issue", url.Values{"reference_run_id": {"run-ref"},
			"candidate_run_id": {"run-can"}, "after": {"no-slash"}}, 400},
		{"a cursor with an unknown category", url.Values{"reference_run_id": {"run-ref"},
			"candidate_run_id": {"run-can"}, "after": {"cloud/x"}}, 400},
		{"limit out of range", url.Values{"reference_run_id": {"run-ref"},
			"candidate_run_id": {"run-can"}, "limit": {"65"}}, 400},
		{"an unknown run", url.Values{"reference_run_id": {"run-ref"}, "candidate_run_id": {"nope"}}, 404},
		{"a run still running", url.Values{"reference_run_id": {"run-ref"}, "candidate_run_id": {"run-live"}}, 409},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a.operationalTargets(tt.query, tt.want)
		})
	}
	// Two empty runs: no target, and no next page.
	if body := a.operationalTargets(url.Values{"reference_run_id": {"run-ref"},
		"candidate_run_id": {"run-can"}}, 200); len(body.Targets) != 0 || body.NextAfter != "" {
		t.Fatalf("empty runs = %+v", body)
	}
}
