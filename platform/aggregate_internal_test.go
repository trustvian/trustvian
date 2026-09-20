package platform

// The overflow guard, driven from inside the package.
//
// RecordCount is a uint64: reaching its limit honestly would take longer than
// the universe has existed, so the only way to test the guard is to construct
// an aggregate already at the boundary. That needs the unexported field, which
// is exactly why this one test lives here rather than in the external suite.
//
// The guard is not defensive theatre. The aggregate is bounded in memory but
// runs over a logically unbounded stream, and a wrapped counter would report
// that nothing had been observed — the most misleading possible answer from a
// type whose entire job is counting.

import (
	"errors"
	"math"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
)

func TestAddRecordRefusesToWrapTheCounter(t *testing.T) {
	at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

	// bound: true because this models an aggregate the constructor produced
	// and that then ran to the counter's limit — not an unbound one, which
	// AddRecord now refuses before it ever reaches the overflow check.
	full := EvaluationAggregate{
		bound:       true,
		runID:       "run-1",
		candidateID: "cand-1",
		environment: "staging",
		profile:     "profile-1",
		recordCount: math.MaxUint64,
	}

	got, err := full.AddRecord(overflowTestRecord(at))
	if !errors.Is(err, ErrAggregateOverflow) {
		t.Fatalf("AddRecord() error = %v, want one wrapping ErrAggregateOverflow", err)
	}
	if got != full {
		t.Errorf("a refused record changed the aggregate:\n got %+v\nwant %+v", got, full)
	}
	if got.recordCount != math.MaxUint64 {
		t.Errorf("recordCount = %d, want it unchanged at MaxUint64 — it must never wrap to zero", got.recordCount)
	}
}

// TestAddRecordSucceedsOneBelowTheLimit proves the guard is placed at the
// boundary rather than one short of it: the last representable observation is
// still counted.
func TestAddRecordSucceedsOneBelowTheLimit(t *testing.T) {
	at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

	nearly := EvaluationAggregate{
		bound:       true,
		runID:       "run-1",
		candidateID: "cand-1",
		environment: "staging",
		profile:     "profile-1",
		recordCount: math.MaxUint64 - 1,
	}

	got, err := nearly.AddRecord(overflowTestRecord(at))
	if err != nil {
		t.Fatalf("AddRecord() one below the limit: %v", err)
	}
	if got.recordCount != math.MaxUint64 {
		t.Errorf("recordCount = %d, want MaxUint64", got.recordCount)
	}

	// And the next one is refused.
	if _, err := got.AddRecord(overflowTestRecord(at)); !errors.Is(err, ErrAggregateOverflow) {
		t.Fatalf("the following AddRecord() error = %v, want ErrAggregateOverflow", err)
	}
}

// overflowTestRecord is a minimal valid record for the environment the
// aggregates above use. Built here rather than reusing the external suite's
// fixture because that one lives in package platform_test.
func overflowTestRecord(at time.Time) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     "evt-overflow",
		Timestamp:   at,
		Environment: "staging",
		Behavior:    trustvian.StableFeatures{Environment: "staging"},
		RiskLevel:   "low",
		Decision:    "allow",
		// MatchedDefault with no PolicyRule: the well-formed pairing. Leaving
		// both zero would be the malformed combination the policy-selection
		// guard refuses — which is how this fixture was first written, and
		// how the guard proved itself.
		MatchedDefault: true,
	}
}

// TestBindingMarkerIsNotForgeableFromValidIdentifiers covers the state an
// external caller cannot build: identifiers present, marker absent.
//
// It matters because the marker is what the guard trusts. If a future change
// ever set the identifiers without setting `bound` — a second constructor, a
// deserializer — this is the case that decides whether the result is usable.
// It must not be: the marker vouches for validation having run, and
// identifiers that merely *look* right are not evidence that it did.
func TestBindingMarkerIsNotForgeableFromValidIdentifiers(t *testing.T) {
	at := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)

	tests := map[string]EvaluationAggregate{
		"identifiers set, marker absent": {
			runID: "run-1", candidateID: "cand-1",
			environment: "staging", profile: "profile-1",
		},
		"marker absent, partial identity": {
			runID: "run-1", environment: "staging",
		},
	}

	for name, unbound := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := unbound.AddRecord(overflowTestRecord(at))
			if !errors.Is(err, ErrUnboundAggregate) {
				t.Fatalf("AddRecord() error = %v, want one wrapping ErrUnboundAggregate", err)
			}
			if got != unbound {
				t.Error("a refused record changed the aggregate")
			}
		})
	}
}

// TestConstructorSetsTheMarkerOnlyOnSuccess: a rejected run must not leave a
// half-built aggregate that would pass the guard.
func TestConstructorSetsTheMarkerOnlyOnSuccess(t *testing.T) {
	if _, err := NewEvaluationAggregate(EvaluationRun{}); err == nil {
		t.Fatal("NewEvaluationAggregate(zero run) succeeded")
	}

	got, err := NewEvaluationAggregate(EvaluationRun{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got.bound {
		t.Error("a failed construction returned an aggregate marked as bound")
	}
	if got != (EvaluationAggregate{}) {
		t.Errorf("a failed construction returned %+v, want the zero value", got)
	}
}
