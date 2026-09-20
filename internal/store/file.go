package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// fileSnapshotVersion identifies the on-disk format this build writes.
// Bump it when the shape of fileSnapshot (or any type it embeds)
// changes in a way a previous version's loader could misread, so an
// older binary detects the difference deliberately instead of silently
// misinterpreting it.
//
// Version 2 added baseline.Key.Scope. That is the only change, and it
// would normally be additive — an unknown JSON field is ignored — which
// is exactly why the bump matters. Scope changes what a record
// *identifies*: one version-2 file can hold (scope A, actor X, prod)
// and (scope B, actor X, prod), which a version-1 reader sees as two
// baselines with the same key and resolves by keeping whichever it
// loaded last. Silently merging two histories that exist precisely to
// be separate is not a compatible read, so version 1 refuses the file
// instead.
//
// Downgrade compatibility is the deliberate cost. A file an old binary
// cannot open is a recoverable operational problem; two learned
// profiles collapsed into one is corrupted state nothing detects. See
// docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md.
const fileSnapshotVersion = 2

// fileSnapshotVersionLegacy is the pre-scope format. It is read, never
// written: every baseline in such a file predates learning scopes, so
// its Key has no Scope field and deserializes to the default scope ("")
// — which is precisely what those baselines already are. The upgrade
// invents nothing and moves no learned state between scopes.
const fileSnapshotVersionLegacy = 1

// fileSnapshot is FileStore's on-disk representation: every Baseline,
// each of which already carries its own Key, so no separate keying
// wrapper is needed.
type fileSnapshot struct {
	Version   int                 `json:"version"`
	Baselines []baseline.Baseline `json:"baselines"`
}

// FileStore is a Store backed by a single JSON file on disk. It exists
// so a Baseline survives a process restart — the MVP's only persistent
// implementation, deliberately simple: no database, no background
// goroutine, no partial/incremental writes.
//
// Durability guarantee: every successful Observe call is fully flushed
// to disk (via a temp-file-plus-atomic-rename write, so a crash mid-write
// never leaves a corrupt file) before Observe returns. If Observe
// returns a non-nil error, the in-memory update still happened — Get
// reflects it immediately — but it may not have reached disk; the next
// successful Observe (to any key) will flush the current full state,
// including that update, so a transient disk error does not permanently
// lose data as long as the process keeps running and a later flush
// succeeds. Data is only lost if the process exits (or crashes) between
// a successful in-memory Observe and the next successful flush.
//
// FileStore has no background flush timer and spawns no goroutines —
// consistent with the rest of this module (see PERFORMANCE.md's
// concurrency notes). This means every Observe call pays the cost of
// serializing and writing the store's *entire* current contents, not
// just the one updated entry; see BenchmarkFileStoreObserve in
// file_bench_test.go for the measured cost of that tradeoff, and
// docs/adr/0006 for why it was chosen anyway for the MVP.
//
// Freeze state (see Freezer) is intentionally not persisted — it is a
// live, current-process operational flag, not part of the learned
// behavioral history, so it resets to unfrozen on every restart.
type FileStore struct {
	inner *InMemory
	path  string

	// flushMu serializes the snapshot-and-write sequence across
	// concurrent Observe calls. It does not guard the in-memory update
	// itself (inner already handles that with its own per-key sharded
	// locks) — only disk writes are serialized, so concurrent Observe
	// calls for different keys still update memory fully concurrently;
	// they only queue behind each other for the (comparatively rare,
	// gated) disk flush.
	flushMu sync.Mutex
}

// NewFileStore opens (or creates) a FileStore backed by the file at
// path. If the file already exists, its contents are loaded
// immediately, synchronously, before NewFileStore returns.
func NewFileStore(path string) (*FileStore, error) {
	data, err := loadFileSnapshot(path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	inner := NewInMemory()
	inner.restore(data)

	return &FileStore{inner: inner, path: path}, nil
}

func (s *FileStore) Get(ctx context.Context, key baseline.Key) (baseline.Baseline, bool) {
	return s.inner.Get(ctx, key)
}

func (s *FileStore) Observe(ctx context.Context, key baseline.Key, fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (baseline.Baseline, bool, error) {
	bl, learned, err := s.inner.Observe(ctx, key, fp, vol, now)
	if err != nil {
		return bl, false, err
	}
	if err := s.flush(); err != nil {
		return bl, false, fmt.Errorf("store: flush %s: %w", s.path, err)
	}
	return bl, learned, nil
}

// Freeze implements Freezer. Not persisted — see FileStore's doc comment.
func (s *FileStore) Freeze(ctx context.Context, key baseline.Key) {
	s.inner.Freeze(ctx, key)
}

// Unfreeze implements Freezer.
func (s *FileStore) Unfreeze(ctx context.Context, key baseline.Key) {
	s.inner.Unfreeze(ctx, key)
}

// IsFrozen implements Freezer.
func (s *FileStore) IsFrozen(ctx context.Context, key baseline.Key) bool {
	return s.inner.IsFrozen(ctx, key)
}

// Flush writes the store's current full contents to disk immediately.
// Observe already does this after every call; Flush exists for callers
// that want an explicit, synchronous durability point (e.g. before
// process shutdown) without waiting on the next Observe.
func (s *FileStore) Flush() error {
	return s.flush()
}

func (s *FileStore) flush() error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	return writeFileSnapshotAtomic(s.path, s.inner.snapshot())
}

func loadFileSnapshot(path string) (map[baseline.Key]baseline.Baseline, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[baseline.Key]baseline.Baseline{}, nil
	}
	if err != nil {
		return nil, err
	}

	var snap fileSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	switch snap.Version {
	case fileSnapshotVersion, fileSnapshotVersionLegacy:
		// Both readable. A legacy file needs no conversion step: the
		// absent Scope field has already deserialized to the default
		// scope by the time we get here.
	default:
		return nil, fmt.Errorf("unsupported snapshot version %d (want %d or %d)", snap.Version, fileSnapshotVersionLegacy, fileSnapshotVersion)
	}

	out := make(map[baseline.Key]baseline.Baseline, len(snap.Baselines))
	for _, bl := range snap.Baselines {
		out[bl.Key] = bl
	}
	return out, nil
}

// writeFileSnapshotAtomic serializes data and writes it to path via a
// temp file plus rename, so a concurrent reader (a new FileStore
// opening the same path) or a crash mid-write never observes a
// partially-written file — os.Rename is atomic on the same filesystem.
func writeFileSnapshotAtomic(path string, data map[baseline.Key]baseline.Baseline) error {
	snap := fileSnapshot{
		Version:   fileSnapshotVersion,
		Baselines: make([]baseline.Baseline, 0, len(data)),
	}
	for _, bl := range data {
		snap.Baselines = append(snap.Baselines, bl)
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".trustvian-store-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	return nil
}

var (
	_ Store   = (*FileStore)(nil)
	_ Freezer = (*FileStore)(nil)
)
