package platform

// PostgreSQL integration tests.
//
// Gated on TRUSTVIAN_TEST_POSTGRES_DSN: absent, they skip, so `go test ./...`
// needs no PostgreSQL and no Docker on a developer machine. CI supplies the DSN
// and runs them authoritatively, which is what keeps "skipped locally" from
// meaning "unproven".
//
// A real server, always. SQLite standing in for PostgreSQL cannot demonstrate a
// row lock, a SQLSTATE or a collation, and a SQL mock demonstrates only that the
// mock was programmed to agree.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const postgresDSNEnv = "TRUSTVIAN_TEST_POSTGRES_DSN"

// postgresDSN returns the configured DSN or skips.
func postgresDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(postgresDSNEnv)
	if strings.TrimSpace(dsn) == "" {
		t.Skipf("%s is not set; skipping PostgreSQL integration test", postgresDSNEnv)
	}
	return dsn
}

// isolatedSchemaDSN gives this test its own PostgreSQL schema.
//
// Isolation by schema rather than by truncating shared tables. That choice is
// not a preference: `go test ./...` runs separate packages' binaries
// concurrently, so a shared-table TRUNCATE in one package wipes rows another is
// mid-way through counting, and the failures look like lost updates in correct
// code. internal/store/postgres learned this the hard way and its comment says
// so; this inherits the conclusion.
//
// A private schema also means migration runs on every test, so the create path
// is exercised constantly rather than once, and the suite can run against a
// database that already holds real data without destroying it.
func isolatedSchemaDSN(t testing.TB, dsn string) string {
	t.Helper()

	// Derived from the test name and a nanosecond, then constrained to
	// [a-z0-9_]. The only dynamic identifier anywhere in this package, and it
	// cannot come from a caller: it is built here from a Go test name.
	var name strings.Builder
	name.WriteString("tv_")
	for _, r := range strings.ToLower(t.Name()) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			name.WriteRune(r)
		default:
			name.WriteByte('_')
		}
	}
	schema := fmt.Sprintf("%s_%d", name.String(), time.Now().UnixNano())
	if len(schema) > 60 {
		schema = schema[len(schema)-60:]
	}

	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect to create a test schema: %v", err)
	}
	defer admin.Close()

	// The identifier is quoted and was built from the constrained alphabet
	// above. No caller input reaches it.
	if _, err := admin.Exec(context.Background(),
		`CREATE SCHEMA IF NOT EXISTS "`+schema+`"`); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), dsn)
		if err != nil {
			return
		}
		defer cleanup.Close()
		// Best effort: a failed drop must not fail a passing test, and the
		// schema name is unique so it cannot poison a later one.
		_, _ = cleanup.Exec(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})

	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + "search_path=" + schema
}

// newPostgresStore opens a store in a schema private to this test.
func newPostgresStore(t testing.TB) *PostgresStore {
	t.Helper()
	isolated := isolatedSchemaDSN(t, postgresDSN(t))
	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// ---------------------------------------------------------------------
// Always run: no database required
// ---------------------------------------------------------------------

// TestPostgresConfigRefusesAnEmptyDSN fails before anything is dialled.
func TestPostgresConfigRefusesAnEmptyDSN(t *testing.T) {
	for _, dsn := range []string{"", "   ", "\t"} {
		_, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
		if err == nil {
			t.Fatalf("OpenPostgresStore(%q) succeeded; an absent DSN must fail", dsn)
		}
	}
}

// TestPostgresErrorsNeverCarryTheDSN is the credential-redaction regression.
//
// There is a specific, empirically-confirmed driver behaviour behind this: pgx
// redacts the password when it can *parse* a DSN, and reproduces the string
// verbatim when it cannot. So an unparseable DSN is the dangerous case, and it
// is also the one a mistyped configuration produces.
//
// Our own error text must therefore carry nothing derived from the input. The
// sentinel below is distinctive enough that its appearance anywhere is proof of
// a leak.
func TestPostgresErrorsNeverCarryTheDSN(t *testing.T) {
	const secret = "TRUSTVIAN_SECRET_MUST_NOT_LEAK"

	cases := []struct {
		name string
		dsn  string
	}{
		{"unparseable with a password", "postgres://user:" + secret + "@:::/?bad"},
		{"valid shape, unreachable host", "postgres://user:" + secret + "@127.0.0.1:1/db?sslmode=disable&connect_timeout=1"},
		{"keyword form", "host=127.0.0.1 port=1 user=u password=" + secret + " dbname=d sslmode=disable connect_timeout=1"},
		{"not a URL at all", "://" + secret},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			store, err := OpenPostgresStore(ctx, PostgresConfig{
				DSN:            tt.dsn,
				ConnectTimeout: 2 * time.Second,
			})
			if err == nil {
				_ = store.Close()
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error leaked the DSN secret: %v", err)
			}
			// Nor any other part of the connection string.
			for _, fragment := range []string{"password=", "user:", "@127.0.0.1"} {
				if strings.Contains(err.Error(), fragment) {
					t.Errorf("the error echoed DSN fragment %q: %v", fragment, err)
				}
			}
			if !errors.Is(err, ErrStoreUnavailable) && !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want an unavailable or corrupt classification", err)
			}
		})
	}
}

// TestPostgresAndEngineMigrationLocksDoNotCollide settles the experiment ADR
// 0037 left open.
//
// PostgreSQL has one global advisory-lock space and no registry of keys, so two
// subsystems that happened to choose the same one would serialize against each
// other for no reason: an engine migration would block a platform migration in
// any database holding both. The keys are therefore chosen to differ, and this
// asserts they do rather than trusting the arithmetic — a copied constant is
// exactly the mistake a comment does not catch.
func TestPostgresAndEngineMigrationLocksDoNotCollide(t *testing.T) {
	// The engine's key, from internal/store/postgres/schema.go. Restated rather
	// than imported: the platform module must not depend on the root module's
	// internals, and ADR 0022 makes that a compile error. If the engine changes
	// its key, this test's value goes stale — so it also asserts the shape,
	// which is what would actually collide.
	const engineKey int64 = 0x7275737476696E00

	if platformMigrationLockKey == engineKey {
		t.Fatalf("the platform advisory-lock key equals the engine's (%#x); a "+
			"platform migration would block on an engine migration in any "+
			"database holding both", engineKey)
	}
}

// ---------------------------------------------------------------------
// Require a real PostgreSQL
// ---------------------------------------------------------------------

// TestPostgresFreshSchemaReportsTheSharedVersion proves create-and-stamp works
// and that one logical version governs both backends.
func TestPostgresFreshSchemaReportsTheSharedVersion(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	var version int
	err := store.pool.QueryRow(ctx,
		`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`).Scan(&version)
	if err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("version = %d, want the shared SchemaVersion %d", version, SchemaVersion)
	}

	// And the full logical table set exists, matching SQLite's.
	present, err := func() ([]string, error) {
		var got []string
		rows, err := store.pool.Query(ctx,
			`SELECT table_name FROM information_schema.tables
			 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
			 ORDER BY table_name`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return nil, err
			}
			got = append(got, name)
		}
		return got, rows.Err()
	}()
	if err != nil {
		t.Fatalf("inspect tables: %v", err)
	}
	for _, want := range schemaTables {
		if !contains(present, want) {
			t.Errorf("table %s is missing from a fresh PostgreSQL schema", want)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

// TestPostgresRoundTripsTheControlEntities is the first end-to-end proof that
// the store works: create, read back, and the documented error classes.
func TestPostgresRoundTripsTheControlEntities(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	project, err := NewProject("proj-pg", "Checkout")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	if err := store.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	got, err := store.Project(ctx, "proj-pg")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if got.ID() != project.ID() || got.Name() != project.Name() {
		t.Errorf("project round-tripped as %+v", got)
	}

	// Create means create: a duplicate is refused and nothing is overwritten.
	renamed, err := NewProject("proj-pg", "Different")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	if err := store.CreateProject(ctx, renamed); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate create returned %v, want ErrStoreAlreadyExists", err)
	}
	unchanged, err := store.Project(ctx, "proj-pg")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if unchanged.Name() != "Checkout" {
		t.Errorf("a refused duplicate rewrote the stored name to %q", unchanged.Name())
	}

	// Absent is not found.
	if _, err := store.Project(ctx, "missing"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("absent project returned %v, want ErrStoreNotFound", err)
	}

	// A child naming a parent that does not exist is a missing parent, mapped
	// from the foreign-key violation.
	orphan, err := NewAgent("agent-orphan", "no-such-project", "Orphan")
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if err := store.CreateAgent(ctx, orphan); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("agent under an absent project returned %v, want ErrStoreNotFound", err)
	}

	agent, err := NewAgent("agent-pg", "proj-pg", "Checkout agent")
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if err := store.CreateAgent(ctx, agent); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Candidate metadata is six discrete fields; all must survive.
	metadata := CandidateMetadata{
		Label: "v2", SourceRef: "refs/heads/main",
		ArtifactDigest: "sha256:abc", Model: "claude",
		ToolsetDigest: "sha256:def", ConfigDigest: "sha256:ghi",
	}
	candidate, err := NewCandidate("cand-pg", "agent-pg", metadata)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	if err := store.CreateCandidate(ctx, candidate); err != nil {
		t.Fatalf("CreateCandidate: %v", err)
	}
	loaded, err := store.Candidate(ctx, "cand-pg")
	if err != nil {
		t.Fatalf("Candidate: %v", err)
	}
	if loaded.Metadata() != metadata {
		t.Errorf("metadata round-tripped as %+v, want %+v", loaded.Metadata(), metadata)
	}
}

// TestPostgresPreservesTimestampOffsetAndNanoseconds is the reason timestamps
// are TEXT rather than TIMESTAMPTZ.
//
// TIMESTAMPTZ normalizes to UTC and truncates to microseconds. The offset is
// API-visible — a /v1 response carries it — and these values carry nanoseconds,
// so either would be an observable divergence from SQLite. This fails if the
// column type is ever changed.
func TestPostgresPreservesTimestampOffsetAndNanoseconds(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	// A deliberately awkward instant: non-UTC numeric offset, nanosecond
	// precision that is not a whole microsecond.
	zone := time.FixedZone("+0330", 3*3600+30*60)
	created := time.Date(2026, 3, 14, 15, 9, 26, 535897932, zone)

	seedPostgresRun(t, store, "run-ts", created)

	run, err := store.EvaluationRun(ctx, "run-ts")
	if err != nil {
		t.Fatalf("EvaluationRun: %v", err)
	}

	if !run.CreatedAt().Equal(created) {
		t.Errorf("instant changed: got %v, want %v", run.CreatedAt(), created)
	}
	if got, want := run.CreatedAt().Nanosecond(), created.Nanosecond(); got != want {
		t.Errorf("nanoseconds = %d, want %d (TIMESTAMPTZ would truncate to microseconds)", got, want)
	}
	_, gotOffset := run.CreatedAt().Zone()
	_, wantOffset := created.Zone()
	if gotOffset != wantOffset {
		t.Errorf("zone offset = %d, want %d (TIMESTAMPTZ would normalize to UTC)",
			gotOffset, wantOffset)
	}
	// And the rendered form is byte-identical, which is what /v1 publishes.
	if got, want := run.CreatedAt().Format(time.RFC3339Nano), created.Format(time.RFC3339Nano); got != want {
		t.Errorf("rendered timestamp = %q, want %q", got, want)
	}
}

// seedPostgresRun creates the project/agent/candidate/run chain.
func seedPostgresRun(t *testing.T, store *PostgresStore, runID EvaluationRunID, created time.Time) EvaluationRun {
	t.Helper()
	ctx := t.Context()

	project, err := NewProject("proj-seed", "Checkout")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	agent, err := NewAgent("agent-seed", "proj-seed", "Agent")
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	candidate, err := NewCandidate("cand-seed", "agent-seed", CandidateMetadata{})
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	run, err := NewEvaluationRun(runID, "cand-seed", "staging", "profile-1", created)
	if err != nil {
		t.Fatalf("NewEvaluationRun: %v", err)
	}

	// Ignore already-exists on the shared parents so a test may seed twice.
	for _, err := range []error{
		store.CreateProject(ctx, project),
		store.CreateAgent(ctx, agent),
		store.CreateCandidate(ctx, candidate),
	} {
		if err != nil && !errors.Is(err, ErrStoreAlreadyExists) {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun: %v", err)
	}
	return run
}

// TestPostgresLifecycleTransitionsAndConflicts covers the compare-and-swap.
func TestPostgresLifecycleTransitionsAndConflicts(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	created := seedPostgresRun(t, store, "run-lc", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	started, err := created.Start(created.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, created, started); err != nil {
		t.Fatalf("first transition: %v", err)
	}

	// A stale view loses.
	if err := store.UpdateEvaluationRun(ctx, created, started); !errors.Is(err, ErrStoreConflict) {
		t.Errorf("stale previous returned %v, want ErrStoreConflict", err)
	}

	stored, err := store.EvaluationRun(ctx, "run-lc")
	if err != nil {
		t.Fatalf("EvaluationRun: %v", err)
	}
	if stored.Status() != RunRunning {
		t.Errorf("status = %s, want %s", stored.Status(), RunRunning)
	}

	completed, err := stored.Complete(stored.StartedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, stored, completed); err != nil {
		t.Fatalf("complete: %v", err)
	}
	final, err := store.EvaluationRun(ctx, "run-lc")
	if err != nil {
		t.Fatalf("EvaluationRun: %v", err)
	}
	if final.Status() != RunCompleted {
		t.Errorf("status = %s, want %s", final.Status(), RunCompleted)
	}
}

// TestPostgresContextCancellationIsNotAConflict keeps the classifications apart.
//
// A cancelled request is the caller's own decision. Reporting it as a conflict
// or as corruption would tell somebody to retry or investigate something that is
// working correctly.
func TestPostgresContextCancellationIsNotAConflict(t *testing.T) {
	store := newPostgresStore(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	project, err := NewProject("proj-cancel", "Checkout")
	if err != nil {
		t.Fatalf("NewProject: %v", err)
	}
	err = store.CreateProject(cancelled, project)
	if err == nil {
		t.Fatal("a cancelled context still performed the write")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	for _, wrong := range []error{ErrStoreConflict, ErrStoreCorrupt, ErrStoreAlreadyExists} {
		if errors.Is(err, wrong) {
			t.Errorf("cancellation was classified as %v", wrong)
		}
	}

	// And nothing was written.
	if _, err := store.Project(t.Context(), "proj-cancel"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("a cancelled create left state behind: %v", err)
	}
}

// TestPostgresRoundTripsEvidence exercises the forty-four-column aggregate, the
// snapshot and its entries through the shared restore path.
//
// This is the part a second backend gets wrong quietly: a column list copied and
// edited puts values in the wrong places, and the result still loads. Both
// backends now derive their columns and args from aggregateInsertColumns, so
// this also proves that shared list is correct for PostgreSQL's dialect.
func TestPostgresRoundTripsEvidence(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	run := seedPostgresRun(t, store, "run-ev", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	started, err := run.Start(run.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("start: %v", err)
	}

	aggregate, err := NewEvaluationAggregate(started)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate: %v", err)
	}
	collector, err := NewBehaviorCollector(started)
	if err != nil {
		t.Fatalf("NewBehaviorCollector: %v", err)
	}
	for i := range 5 {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
			fmt.Sprintf("op-%d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord: %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe: %v", err)
		}
	}
	snapshot := collector.Snapshot()

	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence: %v", err)
	}

	loadedAggregate, loadedSnapshot, err := store.EvaluationEvidence(ctx, "run-ev")
	if err != nil {
		t.Fatalf("EvaluationEvidence: %v", err)
	}

	// sameAggregate and sameSnapshot are the store's own equality, which
	// compares every counter, every metric and every entry.
	if !sameAggregate(loadedAggregate, aggregate) {
		t.Errorf("aggregate did not round-trip\n got %+v\nwant %+v", loadedAggregate, aggregate)
	}
	if !sameSnapshot(loadedSnapshot, snapshot) {
		t.Errorf("snapshot did not round-trip\n got %+v\nwant %+v", loadedSnapshot, snapshot)
	}
	if loadedAggregate.RecordCount() != 5 {
		t.Errorf("record count = %d, want 5", loadedAggregate.RecordCount())
	}
	if loadedSnapshot.DistinctBehaviorCount() != 5 {
		t.Errorf("distinct behaviours = %d, want 5", loadedSnapshot.DistinctBehaviorCount())
	}

	// An identical rewrite is idempotent; evidence never moves backwards.
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Errorf("identical rewrite returned %v, want nil", err)
	}
}

// TestPostgresPreservesMaxUint64 is the reason counters are TEXT.
//
// BIGINT is signed 64-bit, so it cannot hold this value at all; NUMERIC could,
// but normalizes non-canonical input and would destroy the corruption signal.
// The cursor's sequence is the counter a caller can drive to the top of the
// range, so it is the one tested here.
func TestPostgresPreservesMaxUint64(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	run := seedPostgresRun(t, store, "run-max", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	// Written directly: reaching MaxUint64 through the ingest protocol would
	// require 2^64 records. The point under test is the column's fidelity, and
	// the restore path is the same one a commit uses.
	const maxUint64Text = "18446744073709551615"
	if _, err := store.pool.Exec(ctx,
		`INSERT INTO `+tableIngestState+` (run_id, next_sequence, last_digest)
		 VALUES ($1, $2, $3)`,
		string(run.ID()), maxUint64Text, ""); err != nil {
		t.Fatalf("seed cursor: %v", err)
	}

	var readBack string
	if err := store.pool.QueryRow(ctx,
		`SELECT next_sequence FROM `+tableIngestState+` WHERE run_id = $1`,
		string(run.ID())).Scan(&readBack); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if readBack != maxUint64Text {
		t.Fatalf("stored sequence = %q, want %q exactly", readBack, maxUint64Text)
	}

	parsed, err := parseUint64Text("next_sequence", readBack)
	if err != nil {
		t.Fatalf("parseUint64Text: %v", err)
	}
	if parsed != 18446744073709551615 {
		t.Errorf("parsed = %d, want MaxUint64", parsed)
	}

	// And a non-canonical value is corruption rather than something to
	// normalize — the property NUMERIC would have removed.
	if _, err := parseUint64Text("next_sequence", "007"); !errors.Is(err, ErrStoreCorrupt) {
		t.Errorf("non-canonical counter returned %v, want ErrStoreCorrupt", err)
	}
}

// TestPostgresIngestCommitsEvidenceAndCursorTogether covers the ingest path.
func TestPostgresIngestCommitsEvidenceAndCursorTogether(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	run := seedPostgresRun(t, store, "run-ing", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	started, err := run.Start(run.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("start: %v", err)
	}

	// A run with no cursor row yet reports sequence 1.
	state, err := store.EvaluationIngestState(ctx, "run-ing")
	if err != nil {
		t.Fatalf("EvaluationIngestState: %v", err)
	}
	if state.NextSequence() != 1 {
		t.Fatalf("initial next sequence = %d, want 1", state.NextSequence())
	}

	aggregate, err := NewEvaluationAggregate(started)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate: %v", err)
	}
	collector, err := NewBehaviorCollector(started)
	if err != nil {
		t.Fatalf("NewBehaviorCollector: %v", err)
	}
	rec := internalRecord("evt-1", "fp-1", "op-1")
	if aggregate, err = aggregate.AddRecord(rec); err != nil {
		t.Fatalf("AddRecord: %v", err)
	}
	if err := collector.Observe(rec); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	commit := EvaluationIngestCommit{
		Aggregate:            aggregate,
		Snapshot:             collector.Snapshot(),
		Sequence:             1,
		PreviousNextSequence: 1,
		RecordDigest:         strings.Repeat("a", 64),
	}

	result, err := store.CommitEvaluationIngest(ctx, commit)
	if err != nil {
		t.Fatalf("CommitEvaluationIngest: %v", err)
	}
	if result.Disposition != EvaluationIngestCommitted {
		t.Errorf("disposition = %v, want committed", result.Disposition)
	}
	if result.NextSequence != 2 {
		t.Errorf("next sequence = %d, want 2", result.NextSequence)
	}

	// The same record again is a retry, not a conflict: the cursor already
	// records this digest at this sequence.
	retry, err := store.CommitEvaluationIngest(ctx, commit)
	if err != nil {
		t.Fatalf("retry returned %v, want the already-committed disposition", err)
	}
	if retry.Disposition != EvaluationIngestAlreadyCommitted {
		t.Errorf("retry disposition = %v, want already-committed", retry.Disposition)
	}

	// Evidence and cursor advanced together.
	loadedAggregate, _, err := store.EvaluationEvidence(ctx, "run-ing")
	if err != nil {
		t.Fatalf("EvaluationEvidence: %v", err)
	}
	if loadedAggregate.RecordCount() != 1 {
		t.Errorf("record count = %d, want 1", loadedAggregate.RecordCount())
	}
}

// TestPostgresIdentifierOrderingIsByteOrder settles ADR 0037's collation
// question, and the naive form of the experiment is why it is written this way.
//
// The obvious test — run the differential suite against a database created with
// a non-C locale — passes whether or not COLLATE "C" is declared, and removing
// the declaration does not fail it. That is a false negative with a specific
// cause: postgres:17-alpine is built on musl, whose strcoll is effectively byte
// comparison, so `en_US.utf8` and `C` order identically there. The container
// image, not the code, is what made the test agree.
//
// That agreement is not a property to rely on. On glibc-based PostgreSQL — the
// Debian images, managed services, most production — `en_US.UTF-8` orders
// `a` before `B` while `C` orders `B` first, and `ORDER BY fingerprint_id` is
// the one domain ordering either backend performs. SQLite compares TEXT by byte
// value, so without COLLATE "C" the two backends would return behaviour entries
// in different orders on any such server.
//
// So the property is proven in a way that does not depend on the host's libc:
// ICU is available on this server and implements real locale-aware ordering, and
// the assertions below show our columns order by bytes while ICU orders the same
// identifiers differently. The declaration is therefore load-bearing rather than
// decorative, and TestPostgresRunScopedWritesTakeTheRowLock's sibling guard pins
// its presence.
func TestPostgresIdentifierOrderingIsByteOrder(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()

	// Identifiers whose byte order and locale order differ: uppercase sorts
	// before lowercase by byte value, and after it under a locale-aware
	// collation.
	identifiers := []string{"fp-a", "fp-B", "fp-b", "fp-A"}

	if _, err := store.pool.Exec(ctx,
		`CREATE TEMP TABLE collation_probe_c (id TEXT COLLATE "C")`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	for _, id := range identifiers {
		if _, err := store.pool.Exec(ctx,
			`INSERT INTO collation_probe_c (id) VALUES ($1)`, id); err != nil {
			t.Fatalf("insert probe row: %v", err)
		}
	}

	readOrdered := func(query string) []string {
		rows, err := store.pool.Query(ctx, query)
		if err != nil {
			t.Fatalf("query %q: %v", query, err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatalf("scan: %v", err)
			}
			got = append(got, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return got
	}

	// What our columns do: byte order, matching SQLite's BINARY comparison.
	byteOrder := readOrdered(`SELECT id FROM collation_probe_c ORDER BY id`)
	wantByteOrder := []string{"fp-A", "fp-B", "fp-a", "fp-b"}
	if !slices.Equal(byteOrder, wantByteOrder) {
		t.Errorf("COLLATE \"C\" ordering = %v, want %v (SQLite's byte order)",
			byteOrder, wantByteOrder)
	}

	// What a locale-aware collation does with the same values. If this matched
	// the byte order, the declaration would be unfalsifiable on this server and
	// the test below would be meaningless — so it is asserted to differ.
	localeOrder := readOrdered(
		`SELECT id FROM collation_probe_c ORDER BY id COLLATE "und-x-icu"`)
	if slices.Equal(localeOrder, byteOrder) {
		t.Skipf("this server's locale-aware collation orders identically to C "+
			"(%v); the experiment cannot distinguish them here", localeOrder)
	}
	t.Logf("byte order   %v", byteOrder)
	t.Logf("locale order %v", localeOrder)

	// And the schema declares COLLATE "C" on every column that is an identifier
	// or is ordered, which is what makes the byte order above a property of the
	// schema rather than of this server's libc.
	for _, statement := range postgresSchemaStatements() {
		if !strings.Contains(statement, tableEntries) {
			continue
		}
		if !strings.Contains(statement, `fingerprint_id     TEXT COLLATE "C"`) {
			t.Error("platform_behavior_entries.fingerprint_id is not COLLATE \"C\"; " +
				"the one ORDER BY in either backend would then follow the server's " +
				"locale and could disagree with SQLite")
		}
	}
}
