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
// reference side. mode is not_recorded: whether the runner asked for "last"
// or named the execution was never persisted.
type reportReferenceDTO struct {
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id"`
}

// reportProducersDTO names who produced the document. cli_version is
// not_recorded: the version of the CLI that ran the execution was never
// persisted, and the report is produced by the control plane.
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
}

// reportTargetDTO is one target row: version 1's fields, and version 2's
// beside them.
type reportTargetDTO struct {
	targetFrequencyDTO
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
	for _, b := range v1.Behaviors {
		if !recorded {
			b.Classification = notRecorded
		}
		comparison.Behaviors = append(comparison.Behaviors, reportBehaviorDTO{repeatedBehaviorDTO: b})
	}
	for _, t := range v1.Targets {
		comparison.Targets = append(comparison.Targets, reportTargetDTO{targetFrequencyDTO: t})
	}

	var reference *reportReferenceDTO
	if e.ReferenceExecution() != "" {
		reference = &reportReferenceDTO{Mode: notRecorded, ExecutionID: string(e.ReferenceExecution())}
	}
	return scenarioReportResponse{
		Version:     ResultDocumentVersion,
		Scenario:    reportScenarioDTO{Name: e.ScenarioName(), Runs: e.Runs()},
		ExecutionID: string(e.ID()),
		Reference:   reference,
		Producers:   reportProducersDTO{CLIVersion: notRecorded, ControlPlaneVersion: producerVersion},
		Provenance:  newExecutionProvenanceDTO(e),
		Comparison:  comparison,
	}
}
