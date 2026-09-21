package platform

// Structurally corrupt behavior evidence, driven from inside the package.
//
// BehaviorSummary counts are int and the gates are uint64. A negative count
// reaching the conversion would not produce a small wrong number — it would
// produce 18446744073709551615, a gate that no maximum can fail and every
// minimum passes. That is the one path where corrupted evidence turns into a
// passing gate, so the guard runs before any conversion.
//
// NewEvaluationScorecard cannot produce these values. Only a bug inside this
// package could, which is why the test lives here: the external suite cannot
// reach the unexported field, and a guard that cannot be exercised is a guard
// nobody knows is broken.

import (
	"errors"
	"math"
	"testing"
)

// boundScorecardWithBehavior builds a card that passes every other check, so
// a failure is attributable to the behavior summary alone.
func boundScorecardWithBehavior(summary BehaviorSummary) EvaluationScorecard {
	return EvaluationScorecard{
		bound:              true,
		referenceRunID:     "run-ref",
		referenceCandidate: "cand-ref",
		referenceProfile:   "profile-ref",
		referenceRecords:   4,
		candidateRunID:     "run-can",
		candidateCandidate: "cand-can",
		candidateProfile:   "profile-can",
		candidateRecords:   4,
		environment:        "staging",
		behavior:           summary,
	}
}

func TestGateRejectsCorruptedBehaviorSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary BehaviorSummary
	}{
		{
			name:    "negative added count",
			summary: BehaviorSummary{AddedCount: -1, SharedCount: 1, CandidateDistinctCount: 0, ReferenceDistinctCount: 1},
		},
		{
			name:    "negative shared count",
			summary: BehaviorSummary{AddedCount: 1, SharedCount: -1, CandidateDistinctCount: 0, ReferenceDistinctCount: -1},
		},
		{
			name:    "negative removed count",
			summary: BehaviorSummary{RemovedCount: -1, SharedCount: 1, ReferenceDistinctCount: 0, CandidateDistinctCount: 1},
		},
		{
			name:    "negative candidate distinct count",
			summary: BehaviorSummary{CandidateDistinctCount: -1},
		},
		{
			name:    "negative reference distinct count",
			summary: BehaviorSummary{ReferenceDistinctCount: -1},
		},
		{
			name:    "added plus shared does not equal candidate distinct",
			summary: BehaviorSummary{AddedCount: 2, SharedCount: 1, CandidateDistinctCount: 9, RemovedCount: 0, ReferenceDistinctCount: 1},
		},
		{
			name:    "removed plus shared does not equal reference distinct",
			summary: BehaviorSummary{AddedCount: 0, SharedCount: 1, CandidateDistinctCount: 1, RemovedCount: 2, ReferenceDistinctCount: 9},
		},
	}

	policy := NewEvaluationGatePolicy(EvaluationGateLimits{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := EvaluateEvaluationGate(boundScorecardWithBehavior(tt.summary), policy)
			if !errors.Is(err, ErrInvalidGateEvidence) {
				t.Fatalf("EvaluateEvaluationGate() error = %v, want ErrInvalidGateEvidence", err)
			}
			if result != (EvaluationGateResult{}) {
				t.Errorf("result = %+v, want the zero value", result)
			}
		})
	}
}

// The specific failure the guard exists to prevent: a negative count becoming
// an enormous uint64 that satisfies every maximum.
func TestNegativeBehaviorCountNeverBecomesAPassingGate(t *testing.T) {
	corrupt := BehaviorSummary{
		AddedCount:             -1,
		SharedCount:            1,
		CandidateDistinctCount: 0,
		RemovedCount:           0,
		ReferenceDistinctCount: 1,
	}

	// What the conversion would produce without the guard.
	if got := uint64(corrupt.AddedCount); got != math.MaxUint64 {
		t.Fatalf("precondition: uint64(-1) = %d, want MaxUint64", got)
	}

	permissive := NewEvaluationGatePolicy(EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
	result, err := EvaluateEvaluationGate(boundScorecardWithBehavior(corrupt), permissive)
	if err == nil {
		t.Fatalf("corrupted evidence produced verdict %q with no error", result.Verdict())
	}
	if result.Verdict() == GateVerdictPass {
		t.Error("corrupted evidence produced a passing verdict")
	}
}

// A consistent summary passes the guard, so the checks above are not simply
// rejecting everything.
func TestConsistentBehaviorSummaryPassesTheGuard(t *testing.T) {
	consistent := BehaviorSummary{
		ReferenceDistinctCount: 3,
		CandidateDistinctCount: 4,
		AddedCount:             2,
		RemovedCount:           1,
		SharedCount:            2,
	}
	if err := validateBehaviorSummaryArithmetic(consistent); err != nil {
		t.Fatalf("validateBehaviorSummaryArithmetic() error = %v, want nil", err)
	}

	result, err := EvaluateEvaluationGate(
		boundScorecardWithBehavior(consistent),
		NewEvaluationGatePolicy(EvaluationGateLimits{MaxAddedBehaviors: 2}),
	)
	if err != nil {
		t.Fatalf("EvaluateEvaluationGate() error = %v", err)
	}
	if got := result.AddedBehaviors().Actual; got != 2 {
		t.Errorf("AddedBehaviors().Actual = %d, want 2", got)
	}
	if result.Verdict() != GateVerdictPass {
		t.Errorf("Verdict() = %q, want pass", result.Verdict())
	}
}

// An all-zero summary is consistent, and is what a valid empty evaluation
// produces. It must not be mistaken for corruption.
func TestEmptyBehaviorSummaryIsConsistent(t *testing.T) {
	if err := validateBehaviorSummaryArithmetic(BehaviorSummary{}); err != nil {
		t.Fatalf("an empty summary was rejected as corrupt: %v", err)
	}
}
