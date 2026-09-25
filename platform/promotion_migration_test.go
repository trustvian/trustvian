package platform

// The v3 → v4 migration, which has one job that matters: it adds a place to
// record promotion decisions without inventing any.
//
// A schema-3 database recorded no promotions because none could be made.
// Synthesizing one from historical evaluation runs would fabricate an audit
// record — a decision nobody made, limits nobody chose, a moment nothing
// happened at — which is the one thing an audit history must never contain.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// writeSchemaV3 builds a task 065 database: v2's tables plus the environment
// registry, stamped 3.
func writeSchemaV3(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)

	statements := append(schemaV1Statements(), ingestStateTableStatement())
	statements = append(statements, environmentsTableStatement())
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create v3 schema: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_schema_version (id, version) VALUES (1, 3)`); err != nil {
		t.Fatalf("stamp v3: %v", err)
	}
	return db
}

// A populated task 065 database migrates, and everything it held survives.
func TestSchemaV3MigratesToV4PreservingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db := writeSchemaV3(t, path)
	seedV1Content(t, db, "run-legacy", 4)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_environments (project_id, ref, name, rank, status, revision)
		 VALUES ('proj-1', 'staging', 'Staging', 30, 'active', 1)`); err != nil {
		t.Fatalf("seed environment: %v", err)
	}
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v3 database error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	if err := store.requireTables(t.Context(), SchemaVersion, schemaTables); err != nil {
		t.Errorf("migrated database is missing tables: %v", err)
	}

	// Everything the v3 database held is still there.
	run, err := store.EvaluationRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("the v3 run did not survive: %v", err)
	}
	if run.Environment() != "staging" {
		t.Errorf("run environment = %q, want staging", run.Environment())
	}
	environment, err := store.Environment(t.Context(), "proj-1", "staging")
	if err != nil {
		t.Fatalf("the v3 environment did not survive: %v", err)
	}
	if rank, ranked := environment.Rank(); !ranked || rank != 30 {
		t.Errorf("environment rank = (%d, %t), want (30, true)", rank, ranked)
	}

	// And **no promotion was invented.**
	promotions, err := store.ProjectPromotions(t.Context(), "proj-1", "", MaxPromotionPage)
	if err != nil {
		t.Fatalf("ProjectPromotions() after migration error = %v", err)
	}
	if len(promotions) != 0 {
		t.Fatalf("migration fabricated %d promotion decisions; history is not invented",
			len(promotions))
	}
}

// A task 057 database reaches v4 through v2 and v3, one step at a time.
func TestMigrationFromV1ReachesV4(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db := writeSchemaV1(t, path)
	seedV1Content(t, db, "run-legacy", 4)
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v1 database error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version = %d, want %d", version, SchemaVersion)
	}
	// v2's cursor table, v3's backfilled environment and v4's promotion table
	// all exist, which is the whole chain in one assertion.
	if err := store.requireTables(t.Context(), SchemaVersion, schemaTables); err != nil {
		t.Errorf("chained migration is missing tables: %v", err)
	}
	run, err := store.EvaluationRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("the v1 run did not survive: %v", err)
	}
	if _, err := store.Environment(t.Context(), "proj-1", run.Environment()); err != nil {
		t.Errorf("the v1 run's environment was not backfilled: %v", err)
	}
	if promotions, err := store.ProjectPromotions(
		t.Context(), "proj-1", "", MaxPromotionPage); err != nil || len(promotions) != 0 {
		t.Errorf("promotions = %d, err = %v; want an empty history", len(promotions), err)
	}
}

// Reopening a v4 database verifies rather than re-migrating.
func TestV4MigrationIsNotRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	db := writeSchemaV3(t, path)
	db.Close()

	first, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("first open error = %v", err)
	}
	first.Close()

	second, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("second open error = %v", err)
	}
	defer second.Close()

	if version, _ := second.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Errorf("version = %d, want %d", version, SchemaVersion)
	}
}

// A newer schema fails closed, as every version step here does.
func TestV5DatabaseIsRefusedByThisBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE platform_schema_version SET version = 5 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v5: %v", err)
	}
	store.Close()

	if _, err := OpenSQLiteStore(t.Context(), path); !errors.Is(err, ErrStoreSchemaVersion) {
		t.Errorf("opening a v5 database error = %v, want ErrStoreSchemaVersion", err)
	}
}
