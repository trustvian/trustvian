package platform

// Task 087's latency, errors and tokens sections over values.

import (
	"fmt"
	"math"
	"testing"
)

// entryWith is one behavior entry carrying the given operational summary.
func entryWith(fp string, observations uint64, s OperationalSummary) BehaviorEntry {
	return BehaviorEntry{FingerprintID: fp, Behavior: internalRecord("e", fp, "op-"+fp).Behavior,
		Observations: observations, Operational: s}
}

// measured is one observation with a duration of ms milliseconds, an OK span,
// a status code and token usage.
func measured(ms uint64, code uint16, in, out uint64) OperationalSummary {
	var s OperationalSummary
	nanos := ms * 1_000_000
	s.Buckets[durationBucket(nanos)] = 1
	s.Durations = DurationSummary{Count: 1, Sum: nanos, Min: nanos, Max: nanos}
	s.SpanStatus.OK = 1
	switch {
	case code == 0:
		s.HTTPStatus.Unavailable = 1
	case code >= 500:
		s.HTTPStatus.ServerError = 1
	case code >= 400:
		s.HTTPStatus.ClientError = 1
	default:
		s.HTTPStatus.Success = 1
	}
	if code == 429 {
		s.HTTP429 = 1
	}
	s.Tokens = TokenCounts{Input: in, Output: out, Observed: 1}
	return s
}

func TestPairwiseSectionsSumTheRunsBehaviors(t *testing.T) {
	reference := []BehaviorEntry{
		entryWith("a", 1, measured(3, 200, 100, 10)),
		entryWith("b", 1, measured(40, 200, 0, 0)),
	}
	candidate := []BehaviorEntry{
		entryWith("a", 1, measured(3, 429, 300, 30)),
		entryWith("c", 1, measured(12_000, 200, 5, 5)),
	}
	got, err := compareOperationalEntries(reference, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Latency.Comparable() || !got.Errors.Comparable() || !got.Tokens.Comparable() {
		t.Fatalf("every section should be comparable: %+v", got)
	}
	if r := got.Latency.Reference; r.Durations.Count != 2 || r.Buckets[1] != 1 || r.Buckets[3] != 1 ||
		r.Durations.Min != 3_000_000 || r.Durations.Max != 40_000_000 {
		t.Errorf("reference latency = %+v", r)
	}
	if c := got.Latency.Candidate; c.Buckets[10] != 1 || c.Durations.Max != 12_000_000_000 {
		t.Errorf("candidate latency = %+v", c)
	}
	if got.Errors.Candidate.HTTP429 != 1 || got.Errors.Candidate.HTTPStatus.ClientError != 1 {
		t.Errorf("candidate errors = %+v", got.Errors.Candidate)
	}
	if got.Tokens.Reference.Tokens.Input != 100 || got.Tokens.Candidate.Tokens.Input != 305 {
		t.Errorf("tokens = %+v", got.Tokens)
	}
	if got.Tokens.Reference.RunsWithEvidence != 1 || got.Tokens.Candidate.RunsWithEvidence != 1 {
		t.Errorf("runs with evidence = %d/%d, want 1/1",
			got.Tokens.Reference.RunsWithEvidence, got.Tokens.Candidate.RunsWithEvidence)
	}
}

// TestUnavailableSectionsSayWhich: evidence that was not reported never reads
// as comparable, and the reason names the side that lacked it.
func TestUnavailableSectionsSayWhich(t *testing.T) {
	measuredEntries := []BehaviorEntry{entryWith("a", 1, measured(3, 200, 10, 1))}
	unreported := []BehaviorEntry{entryWith("a", 2, unavailableOperational(2))}
	tests := []struct {
		name                 string
		reference, candidate []BehaviorEntry
		want                 OperationalUnavailability
	}{
		{"both reported", measuredEntries, measuredEntries, ""},
		{"candidate unreported", measuredEntries, unreported, CandidateUnavailable},
		{"reference unreported", unreported, measuredEntries, ReferenceUnavailable},
		{"neither", unreported, unreported, BothUnavailable},
		{"an empty run", nil, measuredEntries, ReferenceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareOperationalEntries(tt.reference, tt.candidate)
			if err != nil {
				t.Fatal(err)
			}
			for name, reason := range map[string]OperationalUnavailability{
				"latency": got.Latency.Unavailable, "errors": got.Errors.Unavailable, "tokens": got.Tokens.Unavailable,
			} {
				if reason != tt.want {
					t.Errorf("%s reason = %q, want %q", name, reason, tt.want)
				}
			}
		})
	}
}

// TestErrorsAreAvailableFromEitherSource: a span status or a status code each
// suffice, and an unset span status is a stated outcome while an unavailable
// one is not.
func TestErrorsAreAvailableFromEitherSource(t *testing.T) {
	for _, tt := range []struct {
		name string
		e    ErrorEvidence
		want bool
	}{
		{"nothing stated", ErrorEvidence{SpanStatus: SpanStatusCounts{Unavailable: 3},
			HTTPStatus: HTTPStatusClassCounts{Unavailable: 3}}, false},
		{"an unset span status", ErrorEvidence{SpanStatus: SpanStatusCounts{Unset: 1}}, true},
		{"a status code only", ErrorEvidence{SpanStatus: SpanStatusCounts{Unavailable: 1},
			HTTPStatus: HTTPStatusClassCounts{Success: 1}}, true},
	} {
		if got := tt.e.Available(); got != tt.want {
			t.Errorf("%s: Available() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDifferenceIsSignedAndNeverWraps(t *testing.T) {
	for _, tt := range []struct {
		candidate, reference uint64
		want                 string
	}{
		{5, 3, "2"}, {3, 5, "-2"}, {7, 7, "0"},
		{math.MaxUint64, 0, "18446744073709551615"},
		{0, math.MaxUint64, "-18446744073709551615"},
	} {
		if got := Difference(tt.candidate, tt.reference).String(); got != tt.want {
			t.Errorf("Difference(%d, %d) = %s, want %s", tt.candidate, tt.reference, got, tt.want)
		}
	}
}

// TestRepeatedSectionsSumOverRunsAndCountRunsWithEvidence: each side is the
// sum of its completed repetitions, runs_with_evidence counts the ones that
// carried the section's evidence, and every ordering gives the same result.
func TestRepeatedSectionsSumOverRunsAndCountRunsWithEvidence(t *testing.T) {
	ref := func(i int, s OperationalSummary, observations uint64) repetitionInput {
		in := completedInput(SideReference, i, "a")
		in.entries = []BehaviorEntry{entryWith("a", observations, s)}
		return in
	}
	cand := func(i int, s OperationalSummary, observations uint64) repetitionInput {
		in := completedInput(SideCandidate, i, "a")
		in.entries = []BehaviorEntry{entryWith("a", observations, s)}
		return in
	}
	refs := []repetitionInput{ref(1, measured(3, 200, 10, 1), 1), ref(2, unavailableOperational(4), 4),
		ref(3, measured(8, 200, 20, 2), 1)}
	cands := []repetitionInput{cand(1, measured(3, 429, 30, 3), 1), cand(2, measured(4, 200, 0, 0), 1),
		cand(3, unavailableOperational(1), 1)}
	perms := [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}}
	var want string
	for _, rp := range perms {
		for _, cp := range perms {
			got, err := reduceRepeated(repeatedTestLimits(2, 0), 3, []repetitionInput{
				refs[rp[0]], refs[rp[1]], refs[rp[2]], cands[cp[0]], cands[cp[1]], cands[cp[2]]})
			if err != nil {
				t.Fatal(err)
			}
			shape := fmt.Sprintf("%+v", got.Operational)
			if want == "" {
				want = shape
				tok := got.Operational.Tokens
				if tok.Reference.Tokens.Input != 30 || tok.Reference.RunsWithEvidence != 2 ||
					tok.Reference.Tokens.Unobserved != 4 || tok.Candidate.RunsWithEvidence != 2 ||
					tok.Candidate.Tokens.Input != 30 {
					t.Fatalf("summed tokens = %+v", tok)
				}
				if got.Operational.Errors.Candidate.HTTP429 != 1 ||
					got.Operational.Errors.Candidate.RunsWithEvidence != 2 {
					t.Fatalf("summed errors = %+v", got.Operational.Errors.Candidate)
				}
			} else if shape != want {
				t.Fatalf("permutation %v / %v changed the operational sections", rp, cp)
			}
		}
	}
}

// TestRepeatedSumOverflowIsAnError: a sum past uint64 is refused, never
// wrapped into a small, plausible total.
func TestRepeatedSumOverflowIsAnError(t *testing.T) {
	huge := OperationalSummary{
		Durations: DurationSummary{Unobserved: 1}, SpanStatus: SpanStatusCounts{Unavailable: 1},
		HTTPStatus: HTTPStatusClassCounts{Unavailable: 1},
		Tokens:     TokenCounts{Input: math.MaxUint64, Observed: 1},
	}
	a := completedInput(SideReference, 1, "a")
	a.entries = []BehaviorEntry{entryWith("a", 1, huge)}
	b := completedInput(SideReference, 2, "a")
	b.entries = []BehaviorEntry{entryWith("a", 1, huge)}
	if _, err := reduceRepeated(repeatedTestLimits(1, 0), 2, []repetitionInput{
		a, b, completedInput(SideCandidate, 1, "a"), completedInput(SideCandidate, 2, "a"),
	}); err == nil {
		t.Fatal("an overflowing token sum was accepted")
	}
}
