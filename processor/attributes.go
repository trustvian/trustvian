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
