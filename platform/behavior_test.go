package platform_test

// Task 054: behavioral diff.
//
// The assertions that matter most here are the refusals. A diff's headline
// output is which behaviors are new, and every way of getting that subtly
// wrong — saturating quietly, merging two behaviors that share a fingerprint,
// comparing across environments — produces a confident, specific, wrong
// number rather than a visible failure. Most of this file exists to make those
// paths loud.
//
// TestRealEngineBehavioralDiff is the one that proves the whole chain
// composes: real Events, a real Engine, real DecisionRecords, and behaviors
// located through their stable descriptors rather than through hard-coded
// fingerprint hashes.

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

const behaviorCapacity = 512

// behaviorRun builds a run in a named environment, so comparison tests can
// vary the environment without rebuilding everything.
func behaviorRun(t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
	environment platform.EnvironmentRef, profile platform.BehavioralProfileRef,
) platform.EvaluationRun {
	t.Helper()
	run, err := platform.NewEvaluationRun(runID, candidateID, environment, profile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	return run
}

func newCollector(t *testing.T, run platform.EvaluationRun) *platform.BehaviorCollector {
	t.Helper()
	c, err := platform.NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	return c
}

// behaviorRecord is a well-formed record for one behavioral shape. The
// fingerprint is supplied rather than derived: these are hand-built records,
// and the real-engine test below is what proves the shape matches production.
func behaviorRecord(eventID, fingerprintID, operation, target string, environment string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     eventID,
		Timestamp:   aggEpoch,
		ActorID:     "agent-1",
		ActorType:   event.ActorTypeAIAgent,
		Environment: environment,
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     operation,
			TargetName:        target,
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       environment,
		},
		FingerprintID:  fingerprintID,
		RiskLevel:      "low",
		Decision:       "observe_only",
		MatchedDefault: true,
	}
}

func observe(t *testing.T, c *platform.BehaviorCollector, record trustvian.DecisionRecord) {
	t.Helper()
	if err := c.Observe(record); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
}

// ---------------------------------------------------------------------
// Collector construction
// ---------------------------------------------------------------------

func TestNewBehaviorCollectorRequiresAValidRun(t *testing.T) {
	t.Run("valid run", func(t *testing.T) {
		c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
		s := c.Snapshot()
		if s.RunID() != "run-1" || s.CandidateID() != "cand-1" ||
			s.Environment() != testEnvironment || s.BehavioralProfile() != testProfile {
			t.Fatalf("snapshot did not capture the run's identity: %+v", s)
		}
		if !s.Complete() {
			t.Error("a fresh collector must produce a complete snapshot")
		}
		if s.ObservationCount() != 0 || s.DistinctBehaviorCount() != 0 {
			t.Error("a fresh collector must be empty")
		}
	})

	t.Run("zero-value run", func(t *testing.T) {
		got, err := platform.NewBehaviorCollector(platform.EvaluationRun{})
		if err == nil {
			t.Fatalf("NewBehaviorCollector(zero run) succeeded: %v", got)
		}
		if !errors.Is(err, platform.ErrInvalidID) {
			t.Errorf("error = %v, want one wrapping ErrInvalidID", err)
		}
		if got != nil {
			t.Error("a refused construction returned a non-nil collector")
		}
	})
}

// TestZeroValueBehaviorCollectorRejectsEvidence: unexported fields prevent
// mutation, not `platform.BehaviorCollector{}` — which any package can write,
// and which would otherwise collect evidence for no run at all. The record
// below is valid in every other respect, including a matching empty
// environment, so the binding guard is demonstrably what rejects it.
func TestZeroValueBehaviorCollectorRejectsEvidence(t *testing.T) {
	zero := &platform.BehaviorCollector{}
	rec := behaviorRecord("evt-1", "fp-1", "shell.execute", "build-host", "")

	if err := zero.Observe(rec); !errors.Is(err, platform.ErrUnboundCollector) {
		t.Fatalf("Observe() error = %v, want one wrapping ErrUnboundCollector", err)
	}
	if s := zero.Snapshot(); s.ObservationCount() != 0 || s.DistinctBehaviorCount() != 0 {
		t.Error("an unbound collector produced a populated snapshot")
	}

	// And its snapshot cannot be compared, since it never bound a run.
	valid := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile)).Snapshot()
	if _, err := platform.CompareBehaviorSnapshots(zero.Snapshot(), valid); !errors.Is(err, platform.ErrUnboundCollector) {
		t.Errorf("Compare() with an unbound snapshot: error = %v, want ErrUnboundCollector", err)
	}
}

// ---------------------------------------------------------------------
// Observation
// ---------------------------------------------------------------------

func TestCollectorCountsBehaviors(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))

	observe(t, c, behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment))
	observe(t, c, behaviorRecord("evt-2", "fp-a", "shell.read", "build-host", testEnvironment))
	observe(t, c, behaviorRecord("evt-3", "fp-b", "db.query", "customer-db", testEnvironment))

	s := c.Snapshot()
	if s.ObservationCount() != 3 {
		t.Errorf("ObservationCount() = %d, want 3", s.ObservationCount())
	}
	if s.DistinctBehaviorCount() != 2 {
		t.Errorf("DistinctBehaviorCount() = %d, want 2", s.DistinctBehaviorCount())
	}

	entries := s.Entries()
	if len(entries) != 2 {
		t.Fatalf("len(Entries()) = %d, want 2", len(entries))
	}
	// Sorted by fingerprint: fp-a before fp-b.
	if entries[0].FingerprintID != "fp-a" || entries[1].FingerprintID != "fp-b" {
		t.Fatalf("entries are not sorted by FingerprintID: %+v", entries)
	}
	if entries[0].Observations != 2 || entries[1].Observations != 1 {
		t.Errorf("observation counts = %d/%d, want 2/1", entries[0].Observations, entries[1].Observations)
	}
	if entries[0].Behavior.OperationName != "shell.read" {
		t.Errorf("descriptor not retained: %+v", entries[0].Behavior)
	}
}

// TestDuplicateRecordsCountTwiceInCollector: one Observe is one observation,
// and there is no EventID set. Detecting a repeat would need every identifier
// remembered, which is the unbounded structure this design excludes.
func TestDuplicateRecordsCountTwiceInCollector(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
	rec := behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment)

	observe(t, c, rec)
	observe(t, c, rec)

	s := c.Snapshot()
	if s.ObservationCount() != 2 {
		t.Errorf("ObservationCount() = %d, want 2", s.ObservationCount())
	}
	if got := s.Entries()[0].Observations; got != 2 {
		t.Errorf("entry Observations = %d, want 2", got)
	}
	if s.DistinctBehaviorCount() != 1 {
		t.Errorf("DistinctBehaviorCount() = %d, want 1", s.DistinctBehaviorCount())
	}
}

// ---------------------------------------------------------------------
// Rejection
// ---------------------------------------------------------------------

func TestCollectorRejectsMalformedEvidence(t *testing.T) {
	over := strings.Repeat("x", 257)
	atLimit := strings.Repeat("x", 256)

	tests := map[string]struct {
		mutate  func(*trustvian.DecisionRecord)
		wantErr error
	}{
		"empty event id": {func(r *trustvian.DecisionRecord) { r.EventID = "" }, platform.ErrInvalidBehaviorRecord},
		"zero timestamp": {func(r *trustvian.DecisionRecord) { r.Timestamp = time.Time{} }, platform.ErrInvalidBehaviorRecord},
		"other environment": {func(r *trustvian.DecisionRecord) { r.Environment = "production" },
			platform.ErrBehaviorEnvironmentMismatch},
		"behavior environment drift": {func(r *trustvian.DecisionRecord) { r.Behavior.Environment = "production" },
			platform.ErrInvalidBehaviorRecord},

		"empty fingerprint": {func(r *trustvian.DecisionRecord) { r.FingerprintID = "" }, platform.ErrInvalidBehaviorRecord},
		"over-long fingerprint": {func(r *trustvian.DecisionRecord) { r.FingerprintID = over },
			platform.ErrInvalidBehaviorRecord},
		"control character in fingerprint": {func(r *trustvian.DecisionRecord) { r.FingerprintID = "fp\x00a" },
			platform.ErrInvalidBehaviorRecord},

		"unknown actor type": {func(r *trustvian.DecisionRecord) { r.Behavior.ActorType = "robot" },
			platform.ErrInvalidBehaviorRecord},
		"unknown operation category": {func(r *trustvian.DecisionRecord) { r.Behavior.OperationCategory = "telepathy" },
			platform.ErrInvalidBehaviorRecord},
		"unknown target category": {func(r *trustvian.DecisionRecord) { r.Behavior.TargetCategory = "somewhere" },
			platform.ErrInvalidBehaviorRecord},

		"empty operation name": {func(r *trustvian.DecisionRecord) { r.Behavior.OperationName = "" },
			platform.ErrInvalidBehaviorRecord},
		"over-long operation name": {func(r *trustvian.DecisionRecord) { r.Behavior.OperationName = over },
			platform.ErrInvalidBehaviorRecord},
		"over-long target name": {func(r *trustvian.DecisionRecord) { r.Behavior.TargetName = over },
			platform.ErrInvalidBehaviorRecord},
		"control character in operation name": {func(r *trustvian.DecisionRecord) { r.Behavior.OperationName = "a\nb" },
			platform.ErrInvalidBehaviorRecord},
		"invalid UTF-8 in target name": {func(r *trustvian.DecisionRecord) { r.Behavior.TargetName = "host-\xff" },
			platform.ErrInvalidBehaviorRecord},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
			observe(t, c, behaviorRecord("evt-0", "fp-a", "shell.read", "build-host", testEnvironment))
			before := c.Snapshot()

			rec := behaviorRecord("evt-bad", "fp-b", "shell.execute", "build-host", testEnvironment)
			tt.mutate(&rec)

			err := c.Observe(rec)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Observe() error = %v, want one wrapping %v", err, tt.wantErr)
			}

			after := c.Snapshot()
			if after.ObservationCount() != before.ObservationCount() ||
				after.DistinctBehaviorCount() != before.DistinctBehaviorCount() {
				t.Errorf("a rejected record changed the collector: %d/%d -> %d/%d",
					before.ObservationCount(), before.DistinctBehaviorCount(),
					after.ObservationCount(), after.DistinctBehaviorCount())
			}
		})
	}

	// The boundary is exact and identity is never truncated: 256 bytes is
	// accepted, 257 is refused above.
	t.Run("strings at exactly the limit are accepted", func(t *testing.T) {
		c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
		rec := behaviorRecord("evt-1", atLimit, atLimit, atLimit, testEnvironment)
		observe(t, c, rec)

		entry := c.Snapshot().Entries()[0]
		if entry.FingerprintID != atLimit {
			t.Error("the fingerprint was altered on the way in")
		}
		if len(entry.Behavior.OperationName) != 256 || len(entry.Behavior.TargetName) != 256 {
			t.Error("a retained identity string was truncated; two behaviors could collide")
		}
	})

	t.Run("an empty target name is valid", func(t *testing.T) {
		c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
		rec := behaviorRecord("evt-1", "fp-a", "shell.read", "", testEnvironment)
		rec.Behavior.TargetCategory = event.TargetCategoryUnspecified
		observe(t, c, rec)
		if c.Snapshot().DistinctBehaviorCount() != 1 {
			t.Error("a behavior with no target was rejected")
		}
	})
}

// TestFingerprintConflictFailsClosed: one fingerprint identifies exactly one
// shape. Overwriting the descriptor or merging the counts would report two
// different behaviors as one, and the realistic causes — tampering,
// corruption, a collision — all deserve refusal.
func TestFingerprintConflictFailsClosed(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
	observe(t, c, behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment))
	before := c.Snapshot()

	conflicting := behaviorRecord("evt-2", "fp-a", "shell.execute", "build-host", testEnvironment)
	err := c.Observe(conflicting)
	if !errors.Is(err, platform.ErrFingerprintConflict) {
		t.Fatalf("Observe() error = %v, want one wrapping ErrFingerprintConflict", err)
	}

	after := c.Snapshot()
	if after.ObservationCount() != before.ObservationCount() {
		t.Error("a conflicting record was counted")
	}
	if got := after.Entries()[0].Behavior.OperationName; got != "shell.read" {
		t.Errorf("the descriptor was overwritten to %q", got)
	}
}

// ---------------------------------------------------------------------
// Capacity
// ---------------------------------------------------------------------

// TestCollectorCapacityIsExplicitAndSticky is the most important refusal in
// this task. Comparing the first 512 behaviors of a wider run would produce a
// confident, specific, wrong "added behavior" count — under-reporting exactly
// the runs whose behavioral surface is widest.
func TestCollectorCapacityIsExplicitAndSticky(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))

	for i := range behaviorCapacity {
		rec := behaviorRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%04d", i),
			fmt.Sprintf("tool.%d", i), "build-host", testEnvironment)
		observe(t, c, rec)
	}

	full := c.Snapshot()
	if full.DistinctBehaviorCount() != behaviorCapacity {
		t.Fatalf("DistinctBehaviorCount() = %d, want %d", full.DistinctBehaviorCount(), behaviorCapacity)
	}
	if !full.Complete() {
		t.Fatal("a collector at exactly capacity must still be complete")
	}

	t.Run("a repeat at capacity is still counted", func(t *testing.T) {
		if err := c.Observe(behaviorRecord("evt-repeat", "fp-0000", "tool.0", "build-host", testEnvironment)); err != nil {
			t.Fatalf("Observe(known fingerprint at capacity) error = %v", err)
		}
		s := c.Snapshot()
		if s.DistinctBehaviorCount() != behaviorCapacity {
			t.Errorf("DistinctBehaviorCount() = %d, want %d", s.DistinctBehaviorCount(), behaviorCapacity)
		}
		if !s.Complete() {
			t.Error("a repeat must not saturate the collector")
		}
	})

	t.Run("the 513th distinct behavior saturates", func(t *testing.T) {
		err := c.Observe(behaviorRecord("evt-513", "fp-9999", "tool.new", "build-host", testEnvironment))
		if !errors.Is(err, platform.ErrBehaviorCapacity) {
			t.Fatalf("Observe() error = %v, want one wrapping ErrBehaviorCapacity", err)
		}

		s := c.Snapshot()
		if s.Complete() {
			t.Fatal("the collector still reports complete after refusing a behavior")
		}
		if s.DistinctBehaviorCount() != behaviorCapacity {
			t.Errorf("DistinctBehaviorCount() = %d, want %d — the refused behavior was stored",
				s.DistinctBehaviorCount(), behaviorCapacity)
		}
		for _, e := range s.Entries() {
			if e.FingerprintID == "fp-9999" {
				t.Error("the refused behavior appears in the snapshot")
			}
		}
	})

	t.Run("saturation is sticky", func(t *testing.T) {
		// Even a behavior it already knows is refused: a caller that ignored
		// the capacity error must not keep building a partial picture that
		// reads like a whole one.
		err := c.Observe(behaviorRecord("evt-after", "fp-0000", "tool.0", "build-host", testEnvironment))
		if !errors.Is(err, platform.ErrBehaviorCapacity) {
			t.Fatalf("Observe() after saturation: error = %v, want ErrBehaviorCapacity", err)
		}
		if c.Snapshot().Complete() {
			t.Error("the collector recovered its complete status")
		}
	})

	t.Run("an incomplete snapshot cannot be compared", func(t *testing.T) {
		good := newCollector(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile)).Snapshot()
		saturated := c.Snapshot()

		for name, pair := range map[string][2]platform.BehaviorSnapshot{
			"incomplete reference": {saturated, good},
			"incomplete candidate": {good, saturated},
			"both incomplete":      {saturated, saturated},
		} {
			t.Run(name, func(t *testing.T) {
				got, err := platform.CompareBehaviorSnapshots(pair[0], pair[1])
				if !errors.Is(err, platform.ErrIncompleteSnapshot) {
					t.Fatalf("Compare() error = %v, want one wrapping ErrIncompleteSnapshot", err)
				}
				if len(got.Deltas()) != 0 {
					t.Error("a refused comparison returned partial deltas")
				}
			})
		}
	})
}

// ---------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------

// TestSnapshotIsDetachedFromTheCollector: the collector is the one mutable
// type here, so the moment a snapshot is taken it must stop tracking.
func TestSnapshotIsDetachedFromTheCollector(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
	observe(t, c, behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment))

	early := c.Snapshot()

	observe(t, c, behaviorRecord("evt-2", "fp-a", "shell.read", "build-host", testEnvironment))
	observe(t, c, behaviorRecord("evt-3", "fp-b", "db.query", "customer-db", testEnvironment))

	if early.ObservationCount() != 1 {
		t.Errorf("the earlier snapshot now reports %d observations, want 1", early.ObservationCount())
	}
	if early.DistinctBehaviorCount() != 1 {
		t.Errorf("the earlier snapshot now reports %d behaviors, want 1", early.DistinctBehaviorCount())
	}
	if got := early.Entries()[0].Observations; got != 1 {
		t.Errorf("the earlier snapshot's entry now reports %d observations, want 1", got)
	}
}

// TestSnapshotEntriesAreADefensiveCopy: a caller that sorts, truncates or
// rewrites the returned slice must not reach the snapshot.
func TestSnapshotEntriesAreADefensiveCopy(t *testing.T) {
	c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
	observe(t, c, behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment))
	observe(t, c, behaviorRecord("evt-2", "fp-b", "db.query", "customer-db", testEnvironment))
	s := c.Snapshot()

	entries := s.Entries()
	entries[0].FingerprintID = "tampered"
	entries[0].Observations = 9999
	entries[1].Behavior.OperationName = "rewritten"

	fresh := s.Entries()
	if fresh[0].FingerprintID != "fp-a" || fresh[0].Observations != 1 {
		t.Errorf("mutating the returned slice reached the snapshot: %+v", fresh[0])
	}
	if fresh[1].Behavior.OperationName != "db.query" {
		t.Errorf("mutating a returned descriptor reached the snapshot: %+v", fresh[1])
	}
}

// TestSnapshotOrderIsIndependentOfArrivalOrder: Go randomizes map iteration,
// so sorting is what makes two snapshots of the same evidence identical.
func TestSnapshotOrderIsIndependentOfArrivalOrder(t *testing.T) {
	ids := []string{"fp-m", "fp-a", "fp-z", "fp-c", "fp-b"}

	build := func(order []string) []platform.BehaviorEntry {
		c := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
		for i, id := range order {
			observe(t, c, behaviorRecord(fmt.Sprintf("evt-%d", i), id, "tool."+id, "build-host", testEnvironment))
		}
		return c.Snapshot().Entries()
	}

	forward := build(ids)
	reversed := build([]string{"fp-b", "fp-c", "fp-z", "fp-a", "fp-m"})

	if len(forward) != len(reversed) {
		t.Fatalf("different lengths: %d vs %d", len(forward), len(reversed))
	}
	for i := range forward {
		if forward[i].FingerprintID != reversed[i].FingerprintID {
			t.Fatalf("order differs at %d: %q vs %q", i, forward[i].FingerprintID, reversed[i].FingerprintID)
		}
	}
	for i := 1; i < len(forward); i++ {
		if forward[i-1].FingerprintID >= forward[i].FingerprintID {
			t.Fatalf("entries are not ascending: %q then %q", forward[i-1].FingerprintID, forward[i].FingerprintID)
		}
	}
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// snapshotOf builds a snapshot from (fingerprint, count) pairs.
func snapshotOf(t *testing.T, run platform.EvaluationRun, counts map[string]int) platform.BehaviorSnapshot {
	t.Helper()
	c := newCollector(t, run)
	i := 0
	// Sorted keys so the construction itself is deterministic.
	for _, id := range sortedKeys(counts) {
		for range counts[id] {
			observe(t, c, behaviorRecord(fmt.Sprintf("evt-%d", i), id, "tool."+id, "build-host",
				string(run.Environment())))
			i++
		}
	}
	return c.Snapshot()
}

func sortedKeys(m map[string]int) []string {
	return slices.Sorted(maps.Keys(m))
}

// assertNoMethods fails if value's type exposes any of the named methods.
// These invariants are enforced by absence, which is what a behavioral test
// cannot notice being removed.
func assertNoMethods(t *testing.T, name string, value any, forbidden []string) {
	t.Helper()
	typ := reflect.TypeOf(value)
	for _, bad := range forbidden {
		if _, ok := typ.MethodByName(bad); ok {
			t.Errorf("%s.%s exists; interpretation belongs to the scorecard and gate tasks, not to evidence", name, bad)
		}
	}
}

func deltaFor(t *testing.T, diff platform.BehaviorDiff, fingerprintID string) platform.BehaviorDelta {
	t.Helper()
	for _, d := range diff.Deltas() {
		if d.FingerprintID == fingerprintID {
			return d
		}
	}
	t.Fatalf("no delta for %q in %+v", fingerprintID, diff.Deltas())
	return platform.BehaviorDelta{}
}

func TestCompareIdenticalSnapshots(t *testing.T) {
	ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile),
		map[string]int{"fp-a": 3, "fp-b": 1})
	cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile),
		map[string]int{"fp-a": 3, "fp-b": 1})

	diff, err := platform.CompareBehaviorSnapshots(ref, cand)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if diff.AddedCount() != 0 || diff.RemovedCount() != 0 || diff.SharedCount() != 2 {
		t.Fatalf("added/removed/shared = %d/%d/%d, want 0/0/2",
			diff.AddedCount(), diff.RemovedCount(), diff.SharedCount())
	}
	for _, d := range diff.Deltas() {
		if d.Presence != platform.BehaviorShared {
			t.Errorf("%s presence = %q, want shared", d.FingerprintID, d.Presence)
		}
		if d.RateDelta != 0 {
			t.Errorf("%s RateDelta = %v, want 0", d.FingerprintID, d.RateDelta)
		}
	}
}

func TestCompareClassifiesPresence(t *testing.T) {
	ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile),
		map[string]int{"fp-shared": 1, "fp-gone": 1})
	cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile),
		map[string]int{"fp-shared": 1, "fp-new": 1})

	diff, err := platform.CompareBehaviorSnapshots(ref, cand)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}

	for id, want := range map[string]platform.BehaviorPresence{
		"fp-shared": platform.BehaviorShared,
		"fp-gone":   platform.BehaviorRemoved,
		"fp-new":    platform.BehaviorAdded,
	} {
		if got := deltaFor(t, diff, id).Presence; got != want {
			t.Errorf("%s presence = %q, want %q", id, got, want)
		}
	}
	if diff.AddedCount() != 1 || diff.RemovedCount() != 1 || diff.SharedCount() != 1 {
		t.Errorf("added/removed/shared = %d/%d/%d, want 1/1/1",
			diff.AddedCount(), diff.RemovedCount(), diff.SharedCount())
	}

	// An Added behavior has no reference presence, and vice versa.
	added := deltaFor(t, diff, "fp-new")
	if added.ReferenceCount != 0 || added.ReferenceRate != 0 {
		t.Errorf("added behavior has reference presence: %+v", added)
	}
	removed := deltaFor(t, diff, "fp-gone")
	if removed.CandidateCount != 0 || removed.CandidateRate != 0 {
		t.Errorf("removed behavior has candidate presence: %+v", removed)
	}
}

// TestCompareFrequencyShift pins the exact arithmetic, including the sign of
// the delta. It deliberately asserts nothing about whether the shift is good.
func TestCompareFrequencyShift(t *testing.T) {
	ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile),
		map[string]int{"fp-a": 9, "fp-b": 1})
	cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile),
		map[string]int{"fp-a": 5, "fp-b": 5})

	diff, err := platform.CompareBehaviorSnapshots(ref, cand)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}

	a := deltaFor(t, diff, "fp-a")
	if a.ReferenceCount != 9 || a.CandidateCount != 5 {
		t.Errorf("fp-a counts = %d/%d, want 9/5", a.ReferenceCount, a.CandidateCount)
	}
	if a.ReferenceRate != 0.9 || a.CandidateRate != 0.5 {
		t.Errorf("fp-a rates = %v/%v, want 0.9/0.5", a.ReferenceRate, a.CandidateRate)
	}
	if a.RateDelta != 0.5-0.9 {
		t.Errorf("fp-a RateDelta = %v, want %v", a.RateDelta, 0.5-0.9)
	}

	b := deltaFor(t, diff, "fp-b")
	if b.ReferenceRate != 0.1 || b.CandidateRate != 0.5 {
		t.Errorf("fp-b rates = %v/%v, want 0.1/0.5", b.ReferenceRate, b.CandidateRate)
	}
	if b.RateDelta != 0.5-0.1 {
		t.Errorf("fp-b RateDelta = %v, want %v", b.RateDelta, 0.5-0.1)
	}
}

// TestCompareNormalizesAcrossDifferentRunLengths: rates are each snapshot's
// share of its own observations, which is what makes a long reference
// comparable to a short candidate.
func TestCompareNormalizesAcrossDifferentRunLengths(t *testing.T) {
	ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile),
		map[string]int{"fp-a": 900, "fp-b": 100})
	cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile),
		map[string]int{"fp-a": 90, "fp-b": 10})

	diff, err := platform.CompareBehaviorSnapshots(ref, cand)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	for _, id := range []string{"fp-a", "fp-b"} {
		d := deltaFor(t, diff, id)
		if d.RateDelta != 0 {
			t.Errorf("%s RateDelta = %v, want 0: identical proportions at different scales", id, d.RateDelta)
		}
	}
	if diff.ReferenceObservationCount() != 1000 || diff.CandidateObservationCount() != 100 {
		t.Errorf("observation counts = %d/%d, want 1000/100",
			diff.ReferenceObservationCount(), diff.CandidateObservationCount())
	}
}

func TestCompareEmptySnapshots(t *testing.T) {
	empty := func(id platform.EvaluationRunID) platform.BehaviorSnapshot {
		return newCollector(t, behaviorRun(t, id, "cand-x", testEnvironment, testProfile)).Snapshot()
	}
	populated := snapshotOf(t, behaviorRun(t, "run-p", "cand-p", testEnvironment, testProfile),
		map[string]int{"fp-a": 2, "fp-b": 1})

	t.Run("both empty", func(t *testing.T) {
		diff, err := platform.CompareBehaviorSnapshots(empty("run-1"), empty("run-2"))
		if err != nil {
			t.Fatalf("Compare() error = %v — an empty run is valid, not malformed", err)
		}
		if len(diff.Deltas()) != 0 || diff.AddedCount() != 0 || diff.RemovedCount() != 0 {
			t.Errorf("expected an empty diff, got %+v", diff.Deltas())
		}
	})

	t.Run("empty reference, populated candidate", func(t *testing.T) {
		diff, err := platform.CompareBehaviorSnapshots(empty("run-1"), populated)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if diff.AddedCount() != 2 || diff.RemovedCount() != 0 || diff.SharedCount() != 0 {
			t.Fatalf("added/removed/shared = %d/%d/%d, want 2/0/0",
				diff.AddedCount(), diff.RemovedCount(), diff.SharedCount())
		}
		for _, d := range diff.Deltas() {
			if d.Presence != platform.BehaviorAdded || d.ReferenceRate != 0 {
				t.Errorf("%+v: want every behavior added with zero reference rate", d)
			}
		}
	})

	t.Run("populated reference, empty candidate", func(t *testing.T) {
		diff, err := platform.CompareBehaviorSnapshots(populated, empty("run-2"))
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		if diff.RemovedCount() != 2 || diff.AddedCount() != 0 {
			t.Fatalf("added/removed = %d/%d, want 0/2", diff.AddedCount(), diff.RemovedCount())
		}
		for _, d := range diff.Deltas() {
			if d.Presence != platform.BehaviorRemoved || d.CandidateRate != 0 {
				t.Errorf("%+v: want every behavior removed with zero candidate rate", d)
			}
		}
	})
}

// TestCompareRequiresMatchingEnvironments: environment is a stable fingerprint
// dimension, so comparing across environments would classify every behavior as
// simultaneously added and removed.
func TestCompareRequiresMatchingEnvironments(t *testing.T) {
	staging := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", "staging", testProfile),
		map[string]int{"fp-a": 1})
	production := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", "production", testProfile),
		map[string]int{"fp-a": 1})

	got, err := platform.CompareBehaviorSnapshots(staging, production)
	if !errors.Is(err, platform.ErrBehaviorEnvironmentMismatch) {
		t.Fatalf("Compare() error = %v, want one wrapping ErrBehaviorEnvironmentMismatch", err)
	}
	if len(got.Deltas()) != 0 {
		t.Error("a refused comparison returned deltas")
	}
}

// TestCompareAllowsDifferingProfilesAndCandidates is the other half: task 051
// kept the learning scope out of behavioral identity on purpose, so requiring
// profiles to match would make isolation look like behavioral change. Nor is a
// candidate's identity a behavior's identity.
func TestCompareAllowsDifferingProfilesAndCandidates(t *testing.T) {
	ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-A", testEnvironment, "profile-reference"),
		map[string]int{"fp-a": 2})
	cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-B", testEnvironment, "profile-candidate"),
		map[string]int{"fp-a": 2})

	diff, err := platform.CompareBehaviorSnapshots(ref, cand)
	if err != nil {
		t.Fatalf("Compare() error = %v — differing profiles and candidates must be comparable", err)
	}
	if diff.SharedCount() != 1 || diff.AddedCount() != 0 {
		t.Fatalf("shared/added = %d/%d, want 1/0: identical behavior under different profiles is the same behavior",
			diff.SharedCount(), diff.AddedCount())
	}

	// And the diff records whose evidence it compared.
	if diff.ReferenceCandidateID() != "cand-A" || diff.CandidateCandidateID() != "cand-B" {
		t.Errorf("candidate identity not captured: %q vs %q",
			diff.ReferenceCandidateID(), diff.CandidateCandidateID())
	}
	if diff.ReferenceBehavioralProfile() != "profile-reference" ||
		diff.CandidateBehavioralProfile() != "profile-candidate" {
		t.Error("profile refs not captured")
	}
	if diff.Environment() != testEnvironment {
		t.Errorf("Environment() = %q, want %q", diff.Environment(), testEnvironment)
	}
}

// TestCompareRejectsCrossSnapshotFingerprintConflict: two snapshots may have
// been built independently, so the conflict check runs again here.
func TestCompareRejectsCrossSnapshotFingerprintConflict(t *testing.T) {
	refCollector := newCollector(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile))
	observe(t, refCollector, behaviorRecord("evt-1", "fp-a", "shell.read", "build-host", testEnvironment))

	candCollector := newCollector(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile))
	observe(t, candCollector, behaviorRecord("evt-2", "fp-a", "shell.execute", "build-host", testEnvironment))

	got, err := platform.CompareBehaviorSnapshots(refCollector.Snapshot(), candCollector.Snapshot())
	if !errors.Is(err, platform.ErrFingerprintConflict) {
		t.Fatalf("Compare() error = %v, want one wrapping ErrFingerprintConflict", err)
	}
	if len(got.Deltas()) != 0 {
		t.Error("a refused comparison returned deltas")
	}
}

// TestCompareOutputIsDeterministic: identical evidence delivered in different
// orders must produce identical, identically-ordered diffs.
func TestCompareOutputIsDeterministic(t *testing.T) {
	counts := map[string]int{"fp-m": 2, "fp-a": 1, "fp-z": 3, "fp-c": 1}

	build := func() platform.BehaviorDiff {
		ref := snapshotOf(t, behaviorRun(t, "run-1", "cand-1", testEnvironment, testProfile), counts)
		cand := snapshotOf(t, behaviorRun(t, "run-2", "cand-2", testEnvironment, testProfile),
			map[string]int{"fp-a": 1, "fp-z": 3, "fp-new": 1})
		diff, err := platform.CompareBehaviorSnapshots(ref, cand)
		if err != nil {
			t.Fatalf("Compare() error = %v", err)
		}
		return diff
	}

	first, second := build(), build()
	a, b := first.Deltas(), second.Deltas()
	if len(a) != len(b) {
		t.Fatalf("different delta counts: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("delta %d differs:\n %+v\nvs %+v", i, a[i], b[i])
		}
		if i > 0 && a[i-1].FingerprintID >= a[i].FingerprintID {
			t.Fatalf("deltas are not ascending: %q then %q", a[i-1].FingerprintID, a[i].FingerprintID)
		}
	}
}

// TestCompareIsBounded: two disjoint full snapshots produce exactly 1024
// deltas and no more.
func TestCompareIsBounded(t *testing.T) {
	fill := func(runID platform.EvaluationRunID, prefix string) platform.BehaviorSnapshot {
		c := newCollector(t, behaviorRun(t, runID, "cand-1", testEnvironment, testProfile))
		for i := range behaviorCapacity {
			observe(t, c, behaviorRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("%s-%04d", prefix, i),
				fmt.Sprintf("tool.%d", i), "build-host", testEnvironment))
		}
		return c.Snapshot()
	}

	diff, err := platform.CompareBehaviorSnapshots(fill("run-1", "ref"), fill("run-2", "cand"))
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}
	if got := len(diff.Deltas()); got != 2*behaviorCapacity {
		t.Fatalf("len(Deltas()) = %d, want %d", got, 2*behaviorCapacity)
	}
	if diff.AddedCount() != behaviorCapacity || diff.RemovedCount() != behaviorCapacity {
		t.Errorf("added/removed = %d/%d, want %d/%d",
			diff.AddedCount(), diff.RemovedCount(), behaviorCapacity, behaviorCapacity)
	}
	if diff.SharedCount() != 0 {
		t.Errorf("SharedCount() = %d, want 0: the snapshots are disjoint", diff.SharedCount())
	}
}

// TestDiffExposesNoInterpretation: the diff reports facts. Whether an added
// behavior is acceptable needs thresholds, and thresholds are configuration
// belonging to the gate task.
func TestDiffExposesNoInterpretation(t *testing.T) {
	forbidden := []string{
		"DriftScore", "StabilityScore", "RiskScore", "Score", "Grade", "Severity",
		"Passed", "Failed", "Promotable", "AcceptableChange", "AllowedToShip",
		"Recommendation", "Verdict",
	}
	assertNoMethods(t, "BehaviorDiff", platform.BehaviorDiff{}, forbidden)
	assertNoMethods(t, "BehaviorSnapshot", platform.BehaviorSnapshot{}, forbidden)
}

// ---------------------------------------------------------------------
// The cross-module proof
// ---------------------------------------------------------------------

// TestRealEngineBehavioralDiff runs the whole chain on real engine output.
//
// Two evaluations that share one behavior, drop one, and add one. The
// fingerprints are never hard-coded: behaviors are located through their
// stable descriptors, which is also how a CLI would present them, and which
// keeps this test working if the core's hash implementation ever changes.
func TestRealEngineBehavioralDiff(t *testing.T) {
	const environment = "staging"

	// analyze runs one tool operation through a fresh engine and returns the
	// public record.
	analyze := func(t *testing.T, engine *trustvian.Engine, id, operation, target string, at time.Time) trustvian.DecisionRecord {
		t.Helper()
		ev := event.Event{
			ID:        id,
			Timestamp: at,
			Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
			Operation: event.Operation{Category: event.OperationCategoryTool, Name: operation},
			Target:    event.Target{Name: target, Category: event.TargetCategoryExternal},
			Context:   event.Context{Environment: environment},
		}
		result, err := engine.Analyze(t.Context(), ev)
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		return result.DecisionRecord()
	}

	collect := func(t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
		profile platform.BehavioralProfileRef, operations []string,
	) platform.BehaviorSnapshot {
		t.Helper()
		engine := trustvian.NewEngine(trustvian.WithLearningScope(string(profile)))
		c := newCollector(t, behaviorRun(t, runID, candidateID, environment, profile))

		clock := aggEpoch
		for i, op := range operations {
			clock = clock.Add(90 * time.Second)
			rec := analyze(t, engine, fmt.Sprintf("evt-%s-%d", candidateID, i), op, "build-host", clock)
			if err := c.Observe(rec); err != nil {
				t.Fatalf("Observe() rejected a record a real Engine produced: %v", err)
			}
		}
		return c.Snapshot()
	}

	reference := collect(t, "run-ref", "cand-ref", "profile-ref",
		[]string{"shell.read", "db.query", "shell.read"})
	candidate := collect(t, "run-cand", "cand-new", "profile-cand",
		[]string{"shell.read", "shell.execute"})

	diff, err := platform.CompareBehaviorSnapshots(reference, candidate)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}

	// Locate behaviors by descriptor, never by hash.
	presenceOf := func(operation string) platform.BehaviorPresence {
		t.Helper()
		for _, d := range diff.Deltas() {
			if d.Behavior.OperationName == operation {
				return d.Presence
			}
		}
		t.Fatalf("no delta for operation %q; deltas: %+v", operation, diff.Deltas())
		return ""
	}

	for operation, want := range map[string]platform.BehaviorPresence{
		"shell.read":    platform.BehaviorShared,
		"db.query":      platform.BehaviorRemoved,
		"shell.execute": platform.BehaviorAdded,
	} {
		if got := presenceOf(operation); got != want {
			t.Errorf("%s presence = %q, want %q", operation, got, want)
		}
	}

	if diff.AddedCount() != 1 || diff.RemovedCount() != 1 || diff.SharedCount() != 1 {
		t.Fatalf("added/removed/shared = %d/%d/%d, want 1/1/1",
			diff.AddedCount(), diff.RemovedCount(), diff.SharedCount())
	}

	// The shared behavior's frequency moved, because the reference ran it
	// twice out of three and the candidate once out of two.
	shared := deltaFor(t, diff, fingerprintOf(t, diff, "shell.read"))
	if shared.ReferenceCount != 2 || shared.CandidateCount != 1 {
		t.Errorf("shell.read counts = %d/%d, want 2/1", shared.ReferenceCount, shared.CandidateCount)
	}
	if shared.ReferenceRate != 2.0/3.0 || shared.CandidateRate != 0.5 {
		t.Errorf("shell.read rates = %v/%v, want %v/0.5",
			shared.ReferenceRate, shared.CandidateRate, 2.0/3.0)
	}

	// Two different candidates under two different learning scopes still
	// agree that shell.read is one behavior — profile is not behavioral
	// identity.
	if diff.ReferenceBehavioralProfile() == diff.CandidateBehavioralProfile() {
		t.Fatal("the test did not actually use different profiles")
	}
}

func fingerprintOf(t *testing.T, diff platform.BehaviorDiff, operation string) string {
	t.Helper()
	for _, d := range diff.Deltas() {
		if d.Behavior.OperationName == operation {
			return d.FingerprintID
		}
	}
	t.Fatalf("no behavior named %q", operation)
	return ""
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

func benchCollector(b *testing.B) *platform.BehaviorCollector {
	b.Helper()
	run, err := platform.NewEvaluationRun("run-1", "cand-1", testEnvironment, testProfile, aggEpoch)
	if err != nil {
		b.Fatalf("NewEvaluationRun() error = %v", err)
	}
	c, err := platform.NewBehaviorCollector(run)
	if err != nil {
		b.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	return c
}

func BenchmarkBehaviorCollectorObserveExisting(b *testing.B) {
	c := benchCollector(b)
	rec := behaviorRecord("evt-1", "fp-a", "shell.execute", "build-host", testEnvironment)
	if err := c.Observe(rec); err != nil {
		b.Fatalf("Observe() error = %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if err := c.Observe(rec); err != nil {
			b.Fatalf("Observe() error = %v", err)
		}
	}
}

func BenchmarkBehaviorCollectorObserveNew(b *testing.B) {
	b.ReportAllocs()
	c := benchCollector(b)
	i := 0
	for b.Loop() {
		// A fresh collector every capacity cycle: this measures admitting a
		// new fingerprint, which is bounded at 512 per collector.
		if i%behaviorCapacity == 0 {
			b.StopTimer()
			c = benchCollector(b)
			b.StartTimer()
		}
		rec := behaviorRecord("evt", fmt.Sprintf("fp-%04d", i%behaviorCapacity),
			"shell.execute", "build-host", testEnvironment)
		if err := c.Observe(rec); err != nil {
			b.Fatalf("Observe() error = %v", err)
		}
		i++
	}
}

func benchSnapshot(b *testing.B, runID platform.EvaluationRunID, prefix string, entries int) platform.BehaviorSnapshot {
	b.Helper()
	run, err := platform.NewEvaluationRun(runID, "cand-1", testEnvironment, testProfile, aggEpoch)
	if err != nil {
		b.Fatalf("NewEvaluationRun() error = %v", err)
	}
	c, err := platform.NewBehaviorCollector(run)
	if err != nil {
		b.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range entries {
		rec := behaviorRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("%s-%04d", prefix, i),
			fmt.Sprintf("tool.%d", i), "build-host", testEnvironment)
		if err := c.Observe(rec); err != nil {
			b.Fatalf("Observe() error = %v", err)
		}
	}
	return c.Snapshot()
}

func BenchmarkCompareBehaviorSnapshotsTypical(b *testing.B) {
	ref := benchSnapshot(b, "run-1", "fp", 32)
	cand := benchSnapshot(b, "run-2", "fp", 32)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := platform.CompareBehaviorSnapshots(ref, cand); err != nil {
			b.Fatalf("Compare() error = %v", err)
		}
	}
}

func BenchmarkCompareBehaviorSnapshotsFull(b *testing.B) {
	// Disjoint and full: the worst case, producing the maximum 1024 deltas.
	ref := benchSnapshot(b, "run-1", "ref", behaviorCapacity)
	cand := benchSnapshot(b, "run-2", "cand", behaviorCapacity)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := platform.CompareBehaviorSnapshots(ref, cand); err != nil {
			b.Fatalf("Compare() error = %v", err)
		}
	}
}
