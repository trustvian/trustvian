package platform

import (
	"cmp"
	"fmt"
	"slices"
)

// Correlation-aware counting of behavioral change (task 083, ADR 0052).
//
// The defect this closes: a developer adds one tool, the tool span and its
// transport child are two behavioral identities, and a comparison reports
// `added 2`. ADR 0047 settled that the two identities stay two — the transport
// target is the security-relevant part, and folding identity would hide a tool
// that started posting somewhere else. So the correction belongs in counting,
// which is here.
//
// The rule, in one sentence: **a counted behavioral change is an added
// behavioral identity that is not the recorded child of another added
// behavioral identity of the same run.**
//
// Two properties of that rule matter more than the mechanism:
//
//  1. It is structural. It asks only "who is whose recorded parent", never
//     what instrumentation layer an observation came from — which is what
//     makes it evaluable from durable evidence, since the layer is not
//     persisted.
//
//  2. **Folding only ever lowers the count.** Every case this file cannot
//     resolve — a missing parent, an ambiguous span reference, a cycle,
//     incomplete history — falls back to not folding, which yields the added
//     identity count this platform has always reported. An unresolved
//     correlation can make a comparison stricter than it needed to be; it
//     cannot make one pass that should have failed.

// CountingPolicyVersion identifies the rule that produced a change count.
//
// Reported on every comparison so a stored result carries the policy it was
// computed under. A future rule increments this rather than silently
// reinterpreting results already recorded.
const CountingPolicyVersion = "1"

// CorrelationState says how much of a run's recorded parentage was available
// to the fold, and therefore how much weight the change count carries.
//
// It is the retained history's state, not a second notion: correlation is
// derived from task 067's per-observation history, so that history's bound is
// the correlation bound and its completeness is the correlation completeness.
// A second bound with its own saturation rule would be another thing to reason
// about that said the same thing less well.
type CorrelationState string

const (
	// CorrelationUnavailable means no parentage was available: the run was
	// ingested before schema 7 retained per-observation history. The change
	// count equals the added identity count.
	CorrelationUnavailable CorrelationState = "unavailable"

	// CorrelationComplete means every accepted record of both runs is
	// retained, so every recorded edge was available. The change count is
	// folded.
	CorrelationComplete CorrelationState = "complete"

	// CorrelationPartial means some parentage was missing — retention
	// saturated at MaxRetainedObservations, the run straddles schema 7, or
	// the recorded references were ambiguous or cyclic. The change count
	// equals the added identity count.
	//
	// One value for several causes, the way ObservationHistoryPartial is:
	// no consumer can act on the difference, and a state nobody can act on
	// is one somebody eventually reads as "complete enough".
	CorrelationPartial CorrelationState = "partial"
)

// String renders the state for a wire payload and a diagnostic.
func (s CorrelationState) String() string {
	if s == "" {
		return string(CorrelationUnavailable)
	}
	return string(s)
}

// folded reports whether this state permitted the fold.
//
// Only complete does. Both other states mean the count is the unfolded
// identity count, which is the conservative direction.
func (s CorrelationState) folded() bool { return s == CorrelationComplete }

// BehaviorChange is one counted behavioral change: a root added identity and
// every added identity that contributed to it.
//
// The root is the added identity with no added parent. The contributors are
// the root and the added identities recorded as its descendants — for the
// motivating case, the tool and the transport child it called.
//
// Contributors may appear under more than one change. Two new tools that both
// call one new transport identity are two changes, and the shared child
// contributes to each: overlapping contribution is honest, and merging the two
// into one change would report one where a developer made two.
type BehaviorChange struct {
	// RootFingerprintID is the added identity this change is anchored on.
	RootFingerprintID string

	// ContributingFingerprintIDs are the added identities this change covers,
	// sorted, always including the root. Each resolves to its observations
	// through task 085's existing evidence routes.
	ContributingFingerprintIDs []string
}

// spanRef is the only key a parent reference resolves through.
//
// The pair, never the span id alone: task 084 states that ParentSpanID is
// trace-scoped and is not globally unique. Keying on the span id by itself
// would let a parent reference in one trace resolve to an unrelated span in
// another, which is precisely the inference ADR 0052 forbids.
type spanRef struct {
	trace string
	span  string
}

// behaviorParentage is the recorded child→parent relation over behavioral
// identities, derived from one run's retained observations.
//
// Built once per run and then read: it holds identities, not spans, because
// the fold operates on the added set of a comparison and that set is
// identities.
type behaviorParentage struct {
	// parents maps a child identity to the identities recorded as its
	// parents. A behavioral identity observed under several parents has
	// several entries; this is normal for a transport shape called from more
	// than one place.
	parents map[string]map[string]struct{}

	// degraded marks parentage that could not be fully resolved — an
	// ambiguous span reference, so the derived relation is not trustworthy
	// enough to lower a count with.
	degraded bool
}

// buildBehaviorParentage derives the child→parent identity relation from one
// run's retained observations.
//
// Two passes, because a child may arrive before its parent and neither order
// may change the result. The first pass learns which identity each observed
// span carried; the second resolves every recorded parent reference against
// it. Nothing here depends on sequence, timing or adjacency.
//
// observations must be the run's whole retained history. A caller holding only
// part of it has partial correlation and must not call this.
func buildBehaviorParentage(observations []Observation) behaviorParentage {
	parentage := behaviorParentage{parents: make(map[string]map[string]struct{})}

	// Pass one: (trace, span) → identity.
	identityOf := make(map[spanRef]string, len(observations))
	for _, o := range observations {
		if o.TraceID == "" || o.SpanID == "" || o.FingerprintID == "" {
			// An observation that named no span cannot be anybody's parent
			// and cannot resolve its own. Not an error: an Event built by
			// hand carries no span identity at all (task 084).
			continue
		}
		ref := spanRef{trace: o.TraceID, span: o.SpanID}
		known, seen := identityOf[ref]
		if !seen {
			identityOf[ref] = o.FingerprintID
			continue
		}
		if known != o.FingerprintID {
			// One span reported under two behavioral identities. Which one a
			// child meant is unknowable, so the whole relation is marked
			// degraded rather than resolved by picking. Duplicates of the
			// *same* identity are the common case and are not this.
			parentage.degraded = true
		}
	}

	// Pass two: resolve each recorded parent reference.
	for _, o := range observations {
		if o.FingerprintID == "" || o.ParentSpanID == "" || o.TraceID == "" {
			continue
		}
		parentID, resolved := identityOf[spanRef{trace: o.TraceID, span: o.ParentSpanID}]
		if !resolved {
			// The parent was never observed, was sampled away, or is named in
			// another trace. All three are the documented fallback: the child
			// keeps its own change. Never an error — task 084 refuses any
			// parent-existence check, because requiring one would drop exactly
			// the evidence a lost parent makes valuable.
			continue
		}
		if parentID == o.FingerprintID {
			// A span recorded as its own parent, or two observations of one
			// identity in a parent relation. Folding it would say an identity
			// is its own change's child, which is not a relationship.
			continue
		}
		into, known := parentage.parents[o.FingerprintID]
		if !known {
			into = make(map[string]struct{})
			parentage.parents[o.FingerprintID] = into
		}
		into[parentID] = struct{}{}
	}
	return parentage
}

// foldAddedChanges applies ADR 0052's rule to one comparison's added set.
//
// Returns the counted changes and the correlation state the result carries,
// which may be more pessimistic than the state passed in: an ambiguous or
// cyclic relation degrades a complete history to partial, because the derived
// parentage is what turned out to be untrustworthy rather than the rows.
//
// added must be the added identities of a comparison, sorted. The result is
// sorted by root identity, so two comparisons of the same evidence produce the
// same output.
func foldAddedChanges(
	added []string, parentage behaviorParentage, state CorrelationState,
) ([]BehaviorChange, CorrelationState) {
	// Not folded: every added identity is its own change. This is the
	// unfolded count the platform has always reported, and it is what every
	// unresolved case falls back to.
	if !state.folded() || parentage.degraded {
		if parentage.degraded && state == CorrelationComplete {
			state = CorrelationPartial
		}
		return unfoldedChanges(added), state
	}

	inAdded := make(map[string]struct{}, len(added))
	for _, id := range added {
		inAdded[id] = struct{}{}
	}

	// The added subgraph: edges only where both ends are added. A child whose
	// parent is present in the reference is not folded — that is the case ADR
	// 0047 protects, a known tool changing destination.
	addedParents := make(map[string][]string, len(added))
	addedChildren := make(map[string][]string, len(added))
	for _, child := range added {
		for parent := range parentage.parents[child] {
			if _, isAdded := inAdded[parent]; !isAdded {
				continue
			}
			addedParents[child] = append(addedParents[child], parent)
			addedChildren[parent] = append(addedChildren[parent], child)
		}
	}

	roots := make([]string, 0, len(added))
	for _, id := range added {
		if len(addedParents[id]) == 0 {
			roots = append(roots, id)
		}
	}

	// A cycle among added identities leaves every participant with an added
	// parent and therefore no root, which would count them as zero changes —
	// the one way this rule could undercount. Anything the roots cannot reach
	// is in one, so it becomes a root of its own and the state degrades.
	reached := make(map[string]struct{}, len(added))
	for _, root := range roots {
		markReachable(root, addedChildren, reached)
	}
	stranded := make([]string, 0)
	for _, id := range added {
		if _, ok := reached[id]; !ok {
			stranded = append(stranded, id)
		}
	}
	if len(stranded) > 0 {
		roots = append(roots, stranded...)
		slices.Sort(roots)
		state = CorrelationPartial
	}

	changes := make([]BehaviorChange, 0, len(roots))
	for _, root := range roots {
		covered := make(map[string]struct{})
		markReachable(root, addedChildren, covered)
		contributors := make([]string, 0, len(covered))
		for id := range covered {
			contributors = append(contributors, id)
		}
		slices.Sort(contributors)
		changes = append(changes, BehaviorChange{
			RootFingerprintID:          root,
			ContributingFingerprintIDs: contributors,
		})
	}
	slices.SortFunc(changes, func(a, b BehaviorChange) int {
		return cmp.Compare(a.RootFingerprintID, b.RootFingerprintID)
	})
	return changes, state
}

// markReachable walks the added subgraph from one root, guarding against
// cycles by construction: a node already marked is never expanded twice.
func markReachable(from string, children map[string][]string, seen map[string]struct{}) {
	if _, done := seen[from]; done {
		return
	}
	seen[from] = struct{}{}
	for _, child := range children[from] {
		markReachable(child, children, seen)
	}
}

// unfoldedChanges is the fallback: one change per added identity, each
// contributing only itself.
func unfoldedChanges(added []string) []BehaviorChange {
	changes := make([]BehaviorChange, 0, len(added))
	for _, id := range added {
		changes = append(changes, BehaviorChange{
			RootFingerprintID:          id,
			ContributingFingerprintIDs: []string{id},
		})
	}
	slices.SortFunc(changes, func(a, b BehaviorChange) int {
		return cmp.Compare(a.RootFingerprintID, b.RootFingerprintID)
	})
	return changes
}

// correlationStateFor maps the candidate run's retained-history state to the
// state a comparison's correlation carries.
//
// **The candidate's alone**, and the asymmetry is the point. The added set
// comes from the two behavior snapshots, which CompareBehaviorSnapshots
// already requires to be complete; only the *edges* come from observations,
// and only added identities have edges that matter. So the reference's
// retention cannot affect the fold, and consulting it would mark comparisons
// partial for a reason that does not exist — which would make `partial` the
// usual state and stop anyone reading it.
func correlationStateFor(candidate ObservationHistory) CorrelationState {
	switch {
	case !candidate.Available():
		return CorrelationUnavailable
	case candidate.Complete():
		return CorrelationComplete
	default:
		return CorrelationPartial
	}
}

// validateChangeCountArithmetic refuses a change count that cannot be true of
// its diff.
//
// The fold may only lower the count, never raise it, and a comparison with
// added identities has at least one change. A violation means the fold is
// wrong, and a wrong count in the permissive direction is the failure this
// whole design is arranged to prevent — so it is refused rather than reported.
func validateChangeCountArithmetic(added, changes int) error {
	if changes < 0 {
		return fmt.Errorf("%w: change count %d is negative", ErrInvalidGateEvidence, changes)
	}
	if changes > added {
		return fmt.Errorf("%w: %d counted changes over %d added identities; the fold "+
			"may only lower the count", ErrInvalidGateEvidence, changes, added)
	}
	if added > 0 && changes == 0 {
		return fmt.Errorf("%w: %d added identities folded to zero changes",
			ErrInvalidGateEvidence, added)
	}
	return nil
}
