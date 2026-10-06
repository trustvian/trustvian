// Package status tracks what this Collector has seen of the pipeline feeding
// it, and reports it to a control plane (task 105).
//
// It answers a question the evaluation sink cannot: why is nothing arriving?
// The sink only ever speaks about records it produced, so a span that never
// became a record — because its producer set no service.name, because it
// failed to map, because nothing was sent at all — leaves no trace on the
// control plane. This package counts those facts here, where they are
// visible, and pushes them on a fixed interval.
//
// # What it holds, and what it never holds
//
// Counts, last-seen ages, and a bounded set of resource and identity metadata:
// a producer's service.name, its instrumentation scope names and versions, and
// its telemetry.sdk.* values. Nothing else. No span name, no attribute value
// beyond those named keys, no prompt, completion or argument. Every collection
// is bounded and says when it was truncated; nothing is dropped silently.
//
// # It never re-enters the engine
//
// Like internal/metrics, this describes the pipeline rather than its subjects,
// and it has no path back into an Engine or into a record.
package status

import (
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// WireVersion is the report format this build speaks.
const WireVersion = "1"

// Bounds. Fixed rather than configurable, for the reason the realtime bus
// gives: an option accepting a large number would let a caller opt out of the
// bound while this code still claimed one. They mirror the control plane's own
// validation limits, so a report this package builds is never refused for its
// size.
const (
	// MaxProducers bounds the distinct service names tracked.
	MaxProducers = 64

	// MaxScopes bounds the instrumentation scopes tracked per producer.
	MaxScopes = 16

	// MaxReceiverEndpoints bounds the configured receiver endpoints reported.
	MaxReceiverEndpoints = 4

	// MaxModels bounds the distinct (provider, model) pairs tracked.
	MaxModels = 64

	// MaxTransportTargets bounds the transport-fidelity targets tracked.
	MaxTransportTargets = 64

	// MaxOperationsPerTarget bounds the distinct operation names counted per
	// transport target. Past it the count stops and says so: "at least 32"
	// answers the question the count exists for as well as an exact figure.
	MaxOperationsPerTarget = 32

	// maxText is the longest string reported, in bytes. The control plane
	// refuses longer values, so they are shortened here rather than turning
	// one long service name into a rejected report.
	maxText = 256
)

// ---------------------------------------------------------------------
// Wire DTOs. These own the JSON; nothing here is a platform type.
// ---------------------------------------------------------------------

// Report is one status report, as it travels to the control plane.
//
// Every counter is cumulative since this process started, and every age is
// relative to the moment the report was built. Ages rather than timestamps
// because the control plane converts them against its own clock: a Collector
// whose clock is skewed must not make a producer look stale or fresh.
type Report struct {
	Version     string `json:"version"`
	CollectorID string `json:"collector_id"`
	Instance    string `json:"instance"`
	Sequence    string `json:"sequence"`
	UptimeMS    string `json:"uptime_ms"`

	// ReceiverEndpoints is what the Collector's configuration says its OTLP
	// receivers listen on. Informational: a processor cannot read another
	// component's configuration, so this is supplied by whoever wrote the
	// Collector configuration — `trustvian dev` does — and is empty otherwise.
	ReceiverEndpoints []string `json:"receiver_endpoints"`

	// EvaluationRunID is the run this Collector feeds, when it feeds one.
	EvaluationRunID string `json:"evaluation_run_id,omitempty"`

	Spans SpanCounts `json:"spans"`

	Producers          []Producer `json:"producers"`
	ProducersTruncated bool       `json:"producers_truncated"`

	// Models is every model-layer behavior evaluated: a span whose telemetry
	// named a model call (gen_ai.request.model under GenAI, llm.model_name
	// under OpenInference) and, where it named one, its provider
	// (gen_ai.provider.name or gen_ai.system; llm.provider or llm.system).
	// Display metadata, read from the Event the convention table already
	// produced — nothing here reads an attribute of its own.
	Models          []ModelCalls `json:"models"`
	ModelsTruncated bool         `json:"models_truncated"`

	// Fidelity counts evaluated spans by how their behavior was named.
	Fidelity FidelityCounts `json:"fidelity"`

	// TransportTargets is, per target, how many distinct operations reached it
	// at transport fidelity — the count that shows several model or tool calls
	// collapsing onto one HTTP destination.
	TransportTargets          []TransportTarget `json:"transport_targets"`
	TransportTargetsTruncated bool              `json:"transport_targets_truncated"`
}

// ModelCalls is one (provider, model) pair and how many calls named it.
type ModelCalls struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Calls    string `json:"calls"`
}

// FidelityCounts is evaluated spans by fidelity.
type FidelityCounts struct {
	Semantic  string `json:"semantic"`
	Transport string `json:"transport"`
}

// TransportTarget is one target seen at transport fidelity.
type TransportTarget struct {
	Target              string `json:"target"`
	Spans               string `json:"spans"`
	DistinctOperations  string `json:"distinct_operations"`
	OperationsSaturated bool   `json:"operations_saturated"`
}

// SpanCounts is what became of every span this processor was handed.
type SpanCounts struct {
	// Received is every span handed to the processor.
	Received string `json:"received"`
	// Evaluated is every span the engine analyzed.
	Evaluated string `json:"evaluated"`
	// Invalid is every span that did not map to a valid Event.
	Invalid string `json:"invalid"`
	// AnalyzeErrors is every span whose analysis failed.
	AnalyzeErrors string `json:"analyze_errors"`
}

// Producer is one service.name seen on incoming resources.
type Producer struct {
	ServiceName     string  `json:"service_name"`
	Spans           string  `json:"spans"`
	LastSeenAgeMS   string  `json:"last_seen_age_ms"`
	Scopes          []Scope `json:"scopes"`
	ScopesTruncated bool    `json:"scopes_truncated"`
	SDK             SDK     `json:"sdk"`
}

// Scope is one instrumentation scope a producer's spans came from.
type Scope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// SDK is the producer's telemetry.sdk.* resource attributes.
type SDK struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Version  string `json:"version"`
}

// ---------------------------------------------------------------------
// Tracker
// ---------------------------------------------------------------------

// Tracker accumulates pipeline facts on the span path.
//
// One mutex, taken once per scope batch rather than once per span, so a
// Collector receiving large batches pays for the lock per batch. A nil
// *Tracker records nothing and is safe to call — that is how a processor with
// no `status:` block pays nothing at all.
type Tracker struct {
	mu        sync.Mutex
	producers map[string]*producerState
	truncated bool

	models          map[modelKey]uint64
	modelsTruncated bool

	semantic, transport uint64

	targets          map[string]*targetState
	targetsTruncated bool
}

type modelKey struct{ provider, model string }

type targetState struct {
	spans      uint64
	operations map[string]struct{}
	saturated  bool
}

type producerState struct {
	display         string
	spans           uint64
	lastSeen        time.Time
	scopes          []Scope
	scopesTruncated bool
	sdk             SDK
}

// NewTracker returns an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{
		producers: make(map[string]*producerState),
		models:    make(map[modelKey]uint64),
		targets:   make(map[string]*targetState),
	}
}

// ObserveScope records one scope batch of spans from one resource.
//
// serviceName is the resource's service.name, possibly empty: a producer that
// set none is still a producer, and reporting it as one is what lets the
// control plane name the missing attribute. sdk and scope are copied only the
// first time they are seen, so the steady state allocates nothing.
func (t *Tracker) ObserveScope(serviceName string, sdk SDK, scope Scope, spans int, now time.Time) {
	if t == nil || spans <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	p, ok := t.producers[serviceName]
	if !ok {
		if len(t.producers) >= MaxProducers {
			t.truncated = true
			return
		}
		p = &producerState{display: Sanitize(serviceName), sdk: sanitizeSDK(sdk)}
		t.producers[strings.Clone(serviceName)] = p
	}
	p.spans += uint64(spans)
	p.lastSeen = now

	for _, known := range p.scopes {
		if known.Name == scope.Name && known.Version == scope.Version {
			return
		}
	}
	if len(p.scopes) >= MaxScopes {
		p.scopesTruncated = true
		return
	}
	p.scopes = append(p.scopes, Scope{Name: Sanitize(scope.Name), Version: Sanitize(scope.Version)})
}

// Evaluated is how one analyzed span's behavior was named. Every field comes
// from the Event the convention table produced; the tracker reads no span.
type Evaluated struct {
	// Semantic is true when a convention named the operation.
	Semantic bool
	// Model is true when the convention classified it as a model call.
	Model bool
	// Operation and Target are the Event's operation and target names: for a
	// model call, the model and its provider.
	Operation string
	Target    string
}

// ObserveEvaluated records one analyzed span.
func (t *Tracker) ObserveEvaluated(e Evaluated) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if e.Semantic {
		t.semantic++
		if e.Model {
			key := modelKey{provider: e.Target, model: e.Operation}
			if _, ok := t.models[key]; ok || len(t.models) < MaxModels {
				if !ok {
					key = modelKey{provider: strings.Clone(e.Target), model: strings.Clone(e.Operation)}
				}
				t.models[key]++
			} else {
				t.modelsTruncated = true
			}
		}
		return
	}

	t.transport++
	target, ok := t.targets[e.Target]
	if !ok {
		if len(t.targets) >= MaxTransportTargets {
			t.targetsTruncated = true
			return
		}
		target = &targetState{operations: make(map[string]struct{})}
		t.targets[strings.Clone(e.Target)] = target
	}
	target.spans++
	if _, known := target.operations[e.Operation]; known {
		return
	}
	if len(target.operations) >= MaxOperationsPerTarget {
		target.saturated = true
		return
	}
	target.operations[strings.Clone(e.Operation)] = struct{}{}
}

// Models returns the model section, ordered by provider then model.
func (t *Tracker) Models() ([]ModelCalls, bool) {
	if t == nil {
		return []ModelCalls{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ModelCalls, 0, len(t.models))
	for key, calls := range t.models {
		out = append(out, ModelCalls{Provider: Sanitize(key.provider), Model: Sanitize(key.model), Calls: formatUint(calls)})
	}
	slices.SortFunc(out, func(a, b ModelCalls) int {
		if c := strings.Compare(a.Provider, b.Provider); c != 0 {
			return c
		}
		return strings.Compare(a.Model, b.Model)
	})
	return out, t.modelsTruncated
}

// Fidelity returns the fidelity counts.
func (t *Tracker) Fidelity() FidelityCounts {
	if t == nil {
		return FidelityCounts{Semantic: "0", Transport: "0"}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return FidelityCounts{Semantic: formatUint(t.semantic), Transport: formatUint(t.transport)}
}

// TransportTargets returns the transport-target section, ordered by target.
// Operation names are counted and never reported: at transport fidelity an
// operation name is a span name, which is transport detail this surface does
// not publish.
func (t *Tracker) TransportTargets() ([]TransportTarget, bool) {
	if t == nil {
		return []TransportTarget{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]TransportTarget, 0, len(t.targets))
	for name, target := range t.targets {
		out = append(out, TransportTarget{
			Target:              Sanitize(name),
			Spans:               formatUint(target.spans),
			DistinctOperations:  formatUint(uint64(len(target.operations))),
			OperationsSaturated: target.saturated,
		})
	}
	slices.SortFunc(out, func(a, b TransportTarget) int { return strings.Compare(a.Target, b.Target) })
	return out, t.targetsTruncated
}

// Producers returns the producer section of a report, ordered by service name
// so two reports of the same state are byte-identical.
func (t *Tracker) Producers(now time.Time) ([]Producer, bool) {
	if t == nil {
		return []Producer{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make([]Producer, 0, len(t.producers))
	for _, p := range t.producers {
		scopes := slices.Clone(p.scopes)
		slices.SortFunc(scopes, func(a, b Scope) int {
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			return strings.Compare(a.Version, b.Version)
		})
		out = append(out, Producer{
			ServiceName:     p.display,
			Spans:           formatUint(p.spans),
			LastSeenAgeMS:   formatUint(ageMillis(now, p.lastSeen)),
			Scopes:          scopes,
			ScopesTruncated: p.scopesTruncated,
			SDK:             p.sdk,
		})
	}
	slices.SortFunc(out, func(a, b Producer) int { return strings.Compare(a.ServiceName, b.ServiceName) })
	return out, t.truncated
}

// ageMillis is now-then in whole milliseconds, never negative: a clock that
// stepped backwards reports a fresh producer rather than an impossible age.
func ageMillis(now, then time.Time) uint64 {
	d := now.Sub(then)
	if d < 0 {
		return 0
	}
	return uint64(d / time.Millisecond)
}

// Sanitize makes a producer-supplied string safe to report.
//
// The control plane refuses invalid UTF-8, control characters and values over
// maxText bytes, and a refused report would hide every other fact in it. So
// those are repaired here: invalid bytes and control characters become U+FFFD,
// and the value is cut at a rune boundary. The repair is visible in the
// output rather than silent, which is the point of using a replacement
// character instead of dropping bytes.
func Sanitize(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			clean = false
			break
		}
	}
	if clean && len(s) <= maxText {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			r = utf8.RuneError
		}
		if b.Len()+utf8.RuneLen(r) > maxText {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sanitizeSDK(s SDK) SDK {
	return SDK{Name: Sanitize(s.Name), Language: Sanitize(s.Language), Version: Sanitize(s.Version)}
}
