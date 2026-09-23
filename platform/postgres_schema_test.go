package platform

// PostgreSQL schema lifecycle.
//
// Same policy as SQLite's, and the point of testing it separately is that the
// mechanisms differ: transactional DDL instead of a single connection,
// information_schema instead of sqlite_master, and an advisory lock instead of
// busy_timeout. The policy those mechanisms implement must come out identical.
//
// A newer schema fails closed. Tables without a version row are refused rather
// than adopted, because the safe reading of that state is "something else wrote
// here", not "empty".

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaTestPool opens a raw pool on an isolated schema, for tests that need to
// arrange a schema state before a store ever sees it.
func schemaTestPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := isolatedSchemaDSN(t, postgresDSN(t))
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// createV1Schema builds a task 057 schema: every table except the ingest cursor,
// stamped version 1.
//
// Assembled from the same statements the store uses, minus the last one, so this
// cannot drift from what v1 actually was.
func createV1Schema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	statements := postgresSchemaStatements()
	v1 := statements[:len(statements)-1]
	// Guard the assumption: the omitted statement must be the cursor table.
	if !strings.Contains(statements[len(statements)-1], tableIngestState) {
		t.Fatalf("the last schema statement is no longer %s; createV1Schema would "+
			"build the wrong version", tableIngestState)
	}

	for _, stmt := range v1 {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("create v1 schema: %v", err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO `+tableSchemaVersion+` (id, version) VALUES (1, $1)`,
		schemaVersionV1); err != nil {
		t.Fatalf("stamp v1: %v", err)
	}
}

func storedVersion(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var version int
	if err := pool.QueryRow(context.Background(),
		`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	return version
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = $1)`,
		name).Scan(&exists); err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return exists
}

// TestPostgresOpensAnExistingCurrentSchema is the ordinary restart path.
func TestPostgresOpensAnExistingCurrentSchema(t *testing.T) {
	dsn := isolatedSchemaDSN(t, postgresDSN(t))

	first, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// State written by the first opener must be there for the second.
	if err := first.CreateProject(t.Context(), mustProject(t, "proj-restart", "Checkout")); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("second open on an existing current schema: %v", err)
	}
	defer second.Close()

	got, err := second.Project(t.Context(), "proj-restart")
	if err != nil {
		t.Fatalf("state did not survive the reopen: %v", err)
	}
	if got.Name() != "Checkout" {
		t.Errorf("Name() = %q, want Checkout", got.Name())
	}
}

// TestPostgresMigratesV1ToCurrent covers the supported older schema.
func TestPostgresMigratesV1ToCurrent(t *testing.T) {
	pool, dsn := schemaTestPool(t)
	createV1Schema(t, pool)

	if tableExists(t, pool, tableIngestState) {
		t.Fatal("the v1 fixture already has the cursor table")
	}
	if got := storedVersion(t, pool); got != schemaVersionV1 {
		t.Fatalf("fixture version = %d, want %d", got, schemaVersionV1)
	}

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v1 schema: %v", err)
	}
	defer store.Close()

	if got := storedVersion(t, pool); got != SchemaVersion {
		t.Errorf("version after migration = %d, want %d", got, SchemaVersion)
	}
	if !tableExists(t, pool, tableIngestState) {
		t.Error("the migration did not add the cursor table")
	}

	// And the migrated database works: a run seeded here reports a cursor
	// derived from its evidence, which is the edge the v1 → v2 migration exists
	// to handle.
	running := startedRun(t, store, "run-migrated")
	state, err := store.EvaluationIngestState(t.Context(), running.ID())
	if err != nil {
		t.Fatalf("EvaluationIngestState after migration: %v", err)
	}
	if state.NextSequence() != 1 {
		t.Errorf("NextSequence() = %d, want 1", state.NextSequence())
	}
}

// TestPostgresRefusesANewerSchema is the fail-closed case.
//
// A schema this binary does not understand is never rewritten, never partially
// adopted and never downgraded. Restoring an older schema is a restore
// operation, which is task 071's subject.
func TestPostgresRefusesANewerSchema(t *testing.T) {
	pool, dsn := schemaTestPool(t)

	// Create the current schema through the store, then move the stamp forward.
	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		SchemaVersion+1); err != nil {
		t.Fatalf("stamp a newer version: %v", err)
	}

	reopened, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err == nil {
		_ = reopened.Close()
		t.Fatal("a newer schema was accepted; it must fail closed")
	}
	if !errors.Is(err, ErrStoreSchemaVersion) {
		t.Errorf("error = %v, want ErrStoreSchemaVersion", err)
	}
	// The refusal must not have rewritten the stamp.
	if got := storedVersion(t, pool); got != SchemaVersion+1 {
		t.Errorf("version = %d after a refusal, want %d left untouched",
			got, SchemaVersion+1)
	}
}

// TestPostgresRefusesTablesWithoutAVersionRow is the ambiguous state.
//
// Recognized tables with no version metadata could mean an interrupted create,
// or it could mean something else owns this schema. Adopting it would be a guess
// with a database's contents at stake, so it is refused.
func TestPostgresRefusesTablesWithoutAVersionRow(t *testing.T) {
	pool, dsn := schemaTestPool(t)

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM `+tableSchemaVersion+` WHERE id = 1`); err != nil {
		t.Fatalf("remove the version row: %v", err)
	}

	reopened, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err == nil {
		_ = reopened.Close()
		t.Fatal("a schema with no version row was adopted")
	}
	if !errors.Is(err, ErrStoreSchemaVersion) {
		t.Errorf("error = %v, want ErrStoreSchemaVersion", err)
	}
}

// TestPostgresRefusesAPartialSchema covers a recognized subset that is neither
// version.
func TestPostgresRefusesAPartialSchema(t *testing.T) {
	pool, dsn := schemaTestPool(t)

	// Two of the nine tables: enough to be recognized, not enough to be any
	// version this binary knows.
	for _, stmt := range postgresSchemaStatements() {
		if strings.Contains(stmt, tableSchemaVersion) || strings.Contains(stmt, tableProjects) {
			if _, err := pool.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("create partial schema: %v", err)
			}
		}
	}

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err == nil {
		_ = store.Close()
		t.Fatal("a partial schema was accepted")
	}
	if !errors.Is(err, ErrStoreSchemaVersion) {
		t.Errorf("error = %v, want ErrStoreSchemaVersion", err)
	}
}

// TestPostgresCreateAndStampAreAtomic proves the property transactional DDL buys.
//
// The state migrate refuses to adopt — tables present, version absent — must be
// unreachable through a failed create rather than merely unlikely. Both halves
// commit together or neither does.
func TestPostgresCreateAndStampAreAtomic(t *testing.T) {
	pool, dsn := schemaTestPool(t)

	// Pre-create one table the store's create will collide with, so createSchema
	// fails partway through its own transaction.
	//
	// platform_projects specifically: it has no foreign key, so it can exist on
	// its own, and it is created after the version table — which is what makes
	// the rollback observable. A table with a foreign key could not be
	// pre-created in isolation.
	for _, stmt := range postgresSchemaStatements() {
		if strings.Contains(stmt, "CREATE TABLE "+tableProjects+" ") {
			if _, err := pool.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("pre-create a table: %v", err)
			}
			break
		}
	}

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err == nil {
		_ = store.Close()
		t.Fatal("the store accepted a schema it collided with")
	}

	// The failed create must have rolled back everything it did: no version
	// table, and specifically not the ambiguous tables-without-version state.
	if tableExists(t, pool, tableSchemaVersion) {
		t.Error("a failed create left the version table behind; transactional DDL " +
			"is what prevents the ambiguous state migrate refuses to adopt")
	}
	// platform_projects was pre-created by this test, so its presence proves
	// nothing. A table the store would have created *after* the collision is
	// what must be absent.
	if tableExists(t, pool, tableAgents) {
		t.Error("a failed create left platform_agents behind")
	}
}

// TestPostgresConcurrentInitializationYieldsOneSchema is what the advisory lock
// is for.
//
// Several processes starting against one empty database is the ordinary
// deployment case, not an edge. Without serialization they all read "empty" and
// all try to create, and the losers fail on duplicate tables for no reason the
// operator can act on.
func TestPostgresConcurrentInitializationYieldsOneSchema(t *testing.T) {
	pool, dsn := schemaTestPool(t)

	const openers = 6
	stores := make([]*PostgresStore, openers)
	errs := raceStart(openers, func(worker int) error {
		store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
		stores[worker] = store
		return err
	})
	for _, store := range stores {
		if store != nil {
			t.Cleanup(func() { _ = store.Close() })
		}
	}

	// Every opener must succeed. One winning and five reporting "table already
	// exists" would be a usable database and an alarming startup log.
	for worker, err := range errs {
		if err != nil {
			t.Errorf("opener %d failed: %v", worker, err)
		}
	}

	// One schema, one version, complete.
	if got := storedVersion(t, pool); got != SchemaVersion {
		t.Errorf("version = %d, want %d", got, SchemaVersion)
	}
	var versionRows int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+tableSchemaVersion).Scan(&versionRows); err != nil {
		t.Fatalf("count version rows: %v", err)
	}
	if versionRows != 1 {
		t.Errorf("version table holds %d rows, want exactly 1", versionRows)
	}
	for _, table := range schemaTables {
		if !tableExists(t, pool, table) {
			t.Errorf("table %s is missing after concurrent initialization", table)
		}
	}

	// And every opener got a working store, not a half-initialized one.
	for worker, store := range stores {
		if store == nil {
			continue
		}
		id := ProjectID(fmt.Sprintf("proj-opener-%d", worker))
		if err := store.CreateProject(t.Context(), mustProject(t, id, "Checkout")); err != nil {
			t.Errorf("opener %d cannot use its store: %v", worker, err)
		}
	}
}

// TestPostgresMigrationDoesNotBlockOnTheEngineLock settles ADR 0037's second
// open experiment, against a real lock rather than by comparing two constants.
//
// PostgreSQL has one global advisory-lock space and no registry of keys. If the
// platform and the engine had chosen the same one, a platform migration would
// wait for an engine migration — in any database holding both, which is exactly
// the deployment this backend exists for. TestPostgresAndEngineMigrationLocks-
// DoNotCollide asserts the constants differ; this asserts the consequence.
//
// The engine's key is taken and held here directly, rather than by importing
// internal/store/postgres — the platform module must not reach into the root
// module's internals, and ADR 0022 makes that a compile error.
func TestPostgresMigrationDoesNotBlockOnTheEngineLock(t *testing.T) {
	// The engine's key, from internal/store/postgres/schema.go.
	const engineKey int64 = 0x7275737476696E00

	dsn := isolatedSchemaDSN(t, postgresDSN(t))

	// A separate connection holds the engine's lock for the whole test.
	holder, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	defer holder.Close()

	held, err := holder.Begin(context.Background())
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	defer func() { _ = held.Rollback(context.Background()) }()

	if _, err := held.Exec(context.Background(),
		`SELECT pg_advisory_xact_lock($1)`, engineKey); err != nil {
		t.Fatalf("take the engine lock: %v", err)
	}

	// With the engine's lock held, a platform migration must still complete.
	// Bounded so a collision shows up as a timeout rather than a hung test.
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
		if store != nil {
			_ = store.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the platform migration failed while the engine lock was held: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the platform migration blocked while the engine's advisory lock "+
			"(%#x) was held. The two subsystems are sharing a key, so an engine "+
			"migration would stall a platform migration in any database holding "+
			"both.", engineKey)
	}

	// And the control: the platform's own key does block a second platform
	// migration, which proves the lock is real rather than absent.
	if _, err := held.Exec(context.Background(),
		`SELECT pg_advisory_xact_lock($1)`, platformMigrationLockKey); err != nil {
		t.Fatalf("take the platform lock: %v", err)
	}

	// A second, distinct schema, created before the goroutine starts so the
	// blocking is the migration's and not the schema creation's.
	secondSchema := isolatedSchemaDSN(t, postgresDSN(t))

	blocked := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// A fresh schema, so this open must run a migration and therefore must
		// take the lock.
		store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: secondSchema})
		if store != nil {
			_ = store.Close()
		}
		blocked <- err
	}()

	select {
	case err := <-blocked:
		if err == nil {
			t.Error("a second migration completed while the platform's own advisory " +
				"lock was held; the lock is not being taken")
		}
	case <-time.After(6 * time.Second):
		// Blocking is the expected outcome; the goroutine's own context bounds it.
	}

}
