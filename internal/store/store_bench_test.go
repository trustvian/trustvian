package store_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store"
)

// BenchmarkInMemoryObserveSameKey measures worst-case lock contention: every
// goroutine observes the same actor's Baseline.
func BenchmarkInMemoryObserveSameKey(b *testing.B) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkInMemoryObserveDistinctKeys measures best-case contention: each
// goroutine observes its own actor's Baseline, so per-key locks never
// collide. Comparing this against BenchmarkInMemoryObserveSameKey
// quantifies the benefit of sharding the lock by Key.
func BenchmarkInMemoryObserveDistinctKeys(b *testing.B) {
	s := store.NewInMemory()
	fp := testFingerprint()
	ctx := context.Background()

	var counter atomic.Int64
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		key := baseline.Key{ActorID: fmt.Sprintf("actor-%d", counter.Add(1)), Environment: "production"}
		for pb.Next() {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, time.Now()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// growthFingerprint returns a distinct Fingerprint for index i, mirroring
// testFingerprint but varying TargetName so each simulated actor also
// carries its own distinct fingerprint — matching how a real deployment
// with n distinct actors would also see n distinct behavioral shapes,
// not n actors all sharing one fingerprint.
func growthFingerprint(i int) fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     "POST /payment",
		TargetName:        fmt.Sprintf("target-%d", i),
		Environment:       "production",
	})
}

// BenchmarkInMemoryMemoryGrowth characterizes steady-state Observe cost
// as the number of distinct (ActorID, Environment) keys — and distinct
// Fingerprints — an InMemory store holds grows, since InMemory has no
// eviction/expiration (see docs/PERFORMANCE.md's "what's not benchmarked"
// note this closes). Each sub-benchmark pre-populates a fresh store with
// n distinct keys/fingerprints, then repeatedly cycles through the same n
// keys — comparing B/op and allocs/op across n shows whether per-call
// cost stays flat (a large, already-populated map costing no more per
// Observe than a small one) or grows with the store's size.
func BenchmarkInMemoryMemoryGrowth(b *testing.B) {
	for _, n := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("keys=%d", n), func(b *testing.B) {
			ctx := context.Background()
			s := store.NewInMemory()
			now := time.Now()

			// Pre-populate: every one of the n keys must exist before the
			// timed loop, so the timed portion measures steady-state
			// Observe cost at a store already holding n keys, not the
			// one-time cost of first-touch shard creation.
			keys := make([]baseline.Key, n)
			fps := make([]fingerprint.Fingerprint, n)
			for i := range n {
				keys[i] = baseline.Key{ActorID: fmt.Sprintf("actor-%d", i), Environment: "production"}
				fps[i] = growthFingerprint(i)
				if _, _, err := s.Observe(ctx, keys[i], fps[i], features.VolatileFeatures{}, now); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			i := 0
			for b.Loop() {
				idx := i % n
				if _, _, err := s.Observe(ctx, keys[idx], fps[idx], features.VolatileFeatures{}, now); err != nil {
					b.Fatal(err)
				}
				i++
			}
		})
	}
}
