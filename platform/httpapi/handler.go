package httpapi

// The local control-plane HTTP adapter.
//
// Translation only. This package decodes JSON, calls one control-plane
// method, maps the result or the error, and writes a response. It computes no
// comparison, derives no verdict, and touches no store — it cannot even reach
// one, because it holds a *platform.ControlPlane and nothing else.
//
// It also binds no listener. Whichever task composes a server must default to
// loopback; a package that opened a port as a side effect of being imported
// would be a network surface nobody asked for.
//
// See docs/adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	trustvian "github.com/trustvian/trustvian"
	platform "trustvian-platform"
)

// maxAPIRequestBody bounds a JSON request before it is decoded.
//
// DecisionRecord is fixed-shape but not size-bounded: several fields carry
// caller-supplied strings, and task 050 explicitly left a network limit to
// this task. The cap is applied before decoding rather than after, because a
// limit checked after parsing has already spent the memory it was meant to
// protect.
//
// 256 KiB is a transport bound, not a claim about typical records.
const maxAPIRequestBody = 256 << 10

// Handler serves the /v1 control-plane API.
type Handler struct {
	controlPlane *platform.ControlPlane
	now          func() time.Time
	mux          *http.ServeMux

	// realtimeSubscriber is optional. A subscriber and never a publisher: a
	// transport able to publish could fabricate state a client would believe.
	// Nil means GET /v1/realtime reports the capability as unavailable while
	// every other route keeps working.
	realtimeSubscriber platform.RealtimeSubscriber
	heartbeatInterval  time.Duration
}

// Option configures a Handler.
type Option func(*Handler)

// WithClock replaces the lifecycle clock.
//
// It chooses run lifecycle timestamps and nothing else. It must never reach
// behavior, fingerprints, scores, gate thresholds or identity — those are
// properties of evidence, and a clock that influenced them would make an
// evaluation depend on when it was observed.
func WithClock(now func() time.Time) Option {
	return func(h *Handler) {
		if now != nil {
			h.now = now
		}
	}
}

// WithRealtimeSubscriber enables GET /v1/realtime.
//
// Additive, so existing callers need not construct a bus to ignore realtime.
func WithRealtimeSubscriber(subscriber platform.RealtimeSubscriber) Option {
	return func(h *Handler) {
		if subscriber != nil {
			h.realtimeSubscriber = subscriber
		}
	}
}

// WithHeartbeatInterval replaces the SSE keepalive interval.
//
// Exists so tests can observe a heartbeat without waiting on a real clock. It
// carries no domain meaning.
func WithHeartbeatInterval(interval time.Duration) Option {
	return func(h *Handler) {
		if interval > 0 {
			h.heartbeatInterval = interval
		}
	}
}

// NewHandler returns an http.Handler over the control plane.
func NewHandler(controlPlane *platform.ControlPlane, options ...Option) (http.Handler, error) {
	if controlPlane == nil {
		return nil, errors.New("httpapi: handler requires a control plane")
	}
	h := &Handler{
		controlPlane:      controlPlane,
		now:               time.Now,
		mux:               http.NewServeMux(),
		heartbeatInterval: defaultHeartbeatInterval,
	}
	for _, option := range options {
		option(h)
	}
	h.routes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// routes registers the whole surface.
//
// No collection GET. Task 059+ has not specified sort order, cursors, limits
// or scoping, and a list route added here would freeze all four by accident.
func (h *Handler) routes() {
	h.mux.HandleFunc("POST /v1/projects", h.createProject)
	h.mux.HandleFunc("GET /v1/projects/{project_id}", h.getProject)

	h.mux.HandleFunc("POST /v1/agents", h.createAgent)
	h.mux.HandleFunc("GET /v1/agents/{agent_id}", h.getAgent)

	h.mux.HandleFunc("POST /v1/candidates", h.createCandidate)
	h.mux.HandleFunc("GET /v1/candidates/{candidate_id}", h.getCandidate)

	h.mux.HandleFunc("POST /v1/evaluation-runs", h.createEvaluationRun)
	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}", h.getEvaluationRun)

	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/start", h.startRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/complete", h.completeRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/fail", h.failRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/cancel", h.cancelRun)

	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}/progress", h.progress)
	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}/ingest-state", h.ingestState)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/records", h.ingestRecord)

	h.mux.HandleFunc("POST /v1/evaluations/compare", h.compare)

	h.mux.HandleFunc("GET /v1/realtime", h.realtime)
}

// ---------------------------------------------------------------------
// Request and response plumbing
// ---------------------------------------------------------------------

// decodeJSON reads a strictly-decoded request body.
//
// Strict for envelopes and requests this package owns: an unknown field is a
// client misunderstanding worth surfacing. DecisionRecord is decoded
// separately and leniently — see ingestRecord.
func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if err := requireJSONContentType(r); err != nil {
		return err
	}

	limited := http.MaxBytesReader(w, r.Body, maxAPIRequestBody)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apiError{status: http.StatusRequestEntityTooLarge, code: codePayloadTooLarge,
				message: fmt.Sprintf("request body exceeds %d bytes", maxAPIRequestBody)}
		}
		return apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "request body is not valid JSON for this endpoint"}
	}
	// One JSON value per request: trailing content means the client sent
	// something other than what it thinks it sent.
	if decoder.More() {
		return apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "request body contains more than one JSON value"}
	}
	return nil
}

func requireJSONContentType(r *http.Request) error {
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		return apiError{status: http.StatusUnsupportedMediaType, code: codeUnsupportedMediaType,
			message: "content-type must be application/json"}
	}
	// Parameters such as charset are allowed; the media type is what matters.
	media, _, _ := strings.Cut(contentType, ";")
	if !strings.EqualFold(strings.TrimSpace(media), "application/json") {
		return apiError{status: http.StatusUnsupportedMediaType, code: codeUnsupportedMediaType,
			message: "content-type must be application/json"}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// apiError is a transport-shaped failure carrying its own status and code.
type apiError struct {
	status  int
	code    string
	message string
}

func (e apiError) Error() string { return e.message }

// writeError maps any error to a sanitized response.
//
// Nothing internal escapes: no SQL text, database path, driver message or
// stack trace. A corrupt store is a server failure the client cannot fix, so
// it is never dressed up as a 400 the caller might try to "correct".
func (h *Handler) writeError(w http.ResponseWriter, err error) {
	var api apiError
	if errors.As(err, &api) {
		writeJSON(w, api.status, errorEnvelope{
			Version: WireVersion,
			Error:   errorBody{Code: api.code, Message: api.message},
		})
		return
	}

	status, code, message := classify(err)
	writeJSON(w, status, errorEnvelope{
		Version: WireVersion,
		Error:   errorBody{Code: code, Message: message},
	})
}

// classify maps service and store errors onto the wire contract.
//
// Deliberately explicit rather than reflective: each line is a decision about
// what a client is told, and a default that leaked err.Error() would publish
// whatever the storage layer happened to say.
func classify(err error) (int, string, string) {
	switch {
	case errors.Is(err, platform.ErrStoreNotFound):
		return http.StatusNotFound, codeNotFound, "the requested resource does not exist"

	case errors.Is(err, platform.ErrStoreAlreadyExists):
		return http.StatusConflict, codeAlreadyExists, "a resource with that identity already exists"

	case errors.Is(err, platform.ErrIncompleteSnapshot):
		// Not a failed policy and not an unsafe candidate: a measurement that
		// did not finish. Saying otherwise would claim evidence exists.
		return http.StatusConflict, codeIncompleteEvidence,
			"behavioral evidence saturated, so the comparison cannot be computed"

	case errors.Is(err, platform.ErrIngestSequence):
		return http.StatusConflict, codeConflict, err.Error()

	case errors.Is(err, platform.ErrEvaluationState),
		errors.Is(err, platform.ErrStoreConflict),
		errors.Is(err, platform.ErrInvalidTransition):
		return http.StatusConflict, codeConflict, err.Error()

	case errors.Is(err, platform.ErrStoreCorrupt),
		errors.Is(err, platform.ErrStoreSchemaVersion):
		// Storage damage is a server failure. The message is generic on
		// purpose — the detail belongs in operator logs, not a response body.
		return http.StatusInternalServerError, codeInternal, "internal server error"

	case errors.Is(err, platform.ErrInvalidID),
		errors.Is(err, platform.ErrInvalidName),
		errors.Is(err, platform.ErrInvalidMetadata),
		errors.Is(err, platform.ErrInvalidDecisionRecord),
		errors.Is(err, platform.ErrInvalidBehaviorRecord),
		errors.Is(err, platform.ErrEnvironmentMismatch),
		errors.Is(err, platform.ErrBehaviorEnvironmentMismatch),
		errors.Is(err, platform.ErrFingerprintConflict):
		return http.StatusBadRequest, codeInvalidRequest, err.Error()

	default:
		return http.StatusInternalServerError, codeInternal, "internal server error"
	}
}

// ---------------------------------------------------------------------
// Control entities
// ---------------------------------------------------------------------

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var request createProjectRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	project, err := platform.NewProject(platform.ProjectID(request.ID), request.Name)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.controlPlane.CreateProject(r.Context(), project); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newProjectResponse(project))
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	project, err := h.controlPlane.Project(r.Context(), platform.ProjectID(r.PathValue("project_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newProjectResponse(project))
}

func (h *Handler) createAgent(w http.ResponseWriter, r *http.Request) {
	var request createAgentRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	agent, err := platform.NewAgent(
		platform.AgentID(request.ID), platform.ProjectID(request.ProjectID), request.Name)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.controlPlane.CreateAgent(r.Context(), agent); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAgentResponse(agent))
}

func (h *Handler) getAgent(w http.ResponseWriter, r *http.Request) {
	agent, err := h.controlPlane.Agent(r.Context(), platform.AgentID(r.PathValue("agent_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newAgentResponse(agent))
}

func (h *Handler) createCandidate(w http.ResponseWriter, r *http.Request) {
	var request createCandidateRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	candidate, err := platform.NewCandidate(
		platform.CandidateID(request.ID), platform.AgentID(request.AgentID),
		platform.CandidateMetadata{
			Label:          request.Metadata.Label,
			SourceRef:      request.Metadata.SourceRef,
			ArtifactDigest: request.Metadata.ArtifactDigest,
			Model:          request.Metadata.Model,
			ToolsetDigest:  request.Metadata.ToolsetDigest,
			ConfigDigest:   request.Metadata.ConfigDigest,
		})
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.controlPlane.CreateCandidate(r.Context(), candidate); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newCandidateResponse(candidate))
}

func (h *Handler) getCandidate(w http.ResponseWriter, r *http.Request) {
	candidate, err := h.controlPlane.Candidate(r.Context(), platform.CandidateID(r.PathValue("candidate_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newCandidateResponse(candidate))
}

// ---------------------------------------------------------------------
// Evaluation runs
// ---------------------------------------------------------------------

func (h *Handler) createEvaluationRun(w http.ResponseWriter, r *http.Request) {
	var request createEvaluationRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	// The clock supplies the creation time. Identity still comes from the
	// caller — nothing here generates an ID.
	run, err := platform.NewEvaluationRun(
		platform.EvaluationRunID(request.ID),
		platform.CandidateID(request.CandidateID),
		platform.EnvironmentRef(request.Environment),
		platform.BehavioralProfileRef(request.BehavioralProfile),
		h.now())
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.controlPlane.CreateEvaluationRun(r.Context(), run); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEvaluationRunResponse(run))
}

func (h *Handler) getEvaluationRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.controlPlane.EvaluationRun(r.Context(), platform.EvaluationRunID(r.PathValue("run_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEvaluationRunResponse(run))
}

func (h *Handler) startRun(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id platform.EvaluationRunID) (platform.EvaluationRun, error) {
		return h.controlPlane.StartEvaluationRun(r.Context(), id, h.now())
	})
}

func (h *Handler) completeRun(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id platform.EvaluationRunID) (platform.EvaluationRun, error) {
		return h.controlPlane.CompleteEvaluationRun(r.Context(), id, h.now())
	})
}

func (h *Handler) cancelRun(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, func(id platform.EvaluationRunID) (platform.EvaluationRun, error) {
		return h.controlPlane.CancelEvaluationRun(r.Context(), id, h.now())
	})
}

// failRun carries a reason, so it decodes a body where the others do not.
func (h *Handler) failRun(w http.ResponseWriter, r *http.Request) {
	var request failRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	run, err := h.controlPlane.FailEvaluationRun(
		r.Context(), platform.EvaluationRunID(r.PathValue("run_id")), h.now(), request.Reason)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEvaluationRunResponse(run))
}

func (h *Handler) lifecycle(
	w http.ResponseWriter, r *http.Request,
	apply func(platform.EvaluationRunID) (platform.EvaluationRun, error),
) {
	run, err := apply(platform.EvaluationRunID(r.PathValue("run_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEvaluationRunResponse(run))
}

func (h *Handler) progress(w http.ResponseWriter, r *http.Request) {
	report, err := h.controlPlane.EvaluationProgress(
		r.Context(), platform.EvaluationRunID(r.PathValue("run_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newProgressResponse(report))
}

func (h *Handler) ingestState(w http.ResponseWriter, r *http.Request) {
	id := platform.EvaluationRunID(r.PathValue("run_id"))
	state, err := h.controlPlane.EvaluationIngestState(r.Context(), id)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ingestStateResponse{
		Version:      WireVersion,
		RunID:        string(id),
		NextSequence: u64(state.NextSequence()),
	})
}

// ---------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------

func (h *Handler) ingestRecord(w http.ResponseWriter, r *http.Request) {
	var envelope ingestEnvelope
	if err := decodeJSON(w, r, &envelope); err != nil {
		h.writeError(w, err)
		return
	}
	if envelope.Version != WireVersion {
		h.writeError(w, apiError{
			status: http.StatusBadRequest, code: codeUnsupportedVersion,
			message: fmt.Sprintf("ingest envelope version %q is not supported", envelope.Version)})
		return
	}

	sequence, err := platform.ParseSequence(envelope.Sequence)
	if err != nil {
		h.writeError(w, apiError{
			status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "sequence must be a canonical decimal string of at least 1"})
		return
	}

	if len(envelope.Record) == 0 {
		h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "envelope carries no record"})
		return
	}

	// Decoded leniently, on purpose. DecisionRecord is an additive stable
	// contract, so a newer core adding a field must not make this server
	// reject records it otherwise understands. Versioning lives on the
	// envelope above, which *is* strict.
	var record trustvian.DecisionRecord
	if err := json.Unmarshal(envelope.Record, &record); err != nil {
		h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "record is not a valid decision record"})
		return
	}

	result, err := h.controlPlane.IngestDecisionRecord(r.Context(), platform.IngestRequest{
		RunID:             platform.EvaluationRunID(r.PathValue("run_id")),
		Sequence:          sequence,
		BehavioralProfile: platform.BehavioralProfileRef(envelope.BehavioralProfile),
		Record:            record,
	})
	if err != nil {
		h.writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, ingestResponse{
		Version:          WireVersion,
		Disposition:      string(result.Disposition),
		NextSequence:     u64(result.NextSequence),
		RecordCount:      u64(result.RecordCount),
		BehaviorComplete: result.BehaviorComplete,
	})
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

func (h *Handler) compare(w http.ResponseWriter, r *http.Request) {
	var request compareRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}

	limits, err := request.GateLimits.decode()
	if err != nil {
		h.writeError(w, err)
		return
	}

	comparison, err := h.controlPlane.CompareEvaluations(
		r.Context(),
		platform.EvaluationRunID(request.ReferenceRunID),
		platform.EvaluationRunID(request.CandidateRunID),
		limits)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newCompareResponse(comparison))
}

// decode turns the wire limits into task 056's value.
//
// Every limit is required. Zero is a legitimate strict limit — a maximum of 0
// accepts nothing — so treating an omitted field as zero would silently apply
// the strictest possible policy to a caller who simply forgot one.
func (l gateLimitsDTO) decode() (platform.EvaluationGateLimits, error) {
	read := func(name string, value *string) (uint64, error) {
		if value == nil {
			return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: fmt.Sprintf("gate limit %q is required; zero is a strict limit, not a default", name)}
		}
		parsed, err := strconv.ParseUint(*value, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != *value {
			return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
				message: fmt.Sprintf("gate limit %q must be a canonical decimal string", name)}
		}
		return parsed, nil
	}

	added, err := read("max_added_behaviors", l.MaxAddedBehaviors)
	if err != nil {
		return platform.EvaluationGateLimits{}, err
	}
	block, err := read("max_block_decisions", l.MaxBlockDecisions)
	if err != nil {
		return platform.EvaluationGateLimits{}, err
	}
	critical, err := read("max_critical_risk_observations", l.MaxCriticalRiskObservations)
	if err != nil {
		return platform.EvaluationGateLimits{}, err
	}

	return platform.EvaluationGateLimits{
		MaxAddedBehaviors:           added,
		MaxBlockDecisions:           block,
		MaxCriticalRiskObservations: critical,
	}, nil
}
