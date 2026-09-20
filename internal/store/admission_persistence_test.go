package store_test

// Task 046: the fingerprint admission bound seen through persistence.
//
// The bound lives in internal/baseline, so a store cannot enforce or
// violate it directly. What a store can do is lose or corrupt a baseline
// that sits at or beyond the bound, which is the upgrade risk worth
// covering: a v0.9 deployment can have persisted baselines larger than
// v1.0 will ever admit, and those must survive a round trip intact.
//
// PostgreSQL's equivalent is TestLargeBoundedBaselineRoundTrips in
// internal/store/postgres, which drives a maximal baseline through the
// real code path and is opt-in behind TRUSTVIAN_TEST_POSTGRES_DSN.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store"
)

// admissionFingerprint mirrors internal/baseline's test helper: a family
// of distinct fingerprints standing in for caller-controlled operation
// names.
func admissionFingerprint(i int) fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     fmt.Sprintf("GET /resource/%d", i),
		TargetName:        "api",
		Environment:       "production",
	})
}

// TestInMemoryStoreStaysBoundedUnderFingerprintFlood: the in-memory store
// inherits the bound through Observe, so a flood against one key grows to
// the cap and stops. Row/key growth across *distinct actors* is a
// different dimension and deliberately still unbounded — see
// docs/observability.md.
func TestInMemoryStoreStaysBoundedUnderFingerprintFlood(t *testing.T) {
	ctx := context.Background()
	s := store.NewInMemory()
	now := time.Now()

	for i := range 2000 {
		if _, _, err := s.Observe(ctx, testKey, admissionFingerprint(i), features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Observe() at i=%d: %v", i, err)
		}
	}

	b, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatal("Get() ok = false after observations")
	}
	if got := len(b.Fingerprints); got != 512 {
		t.Fatalf("len(Fingerprints) = %d, want 512", got)
	}
}

// TestFileStoreRoundTripsBoundedBaseline: a baseline at the admission
// bound serializes and loads back whole. This is the shape a long-lived
// production actor eventually reaches, so truncation here would silently
// discard learned detection state.
func TestFileStoreRoundTripsBoundedBaseline(t *testing.T) {
	ctx := context.Background()
	s, path := newFileStore(t)
	now := time.Now()

	for i := range 600 { // past the bound, so the stored baseline sits at it
		if _, _, err := s.Observe(ctx, testKey, admissionFingerprint(i), features.VolatileFeatures{}, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Observe() at i=%d: %v", i, err)
		}
	}

	reopened, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() reopening: %v", err)
	}
	b, ok := reopened.Get(ctx, testKey)
	if !ok {
		t.Fatal("Get() ok = false after reopening the file store")
	}
	if got := len(b.Fingerprints); got != 512 {
		t.Fatalf("len(Fingerprints) = %d after reload, want 512", got)
	}
}

// TestFileStoreRoundTripsOversizedLegacyBaseline is the upgrade case.
// A baseline persisted before the bound existed can hold more than 512
// identities; loading it must return every one of them. Truncating on
// load would delete learned state an operator never agreed to lose, and
// would do it silently during an upgrade.
//
// The fixture is written as a snapshot file rather than produced through
// Observe, because the bounded implementation can no longer create one —
// which is exactly the situation an upgrading deployment is in.
func TestFileStoreRoundTripsOversizedLegacyBaseline(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	now := time.Now().UTC().Truncate(time.Second)

	const legacySize = 800
	fps := make(map[string]baseline.FingerprintStats, legacySize)
	for i := range legacySize {
		fps[admissionFingerprint(i).ID] = baseline.FingerprintStats{
			Count:         3,
			FirstObserved: now.Add(-time.Hour),
			LastObserved:  now.Add(-time.Minute),
		}
	}
	legacy := baseline.New(testKey)
	legacy.Fingerprints = fps
	legacy.LastObserved = now.Add(-time.Minute)

	snapshot := struct {
		Version   int                 `json:"version"`
		Baselines []baseline.Baseline `json:"baselines"`
	}{Version: 1, Baselines: []baseline.Baseline{legacy}}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal legacy snapshot: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write legacy snapshot: %v", err)
	}

	s, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() on a legacy oversized snapshot: %v", err)
	}
	b, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatal("Get() ok = false for a baseline present in the snapshot")
	}
	if got := len(b.Fingerprints); got != legacySize {
		t.Fatalf("len(Fingerprints) = %d after load, want %d — legacy state must not be truncated", got, legacySize)
	}

	// Known fingerprints keep learning; new ones are refused; cardinality
	// neither shrinks nor grows.
	known := admissionFingerprint(0)
	if _, _, err := s.Observe(ctx, testKey, known, features.VolatileFeatures{}, now); err != nil {
		t.Fatalf("Observe() known fingerprint: %v", err)
	}
	if _, _, err := s.Observe(ctx, testKey, admissionFingerprint(legacySize+1), features.VolatileFeatures{}, now.Add(time.Second)); err != nil {
		t.Fatalf("Observe() new fingerprint past the bound: %v", err)
	}

	b, _ = s.Get(ctx, testKey)
	if got := b.Fingerprints[known.ID].Count; got != 4 {
		t.Fatalf("known fingerprint Count = %d, want 4 — an oversized baseline must keep learning what it knows", got)
	}
	if got := len(b.Fingerprints); got != legacySize {
		t.Fatalf("len(Fingerprints) = %d, want %d — oversized state must neither shrink nor grow", got, legacySize)
	}
}
