package platform

import (
	"errors"
	"testing"
)

// Evidence the fold could not have produced is refused, not gated, and only
// when the limit is used — so a caller who omits it gets no new error path.
func TestChangeCountEvidenceIsValidatedBeforeItIsGated(t *testing.T) {
	valid := BehaviorSummary{
		ReferenceDistinctCount: 1, CandidateDistinctCount: 3,
		AddedCount: 2, SharedCount: 1,
		AddedChangeCount: 1, CorrelationState: CorrelationComplete,
		CountingPolicyVersion: CountingPolicyVersion,
	}
	if err := validateChangeCountEvidence(valid); err != nil {
		t.Fatalf("a folded count was refused: %v", err)
	}

	for name, mutate := range map[string]func(*BehaviorSummary){
		"negative":                 func(s *BehaviorSummary) { s.AddedChangeCount = -1 },
		"more changes than added":  func(s *BehaviorSummary) { s.AddedChangeCount = 3 },
		"zero over positive added": func(s *BehaviorSummary) { s.AddedChangeCount = 0 },
		"partial yet folded":       func(s *BehaviorSummary) { s.CorrelationState = CorrelationPartial },
		"unavailable yet folded":   func(s *BehaviorSummary) { s.CorrelationState = CorrelationUnavailable },
		"unknown correlation":      func(s *BehaviorSummary) { s.CorrelationState = "mostly" },
		"empty correlation":        func(s *BehaviorSummary) { s.CorrelationState = "" },
		"no counting policy":       func(s *BehaviorSummary) { s.CountingPolicyVersion = "" },
	} {
		t.Run(name, func(t *testing.T) {
			s := valid
			mutate(&s)
			if err := validateChangeCountEvidence(s); !errors.Is(err, ErrInvalidGateEvidence) {
				t.Errorf("error = %v, want ErrInvalidGateEvidence", err)
			}
		})
	}
}

// A stored check's shape is validated by state; its outcome never is.
func TestStoredChangeGateShapeByState(t *testing.T) {
	good := []ChangeCountGate{
		{State: GateCheckNotEvaluated},
		{State: GateCheckNotRecorded},
		{State: GateCheckEvaluated, Actual: 9, Maximum: 1, Passed: true,
			CorrelationState: CorrelationComplete, CountingPolicyVersion: "1"},
	}
	for _, g := range good {
		if err := validateStoredChangeGate(g); err != nil {
			t.Errorf("%+v refused: %v", g, err)
		}
	}
	bad := []ChangeCountGate{
		{},
		{State: "skipped"},
		{State: GateCheckNotEvaluated, Passed: true},
		{State: GateCheckNotRecorded, Maximum: 1},
		{State: GateCheckEvaluated, CorrelationState: CorrelationComplete},
		{State: GateCheckEvaluated, CountingPolicyVersion: "1"},
	}
	for _, g := range bad {
		if err := validateStoredChangeGate(g); !errors.Is(err, ErrInvalidGateEvidence) {
			t.Errorf("%+v: error = %v, want ErrInvalidGateEvidence", g, err)
		}
	}
}
