package event

import "github.com/trustvian/trustvian/internal/semconv"

// Agent-oriented telemetry normalization, re-exported for adapters outside this
// module.
//
// # Why this lives in `event`
//
// The table itself is internal/semconv — pure, OTel-free, and shared so that two
// adapters cannot drift into two conventions. internal/ is unreachable from
// another Go module, and trustvian-processor is one.
//
// `event` is the right door. ADR 0002 made it public for exactly this shape of
// reason: it is "the one type every caller must construct just to call
// Engine.Analyze at all", and processor/mapping.go already imports it to build
// Event values. The processor is a real consumer, in this repository, today —
// which is ADR 0002's stated bar for public surface, met as written rather than
// stretched.
//
// The alternative was a fourth public top-level package. Rejected as the ceremony
// CLAUDE.md warns about: `event` is already this module's telemetry-facing
// vocabulary, and normalization produces exactly the values `event` defines.
//
// # What this does not do
//
// It applies no trustvian.* override, reads no span, and knows about no
// OpenTelemetry type. An adapter reduces its own span to a Span, calls
// NormalizeSpan, and decides what to do with the result — including letting its
// own explicit overrides win, which they always must.

// SpanKind is a span kind in Trustvian's own vocabulary, for NormalizeSpan.
//
// Deliberately not OpenTelemetry's type: this package imports no OTel package and
// the core never will. An adapter maps its own kind onto this.
type SpanKind = semconv.Kind

// Span kinds NormalizeSpan understands.
const (
	SpanKindUnspecified = semconv.KindUnspecified
	SpanKindClient      = semconv.KindClient
	SpanKindServer      = semconv.KindServer
	SpanKindProducer    = semconv.KindProducer
	SpanKindConsumer    = semconv.KindConsumer
	SpanKindInternal    = semconv.KindInternal
)

// NormalizedSpan is one span reduced to the values the convention tables read.
//
// Attributes and Resource are read, never written; a nil map means "none".
type NormalizedSpan = semconv.Span

// Normalization is what a convention established about one span.
//
// Every field is optional. A zero value means the telemetry did not say, which is
// never an instruction to substitute something — it is an instruction to leave the
// adapter's existing mapping alone.
type Normalization = semconv.Normalized

// Fidelity states how much semantic information a behavior was derived from.
//
// A closed vocabulary of two values. It describes the mapping result rather than
// the span, and it is never behavioral identity — see the type's own
// documentation for why folding it into a fingerprint would discard a baseline
// every time a producer upgraded its instrumentation.
type Fidelity = semconv.Fidelity

// The fidelity vocabulary, and the attribute that carries it outbound.
const (
	FidelityTransport = semconv.FidelityTransport
	FidelitySemantic  = semconv.FidelitySemantic

	// AttrFidelity is the outbound span attribute, beside the other trustvian.*
	// enrichment. There is deliberately no inbound override: a producer able to
	// claim semantic fidelity would defeat the guarantee the indicator makes.
	AttrFidelity = semconv.AttrFidelity
)

// Layer states which instrumentation layer supplied a behavior's operation
// identity: a model call, a named tool call, a retrieval, or the transport.
//
// A closed vocabulary, and never behavioral identity — it exists so a consumer
// can tell a model call from a document-store query without a sixth
// OperationCategory value re-fingerprinting every model call a producer had
// already been emitting. See the type's own documentation and ADR 0047.
type Layer = semconv.Layer

// The layer vocabulary, and the attribute that carries it outbound.
const (
	// LayerUnspecified means nothing classified this behavior. Deliberately not
	// folded into LayerTransport: an Event built directly by an SDK caller was
	// not classified, which is a different statement from "its identity came
	// from the transport".
	LayerUnspecified = semconv.LayerUnspecified

	LayerModel     = semconv.LayerModel
	LayerTool      = semconv.LayerTool
	LayerRetrieval = semconv.LayerRetrieval
	LayerTransport = semconv.LayerTransport

	// AttrLayer is the outbound span attribute, beside the other trustvian.*
	// enrichment. There is no inbound override, for the same reason fidelity has
	// none: a producer able to claim `tool` would defeat the guarantee that a
	// layer reflects what its telemetry established.
	AttrLayer = semconv.AttrLayer
)

// NormalizeSpan reads the agent-oriented conventions one span carries.
//
// Pure: no clock, no I/O, and the maps it is given are not modified. Identical
// input always produces identical output.
//
// Precedence is fixed and tested — OpenTelemetry GenAI, then OpenInference, then
// nothing. A caller applies its own trustvian.* overrides afterwards, because an
// explicit operator statement outranks a convention reading.
func NormalizeSpan(s NormalizedSpan) Normalization {
	return semconv.Normalize(s)
}

// ContentAttributes is every attribute both conventions define as content:
// prompts, completions, tool arguments and results, retrieved documents.
//
// Normalization reads none of them. This is exported so a consumer can *assert*
// that — the paired test in this repository feeds every key through the whole
// pipeline and requires each value to reach no durable or published layer — rather
// than having to take the claim on trust.
func ContentAttributes() []string {
	return semconv.ContentAttributes()
}
