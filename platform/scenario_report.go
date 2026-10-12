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
// reduction every completion already runs, present_in lists the repetitions
// that reduction counted, and the display strings format integers the
// comparison already holds.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
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

	// PresentIn is, for each entry of Comparison.Behaviors in the same order,
	// the repetitions that observed it, each side in index order.
	PresentIn []BehaviorPresentIn

	// TargetOperational is, for each entry of Comparison.Targets in the same
	// order, that target's latency and error sections (task 107, D16): the
	// computation GET /v1/evaluations/operational runs, summed over the same
	// completed repetitions the comparison counted.
	TargetOperational []OperationalTargetRow
}

// BehaviorPresentIn is the repetitions one behavior was observed in.
type BehaviorPresentIn struct {
	Reference, Candidate []ScenarioRepetition
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

	comparison, inputs, err := c.compareRepeatedWithInputs(ctx, request)
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
	targets, err := targetOperational(comparison.Targets, inputs)
	if err != nil {
		return ScenarioExecutionReport{}, err
	}
	return ScenarioExecutionReport{
		Execution: execution, Comparison: comparison,
		PresentIn:         presentIn(comparison.Behaviors, inputs),
		TargetOperational: targets,
	}, nil
}

// targetOperational is each comparison target's operational row, over the
// completed repetitions — the runs the comparison's own sections sum. A
// repetition that did not complete has no evidence to add, as in the
// comparison.
func targetOperational(targets []RepeatedTargetFrequency, inputs []repetitionInput) ([]OperationalTargetRow, error) {
	rows := targetOperationalRows{}
	for _, in := range inputs {
		if !in.evidence.Completed() {
			continue
		}
		if err := rows.addRun(in.evidence.Side, in.entries); err != nil {
			return nil, err
		}
	}
	out := make([]OperationalTargetRow, 0, len(targets))
	for _, t := range targets {
		out = append(out, rows.row(t.Target))
	}
	return out, nil
}

// presentIn lists, per behavior, the completed repetitions whose snapshot
// observed it — the same entries the presence counts were summed from.
func presentIn(behaviors []RepeatedBehaviorPresence, inputs []repetitionInput) []BehaviorPresentIn {
	index := make(map[string]int, len(behaviors))
	out := make([]BehaviorPresentIn, len(behaviors))
	for i, b := range behaviors {
		index[b.FingerprintID] = i
	}
	for _, in := range inputs {
		if !in.evidence.Completed() {
			continue
		}
		at := ScenarioRepetition{Side: in.evidence.Side, Index: in.evidence.Index,
			RunID: in.evidence.RunID, BehavioralProfile: in.evidence.BehavioralProfile}
		for _, entry := range in.entries {
			i, ok := index[entry.FingerprintID]
			if !ok || entry.Observations == 0 {
				continue
			}
			if at.Side == SideReference {
				out[i].Reference = append(out[i].Reference, at)
			} else {
				out[i].Candidate = append(out[i].Candidate, at)
			}
		}
	}
	return out
}

// NotAvailableText is what a display string reads when the evidence behind it
// is absent: a side with no completed repetition, or a ratio over a reference
// that never called the target.
const NotAvailableText = "not available"

// RenderPresence renders a presence count over N as "k/N".
func RenderPresence(present uint64, runs int) string {
	return strconv.FormatUint(present, 10) + "/" + strconv.Itoa(runs)
}

// RenderCallsPerRun renders a side's per-run call range as "min–max", or
// NotAvailableText with no completed repetition.
func RenderCallsPerRun(s FrequencyStats) string {
	if !s.Available() {
		return NotAvailableText
	}
	return strconv.FormatUint(s.CallsPerRunMin, 10) + "–" + strconv.FormatUint(s.CallsPerRunMax, 10)
}

// RenderMilli renders an integer count of thousandths as a decimal, exactly:
// 2400 → "2.4", 2456 → "2.456", 3000 → "3". No digit is rounded away, so the
// string says what the integer says.
func RenderMilli(milli uint64) string {
	whole, fraction := milli/1000, milli%1000
	if fraction == 0 {
		return strconv.FormatUint(whole, 10)
	}
	digits := strings.TrimRight(fmt.Sprintf("%03d", fraction), "0")
	return strconv.FormatUint(whole, 10) + "." + digits
}

// RenderCallsPerRunMean renders a side's mean calls per run from its
// thousandths, or NotAvailableText with no completed repetition.
func RenderCallsPerRunMean(s FrequencyStats) string {
	if !s.Available() {
		return NotAvailableText
	}
	return RenderMilli(s.CallsPerRunMeanMilli)
}

// RenderCallRatio renders a target's candidate-to-reference call ratio from
// its permille as RenderRatio does, or NotAvailableText when the reference
// never called the target.
func RenderCallRatio(t RepeatedTargetFrequency) string {
	if !t.RatioAvailable {
		return NotAvailableText
	}
	return RenderRatio(t.CallRatioPermille)
}
