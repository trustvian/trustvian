package platform_test

// The optional counted-change limit (ADR 0052, issue 131), over the gate
// itself.
//
// A scorecard built from snapshots alone carries the unfolded count with
// correlation unavailable — one change per added identity — which is what
// these exercise. The folded count, where changes and identities differ, is
// exercised through the service in counting_gate_service_test.go, because
// only the control plane has the observations a fold needs.

import (
	"errors"
	"math"
	"testing"

	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// changeLimitScorecard is a comparison whose candidate added `added`
// behaviors beside one shared behavior.
func changeLimitScorecard(t *testing.T, added int) platform.EvaluationScorecard {
	t.Helper()
	reference, candidate := gateEvidence(t)
	reference.feed(t, scorecardRecord("shared", "fp-0", "read", "staging", "allow", "low",
		event.ApprovalNotRequired, 0.9))
	candidate.feed(t, scorecardRecord("c0", "fp-0", "read", "staging", "allow", "low",
		event.ApprovalNotRequired, 0.9))
	for i := range added {
		suffix := string(rune('a' + i))
		candidate.feed(t, scorecardRecord("extra"+suffix, "fp-x"+suffix, "op"+suffix,
			"staging", "allow", "low", event.ApprovalNotRequired, 0.9))
	}
	return scorecardOf(t, reference, candidate)
}

// limitsWith is permissive everywhere except the two behavior limits, so the
// verdict isolates them.
func limitsWith(maxAdded uint64, changes platform.OptionalGateLimit) platform.EvaluationGateLimits {
	return platform.EvaluationGateLimits{
		MaxAddedBehaviors:           maxAdded,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
		MaxAddedBehaviorChanges:     changes,
	}
}

// Omitting the limit leaves the gate exactly as it was before the limit
// existed: the same five checks, the same verdict, and the sixth reported as
// not evaluated — not as passed.
func TestOmittedChangeLimitIsNotEvaluatedAndMovesNoVerdict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		added    int
		maxAdded uint64
		verdict  platform.GateVerdict
	}{
		{"legacy pass", 2, 2, platform.GateVerdictPass},
		{"legacy fail", 3, 2, platform.GateVerdictFail},
		{"nothing added", 0, 0, platform.GateVerdictPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := changeLimitScorecard(t, tc.added)
			result := gateOf(t, card, platform.NewEvaluationGatePolicy(
				limitsWith(tc.maxAdded, platform.OptionalGateLimit{})))

			if result.Verdict() != tc.verdict {
				t.Errorf("verdict = %s, want %s — omitting the new limit must not move "+
					"a legacy verdict", result.Verdict(), tc.verdict)
			}
			changes := result.AddedBehaviorChanges()
			if changes != (platform.ChangeCountGate{State: platform.GateCheckNotEvaluated}) {
				t.Errorf("check = %+v, want not_evaluated with no outcome; absent is "+
					"neither a pass nor a zero", changes)
			}
		})
	}
}

// Explicit zero is a limit, the strictest one, and distinct from absent.
func TestChangeLimitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name       string
		added      int
		maximum    uint64
		wantPassed bool
	}{
		{"zero maximum, nothing added", 0, 0, true},
		{"zero maximum, one added", 1, 0, false},
		{"below maximum", 1, 2, true},
		{"exactly at maximum", 2, 2, true},
		{"one over maximum", 3, 2, false},
		{"uint64 maximum", 3, math.MaxUint64, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := changeLimitScorecard(t, tc.added)
			result := gateOf(t, card, platform.NewEvaluationGatePolicy(
				limitsWith(math.MaxUint64, platform.NewOptionalGateLimit(tc.maximum))))

			changes := result.AddedBehaviorChanges()
			if !changes.Evaluated() {
				t.Fatalf("state = %s, want evaluated: the caller supplied a limit", changes.State)
			}
			if changes.Actual != uint64(tc.added) || changes.Maximum != tc.maximum {
				t.Errorf("actual/maximum = %d/%d, want %d/%d",
					changes.Actual, changes.Maximum, tc.added, tc.maximum)
			}
			if changes.Passed != tc.wantPassed {
				t.Errorf("passed = %v, want %v", changes.Passed, tc.wantPassed)
			}
			wantVerdict := platform.GateVerdictFail
			if tc.wantPassed {
				wantVerdict = platform.GateVerdictPass
			}
			if result.Verdict() != wantVerdict {
				t.Errorf("verdict = %s, want %s: an evaluated check is part of the verdict",
					result.Verdict(), wantVerdict)
			}
			// Snapshots hold no parentage, so this is the unfolded count and
			// says so.
			if changes.CorrelationState != platform.CorrelationUnavailable ||
				changes.CountingPolicyVersion != platform.CountingPolicyVersion {
				t.Errorf("counting context = %s/%q, want unavailable/%q",
					changes.CorrelationState, changes.CountingPolicyVersion,
					platform.CountingPolicyVersion)
			}
		})
	}
}

// Both limits are enforced; neither replaces the other.
func TestIdentityAndChangeLimitsAreBothEnforced(t *testing.T) {
	card := changeLimitScorecard(t, 2) // two identities, two unfolded changes
	for _, tc := range []struct {
		name         string
		maxAdded     uint64
		maxChanges   uint64
		identityPass bool
		changesPass  bool
		wantVerdict  platform.GateVerdict
	}{
		{"both pass", 2, 2, true, true, platform.GateVerdictPass},
		{"identity limit fails", 1, 2, false, true, platform.GateVerdictFail},
		{"change limit fails", 2, 1, true, false, platform.GateVerdictFail},
		{"both fail", 1, 1, false, false, platform.GateVerdictFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := gateOf(t, card, platform.NewEvaluationGatePolicy(
				limitsWith(tc.maxAdded, platform.NewOptionalGateLimit(tc.maxChanges))))
			if result.AddedBehaviors().Passed != tc.identityPass {
				t.Errorf("identity check passed = %v, want %v",
					result.AddedBehaviors().Passed, tc.identityPass)
			}
			if result.AddedBehaviorChanges().Passed != tc.changesPass {
				t.Errorf("change check passed = %v, want %v",
					result.AddedBehaviorChanges().Passed, tc.changesPass)
			}
			if result.Verdict() != tc.wantVerdict {
				t.Errorf("verdict = %s, want %s", result.Verdict(), tc.wantVerdict)
			}
		})
	}
}

// The limit is policy, the count is evidence: the same scorecard under the
// same limits gives the same result every time.
func TestChangeLimitEvaluationIsDeterministic(t *testing.T) {
	card := changeLimitScorecard(t, 3)
	policy := platform.NewEvaluationGatePolicy(
		limitsWith(math.MaxUint64, platform.NewOptionalGateLimit(2)))
	first := gateOf(t, card, policy)
	for range 20 {
		if again := gateOf(t, card, policy); again != first {
			t.Fatalf("gate result changed between evaluations: %+v vs %+v", again, first)
		}
	}
}

// A policy's optional limit survives the policy: what went in comes out.
func TestPolicyCarriesTheOptionalLimitVerbatim(t *testing.T) {
	for _, limit := range []platform.OptionalGateLimit{
		{}, platform.NewOptionalGateLimit(0), platform.NewOptionalGateLimit(math.MaxUint64),
	} {
		policy := platform.NewEvaluationGatePolicy(limitsWith(1, limit))
		if got := policy.Limits().MaxAddedBehaviorChanges; got != limit {
			t.Errorf("policy limit = %+v, want %+v", got, limit)
		}
	}
	if (platform.OptionalGateLimit{}).IsSet() {
		t.Error("the zero OptionalGateLimit reports set; its zero value must be absent")
	}
	if maximum, set := platform.NewOptionalGateLimit(0).Maximum(); !set || maximum != 0 {
		t.Errorf("NewOptionalGateLimit(0) = (%d, %v), want (0, true)", maximum, set)
	}
}

// An unbound policy is refused before the optional limit is looked at.
func TestUnboundPolicyIsRefusedWhateverItsOptionalLimit(t *testing.T) {
	card := changeLimitScorecard(t, 1)
	_, err := platform.EvaluateEvaluationGate(card, platform.EvaluationGatePolicy{})
	if !errors.Is(err, platform.ErrInvalidGatePolicy) {
		t.Errorf("error = %v, want ErrInvalidGatePolicy", err)
	}
}
