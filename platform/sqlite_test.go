package platform_test

// Task 057: local platform persistence.
//
// The assertions that matter here are about what survives and what refuses.
//
// Survival: the full derived chain — diff, scorecard, gate — must still work
// on evidence loaded after a restart, because that chain is why the evidence
// is persisted at all.
//
// Refusal: a create never overwrites, a stale run update never wins, evidence
// never moves backwards, saturation never heals, an unknown or unversioned
// database is never adopted, and no corrupt row ever becomes a bound value.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// mustRun keeps the transition tables readable. A domain transition that
// fails here is a broken fixture, not a store behavior under test.
func mustRun(run platform.EvaluationRun, err error) platform.EvaluationRun {
	if err != nil {
		panic("test fixture built an invalid transition: " + err.Error())
	}
	return run
}

// storePath gives each test its own database file.
func storePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "platform.db")
}

func openStore(t *testing.T, path string) *platform.SQLiteStore {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// seedHierarchy creates the project → agent → candidate → run chain every
// evidence test needs.
func seedHierarchy(t *testing.T, store *platform.SQLiteStore, runID platform.EvaluationRunID,
	candidateID platform.CandidateID, environment platform.EnvironmentRef,
	profile platform.BehavioralProfileRef,
) platform.EvaluationRun {
	t.Helper()
	ctx := t.Context()

	project, err := platform.NewProject("proj-1", "Checkout")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := store.CreateProject(ctx, project); err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("CreateProject() error = %v", err)
	}

	agent, err := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	if err := store.CreateAgent(ctx, agent); err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	candidate, err := platform.NewCandidate(candidateID, "agent-1",
		platform.CandidateMetadata{Label: "v3", SourceRef: "abc123"})
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	if err := store.CreateCandidate(ctx, candidate); err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("CreateCandidate() error = %v", err)
	}

	run, err := platform.NewEvaluationRun(runID, candidateID, environment, profile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	return run
}

// ---------------------------------------------------------------------
// Migration
// ---------------------------------------------------------------------

func TestFreshDatabaseInitializesAndPersists(t *testing.T) {
	path := storePath(t)

	store := openStore(t, path)
	project, err := platform.NewProject("proj-1", "Checkout")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := store.CreateProject(t.Context(), project); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// The point of the task: a restart does not erase it.
	reopened := openStore(t, path)
	loaded, err := reopened.Project(t.Context(), "proj-1")
	if err != nil {
		t.Fatalf("Project() after reopen error = %v", err)
	}
	if loaded.ID() != project.ID() || loaded.Name() != project.Name() {
		t.Errorf("reopened project = %q/%q, want %q/%q",
			loaded.ID(), loaded.Name(), project.ID(), project.Name())
	}
}

func TestRepeatedOpenIsIdempotent(t *testing.T) {
	path := storePath(t)
	for range 3 {
		store, err := platform.OpenSQLiteStore(t.Context(), path)
		if err != nil {
			t.Fatalf("OpenSQLiteStore() error = %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

// ---------------------------------------------------------------------
// Control state
// ---------------------------------------------------------------------

func TestControlStateRoundTrip(t *testing.T) {
	path := storePath(t)
	ctx := t.Context()
	store := openStore(t, path)

	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")

	// Every metadata field populated, because an unexercised field is where
	// a column mapping quietly goes wrong.
	meta := platform.CandidateMetadata{
		Label:          "v3.1",
		SourceRef:      "refs/tags/v3.1",
		ArtifactDigest: "sha256:abc",
		Model:          "claude-opus-5",
		ToolsetDigest:  "sha256:def",
		ConfigDigest:   "sha256:ghi",
	}
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", meta)

	for _, create := range []func() error{
		func() error { return store.CreateProject(ctx, project) },
		func() error { return store.CreateAgent(ctx, agent) },
		func() error { return store.CreateCandidate(ctx, candidate) },
	} {
		if err := create(); err != nil {
			t.Fatalf("create error = %v", err)
		}
	}
	store.Close()

	reopened := openStore(t, path)
	loadedCandidate, err := reopened.Candidate(ctx, "cand-1")
	if err != nil {
		t.Fatalf("Candidate() error = %v", err)
	}
	if got := loadedCandidate.Metadata(); got != meta {
		t.Errorf("metadata = %+v, want %+v", got, meta)
	}
	if loadedCandidate.AgentID() != "agent-1" {
		t.Errorf("AgentID() = %q, want agent-1", loadedCandidate.AgentID())
	}

	loadedAgent, err := reopened.Agent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Agent() error = %v", err)
	}
	if loadedAgent.ProjectID() != "proj-1" || loadedAgent.Name() != "Deploy agent" {
		t.Errorf("agent = %q/%q", loadedAgent.ProjectID(), loadedAgent.Name())
	}
}

func TestMissingValuesAreNotFound(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()

	if _, err := store.Project(ctx, "nope"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("Project() error = %v, want ErrStoreNotFound", err)
	}
	if _, err := store.Agent(ctx, "nope"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("Agent() error = %v, want ErrStoreNotFound", err)
	}
	if _, err := store.Candidate(ctx, "nope"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("Candidate() error = %v, want ErrStoreNotFound", err)
	}
	if _, err := store.EvaluationRun(ctx, "nope"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("EvaluationRun() error = %v, want ErrStoreNotFound", err)
	}
	if _, _, err := store.EvaluationEvidence(ctx, "nope"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("EvaluationEvidence() error = %v, want ErrStoreNotFound", err)
	}
}

// ---------------------------------------------------------------------
// Create means create
// ---------------------------------------------------------------------

// The candidate case is the one that matters: rewriting an artifact digest
// under an existing ID would change what a finished run was evaluated against.
func TestCreateNeverUpserts(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")

	original, _ := platform.NewCandidate("cand-1", "agent-1",
		platform.CandidateMetadata{Label: "v3", SourceRef: "abc123"})

	// Same ID, deliberately different data.
	rewrite, _ := platform.NewCandidate("cand-1", "agent-1",
		platform.CandidateMetadata{Label: "MALICIOUS", ArtifactDigest: "sha256:evil"})

	if err := store.CreateCandidate(ctx, rewrite); !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Fatalf("CreateCandidate() error = %v, want ErrStoreAlreadyExists", err)
	}

	loaded, err := store.Candidate(ctx, "cand-1")
	if err != nil {
		t.Fatalf("Candidate() error = %v", err)
	}
	if loaded.Metadata() != original.Metadata() {
		t.Errorf("stored metadata = %+v, want the original %+v; the create overwrote history",
			loaded.Metadata(), original.Metadata())
	}
}

func TestDuplicateCreateIsRefusedForEveryEntity(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	run := seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")

	project, _ := platform.NewProject("proj-1", "Different name")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Different name")
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{Label: "other"})

	tests := []struct {
		name string
		err  error
	}{
		{"project", store.CreateProject(ctx, project)},
		{"agent", store.CreateAgent(ctx, agent)},
		{"candidate", store.CreateCandidate(ctx, candidate)},
		{"run", store.CreateEvaluationRun(ctx, run)},
	}
	for _, tt := range tests {
		if !errors.Is(tt.err, platform.ErrStoreAlreadyExists) {
			t.Errorf("duplicate %s create error = %v, want ErrStoreAlreadyExists", tt.name, tt.err)
		}
	}
}

// ---------------------------------------------------------------------
// Referential integrity
// ---------------------------------------------------------------------

func TestMissingParentIsRefused(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()

	agent, _ := platform.NewAgent("agent-x", "no-such-project", "Orphan")
	if err := store.CreateAgent(ctx, agent); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("CreateAgent() with missing project error = %v, want ErrStoreNotFound", err)
	}

	candidate, _ := platform.NewCandidate("cand-x", "no-such-agent", platform.CandidateMetadata{})
	if err := store.CreateCandidate(ctx, candidate); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("CreateCandidate() with missing agent error = %v, want ErrStoreNotFound", err)
	}

	run, _ := platform.NewEvaluationRun("run-x", "no-such-candidate", "staging", "profile-1", aggEpoch)
	if err := store.CreateEvaluationRun(ctx, run); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("CreateEvaluationRun() with missing candidate error = %v, want ErrStoreNotFound", err)
	}

	// And the valid hierarchy still works, so the checks above are not simply
	// rejecting everything.
	seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")
}

// ---------------------------------------------------------------------
// Run lifecycle round-trip
// ---------------------------------------------------------------------

func TestEvaluationRunRoundTripAcrossEveryStatus(t *testing.T) {
	// Deliberately not UTC and not whole seconds: normalizing either would
	// discard information the caller chose to record.
	zone := time.FixedZone("test-zone", 5*60*60+30*60)
	created := time.Date(2026, 3, 4, 5, 6, 7, 123456789, zone)

	type step struct{ from, to platform.EvaluationRun }

	// Each case lists the transitions in order, so the store's
	// compare-and-swap sees exactly the single steps the domain allows.
	tests := []struct {
		name       string
		steps      func(platform.EvaluationRun) []step
		wantStatus platform.RunStatus
		wantReason string
	}{
		{
			name:       "pending",
			steps:      func(r platform.EvaluationRun) []step { return nil },
			wantStatus: platform.RunPending,
		},
		{
			name: "running",
			steps: func(r platform.EvaluationRun) []step {
				running := mustRun(r.Start(created.Add(time.Minute)))
				return []step{{r, running}}
			},
			wantStatus: platform.RunRunning,
		},
		{
			name: "completed",
			steps: func(r platform.EvaluationRun) []step {
				running := mustRun(r.Start(created.Add(time.Minute)))
				completed := mustRun(running.Complete(created.Add(2 * time.Minute)))
				return []step{{r, running}, {running, completed}}
			},
			wantStatus: platform.RunCompleted,
		},
		{
			name: "failed",
			steps: func(r platform.EvaluationRun) []step {
				running := mustRun(r.Start(created.Add(time.Minute)))
				failed := mustRun(running.Fail(created.Add(2*time.Minute), "engine unreachable"))
				return []step{{r, running}, {running, failed}}
			},
			wantStatus: platform.RunFailed,
			wantReason: "engine unreachable",
		},
		{
			name: "cancelled from pending",
			steps: func(r platform.EvaluationRun) []step {
				cancelled := mustRun(r.Cancel(created.Add(time.Minute)))
				return []step{{r, cancelled}}
			},
			wantStatus: platform.RunCancelled,
		},
		{
			name: "cancelled from running",
			steps: func(r platform.EvaluationRun) []step {
				running := mustRun(r.Start(created.Add(time.Minute)))
				cancelled := mustRun(running.Cancel(created.Add(2 * time.Minute)))
				return []step{{r, running}, {running, cancelled}}
			},
			wantStatus: platform.RunCancelled,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := storePath(t)
			ctx := t.Context()
			store := openStore(t, path)

			runID := platform.EvaluationRunID(fmt.Sprintf("run-%d", i))
			project, _ := platform.NewProject("proj-1", "Checkout")
			agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
			candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{})
			for _, err := range []error{
				store.CreateProject(ctx, project),
				store.CreateAgent(ctx, agent),
				store.CreateCandidate(ctx, candidate),
			} {
				if err != nil {
					t.Fatalf("seed error = %v", err)
				}
			}

			base, err := platform.NewEvaluationRun(runID, "cand-1", "staging", "profile-1", created)
			if err != nil {
				t.Fatalf("NewEvaluationRun() error = %v", err)
			}
			steps := tt.steps(base)
			want := base
			if len(steps) > 0 {
				want = steps[len(steps)-1].to
			}

			if err := store.CreateEvaluationRun(ctx, base); err != nil {
				t.Fatalf("CreateEvaluationRun() error = %v", err)
			}
			// Each step separately: the store's compare-and-swap only accepts
			// a legitimate single transition, which is the point of it.
			for _, step := range steps {
				if err := store.UpdateEvaluationRun(ctx, step.from, step.to); err != nil {
					t.Fatalf("UpdateEvaluationRun(%s -> %s) error = %v",
						step.from.Status(), step.to.Status(), err)
				}
			}
			store.Close()

			got, err := openStore(t, path).EvaluationRun(ctx, runID)
			if err != nil {
				t.Fatalf("EvaluationRun() after reopen error = %v", err)
			}

			if got.Status() != tt.wantStatus {
				t.Errorf("Status() = %q, want %q", got.Status(), tt.wantStatus)
			}
			if got.CandidateID() != want.CandidateID() ||
				got.Environment() != want.Environment() ||
				got.BehavioralProfile() != want.BehavioralProfile() {
				t.Error("run identity did not survive the round trip")
			}
			if got.FailureReason() != tt.wantReason {
				t.Errorf("FailureReason() = %q, want %q", got.FailureReason(), tt.wantReason)
			}
			for _, ts := range []struct {
				name      string
				got, want time.Time
			}{
				{"CreatedAt", got.CreatedAt(), want.CreatedAt()},
				{"StartedAt", got.StartedAt(), want.StartedAt()},
				{"FinishedAt", got.FinishedAt(), want.FinishedAt()},
			} {
				if !ts.got.Equal(ts.want) {
					t.Errorf("%s = %v, want %v", ts.name, ts.got, ts.want)
				}
				// Nanoseconds and the numeric offset both survive.
				if ts.got.Nanosecond() != ts.want.Nanosecond() {
					t.Errorf("%s lost nanosecond precision: %d vs %d",
						ts.name, ts.got.Nanosecond(), ts.want.Nanosecond())
				}
				if !ts.want.IsZero() {
					_, gotOffset := ts.got.Zone()
					_, wantOffset := ts.want.Zone()
					if gotOffset != wantOffset {
						t.Errorf("%s zone offset = %d, want %d (value was normalized)",
							ts.name, gotOffset, wantOffset)
					}
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// Run compare-and-swap
// ---------------------------------------------------------------------

func TestStaleRunUpdateConflicts(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")

	// Two callers load the same pending run.
	copyA, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	copyB, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}

	running, err := copyA.Start(aggEpoch.Add(time.Minute))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, copyA, running); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}

	// B still holds the pending value and tries a different transition.
	cancelled, err := copyB.Cancel(aggEpoch.Add(2 * time.Minute))
	if err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, copyB, cancelled); !errors.Is(err, platform.ErrStoreConflict) {
		t.Fatalf("stale update error = %v, want ErrStoreConflict", err)
	}

	stored, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if stored.Status() != platform.RunRunning {
		t.Errorf("Status() = %q, want running: B overwrote A's update", stored.Status())
	}
}

func TestTerminalRunCannotBeOverwritten(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")

	pending, _ := store.EvaluationRun(ctx, "run-1")
	running, _ := pending.Start(aggEpoch.Add(time.Minute))
	if err := store.UpdateEvaluationRun(ctx, pending, running); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}
	completed, _ := running.Complete(aggEpoch.Add(2 * time.Minute))
	if err := store.UpdateEvaluationRun(ctx, running, completed); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}

	// A caller holding the running value tries to fail an already-completed run.
	failed, err := running.Fail(aggEpoch.Add(3*time.Minute), "late failure")
	if err != nil {
		t.Fatalf("Fail() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, running, failed); !errors.Is(err, platform.ErrStoreConflict) {
		t.Fatalf("overwrite of terminal state error = %v, want ErrStoreConflict", err)
	}

	stored, _ := store.EvaluationRun(ctx, "run-1")
	if stored.Status() != platform.RunCompleted {
		t.Errorf("Status() = %q, want completed", stored.Status())
	}
}

func TestRunUpdateRefusesIdentityChange(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")
	seedHierarchy(t, store, "run-2", "cand-1", "staging", "profile-1")

	runOne, _ := store.EvaluationRun(ctx, "run-1")

	// A run pointing at a different environment is a different run, and an
	// update is the only place a caller could smuggle that through.
	other, err := platform.NewEvaluationRun("run-1", "cand-1", "production", "profile-1", aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	otherRunning, _ := other.Start(aggEpoch.Add(time.Minute))
	if err := store.UpdateEvaluationRun(ctx, runOne, otherRunning); !errors.Is(err, platform.ErrStoreConflict) {
		t.Errorf("environment change error = %v, want ErrStoreConflict", err)
	}
}

// ---------------------------------------------------------------------
// Evidence: the full chain after restart
// ---------------------------------------------------------------------

// storedEvidence builds one run's evidence through the public reducers.
func storedEvidence(t *testing.T, store *platform.SQLiteStore,
	runID platform.EvaluationRunID, candidateID platform.CandidateID,
	profile platform.BehavioralProfileRef, operations []string,
) (platform.EvaluationAggregate, platform.BehaviorSnapshot) {
	t.Helper()
	ctx := t.Context()
	run := seedHierarchy(t, store, runID, candidateID, "staging", profile)

	aggregate, err := platform.NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := platform.NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}

	for i, op := range operations {
		rec := scorecardRecord(fmt.Sprintf("%s-evt-%d", runID, i),
			"fp-"+op, op, "staging",
			"allow", "low", event.ApprovalNotRequired, 0.9)
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	snapshot := collector.Snapshot()
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	return aggregate, snapshot
}

// The most important test in this task: evidence persisted, the process
// restarted, and the entire derived chain still producing a verdict.
func TestEvidenceSurvivesRestartAndStillDrivesTheFullChain(t *testing.T) {
	path := storePath(t)
	ctx := t.Context()

	store := openStore(t, path)
	refAggregate, refSnapshot := storedEvidence(t, store, "run-ref", "cand-ref", "profile-ref",
		[]string{"read", "list"})
	canAggregate, canSnapshot := storedEvidence(t, store, "run-can", "cand-can", "profile-can",
		[]string{"read", "list", "write"})
	store.Close()

	reopened := openStore(t, path)

	loadedRefAgg, loadedRefSnap, err := reopened.EvaluationEvidence(ctx, "run-ref")
	if err != nil {
		t.Fatalf("EvaluationEvidence(run-ref) error = %v", err)
	}
	loadedCanAgg, loadedCanSnap, err := reopened.EvaluationEvidence(ctx, "run-can")
	if err != nil {
		t.Fatalf("EvaluationEvidence(run-can) error = %v", err)
	}

	t.Run("aggregate reconstructs exactly", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			got, want platform.EvaluationAggregate
		}{
			{"reference", loadedRefAgg, refAggregate},
			{"candidate", loadedCanAgg, canAggregate},
		} {
			if c.got.RunID() != c.want.RunID() || c.got.CandidateID() != c.want.CandidateID() ||
				c.got.Environment() != c.want.Environment() ||
				c.got.BehavioralProfile() != c.want.BehavioralProfile() {
				t.Errorf("%s: identity did not survive", c.name)
			}
			if c.got.RecordCount() != c.want.RecordCount() {
				t.Errorf("%s: RecordCount() = %d, want %d", c.name, c.got.RecordCount(), c.want.RecordCount())
			}
			if c.got.Decisions() != c.want.Decisions() || c.got.Risks() != c.want.Risks() ||
				c.got.Approvals() != c.want.Approvals() ||
				c.got.PolicySelection() != c.want.PolicySelection() {
				t.Errorf("%s: categorical counts did not survive", c.name)
			}
			if c.got.TrustScore() != c.want.TrustScore() ||
				c.got.IdentityConfidence() != c.want.IdentityConfidence() ||
				c.got.AnomalyScore() != c.want.AnomalyScore() ||
				c.got.AnomalyConfidence() != c.want.AnomalyConfidence() ||
				c.got.ContextRisk() != c.want.ContextRisk() {
				t.Errorf("%s: metric summaries did not survive", c.name)
			}
			if !c.got.FirstObservedAt().Equal(c.want.FirstObservedAt()) ||
				!c.got.LastObservedAt().Equal(c.want.LastObservedAt()) {
				t.Errorf("%s: observation timestamps did not survive", c.name)
			}
		}
	})

	t.Run("snapshot reconstructs exactly", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			got, want platform.BehaviorSnapshot
		}{
			{"reference", loadedRefSnap, refSnapshot},
			{"candidate", loadedCanSnap, canSnapshot},
		} {
			if c.got.ObservationCount() != c.want.ObservationCount() {
				t.Errorf("%s: ObservationCount() = %d, want %d",
					c.name, c.got.ObservationCount(), c.want.ObservationCount())
			}
			if c.got.Complete() != c.want.Complete() {
				t.Errorf("%s: Complete() = %v, want %v", c.name, c.got.Complete(), c.want.Complete())
			}
			gotEntries, wantEntries := c.got.Entries(), c.want.Entries()
			if len(gotEntries) != len(wantEntries) {
				t.Fatalf("%s: %d entries, want %d", c.name, len(gotEntries), len(wantEntries))
			}
			for i := range gotEntries {
				if gotEntries[i] != wantEntries[i] {
					t.Errorf("%s: entry %d = %+v, want %+v", c.name, i, gotEntries[i], wantEntries[i])
				}
				if i > 0 && gotEntries[i-1].FingerprintID >= gotEntries[i].FingerprintID {
					t.Errorf("%s: entries are not sorted by fingerprint id", c.name)
				}
			}
		}
	})

	// Persisting evidence is only worth anything if this still works.
	t.Run("the derived chain still runs", func(t *testing.T) {
		diff, err := platform.CompareBehaviorSnapshots(loadedRefSnap, loadedCanSnap)
		if err != nil {
			t.Fatalf("CompareBehaviorSnapshots() on loaded evidence error = %v", err)
		}
		if diff.AddedCount() != 1 {
			t.Errorf("AddedCount() = %d, want 1", diff.AddedCount())
		}

		card, err := platform.NewEvaluationScorecard(loadedRefAgg, loadedCanAgg, diff)
		if err != nil {
			t.Fatalf("NewEvaluationScorecard() on loaded evidence error = %v", err)
		}

		strict := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
			MaxAddedBehaviors:           0,
			MaxBlockDecisions:           math.MaxUint64,
			MaxCriticalRiskObservations: math.MaxUint64,
		})
		result, err := platform.EvaluateEvaluationGate(card, strict)
		if err != nil {
			t.Fatalf("EvaluateEvaluationGate() error = %v", err)
		}
		if result.Verdict() != platform.GateVerdictFail {
			t.Errorf("Verdict() = %q, want fail", result.Verdict())
		}
		if result.AddedBehaviors().Actual != 1 {
			t.Errorf("AddedBehaviors().Actual = %d, want 1", result.AddedBehaviors().Actual)
		}

		relaxed := platform.NewEvaluationGatePolicy(platform.EvaluationGateLimits{
			MaxAddedBehaviors:           1,
			MaxBlockDecisions:           math.MaxUint64,
			MaxCriticalRiskObservations: math.MaxUint64,
		})
		passed, err := platform.EvaluateEvaluationGate(card, relaxed)
		if err != nil {
			t.Fatalf("EvaluateEvaluationGate() error = %v", err)
		}
		if passed.Verdict() != platform.GateVerdictPass {
			t.Errorf("Verdict() = %q, want pass", passed.Verdict())
		}
	})
}

// ---------------------------------------------------------------------
// Bounded and incomplete snapshots
// ---------------------------------------------------------------------

func TestFullBoundedSnapshotSurvives(t *testing.T) {
	path := storePath(t)
	ctx := t.Context()
	store := openStore(t, path)

	run := seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")
	aggregate, _ := platform.NewEvaluationAggregate(run)
	collector, _ := platform.NewBehaviorCollector(run)

	const distinct = 512
	for i := range distinct {
		rec := scorecardRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%04d", i),
			fmt.Sprintf("op-%04d", i), "staging", "allow", "low", event.ApprovalNotRequired, 0.9)
		var err error
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	snapshot := collector.Snapshot()
	if snapshot.DistinctBehaviorCount() != distinct {
		t.Fatalf("precondition: DistinctBehaviorCount() = %d, want %d",
			snapshot.DistinctBehaviorCount(), distinct)
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	store.Close()

	_, loaded, err := openStore(t, path).EvaluationEvidence(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if loaded.DistinctBehaviorCount() != distinct {
		t.Errorf("DistinctBehaviorCount() = %d, want %d; a hidden database limit truncated it",
			loaded.DistinctBehaviorCount(), distinct)
	}
	entries := loaded.Entries()
	for i := 1; i < len(entries); i++ {
		if entries[i-1].FingerprintID >= entries[i].FingerprintID {
			t.Fatalf("entries are not sorted at index %d", i)
		}
	}
}

// Saturation is a fact about what was observed. Persistence records it and
// must never heal it.
func TestIncompleteSnapshotStaysIncompleteAcrossRestart(t *testing.T) {
	path := storePath(t)
	ctx := t.Context()
	store := openStore(t, path)

	run := seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")
	aggregate, _ := platform.NewEvaluationAggregate(run)
	collector, _ := platform.NewBehaviorCollector(run)

	// 513 distinct behaviors: the collector saturates on the last one.
	var saturated bool
	for i := range 513 {
		rec := scorecardRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%04d", i),
			fmt.Sprintf("op-%04d", i), "staging", "allow", "low", event.ApprovalNotRequired, 0.9)
		var err error
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			if !errors.Is(err, platform.ErrBehaviorCapacity) {
				t.Fatalf("Observe() error = %v", err)
			}
			saturated = true
		}
	}
	if !saturated {
		t.Fatal("precondition: the collector did not saturate")
	}

	snapshot := collector.Snapshot()
	if snapshot.Complete() {
		t.Fatal("precondition: snapshot reports complete after saturation")
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() rejected valid incomplete evidence: %v", err)
	}
	store.Close()

	_, loaded, err := openStore(t, path).EvaluationEvidence(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if loaded.Complete() {
		t.Error("Complete() = true after restart; persistence healed saturation")
	}
	if loaded.DistinctBehaviorCount() != 512 {
		t.Errorf("DistinctBehaviorCount() = %d, want 512", loaded.DistinctBehaviorCount())
	}

	// And task 054 still refuses to compare it.
	if _, err := platform.CompareBehaviorSnapshots(loaded, loaded); !errors.Is(err, platform.ErrIncompleteSnapshot) {
		t.Errorf("CompareBehaviorSnapshots() error = %v, want ErrIncompleteSnapshot", err)
	}
}

// ---------------------------------------------------------------------
// Evidence compatibility and staleness
// ---------------------------------------------------------------------

func TestEvidenceIdentityMismatchIsRefused(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()

	refAgg, refSnap := storedEvidence(t, store, "run-ref", "cand-ref", "profile-ref", []string{"read"})
	canAgg, canSnap := storedEvidence(t, store, "run-can", "cand-can", "profile-can", []string{"read"})

	// Aggregate from one run beside a snapshot from another.
	if err := store.SaveEvaluationEvidence(ctx, refAgg, canSnap); !errors.Is(err, platform.ErrStoreConflict) {
		t.Errorf("cross-run evidence error = %v, want ErrStoreConflict", err)
	}
	if err := store.SaveEvaluationEvidence(ctx, canAgg, refSnap); !errors.Is(err, platform.ErrStoreConflict) {
		t.Errorf("cross-run evidence error = %v, want ErrStoreConflict", err)
	}
}

func TestStaleEvidenceCannotOverwriteNewer(t *testing.T) {
	store := openStore(t, storePath(t))
	ctx := t.Context()
	run := seedHierarchy(t, store, "run-1", "cand-1", "staging", "profile-1")

	build := func(count int) (platform.EvaluationAggregate, platform.BehaviorSnapshot) {
		aggregate, _ := platform.NewEvaluationAggregate(run)
		collector, _ := platform.NewBehaviorCollector(run)
		for i := range count {
			rec := scorecardRecord(fmt.Sprintf("evt-%d", i), "fp-1", "read", "staging",
				"allow", "low", event.ApprovalNotRequired, 0.9)
			var err error
			if aggregate, err = aggregate.AddRecord(rec); err != nil {
				t.Fatalf("AddRecord() error = %v", err)
			}
			if err := collector.Observe(rec); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
		}
		return aggregate, collector.Snapshot()
	}

	newerAgg, newerSnap := build(5)
	if err := store.SaveEvaluationEvidence(ctx, newerAgg, newerSnap); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	t.Run("lower count is refused", func(t *testing.T) {
		olderAgg, olderSnap := build(3)
		if err := store.SaveEvaluationEvidence(ctx, olderAgg, olderSnap); !errors.Is(err, platform.ErrStoreConflict) {
			t.Fatalf("stale write error = %v, want ErrStoreConflict", err)
		}
		loaded, _, err := store.EvaluationEvidence(ctx, "run-1")
		if err != nil {
			t.Fatalf("EvaluationEvidence() error = %v", err)
		}
		if loaded.RecordCount() != 5 {
			t.Errorf("RecordCount() = %d, want 5; evidence moved backwards", loaded.RecordCount())
		}
	})

	t.Run("identical retry is idempotent", func(t *testing.T) {
		if err := store.SaveEvaluationEvidence(ctx, newerAgg, newerSnap); err != nil {
			t.Errorf("identical rewrite error = %v, want nil", err)
		}
	})

	t.Run("divergent evidence at the same count is refused", func(t *testing.T) {
		aggregate, _ := platform.NewEvaluationAggregate(run)
		collector, _ := platform.NewBehaviorCollector(run)
		// Same count, different decisions: two views of one moment.
		for i := range 5 {
			rec := scorecardRecord(fmt.Sprintf("other-%d", i), "fp-1", "read", "staging",
				"block", "critical", event.ApprovalDenied, 0.1)
			var err error
			if aggregate, err = aggregate.AddRecord(rec); err != nil {
				t.Fatalf("AddRecord() error = %v", err)
			}
			if err := collector.Observe(rec); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
		}
		if err := store.SaveEvaluationEvidence(ctx, aggregate, collector.Snapshot()); !errors.Is(err, platform.ErrStoreConflict) {
			t.Errorf("divergent evidence error = %v, want ErrStoreConflict", err)
		}
	})

	t.Run("higher count replaces", func(t *testing.T) {
		newestAgg, newestSnap := build(9)
		if err := store.SaveEvaluationEvidence(ctx, newestAgg, newestSnap); err != nil {
			t.Fatalf("SaveEvaluationEvidence() error = %v", err)
		}
		loaded, _, err := store.EvaluationEvidence(ctx, "run-1")
		if err != nil {
			t.Fatalf("EvaluationEvidence() error = %v", err)
		}
		if loaded.RecordCount() != 9 {
			t.Errorf("RecordCount() = %d, want 9", loaded.RecordCount())
		}
	})
}

// ---------------------------------------------------------------------
// SQL values stay data
// ---------------------------------------------------------------------

func TestCallerValuesRemainDataNotSyntax(t *testing.T) {
	path := storePath(t)
	ctx := t.Context()
	store := openStore(t, path)

	names := []string{
		`agent'; DROP TABLE platform_projects; --`,
		`O'Reilly agent`,
		`モデル-v2`,
		`"quoted" \backslash\ %percent%`,
	}

	for i, name := range names {
		projectID := platform.ProjectID(fmt.Sprintf("proj-%d", i))
		project, err := platform.NewProject(projectID, name)
		if err != nil {
			t.Fatalf("NewProject(%q) error = %v", name, err)
		}
		if err := store.CreateProject(ctx, project); err != nil {
			t.Fatalf("CreateProject() error = %v", err)
		}
	}
	store.Close()

	reopened := openStore(t, path)
	for i, name := range names {
		loaded, err := reopened.Project(ctx, platform.ProjectID(fmt.Sprintf("proj-%d", i)))
		if err != nil {
			t.Fatalf("Project() error = %v (a name changed SQL structure)", err)
		}
		if loaded.Name() != name {
			t.Errorf("Name() = %q, want %q", loaded.Name(), name)
		}
	}
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

func benchEvidenceStore(b *testing.B, behaviors int) (*platform.SQLiteStore,
	platform.EvaluationAggregate, platform.BehaviorSnapshot,
) {
	b.Helper()
	ctx := b.Context()
	store, err := platform.OpenSQLiteStore(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore() error = %v", err)
	}

	project, _ := platform.NewProject("proj-1", "Bench")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Bench agent")
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{})
	run, _ := platform.NewEvaluationRun("run-1", "cand-1", "staging", "profile-1", aggEpoch)
	for _, err := range []error{
		store.CreateProject(ctx, project),
		store.CreateAgent(ctx, agent),
		store.CreateCandidate(ctx, candidate),
		store.CreateEvaluationRun(ctx, run),
	} {
		if err != nil {
			b.Fatalf("seed error = %v", err)
		}
	}

	aggregate, _ := platform.NewEvaluationAggregate(run)
	collector, _ := platform.NewBehaviorCollector(run)
	for i := range behaviors {
		rec := scorecardRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%04d", i),
			fmt.Sprintf("op-%04d", i), "staging", "allow", "low", event.ApprovalNotRequired, 0.9)
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			b.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			b.Fatalf("Observe() error = %v", err)
		}
	}
	return store, aggregate, collector.Snapshot()
}

func benchmarkSave(b *testing.B, behaviors int) {
	store, aggregate, snapshot := benchEvidenceStore(b, behaviors)
	defer store.Close()
	ctx := b.Context()

	b.ReportAllocs()
	for b.Loop() {
		// Idempotent rewrite of the same evidence: the stale-write guard
		// accepts it, so the benchmark measures the write path.
		if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
			b.Fatalf("SaveEvaluationEvidence() error = %v", err)
		}
	}
}

func benchmarkLoad(b *testing.B, behaviors int) {
	store, aggregate, snapshot := benchEvidenceStore(b, behaviors)
	defer store.Close()
	ctx := b.Context()
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		b.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := store.EvaluationEvidence(ctx, "run-1"); err != nil {
			b.Fatalf("EvaluationEvidence() error = %v", err)
		}
	}
}

func BenchmarkSaveEvaluationEvidence32(b *testing.B)  { benchmarkSave(b, 32) }
func BenchmarkSaveEvaluationEvidence512(b *testing.B) { benchmarkSave(b, 512) }
func BenchmarkLoadEvaluationEvidence32(b *testing.B)  { benchmarkLoad(b, 32) }
func BenchmarkLoadEvaluationEvidence512(b *testing.B) { benchmarkLoad(b, 512) }

// ---------------------------------------------------------------------
// In-memory store isolation
// ---------------------------------------------------------------------

// Two stores opened with ":memory:" are two databases.
//
// A shared-cache DSN would make them one, and platform state written through
// one store would appear in an unrelated one — projects, runs and evidence
// crossing a boundary callers reasonably assume exists.
func TestIndependentMemoryStoresAreIsolated(t *testing.T) {
	ctx := t.Context()

	storeA, err := platform.OpenSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore(A) error = %v", err)
	}
	defer storeA.Close()

	storeB, err := platform.OpenSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore(B) error = %v", err)
	}
	defer storeB.Close()

	projectA, err := platform.NewProject("project-a", "Only in A")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	projectB, err := platform.NewProject("project-b", "Only in B")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := storeA.CreateProject(ctx, projectA); err != nil {
		t.Fatalf("CreateProject(A) error = %v", err)
	}
	if err := storeB.CreateProject(ctx, projectB); err != nil {
		t.Fatalf("CreateProject(B) error = %v", err)
	}

	t.Run("each store sees its own project", func(t *testing.T) {
		if _, err := storeA.Project(ctx, "project-a"); err != nil {
			t.Errorf("storeA.Project(project-a) error = %v", err)
		}
		if _, err := storeB.Project(ctx, "project-b"); err != nil {
			t.Errorf("storeB.Project(project-b) error = %v", err)
		}
	})

	t.Run("neither store sees the other's project", func(t *testing.T) {
		if _, err := storeA.Project(ctx, "project-b"); !errors.Is(err, platform.ErrStoreNotFound) {
			t.Errorf("storeA.Project(project-b) error = %v, want ErrStoreNotFound", err)
		}
		if _, err := storeB.Project(ctx, "project-a"); !errors.Is(err, platform.ErrStoreNotFound) {
			t.Errorf("storeB.Project(project-a) error = %v, want ErrStoreNotFound", err)
		}
	})

	// Not a projects-table quirk: the whole hierarchy and its evidence stay
	// on their own side.
	t.Run("runs and evidence do not cross", func(t *testing.T) {
		agent, err := platform.NewAgent("agent-a", "project-a", "A's agent")
		if err != nil {
			t.Fatalf("NewAgent() error = %v", err)
		}
		candidate, err := platform.NewCandidate("cand-a", "agent-a", platform.CandidateMetadata{Label: "v1"})
		if err != nil {
			t.Fatalf("NewCandidate() error = %v", err)
		}
		run, err := platform.NewEvaluationRun("run-a", "cand-a", "staging", "profile-a", aggEpoch)
		if err != nil {
			t.Fatalf("NewEvaluationRun() error = %v", err)
		}
		for _, err := range []error{
			storeA.CreateAgent(ctx, agent),
			storeA.CreateCandidate(ctx, candidate),
			storeA.CreateEvaluationRun(ctx, run),
		} {
			if err != nil {
				t.Fatalf("seed in A error = %v", err)
			}
		}

		aggregate, err := platform.NewEvaluationAggregate(run)
		if err != nil {
			t.Fatalf("NewEvaluationAggregate() error = %v", err)
		}
		collector, err := platform.NewBehaviorCollector(run)
		if err != nil {
			t.Fatalf("NewBehaviorCollector() error = %v", err)
		}
		rec := scorecardRecord("evt-0", "fp-0", "read", "staging",
			"allow", "low", event.ApprovalNotRequired, 0.9)
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
		if err := storeA.SaveEvaluationEvidence(ctx, aggregate, collector.Snapshot()); err != nil {
			t.Fatalf("SaveEvaluationEvidence(A) error = %v", err)
		}

		// A has all of it.
		if _, err := storeA.EvaluationRun(ctx, "run-a"); err != nil {
			t.Errorf("storeA.EvaluationRun() error = %v", err)
		}
		if _, _, err := storeA.EvaluationEvidence(ctx, "run-a"); err != nil {
			t.Errorf("storeA.EvaluationEvidence() error = %v", err)
		}

		// B has none of it.
		for _, c := range []struct {
			name string
			err  error
		}{
			{"Agent", func() error { _, err := storeB.Agent(ctx, "agent-a"); return err }()},
			{"Candidate", func() error { _, err := storeB.Candidate(ctx, "cand-a"); return err }()},
			{"EvaluationRun", func() error { _, err := storeB.EvaluationRun(ctx, "run-a"); return err }()},
			{"EvaluationEvidence", func() error { _, _, err := storeB.EvaluationEvidence(ctx, "run-a"); return err }()},
		} {
			if !errors.Is(c.err, platform.ErrStoreNotFound) {
				t.Errorf("storeB.%s() error = %v, want ErrStoreNotFound: state crossed stores", c.name, c.err)
			}
		}
	})
}

// Closing one private database must not disturb another.
func TestClosingOneMemoryStoreLeavesAnotherUsable(t *testing.T) {
	ctx := t.Context()

	storeA, err := platform.OpenSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore(A) error = %v", err)
	}
	storeB, err := platform.OpenSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore(B) error = %v", err)
	}
	defer storeB.Close()

	projectA, _ := platform.NewProject("project-a", "In A")
	projectB, _ := platform.NewProject("project-b", "In B")
	if err := storeA.CreateProject(ctx, projectA); err != nil {
		t.Fatalf("CreateProject(A) error = %v", err)
	}
	if err := storeB.CreateProject(ctx, projectB); err != nil {
		t.Fatalf("CreateProject(B) error = %v", err)
	}

	if err := storeA.Close(); err != nil {
		t.Fatalf("Close(A) error = %v", err)
	}

	loaded, err := storeB.Project(ctx, "project-b")
	if err != nil {
		t.Fatalf("storeB.Project() after closing A error = %v", err)
	}
	if loaded.Name() != "In B" {
		t.Errorf("Name() = %q, want %q", loaded.Name(), "In B")
	}

	// B is still writable, so closing A took nothing with it.
	another, _ := platform.NewProject("project-b2", "Also in B")
	if err := storeB.CreateProject(ctx, another); err != nil {
		t.Errorf("storeB.CreateProject() after closing A error = %v", err)
	}
}

// Many private databases at once, each seeing only its own state.
func TestConcurrentMemoryStoresStayIsolated(t *testing.T) {
	const stores = 8
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make([]error, stores)

	for i := range stores {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()

			ctx := context.Background()
			store, err := platform.OpenSQLiteStore(ctx, ":memory:")
			if err != nil {
				errs[i] = fmt.Errorf("open: %w", err)
				return
			}
			defer store.Close()

			mine := platform.ProjectID(fmt.Sprintf("project-%d", i))
			project, err := platform.NewProject(mine, "Mine")
			if err != nil {
				errs[i] = err
				return
			}
			if err := store.CreateProject(ctx, project); err != nil {
				errs[i] = fmt.Errorf("create: %w", err)
				return
			}
			if _, err := store.Project(ctx, mine); err != nil {
				errs[i] = fmt.Errorf("read own: %w", err)
				return
			}
			// Nobody else's project is visible here.
			for j := range stores {
				if j == i {
					continue
				}
				other := platform.ProjectID(fmt.Sprintf("project-%d", j))
				if _, err := store.Project(ctx, other); !errors.Is(err, platform.ErrStoreNotFound) {
					errs[i] = fmt.Errorf("saw %s: %v", other, err)
					return
				}
			}
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("store %d: %v", i, err)
		}
	}
}

// Isolation is not a reason to lose within-store reads: one open store is one
// database, and a write is visible to the next read on the same handle.
func TestMemoryStoreReadsItsOwnWrites(t *testing.T) {
	ctx := t.Context()
	store, err := platform.OpenSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	defer store.Close()

	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{Label: "v1"})
	run, _ := platform.NewEvaluationRun("run-1", "cand-1", "staging", "profile-1", aggEpoch)
	for _, err := range []error{
		store.CreateProject(ctx, project),
		store.CreateAgent(ctx, agent),
		store.CreateCandidate(ctx, candidate),
		store.CreateEvaluationRun(ctx, run),
	} {
		if err != nil {
			t.Fatalf("write error = %v", err)
		}
	}

	// Several reads in sequence, so a discarded connection would surface as a
	// vanished database rather than passing by luck.
	for range 5 {
		if _, err := store.Project(ctx, "proj-1"); err != nil {
			t.Fatalf("Project() error = %v", err)
		}
		if _, err := store.EvaluationRun(ctx, "run-1"); err != nil {
			t.Fatalf("EvaluationRun() error = %v", err)
		}
	}
}

// The isolation fix is specific to ":memory:". Two stores opened on the same
// file path are still two handles to one durable database, which is what
// makes a file the thing that survives a restart.
func TestFileBackedStoresOnOnePathShareOneDatabase(t *testing.T) {
	ctx := t.Context()
	path := storePath(t)

	storeA := openStore(t, path)
	storeB := openStore(t, path)

	project, err := platform.NewProject("shared-project", "Written through A")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if err := storeA.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject(A) error = %v", err)
	}

	loaded, err := storeB.Project(ctx, "shared-project")
	if err != nil {
		t.Fatalf("storeB.Project() error = %v: file-backed stores stopped sharing", err)
	}
	if loaded.Name() != "Written through A" {
		t.Errorf("Name() = %q, want %q", loaded.Name(), "Written through A")
	}

	// And the shared identity is real: B cannot re-create what A created.
	if err := storeB.CreateProject(ctx, project); !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Errorf("storeB.CreateProject() error = %v, want ErrStoreAlreadyExists", err)
	}
}
