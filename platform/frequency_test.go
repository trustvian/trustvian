package platform

// Task 106's frequency evidence over values: exact figures, the floor
// boundaries, unavailability, permutation invariance and overflow.

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// runWith is one completed repetition whose behaviors were observed the given
// number of times. A behavior's target is "t-" plus its first letter.
func runWith(side ComparisonSide, index int, calls map[string]uint64) repetitionInput {
	in := completedInput(side, index)
	for fp, n := range calls {
		e := BehaviorEntry{FingerprintID: fp, Behavior: internalRecord("e", fp, "op-"+fp).Behavior,
			Observations: n, Operational: unavailableOperational(n)}
		e.Behavior.TargetName = "t-" + fp[:1]
		in.entries = append(in.entries, e)
	}
	return in
}

func stats(runs, total, low, high, mean uint64) FrequencyStats {
	return FrequencyStats{Runs: runs, CallsTotal: total, CallsPerRunMin: low, CallsPerRunMax: high,
		CallsPerRunMeanMilli: mean}
}

func behaviorOf(t *testing.T, c RepeatedEvaluationComparison, fp string) RepeatedBehaviorPresence {
	t.Helper()
	for _, b := range c.Behaviors {
		if b.FingerprintID == fp {
			return b
		}
	}
	t.Fatalf("behavior %s not reported", fp)
	return RepeatedBehaviorPresence{}
}

func targetOf(t *testing.T, c RepeatedEvaluationComparison, name string) RepeatedTargetFrequency {
	t.Helper()
	for _, r := range c.Targets {
		if r.Target.Name == name {
			return r
		}
	}
	t.Fatalf("target %s not reported", name)
	return RepeatedTargetFrequency{}
}

// frequencyFixture is three repetitions a side:
//
//	           ref-1 ref-2 ref-3 | cand-1 cand-2 cand-3
//	crm  (t-c)    3     3     3  |    9      9      8
//	send (t-s)    1     1     1  |    1      —      —
//	kb   (t-k)    2     —     1  |    —      —      —
//	new  (t-n)    —     —     —  |    1      1      1
func frequencyFixture() (refs, cands []repetitionInput) {
	refs = []repetitionInput{
		runWith(SideReference, 1, map[string]uint64{"crm": 3, "send": 1, "kb": 2}),
		runWith(SideReference, 2, map[string]uint64{"crm": 3, "send": 1}),
		runWith(SideReference, 3, map[string]uint64{"crm": 3, "send": 1, "kb": 1}),
	}
	cands = []repetitionInput{
		runWith(SideCandidate, 1, map[string]uint64{"crm": 9, "send": 1, "new": 1}),
		runWith(SideCandidate, 2, map[string]uint64{"crm": 9, "new": 1}),
		runWith(SideCandidate, 3, map[string]uint64{"crm": 8, "new": 1}),
	}
	return refs, cands
}

func TestFrequencyFigures(t *testing.T) {
	refs, cands := frequencyFixture()
	c, err := reduceRepeated(repeatedTestLimits(2, 0), 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		fp        string
		ref, cand FrequencyStats
		lost      bool
	}{
		// 26 / 3 = 8.666… → 8666 thousandths, floored.
		{"crm", stats(3, 9, 3, 3, 3000), stats(3, 26, 8, 9, 8666), false},
		// Present in every reference run, missing from two candidate runs.
		{"send", stats(3, 3, 1, 1, 1000), stats(3, 1, 0, 1, 333), true},
		// Absent from one reference run, so its minimum is 0 — and it is not
		// lost, because it was not in every reference run.
		{"kb", stats(3, 3, 0, 2, 1000), stats(3, 0, 0, 0, 0), false},
		{"new", stats(3, 0, 0, 0, 0), stats(3, 3, 1, 1, 1000), false},
	}
	for _, tt := range tests {
		b := behaviorOf(t, c, tt.fp)
		if b.Reference != tt.ref || b.Candidate != tt.cand || b.Lost != tt.lost {
			t.Errorf("%s: reference %+v candidate %+v lost %v; want %+v %+v %v",
				tt.fp, b.Reference, b.Candidate, b.Lost, tt.ref, tt.cand, tt.lost)
		}
	}
	// t-c: 26 × 1000 / 9 = 2888.8… → 2888 permille.
	if r := targetOf(t, c, "t-c"); !r.RatioAvailable || r.CallRatioPermille != 2888 {
		t.Errorf("t-c ratio = %d (%v), want 2888", r.CallRatioPermille, r.RatioAvailable)
	}
	// t-n: the reference never called it, so there is no ratio — not 0.
	if r := targetOf(t, c, "t-n"); r.RatioAvailable || r.CallRatioPermille != 0 || r.Reference.CallsTotal != 0 {
		t.Errorf("t-n = %+v, want no ratio", r)
	}
	// t-k: the candidate stopped calling it: a ratio of exactly 0 is a figure.
	if r := targetOf(t, c, "t-k"); !r.RatioAvailable || r.CallRatioPermille != 0 {
		t.Errorf("t-k = %+v, want a ratio of 0", r)
	}
	if c.LostTransitions != LostTransitionsNotRecorded {
		t.Errorf("lost transitions = %q", c.LostTransitions)
	}
}

// TestCallRatioFloorBoundaries: exactly on a permille and one call short.
func TestCallRatioFloorBoundaries(t *testing.T) {
	for _, tt := range []struct {
		ref, cand, want uint64
	}{
		{1000, 2000, 2000}, // exact
		{1000, 1999, 1999},
		{3, 2, 666}, // 666.6… floors
		{3, 1, 333},
		{7, 0, 0},
	} {
		got, err := floorDivTimes1000(tt.cand, tt.ref)
		if err != nil || got != tt.want {
			t.Errorf("ratio %d/%d = %d, %v; want %d", tt.cand, tt.ref, got, err, tt.want)
		}
	}
}

// TestFrequencyIsInvariantUnderEveryPermutation: every new counter, for every
// ordering of three repetitions a side — 36 combinations.
func TestFrequencyIsInvariantUnderEveryPermutation(t *testing.T) {
	refs, cands := frequencyFixture()
	perms := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var want string
	for _, rp := range perms {
		for _, cp := range perms {
			c, err := reduceRepeated(repeatedTestLimits(2, 0), 3, []repetitionInput{
				refs[rp[0]], refs[rp[1]], refs[rp[2]], cands[cp[0]], cands[cp[1]], cands[cp[2]]})
			if err != nil {
				t.Fatal(err)
			}
			shape := fmt.Sprintf("%+v %+v %s", c.Behaviors, c.Targets, c.LostTransitions)
			if want == "" {
				want = shape
			} else if shape != want {
				t.Fatalf("permutation %v / %v changed the frequency evidence", rp, cp)
			}
		}
	}
}

// TestFrequencyOverflowIsAnError: sums past uint64 are refused, never wrapped.
func TestFrequencyOverflowIsAnError(t *testing.T) {
	huge := uint64(math.MaxUint64 / 2)
	// No operational summaries on these entries, so the frequency sum is the
	// first to overflow rather than 087's.
	bare := func(in repetitionInput) repetitionInput {
		for i := range in.entries {
			in.entries[i].Operational = OperationalSummary{}
		}
		return in
	}
	_, err := reduceRepeated(repeatedTestLimits(1, 0), 2, []repetitionInput{
		bare(runWith(SideReference, 1, map[string]uint64{"crm": huge})),
		bare(runWith(SideReference, 2, map[string]uint64{"crm": huge + 2})),
		bare(runWith(SideCandidate, 1, map[string]uint64{"crm": 1})),
		bare(runWith(SideCandidate, 2, map[string]uint64{"crm": 1})),
	})
	if !errors.Is(err, ErrFrequencyOverflow) {
		t.Fatalf("error = %v, want ErrFrequencyOverflow", err)
	}
	// A mean whose thousandths exceed uint64 is refused too.
	if _, err := floorDivTimes1000(math.MaxUint64, 1); !errors.Is(err, ErrFrequencyOverflow) {
		t.Fatalf("mean overflow error = %v", err)
	}
}

// TestFrequencyOfASideWithNoCompletedRunIsUnavailable: the per-run figures
// are not stated — never zeros.
func TestFrequencyOfASideWithNoCompletedRunIsUnavailable(t *testing.T) {
	failed := completedInput(SideCandidate, 1)
	failed.evidence.Status = RunFailed
	failed.entries = nil
	c, err := reduceRepeated(repeatedTestLimits(1, 0), 1, []repetitionInput{
		runWith(SideReference, 1, map[string]uint64{"crm": 2}), failed,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := behaviorOf(t, c, "crm")
	if b.Candidate.Available() || b.Candidate != (FrequencyStats{}) {
		t.Fatalf("candidate frequency = %+v, want unavailable", b.Candidate)
	}
	// Lost is a classification over the request's N: absent from the one
	// candidate run that was asked for.
	if !b.Lost {
		t.Error("a behavior in every reference run and no candidate run is not lost")
	}
}
