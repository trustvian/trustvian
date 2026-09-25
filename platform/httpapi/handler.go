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
	"context"
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

	// realtimeWriteTimeout bounds each individual SSE write. It is not a
	// heartbeat interval: one is how often a quiet stream proves it is alive,
	// the other is how long a single socket write may block before the
	// connection is abandoned. Tying them together would make a slower
	// keepalive silently buy a client more time to stall.
	realtimeWriteTimeout time.Duration

	// configErr carries an option's rejection to NewHandler.
	//
	// Options cannot return an error, and a rejected option must not be
	// silently ignored — a caller that asked for a 0 write timeout would
	// otherwise get the default and believe it got what it asked for.
	configErr error
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

// WithRealtimeWriteTimeout replaces the per-write SSE deadline.
//
// Rejected rather than clamped when out of range: a caller asking for an
// unbounded or absurd write deadline is asking for the bound this handler
// claims to have, and answering with a quietly different number would make the
// claim untrue in a way nothing surfaces.
func WithRealtimeWriteTimeout(timeout time.Duration) Option {
	return func(h *Handler) {
		switch {
		case timeout <= 0:
			h.configErr = errors.New("httpapi: realtime write timeout must be positive")
		case timeout > maxRealtimeWriteTimeout:
			h.configErr = fmt.Errorf(
				"httpapi: realtime write timeout must not exceed %s", maxRealtimeWriteTimeout)
		default:
			h.realtimeWriteTimeout = timeout
		}
	}
}

// NewHandler returns an http.Handler over the control plane.
func NewHandler(controlPlane *platform.ControlPlane, options ...Option) (http.Handler, error) {
	if controlPlane == nil {
		return nil, errors.New("httpapi: handler requires a control plane")
	}
	h := &Handler{
		controlPlane:         controlPlane,
		now:                  time.Now,
		mux:                  http.NewServeMux(),
		heartbeatInterval:    defaultHeartbeatInterval,
		realtimeWriteTimeout: defaultRealtimeWriteTimeout,
	}
	for _, option := range options {
		option(h)
	}
	if h.configErr != nil {
		return nil, h.configErr
	}
	h.routes()
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

// routes registers the whole surface.
//
// One collection GET, added by task 065 and scoped to one project's
// environments. Task 058 deferred collection semantics because sort order,
// cursors, limits and scoping were undecided; they are decided for this
// entity and this entity only. Projects, agents, candidates and runs still
// have no list route.
func (h *Handler) routes() {
	h.mux.HandleFunc("POST /v1/projects", h.createProject)
	h.mux.HandleFunc("GET /v1/projects/{project_id}", h.getProject)

	h.mux.HandleFunc("POST /v1/agents", h.createAgent)
	h.mux.HandleFunc("GET /v1/agents/{agent_id}", h.getAgent)

	h.mux.HandleFunc("POST /v1/candidates", h.createCandidate)
	h.mux.HandleFunc("GET /v1/candidates/{candidate_id}", h.getCandidate)

	// One collection route, for one entity, because task 065 is the milestone
	// with a concrete need for it: a caller cannot open an environment by ref
	// when knowing the refs is the question. Its scope, order, cursor and
	// limit are all decided — see listEnvironments.
	h.mux.HandleFunc("POST /v1/environments", h.createEnvironment)
	h.mux.HandleFunc("GET /v1/projects/{project_id}/environments", h.listEnvironments)
	h.mux.HandleFunc("GET /v1/projects/{project_id}/environments/{environment_ref}",
		h.getEnvironment)
	h.mux.HandleFunc("POST /v1/projects/{project_id}/environments/{environment_ref}/configure",
		h.configureEnvironment)
	h.mux.HandleFunc("POST /v1/projects/{project_id}/environments/{environment_ref}/archive",
		h.archiveEnvironment)
	h.mux.HandleFunc("POST /v1/projects/{project_id}/environments/{environment_ref}/activate",
		h.activateEnvironment)

	h.mux.HandleFunc("POST /v1/evaluation-runs", h.createEvaluationRun)
	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}", h.getEvaluationRun)

	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/start", h.startRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/complete", h.completeRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/fail", h.failRun)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/cancel", h.cancelRun)

	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}/progress", h.progress)
	h.mux.HandleFunc("GET /v1/evaluation-runs/{run_id}/ingest-state", h.ingestState)
	h.mux.HandleFunc("POST /v1/evaluation-runs/{run_id}/records", h.ingestRecord)

	// Three promotion routes, added by task 066. POST is flat because the
	// scope is derived from the runs rather than routed; GET by identifier is
	// flat because promotion identity is global, matching /v1/agents/{id}.
	h.mux.HandleFunc("POST /v1/promotions", h.createPromotion)
	h.mux.HandleFunc("GET /v1/promotions/{promotion_id}", h.getPromotion)
	h.mux.HandleFunc("GET /v1/projects/{project_id}/promotions", h.listPromotions)

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
		errors.Is(err, platform.ErrInvalidTransition),
		// An archived environment and a full project are both "the request is
		// coherent, the current configuration refuses it" — the same class a
		// stale revision is in, and the same code a client already handles.
		errors.Is(err, platform.ErrEnvironmentUnavailable),
		errors.Is(err, platform.ErrEnvironmentLimit):
		return http.StatusConflict, codeConflict, err.Error()

	case errors.Is(err, platform.ErrPromotionOrder):
		// The configuration may legitimately change, so this is a conflict
		// rather than a permanently invalid request — the same distinction
		// ErrEnvironmentUnavailable already draws.
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
		// A comparison spanning two projects can never succeed, whatever the
		// state changes to, so it is the caller's request that is wrong.
		errors.Is(err, platform.ErrComparisonScope),
		// Two runs that are not a promotion-eligible pair — different agents,
		// or different environments. The pairing can never succeed whatever
		// the state becomes, so it is the request that is wrong.
		errors.Is(err, platform.ErrPromotionScope),
		errors.Is(err, platform.ErrInvalidGateEvidence),
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
// Environments
// ---------------------------------------------------------------------

func (h *Handler) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var request createEnvironmentRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	// Two constructors rather than a create-then-rank, because a create is
	// one act: an environment created with a rank is still revision 1.
	var environment platform.Environment
	var err error
	if request.Rank != nil {
		environment, err = platform.NewRankedEnvironment(
			platform.EnvironmentRef(request.Ref), platform.ProjectID(request.ProjectID),
			request.Name, *request.Rank)
	} else {
		environment, err = platform.NewEnvironment(
			platform.EnvironmentRef(request.Ref), platform.ProjectID(request.ProjectID), request.Name)
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	if err := h.controlPlane.CreateEnvironment(r.Context(), environment); err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEnvironmentResponse(environment))
}

func (h *Handler) getEnvironment(w http.ResponseWriter, r *http.Request) {
	environment, err := h.controlPlane.Environment(r.Context(),
		platform.ProjectID(r.PathValue("project_id")),
		platform.EnvironmentRef(r.PathValue("environment_ref")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentResponse(environment))
}

// listEnvironments returns one bounded page of a project's environments.
//
// Keyset pagination on `ref`, which is immutable: rank is mutable, and a
// cursor over a mutable key could move a row from a page the caller has not
// read into one it already read. The page is never larger than the limit,
// whatever the project holds — a migrated project may hold more environments
// than may now be created, so the response bound cannot rely on the creation
// cap.
func (h *Handler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")

	limit, err := environmentLimitParam(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	after := r.URL.Query().Get("after")

	page, err := h.controlPlane.ProjectEnvironments(r.Context(),
		platform.ProjectID(projectID), platform.EnvironmentRef(after), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}

	nextAfter, err := h.environmentContinuation(r.Context(), projectID, page, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentListResponse(projectID, page, nextAfter))
}

// environmentContinuation reports the cursor to publish after this page, or
// "" when the traversal is complete.
//
// A short page is the end: the store returned everything it had before
// reaching the limit. A full page is ambiguous — the project may hold exactly
// this many rows, or more — and the ambiguity is resolved by asking for one
// row past the last ref.
//
// The obvious alternative is to fetch limit+1 rows and drop the extra, and
// that is what this used to do. It required the store to accept 65, so the
// public contract said "at most 64 per page" and meant 65, with an off-by-one
// that existed only because one transport wanted it. A second bounded call
// keeps the store's range honest and costs an indexed single-row lookup on
// the primary key, once per full page.
func (h *Handler) environmentContinuation(
	ctx context.Context, projectID string, page []platform.Environment, limit int,
) (string, error) {
	if len(page) < limit {
		return "", nil
	}
	last := page[len(page)-1].Ref()
	probe, err := h.controlPlane.ProjectEnvironments(
		ctx, platform.ProjectID(projectID), last, 1)
	if err != nil {
		return "", err
	}
	if len(probe) == 0 {
		return "", nil
	}
	return string(last), nil
}

// environmentLimitParam reads and bounds ?limit=.
//
// Absent means the maximum. A value above it is refused rather than clamped:
// a client that asked for 500 and silently received 64 would conclude it had
// seen everything.
func environmentLimitParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return platform.MaxEnvironmentPage, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > platform.MaxEnvironmentPage {
		return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: fmt.Sprintf("limit must be an integer between 1 and %d", platform.MaxEnvironmentPage)}
	}
	return limit, nil
}

func (h *Handler) configureEnvironment(w http.ResponseWriter, r *http.Request) {
	var request configureEnvironmentRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	if err := requireRevision(request.Revision); err != nil {
		h.writeError(w, err)
		return
	}
	environment, err := h.controlPlane.ConfigureEnvironment(r.Context(),
		platform.ProjectID(r.PathValue("project_id")),
		platform.EnvironmentRef(r.PathValue("environment_ref")),
		platform.ConfigureEnvironmentRequest{
			Revision:  request.Revision,
			Name:      request.Name,
			Rank:      request.Rank,
			ClearRank: request.ClearRank,
		})
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentResponse(environment))
}

func (h *Handler) archiveEnvironment(w http.ResponseWriter, r *http.Request) {
	h.environmentLifecycle(w, r, h.controlPlane.ArchiveEnvironment)
}

func (h *Handler) activateEnvironment(w http.ResponseWriter, r *http.Request) {
	h.environmentLifecycle(w, r, h.controlPlane.ActivateEnvironment)
}

// environmentLifecycle is the shape archive and activate share: a revision in,
// the new environment out.
func (h *Handler) environmentLifecycle(
	w http.ResponseWriter, r *http.Request,
	transition func(context.Context, platform.ProjectID, platform.EnvironmentRef, uint64) (platform.Environment, error),
) {
	var request revisionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	if err := requireRevision(request.Revision); err != nil {
		h.writeError(w, err)
		return
	}
	environment, err := transition(r.Context(),
		platform.ProjectID(r.PathValue("project_id")),
		platform.EnvironmentRef(r.PathValue("environment_ref")),
		request.Revision)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentResponse(environment))
}

// requireRevision refuses a mutation that did not say what it was changing.
//
// Revision 0 never exists — a new environment starts at 1 — so an absent
// field and an explicit zero are the same mistake, and both are refused
// rather than treated as "whatever is current".
func requireRevision(revision uint64) error {
	if revision == 0 {
		return apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "revision is required; it is the revision the caller read"}
	}
	return nil
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

// ---------------------------------------------------------------------
// Promotions
// ---------------------------------------------------------------------

// createPromotion records one decision.
//
// The handler translates and nothing else: it decodes, hands the control
// plane a fixed-shape request and a clock, and renders what came back. It
// loads no runs, compares no evaluations, evaluates no gate, loads no
// environments, compares no ranks and chooses no outcome.
//
// A gate FAIL is `201`, not `409` and not `500`. The platform was asked to
// decide and it decided; reporting an evidence-backed rejection as a server
// failure would tell an operator their platform is broken when it is working
// exactly as configured.
func (h *Handler) createPromotion(w http.ResponseWriter, r *http.Request) {
	var request createPromotionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		h.writeError(w, err)
		return
	}
	limits, err := request.GateLimits.decode()
	if err != nil {
		h.writeError(w, err)
		return
	}

	promotion, err := h.controlPlane.Promote(r.Context(), platform.PromotionRequest{
		ID:                platform.PromotionID(request.ID),
		ReferenceRunID:    platform.EvaluationRunID(request.ReferenceRunID),
		CandidateRunID:    platform.EvaluationRunID(request.CandidateRunID),
		TargetEnvironment: platform.EnvironmentRef(request.TargetEnvironment),
		GateLimits:        limits,
	}, h.now())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, newPromotionResponse(promotion))
}

// getPromotion reads one recorded decision.
func (h *Handler) getPromotion(w http.ResponseWriter, r *http.Request) {
	promotion, err := h.controlPlane.Promotion(
		r.Context(), platform.PromotionID(r.PathValue("promotion_id")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPromotionResponse(promotion))
}

// listPromotions returns one bounded page of a project's history.
//
// Keyset pagination on the caller-owned identifier, which is immutable.
// Deliberately not on `decided_at`: timestamps are stored as RFC3339Nano
// text, which is not lexically ordered, so a cursor over that column would
// silently skip and repeat rows.
func (h *Handler) listPromotions(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("project_id")

	limit, err := promotionLimitParam(r)
	if err != nil {
		h.writeError(w, err)
		return
	}
	after := r.URL.Query().Get("after")

	page, err := h.controlPlane.ProjectPromotions(r.Context(),
		platform.ProjectID(projectID), platform.PromotionID(after), limit)
	if err != nil {
		h.writeError(w, err)
		return
	}

	nextAfter, err := h.promotionContinuation(r.Context(), projectID, page, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, newPromotionListResponse(projectID, page, nextAfter))
}

// promotionContinuation reports the cursor to publish after this page, or ""
// when the traversal is complete.
//
// A short page is the end. A full page is ambiguous, and the ambiguity is
// resolved by asking for one row past the last identifier — never by fetching
// limit+1, which would require the store to accept a limit above its own
// published bound. The same rule the environment collection follows.
func (h *Handler) promotionContinuation(
	ctx context.Context, projectID string, page []platform.Promotion, limit int,
) (string, error) {
	if len(page) < limit {
		return "", nil
	}
	last := page[len(page)-1].ID()
	probe, err := h.controlPlane.ProjectPromotions(
		ctx, platform.ProjectID(projectID), last, 1)
	if err != nil {
		return "", err
	}
	if len(probe) == 0 {
		return "", nil
	}
	return string(last), nil
}

// promotionLimitParam reads and bounds ?limit=.
//
// Absent means the maximum. A value above it is refused rather than clamped:
// a client that asked for 500 and silently received 64 would conclude it had
// seen everything.
func promotionLimitParam(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return platform.MaxPromotionPage, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > platform.MaxPromotionPage {
		return 0, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: fmt.Sprintf("limit must be an integer between 1 and %d",
				platform.MaxPromotionPage)}
	}
	return limit, nil
}
