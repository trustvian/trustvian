package httpapi_test

// Task 107, D12: POST /v1/evaluations/compare carries task 086's sameness and
// warnings, each run looked up through the executions that recorded it, as
// compare-repeated does. Additive, and read by no check.

import (
	"testing"
)

func (a *api) compareSingle(reference, candidate string) []byte {
	a.t.Helper()
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": reference, "candidate_run_id": candidate,
		"gate_limits": limitsBody("0", "0", "0"),
	})
	a.mustStatus(r, 200, "compare "+reference+" "+candidate)
	return r.Body.Bytes()
}

// TestSingleRunCompareStatesARecordedPairsSameness: two runs an execution
// recorded, with different declared models, compare with their recorded
// values, same_model false and its warning — and the same gate as an
// unrecorded pair with the same behaviors.
func TestSingleRunCompareStatesARecordedPairsSameness(t *testing.T) {
	a := newAPI(t)
	a.beginWithProvenance("e1", 1, nil, "llama3.2", "gemma3:4b")
	a.completeIsolatedRun("e1-reference-1", []string{"read"})
	a.completeIsolatedRun("e1-candidate-1", []string{"read", "send"})
	a.completeExecution("e1", []string{"e1-reference-1"}, []string{"e1-candidate-1"}, 200)

	recorded := a.compareSingle("e1-reference-1", "e1-candidate-1")
	if s := member(t, recorded, "sameness"); s != `{"same_scenario":"true","same_inputs":"true",`+
		`"same_model":"false","same_prompt_ref":"not_recorded",`+
		`"reference":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared","model":"llama3.2"},`+
		`"candidate":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared","model":"gemma3:4b"}}` {
		t.Errorf("sameness = %s", s)
	}
	if w := member(t, recorded, "warnings"); w != `[{"code":"model_differs",`+
		`"text":"The reference and candidate sides declared different models."}]` {
		t.Errorf("warnings = %s", w)
	}

	a.completeIsolatedRun("plain-reference", []string{"read"})
	a.completeIsolatedRun("plain-candidate", []string{"read", "send"})
	unrecorded := a.compareSingle("plain-reference", "plain-candidate")
	if member(t, recorded, "gate") != member(t, unrecorded, "gate") {
		t.Errorf("sameness changed the gate:\n%s\n%s", member(t, recorded, "gate"), member(t, unrecorded, "gate"))
	}
}

// TestSingleRunCompareOfUnrecordedRunsReadsNotRecorded: runs no execution
// recorded state nothing, and warn of nothing.
func TestSingleRunCompareOfUnrecordedRunsReadsNotRecorded(t *testing.T) {
	a := newAPI(t)
	a.completeIsolatedRun("ref-1", []string{"read"})
	a.completeIsolatedRun("can-1", []string{"read"})
	got := a.compareSingle("ref-1", "can-1")
	if s := member(t, got, "sameness"); s != samenessNotRecorded {
		t.Errorf("sameness = %s, want %s", s, samenessNotRecorded)
	}
	if w := member(t, got, "warnings"); w != `[]` {
		t.Errorf("warnings = %s, want []", w)
	}
}
