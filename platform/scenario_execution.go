package platform

// Persisted scenario executions (task 078, ADR 0054).
//
// A scenario execution is one `trustvian eval run` invocation: N reference
// repetitions and N candidate repetitions, compared once. Persisting it is
// what lets a later invocation reuse its reference side instead of executing
// it again (`--reference <execution>|last`).
//
// Metadata only. An execution records identity, scope, N, the ordered
// run/profile associations, where its reference came from, its lifecycle and
// its verdict. It holds no scenario command, no environment value, no limit a
// later execution could inherit, and no content of any kind.

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrScenarioReference reports a recorded reference this execution cannot
	// use: one that is not completed, has a different repetition count, lies
	// outside the execution's project or environment, or whose recorded runs
	// no longer agree with the runs the control plane holds. The message names
	// which.
	//
	// Never a gate result. A comparison against a reference that cannot be
	// trusted is not a pass and not a fail, and it is refused before any
	// candidate workload runs.
	ErrScenarioReference = errors.New("platform: scenario reference is not usable")

	// ErrScenarioExecutionState reports a lifecycle transition the execution's
	// current state does not allow: completing one that is not running,
	// failing one that completed.
	ErrScenarioExecutionState = errors.New("platform: scenario execution is not in the required state")

	// ErrScenarioExecutionsUnsupported reports a store with no scenario
	// execution capability. Both shipped backends have it.
	ErrScenarioExecutionsUnsupported = errors.New("platform: scenario executions are not supported by this store")
)

// ScenarioExecutionID identifies one scenario execution. Caller-generated, like
// a run identifier: the runner names the execution before anything runs, so
// every run identifier it derives can carry it.
type ScenarioExecutionID string

// ScenarioExecutionStatus is an execution's lifecycle state.
//
// Completed means the comparison was evaluated and recorded — PASS or FAIL.
// It does not mean PASS: a gate FAIL is a completed execution, exactly as a
// completed run is not a passing one. Only a completed execution can be a
// reference, and a running one — including one whose runner was killed and
// never came back — never can.
type ScenarioExecutionStatus string

const (
	ScenarioExecutionRunning   ScenarioExecutionStatus = "running"
	ScenarioExecutionCompleted ScenarioExecutionStatus = "completed"
	ScenarioExecutionFailed    ScenarioExecutionStatus = "failed"
)

func (s ScenarioExecutionStatus) valid() bool {
	switch s {
	case ScenarioExecutionRunning, ScenarioExecutionCompleted, ScenarioExecutionFailed:
		return true
	}
	return false
}

// ScenarioScope is where an execution's candidate runs: one project, one agent
// and one environment. It is what `--reference last` matches on, together with
// the scenario name, and what every candidate run is checked against when the
// execution completes.
type ScenarioScope struct {
	ProjectID   ProjectID
	AgentID     AgentID
	Environment EnvironmentRef
}

func (s ScenarioScope) validate() error {
	if err := validateID("scenario execution project id", string(s.ProjectID)); err != nil {
		return err
	}
	if err := validateID("scenario execution agent id", string(s.AgentID)); err != nil {
		return err
	}
	return validateID("scenario execution environment", string(s.Environment))
}

// ScenarioRepetition is one ordered association: the run that was a side's
// index-th repetition, and the learning scope it ran under.
type ScenarioRepetition struct {
	Side              ComparisonSide
	Index             int
	RunID             EvaluationRunID
	BehavioralProfile BehavioralProfileRef
}

// ScenarioExecution is one persisted execution.
//
// Its state is unexported for the reason EvaluationRun's is: the lifecycle is
// a guarantee only if nothing outside this package can set a status. Values are
// built by NewScenarioExecution and moved by the control plane's lifecycle
// methods through the store, which revalidates every transition.
type ScenarioExecution struct {
	id           ScenarioExecutionID
	scenarioName string
	scope        ScenarioScope
	runs         int

	// reference is the execution whose reference side this one reused, or
	// empty when this execution ran its own reference side.
	reference ScenarioExecutionID

	status     ScenarioExecutionStatus
	startedAt  time.Time
	finishedAt time.Time

	// completionSequence orders the completed executions of one project, 1,
	// 2, 3 … in the order their completions committed. Zero until completed.
	// It is what `last` means: not a clock, not an identifier, and not the
	// RFC3339Nano text, which does not sort chronologically.
	completionSequence uint64
	verdict            GateVerdict

	// repetitions is empty until completed, then exactly N per side, each
	// side in index order 1..N, reference side first.
	repetitions []ScenarioRepetition
}

// NewScenarioExecution returns a running execution. reference is empty for an
// execution that runs its own reference side.
func NewScenarioExecution(
	id ScenarioExecutionID, scenarioName string, scope ScenarioScope, runs int,
	reference ScenarioExecutionID, startedAt time.Time,
) (ScenarioExecution, error) {
	e := ScenarioExecution{
		id: id, scenarioName: scenarioName, scope: scope, runs: runs,
		reference: reference, status: ScenarioExecutionRunning, startedAt: startedAt,
	}
	if err := e.validate(); err != nil {
		return ScenarioExecution{}, err
	}
	return e, nil
}

// ID returns the execution's identifier.
func (e ScenarioExecution) ID() ScenarioExecutionID { return e.id }

// ScenarioName returns the scenario file's name.
func (e ScenarioExecution) ScenarioName() string { return e.scenarioName }

// Scope returns the project, agent and environment the execution ran in.
func (e ScenarioExecution) Scope() ScenarioScope { return e.scope }

// Runs returns N, the repetition count per side.
func (e ScenarioExecution) Runs() int { return e.runs }

// ReferenceExecution returns the execution whose reference side this one
// reused, or "" when it ran its own.
func (e ScenarioExecution) ReferenceExecution() ScenarioExecutionID { return e.reference }

// Status returns the lifecycle state.
func (e ScenarioExecution) Status() ScenarioExecutionStatus { return e.status }

// StartedAt returns when the execution began.
func (e ScenarioExecution) StartedAt() time.Time { return e.startedAt }

// FinishedAt returns when it completed or failed; zero while running.
func (e ScenarioExecution) FinishedAt() time.Time { return e.finishedAt }

// CompletionSequence returns the execution's place among its project's
// completed executions; zero unless completed.
func (e ScenarioExecution) CompletionSequence() uint64 { return e.completionSequence }

// Verdict returns the recorded gate verdict; empty unless completed.
func (e ScenarioExecution) Verdict() GateVerdict { return e.verdict }

// Repetitions returns the ordered associations, reference side first; empty
// unless completed. The slice is a copy.
func (e ScenarioExecution) Repetitions() []ScenarioRepetition {
	return append([]ScenarioRepetition(nil), e.repetitions...)
}

// SideRepetitions returns one side's associations in index order.
func (e ScenarioExecution) SideRepetitions(side ComparisonSide) []ScenarioRepetition {
	out := make([]ScenarioRepetition, 0, e.runs)
	for _, r := range e.repetitions {
		if r.Side == side {
			out = append(out, r)
		}
	}
	return out
}

// complete returns the completed execution. The sequence is assigned by the
// store, inside the transaction that commits it.
func (e ScenarioExecution) complete(
	repetitions []ScenarioRepetition, verdict GateVerdict, sequence uint64, at time.Time,
) (ScenarioExecution, error) {
	if e.status != ScenarioExecutionRunning {
		return ScenarioExecution{}, fmt.Errorf("%w: execution %s is %s, not running",
			ErrScenarioExecutionState, preview(string(e.id)), e.status)
	}
	next := e
	next.status = ScenarioExecutionCompleted
	next.repetitions = append([]ScenarioRepetition(nil), repetitions...)
	next.verdict = verdict
	next.completionSequence = sequence
	next.finishedAt = at
	if err := next.validate(); err != nil {
		return ScenarioExecution{}, err
	}
	return next, nil
}

// fail returns the failed execution. Failing a failed execution returns it
// unchanged: failing is the runner's cleanup, and a retried cleanup must not
// be an error.
func (e ScenarioExecution) fail(at time.Time) (ScenarioExecution, error) {
	switch e.status {
	case ScenarioExecutionFailed:
		return e, nil
	case ScenarioExecutionCompleted:
		return ScenarioExecution{}, fmt.Errorf("%w: execution %s already completed",
			ErrScenarioExecutionState, preview(string(e.id)))
	}
	next := e
	next.status = ScenarioExecutionFailed
	next.finishedAt = at
	if err := next.validate(); err != nil {
		return ScenarioExecution{}, err
	}
	return next, nil
}

// validate checks every invariant a stored or constructed execution must hold.
// A restored row that fails it is corrupt.
func (e ScenarioExecution) validate() error {
	if err := validateID("scenario execution id", string(e.id)); err != nil {
		return err
	}
	if err := validateID("scenario name", e.scenarioName); err != nil {
		return err
	}
	if err := e.scope.validate(); err != nil {
		return err
	}
	if e.runs < 1 || e.runs > MaxRepetitions {
		return fmt.Errorf("%w: scenario execution runs is %d; it must be within 1..%d",
			ErrInvalidRepeatedRequest, e.runs, MaxRepetitions)
	}
	if e.reference != "" {
		if err := validateID("scenario reference execution id", string(e.reference)); err != nil {
			return err
		}
		if e.reference == e.id {
			return fmt.Errorf("%w: execution %s names itself as its reference",
				ErrScenarioReference, preview(string(e.id)))
		}
	}
	if !e.status.valid() {
		return fmt.Errorf("%w: unknown scenario execution status %q", ErrInvalidID, e.status)
	}
	if e.startedAt.IsZero() {
		return fmt.Errorf("%w: scenario execution started_at is not set", ErrInvalidTimestamp)
	}

	running := e.status == ScenarioExecutionRunning
	completed := e.status == ScenarioExecutionCompleted
	if running != e.finishedAt.IsZero() {
		return fmt.Errorf("%w: scenario execution finished_at disagrees with status %s",
			ErrInvalidTimestamp, e.status)
	}
	if !running && e.finishedAt.Before(e.startedAt) {
		return fmt.Errorf("%w: scenario execution finished before it started", ErrInvalidTimestamp)
	}
	if !completed {
		if e.completionSequence != 0 || e.verdict != "" || len(e.repetitions) != 0 {
			return fmt.Errorf("%w: a %s scenario execution carries a completion",
				ErrScenarioExecutionState, e.status)
		}
		return nil
	}
	if e.completionSequence == 0 {
		return fmt.Errorf("%w: a completed scenario execution has no completion sequence",
			ErrScenarioExecutionState)
	}
	if e.verdict != GateVerdictPass && e.verdict != GateVerdictFail {
		return fmt.Errorf("%w: scenario execution verdict %q", ErrInvalidID, e.verdict)
	}
	return validateScenarioRepetitions(e.repetitions, e.runs)
}

// validateScenarioRepetitions requires exactly N associations per side, the
// reference side first, each side indexed 1..N in order, every run and profile
// a valid identifier, no run and no profile named twice. Isolation is the
// control plane's rule (ADR 0053 § 3); restating it here means a stored row
// that broke it is refused as corrupt rather than reused.
func validateScenarioRepetitions(repetitions []ScenarioRepetition, runs int) error {
	if len(repetitions) != 2*runs {
		return fmt.Errorf("%w: %d repetition associations for runs %d; want %d",
			ErrInvalidRepeatedRequest, len(repetitions), runs, 2*runs)
	}
	runIDs := make(map[EvaluationRunID]bool, len(repetitions))
	profiles := make(map[BehavioralProfileRef]bool, len(repetitions))
	for i, r := range repetitions {
		wantSide, wantIndex := SideReference, i+1
		if i >= runs {
			wantSide, wantIndex = SideCandidate, i-runs+1
		}
		if r.Side != wantSide || r.Index != wantIndex {
			return fmt.Errorf("%w: association %d is %s %d; want %s %d",
				ErrInvalidRepeatedRequest, i, r.Side, r.Index, wantSide, wantIndex)
		}
		if err := validateID("scenario repetition run id", string(r.RunID)); err != nil {
			return err
		}
		if err := validateID("scenario repetition behavioral profile", string(r.BehavioralProfile)); err != nil {
			return err
		}
		if runIDs[r.RunID] || profiles[r.BehavioralProfile] {
			return fmt.Errorf("%w: run %s or profile %s is associated twice",
				ErrRepeatedIsolation, preview(string(r.RunID)), preview(string(r.BehavioralProfile)))
		}
		runIDs[r.RunID], profiles[r.BehavioralProfile] = true, true
	}
	return nil
}

// restoreScenarioExecution rebuilds a stored execution, refusing anything a
// live one could not be.
func restoreScenarioExecution(e ScenarioExecution) (ScenarioExecution, error) {
	if err := e.validate(); err != nil {
		return ScenarioExecution{}, fmt.Errorf("%w: scenario execution %s: %v",
			ErrStoreCorrupt, preview(string(e.id)), err)
	}
	return e, nil
}
