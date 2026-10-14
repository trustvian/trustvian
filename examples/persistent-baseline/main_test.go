// This file is task 034's mandatory external-consumer proof (see
// docs/archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md):
// examples/ is a genuinely separate Go module (examples/go.mod, with
// only a `replace` directive back to this repository), so a passing
// `go test ./...` here demonstrates empirically that an external OSS
// consumer can select and use durable persistence through public API
// alone. It imports only event, config, and the root trustvian
// package — never internal/*, which Go's own internal/ import
// restriction would refuse to compile regardless of intent.
//
// That restriction is exactly why this proof is necessary rather than
// ceremonial: store.Store's methods reference internal types, so before
// task 034 there was no expression an external module could write to
// obtain one. A test that compiles at all is already most of the claim.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
)

// TestPublicConfigSelectsDurableStoreAcrossRestart is the end-to-end
// claim: public config → CompileStorage → WithStore → Engine, learn,
// discard the Engine, rebuild from the same file, and find the learned
// state still there.
func TestPublicConfigSelectsDurableStoreAcrossRestart(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "baseline.json")
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	first, err := newEngineOn(statePath)
	if err != nil {
		t.Fatalf("newEngineOn: %v", err)
	}

	const observations = 12
	for i := range observations {
		result, err := first.Analyze(ctx, paymentEvent("warm-up", tick()))
		if err != nil {
			t.Fatalf("Analyze %d: %v", i, err)
		}
		if _, err := first.Observe(ctx, result); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}

	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file not written: %v", err)
	}

	// A completely fresh Engine over the same file — nothing shared in
	// memory with the first.
	second, err := newEngineOn(statePath)
	if err != nil {
		t.Fatalf("newEngineOn (restart): %v", err)
	}

	result, err := second.Analyze(ctx, paymentEvent("after-restart", tick()))
	if err != nil {
		t.Fatalf("Analyze after restart: %v", err)
	}

	if result.Anomaly.Confidence == 0 {
		t.Fatalf("Anomaly.Confidence = 0 after restart — the persisted baseline was not loaded")
	}
	if got := result.Fingerprint.ID; got == "" {
		t.Fatalf("Fingerprint.ID is empty")
	}
}

// TestPublicConfigMemoryStoreDoesNotPersist is the control for the test
// above: the identical code path with `type: memory` must *not* carry
// state across engines. Without this, a passing restart test could be
// explained by something other than the file store actually working.
func TestPublicConfigMemoryStoreDoesNotPersist(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	newMemoryEngine := func() *trustvian.Engine {
		t.Helper()
		s, err := config.CompileStorage(config.StorageConfig{
			Version: config.StorageSchemaVersionV1,
			Type:    config.StorageTypeMemory,
		})
		if err != nil {
			t.Fatalf("CompileStorage(memory): %v", err)
		}
		return trustvian.NewEngine(trustvian.WithStore(s))
	}

	first := newMemoryEngine()
	for range 12 {
		result, err := first.Analyze(ctx, paymentEvent("warm-up", tick()))
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if _, err := first.Observe(ctx, result); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}

	second := newMemoryEngine()
	result, err := second.Analyze(ctx, paymentEvent("after-restart", tick()))
	if err != nil {
		t.Fatalf("Analyze after restart: %v", err)
	}
	if result.Anomaly.Confidence != 0 {
		t.Errorf("Anomaly.Confidence = %v for a fresh in-memory store, want 0 — memory storage must not persist", result.Anomaly.Confidence)
	}
}

// TestPublicConfigPostgresFailsClosedWhenUnreachable proves the
// fail-closed guarantee is visible to external consumers too, not just
// enforced internally: a configured-but-unreachable database yields an
// error and no Store, never a silently substituted non-durable one. An
// external caller who asked for shared, durable state and got an
// in-memory store instead would have no way to notice until the state
// was already gone.
//
// Port 1 on loopback is never a PostgreSQL server, so this needs no
// database and runs on any machine.
func TestPublicConfigPostgresFailsClosedWhenUnreachable(t *testing.T) {
	s, err := config.CompileStorage(config.StorageConfig{
		Version: config.StorageSchemaVersionV1,
		Type:    config.StorageTypePostgres,
		Postgres: &config.PostgresStorageConfig{
			DSN:                   "postgres://u:p@127.0.0.1:1/db?sslmode=disable",
			ConnectTimeoutSeconds: 2,
		},
	})

	if err == nil {
		t.Fatal("CompileStorage() = nil error, want an error for an unreachable database")
	}
	if s != nil {
		t.Errorf("CompileStorage() returned a non-nil Store for an unreachable backend — an external caller must never receive a silent substitute")
	}
	if strings.Contains(err.Error(), ":p@") {
		t.Errorf("CompileStorage() err leaked DSN credentials: %v", err)
	}
}

// TestPublicConfigPostgresMissingDSNFailsClosed pins the other half:
// `type: postgres` with no DSN is rejected before any connection is
// attempted, so a half-written config is a startup error rather than a
// silent downgrade.
func TestPublicConfigPostgresMissingDSNFailsClosed(t *testing.T) {
	s, err := config.CompileStorage(config.StorageConfig{
		Version: config.StorageSchemaVersionV1,
		Type:    config.StorageTypePostgres,
	})

	if !errors.Is(err, config.ErrMissingStorageDSN) {
		t.Errorf("CompileStorage() err = %v, want %v", err, config.ErrMissingStorageDSN)
	}
	if s != nil {
		t.Error("CompileStorage() returned a non-nil Store for a config with no DSN")
	}
}

// TestPublicConfigSelectsPostgresStoreAcrossRestart is task 035's
// external-consumer proof, and the reason this file matters more than the
// in-module tests: it is the same restart claim as
// TestPublicConfigSelectsDurableStoreAcrossRestart, made against a real
// PostgreSQL database, from a module that *cannot* import internal/store
// even by accident. If this compiles and passes, an OSS user can run
// Trustvian on production-grade shared storage using public API alone.
//
// Note what this proves beyond the file-store version: the second Engine
// is not merely reading a file this process wrote, it is reading state
// from a database that would be equally visible to a different process on
// a different host — which is the actual point of the PostgreSQL backend.
//
// DSN-gated so `go test ./...` still passes with no database available;
// see docs/storage-guide.md § Running the integration tests.
func TestPublicConfigSelectsPostgresStoreAcrossRestart(t *testing.T) {
	dsn := os.Getenv("TRUSTVIAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRUSTVIAN_TEST_POSTGRES_DSN not set; see docs/storage-guide.md")
	}

	ctx := context.Background()
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func() time.Time { clock = clock.Add(time.Minute); return clock }

	// A unique actor per run, so this test neither depends on an empty
	// database nor disturbs anything else sharing it. An external consumer
	// has no way to truncate Trustvian's tables through public API, which
	// is correct — and this is how a test lives with that.
	actor := fmt.Sprintf("external-e2e-%d-%d", time.Now().UnixNano(), os.Getpid())

	newPostgresEngine := func() (*trustvian.Engine, func()) {
		t.Helper()
		s, err := config.CompileStorage(config.StorageConfig{
			Version:  config.StorageSchemaVersionV1,
			Type:     config.StorageTypePostgres,
			Postgres: &config.PostgresStorageConfig{DSN: dsn},
		})
		if err != nil {
			t.Fatalf("CompileStorage(postgres): %v", err)
		}
		// The lifecycle pattern documented on config.CompileStorage: the
		// pool is released through an optional io.Closer assertion, because
		// store.Store itself has no Close method.
		cleanup := func() {}
		if c, ok := s.(io.Closer); ok {
			cleanup = func() { _ = c.Close() }
		}
		return trustvian.NewEngine(trustvian.WithStore(s)), cleanup
	}

	first, closeFirst := newPostgresEngine()
	const observations = 12
	for i := range observations {
		result, err := first.Analyze(ctx, actorEvent(actor, "warm-up", tick()))
		if err != nil {
			t.Fatalf("Analyze %d: %v", i, err)
		}
		if _, err := first.Observe(ctx, result); err != nil {
			t.Fatalf("Observe %d: %v", i, err)
		}
	}
	// Closed *before* the second engine is built, so the second cannot be
	// sharing a connection, a pool, or any in-process cache with the first.
	closeFirst()

	second, closeSecond := newPostgresEngine()
	defer closeSecond()

	result, err := second.Analyze(ctx, actorEvent(actor, "after-restart", tick()))
	if err != nil {
		t.Fatalf("Analyze after restart: %v", err)
	}
	if result.Anomaly.Confidence == 0 {
		t.Fatal("Anomaly.Confidence = 0 after restart — the baseline persisted in PostgreSQL was not loaded")
	}
}

// actorEvent is paymentEvent with the actor id overridden, so a test can
// key its baseline to a value unique to that run. Identical in every
// other field, which is what keeps the fingerprint — and therefore the
// maturity assertions — the same as the file-store tests'.
func actorEvent(actorID, id string, ts time.Time) event.Event {
	ev := paymentEvent(id, ts)
	ev.Actor.ID = actorID
	return ev
}
