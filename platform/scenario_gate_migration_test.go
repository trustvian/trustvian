package platform

// Schema 14 (task 107, D10): the gate snapshot column, forward-only on both
// backends. An execution completed before it reads not_recorded; nothing is
// backfilled, and every completion after it records its gate.

import (
	"context"
	"database/sql"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// downgradeToV13 turns a current database into the v13 shape: v14's column
// gone, version 13 stamped. exec runs one statement against it.
func downgradeToV13(t testing.TB, exec func(string) error) {
	t.Helper()
	downgradeToV14(t, exec)
	if err := exec(`ALTER TABLE ` + tableScenarioExecutions + ` DROP COLUMN ` + columnGateSnapshot); err != nil {
		t.Fatalf("drop v14 column: %v", err)
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 13 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v13: %v", err)
	}
}

// assertGateNotRecorded: an execution a migration carried forward is still
// completed with its verdict, and has no gate record.
func assertGateNotRecorded(t *testing.T, store Store, want ScenarioExecution) {
	t.Helper()
	got, err := store.ScenarioExecution(context.Background(), want.ID())
	if err != nil {
		t.Fatalf("ScenarioExecution(%s) after migration error = %v", want.ID(), err)
	}
	if got.Status() != ScenarioExecutionCompleted || got.Verdict() != want.Verdict() {
		t.Fatalf("migrated execution %s is %s %s, want completed %s", got.ID(), got.Status(),
			got.Verdict(), want.Verdict())
	}
	if got.GateRecord().Recorded() {
		t.Fatalf("migrated execution %s has a gate record; none was kept before schema 14", got.ID())
	}
}

// writeGateExecution completes an execution after the migration, to prove the
// column is written and read.
func writeGateExecution(t *testing.T, store Store) ScenarioExecution {
	t.Helper()
	ctx := context.Background()
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-proj-1", Environment: "staging"}
	if err := store.CreateScenarioExecution(ctx,
		newRunningExecution(t, "exec-v14", "support", scope, 2, "", scenarioEpoch.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	completed, err := store.CompleteScenarioExecution(ctx, "exec-v14", associations("v14", 2),
		recordedGate(2, GateVerdictFail), scenarioEpoch.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("CompleteScenarioExecution() after migration error = %v", err)
	}
	return completed
}

func assertGateDurable(t *testing.T, store Store, want ScenarioExecution) {
	t.Helper()
	got, err := store.ScenarioExecution(context.Background(), want.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !got.GateRecord().Recorded() || !reflect.DeepEqual(got.GateRecord(), want.GateRecord()) {
		t.Fatalf("gate %+v\nwant %+v", got.GateRecord(), want.GateRecord())
	}
}

func TestSQLiteMigratesToV14RecordingNoGate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		downgrade func(testing.TB, func(string) error)
	}{
		{"from v13", downgradeToV13},
		// v10 is the schema v0.11.0 shipped: one upgrade reaches v14.
		{"from v10", downgradeToV10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, path := testStore(t)
			seedProject(t, store, "proj-1")
			before := writeAndCompleteExecution(t, store)
			store.Close()

			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			tt.downgrade(t, func(statement string) error {
				_, err := raw.ExecContext(context.Background(), statement)
				return err
			})
			raw.Close()

			migrated, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatalf("OpenSQLiteStore() on a downgraded database error = %v", err)
			}
			if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
				t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
			}
			assertExecutionDurable(t, migrated, before)
			assertGateNotRecorded(t, migrated, before)
			written := writeGateExecution(t, migrated)
			migrated.Close()

			reopened, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertGateNotRecorded(t, reopened, before)
			assertGateDurable(t, reopened, written)
		})
	}
}

// TestPostgresMigratesToV14RecordingNoGate is the same on PostgreSQL. It
// skips without a database.
func TestPostgresMigratesToV14RecordingNoGate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		downgrade func(testing.TB, func(string) error)
	}{
		{"from v13", downgradeToV13},
		{"from v10", downgradeToV10},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dsn := isolatedSchemaDSN(t, postgresDSN(t))
			ctx := context.Background()
			store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			seedProject(t, store, "proj-1")
			before := writeAndCompleteExecution(t, store)
			store.Close()

			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			tt.downgrade(t, func(statement string) error {
				_, err := pool.Exec(ctx, statement)
				return err
			})
			pool.Close()

			migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatalf("opening a downgraded schema: %v", err)
			}
			assertExecutionDurable(t, migrated, before)
			assertGateNotRecorded(t, migrated, before)
			written := writeGateExecution(t, migrated)
			migrated.Close()

			reopened, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertGateNotRecorded(t, reopened, before)
			assertGateDurable(t, reopened, written)
		})
	}
}

// TestFreshAndMigratedScenarioExecutionTablesAgreeAtV14: the fresh table and
// a v13 table migrated forward hold the same columns.
func TestFreshAndMigratedScenarioExecutionTablesAgreeAtV14(t *testing.T) {
	fresh, freshPath := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableScenarioExecutions)
	fresh.Close()
	if !slices.Contains(freshColumns, columnGateSnapshot) {
		t.Fatalf("fresh columns %v lack %s", freshColumns, columnGateSnapshot)
	}

	raw, err := sql.Open("sqlite", freshPath)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV13(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()
	migrated, err := OpenSQLiteStore(t.Context(), freshPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got := sqliteTableColumns(t, migrated.db, tableScenarioExecutions)
	if strings.Join(got, ",") != strings.Join(freshColumns, ",") {
		t.Fatalf("migrated columns %v, fresh %v", got, freshColumns)
	}
}
