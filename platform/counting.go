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
// behavioral identity of the same run** — and an identity is such a child only
// when *every* retained occurrence of it is recorded beneath an added
// identity. One occurrence the added parents cannot explain (beneath a known
// parent, as a root, beneath an unresolved parent) keeps the identity counted.
//
// Three properties of that rule matter more than the mechanism:
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
//
//  3. **Partial means the identity count, with no exceptions.** A cycle or an
//     ambiguity anywhere refuses the whole fold rather than the part of the
//     graph it touches, so a reader told `partial` can rely on the change
//     count equalling the added identity count.

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
// The root is an added identity that is not eligible to fold: one with no
// added parent, or one with an occurrence no added parent explains. The
// contributors are
// the root and the added identities recorded as its descendants — for the
// motivating case, the tool and the transport child it called.
//
// Contributors may appear under more than one change. Two new tools that both
// call one new transport identity are two changes, and the shared child
// contributes to each: overlapping contribution is honest, and merging the two
// into one change would report one where a developer made two.
//
// A contributor may also be a root of its own change. An identity observed
// both beneath a new tool and somewhere the new tool cannot explain — beneath
// a known tool, with no parent, beneath a parent that never resolved —
// contributes to the new tool's change and is counted as its own, because
// ADR 0052 folds an identity only when every occurrence of it is beneath an
// added identity.
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
// identities, derived from one run's retained observations, together with
// what the fold needs to know about the *contexts* each identity was observed
// in.
//
// Built once per run and then read: it holds identities, not spans, because
// the fold operates on the added set of a comparison and that set is
// identities. Collapsing spans to identities loses one fact the fold cannot do
// without — whether *every* occurrence of an identity was beneath another
// identity — so that fact is kept alongside the union of parents.
type behaviorParentage struct {
	// parents maps a child identity to every distinct identity any of its
	// occurrences resolved to as a parent. A behavioral identity observed
	// under several parents has several entries; this is normal for a
	// transport shape called from more than one place, and some of those
	// parents may be added while others are not.
	parents map[string]map[string]struct{}

	// independent holds each identity with at least one occurrence that no
	// other identity explains: an occurrence with no parent reference, one
	// whose parent did not resolve in its own trace, one that carried no
	// trace, or one whose parent was the same identity. Such an occurrence
	// is a change on its own whatever else the identity was observed under,
	// so its identity is never folded away.
	independent map[string]struct{}

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
// it and classifies each occurrence as either beneath another identity or
// independent. Nothing here depends on sequence, timing or adjacency, and
// both outputs are sets, so arrival order cannot change them.
//
// observations must be the run's whole retained history. A caller holding only
// part of it has partial correlation and must not call this.
func buildBehaviorParentage(observations []Observation) behaviorParentage {
	parentage := behaviorParentage{
		parents:     make(map[string]map[string]struct{}),
		independent: make(map[string]struct{}),
	}

	// Pass one: (trace, span) → identity.
	identityOf := make(map[spanRef]string, len(observations))
	for _, o := range observations {
		if o.TraceID == "" || o.SpanID == "" || o.FingerprintID == "" {
			// An observation that named no span cannot be anybody's parent.
			// Not an error: an Event built by hand carries no span identity
			// at all (task 084).
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

	// Pass two: classify every occurrence.
	for _, o := range observations {
		if o.FingerprintID == "" {
			// Not an occurrence of any identity, so there is nothing to
			// classify; the added set is identities.
			continue
		}
		if o.ParentSpanID == "" || o.TraceID == "" {
			// A root, or a reference with no trace to resolve it in. Either
			// way no other identity explains this occurrence.
			parentage.independent[o.FingerprintID] = struct{}{}
			continue
		}
		parentID, resolved := identityOf[spanRef{trace: o.TraceID, span: o.ParentSpanID}]
		if !resolved {
			// The parent was never observed, was sampled away, or is named in
			// another trace. All three are the documented fallback: this
			// occurrence keeps its identity a change of its own. Never an
			// error — task 084 refuses any parent-existence check, because
			// requiring one would drop exactly the evidence a lost parent
			// makes valuable.
			parentage.independent[o.FingerprintID] = struct{}{}
			continue
		}
		if parentID == o.FingerprintID {
			// A span recorded as its own parent, or one identity nested in
			// itself. An identity is not its own change's child, and walking
			// further up the span chain to find a different ancestor would be
			// an inference the recorded edge does not make, so the occurrence
			// counts as independent.
			parentage.independent[o.FingerprintID] = struct{}{}
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

// foldEligible reports whether an added identity may be folded into the
// change of an added parent rather than counted as a change of its own.
//
// Conservative by construction, and the reason is the direction of harm:
// every occurrence of the identity must be accounted for by an added parent.
// One occurrence the new parents cannot explain — beneath a parent present in
// the reference, beneath a parent that never resolved, or with no parent at
// all — is a change the developer's new code did not make, and folding it
// would hide it. The motivating hole is ADR 0047's: a known tool reaching a
// new destination that a new tool also reaches.
//
// An identity with no retained occurrences at all is not eligible either;
// nothing recorded says it belongs to anything.
func (p behaviorParentage) foldEligible(id string, inAdded map[string]struct{}) bool {
	if _, independent := p.independent[id]; independent {
		return false
	}
	parents := p.parents[id]
	if len(parents) == 0 {
		return false
	}
	for parent := range parents {
		if _, added := inAdded[parent]; !added {
			return false
		}
	}
	return true
}

// foldAddedChanges applies ADR 0052's rule to one comparison's added set.
//
// Returns the counted changes and the correlation state the result carries,
// which may be more pessimistic than the state passed in: an ambiguous or
// cyclic relation degrades a complete history to partial, because the derived
// parentage is what turned out to be untrustworthy rather than the rows.
// Whenever the returned state is not complete the changes are the unfolded
// ones — one per added identity, each contributing only itself — with no
// exceptions, so a reader of a partial result can rely on it equalling the
// identity count.
//
// added must be the added identities of a comparison, sorted. The result is
// sorted by root identity, and contributors within each change are sorted, so
// two comparisons of the same evidence produce the same output.
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

	// The added subgraph: an edge wherever an added identity was observed
	// beneath another added identity, whether or not the child is eligible to
	// fold. Edges to parents present in the reference are not in it — the
	// case ADR 0047 protects — but they still make their child ineligible
	// above.
	addedChildren := make(map[string][]string, len(added))
	for _, child := range added {
		for parent := range parentage.parents[child] {
			if _, isAdded := inAdded[parent]; isAdded {
				addedChildren[parent] = append(addedChildren[parent], child)
			}
		}
	}
	for parent := range addedChildren {
		slices.Sort(addedChildren[parent])
	}

	// A cycle anywhere in the added subgraph — including one reachable from
	// an unrelated root, which root reachability cannot see — means the
	// recorded relation is not a hierarchy, and a count lowered on the
	// strength of it cannot be defended. The whole fold is refused, not just
	// the cycle's members: partial means the identity count.
	if hasCycle(added, addedChildren) {
		return unfoldedChanges(added), CorrelationPartial
	}

	// Roots: every added identity not eligible to fold. In an acyclic
	// subgraph every eligible identity has an added parent, so following
	// parents always ends at a root, and every added identity is covered by
	// at least one change.
	changes := make([]BehaviorChange, 0, len(added))
	for _, root := range added {
		if parentage.foldEligible(root, inAdded) {
			continue
		}
		changes = append(changes, BehaviorChange{
			RootFingerprintID:          root,
			ContributingFingerprintIDs: reachableFrom(root, addedChildren),
		})
	}
	slices.SortFunc(changes, func(a, b BehaviorChange) int {
		return cmp.Compare(a.RootFingerprintID, b.RootFingerprintID)
	})
	return changes, state
}

// hasCycle reports whether the added subgraph contains a directed cycle.
//
// Kahn's algorithm over the whole graph, not a walk from roots: it repeatedly
// removes identities with no remaining added parent, and anything left over
// sits on or behind a cycle. O(identities + edges), iterative, and bounded by
// the added set, which a snapshot bounds at maxBehaviorEntries.
func hasCycle(nodes []string, children map[string][]string) bool {
	inDegree := make(map[string]int, len(nodes))
	for _, id := range nodes {
		inDegree[id] = 0
	}
	for _, id := range nodes {
		for _, child := range children[id] {
			inDegree[child]++
		}
	}
	ready := make([]string, 0, len(nodes))
	for _, id := range nodes {
		if inDegree[id] == 0 {
			ready = append(ready, id)
		}
	}
	removed := 0
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		removed++
		for _, child := range children[id] {
			inDegree[child]--
			if inDegree[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	return removed < len(inDegree)
}

// reachableFrom lists the root and every added identity recorded beneath it,
// sorted. Iterative, and each identity is expanded once, so it is bounded by
// the added subgraph whatever its shape.
func reachableFrom(root string, children map[string][]string) []string {
	seen := map[string]struct{}{root: {}}
	stack := []string{root}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, child := range children[id] {
			if _, done := seen[child]; done {
				continue
			}
			seen[child] = struct{}{}
			stack = append(stack, child)
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
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
