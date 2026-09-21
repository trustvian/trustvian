package platform_test

// Task 055: evaluation scorecards.
//
// Two classes of assertion dominate this file.
//
// The refusals: three pieces of evidence must describe one comparison, and
// every way of combining unrelated evidence — a mismatched run, a mismatched
// environment, two partial views of one evaluation — produces a card that
// looks complete and is wrong.
//
// The absences: no overall score, no threshold, no verdict, and no field
// reporting zero for a concept the evidence cannot express. Those are pinned
// structurally, because a behavioral test cannot notice a field being added.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// evidence is one evaluation's two parallel reducers, fed from one stream.
type evidence struct {
	run       platform.EvaluationRun
	aggregate platform.EvaluationAggregate
	collector *platform.BehaviorCollector
}

func newEvidence(t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
	environment platform.EnvironmentRef, profile platform.BehavioralProfileRef,
) *evidence {
	t.Helper()
	run := behaviorRun(t, runID, candidateID, environment, profile)
	agg, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	return &evidence{run: run, aggregate: agg, collector: newCollector(t, run)}
}

// feed sends one record down both evidence paths, which is the invariant the
// scorecard's count check exists to protect.
func (e *evidence) feed(t *testing.T, record trustvian.DecisionRecord) {
	t.Helper()
	agg, err := e.aggregate.AddRecord(record)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	e.aggregate = agg
	if err := e.collector.Observe(record); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
}

// feedAggregateOnly deliberately desynchronizes the two paths.
func (e *evidence) feedAggregateOnly(t *testing.T, record trustvian.DecisionRecord) {
	t.Helper()
	agg, err := e.aggregate.AddRecord(record)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	e.aggregate = agg
}

// scorecardRecord is a complete, valid record for one behavioral shape.
func scorecardRecord(eventID, fingerprintID, operation string, environment platform.EnvironmentRef,
	decision, risk string, approval event.ApprovalStatus, trust float64,
) trustvian.DecisionRecord {
	rec := behaviorRecord(eventID, fingerprintID, operation, "build-host", string(environment))
	rec.Decision = decision
	rec.RiskLevel = risk
	rec.ApprovalStatus = approval
	rec.IdentityConfidence = 0.9
	rec.AnomalyScore = 0.4
	rec.AnomalyConfidence = 0.5
	rec.TrustScore = trust
	rec.ContextRisk = 0.1
	return rec
}

func diffOf(t *testing.T, reference, candidate *evidence) platform.BehaviorDiff {
	t.Helper()
	diff, err := platform.CompareBehaviorSnapshots(reference.collector.Snapshot(), candidate.collector.Snapshot())
	if err != nil {
		t.Fatalf("CompareBehaviorSnapshots() error = %v", err)
	}
	return diff
}

func scorecardOf(t *testing.T, reference, candidate *evidence) platform.EvaluationScorecard {
	t.Helper()
	card, err := platform.NewEvaluationScorecard(reference.aggregate, candidate.aggregate, diffOf(t, reference, candidate))
	if err != nil {
		t.Fatalf("NewEvaluationScorecard() error = %v", err)
	}
	return card
}

// ---------------------------------------------------------------------
// Construction and identity
// ---------------------------------------------------------------------

func TestScorecardCapturesComparisonIdentity(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")

	ref.feed(t, scorecardRecord("evt-r1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
	cand.feed(t, scorecardRecord("evt-c1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.8))

	card := scorecardOf(t, ref, cand)

	if card.ReferenceRunID() != "run-ref" || card.CandidateRunID() != "run-cand" {
		t.Errorf("run identity = %q / %q", card.ReferenceRunID(), card.CandidateRunID())
	}
	if card.ReferenceCandidateID() != "cand-ref" || card.CandidateCandidateID() != "cand-new" {
		t.Errorf("candidate identity = %q / %q", card.ReferenceCandidateID(), card.CandidateCandidateID())
	}
	if card.ReferenceBehavioralProfile() != "profile-ref" || card.CandidateBehavioralProfile() != "profile-cand" {
		t.Errorf("profile identity = %q / %q",
			card.ReferenceBehavioralProfile(), card.CandidateBehavioralProfile())
	}
	if card.Environment() != testEnvironment {
		t.Errorf("Environment() = %q, want %q", card.Environment(), testEnvironment)
	}
	if card.ReferenceRecordCount() != 1 || card.CandidateRecordCount() != 1 {
		t.Errorf("record counts = %d / %d, want 1 / 1", card.ReferenceRecordCount(), card.CandidateRecordCount())
	}
}

// TestScorecardRejectsIncompatibleEvidence is the heart of the trust
// boundary: three individually valid pieces of evidence must describe one
// comparison, and there is no partial card.
func TestScorecardRejectsIncompatibleEvidence(t *testing.T) {
	// A well-formed baseline everything else deviates from.
	build := func(t *testing.T, refRun, candRun platform.EvaluationRunID,
		refCand, candCand platform.CandidateID,
		refEnv, candEnv platform.EnvironmentRef,
		refProf, candProf platform.BehavioralProfileRef,
	) (*evidence, *evidence) {
		t.Helper()
		ref := newEvidence(t, refRun, refCand, refEnv, refProf)
		cand := newEvidence(t, candRun, candCand, candEnv, candProf)
		ref.feed(t, scorecardRecord("evt-r1", "fp-a", "shell.read", refEnv, "allow", "low", event.ApprovalNotRequired, 0.9))
		cand.feed(t, scorecardRecord("evt-c1", "fp-a", "shell.read", candEnv, "allow", "low", event.ApprovalNotRequired, 0.9))
		return ref, cand
	}

	t.Run("reference run mismatch", func(t *testing.T) {
		ref, cand := build(t, "run-a", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")
		other, _ := build(t, "run-b", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")

		// The diff describes run-b as its reference; the aggregate is run-a.
		diff := diffOf(t, other, cand)
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diff)
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})

	t.Run("candidate run mismatch", func(t *testing.T) {
		ref, cand := build(t, "run-a", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")
		_, other := build(t, "run-a", "run-d", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")

		diff := diffOf(t, ref, other)
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diff)
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})

	t.Run("candidate id mismatch", func(t *testing.T) {
		ref, cand := build(t, "run-a", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")
		_, other := build(t, "run-a", "run-c", "cand-r", "cand-OTHER", testEnvironment, testEnvironment, "p-r", "p-c")

		diff := diffOf(t, ref, other)
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diff)
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})

	t.Run("profile mismatch on one side", func(t *testing.T) {
		ref, cand := build(t, "run-a", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-c")
		_, other := build(t, "run-a", "run-c", "cand-r", "cand-c", testEnvironment, testEnvironment, "p-r", "p-OTHER")

		diff := diffOf(t, ref, other)
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diff)
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})

	t.Run("environment mismatch", func(t *testing.T) {
		// A cross-environment diff is refused by task 054 before it reaches
		// here, so this checks the aggregate-vs-diff environment agreement:
		// both aggregates in production, the diff describing staging.
		ref, cand := build(t, "run-a", "run-c", "cand-r", "cand-c", "production", "production", "p-r", "p-c")
		sRef, sCand := build(t, "run-a", "run-c", "cand-r", "cand-c", "staging", "staging", "p-r", "p-c")

		diff := diffOf(t, sRef, sCand)
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diff)
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})
}

// TestScorecardRejectsDesynchronizedEvidence is the check that catches two
// partial views of one evaluation.
//
// Tasks 053 and 054 consume the same stream. If the aggregate saw one more
// record than the collector, a card would put a decision distribution over N
// events beside a behavioral summary over N-1 — and there is no correct way
// to reconcile that, so it is refused rather than best-efforted.
func TestScorecardRejectsDesynchronizedEvidence(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")

	ref.feed(t, scorecardRecord("evt-r1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
	cand.feed(t, scorecardRecord("evt-c1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))

	t.Run("compatible before divergence", func(t *testing.T) {
		if _, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diffOf(t, ref, cand)); err != nil {
			t.Fatalf("premise broken, the evidence should be compatible: %v", err)
		}
	})

	// One record reaches the aggregate and not the collector.
	cand.feedAggregateOnly(t, scorecardRecord("evt-c2", "fp-a", "shell.read", testEnvironment, "block", "critical", event.ApprovalDenied, 0.1))

	t.Run("candidate paths disagree", func(t *testing.T) {
		_, err := platform.NewEvaluationScorecard(ref.aggregate, cand.aggregate, diffOf(t, ref, cand))
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})

	t.Run("reference paths disagree", func(t *testing.T) {
		ref2 := newEvidence(t, "run-r2", "cand-r2", testEnvironment, "p-r2")
		cand2 := newEvidence(t, "run-c2", "cand-c2", testEnvironment, "p-c2")
		ref2.feed(t, scorecardRecord("evt-1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
		ref2.feedAggregateOnly(t, scorecardRecord("evt-2", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
		cand2.feed(t, scorecardRecord("evt-3", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))

		_, err := platform.NewEvaluationScorecard(ref2.aggregate, cand2.aggregate, diffOf(t, ref2, cand2))
		if !errors.Is(err, platform.ErrScorecardEvidenceMismatch) {
			t.Fatalf("error = %v, want ErrScorecardEvidenceMismatch", err)
		}
	})
}

// TestScorecardRejectsZeroValueEvidence: a zero aggregate's counts are all
// zero, which is indistinguishable by value from a valid empty evaluation and
// completely different in meaning. Same for a diff with no identity at all.
func TestScorecardRejectsZeroValueEvidence(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")
	diff := diffOf(t, ref, cand)

	tests := map[string]struct {
		reference, candidate platform.EvaluationAggregate
		diff                 platform.BehaviorDiff
	}{
		"zero reference aggregate": {platform.EvaluationAggregate{}, cand.aggregate, diff},
		"zero candidate aggregate": {ref.aggregate, platform.EvaluationAggregate{}, diff},
		"zero diff":                {ref.aggregate, cand.aggregate, platform.BehaviorDiff{}},
		"all zero":                 {platform.EvaluationAggregate{}, platform.EvaluationAggregate{}, platform.BehaviorDiff{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := platform.NewEvaluationScorecard(tt.reference, tt.candidate, tt.diff)
			if !errors.Is(err, platform.ErrInvalidScorecardEvidence) {
				t.Fatalf("error = %v, want one wrapping ErrInvalidScorecardEvidence", err)
			}
			if got != (platform.EvaluationScorecard{}) {
				t.Error("a refused construction returned a non-zero scorecard")
			}
		})
	}
}

// TestValidEmptyEvaluationProducesAScorecard: empty is not malformed. Two
// evaluations that observed nothing are valid evidence that nothing happened.
func TestValidEmptyEvaluationProducesAScorecard(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")

	card := scorecardOf(t, ref, cand)

	if card.ReferenceRecordCount() != 0 || card.CandidateRecordCount() != 0 {
		t.Fatalf("record counts = %d / %d, want 0 / 0", card.ReferenceRecordCount(), card.CandidateRecordCount())
	}

	// Every rate, delta, mean and ratio is undefined rather than zero.
	if _, ok := card.Decisions().Block.Candidate().Value(); ok {
		t.Error("a block rate was defined for an evaluation that observed nothing")
	}
	if _, ok := card.Decisions().Block.Delta(); ok {
		t.Error("a delta was defined with no observations on either side")
	}
	if _, ok := card.Risks().Critical.Candidate().Value(); ok {
		t.Error("a critical-risk rate was defined for an empty evaluation")
	}
	if _, ok := card.Metrics().TrustScore.MeanDelta(); ok {
		t.Error("a mean delta was defined with no observations")
	}
	for name, got := range map[string]func() (float64, bool){
		"PresenceOverlap":      card.Behavior().PresenceOverlap,
		"AddedCandidateRate":   card.Behavior().AddedCandidateRate,
		"RemovedReferenceRate": card.Behavior().RemovedReferenceRate,
	} {
		if v, ok := got(); ok {
			t.Errorf("%s() = (%v, true) for an empty comparison; two unmeasured evaluations are not identical", name, v)
		}
	}
}

// ---------------------------------------------------------------------
// Distributions
// ---------------------------------------------------------------------

// TestScorecardDistributions pins the arithmetic with small exact numbers,
// covering every fixed struct so a mis-wired field shows up here.
func TestScorecardDistributions(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")

	// Reference: 10 records — 6 allow, 1 observe_only, 1 alert, 1 challenge,
	// 1 block; risks 7 low / 2 high / 1 critical; approvals 8 not_required /
	// 1 required / 1 denied.
	type spec struct {
		decision, risk string
		approval       event.ApprovalStatus
		matchedRule    bool
	}
	refSpecs := []spec{
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"observe_only", "low", event.ApprovalNotRequired, false},
		{"alert", "high", event.ApprovalNotRequired, true},
		{"challenge", "high", event.ApprovalRequired, true},
		{"block", "critical", event.ApprovalDenied, true},
	}
	// Candidate: 10 records — 5 allow, 2 alert, 3 block; risks 5 low / 2 high
	// / 3 critical; approvals 5 not_required / 2 approved / 3 denied.
	candSpecs := []spec{
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"allow", "low", event.ApprovalNotRequired, false},
		{"alert", "high", event.ApprovalApproved, true},
		{"alert", "high", event.ApprovalApproved, true},
		{"block", "critical", event.ApprovalDenied, true},
		{"block", "critical", event.ApprovalDenied, true},
		{"block", "critical", event.ApprovalDenied, true},
	}

	feed := func(e *evidence, prefix string, specs []spec) {
		for i, s := range specs {
			rec := scorecardRecord(fmt.Sprintf("evt-%s-%d", prefix, i), "fp-a", "shell.read",
				testEnvironment, s.decision, s.risk, s.approval, 0.8)
			if s.matchedRule {
				rec.MatchedDefault = false
				rec.PolicyRule = "flag-tool-use"
			}
			e.feed(t, rec)
		}
	}
	feed(ref, "r", refSpecs)
	feed(cand, "c", candSpecs)

	card := scorecardOf(t, ref, cand)

	check := func(t *testing.T, name string, c platform.RateComparison, refCount, candCount uint64) {
		t.Helper()
		if c.Reference().Count() != refCount || c.Candidate().Count() != candCount {
			t.Errorf("%s counts = %d/%d, want %d/%d",
				name, c.Reference().Count(), c.Candidate().Count(), refCount, candCount)
			return
		}
		wantRef, wantCand := float64(refCount)/10, float64(candCount)/10
		if v, ok := c.Reference().Value(); !ok || v != wantRef {
			t.Errorf("%s reference rate = (%v, %v), want (%v, true)", name, v, ok, wantRef)
		}
		if v, ok := c.Candidate().Value(); !ok || v != wantCand {
			t.Errorf("%s candidate rate = (%v, %v), want (%v, true)", name, v, ok, wantCand)
		}
		if d, ok := c.Delta(); !ok || d != wantCand-wantRef {
			t.Errorf("%s delta = (%v, %v), want (%v, true)", name, d, ok, wantCand-wantRef)
		}
	}

	t.Run("decisions", func(t *testing.T) {
		d := card.Decisions()
		check(t, "allow", d.Allow, 6, 5)
		check(t, "observe_only", d.ObserveOnly, 1, 0)
		check(t, "alert", d.Alert, 1, 2)
		check(t, "challenge", d.Challenge, 1, 0)
		check(t, "require_approval", d.RequireApproval, 0, 0)
		check(t, "block", d.Block, 1, 3)
	})

	t.Run("risks", func(t *testing.T) {
		r := card.Risks()
		check(t, "low", r.Low, 7, 5)
		check(t, "medium", r.Medium, 0, 0)
		check(t, "high", r.High, 2, 2)
		check(t, "critical", r.Critical, 1, 3)
	})

	t.Run("approvals", func(t *testing.T) {
		a := card.Approvals()
		check(t, "unspecified", a.Unspecified, 0, 0)
		check(t, "not_required", a.NotRequired, 8, 5)
		check(t, "required", a.Required, 1, 0)
		check(t, "approved", a.Approved, 0, 2)
		check(t, "denied", a.Denied, 1, 3)
	})

	t.Run("policy selection", func(t *testing.T) {
		p := card.PolicySelection()
		check(t, "matched_rule", p.MatchedRule, 3, 5)
		check(t, "matched_default", p.MatchedDefault, 7, 5)
	})
}

// TestScorecardMetricComparisons: summaries are copied verbatim and the mean
// delta is exact.
func TestScorecardMetricComparisons(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")

	for i, trust := range []float64{0.8, 1.0} {
		ref.feed(t, scorecardRecord(fmt.Sprintf("evt-r%d", i), "fp-a", "shell.read",
			testEnvironment, "allow", "low", event.ApprovalNotRequired, trust))
	}
	for i, trust := range []float64{0.2, 0.4} {
		cand.feed(t, scorecardRecord(fmt.Sprintf("evt-c%d", i), "fp-a", "shell.read",
			testEnvironment, "allow", "low", event.ApprovalNotRequired, trust))
	}

	card := scorecardOf(t, ref, cand)
	trust := card.Metrics().TrustScore

	if trust.Reference() != ref.aggregate.TrustScore() {
		t.Errorf("reference summary not copied verbatim: %+v vs %+v", trust.Reference(), ref.aggregate.TrustScore())
	}
	if trust.Candidate() != cand.aggregate.TrustScore() {
		t.Errorf("candidate summary not copied verbatim: %+v vs %+v", trust.Candidate(), cand.aggregate.TrustScore())
	}

	// 0.3 - 0.9, computed from the summaries rather than restated.
	refMean, _ := trust.Reference().Mean()
	candMean, _ := trust.Candidate().Mean()
	got, ok := trust.MeanDelta()
	if !ok || got != candMean-refMean {
		t.Fatalf("MeanDelta() = (%v, %v), want (%v, true)", got, ok, candMean-refMean)
	}
	if got >= 0 {
		t.Errorf("MeanDelta() = %v; trust fell, so the delta should be negative", got)
	}

	// Every other signal is wired too.
	m := card.Metrics()
	for name, c := range map[string]platform.MetricComparison{
		"IdentityConfidence": m.IdentityConfidence,
		"AnomalyScore":       m.AnomalyScore,
		"AnomalyConfidence":  m.AnomalyConfidence,
		"ContextRisk":        m.ContextRisk,
	} {
		if c.Reference().Count != 2 || c.Candidate().Count != 2 {
			t.Errorf("%s counts = %d/%d, want 2/2", name, c.Reference().Count, c.Candidate().Count)
		}
		if d, ok := c.MeanDelta(); !ok || d != 0 {
			t.Errorf("%s MeanDelta() = (%v, %v), want (0, true)", name, d, ok)
		}
	}
}

// TestMeanDeltaUndefinedWhenOneSideIsEmpty: a delta against an evaluation
// that observed nothing is not a movement.
func TestMeanDeltaUndefinedWhenOneSideIsEmpty(t *testing.T) {
	ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
	cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")
	cand.feed(t, scorecardRecord("evt-c1", "fp-a", "shell.read", testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.5))

	card := scorecardOf(t, ref, cand)

	if _, ok := card.Metrics().TrustScore.Reference().Mean(); ok {
		t.Error("the empty reference reported a mean")
	}
	if v, ok := card.Metrics().TrustScore.Candidate().Mean(); !ok || v != 0.5 {
		t.Errorf("candidate mean = (%v, %v), want (0.5, true)", v, ok)
	}
	if d, ok := card.Metrics().TrustScore.MeanDelta(); ok {
		t.Errorf("MeanDelta() = (%v, true) against an empty reference", d)
	}
	if d, ok := card.Decisions().Allow.Delta(); ok {
		t.Errorf("Delta() = (%v, true) against an empty reference", d)
	}
}

// ---------------------------------------------------------------------
// Behavioral summary
// ---------------------------------------------------------------------

func TestScorecardBehaviorSummary(t *testing.T) {
	// behaviors builds one evidence pair with the named behavioral shapes.
	build := func(t *testing.T, refOps, candOps []string) platform.EvaluationScorecard {
		t.Helper()
		ref := newEvidence(t, "run-ref", "cand-ref", testEnvironment, "profile-ref")
		cand := newEvidence(t, "run-cand", "cand-new", testEnvironment, "profile-cand")
		for i, op := range refOps {
			ref.feed(t, scorecardRecord(fmt.Sprintf("evt-r%d", i), "fp-"+op, op,
				testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
		}
		for i, op := range candOps {
			cand.feed(t, scorecardRecord(fmt.Sprintf("evt-c%d", i), "fp-"+op, op,
				testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
		}
		return scorecardOf(t, ref, cand)
	}

	tests := map[string]struct {
		refOps, candOps                []string
		added, removed, shared         int
		refDistinct, candDistinct      int
		wantOverlap                    float64
		overlapDefined                 bool
		wantAddedRate, wantRemovedRate float64
		addedDefined, removedDefined   bool
	}{
		"identical": {
			[]string{"a", "b"}, []string{"a", "b"},
			0, 0, 2, 2, 2,
			1.0, true, 0.0, 0.0, true, true,
		},
		"added only": {
			[]string{"a"}, []string{"a", "b"},
			1, 0, 1, 1, 2,
			0.5, true, 0.5, 0.0, true, true,
		},
		"removed only": {
			[]string{"a", "b"}, []string{"a"},
			0, 1, 1, 2, 1,
			0.5, true, 0.0, 0.5, true, true,
		},
		"mixed": {
			[]string{"a", "b"}, []string{"a", "c"},
			1, 1, 1, 2, 2,
			1.0 / 3.0, true, 0.5, 0.5, true, true,
		},
		"both empty": {
			nil, nil,
			0, 0, 0, 0, 0,
			0, false, 0, 0, false, false,
		},
		"empty reference": {
			nil, []string{"a"},
			1, 0, 0, 0, 1,
			0.0, true, 1.0, 0, true, false,
		},
		"empty candidate": {
			[]string{"a"}, nil,
			0, 1, 0, 1, 0,
			0.0, true, 0, 1.0, false, true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			b := build(t, tt.refOps, tt.candOps).Behavior()

			if b.AddedCount != tt.added || b.RemovedCount != tt.removed || b.SharedCount != tt.shared {
				t.Fatalf("added/removed/shared = %d/%d/%d, want %d/%d/%d",
					b.AddedCount, b.RemovedCount, b.SharedCount, tt.added, tt.removed, tt.shared)
			}
			if b.ReferenceDistinctCount != tt.refDistinct || b.CandidateDistinctCount != tt.candDistinct {
				t.Fatalf("distinct = %d/%d, want %d/%d",
					b.ReferenceDistinctCount, b.CandidateDistinctCount, tt.refDistinct, tt.candDistinct)
			}

			if v, ok := b.PresenceOverlap(); ok != tt.overlapDefined || (ok && v != tt.wantOverlap) {
				t.Errorf("PresenceOverlap() = (%v, %v), want (%v, %v)", v, ok, tt.wantOverlap, tt.overlapDefined)
			}
			if v, ok := b.AddedCandidateRate(); ok != tt.addedDefined || (ok && v != tt.wantAddedRate) {
				t.Errorf("AddedCandidateRate() = (%v, %v), want (%v, %v)", v, ok, tt.wantAddedRate, tt.addedDefined)
			}
			if v, ok := b.RemovedReferenceRate(); ok != tt.removedDefined || (ok && v != tt.wantRemovedRate) {
				t.Errorf("RemovedReferenceRate() = (%v, %v), want (%v, %v)", v, ok, tt.wantRemovedRate, tt.removedDefined)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Structural guarantees
// ---------------------------------------------------------------------

// TestScorecardExposesNoVerdict pins the absences. Each name is a judgement
// the evidence cannot support, and several are concepts no current type can
// express at all — reported as zero they would be false security claims.
func TestScorecardExposesNoVerdict(t *testing.T) {
	forbidden := []string{
		// Composite scores. A single number becomes the field callers read
		// instead of the gate, and lets one dimension offset another — which
		// the roadmap forbids outright.
		"OverallScore", "SecurityScore", "SafetyScore", "EvaluationScore",
		"Score", "Grade", "Rating",
		// Verdicts. Task 056 owns these.
		"Passed", "Failed", "Promotable", "Verdict", "Recommendation", "Acceptable",
		// Concepts the evidence cannot express: PolicyRule carries no
		// severity, ContextRisk has no sensitivity threshold, approval status
		// is producer-supplied, and the aggregate retains no per-event
		// rule/resource/delegation correlation.
		"CriticalPolicyViolations", "BlockedSensitiveActions", "UnapprovedSensitiveActions",
		"PolicyComplianceScore", "ApprovalComplianceScore", "SensitiveResourceScore",
		"DelegationScore", "DelegationStability", "BehavioralStabilityScore",
		// Thresholds are configuration and belong to the gate.
		"Threshold", "MaxNewBehaviors", "MaxBlockRate", "MinTrustScore",
	}

	for _, typ := range []struct {
		name  string
		value any
	}{
		{"EvaluationScorecard", platform.EvaluationScorecard{}},
		{"BehaviorSummary", platform.BehaviorSummary{}},
		{"DecisionComparison", platform.DecisionComparison{}},
		{"RiskComparison", platform.RiskComparison{}},
		{"ApprovalComparison", platform.ApprovalComparison{}},
	} {
		t.Run(typ.name, func(t *testing.T) {
			rt := reflect.TypeOf(typ.value)
			for _, bad := range forbidden {
				if _, ok := rt.MethodByName(bad); ok {
					t.Errorf("%s.%s exists; acceptance decisions belong to the gate task, not to evidence", typ.name, bad)
				}
				if _, ok := rt.FieldByName(bad); ok {
					t.Errorf("%s.%s exists as a field; see above", typ.name, bad)
				}
			}
		})
	}
}

// TestScorecardIsFixedShape: construction cost and size must be independent
// of how many records were aggregated and how many behaviors were compared,
// which holds only if nothing variable-length is retained.
func TestScorecardIsFixedShape(t *testing.T) {
	forbidden := map[reflect.Kind]string{
		reflect.Slice:         "a slice grows with the evidence",
		reflect.Map:           "a map is keyed by a caller-controlled value",
		reflect.Pointer:       "a pointer can reach something that grows",
		reflect.Chan:          "a channel is not a value",
		reflect.Interface:     "an interface can hold anything, including retained evidence",
		reflect.UnsafePointer: "unsafe",
	}
	banned := map[reflect.Type]string{
		reflect.TypeOf(trustvian.DecisionRecord{}):     "DecisionRecord",
		reflect.TypeOf(platform.BehaviorDelta{}):       "BehaviorDelta",
		reflect.TypeOf(platform.BehaviorDiff{}):        "BehaviorDiff",
		reflect.TypeOf(platform.EvaluationAggregate{}): "EvaluationAggregate",
	}

	var inspect func(t *testing.T, typ reflect.Type, path string, depth int)
	inspect = func(t *testing.T, typ reflect.Type, path string, depth int) {
		if depth > 6 {
			t.Fatalf("%s: nesting deeper than expected", path)
		}
		if name, bad := banned[typ]; bad {
			t.Errorf("%s retains a %s; the scorecard is a summary, not a copy of its inputs", path, name)
			return
		}
		if reason, bad := forbidden[typ.Kind()]; bad {
			t.Errorf("%s is a %s: %s", path, typ.Kind(), reason)
			return
		}
		switch typ.Kind() {
		case reflect.Array:
			inspect(t, typ.Elem(), path+"[...]", depth+1)
		case reflect.Struct:
			if typ == reflect.TypeOf(time.Time{}) {
				return
			}
			for i := range typ.NumField() {
				inspect(t, typ.Field(i).Type, path+"."+typ.Field(i).Name, depth+1)
			}
		}
	}
	inspect(t, reflect.TypeOf(platform.EvaluationScorecard{}), "EvaluationScorecard", 0)

	// And the size is genuinely constant: a card from a 200-record, 40-behavior
	// comparison is the same size as one from an empty pair.
	small := newEvidence(t, "run-a", "cand-a", testEnvironment, "p-a")
	big := newEvidence(t, "run-b", "cand-b", testEnvironment, "p-b")
	for i := range 200 {
		big.feed(t, scorecardRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i%40),
			fmt.Sprintf("tool.%d", i%40), testEnvironment, "allow", "low", event.ApprovalNotRequired, 0.9))
	}
	empty := scorecardOf(t, small, small)
	full := scorecardOf(t, big, big)

	if got, want := reflect.TypeOf(full).Size(), reflect.TypeOf(empty).Size(); got != want {
		t.Errorf("scorecard size changed from %d to %d bytes", want, got)
	}
	if full.Behavior().SharedCount != 40 {
		t.Errorf("SharedCount = %d, want 40", full.Behavior().SharedCount)
	}
}

// ---------------------------------------------------------------------
// The cross-module proof
// ---------------------------------------------------------------------

// TestRealEngineScorecard drives the entire public chain on real engine
// output:
//
//	Engine → DecisionRecord → Aggregate + BehaviorSnapshot → Diff → Scorecard
//
// Every step uses the public API only, and behaviors are located through
// their stable descriptors rather than hard-coded fingerprint hashes.
func TestRealEngineScorecard(t *testing.T) {
	const environment platform.EnvironmentRef = "staging"

	collect := func(t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
		profile platform.BehavioralProfileRef, operations []string,
	) *evidence {
		t.Helper()
		engine := trustvian.NewEngine(trustvian.WithLearningScope(string(profile)))
		e := newEvidence(t, runID, candidateID, environment, profile)

		clock := aggEpoch
		for i, op := range operations {
			clock = clock.Add(90 * time.Second)
			ev := event.Event{
				ID:        fmt.Sprintf("evt-%s-%d", candidateID, i),
				Timestamp: clock,
				Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
				Operation: event.Operation{Category: event.OperationCategoryTool, Name: op},
				Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
				Context:   event.Context{Environment: string(environment)},
			}
			result, err := engine.Analyze(t.Context(), ev)
			if err != nil {
				t.Fatalf("Analyze() error = %v", err)
			}
			e.feed(t, result.DecisionRecord())
		}
		return e
	}

	reference := collect(t, "run-ref", "cand-ref", "profile-ref",
		[]string{"shell.read", "shell.read", "db.query"})
	candidate := collect(t, "run-cand", "cand-new", "profile-cand",
		[]string{"shell.read", "shell.execute", "shell.execute"})

	card, err := platform.NewEvaluationScorecard(reference.aggregate, candidate.aggregate,
		diffOf(t, reference, candidate))
	if err != nil {
		t.Fatalf("NewEvaluationScorecard() rejected evidence from real engines: %v", err)
	}

	t.Run("identity points at the two runs", func(t *testing.T) {
		if card.ReferenceRunID() != "run-ref" || card.CandidateRunID() != "run-cand" {
			t.Errorf("runs = %q / %q", card.ReferenceRunID(), card.CandidateRunID())
		}
		if card.Environment() != environment {
			t.Errorf("Environment() = %q, want %q", card.Environment(), environment)
		}
	})

	t.Run("observation counts agree across both evidence paths", func(t *testing.T) {
		if card.ReferenceRecordCount() != 3 || card.CandidateRecordCount() != 3 {
			t.Fatalf("record counts = %d / %d, want 3 / 3",
				card.ReferenceRecordCount(), card.CandidateRecordCount())
		}
	})

	t.Run("behavioral summary", func(t *testing.T) {
		b := card.Behavior()
		// shell.read shared; db.query removed; shell.execute added.
		if b.SharedCount != 1 || b.RemovedCount != 1 || b.AddedCount != 1 {
			t.Fatalf("shared/removed/added = %d/%d/%d, want 1/1/1",
				b.SharedCount, b.RemovedCount, b.AddedCount)
		}
		if b.ReferenceDistinctCount != 2 || b.CandidateDistinctCount != 2 {
			t.Errorf("distinct = %d/%d, want 2/2", b.ReferenceDistinctCount, b.CandidateDistinctCount)
		}
		if v, ok := b.PresenceOverlap(); !ok || v != 1.0/3.0 {
			t.Errorf("PresenceOverlap() = (%v, %v), want (1/3, true)", v, ok)
		}
	})

	t.Run("a decision distribution is present", func(t *testing.T) {
		d := card.Decisions()
		total := d.Allow.Candidate().Count() + d.ObserveOnly.Candidate().Count() +
			d.Alert.Candidate().Count() + d.Challenge.Candidate().Count() +
			d.RequireApproval.Candidate().Count() + d.Block.Candidate().Count()
		if total != 3 {
			t.Errorf("candidate decisions total %d, want 3", total)
		}
	})

	t.Run("a numeric comparison is present", func(t *testing.T) {
		trust := card.Metrics().TrustScore
		if trust.Reference().Count != 3 || trust.Candidate().Count != 3 {
			t.Fatalf("trust counts = %d/%d, want 3/3", trust.Reference().Count, trust.Candidate().Count)
		}
		if _, ok := trust.MeanDelta(); !ok {
			t.Error("MeanDelta() undefined with observations on both sides")
		}
		// The engine's own identity confidence, unaltered.
		if v, ok := card.Metrics().IdentityConfidence.Candidate().Mean(); !ok || math.Abs(v-0.9) > 1e-9 {
			t.Errorf("identity confidence mean = (%v, %v), want (0.9, true)", v, ok)
		}
	})
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

func benchEvidence(b *testing.B, runID platform.EvaluationRunID, candidateID platform.CandidateID,
	profile platform.BehavioralProfileRef, records, behaviors int,
) (platform.EvaluationAggregate, *platform.BehaviorCollector) {
	b.Helper()
	run, err := platform.NewEvaluationRun(runID, candidateID, testEnvironment, profile, aggEpoch)
	if err != nil {
		b.Fatalf("NewEvaluationRun() error = %v", err)
	}
	agg, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		b.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := platform.NewBehaviorCollector(run)
	if err != nil {
		b.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range records {
		n := i % max(behaviors, 1)
		rec := scorecardRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("%s-fp-%04d", profile, n),
			fmt.Sprintf("tool.%s.%d", profile, n), testEnvironment,
			"allow", "low", event.ApprovalNotRequired, 0.9)
		if agg, err = agg.AddRecord(rec); err != nil {
			b.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			b.Fatalf("Observe() error = %v", err)
		}
	}
	return agg, collector
}

func BenchmarkNewEvaluationScorecardTypical(b *testing.B) {
	refAgg, refCol := benchEvidence(b, "run-ref", "cand-ref", "pr", 2048, 512)
	candAgg, candCol := benchEvidence(b, "run-cand", "cand-new", "pc", 2048, 512)
	diff, err := platform.CompareBehaviorSnapshots(refCol.Snapshot(), candCol.Snapshot())
	if err != nil {
		b.Fatalf("CompareBehaviorSnapshots() error = %v", err)
	}
	if len(diff.Deltas()) != 1024 {
		b.Fatalf("premise broken: %d deltas, want the bounded maximum of 1024", len(diff.Deltas()))
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := platform.NewEvaluationScorecard(refAgg, candAgg, diff); err != nil {
			b.Fatalf("NewEvaluationScorecard() error = %v", err)
		}
	}
}

func BenchmarkNewEvaluationScorecardEmpty(b *testing.B) {
	refAgg, refCol := benchEvidence(b, "run-ref", "cand-ref", "pr", 0, 0)
	candAgg, candCol := benchEvidence(b, "run-cand", "cand-new", "pc", 0, 0)
	diff, err := platform.CompareBehaviorSnapshots(refCol.Snapshot(), candCol.Snapshot())
	if err != nil {
		b.Fatalf("CompareBehaviorSnapshots() error = %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := platform.NewEvaluationScorecard(refAgg, candAgg, diff); err != nil {
			b.Fatalf("NewEvaluationScorecard() error = %v", err)
		}
	}
}
