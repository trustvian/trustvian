package event_test

// The duration availability rules (task 084).
//
// One function decides them for both adapters, so this is where the rules are
// pinned. The distinction every case here protects is the same one: a duration
// that was not measured is not a duration of zero.

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
)

func TestDurationFrom(t *testing.T) {
	tests := []struct {
		name         string
		start, end   uint64
		wantNanos    uint64
		wantObserved bool
	}{
		{"an ordinary interval", 1_000, 1_500, 500, true},
		{"a genuine zero is a measurement", 1_000, 1_000, 0, true},
		{"one nanosecond", 1_000, 1_001, 1, true},
		{"no end timestamp", 1_000, 0, 0, false},
		{"no start timestamp", 0, 1_500, 0, false},
		{"neither timestamp", 0, 0, 0, false},
		{"end before start is malformed, not zero", 1_500, 1_000, 0, false},
		{"the largest representable interval", 1, uint64(math.MaxInt64) + 1, uint64(math.MaxInt64), true},
		{"beyond the bound", 1, uint64(math.MaxInt64) + 2, 0, false},
		{"a pathological span of nearly the whole range", 1, math.MaxUint64, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nanos, observed := event.DurationFrom(tt.start, tt.end)
			if observed != tt.wantObserved {
				t.Fatalf("observed = %v, want %v", observed, tt.wantObserved)
			}
			if nanos != tt.wantNanos {
				t.Errorf("nanos = %d, want %d", nanos, tt.wantNanos)
			}
			if !observed && nanos != 0 {
				t.Errorf("an unobserved duration reported %d nanoseconds; it must be "+
					"zero so a caller ignoring the bool cannot read a measurement", nanos)
			}
		})
	}
}

// TestZeroAndUnavailableAreDifferent is the distinction the whole type exists
// for, stated as its own test so it cannot be lost in a table.
func TestZeroAndUnavailableAreDifferent(t *testing.T) {
	zeroNanos, zeroObserved := event.DurationFrom(1_000, 1_000)
	missingNanos, missingObserved := event.DurationFrom(1_000, 0)

	if zeroNanos != missingNanos {
		t.Fatalf("the fixture is wrong: %d != %d", zeroNanos, missingNanos)
	}
	if zeroObserved == missingObserved {
		t.Fatal("a measured zero and an unavailable duration are indistinguishable; " +
			"a span with no end timestamp did not take no time")
	}
}

func TestSpanStatusVocabulary(t *testing.T) {
	tests := []struct {
		status event.SpanStatus
		valid  bool
	}{
		{event.StatusUnset, true},
		{event.StatusOK, true},
		{event.StatusError, true},
		{event.StatusUnavailable, false},
		{event.SpanStatus("OK"), false},
		{event.SpanStatus("failed"), false},
		{event.SpanStatus("success"), false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.valid {
			t.Errorf("SpanStatus(%q).Valid() = %v, want %v", tt.status, got, tt.valid)
		}
	}
}

// TestNeitherUnsetNorUnavailableIsSuccess pins the rule that keeps an
// unstatused run from reporting a clean bill of health.
func TestNeitherUnsetNorUnavailableIsSuccess(t *testing.T) {
	for _, status := range []event.SpanStatus{event.StatusUnset, event.StatusUnavailable} {
		if status == event.StatusOK {
			t.Errorf("%q compares equal to StatusOK", status)
		}
	}
	if event.StatusUnset == event.StatusUnavailable {
		t.Error("an explicit unset and an absent status are the same value; " +
			"the producer saying nothing and there being no producer are different facts")
	}
}

func TestSpanLineageVocabulary(t *testing.T) {
	tests := []struct {
		lineage event.SpanLineage
		valid   bool
	}{
		{event.LineageRoot, true},
		{event.LineageChild, true},
		{event.LineageUnspecified, false},
		{event.SpanLineage("Root"), false},
		{event.SpanLineage("parent"), false},
	}
	for _, tt := range tests {
		if got := tt.lineage.Valid(); got != tt.valid {
			t.Errorf("SpanLineage(%q).Valid() = %v, want %v", tt.lineage, got, tt.valid)
		}
	}
}

// TestEventValidateRejectsAnOversizedDuration guards the public engine path.
//
// DurationFrom bounds what a telemetry adapter can produce, but a caller may
// construct an Execution directly. Without this check the engine would accept
// and project a duration no span could have produced, and the platform would
// then have to carry and sum it.
func TestEventValidateRejectsAnOversizedDuration(t *testing.T) {
	base := func() event.Event {
		return event.Event{
			ID:        "evt-1",
			Timestamp: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
			Actor: event.Actor{
				ID: "a", Type: event.ActorTypeService, IdentityConfidence: 1,
			},
			Operation: event.Operation{
				Category: event.OperationCategoryHTTP, Name: "GET",
				Direction: event.DirectionOutbound,
			},
		}
	}

	tests := []struct {
		name     string
		exec     event.Execution
		rejected bool
	}{
		{"nothing observed", event.Execution{}, false},
		{"a measured zero", event.Execution{DurationObserved: true}, false},
		{"an ordinary duration",
			event.Execution{DurationNanos: 1_500_000, DurationObserved: true}, false},
		{"exactly the maximum",
			event.Execution{DurationNanos: event.MaxDurationNanos, DurationObserved: true}, false},
		{"the maximum plus one",
			event.Execution{DurationNanos: event.MaxDurationNanos + 1, DurationObserved: true}, true},
		{"MaxUint64",
			event.Execution{DurationNanos: math.MaxUint64, DurationObserved: true}, true},
		// Not observed, so the stale value is never read and never rejected:
		// the field is meaningless unless the bool says otherwise.
		{"an oversized value that was not observed",
			event.Execution{DurationNanos: math.MaxUint64}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := base()
			ev.Execution = tt.exec
			err := ev.Validate()
			if tt.rejected {
				if err == nil {
					t.Fatal("Validate() accepted a duration beyond the maximum")
				}
				if !errors.Is(err, event.ErrInvalidDuration) {
					t.Errorf("error = %v, want ErrInvalidDuration", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() error = %v, want accepted", err)
			}
		})
	}
}

// TestMaxDurationNanosIsTheOneBound pins that the adapters and the validator
// judge against the same number.
func TestMaxDurationNanosIsTheOneBound(t *testing.T) {
	if event.MaxDurationNanos != uint64(math.MaxInt64) {
		t.Errorf("MaxDurationNanos = %d, want MaxInt64", event.MaxDurationNanos)
	}
	// The conversion accepts exactly the bound and refuses one past it.
	if _, ok := event.DurationFrom(1, event.MaxDurationNanos+1); !ok {
		t.Error("DurationFrom refused an interval of exactly the maximum")
	}
	if _, ok := event.DurationFrom(1, event.MaxDurationNanos+2); ok {
		t.Error("DurationFrom accepted an interval past the maximum")
	}
}
