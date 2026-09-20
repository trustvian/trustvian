package postgres_test

// Task 036's hardening tests: the production failure, lifecycle, and
// schema-integrity conditions task 035's own tests did not reach.
//
// Task 035 proved the happy path and basic concurrency (see
// postgres_test.go) plus the nine shared Store guarantees (see
// internal/store/contract_test.go). Nothing here duplicates those. What
// lives here is the set of conditions a production deployment actually
// meets and a green unit suite says nothing about: a transaction that
// fails partway, a row lock a caller gives up waiting for, an exhausted
// connection pool, a database whose schema metadata is unreadable, a row
// whose stored JSON is not a Baseline.
//
// Two rules shape every test in this file.
//
// **No sleeps as synchronization.** Waiting a fixed duration and hoping a
// lock was acquired produces a test that passes on a fast machine and
// fails in CI. Every ordering constraint here is enforced with a channel
// or by observing committed database state, never by elapsed time. Where
// a *deadline* is the thing under test, it is the subject of the
// assertion, not a guess about someone else's progress.
//
// **Failure injection stays out of production code.** There is no
// FailBeforeCommit option on postgres.Config and there is no test-only
// seam inside Store. Failures are injected through PostgreSQL itself —
// a trigger that raises, a lock held by a second connection, a pool
// sized to one, a row rewritten to invalid JSON — which has the added
// benefit of exercising the real driver error paths rather than a mock's
// idea of them.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
	"github.com/trustvian/trustvian/internal/store/postgres"
)

// hardeningKey is a distinct actor per test, so tests sharing a database
// never read each other's state. Schema isolation (isolatedSchemaDSN)
// already guarantees that; this makes failures easier to read.
func hardeningKey(name string) baseline.Key {
	return baseline.Key{ActorID: name, Environment: "production"}
}

// observationCount reads the Count for the shared test fingerprint,
// which is the number every no-lost-updates assertion here compares
// against.
func observationCount(t *testing.T, s *postgres.Store, key baseline.Key) uint64 {
	t.Helper()

	bl, ok := s.Get(context.Background(), key)
	if !ok {
		t.Fatalf("Get(%s) ok = false, want true", key.ActorID)
	}
	return bl.Fingerprints[testFingerprint().ID].Count
}

// ---------------------------------------------------------------------
// § Transaction integrity
// ---------------------------------------------------------------------

// TestObserveFailureLeavesPreviousBaselineIntact is the rollback proof
// (§12): an Observe that fails partway must not partially mutate stored
// state.
//
// The failure is injected with a PostgreSQL BEFORE UPDATE trigger that
// raises an exception. That placement is deliberate — it fires *after*
// Observe has inserted/locked the row and computed the new Baseline, so
// the transaction dies at the last possible moment, with a full set of
// intended changes pending. If rollback were incomplete, this is the
// scenario that would show it.
//
// A trigger rather than a Go-level seam because it needs no production
// code to know tests exist, and because it exercises the real driver
// error path.
func TestObserveFailureLeavesPreviousBaselineIntact(t *testing.T) {
	dsn := requireDSN(t)
	s, iso := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("rollback-actor")

	// Establish a known-good baseline first: rollback has to preserve
	// something, so there must be something to preserve.
	const committed = 5
	now := testTime
	for range committed {
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		now = now.Add(time.Second)
	}
	if got := observationCount(t, s, key); got != committed {
		t.Fatalf("Count = %d before injection, want %d", got, committed)
	}
	before, _ := s.Get(ctx, key)

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `
		CREATE FUNCTION fail_update() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'injected failure';
		END;
		$$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("create trigger function: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		CREATE TRIGGER fail_update_trigger
		BEFORE UPDATE ON `+postgres.BaselineTable+`
		FOR EACH ROW EXECUTE FUNCTION fail_update()`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err == nil {
		t.Fatal("Observe() error = nil with a failing UPDATE trigger, want an error")
	}

	// Remove the trigger so the verification read is unobstructed.
	if _, err := conn.Exec(ctx, `DROP TRIGGER fail_update_trigger ON `+postgres.BaselineTable); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}

	if got := observationCount(t, s, key); got != committed {
		t.Errorf("Count = %d after a failed Observe, want %d — the rolled-back transaction mutated stored state", got, committed)
	}
	after, ok := s.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false after a failed Observe — the baseline was destroyed, not preserved")
	}
	if after.LastFingerprintID != before.LastFingerprintID {
		t.Errorf("LastFingerprintID = %q after rollback, want %q", after.LastFingerprintID, before.LastFingerprintID)
	}
	if !after.LastObserved.Equal(before.LastObserved) {
		t.Errorf("LastObserved = %v after rollback, want %v", after.LastObserved, before.LastObserved)
	}

	// And the store is still usable afterwards: a failed transaction must
	// not poison the pool.
	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
		t.Errorf("Observe() error = %v after a recovered failure, want nil", err)
	}
	if got := observationCount(t, s, key); got != committed+1 {
		t.Errorf("Count = %d after resuming, want %d", got, committed+1)
	}
}

// ---------------------------------------------------------------------
// § Corrupt persisted state
// ---------------------------------------------------------------------

// TestCorruptStoredBaselineFailsExplicitlyAndIsNotReset covers §14's
// requirement that invalid persisted state produce an error rather than a
// silent reset — and pins the precise, asymmetric behavior of the two
// port methods, because they genuinely differ and the difference is
// load-bearing.
//
// Observe returns ErrCorruptState and — the part that matters most —
// does **not** overwrite the unreadable row with a fresh baseline. An
// implementation that "recovered" by starting over would silently erase
// an actor's entire learned history, which is strictly worse than
// refusing: the operator loses the evidence needed to diagnose the
// corruption and the actor silently becomes a blank slate.
//
// Get has no error return in the Store port, so it can only report
// "no baseline" (documented on Get itself). That is fail-safe in the
// direction that matters — the actor reads as unfamiliar, raising anomaly
// rather than suppressing it — and it does not write, so the corrupt row
// survives for Observe to surface on the very next learning call. This
// test asserts that combination explicitly rather than leaving it to be
// rediscovered.
func TestCorruptStoredBaselineFailsExplicitlyAndIsNotReset(t *testing.T) {
	dsn := requireDSN(t)
	s, iso := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("corrupt-actor")

	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	tests := []struct {
		name string
		// jsonb accepts any valid JSON, so "corrupt" here means
		// well-formed JSON that is not a Baseline — which is the realistic
		// corruption (a bad restore, a hand-edited row, a future layout),
		// not a byte-level mangling the column type would reject outright.
		payload string
	}{
		{"json array where an object belongs", `[1,2,3]`},
		{"json scalar", `"not-a-baseline"`},
		{"object with wrongly typed fields", `{"Fingerprints": "should-be-a-map"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx,
				`UPDATE `+postgres.BaselineTable+` SET baseline = $3 WHERE actor_id = $1 AND environment = $2`,
				key.ActorID, key.Environment, tt.payload,
			); err != nil {
				t.Fatalf("inject corrupt payload: %v", err)
			}

			_, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime)
			if !errors.Is(err, postgres.ErrCorruptState) {
				t.Errorf("Observe() err = %v, want ErrCorruptState", err)
			}
			// The error must not quote the payload wholesale: stored state
			// is not secret, but an error is a log line and a Baseline can
			// name internal identifiers. Naming the actor is enough to act.
			if err != nil && !strings.Contains(err.Error(), key.ActorID) {
				t.Errorf("Observe() err = %v, want it to name the affected actor", err)
			}

			// Get reports absence, and critically writes nothing.
			if _, ok := s.Get(ctx, key); ok {
				t.Error("Get() ok = true for a corrupt row, want false")
			}

			// The decisive assertion: the corrupt row is still corrupt.
			// Nothing reset it to a fresh baseline.
			var stored string
			if err := conn.QueryRow(ctx,
				`SELECT baseline::text FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
				key.ActorID, key.Environment,
			).Scan(&stored); err != nil {
				t.Fatalf("read back stored payload: %v", err)
			}
			if strings.Contains(stored, `"Fingerprints":{"`) {
				t.Errorf("stored payload = %s — the corrupt row was silently replaced with a fresh baseline", stored)
			}
		})
	}
}

// ---------------------------------------------------------------------
// § Cancellation and lock waiting
// ---------------------------------------------------------------------

// TestObserveSurfacesContextErrors is §19: cancellation and deadline
// expiry must be reportable with errors.Is, not flattened into an opaque
// database error. Observe wraps its failures (ErrUnavailable: begin: %w),
// so this also pins that the wrapping preserves the cause — a plain
// fmt.Errorf with %v instead of %w would break it silently.
func TestObserveSurfacesContextErrors(t *testing.T) {
	dsn := requireDSN(t)
	s, _ := newStore(t, dsn)
	fp := testFingerprint()

	tests := []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "already cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline already passed",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.ctx()
			defer cancel()

			_, _, err := s.Observe(ctx, hardeningKey("ctx-"+tt.name), fp, features.VolatileFeatures{}, testTime)
			if err == nil {
				t.Fatal("Observe() error = nil, want an error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Observe() err = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}

// TestObserveWaitingOnRowLockIsCancellable is §20, and the highest-value
// test in this file: a production Observe can find the row it needs
// already locked by another instance, and it must be possible to give up
// on that wait.
//
// Without cancellable lock waits, a request whose client has already gone
// away keeps a connection and a transaction pinned until the lock holder
// finishes. Under load that converts one slow holder into pool
// exhaustion for everyone.
//
// The ordering is enforced with channels and with PostgreSQL's own
// pg_locks view — never a sleep. "Tx B is waiting" is established by
// polling for a row in pg_locks with granted = false, which is the
// database telling us the state we need, rather than us assuming it.
func TestObserveWaitingOnRowLockIsCancellable(t *testing.T) {
	dsn := requireDSN(t)
	// A one-connection pool, so an abandoned lock wait that failed to
	// return its connection would make every later operation here fail
	// rather than being absorbed by spare capacity.
	s, iso := singleConnStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("lock-wait-actor")

	// Materialize the row so the holder has something to lock.
	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	countBefore := observationCount(t, s, key)

	// Tx A: an independent connection holding the row lock.
	holder, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer holder.Close(ctx)

	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("holder Begin() error = %v", err)
	}
	var locked []byte
	if err := holderTx.QueryRow(ctx,
		`SELECT baseline FROM `+postgres.BaselineTable+`
		 WHERE actor_id = $1 AND environment = $2 FOR UPDATE`,
		key.ActorID, key.Environment,
	).Scan(&locked); err != nil {
		t.Fatalf("holder lock error = %v", err)
	}

	// Tx B: the store's Observe, which must block on that lock and then
	// abandon the wait when its context expires.
	waiterCtx, cancelWaiter := context.WithCancel(ctx)
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		_, _, err := s.Observe(waiterCtx, key, fp, features.VolatileFeatures{}, testTime)
		done <- result{err: err, elapsed: time.Since(start)}
	}()

	// Confirm with the database that the waiter really is blocked on a
	// lock before cancelling it. Otherwise a cancel that happened to land
	// before the wait even started would make this test prove nothing.
	waitForBlockedLock(t, iso)

	cancelWaiter()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("Observe() error = nil after its context was cancelled mid-lock-wait, want an error")
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Errorf("Observe() err = %v, want errors.Is(err, context.Canceled)", got.err)
		}
		t.Logf("cancelled lock wait returned after %v", got.elapsed)
	case <-time.After(30 * time.Second):
		t.Fatal("Observe() did not return within 30s of cancellation — the lock wait is not cancellable")
	}

	// Tx A's view is untouched, and committing it still works: the
	// abandoned waiter left no lock, no half-applied update, and no
	// poisoned transaction behind.
	if err := holderTx.Commit(ctx); err != nil {
		t.Errorf("holder Commit() error = %v — the cancelled waiter disturbed the lock holder", err)
	}

	if got := observationCount(t, s, key); got != countBefore {
		t.Errorf("Count = %d after a cancelled Observe, want %d — the abandoned transaction still wrote", got, countBefore)
	}

	// The abandoned transaction returned its connection: on a pool of one,
	// this Observe is only possible if it did.
	assertStoreStillServes(t, s, key)
}

// waitForBlockedLock blocks until PostgreSQL reports at least one
// ungranted lock request, which is the database's own statement that some
// transaction is waiting. Polling a real state signal is what replaces a
// sleep here; the loop has a generous ceiling purely so a hung test fails
// with a clear message instead of the package timeout.
func waitForBlockedLock(t *testing.T, dsn string) {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var blocked int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM pg_locks WHERE NOT granted`,
		).Scan(&blocked); err != nil {
			t.Fatalf("query pg_locks: %v", err)
		}
		if blocked > 0 {
			return
		}
	}
	t.Fatal("no transaction ever blocked on a lock — the test's premise did not hold")
}

// ---------------------------------------------------------------------
// § Pool and connection lifecycle
// ---------------------------------------------------------------------

// assertStoreStillServes is this file's connection-leak detector, and it
// is deliberately functional rather than introspective.
//
// A leak could be asserted by reading pool statistics, but that would mean
// exporting pool internals from postgres.Store purely to satisfy a test —
// exactly the production-API pollution §46 of this task's brief rules out.
// The behavior a leak actually causes is what gets measured instead: on a
// pool of one connection, any operation that failed to return its
// connection makes the *next* operation impossible. So a short-deadline
// Observe that succeeds is proof the connection came back.
//
// This is a stronger test than a statistics read, not a weaker one: it
// fails for every mechanism that strands a connection, including ones a
// counter would miss (a transaction left open on a returned connection,
// for instance).
func assertStoreStillServes(t *testing.T, s *postgres.Store, key baseline.Key) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := s.Observe(ctx, key, testFingerprint(), features.VolatileFeatures{}, testTime); err != nil {
		t.Errorf("Observe() error = %v after earlier operations completed — a connection was not returned to the pool", err)
	}
}

// singleConnStore builds a store whose pool holds exactly one connection.
// Every leak test here uses one: with a pool of one, a single stranded
// connection is immediately fatal instead of being absorbed by spare
// capacity and going unnoticed.
func singleConnStore(t *testing.T, dsn string) (*postgres.Store, string) {
	t.Helper()

	iso := isolatedSchemaDSN(t, dsn)
	s, err := postgres.NewStore(context.Background(), postgres.Config{DSN: iso, MaxConnections: 1})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, iso
}

// TestConnectionsAreReleasedOnEveryPath is §22: the audit that every
// acquire has a matching release, asserted rather than eyeballed. It covers
// the success path and every failure shape that skips the normal Commit —
// a rolled-back transaction, a cancelled context, an unreadable row —
// because those are where a missing Rollback or an early return leaks.
//
// Each subtest runs its failing operation repeatedly. One leak on a
// one-connection pool is enough to fail, but repeating makes the failure
// unambiguous rather than a borderline timing result.
func TestConnectionsAreReleasedOnEveryPath(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	fp := testFingerprint()

	t.Run("successful Observe and Get", func(t *testing.T) {
		s, _ := singleConnStore(t, dsn)
		key := hardeningKey("release-ok")
		for range 10 {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			s.Get(ctx, key)
		}
		assertStoreStillServes(t, s, key)
	})

	t.Run("cancelled Observe", func(t *testing.T) {
		s, _ := singleConnStore(t, dsn)
		for range 10 {
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			_, _, _ = s.Observe(cctx, hardeningKey("release-cancelled"), fp, features.VolatileFeatures{}, testTime)
		}
		assertStoreStillServes(t, s, hardeningKey("release-cancelled"))
	})

	t.Run("cancelled Get", func(t *testing.T) {
		s, _ := singleConnStore(t, dsn)
		for range 10 {
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			s.Get(cctx, hardeningKey("release-get-cancelled"))
		}
		assertStoreStillServes(t, s, hardeningKey("release-get-cancelled"))
	})

	t.Run("Observe failing on corrupt state", func(t *testing.T) {
		s, iso := singleConnStore(t, dsn)
		key := hardeningKey("release-corrupt")
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		corruptRow(t, iso, key)

		for range 10 {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); !errors.Is(err, postgres.ErrCorruptState) {
				t.Fatalf("Observe() err = %v, want ErrCorruptState", err)
			}
			s.Get(ctx, key)
		}
		// A different key, since this one's row is deliberately unreadable.
		assertStoreStillServes(t, s, hardeningKey("release-corrupt-other"))
	})

	t.Run("Observe failing on a rolled-back transaction", func(t *testing.T) {
		s, iso := singleConnStore(t, dsn)
		key := hardeningKey("release-rollback")
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}

		conn, err := pgx.Connect(ctx, iso)
		if err != nil {
			t.Fatalf("pgx.Connect() error = %v", err)
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, `
			CREATE FUNCTION fail_update() RETURNS trigger AS $$
			BEGIN
				RAISE EXCEPTION 'injected failure';
			END;
			$$ LANGUAGE plpgsql`); err != nil {
			t.Fatalf("create trigger function: %v", err)
		}
		if _, err := conn.Exec(ctx, `
			CREATE TRIGGER fail_update_trigger
			BEFORE UPDATE ON `+postgres.BaselineTable+`
			FOR EACH ROW EXECUTE FUNCTION fail_update()`); err != nil {
			t.Fatalf("create trigger: %v", err)
		}

		for range 10 {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err == nil {
				t.Fatal("Observe() error = nil with a failing trigger, want an error")
			}
		}

		if _, err := conn.Exec(ctx, `DROP TRIGGER fail_update_trigger ON `+postgres.BaselineTable); err != nil {
			t.Fatalf("drop trigger: %v", err)
		}
		assertStoreStillServes(t, s, key)
	})
}

// corruptRow rewrites a baseline row's jsonb to valid JSON that is not a
// Baseline — the realistic corruption shape (a bad restore, a hand-edited
// row), since the jsonb column type rejects malformed JSON outright.
func corruptRow(t *testing.T, dsn string, key baseline.Key) {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx,
		`UPDATE `+postgres.BaselineTable+` SET baseline = '[]' WHERE actor_id = $1 AND environment = $2`,
		key.ActorID, key.Environment,
	); err != nil {
		t.Fatalf("inject corrupt payload: %v", err)
	}
}

// TestPoolExhaustionIsBoundedAndContextAware is §21. With the pool's only
// connection occupied, a further operation must wait for capacity and then
// give up when its context expires — a bounded, reportable failure rather
// than an unbounded hang.
//
// The occupation is arranged entirely through public API: one Observe is
// made to block on a row lock held by an independent connection, which
// pins the single pooled connection for as long as the lock is held. That
// is also exactly how exhaustion arises in production — a slow or stuck
// holder — so the reproduction is faithful rather than contrived.
//
// MaxConnections is 1 because that is the smallest reproduction of "all
// connections busy", not to make anything pass; the guarantee is identical
// at any pool size.
func TestPoolExhaustionIsBoundedAndContextAware(t *testing.T) {
	dsn := requireDSN(t)
	s, iso := singleConnStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("pool-actor")

	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	countBefore := observationCount(t, s, key)

	// An independent connection takes the row lock and holds it.
	holder, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer holder.Close(ctx)
	holderTx, err := holder.Begin(ctx)
	if err != nil {
		t.Fatalf("holder Begin() error = %v", err)
	}
	var locked []byte
	if err := holderTx.QueryRow(ctx,
		`SELECT baseline FROM `+postgres.BaselineTable+`
		 WHERE actor_id = $1 AND environment = $2 FOR UPDATE`,
		key.ActorID, key.Environment,
	).Scan(&locked); err != nil {
		t.Fatalf("holder lock error = %v", err)
	}

	// Occupy the pool: this Observe acquires the one connection and blocks
	// on the lock above. Its own context is long-lived, so it is still
	// holding that connection when the exhaustion assertions below run.
	occupierCtx, cancelOccupier := context.WithCancel(ctx)
	occupied := make(chan struct{})
	occupierDone := make(chan struct{})
	go func() {
		defer close(occupierDone)
		close(occupied)
		_, _, _ = s.Observe(occupierCtx, key, fp, features.VolatileFeatures{}, testTime)
	}()
	<-occupied
	waitForBlockedLock(t, iso) // the database confirms the connection is pinned

	// With no capacity left, a second operation must fail on its deadline.
	const deadline = 2 * time.Second

	// `start` is taken BEFORE the context, so the measured window fully
	// contains the context's own. Taking it after — the obvious order —
	// starts the deadline clock first and the stopwatch second, so `elapsed`
	// is legitimately a hair under `deadline` and the "gave up early" check
	// below fails on nothing: observed at 1.999082437s against a 2s
	// deadline, 918µs short, on a runner where that gap happened to be
	// visible.
	start := time.Now()
	waitCtx, cancelWait := context.WithTimeout(ctx, deadline)
	defer cancelWait()

	_, _, err = s.Observe(waitCtx, hardeningKey("pool-other-actor"), fp, features.VolatileFeatures{}, testTime)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("Observe() error = nil with an exhausted pool, want a context error")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Observe() err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if elapsed < deadline {
		t.Errorf("Observe() gave up after %v, before its %v deadline — it did not wait for capacity", elapsed, deadline)
	}
	if elapsed > deadline*5 {
		t.Errorf("Observe() took %v to give up on a %v deadline — the wait is not bounded by the context", elapsed, deadline)
	}
	t.Logf("exhausted-pool Observe gave up after %v (deadline %v)", elapsed, deadline)

	// Release everything and confirm the pool recovers rather than staying
	// wedged: the occupier is cancelled, the lock holder rolls back.
	cancelOccupier()
	<-occupierDone
	if err := holderTx.Rollback(ctx); err != nil {
		t.Errorf("holder Rollback() error = %v", err)
	}

	if got := observationCount(t, s, key); got != countBefore {
		t.Errorf("Count = %d after cancelled/blocked observations, want %d", got, countBefore)
	}
	assertStoreStillServes(t, s, key)
}

// TestCloseSemantics is §23: the lifecycle contract Store's own doc comment
// claims. Each clause is asserted because each is something a caller
// relies on — notably "safe to call more than once", which a deferred
// Close alongside an explicit one makes an ordinary occurrence.
func TestCloseSemantics(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("close-actor")

	iso := isolatedSchemaDSN(t, dsn)
	s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	for i := range 3 {
		if err := s.Close(); err != nil {
			t.Errorf("Close() #%d error = %v, want nil", i+1, err)
		}
	}

	// Operations after Close must fail rather than silently appearing to
	// work. A closed store that accepted writes would be the worst
	// outcome available: the caller would believe state was persisted.
	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err == nil {
		t.Error("Observe() error = nil after Close(), want an error")
	}
	if _, ok := s.Get(ctx, key); ok {
		t.Error("Get() ok = true after Close(), want false")
	}

	// Close released a pool, not the data: a new store on the same
	// database still sees the committed baseline.
	s2, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() after Close error = %v", err)
	}
	defer func() { _ = s2.Close() }()
	if got := observationCount(t, s2, key); got != 1 {
		t.Errorf("Count = %d in a new store after Close, want 1", got)
	}
}

// ---------------------------------------------------------------------
// § Connection loss during operation
// ---------------------------------------------------------------------

// TestConnectionLossDuringOperationFailsExplicitly is §17. Terminating the
// server side of pooled connections (pg_terminate_backend) is the closest
// faithful reproduction of a network partition, a failover, or a DBA
// cycling connections.
//
// The guarantee under test is deliberately not "the next call fails" or
// "the next call succeeds" — pgxpool may transparently replace a dead
// connection, and either outcome is legitimate. The guarantee is that
// Trustvian never *pretends*: a failed Observe reports an error wrapping
// ErrUnavailable, and state read back through an entirely new store and
// pool equals exactly the set of writes that were acknowledged. No
// in-memory shadow copy papers over the gap, and no acknowledged write is
// missing.
func TestConnectionLossDuringOperationFailsExplicitly(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("conn-loss-actor")

	// Tag this store's connections with a unique application_name so the
	// termination below can target *only* them.
	//
	// This matters more than it looks. An earlier version terminated every
	// backend on the database (`WHERE datname = current_database()`), which
	// is correct in isolation and wrong under `go test ./...`: package
	// binaries run in parallel, so it killed connections belonging to other
	// packages' tests mid-transaction and failed them with SQLSTATE 57P01.
	// Schema isolation does not help here — schemas isolate *tables*, not
	// connections. Targeting by application_name does.
	appName := fmt.Sprintf("tv_connloss_%d_%d", os.Getpid(), time.Now().UnixNano())
	iso := withApplicationName(t, isolatedSchemaDSN(t, dsn), appName)

	s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	const committed = 4
	now := testTime
	for range committed {
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		now = now.Add(time.Second)
	}

	// The killer connects without the tag, so it cannot terminate itself.
	killer, err := pgx.Connect(ctx, isolatedSchemaDSN(t, dsn))
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer killer.Close(ctx)

	var killed int
	if err := killer.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND application_name = $1
			  AND pid <> pg_backend_pid()
		) AS terminated`, appName).Scan(&killed); err != nil {
		t.Fatalf("terminate backends: %v", err)
	}
	if killed == 0 {
		t.Fatal("terminated 0 backends — the store's connections were not found, so this test proves nothing")
	}
	t.Logf("terminated %d backend(s) belonging to this store", killed)

	acknowledged := 0
	for range 3 {
		_, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now)
		if err == nil {
			acknowledged++
			now = now.Add(time.Second)
			continue
		}
		if !errors.Is(err, postgres.ErrUnavailable) {
			t.Errorf("Observe() err = %v, want it to wrap ErrUnavailable", err)
		}
		t.Logf("post-termination Observe reported: %v", err)
	}

	// The authoritative check, through a brand-new store and pool: stored
	// state equals exactly what was acknowledged.
	fresh, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() after connection loss error = %v", err)
	}
	defer func() { _ = fresh.Close() }()

	bl, ok := fresh.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false after connection loss — committed state was lost")
	}
	if got, want := bl.Fingerprints[fp.ID].Count, uint64(committed+acknowledged); got != want {
		t.Errorf("Count = %d, want %d (%d committed before termination + %d acknowledged after) — acknowledged writes and stored state disagree",
			got, want, committed, acknowledged)
	}
}

// ---------------------------------------------------------------------
// § Schema metadata integrity
// ---------------------------------------------------------------------

// TestMigrateSchemaVersionCompatibility is §27 and §31, table-driven over
// every state the version table can be in.
//
// The two ambiguity rows were real defects this task found. Before it,
// "baseline data present but no recorded version" was silently stamped
// with the current version, and two version rows were resolved by an
// unordered LIMIT 1 that was observed accepting a database marked with a
// version this binary cannot read. Both now fail closed.
//
// The critical direction is a *newer* recorded version: an older binary
// adopting newer state is how learned behavior gets silently corrupted,
// and it must never proceed.
func TestMigrateSchemaVersionCompatibility(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()

	tests := []struct {
		name string
		// seedBaseline controls whether baseline data exists before the
		// mutation, which is what separates "empty database with no
		// recorded version" from "data of unknown provenance".
		seedBaseline bool
		setup        string
		wantErr      error
	}{
		{
			name:    "expected version is accepted",
			wantErr: nil,
		},
		{
			name:    "newer unsupported version fails closed",
			setup:   `UPDATE ` + postgres.SchemaVersionTable + ` SET version = ` + fmt.Sprint(postgres.SchemaVersion+1),
			wantErr: postgres.ErrSchemaVersionMismatch,
		},
		{
			name:    "far-future version fails closed",
			setup:   `UPDATE ` + postgres.SchemaVersionTable + ` SET version = 9999`,
			wantErr: postgres.ErrSchemaVersionMismatch,
		},
		{
			// SchemaVersion is 1, so there is no genuine older release to be
			// compatible with. 0 stands for "written by something this build
			// does not recognize" — deliberately not an invented upgrade
			// path (see the task file's Migration upgrade path section).
			name:    "unrecognized older version fails closed",
			setup:   `UPDATE ` + postgres.SchemaVersionTable + ` SET version = 0`,
			wantErr: postgres.ErrSchemaVersionMismatch,
		},
		{
			name:         "missing version on an empty database is treated as fresh",
			seedBaseline: false,
			setup:        `DELETE FROM ` + postgres.SchemaVersionTable,
			wantErr:      nil,
		},
		{
			name:         "missing version with existing data fails closed",
			seedBaseline: true,
			setup:        `DELETE FROM ` + postgres.SchemaVersionTable,
			wantErr:      postgres.ErrAmbiguousSchemaState,
		},
		{
			name:    "multiple version rows fail closed",
			setup:   `INSERT INTO ` + postgres.SchemaVersionTable + ` (version) VALUES (` + fmt.Sprint(postgres.SchemaVersion+1) + `)`,
			wantErr: postgres.ErrAmbiguousSchemaState,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iso := isolatedSchemaDSN(t, dsn)

			first, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
			if err != nil {
				t.Fatalf("initial NewStore() error = %v", err)
			}
			if tt.seedBaseline {
				if _, _, err := first.Observe(ctx, hardeningKey("schema-actor"), testFingerprint(), features.VolatileFeatures{}, testTime); err != nil {
					t.Fatalf("seed Observe() error = %v", err)
				}
			}
			_ = first.Close()

			if tt.setup != "" {
				conn, err := pgx.Connect(ctx, iso)
				if err != nil {
					t.Fatalf("pgx.Connect() error = %v", err)
				}
				if _, err := conn.Exec(ctx, tt.setup); err != nil {
					_ = conn.Close(ctx)
					t.Fatalf("setup %q error = %v", tt.setup, err)
				}
				_ = conn.Close(ctx)
			}

			s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
			if s != nil {
				t.Cleanup(func() { _ = s.Close() })
			}

			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("NewStore() error = %v, want nil", err)
				}
				if s == nil {
					t.Fatal("NewStore() returned a nil Store with a nil error")
				}
				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("NewStore() err = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
			if s != nil {
				t.Error("NewStore() returned a non-nil Store alongside a schema error — must fail closed")
			}
			// A schema error must never leak the DSN it connected with.
			if err != nil && strings.Contains(err.Error(), "trustvian:trustvian") {
				t.Errorf("NewStore() err leaked DSN credentials: %v", err)
			}
		})
	}
}

// TestMigratePreservesExistingBaselineData is §30. With SchemaVersion at
// 1 there is no cross-version upgrade to exercise, so what is provable —
// and what actually protects data today — is that re-running the migration
// against a *populated* database is non-destructive. Every process restart
// does exactly this, which makes it by far the most frequently executed
// migration path in any deployment.
func TestMigratePreservesExistingBaselineData(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	iso := isolatedSchemaDSN(t, dsn)
	fp := testFingerprint()

	keys := []baseline.Key{
		hardeningKey("preserve-a"),
		hardeningKey("preserve-b"),
		hardeningKey("preserve-c"),
	}

	first, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	const observations = 7
	now := testTime
	for _, key := range keys {
		for range observations {
			if _, _, err := first.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			now = now.Add(time.Second)
		}
	}
	snapshot, ok := first.Get(ctx, keys[0])
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	_ = first.Close()

	// Re-initialize repeatedly, exactly as repeated restarts would.
	for round := range 3 {
		s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
		if err != nil {
			t.Fatalf("round %d NewStore() error = %v", round, err)
		}
		for _, key := range keys {
			if got := observationCount(t, s, key); got != observations {
				t.Errorf("round %d: Count(%s) = %d, want %d — re-running the migration altered stored data",
					round, key.ActorID, got, observations)
			}
		}
		again, ok := s.Get(ctx, keys[0])
		if !ok {
			t.Fatalf("round %d: Get() ok = false", round)
		}
		assertBaselinesEquivalent(t, snapshot, again)
		_ = s.Close()
	}

	// The schema metadata is unchanged too: still exactly one version row,
	// still the expected version.
	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var rows, version int
	if err := conn.QueryRow(ctx, `SELECT count(*), max(version) FROM `+postgres.SchemaVersionTable).Scan(&rows, &version); err != nil {
		t.Fatalf("read version table: %v", err)
	}
	if rows != 1 || version != postgres.SchemaVersion {
		t.Errorf("version table = %d row(s) at version %d, want 1 row at version %d", rows, version, postgres.SchemaVersion)
	}
}

// TestMigrateFailureIsAtomic is §29: a migration that fails must leave no
// half-built state. PostgreSQL's transactional DDL is what makes this
// achievable, and Migrate runs its DDL and its version insert in one
// transaction specifically to get it.
//
// The failure is injected by pre-creating a table with the version table's
// name but an incompatible shape: CREATE TABLE IF NOT EXISTS then succeeds
// as a no-op and the version read fails on the missing column, aborting
// the migration after its DDL has already run.
func TestMigrateFailureIsAtomic(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	iso := isolatedSchemaDSN(t, dsn)

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx,
		`CREATE TABLE `+postgres.SchemaVersionTable+` (unexpected_column text NOT NULL)`,
	); err != nil {
		t.Fatalf("pre-create conflicting table: %v", err)
	}

	s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err == nil {
		_ = s.Close()
		t.Fatal("NewStore() error = nil against an incompatible version table, want an error")
	}
	if s != nil {
		t.Error("NewStore() returned a non-nil Store alongside a migration error")
	}

	// The aborted migration created nothing. Had its DDL committed
	// independently of the failure, the baseline table would exist now.
	var baselineExists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM pg_tables
			 WHERE schemaname = current_schema() AND tablename = $1
		)`, postgres.BaselineTable,
	).Scan(&baselineExists); err != nil {
		t.Fatalf("check baseline table: %v", err)
	}
	if baselineExists {
		t.Errorf("%s exists after a failed migration — the migration was not atomic", postgres.BaselineTable)
	}

	// And the pre-existing table is untouched: nothing was destroyed in the
	// attempt.
	var columns int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.columns
		  WHERE table_schema = current_schema() AND table_name = $1`,
		postgres.SchemaVersionTable,
	).Scan(&columns); err != nil {
		t.Fatalf("inspect version table: %v", err)
	}
	if columns != 1 {
		t.Errorf("version table has %d column(s) after a failed migration, want the original 1", columns)
	}
}

// TestConcurrentStoreInitializationIsSafe is §25, stronger than task 035's
// version: many stores initialize the same empty database simultaneously,
// as a horizontally-scaled deployment does during a rollout. Exactly one
// logical initialization must result and every instance must succeed.
//
// Duplicated version metadata is the specific failure this guards: it is
// now (correctly) a fail-closed condition, so producing it here would take
// the whole deployment down on its next restart rather than being
// cosmetic untidiness.
func TestConcurrentStoreInitializationIsSafe(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	iso := isolatedSchemaDSN(t, dsn)

	const starters = 12
	var wg sync.WaitGroup
	errs := make(chan error, starters)
	stores := make(chan *postgres.Store, starters)
	release := make(chan struct{})

	for range starters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release // start together, maximizing overlap
			s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
			if err != nil {
				errs <- err
				return
			}
			stores <- s
		}()
	}
	close(release)
	wg.Wait()
	close(errs)
	close(stores)

	for err := range errs {
		t.Errorf("concurrent NewStore() error = %v", err)
	}
	opened := 0
	for s := range stores {
		opened++
		_ = s.Close()
	}
	if opened != starters {
		t.Errorf("%d of %d concurrent initializations succeeded, want all", opened, starters)
	}

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var versionRows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+postgres.SchemaVersionTable).Scan(&versionRows); err != nil {
		t.Fatalf("read version table: %v", err)
	}
	if versionRows != 1 {
		t.Errorf("version table holds %d rows after %d concurrent initializations, want exactly 1", versionRows, starters)
	}
}

// ---------------------------------------------------------------------
// § Bounded state at its upper limit
// ---------------------------------------------------------------------

// TestLargeBoundedBaselineRoundTrips is §36: a Baseline driven past the
// cardinality caps internal/baseline enforces must serialize, store, and
// load back without truncation.
//
// The point is not to test PostgreSQL's capacity — jsonb holds far more —
// but to confirm that a realistically *maximal* Baseline survives the
// round trip intact, since that is the row a long-lived production actor
// eventually produces. It is built by driving real observations through
// the real code path rather than hand-assembling a Baseline, so whatever
// bounds the domain actually applies are the bounds under test. That also
// means this test keeps working if a cap changes.
func TestLargeBoundedBaselineRoundTrips(t *testing.T) {
	dsn := requireDSN(t)
	s, iso := newStore(t, dsn)
	ctx := context.Background()
	key := hardeningKey("large-actor")

	// Comfortably more distinct fingerprints than the per-key caps
	// (predecessors, trigram predecessors, and delegators are each 64), so
	// transition and n-gram state fills to its bound as well.
	const distinct = 120
	fps := make([]fingerprint.Fingerprint, 0, distinct)
	for i := range distinct {
		fps = append(fps, numberedFingerprint(i))
	}

	now := testTime
	for round := range 3 {
		for i, fp := range fps {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{
				Timestamp:  now,
				Latency:    time.Duration(10+round+i%7) * time.Millisecond,
				HasLatency: true,
				// Exercise the delegator map's bound too.
				DelegatedFrom: fmt.Sprintf("delegator-%d", i%80),
			}, now); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			now = now.Add(time.Second)
		}
	}

	written, ok := s.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false, want true")
	}
	if len(written.Fingerprints) != distinct {
		t.Errorf("Fingerprints = %d, want %d — state was truncated", len(written.Fingerprints), distinct)
	}
	if len(written.DelegatorCounts) == 0 {
		t.Error("DelegatorCounts is empty — delegation state did not persist")
	}

	// Report the real serialized size, so a change that balloons it is
	// visible rather than silent.
	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)
	var onDisk int
	if err := conn.QueryRow(ctx,
		`SELECT pg_column_size(baseline) FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
		key.ActorID, key.Environment,
	).Scan(&onDisk); err != nil {
		t.Fatalf("read column size: %v", err)
	}
	t.Logf("maximal baseline: %d fingerprints, %d delegators, %d bytes stored",
		len(written.Fingerprints), len(written.DelegatorCounts), onDisk)

	// The round trip, not just the write: a genuinely separate store and
	// pool must load identical logical state.
	fresh, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer func() { _ = fresh.Close() }()

	loaded, ok := fresh.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false in a fresh store, want true")
	}
	assertBaselinesEquivalent(t, written, loaded)
}

// numberedFingerprint produces a distinct Fingerprint per index, varying
// only the operation name so every other stable dimension stays realistic.
func numberedFingerprint(i int) fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     fmt.Sprintf("POST /endpoint-%d", i),
		TargetName:        "payment-db",
		Environment:       "production",
	})
}

// assertBaselinesEquivalent compares every field of Baseline that carries
// learned behavior. It exists because "the row round-tripped" is only
// meaningful if the comparison covers the state a later pipeline stage
// actually reads — a check of Fingerprints alone would pass while
// sequence, timing, or delegation state was silently dropped.
func assertBaselinesEquivalent(t *testing.T, want, got baseline.Baseline) {
	t.Helper()

	if want.Key != got.Key {
		t.Errorf("Key = %+v, want %+v", got.Key, want.Key)
	}
	if !want.LastObserved.Equal(got.LastObserved) {
		t.Errorf("LastObserved = %v, want %v", got.LastObserved, want.LastObserved)
	}
	if want.LastFingerprintID != got.LastFingerprintID {
		t.Errorf("LastFingerprintID = %q, want %q", got.LastFingerprintID, want.LastFingerprintID)
	}
	if !want.LastFingerprintTime.Equal(got.LastFingerprintTime) {
		t.Errorf("LastFingerprintTime = %v, want %v", got.LastFingerprintTime, want.LastFingerprintTime)
	}
	if want.PreviousFingerprintID != got.PreviousFingerprintID {
		t.Errorf("PreviousFingerprintID = %q, want %q", got.PreviousFingerprintID, want.PreviousFingerprintID)
	}

	if len(want.Fingerprints) != len(got.Fingerprints) {
		t.Errorf("Fingerprints = %d entries, want %d", len(got.Fingerprints), len(want.Fingerprints))
	}
	for id, w := range want.Fingerprints {
		g, ok := got.Fingerprints[id]
		if !ok {
			t.Errorf("Fingerprints[%s] missing after round trip", id)
			continue
		}
		if g.Count != w.Count {
			t.Errorf("Fingerprints[%s].Count = %d, want %d", id, g.Count, w.Count)
		}
		if g.LatencyMean != w.LatencyMean || g.LatencyVariance != w.LatencyVariance {
			t.Errorf("Fingerprints[%s] latency stats = (%v, %v), want (%v, %v)",
				id, g.LatencyMean, g.LatencyVariance, w.LatencyMean, w.LatencyVariance)
		}
		if g.ErrorRate != w.ErrorRate {
			t.Errorf("Fingerprints[%s].ErrorRate = %v, want %v", id, g.ErrorRate, w.ErrorRate)
		}
		if !g.LastObserved.Equal(w.LastObserved) {
			t.Errorf("Fingerprints[%s].LastObserved = %v, want %v", id, g.LastObserved, w.LastObserved)
		}
		if g.HourActivity != w.HourActivity {
			t.Errorf("Fingerprints[%s].HourActivity differs after round trip", id)
		}
		if len(g.PredecessorCounts) != len(w.PredecessorCounts) {
			t.Errorf("Fingerprints[%s].PredecessorCounts = %d entries, want %d",
				id, len(g.PredecessorCounts), len(w.PredecessorCounts))
		}
		for pred, wc := range w.PredecessorCounts {
			if g.PredecessorCounts[pred] != wc {
				t.Errorf("Fingerprints[%s].PredecessorCounts[%s] = %d, want %d",
					id, pred, g.PredecessorCounts[pred], wc)
			}
		}
		if len(g.TrigramCounts) != len(w.TrigramCounts) {
			t.Errorf("Fingerprints[%s].TrigramCounts = %d entries, want %d",
				id, len(g.TrigramCounts), len(w.TrigramCounts))
		}
		for tk, wc := range w.TrigramCounts {
			if g.TrigramCounts[tk] != wc {
				t.Errorf("Fingerprints[%s].TrigramCounts[%+v] = %d, want %d", id, tk, g.TrigramCounts[tk], wc)
			}
		}
	}

	if len(want.DelegatorCounts) != len(got.DelegatorCounts) {
		t.Errorf("DelegatorCounts = %d entries, want %d", len(got.DelegatorCounts), len(want.DelegatorCounts))
	}
	for d, wc := range want.DelegatorCounts {
		if got.DelegatorCounts[d] != wc {
			t.Errorf("DelegatorCounts[%s] = %d, want %d", d, got.DelegatorCounts[d], wc)
		}
	}
}

// ---------------------------------------------------------------------
// § Readiness probing (task 042)
// ---------------------------------------------------------------------

// TestPingReportsDatabaseUsability covers the readiness probe the runtime's
// /readyz endpoint depends on. The three states a probe must distinguish are
// exactly the three an operator cares about: usable, unreachable, and
// closed.
func TestPingReportsDatabaseUsability(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()

	t.Run("healthy database", func(t *testing.T) {
		s, _ := newStore(t, dsn)
		if err := s.Ping(ctx); err != nil {
			t.Errorf("Ping() error = %v against a reachable database, want nil", err)
		}
	})

	t.Run("after Close", func(t *testing.T) {
		iso := isolatedSchemaDSN(t, dsn)
		s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
		if err != nil {
			t.Fatalf("NewStore() error = %v", err)
		}
		_ = s.Close()

		// A closed store must report unusable rather than panic or block —
		// Shutdown closes the store, and a probe can still arrive.
		if err := s.Ping(ctx); err == nil {
			t.Error("Ping() error = nil after Close(), want an error")
		}
	})

	t.Run("connections terminated", func(t *testing.T) {
		// The same application_name targeting task 036 uses, so this cannot
		// disturb other packages' tests running in parallel.
		appName := fmt.Sprintf("tv_ping_%d_%d", os.Getpid(), time.Now().UnixNano())
		iso := withApplicationName(t, isolatedSchemaDSN(t, dsn), appName)

		s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
		if err != nil {
			t.Fatalf("NewStore() error = %v", err)
		}
		t.Cleanup(func() { _ = s.Close() })

		if err := s.Ping(ctx); err != nil {
			t.Fatalf("Ping() error = %v before termination, want nil", err)
		}

		killer, err := pgx.Connect(ctx, isolatedSchemaDSN(t, dsn))
		if err != nil {
			t.Fatalf("pgx.Connect() error = %v", err)
		}
		defer killer.Close(ctx)
		if _, err := killer.Exec(ctx,
			`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
			  WHERE datname = current_database() AND application_name = $1 AND pid <> pg_backend_pid()`,
			appName,
		); err != nil {
			t.Fatalf("terminate backends: %v", err)
		}

		// pgxpool replaces dead connections, so a Ping after termination may
		// legitimately succeed on a fresh one. The guarantee is that it
		// answers — promptly, and without panicking — not which answer it
		// gives. Readiness recovering on its own is the *desired* behavior:
		// it is what lets a database blip resolve without a restart.
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err = s.Ping(pingCtx)
		t.Logf("Ping() after terminating this store's backends: %v", err)

		// And it converges back to healthy without any restart, which is the
		// §31 operational property.
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if s.Ping(ctx) == nil {
				return
			}
		}
		t.Error("Ping() never recovered after connection termination — readiness would stay false without a restart")
	})

	t.Run("unreachable database", func(t *testing.T) {
		// A store that was constructed successfully and whose database then
		// became unreachable cannot be built directly, so this uses a pool
		// pointed at a port nothing listens on: the probe must fail, not hang.
		s, err := postgres.NewStore(ctx, postgres.Config{
			DSN:            "postgres://trustvian:secret@127.0.0.1:1/trustvian?sslmode=disable",
			ConnectTimeout: 2 * time.Second,
		})
		if err == nil {
			_ = s.Close()
			t.Skip("unexpectedly connected to port 1; cannot exercise the unreachable path")
		}
		// NewStore fails closed, which is v0.8 behavior and is the reason
		// readiness never has to report a half-constructed store.
		if !strings.Contains(err.Error(), "unavailable") {
			t.Errorf("NewStore() err = %v, want it to report the database unavailable", err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("NewStore() err leaked the DSN password: %v", err)
		}
	})
}

// TestPingIsBoundedByContext is the property /readyz relies on: a wedged
// database must not hold the probe open past its deadline.
func TestPingIsBoundedByContext(t *testing.T) {
	dsn := requireDSN(t)
	s, _ := newStore(t, dsn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := s.Ping(ctx); err == nil {
		t.Error("Ping() error = nil for a cancelled context, want an error")
	}
}
