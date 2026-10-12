package httpapi

// GET /v1/scenario-executions/{execution_id}/report — task 107's report, the
// result document version 2 (decisions D10 and D11). JSON only: the Markdown
// is rendered from this document by the one renderer, outside the control
// plane.
//
// Translation only (ADR 0031 § 2). The control plane assembles the evidence and
// the recorded gate; this renders it in version 1's shape, field for field,
// with version 2's additions beside it.

import (
	"net/http"

	platform "trustvian-platform"
)

// ResultDocumentVersion is the report's document version. Version "1" is what
// `trustvian eval run --json` writes; version "2" is version 1 plus the
// report's additions, all of them additive.
const ResultDocumentVersion = "2"

// notRecorded is the state of anything the execution did not keep: the gate
// of an execution completed before schema 14, and the fields of version 1 that
// only the runner knew.
const notRecorded = "not_recorded"

// Gate record states.
const (
	gateRecorded    = "recorded"
	gateNotRecorded = notRecorded
)

// costReasonNoPricing says why a comparison has no cost section.
const costReasonNoPricing = "pricing_not_configured"

// scenarioReportResponse is result document version 2. The members up to and
// including comparison are version 1's, in its order.
type scenarioReportResponse struct {
	Version     string              `json:"version"`
	Scenario    reportScenarioDTO   `json:"scenario"`
	ExecutionID string              `json:"execution_id"`
	Reference   *reportReferenceDTO `json:"reference,omitempty"`
	Producers   reportProducersDTO  `json:"producers"`
	// Provenance is the execution's recorded provenance, exactly as GET
	// /v1/scenario-executions/{id} returns it.
	Provenance executionProvenanceResponseDTO `json:"provenance"`
	Comparison reportComparisonDTO            `json:"comparison"`
}

type reportScenarioDTO struct {
	Name string `json:"name"`
	Runs int    `json:"runs"`
}

// reportReferenceDTO is present when the execution reused a recorded
// reference side. mode is how the runner asked for it, "execution" or "last",
// as recorded since schema 15 (D14); not_recorded for an execution begun
// before it, or by a client that sent none.
type reportReferenceDTO struct {
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id"`
}

// reportProducersDTO names who produced the document. cli_version is the
// version of the CLI that ran the execution, as recorded since schema 15
// (D14); not_recorded for an execution begun before it, or by a client that
// sent none. The report itself is produced by the control plane.
type reportProducersDTO struct {
	CLIVersion          string `json:"cli_version"`
	ControlPlaneVersion string `json:"control_plane_version"`
}

// reportComparisonDTO is compare-repeated's response shape, member for member
// and in its order, with the gate as the execution recorded it.
type reportComparisonDTO struct {
	Version string `json:"version"`
	Runs    int    `json:"runs"`
	// GateLimits are the recorded limits; absent when the gate was not
	// recorded.
	GateLimits           *repeatedLimitsResponseDTO `json:"gate_limits,omitempty"`
	Repetitions          []repetitionDTO            `json:"repetitions"`
	Behaviors            []reportBehaviorDTO        `json:"behaviors"`
	Gate                 reportGateDTO              `json:"gate"`
	Operational          reportOperationalDTO       `json:"operational"`
	Targets              []reportTargetDTO          `json:"targets"`
	LostTransitions      string                     `json:"lost_transitions"`
	Suggestions          []suggestionDTO            `json:"suggestions"`
	SuggestionsTruncated bool                       `json:"suggestions_truncated"`
	Sameness             samenessDTO                `json:"sameness"`
	Warnings             []warningDTO               `json:"warnings"`
	Producer             producerDTO                `json:"producer"`
}

// reportBehaviorDTO is one behavior row: version 1's fields, and version 2's
// beside them. classification is not_recorded when the gate was not
// recorded, because classifying needs the k and j that were not kept.
type reportBehaviorDTO struct {
	repeatedBehaviorDTO
	Display   behaviorDisplayDTO `json:"display"`
	PresentIn presentInDTO       `json:"present_in"`
}

// behaviorDisplayDTO is the row's numbers as text, produced here so no adapter
// divides, rounds or formats a fraction. Each string formats integers the row
// already carries, which stay beside it. A side with no completed repetition
// reads "not available" for its per-run figures.
type behaviorDisplayDTO struct {
	ReferencePresence        string `json:"reference_presence"`
	CandidatePresence        string `json:"candidate_presence"`
	ReferenceCallsPerRun     string `json:"reference_calls_per_run"`
	CandidateCallsPerRun     string `json:"candidate_calls_per_run"`
	ReferenceCallsPerRunMean string `json:"reference_calls_per_run_mean"`
	CandidateCallsPerRunMean string `json:"candidate_calls_per_run_mean"`
}

// presentInDTO is the repetitions that observed the behavior, each side in
// index order, with their runs: any reference run and candidate run named here
// resolve the behavior through GET /v1/evidence (task 085). The reader chooses
// the pair; nothing here picks a representative run.
type presentInDTO struct {
	Reference []presentRepetitionDTO `json:"reference"`
	Candidate []presentRepetitionDTO `json:"candidate"`
}

type presentRepetitionDTO struct {
	Index int    `json:"index"`
	RunID string `json:"run_id"`
}

// reportTargetDTO is one target row: version 1's fields, and version 2's
// beside them.
type reportTargetDTO struct {
	targetFrequencyDTO
	Display targetDisplayDTO `json:"display"`
	// Operational is the target's latency and error sections (D16), exactly
	// as GET /v1/evaluations/operational states them for the same runs.
	Operational reportTargetOperationalDTO `json:"operational"`
}

// reportTargetOperationalDTO is one target's latency buckets, span and HTTP
// status classes and 429s, each side summed over its completed repetitions
// with runs_with_evidence beside it. A section that cannot be compared says
// so and carries no delta, as on the operational route.
type reportTargetOperationalDTO struct {
	Latency latencySectionDTO `json:"latency"`
	Errors  errorsSectionDTO  `json:"errors"`
}

// targetDisplayDTO is the target row's numbers as text. call_ratio is "2.4×"
// from call_ratio_permille, or "not available" when the reference never called
// the target.
type targetDisplayDTO struct {
	ReferenceCallsPerRun     string `json:"reference_calls_per_run"`
	CandidateCallsPerRun     string `json:"candidate_calls_per_run"`
	ReferenceCallsPerRunMean string `json:"reference_calls_per_run_mean"`
	CandidateCallsPerRunMean string `json:"candidate_calls_per_run_mean"`
	CallRatio                string `json:"call_ratio"`
}

// reportGateDTO is the gate the execution recorded when it completed: its six
// checks, its four frequency checks and its verdict, never re-evaluated.
// state is recorded, or not_recorded for an execution completed before schema
// 14, which then carries no check and no verdict.
type reportGateDTO struct {
	Checks          []repeatedCheckDTO  `json:"checks,omitempty"`
	Verdict         string              `json:"verdict,omitempty"`
	FrequencyChecks []frequencyCheckDTO `json:"frequency_checks,omitempty"`
	State           string              `json:"state"`
}

// reportOperationalDTO is task 087's sections. Each section already says when
// it is not comparable and then carries no delta; cost_reason says why there
// is no cost section at all.
type reportOperationalDTO struct {
	operationalSectionsDTO
	CostReason string `json:"cost_reason,omitempty"`
}

func (h *Handler) scenarioExecutionReport(w http.ResponseWriter, r *http.Request) {
	report, err := h.controlPlane.ScenarioExecutionReport(r.Context(),
		platform.ScenarioExecutionID(r.PathValue("execution_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newScenarioReportResponse(report, h.producerVersion))
}

func newScenarioReportResponse(report platform.ScenarioExecutionReport, producerVersion string) scenarioReportResponse {
	e, c := report.Execution, report.Comparison
	recorded := e.GateRecord().Recorded()
	// Version 1's comparison, built by the one function compare-repeated and
	// completion use, then carried member for member.
	v1 := newCompareRepeatedResponse(c, producerVersion)

	comparison := reportComparisonDTO{
		Version: v1.Version, Runs: v1.Runs,
		Repetitions:          v1.Repetitions,
		Behaviors:            make([]reportBehaviorDTO, 0, len(v1.Behaviors)),
		Operational:          reportOperationalDTO{operationalSectionsDTO: v1.Operational},
		Targets:              make([]reportTargetDTO, 0, len(v1.Targets)),
		LostTransitions:      v1.LostTransitions,
		Suggestions:          v1.Suggestions,
		SuggestionsTruncated: v1.SuggestionsTruncated,
		Sameness:             v1.Sameness,
		Warnings:             v1.Warnings,
		Producer:             v1.Producer,
		Gate:                 reportGateDTO{State: gateNotRecorded},
	}
	if recorded {
		limits := v1.GateLimits
		comparison.GateLimits = &limits
		comparison.Gate = reportGateDTO{Checks: v1.Gate.Checks, Verdict: v1.Gate.Verdict,
			FrequencyChecks: v1.Gate.FrequencyChecks, State: gateRecorded}
	}
	if c.Cost == nil {
		comparison.Operational.CostReason = costReasonNoPricing
	}
	for i, b := range v1.Behaviors {
		if !recorded {
			b.Classification = notRecorded
		}
		behavior := c.Behaviors[i]
		comparison.Behaviors = append(comparison.Behaviors, reportBehaviorDTO{
			repeatedBehaviorDTO: b,
			Display: behaviorDisplayDTO{
				ReferencePresence:        platform.RenderPresence(behavior.ReferenceRunsPresent, c.Runs),
				CandidatePresence:        platform.RenderPresence(behavior.CandidateRunsPresent, c.Runs),
				ReferenceCallsPerRun:     platform.RenderCallsPerRun(behavior.Reference),
				CandidateCallsPerRun:     platform.RenderCallsPerRun(behavior.Candidate),
				ReferenceCallsPerRunMean: platform.RenderCallsPerRunMean(behavior.Reference),
				CandidateCallsPerRunMean: platform.RenderCallsPerRunMean(behavior.Candidate),
			},
			PresentIn: newPresentInDTO(report.PresentIn[i]),
		})
	}
	for i, t := range v1.Targets {
		target := c.Targets[i]
		comparison.Targets = append(comparison.Targets, reportTargetDTO{
			targetFrequencyDTO: t,
			Display: targetDisplayDTO{
				ReferenceCallsPerRun:     platform.RenderCallsPerRun(target.Reference),
				CandidateCallsPerRun:     platform.RenderCallsPerRun(target.Candidate),
				ReferenceCallsPerRunMean: platform.RenderCallsPerRunMean(target.Reference),
				CandidateCallsPerRunMean: platform.RenderCallsPerRunMean(target.Candidate),
				CallRatio:                platform.RenderCallRatio(target),
			},
			Operational: reportTargetOperationalDTO{
				Latency: newLatencySectionDTO(report.TargetOperational[i].Sections.Latency),
				Errors:  newErrorsSectionDTO(report.TargetOperational[i].Sections.Errors),
			},
		})
	}

	var reference *reportReferenceDTO
	if e.ReferenceExecution() != "" {
		reference = &reportReferenceDTO{Mode: orNotRecorded(string(e.ReferenceMode())),
			ExecutionID: string(e.ReferenceExecution())}
	}
	return scenarioReportResponse{
		Version:     ResultDocumentVersion,
		Scenario:    reportScenarioDTO{Name: e.ScenarioName(), Runs: e.Runs()},
		ExecutionID: string(e.ID()),
		Reference:   reference,
		Producers: reportProducersDTO{CLIVersion: orNotRecorded(e.CLIVersion()),
			ControlPlaneVersion: producerVersion},
		Provenance: newExecutionProvenanceDTO(e),
		Comparison: comparison,
	}
}

// orNotRecorded is a recorded fact, or not_recorded when there is none.
func orNotRecorded(value string) string {
	if value == "" {
		return notRecorded
	}
	return value
}

func newPresentInDTO(p platform.BehaviorPresentIn) presentInDTO {
	side := func(repetitions []platform.ScenarioRepetition) []presentRepetitionDTO {
		out := make([]presentRepetitionDTO, 0, len(repetitions))
		for _, r := range repetitions {
			out = append(out, presentRepetitionDTO{Index: r.Index, RunID: string(r.RunID)})
		}
		return out
	}
	return presentInDTO{Reference: side(p.Reference), Candidate: side(p.Candidate)}
}
