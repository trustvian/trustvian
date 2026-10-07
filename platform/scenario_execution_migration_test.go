package platform

// Schema v8 → v9: task 078's scenario executions, on both backends.

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
)

// downgradeToV8 turns a current database into the v8 shape: the scenario
// execution tables gone, version 8 stamped. exec runs one statement.
func downgradeToV8(t testing.TB, exec func(string) error) {
	t.Helper()
	downgradeToV9(t, exec)
	for _, table := range []string{tableScenarioRepetitions, tableScenarioExecutions} {
		if err := exec(`DROP TABLE ` + table); err != nil {
			t.Fatalf("drop v9 table %s: %v", table, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 8 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v8: %v", err)
	}
}

// seedV8History writes what a schema-8 database holds — a hierarchy, an
// evaluation run and promotion decisions — through the current store, so the
// downgrade leaves real v8 data behind.
func seedV8History(t *testing.T, store Store) (EvaluationRun, []Promotion) {
	t.Helper()
	accepted, rejected := historicalPromotions(t, store)
	ctx := context.Background()
	run, err := NewEvaluationRun("run-legacy", "cand-ref-proj-1", "staging", "profile-legacy", scenarioEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	return run, []Promotion{accepted, rejected}
}

// assertV8HistoryPreserved is the whole claim about existing data: every run
// and decision reads back unchanged.
func assertV8HistoryPreserved(t *testing.T, store Store, run EvaluationRun, promotions []Promotion) {
	t.Helper()
	ctx := context.Background()
	stored, err := store.EvaluationRun(ctx, run.ID())
	if err != nil || stored.Status() != run.Status() || stored.BehavioralProfile() != run.BehavioralProfile() {
		t.Fatalf("run after migration = %+v, %v; want %+v", stored, err, run)
	}
	assertPromotionsVerbatim(t, store, promotions)
}

// assertNoExecutionInvented: a migrated database holds no scenario execution,
// because none was ever recorded; nothing is reconstructed from its runs.
func assertNoExecutionInvented(t *testing.T, store Store) {
	t.Helper()
	if _, err := store.LatestCompletedScenarioExecution(context.Background(), "support",
		ScenarioScope{ProjectID: "proj-1", AgentID: "agent-proj-1", Environment: "staging"},
	); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("a migrated database reports a scenario execution: %v", err)
	}
}

// writeAndCompleteExecution proves the new tables work after the migration.
func writeAndCompleteExecution(t *testing.T, store Store) ScenarioExecution {
	t.Helper()
	ctx := context.Background()
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-proj-1", Environment: "staging"}
	if err := store.CreateScenarioExecution(ctx,
		newRunningExecution(t, "exec-after", "support", scope, 2, "", scenarioEpoch)); err != nil {
		t.Fatalf("CreateScenarioExecution() after migration error = %v", err)
	}
	completed, err := store.CompleteScenarioExecution(ctx, "exec-after", associations("after", 2),
		GateVerdictPass, scenarioEpoch.Add(time.Minute))
	if err != nil {
		t.Fatalf("CompleteScenarioExecution() after migration error = %v", err)
	}
	return completed
}

func assertExecutionDurable(t *testing.T, store Store, want ScenarioExecution) {
	t.Helper()
	latest, err := store.LatestCompletedScenarioExecution(context.Background(), want.ScenarioName(), want.Scope())
	if err != nil {
		t.Fatalf("LatestCompletedScenarioExecution() after restart error = %v", err)
	}
	if latest.ID() != want.ID() || len(latest.Repetitions()) != len(want.Repetitions()) ||
		latest.CompletionSequence() != want.CompletionSequence() {
		t.Fatalf("after restart: %+v\nwant %+v", latest, want)
	}
}

func TestSQLiteSchemaV8MigratesToV9PreservingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v8.db")
	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	run, promotions := seedV8History(t, store)
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV8(t, func(statement string) error {
		_, err := raw.ExecContext(t.Context(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v8 database error = %v", err)
	}
	// A v8 database now migrates through v9 to the current version.
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	if err := migrated.requireTables(t.Context(), SchemaVersion, schemaTables); err != nil {
		t.Fatalf("migrated database is missing tables: %v", err)
	}
	assertV8HistoryPreserved(t, migrated, run, promotions)
	assertNoExecutionInvented(t, migrated)
	written := writeAndCompleteExecution(t, migrated)
	migrated.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	assertV8HistoryPreserved(t, reopened, run, promotions)
	assertExecutionDurable(t, reopened, written)
}

// A v9 stamp without v9's tables is damage, and a newer stamp is a schema this
// build cannot read. Both are refused, never repaired or adopted.
func TestSQLiteSchemaV9DamageAndNewerAreRefused(t *testing.T) {
	for name, damage := range map[string]func(exec func(string) error) error{
		"current stamp, v8 tables": func(exec func(string) error) error {
			for _, table := range []string{tableScenarioRepetitions, tableScenarioExecutions} {
				if err := exec(`DROP TABLE ` + table); err != nil {
					return err
				}
			}
			return nil
		},
		"newer stamp": func(exec func(string) error) error {
			return exec(`UPDATE ` + tableSchemaVersion + ` SET version = ` + strconv.Itoa(SchemaVersion+1) + ` WHERE id = 1`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "damaged.db")
			store, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if err := damage(func(statement string) error {
				_, err := raw.ExecContext(t.Context(), statement)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			if _, err := OpenSQLiteStore(t.Context(), path); !errors.Is(err, ErrStoreSchemaVersion) {
				t.Fatalf("open error = %v, want ErrStoreSchemaVersion", err)
			}
		})
	}
}

func TestPostgresSchemaV8MigratesToV9PreservingData(t *testing.T) {
	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; PostgreSQL migration needs a database", postgresDSNEnv)
	}
	isolated := isolatedSchemaDSN(t, dsn)
	ctx := context.Background()

	store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	run, promotions := seedV8History(t, store)
	store.Close()

	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV8(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() on a v8 schema error = %v", err)
	}
	assertV8HistoryPreserved(t, migrated, run, promotions)
	assertNoExecutionInvented(t, migrated)
	written := writeAndCompleteExecution(t, migrated)
	migrated.Close()

	reopened, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	assertV8HistoryPreserved(t, reopened, run, promotions)
	assertExecutionDurable(t, reopened, written)
}

// The replayed v8 fixture — today's statements filtered to those v8 shipped —
// migrates forward, and the two damaged shapes are refused.
func TestPostgresSchemaV8FixtureMigratesAndDamageIsRefused(t *testing.T) {
	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; PostgreSQL migration needs a database", postgresDSNEnv)
	}
	ctx := context.Background()

	t.Run("replayed v8", func(t *testing.T) {
		isolated := isolatedSchemaDSN(t, dsn)
		pool, err := pgxpool.New(ctx, isolated)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		createOlderSchema(t, pool, schemaVersionV8)
		store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
		if err != nil {
			t.Fatalf("OpenPostgresStore() on a replayed v8 schema error = %v", err)
		}
		defer store.Close()
		if got := storedVersion(t, pool); got != SchemaVersion {
			t.Fatalf("version after migration = %d, want %d", got, SchemaVersion)
		}
	})

	for name, damage := range map[string]func(*pgxpool.Pool) error{
		"current stamp, v8 tables": func(pool *pgxpool.Pool) error {
			for _, table := range []string{tableScenarioRepetitions, tableScenarioExecutions} {
				if _, err := pool.Exec(ctx, `DROP TABLE `+table); err != nil {
					return err
				}
			}
			return nil
		},
		"newer stamp": func(pool *pgxpool.Pool) error {
			_, err := pool.Exec(ctx, `UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`, SchemaVersion+1)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			isolated := isolatedSchemaDSN(t, dsn)
			store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			pool, err := pgxpool.New(ctx, isolated)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if err := damage(pool); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated}); !errors.Is(err, ErrStoreSchemaVersion) {
				t.Fatalf("open error = %v, want ErrStoreSchemaVersion", err)
			}
		})
	}
}
