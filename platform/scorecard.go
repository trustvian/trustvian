package platform

// Evaluation scorecards: one fixed-shape comparison of two evaluations,
// composed from the bounded evidence tasks 053 and 054 already produce.
//
//	reference EvaluationAggregate ───────┐
//	BehaviorDiff(reference → candidate) ─┼──▶ EvaluationScorecard
//	candidate EvaluationAggregate ───────┘
//
// No third reducer. Records were consumed once, by two reducers designed
// against the same stream; re-reading them here would be a third ingestion
// path with a third chance to disagree with the other two.
//
// See docs/adr/0028-scorecards-are-fixed-shape-comparative-evidence.md.

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidScorecardEvidence reports evidence that is zero-valued,
	// unbound, or structurally impossible.
	ErrInvalidScorecardEvidence = errors.New("platform: invalid scorecard evidence")

	// ErrScorecardEvidenceMismatch reports individually valid inputs that
	// describe different comparisons.
	ErrScorecardEvidenceMismatch = errors.New("platform: scorecard evidence describes different evaluations")
)

// EvidenceRate is a count against the total it was drawn from.
//
// Value reports availability alongside the ratio, and reports unavailable when
// the total is zero. That is the whole reason this type exists rather than a
// bare float64: "nothing was observed" and "observed, never occurred" are
// different facts, and collapsing them into 0.0 always errs toward looking
// safe — an evaluation that ran nothing would show a zero block rate.
type EvidenceRate struct {
	count uint64
	total uint64
}

// Count is the number of observations in this category.
func (r EvidenceRate) Count() uint64 { return r.count }

// Total is the number of observations the count was drawn from.
func (r EvidenceRate) Total() uint64 { return r.total }

// Value returns count/total, and false when nothing was observed.
func (r EvidenceRate) Value() (float64, bool) {
	if r.total == 0 {
		return 0, false
	}
	return float64(r.count) / float64(r.total), true
}

// RateComparison is one category's rate on both sides of a comparison.
type RateComparison struct {
	reference EvidenceRate
	candidate EvidenceRate
}

// Reference and Candidate return each side's rate.
func (c RateComparison) Reference() EvidenceRate { return c.reference }
func (c RateComparison) Candidate() EvidenceRate { return c.candidate }

// Delta returns candidate rate minus reference rate, and false unless both
// rates are defined. A delta against an evaluation that observed nothing is
// not a movement, and reporting one would invent a baseline.
func (c RateComparison) Delta() (float64, bool) {
	ref, refOK := c.reference.Value()
	cand, candOK := c.candidate.Value()
	if !refOK || !candOK {
		return 0, false
	}
	return cand - ref, true
}

func compareRates(referenceCount, referenceTotal, candidateCount, candidateTotal uint64) RateComparison {
	return RateComparison{
		reference: EvidenceRate{count: referenceCount, total: referenceTotal},
		candidate: EvidenceRate{count: candidateCount, total: candidateTotal},
	}
}

// DecisionComparison is how the six policy decisions moved.
//
// An explicit struct, never map[string]RateComparison: the categories are
// closed and known, and a map would accept an unknown key, add cardinality,
// and weaken the schema.
//
// Names stay factual. Block is not "violation", Challenge is not "bad": a
// policy decision is evidence of what policy decided.
type DecisionComparison struct {
	Allow           RateComparison
	ObserveOnly     RateComparison
	Alert           RateComparison
	Challenge       RateComparison
	RequireApproval RateComparison
	Block           RateComparison
}

// RiskComparison is how the four risk levels moved.
//
// Critical risk is a risk reading. It is not a critical policy violation —
// those are different concepts, and the aggregate holds no evidence that
// could distinguish them.
type RiskComparison struct {
	Low      RateComparison
	Medium   RateComparison
	High     RateComparison
	Critical RateComparison
}

// ApprovalComparison is how approval evidence moved.
//
// Evidence counts, not compliance. ApprovalStatus is supplied by whoever
// produced the event: Denied records that a producer reported denial, and
// says nothing about whether an action was authorized. Interpreting it needs
// policy context that belongs to the gate task.
type ApprovalComparison struct {
	Unspecified RateComparison
	NotRequired RateComparison
	Required    RateComparison
	Approved    RateComparison
	Denied      RateComparison
}

// PolicySelectionComparison is how decisions were reached — by a matching
// rule, or by the policy default.
//
// Shape only. No rule names are retained anywhere in this chain, so severity
// could not be inferred even if inferring it were sound.
type PolicySelectionComparison struct {
	MatchedRule    RateComparison
	MatchedDefault RateComparison
}

// MetricComparison is one numeric signal's summary on both sides.
type MetricComparison struct {
	reference MetricSummary
	candidate MetricSummary
}

// Reference and Candidate return each side's summary.
func (m MetricComparison) Reference() MetricSummary { return m.reference }
func (m MetricComparison) Candidate() MetricSummary { return m.candidate }

// MeanDelta returns candidate mean minus reference mean, and false unless
// both means exist.
//
// Inherits task 053's floating-point semantics: deterministic for the same
// ordered stream, not guaranteed bit-identical under arbitrary reordering,
// because floating-point addition is not associative. One more reason a
// critical gate should rest on counts rather than on an average.
func (m MetricComparison) MeanDelta() (float64, bool) {
	ref, refOK := m.reference.Mean()
	cand, candOK := m.candidate.Mean()
	if !refOK || !candOK {
		return 0, false
	}
	return cand - ref, true
}

// MetricComparisons is the five numeric signals every record carries.
//
// TrustScore is **not** redefined here. Trust.Score remains an event-level
// engine signal with its own documented formula and compatibility promise;
// this compares aggregate summaries of it, under its own name.
type MetricComparisons struct {
	IdentityConfidence MetricComparison
	AnomalyScore       MetricComparison
	AnomalyConfidence  MetricComparison
	TrustScore         MetricComparison
	ContextRisk        MetricComparison
}

// BehaviorSummary is the behavioral half of the card: presence counts and the
// ratios derived from them.
//
// The diff's ≤1024 deltas are deliberately absent. A caller wanting
// per-behavior rows reads the BehaviorDiff it already holds; duplicating them
// would make this card's size and construction cost scale with behavioral
// cardinality for data already within reach.
type BehaviorSummary struct {
	ReferenceDistinctCount int
	CandidateDistinctCount int

	AddedCount   int
	RemovedCount int
	SharedCount  int
}

// AddedCandidateRate is the share of the candidate's distinct behaviors that
// the reference never observed. Undefined when the candidate observed none.
func (s BehaviorSummary) AddedCandidateRate() (float64, bool) {
	return ratio(s.AddedCount, s.CandidateDistinctCount)
}

// RemovedReferenceRate is the share of the reference's distinct behaviors the
// candidate did not observe. Undefined when the reference observed none.
func (s BehaviorSummary) RemovedReferenceRate() (float64, bool) {
	return ratio(s.RemovedCount, s.ReferenceDistinctCount)
}

// PresenceOverlap is shared behaviors over the union — Jaccard presence
// similarity. Undefined when neither evaluation observed any behavior.
//
// Named for what it mathematically is. Calling it a "behavioral stability
// score" would smuggle in the claim that more overlap is better, which is a
// question about the change rather than about the number — and would be the
// field callers read instead of the gate.
//
// Two empty evaluations are not behaviorally identical; they are unmeasured.
// Neither 1.0 nor 0.0 is true, so neither is returned.
func (s BehaviorSummary) PresenceOverlap() (float64, bool) {
	return ratio(s.SharedCount, s.AddedCount+s.RemovedCount+s.SharedCount)
}

func ratio(part, whole int) (float64, bool) {
	if whole <= 0 {
		return 0, false
	}
	return float64(part) / float64(whole), true
}

// EvaluationScorecard is a fixed-shape comparison of two evaluations.
//
// It reports how the decision, risk, approval and policy-selection
// distributions moved, how the five numeric signals moved, and how much
// behavioral presence overlapped.
//
// It reports no verdict. There is no OverallScore, Grade, Passed or
// Promotable, and no threshold — each would need policy semantics no current
// type carries, and a composite number would become the field callers read
// instead of the gate. The roadmap's own rule is the reason: a high average
// must not override a critical violation, and a weighted score is precisely
// the mechanism by which it could.
//
// Several metrics the roadmap names — critical policy violations, blocked or
// unapproved sensitive actions, per-rule compliance, delegation stability —
// are **absent rather than reported as zero**. A zero derived from evidence
// that cannot express the concept is a false security claim, and reads as a
// measurement that found nothing rather than one that never ran.
//
// Fixed-shape and O(1): no slice, map, pointer, interface, or retained input.
// Construction cost is independent of how many records were aggregated and
// how many behaviors were compared, because only summary accessors are read.
type EvaluationScorecard struct {
	// bound marks a card this package's constructor produced, so task 056
	// can fail closed on one it did not. Same guard, same reasoning as
	// EvaluationAggregate: unexported fields prevent mutation, not
	// EvaluationScorecard{}.
	bound bool

	referenceRunID     EvaluationRunID
	referenceCandidate CandidateID
	referenceProfile   BehavioralProfileRef
	referenceRecords   uint64
	candidateRunID     EvaluationRunID
	candidateCandidate CandidateID
	candidateProfile   BehavioralProfileRef
	candidateRecords   uint64
	environment        EnvironmentRef

	decisions DecisionComparison
	risks     RiskComparison
	approvals ApprovalComparison
	policy    PolicySelectionComparison
	metrics   MetricComparisons
	behavior  BehaviorSummary
}

// Comparison identity, so a card is self-describing.
func (s EvaluationScorecard) ReferenceRunID() EvaluationRunID   { return s.referenceRunID }
func (s EvaluationScorecard) ReferenceCandidateID() CandidateID { return s.referenceCandidate }
func (s EvaluationScorecard) ReferenceBehavioralProfile() BehavioralProfileRef {
	return s.referenceProfile
}
func (s EvaluationScorecard) CandidateRunID() EvaluationRunID   { return s.candidateRunID }
func (s EvaluationScorecard) CandidateCandidateID() CandidateID { return s.candidateCandidate }
func (s EvaluationScorecard) CandidateBehavioralProfile() BehavioralProfileRef {
	return s.candidateProfile
}

// Environment is the environment both evaluations ran in; a comparison across
// environments is refused.
func (s EvaluationScorecard) Environment() EnvironmentRef { return s.environment }

// Observation counts, which both evidence paths agreed on.
func (s EvaluationScorecard) ReferenceRecordCount() uint64 { return s.referenceRecords }
func (s EvaluationScorecard) CandidateRecordCount() uint64 { return s.candidateRecords }

// The comparison itself, returned by value.
func (s EvaluationScorecard) Decisions() DecisionComparison              { return s.decisions }
func (s EvaluationScorecard) Risks() RiskComparison                      { return s.risks }
func (s EvaluationScorecard) Approvals() ApprovalComparison              { return s.approvals }
func (s EvaluationScorecard) PolicySelection() PolicySelectionComparison { return s.policy }
func (s EvaluationScorecard) Metrics() MetricComparisons                 { return s.metrics }
func (s EvaluationScorecard) Behavior() BehaviorSummary                  { return s.behavior }

// NewEvaluationScorecard composes two aggregates and their behavioral diff
// into one comparison, or refuses.
//
// The direction is reference → candidate, matching CompareBehaviorSnapshots,
// and is not symmetric.
//
// Both aggregates are required rather than just the candidate's: the card's
// purpose is comparison, and "reference block rate → candidate block rate"
// cannot be expressed from one side. Equally the diff cannot be derived from
// the aggregates, nor they from it — the two inputs answer different
// questions, which is why both exist.
//
// Every compatibility check runs before anything is built. There is no
// partial card: evidence that does not describe one comparison is refused
// rather than combined.
func NewEvaluationScorecard(
	reference EvaluationAggregate,
	candidate EvaluationAggregate,
	diff BehaviorDiff,
) (EvaluationScorecard, error) {
	if err := validateAggregateEvidence("reference", reference); err != nil {
		return EvaluationScorecard{}, err
	}
	if err := validateAggregateEvidence("candidate", candidate); err != nil {
		return EvaluationScorecard{}, err
	}
	if err := validateDiffEvidence(diff); err != nil {
		return EvaluationScorecard{}, err
	}
	if err := assertSameComparison(reference, candidate, diff); err != nil {
		return EvaluationScorecard{}, err
	}

	refTotal, candTotal := reference.RecordCount(), candidate.RecordCount()
	refDec, candDec := reference.Decisions(), candidate.Decisions()
	refRisk, candRisk := reference.Risks(), candidate.Risks()
	refApp, candApp := reference.Approvals(), candidate.Approvals()
	refPol, candPol := reference.PolicySelection(), candidate.PolicySelection()

	return EvaluationScorecard{
		bound: true,

		referenceRunID:     reference.RunID(),
		referenceCandidate: reference.CandidateID(),
		referenceProfile:   reference.BehavioralProfile(),
		referenceRecords:   refTotal,
		candidateRunID:     candidate.RunID(),
		candidateCandidate: candidate.CandidateID(),
		candidateProfile:   candidate.BehavioralProfile(),
		candidateRecords:   candTotal,
		environment:        reference.Environment(),

		decisions: DecisionComparison{
			Allow:           compareRates(refDec.Allow, refTotal, candDec.Allow, candTotal),
			ObserveOnly:     compareRates(refDec.ObserveOnly, refTotal, candDec.ObserveOnly, candTotal),
			Alert:           compareRates(refDec.Alert, refTotal, candDec.Alert, candTotal),
			Challenge:       compareRates(refDec.Challenge, refTotal, candDec.Challenge, candTotal),
			RequireApproval: compareRates(refDec.RequireApproval, refTotal, candDec.RequireApproval, candTotal),
			Block:           compareRates(refDec.Block, refTotal, candDec.Block, candTotal),
		},
		risks: RiskComparison{
			Low:      compareRates(refRisk.Low, refTotal, candRisk.Low, candTotal),
			Medium:   compareRates(refRisk.Medium, refTotal, candRisk.Medium, candTotal),
			High:     compareRates(refRisk.High, refTotal, candRisk.High, candTotal),
			Critical: compareRates(refRisk.Critical, refTotal, candRisk.Critical, candTotal),
		},
		approvals: ApprovalComparison{
			Unspecified: compareRates(refApp.Unspecified, refTotal, candApp.Unspecified, candTotal),
			NotRequired: compareRates(refApp.NotRequired, refTotal, candApp.NotRequired, candTotal),
			Required:    compareRates(refApp.Required, refTotal, candApp.Required, candTotal),
			Approved:    compareRates(refApp.Approved, refTotal, candApp.Approved, candTotal),
			Denied:      compareRates(refApp.Denied, refTotal, candApp.Denied, candTotal),
		},
		policy: PolicySelectionComparison{
			MatchedRule:    compareRates(refPol.MatchedRule, refTotal, candPol.MatchedRule, candTotal),
			MatchedDefault: compareRates(refPol.MatchedDefault, refTotal, candPol.MatchedDefault, candTotal),
		},
		metrics: MetricComparisons{
			IdentityConfidence: MetricComparison{reference.IdentityConfidence(), candidate.IdentityConfidence()},
			AnomalyScore:       MetricComparison{reference.AnomalyScore(), candidate.AnomalyScore()},
			AnomalyConfidence:  MetricComparison{reference.AnomalyConfidence(), candidate.AnomalyConfidence()},
			TrustScore:         MetricComparison{reference.TrustScore(), candidate.TrustScore()},
			ContextRisk:        MetricComparison{reference.ContextRisk(), candidate.ContextRisk()},
		},
		behavior: BehaviorSummary{
			ReferenceDistinctCount: diff.ReferenceDistinctCount(),
			CandidateDistinctCount: diff.CandidateDistinctCount(),
			AddedCount:             diff.AddedCount(),
			RemovedCount:           diff.RemovedCount(),
			SharedCount:            diff.SharedCount(),
		},
	}, nil
}

// validateAggregateEvidence rejects an aggregate that cannot be evidence.
//
// The bound check is the important one: a zero-value EvaluationAggregate is
// constructible from any package, and all of its counts are zero — which is
// indistinguishable by value from a valid empty evaluation, and completely
// different in meaning.
//
// The arithmetic checks below are not a re-validation of records; those no
// longer exist here. They are identities task 053 guarantees, verified
// because a scorecard is a trust boundary *between* evidence components even
// when each component is internally sound.
func validateAggregateEvidence(side string, a EvaluationAggregate) error {
	if !a.bound {
		return fmt.Errorf("%w: %s aggregate did not come from NewEvaluationAggregate",
			ErrInvalidScorecardEvidence, side)
	}

	total := a.RecordCount()
	for _, check := range [...]struct {
		name  string
		total uint64
	}{
		{"decision", a.Decisions().Total()},
		{"risk", a.Risks().Total()},
		{"approval", a.Approvals().Total()},
		{"policy selection", a.PolicySelection().Total()},
	} {
		if check.total != total {
			return fmt.Errorf("%w: %s aggregate has %d records but %d %s counts",
				ErrInvalidScorecardEvidence, side, total, check.total, check.name)
		}
	}

	for _, m := range [...]struct {
		name    string
		summary MetricSummary
	}{
		{"identity_confidence", a.IdentityConfidence()},
		{"anomaly_score", a.AnomalyScore()},
		{"anomaly_confidence", a.AnomalyConfidence()},
		{"trust_score", a.TrustScore()},
		{"context_risk", a.ContextRisk()},
	} {
		if m.summary.Count != total {
			return fmt.Errorf("%w: %s aggregate has %d records but %d %s observations",
				ErrInvalidScorecardEvidence, side, total, m.summary.Count, m.name)
		}
	}
	return nil
}

// validateDiffEvidence rejects a BehaviorDiff that cannot be evidence.
//
// BehaviorDiff carries no bound marker and does not need one. Every diff
// CompareBehaviorSnapshots produces has non-empty run, candidate, profile and
// environment identifiers, because they originate in an EvaluationRun that
// validateRunBinding already checked. A zero value has none of them, which is
// a reliable discriminator — and means task 054's shape is not modified for
// this task's convenience.
func validateDiffEvidence(d BehaviorDiff) error {
	for _, field := range [...]struct{ name, value string }{
		{"reference run id", string(d.ReferenceRunID())},
		{"reference candidate id", string(d.ReferenceCandidateID())},
		{"reference behavioral profile", string(d.ReferenceBehavioralProfile())},
		{"candidate run id", string(d.CandidateRunID())},
		{"candidate candidate id", string(d.CandidateCandidateID())},
		{"candidate behavioral profile", string(d.CandidateBehavioralProfile())},
		{"environment", string(d.Environment())},
	} {
		if field.value == "" {
			return fmt.Errorf("%w: behavioral diff has no %s; it did not come from CompareBehaviorSnapshots",
				ErrInvalidScorecardEvidence, field.name)
		}
	}
	return nil
}

// assertSameComparison verifies that three individually valid pieces of
// evidence describe one comparison.
//
// Each aggregate is matched against its *own* side of the diff. The two sides
// need not match each other: comparing two candidates under two learning
// scopes is the normal case, and task 051 kept scope out of behavioral
// identity precisely so that works.
func assertSameComparison(reference, candidate EvaluationAggregate, diff BehaviorDiff) error {
	for _, check := range [...]struct {
		what           string
		aggregate, own string
	}{
		{"reference run id", string(reference.RunID()), string(diff.ReferenceRunID())},
		{"reference candidate id", string(reference.CandidateID()), string(diff.ReferenceCandidateID())},
		{"reference behavioral profile", string(reference.BehavioralProfile()), string(diff.ReferenceBehavioralProfile())},
		{"candidate run id", string(candidate.RunID()), string(diff.CandidateRunID())},
		{"candidate candidate id", string(candidate.CandidateID()), string(diff.CandidateCandidateID())},
		{"candidate behavioral profile", string(candidate.BehavioralProfile()), string(diff.CandidateBehavioralProfile())},
	} {
		if check.aggregate != check.own {
			return fmt.Errorf("%w: aggregate %s is %s, the diff says %s",
				ErrScorecardEvidenceMismatch, check.what,
				preview(check.aggregate), preview(check.own))
		}
	}

	if reference.Environment() != candidate.Environment() || reference.Environment() != diff.Environment() {
		return fmt.Errorf("%w: environments are %s (reference), %s (candidate) and %s (diff)",
			ErrScorecardEvidenceMismatch,
			preview(string(reference.Environment())), preview(string(candidate.Environment())),
			preview(string(diff.Environment())))
	}

	// The check that catches two partial views of one evaluation. Tasks 053
	// and 054 consume the same stream, so a disagreement means one evidence
	// path saw records the other did not — and a card built from both would
	// put a decision distribution over N events beside a behavioral summary
	// over M. There is no correct way to reconcile that.
	if reference.RecordCount() != diff.ReferenceObservationCount() {
		return fmt.Errorf("%w: reference aggregate saw %d records, the behavioral diff saw %d",
			ErrScorecardEvidenceMismatch, reference.RecordCount(), diff.ReferenceObservationCount())
	}
	if candidate.RecordCount() != diff.CandidateObservationCount() {
		return fmt.Errorf("%w: candidate aggregate saw %d records, the behavioral diff saw %d",
			ErrScorecardEvidenceMismatch, candidate.RecordCount(), diff.CandidateObservationCount())
	}
	return nil
}
