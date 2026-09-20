package postgres_test

// Task 036's stress tier (§8, §9, §10, §43).
//
// These are correctness tests, not benchmarks. Each one asserts an exact
// count — "every successful observation is represented" — rather than a
// latency or a throughput figure, because a lost update is a correctness
// defect and no amount of measured speed excuses one. Timings that do
// appear are logged for information and never asserted on: a threshold on
// network-dependent latency would be a flake generator, not a test.
//
// # Test tiers
//
// Three tiers, selected by the standard toolchain flags rather than a new
// gating mechanism of this project's own:
//
//	go test ./...                                    # unit only — no database needed
//	TRUSTVIAN_TEST_POSTGRES_DSN=... go test -short ./...   # + integration, without stress
//	TRUSTVIAN_TEST_POSTGRES_DSN=... go test ./...          # + stress (the release gate)
//
// Everything in this file skips under -short. The integration tier stays
// fast enough for routine use, while the release gate runs the full
// contention load. See docs/storage-guide.md § Running the integration
// tests.
//
// Why these sizes: large enough that a lost update is overwhelmingly
// likely to appear if one is possible (task 035's mutation testing lost
// ~160 of 200 observations when row locking was removed, so a defect is
// not subtle at this scale), and small enough that the whole file runs in
// seconds rather than minutes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/store/postgres"
)

// requireStress skips unless the stress tier is selected.
func requireStress(t *testing.T) string {
	t.Helper()

	dsn := requireDSN(t)
	if testing.Short() {
		t.Skip("stress tier skipped under -short; see this file's doc comment for the three tiers")
	}
	return dsn
}

// TestStressSameKeyContentionLosesNoObservations is §8: task 035 proved
// same-key concurrency at 8×25; this drives 32 writers × 100 observations
// at one row, repeated, which is the shape a hot single actor produces in
// production when every replica is observing it at once.
//
// Every one of those observations contends for the same row lock, so this
// is simultaneously the worst case for throughput and the only case where
// a lost update can occur. The assertion is exact: N×M, no tolerance.
//
// Repeated rounds, each on a fresh key, because a concurrency defect can
// be probabilistic — one green run of a racy implementation proves
// nothing, and a defect that appears in one round out of three is still a
// defect.
func TestStressSameKeyContentionLosesNoObservations(t *testing.T) {
	dsn := requireStress(t)
	s, _ := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()

	const (
		writers          = 32
		observationsEach = 100
		rounds           = 3
		wantTotal        = writers * observationsEach
	)

	for round := range rounds {
		key := hardeningKey(fmt.Sprintf("stress-same-key-%d", round))

		var wg sync.WaitGroup
		errs := make(chan error, writers)
		start := make(chan struct{})

		begin := time.Now()
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // release all writers together
				for range observationsEach {
					if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		elapsed := time.Since(begin)

		// Every observation must have *succeeded*. A test that tolerated
		// errors and then counted only the survivors would pass against an
		// implementation that dropped writes under load.
		for err := range errs {
			t.Fatalf("round %d: concurrent Observe() error = %v", round, err)
		}

		if got := observationCount(t, s, key); got != wantTotal {
			t.Errorf("round %d: Count = %d after %d concurrent observations, want %d — %d update(s) were lost",
				round, got, wantTotal, wantTotal, wantTotal-int(got))
		}

		t.Logf("round %d: %d writers × %d observations on one key in %v (%.0f obs/s)",
			round, writers, observationsEach, elapsed, float64(wantTotal)/elapsed.Seconds())
	}
}

// TestStressFirstWriteContentionLosesNoObservations is §9, the case
// `SELECT ... FOR UPDATE` cannot protect on its own: there is no row to
// lock yet, so without the `INSERT ... ON CONFLICT DO NOTHING` that
// precedes it, concurrent first observations would each read an empty
// baseline and the last writer would win.
//
// Task 035 proved this at 24 writers; this drives 96 at a guaranteed-absent
// row, across several fresh keys. Three things are asserted: every
// observation survives, exactly one row exists (not one per writer), and
// the row is a single coherent baseline rather than a partially-merged one.
func TestStressFirstWriteContentionLosesNoObservations(t *testing.T) {
	dsn := requireStress(t)
	s, iso := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	const (
		writers = 96
		rounds  = 5
	)

	for round := range rounds {
		key := hardeningKey(fmt.Sprintf("stress-first-write-%d", round))

		// The premise: the row genuinely does not exist yet.
		var exists int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
			key.ActorID, key.Environment,
		).Scan(&exists); err != nil {
			t.Fatalf("round %d: precondition query: %v", round, err)
		}
		if exists != 0 {
			t.Fatalf("round %d: row already exists — the test's premise did not hold", round)
		}

		var wg sync.WaitGroup
		errs := make(chan error, writers)
		start := make(chan struct{})

		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start // every writer races the absent row simultaneously
				if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
					errs <- err
				}
			}()
		}
		close(start)
		wg.Wait()
		close(errs)

		for err := range errs {
			t.Fatalf("round %d: concurrent first Observe() error = %v", round, err)
		}

		if got := observationCount(t, s, key); got != writers {
			t.Errorf("round %d: Count = %d after %d concurrent first observations, want %d — %d update(s) were lost",
				round, got, writers, writers, writers-int(got))
		}

		// Exactly one row: the ON CONFLICT clause must have collapsed every
		// racing insert into one, not produced a duplicate per writer.
		var rows int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
			key.ActorID, key.Environment,
		).Scan(&rows); err != nil {
			t.Fatalf("round %d: row count query: %v", round, err)
		}
		if rows != 1 {
			t.Errorf("round %d: %d rows for one key, want exactly 1", round, rows)
		}
	}
}

// TestStressMultiKeyConcurrencyDoesNotSerializeGlobally is §10: many
// distinct actors observed at once must not contend, because nothing in
// the write path takes a table lock or an advisory lock — only a row lock,
// and distinct keys are distinct rows.
//
// Correctness is asserted exactly (every key reaches its own full count).
// The timing comparison against the same-key case is *logged, not
// asserted*: it is the evidence a human reads to confirm there is no
// accidental global serialization, and turning network-dependent latency
// into a pass/fail threshold would only produce flakes. A regression here
// would be unmistakable in the logged ratio.
func TestStressMultiKeyConcurrencyDoesNotSerializeGlobally(t *testing.T) {
	dsn := requireStress(t)
	s, _ := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()

	const (
		keys             = 32
		observationsEach = 100
		total            = keys * observationsEach
	)

	var wg sync.WaitGroup
	errs := make(chan error, keys)
	start := make(chan struct{})

	begin := time.Now()
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := hardeningKey(fmt.Sprintf("stress-multi-key-%d", i))
			<-start
			for range observationsEach {
				if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
					errs <- err
					return
				}
			}
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	elapsed := time.Since(begin)

	for err := range errs {
		t.Fatalf("concurrent Observe() error = %v", err)
	}

	for i := range keys {
		key := hardeningKey(fmt.Sprintf("stress-multi-key-%d", i))
		if got := observationCount(t, s, key); got != observationsEach {
			t.Errorf("Count(%s) = %d, want %d", key.ActorID, got, observationsEach)
		}
	}

	t.Logf("%d keys × %d observations in %v (%.0f obs/s) — compare against the same-key figure above; "+
		"a similar rate for both would indicate global serialization",
		keys, observationsEach, elapsed, float64(total)/elapsed.Seconds())
}

// TestStressMixedContentionUnderCancellation combines the two failure
// modes that only interact under load: some writers complete, others give
// up mid-flight. The invariant is that the count equals exactly the number
// that *succeeded* — abandoned transactions must contribute nothing, and
// must not prevent their successors from contributing.
//
// This is the closest thing here to real production traffic, where clients
// disconnect and deadlines expire while other work on the same actor
// continues.
func TestStressMixedContentionUnderCancellation(t *testing.T) {
	dsn := requireStress(t)
	s, _ := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := hardeningKey("stress-mixed")

	const (
		committers = 16
		abandoners = 16
		eachCommit = 50
	)

	var wg sync.WaitGroup
	var succeeded int64
	var mu sync.Mutex
	errs := make(chan error, committers*eachCommit)
	start := make(chan struct{})

	for range committers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range eachCommit {
				if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
					errs <- err
					return
				}
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}

	// Abandoners cancel immediately, so most never reach a commit — but any
	// that happen to win the race and commit before cancellation are
	// counted, so the final assertion stays exact rather than approximate.
	for range abandoners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			if _, _, err := s.Observe(cctx, key, fp, features.VolatileFeatures{}, testTime); err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			} else if !errors.Is(err, context.Canceled) {
				errs <- fmt.Errorf("abandoner got %w, want context.Canceled", err)
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("Observe() error = %v", err)
	}

	if got := observationCount(t, s, key); got != uint64(succeeded) {
		t.Errorf("Count = %d, want %d (the number of Observe calls that returned nil) — "+
			"stored state and acknowledged writes disagree", got, succeeded)
	}
	t.Logf("%d committers × %d + %d cancelled writers: %d acknowledged, %d stored",
		committers, eachCommit, abandoners, succeeded, observationCount(t, s, key))

	assertStoreStillServes(t, s, hardeningKey("stress-mixed-after"))
}

// TestStressManyDistinctActorsBoundedRowCount confirms §35 at scale: one
// logical row per baseline.Key and nothing else. An implementation that
// had drifted toward per-observation rows — the event-warehouse shape this
// milestone explicitly rejects — would show it here immediately.
func TestStressManyDistinctActorsBoundedRowCount(t *testing.T) {
	dsn := requireStress(t)
	s, iso := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()

	const (
		actors           = 200
		observationsEach = 10
	)

	now := testTime
	for i := range actors {
		key := hardeningKey(fmt.Sprintf("bounded-actor-%d", i))
		for range observationsEach {
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			now = now.Add(time.Second)
		}
	}

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+postgres.BaselineTable).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != actors {
		t.Errorf("%s holds %d rows after %d actors × %d observations, want %d — "+
			"row count must track distinct keys, never observation volume",
			postgres.BaselineTable, rows, actors, observationsEach, actors)
	}

	// And no other table appeared alongside it.
	var tables []string
	tableRows, err := conn.Query(ctx,
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY tablename`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer tableRows.Close()
	for tableRows.Next() {
		var name string
		if err := tableRows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		tables = append(tables, name)
	}
	if len(tables) != 2 {
		t.Errorf("schema holds tables %v, want exactly 2 (%s, %s) — no event/history table may be introduced",
			tables, postgres.BaselineTable, postgres.SchemaVersionTable)
	}
}

// ---------------------------------------------------------------------
// § Database restart durability
// ---------------------------------------------------------------------

// restartCmdEnv names a shell command that restarts the test database, for
// example:
//
//	export TRUSTVIAN_TEST_POSTGRES_RESTART_CMD='docker restart trustvian-pg'
//
// It is a separate variable from the DSN because restarting a database is
// a genuinely destructive operation on whatever server the DSN points at.
// Requiring it to be named explicitly means nobody bounces a shared or
// staging database by exporting one connection string, and it keeps
// `go test ./...` — and the ordinary integration tier — free of any
// container dependency.
const restartCmdEnv = "TRUSTVIAN_TEST_POSTGRES_RESTART_CMD"

// TestDatabaseRestartPreservesCommittedBaseline is §15, the mandatory
// durability proof: state PostgreSQL acknowledged must survive the
// database process itself going away and coming back.
//
// This is a different guarantee from
// TestRestartDurabilityAcrossStoreInstances (task 035), which restarts the
// *client* and therefore only proves nothing is cached in Trustvian's
// process. Here the server's own buffers, connections, and WAL are
// involved: a commit that lived only in a page cache would be lost, and
// this is the test that would catch it.
//
// Skips unless TRUSTVIAN_TEST_POSTGRES_RESTART_CMD is set, since only the
// operator of a given database can say how to restart it safely. Task 037
// owns the canonical container environment; this test deliberately builds
// none of it and shells out to whatever command it is given.
//
// Note the blast radius: restarting the server disrupts every other
// connection to it, and `go test ./...` runs package binaries in parallel.
// Setting the restart variable for a whole-repository run can therefore
// make unrelated integration tests fail through no fault of their own.
// Run it on its own:
//
//	TRUSTVIAN_TEST_POSTGRES_DSN=... TRUSTVIAN_TEST_POSTGRES_RESTART_CMD='docker restart trustvian-pg' \
//	  go test -run TestDatabaseRestartPreservesCommittedBaseline ./internal/store/postgres/
//
// The variable is left unset for ordinary runs precisely so this is an
// explicit, deliberate act rather than a surprise.
func TestDatabaseRestartPreservesCommittedBaseline(t *testing.T) {
	dsn := requireDSN(t)
	restart := restartCommand(t)

	ctx := context.Background()
	fp := testFingerprint()

	// Not schema-isolated: a restart affects the whole server, so this test
	// cannot run concurrently with others regardless, and using the real
	// default schema makes the proof about ordinary production tables.
	// A key unique to this run keeps it from colliding with anything else.
	key := baseline.Key{
		ActorID:     fmt.Sprintf("db-restart-%d", time.Now().UnixNano()),
		Environment: "production",
	}

	before, err := postgres.NewStore(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	const observations = 9
	now := testTime
	for range observations {
		if _, _, err := before.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		now = now.Add(time.Second)
	}
	committed, ok := before.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false before restart, want true")
	}
	// Close first: an acknowledged commit must not depend on the client
	// still being connected when the server goes down.
	_ = before.Close()

	// Record when the server started, so the assertion below can prove the
	// restart actually happened. Without this, a restart command that
	// silently did nothing — a typo, a wrong container name, a command that
	// exits 0 without acting — would make this test pass while proving
	// nothing at all about durability.
	startedBefore := postmasterStartTime(t, dsn)

	t.Logf("restarting database with: %s", restart)
	if out, err := runRestart(restart); err != nil {
		t.Fatalf("restart command failed: %v\noutput: %s", err, out)
	}

	// A completely new store against the restarted server. NewStore's own
	// connect timeout and retry-free Ping is what makes this wait bounded;
	// the loop below tolerates the server still coming up, which is a
	// property of the restart, not of Trustvian.
	after := connectAfterRestart(t, dsn)
	defer func() { _ = after.Close() }()

	// The restart is now a verified fact, not an assumption: PostgreSQL
	// reports a later postmaster start time than it did before.
	startedAfter := postmasterStartTime(t, dsn)
	if !startedAfter.After(startedBefore) {
		t.Fatalf("postmaster start time is unchanged (%v) — the database never restarted, so this test proves nothing; check %s",
			startedAfter, restartCmdEnv)
	}
	t.Logf("verified restart: postmaster start time moved from %v to %v", startedBefore, startedAfter)

	reloaded, ok := after.Get(ctx, key)
	if !ok {
		t.Fatal("Get() ok = false after database restart — committed state did not survive")
	}
	assertBaselinesEquivalent(t, committed, reloaded)

	// The store is fully functional afterwards, not merely readable.
	if _, _, err := after.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
		t.Fatalf("Observe() error = %v after restart, want nil", err)
	}
	if got := observationCount(t, after, key); got != observations+1 {
		t.Errorf("Count = %d after restart and one more observation, want %d", got, observations+1)
	}

	t.Logf("durability confirmed: %d observations committed before the restart were all present after it", observations)
}

// connectAfterRestart opens a store once the restarted server accepts
// connections. The retry loop lives here, in the test, and deliberately
// not in NewStore: Trustvian fails fast on an unavailable database by
// design (docs/SECURITY.md), and a production deployment's supervisor —
// not the store — owns restart-tolerance.
func connectAfterRestart(t *testing.T, dsn string) *postgres.Store {
	t.Helper()

	ctx := context.Background()
	deadline := time.Now().Add(60 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		s, err := postgres.NewStore(ctx, postgres.Config{DSN: dsn, ConnectTimeout: 3 * time.Second})
		if err == nil {
			return s
		}
		lastErr = err
	}
	t.Fatalf("database did not accept connections within 60s of restart: %v", lastErr)
	return nil
}

// restartCommand returns the configured restart command, skipping the test
// when none is set.
func restartCommand(t *testing.T) string {
	t.Helper()

	cmd := os.Getenv(restartCmdEnv)
	if cmd == "" {
		t.Skipf("%s not set; skipping database-restart durability test (see docs/storage-guide.md § Database restart durability)", restartCmdEnv)
	}
	return cmd
}

// runRestart executes the operator-supplied restart command. It is run
// through a shell so a natural one-liner ("docker restart trustvian-pg",
// "systemctl restart postgresql") works as written. The command comes from
// the developer's own environment, not from any untrusted input — this is
// test scaffolding for a database the developer already controls, and it
// never runs unless they opt in by setting the variable.
func runRestart(command string) (string, error) {
	cmd := exec.Command("sh", "-c", command)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// postmasterStartTime reports when the PostgreSQL server process started.
// It is how the restart test proves a restart occurred rather than
// assuming the configured command worked — the same discipline that keeps
// a concurrency test from passing vacuously.
func postmasterStartTime(t *testing.T, dsn string) time.Time {
	t.Helper()

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var started time.Time
	if err := conn.QueryRow(ctx, `SELECT pg_postmaster_start_time()`).Scan(&started); err != nil {
		t.Fatalf("read postmaster start time: %v", err)
	}
	return started
}
