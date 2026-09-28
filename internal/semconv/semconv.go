// Package semconv normalizes agent-oriented telemetry conventions into the
// behavioral dimensions Trustvian's core already has.
//
// # Why this package exists at all
//
// Two adapters turn spans into event.Event values: internal/otel in this module,
// for the in-process SDK path, and the Collector processor in
// trustvian-processor, for the OTLP pipeline path. They cannot share their
// traversal code — sdktrace.ReadOnlySpan and ptrace.Span are unrelated types and
// the processor is a separate Go module — so processor/mapping.go reimplements
// internal/otel's traversal, and documents why.
//
// A *mapping table* is not forced to be duplicated the same way. Both adapters
// reduce their span to the same three plain Go values first: a kind, a name, and
// a map of attributes. So the table lives here once, takes those values, and both
// call it. Two copies of a convention table is two conventions, and the one a
// developer gets is whichever adapter their telemetry happened to take.
//
// # Why it imports no OpenTelemetry package
//
// .claude/rules/go.md confines go.opentelemetry.io/otel{,/sdk,/trace} to
// internal/otel, so this package could not import them even if it wanted to. It
// does not want to, and not only because of the rule: the GenAI attribute keys
// are not in the semconv package this repository pins. They were added around
// semconv v1.39.0, grew through v1.41.0, and are absent from v1.42.0 onward —
// which is the version both adapters use. The names have to be string literals
// somewhere regardless, so putting them here costs nothing and buys a package the
// core's dependency graph can contain.
//
// That instability is also the reason for every "degrade, do not break" rule
// below. A convention whose Go constants disappeared between two patch releases
// is not something to hard-code confidently.
//
// # What it reads, and what it refuses to read
//
// Identity: which tool, which model, which agent, which conversation. Those are
// behavioral dimensions — what the actor did.
//
// Content: prompts, completions, tool arguments, tool results, retrieved
// documents. Those are what the actor *said*, they routinely carry customer
// data, and this package reads none of them. Every content attribute named in
// either convention carries an explicit sensitivity warning upstream; the
// deny-list in content.go records them so a reader can see the refusal is
// deliberate and complete rather than incidental.
//
// # Purity
//
// Normalize is a pure function: no clock, no I/O, no randomness, and it does not
// modify the map it is given. Identical input always produces identical output,
// which is what makes the table testable without a span at all.
package semconv

// Kind is a span kind, in Trustvian's own vocabulary.
//
// Not trace.SpanKind or ptrace.SpanKind: this package holds no OpenTelemetry
// import, and both adapters already translate their own kind into a direction.
// Only the two values the table distinguishes are named; everything else is
// KindUnspecified, because the table does not branch on the rest.
type Kind string

const (
	KindUnspecified Kind = ""
	KindClient      Kind = "client"
	KindServer      Kind = "server"
	KindProducer    Kind = "producer"
	KindConsumer    Kind = "consumer"
	KindInternal    Kind = "internal"
)

// Span is one span reduced to the values the table reads.
//
// Attributes and Resource are read, never written. A nil map is valid and means
// "no attributes", which is the same thing as an empty one here.
type Span struct {
	Kind       Kind
	Name       string
	Attributes map[string]any
	Resource   map[string]any
}

// Normalized is what a convention established about one span.
//
// Every field is optional and the zero value means "the telemetry did not say".
// That is the contract callers depend on: an empty field is never a signal to
// substitute something, it is a signal to leave today's mapping alone.
//
// The string fields are plain strings rather than event.OperationCategory and
// event.ActorType so that this package carries no opinion about which core types
// exist. The two adapters convert, which is a cast each.
type Normalized struct {
	// OperationCategory is one of event's category values, as a string. Empty
	// means no convention matched.
	OperationCategory string

	// OperationName is the tool, model, agent or data source the telemetry
	// named. Never derived from the span name: a span name is transport.
	OperationName string

	// TargetName is what the operation reached, when a convention said so.
	// Empty means "the caller should keep whatever the transport mapping
	// found", which is not the same as "the target is nothing".
	TargetName string

	// ActorType is set only when the producer established an agent identity.
	// See actorTypeFor for why presence of a GenAI operation is not enough.
	ActorType string

	// SessionID is the conversation or session identifier, for correlation
	// only. It never becomes behavioral identity — see event.Context.
	SessionID string

	// Fidelity says where OperationName came from. It describes this result,
	// not the span: a span carrying conventions the table declined to read is
	// FidelityTransport, because transport is what the behavior was derived
	// from.
	Fidelity Fidelity
}

// Matched reports whether a convention established an operation identity.
//
// The discriminator is OperationName rather than OperationCategory, and that is
// deliberate: a category without a name is the fabrication this package exists
// to prevent. It would render as "tool · POST", a semantic category wearing a
// transport name.
func (n Normalized) Matched() bool {
	return n.OperationName != "" && n.OperationCategory != ""
}

// Normalize reads the agent-oriented conventions one span carries.
//
// Precedence is fixed and tested rather than left to map iteration:
//
//  1. OpenTelemetry GenAI      gen_ai.operation.name present
//  2. OpenInference            openinference.span.kind present
//  3. neither                  a zero Normalized, meaning "keep today's mapping"
//
// GenAI wins when a span carries both. It is the vendor-neutral OpenTelemetry
// convention, its operation attribute is Required where OpenInference's kind is
// required only within a vendor's own spec, and a span carrying both is in
// practice an OpenInference producer that also emits GenAI — so preferring GenAI
// picks the more portable reading of the same event.
//
// Callers apply their own trustvian.* overrides *after* this, because an explicit
// operator statement outranks a convention reading. This function knows nothing
// about them.
func Normalize(s Span) Normalized {
	if n, ok := normalizeGenAI(s); ok {
		return n
	}
	if n, ok := normalizeOpenInference(s); ok {
		return n
	}
	return Normalized{}
}

// stringAttr reads a string attribute, or "" when it is missing or not a string.
//
// The type check is the malformed-attribute rule: a producer emitting
// gen_ai.tool.name as an integer, or as a list because a framework flattened
// something, yields "" and therefore no semantic match. Degrading is the
// specified behavior; erroring on somebody else's telemetry is not.
func stringAttr(attrs map[string]any, key string) string {
	v, _ := attrs[key].(string)
	return v
}

// firstString returns the first key that holds a non-empty string.
//
// Used where a convention has more than one spelling for one fact — either
// because it renamed an attribute and producers lag, or because it defined two.
func firstString(attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringAttr(attrs, key); v != "" {
			return v
		}
	}
	return ""
}
