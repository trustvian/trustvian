package platform

// The control plane: the authoritative platform service layer.
//
// Every rule about what an evaluation is lives here — which lifecycle
// transitions are legal, when evidence may be ingested, what a comparison
// requires. Transports call these methods and translate; they decide nothing.
//
// The alternative works exactly until the second caller arrives. A CLI that
// reimplemented "ingest only while running" would drift from the HTTP
// version, and the drift would stay invisible until the two disagreed about a
// real evaluation.
//
// See docs/adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md.

import (
	"context"
	"errors"
	"fmt"
	"time"

	trustvian "github.com/trustvian/trustvian"
)

// ErrEvaluationState reports an operation that does not fit a run's current
// lifecycle state — ingesting into a pending run, comparing an unfinished
// one.
//
// Not a storage conflict and not corruption: the caller asked for something
// coherent at the wrong moment.
var ErrEvaluationState = errors.New("platform: evaluation is not in the required state")

// ControlPlane serves platform operations over narrow store capabilities.
//
// The dependencies are interfaces, never *SQLiteStore, so task 064 can supply
// PostgreSQL without any of this changing. SQLite happens to implement all
// three; the service must not be able to tell.
type ControlPlane struct {
	control     ControlStore
	evaluations EvaluationStore
	ingest      EvaluationIngestStore
}

// NewControlPlane wires the service to its capabilities.
func NewControlPlane(
	control ControlStore,
	evaluations EvaluationStore,
	ingest EvaluationIngestStore,
) (*ControlPlane, error) {
	if control == nil || evaluations == nil || ingest == nil {
		return nil, errors.New("platform: control plane requires control, evaluation and ingest stores")
	}
	return &ControlPlane{control: control, evaluations: evaluations, ingest: ingest}, nil
}

// ---------------------------------------------------------------------
// Control entities
// ---------------------------------------------------------------------

// Identity is caller-owned throughout: nothing here generates an ID, and no
// UUID dependency exists. ADR 0025 is binding.

func (c *ControlPlane) CreateProject(ctx context.Context, project Project) error {
	return c.control.CreateProject(ctx, project)
}

func (c *ControlPlane) Project(ctx context.Context, id ProjectID) (Project, error) {
	return c.control.Project(ctx, id)
}

func (c *ControlPlane) CreateAgent(ctx context.Context, agent Agent) error {
	return c.control.CreateAgent(ctx, agent)
}

func (c *ControlPlane) Agent(ctx context.Context, id AgentID) (Agent, error) {
	return c.control.Agent(ctx, id)
}

func (c *ControlPlane) CreateCandidate(ctx context.Context, candidate Candidate) error {
	return c.control.CreateCandidate(ctx, candidate)
}

func (c *ControlPlane) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	return c.control.Candidate(ctx, id)
}

// ---------------------------------------------------------------------
// Evaluation run lifecycle
// ---------------------------------------------------------------------

func (c *ControlPlane) CreateEvaluationRun(ctx context.Context, run EvaluationRun) error {
	return c.evaluations.CreateEvaluationRun(ctx, run)
}

func (c *ControlPlane) EvaluationRun(ctx context.Context, id EvaluationRunID) (EvaluationRun, error) {
	return c.evaluations.EvaluationRun(ctx, id)
}

// StartEvaluationRun moves a run to Running at the given time.
func (c *ControlPlane) StartEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Start(at)
	})
}

// CompleteEvaluationRun ends a run successfully.
//
// Completed means execution ended, not that the candidate passed, is safe, or
// may be promoted. Task 052 drew that line and comparison keeps it.
func (c *ControlPlane) CompleteEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Complete(at)
	})
}

func (c *ControlPlane) FailEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time, reason string,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Fail(at, reason)
	})
}

func (c *ControlPlane) CancelEvaluationRun(
	ctx context.Context, id EvaluationRunID, at time.Time,
) (EvaluationRun, error) {
	return c.transition(ctx, id, func(run EvaluationRun) (EvaluationRun, error) {
		return run.Cancel(at)
	})
}

// transition applies one domain transition under the store's compare-and-swap.
//
// There is no second lifecycle implementation here: the domain decides what is
// legal, and task 057's CAS decides whether the caller's view is still current.
func (c *ControlPlane) transition(
	ctx context.Context, id EvaluationRunID,
	apply func(EvaluationRun) (EvaluationRun, error),
) (EvaluationRun, error) {
	current, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationRun{}, err
	}
	next, err := apply(current)
	if err != nil {
		return EvaluationRun{}, fmt.Errorf("%w: %w", ErrEvaluationState, err)
	}
	if err := c.evaluations.UpdateEvaluationRun(ctx, current, next); err != nil {
		return EvaluationRun{}, err
	}
	return next, nil
}

// ---------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------

// IngestDisposition is what happened to a submitted record.
//
// Two values, not three: a sequence conflict is an error. Presenting it as a
// success state would let a client treat "your record was not applied" as
// progress.
type IngestDisposition string

const (
	// IngestApplied means the record was folded into the evidence.
	IngestApplied IngestDisposition = "applied"

	// IngestReplayed means this exact record was already applied, and the
	// request changed nothing.
	IngestReplayed IngestDisposition = "replayed"
)

// IngestRequest is one record offered to a running evaluation.
type IngestRequest struct {
	RunID    EvaluationRunID
	Sequence uint64

	// BehavioralProfile travels beside the record rather than inside it.
	// DecisionRecord carries no learning scope — ADR 0024 decided a caller
	// that chose the scope already knows it — and adding the field to the
	// core's public type would push a platform concept into every producer.
	BehavioralProfile BehavioralProfileRef

	Record trustvian.DecisionRecord
}

// IngestResult reports the outcome and the cursor a client should use next.
type IngestResult struct {
	Disposition  IngestDisposition
	NextSequence uint64
	RecordCount  uint64

	// BehaviorComplete is false once the collector saturated at 512 distinct
	// behaviors. Aggregate evidence keeps advancing; behavioral evidence no
	// longer describes the whole run, and a later comparison will refuse it.
	BehaviorComplete bool
}

// IngestDecisionRecord folds one record into a running evaluation.
//
// The sequence is the retry contract. Task 053 made duplicates count twice on
// purpose, so a resubmitted record would otherwise corrupt the evidence; here
// only the expected sequence applies, only an identical retry of the previous
// one replays, and everything else fails closed.
func (c *ControlPlane) IngestDecisionRecord(
	ctx context.Context, request IngestRequest,
) (IngestResult, error) {
	run, err := c.evaluations.EvaluationRun(ctx, request.RunID)
	if err != nil {
		return IngestResult{}, err
	}

	// Evidence belongs to an execution that is happening. A pending run has
	// not started; a terminal one is finished and its evidence is what a
	// comparison will read.
	if run.Status() != RunRunning {
		return IngestResult{}, fmt.Errorf("%w: run %s is %s, records are accepted only while running",
			ErrEvaluationState, preview(string(request.RunID)), run.Status())
	}
	if request.BehavioralProfile != run.BehavioralProfile() {
		return IngestResult{}, fmt.Errorf(
			"%w: record was produced under profile %s, run %s uses %s",
			ErrInvalidBehaviorRecord, preview(string(request.BehavioralProfile)),
			preview(string(request.RunID)), preview(string(run.BehavioralProfile())))
	}

	digest, err := RecordDigest(request.Record)
	if err != nil {
		return IngestResult{}, err
	}

	state, err := c.ingest.EvaluationIngestState(ctx, request.RunID)
	if err != nil {
		return IngestResult{}, err
	}

	switch {
	case request.Sequence == state.NextSequence():
		// The expected record. Fall through and apply it.

	case request.Sequence+1 == state.NextSequence():
		// A retry of the last accepted record. Replay only when it is
		// provably the same record — a different payload under the same
		// sequence is two records claiming one position.
		//
		// A migrated task 057 run has no recorded digest, so nothing can be
		// proven identical and this conflicts rather than guessing.
		if state.LastDigest() != "" && state.LastDigest() == digest {
			// The counts travel with the cursor, read in one transaction, so
			// this reply describes a state that actually existed rather than
			// a cursor from one moment beside evidence from another.
			return IngestResult{
				Disposition:      IngestReplayed,
				NextSequence:     state.NextSequence(),
				RecordCount:      state.RecordCount(),
				BehaviorComplete: state.BehaviorComplete(),
			}, nil
		}
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d was already accepted with different content",
			ErrIngestSequence, request.Sequence)

	case request.Sequence < state.NextSequence():
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d is stale, run %s expects %d",
			ErrIngestSequence, request.Sequence,
			preview(string(request.RunID)), state.NextSequence())

	default:
		return IngestResult{}, fmt.Errorf(
			"%w: sequence %d leaves a gap, run %s expects %d",
			ErrIngestSequence, request.Sequence,
			preview(string(request.RunID)), state.NextSequence())
	}

	return c.applyRecord(ctx, run, request, digest, state)
}

// applyRecord folds the record into both reducers and commits atomically.
func (c *ControlPlane) applyRecord(
	ctx context.Context, run EvaluationRun, request IngestRequest,
	digest string, state EvaluationIngestState,
) (IngestResult, error) {
	aggregate, collector, err := c.currentEvidence(ctx, run)
	if err != nil {
		return IngestResult{}, err
	}

	aggregate, err = aggregate.AddRecord(request.Record)
	if err != nil {
		return IngestResult{}, err
	}

	// Saturation is degraded evidence, not a failed ingest: the aggregate
	// legitimately accepted the record, and refusing would discard sound
	// decision evidence because *behavioral* evidence filled up. The snapshot
	// stays incomplete and says so, which is what makes a later comparison
	// refuse rather than quietly under-report.
	//
	// Every other collector error — environment mismatch, fingerprint
	// conflict, invalid identity, overflow — rejects the whole ingest.
	if err := collector.Observe(request.Record); err != nil && !errors.Is(err, ErrBehaviorCapacity) {
		return IngestResult{}, err
	}

	snapshot := collector.Snapshot()

	committed, err := c.ingest.CommitEvaluationIngest(ctx, EvaluationIngestCommit{
		PreviousNextSequence: state.NextSequence(),
		Sequence:             request.Sequence,
		RecordDigest:         digest,
		Aggregate:            aggregate,
		Snapshot:             snapshot,
	})
	if err != nil {
		return IngestResult{}, err
	}

	// The store may report that another request committed this exact record
	// first. Concurrent identical submissions are the retry contract working,
	// so all but one replay rather than conflicting — and the counts come
	// from the transaction that decided it, never from a second read that
	// could observe a third request's state.
	disposition := IngestApplied
	if committed.Disposition == EvaluationIngestAlreadyCommitted {
		disposition = IngestReplayed
	}
	return IngestResult{
		Disposition:      disposition,
		NextSequence:     committed.NextSequence,
		RecordCount:      committed.RecordCount,
		BehaviorComplete: committed.BehaviorComplete,
	}, nil
}

// currentEvidence loads a run's evidence, or starts it empty.
//
// The collector is rebuilt from the stored snapshot through a package-private
// helper. That is what lets a restarted process continue a running evaluation
// rather than beginning its behavioral evidence again — and it stays
// unexported because a public constructor would let any caller forge
// collector state.
func (c *ControlPlane) currentEvidence(
	ctx context.Context, run EvaluationRun,
) (EvaluationAggregate, *BehaviorCollector, error) {
	aggregate, snapshot, err := c.evaluations.EvaluationEvidence(ctx, run.ID())
	switch {
	case errors.Is(err, ErrStoreNotFound):
		aggregate, err = NewEvaluationAggregate(run)
		if err != nil {
			return EvaluationAggregate{}, nil, err
		}
		collector, err := NewBehaviorCollector(run)
		if err != nil {
			return EvaluationAggregate{}, nil, err
		}
		return aggregate, collector, nil

	case err != nil:
		return EvaluationAggregate{}, nil, err
	}

	collector, err := behaviorCollectorFromSnapshot(snapshot)
	if err != nil {
		return EvaluationAggregate{}, nil, err
	}
	return aggregate, collector, nil
}

// ---------------------------------------------------------------------
// Progress
// ---------------------------------------------------------------------

// EvaluationProgressReport is what an evaluation has observed so far.
//
// Facts only. No pass, fail, promotable or safety field: a running evaluation
// has progress, not a verdict, and a field like that would be read as one.
type EvaluationProgressReport struct {
	Run EvaluationRun

	RecordCount              uint64
	BehaviorObservationCount uint64
	DistinctBehaviorCount    int
	BehaviorComplete         bool
	NextIngestSequence       uint64
}

// EvaluationProgress reports a run's current evidence state.
func (c *ControlPlane) EvaluationProgress(
	ctx context.Context, id EvaluationRunID,
) (EvaluationProgressReport, error) {
	run, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationProgressReport{}, err
	}
	state, err := c.ingest.EvaluationIngestState(ctx, id)
	if err != nil {
		return EvaluationProgressReport{}, err
	}

	// Every count comes from the cursor read above, which loads them in one
	// transaction alongside the sequence. Reading the evidence separately
	// would let a concurrent ingest move one between the two reads and
	// produce a report describing a state that never existed — a cursor at
	// N+1 beside an aggregate already at N+2.
	//
	// The run's status is read separately on purpose: it is independent of
	// evidence, and a lifecycle transition racing this read is a genuinely
	// concurrent fact rather than a torn one.
	return EvaluationProgressReport{
		Run:                      run,
		RecordCount:              state.RecordCount(),
		BehaviorObservationCount: state.BehaviorObservationCount(),
		DistinctBehaviorCount:    state.DistinctBehaviorCount(),
		BehaviorComplete:         state.BehaviorComplete(),
		NextIngestSequence:       state.NextSequence(),
	}, nil
}

// EvaluationIngestState reports a run's ingest cursor, so a client can
// resynchronize after losing track of its own sequence.
func (c *ControlPlane) EvaluationIngestState(
	ctx context.Context, id EvaluationRunID,
) (EvaluationIngestState, error) {
	return c.ingest.EvaluationIngestState(ctx, id)
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// EvaluationComparison is the full derived result of comparing two
// evaluations under one set of caller limits.
//
// Not persisted. Every part is a deterministic function of stored evidence,
// and ADR 0030 keeps one source of truth by recomputing rather than storing a
// second copy that can disagree.
type EvaluationComparison struct {
	Reference EvaluationRun
	Candidate EvaluationRun

	Diff      BehaviorDiff
	Scorecard EvaluationScorecard
	Gate      EvaluationGateResult
}

// CompareEvaluations derives diff, scorecard and gate result for two runs.
//
// The four steps below exist here once. A handler repeating them is exactly
// the duplication ADR 0031 forbids, because they are four calls in a fixed
// order with preconditions between them — the shape someone reimplements
// "just for this endpoint".
func (c *ControlPlane) CompareEvaluations(
	ctx context.Context,
	referenceRunID, candidateRunID EvaluationRunID,
	limits EvaluationGateLimits,
) (EvaluationComparison, error) {
	reference, err := c.completedRun(ctx, "reference", referenceRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}
	candidate, err := c.completedRun(ctx, "candidate", candidateRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}

	referenceAggregate, referenceSnapshot, err := c.evaluations.EvaluationEvidence(ctx, referenceRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}
	candidateAggregate, candidateSnapshot, err := c.evaluations.EvaluationEvidence(ctx, candidateRunID)
	if err != nil {
		return EvaluationComparison{}, err
	}

	// Task 054 refuses a saturated snapshot, and that refusal is passed
	// through rather than worked around. Incomplete evidence is not a failed
	// policy and not an unsafe candidate — it is a measurement that did not
	// finish, and manufacturing a scorecard from it would claim otherwise.
	diff, err := CompareBehaviorSnapshots(referenceSnapshot, candidateSnapshot)
	if err != nil {
		return EvaluationComparison{}, err
	}

	scorecard, err := NewEvaluationScorecard(referenceAggregate, candidateAggregate, diff)
	if err != nil {
		return EvaluationComparison{}, err
	}

	gate, err := EvaluateEvaluationGate(scorecard, NewEvaluationGatePolicy(limits))
	if err != nil {
		return EvaluationComparison{}, err
	}

	return EvaluationComparison{
		Reference: reference,
		Candidate: candidate,
		Diff:      diff,
		Scorecard: scorecard,
		Gate:      gate,
	}, nil
}

// completedRun loads a run and requires it to have finished successfully.
//
// A running evaluation has mutable evidence, so a comparison of it would
// describe a moment that has already passed. A failed or cancelled run is not
// a completed evaluation at all.
func (c *ControlPlane) completedRun(
	ctx context.Context, side string, id EvaluationRunID,
) (EvaluationRun, error) {
	run, err := c.evaluations.EvaluationRun(ctx, id)
	if err != nil {
		return EvaluationRun{}, err
	}
	if run.Status() != RunCompleted {
		return EvaluationRun{}, fmt.Errorf(
			"%w: %s run %s is %s, comparison needs a completed evaluation",
			ErrEvaluationState, side, preview(string(id)), run.Status())
	}
	return run, nil
}
