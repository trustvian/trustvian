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
			Reference: reference,
			At:        h.now(),
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
	}
}
