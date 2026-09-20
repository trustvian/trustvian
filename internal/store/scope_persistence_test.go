package store_test

// Task 051's persistence half: learning scopes must survive a restart,
// pre-scope snapshots must still load, and a scoped snapshot must never be
// silently misread by a reader that predates scopes.
//
// That last one is the reason the snapshot version moved at all. Adding a
// JSON field is normally additive — an unknown field is ignored — but Scope
// changes what a record *identifies*. A version-1 reader handed a file
// holding (scope A, actor X, prod) and (scope B, actor X, prod) sees two
// baselines with the same key and keeps whichever it loads last, merging two
// histories that exist precisely to be separate. These tests pin both halves
// of the resulting contract: old files load, new files are refused by old
// readers.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/store"
)

var (
	// Same actor and environment as testKey (store_test.go); only the
	// scope differs, which is the whole point.
	scopedA = baseline.Key{Scope: "scope-a", ActorID: testKey.ActorID, Environment: testKey.Environment}
	scopedB = baseline.Key{Scope: "scope-b", ActorID: testKey.ActorID, Environment: testKey.Environment}

	// scopeTestTime is fixed so no assertion here depends on wall-clock
	// timing. It matches the legacy fixture's own timestamps.
	scopeTestTime = time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
)

// TestFileStorePersistsScopesIndependently: same actor, same environment,
// different scopes, one file, across a restart.
func TestFileStorePersistsScopesIndependently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	ctx := context.Background()
	fp := testFingerprint()

	first, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	for i := range 4 {
		if _, _, err := first.Observe(ctx, scopedA, fp, features.VolatileFeatures{}, scopeTestTime.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("Observe(scope-a) error = %v", err)
		}
	}
	if _, _, err := first.Observe(ctx, scopedB, fp, features.VolatileFeatures{}, scopeTestTime); err != nil {
		t.Fatalf("Observe(scope-b) error = %v", err)
	}
	// And the default scope, so all three coexist in one file.
	if _, _, err := first.Observe(ctx, testKey, fp, features.VolatileFeatures{}, scopeTestTime); err != nil {
		t.Fatalf("Observe(default) error = %v", err)
	}

	reopened, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}

	for _, tc := range []struct {
		key       baseline.Key
		wantCount uint64
	}{
		{scopedA, 4},
		{scopedB, 1},
		{testKey, 1},
	} {
		bl, ok := reopened.Get(ctx, tc.key)
		if !ok {
			t.Fatalf("Get(%+v) ok = false after restart", tc.key)
		}
		if got := bl.Fingerprints[fp.ID].Count; got != tc.wantCount {
			t.Errorf("Get(%+v) Count = %d, want %d — scopes collided on disk", tc.key, got, tc.wantCount)
		}
		if bl.Key != tc.key {
			t.Errorf("restored Baseline.Key = %+v, want %+v", bl.Key, tc.key)
		}
	}
}

// TestFileStoreWritesScopedSnapshotVersion checks the on-disk marker
// directly. The behavioral tests above would pass with any version number;
// what an older reader does depends on this byte.
func TestFileStoreWritesScopedSnapshotVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	s, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() error = %v", err)
	}
	if _, _, err := s.Observe(context.Background(), testKey, testFingerprint(), features.VolatileFeatures{}, scopeTestTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	var snap struct {
		Version   int               `json:"version"`
		Baselines []json.RawMessage `json:"baselines"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("parse snapshot: %v", err)
	}
	if snap.Version != 2 {
		t.Errorf("snapshot version = %d, want 2 — a version-1 reader would silently merge scopes", snap.Version)
	}
	if !strings.Contains(string(raw), `"Scope"`) {
		t.Errorf("snapshot carries no Scope field:\n%s", raw)
	}
}

// legacySnapshot is a hand-written version-1 file: the exact shape a
// pre-task-051 binary wrote, with no Scope anywhere. Hand-written on
// purpose — generating it with the current code would only prove the
// current code round-trips itself.
const legacySnapshot = `{
  "version": 1,
  "baselines": [
    {
      "Key": {"ActorID": "svc-payment", "Environment": "production"},
      "Fingerprints": {
        "legacy-fp-1": {
          "Count": 7,
          "FirstObserved": "2026-01-01T00:00:00Z",
          "LastObserved": "2026-01-01T02:00:00Z"
        }
      },
      "LastObserved": "2026-01-01T02:00:00Z",
      "LastFingerprintID": "legacy-fp-1",
      "LastFingerprintTime": "2026-01-01T02:00:00Z",
      "PreviousFingerprintID": "",
      "DelegatorCounts": {"svc-orchestrator": 3}
    }
  ]
}`

// TestFileStoreLoadsLegacySnapshotIntoDefaultScope is the upgrade path.
// Every pre-scope baseline is, semantically, already in the default scope —
// so loading must land it there with its learned state byte-for-byte intact
// and must invent nothing.
func TestFileStoreLoadsLegacySnapshotIntoDefaultScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(path, []byte(legacySnapshot), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := store.NewFileStore(path)
	if err != nil {
		t.Fatalf("NewFileStore() on a version-1 snapshot error = %v — the upgrade path is broken", err)
	}
	ctx := context.Background()

	bl, ok := s.Get(ctx, testKey)
	if !ok {
		t.Fatal("Get(default scope) ok = false: the legacy baseline did not land in the default scope")
	}
	if bl.Key.Scope != "" {
		t.Errorf("restored Key.Scope = %q, want empty — a scope was invented during the upgrade", bl.Key.Scope)
	}
	if got := bl.Fingerprints["legacy-fp-1"].Count; got != 7 {
		t.Errorf("Count = %d, want 7 — learned state was lost in the upgrade", got)
	}
	if got := bl.DelegatorCounts["svc-orchestrator"]; got != 3 {
		t.Errorf("DelegatorCounts = %d, want 3", got)
	}
	if bl.LastFingerprintID != "legacy-fp-1" {
		t.Errorf("LastFingerprintID = %q, want legacy-fp-1 — sequence state was lost", bl.LastFingerprintID)
	}

	// It is not found under any other scope, which is the other half of
	// "landed in the default scope" rather than "landed everywhere".
	if _, ok := s.Get(ctx, scopedA); ok {
		t.Error("the legacy baseline is visible under a non-default scope")
	}

	// And it keeps learning normally after the upgrade, rather than being
	// read-only or re-keyed on the next write.
	if _, learned, err := s.Observe(ctx, testKey, testFingerprint(), features.VolatileFeatures{}, scopeTestTime.Add(time.Hour)); err != nil || !learned {
		t.Fatalf("Observe() after upgrade: learned = %v, err = %v", learned, err)
	}
	if bl, _ := s.Get(ctx, testKey); bl.Fingerprints["legacy-fp-1"].Count != 7 {
		t.Error("the upgraded baseline lost its pre-existing fingerprint on the next write")
	}
}

// TestFileStoreRefusesUnknownSnapshotVersion is the fail-closed half. A
// version this build does not recognize is refused, not guessed at — the
// same stance that protects a version-1 binary from a version-2 file.
func TestFileStoreRefusesUnknownSnapshotVersion(t *testing.T) {
	for _, version := range []string{"0", "3", "99"} {
		t.Run("version "+version, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "baseline.json")
			body := strings.Replace(legacySnapshot, `"version": 1`, `"version": `+version, 1)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			s, err := store.NewFileStore(path)
			if err == nil {
				t.Fatalf("NewFileStore() accepted snapshot version %s; store = %v", version, s)
			}
			if !strings.Contains(err.Error(), "unsupported snapshot version") {
				t.Errorf("error = %v, want one naming the unsupported version", err)
			}
		})
	}
}

// TestFreezeIsScopedToOneKey: Freezer is keyed by baseline.Key, so it became
// scope-aware for free when Key gained Scope. "For free" is exactly the kind
// of claim worth a test — freezing one profile must not stop another from
// learning.
func TestFreezeIsScopedToOneKey(t *testing.T) {
	for name, newStore := range map[string]func(*testing.T) store.Store{
		"InMemory": func(t *testing.T) store.Store { return store.NewInMemory() },
		"FileStore": func(t *testing.T) store.Store {
			s, err := store.NewFileStore(filepath.Join(t.TempDir(), "baseline.json"))
			if err != nil {
				t.Fatalf("NewFileStore() error = %v", err)
			}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newStore(t)
			freezer, ok := s.(store.Freezer)
			if !ok {
				t.Fatalf("%s does not implement Freezer", name)
			}
			ctx := context.Background()
			fp := testFingerprint()

			freezer.Freeze(ctx, scopedA)

			if freezer.IsFrozen(ctx, scopedB) {
				t.Error("freezing scope-a also froze scope-b")
			}
			if !freezer.IsFrozen(ctx, scopedA) {
				t.Error("scope-a is not frozen after Freeze")
			}

			_, learned, err := s.Observe(ctx, scopedA, fp, features.VolatileFeatures{}, scopeTestTime)
			if err != nil {
				t.Fatalf("Observe(frozen) error = %v", err)
			}
			if learned {
				t.Error("Observe on a frozen key reported learned = true: a no-op must not claim learning")
			}

			_, learned, err = s.Observe(ctx, scopedB, fp, features.VolatileFeatures{}, scopeTestTime)
			if err != nil {
				t.Fatalf("Observe(scope-b) error = %v", err)
			}
			if !learned {
				t.Error("scope-b could not learn while scope-a was frozen")
			}
			if bl, _ := s.Get(ctx, scopedA); len(bl.Fingerprints) != 0 {
				t.Error("the frozen scope learned anyway")
			}
		})
	}
}
