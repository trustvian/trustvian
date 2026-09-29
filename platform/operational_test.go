package platform_test

// Operational aggregates (task 084): the two summaries, their invariants, and
// their overflow behaviour.

import (
	"math"
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
func TestDurationSumOverflowIsRefused(t *testing.T) {
	huge := uint64(math.MaxUint64)/2 + 1
	text := func(v uint64) string {
		out := ""
		for v > 0 {
			out = string(rune('0'+v%10)) + out
			v /= 10
		}
		if out == "" {
			return "0"
		}
		return out
	}

	a := addAll(t, newTestAggregate(t), operationalRecord("e1", text(huge), event.StatusUnset))
	_, err := a.AddRecord(operationalRecord("e2", text(huge), event.StatusUnset))
	if err == nil {
		t.Fatal("a duration sum past MaxUint64 was accepted; it must be refused rather than wrapped")
	}
	// And the aggregate is unchanged, because AddRecord validates before it advances.
	if a.Durations().Sum != huge {
		t.Errorf("Sum = %d after a refused record, want %d", a.Durations().Sum, huge)
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
