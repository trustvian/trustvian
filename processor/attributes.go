package trustvianprocessor

import (
	"go.opentelemetry.io/collector/pdata/pcommon"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// Trustvian output attributes — the same five names and meanings as
// internal/otel.AttributesFromResult in the core module (see
// docs/OPENTELEMETRY.md there), necessarily reimplemented here for the
// same module-boundary reason as EventFromSpan (see mapping.go):
// internal/otel is under internal/ in the core module and unreachable
// from a separate module. trustvian.behavior.id is deliberately
// omitted for the identical reason it's omitted there — see the core
// module's rationale, which applies unchanged here since nothing about
// its meaning became any more defined by moving to a Collector
// processor.
const (
	attrAnomalyScore  = "trustvian.anomaly.score"
	attrTrustScore    = "trustvian.trust.score"
	attrRiskLevel     = "trustvian.risk.level"
	attrDecision      = "trustvian.decision"
	attrFingerprintID = "trustvian.fingerprint.id"

	// attrFidelity says where the Event's operation identity came from —
	// "semantic" or "transport". Task 075.
	//
	// The name comes from event.AttrFidelity rather than being spelled again, so
	// the two adapters cannot disagree about it. Outbound only: there is no
	// inbound override, because a producer able to claim semantic fidelity would
	// defeat the guarantee the indicator makes.
	attrFidelity = event.AttrFidelity

	// attrLayer says which instrumentation layer supplied the identity — a model
	// call, a named tool call, a retrieval, or the transport. Task 083.
	//
	// Outbound only, like attrFidelity, and for the same reason: a producer able
	// to claim `tool` would defeat the guarantee that a layer reflects what its
	// telemetry established. It is never behavioral identity; see ADR 0047.
	attrLayer = event.AttrLayer
)

// SetAttributesFromResult writes the outbound trustvian.* attributes
// derived from result directly onto attrs (a span's own attribute map).
//
// Unlike internal/otel.AttributesFromResult in the core module — which
// deliberately returns a slice for a caller to attach later, since that
// module has no live span to write to — this function DOES mutate a
// live span's attributes in place. That's the correct, expected
// difference: task 008 explicitly left "attaching attributes to a real
// span" as the Collector processor's concern, not the core adapter's.
// This is that attachment point.
//
// Anomaly.Score and Trust.Score are not independently re-validated for
// NaN/Inf here, for the same reason as the core module's version:
// internal/trust.Compute and internal/anomaly's noisy-OR combination
// already guarantee both are finite and bounded to [0,1] before a
// Result is ever produced.
func SetAttributesFromResult(attrs pcommon.Map, result trustvian.Result) {
	attrs.PutDouble(attrAnomalyScore, result.Anomaly.Score)
	attrs.PutDouble(attrTrustScore, result.Trust.Score)
	attrs.PutStr(attrRiskLevel, string(result.Trust.Risk))
	attrs.PutStr(attrDecision, string(result.Decision))
	attrs.PutStr(attrFingerprintID, result.Fingerprint.ID)
	attrs.PutStr(attrFidelity, string(fidelityOf(result)))
	attrs.PutStr(attrLayer, string(layerOf(result)))
}

// fidelityOf reads back what the inbound mapping recorded on the Event.
//
// From result.Event.Attributes rather than a Result field, because fidelity is a
// property of how the Event was derived and Result already retains its Event for
// that kind of traceability. A Result whose Event never passed through
// EventFromSpan carries none, and gets "transport" — the honest answer, since
// nothing proved a semantic identity.
func fidelityOf(result trustvian.Result) event.Fidelity {
	raw, _ := result.Event.Attributes[attrFidelity].(string)
	return event.Fidelity(raw).OrTransport()
}

// layerOf reads back the layer the inbound mapping recorded.
//
// No transport fallback, unlike fidelityOf, and the asymmetry is deliberate: a
// Result whose Event never passed through EventFromSpan was never classified, and
// reporting it as `transport` would assert that its identity came from a protocol
// when nothing established that. It stays empty, which consumers render as "not
// classified".
func layerOf(result trustvian.Result) event.Layer {
	raw, _ := result.Event.Attributes[attrLayer].(string)
	layer := event.Layer(raw)
	if !layer.Valid() {
		return event.LayerUnspecified
	}
	return layer
}

// layerFor decides the layer for one normalization result.
//
// Gated on Matched() so it cannot claim a layer for a row that did not fire — the
// same guard fidelity uses, applied once rather than per branch.
func layerFor(n event.Normalization) event.Layer {
	if n.Matched() && n.Layer.Valid() {
		return n.Layer
	}
	return event.LayerTransport
}
