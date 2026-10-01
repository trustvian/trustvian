package platform

// The scenario execution lifecycle, and reference resolution (task 078,
// ADR 0054).
//
// The control plane decides everything here: which execution `last` names,
// whether a reference is usable, whether a run belongs to the execution, and
// the verdict — through CompareRepeatedEvaluations, unchanged. The runner
// names an execution, runs workloads and reports run identifiers.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrScenarioScope reports a run submitted to an execution it does not belong
// to: another project, environment or — on the candidate side — agent.
var ErrScenarioScope = errors.New("platform: run is outside the scenario execution's scope")

// ScenarioReference is what an execution compares against.
//
// The zero value means "this execution runs its own reference side". Last and
// ExecutionID are mutually exclusive.
type ScenarioReference struct {
	// Last selects the most recently completed execution of the same
	// scenario name in the same project, agent and environment.
	Last bool
	// ExecutionID names one prior execution explicitly.
	ExecutionID ScenarioExecutionID
}

// Recorded reports whether a prior execution's reference side is reused.
func (r ScenarioReference) Recorded() bool { return r.Last || r.ExecutionID != "" }

// BeginScenarioExecutionRequest starts one execution.
type BeginScenarioExecutionRequest struct {
	ID           ScenarioExecutionID
	ScenarioName string
	Runs         int
	Scope        ScenarioScope
	Reference    ScenarioReference
	At           time.Time
}

// CompleteScenarioExecutionRequest finishes one execution.
//
// ReferenceRunIDs is the execution's own reference side, in repetition order,
// and must be empty when the execution reuses a recorded reference: the
// control plane supplies those runs itself, so a client cannot mix them.
type CompleteScenarioExecutionRequest struct {
	ID              ScenarioExecutionID
	ReferenceRunIDs []EvaluationRunID
	CandidateRunIDs []EvaluationRunID
	Limits          RepeatedEvaluationGateLimits
	At              time.Time
}

func (c *ControlPlane) scenarioExecutions() (ScenarioExecutionStore, error) {
	store, ok := c.control.(ScenarioExecutionStore)
	if !ok {
		return nil, ErrScenarioExecutionsUnsupported
	}
	return store, nil
}

// BeginScenarioExecution records a running execution and, when it reuses a
// recorded reference, resolves and validates that reference first.
//
// Resolution happens here, before the runner launches any candidate workload,
// so a missing or unusable reference costs nothing to discover. It returns the
// new execution and the reference execution, which is the zero value when the
// execution runs its own reference side.
//
// `last` selects first and validates second. The newest completed execution of
// this scenario in this scope is the reference or nothing is: when it has a
// different N or its evidence is unusable, the error names it, and an older
// execution is never substituted. A silent fallback would compare against a
// reference the caller did not ask for.
func (c *ControlPlane) BeginScenarioExecution(
	ctx context.Context, request BeginScenarioExecutionRequest,
) (ScenarioExecution, ScenarioExecution, error) {
	store, err := c.scenarioExecutions()
	if err != nil {
		return ScenarioExecution{}, ScenarioExecution{}, err
	}
	if request.Reference.Last && request.Reference.ExecutionID != "" {
		return ScenarioExecution{}, ScenarioExecution{}, fmt.Errorf(
			"%w: a reference is either last or one execution, not both", ErrInvalidRepeatedRequest)
	}
	if request.At.IsZero() {
		return ScenarioExecution{}, ScenarioExecution{}, fmt.Errorf(
			"%w: scenario execution start time is not set", ErrInvalidTimestamp)
	}
	// Validated before any reference is looked up, so a malformed request is
	// reported as one rather than as a missing reference.
	if _, err := NewScenarioExecution(request.ID, request.ScenarioName, request.Scope,
		request.Runs, request.Reference.ExecutionID, request.At); err != nil {
		return ScenarioExecution{}, ScenarioExecution{}, err
	}

	var reference ScenarioExecution
	if request.Reference.Recorded() {
		if request.Reference.Last {
			reference, err = store.LatestCompletedScenarioExecution(ctx, request.ScenarioName, request.Scope)
		} else {
			reference, err = store.ScenarioExecution(ctx, request.Reference.ExecutionID)
		}
		if err != nil {
			return ScenarioExecution{}, ScenarioExecution{}, err
		}
		if _, err := c.usableReference(ctx, reference, request.Runs, request.Scope); err != nil {
			return ScenarioExecution{}, ScenarioExecution{}, err
		}
	}

	execution, err := NewScenarioExecution(request.ID, request.ScenarioName, request.Scope,
		request.Runs, reference.ID(), request.At)
	if err != nil {
		return ScenarioExecution{}, ScenarioExecution{}, err
	}
	if err := store.CreateScenarioExecution(ctx, execution); err != nil {
		return ScenarioExecution{}, ScenarioExecution{}, err
	}
	return execution, reference, nil
}

// usableReference validates a recorded reference against the execution that
// would reuse it, and returns the reference side's runs in repetition order.
//
// Compatibility, for an explicit reference and for `last` alike:
//
//   - the reference is completed — running, failed or interrupted executions
//     never are references, and a completed one may carry a gate FAIL;
//   - it has the same N, never truncated or padded;
//   - it is in the same project and environment, which the comparison itself
//     requires.
//
// The scenario name and agent are what `last` matches on; an explicit
// reference may name another scenario or agent, because naming it is the
// caller's decision (a renamed scenario keeps its history this way).
//
// Then the stored associations are checked against the runs themselves: each
// reference run must still exist, be completed, carry the profile the
// execution recorded, lie in the reference's project and environment, and have
// complete evidence. A completed run with zero records is usable here — check
// 3 is what fails it, as it would in a self-contained execution.
func (c *ControlPlane) usableReference(
	ctx context.Context, reference ScenarioExecution, runs int, scope ScenarioScope,
) ([]EvaluationRunID, error) {
	name := preview(string(reference.ID()))
	if reference.Status() != ScenarioExecutionCompleted {
		return nil, fmt.Errorf("%w: execution %s is %s; only a completed execution can be a reference",
			ErrScenarioReference, name, reference.Status())
	}
	if reference.Runs() != runs {
		return nil, fmt.Errorf("%w: execution %s has runs %d and this scenario has runs %d; "+
			"a reference is never truncated or padded to fit",
			ErrScenarioReference, name, reference.Runs(), runs)
	}
	if reference.Scope().ProjectID != scope.ProjectID || reference.Scope().Environment != scope.Environment {
		return nil, fmt.Errorf("%w: execution %s ran in project %s, environment %s; this execution "+
			"runs in project %s, environment %s",
			ErrScenarioReference, name,
			preview(string(reference.Scope().ProjectID)), preview(string(reference.Scope().Environment)),
			preview(string(scope.ProjectID)), preview(string(scope.Environment)))
	}

	recorded := reference.SideRepetitions(SideReference)
	if len(recorded) != runs {
		return nil, fmt.Errorf("%w: execution %s records %d reference repetitions for runs %d",
			ErrScenarioReference, name, len(recorded), runs)
	}
	ids := make([]EvaluationRunID, 0, runs)
	for _, r := range recorded {
		run, err := c.evaluations.EvaluationRun(ctx, r.RunID)
		if errors.Is(err, ErrStoreNotFound) {
			return nil, fmt.Errorf("%w: execution %s records run %s as reference repetition %d, "+
				"and that run no longer exists", ErrScenarioReference, name, preview(string(r.RunID)), r.Index)
		}
		if err != nil {
			return nil, err
		}
		if run.Status() != RunCompleted {
			return nil, fmt.Errorf("%w: reference repetition %d of execution %s (run %s) is %s, not completed",
				ErrScenarioReference, r.Index, name, preview(string(r.RunID)), run.Status())
		}
		if run.BehavioralProfile() != r.BehavioralProfile {
			return nil, fmt.Errorf("%w: execution %s records profile %s for run %s, which ran under %s",
				ErrScenarioReference, name, preview(string(r.BehavioralProfile)),
				preview(string(r.RunID)), preview(string(run.BehavioralProfile())))
		}
		if err := c.requireRunInScope(ctx, run, reference.Scope(), false); err != nil {
			return nil, fmt.Errorf("%w: execution %s: %v", ErrScenarioReference, name, err)
		}
		_, snapshot, err := c.comparisonEvidence(ctx, run)
		if err != nil {
			return nil, err
		}
		if !snapshot.Complete() {
			return nil, fmt.Errorf("%w: reference repetition %d of execution %s (run %s) saturated "+
				"its behavior snapshot", ErrIncompleteSnapshot, r.Index, name, preview(string(r.RunID)))
		}
		ids = append(ids, r.RunID)
	}
	return ids, nil
}

// requireRunInScope checks one run against an execution's scope: project and
// environment always, and the agent when withAgent — the candidate side.
//
// The reference side is not held to the agent. Comparing two agents in one
// project is coherent for a single comparison (requireSameProject), and a
// self-contained scenario's reference side keeps that latitude.
func (c *ControlPlane) requireRunInScope(
	ctx context.Context, run EvaluationRun, scope ScenarioScope, withAgent bool,
) error {
	if run.Environment() != scope.Environment {
		return fmt.Errorf("%w: run %s is in environment %s, the execution in %s",
			ErrScenarioScope, preview(string(run.ID())), preview(string(run.Environment())),
			preview(string(scope.Environment)))
	}
	candidate, err := c.control.Candidate(ctx, run.CandidateID())
	if err != nil {
		return err
	}
	agent, err := c.control.Agent(ctx, candidate.AgentID())
	if err != nil {
		return err
	}
	if agent.ProjectID() != scope.ProjectID {
		return fmt.Errorf("%w: run %s is in project %s, the execution in %s",
			ErrScenarioScope, preview(string(run.ID())), preview(string(agent.ProjectID())),
			preview(string(scope.ProjectID)))
	}
	if withAgent && agent.ID() != scope.AgentID {
		return fmt.Errorf("%w: run %s belongs to agent %s, the execution to %s",
			ErrScenarioScope, preview(string(run.ID())), preview(string(agent.ID())),
			preview(string(scope.AgentID)))
	}
	return nil
}

// CompleteScenarioExecution evaluates an execution and records it completed.
//
// The verdict is CompareRepeatedEvaluations', called once and unchanged: every
// rule that holds for a self-contained comparison — isolation across all 2N
// profiles, one environment, one-to-one identity, complete evidence, the six
// checks — holds for one that reuses a recorded reference, because it is the
// same call. A gate FAIL completes the execution; a refusal leaves it running,
// for the runner to fail.
func (c *ControlPlane) CompleteScenarioExecution(
	ctx context.Context, request CompleteScenarioExecutionRequest,
) (ScenarioExecution, RepeatedEvaluationComparison, error) {
	none := func(err error) (ScenarioExecution, RepeatedEvaluationComparison, error) {
		return ScenarioExecution{}, RepeatedEvaluationComparison{}, err
	}
	store, err := c.scenarioExecutions()
	if err != nil {
		return none(err)
	}
	if err := validateID("scenario execution id", string(request.ID)); err != nil {
		return none(err)
	}
	if request.At.IsZero() {
		return none(fmt.Errorf("%w: scenario execution completion time is not set", ErrInvalidTimestamp))
	}
	execution, err := store.ScenarioExecution(ctx, request.ID)
	if err != nil {
		return none(err)
	}
	if execution.Status() != ScenarioExecutionRunning {
		return none(fmt.Errorf("%w: execution %s is %s, not running",
			ErrScenarioExecutionState, preview(string(request.ID)), execution.Status()))
	}
	runs := execution.Runs()
	if len(request.CandidateRunIDs) != runs {
		return none(fmt.Errorf("%w: %d candidate runs for an execution with runs %d",
			ErrInvalidRepeatedRequest, len(request.CandidateRunIDs), runs))
	}

	referenceIDs := request.ReferenceRunIDs
	if execution.ReferenceExecution() != "" {
		if len(request.ReferenceRunIDs) != 0 {
			return none(fmt.Errorf("%w: execution %s reuses execution %s's reference side; "+
				"reference runs are not accepted from the client",
				ErrInvalidRepeatedRequest, preview(string(request.ID)),
				preview(string(execution.ReferenceExecution()))))
		}
		reference, err := store.ScenarioExecution(ctx, execution.ReferenceExecution())
		if errors.Is(err, ErrStoreNotFound) {
			return none(fmt.Errorf("%w: reference execution %s no longer exists",
				ErrScenarioReference, preview(string(execution.ReferenceExecution()))))
		}
		if err != nil {
			return none(err)
		}
		// Revalidated rather than trusted from begin: this is the moment the
		// runs are compared, and it is the state now that the verdict is
		// about.
		referenceIDs, err = c.usableReference(ctx, reference, runs, execution.Scope())
		if err != nil {
			return none(err)
		}
	} else {
		if len(referenceIDs) != runs {
			return none(fmt.Errorf("%w: %d reference runs for an execution with runs %d",
				ErrInvalidRepeatedRequest, len(referenceIDs), runs))
		}
		for _, id := range referenceIDs {
			run, err := c.evaluations.EvaluationRun(ctx, id)
			if err != nil {
				return none(err)
			}
			if err := c.requireRunInScope(ctx, run, execution.Scope(), false); err != nil {
				return none(err)
			}
		}
	}
	for _, id := range request.CandidateRunIDs {
		run, err := c.evaluations.EvaluationRun(ctx, id)
		if err != nil {
			return none(err)
		}
		if err := c.requireRunInScope(ctx, run, execution.Scope(), true); err != nil {
			return none(err)
		}
	}

	comparison, err := c.CompareRepeatedEvaluations(ctx, RepeatedEvaluationRequest{
		ReferenceRunIDs: referenceIDs,
		CandidateRunIDs: request.CandidateRunIDs,
		Limits:          request.Limits,
	})
	if err != nil {
		return none(err)
	}

	repetitions := make([]ScenarioRepetition, 0, len(comparison.Repetitions))
	for _, r := range comparison.Repetitions {
		repetitions = append(repetitions, ScenarioRepetition{
			Side: r.Side, Index: r.Index, RunID: r.RunID, BehavioralProfile: r.BehavioralProfile,
		})
	}
	completed, err := store.CompleteScenarioExecution(ctx, request.ID, repetitions,
		comparison.Gate.Verdict(), request.At)
	if err != nil {
		return none(err)
	}
	return completed, comparison, nil
}

// FailScenarioExecution records that an execution ended without a verdict: a
// repetition failed, a run could not be completed, or the comparison was
// refused. Failing a failed execution is not an error; failing a completed
// one is.
func (c *ControlPlane) FailScenarioExecution(
	ctx context.Context, id ScenarioExecutionID, at time.Time,
) (ScenarioExecution, error) {
	store, err := c.scenarioExecutions()
	if err != nil {
		return ScenarioExecution{}, err
	}
	if err := validateID("scenario execution id", string(id)); err != nil {
		return ScenarioExecution{}, err
	}
	if at.IsZero() {
		return ScenarioExecution{}, fmt.Errorf("%w: scenario execution failure time is not set",
			ErrInvalidTimestamp)
	}
	return store.FailScenarioExecution(ctx, id, at)
}

// ScenarioExecution loads one execution with its associations.
func (c *ControlPlane) ScenarioExecution(
	ctx context.Context, id ScenarioExecutionID,
) (ScenarioExecution, error) {
	store, err := c.scenarioExecutions()
	if err != nil {
		return ScenarioExecution{}, err
	}
	if err := validateID("scenario execution id", string(id)); err != nil {
		return ScenarioExecution{}, err
	}
	return store.ScenarioExecution(ctx, id)
}
