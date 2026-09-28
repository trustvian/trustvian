package semconv

// Fidelity states how much semantic information a behavior was derived from.
//
// A closed two-value vocabulary. Task 075 rules out the two obvious wrong
// shapes explicitly — free text and a confidence float — and a per-dimension
// matrix is a third nobody asked for. Its whole purpose is to let a consumer say
// "this is what your telemetry told us" instead of implying a fidelity the
// producer never supplied.
//
// It describes the *mapping result*, not the span. A span carrying GenAI
// attributes that the table declined to read — an operation name outside the
// enum, a tool operation with no tool name — is FidelityTransport, because
// transport is what the behavior was actually derived from. Fidelity answers
// "where did this name come from", so the answer has to be about the name.
//
// It is deliberately *not* a StableFeatures dimension. Folding it into the
// fingerprint would mean the same logical behavior fingerprinted differently
// before and after a producer upgraded its instrumentation, discarding a learned
// baseline and reporting the reset as a new behavior.
type Fidelity string

const (
	// FidelityTransport means only protocol and target were available. This is
	// today's behavior for every span, and stays the value for every span no
	// convention matched.
	FidelityTransport Fidelity = "transport"

	// FidelitySemantic means an agent-oriented convention supplied the
	// operation identity.
	FidelitySemantic Fidelity = "semantic"
)

// AttrFidelity is the outbound span attribute carrying the value.
//
// Named beside the other trustvian.* enrichment so a trace backend shows it
// alongside everything else, which is the fan-out property task 075 asks for.
//
// There is deliberately no *inbound* override for it. A producer able to claim
// semantic fidelity would defeat the one guarantee the indicator exists to make:
// that a semantic name came from telemetry rather than from Trustvian.
const AttrFidelity = "trustvian.fidelity"

// Valid reports whether f is one of the two members.
//
// Used by the platform boundary rather than by this package: a fidelity arriving
// over the wire from an older or newer producer must be recognizable as
// unsupported instead of being carried through as if it meant something.
func (f Fidelity) Valid() bool {
	switch f {
	case FidelityTransport, FidelitySemantic:
		return true
	default:
		return false
	}
}

// OrTransport returns f when it is valid, and FidelityTransport otherwise.
//
// The safe direction for an unrecognized value: claiming less than the telemetry
// proved is a smaller error than claiming more, and "absent means transport" is
// what lets an old producer, an old Collector and a new control plane interoperate
// without negotiating a version.
func (f Fidelity) OrTransport() Fidelity {
	if f.Valid() {
		return f
	}
	return FidelityTransport
}
