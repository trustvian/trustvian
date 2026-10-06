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
	return &Tracker{producers: make(map[string]*producerState)}
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
