package platform

// The report of one scenario execution (task 107, decisions D10 and D11).
//
// A report is keyed by a persisted execution, because a comparison has no
// identity of its own. It is the execution's evidence read back from what is
// persisted — the recorded runs' snapshots and aggregates, the execution's own
// provenance — and its gate as the execution recorded it when it completed.
// The gate is never re-evaluated here: an execution completed before schema 14
// recorded none, and its report says so rather than deriving one from today's
// code.
//
// Presentation only. Nothing here is new evidence: the comparison is the
// reduction every completion already runs.

import (
	"context"
	"fmt"
)

// ScenarioExecutionReport is one completed execution's evidence, as its report
// presents it.
type ScenarioExecutionReport struct {
	Execution ScenarioExecution

	// Comparison is the execution's repeated comparison, reduced from its
	// recorded runs. Its Gate and Limits are the ones the execution recorded.
	// When the execution recorded no gate (Execution.GateRecord().Recorded()
	// is false) they are the zero value and every behavior's Classification is
	// empty: classification needs k and j, which were not kept.
	Comparison RepeatedEvaluationComparison
}

// ScenarioExecutionReport assembles the report of one completed execution.
// A running or failed execution has no comparison to report and is
// ErrScenarioExecutionState.
func (c *ControlPlane) ScenarioExecutionReport(
	ctx context.Context, id ScenarioExecutionID,
) (ScenarioExecutionReport, error) {
	execution, err := c.ScenarioExecution(ctx, id)
	if err != nil {
		return ScenarioExecutionReport{}, err
	}
	if execution.Status() != ScenarioExecutionCompleted {
		return ScenarioExecutionReport{}, fmt.Errorf("%w: execution %s is %s; only a completed execution has a report",
			ErrScenarioExecutionState, preview(string(id)), execution.Status())
	}
	request := RepeatedEvaluationRequest{}
	for _, r := range execution.SideRepetitions(SideReference) {
		request.ReferenceRunIDs = append(request.ReferenceRunIDs, r.RunID)
	}
	for _, r := range execution.SideRepetitions(SideCandidate) {
		request.CandidateRunIDs = append(request.CandidateRunIDs, r.RunID)
	}
	gate := execution.GateRecord()
	if gate.Recorded() {
		request.Limits = gate.Limits()
	} else {
		// No limits were kept. The reduction still needs a valid k and j to
		// run, and every output that depends on them — the classification,
		// the checks, the verdict — is discarded below, never shown.
		request.Limits = RepeatedEvaluationGateLimits{AddedCandidatePresenceMinimum: 1}
	}

	comparison, err := c.compareRepeated(ctx, request)
	if err != nil {
		return ScenarioExecutionReport{}, err
	}
	// The execution's own record, exactly as its completion stated it.
	comparison.Sameness, comparison.Warnings = newComparisonSameness(
		execution.Provenance(SideReference), execution.Provenance(SideCandidate))
	if gate.Recorded() {
		comparison.Limits, comparison.Gate = gate.Limits(), gate.Gate()
	} else {
		comparison.Limits, comparison.Gate = RepeatedEvaluationGateLimits{}, RepeatedEvaluationGateResult{}
		for i := range comparison.Behaviors {
			comparison.Behaviors[i].Classification = ""
		}
	}
	return ScenarioExecutionReport{Execution: execution, Comparison: comparison}, nil
}
