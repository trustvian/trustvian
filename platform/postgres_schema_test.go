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

// createsTable reports whether stmt is the CREATE TABLE for exactly this
// table, rather than merely a statement mentioning it.
//
// Containment is not enough: every child table names its parent in a REFERENCES
// clause, so matching on the table name alone selects statements that create
// something else entirely and depend on tables the fixture never created.
func createsTable(stmt, table string) bool {
	return strings.HasPrefix(strings.TrimSpace(stmt), `CREATE TABLE `+table+` (`)
}

// createOlderSchema builds a historical schema by replaying the statements the
// store uses, minus the ones later versions appended, and stamping the version
// they belonged to.
//
// Assembled from the live statements rather than copied, so a fixture cannot
// drift from what that version actually was. The omitted statements are named
// and checked from the end backwards: appending a table without extending this
// would otherwise silently build the wrong past. Task 066 appended two — the
// promotions table and its index — which is exactly the check firing as
// designed rather than a fixture that quietly kept working.
func createOlderSchema(t *testing.T, pool *pgxpool.Pool, version int) {
	t.Helper()
	ctx := context.Background()

	statements := postgresSchemaStatements()
	tail := []struct {
		offset   int
		contains string
	}{
		{1, indexPromotionsByProject},
		{2, `CREATE TABLE ` + tablePromotions},
		{3, `CREATE TABLE ` + tableEnvironments},
		{4, `CREATE TABLE ` + tableIngestState},
	}
	for _, want := range tail {
		if !strings.Contains(statements[len(statements)-want.offset], want.contains) {
			t.Fatalf("schema statement %d from the end no longer contains %q; the "+
				"fixture would build the wrong version", want.offset, want.contains)
		}
	}

	// One entry per version this binary can still open, counting back over the
	// statements every later version appended.
	var older []string
	switch version {
	case schemaVersionV1:
		older = statements[:len(statements)-4]
	case schemaVersionV2:
		older = statements[:len(statements)-3]
	case schemaVersionV3:
		older = statements[:len(statements)-2]
	default:
		t.Fatalf("no fixture for schema version %d", version)
	}

	for _, stmt := range older {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("create v%d schema: %v", version, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO `+tableSchemaVersion+` (id, version) VALUES (1, $1)`,
		version); err != nil {
		t.Fatalf("stamp v%d: %v", version, err)
	}
}

// createV1Schema builds a task 057 schema: no ingest cursor and no environment
// registry, stamped version 1.
func createV1Schema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createOlderSchema(t, pool, schemaVersionV1)
}

// createV2Schema builds a task 058 schema: the ingest cursor, but no
// environment registry, stamped version 2.
func createV2Schema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createOlderSchema(t, pool, schemaVersionV2)
}

// createV3Schema builds a task 065 schema: the environment registry, but no
// promotion history, stamped version 3.
func createV3Schema(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	createOlderSchema(t, pool, schemaVersionV3)
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
	if !tableExists(t, pool, tableEnvironments) {
		t.Error("the migration did not reach v3; the environment registry is absent")
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

// seedPostgresV2Environments writes one project, one agent, one candidate and
// one run per distinct environment ref — the only way schema 2 could record
// that an environment existed.
func seedPostgresV2Environments(t *testing.T, pool *pgxpool.Pool, projectID string, refs []string) {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("seed v2: %v", err)
		}
	}
	exec(`INSERT INTO `+tableProjects+` (id, name) VALUES ($1, $2)`, projectID, "Checkout")
	exec(`INSERT INTO `+tableAgents+` (id, project_id, name) VALUES ($1, $2, $3)`,
		"agent-"+projectID, projectID, "Agent")
	exec(`INSERT INTO `+tableCandidates+`
	      (id, agent_id, label, source_ref, artifact_digest, model, toolset_digest, config_digest)
	      VALUES ($1, $2, '', '', '', '', '', '')`, "cand-"+projectID, "agent-"+projectID)

	for i, ref := range refs {
		exec(`INSERT INTO `+tableRuns+`
		      (id, candidate_id, environment, behavioral_profile, status,
		       created_at, started_at, finished_at, failure_reason)
		      VALUES ($1, $2, $3, 'profile-1', 'completed', $4, $4, $4, '')`,
			fmt.Sprintf("run-%s-%03d", projectID, i), "cand-"+projectID, ref, created)
	}
}

// listAllPostgresEnvironments pages to completion, which is the only way to
// read a project migration left above the page bound.
func listAllPostgresEnvironments(t *testing.T, store *PostgresStore, projectID ProjectID) []Environment {
	t.Helper()
	var all []Environment
	after := EnvironmentRef("")
	for {
		page, err := store.ProjectEnvironments(t.Context(), projectID, after, MaxEnvironmentPage)
		if err != nil {
			t.Fatalf("ProjectEnvironments() error = %v", err)
		}
		all = append(all, page...)
		if len(page) < MaxEnvironmentPage {
			return all
		}
		after = page[len(page)-1].Ref()
	}
}

// TestPostgresMigratesV2ToV3PreservingHistory is the SQLite migration suite's
// counterpart, and it is a separate test rather than a shared one because the
// two migrations are different code: transactional DDL and `$1` here, a single
// connection and `?` there. The policy they implement must come out identical,
// so the fixtures are the same three boundaries — 63, 64 and 65 — plus the
// case only a migrated database can reach.
//
// The cap governs creation, not existence: a valid v2 database may reference
// more environments than may now be created, and the 65 case is the one a
// "just cap it" migration would silently break.
func TestPostgresMigratesV2ToV3PreservingHistory(t *testing.T) {
	tests := []struct {
		name        string
		historical  int
		roomForMore bool
	}{
		{"below the cap", maxProjectEnvironments - 1, true},
		{"at the cap", maxProjectEnvironments, false},
		{"above the cap", maxProjectEnvironments + 1, false},
		{"far above the cap", 130, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool, dsn := schemaTestPool(t)
			createV2Schema(t, pool)
			refs := environmentRefs(tt.historical)
			seedPostgresV2Environments(t, pool, "proj-1", refs)

			store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatalf("opening a v2 schema: %v", err)
			}
			defer store.Close()

			if got := storedVersion(t, pool); got != SchemaVersion {
				t.Fatalf("version after migration = %d, want %d", got, SchemaVersion)
			}

			all := listAllPostgresEnvironments(t, store, "proj-1")
			if len(all) != tt.historical {
				t.Fatalf("migrated %d environments, want %d — history is not negotiable",
					len(all), tt.historical)
			}
			for _, env := range all {
				if env.Name() != string(env.Ref()) {
					t.Errorf("%s name = %q, want the ref", env.Ref(), env.Name())
				}
				if _, ranked := env.Rank(); ranked {
					t.Errorf("%s was migrated with a rank; migration invents no ordering", env.Ref())
				}
				if env.Status() != EnvironmentActive || env.Revision() != 1 {
					t.Errorf("%s = %s revision %d, want active revision 1",
						env.Ref(), env.Status(), env.Revision())
				}
			}

			// Creation from here obeys the cap, and identity still precedes it.
			err = store.CreateEnvironment(t.Context(),
				mustEnvironmentValue(t, "brand-new", "proj-1", "New"))
			if tt.roomForMore {
				if err != nil {
					t.Errorf("create with room to spare error = %v", err)
				}
				if next := store.CreateEnvironment(t.Context(),
					mustEnvironmentValue(t, "one-more", "proj-1", "New")); !errors.Is(next, ErrEnvironmentLimit) {
					t.Errorf("create past the cap error = %v, want ErrEnvironmentLimit", next)
				}
			} else if !errors.Is(err, ErrEnvironmentLimit) {
				t.Errorf("create at or over the cap error = %v, want ErrEnvironmentLimit", err)
			}

			if err := store.CreateEnvironment(t.Context(),
				mustEnvironmentValue(t, refs[0], "proj-1", "New")); !errors.Is(err, ErrStoreAlreadyExists) {
				t.Errorf("re-creating a migrated ref error = %v, want ErrStoreAlreadyExists", err)
			}
		})
	}
}

// The same ref under two projects is two environments, because identity is the
// pair — and PostgreSQL is the backend where a global unique index would have
// been the tempting shortcut.
func TestPostgresMigrationKeepsProjectsIndependent(t *testing.T) {
	pool, dsn := schemaTestPool(t)
	createV2Schema(t, pool)
	seedPostgresV2Environments(t, pool, "proj-a", []string{"staging", "production"})
	seedPostgresV2Environments(t, pool, "proj-b", []string{"staging"})

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v2 schema: %v", err)
	}
	defer store.Close()

	for _, project := range []ProjectID{"proj-a", "proj-b"} {
		if _, err := store.Environment(t.Context(), project, "staging"); err != nil {
			t.Errorf("%s has no staging after migration: %v", project, err)
		}
	}
	if _, err := store.Environment(t.Context(), "proj-b", "production"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("proj-b inherited proj-a's production: %v", err)
	}
	if got := len(listAllPostgresEnvironments(t, store, "proj-a")); got != 2 {
		t.Errorf("proj-a has %d environments, want 2", got)
	}
}

// TestPostgresMigratesV3ToV4AddingAnEmptyHistory is task 066's migration.
//
// The assertion that matters most is the negative one: a migrated database has
// *no* promotions. Every completed evaluation in a v3 database was gated by
// something, and a migration that turned those gate results into promotion rows
// would be fabricating an audit trail — records asserting that somebody decided
// to advance a candidate, when nobody did. An empty history is the only honest
// answer, and the count below is what keeps it that way.
func TestPostgresMigratesV3ToV4AddingAnEmptyHistory(t *testing.T) {
	pool, dsn := schemaTestPool(t)
	createV3Schema(t, pool)
	seedPostgresV2Environments(t, pool, "proj-1", []string{"staging", "production"})
	seedPostgresV3Environments(t, pool, "proj-1", []string{"staging", "production"})

	if tableExists(t, pool, tablePromotions) {
		t.Fatal("the v3 fixture already has the promotion table")
	}
	if got := storedVersion(t, pool); got != schemaVersionV3 {
		t.Fatalf("fixture version = %d, want %d", got, schemaVersionV3)
	}

	store, err := OpenPostgresStore(context.Background(), PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v3 schema: %v", err)
	}
	defer store.Close()

	if got := storedVersion(t, pool); got != SchemaVersion {
		t.Errorf("version after migration = %d, want %d", got, SchemaVersion)
	}
	if !tableExists(t, pool, tablePromotions) {
		t.Fatal("the migration did not add the promotion table")
	}

	var rows int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM `+tablePromotions).Scan(&rows); err != nil {
		t.Fatalf("count promotions: %v", err)
	}
	if rows != 0 {
		t.Errorf("the migration synthesized %d promotion(s); a decision nobody "+
			"made must not appear in the audit history", rows)
	}

	// The index the collection pages on has to exist too, or every project
	// history would be a sequential scan that still returns the right answer.
	var indexed bool
	if err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = current_schema() AND indexname = $1)`,
		indexPromotionsByProject).Scan(&indexed); err != nil {
		t.Fatalf("check index: %v", err)
	}
	if !indexed {
		t.Errorf("the migration did not create %s", indexPromotionsByProject)
	}

	// Pre-existing state survived, and the migrated database accepts a decision.
	if got := len(listAllPostgresEnvironments(t, store, "proj-1")); got != 2 {
		t.Errorf("proj-1 has %d environments after migration, want 2", got)
	}
	history, err := store.ProjectPromotions(t.Context(), "proj-1", "", MaxPromotionPage)
	if err != nil {
		t.Fatalf("ProjectPromotions() error = %v", err)
	}
	if len(history) != 0 {
		t.Errorf("ProjectPromotions() returned %d rows, want none", len(history))
	}
}

// seedPostgresV3Environments gives a v3 fixture the ranked environments a
// promotion needs, written directly because the store cannot open the database
// until it has been migrated.
func seedPostgresV3Environments(t *testing.T, pool *pgxpool.Pool, projectID string, refs []string) {
	t.Helper()
	for i, ref := range refs {
		if _, err := pool.Exec(context.Background(),
			`INSERT INTO `+tableEnvironments+`
			 (project_id, ref, name, rank, status, revision)
			 VALUES ($1, $2, $3, $4, 'active', 1)
			 ON CONFLICT DO NOTHING`,
			projectID, ref, ref, (i+1)*10); err != nil {
			t.Fatalf("seed v3 environment %s: %v", ref, err)
		}
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

	// Two of the eleven tables: enough to be recognized, not enough to be any
	// version this binary knows.
	for _, stmt := range postgresSchemaStatements() {
		if createsTable(stmt, tableSchemaVersion) || createsTable(stmt, tableProjects) {
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
