package platform

// One persistence contract, every backend.
//
// This is Task 064's primary defence against drift. The interfaces say what a
// store must do; this says it in executable form, and both implementations run
// the identical body. A capability that works on one backend and not the other
// fails here rather than in a deployment.
//
// Only behaviour that exists today. There is no update-project, no delete and no
// list, so there are no cases for them — inventing CRUD to make the table look
// symmetric would test code that does not exist and imply an API that should
// not.
//
// The oracle is the current SQLite behaviour, because that is what callers
// already depend on.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// storeBackend is one implementation under test.
type storeBackend struct {
	name string
	open func(testing.TB) Store
}

// conformanceBackends returns every backend available in this environment.
//
// SQLite twice, because a file and ":memory:" are different enough to be worth
// both. PostgreSQL only when a DSN is configured — absent, it is skipped rather
// than faked, since a stand-in cannot demonstrate a row lock or a SQLSTATE.
func conformanceBackends(t *testing.T) []storeBackend {
	backends := []storeBackend{
		{
			name: "sqlite-file",
			open: func(tb testing.TB) Store {
				tb.Helper()
				store, err := OpenSQLiteStore(context.Background(),
					filepath.Join(tb.TempDir(), "platform.db"))
				if err != nil {
					tb.Fatalf("OpenSQLiteStore() error = %v", err)
				}
				tb.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
		{
			name: "sqlite-memory",
			open: func(tb testing.TB) Store {
				tb.Helper()
				store, err := OpenSQLiteStore(context.Background(), ":memory:")
				if err != nil {
					tb.Fatalf("OpenSQLiteStore() error = %v", err)
				}
				tb.Cleanup(func() { _ = store.Close() })
				return store
			},
		},
	}

	if dsn := conformancePostgresDSN(); dsn != "" {
		backends = append(backends, storeBackend{
			name: "postgres",
			open: func(tb testing.TB) Store {
				tb.Helper()
				store, err := OpenPostgresStore(context.Background(), PostgresConfig{
					DSN: isolatedSchemaDSN(tb, dsn),
				})
				if err != nil {
					tb.Fatalf("OpenPostgresStore() error = %v", err)
				}
				tb.Cleanup(func() { _ = store.Close() })
				return store
			},
		})
	}
	return backends
}

// conformancePostgresDSN reads the DSN without skipping, so the SQLite backends
// still run when PostgreSQL is absent.
func conformancePostgresDSN() string {
	return strings.TrimSpace(os.Getenv(postgresDSNEnv))
}

// TestStoreConformance runs the whole contract against every backend.
func TestStoreConformance(t *testing.T) {
	backends := conformanceBackends(t)
	if len(backends) < 2 {
		t.Fatal("no backends to test; the suite would pass vacuously")
	}

	for _, backend := range backends {
		t.Run(backend.name, func(t *testing.T) {
			t.Run("projects", func(t *testing.T) { conformProjects(t, backend.open) })
			t.Run("agents", func(t *testing.T) { conformAgents(t, backend.open) })
			t.Run("candidates", func(t *testing.T) { conformCandidates(t, backend.open) })
			t.Run("environments", func(t *testing.T) { conformEnvironments(t, backend.open) })
			t.Run("environment-cap", func(t *testing.T) { conformEnvironmentCap(t, backend.open) })
			t.Run("environment-paging", func(t *testing.T) { conformEnvironmentPaging(t, backend.open) })
			t.Run("environment-contention", func(t *testing.T) { conformEnvironmentContention(t, backend.open) })
			t.Run("runs", func(t *testing.T) { conformRuns(t, backend.open) })
			t.Run("lifecycle", func(t *testing.T) { conformLifecycle(t, backend.open) })
			t.Run("evidence", func(t *testing.T) { conformEvidence(t, backend.open) })
			t.Run("ingest", func(t *testing.T) { conformIngest(t, backend.open) })
			t.Run("timestamps", func(t *testing.T) { conformTimestamps(t, backend.open) })
			t.Run("cancellation", func(t *testing.T) { conformCancellation(t, backend.open) })
		})
	}
}

// ---------------------------------------------------------------------
// Control entities
// ---------------------------------------------------------------------

func conformProjects(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()

	project := mustProject(t, "proj-1", "Checkout")
	if err := store.CreateProject(ctx, project); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	got, err := store.Project(ctx, "proj-1")
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if got.ID() != "proj-1" || got.Name() != "Checkout" {
		t.Errorf("Project() = %+v, want id proj-1 name Checkout", got)
	}

	// Create means create. The stored value is left exactly as it was, which is
	// the case store.go documents this error for.
	if err := store.CreateProject(ctx, mustProject(t, "proj-1", "Renamed")); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate CreateProject() = %v, want ErrStoreAlreadyExists", err)
	}
	after, err := store.Project(ctx, "proj-1")
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if after.Name() != "Checkout" {
		t.Errorf("a refused duplicate changed the stored name to %q", after.Name())
	}

	if _, err := store.Project(ctx, "absent"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("Project(absent) = %v, want ErrStoreNotFound", err)
	}
}

func conformAgents(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()

	// A child under an unknown parent is a missing parent, not a malformed
	// child — the distinction store.go calls out.
	if err := store.CreateAgent(ctx, mustAgent(t, "agent-1", "no-such-project", "A")); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("CreateAgent under an absent project = %v, want ErrStoreNotFound", err)
	}

	if err := store.CreateProject(ctx, mustProject(t, "proj-1", "Checkout")); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := store.CreateAgent(ctx, mustAgent(t, "agent-1", "proj-1", "Deploy agent")); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}

	got, err := store.Agent(ctx, "agent-1")
	if err != nil {
		t.Fatalf("Agent() error = %v", err)
	}
	if got.ProjectID() != "proj-1" || got.Name() != "Deploy agent" {
		t.Errorf("Agent() = %+v", got)
	}

	if err := store.CreateAgent(ctx, mustAgent(t, "agent-1", "proj-1", "Other")); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate CreateAgent() = %v, want ErrStoreAlreadyExists", err)
	}
	if _, err := store.Agent(ctx, "absent"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("Agent(absent) = %v, want ErrStoreNotFound", err)
	}
}

func conformCandidates(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	seedParents(t, store)

	// All six metadata fields, each distinct, so a column swap is visible.
	metadata := CandidateMetadata{
		Label: "v2", SourceRef: "refs/heads/main",
		ArtifactDigest: "sha256:artifact", Model: "claude",
		ToolsetDigest: "sha256:toolset", ConfigDigest: "sha256:config",
	}
	if err := store.CreateCandidate(ctx, mustCandidate(t, "cand-1", "agent-1", metadata)); err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	got, err := store.Candidate(ctx, "cand-1")
	if err != nil {
		t.Fatalf("Candidate() error = %v", err)
	}
	if got.Metadata() != metadata {
		t.Errorf("metadata = %+v, want %+v", got.Metadata(), metadata)
	}

	// The case store.go singles out: the same CandidateID arriving with a
	// different artifact digest must not rewrite what a finished run was
	// evaluated against.
	other := metadata
	other.ArtifactDigest = "sha256:different"
	if err := store.CreateCandidate(ctx, mustCandidate(t, "cand-1", "agent-1", other)); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate CreateCandidate() = %v, want ErrStoreAlreadyExists", err)
	}
	unchanged, err := store.Candidate(ctx, "cand-1")
	if err != nil {
		t.Fatalf("Candidate() error = %v", err)
	}
	if unchanged.Metadata().ArtifactDigest != "sha256:artifact" {
		t.Errorf("a refused duplicate rewrote the artifact digest to %q",
			unchanged.Metadata().ArtifactDigest)
	}

	// Empty metadata is legal and must round-trip as empty, not as absent.
	if err := store.CreateCandidate(ctx, mustCandidate(t, "cand-empty", "agent-1", CandidateMetadata{})); err != nil {
		t.Fatalf("CreateCandidate(empty metadata) error = %v", err)
	}
	empty, err := store.Candidate(ctx, "cand-empty")
	if err != nil {
		t.Fatalf("Candidate() error = %v", err)
	}
	if empty.Metadata() != (CandidateMetadata{}) {
		t.Errorf("empty metadata round-tripped as %+v", empty.Metadata())
	}

	if err := store.CreateCandidate(ctx, mustCandidate(t, "cand-orphan", "no-such-agent", metadata)); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("CreateCandidate under an absent agent = %v, want ErrStoreNotFound", err)
	}
}

func conformRuns(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	seedParents(t, store)
	seedCandidate(t, store)

	created := conformanceEpoch()
	run := mustRun(t, "run-1", "cand-1", created)
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}

	got, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if got.CandidateID() != "cand-1" || got.Environment() != "staging" ||
		got.BehavioralProfile() != "profile-1" {
		t.Errorf("EvaluationRun() = %+v", got)
	}
	if !got.CreatedAt().Equal(created) {
		t.Errorf("CreatedAt() = %v, want %v", got.CreatedAt(), created)
	}
	// An unstarted run has no start or finish instant, stored as absent rather
	// than as a zero sentinel.
	if !got.StartedAt().IsZero() || !got.FinishedAt().IsZero() {
		t.Errorf("a created run reports StartedAt %v FinishedAt %v; both must be zero",
			got.StartedAt(), got.FinishedAt())
	}

	if err := store.CreateEvaluationRun(ctx, run); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate CreateEvaluationRun() = %v, want ErrStoreAlreadyExists", err)
	}
	if _, err := store.EvaluationRun(ctx, "absent"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("EvaluationRun(absent) = %v, want ErrStoreNotFound", err)
	}

	orphan := mustRun(t, "run-orphan", "no-such-candidate", created)
	if err := store.CreateEvaluationRun(ctx, orphan); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("CreateEvaluationRun under an absent candidate = %v, want ErrStoreNotFound", err)
	}
}

// ---------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------

func conformLifecycle(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	created := seedRunFor(t, store, "run-1")

	started, err := created.Start(created.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, created, started); err != nil {
		t.Fatalf("start error = %v", err)
	}

	running, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if running.Status() != RunRunning {
		t.Fatalf("Status() = %s, want %s", running.Status(), RunRunning)
	}
	if !running.StartedAt().Equal(started.StartedAt()) {
		t.Errorf("StartedAt() = %v, want %v", running.StartedAt(), started.StartedAt())
	}

	// A stale previous loses, whichever mechanism catches it.
	if err := store.UpdateEvaluationRun(ctx, created, started); !errors.Is(err, ErrStoreConflict) {
		t.Errorf("stale previous = %v, want ErrStoreConflict", err)
	}

	completed, err := running.Complete(running.StartedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, running, completed); err != nil {
		t.Fatalf("complete error = %v", err)
	}

	final, err := store.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if final.Status() != RunCompleted {
		t.Errorf("Status() = %s, want %s", final.Status(), RunCompleted)
	}
	if final.FinishedAt().IsZero() {
		t.Error("a completed run records no finish instant")
	}

	// Terminal is final: a further transition from the terminal state is
	// refused by the domain, and the store must not apply one.
	if _, err := final.Start(final.FinishedAt().Add(time.Second)); err == nil {
		t.Error("the domain allowed a transition out of a terminal state")
	}
}

// ---------------------------------------------------------------------
// Evidence
// ---------------------------------------------------------------------

func conformEvidence(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	running := startedRun(t, store, "run-1")

	// Absent evidence is not found, and the pair is never half-returned.
	if _, _, err := store.EvaluationEvidence(ctx, "run-1"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("EvaluationEvidence() with no evidence = %v, want ErrStoreNotFound", err)
	}

	aggregate, snapshot := conformanceEvidence(t, running, 3)
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	loadedAggregate, loadedSnapshot, err := store.EvaluationEvidence(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if !sameAggregate(loadedAggregate, aggregate) {
		t.Error("the aggregate did not round-trip")
	}
	if !sameSnapshot(loadedSnapshot, snapshot) {
		t.Error("the snapshot did not round-trip")
	}
	if loadedAggregate.RecordCount() != 3 {
		t.Errorf("RecordCount() = %d, want 3", loadedAggregate.RecordCount())
	}

	// An identical rewrite at the same count is idempotent.
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Errorf("identical rewrite = %v, want nil", err)
	}

	// Evidence never moves backwards.
	older, olderSnapshot := conformanceEvidence(t, running, 1)
	if err := store.SaveEvaluationEvidence(ctx, older, olderSnapshot); !errors.Is(err, ErrStoreConflict) {
		t.Errorf("a lower record count = %v, want ErrStoreConflict", err)
	}

	// Growing forward is fine, and replaces both halves together.
	newer, newerSnapshot := conformanceEvidence(t, running, 5)
	if err := store.SaveEvaluationEvidence(ctx, newer, newerSnapshot); err != nil {
		t.Fatalf("growing evidence = %v", err)
	}
	grown, grownSnapshot, err := store.EvaluationEvidence(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if grown.RecordCount() != 5 || grownSnapshot.ObservationCount() != 5 {
		t.Errorf("after growth: records %d observations %d, want 5 and 5",
			grown.RecordCount(), grownSnapshot.ObservationCount())
	}
	// Entries are replaced wholesale, not merged: a leftover from the
	// three-record write would make the set describe no single moment.
	if grownSnapshot.DistinctBehaviorCount() != 5 {
		t.Errorf("DistinctBehaviorCount() = %d, want 5", grownSnapshot.DistinctBehaviorCount())
	}
}

// ---------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------

func conformIngest(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	running := startedRun(t, store, "run-1")

	// A run with no cursor row starts at sequence 1.
	state, err := store.EvaluationIngestState(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.NextSequence() != 1 {
		t.Fatalf("initial NextSequence() = %d, want 1", state.NextSequence())
	}

	first := conformanceCommit(t, running, 1, 1, strings.Repeat("a", 64))
	result, err := store.CommitEvaluationIngest(ctx, first)
	if err != nil {
		t.Fatalf("CommitEvaluationIngest() error = %v", err)
	}
	if result.Disposition != EvaluationIngestCommitted {
		t.Errorf("Disposition = %v, want committed", result.Disposition)
	}
	if result.NextSequence != 2 {
		t.Errorf("NextSequence = %d, want 2", result.NextSequence)
	}

	// The same record again is a retry the protocol expects, not a failure.
	retry, err := store.CommitEvaluationIngest(ctx, first)
	if err != nil {
		t.Fatalf("identical retry = %v, want the already-committed disposition", err)
	}
	if retry.Disposition != EvaluationIngestAlreadyCommitted {
		t.Errorf("retry Disposition = %v, want already-committed", retry.Disposition)
	}

	// A gap is refused: sequence 3 when the cursor expects 2.
	gap := conformanceCommit(t, running, 3, 3, strings.Repeat("b", 64))
	if _, err := store.CommitEvaluationIngest(ctx, gap); !errors.Is(err, ErrIngestSequence) {
		t.Errorf("a sequence gap = %v, want ErrIngestSequence", err)
	}

	// Evidence and cursor advanced together, and only once.
	aggregate, _, err := store.EvaluationEvidence(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1", aggregate.RecordCount())
	}
	cursor, err := store.EvaluationIngestState(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if cursor.NextSequence() != 2 {
		t.Errorf("NextSequence() = %d, want 2", cursor.NextSequence())
	}
	if cursor.RecordCount() != 1 {
		t.Errorf("cursor RecordCount() = %d, want 1", cursor.RecordCount())
	}
}

// ---------------------------------------------------------------------
// Timestamps
// ---------------------------------------------------------------------

// conformTimestamps is where the two backends would diverge most easily.
//
// A native timestamp column normalizes the zone and truncates precision, and
// both are observable through /v1. This asserts neither happens, on whichever
// backend is running.
func conformTimestamps(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	seedParents(t, store)
	seedCandidate(t, store)

	// Non-UTC numeric offset, and a nanosecond value that is not a whole
	// microsecond.
	zone := time.FixedZone("+0545", 5*3600+45*60)
	created := time.Date(2026, 7, 4, 1, 2, 3, 123456789, zone)

	if err := store.CreateEvaluationRun(ctx, mustRun(t, "run-ts", "cand-1", created)); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	got, err := store.EvaluationRun(ctx, "run-ts")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}

	if !got.CreatedAt().Equal(created) {
		t.Errorf("instant changed: %v, want %v", got.CreatedAt(), created)
	}
	if got.CreatedAt().Nanosecond() != created.Nanosecond() {
		t.Errorf("nanoseconds = %d, want %d — a microsecond-precision column truncates here",
			got.CreatedAt().Nanosecond(), created.Nanosecond())
	}
	_, gotOffset := got.CreatedAt().Zone()
	_, wantOffset := created.Zone()
	if gotOffset != wantOffset {
		t.Errorf("zone offset = %d, want %d — a TIMESTAMPTZ column normalizes to UTC here",
			gotOffset, wantOffset)
	}
	// The rendered form is what /v1 publishes, so it must be byte-identical.
	if got, want := got.CreatedAt().Format(time.RFC3339Nano), created.Format(time.RFC3339Nano); got != want {
		t.Errorf("rendered = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------
// Cancellation
// ---------------------------------------------------------------------

func conformCancellation(t *testing.T, open func(testing.TB) Store) {
	store := open(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := store.CreateProject(cancelled, mustProject(t, "proj-cancelled", "Checkout"))
	if err == nil {
		t.Fatal("a cancelled context still performed the write")
	}
	// Cancellation is the caller's own decision, never a store condition.
	for _, wrong := range []error{ErrStoreConflict, ErrStoreCorrupt, ErrStoreAlreadyExists} {
		if errors.Is(err, wrong) {
			t.Errorf("cancellation was classified as %v", wrong)
		}
	}
	// And nothing was written.
	if _, err := store.Project(t.Context(), "proj-cancelled"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("a cancelled create left state behind: %v", err)
	}
}

// ---------------------------------------------------------------------
// Shared fixtures
// ---------------------------------------------------------------------

func conformanceEpoch() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}

func mustProject(t testing.TB, id ProjectID, name string) Project {
	t.Helper()
	project, err := NewProject(id, name)
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	return project
}

func mustAgent(t testing.TB, id AgentID, projectID ProjectID, name string) Agent {
	t.Helper()
	agent, err := NewAgent(id, projectID, name)
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	return agent
}

func mustCandidate(t testing.TB, id CandidateID, agentID AgentID, m CandidateMetadata) Candidate {
	t.Helper()
	candidate, err := NewCandidate(id, agentID, m)
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	return candidate
}

func mustRun(t testing.TB, id EvaluationRunID, candidateID CandidateID, created time.Time) EvaluationRun {
	t.Helper()
	run, err := NewEvaluationRun(id, candidateID, "staging", "profile-1", created)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	return run
}

func seedParents(t testing.TB, store Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.CreateProject(ctx, mustProject(t, "proj-1", "Checkout")); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateAgent(ctx, mustAgent(t, "agent-1", "proj-1", "Deploy agent")); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
}

func seedCandidate(t testing.TB, store Store) {
	t.Helper()
	if err := store.CreateCandidate(context.Background(),
		mustCandidate(t, "cand-1", "agent-1", CandidateMetadata{Label: "v1"})); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
}

// seedRunFor creates the whole chain and returns the created run.
func seedRunFor(t testing.TB, store Store, id EvaluationRunID) EvaluationRun {
	t.Helper()
	seedParents(t, store)
	seedCandidate(t, store)
	run := mustRun(t, id, "cand-1", conformanceEpoch())
	if err := store.CreateEvaluationRun(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

// startedRun creates the chain and moves the run to running.
func startedRun(t testing.TB, store Store, id EvaluationRunID) EvaluationRun {
	t.Helper()
	created := seedRunFor(t, store, id)
	started, err := created.Start(created.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(context.Background(), created, started); err != nil {
		t.Fatalf("start: %v", err)
	}
	return started
}

// conformanceEvidence builds an aggregate and snapshot over records
// observations, each with a distinct fingerprint.
func conformanceEvidence(t testing.TB, run EvaluationRun, records int) (EvaluationAggregate, BehaviorSnapshot) {
	t.Helper()
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range records {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
			fmt.Sprintf("op-%d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}
	return aggregate, collector.Snapshot()
}

// conformanceCommit builds a one-record commit at the given sequence.
func conformanceCommit(
	t testing.TB, run EvaluationRun, sequence, previousNext uint64, digest string,
) EvaluationIngestCommit {
	t.Helper()
	aggregate, snapshot := conformanceEvidence(t, run, 1)
	return EvaluationIngestCommit{
		Aggregate:            aggregate,
		Snapshot:             snapshot,
		Sequence:             sequence,
		PreviousNextSequence: previousNext,
		RecordDigest:         digest,
	}
}

// ---------------------------------------------------------------------
// Environments (task 065)
// ---------------------------------------------------------------------

func mustEnvironmentValue(t testing.TB, ref, project, name string) Environment {
	t.Helper()
	env, err := NewEnvironment(EnvironmentRef(ref), ProjectID(project), name)
	if err != nil {
		t.Fatalf("NewEnvironment() error = %v", err)
	}
	return env
}

// seedProject creates one project, for the environment cases that do not need
// an agent or a candidate.
func seedProject(t testing.TB, store Store, id string) {
	t.Helper()
	if err := store.CreateProject(context.Background(), mustProject(t, ProjectID(id), "P")); err != nil {
		t.Fatalf("seed project %s: %v", id, err)
	}
}

func conformEnvironments(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-2")

	// Create and read back, field for field, including the unranked case.
	env := mustEnvironmentValue(t, "staging", "proj-1", "Staging")
	if err := store.CreateEnvironment(ctx, env); err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	loaded, err := store.Environment(ctx, "proj-1", "staging")
	if err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if loaded.Ref() != "staging" || loaded.ProjectID() != "proj-1" ||
		loaded.Name() != "Staging" || loaded.Status() != EnvironmentActive ||
		loaded.Revision() != 1 {
		t.Errorf("loaded = %+v, want the created value", loaded)
	}
	if _, ranked := loaded.Rank(); ranked {
		t.Error("loaded environment is ranked; it was created without one")
	}

	// A ranked one round-trips its rank, including rank 0.
	for _, rank := range []uint16{0, 30} {
		ref := fmt.Sprintf("ranked-%d", rank)
		ranked, err := NewRankedEnvironment(EnvironmentRef(ref), "proj-1", "R", rank)
		if err != nil {
			t.Fatalf("NewRankedEnvironment() error = %v", err)
		}
		if err := store.CreateEnvironment(ctx, ranked); err != nil {
			t.Fatalf("CreateEnvironment(%s) error = %v", ref, err)
		}
		back, err := store.Environment(ctx, "proj-1", EnvironmentRef(ref))
		if err != nil {
			t.Fatalf("Environment(%s) error = %v", ref, err)
		}
		got, isRanked := back.Rank()
		if !isRanked || got != rank {
			t.Errorf("%s rank = (%d, %t), want (%d, true)", ref, got, isRanked, rank)
		}
	}

	// Duplicate identity, with the stored value untouched.
	other := mustEnvironmentValue(t, "staging", "proj-1", "Different name")
	if err := store.CreateEnvironment(ctx, other); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate create error = %v, want ErrStoreAlreadyExists", err)
	}
	if again, _ := store.Environment(ctx, "proj-1", "staging"); again.Name() != "Staging" {
		t.Errorf("a refused create rewrote the stored name to %q", again.Name())
	}

	// The same ref in another project is another environment.
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "staging", "proj-2", "Other staging")); err != nil {
		t.Fatalf("same ref in a second project error = %v, want it accepted", err)
	}
	second, err := store.Environment(ctx, "proj-2", "staging")
	if err != nil {
		t.Fatalf("Environment(proj-2) error = %v", err)
	}
	if second.Name() != "Other staging" {
		t.Errorf("proj-2 staging name = %q; the two projects share a row", second.Name())
	}

	// Missing parent, missing environment.
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "staging", "proj-absent", "X")); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("create under a missing project error = %v, want ErrStoreNotFound", err)
	}
	if _, err := store.Environment(ctx, "proj-1", "absent"); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("Environment(absent) error = %v, want ErrStoreNotFound", err)
	}

	// Compare-and-swap: the current revision wins, a stale one does not.
	current, _ := store.Environment(ctx, "proj-1", "staging")
	renamed, err := current.Rename("Staging EU")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if err := store.UpdateEnvironment(ctx, current, renamed); err != nil {
		t.Fatalf("UpdateEnvironment() error = %v", err)
	}
	stale, err := current.Rename("Written by a stale caller")
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if err := store.UpdateEnvironment(ctx, current, stale); !errors.Is(err, ErrStoreConflict) {
		t.Errorf("stale update error = %v, want ErrStoreConflict", err)
	}
	after, _ := store.Environment(ctx, "proj-1", "staging")
	if after.Name() != "Staging EU" || after.Revision() != 2 {
		t.Errorf("after a refused stale update: name %q revision %d, want Staging EU / 2",
			after.Name(), after.Revision())
	}

	// Identity cannot move.
	moved := mustEnvironmentValue(t, "renamed-ref", "proj-1", "X")
	if err := store.UpdateEnvironment(ctx, after, moved); !errors.Is(err, ErrInvalidID) {
		t.Errorf("update changing the ref error = %v, want ErrInvalidID", err)
	}
	crossProject := mustEnvironmentValue(t, "staging", "proj-2", "X")
	if err := store.UpdateEnvironment(ctx, after, crossProject); !errors.Is(err, ErrInvalidID) {
		t.Errorf("update changing the project error = %v, want ErrInvalidID", err)
	}

	// Archive and reactivate survive a round trip.
	archived, _ := after.Archive()
	if err := store.UpdateEnvironment(ctx, after, archived); err != nil {
		t.Fatalf("archive error = %v", err)
	}
	if back, _ := store.Environment(ctx, "proj-1", "staging"); back.Status() != EnvironmentArchived {
		t.Errorf("status = %q, want archived", back.Status())
	}
	reactivated, _ := archived.Activate()
	if err := store.UpdateEnvironment(ctx, archived, reactivated); err != nil {
		t.Fatalf("activate error = %v", err)
	}
	if back, _ := store.Environment(ctx, "proj-1", "staging"); back.Status() != EnvironmentActive {
		t.Errorf("status = %q, want active", back.Status())
	}
}

// conformEnvironmentCap covers the creation cap and — the part that is easy to
// get backwards — its interaction with identity.
func conformEnvironmentCap(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-2")

	for i := range maxProjectEnvironments {
		ref := fmt.Sprintf("env-%03d", i)
		if err := store.CreateEnvironment(ctx,
			mustEnvironmentValue(t, ref, "proj-1", "E")); err != nil {
			t.Fatalf("create %s error = %v; the first %d must fit",
				ref, err, maxProjectEnvironments)
		}
	}

	// One more new ref is refused.
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "one-too-many", "proj-1", "E")); !errors.Is(err, ErrEnvironmentLimit) {
		t.Errorf("create past the cap error = %v, want ErrEnvironmentLimit", err)
	}

	// Identity before cap: re-creating a ref the project already has is a
	// duplicate, never a limit. This is the assertion that fails if the two
	// checks are ever reordered, and it needs no concurrency to catch it.
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "env-000", "proj-1", "E")); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Errorf("duplicate at the cap error = %v, want ErrStoreAlreadyExists", err)
	}

	// The cap is per project.
	if err := store.CreateEnvironment(ctx,
		mustEnvironmentValue(t, "env-000", "proj-2", "E")); err != nil {
		t.Errorf("create in a second project error = %v; the cap is per project", err)
	}
}

func conformEnvironmentPaging(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-empty")

	// A project that does not exist, and one that exists with nothing in it,
	// are different answers.
	if _, err := store.ProjectEnvironments(ctx, "proj-absent", "", 10); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("list for a missing project error = %v, want ErrStoreNotFound", err)
	}
	empty, err := store.ProjectEnvironments(ctx, "proj-empty", "", 10)
	if err != nil {
		t.Fatalf("list for an empty project error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("empty project returned %d environments", len(empty))
	}

	// Refs whose byte order differs from a locale-aware collation: "Zulu"
	// sorts before "alpha" by byte value and after it in many locales, which
	// is what proves COLLATE "C" is doing its job on PostgreSQL.
	refs := []string{"Zulu", "alpha", "_under", "beta"}
	for _, ref := range refs {
		if err := store.CreateEnvironment(ctx,
			mustEnvironmentValue(t, ref, "proj-1", "E")); err != nil {
			t.Fatalf("create %s error = %v", ref, err)
		}
	}
	page, err := store.ProjectEnvironments(ctx, "proj-1", "", 10)
	if err != nil {
		t.Fatalf("ProjectEnvironments() error = %v", err)
	}
	want := []string{"Zulu", "_under", "alpha", "beta"} // byte order
	got := make([]string, 0, len(page))
	for _, env := range page {
		got = append(got, string(env.Ref()))
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v (byte order, not locale order)", got, want)
	}

	// The cursor is exclusive and need not name an existing row.
	after, err := store.ProjectEnvironments(ctx, "proj-1", "_under", 10)
	if err != nil {
		t.Fatalf("ProjectEnvironments(after) error = %v", err)
	}
	if len(after) != 2 || after[0].Ref() != "alpha" {
		t.Errorf("after=_under returned %d rows starting at %q, want 2 starting at alpha",
			len(after), after[0].Ref())
	}
	between, err := store.ProjectEnvironments(ctx, "proj-1", "aaa", 10)
	if err != nil {
		t.Fatalf("ProjectEnvironments(nonexistent cursor) error = %v", err)
	}
	if len(between) != 2 {
		t.Errorf("a cursor naming no row returned %d rows, want the 2 that sort after it", len(between))
	}

	// The limit is honoured and bounded.
	limited, err := store.ProjectEnvironments(ctx, "proj-1", "", 2)
	if err != nil {
		t.Fatalf("ProjectEnvironments(limit 2) error = %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("limit 2 returned %d rows", len(limited))
	}
	for _, limit := range []int{0, -1, maxEnvironmentFetch + 1} {
		if _, err := store.ProjectEnvironments(ctx, "proj-1", "", limit); !errors.Is(err, ErrInvalidID) {
			t.Errorf("limit %d error = %v, want it refused", limit, err)
		}
	}

	// Renaming, re-ranking and archiving between pages moves nothing: the
	// traversal key is the immutable ref.
	first, err := store.ProjectEnvironments(ctx, "proj-1", "", 2)
	if err != nil {
		t.Fatalf("first page error = %v", err)
	}
	target, _ := store.Environment(ctx, "proj-1", "beta")
	ranked, _ := target.WithRank(1)
	if err := store.UpdateEnvironment(ctx, target, ranked); err != nil {
		t.Fatalf("re-rank between pages error = %v", err)
	}
	archivedTarget, _ := ranked.Archive()
	if err := store.UpdateEnvironment(ctx, ranked, archivedTarget); err != nil {
		t.Fatalf("archive between pages error = %v", err)
	}
	second, err := store.ProjectEnvironments(ctx, "proj-1", first[len(first)-1].Ref(), 2)
	if err != nil {
		t.Fatalf("second page error = %v", err)
	}
	seen := append([]string{}, string(first[0].Ref()), string(first[1].Ref()))
	for _, env := range second {
		seen = append(seen, string(env.Ref()))
	}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Errorf("paged traversal saw %v, want %v — a mutable field moved a row", seen, want)
	}

	// An archived environment still lists: the collection is configuration,
	// not a work queue.
	if len(second) != 2 {
		t.Errorf("second page returned %d rows, want 2 including the archived one", len(second))
	}
}

// conformEnvironmentContention is the cap under concurrent writers, stated as
// four races because the answer depends on both the ref and the count.
func conformEnvironmentContention(t *testing.T, open func(testing.TB) Store) {
	// Each race is its own store, so a failure names one scenario.
	race := func(t *testing.T, seed int, refs [2]string) (successes, duplicates, limits int, total int) {
		t.Helper()
		ctx := context.Background()
		store := open(t)
		seedProject(t, store, "proj-1")
		for i := range seed {
			if err := store.CreateEnvironment(ctx,
				mustEnvironmentValue(t, fmt.Sprintf("seed-%03d", i), "proj-1", "E")); err != nil {
				t.Fatalf("seeding %d error = %v", i, err)
			}
		}

		start := make(chan struct{})
		results := make(chan error, 2)
		for _, ref := range refs {
			go func(ref string) {
				env, err := NewEnvironment(EnvironmentRef(ref), "proj-1", "Racing")
				if err != nil {
					results <- err
					return
				}
				<-start
				results <- store.CreateEnvironment(ctx, env)
			}(ref)
		}
		close(start)

		for range 2 {
			switch err := <-results; {
			case err == nil:
				successes++
			case errors.Is(err, ErrStoreAlreadyExists):
				duplicates++
			case errors.Is(err, ErrEnvironmentLimit):
				limits++
			default:
				t.Fatalf("unexpected create error = %v", err)
			}
		}

		// Count what actually landed, in pages, since a migrated-size project
		// would not fit one.
		after := EnvironmentRef("")
		for {
			page, err := store.ProjectEnvironments(ctx, "proj-1", after, MaxEnvironmentPage)
			if err != nil {
				t.Fatalf("counting environments error = %v", err)
			}
			total += len(page)
			if len(page) < MaxEnvironmentPage {
				break
			}
			after = page[len(page)-1].Ref()
		}
		return successes, duplicates, limits, total
	}

	cap := maxProjectEnvironments

	t.Run("below the cap, distinct refs", func(t *testing.T) {
		successes, duplicates, limits, total := race(t, cap-1, [2]string{"new-a", "new-b"})
		if successes != 1 || limits != 1 || duplicates != 0 {
			t.Errorf("successes=%d duplicates=%d limits=%d, want 1/0/1",
				successes, duplicates, limits)
		}
		if total != cap {
			t.Errorf("final count = %d, want %d — two writers must not both fit", total, cap)
		}
	})

	t.Run("below the cap, same absent ref", func(t *testing.T) {
		successes, duplicates, limits, total := race(t, cap-1, [2]string{"new-a", "new-a"})
		if successes != 1 || duplicates != 1 || limits != 0 {
			t.Errorf("successes=%d duplicates=%d limits=%d, want 1/1/0 — "+
				"the loser found the row, not the cap", successes, duplicates, limits)
		}
		if total != cap {
			t.Errorf("final count = %d, want %d", total, cap)
		}
	})

	t.Run("at the cap, same absent ref", func(t *testing.T) {
		successes, duplicates, limits, total := race(t, cap, [2]string{"new-a", "new-a"})
		if successes != 0 || limits != 2 || duplicates != 0 {
			t.Errorf("successes=%d duplicates=%d limits=%d, want 0/0/2 — "+
				"sharing a name creates no room", successes, duplicates, limits)
		}
		if total != cap {
			t.Errorf("final count = %d, want %d", total, cap)
		}
	})

	t.Run("at the cap, distinct refs", func(t *testing.T) {
		successes, duplicates, limits, total := race(t, cap, [2]string{"new-a", "new-b"})
		if successes != 0 || limits != 2 || duplicates != 0 {
			t.Errorf("successes=%d duplicates=%d limits=%d, want 0/0/2",
				successes, duplicates, limits)
		}
		if total != cap {
			t.Errorf("final count = %d, want %d", total, cap)
		}
	})
}
