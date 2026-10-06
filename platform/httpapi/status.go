package httpapi

// Pipeline status over /v1 (task 105).
//
// Translation only, like every other route here: decode a Collector's report,
// hand it to the control plane with this server's clock, and render the
// document the control plane computes. Nothing here decides whether a
// Collector is reporting or stale, which producer is active, or what to
// suggest — the control plane owns all of it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	platform "trustvian-platform"
)

// maxStatusReportBody bounds one status report, before it is decoded. The
// processor checks the same figure on its encoded body, so a report it builds
// is never refused for its size.
const maxStatusReportBody = 64 << 10

// ---------------------------------------------------------------------
// Wire DTOs
// ---------------------------------------------------------------------

type statusReportRequest struct {
	Version           string                  `json:"version"`
	CollectorID       string                  `json:"collector_id"`
	Instance          string                  `json:"instance"`
	Sequence          string                  `json:"sequence"`
	UptimeMS          string                  `json:"uptime_ms"`
	ReceiverEndpoints []string                `json:"receiver_endpoints"`
	EvaluationRunID   string                  `json:"evaluation_run_id,omitempty"`
	Spans             statusSpanCountsDTO     `json:"spans"`
	Producers         []statusProducerRequest `json:"producers"`
	ProducersTrunc    bool                    `json:"producers_truncated"`

	// Optional sections: nil means the Collector did not report them, which
	// the document states rather than rendering as zero.
	Models                    *[]statusModelDTO           `json:"models"`
	ModelsTruncated           bool                        `json:"models_truncated"`
	Fidelity                  *statusFidelityRequest      `json:"fidelity"`
	TransportTargets          *[]statusTransportTargetDTO `json:"transport_targets"`
	TransportTargetsTruncated bool                        `json:"transport_targets_truncated"`
}

type statusModelDTO struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Calls    string `json:"calls"`
}

type statusFidelityRequest struct {
	Semantic  string `json:"semantic"`
	Transport string `json:"transport"`
}

type statusTransportTargetDTO struct {
	Target              string `json:"target"`
	Spans               string `json:"spans"`
	DistinctOperations  string `json:"distinct_operations"`
	OperationsSaturated bool   `json:"operations_saturated"`
}

// The document's optional sections. Reported is false when the Collector sent
// nothing for the section, and then no count is published at all.

type statusModelsSection struct {
	Reported  bool             `json:"reported"`
	Calls     []statusModelDTO `json:"calls"`
	Truncated bool             `json:"truncated"`
}

type statusFidelitySection struct {
	Reported  bool   `json:"reported"`
	Semantic  string `json:"semantic,omitempty"`
	Transport string `json:"transport,omitempty"`
}

type statusTransportSection struct {
	Reported  bool                       `json:"reported"`
	Targets   []statusTransportTargetDTO `json:"targets"`
	Truncated bool                       `json:"truncated"`
}

type statusSpanCountsDTO struct {
	Received      string `json:"received"`
	Evaluated     string `json:"evaluated"`
	Invalid       string `json:"invalid"`
	AnalyzeErrors string `json:"analyze_errors"`
}

type statusProducerRequest struct {
	ServiceName     string           `json:"service_name"`
	Spans           string           `json:"spans"`
	LastSeenAgeMS   string           `json:"last_seen_age_ms"`
	Scopes          []statusScopeDTO `json:"scopes"`
	ScopesTruncated bool             `json:"scopes_truncated"`
	SDK             statusSDKDTO     `json:"sdk"`
}

type statusScopeDTO struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type statusSDKDTO struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Version  string `json:"version"`
}

type statusReportResponse struct {
	Version     string `json:"version"`
	Disposition string `json:"disposition"`
}

// statusDocument is GET /v1/status.
type statusDocument struct {
	Version string `json:"version"`
	ReadAt  string `json:"read_at"`

	// HeldSince is when this control plane began holding status. Reports are
	// in memory only, so a restart forgets them, and a reader seeing an empty
	// document needs to know how long "nothing" has covered.
	HeldSince string `json:"held_since"`

	Collectors []statusCollectorDTO `json:"collectors"`
	Engine     statusEngineDTO      `json:"engine"`
	Bounds     statusBoundsDTO      `json:"bounds"`
}

type statusCollectorDTO struct {
	CollectorID        string                 `json:"collector_id"`
	Instance           string                 `json:"instance"`
	State              string                 `json:"state"`
	LastReportAt       string                 `json:"last_report_at"`
	StartedAt          string                 `json:"started_at"`
	ReceiverEndpoints  []string               `json:"receiver_endpoints"`
	EvaluationRunID    string                 `json:"evaluation_run_id"`
	Spans              statusSpanCountsDTO    `json:"spans"`
	Producers          []statusProducerOutDTO `json:"producers"`
	ProducersTruncated bool                   `json:"producers_truncated"`
	Models             statusModelsSection    `json:"models"`
	Fidelity           statusFidelitySection  `json:"fidelity"`
	TransportTargets   statusTransportSection `json:"transport_targets"`
}

type statusProducerOutDTO struct {
	ServiceName     string           `json:"service_name"`
	Spans           string           `json:"spans"`
	LastSeenAt      string           `json:"last_seen_at"`
	Scopes          []statusScopeDTO `json:"scopes"`
	ScopesTruncated bool             `json:"scopes_truncated"`
	SDK             statusSDKDTO     `json:"sdk"`
}

type statusEngineDTO struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

// statusBoundsDTO publishes every bound the document is subject to, so a
// renderer can name them without holding a copy that could drift.
type statusBoundsDTO struct {
	Collectors          string `json:"collectors"`
	ProducersPerReport  string `json:"producers_per_collector"`
	ScopesPerProducer   string `json:"scopes_per_producer"`
	ReceiverEndpoints   string `json:"receiver_endpoints_per_collector"`
	ModelsPerCollector  string `json:"models_per_collector"`
	TargetsPerCollector string `json:"transport_targets_per_collector"`
	OperationsPerTarget string `json:"operations_per_target"`
	FreshWindowSeconds  string `json:"fresh_window_seconds"`
	ExpiryWindowSeconds string `json:"expiry_window_seconds"`
}

// ---------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------

// reportCollectorStatus is POST /v1/collectors/{collector_id}/status.
func (h *Handler) reportCollectorStatus(w http.ResponseWriter, r *http.Request) {
	if err := requireJSONContentType(r); err != nil {
		h.writeError(w, err)
		return
	}
	var request statusReportRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStatusReportBody))
	if err := decoder.Decode(&request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			h.writeError(w, apiError{status: http.StatusRequestEntityTooLarge, code: codePayloadTooLarge,
				message: fmt.Sprintf("request body exceeds %d bytes", maxStatusReportBody)})
			return
		}
		h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "request body is not valid JSON for this endpoint"})
		return
	}
	// Unknown fields are tolerated here, unlike the mutation routes: a newer
	// Collector reporting a section this build does not know must still be
	// heard about everything it does know. Nothing unknown is retained.
	if request.Version != WireVersion {
		h.writeError(w, apiError{status: http.StatusBadRequest, code: codeUnsupportedVersion,
			message: fmt.Sprintf("status report version %q is not supported", request.Version)})
		return
	}
	if request.CollectorID != r.PathValue("collector_id") {
		h.writeError(w, apiError{status: http.StatusBadRequest, code: codeInvalidRequest,
			message: "collector_id does not match the route"})
		return
	}
	report, err := request.toDomain()
	if err != nil {
		h.writeError(w, err)
		return
	}
	disposition, err := h.controlPlane.ReportCollectorStatus(r.Context(), report, h.now())
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, statusReportResponse{Version: WireVersion, Disposition: string(disposition)})
}

// pipelineStatus is GET /v1/status.
func (h *Handler) pipelineStatus(w http.ResponseWriter, r *http.Request) {
	status := h.controlPlane.PipelineStatus(r.Context(), h.now())
	writeJSON(w, http.StatusOK, h.newStatusDocument(status))
}

// ---------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------

func (q statusReportRequest) toDomain() (platform.CollectorStatusReport, error) {
	var err error
	field := func(name, raw string) uint64 {
		if err != nil {
			return 0
		}
		var v uint64
		v, err = parseCounter(name, raw)
		return v
	}
	report := platform.CollectorStatusReport{
		CollectorID:       q.CollectorID,
		Instance:          q.Instance,
		Sequence:          field("sequence", q.Sequence),
		Uptime:            millis(field("uptime_ms", q.UptimeMS)),
		ReceiverEndpoints: q.ReceiverEndpoints,
		EvaluationRunID:   q.EvaluationRunID,
		Spans: platform.CollectorSpanCounts{
			Received:      field("spans.received", q.Spans.Received),
			Evaluated:     field("spans.evaluated", q.Spans.Evaluated),
			Invalid:       field("spans.invalid", q.Spans.Invalid),
			AnalyzeErrors: field("spans.analyze_errors", q.Spans.AnalyzeErrors),
		},
		ProducersTruncated: q.ProducersTrunc,
	}
	if len(q.Producers) > platform.MaxStatusProducers {
		return platform.CollectorStatusReport{}, fmt.Errorf(
			"%w: at most %d producers may be reported", platform.ErrInvalidStatusReport, platform.MaxStatusProducers)
	}
	for _, p := range q.Producers {
		producer := platform.ProducerStatus{
			ServiceName:     p.ServiceName,
			Spans:           field("producer spans", p.Spans),
			LastSeenAge:     millis(field("producer last_seen_age_ms", p.LastSeenAgeMS)),
			ScopesTruncated: p.ScopesTruncated,
			SDK:             platform.TelemetrySDK{Name: p.SDK.Name, Language: p.SDK.Language, Version: p.SDK.Version},
		}
		for _, s := range p.Scopes {
			producer.Scopes = append(producer.Scopes, platform.InstrumentationScope{Name: s.Name, Version: s.Version})
		}
		report.Producers = append(report.Producers, producer)
	}
	if q.Models != nil {
		if len(*q.Models) > platform.MaxStatusModels {
			return platform.CollectorStatusReport{}, fmt.Errorf(
				"%w: at most %d models may be reported", platform.ErrInvalidStatusReport, platform.MaxStatusModels)
		}
		report.ModelsReported = true
		report.ModelsTruncated = q.ModelsTruncated
		for _, m := range *q.Models {
			report.Models = append(report.Models, platform.ModelCalls{
				Provider: m.Provider, Model: m.Model, Calls: field("model calls", m.Calls)})
		}
	}
	if q.Fidelity != nil {
		report.FidelityReported = true
		report.Fidelity = platform.FidelityCounts{
			Semantic:  field("fidelity.semantic", q.Fidelity.Semantic),
			Transport: field("fidelity.transport", q.Fidelity.Transport),
		}
	}
	if q.TransportTargets != nil {
		if len(*q.TransportTargets) > platform.MaxStatusTransportTargets {
			return platform.CollectorStatusReport{}, fmt.Errorf("%w: at most %d transport targets may be reported",
				platform.ErrInvalidStatusReport, platform.MaxStatusTransportTargets)
		}
		report.TransportTargetsReported = true
		report.TransportTargetsTruncated = q.TransportTargetsTruncated
		for _, target := range *q.TransportTargets {
			report.TransportTargets = append(report.TransportTargets, platform.TransportTargetStatus{
				Target:              target.Target,
				Spans:               field("transport target spans", target.Spans),
				DistinctOperations:  field("transport target distinct_operations", target.DistinctOperations),
				OperationsSaturated: target.OperationsSaturated,
			})
		}
	}
	if err != nil {
		return platform.CollectorStatusReport{}, err
	}
	return report, nil
}

// maxStatusMillis keeps a millisecond count convertible to a Duration without
// overflow — about 292 years, far past any honest uptime or age.
const maxStatusMillis = uint64(1<<63-1) / uint64(time.Millisecond)

// parseCounter reads one canonical decimal counter.
func parseCounter(name, raw string) (uint64, error) {
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || strconv.FormatUint(v, 10) != raw {
		return 0, fmt.Errorf("%w: %s %q is not a canonical decimal count", platform.ErrInvalidStatusReport, name, raw)
	}
	return v, nil
}

// millis converts a validated millisecond count; one past maxStatusMillis is
// clamped to it rather than wrapping into a negative duration.
func millis(v uint64) time.Duration {
	return time.Duration(min(v, maxStatusMillis)) * time.Millisecond
}

func newStatusSpanCountsDTO(c platform.CollectorSpanCounts) statusSpanCountsDTO {
	return statusSpanCountsDTO{
		Received: u64(c.Received), Evaluated: u64(c.Evaluated),
		Invalid: u64(c.Invalid), AnalyzeErrors: u64(c.AnalyzeErrors),
	}
}

func (h *Handler) newStatusDocument(s platform.PipelineStatus) statusDocument {
	collectors := make([]statusCollectorDTO, 0, len(s.Collectors))
	for _, c := range s.Collectors {
		producers := make([]statusProducerOutDTO, 0, len(c.Report.Producers))
		for i, p := range c.Report.Producers {
			scopes := make([]statusScopeDTO, 0, len(p.Scopes))
			for _, scope := range p.Scopes {
				scopes = append(scopes, statusScopeDTO{Name: scope.Name, Version: scope.Version})
			}
			producers = append(producers, statusProducerOutDTO{
				ServiceName:     p.ServiceName,
				Spans:           u64(p.Spans),
				LastSeenAt:      formatTime(c.ProducerLastSeen[i]),
				Scopes:          scopes,
				ScopesTruncated: p.ScopesTruncated,
				SDK:             statusSDKDTO{Name: p.SDK.Name, Language: p.SDK.Language, Version: p.SDK.Version},
			})
		}
		endpoints := c.Report.ReceiverEndpoints
		if endpoints == nil {
			endpoints = []string{}
		}
		collectors = append(collectors, statusCollectorDTO{
			CollectorID:        c.Report.CollectorID,
			Instance:           c.Report.Instance,
			State:              string(c.State),
			LastReportAt:       formatTime(c.LastReportAt),
			StartedAt:          formatTime(c.StartedAt),
			ReceiverEndpoints:  endpoints,
			EvaluationRunID:    c.Report.EvaluationRunID,
			Spans:              newStatusSpanCountsDTO(c.Report.Spans),
			Producers:          producers,
			ProducersTruncated: c.Report.ProducersTruncated,
			Models:             newStatusModelsSection(c.Report),
			Fidelity:           newStatusFidelitySection(c.Report),
			TransportTargets:   newStatusTransportSection(c.Report),
		})
	}
	return statusDocument{
		Version:    WireVersion,
		ReadAt:     formatTime(s.ReadAt),
		HeldSince:  formatTime(h.statusSince),
		Collectors: collectors,
		Engine:     statusEngineDTO{State: string(s.Engine.State), Reason: s.Engine.Reason},
		Bounds: statusBoundsDTO{
			Collectors:          strconv.Itoa(platform.MaxStatusCollectors),
			ProducersPerReport:  strconv.Itoa(platform.MaxStatusProducers),
			ScopesPerProducer:   strconv.Itoa(platform.MaxStatusScopes),
			ReceiverEndpoints:   strconv.Itoa(platform.MaxStatusReceiverEndpoints),
			ModelsPerCollector:  strconv.Itoa(platform.MaxStatusModels),
			TargetsPerCollector: strconv.Itoa(platform.MaxStatusTransportTargets),
			OperationsPerTarget: strconv.Itoa(platform.MaxStatusOperationsPerTarget),
			FreshWindowSeconds:  strconv.Itoa(int(platform.StatusFreshWindow / time.Second)),
			ExpiryWindowSeconds: strconv.Itoa(int(platform.StatusExpiry / time.Second)),
		},
	}
}

func newStatusModelsSection(r platform.CollectorStatusReport) statusModelsSection {
	section := statusModelsSection{Reported: r.ModelsReported, Calls: []statusModelDTO{}, Truncated: r.ModelsTruncated}
	for _, m := range r.Models {
		section.Calls = append(section.Calls, statusModelDTO{Provider: m.Provider, Model: m.Model, Calls: u64(m.Calls)})
	}
	return section
}

func newStatusFidelitySection(r platform.CollectorStatusReport) statusFidelitySection {
	if !r.FidelityReported {
		return statusFidelitySection{}
	}
	return statusFidelitySection{
		Reported: true, Semantic: u64(r.Fidelity.Semantic), Transport: u64(r.Fidelity.Transport),
	}
}

func newStatusTransportSection(r platform.CollectorStatusReport) statusTransportSection {
	section := statusTransportSection{
		Reported: r.TransportTargetsReported, Targets: []statusTransportTargetDTO{},
		Truncated: r.TransportTargetsTruncated,
	}
	for _, t := range r.TransportTargets {
		section.Targets = append(section.Targets, statusTransportTargetDTO{
			Target: t.Target, Spans: u64(t.Spans), DistinctOperations: u64(t.DistinctOperations),
			OperationsSaturated: t.OperationsSaturated,
		})
	}
	return section
}
