package postgres_test

// Tests here fall into two groups.
//
// The first needs no database and always runs: DSN validation,
// fail-closed construction, and — most importantly — the credential
// redaction regression. Those are correctness and security properties
// that must never depend on a developer having PostgreSQL installed.
//
// The second is gated on TRUSTVIAN_TEST_POSTGRES_DSN and covers what
// only a real server can show: the concurrent-first-observation race,
// restart durability across separate connection pools, context
// cancellation, and the operator-facing inspection columns. Unset, they
// skip, so `go test ./...` never requires a server — see
// docs/storage-guide.md for the one-line docker command that sets it up.
//
// The nine *contract* guarantees are asserted elsewhere, against every
// backend at once, in internal/store/contract_test.go. This file
// deliberately does not restate them.

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

const dsnEnv = "TRUSTVIAN_TEST_POSTGRES_DSN"

var testTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func testFingerprint() fingerprint.Fingerprint {
	return fingerprint.Compute(features.StableFeatures{
		ActorType:         event.ActorTypeService,
		OperationCategory: event.OperationCategoryHTTP,
		OperationName:     "POST /payment",
		TargetName:        "payment-db",
		Environment:       "production",
	})
}

// requireDSN skips the calling test when no test database is configured.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping PostgreSQL integration test (see docs/storage-guide.md)", dsnEnv)
	}
	return dsn
}

// newStore builds a store whose tables live in a PostgreSQL schema
// private to this test, and returns it alongside the isolated DSN.
//
// Isolation is by schema rather than by truncating a shared table. The
// first version of this helper truncated, reasoning that the tests in a
// package run sequentially — which is true, and which missed that
// `go test ./...` runs *separate packages' binaries concurrently*. Two
// packages here exercise PostgreSQL (this one and internal/store's
// contract), so a shared-table TRUNCATE in one wiped rows the other was
// mid-way through counting, producing phantom "lost update" failures in
// a correct implementation. A private schema removes the shared
// resource instead of trying to time-share it, and also means these
// tests can run against a database that already holds real data without
// destroying it.
func newStore(t testing.TB, dsn string) (*postgres.Store, string) {
	t.Helper()

	isolated := isolatedSchemaDSN(t, dsn)
	s, err := postgres.NewStore(context.Background(), postgres.Config{DSN: isolated})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, isolated
}

// --- Always run: no database required ---

// TestNewStoreUnparseableDSNDoesNotLeakCredentials is a security
// regression test for a real, empirically-confirmed driver behavior:
// pgx redacts the password when it can parse a DSN
// (`postgres://u:xxxxx@host/db`), but reproduces the input *verbatim*
// when it cannot — precisely the case an invalid DSN hits. Wrapping the
// driver's parse error would therefore leak credentials into logs on the
// one code path most likely to be logged.
//
// This test exists so that a future "improve the error message by
// wrapping err" change fails loudly instead of silently reintroducing
// the leak.
func TestNewStoreUnparseableDSNDoesNotLeakCredentials(t *testing.T) {
	const secret = "SUPERSECRETPASSWORD"

	_, err := postgres.NewStore(context.Background(), postgres.Config{
		DSN: "not-a-valid-dsn://" + secret,
	})
	if err == nil {
		t.Fatal("NewStore() error = nil for an unparseable DSN, want an error")
	}
	if !errors.Is(err, postgres.ErrInvalidDSN) {
		t.Errorf("NewStore() err = %v, want it to wrap ErrInvalidDSN", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("NewStore() error leaked the DSN contents: %q", err.Error())
	}
}

func TestNewStoreEmptyDSNFailsClosed(t *testing.T) {
	s, err := postgres.NewStore(context.Background(), postgres.Config{DSN: ""})
	if !errors.Is(err, postgres.ErrInvalidDSN) {
		t.Errorf("NewStore() err = %v, want ErrInvalidDSN", err)
	}
	if s != nil {
		t.Error("NewStore() returned a non-nil Store alongside an error")
	}
}

// TestNewStoreUnreachableDatabaseFailsClosed proves fail-fast startup:
// a well-formed DSN pointing nowhere must fail construction rather than
// deferring the problem to the first Observe, and must not leak the
// password while doing so.
func TestNewStoreUnreachableDatabaseFailsClosed(t *testing.T) {
	const secret = "PW_THAT_MUST_NOT_APPEAR"

	start := time.Now()
	s, err := postgres.NewStore(context.Background(), postgres.Config{
		// Port 1 is reserved and never listening.
		DSN:            "postgres://trustvian:" + secret + "@127.0.0.1:1/trustvian?sslmode=disable",
		ConnectTimeout: 3 * time.Second,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewStore() error = nil for an unreachable database, want an error")
	}
	if !errors.Is(err, postgres.ErrUnavailable) {
		t.Errorf("NewStore() err = %v, want it to wrap ErrUnavailable", err)
	}
	if s != nil {
		t.Error("NewStore() returned a non-nil Store alongside an error — never a partially usable store")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("NewStore() error leaked the DSN password: %q", err.Error())
	}
	// The ConnectTimeout is what makes this bounded rather than waiting
	// out the OS TCP timeout.
	if elapsed > 10*time.Second {
		t.Errorf("NewStore() took %v for an unreachable database, want it bounded by ConnectTimeout", elapsed)
	}
}

// --- Gated on a real database ---

// TestConcurrentFirstObservationRace is §19's mandatory case and the one
// a naive implementation gets wrong. `SELECT ... FOR UPDATE` locks
// nothing when the row does not exist, so N concurrent *first*
// observations for a brand-new key could each read "empty" and the last
// UPDATE would win, discarding N-1 observations.
//
// Observe defends against this by materializing the row with
// `INSERT ... ON CONFLICT DO NOTHING` before taking the lock. This test
// starts from a guaranteed-absent row and requires that all N
// observations survive, in exactly one row.
func TestConcurrentFirstObservationRace(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := baseline.Key{ActorID: "race-actor", Environment: "production"}

	const writers = 24

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, writers)
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // maximize the chance all writers race on the absent row
			if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent first Observe() error = %v", err)
	}

	bl, ok := s.Get(ctx, key)
	if !ok {
		t.Fatalf("Get() ok = false after %d observations, want true", writers)
	}
	if got := bl.Fingerprints[fp.ID].Count; got != writers {
		t.Errorf("Count = %d, want %d — %d concurrent first-observation(s) were lost",
			got, writers, writers-int(got))
	}

	// Exactly one logical baseline, not one row per racing writer.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)
	var rows int
	if err := conn.QueryRow(ctx,
		"SELECT count(*) FROM "+postgres.BaselineTable+" WHERE actor_id = $1 AND environment = $2",
		key.ActorID, key.Environment,
	).Scan(&rows); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if rows != 1 {
		t.Errorf("row count = %d, want exactly 1 logical baseline", rows)
	}
}

// TestRestartDurabilityAcrossStoreInstances is §31: state written by one
// Store must be readable by a genuinely separate one. The first store is
// closed before the second is constructed, so its connection pool and
// every in-process value it held are gone — nothing but the database can
// be responsible for this passing.
func TestRestartDurabilityAcrossStoreInstances(t *testing.T) {
	dsn := requireDSN(t)
	first, dsn := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := baseline.Key{ActorID: "restart-actor", Environment: "production"}

	const observations = 7
	now := testTime
	for range observations {
		if _, _, err := first.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		now = now.Add(time.Second)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := postgres.NewStore(ctx, postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("NewStore() (restart) error = %v", err)
	}
	defer second.Close()

	bl, ok := second.Get(ctx, key)
	if !ok {
		t.Fatalf("Get() ok = false after restart, want true")
	}
	if got := bl.Fingerprints[fp.ID].Count; got != observations {
		t.Errorf("Count = %d after restart, want %d", got, observations)
	}
	// Sequence state must survive too, not just the counter.
	if bl.LastFingerprintID != fp.ID {
		t.Errorf("LastFingerprintID = %q after restart, want %q", bl.LastFingerprintID, fp.ID)
	}
}

// TestObserveHonorsContextCancellation is §22: a cancelled context must
// surface as an error rather than being swallowed, and must not commit.
func TestObserveHonorsContextCancellation(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn)
	fp := testFingerprint()
	key := baseline.Key{ActorID: "cancel-actor", Environment: "production"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, testTime); err == nil {
		t.Fatal("Observe() error = nil for a cancelled context, want an error")
	}

	// Nothing may have been committed.
	if _, ok := s.Get(context.Background(), key); ok {
		t.Error("Get() ok = true after a cancelled Observe — the transaction must not have committed")
	}
}

func TestGetHonorsContextCancellation(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn)
	fp := testFingerprint()
	key := baseline.Key{ActorID: "cancel-get-actor", Environment: "production"}

	if _, _, err := s.Observe(context.Background(), key, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Get has no error return (see its doc comment), so a cancelled
	// context surfaces as "not found" — fail-safe in the direction that
	// raises anomaly rather than suppressing it.
	if _, ok := s.Get(ctx, key); ok {
		t.Error("Get() ok = true for a cancelled context, want false")
	}
}

// TestInspectionColumnsArePopulated is §15: an operator must be able to
// answer "which actors, how much learned, when last updated" with plain
// SQL and no JSON decoding.
func TestInspectionColumnsArePopulated(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()
	key := baseline.Key{ActorID: "inspect-actor", Environment: "production"}

	const observations = 4
	now := testTime
	for range observations {
		if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		now = now.Add(time.Second)
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var (
		schemaVersion    int
		fingerprintCount int
		observationCount int64
		lastObserved     *time.Time
		updatedAt        time.Time
	)
	if err := conn.QueryRow(ctx,
		`SELECT schema_version, fingerprint_count, observation_count, last_observed, updated_at
		 FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
		key.ActorID, key.Environment,
	).Scan(&schemaVersion, &fingerprintCount, &observationCount, &lastObserved, &updatedAt); err != nil {
		t.Fatalf("inspection query error = %v", err)
	}

	if schemaVersion != postgres.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", schemaVersion, postgres.SchemaVersion)
	}
	if fingerprintCount != 1 {
		t.Errorf("fingerprint_count = %d, want 1", fingerprintCount)
	}
	if observationCount != observations {
		t.Errorf("observation_count = %d, want %d", observationCount, observations)
	}
	if lastObserved == nil {
		t.Error("last_observed is NULL, want the baseline's own LastObserved")
	}
	if updatedAt.IsZero() {
		t.Error("updated_at is zero")
	}
}

// TestMigrateIsIdempotentAndConcurrencySafe is §17: repeated and
// simultaneous initialization must both be safe. Constructing many
// stores at once exercises the advisory-lock path that serializes
// concurrent startups.
func TestMigrateIsIdempotentAndConcurrencySafe(t *testing.T) {
	dsn := requireDSN(t)
	_ = requireDSN(t)
	ctx := context.Background()

	const starters = 8
	var wg sync.WaitGroup
	errs := make(chan error, starters)
	stores := make(chan *postgres.Store, starters)
	start := make(chan struct{})

	for range starters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			s, err := postgres.NewStore(ctx, postgres.Config{DSN: dsn})
			if err != nil {
				errs <- err
				return
			}
			stores <- s
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(stores)

	for err := range errs {
		t.Fatalf("concurrent NewStore() error = %v", err)
	}
	for s := range stores {
		_ = s.Close()
	}

	// Exactly one version row, despite N concurrent initializations.
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)
	var versionRows int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM "+postgres.SchemaVersionTable).Scan(&versionRows); err != nil {
		t.Fatalf("version count error = %v", err)
	}
	if versionRows != 1 {
		t.Errorf("%s row count = %d, want exactly 1", postgres.SchemaVersionTable, versionRows)
	}
}

// TestMigrateRejectsIncompatibleSchemaVersion is §16/§17's fail-closed
// requirement: a database written by a different schema version must
// abort startup rather than being silently upgraded or misread.
func TestMigrateRejectsIncompatibleSchemaVersion(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn) // ensures the schema exists
	_ = s

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	// Pose as a future version, then restore afterwards.
	future := postgres.SchemaVersion + 99
	if _, err := conn.Exec(ctx, "UPDATE "+postgres.SchemaVersionTable+" SET version = $1", future); err != nil {
		t.Fatalf("bump version error = %v", err)
	}
	newer, err := postgres.NewStore(ctx, postgres.Config{DSN: dsn})
	if !errors.Is(err, postgres.ErrSchemaVersionMismatch) {
		t.Errorf("NewStore() err = %v, want ErrSchemaVersionMismatch", err)
	}
	if newer != nil {
		t.Error("NewStore() returned a non-nil Store for an incompatible schema — must fail closed")
	}
}

// TestDifferentKeysDoNotSerializeGlobally is §20: distinct keys are
// distinct rows, so concurrent observations for different actors must
// not contend on a shared lock. Asserted on correctness (full isolation
// under concurrency) rather than on timing, which would be flaky.
func TestDifferentKeysDoNotSerializeGlobally(t *testing.T) {
	dsn := requireDSN(t)
	s, dsn := newStore(t, dsn)
	ctx := context.Background()
	fp := testFingerprint()

	const (
		keys             = 12
		observationsEach = 8
	)

	var wg sync.WaitGroup
	errs := make(chan error, keys)
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := baseline.Key{ActorID: fmt.Sprintf("parallel-actor-%d", i), Environment: "production"}
			now := testTime
			for range observationsEach {
				if _, _, err := s.Observe(ctx, key, fp, features.VolatileFeatures{}, now); err != nil {
					errs <- err
					return
				}
				now = now.Add(time.Second)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Observe() error = %v", err)
	}

	for i := range keys {
		key := baseline.Key{ActorID: fmt.Sprintf("parallel-actor-%d", i), Environment: "production"}
		bl, ok := s.Get(ctx, key)
		if !ok {
			t.Fatalf("Get(%s) ok = false", key.ActorID)
		}
		if got := bl.Fingerprints[fp.ID].Count; got != observationsEach {
			t.Errorf("%s: Count = %d, want %d", key.ActorID, got, observationsEach)
		}
	}
}
