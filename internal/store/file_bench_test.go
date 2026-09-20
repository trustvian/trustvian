package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/store"
)

// BenchmarkFileStoreObserveSameKey is FileStore's counterpart to
// BenchmarkInMemoryObserveSameKey: every goroutine observes the same
// actor's Baseline. Unlike InMemory, every Observe call here also
// flushes the store's full contents to disk (see FileStore's doc
// comment), so this measures persistence's real cost, not just lock
// contention.
func BenchmarkFileStoreObserveSameKey(b *testing.B) {
	s, err := store.NewFileStore(filepath.Join(b.TempDir(), "baseline.json"))
	if err != nil {
		b.Fatal(err)
	}
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

// BenchmarkFileStoreObserveDistinctKeys mirrors
// BenchmarkInMemoryObserveDistinctKeys: each goroutine observes its own
// actor. In-memory updates still shard by key exactly as InMemory does,
// but every Observe flushes the *entire* store regardless of which key
// changed — this benchmark's store therefore grows across the run as
// more distinct keys are added, unlike the same-key case, showing how
// per-Observe flush cost scales with total store size, not per-key
// contention.
func BenchmarkFileStoreObserveDistinctKeys(b *testing.B) {
	s, err := store.NewFileStore(filepath.Join(b.TempDir(), "baseline.json"))
	if err != nil {
		b.Fatal(err)
	}
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
