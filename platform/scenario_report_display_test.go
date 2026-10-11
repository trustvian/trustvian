package platform

// The report's display strings and present_in (task 107): formatting of
// integers the comparison holds, and the repetitions its counts came from.

import (
	"math"
	"strconv"
	"testing"
)

func TestReportDisplayStrings(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{RenderPresence(8, 10), "8/10"},
		{RenderPresence(0, 3), "0/3"},
		{RenderMilli(2400), "2.4"},
		{RenderMilli(2456), "2.456"},
		{RenderMilli(2450), "2.45"},
		{RenderMilli(3000), "3"},
		{RenderMilli(0), "0"},
		{RenderMilli(1), "0.001"},
		{RenderMilli(10), "0.01"},
		{RenderMilli(math.MaxUint64), "18446744073709551.615"},
		{RenderCallsPerRun(FrequencyStats{Runs: 3, CallsPerRunMin: 1, CallsPerRunMax: 4}), "1–4"},
		{RenderCallsPerRun(FrequencyStats{Runs: 3, CallsPerRunMin: 2, CallsPerRunMax: 2}), "2–2"},
		{RenderCallsPerRun(FrequencyStats{}), NotAvailableText},
		{RenderCallsPerRunMean(FrequencyStats{Runs: 3, CallsTotal: 7, CallsPerRunMeanMilli: 2333}), "2.333"},
		{RenderCallsPerRunMean(FrequencyStats{}), NotAvailableText},
		{RenderCallRatio(RepeatedTargetFrequency{RatioAvailable: true, CallRatioPermille: 2400}), "2.4×"},
		{RenderCallRatio(RepeatedTargetFrequency{RatioAvailable: true, CallRatioPermille: 1000}), "1.0×"},
		{RenderCallRatio(RepeatedTargetFrequency{}), NotAvailableText},
	} {
		if tt.got != tt.want {
			t.Errorf("rendered %q, want %q", tt.got, tt.want)
		}
	}
}

// TestRenderMilliIsExact: every rendering parses back to the integer it came
// from — nothing is rounded away.
func TestRenderMilliIsExact(t *testing.T) {
	for _, milli := range []uint64{0, 1, 9, 10, 99, 100, 999, 1000, 1001, 2333, 12340, 999999} {
		text := RenderMilli(milli)
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || uint64(math.Round(f*1000)) != milli {
			t.Errorf("RenderMilli(%d) = %q", milli, text)
		}
	}
}

// TestPresentInListsTheRepetitionsTheCountsCameFrom: for every behavior, the
// repetitions listed per side are exactly as many as its presence count, in
// index order, and a repetition that did not complete is never listed.
func TestPresentInListsTheRepetitionsTheCountsCameFrom(t *testing.T) {
	inputs := []repetitionInput{
		completedInput(SideReference, 1, "a", "b"),
		completedInput(SideReference, 2, "a"),
		completedInput(SideReference, 3, "a", "c"),
		completedInput(SideCandidate, 1, "b"),
		{evidence: RepetitionEvidence{Side: SideCandidate, Index: 2, RunID: "candidate-2", Status: RunFailed}},
		completedInput(SideCandidate, 3, "a", "b", "c"),
	}
	comparison, err := reduceRepeated(repeatedTestLimits(1, 0), 3, inputs)
	if err != nil {
		t.Fatal(err)
	}
	present := presentIn(comparison.Behaviors, inputs)
	if len(present) != len(comparison.Behaviors) {
		t.Fatalf("%d present_in for %d behaviors", len(present), len(comparison.Behaviors))
	}
	for i, b := range comparison.Behaviors {
		p := present[i]
		if uint64(len(p.Reference)) != b.ReferenceRunsPresent || uint64(len(p.Candidate)) != b.CandidateRunsPresent {
			t.Errorf("%s: present_in %d/%d, counts %d/%d", b.FingerprintID,
				len(p.Reference), len(p.Candidate), b.ReferenceRunsPresent, b.CandidateRunsPresent)
		}
		for side, reps := range map[ComparisonSide][]ScenarioRepetition{SideReference: p.Reference, SideCandidate: p.Candidate} {
			for j, r := range reps {
				if r.Side != side || r.RunID == "" || (j > 0 && reps[j-1].Index >= r.Index) {
					t.Errorf("%s %s: %+v", b.FingerprintID, side, reps)
				}
				if r.Index == 2 && side == SideCandidate {
					t.Errorf("%s lists the failed repetition", b.FingerprintID)
				}
			}
		}
	}
	if b := present[0]; len(b.Reference) != 3 || b.Candidate[0].Index != 3 {
		t.Errorf("behavior a: %+v", b)
	}
}
