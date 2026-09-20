package store_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store"
)

func newFileStore(t *testing.T) (*store.FileStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baseline.json")
	s, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	return s, path
}

func TestFileStoreGetMissingKey(t *testing.T) {
	s, _ := newFileStore(t)

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

func TestFileStoreObserveThenGet(t *testing.T) {
	s, _ := newFileStore(t)
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

func TestFileStoreGetSnapshotUnaffectedByLaterObserve(t *testing.T) {
	s, _ := newFileStore(t)
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

func TestFileStoreObserveConcurrentSameKey(t *testing.T) {
	s, _ := newFileStore(t)
	fp := testFingerprint()
	ctx := context.Background()

	const goroutines = 20
	const perGoroutine = 25

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

func TestFileStoreObserveConcurrentDistinctKeys(t *testing.T) {
	s, _ := newFileStore(t)
	fp := testFingerprint()
	ctx := context.Background()

	const actors = 20
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

func TestFileStoreSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	fp := testFingerprint()
	ctx := context.Background()

	first, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	for range 5 {
		if _, _, err := first.Observe(ctx, testKey, fp, features.VolatileFeatures{HasLatency: true, Latency: 20 * time.Millisecond}, time.Now()); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	// A fresh FileStore instance against the same file — simulating a
	// process restart — must see everything the first instance wrote.
	second, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() (restart) error = %v", err)
	}
	got, ok := second.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after restart, want the 5 prior observations to survive")
	}
	stats := got.Fingerprints[fp.ID]
	if stats.Count != 5 {
		t.Fatalf("Count = %d after restart, want 5", stats.Count)
	}
	if stats.LatencyObservations != 5 || stats.LatencyMeanDuration() != 20*time.Millisecond {
		t.Fatalf("latency stats did not survive restart intact: %+v", stats)
	}
}

// TestFileStoreSurvivesRestartWithTrigramState is the one genuinely new
// persistence risk task 027 introduces: baseline.TrigramKey is a struct
// (unlike every other map key this package has ever persisted, which
// were plain strings) and needs encoding.TextMarshaler/TextUnmarshaler
// for encoding/json to accept it as a map key at all. This proves the
// full round trip through a real FileStore write-then-reopen cycle —
// not just TrigramKey's own MarshalText/UnmarshalText in isolation
// (see baseline_test.go's TestTrigramKeyTextRoundTrip) — actually
// preserves TrigramCounts/TrigramContinuationTotal correctly.
func TestFileStoreSurvivesRestartWithTrigramState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	fpA := testFingerprint()
	fpB := destinationFingerprint()
	fpC := fingerprint.Compute(features.StableFeatures{
		ActorType: event.ActorTypeService, OperationCategory: event.OperationCategoryHTTP,
		OperationName: "GET /export", TargetName: "payment-db", Environment: "production",
	})
	ctx := context.Background()

	first, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	now := time.Now()
	for range 3 {
		if _, _, err := first.Observe(ctx, testKey, fpA, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe(A) error = %v", err)
		}
		now = now.Add(time.Second)
		if _, _, err := first.Observe(ctx, testKey, fpB, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe(B) error = %v", err)
		}
		now = now.Add(time.Second)
		if _, _, err := first.Observe(ctx, testKey, fpC, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe(C) error = %v", err)
		}
		now = now.Add(time.Second)
	}

	// A fresh FileStore instance against the same file — simulating a
	// process restart.
	second, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() (restart) error = %v", err)
	}
	got, ok := second.Get(ctx, testKey)
	if !ok {
		t.Fatalf("Get() ok = false after restart")
	}

	key := baseline.TrigramKey{First: fpA.ID, Second: fpB.ID}
	if gotCount := got.Fingerprints[fpC.ID].TrigramCounts[key]; gotCount != 3 {
		t.Fatalf("TrigramCounts[{A,B}] for C after restart = %d, want 3", gotCount)
	}
	if gotTotal := got.Fingerprints[fpB.ID].TrigramContinuationTotal[fpA.ID]; gotTotal != 3 {
		t.Fatalf("TrigramContinuationTotal[A] for B after restart = %d, want 3", gotTotal)
	}
	if got.PreviousFingerprintID != fpB.ID {
		t.Fatalf("PreviousFingerprintID after restart = %q, want %q", got.PreviousFingerprintID, fpB.ID)
	}
}

func TestFileStoreOpeningEmptyPathStartsEmpty(t *testing.T) {
	// The file does not exist yet — the very first run.
	path := filepath.Join(t.TempDir(), "does-not-exist-yet.json")

	s, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() on a non-existent file: error = %v, want nil (first run)", err)
	}
	if _, ok := s.Get(context.Background(), testKey); ok {
		t.Fatalf("Get() ok = true on a freshly opened, never-written store")
	}
}

func TestFileStoreWritesAreAtomic(t *testing.T) {
	s, path := newFileStore(t)
	fp := testFingerprint()
	ctx := context.Background()

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file after a successful flush: %s (rename should have consumed it)", e.Name())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected store file to exist at %s: %v", path, err)
	}
}

func TestFileStoreFreezeMakesObserveANoOp(t *testing.T) {
	s, _ := newFileStore(t)
	fp := testFingerprint()
	ctx := context.Background()

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	s.Freeze(ctx, testKey)
	if !s.IsFrozen(ctx, testKey) {
		t.Fatalf("IsFrozen() = false after Freeze()")
	}

	if _, _, err := s.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	b, _ := s.Get(ctx, testKey)
	if b.Fingerprints[fp.ID].Count != 1 {
		t.Fatalf("Count = %d after a frozen Observe, want unchanged at 1", b.Fingerprints[fp.ID].Count)
	}
}

func TestFileStoreFreezeStateNotPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	fp := testFingerprint()
	ctx := context.Background()

	first, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if _, _, err := first.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	first.Freeze(ctx, testKey)

	second, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() (restart) error = %v", err)
	}
	if second.IsFrozen(ctx, testKey) {
		t.Fatalf("IsFrozen() = true after restart, want freeze state to reset (documented as not persisted)")
	}
	// And learning should work again post-restart.
	if _, _, err := second.Observe(ctx, testKey, fp, features.VolatileFeatures{}, time.Now()); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	b, _ := second.Get(ctx, testKey)
	if b.Fingerprints[fp.ID].Count != 2 {
		t.Fatalf("Count = %d after restart + one more Observe, want 2", b.Fingerprints[fp.ID].Count)
	}
}
