package platform

// Overflow guards for the behavioral collector, driven from inside the
// package.
//
// Both counters are uint64: reaching either honestly would take longer than
// the universe has existed, so the only way to exercise the guards is to
// construct a collector already at the boundary. That needs unexported fields,
// which is why this one file is internal.
//
// The guards are not defensive theatre. A collector is bounded in memory but
// runs over a logically unbounded stream, and a wrapped counter would report
// that a behavior had been observed zero times — the most misleading possible
// answer from a type whose entire output is counts and the rates derived from
// them. A rate of 0/N is a behavior that did not happen.

import (
	"errors"
	"math"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

const overflowEnvironment = "staging"

// overflowCollector builds a bound collector holding one known behavior.
func overflowCollector(observations, entryObservations uint64) *BehaviorCollector {
	behavior := overflowBehavior()
	return &BehaviorCollector{
		bound:        true,
		runID:        "run-1",
		candidateID:  "cand-1",
		environment:  overflowEnvironment,
		profile:      "profile-1",
		observations: observations,
		complete:     true,
		entries: map[string]BehaviorEntry{
			"fp-a": {FingerprintID: "fp-a", Behavior: behavior, Observations: entryObservations},
		},
	}
}

func overflowBehavior() trustvian.StableFeatures {
	return trustvian.StableFeatures{
		ActorType:         event.ActorTypeAIAgent,
		OperationCategory: event.OperationCategoryTool,
		OperationName:     "shell.read",
		TargetName:        "build-host",
		TargetCategory:    event.TargetCategoryExternal,
		Environment:       overflowEnvironment,
	}
}

func overflowRecord(fingerprintID string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:       "evt-overflow",
		Timestamp:     time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC),
		Environment:   overflowEnvironment,
		Behavior:      overflowBehavior(),
		FingerprintID: fingerprintID,
	}
}

// TestObserveRefusesToWrapTheTotalCounter: the collector-wide observation
// count must never roll to zero.
func TestObserveRefusesToWrapTheTotalCounter(t *testing.T) {
	c := overflowCollector(math.MaxUint64, 1)

	err := c.Observe(overflowRecord("fp-a"))
	if !errors.Is(err, ErrBehaviorOverflow) {
		t.Fatalf("Observe() error = %v, want one wrapping ErrBehaviorOverflow", err)
	}
	if c.observations != math.MaxUint64 {
		t.Errorf("observations = %d, want it unchanged at MaxUint64 — it must never wrap", c.observations)
	}
	if got := c.entries["fp-a"].Observations; got != 1 {
		t.Errorf("entry Observations = %d, want 1 — the entry was touched on a failure path", got)
	}
	if !c.complete {
		t.Error("an overflow refusal marked the collector incomplete; capacity was not the problem")
	}
}

// TestObserveRefusesToWrapAPerEntryCounter: a single behavior's count is
// guarded independently of the total, since one behavior can dominate a run.
func TestObserveRefusesToWrapAPerEntryCounter(t *testing.T) {
	c := overflowCollector(42, math.MaxUint64)

	err := c.Observe(overflowRecord("fp-a"))
	if !errors.Is(err, ErrBehaviorOverflow) {
		t.Fatalf("Observe() error = %v, want one wrapping ErrBehaviorOverflow", err)
	}
	if c.observations != 42 {
		t.Errorf("observations = %d, want 42 — the total advanced on a failure path", c.observations)
	}
	if got := c.entries["fp-a"].Observations; got != math.MaxUint64 {
		t.Errorf("entry Observations = %d, want it unchanged at MaxUint64", got)
	}
	if got := c.Snapshot().ObservationCount(); got != 42 {
		t.Errorf("snapshot reports %d observations, want 42", got)
	}
}

// TestObserveCountsTheLastRepresentableObservation proves the guards sit at
// the boundary rather than one short of it: the final representable
// observation is still counted, and only the next is refused.
func TestObserveCountsTheLastRepresentableObservation(t *testing.T) {
	t.Run("total", func(t *testing.T) {
		c := overflowCollector(math.MaxUint64-1, 1)

		if err := c.Observe(overflowRecord("fp-a")); err != nil {
			t.Fatalf("Observe() one below the limit: %v", err)
		}
		if c.observations != math.MaxUint64 {
			t.Fatalf("observations = %d, want MaxUint64", c.observations)
		}
		if err := c.Observe(overflowRecord("fp-a")); !errors.Is(err, ErrBehaviorOverflow) {
			t.Fatalf("the following Observe() error = %v, want ErrBehaviorOverflow", err)
		}
	})

	t.Run("per entry", func(t *testing.T) {
		c := overflowCollector(1, math.MaxUint64-1)

		if err := c.Observe(overflowRecord("fp-a")); err != nil {
			t.Fatalf("Observe() one below the limit: %v", err)
		}
		if got := c.entries["fp-a"].Observations; got != math.MaxUint64 {
			t.Fatalf("entry Observations = %d, want MaxUint64", got)
		}
		if err := c.Observe(overflowRecord("fp-a")); !errors.Is(err, ErrBehaviorOverflow) {
			t.Fatalf("the following Observe() error = %v, want ErrBehaviorOverflow", err)
		}
	})
}
