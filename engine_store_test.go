package trustvian_test

import (
	"context"
	"time"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store"
)

// countingStore wraps an in-memory Store and counts calls, so tests can
// assert an Engine actually used the Store it was configured with rather
// than silently falling back to its own default.
type countingStore struct {
	inner    store.Store
	gets     int
	observes int
}

func (c *countingStore) Get(ctx context.Context, key baseline.Key) (baseline.Baseline, bool) {
	c.gets++
	if c.inner == nil {
		c.inner = store.NewInMemory()
	}
	return c.inner.Get(ctx, key)
}

func (c *countingStore) Observe(ctx context.Context, key baseline.Key, fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (baseline.Baseline, bool, error) {
	c.observes++
	if c.inner == nil {
		c.inner = store.NewInMemory()
	}
	return c.inner.Observe(ctx, key, fp, vol, now)
}

var _ store.Store = (*countingStore)(nil)
