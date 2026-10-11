package platform

// The recovery drill for the platform database, in both directions each
// schema step supports, on both backends. Task 081 built it for v13; task 107
// extends it to v14's gate snapshot:
//
//   - upgrade: a backup taken at v10 — by the real v0.11.0 trustvian-local,
//     the schema v0.11.0 users hold — and ones taken at v12 and v13 restore
//     into this binary and migrate to v14, every behavior reading unrecorded
//     (v13's step) and the completed execution reading its gate not recorded
//     (v14's step);
//   - restore at the new version: a backup taken at v14 restores and passes
//     the restore-time invariant checks with every count intact, and its
//     execution's gate snapshot decodes and re-proves every invariant, equal
//     to what the completion recorded.
//
// A backup is what an operator takes: VACUUM INTO for SQLite, pg_dump and
// pg_restore for PostgreSQL. Gated on TRUSTVIAN_TEST_PLATFORM_DRILL, because
// it needs the PostgreSQL client tools and, for the v10 leg, the previous
// release's binary (TRUSTVIAN_TEST_PLATFORM_UPGRADE_FROM_BINARY), which the
// CI job builds from its tag.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	osexec "os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

const (
	drillEnv             = "TRUSTVIAN_TEST_PLATFORM_DRILL"
	drillUpgradeFromEnv  = "TRUSTVIAN_TEST_PLATFORM_UPGRADE_FROM_BINARY"
	drillRunID           = "drill-run"
	drillProfile         = "drill-profile"
	drillCandidateRunID  = "drill-run-candidate"
	drillCandidateProf   = "drill-profile-candidate"
	drillExecutionID     = "drill-exec"
	drillEnvironment     = "staging"
	drillRecordsPerShape = 3
)

func requireDrill(t *testing.T) {
	t.Helper()
	if os.Getenv(drillEnv) == "" {
		t.Skipf("%s is not set; the platform recovery drill needs the PostgreSQL client tools "+
			"and the previous release's trustvian-local", drillEnv)
	}
}

// drillRecord is one record of the drill's single behavior.
func drillRecord(i int) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID: "drill-e" + strconv.Itoa(i), Timestamp: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC),
		ActorID: "agent-1", ActorType: event.ActorTypeAIAgent, Environment: drillEnvironment,
		Behavior: trustvian.StableFeatures{
			ActorType: event.ActorTypeAIAgent, OperationCategory: event.OperationCategoryTool,
			OperationName: "chat", TargetName: "ollama.localhost", TargetCategory: event.TargetCategoryExternal,
			Environment: drillEnvironment,
		},
		FingerprintID: "fp-drill", IdentityConfidence: 0.9, AnomalyScore: 0.2, AnomalyConfidence: 0.5,
		TrustScore: 0.8, ContextRisk: 0.1, RiskLevel: "low", Decision: "allow",
		PolicyReason: "default allow", MatchedDefault: true,
	}
}

// seedOverHTTP provisions a hierarchy and completes one run of semantic model
// calls through a control plane's /v1 API — the old binary's or this one's.
func seedOverHTTP(t *testing.T, base string) {
	t.Helper()
	post := func(path string, body any, want int) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(base+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("POST %s = %d, want %d: %s", path, resp.StatusCode, want, got)
		}
	}
	post("/v1/projects", map[string]string{"id": "proj-1", "name": "Drill"}, 201)
	post("/v1/agents", map[string]string{"id": "agent-1", "project_id": "proj-1", "name": "Agent"}, 201)
	post("/v1/candidates", map[string]any{"id": "cand-1", "agent_id": "agent-1"}, 201)
	post("/v1/environments", map[string]any{"project_id": "proj-1", "ref": drillEnvironment, "name": "Staging"}, 201)
	for _, run := range []struct{ id, profile string }{
		{drillRunID, drillProfile}, {drillCandidateRunID, drillCandidateProf},
	} {
		post("/v1/evaluation-runs", map[string]string{"id": run.id, "candidate_id": "cand-1",
			"environment": drillEnvironment, "behavioral_profile": run.profile}, 201)
		post("/v1/evaluation-runs/"+run.id+"/start", nil, 200)
		for i := range drillRecordsPerShape {
			post("/v1/evaluation-runs/"+run.id+"/records", map[string]any{
				"version": "1", "sequence": FormatSequence(uint64(i + 1)), "behavioral_profile": run.profile,
				"fidelity": "semantic", "behavior_layer": "model", "record": drillRecord(i),
			}, 200)
		}
		post("/v1/evaluation-runs/"+run.id+"/complete", nil, 200)
	}
	// One completed scenario execution over the two runs, as `eval run`
	// records it: what a v14 binary must read with its gate not recorded.
	post("/v1/scenario-executions", map[string]any{"id": drillExecutionID, "scenario_name": "drill",
		"runs": 1, "project_id": "proj-1", "agent_id": "agent-1", "environment": drillEnvironment}, 201)
	post("/v1/scenario-executions/"+drillExecutionID+"/complete", map[string]any{
		"reference_run_ids": []string{drillRunID}, "candidate_run_ids": []string{drillCandidateRunID},
		"gate_limits": map[string]string{
			"added_candidate_presence_minimum": "1", "added_reference_presence_maximum": "0",
			"max_repeated_added_behaviors": "0", "max_block_decisions_per_run": "0",
			"max_critical_risk_observations_per_run": "0",
		},
	}, 200)
}

// runOldControlPlane runs the previous release's trustvian-local until seed
// returns, then stops it.
func runOldControlPlane(t *testing.T, stateDir string, env []string, seed func(base string)) {
	t.Helper()
	binary := os.Getenv(drillUpgradeFromEnv)
	if binary == "" {
		t.Fatalf("%s is not set; the v10 leg needs the previous release's trustvian-local", drillUpgradeFromEnv)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	args := []string{"--state-dir", stateDir, "--listen", address}
	if env != nil {
		args = append(args, "--backend", "postgres")
	}
	cmd := osexec.Command(binary, args...)
	cmd.Env = append(os.Environ(), env...)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	defer stop()
	base := "http://" + address
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := http.Get(base + "/v1/projects/none/environments")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the previous release never answered: %v\n%s", err, output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	seed(base)
	stop()
}

// seedCurrent completes the same runs and execution through this binary's
// control plane, with the same envelope facts, and returns the completed
// execution. Its limits include every optional one, so the recorded gate
// carries evaluated per-target and model-layer checks. The caller closes the
// store.
func seedCurrent(t *testing.T, store Store) ScenarioExecution {
	t.Helper()
	ctx := context.Background()
	seedParents(t, store)
	seedCandidate(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	epoch := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		id      EvaluationRunID
		profile BehavioralProfileRef
	}{{drillRunID, drillProfile}, {drillCandidateRunID, drillCandidateProf}} {
		run, err := NewEvaluationRun(r.id, "cand-1", drillEnvironment, r.profile, epoch)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CreateEvaluationRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		started, err := run.Start(epoch.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
			t.Fatal(err)
		}
		for i := range drillRecordsPerShape {
			if _, err := plane.IngestDecisionRecord(ctx, IngestRequest{
				RunID: r.id, Sequence: uint64(i + 1), BehavioralProfile: r.profile,
				Fidelity: event.FidelitySemantic, BehaviorLayer: event.LayerModel, Record: drillRecord(i),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := plane.CompleteEvaluationRun(ctx, r.id, epoch.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: drillEnvironment}
	if _, _, err := plane.BeginScenarioExecution(ctx, BeginScenarioExecutionRequest{
		ID: drillExecutionID, ScenarioName: "drill", Runs: 1, Scope: scope, At: epoch.Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	execution, _, err := plane.CompleteScenarioExecution(ctx, CompleteScenarioExecutionRequest{
		ID: drillExecutionID, ReferenceRunIDs: []EvaluationRunID{drillRunID},
		CandidateRunIDs: []EvaluationRunID{drillCandidateRunID},
		Limits: RepeatedEvaluationGateLimits{
			AddedCandidatePresenceMinimum: 1,
			MinCandidateFrequency:         NewOptionalGateLimit(1),
			MaxLostBehaviors:              NewOptionalGateLimit(0),
			MaxCallsPerRun:                []TargetCallLimit{{Target: "ollama.localhost", Max: drillRecordsPerShape}},
			MaxLLMCallsPerRun:             NewOptionalGateLimit(drillRecordsPerShape),
		},
		At: epoch.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !execution.GateRecord().Recorded() || execution.Verdict() != GateVerdictPass {
		t.Fatalf("seeded execution %s: verdict %s, gate recorded %v", execution.ID(), execution.Verdict(),
			execution.GateRecord().Recorded())
	}
	return execution
}

// assertDrillExecution: the execution survives as completed PASS. want is the
// value the completion returned when the gate must be restored equal to it,
// or the zero value when the backup predates schema 14 and the gate must read
// not recorded.
func assertDrillExecution(t *testing.T, store Store, want ScenarioExecution) {
	t.Helper()
	got, err := store.ScenarioExecution(context.Background(), drillExecutionID)
	if err != nil {
		t.Fatalf("ScenarioExecution() after restore error = %v", err)
	}
	if got.Status() != ScenarioExecutionCompleted || got.Verdict() != GateVerdictPass ||
		len(got.Repetitions()) != 2 {
		t.Fatalf("restored execution %+v", got)
	}
	if want.ID() == "" {
		if got.GateRecord().Recorded() {
			t.Fatal("an execution completed before schema 14 restored with a gate record")
		}
		return
	}
	record := got.GateRecord()
	if !record.Recorded() || !reflect.DeepEqual(record, want.GateRecord()) {
		t.Fatalf("restored gate %+v\nwant          %+v", record, want.GateRecord())
	}
	// Re-proved, not only read: the record's own invariants, against the N
	// it was recorded for, through the stored text.
	text, err := encodeScenarioGateRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeScenarioGateRecord(text, got.Runs()); err != nil {
		t.Fatalf("restored gate breaks an invariant: %v", err)
	}
	frequency := record.Gate().FrequencyChecks()
	for _, c := range frequency {
		if c.State != GateCheckEvaluated {
			t.Fatalf("drill fixture: %s is %s, want every optional check evaluated", c.Name, c.State)
		}
	}
}

// assertDrillEntry: the one behavior, with the observations the drill made
// and the fidelity a migration or a restore must leave.
func assertDrillEntry(t *testing.T, store Store, want BehaviorFidelity) {
	t.Helper()
	_, snapshot, err := store.EvaluationEvidence(context.Background(), drillRunID)
	if err != nil {
		t.Fatalf("EvaluationEvidence() after restore error = %v", err)
	}
	entries := snapshot.Entries()
	if len(entries) != 1 || entries[0].Observations != drillRecordsPerShape {
		t.Fatalf("restored entries %+v", entries)
	}
	if entries[0].Fidelity != want {
		t.Fatalf("restored fidelity %+v, want %+v", entries[0].Fidelity, want)
	}
	if err := validateBehaviorFidelity(entries[0].Fidelity, entries[0].Observations); err != nil {
		t.Fatalf("restored fidelity breaks an invariant: %v", err)
	}
}

var semanticDrill = BehaviorFidelity{Semantic: drillRecordsPerShape, LayerModel: drillRecordsPerShape}

// sqliteBackup is an operator's online SQLite backup, VACUUM INTO.
func sqliteBackup(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	backup := filepath.Join(t.TempDir(), "backup.db")
	if _, err := db.Exec(`VACUUM INTO '` + backup + `'`); err != nil {
		t.Fatalf("VACUUM INTO: %v", err)
	}
	return backup
}

func TestPlatformDrillSQLite(t *testing.T) {
	requireDrill(t)
	t.Run("v10 backup from v0.11.0 restores and migrates", func(t *testing.T) {
		state := t.TempDir()
		runOldControlPlane(t, state, nil, func(base string) { seedOverHTTP(t, base) })
		backup := sqliteBackup(t, filepath.Join(state, "platform.db"))
		raw, err := sql.Open("sqlite", backup)
		if err != nil {
			t.Fatal(err)
		}
		assertStamp(t, raw.QueryRow(`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`), schemaVersionV10)
		raw.Close()
		restored, err := OpenSQLiteStore(t.Context(), backup)
		if err != nil {
			t.Fatalf("opening a v10 backup: %v", err)
		}
		defer restored.Close()
		if v, _ := restored.storedSchemaVersion(t.Context()); v != SchemaVersion {
			t.Fatalf("version = %d, want %d", v, SchemaVersion)
		}
		assertDrillEntry(t, restored, unrecordedFidelity(drillRecordsPerShape))
		assertDrillExecution(t, restored, ScenarioExecution{})
	})
	for _, leg := range []struct {
		name      string
		stamp     int
		downgrade func(testing.TB, func(string) error)
		fidelity  BehaviorFidelity
	}{
		{"v12", schemaVersionV12, downgradeToV12, unrecordedFidelity(drillRecordsPerShape)},
		{"v13", schemaVersionV13, downgradeToV13, semanticDrill},
	} {
		t.Run(leg.name+" backup restores and migrates", func(t *testing.T) {
			store, path := testStore(t)
			seedCurrent(t, store)
			store.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			leg.downgrade(t, func(s string) error { _, err := raw.Exec(s); return err })
			raw.Close()
			backup := sqliteBackup(t, path)
			raw, err = sql.Open("sqlite", backup)
			if err != nil {
				t.Fatal(err)
			}
			assertStamp(t, raw.QueryRow(`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`), leg.stamp)
			raw.Close()
			restored, err := OpenSQLiteStore(t.Context(), backup)
			if err != nil {
				t.Fatalf("opening a %s backup: %v", leg.name, err)
			}
			defer restored.Close()
			if v, _ := restored.storedSchemaVersion(t.Context()); v != SchemaVersion {
				t.Fatalf("version = %d, want %d", v, SchemaVersion)
			}
			assertDrillEntry(t, restored, leg.fidelity)
			assertDrillExecution(t, restored, ScenarioExecution{})
		})
	}
	t.Run("v14 backup restores with every count and invariant", func(t *testing.T) {
		store, path := testStore(t)
		completed := seedCurrent(t, store)
		store.Close()
		restored, err := OpenSQLiteStore(t.Context(), sqliteBackup(t, path))
		if err != nil {
			t.Fatalf("opening a v14 backup: %v", err)
		}
		defer restored.Close()
		assertDrillEntry(t, restored, semanticDrill)
		assertDrillExecution(t, restored, completed)
	})
}

// pgDumpRestore dumps one schema with pg_dump and restores it into a new
// database with pg_restore, returning a DSN for the restored copy.
func pgDumpRestore(t *testing.T, dsn string) string {
	t.Helper()
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := osexec.LookPath(tool); err != nil {
			t.Fatalf("%s is not on PATH; the drill restores with the tools an operator uses", tool)
		}
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := u.Query().Get("search_path")
	if schema == "" {
		t.Fatalf("no schema in %s", redactDSN(dsn))
	}
	base := *u
	q := base.Query()
	q.Del("search_path")
	q.Del("options")
	base.RawQuery = q.Encode()

	archive := filepath.Join(t.TempDir(), "platform.dump")
	if out, err := osexec.Command("pg_dump", "--format=custom", "--no-owner", "--schema="+schema,
		"--file="+archive, base.String()).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v\n%s", err, out)
	}

	database := fmt.Sprintf("tv_drill_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(context.Background(), base.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `CREATE DATABASE "`+database+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), base.String())
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+database+`" WITH (FORCE)`)
	})
	target := base
	target.Path = "/" + database
	if out, err := osexec.Command("pg_restore", "--no-owner", "--exit-on-error",
		"--dbname="+target.String(), archive).CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v\n%s", err, out)
	}
	restored := target
	rq := restored.Query()
	for k, v := range u.Query() {
		if k == "search_path" || k == "options" {
			rq[k] = v
		}
	}
	restored.RawQuery = rq.Encode()
	return restored.String()
}

// assertStamp: the backup holds the schema the leg claims, before anything
// migrates it.
func assertStamp(t *testing.T, row interface{ Scan(...any) error }, want int) {
	t.Helper()
	var version int
	if err := row.Scan(&version); err != nil || version != want {
		t.Fatalf("backup schema version = %d (%v), want %d", version, err, want)
	}
}

func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil {
		return u.Redacted()
	}
	return "the DSN"
}

func TestPlatformDrillPostgres(t *testing.T) {
	requireDrill(t)
	ctx := context.Background()
	t.Run("v10 backup from v0.11.0 restores and migrates", func(t *testing.T) {
		dsn := isolatedSchemaDSN(t, postgresDSN(t))
		runOldControlPlane(t, t.TempDir(), []string{"TRUSTVIAN_PLATFORM_POSTGRES_DSN=" + dsn},
			func(base string) { seedOverHTTP(t, base) })
		backup := pgDumpRestore(t, dsn)
		pool, err := pgxpool.New(ctx, backup)
		if err != nil {
			t.Fatal(err)
		}
		assertStamp(t, pool.QueryRow(ctx, `SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`), schemaVersionV10)
		pool.Close()
		restored, err := OpenPostgresStore(ctx, PostgresConfig{DSN: backup})
		if err != nil {
			t.Fatalf("opening a v10 backup: %v", err)
		}
		defer restored.Close()
		assertDrillEntry(t, restored, unrecordedFidelity(drillRecordsPerShape))
		assertDrillExecution(t, restored, ScenarioExecution{})
	})
	for _, leg := range []struct {
		name      string
		stamp     int
		downgrade func(testing.TB, func(string) error)
		fidelity  BehaviorFidelity
	}{
		{"v12", schemaVersionV12, downgradeToV12, unrecordedFidelity(drillRecordsPerShape)},
		{"v13", schemaVersionV13, downgradeToV13, semanticDrill},
	} {
		t.Run(leg.name+" backup restores and migrates", func(t *testing.T) {
			dsn := isolatedSchemaDSN(t, postgresDSN(t))
			store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
			if err != nil {
				t.Fatal(err)
			}
			seedCurrent(t, store)
			store.Close()
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			leg.downgrade(t, func(s string) error { _, err := pool.Exec(ctx, s); return err })
			pool.Close()
			backup := pgDumpRestore(t, dsn)
			pool, err = pgxpool.New(ctx, backup)
			if err != nil {
				t.Fatal(err)
			}
			assertStamp(t, pool.QueryRow(ctx, `SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`), leg.stamp)
			pool.Close()
			restored, err := OpenPostgresStore(ctx, PostgresConfig{DSN: backup})
			if err != nil {
				t.Fatalf("opening a %s backup: %v", leg.name, err)
			}
			defer restored.Close()
			assertDrillEntry(t, restored, leg.fidelity)
			assertDrillExecution(t, restored, ScenarioExecution{})
		})
	}
	t.Run("v14 backup restores with every count and invariant", func(t *testing.T) {
		dsn := isolatedSchemaDSN(t, postgresDSN(t))
		store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
		if err != nil {
			t.Fatal(err)
		}
		completed := seedCurrent(t, store)
		store.Close()
		restored, err := OpenPostgresStore(ctx, PostgresConfig{DSN: pgDumpRestore(t, dsn)})
		if err != nil {
			t.Fatalf("opening a v14 backup: %v", err)
		}
		defer restored.Close()
		assertDrillEntry(t, restored, semanticDrill)
		assertDrillExecution(t, restored, completed)
	})
}
