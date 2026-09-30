package platform

import (
	"slices"
	"strings"
	"testing"
)

// The counting contract of ADR 0052, exercised over the relation the fold
// actually consumes.
//
// These replace `TestCountingFoldIsNotImplementedYet`, which asserted the gap
// and failed when it closed. Every case ADR 0052's table names has a test
// here, and the ones that must not fold are the majority — because the safety
// property of this design is that an unresolved correlation counts *more*.

// obs builds one retained observation with just the fields the fold reads.
func obs(fingerprint, trace, span, parent string) Observation {
	return Observation{
		FingerprintID: fingerprint,
		TraceID:       trace,
		SpanID:        span,
		ParentSpanID:  parent,
	}
}

// countChanges is the whole pipeline under test: derive parentage, fold.
func countChanges(
	t *testing.T, observations []Observation, added []string, state CorrelationState,
) ([]BehaviorChange, CorrelationState) {
	t.Helper()
	slices.Sort(added)
	changes, got := foldAddedChanges(added, buildBehaviorParentage(observations), state)
	if err := validateChangeCountArithmetic(len(added), len(changes)); err != nil {
		t.Fatalf("the fold produced an impossible count: %v", err)
	}
	return changes, got
}

func rootsOf(changes []BehaviorChange) []string {
	roots := make([]string, 0, len(changes))
	for _, c := range changes {
		roots = append(roots, c.RootFingerprintID)
	}
	return roots
}

// TestANewToolAndItsTransportChildAreOneChange is the defect, closed.
func TestANewToolAndItsTransportChildAreOneChange(t *testing.T) {
	// The motivating shape: one tool span, one transport child that names it.
	observations := []Observation{
		obs("fp-tool", "trace-1", "span-tool", ""),
		obs("fp-http", "trace-1", "span-http", "span-tool"),
	}
	changes, state := countChanges(t, observations,
		[]string{"fp-tool", "fp-http"}, CorrelationComplete)

	if len(changes) != 1 {
		t.Fatalf("counted %d changes over one act, want 1: roots %v", len(changes), rootsOf(changes))
	}
	if changes[0].RootFingerprintID != "fp-tool" {
		t.Errorf("root = %q, want the tool; the transport child folds into its parent",
			changes[0].RootFingerprintID)
	}
	want := []string{"fp-http", "fp-tool"}
	if !slices.Equal(changes[0].ContributingFingerprintIDs, want) {
		t.Errorf("contributors = %v, want %v; both identities must remain linked to "+
			"the change so their evidence stays reachable",
			changes[0].ContributingFingerprintIDs, want)
	}
	if state != CorrelationComplete {
		t.Errorf("state = %q, want complete", state)
	}
}

// TestAKnownToolChangingDestinationStillCounts is the case ADR 0047 protected.
//
// The tool is unchanged and therefore not added; only the transport identity
// is new. Folding it into its parent would hide exactly the change this
// platform exists to catch, so the parent being *shared* is what keeps the
// transport identity a root.
func TestAKnownToolChangingDestinationStillCounts(t *testing.T) {
	// Candidate: the same tool, now posting somewhere else.
	observations := []Observation{
		obs("fp-tool", "trace-1", "span-tool", ""),
		obs("fp-http-attacker", "trace-1", "span-http", "span-tool"),
	}
	// fp-tool is in both runs, so only the transport identity is added.
	changes, state := countChanges(t, observations,
		[]string{"fp-http-attacker"}, CorrelationComplete)

	if len(changes) != 1 {
		t.Fatalf("counted %d changes, want 1; a known tool changing destination "+
			"must reach the gate", len(changes))
	}
	if changes[0].RootFingerprintID != "fp-http-attacker" {
		t.Errorf("root = %q, want the new transport identity", changes[0].RootFingerprintID)
	}
	if state != CorrelationComplete {
		t.Errorf("state = %q, want complete", state)
	}
}

// TestTwoNewToolsSharingOneChildAreTwoChanges is why the rule anchors on roots
// rather than on connected components.
//
// The three identities form one connected component. Counting components
// would report one change where a developer added two tools — an undercount,
// which is the direction that turns a real change into a PASS.
func TestTwoNewToolsSharingOneChildAreTwoChanges(t *testing.T) {
	observations := []Observation{
		obs("fp-tool-a", "trace-1", "span-a", ""),
		obs("fp-shared", "trace-1", "span-a-child", "span-a"),
		obs("fp-tool-b", "trace-2", "span-b", ""),
		obs("fp-shared", "trace-2", "span-b-child", "span-b"),
	}
	changes, _ := countChanges(t, observations,
		[]string{"fp-tool-a", "fp-tool-b", "fp-shared"}, CorrelationComplete)

	if got := rootsOf(changes); !slices.Equal(got, []string{"fp-tool-a", "fp-tool-b"}) {
		t.Fatalf("roots = %v, want both tools; counting connected components would "+
			"merge two changes into one", got)
	}
	// The shared child contributes to both, which is honest: it is part of
	// what each tool added.
	for _, c := range changes {
		if !slices.Contains(c.ContributingFingerprintIDs, "fp-shared") {
			t.Errorf("change %q does not list the shared child as a contributor",
				c.RootFingerprintID)
		}
	}
}

// TestBothArrivalOrdersProduceTheSameCount pins order independence.
//
// A child span completes and exports before its parent, so the child-first
// order is the common one and neither may change the result.
func TestBothArrivalOrdersProduceTheSameCount(t *testing.T) {
	parentFirst := []Observation{
		obs("fp-tool", "trace-1", "span-tool", ""),
		obs("fp-http", "trace-1", "span-http", "span-tool"),
	}
	childFirst := []Observation{
		obs("fp-http", "trace-1", "span-http", "span-tool"),
		obs("fp-tool", "trace-1", "span-tool", ""),
	}
	a, _ := countChanges(t, parentFirst, []string{"fp-tool", "fp-http"}, CorrelationComplete)
	b, _ := countChanges(t, childFirst, []string{"fp-tool", "fp-http"}, CorrelationComplete)

	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("parent-first counted %d, child-first counted %d; want 1 each", len(a), len(b))
	}
	if !slices.Equal(rootsOf(a), rootsOf(b)) {
		t.Errorf("arrival order changed the result: %v vs %v", rootsOf(a), rootsOf(b))
	}
}

// TestUnresolvedParentageAlwaysCountsMore is the safety property, as a table.
//
// Every case ADR 0052 cannot resolve must fall back to counting the child as
// its own change. A fold that lowered a count here would be the one failure
// this design exists to prevent.
func TestUnresolvedParentageAlwaysCountsMore(t *testing.T) {
	cases := []struct {
		name         string
		observations []Observation
		added        []string
		wantChanges  int
		wantDegraded bool
		why          string
	}{
		{
			name: "parent never observed",
			observations: []Observation{
				obs("fp-http", "trace-1", "span-http", "span-missing"),
			},
			added:       []string{"fp-http"},
			wantChanges: 1,
			why:         "a sampled-away parent must not make its child disappear from the count",
		},
		{
			name: "parent in another trace",
			observations: []Observation{
				obs("fp-tool", "trace-1", "span-tool", ""),
				obs("fp-http", "trace-2", "span-http", "span-tool"),
			},
			added:       []string{"fp-tool", "fp-http"},
			wantChanges: 2,
			why:         "ParentSpanID is trace-scoped; resolving it across traces is the inference ADR 0052 forbids",
		},
		{
			name: "one span reported under two identities",
			observations: []Observation{
				obs("fp-tool", "trace-1", "span-tool", ""),
				obs("fp-other", "trace-1", "span-tool", ""),
				obs("fp-http", "trace-1", "span-http", "span-tool"),
			},
			added:        []string{"fp-tool", "fp-other", "fp-http"},
			wantChanges:  3,
			wantDegraded: true,
			why:          "which identity the child meant is unknowable, so nothing folds",
		},
		{
			name: "cycle among added identities",
			observations: []Observation{
				obs("fp-a", "trace-1", "span-a", "span-b"),
				obs("fp-b", "trace-1", "span-b", "span-a"),
			},
			added:        []string{"fp-a", "fp-b"},
			wantChanges:  2,
			wantDegraded: true,
			why:          "every participant has an added parent, so without this both would vanish from the count",
		},
		{
			name: "self-parent",
			observations: []Observation{
				obs("fp-a", "trace-1", "span-a", "span-a"),
			},
			added:       []string{"fp-a"},
			wantChanges: 1,
			why:         "an identity is not its own change's child",
		},
		{
			name: "observation carries no span identity",
			observations: []Observation{
				obs("fp-a", "", "", ""),
				obs("fp-b", "", "", ""),
			},
			added:       []string{"fp-a", "fp-b"},
			wantChanges: 2,
			why:         "an Event built by hand names no span and cannot be anybody's parent",
		},
		{
			name: "duplicate observation of one span, same identity",
			observations: []Observation{
				obs("fp-tool", "trace-1", "span-tool", ""),
				obs("fp-tool", "trace-1", "span-tool", ""),
				obs("fp-http", "trace-1", "span-http", "span-tool"),
			},
			added:       []string{"fp-tool", "fp-http"},
			wantChanges: 1,
			why:         "a repeat of the same identity is not ambiguity and must still fold",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changes, state := countChanges(t, tc.observations, tc.added, CorrelationComplete)
			if len(changes) != tc.wantChanges {
				t.Errorf("counted %d changes, want %d — %s (roots %v)",
					len(changes), tc.wantChanges, tc.why, rootsOf(changes))
			}
			if tc.wantDegraded && state != CorrelationPartial {
				t.Errorf("state = %q, want partial: an unresolvable relation must say so",
					state)
			}
			if !tc.wantDegraded && state != CorrelationComplete {
				t.Errorf("state = %q, want complete", state)
			}
		})
	}
}

// TestNestedCallsFoldToTheOutermostAddedIdentity covers depth.
func TestNestedCallsFoldToTheOutermostAddedIdentity(t *testing.T) {
	observations := []Observation{
		obs("fp-workflow", "trace-1", "span-w", ""),
		obs("fp-tool", "trace-1", "span-t", "span-w"),
		obs("fp-http", "trace-1", "span-h", "span-t"),
	}
	changes, _ := countChanges(t, observations,
		[]string{"fp-workflow", "fp-tool", "fp-http"}, CorrelationComplete)

	if len(changes) != 1 || changes[0].RootFingerprintID != "fp-workflow" {
		t.Fatalf("roots = %v, want one change rooted at the outermost identity",
			rootsOf(changes))
	}
	if len(changes[0].ContributingFingerprintIDs) != 3 {
		t.Errorf("contributors = %v, want all three levels",
			changes[0].ContributingFingerprintIDs)
	}
}

// TestConcurrentCallsUnderOneParentAreOneChange covers breadth.
func TestConcurrentCallsUnderOneParentAreOneChange(t *testing.T) {
	observations := []Observation{
		obs("fp-tool", "trace-1", "span-t", ""),
		obs("fp-http-a", "trace-1", "span-a", "span-t"),
		obs("fp-http-b", "trace-1", "span-b", "span-t"),
	}
	changes, _ := countChanges(t, observations,
		[]string{"fp-tool", "fp-http-a", "fp-http-b"}, CorrelationComplete)

	if len(changes) != 1 {
		t.Fatalf("counted %d changes, want 1; one new tool reaching two destinations "+
			"is one change with three contributors", len(changes))
	}
	if len(changes[0].ContributingFingerprintIDs) != 3 {
		t.Errorf("contributors = %v, want all three",
			changes[0].ContributingFingerprintIDs)
	}
}

// TestRepeatedExecutionIsNotChange pins that counting is over a set.
func TestRepeatedExecutionIsNotChange(t *testing.T) {
	observations := make([]Observation, 0, 200)
	for i := range 100 {
		span := "span-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		observations = append(observations,
			obs("fp-tool", "trace-1", span, ""),
			obs("fp-http", "trace-1", span+"-c", span),
		)
	}
	changes, _ := countChanges(t, observations,
		[]string{"fp-tool", "fp-http"}, CorrelationComplete)

	if len(changes) != 1 {
		t.Errorf("counted %d changes over 100 executions of one act, want 1; "+
			"frequency is RateDelta's and is not a change count", len(changes))
	}
}

// TestIncompleteCorrelationFallsBackToTheIdentityCount covers the states.
//
// Both non-complete states report the unfolded count — the number this
// platform has always reported — rather than refusing. Refusing would make
// every pre-schema-7 run uncomparable, which is a regression for a
// correction.
func TestIncompleteCorrelationFallsBackToTheIdentityCount(t *testing.T) {
	// A relation that *would* fold, so a fallback is visible as a difference.
	observations := []Observation{
		obs("fp-tool", "trace-1", "span-tool", ""),
		obs("fp-http", "trace-1", "span-http", "span-tool"),
	}
	added := []string{"fp-tool", "fp-http"}

	for _, state := range []CorrelationState{CorrelationPartial, CorrelationUnavailable} {
		t.Run(string(state), func(t *testing.T) {
			changes, got := countChanges(t, observations, added, state)
			if len(changes) != len(added) {
				t.Errorf("counted %d changes under %q, want the unfolded %d",
					len(changes), state, len(added))
			}
			if got != state {
				t.Errorf("state = %q, want %q reported through unchanged", got, state)
			}
			for _, c := range changes {
				if len(c.ContributingFingerprintIDs) != 1 {
					t.Errorf("change %q lists %v; an unfolded change contributes only itself",
						c.RootFingerprintID, c.ContributingFingerprintIDs)
				}
			}
		})
	}
}

// TestCorrelationStateComesFromTheCandidateAlone pins the asymmetry.
//
// The added set comes from the two snapshots, which CompareBehaviorSnapshots
// already requires to be complete; only the edges come from observations, and
// only the candidate's added identities have edges that matter. Consulting the
// reference's retention too would mark comparisons partial for a reason that
// does not exist, and a state reported pessimistically everywhere is a state
// nobody reads.
func TestCorrelationStateComesFromTheCandidateAlone(t *testing.T) {
	cases := []struct {
		name      string
		candidate ObservationHistory
		want      CorrelationState
	}{
		{"complete", NewObservationHistory(true, 10, true, 10), CorrelationComplete},
		{"partial", NewObservationHistory(true, 10, false, 99), CorrelationPartial},
		{"unavailable", NewObservationHistory(false, 0, false, 10), CorrelationUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := correlationStateFor(tc.candidate); got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTheFoldIsDeterministic pins ordering, so a stored result is reproducible.
func TestTheFoldIsDeterministic(t *testing.T) {
	observations := []Observation{
		obs("fp-b", "trace-1", "span-b", ""),
		obs("fp-a", "trace-1", "span-a", ""),
		obs("fp-c", "trace-1", "span-c", "span-a"),
		obs("fp-d", "trace-1", "span-d", "span-b"),
	}
	added := []string{"fp-d", "fp-a", "fp-c", "fp-b"}

	first, _ := countChanges(t, slices.Clone(observations), slices.Clone(added), CorrelationComplete)
	slices.Reverse(observations)
	second, _ := countChanges(t, observations, slices.Clone(added), CorrelationComplete)

	if !slices.Equal(rootsOf(first), rootsOf(second)) {
		t.Fatalf("roots differ by input order: %v vs %v", rootsOf(first), rootsOf(second))
	}
	if !slices.Equal(rootsOf(first), []string{"fp-a", "fp-b"}) {
		t.Errorf("roots = %v, want the two parents sorted", rootsOf(first))
	}
	for _, c := range first {
		if !slices.IsSorted(c.ContributingFingerprintIDs) {
			t.Errorf("contributors of %q are unsorted: %v",
				c.RootFingerprintID, c.ContributingFingerprintIDs)
		}
	}
}

// TestChangeCountArithmeticRefusesTheImpossible guards the invariant itself.
//
// The fold may only lower a count. A violation means the fold is wrong, and a
// wrong count in the permissive direction is the failure mode this design is
// arranged to prevent — so it is refused rather than reported.
func TestChangeCountArithmeticRefusesTheImpossible(t *testing.T) {
	cases := []struct {
		name     string
		added    int
		changes  int
		wantErr  bool
		mentions string
	}{
		{name: "equal is fine", added: 3, changes: 3},
		{name: "folded is fine", added: 3, changes: 1},
		{name: "none added, none counted", added: 0, changes: 0},
		{name: "more changes than identities", added: 2, changes: 3, wantErr: true, mentions: "may only lower"},
		{name: "added folded to zero", added: 2, changes: 0, wantErr: true, mentions: "folded to zero"},
		{name: "negative", added: 1, changes: -1, wantErr: true, mentions: "negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChangeCountArithmetic(tc.added, tc.changes)
			if tc.wantErr && err == nil {
				t.Fatalf("validateChangeCountArithmetic(%d, %d) = nil, want an error",
					tc.added, tc.changes)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateChangeCountArithmetic(%d, %d) = %v, want nil",
					tc.added, tc.changes, err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("error %q does not mention %q", err, tc.mentions)
			}
		})
	}
}
