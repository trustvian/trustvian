package platform

// The v2 → v3 migration, which has one job that matters: every environment a
// run ever named must still exist afterwards.
//
// Schema 2 had no registry and no cap, so a valid database may reference far
// more distinct environments than may now be created. The cap governs
// creation, not existence — these fixtures are 63, 64 and 65 because that is
// where the two rules meet, and the 65 case is the one a "just cap it"
// migration would silently break.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// writeSchemaV2 builds a task 058 database: v1's tables plus the ingest
// cursor, stamped 2.
func writeSchemaV2(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)
	statements := append(schemaV1Statements(), ingestStateTableStatement())
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create v2 schema: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_schema_version (id, version) VALUES (1, 2)`); err != nil {
		t.Fatalf("stamp v2: %v", err)
	}
	return db
}

// seedV2Environments writes one project, one agent, one candidate, and one run
// per distinct environment ref — the only way schema 2 could record that an
// environment existed.
func seedV2Environments(t *testing.T, db *sql.DB, projectID string, refs []string) {
	t.Helper()
	ctx := context.Background()
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("seed v2 %q: %v", query, err)
		}
	}
	exec(`INSERT INTO platform_projects (id, name) VALUES (?, ?)`, projectID, "Checkout")
	exec(`INSERT INTO platform_agents (id, project_id, name) VALUES (?, ?, ?)`,
		"agent-"+projectID, projectID, "Agent")
	exec(`INSERT INTO platform_candidates
	      (id, agent_id, label, source_ref, artifact_digest, model, toolset_digest, config_digest)
	      VALUES (?, ?, '', '', '', '', '', '')`, "cand-"+projectID, "agent-"+projectID)

	for i, ref := range refs {
		exec(`INSERT INTO platform_evaluation_runs
		      (id, candidate_id, environment, behavioral_profile, status,
		       created_at, started_at, finished_at, failure_reason)
		      VALUES (?, ?, ?, 'profile-1', 'completed', ?, ?, ?, '')`,
			fmt.Sprintf("run-%s-%03d", projectID, i), "cand-"+projectID, ref,
			created, created, created)
	}
}

func environmentRefs(count int) []string {
	refs := make([]string, 0, count)
	for i := range count {
		refs = append(refs, fmt.Sprintf("historical-%03d", i))
	}
	return refs
}

// listAllEnvironments pages the collection to completion, which is the only
// way to read a project that migration left above the page bound.
func listAllEnvironments(t *testing.T, store *SQLiteStore, projectID ProjectID) []Environment {
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

// TestMigrationBackfillsHistoricalEnvironments is the boundary, three ways.
func TestMigrationBackfillsHistoricalEnvironments(t *testing.T) {
	tests := []struct {
		name        string
		historical  int
		roomForMore bool
	}{
		{"below the cap", maxProjectEnvironments - 1, true},
		{"at the cap", maxProjectEnvironments, false},
		{"above the cap", maxProjectEnvironments + 1, false},
		// Two pages and change: a project this size exists only because
		// migration preserved it, and enumerating it is the case the
		// response bound exists for.
		{"far above the cap", 130, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "v2.db")
			db := writeSchemaV2(t, path)
			refs := environmentRefs(tt.historical)
			seedV2Environments(t, db, "proj-1", refs)
			db.Close()

			store, err := OpenSQLiteStore(t.Context(), path)
			if err != nil {
				t.Fatalf("OpenSQLiteStore() on a v2 database error = %v", err)
			}
			defer store.Close()

			if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
				t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
			}

			// Every historical ref is present, unranked, active, revision 1,
			// and named after itself. Nothing dropped, merged or invented.
			all := listAllEnvironments(t, store, "proj-1")
			if len(all) != tt.historical {
				t.Fatalf("migrated %d environments, want %d — history is not negotiable",
					len(all), tt.historical)
			}
			for _, env := range all {
				if env.Name() != string(env.Ref()) {
					t.Errorf("%s name = %q, want the ref", env.Ref(), env.Name())
				}
				if _, ranked := env.Rank(); ranked {
					t.Errorf("%s was migrated with a rank; migration must invent no ordering", env.Ref())
				}
				if env.Status() != EnvironmentActive || env.Revision() != 1 {
					t.Errorf("%s = %s revision %d, want active revision 1",
						env.Ref(), env.Status(), env.Revision())
				}
			}

			// Every historical run still loads and still resolves its own
			// environment.
			for i, ref := range refs {
				runID := EvaluationRunID(fmt.Sprintf("run-proj-1-%03d", i))
				run, err := store.EvaluationRun(t.Context(), runID)
				if err != nil {
					t.Fatalf("EvaluationRun(%s) after migration error = %v", runID, err)
				}
				if string(run.Environment()) != ref {
					t.Errorf("%s environment = %q, want %q", runID, run.Environment(), ref)
				}
				if _, err := store.Environment(t.Context(), "proj-1", EnvironmentRef(ref)); err != nil {
					t.Errorf("environment %s does not resolve after migration: %v", ref, err)
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

			// A migrated environment is still ordinary configuration.
			existing, err := store.Environment(t.Context(), "proj-1", EnvironmentRef(refs[0]))
			if err != nil {
				t.Fatalf("Environment() error = %v", err)
			}
			ranked, err := existing.WithRank(10)
			if err != nil {
				t.Fatalf("WithRank() error = %v", err)
			}
			if err := store.UpdateEnvironment(t.Context(), existing, ranked); err != nil {
				t.Errorf("configuring a migrated environment error = %v", err)
			}
			archived, _ := ranked.Archive()
			if err := store.UpdateEnvironment(t.Context(), ranked, archived); err != nil {
				t.Errorf("archiving a migrated environment error = %v", err)
			}
		})
	}
}

// The same ref under two projects is two environments, because identity is the
// pair.
func TestMigrationKeepsProjectsIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db := writeSchemaV2(t, path)
	seedV2Environments(t, db, "proj-a", []string{"staging", "production"})
	seedV2Environments(t, db, "proj-b", []string{"staging"})
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
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
	if got := len(listAllEnvironments(t, store, "proj-a")); got != 2 {
		t.Errorf("proj-a has %d environments, want 2", got)
	}
}

// A v2 database with no runs migrates to an empty registry rather than
// failing or inventing anything.
func TestMigrationOfAnEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db := writeSchemaV2(t, path)
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Errorf("version = %d, want %d", version, SchemaVersion)
	}
	if err := store.requireTables(t.Context(), SchemaVersion, schemaTables); err != nil {
		t.Errorf("migrated database is missing tables: %v", err)
	}
}

// A task 057 database reaches v3 through v2, and everything it held survives
// both steps.
func TestMigrationFromV1ReachesV3(t *testing.T) {
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
	run, err := store.EvaluationRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("the v1 run did not survive: %v", err)
	}
	if _, err := store.Environment(t.Context(), "proj-1", run.Environment()); err != nil {
		t.Errorf("the v1 run's environment %q was not backfilled: %v", run.Environment(), err)
	}
}

// Reopening a v3 database verifies rather than re-migrating, and a v3
// database is refused by a build that only understands v2.
func TestMigrationIsNotRepeated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	db := writeSchemaV2(t, path)
	seedV2Environments(t, db, "proj-1", []string{"staging"})
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

	if got := len(listAllEnvironments(t, second, "proj-1")); got != 1 {
		t.Errorf("reopening produced %d environments, want 1 — the backfill ran twice", got)
	}
}
