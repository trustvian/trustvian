package httpapi

// /v1/scenario-executions — task 078's persisted scenario executions.
//
// Translation only (ADR 0031 § 2). Reference resolution, scope checks and the
// verdict are the control plane's: begin resolves the reference, complete
// calls CompareRepeatedEvaluations once and renders its result exactly as
// POST /v1/evaluations/compare-repeated does.

import (
	"net/http"

	platform "trustvian-platform"
)

// Reference modes. "execution" names one prior execution; "last" asks the
// control plane for the most recently completed one in this scope.
const (
	referenceModeExecution = "execution"
	referenceModeLast      = "last"
)

type scenarioReferenceDTO struct {
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id,omitempty"`
}

type beginScenarioExecutionRequest struct {
	ID           string                `json:"id"`
	ScenarioName string                `json:"scenario_name"`
	Runs         int                   `json:"runs"`
	ProjectID    string                `json:"project_id"`
	AgentID      string                `json:"agent_id"`
	Environment  string                `json:"environment"`
	Reference    *scenarioReferenceDTO `json:"reference,omitempty"`

	// Provenance is task 086's: optional, and nothing is recorded without it.
	Provenance *executionProvenanceDTO `json:"provenance,omitempty"`
}

type completeScenarioExecutionRequest struct {
	ReferenceRunIDs []string          `json:"reference_run_ids,omitempty"`
	CandidateRunIDs []string          `json:"candidate_run_ids"`
	GateLimits      repeatedLimitsDTO `json:"gate_limits"`
}

type scenarioRepetitionDTO struct {
	Side              string `json:"side"`
	Index             int    `json:"index"`
	RunID             string `json:"run_id"`
	BehavioralProfile string `json:"behavioral_profile"`
}

// scenarioExecutionDTO is one execution. Metadata only: identity, scope, N,
// provenance, lifecycle, verdict and the ordered associations.
type scenarioExecutionDTO struct {
	ID                   string                  `json:"id"`
	ScenarioName         string                  `json:"scenario_name"`
	ProjectID            string                  `json:"project_id"`
	AgentID              string                  `json:"agent_id"`
	Environment          string                  `json:"environment"`
	Runs                 int                     `json:"runs"`
	ReferenceExecutionID string                  `json:"reference_execution_id,omitempty"`
	Status               string                  `json:"status"`
	StartedAt            string                  `json:"started_at"`
	FinishedAt           string                  `json:"finished_at,omitempty"`
	CompletionSequence   string                  `json:"completion_sequence,omitempty"`
	Verdict              string                  `json:"verdict,omitempty"`
	Repetitions          []scenarioRepetitionDTO `json:"repetitions"`

	// Provenance is what each side ran (task 086). A reused reference side
	// carries the referenced execution's.
	Provenance executionProvenanceResponseDTO `json:"provenance"`
}

type scenarioExecutionResponse struct {
	Version   string               `json:"version"`
	Execution scenarioExecutionDTO `json:"execution"`
}

// beginScenarioExecutionResponse carries the reference execution as resolved,
// so a runner can report which one `last` named without a second request.
type beginScenarioExecutionResponse struct {
	Version            string                `json:"version"`
	Execution          scenarioExecutionDTO  `json:"execution"`
	ReferenceExecution *scenarioExecutionDTO `json:"reference_execution,omitempty"`
}

// completeScenarioExecutionResponse is the completed execution and the
// comparison, the latter in exactly compare-repeated's shape.
type completeScenarioExecutionResponse struct {
	Version    string                  `json:"version"`
	Execution  scenarioExecutionDTO    `json:"execution"`
	Comparison compareRepeatedResponse `json:"comparison"`
}

// emptyRequest is a body that must be an empty object, for the lifecycle
// route that takes no parameters but still requires JSON.
type emptyRequest struct{}

func (h *Handler) beginScenarioExecution(w http.ResponseWriter, r *http.Request) {
	var request beginScenarioExecutionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	var reference platform.ScenarioReference
	if request.Reference != nil {
		switch request.Reference.Mode {
		case referenceModeLast:
			if request.Reference.ExecutionID != "" {
				h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
					message: `reference mode "last" takes no execution_id`})
				return
			}
			reference.Last = true
		case referenceModeExecution:
			if request.Reference.ExecutionID == "" {
				h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
					message: `reference mode "execution" requires execution_id`})
				return
			}
			reference.ExecutionID = platform.ScenarioExecutionID(request.Reference.ExecutionID)
		default:
			h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: `reference mode must be "execution" or "last"`})
			return
		}
	}
	execution, referenceExecution, err := h.controlPlane.BeginScenarioExecution(r.Context(),
		platform.BeginScenarioExecutionRequest{
			ID:           platform.ScenarioExecutionID(request.ID),
			ScenarioName: request.ScenarioName,
			Runs:         request.Runs,
			Scope: platform.ScenarioScope{
				ProjectID:   platform.ProjectID(request.ProjectID),
				AgentID:     platform.AgentID(request.AgentID),
				Environment: platform.EnvironmentRef(request.Environment),
			},
			Reference:  reference,
			At:         h.now(),
			Provenance: request.Provenance.decode(),
		})
	if err != nil {
		h.writeError(w, err)
		return
	}
	response := beginScenarioExecutionResponse{
		Version: WireVersion, Execution: newScenarioExecutionDTO(execution),
	}
	if referenceExecution.ID() != "" {
		dto := newScenarioExecutionDTO(referenceExecution)
		response.ReferenceExecution = &dto
	}
	writeJSON(w, http.StatusCreated, response)
}

func (h *Handler) getScenarioExecution(w http.ResponseWriter, r *http.Request) {
	execution, err := h.controlPlane.ScenarioExecution(r.Context(),
		platform.ScenarioExecutionID(r.PathValue("execution_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scenarioExecutionResponse{
		Version: WireVersion, Execution: newScenarioExecutionDTO(execution)})
}

func (h *Handler) completeScenarioExecution(w http.ResponseWriter, r *http.Request) {
	var request completeScenarioExecutionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	limits, err := request.GateLimits.decode()
	if err != nil {
		h.writeError(w, err)
		return
	}
	toIDs := func(raw []string) []platform.EvaluationRunID {
		out := make([]platform.EvaluationRunID, len(raw))
		for i, id := range raw {
			out[i] = platform.EvaluationRunID(id)
		}
		return out
	}
	execution, comparison, err := h.controlPlane.CompleteScenarioExecution(r.Context(),
		platform.CompleteScenarioExecutionRequest{
			ID:              platform.ScenarioExecutionID(r.PathValue("execution_id")),
			ReferenceRunIDs: toIDs(request.ReferenceRunIDs),
			CandidateRunIDs: toIDs(request.CandidateRunIDs),
			Limits:          limits,
			At:              h.now(),
		})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, completeScenarioExecutionResponse{
		Version:    WireVersion,
		Execution:  newScenarioExecutionDTO(execution),
		Comparison: newCompareRepeatedResponse(comparison, h.producerVersion),
	})
}

func (h *Handler) failScenarioExecution(w http.ResponseWriter, r *http.Request) {
	var request emptyRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	execution, err := h.controlPlane.FailScenarioExecution(r.Context(),
		platform.ScenarioExecutionID(r.PathValue("execution_id")), h.now())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scenarioExecutionResponse{
		Version: WireVersion, Execution: newScenarioExecutionDTO(execution)})
}

func newScenarioExecutionDTO(e platform.ScenarioExecution) scenarioExecutionDTO {
	repetitions := make([]scenarioRepetitionDTO, 0, 2*e.Runs())
	for _, r := range e.Repetitions() {
		repetitions = append(repetitions, scenarioRepetitionDTO{
			Side: string(r.Side), Index: r.Index, RunID: string(r.RunID),
			BehavioralProfile: string(r.BehavioralProfile),
		})
	}
	sequence := ""
	if e.CompletionSequence() != 0 {
		sequence = u64(e.CompletionSequence())
	}
	scope := e.Scope()
	return scenarioExecutionDTO{
		ID: string(e.ID()), ScenarioName: e.ScenarioName(),
		ProjectID: string(scope.ProjectID), AgentID: string(scope.AgentID),
		Environment: string(scope.Environment), Runs: e.Runs(),
		ReferenceExecutionID: string(e.ReferenceExecution()),
		Status:               string(e.Status()),
		StartedAt:            formatTime(e.StartedAt()),
		FinishedAt:           formatTime(e.FinishedAt()),
		CompletionSequence:   sequence,
		Verdict:              string(e.Verdict()),
		Repetitions:          repetitions,
		Provenance:           newExecutionProvenanceDTO(e),
	}
}

// ---------------------------------------------------------------------
// Discovery and reference check (task 102)
// ---------------------------------------------------------------------

// scenarioExecutionSummaryDTO is one listed execution: the by-id shape without
// its run associations, which a list does not need and the by-id route keeps.
type scenarioExecutionSummaryDTO struct {
	ID                   string `json:"id"`
	ScenarioName         string `json:"scenario_name"`
	ProjectID            string `json:"project_id"`
	AgentID              string `json:"agent_id"`
	Environment          string `json:"environment"`
	Runs                 int    `json:"runs"`
	ReferenceExecutionID string `json:"reference_execution_id,omitempty"`
	Status               string `json:"status"`
	StartedAt            string `json:"started_at"`
	FinishedAt           string `json:"finished_at,omitempty"`
	Verdict              string `json:"verdict,omitempty"`
}

type scenarioExecutionListResponse struct {
	Version    string                        `json:"version"`
	ProjectID  string                        `json:"project_id"`
	Order      string                        `json:"order"`
	Executions []scenarioExecutionSummaryDTO `json:"executions"`
	NextAfter  string                        `json:"next_after,omitempty"`
}

func newScenarioExecutionSummaryDTO(e platform.ScenarioExecution) scenarioExecutionSummaryDTO {
	scope := e.Scope()
	return scenarioExecutionSummaryDTO{
		ID: string(e.ID()), ScenarioName: e.ScenarioName(),
		ProjectID: string(scope.ProjectID), AgentID: string(scope.AgentID),
		Environment: string(scope.Environment), Runs: e.Runs(),
		ReferenceExecutionID: string(e.ReferenceExecution()),
		Status:               string(e.Status()),
		StartedAt:            formatTime(e.StartedAt()),
		FinishedAt:           formatTime(e.FinishedAt()),
		Verdict:              string(e.Verdict()),
	}
}

// listScenarioExecutions serves GET /v1/projects/{project_id}/scenario-executions:
// newest first by start time (ADR 0063), filtered by equality on agent_id,
// environment and scenario.
func (h *Handler) listScenarioExecutions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := platform.ScenarioExecutionFilter{
		ProjectID:    platform.ProjectID(r.PathValue("project_id")),
		AgentID:      platform.AgentID(query.Get("agent_id")),
		Environment:  platform.EnvironmentRef(query.Get("environment")),
		ScenarioName: query.Get("scenario"),
	}
	limit, err := listLimitParam(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	page, err := h.controlPlane.RecentScenarioExecutions(r.Context(), filter, query.Get("after"), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	nextAfter := ""
	if len(page) == limit {
		last := platform.FormatRecencyCursor(platform.RecencyCursor{
			Key: page[len(page)-1].Key, ID: string(page[len(page)-1].Execution.ID()),
		})
		probe, err := h.controlPlane.RecentScenarioExecutions(r.Context(), filter, last, 1)
		if err != nil {
			h.writeError(w, err)
			return
		}
		if len(probe) > 0 {
			nextAfter = last
		}
	}
	executions := make([]scenarioExecutionSummaryDTO, 0, len(page))
	for _, entry := range page {
		executions = append(executions, newScenarioExecutionSummaryDTO(entry.Execution))
	}
	writeJSON(w, http.StatusOK, scenarioExecutionListResponse{
		Version: WireVersion, ProjectID: string(filter.ProjectID), Order: "started_at_desc",
		Executions: executions, NextAfter: nextAfter,
	})
}

// referenceCheckResponse answers whether an execution would be accepted as a
// reference — by the control plane's own validation — and states the
// conditions that answer holds under.
type referenceCheckResponse struct {
	Version     string `json:"version"`
	ExecutionID string `json:"execution_id"`
	Usable      bool   `json:"usable"`
	Reason      string `json:"reason,omitempty"`
	// The conditions the check was made for: a scenario with this repetition
	// count, run in this project and environment. A scenario that differs on
	// any of them is refused by the same validation.
	Runs        int    `json:"runs"`
	ProjectID   string `json:"project_id"`
	Environment string `json:"environment"`
}

// referenceCheck serves GET /v1/scenario-executions/{execution_id}/reference-check.
// A read: it records nothing and runs nothing.
func (h *Handler) referenceCheck(w http.ResponseWriter, r *http.Request) {
	check, err := h.controlPlane.CheckScenarioReference(r.Context(),
		platform.ScenarioExecutionID(r.PathValue("execution_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	scope := check.Execution.Scope()
	writeJSON(w, http.StatusOK, referenceCheckResponse{
		Version: WireVersion, ExecutionID: string(check.Execution.ID()),
		Usable: check.Usable, Reason: check.Reason,
		Runs: check.Execution.Runs(), ProjectID: string(scope.ProjectID),
		Environment: string(scope.Environment),
	})
}
