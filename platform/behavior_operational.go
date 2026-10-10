package platform

// Per-behavior operational evidence (task 087).
//
// Task 084 aggregated duration and span status per run. That answers "did the
// run get slower", not "which call got slower" — and after a model, prompt or
// tool change the second is the question. So each BehaviorEntry carries one
// fixed-shape summary of the observations it counted: a fixed-boundary duration
// histogram beside 084's duration statistics, 084's span-status counts, HTTP
// status classes with 429 counted separately, and token sums.
//
// Bounded by construction: a snapshot holds at most maxBehaviorEntries entries,
// and this adds a fixed number of integers to each. Every set of counters
// partitions the entry's observations, which is what lets "no evidence" be told
// from "a measured zero" without a nullable column: an observation that stated
// nothing is counted as unobserved or unavailable, never as zero.
//
// No percentile, no mean, no rate, no float. A bucket count is an integer per
// fixed interval, order-independent and reproducible, which is the property ADR
// 0029 asks of evidence; an interpolated percentile is none of those.

import (
	"errors"
	"fmt"
	"math"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// DurationBucketCount is how many duration buckets a summary holds: ten
// upper-inclusive boundaries and one bucket above the last.
const DurationBucketCount = 11

// durationBucketUpperMillis are the upper-inclusive bucket boundaries, in
// integer milliseconds. The eleventh bucket is everything above 10 000 ms.
//
// Chosen to separate the cases a developer asks about — sub-millisecond local
// calls, ordinary network calls, slow model calls, and calls that now take ten
// seconds — and fixed: a boundary change is a schema change, not a setting,
// because stored counts cannot be re-bucketed.
var durationBucketUpperMillis = [DurationBucketCount - 1]uint64{1, 5, 10, 50, 100, 250, 500, 1000, 2500, 10000}

// DurationBucketUpperMillis returns the ten upper-inclusive boundaries, in
// milliseconds. A copy, so a caller cannot move a boundary.
func DurationBucketUpperMillis() [DurationBucketCount - 1]uint64 { return durationBucketUpperMillis }

// DurationBuckets counts measured durations per bucket. Index i counts
// durations at most durationBucketUpperMillis[i] milliseconds (and above the
// boundary before it); the last index counts durations above 10 000 ms.
type DurationBuckets [DurationBucketCount]uint64

// durationBucket is the bucket a duration of nanos falls in. Upper-inclusive:
// exactly 1 ms is in the first bucket and 1 ms plus one nanosecond is in the
// second. A measured 0 ns is a measurement and lands in the first bucket.
func durationBucket(nanos uint64) int {
	for i, upper := range durationBucketUpperMillis {
		if nanos <= upper*1_000_000 {
			return i
		}
	}
	return DurationBucketCount - 1
}

// HTTPStatusClassCounts counts observations by the class of the HTTP status
// code their span reported, and those whose span reported none.
//
// Unavailable is never inferred from span status: an UNSET or ERROR span says
// nothing about HTTP, and a span from a non-HTTP call has no status code at all.
type HTTPStatusClassCounts struct {
	Informational uint64 // 1xx
	Success       uint64 // 2xx
	Redirection   uint64 // 3xx
	ClientError   uint64 // 4xx
	ServerError   uint64 // 5xx
	Unavailable   uint64
}

// Stated is the observations whose span reported a status code.
func (c HTTPStatusClassCounts) Stated() uint64 {
	return c.Informational + c.Success + c.Redirection + c.ClientError + c.ServerError
}

// TokenCounts sums the token usage observations reported, and counts how many
// reported any.
//
// Unsplit is a total reported without an input/output split, kept apart rather
// than divided: the producer did not say how it splits.
type TokenCounts struct {
	Input   uint64
	Output  uint64
	Unsplit uint64

	// Observed is how many observations reported any token count, and
	// Unobserved how many reported none. Together they are the entry's
	// observations, so a sum of 0 over 0 observed reads as unavailable rather
	// than as zero tokens.
	Observed   uint64
	Unobserved uint64
}

// OperationalSummary is one behavior's operational evidence.
type OperationalSummary struct {
	Buckets    DurationBuckets
	Durations  DurationSummary
	SpanStatus SpanStatusCounts
	HTTPStatus HTTPStatusClassCounts

	// HTTP429 is the 429 responses among HTTPStatus.ClientError, counted on
	// their own because a rate-limited target is a different finding from one
	// refusing malformed requests.
	HTTP429 uint64

	Tokens TokenCounts
}

// OperationalFacts are what a record's ingest envelope said about its span
// beyond the record itself: the HTTP status code and the token usage. Neither
// is on DecisionRecord, and neither is engine evidence (task 087).
type OperationalFacts struct {
	// HTTPStatusCode is 0 when the span reported none, else 100-599.
	HTTPStatusCode uint16

	Usage event.Usage

	// Fidelity and Layer are what the envelope stated about how the record's
	// behavior was named (tasks 075 and 083), counted per behavior since task
	// 081. Both zero means the envelope stated neither, which is counted
	// unrecorded — never transport.
	Fidelity event.Fidelity
	Layer    event.Layer
}

// Validate refuses facts no adapter following the contract produces — reported
// as an invalid record, since they arrive with one — a status
// code outside 100-599, or a total reported beside an input or output part.
func (f OperationalFacts) Validate() error {
	if f.Fidelity != "" && !f.Fidelity.Valid() {
		return fmt.Errorf("%w: fidelity %q is not transport or semantic", ErrInvalidDecisionRecord, f.Fidelity)
	}
	if f.Layer != event.LayerUnspecified && !f.Layer.Valid() {
		return fmt.Errorf("%w: behavior layer %q is not model, tool, retrieval or transport",
			ErrInvalidDecisionRecord, f.Layer)
	}
	if err := validateFidelityPair(f.Fidelity, f.Layer); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidDecisionRecord, err)
	}
	if f.HTTPStatusCode != 0 && (f.HTTPStatusCode < 100 || f.HTTPStatusCode > 599) {
		return fmt.Errorf("%w: http status code %d is outside 100-599", ErrInvalidDecisionRecord, f.HTTPStatusCode)
	}
	if f.Usage.HasUnsplit && (f.Usage.HasInput || f.Usage.HasOutput) {
		return fmt.Errorf("%w: an unsplit token total is reported only when input and output are absent",
			ErrInvalidDecisionRecord)
	}
	// The adapters' own plausibility bound. Past it, a handful of records
	// could overflow a later sum and refuse every comparison of the run.
	for _, part := range []struct {
		has   bool
		value uint64
	}{{f.Usage.HasInput, f.Usage.Input}, {f.Usage.HasOutput, f.Usage.Output}, {f.Usage.HasUnsplit, f.Usage.Unsplit}} {
		if part.has && part.value > event.MaxTokenCount {
			return fmt.Errorf("%w: a token count of %d exceeds the per-observation maximum %d",
				ErrInvalidDecisionRecord, part.value, event.MaxTokenCount)
		}
	}
	return nil
}

// unavailableOperational is the summary of observations that stated nothing:
// what a behavior recorded before schema 11 holds, and what the migration
// writes for it. Every "nothing was stated" counter equals the observations.
func unavailableOperational(observations uint64) OperationalSummary {
	return OperationalSummary{
		Durations:  DurationSummary{Unobserved: observations},
		SpanStatus: SpanStatusCounts{Unavailable: observations},
		HTTPStatus: HTTPStatusClassCounts{Unavailable: observations},
		Tokens:     TokenCounts{Unobserved: observations},
	}
}

// observe folds one observation in, or reports why it could not, leaving the
// receiver unchanged either way.
//
// The duration and span status come from the record and are validated exactly
// as the run aggregate validates them; facts must already be valid.
func (s OperationalSummary) observe(record trustvian.DecisionRecord, facts OperationalFacts) (OperationalSummary, error) {
	durations, statuses, err := observeOperational(s.Durations, s.SpanStatus, record)
	if err != nil {
		if errors.Is(err, ErrAggregateOverflow) {
			return s, fmt.Errorf("%w: %w", ErrBehaviorOverflow, err)
		}
		return s, err
	}
	next := s
	next.Durations, next.SpanStatus = durations, statuses

	if record.DurationNanos != "" {
		// Validated by observeOperational above, so this parse cannot fail.
		nanos, _ := record.ValidateDurationNanos()
		if err := addCount(&next.Buckets[durationBucket(nanos)], 1, "duration bucket"); err != nil {
			return s, err
		}
	}

	class := &next.HTTPStatus.Unavailable
	switch code := facts.HTTPStatusCode; {
	case code == 0:
	case code < 200:
		class = &next.HTTPStatus.Informational
	case code < 300:
		class = &next.HTTPStatus.Success
	case code < 400:
		class = &next.HTTPStatus.Redirection
	case code < 500:
		class = &next.HTTPStatus.ClientError
	default:
		class = &next.HTTPStatus.ServerError
	}
	if err := addCount(class, 1, "http status class"); err != nil {
		return s, err
	}
	if facts.HTTPStatusCode == 429 {
		if err := addCount(&next.HTTP429, 1, "http 429"); err != nil {
			return s, err
		}
	}

	u := facts.Usage
	if !u.Observed() {
		if err := addCount(&next.Tokens.Unobserved, 1, "tokens unobserved"); err != nil {
			return s, err
		}
		return next, nil
	}
	for _, part := range []struct {
		field *uint64
		value uint64
		name  string
	}{
		{&next.Tokens.Input, u.Input, "input tokens"},
		{&next.Tokens.Output, u.Output, "output tokens"},
		{&next.Tokens.Unsplit, u.Unsplit, "unsplit tokens"},
		{&next.Tokens.Observed, 1, "tokens observed"},
	} {
		if err := addCount(part.field, part.value, part.name); err != nil {
			return s, err
		}
	}
	return next, nil
}

// addCount adds n to *field, refusing to wrap. Overflow is refused rather than
// wrapped, as every counter on the aggregate refuses it: a sum that silently
// restarted would report fewer tokens for a longer run.
func addCount(field *uint64, n uint64, what string) error {
	if *field > math.MaxUint64-n {
		return fmt.Errorf("%w: %s would exceed %d", ErrBehaviorOverflow, what, uint64(math.MaxUint64))
	}
	*field += n
	return nil
}

// Add returns the counter-wise sum of two summaries, for a run's total over
// its behaviors or a target's over the behaviors that reach it. Min and Max are
// the extrema of both; overflow is an error.
func (s OperationalSummary) Add(o OperationalSummary) (OperationalSummary, error) {
	out := s
	var err error
	add := func(field *uint64, n uint64) {
		if err == nil {
			err = addCount(field, n, "operational summary")
		}
	}
	for i := range out.Buckets {
		add(&out.Buckets[i], o.Buckets[i])
	}
	add(&out.Durations.Count, o.Durations.Count)
	add(&out.Durations.Unobserved, o.Durations.Unobserved)
	add(&out.Durations.Sum, o.Durations.Sum)
	add(&out.SpanStatus.Unavailable, o.SpanStatus.Unavailable)
	add(&out.SpanStatus.Unset, o.SpanStatus.Unset)
	add(&out.SpanStatus.OK, o.SpanStatus.OK)
	add(&out.SpanStatus.Error, o.SpanStatus.Error)
	add(&out.HTTPStatus.Informational, o.HTTPStatus.Informational)
	add(&out.HTTPStatus.Success, o.HTTPStatus.Success)
	add(&out.HTTPStatus.Redirection, o.HTTPStatus.Redirection)
	add(&out.HTTPStatus.ClientError, o.HTTPStatus.ClientError)
	add(&out.HTTPStatus.ServerError, o.HTTPStatus.ServerError)
	add(&out.HTTPStatus.Unavailable, o.HTTPStatus.Unavailable)
	add(&out.HTTP429, o.HTTP429)
	add(&out.Tokens.Input, o.Tokens.Input)
	add(&out.Tokens.Output, o.Tokens.Output)
	add(&out.Tokens.Unsplit, o.Tokens.Unsplit)
	add(&out.Tokens.Observed, o.Tokens.Observed)
	add(&out.Tokens.Unobserved, o.Tokens.Unobserved)
	if err != nil {
		return s, err
	}
	// Extrema of the observed durations only: a side with none contributes
	// none, so its zero Min must not become the combined minimum.
	switch {
	case s.Durations.Count == 0:
		out.Durations.Min, out.Durations.Max = o.Durations.Min, o.Durations.Max
	case o.Durations.Count != 0:
		out.Durations.Min = min(s.Durations.Min, o.Durations.Min)
		out.Durations.Max = max(s.Durations.Max, o.Durations.Max)
	}
	return out, nil
}

// validateOperationalSummary refuses a summary AddRecord could not have
// produced for an entry of `observations`: the restore-side half of the
// invariants observe keeps on write. Nothing is clamped or repaired.
func validateOperationalSummary(s OperationalSummary, observations uint64) error {
	// 084's own rules: duration counts and span-status counts partition the
	// observations, and the statistics agree with each other.
	if err := validateRestoredOperational(s.Durations, s.SpanStatus, observations); err != nil {
		return err
	}

	bucketTotal, err := sumNoOverflow(s.Buckets[:])
	if err != nil {
		return errors.New("duration buckets overflow")
	}
	if bucketTotal != s.Durations.Count {
		return fmt.Errorf("duration buckets total %d, observed durations are %d", bucketTotal, s.Durations.Count)
	}
	if s.Durations.Count > 0 {
		// The sum lies between what the bucket counts allow: at least each
		// count times its bucket's lower edge, at most each count times its
		// upper edge (the last bucket's edge is the per-observation maximum).
		// Computed with an overflow flag: a lower bound past uint64 cannot be
		// met, and an upper bound past it constrains nothing.
		if err := checkSumAgainstBuckets(s.Buckets, s.Durations.Sum); err != nil {
			return err
		}
		// The extrema are observed values, so each lies in a bucket that
		// counted something, and no bucket outside their range counted
		// anything.
		lo, hi := durationBucket(s.Durations.Min), durationBucket(s.Durations.Max)
		for i, n := range s.Buckets {
			switch {
			case (i == lo || i == hi) && n == 0:
				return fmt.Errorf("duration bucket %d is empty but holds an observed extremum", i)
			case (i < lo || i > hi) && n != 0:
				return fmt.Errorf("duration bucket %d counts %d outside the observed range", i, n)
			}
		}
	}

	h := s.HTTPStatus
	httpTotal, err := sumNoOverflow([]uint64{
		h.Informational, h.Success, h.Redirection, h.ClientError, h.ServerError, h.Unavailable})
	if err != nil {
		return errors.New("http status class counts overflow")
	}
	if httpTotal != observations {
		return fmt.Errorf("http status class counts total %d, observations are %d", httpTotal, observations)
	}
	if s.HTTP429 > h.ClientError {
		return fmt.Errorf("http 429 count %d exceeds the 4xx count %d", s.HTTP429, h.ClientError)
	}

	t := s.Tokens
	tokenTotal, err := sumNoOverflow([]uint64{t.Observed, t.Unobserved})
	if err != nil {
		return errors.New("token observation counts overflow")
	}
	if tokenTotal != observations {
		return fmt.Errorf("token observation counts total %d, observations are %d", tokenTotal, observations)
	}
	if t.Observed == 0 && (t.Input != 0 || t.Output != 0 || t.Unsplit != 0) {
		return fmt.Errorf("no observation reported tokens but sums are input %d, output %d, unsplit %d",
			t.Input, t.Output, t.Unsplit)
	}
	return nil
}

// checkSumAgainstBuckets refuses a duration sum the bucket counts cannot
// produce.
func checkSumAgainstBuckets(buckets DurationBuckets, sum uint64) error {
	var lower, upper uint64
	lowerOverflow, upperOverflow := false, false
	for i, n := range buckets {
		lo := uint64(0)
		if i > 0 {
			lo = durationBucketUpperMillis[i-1]*1_000_000 + 1
		}
		hi := event.MaxDurationNanos
		if i < len(durationBucketUpperMillis) {
			hi = durationBucketUpperMillis[i] * 1_000_000
		}
		if v, ok := addMulNoOverflow(lower, n, lo); ok {
			lower = v
		} else {
			lowerOverflow = true
		}
		if v, ok := addMulNoOverflow(upper, n, hi); ok {
			upper = v
		} else {
			upperOverflow = true
		}
	}
	if lowerOverflow || sum < lower {
		return fmt.Errorf("duration sum %d is below what the bucket counts imply", sum)
	}
	if !upperOverflow && sum > upper {
		return fmt.Errorf("duration sum %d is above what the bucket counts imply", sum)
	}
	return nil
}
