package otel

import (
	"go.opentelemetry.io/otel/attribute"

	trustvian "github.com/trustvian/trustvian"
	trustviansemconv "github.com/trustvian/trustvian/internal/semconv"
)

// Trustvian output attributes: the outbound half of this adapter's
// boundary, enriching telemetry with Trustvian's verdict on it. See
// AttributesFromResult.
//
// These are distinct from, and serve the opposite direction of, the
// four input override attributes above (AttrActorID, etc.) — those
// influence how a span becomes an Event; these describe what Trustvian
// decided about the Event a span already became.
const (
	AttrAnomalyScore  = "trustvian.anomaly.score"
	AttrTrustScore    = "trustvian.trust.score"
	AttrRiskLevel     = "trustvian.risk.level"
	AttrDecision      = "trustvian.decision"
	AttrFingerprintID = "trustvian.fingerprint.id"

	// AttrFidelity says where the Event's operation identity came from:
	// "semantic" when an agent-oriented convention supplied it, "transport" when
	// only protocol and target were available.
	//
	// Outbound only. There is deliberately no inbound override of the same name —
	// a producer able to *claim* semantic fidelity would defeat the one guarantee
	// the indicator makes, which is that a semantic name came from telemetry
	// rather than from Trustvian. See internal/semconv.Fidelity.
	AttrFidelity = trustviansemconv.AttrFidelity
)

// AttributesFromResult derives the outbound trustvian.* attributes from
// a Result, for a caller to attach to a span or export alongside one.
// It is a pure function: identical input always produces identical
// output, and result is never modified. It does not write to a live
// span itself — attaching the returned attributes to a real span (e.g.
// inside a future OTel Collector processor) is a caller's concern, not
// this adapter's; see the package doc for why the line is drawn there.
//
// trustvian.behavior.id, named alongside these five in the original
// project spec, is deliberately not included here. Its meaning was
// never defined beyond "carried over from the spec's original naming,"
// and every plausible reading collapses into Fingerprint.ID:
// internal/fingerprint's Fingerprint already IS the identity of one
// behavioral shape for an actor, not a per-request identifier that a
// separate "behavior ID" would need to disambiguate from. Inventing a
// second identifier for the same concept — or a new "actor-level
// profile spanning multiple Fingerprints" concept nothing in the
// domain model tracks today — would be exactly the kind of
// undocumented telemetry attribute CLAUDE.md's OpenTelemetry section
// warns against adding without a real, current need. If a genuinely
// distinct behavior-level identity is scoped later (see
// docs/tasks/008-otel.md), this attribute name is reserved for it, not
// implemented speculatively now.
//
// Anomaly.Score and Trust.Score are not independently re-validated for
// NaN/Inf here: internal/trust.Compute already clamps its inputs and
// output to [0,1] (internal/trust/trust.go), and internal/anomaly's
// noisy-OR combination is bounded to [0,1] by construction (every
// signal's contribution is clamped before combining) — see
// TestComputeScenarioMatrixBoundsAndMonotonicity and
// TestComputeClampsOutOfRangeInputs. Re-checking a value this function
// cannot itself produce out-of-range would duplicate an already-proven
// invariant rather than close a real gap.
func AttributesFromResult(result trustvian.Result) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Float64(AttrAnomalyScore, result.Anomaly.Score),
		attribute.Float64(AttrTrustScore, result.Trust.Score),
		attribute.String(AttrRiskLevel, string(result.Trust.Risk)),
		attribute.String(AttrDecision, string(result.Decision)),
		attribute.String(AttrFingerprintID, result.Fingerprint.ID),
		attribute.String(AttrFidelity, string(fidelityOf(result))),
	}
}

// fidelityOf reads back what the inbound mapping recorded.
//
// It comes from result.Event.Attributes rather than from a Result field because
// fidelity is a property of how the Event was *derived*, and Result already
// retains its Event for exactly this kind of traceability. Adding a Result field
// would be a core change task 075 must not make.
//
// A Result whose Event never passed through this adapter — one built by hand, or
// by a caller using the SDK directly — carries no such attribute, and gets
// "transport". That is the honest answer: nothing proved a semantic identity.
func fidelityOf(result trustvian.Result) trustviansemconv.Fidelity {
	raw, _ := result.Event.Attributes[AttrFidelity].(string)
	return trustviansemconv.Fidelity(raw).OrTransport()
}
