package platform

// Evidence resolution: from a finding to the observations behind it (task 085).
//
// The gate reports `added_behaviors actual 3 maximum 0 FAIL` and nothing could
// answer *which three*. BehaviorDelta holds no reference to an observation, and
// EvaluationGateResult is deliberately closed — five named integer checks and a
// verdict, with no slice, map or pointer (ADR 0028, ADR 0029).
//
// **This does not reopen that shape.** Resolution is a query against
// authoritative state, not a payload carried inside a verdict: a gate result is
// unchanged, and a resolution is asked for separately by naming the comparison
// and the finding inside it.
//
// Nothing here recomputes. Behavioral identities come from the same
// CompareBehaviorSnapshots call over the same persisted snapshots that
// CompareEvaluations uses, recorded counts come from the persisted aggregate,
// and observations are returned as they were written.
//
// See docs/tasks/v1.0/085-evidence-resolution.md.

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrInvalidFinding reports a finding reference that does not name something
// the comparison contains.
//
// Distinct from ErrStoreNotFound, which means the run itself is missing: a
// well-formed reference to a behavior this comparison never saw is a caller
// error about the *finding*, and collapsing the two would make a typo in a
// fingerprint indistinguishable from a deleted run.
var ErrInvalidFinding = errors.New("platform: invalid finding reference")

// GateCheckName is one of the gate's five named checks, spelled as the wire
// spells it so a developer can paste what the gate printed.
type GateCheckName string

const (
	CheckReferenceEvidence        GateCheckName = "reference_evidence"
	CheckCandidateEvidence        GateCheckName = "candidate_evidence"
	CheckAddedBehaviors           GateCheckName = "added_behaviors"
	CheckBlockDecisions           GateCheckName = "block_decisions"
	CheckCriticalRiskObservations GateCheckName = "critical_risk_observations"
)

// Valid reports whether the name is one the gate publishes.
func (c GateCheckName) Valid() bool {
	switch c {
	case CheckReferenceEvidence, CheckCandidateEvidence, CheckAddedBehaviors,
		CheckBlockDecisions, CheckCriticalRiskObservations:
		return true
	default:
		return false
	}
}

// ComparisonSide names which run's evidence a resolution is about.
//
// Reference and candidate evidence stay distinct at every layer. A resolution
// that did not say which side it read would be describing one run's behavior
// with another run's observations, and nothing in the values would reveal it.
type ComparisonSide string

const (
	SideReference ComparisonSide = "reference"
	SideCandidate ComparisonSide = "candidate"
)

// Valid reports whether the side is one of the two.
func (s ComparisonSide) Valid() bool {
	return s == SideReference || s == SideCandidate
}

// FindingRef identifies one finding inside one comparison.
//
// Built entirely from values that are already durable and caller-owned — two
// run identifiers, and either a gate check name or a fingerprint — so **the
// reference is stable by construction**. There is no minted identity, no
// finding table and nothing to migrate: the same reference resolves to the same
// finding later, or reports that the evidence is gone.
type FindingRef struct {
	ReferenceRunID EvaluationRunID
	CandidateRunID EvaluationRunID

	// Exactly one of Check and Behavior is set.
	Check    GateCheckName
	Behavior string

	// Side is optional and only meaningful with Behavior. Empty means "derive
	// it from the delta's presence"; a stated side that contradicts the
	// presence is refused rather than honoured.
	Side ComparisonSide
}

// validate checks the reference's shape, before anything is loaded.
func (f FindingRef) validate() error {
	if err := validateID("reference run id", string(f.ReferenceRunID)); err != nil {
		return err
	}
	if err := validateID("candidate run id", string(f.CandidateRunID)); err != nil {
		return err
	}
	switch {
	case f.Check != "" && f.Behavior != "":
		return fmt.Errorf(
			"%w: a finding names a gate check or a behavior, never both", ErrInvalidFinding)
	case f.Check == "" && f.Behavior == "":
		return fmt.Errorf(
			"%w: a finding must name a gate check or a behavior", ErrInvalidFinding)
	case f.Check != "" && !f.Check.Valid():
		return fmt.Errorf("%w: %q is not a gate check this platform publishes",
			ErrInvalidFinding, preview(string(f.Check)))
	}
	if f.Behavior != "" {
		if err := validateID("finding behavior", f.Behavior); err != nil {
			return err
		}
	}
	if f.Side != "" {
		if !f.Side.Valid() {
			return fmt.Errorf("%w: %q is not a comparison side",
				ErrInvalidFinding, preview(string(f.Side)))
		}
		if f.Check != "" {
			return fmt.Errorf(
				"%w: a gate check counts one side already; side is only meaningful "+
					"with a behavior", ErrInvalidFinding)
		}
	}
	return nil
}

// ResolutionStatus is what a resolution found, and it has four values because
// three of them look identical in the payload.
//
// **It describes the finding, not the page.** Paging past the last match
// returns an empty page whose status is still `resolved`, because the evidence
// exists — the caller has simply read all of it. Deriving the status from the
// page length instead made a continuation request report that a finding with
// plenty of evidence had none, which is the opposite of the truth and arrives
// exactly when a developer has finished reading it.
//
// Page exhaustion is reported by the continuation cursor being absent, which is
// the same signal every other collection in this API uses.
type ResolutionStatus string

const (
	// ResolutionResolved means the finding has supporting evidence — somewhere
	// in its retained history, not necessarily on this page.
	ResolutionResolved ResolutionStatus = "resolved"

	// ResolutionNoneFound means no retained observation matches the finding at
	// all — and the retained history is complete, so that is a fact about the
	// run rather than about storage or about where the cursor happened to be.
	ResolutionNoneFound ResolutionStatus = "none_found"

	// ResolutionIndeterminate means nothing matches and the history is partial
	// or unavailable, so the absence establishes nothing.
	//
	// This value is why the status exists. A zero-row page from a saturated run
	// and a zero-row page from a run that genuinely did nothing are the same
	// bytes, and reporting both as "none" turns missing evidence into evidence
	// of absence — the error ADR 0029 refuses one layer up, and a worse one
	// here because a developer uses this to decide whether a regression is real.
	ResolutionIndeterminate ResolutionStatus = "indeterminate"

	// ResolutionAggregateOnly means the check counts something no observation
	// can be attributed to, so there is nothing to link rather than nothing
	// found.
	ResolutionAggregateOnly ResolutionStatus = "aggregate_only"
)

// BehaviorResolution answers "which behavioral identities contributed to this".
type BehaviorResolution struct {
	Finding FindingRef
	Status  ResolutionStatus

	// Side is empty when Status is aggregate_only: the check belongs to no
	// side's observations.
	Side ComparisonSide

	// RecordedCount is the number the recorded evidence holds — the gate's own
	// actual, read from the aggregate and the diff rather than derived from
	// anything returned here.
	RecordedCount uint64

	Behaviors []BehaviorDelta

	// History describes Side's retained observations, so a caller knows what
	// the next step can and cannot establish before it takes it.
	History ObservationHistory
}

// ObservationResolution answers "which retained observations support this".
type ObservationResolution struct {
	Finding FindingRef
	Status  ResolutionStatus
	Side    ComparisonSide

	// RecordedCount is what the evidence holds. It is **not** a count of
	// Observations and must not be reconciled with one: block and critical-risk
	// checks count every record the run ingested, while retained history is
	// bounded at MaxRetainedObservations and may be partial or absent. A
	// mismatch is bounded retention describing itself.
	RecordedCount uint64

	// Exhaustive reports whether paging this resolution to its end would yield
	// every matching observation that ever existed.
	//
	// True only when the side's retained history is complete. A full set of
	// matches drawn from partial history is still a sample, and a caller told
	// it was exhaustive would conclude the matches are all of them.
	Exhaustive bool

	Observations []Observation
	History      ObservationHistory
}

// ---------------------------------------------------------------------
// Resolution
// ---------------------------------------------------------------------

// findingContext is the comparison a finding is resolved against.
type findingContext struct {
	reference, candidate       EvaluationRun
	referenceAgg, candidateAgg EvaluationAggregate
	diff                       BehaviorDiff
}

// resolveFindingContext loads and validates the comparison behind a finding.
//
// The comparison's own preconditions, reused rather than restated: both runs
// exist, both are completed, both belong to one project, and a saturated
// snapshot refuses the comparison (task 054) and therefore refuses a resolution
// over it. A resolution that accepted evidence the comparison rejects would be
// explaining a verdict that was never produced.
func (c *ControlPlane) resolveFindingContext(
	ctx context.Context, finding FindingRef,
) (findingContext, error) {
	reference, err := c.completedRun(ctx, "reference", finding.ReferenceRunID)
	if err != nil {
		return findingContext{}, err
	}
	candidate, err := c.completedRun(ctx, "candidate", finding.CandidateRunID)
	if err != nil {
		return findingContext{}, err
	}
	if err := c.requireSameProject(ctx, reference, candidate); err != nil {
		return findingContext{}, err
	}

	referenceAgg, referenceSnapshot, err := c.comparisonEvidence(ctx, reference)
	if err != nil {
		return findingContext{}, err
	}
	candidateAgg, candidateSnapshot, err := c.comparisonEvidence(ctx, candidate)
	if err != nil {
		return findingContext{}, err
	}

	// The same function over the same inputs CompareEvaluations uses, so a
	// resolution and a comparison cannot disagree about what changed.
	diff, err := CompareBehaviorSnapshots(referenceSnapshot, candidateSnapshot)
	if err != nil {
		return findingContext{}, err
	}

	return findingContext{
		reference: reference, candidate: candidate,
		referenceAgg: referenceAgg, candidateAgg: candidateAgg,
		diff: diff,
	}, nil
}

// runFor returns the run on one side.
func (f findingContext) runFor(side ComparisonSide) EvaluationRun {
	if side == SideReference {
		return f.reference
	}
	return f.candidate
}

// delta finds one behavioral identity in the diff.
func (f findingContext) delta(fingerprintID string) (BehaviorDelta, bool) {
	for _, d := range f.diff.Deltas() {
		if d.FingerprintID == fingerprintID {
			return d, true
		}
	}
	return BehaviorDelta{}, false
}

// checkPlan is how one gate check maps onto retained evidence.
type checkPlan struct {
	side          ComparisonSide
	recordedCount uint64

	// aggregateOnly marks a check whose failure is an absence: there is no
	// observation that caused it, so none is invented.
	aggregateOnly bool

	// filter selects the supporting observations, empty for a check whose
	// supporting set is per-behavior.
	filter ObservationFilter

	// requiresBehavior marks a check resolved one behavioral identity at a
	// time, because its contributing set is a set of behaviors rather than a
	// single predicate.
	requiresBehavior bool
}

// planForCheck maps a gate check onto the evidence that supports it.
//
// The table is the task's central product decision, stated once:
//
//   - added_behaviors counts behaviors, so it resolves to behaviors first and
//     to observations one behavior at a time. A predicate over up to 512
//     fingerprint digests is not a bounded query.
//   - block_decisions and critical_risk_observations count candidate records
//     carrying one recorded value, which is exactly one equality predicate.
//   - reference_evidence and candidate_evidence are minimum-count checks: they
//     fail when a run observed *too little*, and the evidence for that is an
//     absence. Returning every observation in the run would answer a different
//     question confidently, so they report aggregate_only and stop.
func planForCheck(check GateCheckName, ctx findingContext) (checkPlan, error) {
	switch check {
	case CheckAddedBehaviors:
		return checkPlan{
			side:             SideCandidate,
			recordedCount:    uint64(ctx.diff.AddedCount()),
			requiresBehavior: true,
		}, nil

	case CheckBlockDecisions:
		return checkPlan{
			side:          SideCandidate,
			recordedCount: ctx.candidateAgg.Decisions().Block,
			filter:        ObservationFilter{Decision: decisionBlock},
		}, nil

	case CheckCriticalRiskObservations:
		return checkPlan{
			side:          SideCandidate,
			recordedCount: ctx.candidateAgg.Risks().Critical,
			filter:        ObservationFilter{RiskLevel: riskCritical},
		}, nil

	case CheckReferenceEvidence:
		return checkPlan{
			aggregateOnly: true,
			recordedCount: ctx.referenceAgg.RecordCount(),
		}, nil

	case CheckCandidateEvidence:
		return checkPlan{
			aggregateOnly: true,
			recordedCount: ctx.candidateAgg.RecordCount(),
		}, nil

	default:
		return checkPlan{}, fmt.Errorf("%w: %q is not a gate check this platform publishes",
			ErrInvalidFinding, preview(string(check)))
	}
}

// sideForDelta picks which run's observations a behavioral delta is about.
//
// Presence decides it where presence *can*: a behavior the candidate added
// exists only there, and one it removed exists only in the reference. So an
// omitted side is answerable for those two, and answering it saves a developer
// restating something the diff already knows.
//
// **A shared behavior has no natural side and must name one.** Both runs
// contain it, both hold their own observations of it, and the two are exactly
// what a developer is comparing. Defaulting to the candidate would answer a
// question nobody asked, silently, and look identical to the answer they
// wanted — the reference-side rows would simply never appear. Refusing costs
// one flag and cannot mislead.
//
// A stated side that contradicts an added or removed presence is refused rather
// than honoured, because returning the other run's rows is the failure this
// whole task exists to prevent.
func sideForDelta(delta BehaviorDelta, requested ComparisonSide) (ComparisonSide, error) {
	if requested == "" {
		switch delta.Presence {
		case BehaviorAdded:
			return SideCandidate, nil
		case BehaviorRemoved:
			return SideReference, nil
		default:
			return "", fmt.Errorf(
				"%w: behavior %s is present in both runs; name the side to resolve "+
					"— reference or candidate", ErrInvalidFinding,
				preview(delta.FingerprintID))
		}
	}

	switch delta.Presence {
	case BehaviorAdded:
		if requested != SideCandidate {
			return "", fmt.Errorf(
				"%w: behavior %s was added by the candidate and has no reference-run "+
					"observations", ErrInvalidFinding, preview(delta.FingerprintID))
		}
	case BehaviorRemoved:
		if requested != SideReference {
			return "", fmt.Errorf(
				"%w: behavior %s was removed and has no candidate-run observations",
				ErrInvalidFinding, preview(delta.FingerprintID))
		}
	}
	return requested, nil
}

// ResolveFindingBehaviors returns the behavioral identities that contributed to
// a finding, as one bounded page.
//
// Paged by fingerprint id in byte order, which is the order
// BehaviorSnapshot.Entries and BehaviorDiff.Deltas already guarantee — so the
// cursor is a stable key rather than a position.
func (c *ControlPlane) ResolveFindingBehaviors(
	ctx context.Context, finding FindingRef, after string, limit int,
) (BehaviorResolution, error) {
	if err := finding.validate(); err != nil {
		return BehaviorResolution{}, err
	}
	if err := validateListPage("finding behavior", after, limit); err != nil {
		return BehaviorResolution{}, err
	}

	context, err := c.resolveFindingContext(ctx, finding)
	if err != nil {
		return BehaviorResolution{}, err
	}

	var (
		side       ComparisonSide
		recorded   uint64
		candidates []BehaviorDelta
	)

	if finding.Behavior != "" {
		delta, ok := context.delta(finding.Behavior)
		if !ok {
			return BehaviorResolution{}, fmt.Errorf(
				"%w: this comparison holds no behavior %s",
				ErrInvalidFinding, preview(finding.Behavior))
		}
		side, err = sideForDelta(delta, finding.Side)
		if err != nil {
			return BehaviorResolution{}, err
		}
		recorded = deltaCountFor(delta, side)
		candidates = []BehaviorDelta{delta}
	} else {
		plan, err := planForCheck(finding.Check, context)
		if err != nil {
			return BehaviorResolution{}, err
		}
		if plan.aggregateOnly {
			return BehaviorResolution{
				Finding:       finding,
				Status:        ResolutionAggregateOnly,
				RecordedCount: plan.recordedCount,
			}, nil
		}
		side = plan.side
		recorded = plan.recordedCount

		if finding.Check != CheckAddedBehaviors {
			// A count of records, not of behaviors. Saying "no contributing
			// behaviors" would read as a fact about the run rather than about
			// the question, so it is refused with the route that answers it.
			return BehaviorResolution{}, fmt.Errorf(
				"%w: %s counts observations rather than behaviors; resolve it "+
					"through the observation route", ErrInvalidFinding, finding.Check)
		}
		candidates = addedDeltas(context.diff)
	}

	history, err := c.sideHistory(ctx, context, side)
	if err != nil {
		return BehaviorResolution{}, err
	}

	// From the whole contributing set, not from the page: asking for the page
	// after the last fingerprint must not report that the check contributed
	// nothing. The set is the diff's own, so no second query is needed and no
	// snapshot question arises.
	page := pageDeltas(candidates, after, limit)
	status := ResolutionNoneFound
	if len(candidates) > 0 {
		status = ResolutionResolved
	}

	return BehaviorResolution{
		Finding:       finding,
		Status:        status,
		Side:          side,
		RecordedCount: recorded,
		Behaviors:     page,
		History:       history,
	}, nil
}

// ResolveFindingObservations returns the retained observations supporting a
// finding, as one bounded page.
func (c *ControlPlane) ResolveFindingObservations(
	ctx context.Context, finding FindingRef, after string, limit int,
) (ObservationResolution, error) {
	if err := finding.validate(); err != nil {
		return ObservationResolution{}, err
	}
	if err := validateObservationPage(after, limit); err != nil {
		return ObservationResolution{}, err
	}
	cursor, err := ParseObservationCursor(after)
	if err != nil {
		return ObservationResolution{}, err
	}

	context, err := c.resolveFindingContext(ctx, finding)
	if err != nil {
		return ObservationResolution{}, err
	}

	var (
		side     ComparisonSide
		recorded uint64
		filter   ObservationFilter
	)

	if finding.Behavior != "" {
		delta, ok := context.delta(finding.Behavior)
		if !ok {
			return ObservationResolution{}, fmt.Errorf(
				"%w: this comparison holds no behavior %s",
				ErrInvalidFinding, preview(finding.Behavior))
		}
		side, err = sideForDelta(delta, finding.Side)
		if err != nil {
			return ObservationResolution{}, err
		}
		recorded = deltaCountFor(delta, side)
		filter = ObservationFilter{FingerprintID: delta.FingerprintID}
	} else {
		plan, err := planForCheck(finding.Check, context)
		if err != nil {
			return ObservationResolution{}, err
		}
		if plan.aggregateOnly {
			return ObservationResolution{
				Finding:       finding,
				Status:        ResolutionAggregateOnly,
				RecordedCount: plan.recordedCount,
			}, nil
		}
		if plan.requiresBehavior {
			return ObservationResolution{}, fmt.Errorf(
				"%w: %s counts behaviors; name one with a behavior reference to "+
					"resolve its observations", ErrInvalidFinding, finding.Check)
		}
		side = plan.side
		recorded = plan.recordedCount
		filter = plan.filter
	}

	store, ok := c.ingest.(ObservationStore)
	if !ok {
		// A backend that retains no history. The honest answer is the same one a
		// run predating retention gets, never an empty "nothing happened".
		return ObservationResolution{
			Finding: finding, Status: ResolutionIndeterminate,
			Side: side, RecordedCount: recorded,
		}, nil
	}

	runID := context.runFor(side).ID()
	page, err := store.FindObservations(ctx, runID, filter, cursor, limit)
	if err != nil {
		return ObservationResolution{}, err
	}

	return ObservationResolution{
		Finding:       finding,
		Status:        observationStatus(page.Matched, page.History),
		Side:          side,
		RecordedCount: recorded,
		Exhaustive:    page.History.Complete(),
		Observations:  page.Observations,
		History:       page.History,
	}, nil
}

// observationStatus separates "nothing happened" from "nothing is known", and
// both from "you have read it all".
//
// matched is whether the finding has **any** retained supporting observation,
// asked of storage independently of the cursor — never the length of the page
// and never derived from RecordedCount, which counts records the run ingested
// and may include observations retention never kept.
func observationStatus(matched bool, history ObservationHistory) ResolutionStatus {
	switch {
	case matched:
		return ResolutionResolved
	case history.Complete():
		return ResolutionNoneFound
	default:
		return ResolutionIndeterminate
	}
}

// sideHistory reads one side's retained-history state.
func (c *ControlPlane) sideHistory(
	ctx context.Context, context findingContext, side ComparisonSide,
) (ObservationHistory, error) {
	store, ok := c.ingest.(ObservationStore)
	if !ok {
		return ObservationHistory{}, nil
	}
	// One row is enough to learn the history state, and the page travels with
	// it in one snapshot, so this costs a bounded read rather than a scan.
	page, err := store.RunObservations(ctx, context.runFor(side).ID(), 0, 1)
	if err != nil {
		return ObservationHistory{}, err
	}
	return page.History, nil
}

// deltaCountFor reports the recorded observation count on one side of a delta.
func deltaCountFor(delta BehaviorDelta, side ComparisonSide) uint64 {
	if side == SideReference {
		return delta.ReferenceCount
	}
	return delta.CandidateCount
}

// addedDeltas returns the deltas the added-behavior count counts.
//
// Filtered from the diff rather than recounted: AddedCount and this set are the
// same fact, and deriving one from the other separately is how they would come
// to disagree.
func addedDeltas(diff BehaviorDiff) []BehaviorDelta {
	out := make([]BehaviorDelta, 0, diff.AddedCount())
	for _, d := range diff.Deltas() {
		if d.Presence == BehaviorAdded {
			out = append(out, d)
		}
	}
	return out
}

// pageDeltas applies the established cursor rules to an ordered delta set.
//
// Byte order on FingerprintID, exclusive `after`, which is the same traversal
// EvaluationRunBehaviors uses over the same ordering — so a cursor names a row
// rather than a position and a page boundary does not move.
func pageDeltas(deltas []BehaviorDelta, after string, limit int) []BehaviorDelta {
	start := 0
	if after != "" {
		start = sort.Search(len(deltas), func(i int) bool {
			return deltas[i].FingerprintID > after
		})
	}
	end := min(start+limit, len(deltas))
	if start >= len(deltas) {
		return nil
	}
	return deltas[start:end]
}
