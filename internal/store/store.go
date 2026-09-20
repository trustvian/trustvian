// Package store defines the narrow persistence port Baseline data flows
// through, and an in-memory implementation of it. It intentionally exposes
// only the two operations the pipeline actually needs — read the current
// snapshot for a Key, apply one observation — rather than a generic
// repository interface.
package store

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// Store is the port the Engine reads and updates Baseline data through.
// Implementations must be safe for concurrent use by multiple goroutines.
type Store interface {
	// Get returns the current Baseline for key. If no observation has
	// ever been recorded for key, it returns an empty baseline.New(key)
	// and false.
	Get(ctx context.Context, key baseline.Key) (baseline.Baseline, bool)

	// Observe applies one observation of fp and vol to the Baseline for
	// key at time now, and returns the resulting Baseline.
	//
	// learned reports whether the observation actually reached fp's
	// learned statistics. It is false when Baseline.Observe's admission
	// control refused an unknown fingerprint at capacity, and false when
	// an implementation declined the write for its own reasons (a frozen
	// key — see Freezer). An implementation must never report true for a
	// write that did not happen: Engine.Observe passes this straight to
	// its caller, and a caller measuring how much a baseline learned
	// gets a wrong answer rather than a missing one.
	//
	// A returned error always accompanies learned == false.
	Observe(ctx context.Context, key baseline.Key, fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (bl baseline.Baseline, learned bool, err error)
}

// Freezer is an optional capability a Store implementation may provide:
// suspending learning for one Key without discarding its history —
// useful, for example, so an analyst can inspect a Baseline during an
// active investigation without it continuing to drift while they work.
//
// This is deliberately not part of Store itself: Engine and every
// pipeline package have no need to know freezing exists, and adding it
// to Store would ripple into every implementation and every consumer
// for a capability most callers never use. A caller that wants it
// type-asserts a Store to Freezer.
//
// Freeze state is scoped strictly to one Key at a time — there is no
// "freeze everything" operation, and freezing is intentionally not
// itself persisted (see FileStore): it's a live, current-process
// operational flag, not part of the learned behavioral history.
type Freezer interface {
	// Freeze suspends learning for key: subsequent Observe calls become
	// no-ops (the Baseline is returned unchanged) until Unfreeze. Get is
	// unaffected — full history remains readable while frozen.
	Freeze(ctx context.Context, key baseline.Key)
	// Unfreeze resumes learning for key.
	Unfreeze(ctx context.Context, key baseline.Key)
	// IsFrozen reports whether key is currently frozen.
	IsFrozen(ctx context.Context, key baseline.Key) bool
}

// shard holds the current Baseline for exactly one Key, behind its own
// lock. Scoping the lock to a single Key rather than a fixed hash bucket
// means concurrent Observe/Get calls for different actors never contend
// with each other — contention only exists between calls for the same
// actor. Key includes the learning scope, so two scopes over one actor
// are two shards and do not contend either.
type shard struct {
	mu       sync.RWMutex
	baseline baseline.Baseline
	frozen   bool
}

// InMemory is a Store backed by an in-memory map, safe for concurrent use.
// It is the MVP's only Baseline persistence: baselines do not survive
// process restarts. The Store port exists so a persistent implementation
// can be added later without changing any caller.
type InMemory struct {
	mu     sync.RWMutex
	shards map[baseline.Key]*shard
}

// NewInMemory returns an empty InMemory store.
func NewInMemory() *InMemory {
	return &InMemory{shards: make(map[baseline.Key]*shard)}
}

func (s *InMemory) Get(ctx context.Context, key baseline.Key) (baseline.Baseline, bool) {
	sh, ok := s.lookup(key)
	if !ok {
		return baseline.New(key), false
	}
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.baseline, true
}

func (s *InMemory) Observe(ctx context.Context, key baseline.Key, fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (baseline.Baseline, bool, error) {
	sh := s.getOrCreate(key)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.frozen {
		// Frozen is a deliberate no-op, and reporting it as learning
		// would make an analyst's own inspection freeze look like
		// continued drift.
		return sh.baseline, false, nil
	}
	updated, learned := sh.baseline.Observe(fp, vol, now)
	sh.baseline = updated
	return sh.baseline, learned, nil
}

// Freeze implements Freezer.
func (s *InMemory) Freeze(ctx context.Context, key baseline.Key) {
	sh := s.getOrCreate(key)
	sh.mu.Lock()
	sh.frozen = true
	sh.mu.Unlock()
}

// Unfreeze implements Freezer.
func (s *InMemory) Unfreeze(ctx context.Context, key baseline.Key) {
	sh := s.getOrCreate(key)
	sh.mu.Lock()
	sh.frozen = false
	sh.mu.Unlock()
}

// IsFrozen implements Freezer.
func (s *InMemory) IsFrozen(ctx context.Context, key baseline.Key) bool {
	sh, ok := s.lookup(key)
	if !ok {
		return false
	}
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return sh.frozen
}

// snapshot returns a consistent point-in-time copy of every Baseline
// currently held, keyed by Key. Used by FileStore to serialize the
// store to disk; not part of Store or Freezer since no pipeline
// consumer needs to enumerate every key at once.
func (s *InMemory) snapshot() map[baseline.Key]baseline.Baseline {
	s.mu.RLock()
	shards := make(map[baseline.Key]*shard, len(s.shards))
	maps.Copy(shards, s.shards)
	s.mu.RUnlock()

	out := make(map[baseline.Key]baseline.Baseline, len(shards))
	for key, sh := range shards {
		sh.mu.RLock()
		out[key] = sh.baseline
		sh.mu.RUnlock()
	}
	return out
}

// restore seeds an empty InMemory from a previously captured snapshot
// (see snapshot). Intended for use only at construction time, before
// concurrent access begins; it still locks correctly if called later.
func (s *InMemory) restore(data map[baseline.Key]baseline.Baseline) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, bl := range data {
		s.shards[key] = &shard{baseline: bl}
	}
}

func (s *InMemory) lookup(key baseline.Key) (*shard, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sh, ok := s.shards[key]
	return sh, ok
}

func (s *InMemory) getOrCreate(key baseline.Key) *shard {
	if sh, ok := s.lookup(key); ok {
		return sh
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if sh, ok := s.shards[key]; ok {
		return sh
	}
	sh := &shard{baseline: baseline.New(key)}
	s.shards[key] = sh
	return sh
}

var (
	_ Store   = (*InMemory)(nil)
	_ Freezer = (*InMemory)(nil)
)
