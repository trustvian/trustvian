package platform

// The v4 → v5 migration, which is the first in this schema that changes no
// table and no row.
//
// Its whole content is three indexes and a version stamp. That makes the
// negative assertions the important ones: a migration that added a column, or
// wrote a row, or synthesized a project from something it found lying around,
// would be inventing platform state from a schema change. The tests below
// compare the database to itself across the migration and require it to be
// unchanged apart from the indexes and the stamp.

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"
)

// writeSchemaV4 builds a task 066 database: v3's tables plus the promotion
// table and its index, stamped 4, and without any of v5's indexes.
func writeSchemaV4(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)

	statements := append(schemaV1Statements(), ingestStateTableStatement())
	statements = append(statements,
		environmentsTableStatement(),
		promotionsTableStatement(),
		promotionsIndexStatement())
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create v4 schema: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_schema_version (id, version) VALUES (1, 4)`); err != nil {
		t.Fatalf("stamp v4: %v", err)
	}
	return db
}

// sqliteIndexNames lists the named indexes in a SQLite database.
//
// Auto-indexes are excluded: SQLite creates one per UNIQUE constraint with a
// generated name, and counting those would make this test depend on how the
// tables were declared rather than on what the migration did.
func sqliteIndexNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'index' AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	return names
}

// sqliteTableColumns lists one table's columns, so a migration that quietly
// added one is caught by name rather than by count.
func sqliteTableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM pragma_table_info(?) ORDER BY name`, table)
	if err != nil {
		t.Fatalf("list columns of %s: %v", table, err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list columns of %s: %v", table, err)
	}
	return names
}

// sqliteTableNames lists the platform tables present.
func sqliteTableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	return names
}

// TestSchemaV4MigratesToV5AddingOnlyIndexes is task 074's migration, asserted
// from both sides: what it must add, and everything it must leave alone.
func TestSchemaV4MigratesToV5AddingOnlyIndexes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db := writeSchemaV4(t, path)
	seedV1Content(t, db, "run-legacy", 4)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_environments (project_id, ref, name, rank, status, revision)
		 VALUES ('proj-1', 'staging', 'Staging', 30, 'active', 1)`); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	// The shape of the database before the migration, for comparison after.
	beforeTables := sqliteTableNames(t, db)
	beforeIndexes := sqliteIndexNames(t, db)
	beforeColumns := map[string][]string{}
	for _, table := range beforeTables {
		beforeColumns[table] = sqliteTableColumns(t, db, table)
	}
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v4 database error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}

	// Exactly the three intended indexes, and nothing else.
	afterIndexes := sqliteIndexNames(t, store.db)
	var added []string
	for _, name := range afterIndexes {
		if !slices.Contains(beforeIndexes, name) {
			added = append(added, name)
		}
	}
	want := []string{indexAgentsByProject, indexCandidatesByAgent, indexRunsByCandidate}
	slices.Sort(want)
	slices.Sort(added)
	if !slices.Equal(added, want) {
		t.Errorf("migration added indexes %v, want exactly %v", added, want)
	}
	for _, name := range beforeIndexes {
		if !slices.Contains(afterIndexes, name) {
			t.Errorf("migration dropped index %s", name)
		}
	}

	// No table added, none dropped.
	afterTables := sqliteTableNames(t, store.db)
	if !slices.Equal(afterTables, beforeTables) {
		t.Errorf("tables after = %v, before = %v; an index-only migration adds none",
			afterTables, beforeTables)
	}

	// No column added to any table.
	for _, table := range afterTables {
		after := sqliteTableColumns(t, store.db, table)
		if !slices.Equal(after, beforeColumns[table]) {
			t.Errorf("%s columns after = %v, before = %v; an index-only migration "+
				"changes no column", table, after, beforeColumns[table])
		}
	}
}

// TestV5MigrationWritesNoRow is the fabrication guard.
//
// Counting rows per table across the migration, so a synthesized project,
// agent, candidate, run or promotion is caught wherever it was inserted. The
// promotion count matters most: a decision nobody made is the one record this
// platform must never contain, and task 066 refused to synthesize one from
// historical evaluations for exactly this reason.
func TestV5MigrationWritesNoRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db := writeSchemaV4(t, path)
	seedV1Content(t, db, "run-legacy", 4)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_environments (project_id, ref, name, rank, status, revision)
		 VALUES ('proj-1', 'staging', 'Staging', 30, 'active', 1)`); err != nil {
		t.Fatalf("seed environment: %v", err)
	}

	tables := sqliteTableNames(t, db)
	before := map[string]int{}
	for _, table := range tables {
		before[table] = countRows(t, db, table)
	}
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	defer store.Close()

	for _, table := range tables {
		after := countRows(t, store.db, table)
		want := before[table]
		if table == tableSchemaVersion {
			// The one row the migration touches, and it updates rather than
			// inserts: the count must still be exactly one.
			if after != 1 {
				t.Errorf("%s holds %d rows, want 1", table, after)
			}
			continue
		}
		if after != want {
			t.Errorf("%s holds %d rows after the migration, %d before; an index-only "+
				"migration writes none", table, after, want)
		}
	}

	// Named explicitly, because "the count did not change" reads as an
	// accident and "no promotion was invented" is the actual requirement.
	if before[tablePromotions] != 0 || countRows(t, store.db, tablePromotions) != 0 {
		t.Error("the migration fabricated a promotion decision")
	}
}

// countRows counts one table, for the comparison above.
func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var count int
	// table comes from sqlite_master, never from a caller.
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

// TestV5PreservesEveryStoredValue compares content field by field rather than
// by row count, because a migration could in principle rewrite a row without
// changing how many there are.
func TestV5PreservesEveryStoredValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")
	db := writeSchemaV4(t, path)
	seedV1Content(t, db, "run-legacy", 7)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_environments (project_id, ref, name, rank, status, revision)
		 VALUES ('proj-1', 'staging', 'Staging', 30, 'active', 2)`); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := t.Context()

	project, err := store.Project(ctx, "proj-1")
	if err != nil {
		t.Fatalf("project did not survive: %v", err)
	}
	if project.ID() != "proj-1" {
		t.Errorf("project id = %q", project.ID())
	}

	run, err := store.EvaluationRun(ctx, "run-legacy")
	if err != nil {
		t.Fatalf("run did not survive: %v", err)
	}
	if run.Environment() != "staging" || run.CandidateID() != "cand-1" {
		t.Errorf("run = %s / %s, want staging / cand-1", run.Environment(), run.CandidateID())
	}

	environment, err := store.Environment(ctx, "proj-1", "staging")
	if err != nil {
		t.Fatalf("environment did not survive: %v", err)
	}
	if rank, ranked := environment.Rank(); !ranked || rank != 30 {
		t.Errorf("rank = (%d, %t), want (30, true)", rank, ranked)
	}
	if environment.Revision() != 2 {
		t.Errorf("revision = %d, want 2; the migration must not touch it",
			environment.Revision())
	}
	if environment.Name() != "Staging" {
		t.Errorf("name = %q, want Staging", environment.Name())
	}

	// And the collections the migration exists to serve now work against the
	// migrated data.
	agents, err := store.ProjectAgents(ctx, "proj-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("ProjectAgents() after migration error = %v", err)
	}
	if len(agents) != 1 || agents[0].ID() != "agent-1" {
		t.Errorf("agents = %v, want exactly agent-1", agents)
	}
	runs, err := store.CandidateEvaluationRuns(ctx, "cand-1", "", MaxListPage)
	if err != nil {
		t.Fatalf("CandidateEvaluationRuns() after migration error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID() != "run-legacy" {
		t.Errorf("runs = %v, want exactly run-legacy", runs)
	}
}

// TestFullChainReachesV5 walks v1 → v2 → v3 → v4 → v5 in one open.
func TestFullChainReachesV5(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db := writeSchemaV1(t, path)
	seedV1Content(t, db, "run-legacy", 3)
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v1 database error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version = %d, want %d", version, SchemaVersion)
	}
	// Every step's artefact: v2's cursor table, v3's backfilled environment,
	// v4's promotion table, v5's indexes.
	if err := store.requireTables(t.Context(), SchemaVersion, schemaTables); err != nil {
		t.Errorf("chained migration is missing tables: %v", err)
	}
	indexes := sqliteIndexNames(t, store.db)
	for _, want := range []string{
		indexPromotionsByProject,
		indexAgentsByProject, indexCandidatesByAgent, indexRunsByCandidate,
	} {
		if !slices.Contains(indexes, want) {
			t.Errorf("chained migration did not create %s", want)
		}
	}
	run, err := store.EvaluationRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("the v1 run did not survive the chain: %v", err)
	}
	if _, err := store.Environment(t.Context(), "proj-1", run.Environment()); err != nil {
		t.Errorf("the v1 run's environment was not backfilled: %v", err)
	}
}

// TestFreshDatabaseAndMigratedDatabaseAgree is the drift guard between the two
// ways a v5 schema comes into existence.
//
// A fresh create runs schemaStatements(); a migration runs each version's own
// statements in turn. If those two ever disagree, one deployment gets an index
// the other lacks and the difference shows up as a performance mystery rather
// than as a failure. Comparing the resulting index and table sets is what
// keeps them honest.
func TestFreshDatabaseAndMigratedDatabaseAgree(t *testing.T) {
	freshPath := filepath.Join(t.TempDir(), "fresh.db")
	fresh, err := OpenSQLiteStore(t.Context(), freshPath)
	if err != nil {
		t.Fatalf("fresh open error = %v", err)
	}
	defer fresh.Close()

	migratedPath := filepath.Join(t.TempDir(), "migrated.db")
	writeSchemaV1(t, migratedPath).Close()
	migrated, err := OpenSQLiteStore(t.Context(), migratedPath)
	if err != nil {
		t.Fatalf("migrated open error = %v", err)
	}
	defer migrated.Close()

	if a, b := sqliteTableNames(t, fresh.db), sqliteTableNames(t, migrated.db); !slices.Equal(a, b) {
		t.Errorf("fresh tables = %v, migrated = %v", a, b)
	}
	if a, b := sqliteIndexNames(t, fresh.db), sqliteIndexNames(t, migrated.db); !slices.Equal(a, b) {
		t.Errorf("fresh indexes = %v, migrated = %v; a fresh database and a migrated "+
			"one must end up identical", a, b)
	}
}
