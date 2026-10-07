package platform

// The comparison rule table (task 106, ADR 0064): every rule fires exactly on
// its stated evidence, stays silent without it, and changes nothing else.

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func suggestionsFor(c RepeatedEvaluationComparison, rule string) []Suggestion {
	var out []Suggestion
	for _, s := range c.Suggestions {
		if s.Rule == rule {
			out = append(out, s)
		}
	}
	return out
}

// with429 marks a run's entries for one target as answered with HTTP status
// codes: n of them 429, the rest 2xx.
func with429(in repetitionInput, target string, n uint64) repetitionInput {
	in.entries = slices.Clone(in.entries) // never the caller's backing array
	for i, e := range in.entries {
		if e.Behavior.TargetName != target {
			continue
		}
		s := unavailableOperational(e.Observations)
		s.HTTPStatus = HTTPStatusClassCounts{ClientError: n, Success: e.Observations - n}
		s.HTTP429 = n
		in.entries[i].Operational = s
	}
	return in
}

func TestRenderRatio(t *testing.T) {
	for permille, want := range map[uint64]string{2400: "2.4×", 2000: "2.0×", 2999: "2.9×", 999: "0.9×", 0: "0.0×", 12345: "12.3×"} {
		if got := RenderRatio(permille); got != want {
			t.Errorf("RenderRatio(%d) = %q, want %q", permille, got, want)
		}
	}
}

func TestRuleAddedInAllRuns(t *testing.T) {
	refs, cands := frequencyFixture() // "new" is in 3/3 candidate runs and no reference run
	c, err := reduceRepeated(lenientLimits(), 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	got := suggestionsFor(c, "compare.added_in_all_runs")
	if len(got) != 1 || got[0].RuleVersion != 1 ||
		got[0].Text != "tool/op-new → t-n was added in 3/3 candidate runs. If this was expected, add it to "+
			"the scenario; otherwise investigate before promoting." {
		t.Fatalf("rule 1 = %+v", got)
	}
	// In 2 of 3 candidate runs: not every run, so it does not fire.
	cands[2] = runWith(SideCandidate, 3, map[string]uint64{"crm": 8})
	c, err = reduceRepeated(lenientLimits(), 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestionsFor(c, "compare.added_in_all_runs"); len(got) != 0 {
		t.Fatalf("rule 1 fired on 2/3: %+v", got)
	}
}

func TestRuleLostInHalf(t *testing.T) {
	refs, cands := frequencyFixture() // "send" is in 3/3 reference and 1/3 candidate runs
	c, err := reduceRepeated(lenientLimits(), 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	got := suggestionsFor(c, "compare.lost_in_half")
	if len(got) != 1 || !strings.Contains(got[0].Text, "is missing from 2 of 3 candidate runs") {
		t.Fatalf("rule 3 = %+v", got)
	}
	// In 2 of 3 candidate runs: 2 × 2 > 3, so it is lost but not lost in half.
	cands[1] = runWith(SideCandidate, 2, map[string]uint64{"crm": 9, "new": 1, "send": 1})
	c, err = reduceRepeated(lenientLimits(), 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestionsFor(c, "compare.lost_in_half"); len(got) != 0 {
		t.Fatalf("rule 3 fired at 2/3: %+v", got)
	}
	if !behaviorOf(t, c, "send").Lost {
		t.Error("send at 2/3 should still be lost")
	}
}

func TestRuleTargetLoadWith429(t *testing.T) {
	refs, cands := frequencyFixture() // t-c: 26 candidate calls over 9 reference calls, 2888 permille
	stated := func(cands []repetitionInput, n uint64) []repetitionInput {
		out := slices.Clone(cands)
		for i := range out {
			out[i] = with429(out[i], "t-c", n)
		}
		return out
	}
	tests := []struct {
		name  string
		cands []repetitionInput
		fires bool
	}{
		{"429s and twice the load", stated(cands, 1), true},
		{"status stated, no 429", stated(cands, 0), false},
		// The other two rows' evidence, unreported: no status code at all.
		{"no status evidence", cands, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := reduceRepeated(lenientLimits(), 3, append(slices.Clone(refs), tt.cands...))
			if err != nil {
				t.Fatal(err)
			}
			got := suggestionsFor(c, "compare.target_load_with_429")
			if (len(got) == 1) != tt.fires {
				t.Fatalf("rule 2 = %+v, want fires %v", got, tt.fires)
			}
			if tt.fires && got[0].Text != "t-c received 2.8× the reference's calls and returned 3 429 responses. "+
				"Check its rate limit." {
				t.Errorf("text = %q", got[0].Text)
			}
		})
	}

	// Below twice the load, 429s alone do not fire it: t-s goes 3 → 1.
	low := slices.Clone(cands)
	low[0] = with429(low[0], "t-s", 1)
	c, err := reduceRepeated(lenientLimits(), 3, append(slices.Clone(refs), low...))
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestionsFor(c, "compare.target_load_with_429"); len(got) != 0 {
		t.Fatalf("rule 2 fired below the threshold: %+v", got)
	}
}

// TestSuggestionsChangeNothingElse: removing the rule table's output leaves
// every check, the verdict and the evidence exactly as they were.
func TestSuggestionsChangeNothingElse(t *testing.T) {
	refs, cands := frequencyFixture()
	l := lenientLimits()
	l.MaxLostBehaviors = NewOptionalGateLimit(0)
	c, err := reduceRepeated(l, 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Suggestions) == 0 {
		t.Fatal("the fixture fires no rule; this test would prove nothing")
	}
	without := c
	without.Suggestions, without.SuggestionsTruncated = nil, false
	saved := comparisonRules
	comparisonRules = nil
	recomputed, err := reduceRepeated(l, 3, append(refs, cands...))
	comparisonRules = saved
	if err != nil {
		t.Fatal(err)
	}
	recomputed.Suggestions = nil
	if !reflect.DeepEqual(without, recomputed) {
		t.Fatal("the rule table changed something besides its own field")
	}
}

func TestComparisonSuggestionsAreBounded(t *testing.T) {
	var ref, cand []repetitionInput
	calls := map[string]uint64{}
	for i := range MaxComparisonSuggestions + 5 {
		calls[fmt.Sprintf("a%03d", i)] = 1
	}
	ref = append(ref, runWith(SideReference, 1, map[string]uint64{"x": 1}))
	cand = append(cand, runWith(SideCandidate, 1, calls))
	c, err := reduceRepeated(RepeatedEvaluationGateLimits{AddedCandidatePresenceMinimum: 1,
		MaxRepeatedAddedBehaviors: 1000, MaxBlockDecisionsPerRun: 1, MaxCriticalRiskObservationsPerRun: 1},
		1, append(ref, cand...))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Suggestions) != MaxComparisonSuggestions || !c.SuggestionsTruncated {
		t.Fatalf("%d suggestions, truncated %v", len(c.Suggestions), c.SuggestionsTruncated)
	}
}
