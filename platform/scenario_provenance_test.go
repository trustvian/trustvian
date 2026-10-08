package platform

// Task 086: scenario provenance on executions — validation, persistence on
// both backends, and schema 12's migration.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testDigest(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// fullProvenance is a side with every field recorded.
func fullProvenance(model string) SideProvenance {
	return SideProvenance{
		ScenarioDigest: testDigest("a"), InputsDeclared: true, InputDigest: testDigest("b"),
		SideDeclaration: SideDeclaration{
			Model: model, PromptRef: PromptRef{Name: "support/system@v14", Digest: testDigest("c")},
		},
	}
}

// downgradeToV11 turns a current database into the v11 shape: v12's columns
// gone, version 11 stamped. exec runs one statement against it.
func downgradeToV11(t testing.TB, exec func(string) error) {
	t.Helper()
	for _, column := range scenarioProvenanceColumns() {
		if err := exec(`ALTER TABLE ` + tableScenarioExecutions + ` DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop v12 column %s: %v", column, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 11 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v11: %v", err)
	}
}

func TestSideProvenanceValidation(t *testing.T) {
	valid := fullProvenance("llama3.2")
	for _, tt := range []struct {
		name     string
		edit     func(*SideProvenance)
		mentions string
	}{
		{"everything recorded", func(*SideProvenance) {}, ""},
		{"nothing recorded", func(p *SideProvenance) { *p = SideProvenance{} }, ""},
		{"no inputs declared", func(p *SideProvenance) { p.InputsDeclared, p.InputDigest = false, "" }, ""},
		{"a model alone", func(p *SideProvenance) { *p = SideProvenance{SideDeclaration: SideDeclaration{Model: "m"}} }, ""},
		{"inputs without a scenario", func(p *SideProvenance) { p.ScenarioDigest = "" }, "only with a scenario digest"},
		{"declared without a digest", func(p *SideProvenance) { p.InputDigest = "" }, "exactly when"},
		{"a digest without declaring", func(p *SideProvenance) { p.InputsDeclared = false }, "exactly when"},
		{"an upper-case digest", func(p *SideProvenance) { p.ScenarioDigest = "sha256:" + strings.Repeat("A", 64) }, "scenario_digest"},
		{"a short digest", func(p *SideProvenance) { p.InputDigest = "sha256:abc" }, "input_digest"},
		{"another algorithm", func(p *SideProvenance) { p.ScenarioDigest = "md5:" + strings.Repeat("a", 32) }, "scenario_digest"},
		{"prompt text as a model", func(p *SideProvenance) { p.Model = "You are a helpful assistant." }, "model"},
		{"prompt text as a name", func(p *SideProvenance) { p.PromptRef.Name = "You are a helpful assistant." }, "prompt_ref.name"},
		{"a long name", func(p *SideProvenance) { p.PromptRef.Name = strings.Repeat("n", 129) }, "prompt_ref.name"},
		{"a name without a digest", func(p *SideProvenance) { p.PromptRef.Digest = "" }, "prompt_ref.digest"},
		{"a digest without a name", func(p *SideProvenance) { p.PromptRef.Name = "" }, "prompt_ref.name"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			tt.edit(&p)
			err := p.validate()
			if tt.mentions == "" {
				if err != nil {
					t.Fatalf("validate() = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidRepeatedRequest) || !strings.Contains(err.Error(), tt.mentions) {
				t.Fatalf("validate() = %v, want one mentioning %q", err, tt.mentions)
			}
		})
	}
}

// conformScenarioProvenance: each side's provenance round-trips exactly, a
// side with nothing recorded reads back as nothing recorded, and completion
// changes none of it.
func conformScenarioProvenance(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	scope := scenarioScope("proj-1")
	for _, tt := range []struct {
		id                   string
		reference, candidate SideProvenance
	}{
		{"exec-full", fullProvenance("llama3.2"), fullProvenance("gemma3:4b")},
		{"exec-none", SideProvenance{}, SideProvenance{}},
		{"exec-no-inputs", SideProvenance{ScenarioDigest: testDigest("d")},
			SideProvenance{ScenarioDigest: testDigest("e"), SideDeclaration: SideDeclaration{Model: "m"}}},
	} {
		e, err := newRunningExecution(t, tt.id, "support", scope, 1, "", scenarioEpoch).
			withProvenance(tt.reference, tt.candidate)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreateScenarioExecution(ctx, e); err != nil {
			t.Fatalf("CreateScenarioExecution(%s) error = %v", tt.id, err)
		}
		completed, err := store.CompleteScenarioExecution(ctx, ScenarioExecutionID(tt.id),
			associations(tt.id, 1), GateVerdictPass, scenarioEpoch.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := store.ScenarioExecution(ctx, ScenarioExecutionID(tt.id))
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range []ScenarioExecution{completed, loaded} {
			if got.Provenance(SideReference) != tt.reference || got.Provenance(SideCandidate) != tt.candidate {
				t.Fatalf("%s: provenance %+v / %+v, want %+v / %+v", tt.id,
					got.Provenance(SideReference), got.Provenance(SideCandidate), tt.reference, tt.candidate)
			}
		}
		// Each run reads back as its own side's provenance; an unrecorded
		// run is absent.
		recorded, complete, err := store.(RunProvenanceStore).RunProvenance(ctx, []EvaluationRunID{
			EvaluationRunID(tt.id + "-reference-1"), EvaluationRunID(tt.id + "-candidate-1"), "unrecorded"})
		if err != nil || !complete {
			t.Fatalf("RunProvenance(%s) = %v, complete %v", tt.id, err, complete)
		}
		if len(recorded) != 2 ||
			len(recorded[EvaluationRunID(tt.id+"-reference-1")]) != 1 ||
			recorded[EvaluationRunID(tt.id+"-reference-1")][0] != tt.reference ||
			len(recorded[EvaluationRunID(tt.id+"-candidate-1")]) != 1 ||
			recorded[EvaluationRunID(tt.id+"-candidate-1")][0] != tt.candidate {
			t.Fatalf("RunProvenance(%s) = %+v", tt.id, recorded)
		}
	}
}

func TestScenarioProvenanceConformsOnEveryBackend(t *testing.T) {
	backends := conformanceBackends(t)
	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) { conformScenarioProvenance(t, backend.open) })
	}
}

// assertMigratedExecutionNotRecorded: an execution begun before schema 12
// reads back with nothing recorded on either side — not an empty digest, not
// "no inputs".
func assertMigratedExecutionNotRecorded(t *testing.T, store Store, id ScenarioExecutionID) {
	t.Helper()
	e, err := store.ScenarioExecution(context.Background(), id)
	if err != nil {
		t.Fatalf("ScenarioExecution(%s) after migration error = %v", id, err)
	}
	for _, side := range []ComparisonSide{SideReference, SideCandidate} {
		if p := e.Provenance(side); p != (SideProvenance{}) || p.ScenarioRecorded() || p.InputsDeclared {
			t.Fatalf("migrated %s side = %+v, want nothing recorded", side, p)
		}
	}
}

// writeProvenanceExecution records an execution with provenance after the
// migration, to prove the new columns are written and read.
func writeProvenanceExecution(t *testing.T, store Store) ScenarioExecution {
	t.Helper()
	e, err := newRunningExecution(t, "exec-v12", "support",
		ScenarioScope{ProjectID: "proj-1", AgentID: "agent-proj-1", Environment: "staging"}, 2, "",
		scenarioEpoch.Add(time.Hour)).withProvenance(fullProvenance("llama3.2"), fullProvenance("gemma3:4b"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateScenarioExecution(context.Background(), e); err != nil {
		t.Fatalf("CreateScenarioExecution() after migration error = %v", err)
	}
	return e
}

func assertProvenanceDurable(t *testing.T, store Store, want ScenarioExecution) {
	t.Helper()
	got, err := store.ScenarioExecution(context.Background(), want.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, side := range []ComparisonSide{SideReference, SideCandidate} {
		if got.Provenance(side) != want.Provenance(side) {
			t.Fatalf("%s side = %+v, want %+v", side, got.Provenance(side), want.Provenance(side))
		}
	}
}

// TestSQLiteSchemaV11MigratesToV12RecordingNothing is the upgrade drill on
// SQLite: v11 data survives, executions read not recorded, and the new
// columns work and survive a restart.
func TestSQLiteSchemaV11MigratesToV12RecordingNothing(t *testing.T) {
	store, path := testStore(t)
	seedProject(t, store, "proj-1")
	before := writeAndCompleteExecution(t, store)
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV11(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v11 database error = %v", err)
	}
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	assertExecutionDurable(t, migrated, before)
	assertMigratedExecutionNotRecorded(t, migrated, before.ID())
	written := writeProvenanceExecution(t, migrated)
	migrated.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertMigratedExecutionNotRecorded(t, reopened, before.ID())
	assertProvenanceDurable(t, reopened, written)
}

// TestPostgresSchemaV11MigratesToV12RecordingNothing is the same drill on
// PostgreSQL. It skips without a database.
func TestPostgresSchemaV11MigratesToV12RecordingNothing(t *testing.T) {
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
	downgradeToV11(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v11 schema: %v", err)
	}
	assertExecutionDurable(t, migrated, before)
	assertMigratedExecutionNotRecorded(t, migrated, before.ID())
	written := writeProvenanceExecution(t, migrated)
	migrated.Close()

	reopened, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	assertMigratedExecutionNotRecorded(t, reopened, before.ID())
	assertProvenanceDurable(t, reopened, written)
}

// TestFreshAndMigratedScenarioExecutionTablesAgree: the fresh v12 table and a
// v11 table migrated forward hold the same columns in the same order.
func TestFreshAndMigratedScenarioExecutionTablesAgree(t *testing.T) {
	fresh, freshPath := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableScenarioExecutions)
	fresh.Close()

	raw, err := sql.Open("sqlite", freshPath)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV11(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()
	migrated, err := OpenSQLiteStore(t.Context(), filepath.Clean(freshPath))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got := sqliteTableColumns(t, migrated.db, tableScenarioExecutions)
	if strings.Join(got, ",") != strings.Join(freshColumns, ",") {
		t.Fatalf("migrated columns %v, fresh %v", got, freshColumns)
	}
	for _, column := range scenarioProvenanceColumns() {
		if !strings.Contains(","+strings.Join(got, ",")+",", ","+column+",") {
			t.Fatalf("column %s missing from %v", column, got)
		}
	}
}

// TestRestoredProvenanceRefusesDamage is the restore drill's half that a
// backup cannot prove by itself: a stored row no live execution could have
// written is refused as corrupt, never read as a digest.
func TestRestoredProvenanceRefusesDamage(t *testing.T) {
	for name, damage := range map[string]string{
		"an empty scenario digest":         `reference_scenario_digest = ''`,
		"an empty input digest":            `candidate_scenario_digest = '` + testDigest("a") + `', candidate_input_digest = ''`,
		"inputs without a scenario digest": `reference_input_digest = '` + testDigest("b") + `'`,
		"a malformed digest":               `candidate_scenario_digest = 'sha256:nothex'`,
		"a prompt name without its digest": `reference_prompt_ref_name = 'sys'`,
		"prompt text as a model":           `candidate_model = 'You are a helpful assistant.'`,
	} {
		t.Run(name, func(t *testing.T) {
			store, path := testStore(t)
			seedProject(t, store, "proj-1")
			written := writeAndCompleteExecution(t, store)
			store.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(`UPDATE `+tableScenarioExecutions+` SET `+damage+` WHERE id = ?`,
				string(written.ID())); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			reopened, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err := reopened.ScenarioExecution(t.Context(), written.ID()); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}
