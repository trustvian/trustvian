package semconv

// Layer states which instrumentation layer supplied a behavior's operation
// identity.
//
// # Why this is not an OperationCategory
//
// The obvious shape for "this was a model call" is a sixth
// event.OperationCategory value. It is refused, and not narrowly:
// OperationCategory is a StableFeatures dimension, so adding `model` would
// re-fingerprint every model call a producer had already been emitting and
// discard those actors' learned baselines — in order to improve a label. ADR 0047
// records the decision; task 083 records the reasoning in full.
//
// A non-identity classification also carries strictly more information than a
// category could. `external` today means both "a model was consulted" and "a
// document store was queried", and a category cannot separate them without
// becoming two categories. This can, at no cost to identity.
//
// # Why four values
//
// Three would be dishonest. Collapsing retrieval into LayerModel asserts a model
// call that did not happen; collapsing it into LayerTransport denies a convention
// that did match. Four is the smallest vocabulary that says only what the
// telemetry said.
//
// # Why it is gated on fidelity
//
// A layer is claimed exactly when Fidelity is FidelitySemantic — when the
// convention supplied the operation *name*. A GenAI span whose category matched
// but whose identity attribute was missing keeps today's transport mapping, so
// its layer is LayerTransport: transport is what its behavior was actually
// derived from. One gate rather than two is what stops the two indicators from
// disagreeing about one span.
//
// # What it is not
//
// Not behavioral identity, in any of its three senses. It never enters
// StableFeatures, a Fingerprint or a baseline key, and a test asserts that. It is
// also not a *count*: how many counted changes one act produces is a control-plane
// policy, which ADR 0047 keeps separate from this classification on purpose.
type Layer string

const (
	// LayerUnspecified means nothing classified this behavior — an Event that
	// never passed through a telemetry adapter, constructed directly by an SDK
	// caller.
	//
	// Deliberately *not* folded into LayerTransport, which is where this type
	// differs from Fidelity. Fidelity answers "where did the name come from",
	// and "nothing proved a semantic name" really is transport. Layer answers
	// "what kind of operation was this", and a hand-built Event with category
	// `tool` is not a transport operation — Trustvian simply did not classify
	// it. Rendering it as transport would assert something no telemetry said.
	LayerUnspecified Layer = ""

	// LayerModel means the producer named a model or embedding invocation.
	LayerModel Layer = "model"

	// LayerTool means the producer named a tool, agent or workflow invocation —
	// the agent acting.
	LayerTool Layer = "tool"

	// LayerRetrieval means the producer named a retrieval or rerank against a
	// data source.
	LayerRetrieval Layer = "retrieval"

	// LayerTransport means no convention named the operation, so identity came
	// from the transport: HTTP, DB or RPC, or an explicit operator override.
	//
	// Whether such an operation was outbound or inbound is already answered by
	// Operation.Direction, and is deliberately not duplicated here.
	//
	// So LayerTransport establishes no direction, and nothing may present it as
	// one. It is the value for an inbound SERVER or CONSUMER span, for an
	// outbound CLIENT or PRODUCER span, and for a span whose kind states no
	// direction at all — what they share is that no convention named the
	// operation, not which way the call went. A renderer wanting to say
	// "outbound request" must read Operation.Direction and combine the two;
	// saying it from this value alone is a claim the value does not support.
	LayerTransport Layer = "transport"
)

// AttrLayer is the outbound span attribute carrying the value.
//
// Beside the other trustvian.* enrichment, so a trace backend shows it next to
// everything else — the same fan-out property task 075 asked for on fidelity.
//
// There is deliberately no *inbound* override, for the same reason fidelity has
// none: a producer able to claim `tool` would defeat the guarantee that a layer
// reflects what its telemetry actually established.
const AttrLayer = "trustvian.behavior.layer"

// Valid reports whether l is one of the four classified members.
//
// LayerUnspecified is not valid: it means "nothing classified this", which is a
// legitimate state to render but never a value to accept over the wire. The
// platform boundary uses this to refuse an unrecognized layer from an older or
// newer producer rather than carrying it through as if it meant something.
//
// So this is also the answer to "was this behavior classified at all", and there
// is deliberately no second predicate saying so: two names for one condition are
// two things that can drift apart.
func (l Layer) Valid() bool {
	switch l {
	case LayerModel, LayerTool, LayerRetrieval, LayerTransport:
		return true
	default:
		return false
	}
}
