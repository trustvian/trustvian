package httpapi

// Wire contracts for the local control-plane API.
//
// These types own the JSON. No domain value carries a tag: task 052 made
// Project, Agent, Candidate and EvaluationRun unexported-field values on
// purpose, and a JSON tag would make the wire format a property of the domain
// type — renaming a field would silently break clients, and adding one would
// silently appear on the wire.
//
// Conversion is mechanical. Anything that looks like a decision belongs in
// the control plane, not here.

import (
	platform "trustvian-platform"
)

// WireVersion is the envelope version this build speaks.
//
// Distinct from the /v1 path, which versions the API shape. This versions the
// DecisionRecord wrapper, and task 050 deliberately put record versioning in
// the transport rather than inside the record.
const WireVersion = "1"

// ---------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------

type errorEnvelope struct {
	Version string    `json:"version"`
	Error   errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error codes. Small and stable: the code is contract, the message is
// observational and may be reworded without breaking a client.
const (
	codeInvalidRequest       = "invalid_request"
	codeNotFound             = "not_found"
	codeAlreadyExists        = "already_exists"
	codeConflict             = "conflict"
	codePayloadTooLarge      = "payload_too_large"
	codeUnsupportedMediaType = "unsupported_media_type"
	codeUnsupportedVersion   = "unsupported_version"
	codeIncompleteEvidence   = "incomplete_evidence"
	codeInternal             = "internal"
	codeRealtimeUnavailable  = "realtime_unavailable"
)

// ---------------------------------------------------------------------
// Control entities
// ---------------------------------------------------------------------

type createProjectRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type projectResponse struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Name    string `json:"name"`
}

func newProjectResponse(p platform.Project) projectResponse {
	return projectResponse{Version: WireVersion, ID: string(p.ID()), Name: p.Name()}
}

// ---------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------

// createEnvironmentRequest is flat, with project_id in the body, matching
// POST /v1/agents. The reads and mutations below are project-scoped instead,
// because an environment ref is unique within a project and nowhere else.
type createEnvironmentRequest struct {
	ProjectID string `json:"project_id"`
	Ref       string `json:"ref"`
	Name      string `json:"name"`

	// Rank is optional: omitted or null means unranked, which is a different
	// state from rank 0. A pointer is what makes those two distinguishable on
	// the wire.
	Rank *uint16 `json:"rank"`
}

// configureEnvironmentRequest changes a name, a rank, or both.
//
// Revision is required on every mutation. There is deliberately no
// unconditional write: two operators reordering ranks would otherwise
// overwrite each other, and the loser would never know.
type configureEnvironmentRequest struct {
	Revision  uint64  `json:"revision"`
	Name      *string `json:"name"`
	Rank      *uint16 `json:"rank"`
	ClearRank bool    `json:"clear_rank"`
}

// revisionRequest is the body archive and activate share.
type revisionRequest struct {
	Revision uint64 `json:"revision"`
}

type environmentResponse struct {
	Version   string `json:"version"`
	ProjectID string `json:"project_id"`
	Ref       string `json:"ref"`
	Name      string `json:"name"`

	// Rank is omitted entirely when the environment is unranked, rather than
	// sent as 0 — the wire keeps the distinction the domain makes.
	Rank *uint16 `json:"rank,omitempty"`

	Status   string `json:"status"`
	Revision uint64 `json:"revision"`
}

func newEnvironmentResponse(e platform.Environment) environmentResponse {
	response := environmentResponse{
		Version:   WireVersion,
		ProjectID: string(e.ProjectID()),
		Ref:       string(e.Ref()),
		Name:      e.Name(),
		Status:    string(e.Status()),
		Revision:  e.Revision(),
	}
	if rank, ranked := e.Rank(); ranked {
		response.Rank = &rank
	}
	return response
}

// environmentListResponse is an object rather than a bare array, so the
// envelope has somewhere to live and next_after is an additive field rather
// than a shape change.
type environmentListResponse struct {
	Version      string                `json:"version"`
	ProjectID    string                `json:"project_id"`
	Environments []environmentResponse `json:"environments"`

	// NextAfter is present exactly when another page follows, and absent on
	// the last one. A caller pages until it is gone.
	NextAfter string `json:"next_after,omitempty"`
}

func newEnvironmentListResponse(
	projectID string, environments []platform.Environment, nextAfter string,
) environmentListResponse {
	page := make([]environmentResponse, 0, len(environments))
	for _, env := range environments {
		page = append(page, newEnvironmentResponse(env))
	}
	return environmentListResponse{
		Version:      WireVersion,
		ProjectID:    projectID,
		Environments: page,
		NextAfter:    nextAfter,
	}
}

type createAgentRequest struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

type agentResponse struct {
	Version   string `json:"version"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
}

func newAgentResponse(a platform.Agent) agentResponse {
	return agentResponse{
		Version:   WireVersion,
		ID:        string(a.ID()),
		ProjectID: string(a.ProjectID()),
		Name:      a.Name(),
	}
}

type candidateMetadataDTO struct {
	Label          string `json:"label,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
	ArtifactDigest string `json:"artifact_digest,omitempty"`
	Model          string `json:"model,omitempty"`
	ToolsetDigest  string `json:"toolset_digest,omitempty"`
	ConfigDigest   string `json:"config_digest,omitempty"`
}

type createCandidateRequest struct {
	ID       string               `json:"id"`
	AgentID  string               `json:"agent_id"`
	Metadata candidateMetadataDTO `json:"metadata"`
}

type candidateResponse struct {
	Version  string               `json:"version"`
	ID       string               `json:"id"`
	AgentID  string               `json:"agent_id"`
	Metadata candidateMetadataDTO `json:"metadata"`
}

func newCandidateResponse(c platform.Candidate) candidateResponse {
	m := c.Metadata()
	return candidateResponse{
		Version: WireVersion,
		ID:      string(c.ID()),
		AgentID: string(c.AgentID()),
		Metadata: candidateMetadataDTO{
			Label:          m.Label,
			SourceRef:      m.SourceRef,
			ArtifactDigest: m.ArtifactDigest,
			Model:          m.Model,
			ToolsetDigest:  m.ToolsetDigest,
			ConfigDigest:   m.ConfigDigest,
		},
	}
}

// ---------------------------------------------------------------------
// Evaluation runs
// ---------------------------------------------------------------------

type createEvaluationRunRequest struct {
	ID                string `json:"id"`
	CandidateID       string `json:"candidate_id"`
	Environment       string `json:"environment"`
	BehavioralProfile string `json:"behavioral_profile"`
}

type failRunRequest struct {
	Reason string `json:"reason"`
}

type evaluationRunResponse struct {
	Version           string `json:"version"`
	ID                string `json:"id"`
	CandidateID       string `json:"candidate_id"`
	Environment       string `json:"environment"`
	BehavioralProfile string `json:"behavioral_profile"`
	Status            string `json:"status"`
	CreatedAt         string `json:"created_at"`
	StartedAt         string `json:"started_at,omitempty"`
	FinishedAt        string `json:"finished_at,omitempty"`
	FailureReason     string `json:"failure_reason,omitempty"`
}

func newEvaluationRunResponse(r platform.EvaluationRun) evaluationRunResponse {
	return evaluationRunResponse{
		Version:           WireVersion,
		ID:                string(r.ID()),
		CandidateID:       string(r.CandidateID()),
		Environment:       string(r.Environment()),
		BehavioralProfile: string(r.BehavioralProfile()),
		Status:            string(r.Status()),
		CreatedAt:         formatTime(r.CreatedAt()),
		StartedAt:         formatTime(r.StartedAt()),
		FinishedAt:        formatTime(r.FinishedAt()),
		FailureReason:     r.FailureReason(),
	}
}

// ---------------------------------------------------------------------
// Ingest
// ---------------------------------------------------------------------

// ingestEnvelope wraps one DecisionRecord for transport.
//
// The record stays raw so it can be decoded separately: the envelope is
// decoded strictly, while the record is not. DecisionRecord is an additive
// stable contract, and an older server must not reject a newer record merely
// because the core added a field.
//
// There is deliberately no metadata map. DecisionRecord structurally excludes
// event attributes, tool arguments, prompts and completions, and an escape
// hatch here would undo that boundary on its first use.
type ingestEnvelope struct {
	Version string `json:"version"`

	// Sequence is canonical decimal text, not a JSON number: uint64 exceeds
	// what a JSON double holds exactly, so a browser client would silently
	// round large values.
	Sequence string `json:"sequence"`

	// BehavioralProfile rides beside the record because DecisionRecord
	// carries no learning scope, and ADR 0024 kept it that way.
	BehavioralProfile string `json:"behavioral_profile"`

	Record jsonRaw `json:"record"`
}

type ingestResponse struct {
	Version          string `json:"version"`
	Disposition      string `json:"disposition"`
	NextSequence     string `json:"next_sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`
}

type ingestStateResponse struct {
	Version      string `json:"version"`
	RunID        string `json:"run_id"`
	NextSequence string `json:"next_sequence"`
}

// ---------------------------------------------------------------------
// Progress
// ---------------------------------------------------------------------

// progressResponse reports facts. No pass, verdict, or promotable field: a
// running evaluation has progress, and a field like that would be read as an
// answer it cannot give.
type progressResponse struct {
	Version                  string `json:"version"`
	RunID                    string `json:"run_id"`
	Status                   string `json:"status"`
	RecordCount              string `json:"record_count"`
	BehaviorObservationCount string `json:"behavior_observation_count"`
	DistinctBehaviorCount    int    `json:"distinct_behavior_count"`
	BehaviorComplete         bool   `json:"behavior_complete"`
	NextIngestSequence       string `json:"next_ingest_sequence"`
}

// ---------------------------------------------------------------------
// Comparison
// ---------------------------------------------------------------------

// gateLimitsDTO uses pointers so an omitted limit is distinguishable from
// zero.
//
// Zero is a legitimate strict limit — MaxBlockDecisions of 0 accepts no block
// decision at all — so collapsing omitted into zero would silently apply the
// strictest possible policy to a caller who forgot a field.
type gateLimitsDTO struct {
	MaxAddedBehaviors           *string `json:"max_added_behaviors"`
	MaxBlockDecisions           *string `json:"max_block_decisions"`
	MaxCriticalRiskObservations *string `json:"max_critical_risk_observations"`
}

type compareRequest struct {
	ReferenceRunID string        `json:"reference_run_id"`
	CandidateRunID string        `json:"candidate_run_id"`
	GateLimits     gateLimitsDTO `json:"gate_limits"`
}

type behaviorDescriptorDTO struct {
	ActorType         string `json:"actor_type"`
	OperationCategory string `json:"operation_category"`
	OperationName     string `json:"operation_name,omitempty"`
	TargetName        string `json:"target_name,omitempty"`
	TargetCategory    string `json:"target_category,omitempty"`
	Environment       string `json:"environment,omitempty"`
}

type behaviorDeltaDTO struct {
	Change                string                `json:"change"`
	FingerprintID         string                `json:"fingerprint_id"`
	Behavior              behaviorDescriptorDTO `json:"behavior"`
	ReferenceObservations string                `json:"reference_observations"`
	CandidateObservations string                `json:"candidate_observations"`
}

type behaviorDiffDTO struct {
	ReferenceObservationCount string             `json:"reference_observation_count"`
	CandidateObservationCount string             `json:"candidate_observation_count"`
	AddedCount                int                `json:"added_count"`
	RemovedCount              int                `json:"removed_count"`
	SharedCount               int                `json:"shared_count"`
	Deltas                    []behaviorDeltaDTO `json:"deltas"`
}

type rateDTO struct {
	Count string `json:"count"`
	Total string `json:"total"`
}

type rateComparisonDTO struct {
	Reference rateDTO `json:"reference"`
	Candidate rateDTO `json:"candidate"`
}

type decisionComparisonDTO struct {
	Allow           rateComparisonDTO `json:"allow"`
	ObserveOnly     rateComparisonDTO `json:"observe_only"`
	Alert           rateComparisonDTO `json:"alert"`
	Challenge       rateComparisonDTO `json:"challenge"`
	RequireApproval rateComparisonDTO `json:"require_approval"`
	Block           rateComparisonDTO `json:"block"`
}

type riskComparisonDTO struct {
	Low      rateComparisonDTO `json:"low"`
	Medium   rateComparisonDTO `json:"medium"`
	High     rateComparisonDTO `json:"high"`
	Critical rateComparisonDTO `json:"critical"`
}

type approvalComparisonDTO struct {
	Unspecified rateComparisonDTO `json:"unspecified"`
	NotRequired rateComparisonDTO `json:"not_required"`
	Required    rateComparisonDTO `json:"required"`
	Approved    rateComparisonDTO `json:"approved"`
	Denied      rateComparisonDTO `json:"denied"`
}

type policySelectionComparisonDTO struct {
	MatchedRule    rateComparisonDTO `json:"matched_rule"`
	MatchedDefault rateComparisonDTO `json:"matched_default"`
}

// metricSummaryDTO reports availability alongside the mean, because an empty
// mean is absent rather than zero — the distinction task 053 built the whole
// aggregate around.
type metricSummaryDTO struct {
	Count string  `json:"count"`
	Sum   float64 `json:"sum"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Mean  float64 `json:"mean"`
	Known bool    `json:"mean_known"`
}

type metricComparisonDTO struct {
	Reference      metricSummaryDTO `json:"reference"`
	Candidate      metricSummaryDTO `json:"candidate"`
	MeanDelta      float64          `json:"mean_delta"`
	MeanDeltaKnown bool             `json:"mean_delta_known"`
}

type metricComparisonsDTO struct {
	IdentityConfidence metricComparisonDTO `json:"identity_confidence"`
	AnomalyScore       metricComparisonDTO `json:"anomaly_score"`
	AnomalyConfidence  metricComparisonDTO `json:"anomaly_confidence"`
	TrustScore         metricComparisonDTO `json:"trust_score"`
	ContextRisk        metricComparisonDTO `json:"context_risk"`
}

type ratioDTO struct {
	Value float64 `json:"value"`
	Known bool    `json:"known"`
}

type behaviorSummaryDTO struct {
	ReferenceDistinctCount int      `json:"reference_distinct_count"`
	CandidateDistinctCount int      `json:"candidate_distinct_count"`
	AddedCount             int      `json:"added_count"`
	RemovedCount           int      `json:"removed_count"`
	SharedCount            int      `json:"shared_count"`
	AddedCandidateRate     ratioDTO `json:"added_candidate_rate"`
	RemovedReferenceRate   ratioDTO `json:"removed_reference_rate"`
	PresenceOverlap        ratioDTO `json:"presence_overlap"`
}

type scorecardDTO struct {
	ReferenceRunID       string `json:"reference_run_id"`
	ReferenceCandidateID string `json:"reference_candidate_id"`
	CandidateRunID       string `json:"candidate_run_id"`
	CandidateCandidateID string `json:"candidate_candidate_id"`
	Environment          string `json:"environment"`

	ReferenceRecordCount string `json:"reference_record_count"`
	CandidateRecordCount string `json:"candidate_record_count"`

	Decisions       decisionComparisonDTO        `json:"decisions"`
	Risks           riskComparisonDTO            `json:"risks"`
	Approvals       approvalComparisonDTO        `json:"approvals"`
	PolicySelection policySelectionComparisonDTO `json:"policy_selection"`
	Metrics         metricComparisonsDTO         `json:"metrics"`
	Behavior        behaviorSummaryDTO           `json:"behavior"`
}

type minimumGateDTO struct {
	Actual  string `json:"actual"`
	Minimum string `json:"minimum"`
	Passed  bool   `json:"passed"`
}

type maximumGateDTO struct {
	Actual  string `json:"actual"`
	Maximum string `json:"maximum"`
	Passed  bool   `json:"passed"`
}

// gateResultDTO carries all five checks, always. Task 056 evaluates every
// gate on every call so a FAIL shows everything measured, and truncating that
// here would throw away the property.
type gateResultDTO struct {
	ReferenceEvidence        minimumGateDTO `json:"reference_evidence"`
	CandidateEvidence        minimumGateDTO `json:"candidate_evidence"`
	AddedBehaviors           maximumGateDTO `json:"added_behaviors"`
	BlockDecisions           maximumGateDTO `json:"block_decisions"`
	CriticalRiskObservations maximumGateDTO `json:"critical_risk_observations"`
	Verdict                  string         `json:"verdict"`
}

type compareResponse struct {
	Version   string          `json:"version"`
	Reference string          `json:"reference_run_id"`
	Candidate string          `json:"candidate_run_id"`
	Diff      behaviorDiffDTO `json:"behavior_diff"`
	Scorecard scorecardDTO    `json:"scorecard"`
	Gate      gateResultDTO   `json:"gate"`
}

// ---------------------------------------------------------------------
// Realtime (SSE)
// ---------------------------------------------------------------------

// streamReadyPayload is the first frame on every connection.
//
// Both fields are deliberately constant. The bus retains no history, so a
// client is told plainly that it must resynchronize from authoritative state
// rather than expect a replay — and told before it starts consuming, while it
// can still act on it.
type streamReadyPayload struct {
	Version         string `json:"version"`
	ReplayAvailable bool   `json:"replay_available"`
	ResyncRequired  bool   `json:"resync_required"`
}

type realtimeScopeDTO struct {
	ProjectID         string `json:"project_id"`
	AgentID           string `json:"agent_id"`
	CandidateID       string `json:"candidate_id"`
	RunID             string `json:"run_id"`
	Environment       string `json:"environment,omitempty"`
	BehavioralProfile string `json:"behavioral_profile,omitempty"`
}

type realtimeEvaluationDTO struct {
	Status        string `json:"status"`
	CreatedAt     string `json:"created_at,omitempty"`
	StartedAt     string `json:"started_at,omitempty"`
	FinishedAt    string `json:"finished_at,omitempty"`
	FailureReason string `json:"failure_reason,omitempty"`
}

// realtimeObservationDTO carries the bounded projection and nothing more.
//
// No attributes, tool arguments, prompts, completions, contributors,
// PolicyReason, or raw record — task 050's privacy boundary holds on this path
// too, and a test asserts a distinctive attribute value never appears here.
type realtimeObservationDTO struct {
	Sequence         string `json:"sequence"`
	RecordCount      string `json:"record_count"`
	BehaviorComplete bool   `json:"behavior_complete"`

	FingerprintID string                `json:"fingerprint_id"`
	Behavior      behaviorDescriptorDTO `json:"behavior"`

	Decision       string `json:"decision"`
	RiskLevel      string `json:"risk_level"`
	ApprovalStatus string `json:"approval_status,omitempty"`

	TrustScore        float64 `json:"trust_score"`
	AnomalyScore      float64 `json:"anomaly_score"`
	AnomalyConfidence float64 `json:"anomaly_confidence"`

	NewBehavior bool `json:"new_behavior"`
}

// realtimeEventPayload is one SSE data frame.
//
// Lifecycle and observation halves are both present in the type and omitted on
// the wire when empty, matching the fixed-shape domain value rather than
// inventing a variant schema.
type realtimeEventPayload struct {
	Version string           `json:"version"`
	Kind    string           `json:"kind"`
	Scope   realtimeScopeDTO `json:"scope"`

	Evaluation  *realtimeEvaluationDTO  `json:"evaluation,omitempty"`
	Observation *realtimeObservationDTO `json:"observation,omitempty"`
}

// ---------------------------------------------------------------------
// Promotions (task 066)
// ---------------------------------------------------------------------

// createPromotionRequest is flat, with no scope in the path — matching
// POST /v1/agents and POST /v1/environments, because the scope is derived
// rather than routed.
//
// Everything the server derives is absent by construction. Strict decoding
// makes that a security property rather than a style choice: a caller cannot
// express `"outcome": "accepted"`, `"source_environment": …` or
// `"source_rank": 0`, so no adapter bug can forward one.
type createPromotionRequest struct {
	ID string `json:"id"`

	ReferenceRunID string `json:"reference_run_id"`
	CandidateRunID string `json:"candidate_run_id"`

	TargetEnvironment string `json:"target_environment"`

	GateLimits gateLimitsDTO `json:"gate_limits"`
}

// environmentPositionDTO is one environment as the decision saw it.
//
// `rank` is a number, like the environment response. `revision` is a string,
// like every other uint64 crossing /v1.
type environmentPositionDTO struct {
	Ref      string `json:"ref"`
	Rank     uint16 `json:"rank"`
	Revision string `json:"revision"`
}

// promotionResponse is the stored record, including the decision-time gate
// result.
//
// `gate_result` reuses gateResultDTO byte for byte — the same shape
// POST /v1/evaluations/compare publishes under its `gate` key — so a client
// that can render a comparison's gate can render a promotion's with no new
// code. It is served from the stored row, never re-derived per request.
type promotionResponse struct {
	Version string `json:"version"`

	ID        string `json:"id"`
	ProjectID string `json:"project_id"`

	CandidateID          string `json:"candidate_id"`
	ReferenceCandidateID string `json:"reference_candidate_id"`

	ReferenceRunID string `json:"reference_run_id"`
	CandidateRunID string `json:"candidate_run_id"`

	SourceEnvironment environmentPositionDTO `json:"source_environment"`
	TargetEnvironment environmentPositionDTO `json:"target_environment"`

	GateLimits gateLimitsResponseDTO `json:"gate_limits"`
	GateResult gateResultDTO         `json:"gate_result"`

	Outcome   string `json:"outcome"`
	DecidedAt string `json:"decided_at"`
}

// gateLimitsResponseDTO is the response form of the three limits.
//
// Plain strings rather than gateLimitsDTO's pointers: on a request a nil
// distinguishes "omitted" from "0", and on a response every limit is present
// by construction.
type gateLimitsResponseDTO struct {
	MaxAddedBehaviors           string `json:"max_added_behaviors"`
	MaxBlockDecisions           string `json:"max_block_decisions"`
	MaxCriticalRiskObservations string `json:"max_critical_risk_observations"`
}

// promotionListResponse is one bounded page of a project's history.
//
// next_after is omitted on the last page, so its presence means exactly "call
// again" rather than "there might be more".
type promotionListResponse struct {
	Version    string              `json:"version"`
	ProjectID  string              `json:"project_id"`
	Promotions []promotionResponse `json:"promotions"`
	NextAfter  string              `json:"next_after,omitempty"`
}

// ---------------------------------------------------------------------
// Hierarchy collections (task 074)
// ---------------------------------------------------------------------

// Four bounded pages, each carrying the *existing* detail DTO for its entity.
//
// A collection publishes no field a by-id read does not already publish, so a
// listing cannot become a privacy regression by accident and the two shapes
// cannot drift into disagreeing about what a Project is. The cost is one
// redundant `version` per element, which is what the environment and promotion
// collections already pay for the same guarantee.
//
// Each envelope is an object rather than a bare array so `next_after` is an
// additive field rather than a shape change, and it is omitted on the last
// page — its presence means "call again", never "there might be more".
//
// No total_count, page_number, offset or sort. Task 074 defines none of them:
// a count over an unbounded collection is a table scan, and an offset is the
// pagination model this API deliberately did not choose.

// projectListResponse is the root collection. It carries no scope field
// because it has no parent.
type projectListResponse struct {
	Version  string            `json:"version"`
	Projects []projectResponse `json:"projects"`

	NextAfter string `json:"next_after,omitempty"`
}

func newProjectListResponse(
	projects []platform.Project, nextAfter string,
) projectListResponse {
	page := make([]projectResponse, 0, len(projects))
	for _, project := range projects {
		page = append(page, newProjectResponse(project))
	}
	return projectListResponse{
		Version:   WireVersion,
		Projects:  page,
		NextAfter: nextAfter,
	}
}

type agentListResponse struct {
	Version   string          `json:"version"`
	ProjectID string          `json:"project_id"`
	Agents    []agentResponse `json:"agents"`

	NextAfter string `json:"next_after,omitempty"`
}

func newAgentListResponse(
	projectID string, agents []platform.Agent, nextAfter string,
) agentListResponse {
	page := make([]agentResponse, 0, len(agents))
	for _, agent := range agents {
		page = append(page, newAgentResponse(agent))
	}
	return agentListResponse{
		Version:   WireVersion,
		ProjectID: projectID,
		Agents:    page,
		NextAfter: nextAfter,
	}
}

type candidateListResponse struct {
	Version    string              `json:"version"`
	AgentID    string              `json:"agent_id"`
	Candidates []candidateResponse `json:"candidates"`

	NextAfter string `json:"next_after,omitempty"`
}

func newCandidateListResponse(
	agentID string, candidates []platform.Candidate, nextAfter string,
) candidateListResponse {
	page := make([]candidateResponse, 0, len(candidates))
	for _, candidate := range candidates {
		page = append(page, newCandidateResponse(candidate))
	}
	return candidateListResponse{
		Version:    WireVersion,
		AgentID:    agentID,
		Candidates: page,
		NextAfter:  nextAfter,
	}
}

type evaluationRunListResponse struct {
	Version     string                  `json:"version"`
	CandidateID string                  `json:"candidate_id"`
	Runs        []evaluationRunResponse `json:"evaluation_runs"`

	NextAfter string `json:"next_after,omitempty"`
}

func newEvaluationRunListResponse(
	candidateID string, runs []platform.EvaluationRun, nextAfter string,
) evaluationRunListResponse {
	page := make([]evaluationRunResponse, 0, len(runs))
	for _, run := range runs {
		page = append(page, newEvaluationRunResponse(run))
	}
	return evaluationRunListResponse{
		Version:     WireVersion,
		CandidateID: candidateID,
		Runs:        page,
		NextAfter:   nextAfter,
	}
}
