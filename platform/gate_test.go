package platform_test

// Task 056: deterministic hard gates.
//
// Three classes of assertion dominate this file.
//
// The fail-closed boundaries: an unbound policy and an unbound scorecard are
// errors, while a *valid* evaluation that observed nothing is not — it is
// evidence that fails the sufficiency gate. Collapsing those two outcomes is
// the failure this layer exists to prevent.
//
// The composition rule: all five checks are evaluated every time, PASS needs
// all five, and no value anywhere can compensate for one that failed. The
// absence of a compensating path is the invariant, so it is tested by
// construction rather than by inspecting a weighting model that never exists.
//
// The absences: no float gate, no rule DSL, no overall score, and no field
// named for a severity or sensitivity concept the evidence cannot express.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// strictPolicy accepts nothing: three maximums of exactly zero.
func strictPolicy() platform.EvaluationGatePolicy {
	return platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{})
}

// permissivePolicy accepts anything expressible.
func permissivePolicy() platform.EvaluationGatePolicy {
	return platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
}

// gateEvidence builds one reference/candidate pair sharing a behavior, so the
// added-behavior count is controllable independently of record counts.
func gateEvidence(t *testing.T) (reference, candidate *evidence) {
	t.Helper()
	reference = newEvidence(t, "run-ref", "cand-ref", "staging", "profile-ref")
	candidate = newEvidence(t, "run-can", "cand-can", "staging", "profile-can")
	return reference, candidate
}

func gateOf(t *testing.T, card platform.EvaluationScorecard, policy platform.EvaluationGatePolicy,
) platform.EvaluationGateResult {
	t.Helper()
	result, err := platform.EvaluateEvaluationGate(card, policy)
	if err != nil {
		t.Fatalf("EvaluateEvaluationGate() error = %v", err)
	}
	return result
}

// ---------------------------------------------------------------------
// Policy validity
// ---------------------------------------------------------------------

func TestZeroPolicyIsRejected(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("e1", "fp-1", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("e2", "fp-1", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	card := scorecardOf(t, reference, candidate)

	_, err := platform.EvaluateEvaluationGate(card, platform.EvaluationGatePolicy{})
	if !errors.Is(err, platform.ErrInvalidGatePolicy) {
		t.Fatalf("EvaluateEvaluationGate(zero policy) error = %v, want ErrInvalidGatePolicy", err)
	}
}

// The distinction the bound marker exists for: an absent policy and the
// strictest expressible policy have the same limit values and opposite
// meanings.
func TestConstructorProducedAllZeroPolicyIsValidAndStrict(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("e1", "fp-1", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("e2", "fp-1", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	card := scorecardOf(t, reference, candidate)

	policy := strictPolicy()
	limits := policy.Limits()
	if limits.MaxAddedBehaviors != 0 || limits.MaxBlockDecisions != 0 ||
		limits.MaxCriticalRiskObservations != 0 {
		t.Fatalf("Limits() = %+v, want three zeros", limits)
	}

	// Valid, and strict rather than disabled: identical behavior passes.
	result := gateOf(t, card, policy)
	if result.Verdict() != platform.GateVerdictPass {
		t.Fatalf("Verdict() = %q, want pass (no added behavior, no block, no critical risk)", result.Verdict())
	}

	// One added behavior now fails, proving 0 is enforced rather than ignored.
	candidate.feed(t, scorecardRecord("e3", "fp-2", "write", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	strictCard := scorecardOf(t, reference, candidate)
	strictResult := gateOf(t, strictCard, policy)
	if strictResult.AddedBehaviors().Passed {
		t.Error("AddedBehaviors().Passed = true with maximum 0 and one added behavior; 0 was treated as disabled")
	}
	if strictResult.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", strictResult.Verdict())
	}
}

func TestMaxUint64LimitsAreValid(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("e1", "fp-1", "read", "staging", "block", "critical", event.ApprovalDenied, 0.1))
	candidate.feed(t, scorecardRecord("e2", "fp-2", "write", "staging", "block", "critical", event.ApprovalDenied, 0.1))
	card := scorecardOf(t, reference, candidate)

	result := gateOf(t, card, permissivePolicy())
	if result.Verdict() != platform.GateVerdictPass {
		t.Fatalf("Verdict() = %q, want pass under MaxUint64 limits", result.Verdict())
	}
	for _, g := range []struct {
		name string
		gate platform.MaximumCountGate
	}{
		{"AddedBehaviors", result.AddedBehaviors()},
		{"BlockDecisions", result.BlockDecisions()},
		{"CriticalRiskObservations", result.CriticalRiskObservations()},
	} {
		if g.gate.Maximum != math.MaxUint64 {
			t.Errorf("%s().Maximum = %d, want MaxUint64", g.name, g.gate.Maximum)
		}
		if !g.gate.Passed {
			t.Errorf("%s().Passed = false under MaxUint64", g.name)
		}
	}
}

// ---------------------------------------------------------------------
// Scorecard validity
// ---------------------------------------------------------------------

func TestZeroScorecardIsRejected(t *testing.T) {
	_, err := platform.EvaluateEvaluationGate(platform.EvaluationScorecard{}, strictPolicy())
	if !errors.Is(err, platform.ErrInvalidGateEvidence) {
		t.Fatalf("EvaluateEvaluationGate(zero scorecard) error = %v, want ErrInvalidGateEvidence", err)
	}
}

// Policy is checked before evidence, so a caller holding two invalid inputs
// learns about the one they control.
func TestZeroPolicyAndZeroScorecardReportsPolicy(t *testing.T) {
	_, err := platform.EvaluateEvaluationGate(platform.EvaluationScorecard{}, platform.EvaluationGatePolicy{})
	if !errors.Is(err, platform.ErrInvalidGatePolicy) {
		t.Fatalf("error = %v, want ErrInvalidGatePolicy", err)
	}
}

func TestErrorResultIsZeroValued(t *testing.T) {
	result, err := platform.EvaluateEvaluationGate(platform.EvaluationScorecard{}, strictPolicy())
	if err == nil {
		t.Fatal("expected an error")
	}
	if result != (platform.EvaluationGateResult{}) {
		t.Errorf("result on error = %+v, want the zero value", result)
	}
	if result.Verdict() == platform.GateVerdictPass {
		t.Error("a result returned alongside an error reports pass")
	}
}

// ---------------------------------------------------------------------
// Evidence sufficiency
// ---------------------------------------------------------------------

// The central fail-open case: a candidate that ran nothing satisfies every
// maximum. Only the sufficiency gates stand between that and a PASS.
func TestEvidenceSufficiency(t *testing.T) {
	tests := []struct {
		name                         string
		referenceRecords             int
		candidateRecords             int
		wantReference, wantCandidate bool
		wantVerdict                  platform.GateVerdict
	}{
		{"both non-empty", 1, 1, true, true, platform.GateVerdictPass},
		{"empty reference", 0, 1, false, true, platform.GateVerdictFail},
		{"empty candidate", 1, 0, true, false, platform.GateVerdictFail},
		{"both empty", 0, 0, false, false, platform.GateVerdictFail},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, candidate := gateEvidence(t)
			for i := range tt.referenceRecords {
				reference.feed(t, scorecardRecord("r"+string(rune('a'+i)), "fp-1", "read", "staging",
					"allow", "low", event.ApprovalNotRequired, 0.9))
			}
			for i := range tt.candidateRecords {
				candidate.feed(t, scorecardRecord("c"+string(rune('a'+i)), "fp-1", "read", "staging",
					"allow", "low", event.ApprovalNotRequired, 0.9))
			}
			card := scorecardOf(t, reference, candidate)

			// A valid empty evaluation is not an input error.
			result, err := platform.EvaluateEvaluationGate(card, permissivePolicy())
			if err != nil {
				t.Fatalf("EvaluateEvaluationGate() error = %v, want nil (empty evidence is valid)", err)
			}

			if got := result.ReferenceEvidence().Passed; got != tt.wantReference {
				t.Errorf("ReferenceEvidence().Passed = %v, want %v", got, tt.wantReference)
			}
			if got := result.CandidateEvidence().Passed; got != tt.wantCandidate {
				t.Errorf("CandidateEvidence().Passed = %v, want %v", got, tt.wantCandidate)
			}
			if result.Verdict() != tt.wantVerdict {
				t.Errorf("Verdict() = %q, want %q", result.Verdict(), tt.wantVerdict)
			}
			for _, g := range []struct {
				name string
				min  uint64
			}{{"ReferenceEvidence", result.ReferenceEvidence().Minimum}, {"CandidateEvidence", result.CandidateEvidence().Minimum}} {
				if g.min != 1 {
					t.Errorf("%s().Minimum = %d, want 1", g.name, g.min)
				}
			}

			// Even when both evidence gates fail, the maximum gates are
			// still reported — a complete, auditable result.
			if result.AddedBehaviors().Maximum != math.MaxUint64 {
				t.Error("AddedBehaviors() was not populated")
			}
		})
	}
}

// An empty candidate under the strictest policy is the exact fail-open shape:
// every maximum is satisfied and nothing ran.
func TestEmptyCandidateDoesNotPassByVacuousMaximums(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("e1", "fp-1", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	card := scorecardOf(t, reference, candidate)

	result := gateOf(t, card, strictPolicy())

	if !result.AddedBehaviors().Passed || !result.BlockDecisions().Passed ||
		!result.CriticalRiskObservations().Passed {
		t.Fatal("precondition: the three maximum gates should all be satisfied by an empty candidate")
	}
	if result.Verdict() != platform.GateVerdictFail {
		t.Fatalf("Verdict() = %q, want fail: a candidate that ran nothing satisfied every maximum", result.Verdict())
	}
}

// ---------------------------------------------------------------------
// Maximum gates
// ---------------------------------------------------------------------

func TestAddedBehaviorGateBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		added      int
		maximum    uint64
		wantPassed bool
	}{
		{"below maximum", 1, 2, true},
		{"at maximum", 2, 2, true},
		{"above maximum", 3, 2, false},
		{"zero maximum, none added", 0, 0, true},
		{"zero maximum, one added", 1, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, candidate := gateEvidence(t)
			shared := scorecardRecord("shared", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9)
			reference.feed(t, shared)
			candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			for i := range tt.added {
				candidate.feed(t, scorecardRecord(
					"extra"+string(rune('a'+i)), "fp-x"+string(rune('a'+i)),
					"op"+string(rune('a'+i)), "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			}
			card := scorecardOf(t, reference, candidate)

			result := gateOf(t, card, platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
				MaxAddedBehaviors:           tt.maximum,
				MaxBlockDecisions:           math.MaxUint64,
				MaxCriticalRiskObservations: math.MaxUint64,
			}))

			gate := result.AddedBehaviors()
			if gate.Actual != uint64(tt.added) {
				t.Errorf("AddedBehaviors().Actual = %d, want %d", gate.Actual, tt.added)
			}
			if gate.Maximum != tt.maximum {
				t.Errorf("AddedBehaviors().Maximum = %d, want %d", gate.Maximum, tt.maximum)
			}
			if gate.Passed != tt.wantPassed {
				t.Errorf("AddedBehaviors().Passed = %v, want %v", gate.Passed, tt.wantPassed)
			}
		})
	}
}

func TestBlockDecisionGateBoundaries(t *testing.T) {
	tests := []struct {
		name            string
		candidateBlocks int
		maximum         uint64
		wantPassed      bool
	}{
		{"below maximum", 1, 2, true},
		{"at maximum", 2, 2, true},
		{"above maximum", 3, 2, false},
		{"zero maximum, none blocked", 0, 0, true},
		{"zero maximum, one blocked", 1, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, candidate := gateEvidence(t)
			reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			for i := range tt.candidateBlocks {
				candidate.feed(t, scorecardRecord("b"+string(rune('a'+i)), "fp-0", "read", "staging",
					"block", "low", event.ApprovalNotRequired, 0.9))
			}
			card := scorecardOf(t, reference, candidate)

			result := gateOf(t, card, platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
				MaxAddedBehaviors:           math.MaxUint64,
				MaxBlockDecisions:           tt.maximum,
				MaxCriticalRiskObservations: math.MaxUint64,
			}))

			gate := result.BlockDecisions()
			if gate.Actual != uint64(tt.candidateBlocks) {
				t.Errorf("BlockDecisions().Actual = %d, want %d", gate.Actual, tt.candidateBlocks)
			}
			if gate.Passed != tt.wantPassed {
				t.Errorf("BlockDecisions().Passed = %v, want %v", gate.Passed, tt.wantPassed)
			}
		})
	}
}

// Pins the source: the candidate block *count*, not a rate, not the reference
// side, not critical risk. Every alternative source is given a different
// value so a mutation cannot coincide with the right answer.
func TestBlockDecisionGateReadsCandidateCountOnly(t *testing.T) {
	reference, candidate := gateEvidence(t)

	// Reference: 3 blocks out of 4 records — a high block rate.
	for i := range 3 {
		reference.feed(t, scorecardRecord("rb"+string(rune('a'+i)), "fp-0", "read", "staging",
			"block", "low", event.ApprovalNotRequired, 0.9))
	}
	reference.feed(t, scorecardRecord("ra", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))

	// Candidate: 1 block out of 5 records, and 2 critical-risk readings.
	candidate.feed(t, scorecardRecord("cb", "fp-0", "read", "staging", "block", "low", event.ApprovalNotRequired, 0.9))
	for i := range 2 {
		candidate.feed(t, scorecardRecord("cc"+string(rune('a'+i)), "fp-0", "read", "staging",
			"allow", "critical", event.ApprovalNotRequired, 0.9))
	}
	for i := range 2 {
		candidate.feed(t, scorecardRecord("ca"+string(rune('a'+i)), "fp-0", "read", "staging",
			"allow", "low", event.ApprovalNotRequired, 0.9))
	}

	card := scorecardOf(t, reference, candidate)
	result := gateOf(t, card, permissivePolicy())

	if got := result.BlockDecisions().Actual; got != 1 {
		t.Errorf("BlockDecisions().Actual = %d, want 1 (candidate block count); "+
			"reference blocks = 3, candidate critical risks = 2", got)
	}
	if got := result.CriticalRiskObservations().Actual; got != 2 {
		t.Errorf("CriticalRiskObservations().Actual = %d, want 2 (candidate critical count); "+
			"candidate blocks = 1", got)
	}
}

func TestCriticalRiskGateBoundaries(t *testing.T) {
	tests := []struct {
		name       string
		critical   int
		maximum    uint64
		wantPassed bool
	}{
		{"below maximum", 1, 2, true},
		{"at maximum", 2, 2, true},
		{"above maximum", 3, 2, false},
		{"zero maximum, none critical", 0, 0, true},
		{"zero maximum, one critical", 1, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, candidate := gateEvidence(t)
			reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			for i := range tt.critical {
				candidate.feed(t, scorecardRecord("k"+string(rune('a'+i)), "fp-0", "read", "staging",
					"allow", "critical", event.ApprovalNotRequired, 0.9))
			}
			card := scorecardOf(t, reference, candidate)

			result := gateOf(t, card, platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
				MaxAddedBehaviors:           math.MaxUint64,
				MaxBlockDecisions:           math.MaxUint64,
				MaxCriticalRiskObservations: tt.maximum,
			}))

			gate := result.CriticalRiskObservations()
			if gate.Actual != uint64(tt.critical) {
				t.Errorf("CriticalRiskObservations().Actual = %d, want %d", gate.Actual, tt.critical)
			}
			if gate.Passed != tt.wantPassed {
				t.Errorf("CriticalRiskObservations().Passed = %v, want %v", gate.Passed, tt.wantPassed)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Verdict composition
// ---------------------------------------------------------------------

func TestVerdictComposition(t *testing.T) {
	tests := []struct {
		name        string
		limits      platform.EvaluationGateLimits
		wantVerdict platform.GateVerdict
		wantFailed  int
	}{
		{
			name:        "all five pass",
			limits:      platform.EvaluationGateLimits{MaxAddedBehaviors: 1, MaxBlockDecisions: 1, MaxCriticalRiskObservations: 1},
			wantVerdict: platform.GateVerdictPass,
		},
		{
			name:        "added behaviors alone fails",
			limits:      platform.EvaluationGateLimits{MaxAddedBehaviors: 0, MaxBlockDecisions: 1, MaxCriticalRiskObservations: 1},
			wantVerdict: platform.GateVerdictFail,
			wantFailed:  1,
		},
		{
			name:        "block decisions alone fails",
			limits:      platform.EvaluationGateLimits{MaxAddedBehaviors: 1, MaxBlockDecisions: 0, MaxCriticalRiskObservations: 1},
			wantVerdict: platform.GateVerdictFail,
			wantFailed:  1,
		},
		{
			name:        "critical risk alone fails",
			limits:      platform.EvaluationGateLimits{MaxAddedBehaviors: 1, MaxBlockDecisions: 1, MaxCriticalRiskObservations: 0},
			wantVerdict: platform.GateVerdictFail,
			wantFailed:  1,
		},
		{
			name:        "all three maximums fail",
			limits:      platform.EvaluationGateLimits{},
			wantVerdict: platform.GateVerdictFail,
			wantFailed:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reference, candidate := gateEvidence(t)
			reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
			// One added behavior, one block, one critical risk.
			candidate.feed(t, scorecardRecord("c1", "fp-1", "write", "staging", "block", "critical", event.ApprovalNotRequired, 0.9))

			card := scorecardOf(t, reference, candidate)
			result := gateOf(t, card, platform.NewEvaluationGatePolicy(tt.limits))

			if result.Verdict() != tt.wantVerdict {
				t.Errorf("Verdict() = %q, want %q", result.Verdict(), tt.wantVerdict)
			}

			// No short-circuit: every check is populated whatever happened.
			failed := 0
			for _, g := range []platform.MaximumCountGate{
				result.AddedBehaviors(), result.BlockDecisions(), result.CriticalRiskObservations(),
			} {
				if !g.Passed {
					failed++
				}
				if g.Actual != 1 {
					t.Errorf("a maximum gate reported Actual = %d, want 1; checks were not all evaluated", g.Actual)
				}
			}
			if failed != tt.wantFailed {
				t.Errorf("failed maximum gates = %d, want %d", failed, tt.wantFailed)
			}
			if !result.ReferenceEvidence().Passed || !result.CandidateEvidence().Passed {
				t.Error("evidence gates should pass; both sides observed records")
			}
		})
	}
}

// ---------------------------------------------------------------------
// Averages cannot compensate
// ---------------------------------------------------------------------

// Excellent numeric evidence in every dimension, one failed count gate. If any
// compensating path existed, this is where it would show.
func TestFavourableAveragesCannotOverrideAFailedCountGate(t *testing.T) {
	reference, candidate := gateEvidence(t)

	perfect := func(eventID, fingerprintID, operation, decision, risk string) trustvian.DecisionRecord {
		rec := scorecardRecord(eventID, fingerprintID, operation, "staging", decision, risk,
			event.ApprovalApproved, 1.0)
		rec.IdentityConfidence = 1.0
		rec.TrustScore = 1.0
		rec.AnomalyScore = 0.0
		rec.AnomalyConfidence = 1.0
		rec.ContextRisk = 0.0
		return rec
	}

	reference.feed(t, perfect("r0", "fp-0", "read", "allow", "low"))
	candidate.feed(t, perfect("c0", "fp-0", "read", "allow", "low"))
	// Identical behavior: PresenceOverlap is 1.0, nothing added or removed.
	candidate.feed(t, perfect("c1", "fp-0", "read", "block", "low"))

	card := scorecardOf(t, reference, candidate)

	// Confirm the compensating evidence really is maximally favourable.
	if mean, ok := card.Metrics().TrustScore.Candidate().Mean(); !ok || mean != 1.0 {
		t.Fatalf("precondition: candidate TrustScore mean = %v (ok=%v), want 1.0", mean, ok)
	}
	if overlap, ok := card.Behavior().PresenceOverlap(); !ok || overlap != 1.0 {
		t.Fatalf("precondition: PresenceOverlap = %v (ok=%v), want 1.0", overlap, ok)
	}

	strict := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           0,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
	failed := gateOf(t, card, strict)
	if failed.Verdict() != platform.GateVerdictFail {
		t.Fatalf("Verdict() = %q, want fail: a perfect trust score overrode a failed block gate",
			failed.Verdict())
	}

	// Change only the threshold; every float is identical.
	relaxed := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           1,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
	passed := gateOf(t, card, relaxed)
	if passed.Verdict() != platform.GateVerdictPass {
		t.Fatalf("Verdict() = %q, want pass after raising only the block limit", passed.Verdict())
	}
}

// The same invariant for the critical-risk gate.
func TestFavourableAveragesCannotOverrideCriticalRiskGate(t *testing.T) {
	reference, candidate := gateEvidence(t)
	rec := func(eventID, decision, risk string) trustvian.DecisionRecord {
		r := scorecardRecord(eventID, "fp-0", "read", "staging", decision, risk, event.ApprovalApproved, 1.0)
		r.IdentityConfidence = 1.0
		r.TrustScore = 1.0
		r.ContextRisk = 0.0
		return r
	}
	reference.feed(t, rec("r0", "allow", "low"))
	candidate.feed(t, rec("c0", "allow", "low"))
	candidate.feed(t, rec("c1", "allow", "critical"))

	card := scorecardOf(t, reference, candidate)

	strict := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: 0,
	})
	if got := gateOf(t, card, strict).Verdict(); got != platform.GateVerdictFail {
		t.Fatalf("Verdict() = %q, want fail", got)
	}

	relaxed := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: 1,
	})
	if got := gateOf(t, card, relaxed).Verdict(); got != platform.GateVerdictPass {
		t.Fatalf("Verdict() = %q, want pass after raising only the critical-risk limit", got)
	}
}

// ---------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------

func TestGateResultCapturesComparisonIdentity(t *testing.T) {
	reference := newEvidence(t, "run-ref", "cand-ref", "staging", "profile-ref")
	candidate := newEvidence(t, "run-can", "cand-can", "staging", "profile-can")
	reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))

	result := gateOf(t, scorecardOf(t, reference, candidate), permissivePolicy())

	// Two different candidates under two different profiles is the normal
	// case; task 051 kept learning scope out of behavioral identity for it.
	if got := result.ReferenceRunID(); got != "run-ref" {
		t.Errorf("ReferenceRunID() = %q, want run-ref", got)
	}
	if got := result.ReferenceCandidateID(); got != "cand-ref" {
		t.Errorf("ReferenceCandidateID() = %q, want cand-ref", got)
	}
	if got := result.CandidateRunID(); got != "run-can" {
		t.Errorf("CandidateRunID() = %q, want run-can", got)
	}
	if got := result.CandidateCandidateID(); got != "cand-can" {
		t.Errorf("CandidateCandidateID() = %q, want cand-can", got)
	}
	if got := result.Environment(); got != "staging" {
		t.Errorf("Environment() = %q, want staging", got)
	}
}

// ---------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------

func TestGateEvaluationIsDeterministic(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("c0", "fp-1", "write", "staging", "block", "critical", event.ApprovalDenied, 0.2))
	card := scorecardOf(t, reference, candidate)
	policy := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{MaxAddedBehaviors: 1})

	first := gateOf(t, card, policy)
	for range 32 {
		if got := gateOf(t, card, policy); got != first {
			t.Fatalf("repeated evaluation differed:\n first = %+v\n got   = %+v", first, got)
		}
	}
}

// ---------------------------------------------------------------------
// Structural absences
// ---------------------------------------------------------------------

// Unsupported severity and sensitivity semantics must stay absent rather than
// appear as a field reporting zero — a measurement that never ran reads
// exactly like one that found nothing.
func TestGateExposesNoUnsupportedSemantics(t *testing.T) {
	forbidden := []string{
		"criticalpolicyviolation", "blockedsensitive", "unapprovedsensitive",
		"sensitiveaction", "sensitivethreshold", "approvalcompliance",
		"policycompliance", "delegationcompliance",
		"mintrustscore", "maxanomalyscore", "minpresenceoverlap",
		"maxblockrate", "maxcriticalriskrate",
		"overallscore", "weightedscore", "grade", "recommendation",
		"promotable", "promote", "deploy", "release", "approve",
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(platform.EvaluationGatePolicy{}),
		reflect.TypeOf(platform.EvaluationGateResult{}),
		reflect.TypeOf(platform.EvaluationGateLimits{}),
		reflect.TypeOf(platform.MinimumCountGate{}),
		reflect.TypeOf(platform.MaximumCountGate{}),
	} {
		var names []string
		for i := range typ.NumField() {
			names = append(names, typ.Field(i).Name)
		}
		for i := range typ.NumMethod() {
			names = append(names, typ.Method(i).Name)
		}
		for _, name := range names {
			lower := strings.ToLower(name)
			for _, bad := range forbidden {
				if strings.Contains(lower, bad) {
					t.Errorf("%s exposes %q, which names a concept the evidence cannot express",
						typ.Name(), name)
				}
			}
		}
	}
}

// No approval gate: ApprovalStatus is producer-supplied evidence, not proof
// of authorization, so it cannot yet gate anything.
func TestGateLimitsExposeOnlyTheThreeApprovedMaximums(t *testing.T) {
	typ := reflect.TypeOf(platform.EvaluationGateLimits{})
	want := map[string]bool{
		"MaxAddedBehaviors": true, "MaxBlockDecisions": true, "MaxCriticalRiskObservations": true,
	}
	if typ.NumField() != len(want) {
		t.Errorf("EvaluationGateLimits has %d fields, want exactly %d", typ.NumField(), len(want))
	}
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !want[f.Name] {
			t.Errorf("unexpected limit %q; a new gate is a policy decision, not a config line", f.Name)
		}
		if f.Type.Kind() != reflect.Uint64 {
			t.Errorf("limit %q has kind %v, want uint64: hard gates are integer-only", f.Name, f.Type.Kind())
		}
	}
}

// Every gate comparison must be integer. A float anywhere in the result would
// mean a value that is not guaranteed bit-identical under record reordering
// took part in a verdict.
func TestGateResultHoldsNoFloatingPointValue(t *testing.T) {
	var walk func(reflect.Type, string)
	seen := map[reflect.Type]bool{}
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Float32, reflect.Float64:
			t.Errorf("%s is a floating-point value; gate evidence must be integer-only", path)
		case reflect.Struct:
			for i := range typ.NumField() {
				f := typ.Field(i)
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(platform.EvaluationGateResult{}), "EvaluationGateResult")
	walk(reflect.TypeOf(platform.EvaluationGatePolicy{}), "EvaluationGatePolicy")
}

func TestGateTypesAreFixedShape(t *testing.T) {
	forbidden := map[reflect.Kind]string{
		reflect.Slice: "slice", reflect.Map: "map", reflect.Ptr: "pointer",
		reflect.Interface: "interface", reflect.Chan: "channel", reflect.Func: "function",
	}
	retained := map[string]bool{
		"EvaluationScorecard": true, "BehaviorDiff": true, "EvaluationAggregate": true,
		"DecisionRecord": true, "BehaviorSnapshot": true, "BehaviorCollector": true,
	}

	var walk func(reflect.Type, string)
	seen := map[reflect.Type]bool{}
	walk = func(typ reflect.Type, path string) {
		if seen[typ] {
			return
		}
		seen[typ] = true
		if name, bad := forbidden[typ.Kind()]; bad {
			t.Errorf("%s is a %s; gate types are fixed-shape", path, name)
			return
		}
		if retained[typ.Name()] {
			t.Errorf("%s retains a %s; the gate keeps no input object", path, typ.Name())
			return
		}
		if typ.Kind() == reflect.Struct {
			for i := range typ.NumField() {
				f := typ.Field(i)
				walk(f.Type, path+"."+f.Name)
			}
		}
	}
	walk(reflect.TypeOf(platform.EvaluationGateResult{}), "EvaluationGateResult")
	walk(reflect.TypeOf(platform.EvaluationGatePolicy{}), "EvaluationGatePolicy")
}

// Task 052's reflection style: a pointer-receiver method would let a caller
// mutate a value the package handed out. The method-set difference is the
// check that actually proves it — a receiver-kind scan over reflect.TypeOf(v)
// is unreachable, because T's method set excludes pointer-receiver methods.
func TestGateTypesHaveNoPointerOnlyMutation(t *testing.T) {
	for _, v := range []any{
		platform.EvaluationGatePolicy{},
		platform.EvaluationGateResult{},
		platform.EvaluationGateLimits{},
	} {
		value := reflect.TypeOf(v)
		pointer := reflect.PointerTo(value)

		valueMethods := map[string]bool{}
		for i := range value.NumMethod() {
			valueMethods[value.Method(i).Name] = true
		}
		for i := range pointer.NumMethod() {
			name := pointer.Method(i).Name
			if !valueMethods[name] {
				t.Errorf("%s has pointer-only method %q; gate values are read-shaped",
					value.Name(), name)
			}
		}
	}
}

// A copied result stays valid: validity is a property of the value, not of
// one instance.
func TestCopyingAGateResultPreservesIt(t *testing.T) {
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("r0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	original := gateOf(t, scorecardOf(t, reference, candidate), strictPolicy())

	duplicate := original
	if duplicate != original {
		t.Fatal("a copied result differs from its original")
	}
	if duplicate.Verdict() != original.Verdict() {
		t.Error("a copied result reports a different verdict")
	}
}

// ---------------------------------------------------------------------
// Real Engine integration
// ---------------------------------------------------------------------

// The full public chain, and the point it proves: the same scorecard yields
// FAIL or PASS depending only on the caller's limits. Evidence and acceptance
// policy are separate, and the engine is not re-run between the two.
func TestRealEngineGate(t *testing.T) {
	buildEvidence := func(runID platform.EvaluationRunID, candidateID platform.CandidateID,
		profile platform.BehavioralProfileRef, operations []string,
	) *evidence {
		t.Helper()
		engine := trustvian.NewEngine(trustvian.WithLearningScope(string(profile)))
		ev := newEvidence(t, runID, candidateID, "staging", profile)

		clock := aggEpoch
		for i, operation := range operations {
			clock = clock.Add(90 * time.Second)
			result, err := engine.Analyze(t.Context(), event.Event{
				ID:        fmt.Sprintf("evt-%s-%d", candidateID, i),
				Timestamp: clock,
				Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
				Operation: event.Operation{Category: event.OperationCategoryTool, Name: operation},
				Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
				Context:   event.Context{Environment: "staging"},
			})
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			ev.feed(t, result.DecisionRecord())
		}
		return ev
	}

	// The candidate performs everything the reference did, plus one new
	// operation — exactly one added behavior, with no hard-coded fingerprint.
	reference := buildEvidence("run-ref", "cand-ref", "profile-ref", []string{"read", "list"})
	candidate := buildEvidence("run-can", "cand-can", "profile-can", []string{"read", "list", "write"})

	card := scorecardOf(t, reference, candidate)
	if got := card.Behavior().AddedCount; got != 1 {
		t.Fatalf("precondition: AddedCount = %d, want 1", got)
	}

	strict := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           0,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
	failed := gateOf(t, card, strict)
	if got := failed.AddedBehaviors().Actual; got != 1 {
		t.Errorf("AddedBehaviors().Actual = %d, want 1", got)
	}
	if failed.AddedBehaviors().Passed {
		t.Error("AddedBehaviors().Passed = true with maximum 0 and one added behavior")
	}
	if failed.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", failed.Verdict())
	}

	// Same card, same evidence, one different limit.
	relaxed := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           1,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
	passed := gateOf(t, card, relaxed)
	if passed.Verdict() != platform.GateVerdictPass {
		t.Errorf("Verdict() = %q, want pass under MaxAddedBehaviors = 1", passed.Verdict())
	}
	if passed.AddedBehaviors().Actual != failed.AddedBehaviors().Actual {
		t.Error("the evidence changed between evaluations; only the limit should have")
	}
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

// benchGateInputs builds a full-size card — 2,048 records and 512 behaviors a
// side, producing the bounded maximum of 1,024 diff deltas — so the benchmark
// measures gate evaluation against the largest evidence it can ever see.
func benchGateInputs(b *testing.B, pass bool) (platform.EvaluationScorecard, platform.EvaluationGatePolicy) {
	b.Helper()
	refAgg, refCol := benchEvidence(b, "run-ref", "cand-ref", "pr", 2048, 512)
	canAgg, canCol := benchEvidence(b, "run-can", "cand-can", "pc", 2048, 512)

	diff, err := platform.CompareBehaviorSnapshots(refCol.Snapshot(), canCol.Snapshot())
	if err != nil {
		b.Fatalf("CompareBehaviorSnapshots() error = %v", err)
	}
	card, err := platform.NewEvaluationScorecard(refAgg, canAgg, diff)
	if err != nil {
		b.Fatalf("NewEvaluationScorecard() error = %v", err)
	}

	// The two profiles share no behavior, so every candidate behavior is
	// added: MaxUint64 passes every gate, 0 fails the added-behavior gate.
	limit := uint64(math.MaxUint64)
	if !pass {
		limit = 0
	}
	return card, platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
		MaxAddedBehaviors:           limit,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	})
}

func BenchmarkEvaluateEvaluationGatePass(b *testing.B) {
	card, policy := benchGateInputs(b, true)
	b.ReportAllocs()
	for b.Loop() {
		result, err := platform.EvaluateEvaluationGate(card, policy)
		if err != nil || result.Verdict() != platform.GateVerdictPass {
			b.Fatalf("EvaluateEvaluationGate() = %q, %v", result.Verdict(), err)
		}
	}
}

func BenchmarkEvaluateEvaluationGateFail(b *testing.B) {
	card, policy := benchGateInputs(b, false)
	b.ReportAllocs()
	for b.Loop() {
		result, err := platform.EvaluateEvaluationGate(card, policy)
		if err != nil || result.Verdict() != platform.GateVerdictFail {
			b.Fatalf("EvaluateEvaluationGate() = %q, %v", result.Verdict(), err)
		}
	}
}
