package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store"
)

var testKey = baseline.Key{ActorID: "svc-payment", Environment: "production"}

func testFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     "POST /payment",
		TargetName:        "payment-db",
		Environment:       "production",
	})
}

func TestInMemoryGetMissingKey(t *testing.T) {
	s := store.NewInMemory()

	b, ok := s.Get(context.Background(), testKey)
	if ok {
		t.Fatalf("Get() ok = true for a never-observed key")
	}
	if b.Key != testKey {
		t.Fatalf("Get() Key = %+v, want %+v", b.Key, testKey)
	}
	if len(b.Fingerprints) != 0 {
		t.Fatalf("Get() on missing key returned non-empty Fingerprints: %v", b.Fingerprints)
	}
}

func TestInMemoryObserveThenGet(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	observed, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{HasLatency: true, Latency: 10 * time.Millisecond}, time.Now())
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if observed.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Observe() Count = %d, want 1", observed.Fingerprints[fp.ID].Count)
	}

	got, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after Observe")
	}
	if got.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Get() Count = %d, want 1", got.Fingerprints[fp.ID].Count)
	}
}

func TestInMemoryGetSnapshotUnaffectedByLaterObserve(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	snapshot, _ := s.Get(ctx, testKey)

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	if got := snapshot.Fingerprints[fp.ID].Count; got != 1 {
		t.Fatalf("earlier Get() snapshot changed after a later Observe: Count = %d, want 1", got)
	}
}

func TestInMemoryObserveConcurrentSameKey(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	const goroutines = 50
	const perGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
					t.Errorf("Observe() error = %v", err)
				}
			}
		}()
	}
	wg.Wait()

	got, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after concurrent Observe calls")
	}
	want := uint64(goroutines * perGoroutine)
	if got := got.Fingerprints[fp.ID].Count; got != want {
		t.Fatalf("Count = %d after %d concurrent Observe calls, want %d (lost update)", got, want, want)
	}
}

func TestInMemoryObserveConcurrentDistinctKeys(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	const actors = 100
	var wg sync.WaitGroup
	wg.Add(actors)
	for i := range actors {
		go func(i int) {
			defer wg.Done()
			key := baseline.Key{ActorID: fmt.Sprintf("actor-%d", i), Environment: "production"}
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, time.Now()); err != nil {
				t.Errorf("Observe() error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	for i := range actors {
		key := baseline.Key{ActorID: fmt.Sprintf("actor-%d", i), Environment: "production"}
		b, ok := s.Get(ctx, key)
		if !ok {
			t.Fatalf("actor-%d: Get() ok = false", i)
		}
		if got := b.Fingerprints[fp.ID].Count; got != 1 {
			t.Fatalf("actor-%d: Count = %d, want 1", i, got)
		}
	}
}

func TestInMemoryFreezeMakesObserveANoOp(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	s.Freeze(ctx, testKey)
	if !s.IsFrozen(ctx, testKey) {
		t.Fatalf("IsFrozen() = false immediately after Freeze()")
	}

	before, _ := s.Get(ctx, testKey)
	got, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now())
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if got.Fingerprints[fp.ID].Count != before.Fingerprints[fp.ID].Count {
		t.Fatalf("Observe() on a frozen key changed Count: before=%d after=%d",
			before.Fingerprints[fp.ID].Count, got.Fingerprints[fp.ID].Count)
	}
	if got.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Count = %d after a frozen Observe, want unchanged at 1", got.Fingerprints[fp.ID].Count)
	}

	// Get must still return full history while frozen.
	current, ok := s.Get(ctx, testKey)
	if !ok || current.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Get() while frozen = %+v, ok=%v, want full history intact", current, ok)
	}
}

func TestInMemoryUnfreezeResumesLearning(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	s.Freeze(ctx, testKey)
	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if b, _ := s.Get(ctx, testKey); b.Fingerprints[fp.ID].Count != 0 {
		t.Fatalf("Count = %d while frozen, want 0 (never learned)", b.Fingerprints[fp.ID].Count)
	}

	s.Unfreeze(ctx, testKey)
	if s.IsFrozen(ctx, testKey) {
		t.Fatalf("IsFrozen() = true after Unfreeze()")
	}
	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if b, _ := s.Get(ctx, testKey); b.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Count = %d after Unfreeze, want 1", b.Fingerprints[fp.ID].Count)
	}
}

func TestInMemoryIsFrozenDefaultsFalse(t *testing.T) {
	s := store.NewInMemory()
	ctx := context.Background()

	if s.IsFrozen(ctx, testKey) {
		t.Fatalf("IsFrozen() = true for a key that was never touched, want false")
	}
}

func TestInMemoryFreezeIsPerKey(t *testing.T) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()
	otherKey := baseline.Key{ActorID: "other-actor", Environment: "production"}

	s.Freeze(ctx, testKey)

	if _, _, err := s.Observe(ctx, otherKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if b, _ := s.Get(ctx, otherKey); b.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Count = %d for an unrelated, unfrozen key, want 1 (freeze must not leak across keys)", b.Fingerprints[fp.ID].Count)
	}
}

// destinationFingerprint is TestInMemoryObserveConcurrentTransitionTracking's
// shared destination: every goroutine observes its own distinct
// predecessor fingerprint, then this same one, so the resulting
// PredecessorCounts map is actually exercised under real contention —
// unlike TestInMemoryObserveConcurrentSameKey above, which always
// observes one unchanging fingerprint and so never touches the
// predecessor-tracking path at all.
func destinationFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryDB,
		OperationName:     "DELETE customer",
		TargetName:        "customer-db",
		Environment:       "production",
	})
}

// TestInMemoryObserveConcurrentTransitionTracking is the v0.6
// foundation's own dedicated concurrency proof (see docs/tasks/025-sequence-analysis-foundation.md
// § Concurrency): many goroutines, each with its own distinct
// predecessor fingerprint, race to observe (predecessor, then the
// shared destination) for the *same* Key. Baseline.Observe's
// transition bookkeeping (LastFingerprintID/Time, PredecessorCounts)
// must remain race-free and internally consistent under this — proven
// by running under `go test -race`, not merely by not panicking.
func TestInMemoryObserveConcurrentTransitionTracking(t *testing.T) {
	s := store.NewInMemory()
	dest := destinationFingerprint()
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			pred := fingerprint.Compute(features.StableFeatures{
				ActorType:         event.ActorTypeService,
				OperationCategory: event.OperationCategoryDB,
				OperationName:     fmt.Sprintf("predecessor-%d", i),
				TargetName:        "customer-db",
				Environment:       "production",
			})
			now := time.Now()
			if _, _, err := s.Observe(ctx, testKey, pred, features.VolatileFeatures{}, now); err != nil {
				t.Errorf("Observe(predecessor) error = %v", err)
			}
			if _, _, err := s.Observe(ctx, testKey, dest, features.VolatileFeatures{}, now.Add(time.Millisecond)); err != nil {
				t.Errorf("Observe(destination) error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	got, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after concurrent Observe calls")
	}

	// goroutines predecessors + 1 shared destination.
	if want := goroutines + 1; len(got.Fingerprints) != want {
		t.Fatalf("len(Fingerprints) = %d, want %d", len(got.Fingerprints), want)
	}
	if got.Fingerprints[dest.ID].Count != goroutines {
		t.Fatalf("dest Count = %d, want %d", got.Fingerprints[dest.ID].Count, goroutines)
	}

	// The bound must hold even under concurrent writers racing to add
	// distinct predecessors — recordPredecessor's "refuse past the
	// bound" policy must never be bypassed by a race. Real wall-clock
	// interleaving across goroutines is inherently nondeterministic
	// (which specific predecessor immediately preceded a given dest
	// observation — or whether dest even had a valid predecessor at
	// all at that instant — depends on scheduling), so this test
	// checks the invariants that must hold regardless of interleaving,
	// not exact per-predecessor counts.
	predCounts := got.Fingerprints[dest.ID].PredecessorCounts
	if len(predCounts) > 64 {
		t.Fatalf("len(PredecessorCounts) = %d, want <= 64 (bound must hold under concurrency)", len(predCounts))
	}

	var total uint64
	for _, count := range predCounts {
		total += count
	}
	if total > got.Fingerprints[dest.ID].Count {
		t.Fatalf("sum(PredecessorCounts) = %d, want <= dest.Count (%d) — each dest observation can contribute at most one predecessor count", total, got.Fingerprints[dest.ID].Count)
	}
}

// TestInMemoryObserveConcurrentTrigramTracking is task 027's own
// dedicated concurrency proof, mirroring
// TestInMemoryObserveConcurrentTransitionTracking one level up: many
// goroutines, each with its own distinct grandparent fingerprint, race
// to observe (grandparent, then the *same shared* predecessor, then
// the *same shared* destination) for the same Key — stressing both new
// bounded maps (TrigramCounts on the destination, keyed by
// (grandparent, predecessor) pairs; TrigramContinuationTotal on the
// shared predecessor, keyed by grandparent) under real concurrent
// contention. Proven by running under `go test -race`, not merely by
// not panicking.
func TestInMemoryObserveConcurrentTrigramTracking(t *testing.T) {
	s := store.NewInMemory()
	predecessor := destinationFingerprint()
	dest := fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryDB,
		OperationName: "EXPORT customer", TargetName: "customer-db", Environment: "production",
	})
	ctx := context.Background()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(i int) {
			defer wg.Done()
			grandparent := fingerprint.Compute(features.StableFeatures{
				ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryDB,
				OperationName: fmt.Sprintf("grandparent-%d", i), TargetName: "customer-db", Environment: "production",
			})
			now := time.Now()
			if _, _, err := s.Observe(ctx, testKey, grandparent, features.VolatileFeatures{}, now); err != nil {
				t.Errorf("Observe(grandparent) error = %v", err)
			}
			if _, _, err := s.Observe(ctx, testKey, predecessor, features.VolatileFeatures{}, now.Add(time.Millisecond)); err != nil {
				t.Errorf("Observe(predecessor) error = %v", err)
			}
			if _, _, err := s.Observe(ctx, testKey, dest, features.VolatileFeatures{}, now.Add(2*time.Millisecond)); err != nil {
				t.Errorf("Observe(dest) error = %v", err)
			}
		}(i)
	}
	wg.Wait()

	got, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after concurrent Observe calls")
	}

	// Both new bounded maps must hold their independent bound even
	// under concurrent writers — real wall-clock interleaving is
	// inherently nondeterministic (whether a given dest observation
	// actually completed a valid 3-gram at all depends on scheduling),
	// so this checks the invariants that must hold regardless of
	// interleaving, not exact per-pair counts.
	trigramCounts := got.Fingerprints[dest.ID].TrigramCounts
	if len(trigramCounts) > 64 {
		t.Fatalf("len(TrigramCounts) = %d, want <= 64 (bound must hold under concurrency)", len(trigramCounts))
	}
	continuationTotal := got.Fingerprints[predecessor.ID].TrigramContinuationTotal
	if len(continuationTotal) > 64 {
		t.Fatalf("len(TrigramContinuationTotal) = %d, want <= 64 (bound must hold under concurrency)", len(continuationTotal))
	}

	var trigramSum uint64
	for _, count := range trigramCounts {
		trigramSum += count
	}
	if trigramSum > got.Fingerprints[dest.ID].Count {
		t.Fatalf("sum(TrigramCounts) = %d, want <= dest.Count (%d)", trigramSum, got.Fingerprints[dest.ID].Count)
	}
}
