package platform

// Pipeline status: what the Collectors feeding this control plane report about
// the telemetry reaching them (task 105).
//
//	reported state is ephemeral
//	the control plane holds it in memory and never persists it
//
// It exists to explain an empty Live view. Everything the control plane knew
// before this arrived inside an ingest envelope, so a span a Collector dropped,
// or could not attribute to an actor, or never received, left no trace here.
// A Collector now reports those facts on a fixed interval, and this file keeps
// the latest report per Collector — bounded, aged out, and lost on restart, in
// the same sense realtime is (ADR 0032): notification-grade state that a reader
// re-establishes by asking again, never evidence a gate or a promotion reads.
//
// Content-free by construction. A report carries counts, ages, and the named
// resource and identity metadata a producer declares — service.name,
// instrumentation scope, telemetry.sdk.* — and nothing a span carries about its
// subject.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// Status bounds. Fixed, for the reason the realtime bus gives: an option that
// accepted a large value would let a caller opt out of the bound while the code
// still claimed one. Mirrored by the processor's own tracker, so a report it
// builds is never refused for its size.
const (
	// MaxStatusCollectors bounds the Collectors held at once.
	MaxStatusCollectors = 16

	// MaxStatusProducers bounds the producers one report may carry.
	MaxStatusProducers = 64

	// MaxStatusScopes bounds the instrumentation scopes per producer.
	MaxStatusScopes = 16

	// MaxStatusReceiverEndpoints bounds the receiver endpoints one report may
	// name.
	MaxStatusReceiverEndpoints = 4

	// MaxStatusModels bounds the (provider, model) pairs one report may carry.
	MaxStatusModels = 64

	// MaxStatusTransportTargets bounds the transport-fidelity targets one
	// report may carry.
	MaxStatusTransportTargets = 64

	// MaxStatusOperationsPerTarget is the most distinct operations a Collector
	// counts per transport target before it reports the count as saturated.
	MaxStatusOperationsPerTarget = 32

	// StatusFreshWindow is how recently a Collector must have reported, and a
	// producer have sent spans, to count as active. Three default report
	// intervals: one lost report does not make a healthy pipeline look idle.
	StatusFreshWindow = 30 * time.Second

	// StatusExpiry is how long a Collector that stopped reporting stays
	// visible, as stale, before it is forgotten.
	StatusExpiry = 5 * time.Minute
)

var (
	// ErrInvalidStatusReport reports a status report that does not follow the
	// contract. Permanently wrong as sent, so it is the caller's to fix.
	ErrInvalidStatusReport = errors.New("platform: invalid status report")

	// ErrStatusCollectorLimit reports that MaxStatusCollectors Collectors are
	// already reporting. An existing Collector is never evicted to admit a new
	// one: a reporter must not be able to hide another by connecting.
	ErrStatusCollectorLimit = errors.New("platform: status collector limit reached")
)

// ---------------------------------------------------------------------
// The report a Collector sends
// ---------------------------------------------------------------------

// CollectorStatusReport is one Collector's report, as received.
//
// Counters are cumulative since that Collector process started; ages are
// relative to when it built the report. Ages rather than timestamps so the
// control plane converts them against its own clock, and a Collector with a
// skewed clock cannot make a producer look fresh or stale.
type CollectorStatusReport struct {
	CollectorID string
	Instance    string
	Sequence    uint64
	Uptime      time.Duration

	ReceiverEndpoints []string
	EvaluationRunID   string

	Spans CollectorSpanCounts

	Producers          []ProducerStatus
	ProducersTruncated bool

	// The sections below are optional on the wire: a Collector built before
	// they existed sends none of them, and "not reported" must never read as
	// zero. Each carries its own Reported flag for that reason.

	// Models is every model call the Collector evaluated, by provider and
	// model, as display metadata.
	ModelsReported  bool
	Models          []ModelCalls
	ModelsTruncated bool

	// Fidelity is evaluated spans by how their behavior was named.
	FidelityReported bool
	Fidelity         FidelityCounts

	// TransportTargets is, per target, the distinct operations seen at
	// transport fidelity.
	TransportTargetsReported  bool
	TransportTargets          []TransportTargetStatus
	TransportTargetsTruncated bool

	// Actors is which link of the actor chain bound each span.
	ActorsReported bool
	Actors         ActorBinding

	// Learning is what the engine's Observe reported, as the Collector saw it.
	LearningReported bool
	Learning         LearningOutcomes
}

// ActorBinding counts spans by how their actor was bound: the explicit
// trustvian.actor.id override, the resource's service.name, or neither. An
// unbound span was never evaluated.
type ActorBinding struct {
	BoundByOverride    uint64
	BoundByServiceName uint64
	Unbound            uint64
}

// LearningOutcomes counts what Observe reported.
//
// NotLearned is not split into causes, because Observe does not say which
// applied — an ineligible decision, a declined write, or a baseline at its
// fingerprint admission bound. NotLearnedByDecision groups the same count by
// the decision each observation followed; it sums to NotLearned.
type LearningOutcomes struct {
	Learned              uint64
	NotLearned           uint64
	ObserveErrors        uint64
	NotLearnedByDecision []DecisionTally
}

// DecisionTally is one engine decision and a count.
type DecisionTally struct {
	Decision string
	Count    uint64
}

// ModelCalls is one (provider, model) pair and how many calls named it.
// Provider is empty when the telemetry named a model and no provider.
type ModelCalls struct {
	Provider string
	Model    string
	Calls    uint64
}

// FidelityCounts is evaluated spans by fidelity.
type FidelityCounts struct {
	Semantic  uint64
	Transport uint64
}

// TransportTargetStatus is one target seen at transport fidelity: how many
// spans reached it and how many distinct operations those spans named.
// Target is empty when the spans named none. Operation names are counted by
// the Collector and never reported.
type TransportTargetStatus struct {
	Target              string
	Spans               uint64
	DistinctOperations  uint64
	OperationsSaturated bool
}

// CollectorSpanCounts is what became of every span a Collector was handed.
type CollectorSpanCounts struct {
	Received      uint64
	Evaluated     uint64
	Invalid       uint64
	AnalyzeErrors uint64
}

// ProducerStatus is one service.name a Collector has seen. An empty
// ServiceName is a producer that set none, which is a fact worth reporting
// rather than a missing row.
type ProducerStatus struct {
	ServiceName     string
	Spans           uint64
	LastSeenAge     time.Duration
	Scopes          []InstrumentationScope
	ScopesTruncated bool
	SDK             TelemetrySDK
}

// InstrumentationScope names one instrumentation library.
type InstrumentationScope struct {
	Name    string
	Version string
}

// TelemetrySDK is a producer's telemetry.sdk.* resource attributes.
type TelemetrySDK struct {
	Name     string
	Language string
	Version  string
}

// validate applies the contract's bounds before anything is retained.
func (r CollectorStatusReport) validate() error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidStatusReport}, args...)...)
	}
	if err := validateID("collector_id", r.CollectorID); err != nil {
		return invalid("%v", err)
	}
	if len(r.Instance) != 16 || strings.Trim(r.Instance, "0123456789abcdef") != "" {
		return invalid("instance must be 16 lowercase hexadecimal characters")
	}
	if r.Sequence == 0 {
		return invalid("sequence starts at 1")
	}
	if r.Uptime < 0 {
		return invalid("uptime is negative")
	}
	if len(r.ReceiverEndpoints) > MaxStatusReceiverEndpoints {
		return invalid("at most %d receiver endpoints may be reported", MaxStatusReceiverEndpoints)
	}
	for _, endpoint := range r.ReceiverEndpoints {
		if err := validateID("receiver endpoint", endpoint); err != nil {
			return invalid("%v", err)
		}
	}
	if r.EvaluationRunID != "" {
		if err := validateID("evaluation_run_id", r.EvaluationRunID); err != nil {
			return invalid("%v", err)
		}
	}
	if r.Spans.Evaluated > r.Spans.Received || r.Spans.Invalid > r.Spans.Received ||
		r.Spans.AnalyzeErrors > r.Spans.Received {
		return invalid("a span count exceeds the spans received")
	}
	if len(r.Producers) > MaxStatusProducers {
		return invalid("at most %d producers may be reported", MaxStatusProducers)
	}
	seen := make(map[string]struct{}, len(r.Producers))
	for _, p := range r.Producers {
		if _, dup := seen[p.ServiceName]; dup {
			return invalid("producer %q is reported twice", p.ServiceName)
		}
		seen[p.ServiceName] = struct{}{}
		if p.ServiceName != "" {
			if err := validateText(ErrInvalidStatusReport, "service_name", p.ServiceName); err != nil {
				return err
			}
		}
		if p.LastSeenAge < 0 {
			return invalid("a producer's last-seen age is negative")
		}
		if len(p.Scopes) > MaxStatusScopes {
			return invalid("at most %d scopes may be reported per producer", MaxStatusScopes)
		}
		for _, s := range p.Scopes {
			if err := validateOptionalText("scope name", s.Name); err != nil {
				return err
			}
			if err := validateOptionalText("scope version", s.Version); err != nil {
				return err
			}
		}
		for _, field := range []struct{ name, value string }{
			{"sdk name", p.SDK.Name}, {"sdk language", p.SDK.Language}, {"sdk version", p.SDK.Version},
		} {
			if err := validateOptionalText(field.name, field.value); err != nil {
				return err
			}
		}
	}
	if len(r.Models) > MaxStatusModels {
		return invalid("at most %d models may be reported", MaxStatusModels)
	}
	models := make(map[[2]string]struct{}, len(r.Models))
	for _, m := range r.Models {
		key := [2]string{m.Provider, m.Model}
		if _, dup := models[key]; dup {
			return invalid("model %q from %q is reported twice", m.Model, m.Provider)
		}
		models[key] = struct{}{}
		if err := validateText(ErrInvalidStatusReport, "model", m.Model); err != nil {
			return err
		}
		if m.Model == "" {
			return invalid("a model call names no model")
		}
		if err := validateOptionalText("provider", m.Provider); err != nil {
			return err
		}
	}
	if len(r.TransportTargets) > MaxStatusTransportTargets {
		return invalid("at most %d transport targets may be reported", MaxStatusTransportTargets)
	}
	targets := make(map[string]struct{}, len(r.TransportTargets))
	for _, target := range r.TransportTargets {
		if _, dup := targets[target.Target]; dup {
			return invalid("transport target %q is reported twice", target.Target)
		}
		targets[target.Target] = struct{}{}
		if err := validateOptionalText("transport target", target.Target); err != nil {
			return err
		}
		if target.DistinctOperations > MaxStatusOperationsPerTarget {
			return invalid("a transport target reports more than %d distinct operations", MaxStatusOperationsPerTarget)
		}
		if target.DistinctOperations > target.Spans {
			return invalid("transport target %q reports more operations than spans", target.Target)
		}
	}
	if len(r.Learning.NotLearnedByDecision) > len(knownDecisions) {
		return invalid("at most %d decisions may be reported", len(knownDecisions))
	}
	var notLearned uint64
	decisions := make(map[string]struct{}, len(r.Learning.NotLearnedByDecision))
	for _, tally := range r.Learning.NotLearnedByDecision {
		if _, known := knownDecisions[tally.Decision]; !known {
			return invalid("%q is not an engine decision", tally.Decision)
		}
		if _, dup := decisions[tally.Decision]; dup {
			return invalid("decision %q is reported twice", tally.Decision)
		}
		decisions[tally.Decision] = struct{}{}
		if notLearned+tally.Count < notLearned {
			return invalid("the not-learned counts overflow")
		}
		notLearned += tally.Count
	}
	if notLearned != r.Learning.NotLearned {
		return invalid("the not-learned counts by decision do not sum to the not-learned total")
	}
	return nil
}

// knownDecisions is the engine's decision vocabulary, restated for the reason
// aggregate.go gives.
var knownDecisions = map[string]struct{}{
	decisionAllow: {}, decisionObserveOnly: {}, decisionAlert: {},
	decisionChallenge: {}, decisionRequireApproval: {}, decisionBlock: {},
}

func validateOptionalText(field, value string) error {
	if value == "" {
		return nil
	}
	return validateText(ErrInvalidStatusReport, field, value)
}

// clone copies every slice, so a held report shares nothing with its caller.
func (r CollectorStatusReport) clone() CollectorStatusReport {
	out := r
	out.ReceiverEndpoints = slices.Clone(r.ReceiverEndpoints)
	out.Models = slices.Clone(r.Models)
	out.TransportTargets = slices.Clone(r.TransportTargets)
	out.Learning.NotLearnedByDecision = slices.Clone(r.Learning.NotLearnedByDecision)
	out.Producers = make([]ProducerStatus, len(r.Producers))
	for i, p := range r.Producers {
		p.Scopes = slices.Clone(p.Scopes)
		out.Producers[i] = p
	}
	return out
}

// ---------------------------------------------------------------------
// The registry
// ---------------------------------------------------------------------

// StatusReportDisposition is what became of one report.
type StatusReportDisposition string

const (
	// StatusReportAccepted means the report replaced the Collector's previous
	// one.
	StatusReportAccepted StatusReportDisposition = "accepted"

	// StatusReportIgnored means a newer report from the same process was
	// already held: a delayed report arriving late must not roll the counters
	// back.
	StatusReportIgnored StatusReportDisposition = "ignored"
)

// statusRegistry holds the latest report per Collector.
type statusRegistry struct {
	mu         sync.Mutex
	collectors map[string]heldStatus
}

type heldStatus struct {
	report     CollectorStatusReport
	receivedAt time.Time
}

func newStatusRegistry() *statusRegistry {
	return &statusRegistry{collectors: make(map[string]heldStatus)}
}

// record holds report as its Collector's latest, received at receivedAt.
//
// changed reports whether the held facts moved in a way a reader would see:
// a new Collector, a new process, or any count, section or name other than the
// sequence, the uptime and the producers' ages, which advance on every report
// and would otherwise make every report a change.
func (s *statusRegistry) record(
	report CollectorStatusReport, receivedAt time.Time,
) (disposition StatusReportDisposition, changed bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Expired entries leave first, so a Collector that went away hands its
	// slot back without anyone else having to be evicted.
	for id, held := range s.collectors {
		if receivedAt.Sub(held.receivedAt) > StatusExpiry {
			delete(s.collectors, id)
		}
	}

	held, known := s.collectors[report.CollectorID]
	if known && held.report.Instance == report.Instance && report.Sequence <= held.report.Sequence {
		return StatusReportIgnored, false, nil
	}
	if !known && len(s.collectors) >= MaxStatusCollectors {
		return "", false, fmt.Errorf("%w: %d collectors", ErrStatusCollectorLimit, MaxStatusCollectors)
	}
	changed = !known || !sameFacts(held.report, report)
	s.collectors[report.CollectorID] = heldStatus{report: report.clone(), receivedAt: receivedAt}
	return StatusReportAccepted, changed, nil
}

// sameFacts compares two reports ignoring the fields that advance on every
// report regardless of what happened: sequence, uptime and producer ages.
//
// Written out field by field rather than with reflection: the rules of this
// repository exclude reflection, and a comparison that names every field is one
// a new field cannot silently slip past — adding one to the report without
// adding it here leaves a test that compares two reports differing only in it
// failing.
func sameFacts(a, b CollectorStatusReport) bool {
	if a.CollectorID != b.CollectorID || a.Instance != b.Instance ||
		a.EvaluationRunID != b.EvaluationRunID || a.Spans != b.Spans ||
		a.ProducersTruncated != b.ProducersTruncated ||
		a.ModelsReported != b.ModelsReported || a.ModelsTruncated != b.ModelsTruncated ||
		a.FidelityReported != b.FidelityReported || a.Fidelity != b.Fidelity ||
		a.TransportTargetsReported != b.TransportTargetsReported ||
		a.TransportTargetsTruncated != b.TransportTargetsTruncated ||
		a.ActorsReported != b.ActorsReported || a.Actors != b.Actors ||
		a.LearningReported != b.LearningReported ||
		a.Learning.Learned != b.Learning.Learned || a.Learning.NotLearned != b.Learning.NotLearned ||
		a.Learning.ObserveErrors != b.Learning.ObserveErrors {
		return false
	}
	if !slices.Equal(a.ReceiverEndpoints, b.ReceiverEndpoints) ||
		!slices.Equal(a.Models, b.Models) ||
		!slices.Equal(a.TransportTargets, b.TransportTargets) ||
		!slices.Equal(a.Learning.NotLearnedByDecision, b.Learning.NotLearnedByDecision) {
		return false
	}
	return slices.EqualFunc(a.Producers, b.Producers, func(p, q ProducerStatus) bool {
		return p.ServiceName == q.ServiceName && p.Spans == q.Spans &&
			p.ScopesTruncated == q.ScopesTruncated && p.SDK == q.SDK &&
			slices.Equal(p.Scopes, q.Scopes)
	})
}

// snapshot returns every unexpired entry, ordered by Collector identifier.
func (s *statusRegistry) snapshot(now time.Time) []heldStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]heldStatus, 0, len(s.collectors))
	for _, id := range slices.Sorted(maps.Keys(s.collectors)) {
		held := s.collectors[id]
		if now.Sub(held.receivedAt) > StatusExpiry {
			continue
		}
		out = append(out, heldStatus{report: held.report.clone(), receivedAt: held.receivedAt})
	}
	return out
}

// ---------------------------------------------------------------------
// The status the control plane publishes
// ---------------------------------------------------------------------

// CollectorState is whether a Collector is still reporting.
type CollectorState string

const (
	// CollectorReporting means a report arrived within StatusFreshWindow.
	CollectorReporting CollectorState = "reporting"

	// CollectorStale means the last report is older than that, but the
	// Collector has not yet expired.
	CollectorStale CollectorState = "stale"
)

// PipelineStatus is the whole status document, as of ReadAt.
type PipelineStatus struct {
	ReadAt     time.Time
	Collectors []CollectorStatus
	Engine     EngineStatus

	// LastIngestAt is when this control plane last committed an ingest
	// record, on its own clock, or the zero time when it has committed none
	// since it started. Held in memory only, like the reports.
	LastIngestAt time.Time

	// Landing is which view an interface opens on: Live when something is
	// active — a Collector reporting with a producer seen within the fresh
	// window, or an ingest record committed within it — and Status otherwise.
	// Decided here so no interface holds its own copy of the rule.
	Landing Landing

	// Suggestions are the rule table's outputs over the fields above, in table
	// order (status_rules.go, ADR 0064). Nothing reads them.
	Suggestions          []Suggestion
	SuggestionsTruncated bool
}

// CollectorStatus is one Collector's latest report, placed on this control
// plane's clock.
type CollectorStatus struct {
	Report       CollectorStatusReport
	State        CollectorState
	LastReportAt time.Time
	StartedAt    time.Time

	// ProducerLastSeen holds each producer's last span time, index-aligned
	// with Report.Producers.
	ProducerLastSeen []time.Time
}

// Landing is the view an interface opens on.
type Landing string

const (
	// LandingLive means something is active, so the live view has something
	// to show.
	LandingLive Landing = "live"

	// LandingStatus means nothing is active, so the status view — which says
	// why — is the more useful first screen.
	LandingStatus Landing = "status"
)

// landingFor applies the rule: active means a reporting Collector that has
// seen a producer within the fresh window, or an ingest record committed
// within it. The second covers a Collector with no `status:` block: records
// arriving are activity whether or not anything reports pipeline status.
func landingFor(now, lastIngest time.Time, collectors []CollectorStatus) Landing {
	if ingestFresh(now, lastIngest) {
		return LandingLive
	}
	for _, c := range collectors {
		if c.State != CollectorReporting {
			continue
		}
		for _, seen := range c.ProducerLastSeen {
			if now.Sub(seen) <= StatusFreshWindow {
				return LandingLive
			}
		}
	}
	return LandingStatus
}

// ingestFresh reports whether an ingest record was committed within the fresh
// window before now. The zero time is "never", which is never fresh.
func ingestFresh(now, lastIngest time.Time) bool {
	return !lastIngest.IsZero() && now.Sub(lastIngest) <= StatusFreshWindow
}

// noteIngest records that an ingest record was committed at `at`. It only
// moves forward, so concurrent commits finishing out of order never move
// last_ingest_at back. The zero time records nothing.
func (c *ControlPlane) noteIngest(at time.Time) {
	if at.IsZero() {
		return
	}
	next := at.UnixNano()
	for {
		current := c.lastIngest.Load()
		if current >= next || c.lastIngest.CompareAndSwap(current, next) {
			return
		}
	}
}

// lastIngestAt returns the last committed ingest time, or the zero time.
func (c *ControlPlane) lastIngestAt() time.Time {
	if nanos := c.lastIngest.Load(); nanos != 0 {
		return time.Unix(0, nanos).UTC()
	}
	return time.Time{}
}

// EngineState says whether engine facts are available.
type EngineState string

// EngineUnavailable is the only state today: the engine exposes no accessor
// for its baseline count, maturity or fingerprint admission, and task 105
// decided to report that rather than change the engine or approximate it.
const EngineUnavailable EngineState = "unavailable"

// EngineStatus says what is known about the engine's learned state.
type EngineStatus struct {
	State  EngineState
	Reason string
}

// engineUnavailableReason is stated where it is shown, so a reader is never
// left to infer why a section is empty.
const engineUnavailableReason = "the engine exposes no statistics accessor; baseline count, maturity and " +
	"fingerprint admission against the 512 bound are not available (task 105). Each Collector's learning " +
	"section reports what Observe returned"

// ReportCollectorStatus holds one Collector's report as its latest.
//
// receivedAt is this control plane's clock, never the Collector's: every age
// in the report is converted against it.
func (c *ControlPlane) ReportCollectorStatus(
	_ context.Context, report CollectorStatusReport, receivedAt time.Time,
) (StatusReportDisposition, error) {
	if err := report.validate(); err != nil {
		return "", err
	}
	disposition, changed, err := c.status.record(report, receivedAt)
	if err != nil {
		return "", err
	}
	// Notification, after the state is held, and only when a reader would see
	// something different: a Collector reporting every ten seconds about an
	// idle pipeline must not wake every open view every ten seconds.
	if changed && c.realtime != nil {
		c.realtime.Publish(RealtimeEvent{
			Kind:   RealtimeStatusChanged,
			Status: RealtimeStatusChange{CollectorID: report.CollectorID},
		})
	}
	return disposition, nil
}

// PipelineStatus reads the status document as of now.
//
// Pure over the held reports, the last ingest time and now: the same inputs
// produce the same document. It reads no store, so it costs the same whatever
// the database holds.
func (c *ControlPlane) PipelineStatus(_ context.Context, now time.Time) PipelineStatus {
	held := c.status.snapshot(now)
	collectors := make([]CollectorStatus, 0, len(held))
	for _, h := range held {
		state := CollectorStale
		if now.Sub(h.receivedAt) <= StatusFreshWindow {
			state = CollectorReporting
		}
		lastSeen := make([]time.Time, len(h.report.Producers))
		for i, p := range h.report.Producers {
			lastSeen[i] = h.receivedAt.Add(-p.LastSeenAge)
		}
		collectors = append(collectors, CollectorStatus{
			Report:           h.report,
			State:            state,
			LastReportAt:     h.receivedAt,
			StartedAt:        h.receivedAt.Add(-h.report.Uptime),
			ProducerLastSeen: lastSeen,
		})
	}
	lastIngest := c.lastIngestAt()
	status := PipelineStatus{
		ReadAt:       now,
		Collectors:   collectors,
		Engine:       EngineStatus{State: EngineUnavailable, Reason: engineUnavailableReason},
		LastIngestAt: lastIngest,
		Landing:      landingFor(now, lastIngest, collectors),
	}
	status.Suggestions, status.SuggestionsTruncated = evaluateStatusRules(status)
	return status
}
