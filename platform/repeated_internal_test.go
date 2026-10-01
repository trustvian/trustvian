package platform

// The repeated reduction over values, without a store: the refusals that would
// otherwise need hundreds of ingests, and exhaustive permutation invariance.

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func repeatedTestLimits(k, j uint64) RepeatedEvaluationGateLimits {
	return RepeatedEvaluationGateLimits{AddedCandidatePresenceMinimum: k, AddedReferencePresenceMaximum: j}
}

func entriesFor(fps ...string) []BehaviorEntry {
	out := make([]BehaviorEntry, 0, len(fps))
	for _, fp := range fps {
		out = append(out, BehaviorEntry{FingerprintID: fp,
			Behavior: internalRecord("e", fp, "op-"+fp).Behavior, Observations: 1})
	}
	return out
}

func completedInput(side ComparisonSide, index int, fps ...string) repetitionInput {
	return repetitionInput{
		evidence: RepetitionEvidence{Side: side, Index: index, Status: RunCompleted, RecordCount: 1,
			RunID: EvaluationRunID(fmt.Sprintf("%s-%d", side, index))},
		entries: entriesFor(fps...),
	}
}

// A repetition whose snapshot saturated is refused, never counted.
func TestASaturatedRepetitionSnapshotIsRefused(t *testing.T) {
	run, err := NewEvaluationRun("run-sat", "cand-1", "staging", "profile-sat",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxBehaviorEntries + 1 {
		// The 513th distinct behavior saturates the collector; its error is
		// the collector's own and is not what this test asserts.
		_ = collector.Observe(internalRecord(fmt.Sprintf("e%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op%d", i)))
	}
	snapshot := collector.Snapshot()
	if snapshot.Complete() {
		t.Fatal("the collector did not saturate; this test would pass vacuously")
	}
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatal(err)
	}
	_, err = completedRepetition(RepetitionEvidence{Side: SideCandidate, Index: 1, RunID: run.ID(),
		Status: RunCompleted}, aggregate, snapshot)
	if !errors.Is(err, ErrIncompleteSnapshot) {
		t.Errorf("error = %v, want ErrIncompleteSnapshot", err)
	}
}

// More than 512 distinct identities across the whole execution is incomplete
// evidence too, even when each repetition alone fits.
func TestTheExecutionWideIdentityBoundIsRefused(t *testing.T) {
	half := func(prefix string) []string {
		out := make([]string, 300)
		for i := range out {
			out[i] = fmt.Sprintf("%s-%d", prefix, i)
		}
		return out
	}
	_, err := reduceRepeated(repeatedTestLimits(1, 0), 1, []repetitionInput{
		completedInput(SideReference, 1, half("a")...),
		completedInput(SideCandidate, 1, half("b")...),
	})
	if !errors.Is(err, ErrIncompleteSnapshot) {
		t.Errorf("600 distinct identities: error = %v, want ErrIncompleteSnapshot", err)
	}
	if _, err := reduceRepeated(repeatedTestLimits(1, 0), 1, []repetitionInput{
		completedInput(SideReference, 1, half("a")[:256]...),
		completedInput(SideCandidate, 1, half("b")[:256]...),
	}); err != nil {
		t.Errorf("exactly 512 distinct identities refused: %v", err)
	}
}

// One fingerprint with two descriptors across repetitions is refused.
func TestADescriptorConflictAcrossRepetitionsIsRefused(t *testing.T) {
	conflicting := completedInput(SideCandidate, 1, "x")
	conflicting.entries[0].Behavior.OperationName = "something-else"
	_, err := reduceRepeated(repeatedTestLimits(1, 0), 1, []repetitionInput{
		completedInput(SideReference, 1, "x"), conflicting,
	})
	if !errors.Is(err, ErrFingerprintConflict) {
		t.Errorf("error = %v, want ErrFingerprintConflict", err)
	}
}

// Every ordering of three repetitions per side — 36 combinations — gives the
// same presence and the same gate.
func TestPresenceIsInvariantUnderEveryPermutation(t *testing.T) {
	refs := []repetitionInput{completedInput(SideReference, 1, "a", "b"),
		completedInput(SideReference, 2, "a"), completedInput(SideReference, 3, "a", "c")}
	cands := []repetitionInput{completedInput(SideCandidate, 1, "a", "x"),
		completedInput(SideCandidate, 2, "b"), completedInput(SideCandidate, 3, "a", "x", "c")}
	perms := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var want string
	for _, rp := range perms {
		for _, cp := range perms {
			inputs := []repetitionInput{refs[rp[0]], refs[rp[1]], refs[rp[2]],
				cands[cp[0]], cands[cp[1]], cands[cp[2]]}
			got, err := reduceRepeated(repeatedTestLimits(2, 0), 3, inputs)
			if err != nil {
				t.Fatal(err)
			}
			shape := fmt.Sprint(got.Behaviors, got.Gate.Checks(), got.Gate.Verdict())
			if want == "" {
				want = shape
			} else if shape != want {
				t.Fatalf("permutation %v / %v changed the result", rp, cp)
			}
		}
	}
}
