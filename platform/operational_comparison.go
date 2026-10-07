package platform

// Latency, error and token comparison (task 087).
//
// Three fixed-shape sections on a comparison, each a reference side and a
// candidate side summed from those runs' behavior entries. Run-level evidence
// is the sum over the run's behaviors: a comparison is built only from complete
// snapshots, so every record a run accepted is in exactly one entry and the sum
// is the run.
//
// A section is comparable only when both sides carry its evidence. When either
// does not, the section says which, from a closed set of reasons, and carries
// no delta at all — a delta computed against "nothing was measured" would read
// as a change nobody observed. That is 082's criterion 2, enforced by the
// shape: the field does not exist, so it cannot be rendered.
//
// Integer evidence only (ADR 0029): sums, counts, extrema and fixed-boundary
// buckets. No mean, no percentile, no rate. A delta is candidate minus
// reference of the same counter, signed.

import "fmt"

// OperationalUnavailability says which side of a section carried no evidence.
// The empty value means the section is comparable.
type OperationalUnavailability string

const (
	ReferenceUnavailable OperationalUnavailability = "reference_unavailable"
	CandidateUnavailable OperationalUnavailability = "candidate_unavailable"
	BothUnavailable      OperationalUnavailability = "both_unavailable"
)

func unavailability(reference, candidate bool) OperationalUnavailability {
	switch {
	case reference && candidate:
		return ""
	case candidate:
		return ReferenceUnavailable
	case reference:
		return CandidateUnavailable
	default:
		return BothUnavailable
	}
}

// LatencyEvidence is one side's durations: the fixed-boundary buckets and 084's
// statistics. Latency is descriptive: through a model-driven agent the model
// dominates it, and nothing here attributes a change to the change under test.
type LatencyEvidence struct {
	RunsWithEvidence uint64
	Buckets          DurationBuckets
	Durations        DurationSummary
}

// Available reports whether any duration was measured.
func (e LatencyEvidence) Available() bool { return e.Durations.Count > 0 }

// ErrorEvidence is one side's span statuses, HTTP status classes and 429s.
type ErrorEvidence struct {
	RunsWithEvidence uint64
	SpanStatus       SpanStatusCounts
	HTTPStatus       HTTPStatusClassCounts
	HTTP429          uint64
}

// Available reports whether any observation stated a span status or an HTTP
// status code. Unavailable span statuses and status codes are not evidence of
// success, so a side made only of them has nothing to compare.
func (e ErrorEvidence) Available() bool {
	return e.SpanStatus.Unset+e.SpanStatus.OK+e.SpanStatus.Error > 0 || e.HTTPStatus.Stated() > 0
}

// TokenEvidence is one side's token sums.
type TokenEvidence struct {
	RunsWithEvidence uint64
	Tokens           TokenCounts
}

// Available reports whether any observation reported a token count.
func (e TokenEvidence) Available() bool { return e.Tokens.Observed > 0 }

// LatencyComparison is the latency section.
type LatencyComparison struct {
	Reference, Candidate LatencyEvidence
	Unavailable          OperationalUnavailability
}

// Comparable reports whether both sides measured durations.
func (c LatencyComparison) Comparable() bool { return c.Unavailable == "" }

// ErrorComparison is the errors section.
type ErrorComparison struct {
	Reference, Candidate ErrorEvidence
	Unavailable          OperationalUnavailability
}

// Comparable reports whether both sides stated any outcome.
func (c ErrorComparison) Comparable() bool { return c.Unavailable == "" }

// TokenComparison is the tokens section.
type TokenComparison struct {
	Reference, Candidate TokenEvidence
	Unavailable          OperationalUnavailability
}

// Comparable reports whether both sides reported tokens.
func (c TokenComparison) Comparable() bool { return c.Unavailable == "" }

// OperationalComparison is the three sections together.
type OperationalComparison struct {
	Latency LatencyComparison
	Errors  ErrorComparison
	Tokens  TokenComparison
}

// operationalSide accumulates one side over one or more runs: the summed
// summary and, per section, how many runs carried that section's evidence.
type operationalSide struct {
	sum                               OperationalSummary
	latencyRuns, errorRuns, tokenRuns uint64
}

// addRun folds one run's entries into the side.
func (s *operationalSide) addRun(entries []BehaviorEntry) error {
	var run OperationalSummary
	for _, e := range entries {
		next, err := run.Add(e.Operational)
		if err != nil {
			return fmt.Errorf("summing operational evidence: %w", err)
		}
		run = next
	}
	sum, err := s.sum.Add(run)
	if err != nil {
		return fmt.Errorf("summing operational evidence: %w", err)
	}
	s.sum = sum
	if (LatencyEvidence{Durations: run.Durations}).Available() {
		s.latencyRuns++
	}
	if (ErrorEvidence{SpanStatus: run.SpanStatus, HTTPStatus: run.HTTPStatus}).Available() {
		s.errorRuns++
	}
	if (TokenEvidence{Tokens: run.Tokens}).Available() {
		s.tokenRuns++
	}
	return nil
}

func (s operationalSide) latency() LatencyEvidence {
	return LatencyEvidence{RunsWithEvidence: s.latencyRuns, Buckets: s.sum.Buckets, Durations: s.sum.Durations}
}

func (s operationalSide) errors() ErrorEvidence {
	return ErrorEvidence{RunsWithEvidence: s.errorRuns, SpanStatus: s.sum.SpanStatus,
		HTTPStatus: s.sum.HTTPStatus, HTTP429: s.sum.HTTP429}
}

func (s operationalSide) tokens() TokenEvidence {
	return TokenEvidence{RunsWithEvidence: s.tokenRuns, Tokens: s.sum.Tokens}
}

// compareOperational builds the three sections from two accumulated sides.
func compareOperational(reference, candidate operationalSide) OperationalComparison {
	rl, cl := reference.latency(), candidate.latency()
	re, ce := reference.errors(), candidate.errors()
	rt, ct := reference.tokens(), candidate.tokens()
	return OperationalComparison{
		Latency: LatencyComparison{Reference: rl, Candidate: cl,
			Unavailable: unavailability(rl.Available(), cl.Available())},
		Errors: ErrorComparison{Reference: re, Candidate: ce,
			Unavailable: unavailability(re.Available(), ce.Available())},
		Tokens: TokenComparison{Reference: rt, Candidate: ct,
			Unavailable: unavailability(rt.Available(), ct.Available())},
	}
}

// compareOperationalEntries is the pairwise case: one run per side.
func compareOperationalEntries(reference, candidate []BehaviorEntry) (OperationalComparison, error) {
	var ref, cand operationalSide
	if err := ref.addRun(reference); err != nil {
		return OperationalComparison{}, err
	}
	if err := cand.addRun(candidate); err != nil {
		return OperationalComparison{}, err
	}
	return compareOperational(ref, cand), nil
}

// SignedCount is a signed difference of two uint64 counters: candidate minus
// reference. Sign and magnitude, because the difference of two uint64 values
// does not fit an int64.
type SignedCount struct {
	Negative  bool
	Magnitude uint64
}

// Difference returns candidate - reference.
func Difference(candidate, reference uint64) SignedCount {
	if candidate >= reference {
		return SignedCount{Magnitude: candidate - reference}
	}
	return SignedCount{Negative: true, Magnitude: reference - candidate}
}

// String renders canonical signed decimal: "0", "12", "-12".
func (s SignedCount) String() string {
	if s.Negative && s.Magnitude != 0 {
		return fmt.Sprintf("-%d", s.Magnitude)
	}
	return fmt.Sprintf("%d", s.Magnitude)
}
