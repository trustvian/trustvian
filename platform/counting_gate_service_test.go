package platform_test

// The optional counted-change limit through the whole service (ADR 0052,
// issue 131), over a real SQLite store.
//
// These are the cases the gate alone cannot reach, because only the control
// plane holds the observations the fold needs: a complete fold where changes
// and identities differ, a partial correlation where they are forced equal,
// and a promotion that records the check and reads it back across a restart.

import (
	"fmt"
	"math"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	platform "trustvian-platform"
)

// oneActCandidate is one new tool and the transport child it calls: two
// added identities, one counted change, correlation complete.
func oneActCandidate() []trustvian.DecisionRecord {
	return []trustvian.DecisionRecord{
		correlatedRecord("can-1", "fp-read", "read", "store", "trace-c", "span-c", ""),
		correlatedRecord("can-2", "fp-tool", "export_customer", "", "trace-c", "span-tool", "span-c"),
		correlatedRecord("can-3", "fp-http", "post", "export.localhost", "trace-c", "span-http", "span-tool"),
	}
}

// cyclicCandidate adds four identities whose recorded parentage is cyclic, so
// ADR 0052 refuses the fold: correlation partial, four changes.
func cyclicCandidate() []trustvian.DecisionRecord {
	return []trustvian.DecisionRecord{
		correlatedRecord("can-1", "fp-read", "read", "store", "trace-0", "span-0", ""),
		correlatedRecord("can-a", "fp-a", "tool_a", "", "trace-1", "span-a", ""),
		correlatedRecord("can-b", "fp-b", "post", "b.example", "trace-1", "span-b", "span-a"),
		correlatedRecord("can-c", "fp-c", "step_c", "", "trace-2", "span-c", "span-d"),
		correlatedRecord("can-d", "fp-d", "step_d", "", "trace-2", "span-d", "span-c"),
	}
}

func readReference() []trustvian.DecisionRecord {
	return []trustvian.DecisionRecord{
		correlatedRecord("ref-1", "fp-read", "read", "store", "trace-r", "span-r", ""),
	}
}

func serviceLimits(maxAdded uint64, changes platform.OptionalGateLimit) platform.EvaluationGateLimits {
	return platform.EvaluationGateLimits{
		MaxAddedBehaviors:           maxAdded,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
		MaxAddedBehaviorChanges:     changes,
	}
}

func compareWith(
	t *testing.T, f *controlPlaneFixture, limits platform.EvaluationGateLimits,
) platform.EvaluationComparison {
	t.Helper()
	comparison, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can", limits)
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}
	return comparison
}

// The limit reads the authoritative folded count, not the identity count.
func TestChangeLimitReadsTheFoldedCountThroughTheService(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", readReference())
	f.completeCorrelatedRun(t, "run-can", "cand-can", oneActCandidate())

	for _, tc := range []struct {
		name        string
		maxAdded    uint64
		maxChanges  uint64
		changesPass bool
		verdict     platform.GateVerdict
	}{
		// Two identities, one change. A change limit of 1 passes where an
		// identity limit of 1 would not — the whole point of the new unit.
		{"change limit at the folded count", math.MaxUint64, 1, true, platform.GateVerdictPass},
		{"explicit zero", math.MaxUint64, 0, false, platform.GateVerdictFail},
		// Both enforced: the identity limit still fails an act the change
		// limit accepts. Neither replaces the other.
		{"identity limit still enforced", 1, 1, true, platform.GateVerdictFail},
		{"both satisfied", 2, 1, true, platform.GateVerdictPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comparison := compareWith(t, f,
				serviceLimits(tc.maxAdded, platform.NewOptionalGateLimit(tc.maxChanges)))
			changes := comparison.Gate.AddedBehaviorChanges()

			if comparison.Diff.AddedCount() != 2 || comparison.Diff.AddedChangeCount() != 1 {
				t.Fatalf("diff = %d identities / %d changes, want 2 / 1",
					comparison.Diff.AddedCount(), comparison.Diff.AddedChangeCount())
			}
			if !changes.Evaluated() || changes.Actual != 1 || changes.Maximum != tc.maxChanges {
				t.Errorf("check = %+v, want evaluated actual 1 maximum %d; the gate must "+
					"read the diff's AddedChangeCount", changes, tc.maxChanges)
			}
			if changes.Passed != tc.changesPass || comparison.Gate.Verdict() != tc.verdict {
				t.Errorf("passed/verdict = %v/%s, want %v/%s",
					changes.Passed, comparison.Gate.Verdict(), tc.changesPass, tc.verdict)
			}
			if changes.CorrelationState != platform.CorrelationComplete {
				t.Errorf("correlation = %s, want complete", changes.CorrelationState)
			}
			if got := comparison.Gate.AddedBehaviors().Actual; got != 2 {
				t.Errorf("identity check actual = %d, want 2; max_added_behaviors "+
					"still counts identities", got)
			}
		})
	}
}

// Omitting the limit through the service changes nothing a legacy caller sees.
func TestOmittedChangeLimitMovesNoVerdictThroughTheService(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", readReference())
	f.completeCorrelatedRun(t, "run-can", "cand-can", oneActCandidate())

	for _, maxAdded := range []uint64{1, 2} {
		legacy := compareWith(t, f, serviceLimits(maxAdded, platform.OptionalGateLimit{}))
		if got := legacy.Gate.AddedBehaviorChanges().State; got != platform.GateCheckNotEvaluated {
			t.Errorf("state = %s, want not_evaluated", got)
		}
		want := platform.GateVerdictFail
		if maxAdded >= 2 {
			want = platform.GateVerdictPass
		}
		if legacy.Gate.Verdict() != want {
			t.Errorf("max_added_behaviors=%d verdict = %s, want %s — the identity rule "+
				"alone decides when the new limit is omitted", maxAdded, legacy.Gate.Verdict(), want)
		}
	}
}

// Partial correlation is ADR 0052's conservative fallback: the count is the
// identity count, so a change limit set on the assumption of folding fails.
// It is evaluated rather than refused, and says why the count is what it is.
func TestChangeLimitUnderPartialCorrelationUsesTheIdentityCount(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", readReference())
	f.completeCorrelatedRun(t, "run-can", "cand-can", cyclicCandidate())

	comparison := compareWith(t, f, serviceLimits(math.MaxUint64, platform.NewOptionalGateLimit(3)))
	changes := comparison.Gate.AddedBehaviorChanges()

	if changes.CorrelationState != platform.CorrelationPartial {
		t.Fatalf("correlation = %s, want partial", changes.CorrelationState)
	}
	if changes.Actual != 4 || changes.Actual != uint64(comparison.Diff.AddedCount()) {
		t.Errorf("actual = %d over %d identities; partial means the identity count",
			changes.Actual, comparison.Diff.AddedCount())
	}
	if changes.Passed || comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("passed/verdict = %v/%s, want false/fail: an unresolved fold must "+
			"not pass a limit a resolved one would have needed", changes.Passed,
			comparison.Gate.Verdict())
	}
}

// A promotion records the check it consumed, derives its limit from it, and
// reads both back unchanged after a restart. Absent stays absent and zero
// stays zero.
func TestPromotionRecordsTheChangeCheckAcrossARestart(t *testing.T) {
	f := &promotionFixture{newFixture(t)}
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", readReference())
	f.completeCorrelatedRun(t, "run-can", "cand-can", oneActCandidate())
	f.rankEnvironment(t, fixtureEnvironment, 30)
	f.createRankedEnvironment(t, "production", 40)

	requests := []struct {
		id      platform.PromotionID
		changes platform.OptionalGateLimit
		outcome platform.PromotionOutcome
		state   platform.GateCheckState
	}{
		{"promo-absent", platform.OptionalGateLimit{}, platform.PromotionAccepted, platform.GateCheckNotEvaluated},
		{"promo-zero", platform.NewOptionalGateLimit(0), platform.PromotionRejected, platform.GateCheckEvaluated},
		{"promo-one", platform.NewOptionalGateLimit(1), platform.PromotionAccepted, platform.GateCheckEvaluated},
	}
	recorded := map[platform.PromotionID]platform.Promotion{}
	for _, r := range requests {
		request := promotionRequest(serviceLimits(math.MaxUint64, r.changes))
		request.ID = r.id
		promotion, err := f.plane.Promote(t.Context(), request, decisionAt)
		if err != nil {
			t.Fatalf("Promote(%s) error = %v", r.id, err)
		}
		if promotion.Outcome() != r.outcome {
			t.Errorf("%s: outcome = %s, want %s", r.id, promotion.Outcome(), r.outcome)
		}
		if got := promotion.GateResult().AddedBehaviorChanges().State; got != r.state {
			t.Errorf("%s: state = %s, want %s", r.id, got, r.state)
		}
		if promotion.GateLimits().MaxAddedBehaviorChanges != r.changes {
			t.Errorf("%s: recorded limit = %+v, want %+v", r.id,
				promotion.GateLimits().MaxAddedBehaviorChanges, r.changes)
		}
		recorded[r.id] = promotion
	}

	f.store.Close()
	reopened := openFixture(t, f.path)
	for id, before := range recorded {
		after, err := reopened.plane.Promotion(t.Context(), id)
		if err != nil {
			t.Fatalf("Promotion(%s) after restart error = %v", id, err)
		}
		if after.GateResult() != before.GateResult() || after.GateLimits() != before.GateLimits() ||
			after.Outcome() != before.Outcome() {
			t.Errorf("%s changed across a restart:\n before %+v %+v\n after  %+v %+v", id,
				before.GateLimits(), before.GateResult(), after.GateLimits(), after.GateResult())
		}
	}
}

// Retention saturation, end to end: a candidate that ingests past
// MaxRetainedObservations has a partial history, so its correlation is partial,
// its change count is the identity count, and the change limit is evaluated
// against that conservative number (ADR 0052 § correlation completeness).
//
// The act the fold would collapse — a new tool and its transport child — is
// ingested first and so is retained; only the saturation keeps it unfolded.
func TestSaturatedRetentionCountsIdentitiesAndGatesConservatively(t *testing.T) {
	if testing.Short() {
		t.Skip("ingests past the retention bound")
	}
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", readReference())

	records := oneActCandidate()
	for i := len(records); i <= platform.MaxRetainedObservations; i++ {
		records = append(records, correlatedRecord(
			fmt.Sprintf("can-fill-%d", i), "fp-read", "read", "store",
			"trace-fill", fmt.Sprintf("span-fill-%d", i), ""))
	}
	f.completeCorrelatedRun(t, "run-can", "cand-can", records)

	comparison := compareWith(t, f, serviceLimits(math.MaxUint64, platform.NewOptionalGateLimit(1)))
	diff, changes := comparison.Diff, comparison.Gate.AddedBehaviorChanges()

	if diff.CorrelationState() != platform.CorrelationPartial {
		t.Fatalf("correlation = %s, want partial: %d records exceed the %d retention bound",
			diff.CorrelationState(), len(records), platform.MaxRetainedObservations)
	}
	if diff.AddedCount() != 2 || diff.AddedChangeCount() != 2 {
		t.Errorf("identities/changes = %d/%d, want 2/2: a saturated history must not fold",
			diff.AddedCount(), diff.AddedChangeCount())
	}
	if !changes.Evaluated() || changes.Actual != 2 || changes.Passed ||
		changes.CorrelationState != platform.CorrelationPartial ||
		comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("check = %+v verdict %s, want evaluated actual 2 failed under partial "+
			"correlation — the limit of 1 a complete fold would have met must not pass here",
			changes, comparison.Gate.Verdict())
	}
}
