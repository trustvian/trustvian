package trustvianprocessor

import (
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/trustvian/trustvian/event"
)

// Trustvian-specific override attributes — the exact same names and
// meanings as internal/otel's inbound overrides in the core module.
// Necessarily reimplemented here: this is a separate Go module and
// cannot import github.com/trustvian/trustvian/internal/otel (see
// README.md § why this duplicates internal/otel's mapping).
const (
	attrActorID            = "trustvian.actor.id"
	attrActorType          = "trustvian.actor.type"
	attrIdentityConfidence = "trustvian.identity.confidence"
	attrOperationCategory  = "trustvian.operation.category"

	// Bridged volatile-signal keys features.Extract reads — mirrors
	// features.AttrDurationMS/AttrError's literal values exactly (that
	// package is also internal/ and unreachable from here, so the
	// string literals themselves are the contract, not the constant).
	attrDurationMS = "duration_ms"
	attrError      = "error"
)

// EventFromSpan derives an event.Event from one pdata span plus its
// resource's attributes. It is a pure function: identical input always
// produces identical output, and neither argument is modified.
//
// This mirrors internal/otel.EventFromSpan's semantic-convention
// mapping and override-attribute conventions (see this core
// repository's docs/OPENTELEMETRY.md) as closely as the two data
// models allow — it reuses the exact same semconv key constants
// (go.opentelemetry.io/otel/semconv, a plain-constants package with no
// SDK dependency) so the convention names themselves can never drift
// between the two adapters, even though the traversal code differs.
//
// Since task 075 it reads the agent-oriented conventions — OpenTelemetry GenAI
// and OpenInference — through event.NormalizeSpan. That is **not** a second copy
// of the table: internal/semconv in the core module holds it once, and
// internal/otel and this file both call it. The traversal below is duplicated
// because the span types are unrelated; the convention table is not, because both
// adapters reduce their span to the same plain map first. Two copies of a
// convention table would be two conventions, and the one a developer gets would
// be whichever adapter their telemetry happened to take.
//
// Why this can't just call internal/otel.EventFromSpan: that function
// takes sdktrace.ReadOnlySpan, a type only the OpenTelemetry SDK's own
// in-process span-export path produces (it has a deliberately
// unexported method — see internal/otel's own testing notes in the
// core repository). A Collector processor never sees that type; it
// receives ptrace.Span, the OTLP wire/pipeline data model, which has no
// relationship to sdktrace.ReadOnlySpan at all. There is no legitimate
// adapter between them, so this is a parallel implementation, not a
// shortcut around a shared one that could have existed instead.
func EventFromSpan(resourceAttrs pcommon.Map, span ptrace.Span) event.Event {
	attrs := attributeMap(span.Attributes())
	bridgeVolatileSignals(attrs, span)

	actorID := stringAttr(attrs, attrActorID)
	if actorID == "" {
		actorID = stringResourceAttr(resourceAttrs, string(semconv.ServiceNameKey))
	}

	actorType := event.ActorType(stringAttr(attrs, attrActorType))
	if actorType == "" {
		actorType = event.ActorTypeService
	}

	identityConfidence := 1.0
	if v, ok := floatAttr(attrs, attrIdentityConfidence); ok {
		identityConfidence = v
	}

	// The shared convention table. Read after the overrides above, which outrank
	// it, and before the transport fallbacks below, which defer to it.
	normalized := event.NormalizeSpan(event.NormalizedSpan{
		Kind:       spanKind(span.Kind()),
		Name:       span.Name(),
		Attributes: attrs,
		Resource:   resourceAttrs.AsRaw(),
	})

	category := event.OperationCategory(stringAttr(attrs, attrOperationCategory))
	if category == "" && normalized.Matched() {
		category = event.OperationCategory(normalized.OperationCategory)
	}
	if category == "" {
		category = inferCategory(attrs)
	}

	// A span name is transport: "POST /v1/export" describes the request, not the
	// tool the agent chose. It stays the operation name only when no convention
	// supplied a better one.
	operationName := span.Name()
	if normalized.Matched() {
		operationName = normalized.OperationName
	}

	// The convention's target where it has one, else the transport's. Left empty
	// rather than substituted when neither says: inventing a target is the
	// fabrication task 075 forbids, and Target.Name is optional.
	target := normalized.TargetName
	if target == "" {
		target = targetName(attrs)
	}

	// A convention may raise the actor type but never override an explicit one.
	// Actor.Type is a StableFeatures dimension, so a wrong upgrade discards that
	// actor's learned baseline — see internal/semconv's own reasoning.
	if stringAttr(attrs, attrActorType) == "" && normalized.ActorType != "" {
		actorType = event.ActorType(normalized.ActorType)
	}

	// Recorded on the Event, not in a core field: Event has no fidelity field and
	// task 075 must not add one. Attributes is where this adapter already puts
	// derived values, and SetAttributesFromResult reads it back from here.
	attrs[event.AttrFidelity] = string(normalized.Fidelity.OrTransport())

	// The layer, recorded the same way and for the same reason. A span the table
	// declined to read is transport rather than unclassified: this adapter *did*
	// classify it, and the answer is that its identity came from the transport.
	// Task 083.
	attrs[event.AttrLayer] = string(layerFor(normalized))

	return event.Event{
		ID:        span.SpanID().String(),
		Timestamp: span.StartTimestamp().AsTime(),
		Actor: event.Actor{
			ID:                 actorID,
			Type:               actorType,
			IdentityConfidence: identityConfidence,
		},
		Operation: event.Operation{
			Category:  category,
			Name:      operationName,
			Direction: directionFromSpanKind(span.Kind()),
		},
		Target:     event.Target{Name: target},
		Attributes: attrs,
		Context: event.Context{
			Environment: stringResourceAttr(resourceAttrs, string(semconv.DeploymentEnvironmentNameKey)),
			TraceID:     span.TraceID().String(),
			SpanID:      span.SpanID().String(),
			SessionID:   normalized.SessionID,
		},
	}
}

// spanKind translates the pipeline's kind into the table's own vocabulary.
//
// The table holds no OpenTelemetry import, so the translation happens here where
// the pdata types already are — the mirror of internal/otel's own spanKind.
func spanKind(kind ptrace.SpanKind) event.SpanKind {
	switch kind {
	case ptrace.SpanKindClient:
		return event.SpanKindClient
	case ptrace.SpanKindServer:
		return event.SpanKindServer
	case ptrace.SpanKindProducer:
		return event.SpanKindProducer
	case ptrace.SpanKindConsumer:
		return event.SpanKindConsumer
	case ptrace.SpanKindInternal:
		return event.SpanKindInternal
	default:
		return event.SpanKindUnspecified
	}
}

// bridgeVolatileSignals sets the duration_ms/error keys from span
// timing and status, exactly like internal/otel's bridging (span
// duration/status are fields on the span, not attributes, and
// features.Extract only ever reads Event.Attributes).
//
// span.EndTimestamp() == 0 is the correct "unset" test here, not
// span.EndTimestamp().AsTime().IsZero(): pcommon.Timestamp is a Unix-
// epoch-nanoseconds uint64, so its zero value converts to 1970-01-01,
// not Go's zero time.Time{} (year 1) — unlike sdktrace.ReadOnlySpan's
// EndTime(), which returns a real time.Time and can be compared with
// IsZero() directly. Using IsZero() here would silently treat every
// span with a genuinely unset EndTimestamp as if it ended in 1970.
func bridgeVolatileSignals(attrs map[string]any, span ptrace.Span) {
	if span.EndTimestamp() != 0 {
		if dur := span.EndTimestamp().AsTime().Sub(span.StartTimestamp().AsTime()); dur > 0 {
			attrs[attrDurationMS] = float64(dur) / float64(time.Millisecond)
		}
	}
	if span.Status().Code() == ptrace.StatusCodeError {
		attrs[attrError] = true
	}
}

func attributeMap(m pcommon.Map) map[string]any {
	out := make(map[string]any, m.Len())
	for k, v := range m.All() {
		out[k] = v.AsRaw()
	}
	return out
}

func stringAttr(attrs map[string]any, key string) string {
	v, _ := attrs[key].(string)
	return v
}

func stringResourceAttr(resourceAttrs pcommon.Map, key string) string {
	v, ok := resourceAttrs.Get(key)
	if !ok {
		return ""
	}
	return v.Str()
}

func floatAttr(attrs map[string]any, key string) (float64, bool) {
	switch v := attrs[key].(type) {
	case float64:
		return v, true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}

// inferCategory mirrors internal/otel's fallback order exactly: HTTP,
// then DB, then RPC as the generic catch-all.
func inferCategory(attrs map[string]any) event.OperationCategory {
	if _, ok := attrs[string(semconv.HTTPRequestMethodKey)]; ok {
		return event.OperationCategoryHTTP
	}
	if _, ok := attrs[string(semconv.DBSystemNameKey)]; ok {
		return event.OperationCategoryDB
	}
	return event.OperationCategoryRPC
}

func targetName(attrs map[string]any) string {
	if v := stringAttr(attrs, string(semconv.ServicePeerNameKey)); v != "" {
		return v
	}
	if v := stringAttr(attrs, string(semconv.DBNamespaceKey)); v != "" {
		return v
	}
	return stringAttr(attrs, string(semconv.ServerAddressKey))
}

func directionFromSpanKind(kind ptrace.SpanKind) event.OperationDirection {
	switch kind {
	case ptrace.SpanKindServer, ptrace.SpanKindConsumer:
		return event.DirectionInbound
	case ptrace.SpanKindClient, ptrace.SpanKindProducer:
		return event.DirectionOutbound
	default:
		return event.DirectionUnspecified
	}
}
