package platform_test

// Operational aggregates (task 084): the two summaries, their invariants, and
// their overflow behaviour.

import (
	"errors"
	"math"
	"strconv"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// operationalRecord is a valid record carrying chosen operational evidence.
func operationalRecord(id, duration string, status event.SpanStatus) trustvian.DecisionRecord {
	record := validRecord()
	record.EventID = id
	record.DurationNanos = duration
	record.SpanStatus = status
	return record
}

func addAll(t *testing.T, a platform.EvaluationAggregate,
	records ...trustvian.DecisionRecord) platform.EvaluationAggregate {
	t.Helper()
	for _, record := range records {
		next, err := a.AddRecord(record)
		if err != nil {
			t.Fatalf("AddRecord(%s) error = %v", record.EventID, err)
		}
		a = next
	}
	return a
}

// TestDurationSummaryMixesKnownAndUnknown is the case the whole availability
// design exists for: measured values and missing ones in one run.
func TestDurationSummaryMixesKnownAndUnknown(t *testing.T) {
	a := addAll(t, newTestAggregate(t),
		operationalRecord("e1", "1000", event.StatusOK),
		operationalRecord("e2", "", event.StatusUnset),
		operationalRecord("e3", "0", event.StatusError),
		operationalRecord("e4", "3000", ""),
	)

	d := a.Durations()
	if d.Count != 3 {
		t.Errorf("Count = %d, want 3", d.Count)
	}
	if d.Unobserved != 1 {
		t.Errorf("Unobserved = %d, want 1", d.Unobserved)
	}
	if d.Sum != 4000 {
		t.Errorf("Sum = %d, want 4000", d.Sum)
	}
	// The measured zero participates in Min, which is the point of recording it.
	if d.Min != 0 {
		t.Errorf("Min = %d, want 0; a measured zero is a measurement", d.Min)
	}
	if d.Max != 3000 {
		t.Errorf("Max = %d, want 3000", d.Max)
	}

	mean, ok := d.Mean()
	if !ok {
		t.Fatal("Mean reported nothing observed")
	}
	if mean != 4000/3 {
		t.Errorf("Mean = %d, want %d", mean, 4000/3)
	}

	// The denominator is explicit: every record landed in exactly one bucket.
	if d.Count+d.Unobserved != a.RecordCount() {
		t.Errorf("Count+Unobserved = %d, RecordCount = %d; the denominator must be whole",
			d.Count+d.Unobserved, a.RecordCount())
	}
}

// TestEmptyDurationSummaryReportsNothingRatherThanZero.
func TestEmptyDurationSummaryReportsNothingRatherThanZero(t *testing.T) {
	a := addAll(t, newTestAggregate(t), operationalRecord("e1", "", event.StatusUnset))
	if _, ok := a.Durations().Mean(); ok {
		t.Error("Mean reported a value when nothing was observed")
	}
	if a.Durations().Count != 0 {
		t.Errorf("Count = %d, want 0", a.Durations().Count)
	}
}

// TestSpanStatusCountsKeepUnsetOutOfSuccess is the rule that stops an
// unstatused run reporting a clean bill of health.
func TestSpanStatusCountsKeepUnsetOutOfSuccess(t *testing.T) {
	a := addAll(t, newTestAggregate(t),
		operationalRecord("e1", "", event.StatusOK),
		operationalRecord("e2", "", event.StatusUnset),
		operationalRecord("e3", "", event.StatusUnset),
		operationalRecord("e4", "", event.StatusError),
		operationalRecord("e5", "", event.StatusUnavailable),
	)

	c := a.SpanStatuses()
	if c.OK != 1 || c.Unset != 2 || c.Error != 1 || c.Unavailable != 1 {
		t.Fatalf("counts = %+v, want OK 1 Unset 2 Error 1 Unavailable 1", c)
	}
	if c.Total() != a.RecordCount() {
		t.Errorf("Total = %d, RecordCount = %d", c.Total(), a.RecordCount())
	}
	// Stated is the honest denominator: two of these five said nothing and one
	// had nothing to say, and none of the three is a success.
	if c.Stated() != 2 {
		t.Errorf("Stated = %d, want 2 (OK+Error only)", c.Stated())
	}
	if c.Stated() == c.Total() {
		t.Error("Stated equals Total, so unstatused observations are being counted " +
			"as having stated an outcome")
	}
}

// TestUnrecognizedSpanStatusIsRefused: the vocabulary is closed, like every
// other counted enum on the aggregate.
func TestUnrecognizedSpanStatusIsRefused(t *testing.T) {
	for _, bogus := range []event.SpanStatus{"OK", "failed", "success", "2"} {
		_, err := newTestAggregate(t).AddRecord(operationalRecord("e1", "", bogus))
		if err == nil {
			t.Errorf("AddRecord accepted span status %q", bogus)
		}
	}
}

// TestMalformedDurationIsRefused: this code writes the field, so a value it
// cannot parse was not produced by an engine following the contract.
func TestMalformedDurationIsRefused(t *testing.T) {
	for _, bogus := range []string{"1.5", "-1", "0x10", " 10", "010", "1e3", "abc"} {
		_, err := newTestAggregate(t).AddRecord(operationalRecord("e1", bogus, event.StatusUnset))
		if err == nil {
			t.Errorf("AddRecord accepted duration %q", bogus)
		}
	}
}

// TestDurationSumOverflowIsRefused follows the existing counter contract:
// refused, never wrapped.
//
// Every individual duration here is **valid** — exactly event.MaxDurationNanos,
// the largest a single observation may report. That is the point: the
// per-observation bound and the aggregate's capacity are different limits, and a
// run legitimately totals more span time than any one span took. Two maxima fit
// in a uint64 sum with one nanosecond to spare; the third does not.
//
// An earlier version of this test reached overflow with a single out-of-range
// value, which now fails validation instead and tested nothing about the sum.
func TestDurationSumOverflowIsRefused(t *testing.T) {
	maxOne := strconv.FormatUint(event.MaxDurationNanos, 10)

	a := addAll(t, newTestAggregate(t),
		operationalRecord("e1", maxOne, event.StatusUnset),
		operationalRecord("e2", maxOne, event.StatusUnset),
	)
	// 2 * (2^63 - 1) = 2^64 - 2, one short of the uint64 ceiling.
	if want := uint64(math.MaxUint64) - 1; a.Durations().Sum != want {
		t.Fatalf("Sum = %d, want %d", a.Durations().Sum, want)
	}

	// One more nanosecond fits exactly.
	fits, err := a.AddRecord(operationalRecord("e3", "1", event.StatusUnset))
	if err != nil {
		t.Fatalf("a sum reaching exactly MaxUint64 was refused: %v", err)
	}
	if fits.Durations().Sum != math.MaxUint64 {
		t.Errorf("Sum = %d, want MaxUint64", fits.Durations().Sum)
	}

	// Two do not.
	if _, err := a.AddRecord(operationalRecord("e3", "2", event.StatusUnset)); err == nil {
		t.Fatal("a duration sum past MaxUint64 was accepted; it must be refused rather than wrapped")
	}
	// And the aggregate is unchanged, because AddRecord validates before it advances.
	if want := uint64(math.MaxUint64) - 1; a.Durations().Sum != want {
		t.Errorf("Sum = %d after a refused record, want %d", a.Durations().Sum, want)
	}
}

// TestIndividualDurationBound is finding 1's regression guard.
//
// A record submitted directly to the control plane used to be able to carry the
// whole uint64 range, fill an evaluation's duration sum in one observation, and
// make every later positive duration fail with overflow. The bound the telemetry
// adapters apply is now applied here too.
func TestIndividualDurationBound(t *testing.T) {
	maxOne := strconv.FormatUint(event.MaxDurationNanos, 10)
	overOne := strconv.FormatUint(event.MaxDurationNanos+1, 10)

	tests := []struct {
		name     string
		duration string
		accepted bool
	}{
		{"absent is unavailable", "", true},
		{"a measured zero", "0", true},
		{"an ordinary duration", "1500000", true},
		{"exactly the maximum", maxOne, true},
		{"the maximum plus one", overOne, false},
		{"MaxUint64", "18446744073709551615", false},
		{"a negative value", "-1", false},
		{"a float", "1.5", false},
		{"a leading zero is not canonical", "0100", false},
		{"hexadecimal", "0x10", false},
		{"leading space", " 10", false},
		{"scientific notation", "1e3", false},
		{"not a number", "abc", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, err := newTestAggregate(t).AddRecord(
				operationalRecord("e1", tt.duration, event.StatusUnset))
			if tt.accepted {
				if err != nil {
					t.Fatalf("AddRecord() error = %v, want accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("AddRecord accepted duration %q; sum is now %d",
					tt.duration, next.Durations().Sum)
			}
			if !errors.Is(err, platform.ErrInvalidDecisionRecord) {
				t.Errorf("error = %v, want platform.ErrInvalidDecisionRecord", err)
			}
			// Refused, not downgraded: an invalid value must not be counted as
			// an unobserved duration either.
			if next.RecordCount() != 0 {
				t.Errorf("the aggregate advanced to %d records on a refused value",
					next.RecordCount())
			}
		})
	}
}

// TestOversizedDurationCannotStarveLaterRecords is the consequence that made
// finding 1 worth fixing, asserted end to end on the aggregate.
func TestOversizedDurationCannotStarveLaterRecords(t *testing.T) {
	a := newTestAggregate(t)
	if _, err := a.AddRecord(
		operationalRecord("attack", "18446744073709551615", event.StatusUnset)); err == nil {
		t.Fatal("an out-of-range duration was accepted")
	}
	// The run is untouched, so ordinary evidence still records.
	next, err := a.AddRecord(operationalRecord("e1", "1000000", event.StatusUnset))
	if err != nil {
		t.Fatalf("a later valid record failed: %v", err)
	}
	if next.Durations().Sum != 1_000_000 {
		t.Errorf("Sum = %d, want 1000000", next.Durations().Sum)
	}
}

// TestOperationalInvariantsHoldAcrossAMixedRun is the property a migrated row
// also has to satisfy, asserted here on a live one.
func TestOperationalInvariantsHoldAcrossAMixedRun(t *testing.T) {
	a := newTestAggregate(t)
	statuses := []event.SpanStatus{
		event.StatusOK, event.StatusError, event.StatusUnset, event.StatusUnavailable,
	}
	for i := range 40 {
		duration := ""
		if i%3 != 0 {
			duration = "1000"
		}
		a = addAll(t, a, operationalRecord("e"+string(rune('A'+i%26))+string(rune('0'+i/26)),
			duration, statuses[i%len(statuses)]))
	}

	d, c := a.Durations(), a.SpanStatuses()
	if d.Count+d.Unobserved != a.RecordCount() {
		t.Errorf("duration buckets sum to %d, RecordCount = %d", d.Count+d.Unobserved, a.RecordCount())
	}
	if c.Total() != a.RecordCount() {
		t.Errorf("status buckets sum to %d, RecordCount = %d", c.Total(), a.RecordCount())
	}
}

// TestSumIsNotWallClock documents, as an executable note, that summed span time
// is not the run's elapsed time — the misreading the field's own comment warns
// against.
func TestSumIsNotWallClock(t *testing.T) {
	// A parent and its child, both a second long, overlapping entirely.
	a := addAll(t, newTestAggregate(t),
		operationalRecord("parent", "1000000000", event.StatusUnset),
		operationalRecord("child", "1000000000", event.StatusUnset),
	)
	if a.Durations().Sum != 2_000_000_000 {
		t.Fatalf("Sum = %d, want 2s", a.Durations().Sum)
	}
	// Two overlapping seconds sum to two, while only one second elapsed. Nothing
	// in this package claims otherwise, and nothing downstream may.
}
