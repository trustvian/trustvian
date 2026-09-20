package trustvian

import (
	"time"

	"github.com/trustvian/trustvian/event"
)

// DecisionRecord is a detached, serializable public projection of one
// Engine.Analyze outcome: the evidence that produced a Decision, and the
// Decision itself.
//
// Detached, not immutable — the fields are exported and Contributors is a
// slice, so a holder can modify one. The guarantee is ownership rather than
// constancy: a record shares no memory with the Result it came from.
// Mutating that Result afterward cannot change the record, mutating the
// record cannot change the Result, and producing one mutates nothing at
// all.
//
// Result is the engine's rich in-process output and stays that. This is the
// durable boundary — what a consumer persists, streams, aggregates, or sends
// over an API. Every field is a public type or a primitive, so a consumer can
// declare, marshal, and store one without importing anything beyond this
// package.
//
// What it deliberately is not: an archive of the observed event. The record
// carries security evidence, not producer payload. Event.Attributes, tool
// arguments, prompts, and completions have no field here and cannot leak into
// its JSON. A consumer that needs raw history is responsible for storing it
// itself, as an explicit choice rather than a side effect of recording a
// decision.
//
// That makes the record fixed-shape, which is not the same as size-bounded.
// Several fields carry caller-supplied strings — identifiers, operation and
// target names — and nothing here limits their length. What is excluded is
// the open-ended part: no attribute map, no arbitrary payload, no field that
// grows with what a producer chose to send. Request and field size limits
// belong to whatever ingests events over a network, not to this type.
//
// It carries no notion of the system consuming it either — no project,
// candidate, evaluation, or promotion identifier. Those belong to whatever
// associates records with its own metadata, not to the engine that produced
// them.
type DecisionRecord struct {
	// EventID and Timestamp identify the observed action this decision was
	// made about.
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`

	// ActorID, ActorType, and IdentityConfidence describe who acted.
	// IdentityConfidence is the upstream signal trust consumed, not
	// something Trustvian computed.
	ActorID            string          `json:"actor_id"`
	ActorType          event.ActorType `json:"actor_type"`
	IdentityConfidence float64         `json:"identity_confidence"`

	// Environment is the learning scope this decision was made in. It comes
	// from the baseline key the engine actually used, so it reflects the
	// scope the baseline was read from rather than what the event claimed.
	Environment string `json:"environment"`

	// Behavior is the stable behavioral shape — what kind of action this
	// was. It is what lets a consumer explain that an actor started running
	// shell commands without reversing a fingerprint hash.
	Behavior StableFeatures `json:"behavior"`

	// FingerprintID is the stable identity derived from Behavior. Two events
	// with identical stable dimensions share it.
	FingerprintID string `json:"fingerprint_id"`

	// AnomalyScore is how unusual this behavior is; AnomalyConfidence is how
	// much learned history supports that reading. They are separate numbers
	// on purpose — a brand-new fingerprint scores maximally novel with zero
	// confidence, and consuming the score without the confidence turns cold
	// start into a false positive.
	AnomalyScore      float64 `json:"anomaly_score"`
	AnomalyConfidence float64 `json:"anomaly_confidence"`

	// Contributors are the signals that produced AnomalyScore, in the order
	// the engine recorded them.
	Contributors []ContributorRecord `json:"contributors,omitempty"`

	// TrustScore and RiskLevel are the per-event outcome. TrustScore is
	// evidence about one action; it is not an aggregate across a set of
	// them, and combining many is the consumer's job rather than something
	// this number already did.
	TrustScore float64 `json:"trust_score"`
	RiskLevel  string  `json:"risk_level"`

	// ContextRisk is the configured penalty for this kind of operation —
	// the one input Trustvian does not learn, which is why repetition never
	// erodes it.
	ContextRisk float64 `json:"context_risk"`

	// Decision is what policy concluded.
	Decision string `json:"decision"`

	// PolicyRule names the rule that matched, empty when none did.
	// MatchedDefault reports whether the policy's default applied instead.
	// PolicyReason is always present.
	PolicyRule     string `json:"policy_rule,omitempty"`
	PolicyReason   string `json:"policy_reason"`
	MatchedDefault bool   `json:"matched_default"`

	// Correlation and evidence carried from the event's context. All are
	// caller-supplied identifiers, and none participates in behavioral
	// identity — two events differing only in these fields share a
	// FingerprintID.
	//
	// TraceID and SpanID connect a decision back to the telemetry that
	// produced it. SessionID groups a bounded interaction. DelegatedFrom and
	// ApprovalStatus are behavioral evidence in their own right: delegation
	// familiarity is learned, and approval is enforceable by policy.
	TraceID        string               `json:"trace_id,omitempty"`
	SpanID         string               `json:"span_id,omitempty"`
	SessionID      string               `json:"session_id,omitempty"`
	DelegatedFrom  string               `json:"delegated_from,omitempty"`
	ApprovalStatus event.ApprovalStatus `json:"approval_status,omitempty"`
}

// ContributorRecord is one signal's contribution to an anomaly score.
//
// Value is the raw signal strength in [0,1] before Weight is applied, so a
// consumer can show both what fired and how much it counted. Detail is the
// engine's own explanation of why it fired, and is what makes a score
// explainable rather than merely reportable.
type ContributorRecord struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail,omitempty"`
}

// DecisionRecord projects r into its public, serializable form.
//
// It is a pure copy: no I/O, no clock, no identifier generation, no scoring,
// no store access, and no mutation of r or of any baseline. The same Result
// always projects to the same record.
//
// The returned record owns its data: r's contributors are copied, not shared,
// so mutating either side afterward leaves the other unchanged.
//
// Named DecisionRecord rather than Record because `result.Record()` reads as
// an instruction to record something, and this API is frozen at v1.0.
func (r Result) DecisionRecord() DecisionRecord {
	rec := DecisionRecord{
		EventID:   r.Event.ID,
		Timestamp: r.Event.Timestamp,

		ActorID:            r.Event.Actor.ID,
		ActorType:          r.Event.Actor.Type,
		IdentityConfidence: r.Event.Actor.IdentityConfidence,

		// From the baseline key rather than Event.Context: this is the scope
		// the engine read and will write, which is the one a consumer needs
		// to group records by.
		Environment: r.BaselineKey.Environment,

		Behavior:      publicStableFeatures(r.Fingerprint.Stable),
		FingerprintID: r.Fingerprint.ID,

		AnomalyScore:      r.Anomaly.Score,
		AnomalyConfidence: r.Anomaly.Confidence,

		TrustScore:  r.Trust.Score,
		RiskLevel:   string(r.Trust.Risk),
		ContextRisk: r.Trust.ContextRisk,

		Decision:       string(r.Decision),
		PolicyRule:     r.Explanation.RuleName,
		PolicyReason:   r.Explanation.Reason,
		MatchedDefault: r.Explanation.MatchedDefault,

		TraceID:        r.Event.Context.TraceID,
		SpanID:         r.Event.Context.SpanID,
		SessionID:      r.Event.Context.SessionID,
		DelegatedFrom:  r.Event.Context.DelegatedFrom,
		ApprovalStatus: r.Event.Context.ApprovalStatus,
	}

	// Copied, not shared: a record handed to a consumer must not alias a
	// slice the engine's caller still holds a Result for.
	if len(r.Anomaly.Contributors) > 0 {
		rec.Contributors = make([]ContributorRecord, len(r.Anomaly.Contributors))
		for i, c := range r.Anomaly.Contributors {
			rec.Contributors[i] = ContributorRecord{
				Name:   c.Name,
				Value:  c.Value,
				Weight: c.Weight,
				Detail: c.Detail,
			}
		}
	}

	return rec
}
