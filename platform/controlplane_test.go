package platform_test

// Task 058: the control-plane service.
//
// Most of this file is about one question: what happens when the same record
// arrives twice. Task 053 made a duplicate count twice on purpose, so the
// retry contract has to live somewhere, and these tests are where it is
// pinned — expected applies, an identical retry of the last record replays,
// and every other shape fails closed.
//
// The rest pins the boundaries a transport must not be able to talk past:
// ingest only while running, profile bound to the run, comparison only over
// completed evidence.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// controlPlaneFixture is a service over a real SQLite store.
type controlPlaneFixture struct {
	plane *platform.ControlPlane
	store *platform.SQLiteStore
	path  string
}

func newFixture(t *testing.T) *controlPlaneFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.db")
	return openFixture(t, path)
}

func openFixture(t *testing.T, path string) *controlPlaneFixture {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	return &controlPlaneFixture{plane: plane, store: store, path: path}
}

const (
	fixtureProfile     platform.BehavioralProfileRef = "profile-1"
	fixtureEnvironment platform.EnvironmentRef       = "staging"
)

// seedRunning creates the hierarchy and leaves the run in Running.
func (f *controlPlaneFixture) seedRunning(t *testing.T, runID platform.EvaluationRunID) {
	t.Helper()
	f.seedPending(t, runID)
	if _, err := f.plane.StartEvaluationRun(t.Context(), runID, aggEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
}

func (f *controlPlaneFixture) seedPending(t *testing.T, runID platform.EvaluationRunID) {
	t.Helper()
	ctx := t.Context()

	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{Label: "v1"})

	for _, err := range []error{
		f.plane.CreateProject(ctx, project),
		f.plane.CreateAgent(ctx, agent),
		f.plane.CreateCandidate(ctx, candidate),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			t.Fatalf("seed error = %v", err)
		}
	}

	run, err := platform.NewEvaluationRun(runID, "cand-1", fixtureEnvironment, fixtureProfile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
}

// ingestRecord is a valid record for one behavioral shape.
func ingestRecord(eventID, fingerprintID, operation string) trustvian.DecisionRecord {
	return scorecardRecord(eventID, fingerprintID, operation, fixtureEnvironment,
		"allow", "low", event.ApprovalNotRequired, 0.9)
}

func (f *controlPlaneFixture) ingest(
	t *testing.T, runID platform.EvaluationRunID, sequence uint64, record trustvian.DecisionRecord,
) (platform.IngestResult, error) {
	t.Helper()
	return f.plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
		RunID:             runID,
		Sequence:          sequence,
		BehavioralProfile: fixtureProfile,
		Record:            record,
	})
}

// ---------------------------------------------------------------------
// Hierarchy and lifecycle
// ---------------------------------------------------------------------

func TestControlPlaneCreatesAndReadsHierarchy(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.seedPending(t, "run-1")

	if _, err := f.plane.Project(ctx, "proj-1"); err != nil {
		t.Errorf("Project() error = %v", err)
	}
	if _, err := f.plane.Agent(ctx, "agent-1"); err != nil {
		t.Errorf("Agent() error = %v", err)
	}
	if _, err := f.plane.Candidate(ctx, "cand-1"); err != nil {
		t.Errorf("Candidate() error = %v", err)
	}
	run, err := f.plane.EvaluationRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if run.Status() != platform.RunPending {
		t.Errorf("Status() = %q, want pending", run.Status())
	}
}

func TestControlPlaneLifecycleTransitions(t *testing.T) {
	tests := []struct {
		name  string
		drive func(t *testing.T, f *controlPlaneFixture, id platform.EvaluationRunID) (platform.EvaluationRun, error)
		want  platform.RunStatus
	}{
		{
			name: "pending to running to completed",
			drive: func(t *testing.T, f *controlPlaneFixture, id platform.EvaluationRunID) (platform.EvaluationRun, error) {
				if _, err := f.plane.StartEvaluationRun(t.Context(), id, aggEpoch.Add(time.Minute)); err != nil {
					return platform.EvaluationRun{}, err
				}
				return f.plane.CompleteEvaluationRun(t.Context(), id, aggEpoch.Add(2*time.Minute))
			},
			want: platform.RunCompleted,
		},
		{
			name: "pending to cancelled",
			drive: func(t *testing.T, f *controlPlaneFixture, id platform.EvaluationRunID) (platform.EvaluationRun, error) {
				return f.plane.CancelEvaluationRun(t.Context(), id, aggEpoch.Add(time.Minute))
			},
			want: platform.RunCancelled,
		},
		{
			name: "running to failed",
			drive: func(t *testing.T, f *controlPlaneFixture, id platform.EvaluationRunID) (platform.EvaluationRun, error) {
				if _, err := f.plane.StartEvaluationRun(t.Context(), id, aggEpoch.Add(time.Minute)); err != nil {
					return platform.EvaluationRun{}, err
				}
				return f.plane.FailEvaluationRun(t.Context(), id, aggEpoch.Add(2*time.Minute), "engine unreachable")
			},
			want: platform.RunFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.seedPending(t, "run-1")

			run, err := tt.drive(t, f, "run-1")
			if err != nil {
				t.Fatalf("drive error = %v", err)
			}
			if run.Status() != tt.want {
				t.Errorf("Status() = %q, want %q", run.Status(), tt.want)
			}

			stored, err := f.plane.EvaluationRun(t.Context(), "run-1")
			if err != nil {
				t.Fatalf("EvaluationRun() error = %v", err)
			}
			if stored.Status() != tt.want {
				t.Errorf("stored Status() = %q, want %q", stored.Status(), tt.want)
			}
		})
	}
}

// The domain decides what is legal; the service does not reimplement it.
func TestControlPlaneRefusesInvalidTransition(t *testing.T) {
	f := newFixture(t)
	f.seedPending(t, "run-1")

	// Completing a run that never started.
	if _, err := f.plane.CompleteEvaluationRun(t.Context(), "run-1", aggEpoch.Add(time.Minute)); !errors.Is(err, platform.ErrEvaluationState) {
		t.Fatalf("CompleteEvaluationRun() error = %v, want ErrEvaluationState", err)
	}
}

// ---------------------------------------------------------------------
// Ingest preconditions
// ---------------------------------------------------------------------

func TestIngestRequiresRunningRun(t *testing.T) {
	tests := []struct {
		name  string
		drive func(t *testing.T, f *controlPlaneFixture)
	}{
		{"pending", func(t *testing.T, f *controlPlaneFixture) {}},
		{"completed", func(t *testing.T, f *controlPlaneFixture) {
			mustStart(t, f, "run-1")
			if _, err := f.plane.CompleteEvaluationRun(t.Context(), "run-1", aggEpoch.Add(2*time.Minute)); err != nil {
				t.Fatalf("complete: %v", err)
			}
		}},
		{"failed", func(t *testing.T, f *controlPlaneFixture) {
			mustStart(t, f, "run-1")
			if _, err := f.plane.FailEvaluationRun(t.Context(), "run-1", aggEpoch.Add(2*time.Minute), "boom"); err != nil {
				t.Fatalf("fail: %v", err)
			}
		}},
		{"cancelled", func(t *testing.T, f *controlPlaneFixture) {
			if _, err := f.plane.CancelEvaluationRun(t.Context(), "run-1", aggEpoch.Add(time.Minute)); err != nil {
				t.Fatalf("cancel: %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			f.seedPending(t, "run-1")
			tt.drive(t, f)

			_, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read"))
			if !errors.Is(err, platform.ErrEvaluationState) {
				t.Fatalf("ingest error = %v, want ErrEvaluationState", err)
			}

			// Nothing advanced.
			state, err := f.plane.EvaluationIngestState(t.Context(), "run-1")
			if err != nil {
				t.Fatalf("EvaluationIngestState() error = %v", err)
			}
			if state.NextSequence() != 1 {
				t.Errorf("NextSequence() = %d, want 1", state.NextSequence())
			}
		})
	}
}

func mustStart(t *testing.T, f *controlPlaneFixture, id platform.EvaluationRunID) {
	t.Helper()
	if _, err := f.plane.StartEvaluationRun(t.Context(), id, aggEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
}

// The profile rides beside the record because DecisionRecord carries no
// learning scope. Binding it to the run is what stops evidence from one
// scope being folded into an evaluation of another.
func TestIngestRequiresMatchingBehavioralProfile(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	_, err := f.plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
		RunID:             "run-1",
		Sequence:          1,
		BehavioralProfile: "some-other-profile",
		Record:            ingestRecord("evt-0", "fp-0", "read"),
	})
	if err == nil {
		t.Fatal("a record from another profile was accepted")
	}

	state, _ := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if state.NextSequence() != 1 {
		t.Errorf("NextSequence() = %d, want 1: a rejected record advanced the cursor", state.NextSequence())
	}
	if _, _, err := f.store.EvaluationEvidence(t.Context(), "run-1"); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("evidence exists after a rejected ingest: %v", err)
	}
}

// ---------------------------------------------------------------------
// The sequence protocol
// ---------------------------------------------------------------------

func TestFirstIngestStartsAtSequenceOne(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	state, err := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.NextSequence() != 1 {
		t.Fatalf("NextSequence() = %d, want 1", state.NextSequence())
	}

	result, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read"))
	if err != nil {
		t.Fatalf("ingest error = %v", err)
	}
	if result.Disposition != platform.IngestApplied {
		t.Errorf("Disposition = %q, want applied", result.Disposition)
	}
	if result.RecordCount != 1 || result.NextSequence != 2 {
		t.Errorf("RecordCount/NextSequence = %d/%d, want 1/2", result.RecordCount, result.NextSequence)
	}
	if !result.BehaviorComplete {
		t.Error("BehaviorComplete = false on a fresh run")
	}
}

func TestOrderedIngestAccumulates(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	for i := range 3 {
		result, err := f.ingest(t, "run-1", uint64(i+1),
			ingestRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op-%d", i)))
		if err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
		if result.RecordCount != uint64(i+1) {
			t.Errorf("RecordCount = %d, want %d", result.RecordCount, i+1)
		}
	}

	state, _ := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if state.NextSequence() != 4 {
		t.Errorf("NextSequence() = %d, want 4", state.NextSequence())
	}
}

// The whole point of the protocol: a genuine retry must not count twice.
func TestRetryOfLastRecordReplaysWithoutDoubleCounting(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	record := ingestRecord("evt-0", "fp-0", "read")
	if _, err := f.ingest(t, "run-1", 1, record); err != nil {
		t.Fatalf("first ingest error = %v", err)
	}

	result, err := f.ingest(t, "run-1", 1, record)
	if err != nil {
		t.Fatalf("retry error = %v, want a replay", err)
	}
	if result.Disposition != platform.IngestReplayed {
		t.Errorf("Disposition = %q, want replayed", result.Disposition)
	}
	if result.RecordCount != 1 {
		t.Errorf("RecordCount = %d, want 1: the retry was counted again", result.RecordCount)
	}

	aggregate, _, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("stored RecordCount() = %d, want 1", aggregate.RecordCount())
	}
}

// Two different records cannot claim one position.
func TestSameSequenceWithDifferentRecordConflicts(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("first ingest error = %v", err)
	}

	_, err := f.ingest(t, "run-1", 1, ingestRecord("evt-other", "fp-1", "write"))
	if !errors.Is(err, platform.ErrIngestSequence) {
		t.Fatalf("error = %v, want ErrIngestSequence", err)
	}

	aggregate, _, _ := f.store.EvaluationEvidence(t.Context(), "run-1")
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1", aggregate.RecordCount())
	}
}

func TestSequenceGapConflicts(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("first ingest error = %v", err)
	}

	// Expected 2, sent 3.
	_, err := f.ingest(t, "run-1", 3, ingestRecord("evt-2", "fp-2", "exec"))
	if !errors.Is(err, platform.ErrIngestSequence) {
		t.Fatalf("error = %v, want ErrIngestSequence", err)
	}

	state, _ := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if state.NextSequence() != 2 {
		t.Errorf("NextSequence() = %d, want 2", state.NextSequence())
	}
}

func TestStaleSequenceConflicts(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	for i := range 3 {
		if _, err := f.ingest(t, "run-1", uint64(i+1),
			ingestRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op-%d", i))); err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
	}

	// Two steps back is not replayable: only the last record's digest is kept.
	_, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "op-0"))
	if !errors.Is(err, platform.ErrIngestSequence) {
		t.Fatalf("error = %v, want ErrIngestSequence", err)
	}

	aggregate, _, _ := f.store.EvaluationEvidence(t.Context(), "run-1")
	if aggregate.RecordCount() != 3 {
		t.Errorf("RecordCount() = %d, want 3", aggregate.RecordCount())
	}
}

// Racing requests must not both aggregate the same position.
func TestConcurrentSameSequenceAppliesOnce(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	const attempts = 8
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	results := make([]platform.IngestResult, attempts)
	errs := make([]error, attempts)

	// Distinct records, same sequence: at most one may apply, and the rest
	// must conflict rather than replay.
	for i := range attempts {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = f.plane.IngestDecisionRecord(context.Background(), platform.IngestRequest{
				RunID:             "run-1",
				Sequence:          1,
				BehavioralProfile: fixtureProfile,
				Record:            ingestRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op-%d", i)),
			})
		}()
	}
	start.Done()
	done.Wait()

	applied := 0
	for i := range attempts {
		if errs[i] == nil && results[i].Disposition == platform.IngestApplied {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("applied = %d, want exactly 1", applied)
	}

	aggregate, _, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1: a race double-counted", aggregate.RecordCount())
	}
}

// ---------------------------------------------------------------------
// Restart continuation
// ---------------------------------------------------------------------

// The proof that a restarted process continues an evaluation rather than
// restarting its behavioral evidence — and that it does so through the
// private snapshot-to-collector path, with no exported restore constructor.
func TestIngestResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	f := openFixture(t, path)
	f.seedRunning(t, "run-1")

	const before = 3
	for i := range before {
		if _, err := f.ingest(t, "run-1", uint64(i+1),
			ingestRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op-%d", i))); err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
	}
	if err := f.store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// A new store and a new service over the same file.
	reopened := openFixture(t, path)

	state, err := reopened.plane.EvaluationIngestState(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.NextSequence() != before+1 {
		t.Fatalf("NextSequence() = %d, want %d", state.NextSequence(), before+1)
	}

	result, err := reopened.ingest(t, "run-1", before+1, ingestRecord("evt-new", "fp-new", "op-new"))
	if err != nil {
		t.Fatalf("ingest after restart error = %v", err)
	}
	if result.RecordCount != before+1 {
		t.Errorf("RecordCount = %d, want %d", result.RecordCount, before+1)
	}

	_, snapshot, err := reopened.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if got := snapshot.DistinctBehaviorCount(); got != before+1 {
		t.Errorf("DistinctBehaviorCount() = %d, want %d: prior behaviors were lost", got, before+1)
	}
	// The earlier behaviors are still there, not replaced by the new one.
	seen := map[string]bool{}
	for _, entry := range snapshot.Entries() {
		seen[entry.FingerprintID] = true
	}
	for i := range before {
		if !seen[fmt.Sprintf("fp-%d", i)] {
			t.Errorf("behavior fp-%d did not survive the restart", i)
		}
	}
	if !seen["fp-new"] {
		t.Error("the post-restart behavior is missing")
	}
}

// ---------------------------------------------------------------------
// Saturation
// ---------------------------------------------------------------------

// Saturation is degraded evidence, not a failed ingest: the aggregate keeps
// advancing while the snapshot stops being the whole truth and says so.
func TestSaturationAdvancesAggregateAndMarksEvidenceIncomplete(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	const capacity = 512
	for i := range capacity {
		result, err := f.ingest(t, "run-1", uint64(i+1),
			ingestRecord(fmt.Sprintf("evt-%04d", i), fmt.Sprintf("fp-%04d", i), fmt.Sprintf("op-%04d", i)))
		if err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
		if !result.BehaviorComplete {
			t.Fatalf("BehaviorComplete = false at record %d, before saturation", i+1)
		}
	}

	// The 513th distinct behavior saturates the collector.
	result, err := f.ingest(t, "run-1", capacity+1,
		ingestRecord("evt-overflow", "fp-overflow", "op-overflow"))
	if err != nil {
		t.Fatalf("saturating ingest error = %v, want it applied", err)
	}
	if result.Disposition != platform.IngestApplied {
		t.Errorf("Disposition = %q, want applied", result.Disposition)
	}
	if result.BehaviorComplete {
		t.Error("BehaviorComplete = true after saturation")
	}
	if result.RecordCount != capacity+1 {
		t.Errorf("RecordCount = %d, want %d", result.RecordCount, capacity+1)
	}

	aggregate, snapshot, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != capacity+1 {
		t.Errorf("aggregate RecordCount() = %d, want %d", aggregate.RecordCount(), capacity+1)
	}
	if snapshot.ObservationCount() != capacity {
		t.Errorf("snapshot ObservationCount() = %d, want %d", snapshot.ObservationCount(), capacity)
	}
	if snapshot.Complete() {
		t.Error("snapshot Complete() = true after saturation")
	}

	// Aggregate evidence keeps advancing; behavioral evidence stays bounded.
	next, err := f.ingest(t, "run-1", capacity+2, ingestRecord("evt-after", "fp-0000", "op-0000"))
	if err != nil {
		t.Fatalf("post-saturation ingest error = %v", err)
	}
	if next.RecordCount != capacity+2 {
		t.Errorf("RecordCount = %d, want %d", next.RecordCount, capacity+2)
	}
	if next.BehaviorComplete {
		t.Error("BehaviorComplete recovered after saturation")
	}

	_, snapshot, _ = f.store.EvaluationEvidence(t.Context(), "run-1")
	if snapshot.DistinctBehaviorCount() != capacity {
		t.Errorf("DistinctBehaviorCount() = %d, want %d", snapshot.DistinctBehaviorCount(), capacity)
	}
}

// Every other collector failure rejects the whole ingest.
func TestNonCapacityCollectorFailureRejectsIngest(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("first ingest error = %v", err)
	}

	// The same fingerprint describing a different behavior.
	conflicting := ingestRecord("evt-1", "fp-0", "write")
	if _, err := f.ingest(t, "run-1", 2, conflicting); !errors.Is(err, platform.ErrFingerprintConflict) {
		t.Fatalf("error = %v, want ErrFingerprintConflict", err)
	}

	state, _ := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if state.NextSequence() != 2 {
		t.Errorf("NextSequence() = %d, want 2: a rejected ingest advanced the cursor", state.NextSequence())
	}
	aggregate, _, _ := f.store.EvaluationEvidence(t.Context(), "run-1")
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1", aggregate.RecordCount())
	}
}

// ---------------------------------------------------------------------
// Progress
// ---------------------------------------------------------------------

func TestEvaluationProgressReportsFacts(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	report, err := f.plane.EvaluationProgress(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationProgress() error = %v", err)
	}
	if report.RecordCount != 0 || report.NextIngestSequence != 1 {
		t.Errorf("empty run progress = %d records, next %d", report.RecordCount, report.NextIngestSequence)
	}
	if !report.BehaviorComplete {
		t.Error("a run that observed nothing is not behaviorally incomplete")
	}

	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("ingest error = %v", err)
	}

	report, err = f.plane.EvaluationProgress(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationProgress() error = %v", err)
	}
	if report.RecordCount != 1 || report.DistinctBehaviorCount != 1 ||
		report.BehaviorObservationCount != 1 || report.NextIngestSequence != 2 {
		t.Errorf("progress = %+v", report)
	}
	if report.Run.Status() != platform.RunRunning {
		t.Errorf("Status() = %q, want running", report.Run.Status())
	}
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// completeEvaluation drives one run end to end with the given operations.
func (f *controlPlaneFixture) completeEvaluation(
	t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID, operations []string,
) {
	t.Helper()
	ctx := t.Context()

	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := platform.NewCandidate(candidateID, "agent-1", platform.CandidateMetadata{Label: "v1"})
	for _, err := range []error{
		f.plane.CreateProject(ctx, project),
		f.plane.CreateAgent(ctx, agent),
		f.plane.CreateCandidate(ctx, candidate),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			t.Fatalf("seed error = %v", err)
		}
	}

	run, err := platform.NewEvaluationRun(runID, candidateID, fixtureEnvironment, fixtureProfile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	mustStart(t, f, runID)

	for i, op := range operations {
		if _, err := f.ingest(t, runID, uint64(i+1),
			ingestRecord(fmt.Sprintf("%s-evt-%d", runID, i), "fp-"+op, op)); err != nil {
			t.Fatalf("ingest error = %v", err)
		}
	}
	if _, err := f.plane.CompleteEvaluationRun(ctx, runID, aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}

func TestCompareEvaluationsDerivesTheFullChain(t *testing.T) {
	f := newFixture(t)
	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read", "list"})
	f.completeEvaluation(t, "run-can", "cand-can", []string{"read", "list", "write"})

	strict, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can",
		platform.EvaluationGateLimits{
			MaxAddedBehaviors:           0,
			MaxBlockDecisions:           math.MaxUint64,
			MaxCriticalRiskObservations: math.MaxUint64,
		})
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}
	if strict.Diff.AddedCount() != 1 {
		t.Errorf("AddedCount() = %d, want 1", strict.Diff.AddedCount())
	}
	if strict.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", strict.Gate.Verdict())
	}

	// Same evidence, one different limit.
	relaxed, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can",
		platform.EvaluationGateLimits{
			MaxAddedBehaviors:           1,
			MaxBlockDecisions:           math.MaxUint64,
			MaxCriticalRiskObservations: math.MaxUint64,
		})
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}
	if relaxed.Gate.Verdict() != platform.GateVerdictPass {
		t.Errorf("Verdict() = %q, want pass", relaxed.Gate.Verdict())
	}
}

// A running evaluation has mutable evidence; a failed or cancelled one is not
// a completed evaluation at all.
func TestCompareRequiresCompletedRuns(t *testing.T) {
	f := newFixture(t)
	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read"})

	f.seedPending(t, "run-open")
	mustStart(t, f, "run-open")
	if _, err := f.ingest(t, "run-open", 1, ingestRecord("evt-0", "fp-read", "read")); err != nil {
		t.Fatalf("ingest error = %v", err)
	}

	limits := platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	}

	if _, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-open", limits); !errors.Is(err, platform.ErrEvaluationState) {
		t.Errorf("running candidate error = %v, want ErrEvaluationState", err)
	}
	if _, err := f.plane.CompareEvaluations(t.Context(), "run-open", "run-ref", limits); !errors.Is(err, platform.ErrEvaluationState) {
		t.Errorf("running reference error = %v, want ErrEvaluationState", err)
	}
}

// Incomplete evidence refuses comparison through task 054's own semantics.
// Nothing manufactures a scorecard from a measurement that did not finish.
func TestCompareRefusesIncompleteBehavioralEvidence(t *testing.T) {
	f := newFixture(t)
	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read"})

	f.seedPending(t, "run-sat")
	mustStart(t, f, "run-sat")
	for i := range 513 {
		if _, err := f.ingest(t, "run-sat", uint64(i+1),
			ingestRecord(fmt.Sprintf("s-evt-%04d", i), fmt.Sprintf("s-fp-%04d", i), fmt.Sprintf("s-op-%04d", i))); err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
	}
	if _, err := f.plane.CompleteEvaluationRun(t.Context(), "run-sat", aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}

	_, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-sat",
		platform.EvaluationGateLimits{
			MaxAddedBehaviors:           math.MaxUint64,
			MaxBlockDecisions:           math.MaxUint64,
			MaxCriticalRiskObservations: math.MaxUint64,
		})
	if !errors.Is(err, platform.ErrIncompleteSnapshot) {
		t.Fatalf("error = %v, want ErrIncompleteSnapshot", err)
	}
}

// ---------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------

// Ingest and comparison both include real SQLite work, so these are I/O
// numbers for regression tracking rather than a latency gate.

func benchFixture(b *testing.B) *controlPlaneFixture {
	b.Helper()
	store, err := platform.OpenSQLiteStore(b.Context(), filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	b.Cleanup(func() { store.Close() })
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		b.Fatalf("NewControlPlane() error = %v", err)
	}
	return &controlPlaneFixture{plane: plane, store: store}
}

func (f *controlPlaneFixture) benchSeedRunning(b *testing.B, runID platform.EvaluationRunID, candidateID platform.CandidateID) {
	b.Helper()
	ctx := b.Context()

	project, _ := platform.NewProject("proj-1", "Bench")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Bench agent")
	candidate, _ := platform.NewCandidate(candidateID, "agent-1", platform.CandidateMetadata{})
	for _, err := range []error{
		f.plane.CreateProject(ctx, project),
		f.plane.CreateAgent(ctx, agent),
		f.plane.CreateCandidate(ctx, candidate),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			b.Fatalf("seed error = %v", err)
		}
	}
	run, _ := platform.NewEvaluationRun(runID, candidateID, fixtureEnvironment, fixtureProfile, aggEpoch)
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		b.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := f.plane.StartEvaluationRun(ctx, runID, aggEpoch.Add(time.Minute)); err != nil {
		b.Fatalf("StartEvaluationRun() error = %v", err)
	}
}

// BenchmarkIngestDecisionRecord measures one accepted record, persistence
// included. Each iteration advances the sequence, so this is the real path
// rather than a repeated replay.
func BenchmarkIngestDecisionRecord(b *testing.B) {
	f := benchFixture(b)
	f.benchSeedRunning(b, "run-1", "cand-1")
	ctx := b.Context()

	var sequence uint64
	b.ReportAllocs()
	for b.Loop() {
		sequence++
		// A bounded set of behaviors, so the collector stays under capacity
		// and the benchmark measures steady-state ingest.
		n := sequence % 64
		if _, err := f.plane.IngestDecisionRecord(ctx, platform.IngestRequest{
			RunID:             "run-1",
			Sequence:          sequence,
			BehavioralProfile: fixtureProfile,
			Record: ingestRecord(
				fmt.Sprintf("evt-%d", sequence), fmt.Sprintf("fp-%d", n), fmt.Sprintf("op-%d", n)),
		}); err != nil {
			b.Fatalf("IngestDecisionRecord() error = %v", err)
		}
	}
}

func benchmarkCompare(b *testing.B, behaviors int) {
	f := benchFixture(b)
	ctx := b.Context()

	build := func(runID platform.EvaluationRunID, candidateID platform.CandidateID, prefix string) {
		f.benchSeedRunning(b, runID, candidateID)
		for i := range behaviors {
			if _, err := f.plane.IngestDecisionRecord(ctx, platform.IngestRequest{
				RunID: runID, Sequence: uint64(i + 1), BehavioralProfile: fixtureProfile,
				Record: ingestRecord(
					fmt.Sprintf("%s-evt-%d", prefix, i),
					fmt.Sprintf("%s-fp-%04d", prefix, i),
					fmt.Sprintf("%s-op-%04d", prefix, i)),
			}); err != nil {
				b.Fatalf("ingest error = %v", err)
			}
		}
		if _, err := f.plane.CompleteEvaluationRun(ctx, runID, aggEpoch.Add(time.Hour)); err != nil {
			b.Fatalf("CompleteEvaluationRun() error = %v", err)
		}
	}
	build("run-ref", "cand-ref", "r")
	build("run-can", "cand-can", "c")

	limits := platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, err := f.plane.CompareEvaluations(ctx, "run-ref", "run-can", limits); err != nil {
			b.Fatalf("CompareEvaluations() error = %v", err)
		}
	}
}

// Comparison loads two bounded evidence states and diffs up to 512 behaviors
// per side, so cost tracks behavioral cardinality, not history length.
func BenchmarkCompareEvaluations32(b *testing.B)  { benchmarkCompare(b, 32) }
func BenchmarkCompareEvaluations512(b *testing.B) { benchmarkCompare(b, 512) }

// ---------------------------------------------------------------------
// Service-level sequence checks, isolated from the store
// ---------------------------------------------------------------------

// permissiveIngestStore accepts every commit and reports whatever cursor it
// is told to.
//
// The real SQLite store re-checks the sequence inside its transaction, which
// is correct defence in depth — and it means a service-level sequence bug is
// invisible when the two are tested together. This double removes that second
// layer so the control plane's own check is what is under test.
type permissiveIngestStore struct {
	state    platform.EvaluationIngestState
	commits  int
	delegate platform.EvaluationIngestStore
}

func (s *permissiveIngestStore) EvaluationIngestState(
	ctx context.Context, id platform.EvaluationRunID,
) (platform.EvaluationIngestState, error) {
	return s.delegate.EvaluationIngestState(ctx, id)
}

func (s *permissiveIngestStore) CommitEvaluationIngest(
	ctx context.Context, commit platform.EvaluationIngestCommit,
) error {
	s.commits++
	return nil // accepts anything, including a sequence the store would refuse
}

func TestServiceRejectsGapsWithoutRelyingOnTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	permissive := &permissiveIngestStore{delegate: store}
	plane, err := platform.NewControlPlane(store, store, permissive)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}

	// Seed through a plane backed by the real store, so the run exists.
	seeder := &controlPlaneFixture{plane: mustPlane(t, store), store: store, path: path}
	seeder.seedRunning(t, "run-1")

	tests := []struct {
		name     string
		sequence uint64
	}{
		{"gap of one", 2},
		{"large gap", 99},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := permissive.commits
			_, err := plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
				RunID:             "run-1",
				Sequence:          tt.sequence,
				BehavioralProfile: fixtureProfile,
				Record:            ingestRecord("evt-gap", "fp-gap", "gap"),
			})
			if !errors.Is(err, platform.ErrIngestSequence) {
				t.Fatalf("error = %v, want ErrIngestSequence", err)
			}
			if permissive.commits != before {
				t.Error("the service committed a gapped sequence; it is relying on the store to refuse")
			}
		})
	}

	// The expected sequence still reaches the store.
	if _, err := plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
		RunID: "run-1", Sequence: 1, BehavioralProfile: fixtureProfile,
		Record: ingestRecord("evt-0", "fp-0", "read"),
	}); err != nil {
		t.Fatalf("expected sequence error = %v", err)
	}
	if permissive.commits != 1 {
		t.Errorf("commits = %d, want 1", permissive.commits)
	}
}

func mustPlane(t *testing.T, store *platform.SQLiteStore) *platform.ControlPlane {
	t.Helper()
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	return plane
}
