package platform

// Operational evidence aggregated per evaluation run: how long observations
// took, and what their producers said about success.
//
// Task 084. Both summaries are O(1) in the number of records, like everything
// else on EvaluationAggregate — no slice, no map, no retained observation. Per
// observation retention is task 067's, and adding it here would be the
// workaround 084 explicitly refuses.
//
// Neither type computes a rate, a percentile or a verdict. They publish counts
// so a caller can choose its own denominator, which is the same "evidence, not
// verdicts" line the decision and risk counters already hold.

import (
	"fmt"
	"math"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// DurationSummary is the descriptive statistics of observed span durations,
// in nanoseconds, plus how many observations had none.
//
// Integer rather than the float64 MetricSummary the score signals use. Those
// are bounded to [0,1] where a float is exact enough; a nanosecond sum is not,
// and would start losing whole microseconds past 2^53 — about 104 days of
// summed span time, which a busy run reaches.
type DurationSummary struct {
	// Count is how many observations carried a measured duration.
	Count uint64

	// Unobserved is how many did not.
	//
	// Reported rather than derived, so "no duration was measured" can never be
	// read as "the duration was zero". Count+Unobserved equals the aggregate's
	// RecordCount, which is what makes the denominator explicit.
	Unobserved uint64

	// Sum is the total observed span time in nanoseconds.
	//
	// **Not the run's wall-clock latency, and must never be presented as one.**
	// Spans nest and run concurrently, so this double-counts a parent and its
	// children and routinely exceeds the elapsed time of the run it describes.
	Sum uint64

	Min uint64
	Max uint64
}

// Mean returns the arithmetic mean in nanoseconds, and false when nothing was
// observed.
//
// The bool is the same guard MetricSummary.Mean uses, for the same reason:
// returning 0 would make "nothing measured" indistinguishable from "everything
// was instantaneous", and those lead to opposite conclusions.
func (s DurationSummary) Mean() (uint64, bool) {
	if s.Count == 0 {
		return 0, false
	}
	return s.Sum / s.Count, true
}

// observe folds one measured duration in.
//
// Overflow is refused rather than wrapped, the same contract every counter on
// the aggregate has: a summary that silently restarted its sum would report a
// smaller total for a longer run.
func (s DurationSummary) observe(nanos uint64) (DurationSummary, error) {
	if s.Count == math.MaxUint64 {
		return s, fmt.Errorf("%w: duration count is already at %d", ErrAggregateOverflow, s.Count)
	}
	if s.Sum > math.MaxUint64-nanos {
		return s, fmt.Errorf("%w: duration sum would exceed %d nanoseconds",
			ErrAggregateOverflow, uint64(math.MaxUint64))
	}
	if s.Count == 0 {
		return DurationSummary{Count: 1, Unobserved: s.Unobserved, Sum: nanos, Min: nanos, Max: nanos}, nil
	}
	s.Count++
	s.Sum += nanos
	s.Min = min(s.Min, nanos)
	s.Max = max(s.Max, nanos)
	return s, nil
}

// skip folds in one observation that carried no duration.
func (s DurationSummary) skip() (DurationSummary, error) {
	if s.Unobserved == math.MaxUint64 {
		return s, fmt.Errorf("%w: unobserved duration count is already at %d",
			ErrAggregateOverflow, s.Unobserved)
	}
	s.Unobserved++
	return s, nil
}

// SpanStatusCounts is how many observations carried each status.
//
// Four counters rather than an error rate, and the split is the point.
// OpenTelemetry's status defaults to UNSET and most instrumentation never sets
// OK, so folding Unset or Unavailable into "not an error" would let a run of
// entirely unstatused spans report a clean bill of health. A caller wanting a
// rate chooses its own denominator from these, and has to say which one.
type SpanStatusCounts struct {
	// Unavailable is observations whose record stated no status at all.
	Unavailable uint64

	// Unset is observations whose producer explicitly expressed no opinion.
	Unset uint64

	OK    uint64
	Error uint64
}

// Total is every counted observation, and equals the aggregate's RecordCount.
func (c SpanStatusCounts) Total() uint64 {
	return c.Unavailable + c.Unset + c.OK + c.Error
}

// Stated is the observations whose producer actually expressed an outcome.
//
// The honest denominator for an error rate: Error/Stated answers "of the calls
// that said whether they worked, how many failed", where Error/Total would
// dilute the answer with every span that said nothing.
func (c SpanStatusCounts) Stated() uint64 { return c.OK + c.Error }

// count folds one record's status in, refusing an unrecognized value.
//
// Refused rather than bucketed, exactly as the decision and risk counters do: a
// status this build does not know is a record it cannot summarize honestly, and
// inventing an "other" bucket would preserve the unknown under a friendlier name.
func (c SpanStatusCounts) count(status event.SpanStatus, eventID string) (SpanStatusCounts, error) {
	target := &c.Unavailable
	switch status {
	case event.StatusUnavailable:
	case event.StatusUnset:
		target = &c.Unset
	case event.StatusOK:
		target = &c.OK
	case event.StatusError:
		target = &c.Error
	default:
		return c, fmt.Errorf("%w: event %s has unrecognized span status %s",
			ErrInvalidDecisionRecord, preview(eventID), preview(string(status)))
	}
	if *target == math.MaxUint64 {
		return c, fmt.Errorf("%w: span status counter is already at %d",
			ErrAggregateOverflow, *target)
	}
	*target++
	return c, nil
}

// observeOperational folds one record's duration and status into the two
// summaries, or reports why it could not.
//
// A record whose duration text is present but not canonical decimal is refused
// rather than treated as unobserved: this code writes that field, so a value it
// cannot parse was not produced by an engine following the contract, and
// guessing would turn tampering into a silently smaller run.
func observeOperational(
	durations DurationSummary, statuses SpanStatusCounts, record trustvian.DecisionRecord,
) (DurationSummary, SpanStatusCounts, error) {
	nanos, observed := record.DurationNanosValue()
	switch {
	case record.DurationNanos != "" && !observed:
		return durations, statuses, fmt.Errorf(
			"%w: event %s reports duration %s, which is not canonical decimal nanoseconds",
			ErrInvalidDecisionRecord, preview(record.EventID), preview(record.DurationNanos))
	case observed:
		next, err := durations.observe(nanos)
		if err != nil {
			return durations, statuses, err
		}
		durations = next
	default:
		next, err := durations.skip()
		if err != nil {
			return durations, statuses, err
		}
		durations = next
	}

	statuses, err := statuses.count(record.SpanStatus, record.EventID)
	if err != nil {
		return durations, statuses, err
	}
	return durations, statuses, nil
}
