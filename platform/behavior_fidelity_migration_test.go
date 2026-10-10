package platform

// Task 081: schema 13's migration and the stored fidelity counts, on both
// backends.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trustvian/trustvian/event"
)

// downgradeToV12 turns a current database into the v12 shape: v13's column
// gone, version 12 stamped. exec runs one statement against it.
func downgradeToV12(t testing.TB, exec func(string) error) {
	t.Helper()
	if err := exec(`ALTER TABLE ` + tableEntries + ` DROP COLUMN ` + columnFidelityCounts); err != nil {
		t.Fatalf("drop v13 column: %v", err)
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 12 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v12: %v", err)
	}
}

// ingestThree ingests three records of one behavior into a running run.
func ingestThree(t *testing.T, plane *ControlPlane, run EvaluationRun) {
	t.Helper()
	for i := range 3 {
		if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1), BehavioralProfile: run.BehavioralProfile(),
			Record: operationalTestRecord(run, "e"+strconv.Itoa(i), "1000", event.StatusOK),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// assertMigratedEntriesUnrecorded: every entry a migration carried forward
// reads unrecorded in both groups, and keeps every invariant.
func assertMigratedEntriesUnrecorded(t *testing.T, store Store, runID EvaluationRunID) {
	t.Helper()
	_, snapshot, err := store.EvaluationEvidence(context.Background(), runID)
	if err != nil {
		t.Fatalf("EvaluationEvidence() after migration error = %v", err)
	}
	if len(snapshot.Entries()) == 0 {
		t.Fatal("the fixture has no entries; this proves nothing")
	}
	for _, entry := range snapshot.Entries() {
		if want := unrecordedFidelity(entry.Observations); entry.Fidelity != want {
			t.Errorf("migrated entry %s = %+v, want %+v", entry.FingerprintID, entry.Fidelity, want)
		}
		if level, mixed := entry.Fidelity.Reported(); level != FidelityLevelUnrecorded || mixed {
			t.Errorf("migrated entry reports %s, mixed %v", level, mixed)
		}
	}
}

func TestSQLiteSchemaV12MigratesToV13MarkingFidelityUnrecorded(t *testing.T) {
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	ingestThree(t, plane, run)
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV12(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v12 database error = %v", err)
	}
	defer migrated.Close()
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	assertMigratedEntriesUnrecorded(t, migrated, run.ID())
}

func TestPostgresSchemaV12MigratesToV13MarkingFidelityUnrecorded(t *testing.T) {
	dsn := isolatedSchemaDSN(t, postgresDSN(t))
	ctx := context.Background()
	store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	seedParents(t, store)
	seedCandidate(t, store)
	run := mustRun(t, "run-1", "cand-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	started, err := run.Start(run.CreatedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatal(err)
	}
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	ingestThree(t, plane, started)
	store.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV12(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v12 schema: %v", err)
	}
	defer migrated.Close()
	assertMigratedEntriesUnrecorded(t, migrated, run.ID())
}

// TestFreshAndMigratedEntryTablesAgreeAtV13: the fresh entry table and a v12
// table migrated forward hold the same columns, in the same order.
func TestFreshAndMigratedEntryTablesAgreeAtV13(t *testing.T) {
	fresh, freshPath := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableEntries)
	fresh.Close()

	raw, err := sql.Open("sqlite", freshPath)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV12(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()
	migrated, err := OpenSQLiteStore(t.Context(), filepath.Clean(freshPath))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got := sqliteTableColumns(t, migrated.db, tableEntries)
	if strings.Join(got, ",") != strings.Join(freshColumns, ",") {
		t.Fatalf("migrated columns %v, fresh %v", got, freshColumns)
	}
}

// TestMigrationBackfillMatchesTheUnrecordedCounts: the SQL backfill and the Go
// value it stands for are the same text.
func TestMigrationBackfillMatchesTheUnrecordedCounts(t *testing.T) {
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	ingestThree(t, plane, run)
	store.Close()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(behaviorFidelityBackfillStatement()); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := raw.QueryRow(`SELECT ` + columnFidelityCounts + ` FROM ` + tableEntries).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if want := encodeFidelityCounts(unrecordedFidelity(3)); text != want {
		t.Fatalf("backfill wrote %q, want %q", text, want)
	}
}

// TestRestoredFidelityRefusesDamage: a stored row no fold could have written is
// refused as corrupt, never read.
func TestRestoredFidelityRefusesDamage(t *testing.T) {
	for name, damage := range map[string]string{
		"empty":                    `''`,
		"eight counters":           `'0,0,3,0,0,0,0,0'`,
		"short of observations":    `'0,0,2,0,0,0,0,0,2'`,
		"layer disagrees":          `'0,3,0,0,0,0,2,0,1'`,
		"semantic without a layer": `'3,0,0,0,0,0,0,0,0'`,
		"leading zero":             `'0,0,03,0,0,0,0,0,3'`,
	} {
		t.Run(name, func(t *testing.T) {
			store, path := testStore(t)
			run := seedRunningRun(t, store)
			plane, err := NewControlPlane(store, store, store)
			if err != nil {
				t.Fatal(err)
			}
			ingestThree(t, plane, run)
			store.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`UPDATE ` + tableEntries + ` SET ` + columnFidelityCounts + ` = ` + damage); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			reopened, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, _, err := reopened.EvaluationEvidence(t.Context(), run.ID()); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}
