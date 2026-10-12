package platform

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// downgradeToV14 turns a current database into the v14 shape: v15's columns
// gone, version 14 stamped. exec runs one statement against it.
func downgradeToV14(t testing.TB, exec func(string) error) {
	t.Helper()
	for _, column := range []string{columnCLIVersion, columnReferenceMode} {
		if err := exec(`ALTER TABLE ` + tableScenarioExecutions + ` DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop v15 column %s: %v", column, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 14 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v14: %v", err)
	}
}

func TestValidateCLIVersion(t *testing.T) {
	for _, tt := range []struct {
		version string
		ok      bool
	}{
		{"", true},
		{"v0.12.0", true},
		{"v0.12.0 3e86f9cead2586e7ee90e14185a8f34074b54240", true},
		{"(devel)", true},
		{strings.Repeat("v", maxIdentifierLength), true},
		{strings.Repeat("v", maxIdentifierLength+1), false},
		{" v0.12.0", false},
		{"v0.12.0 ", false},
		{"   ", false},
		{"v0.12\n.0", false},
		{"v0.12\x7f", false},
	} {
		err := validateCLIVersion(tt.version)
		if (err == nil) != tt.ok {
			t.Errorf("validateCLIVersion(%q) = %v, want ok %v", tt.version, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrInvalidRepeatedRequest) {
			t.Errorf("validateCLIVersion(%q) = %v, want ErrInvalidRepeatedRequest", tt.version, err)
		}
	}
}

// A reference mode exists only beside a reference, and only from the closed
// vocabulary. A reference without a mode is valid: that is every execution
// begun before schema 15.
func TestProducerFactsAreHeldToTheExecution(t *testing.T) {
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: "staging"}
	own := newRunningExecution(t, "e-own", "support", scope, 2, "", scenarioEpoch)
	reused := newRunningExecution(t, "e-reused", "support", scope, 2, "e-own", scenarioEpoch)
	for _, tt := range []struct {
		name string
		base ScenarioExecution
		cli  string
		mode ScenarioReferenceMode
		ok   bool
	}{
		{"nothing recorded", own, "", "", true},
		{"a version, own reference side", own, "v0.13.0", "", true},
		{"last, reused", reused, "v0.13.0", ScenarioReferenceModeLast, true},
		{"execution, reused", reused, "v0.13.0", ScenarioReferenceModeExecution, true},
		{"reused, mode not recorded", reused, "", "", true},
		{"a mode without a reference", own, "v0.13.0", ScenarioReferenceModeLast, false},
		{"an unknown mode", reused, "v0.13.0", "latest", false},
		{"a padded version", own, " v0.13.0", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.base.withProducer(tt.cli, tt.mode)
			if (err == nil) != tt.ok {
				t.Fatalf("withProducer(%q, %q) = %v, want ok %v", tt.cli, tt.mode, err, tt.ok)
			}
			if err == nil && (got.CLIVersion() != tt.cli || got.ReferenceMode() != tt.mode) {
				t.Errorf("recorded %q %q, want %q %q", got.CLIVersion(), got.ReferenceMode(), tt.cli, tt.mode)
			}
			if err == nil && tt.base.CLIVersion() != "" {
				t.Errorf("withProducer changed its receiver")
			}
		})
	}
}

// producerExecution is a running execution reusing reference e-ref, with both
// facts recorded.
func producerExecution(t *testing.T, id string) ScenarioExecution {
	t.Helper()
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-proj-1", Environment: "staging"}
	e, err := newRunningExecution(t, id, "support", scope, 2, "e-ref", scenarioEpoch.Add(3*time.Hour)).
		withProducer("v0.13.0 3e86f9c", ScenarioReferenceModeLast)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func assertProducer(t *testing.T, store Store, id ScenarioExecutionID, cli string, mode ScenarioReferenceMode) {
	t.Helper()
	got, err := store.ScenarioExecution(context.Background(), id)
	if err != nil {
		t.Fatalf("ScenarioExecution(%s) error = %v", id, err)
	}
	if got.CLIVersion() != cli || got.ReferenceMode() != mode {
		t.Fatalf("execution %s records %q %q, want %q %q", id, got.CLIVersion(), got.ReferenceMode(), cli, mode)
	}
}

func TestProducerFactsRoundTripOnSQLite(t *testing.T) {
	store, _ := testStore(t)
	defer store.Close()
	if err := store.CreateScenarioExecution(t.Context(), producerExecution(t, "e-facts")); err != nil {
		t.Fatal(err)
	}
	assertProducer(t, store, "e-facts", "v0.13.0 3e86f9c", ScenarioReferenceModeLast)
}

// A stored empty string, an unknown mode, or a mode with no reference is
// corruption: no write produces any of them.
func TestDamagedProducerFactsAreCorrupt(t *testing.T) {
	for _, tt := range []struct {
		name, statement string
	}{
		{"an empty cli version", `UPDATE ` + tableScenarioExecutions + ` SET cli_version = '' WHERE id = 'e-facts'`},
		{"an empty reference mode", `UPDATE ` + tableScenarioExecutions + ` SET reference_mode = '' WHERE id = 'e-facts'`},
		{"an unknown reference mode", `UPDATE ` + tableScenarioExecutions + ` SET reference_mode = 'latest' WHERE id = 'e-facts'`},
		{"a padded cli version", `UPDATE ` + tableScenarioExecutions + ` SET cli_version = ' v1' WHERE id = 'e-facts'`},
		{"a mode without a reference", `UPDATE ` + tableScenarioExecutions + ` SET reference_execution_id = NULL WHERE id = 'e-facts'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			defer store.Close()
			if err := store.CreateScenarioExecution(t.Context(), producerExecution(t, "e-facts")); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(t.Context(), tt.statement); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ScenarioExecution(t.Context(), "e-facts"); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("ScenarioExecution() = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// An execution begun before schema 15 reads both facts as not recorded after
// the upgrade, and one begun after it keeps them.
func TestSQLiteMigratesToV15RecordingNothing(t *testing.T) {
	for _, tt := range []struct {
		name      string
		downgrade func(testing.TB, func(string) error)
	}{
		{"from v14", downgradeToV14},
		// v10 is the schema v0.11.0 shipped: one upgrade reaches v15.
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
			assertProducer(t, migrated, before.ID(), "", "")
			if err := migrated.CreateScenarioExecution(t.Context(), producerExecution(t, "e-facts")); err != nil {
				t.Fatal(err)
			}
			migrated.Close()

			reopened, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertProducer(t, reopened, before.ID(), "", "")
			assertProducer(t, reopened, "e-facts", "v0.13.0 3e86f9c", ScenarioReferenceModeLast)
		})
	}
}

// TestPostgresMigratesToV15RecordingNothing is the same on PostgreSQL. It
// skips without a database.
func TestPostgresMigratesToV15RecordingNothing(t *testing.T) {
	for _, tt := range []struct {
		name      string
		downgrade func(testing.TB, func(string) error)
	}{
		{"from v14", downgradeToV14},
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
			assertProducer(t, migrated, before.ID(), "", "")
			if err := migrated.CreateScenarioExecution(ctx, producerExecution(t, "e-facts")); err != nil {
				t.Fatal(err)
			}
			migrated.Close()

			reopened, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			assertProducer(t, reopened, before.ID(), "", "")
			assertProducer(t, reopened, "e-facts", "v0.13.0 3e86f9c", ScenarioReferenceModeLast)
		})
	}
}

// The fresh table and a v14 table migrated forward hold the same columns.
func TestFreshAndMigratedScenarioExecutionTablesAgreeAtV15(t *testing.T) {
	fresh, freshPath := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableScenarioExecutions)
	fresh.Close()
	for _, column := range []string{columnCLIVersion, columnReferenceMode} {
		if !slices.Contains(freshColumns, column) {
			t.Fatalf("fresh columns %v lack %s", freshColumns, column)
		}
	}
	raw, err := sql.Open("sqlite", freshPath)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV14(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()
	migrated, err := OpenSQLiteStore(t.Context(), freshPath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if got := sqliteTableColumns(t, migrated.db, tableScenarioExecutions); strings.Join(got, ",") != strings.Join(freshColumns, ",") {
		t.Fatalf("migrated columns %v, fresh %v", got, freshColumns)
	}
}
