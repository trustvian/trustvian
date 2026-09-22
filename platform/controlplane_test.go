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
	"slices"
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
) (platform.EvaluationIngestCommitResult, error) {
	s.commits++
	// Accepts anything, including a sequence the real store would refuse.
	return platform.EvaluationIngestCommitResult{
		Disposition:      platform.EvaluationIngestCommitted,
		NextSequence:     commit.Sequence + 1,
		RecordCount:      commit.Aggregate.RecordCount(),
		BehaviorComplete: commit.Snapshot.Complete(),
	}, nil
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

// The durable transaction also rechecks Running, which is what makes the
// terminal race safe — and which would otherwise hide a missing preflight.
// This isolates the service's own check against a store that refuses nothing.
func TestServiceRejectsNonRunningWithoutRelyingOnTheStore(t *testing.T) {
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

	seeder := &controlPlaneFixture{plane: mustPlane(t, store), store: store, path: path}
	seeder.seedPending(t, "run-1")

	tests := []struct {
		name  string
		drive func()
	}{
		{"pending", func() {}},
		{"cancelled", func() {
			if _, err := seeder.plane.CancelEvaluationRun(
				t.Context(), "run-1", aggEpoch.Add(time.Minute)); err != nil {
				t.Fatalf("cancel: %v", err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.drive()
			before := permissive.commits
			_, err := plane.IngestDecisionRecord(t.Context(), platform.IngestRequest{
				RunID: "run-1", Sequence: 1, BehavioralProfile: fixtureProfile,
				Record: ingestRecord("evt-0", "fp-0", "read"),
			})
			if !errors.Is(err, platform.ErrEvaluationState) {
				t.Fatalf("error = %v, want ErrEvaluationState", err)
			}
			if permissive.commits != before {
				t.Error("the service committed into a non-running run; it is relying on the store to refuse")
			}
		})
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

// Concurrent identical submissions are the retry contract working, not a
// pile-up of conflicts: one applies and the rest replay.
//
// A client that retries on a slow response sends exactly this shape, so
// treating the losers as conflicts would make ordinary network behaviour look
// like an error the caller cannot distinguish from a real one.
func TestConcurrentIdenticalSequenceReplays(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	const attempts = 8
	record := ingestRecord("evt-0", "fp-0", "read")

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	results := make([]platform.IngestResult, attempts)
	errs := make([]error, attempts)

	for i := range attempts {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i], errs[i] = f.plane.IngestDecisionRecord(context.Background(), platform.IngestRequest{
				RunID:             "run-1",
				Sequence:          1,
				BehavioralProfile: fixtureProfile,
				Record:            record, // byte-identical every time
			})
		}()
	}
	start.Done()
	done.Wait()

	var applied, replayed int
	for i := range attempts {
		if errs[i] != nil {
			t.Errorf("attempt %d error = %v; an identical retry must not conflict", i, errs[i])
			continue
		}
		switch results[i].Disposition {
		case platform.IngestApplied:
			applied++
		case platform.IngestReplayed:
			replayed++
		default:
			t.Errorf("attempt %d disposition = %q", i, results[i].Disposition)
		}
	}
	if applied != 1 {
		t.Errorf("applied = %d, want exactly 1", applied)
	}
	if replayed != attempts-1 {
		t.Errorf("replayed = %d, want %d", replayed, attempts-1)
	}

	// Every response describes the same committed state.
	for i := range attempts {
		if errs[i] != nil {
			continue
		}
		if results[i].RecordCount != 1 || results[i].NextSequence != 2 {
			t.Errorf("attempt %d reported count %d / next %d, want 1 / 2",
				i, results[i].RecordCount, results[i].NextSequence)
		}
	}

	aggregate, snapshot, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1: a concurrent retry double-counted", aggregate.RecordCount())
	}
	if snapshot.ObservationCount() != 1 {
		t.Errorf("ObservationCount() = %d, want 1", snapshot.ObservationCount())
	}
	state, err := f.plane.EvaluationIngestState(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.NextSequence() != 2 {
		t.Errorf("NextSequence() = %d, want 2", state.NextSequence())
	}
}

// Progress never reports a cursor from one moment beside evidence from
// another: the counts travel together, read in one transaction.
func TestProgressStaysCoherentUnderConcurrentIngest(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")

	var stop sync.WaitGroup
	stop.Add(1)
	done := make(chan struct{})

	go func() {
		defer stop.Done()
		for sequence := uint64(1); ; sequence++ {
			select {
			case <-done:
				return
			default:
			}
			if _, err := f.plane.IngestDecisionRecord(context.Background(), platform.IngestRequest{
				RunID: "run-1", Sequence: sequence, BehavioralProfile: fixtureProfile,
				Record: ingestRecord(fmt.Sprintf("evt-%d", sequence),
					fmt.Sprintf("fp-%d", sequence%32), fmt.Sprintf("op-%d", sequence%32)),
			}); err != nil {
				return
			}
		}
	}()

	for range 200 {
		report, err := f.plane.EvaluationProgress(context.Background(), "run-1")
		if err != nil {
			t.Fatalf("EvaluationProgress() error = %v", err)
		}
		// For API-managed evidence one ingest is one record, so the cursor
		// and the aggregate must agree in every observation.
		if report.NextIngestSequence != report.RecordCount+1 {
			t.Fatalf("torn read: next sequence %d with record count %d",
				report.NextIngestSequence, report.RecordCount)
		}
		if report.BehaviorObservationCount > report.RecordCount {
			t.Fatalf("torn read: %d behavior observations against %d records",
				report.BehaviorObservationCount, report.RecordCount)
		}
	}
	close(done)
	stop.Wait()
}

// ---------------------------------------------------------------------
// Zero-record evaluations
// ---------------------------------------------------------------------

// completeEmptyRun drives a run to Completed without ingesting anything.
func (f *controlPlaneFixture) completeEmptyRun(
	t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
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
	if _, err := f.plane.CompleteEvaluationRun(ctx, runID, aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}

// permissiveLimits accepts anything a maximum gate could measure, so only the
// mandatory minimum-evidence gates can fail.
func permissiveLimits() platform.EvaluationGateLimits {
	return platform.EvaluationGateLimits{
		MaxAddedBehaviors:           math.MaxUint64,
		MaxBlockDecisions:           math.MaxUint64,
		MaxCriticalRiskObservations: math.MaxUint64,
	}
}

// A run that ingested nothing persists no evidence rows, but it is a real
// completed evaluation that observed zero records — not a missing one.
//
// Task 056 made the minimum-evidence gates mandatory precisely because such a
// candidate satisfies every maximum. Reporting it as absent would hide it
// instead of failing it.
func TestBothEmptyEvaluationsCompareAndFailEvidenceGates(t *testing.T) {
	f := newFixture(t)
	f.completeEmptyRun(t, "run-ref", "cand-ref")
	f.completeEmptyRun(t, "run-can", "cand-can")

	comparison, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can", permissiveLimits())
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v; empty evidence is not absence", err)
	}

	t.Run("scorecard reports zero on both sides", func(t *testing.T) {
		if got := comparison.Scorecard.ReferenceRecordCount(); got != 0 {
			t.Errorf("ReferenceRecordCount() = %d, want 0", got)
		}
		if got := comparison.Scorecard.CandidateRecordCount(); got != 0 {
			t.Errorf("CandidateRecordCount() = %d, want 0", got)
		}
	})

	t.Run("diff is empty in every direction", func(t *testing.T) {
		if comparison.Diff.AddedCount() != 0 || comparison.Diff.RemovedCount() != 0 ||
			comparison.Diff.SharedCount() != 0 {
			t.Errorf("diff = added %d, removed %d, shared %d; want 0/0/0",
				comparison.Diff.AddedCount(), comparison.Diff.RemovedCount(),
				comparison.Diff.SharedCount())
		}
	})

	t.Run("both evidence gates fail at zero", func(t *testing.T) {
		for _, g := range []struct {
			name string
			gate platform.MinimumCountGate
		}{
			{"ReferenceEvidence", comparison.Gate.ReferenceEvidence()},
			{"CandidateEvidence", comparison.Gate.CandidateEvidence()},
		} {
			if g.gate.Actual != 0 || g.gate.Minimum != 1 || g.gate.Passed {
				t.Errorf("%s = actual %d, minimum %d, passed %v; want 0/1/false",
					g.name, g.gate.Actual, g.gate.Minimum, g.gate.Passed)
			}
		}
	})

	t.Run("satisfied maximums do not compensate", func(t *testing.T) {
		// Each maximum gate is trivially satisfied by an evaluation that
		// observed nothing. That is exactly the fail-open shape task 056
		// exists to prevent, so the verdict must still be FAIL.
		for _, g := range []struct {
			name string
			gate platform.MaximumCountGate
		}{
			{"AddedBehaviors", comparison.Gate.AddedBehaviors()},
			{"BlockDecisions", comparison.Gate.BlockDecisions()},
			{"CriticalRiskObservations", comparison.Gate.CriticalRiskObservations()},
		} {
			if !g.gate.Passed {
				t.Errorf("precondition: %s should be satisfied by empty evidence", g.name)
			}
		}
		if comparison.Gate.Verdict() != platform.GateVerdictFail {
			t.Fatalf("Verdict() = %q, want fail: an evaluation that ran nothing passed",
				comparison.Gate.Verdict())
		}
	})
}

// Empty evidence participates in the diff rather than being treated as
// absence: every candidate behavior is Added against an empty reference.
func TestEmptyReferenceAgainstPopulatedCandidate(t *testing.T) {
	f := newFixture(t)
	f.completeEmptyRun(t, "run-ref", "cand-ref")
	f.completeEvaluation(t, "run-can", "cand-can", []string{"read"})

	comparison, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can", permissiveLimits())
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}

	if comparison.Diff.AddedCount() != 1 || comparison.Diff.RemovedCount() != 0 ||
		comparison.Diff.SharedCount() != 0 {
		t.Errorf("diff = added %d, removed %d, shared %d; want 1/0/0",
			comparison.Diff.AddedCount(), comparison.Diff.RemovedCount(), comparison.Diff.SharedCount())
	}
	if comparison.Gate.ReferenceEvidence().Passed {
		t.Error("ReferenceEvidence passed with zero records")
	}
	if !comparison.Gate.CandidateEvidence().Passed {
		t.Error("CandidateEvidence failed with one record")
	}
	if comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", comparison.Gate.Verdict())
	}
}

// And the reverse direction: an empty candidate removes everything.
func TestPopulatedReferenceAgainstEmptyCandidate(t *testing.T) {
	f := newFixture(t)
	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read", "list"})
	f.completeEmptyRun(t, "run-can", "cand-can")

	comparison, err := f.plane.CompareEvaluations(t.Context(), "run-ref", "run-can", permissiveLimits())
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}

	if comparison.Diff.RemovedCount() != 2 || comparison.Diff.AddedCount() != 0 ||
		comparison.Diff.SharedCount() != 0 {
		t.Errorf("diff = added %d, removed %d, shared %d; want 0/2/0",
			comparison.Diff.AddedCount(), comparison.Diff.RemovedCount(), comparison.Diff.SharedCount())
	}
	if !comparison.Gate.ReferenceEvidence().Passed {
		t.Error("ReferenceEvidence failed with two records")
	}
	if comparison.Gate.CandidateEvidence().Passed {
		t.Error("CandidateEvidence passed with zero records")
	}
	if comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", comparison.Gate.Verdict())
	}
}

// The empty interpretation comes from durable state, not from anything the
// process remembered.
func TestEmptyComparisonSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	f := openFixture(t, path)
	f.completeEmptyRun(t, "run-ref", "cand-ref")
	f.completeEmptyRun(t, "run-can", "cand-can")
	if err := f.store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openFixture(t, path)
	comparison, err := reopened.plane.CompareEvaluations(
		t.Context(), "run-ref", "run-can", permissiveLimits())
	if err != nil {
		t.Fatalf("CompareEvaluations() after restart error = %v", err)
	}
	if comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("Verdict() = %q, want fail", comparison.Gate.Verdict())
	}
	if comparison.Gate.ReferenceEvidence().Passed || comparison.Gate.CandidateEvidence().Passed {
		t.Error("an evidence gate passed for a zero-record run after restart")
	}
}

// misreportingIngestStore claims a run has ingested records while the
// evidence store reports none.
//
// The real SQLite store cannot produce that pair — its own cursor read
// already refuses a cursor without evidence — which is correct defence in
// depth and also means the service's own check is never exercised against it.
// Task 064's PostgreSQL backend need not validate identically, so the service
// must not depend on a store catching this.
type misreportingIngestStore struct {
	state platform.EvaluationIngestState
}

func (s *misreportingIngestStore) EvaluationIngestState(
	ctx context.Context, id platform.EvaluationRunID,
) (platform.EvaluationIngestState, error) {
	return s.state, nil // no error, and not an empty state
}

func (s *misreportingIngestStore) CommitEvaluationIngest(
	ctx context.Context, commit platform.EvaluationIngestCommit,
) (platform.EvaluationIngestCommitResult, error) {
	return platform.EvaluationIngestCommitResult{}, errors.New("not used")
}

// missingEvidenceStore reports no evidence for every run.
type missingEvidenceStore struct {
	platform.EvaluationStore
}

func (s *missingEvidenceStore) EvaluationEvidence(
	ctx context.Context, id platform.EvaluationRunID,
) (platform.EvaluationAggregate, platform.BehaviorSnapshot, error) {
	return platform.EvaluationAggregate{}, platform.BehaviorSnapshot{}, platform.ErrStoreNotFound
}

// Absence of evidence is emptiness only when the durable cursor agrees
// nothing was ever written. A cursor reporting records beside missing
// evidence is impossible state, and the service must refuse it on its own.
func TestComparisonRefusesEmptyWhenCursorReportsRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Two completed runs, seeded through a real plane.
	seeder := &controlPlaneFixture{plane: mustPlane(t, store), store: store, path: path}
	seeder.completeEmptyRun(t, "run-ref", "cand-ref")
	seeder.completeEmptyRun(t, "run-can", "cand-can")

	// A store pair that reports no evidence while the cursor claims records.
	plane, err := platform.NewControlPlane(
		store,
		&missingEvidenceStore{EvaluationStore: store},
		&misreportingIngestStore{state: nonEmptyIngestState(t, store)},
	)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}

	_, err = plane.CompareEvaluations(t.Context(), "run-ref", "run-can", permissiveLimits())
	if !errors.Is(err, platform.ErrStoreCorrupt) {
		t.Fatalf("CompareEvaluations() error = %v, want ErrStoreCorrupt; "+
			"the service synthesized empty evidence for a run whose cursor reports records", err)
	}
}

// nonEmptyIngestState produces a state that is valid but not empty, by
// ingesting into a throwaway run and reading its cursor.
func nonEmptyIngestState(t *testing.T, store *platform.SQLiteStore) platform.EvaluationIngestState {
	t.Helper()
	ctx := t.Context()
	plane := mustPlane(t, store)

	candidate, _ := platform.NewCandidate("cand-cursor", "agent-1", platform.CandidateMetadata{})
	if err := store.CreateCandidate(ctx, candidate); err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}
	run, _ := platform.NewEvaluationRun("run-cursor", "cand-cursor", fixtureEnvironment, fixtureProfile, aggEpoch)
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := plane.StartEvaluationRun(ctx, "run-cursor", aggEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
	if _, err := plane.IngestDecisionRecord(ctx, platform.IngestRequest{
		RunID: "run-cursor", Sequence: 1, BehavioralProfile: fixtureProfile,
		Record: ingestRecord("evt-0", "fp-0", "read"),
	}); err != nil {
		t.Fatalf("ingest error = %v", err)
	}
	state, err := store.EvaluationIngestState(ctx, "run-cursor")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.RecordCount() == 0 {
		t.Fatal("precondition: the fixture state should report records")
	}
	return state
}

// ---------------------------------------------------------------------
// Realtime publication
// ---------------------------------------------------------------------

// recordingPublisher captures what the control plane announced.
type recordingPublisher struct {
	mu     sync.Mutex
	events []platform.RealtimeEvent
}

func (p *recordingPublisher) Publish(e platform.RealtimeEvent) platform.RealtimePublishResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	return platform.RealtimePublishResult{Delivered: 1}
}

func (p *recordingPublisher) captured() []platform.RealtimeEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.events)
}

func (p *recordingPublisher) kinds() []platform.RealtimeEventKind {
	var kinds []platform.RealtimeEventKind
	for _, e := range p.captured() {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

// failingPublisher stands in for a broken or closed bus.
type failingPublisher struct{ calls int }

func (p *failingPublisher) Publish(platform.RealtimeEvent) platform.RealtimePublishResult {
	p.calls++
	return platform.RealtimePublishResult{Closed: true}
}

// realtimeFixture is a control plane with a recording publisher attached.
func newRealtimeFixture(t *testing.T) (*controlPlaneFixture, *recordingPublisher) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	publisher := &recordingPublisher{}
	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(publisher))
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	return &controlPlaneFixture{plane: plane, store: store, path: path}, publisher
}

// Realtime is optional infrastructure: task 058 callers are unchanged.
func TestControlPlaneWorksWithoutAPublisher(t *testing.T) {
	f := newFixture(t)
	f.seedRunning(t, "run-1")
	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("ingest without a publisher error = %v", err)
	}
}

func TestLifecycleMutationsArePublished(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	ctx := t.Context()
	f.seedPending(t, "run-1")

	if got := publisher.kinds(); len(got) != 1 || got[0] != platform.RealtimeEvaluationCreated {
		t.Fatalf("after create, kinds = %v, want one evaluation_created", got)
	}

	mustStart(t, f, "run-1")
	if _, err := f.plane.CompleteEvaluationRun(ctx, "run-1", aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}

	want := []platform.RealtimeEventKind{
		platform.RealtimeEvaluationCreated,
		platform.RealtimeEvaluationStarted,
		platform.RealtimeEvaluationCompleted,
	}
	if got := publisher.kinds(); !slices.Equal(got, want) {
		t.Errorf("kinds = %v, want %v", got, want)
	}

	// Lifecycle events carry factual state and the full hierarchy.
	last := publisher.captured()[2]
	if last.Evaluation.Status != platform.RunCompleted {
		t.Errorf("Status = %q, want completed", last.Evaluation.Status)
	}
	if last.Evaluation.FinishedAt.IsZero() {
		t.Error("FinishedAt is zero on a completion event")
	}
	if last.Scope.ProjectID != "proj-1" || last.Scope.AgentID != "agent-1" ||
		last.Scope.CandidateID != "cand-1" || last.Scope.RunID != "run-1" {
		t.Errorf("scope = %+v; the hierarchy was not resolved", last.Scope)
	}
}

func TestFailAndCancelArePublished(t *testing.T) {
	tests := []struct {
		name  string
		drive func(t *testing.T, f *controlPlaneFixture)
		want  platform.RealtimeEventKind
	}{
		{
			name: "failed",
			drive: func(t *testing.T, f *controlPlaneFixture) {
				mustStart(t, f, "run-1")
				if _, err := f.plane.FailEvaluationRun(
					t.Context(), "run-1", aggEpoch.Add(time.Hour), "engine unreachable"); err != nil {
					t.Fatalf("FailEvaluationRun() error = %v", err)
				}
			},
			want: platform.RealtimeEvaluationFailed,
		},
		{
			name: "cancelled",
			drive: func(t *testing.T, f *controlPlaneFixture) {
				if _, err := f.plane.CancelEvaluationRun(
					t.Context(), "run-1", aggEpoch.Add(time.Minute)); err != nil {
					t.Fatalf("CancelEvaluationRun() error = %v", err)
				}
			},
			want: platform.RealtimeEvaluationCancelled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, publisher := newRealtimeFixture(t)
			f.seedPending(t, "run-1")
			tt.drive(t, f)

			kinds := publisher.kinds()
			if len(kinds) == 0 || kinds[len(kinds)-1] != tt.want {
				t.Fatalf("kinds = %v, want it to end with %q", kinds, tt.want)
			}
			if tt.want == platform.RealtimeEvaluationFailed {
				last := publisher.captured()[len(kinds)-1]
				if last.Evaluation.FailureReason != "engine unreachable" {
					t.Errorf("FailureReason = %q", last.Evaluation.FailureReason)
				}
			}
		})
	}
}

// Reads have no side effect. Publishing a comparison would give a read a
// side effect and make a caller-limit-dependent derived value look durable.
func TestReadsAndComparisonPublishNothing(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	ctx := t.Context()
	f.completeEvaluation(t, "run-ref", "cand-ref", []string{"read"})
	f.completeEvaluation(t, "run-can", "cand-can", []string{"read", "write"})

	before := len(publisher.captured())

	if _, err := f.plane.Project(ctx, "proj-1"); err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if _, err := f.plane.EvaluationProgress(ctx, "run-ref"); err != nil {
		t.Fatalf("EvaluationProgress() error = %v", err)
	}
	if _, err := f.plane.EvaluationIngestState(ctx, "run-ref"); err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if _, err := f.plane.CompareEvaluations(ctx, "run-ref", "run-can", permissiveLimits()); err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}

	if after := len(publisher.captured()); after != before {
		t.Errorf("reads published %d events, want 0", after-before)
	}
}

// Publication happens after the commit, never before: a failed mutation
// announces nothing a subscriber could believe.
func TestFailedMutationsPublishNothing(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	ctx := t.Context()
	f.seedPending(t, "run-1")
	before := len(publisher.captured())

	// An illegal transition.
	if _, err := f.plane.CompleteEvaluationRun(ctx, "run-1", aggEpoch.Add(time.Minute)); err == nil {
		t.Fatal("completing a pending run succeeded")
	}
	// Ingest into a non-running run.
	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err == nil {
		t.Fatal("ingest into a pending run succeeded")
	}
	// A duplicate create.
	run, _ := platform.NewEvaluationRun("run-1", "cand-1", fixtureEnvironment, fixtureProfile, aggEpoch)
	if err := f.plane.CreateEvaluationRun(ctx, run); err == nil {
		t.Fatal("duplicate create succeeded")
	}

	if after := len(publisher.captured()); after != before {
		t.Errorf("failed operations published %d events, want 0", after-before)
	}
}

// The critical direction: a broken bus must never turn a committed write into
// an apparent failure, because a client would then retry a write that landed.
func TestRealtimeFailureDoesNotFailTheOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	broken := &failingPublisher{}
	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(broken))
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	f := &controlPlaneFixture{plane: plane, store: store, path: path}

	f.seedRunning(t, "run-1")
	result, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read"))
	if err != nil {
		t.Fatalf("ingest error = %v; a delivery problem failed a committed write", err)
	}
	if result.Disposition != platform.IngestApplied || result.RecordCount != 1 {
		t.Errorf("result = %+v, want applied with one record", result)
	}
	if broken.calls == 0 {
		t.Error("the publisher was never called, so the test proves nothing")
	}

	// And the write really is durable.
	aggregate, _, err := store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1", aggregate.RecordCount())
	}
}

// An actually closed bus behaves the same way.
func TestClosedBusDoesNotFailTheOperation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := platform.NewInMemoryRealtimeBus()
	if err := bus.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(bus))
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	f := &controlPlaneFixture{plane: plane, store: store, path: path}

	f.seedRunning(t, "run-1")
	if _, err := f.ingest(t, "run-1", 1, ingestRecord("evt-0", "fp-0", "read")); err != nil {
		t.Fatalf("ingest against a closed bus error = %v", err)
	}
}

// ---------------------------------------------------------------------
// Observations
// ---------------------------------------------------------------------

func TestAppliedIngestPublishesExactlyOneObservation(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	f.seedRunning(t, "run-1")
	before := len(publisher.captured())

	record := ingestRecord("evt-0", "fp-0", "read")
	if _, err := f.ingest(t, "run-1", 1, record); err != nil {
		t.Fatalf("ingest error = %v", err)
	}

	captured := publisher.captured()[before:]
	if len(captured) != 1 {
		t.Fatalf("published %d events, want 1", len(captured))
	}
	observed := captured[0]
	if observed.Kind != platform.RealtimeObservationRecorded {
		t.Errorf("Kind = %q, want observation", observed.Kind)
	}

	o := observed.Observation
	if o.Sequence != 1 || o.RecordCount != 1 || !o.BehaviorComplete {
		t.Errorf("observation counts = %+v", o)
	}
	if o.FingerprintID != record.FingerprintID || o.Behavior != record.Behavior {
		t.Error("the observation does not describe the ingested record")
	}
	if o.Decision != record.Decision || o.RiskLevel != record.RiskLevel ||
		o.TrustScore != record.TrustScore {
		t.Error("decision fields do not match the record")
	}
	if !o.NewBehavior {
		t.Error("NewBehavior = false for the first sighting of a fingerprint")
	}
}

// A retry that produced no second durable record must produce no second live
// observation.
func TestReplayedIngestPublishesNothing(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	f.seedRunning(t, "run-1")

	record := ingestRecord("evt-0", "fp-0", "read")
	if _, err := f.ingest(t, "run-1", 1, record); err != nil {
		t.Fatalf("first ingest error = %v", err)
	}
	afterFirst := len(publisher.captured())

	result, err := f.ingest(t, "run-1", 1, record)
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if result.Disposition != platform.IngestReplayed {
		t.Fatalf("Disposition = %q, want replayed", result.Disposition)
	}

	if after := len(publisher.captured()); after != afterFirst {
		t.Errorf("a replayed ingest published %d events, want 0", after-afterFirst)
	}
}

func TestNewBehaviorTracksFirstSightings(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	f.seedRunning(t, "run-1")
	before := len(publisher.captured())

	// Same fingerprint twice, then a different one.
	for i, record := range []trustvian.DecisionRecord{
		ingestRecord("evt-0", "fp-a", "read"),
		ingestRecord("evt-1", "fp-a", "read"),
		ingestRecord("evt-2", "fp-b", "write"),
	} {
		if _, err := f.ingest(t, "run-1", uint64(i+1), record); err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
	}

	captured := publisher.captured()[before:]
	if len(captured) != 3 {
		t.Fatalf("published %d observations, want 3", len(captured))
	}
	for i, want := range []bool{true, false, true} {
		if got := captured[i].Observation.NewBehavior; got != want {
			t.Errorf("observation %d NewBehavior = %v, want %v", i+1, got, want)
		}
	}
}

// At saturation a new behavior is still a real live observation, even though
// the bounded snapshot cannot retain it. Nothing is evicted, and completeness
// is reported honestly.
func TestSaturationPublishesNewBehaviorWithIncompleteEvidence(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	f.seedRunning(t, "run-1")

	const capacity = 512
	for i := range capacity {
		if _, err := f.ingest(t, "run-1", uint64(i+1),
			ingestRecord(fmt.Sprintf("evt-%04d", i), fmt.Sprintf("fp-%04d", i),
				fmt.Sprintf("op-%04d", i))); err != nil {
			t.Fatalf("ingest %d error = %v", i+1, err)
		}
	}
	before := len(publisher.captured())

	// The 513th distinct behavior saturates the collector.
	if _, err := f.ingest(t, "run-1", capacity+1,
		ingestRecord("evt-overflow", "fp-overflow", "op-overflow")); err != nil {
		t.Fatalf("saturating ingest error = %v", err)
	}

	captured := publisher.captured()[before:]
	if len(captured) != 1 {
		t.Fatalf("published %d events, want 1", len(captured))
	}
	o := captured[0].Observation
	if !o.NewBehavior {
		t.Error("NewBehavior = false for an unseen behavior at saturation")
	}
	if o.BehaviorComplete {
		t.Error("BehaviorComplete = true after saturation")
	}
	if o.RecordCount != capacity+1 {
		t.Errorf("RecordCount = %d, want %d", o.RecordCount, capacity+1)
	}

	// Durable semantics are unchanged by any of this.
	aggregate, snapshot, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != capacity+1 || snapshot.ObservationCount() != capacity ||
		snapshot.Complete() {
		t.Errorf("durable state = %d records, %d observations, complete=%v",
			aggregate.RecordCount(), snapshot.ObservationCount(), snapshot.Complete())
	}
}

// ---------------------------------------------------------------------
// Scope filtering end to end
// ---------------------------------------------------------------------

// Two hierarchies through a real bus: each subscription receives exactly its
// own events, with no cross-delivery and no database read on the subscriber
// side.
func TestRealtimeScopeFilteringAcrossHierarchies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := platform.OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := platform.NewInMemoryRealtimeBus()
	t.Cleanup(func() { bus.Close() })
	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(bus))
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	ctx := t.Context()

	// Two independent hierarchies.
	for _, h := range []struct{ project, agent, candidate, run string }{
		{"proj-a", "agent-a", "cand-a", "run-a"},
		{"proj-b", "agent-b", "cand-b", "run-b"},
	} {
		project, _ := platform.NewProject(platform.ProjectID(h.project), "P")
		agent, _ := platform.NewAgent(platform.AgentID(h.agent), platform.ProjectID(h.project), "A")
		candidate, _ := platform.NewCandidate(
			platform.CandidateID(h.candidate), platform.AgentID(h.agent), platform.CandidateMetadata{})
		run, _ := platform.NewEvaluationRun(
			platform.EvaluationRunID(h.run), platform.CandidateID(h.candidate),
			fixtureEnvironment, fixtureProfile, aggEpoch)
		for _, err := range []error{
			plane.CreateProject(ctx, project),
			plane.CreateAgent(ctx, agent),
			plane.CreateCandidate(ctx, candidate),
			plane.CreateEvaluationRun(ctx, run),
		} {
			if err != nil {
				t.Fatalf("seed %s error = %v", h.run, err)
			}
		}
	}

	byProjectA, err := bus.Subscribe(ctx, platform.RealtimeFilter{ProjectID: "proj-a"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	byAgentB, err := bus.Subscribe(ctx, platform.RealtimeFilter{AgentID: "agent-b"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}
	byRunA, err := bus.Subscribe(ctx, platform.RealtimeFilter{RunID: "run-a"})
	if err != nil {
		t.Fatalf("Subscribe() error = %v", err)
	}

	// Start both runs, then ingest one record into each.
	for _, runID := range []platform.EvaluationRunID{"run-a", "run-b"} {
		if _, err := plane.StartEvaluationRun(ctx, runID, aggEpoch.Add(time.Minute)); err != nil {
			t.Fatalf("StartEvaluationRun(%s) error = %v", runID, err)
		}
	}
	for _, runID := range []platform.EvaluationRunID{"run-a", "run-b"} {
		if _, err := plane.IngestDecisionRecord(ctx, platform.IngestRequest{
			RunID: runID, Sequence: 1, BehavioralProfile: fixtureProfile,
			Record: ingestRecord(string(runID)+"-evt", "fp-"+string(runID), "op"),
		}); err != nil {
			t.Fatalf("ingest %s error = %v", runID, err)
		}
	}

	// Each subscription sees exactly one start and one observation, for its
	// own side only.
	for _, c := range []struct {
		name         string
		subscription platform.RealtimeSubscription
		wantRun      platform.EvaluationRunID
	}{
		{"project A", byProjectA, "run-a"},
		{"agent B", byAgentB, "run-b"},
		{"run A", byRunA, "run-a"},
	} {
		wantKinds := []platform.RealtimeEventKind{
			platform.RealtimeEvaluationStarted, platform.RealtimeObservationRecorded,
		}
		for _, wantKind := range wantKinds {
			received := <-c.subscription.Events()
			if received.Kind != wantKind {
				t.Errorf("%s: kind = %q, want %q", c.name, received.Kind, wantKind)
			}
			if received.Scope.RunID != c.wantRun {
				t.Errorf("%s: received run %q, want %q; events crossed scopes",
					c.name, received.Scope.RunID, c.wantRun)
			}
		}
		// Nothing more is queued for this subscription.
		select {
		case extra := <-c.subscription.Events():
			t.Errorf("%s: unexpected extra event %q for run %q",
				c.name, extra.Kind, extra.Scope.RunID)
		default:
		}
	}
}

// Concurrent identical retries publish exactly one observation.
//
// Distinct from the preflight replay test above, and the distinction matters:
// preflight returns before the record is applied at all, so it never exercises
// the commit path's own replay detection. A concurrent retry does — several
// requests pass preflight, one commits, and the rest learn from the
// transaction that their record was already durable. Every one of those is
// still a replay, and a replay publishes nothing.
func TestConcurrentIdenticalIngestPublishesOneObservation(t *testing.T) {
	f, publisher := newRealtimeFixture(t)
	f.seedRunning(t, "run-1")
	before := len(publisher.captured())

	const attempts = 8
	record := ingestRecord("evt-0", "fp-0", "read")

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	errs := make([]error, attempts)

	for i := range attempts {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			_, errs[i] = f.plane.IngestDecisionRecord(context.Background(), platform.IngestRequest{
				RunID:             "run-1",
				Sequence:          1,
				BehavioralProfile: fixtureProfile,
				Record:            record,
			})
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("attempt %d error = %v; an identical retry must not conflict", i, err)
		}
	}

	observations := publisher.captured()[before:]
	if len(observations) != 1 {
		t.Fatalf("published %d observations, want exactly 1: a concurrent retry was announced twice",
			len(observations))
	}
	if observations[0].Observation.RecordCount != 1 {
		t.Errorf("RecordCount = %d, want 1", observations[0].Observation.RecordCount)
	}

	aggregate, _, err := f.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if aggregate.RecordCount() != 1 {
		t.Errorf("RecordCount() = %d, want 1", aggregate.RecordCount())
	}
}
