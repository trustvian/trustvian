package postgres_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/store/postgres"
)

// These benchmarks are the PostgreSQL counterparts to
// internal/store's BenchmarkInMemory*/BenchmarkFileStore*, split the same
// way — same key versus distinct keys — because the two measure different
// things and a blended number would hide both. Same-key measures the row
// lock's serialization cost; distinct-keys measures throughput when
// nothing contends.
//
// Every number they produce is dominated by network round-trips to the
// server and is therefore a property of the deployment, not of this code.
// They exist to catch a *regression* — an extra round-trip per Observe, a
// statement that stopped using the index — not to advertise a latency
// figure. Read them as a ratio against each other and against the run
// that preceded a change, never as an absolute. See docs/PERFORMANCE.md.
//
// benchDSN gates every benchmark here on TRUSTVIAN_TEST_POSTGRES_DSN, so
// `go test -bench=.` on a machine without PostgreSQL skips rather than
// fails.
func benchDSN(b *testing.B) string {
	b.Helper()

	dsn := os.Getenv("TRUSTVIAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		b.Skip("TRUSTVIAN_TEST_POSTGRES_DSN not set; see docs/storage-guide.md")
	}
	return dsn
}

// newBenchStore mirrors newStore's schema isolation. A benchmark that
// shared the table with a concurrently-running test binary would measure
// that interference rather than the store.
func newBenchStore(b *testing.B, dsn string) *postgres.Store {
	b.Helper()

	s, err := postgres.NewStore(context.Background(), postgres.Config{
		DSN: isolatedSchemaDSN(b, dsn),
	})
	if err != nil {
		b.Fatalf("NewStore() error = %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	return s
}

// BenchmarkObserveSameKey has every goroutine observe one actor's
// Baseline, so all of them contend on the same row lock. This is the
// worst case for write throughput and the number to watch if the locking
// strategy ever changes.
func BenchmarkObserveSameKey(b *testing.B) {
	s := newBenchStore(b, benchDSN(b))
	fp := testFingerprint()
	key := baseline.Key{ActorID: "bench-actor", Environment: "production"}
	ctx := context.Background()

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, time.Now()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkObserveDistinctKeys gives each goroutine its own actor, so no
// two contend on a row. Distinct keys are distinct rows by construction
// (the primary key is {actor_id, environment}), so this is the case a
// real multi-actor deployment spends most of its time in.
func BenchmarkObserveDistinctKeys(b *testing.B) {
	s := newBenchStore(b, benchDSN(b))
	fp := testFingerprint()
	ctx := context.Background()

	var counter atomic.Int64

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		id := counter.Add(1)
		key := baseline.Key{ActorID: fmt.Sprintf("bench-actor-%d", id), Environment: "production"}
		for pb.Next() {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, time.Now()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkGet measures the read path, which is what Engine.Analyze calls
// on every single event — so it matters more for end-to-end latency than
// either Observe benchmark. One statement, no transaction, no lock.
func BenchmarkGet(b *testing.B) {
	s := newBenchStore(b, benchDSN(b))
	fp := testFingerprint()
	key := baseline.Key{ActorID: "bench-get-actor", Environment: "production"}
	ctx := context.Background()

	// A populated baseline, so this measures decoding real state rather
	// than the empty-row path.
	now := testTime
	for range 20 {
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			b.Fatal(err)
		}
		now = now.Add(time.Second)
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, ok := s.Get(ctx, key); !ok {
				b.Fatal("Get() ok = false, want true")
			}
		}
	})
}
