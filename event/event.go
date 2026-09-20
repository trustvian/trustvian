// Package event defines Trustvian's Event domain model: the single,
// generic shape that every stage of the pipeline (features, fingerprint,
// baseline, anomaly, trust, policy) consumes as input.
//
// Event and its nested types are treated as immutable value types: callers
// construct a new value rather than mutating fields of an existing one, and
// nothing in this package hands back a pointer that aliases internal state.
//
// JSON struct tags are provided so an Event can be read from a file (see
// cmd/trustvian) or any other JSON source; this is a serialization
// convenience only and does not make JSON Trustvian's canonical wire
// format.
package event

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Sentinel errors returned by Validate, usable with errors.Is.
var (
	ErrMissingID                 = errors.New("event: missing id")
	ErrMissingTimestamp          = errors.New("event: missing timestamp")
	ErrMissingActorID            = errors.New("event: missing actor id")
	ErrInvalidActorType          = errors.New("event: invalid actor type")
	ErrInvalidIdentityConfidence = errors.New("event: identity confidence out of range [0,1]")
	ErrMissingOperationName      = errors.New("event: missing operation name")
	ErrInvalidOperationCategory  = errors.New("event: invalid operation category")
	ErrInvalidOperationDirection = errors.New("event: invalid operation direction")
	// ErrInvalidTimestamp rejects a timestamp Go's standard-library JSON
	// encoder cannot represent. See Event.Validate.
	ErrInvalidTimestamp = errors.New("event: timestamp cannot be represented as RFC 3339")
)

// ActorType identifies the kind of thing that performed an Operation.
type ActorType string

const (
	ActorTypeService        ActorType = "service"
	ActorTypeUser           ActorType = "user"
	ActorTypeServiceAccount ActorType = "service_account"
	ActorTypeAIAgent        ActorType = "ai_agent"
	ActorTypeDevice         ActorType = "device"
	ActorTypeUnknown        ActorType = "unknown"
)

func (t ActorType) valid() bool {
	switch t {
	case ActorTypeService, ActorTypeUser, ActorTypeServiceAccount, ActorTypeAIAgent, ActorTypeDevice, ActorTypeUnknown:
		return true
	default:
		return false
	}
}

// Actor is who or what performed the Operation.
//
// IdentityConfidence reflects how much upstream authentication/telemetry
// vouches for Actor.ID; it is an input Trustvian trusts, not something
// Trustvian itself computes.
type Actor struct {
	ID                 string    `json:"id"`
	Type               ActorType `json:"type"`
	IdentityConfidence float64   `json:"identity_confidence"`
}

func (a Actor) validate() error {
	if a.ID == "" {
		return ErrMissingActorID
	}
	if !a.Type.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidActorType, a.Type)
	}
	if math.IsNaN(a.IdentityConfidence) || a.IdentityConfidence < 0 || a.IdentityConfidence > 1 {
		return fmt.Errorf("%w: %v", ErrInvalidIdentityConfidence, a.IdentityConfidence)
	}
	return nil
}

// OperationCategory classifies what kind of action an Operation represents.
type OperationCategory string

const (
	OperationCategoryHTTP     OperationCategory = "http"
	OperationCategoryDB       OperationCategory = "db"
	OperationCategoryRPC      OperationCategory = "rpc"
	OperationCategoryTool     OperationCategory = "tool"
	OperationCategoryExternal OperationCategory = "external"
)

func (c OperationCategory) valid() bool {
	switch c {
	case OperationCategoryHTTP, OperationCategoryDB, OperationCategoryRPC, OperationCategoryTool, OperationCategoryExternal:
		return true
	default:
		return false
	}
}

// OperationDirection indicates whether the Operation was initiated by the
// Actor (outbound) or received by it (inbound). It is optional; the zero
// value means unspecified.
type OperationDirection string

const (
	DirectionUnspecified OperationDirection = ""
	DirectionInbound     OperationDirection = "inbound"
	DirectionOutbound    OperationDirection = "outbound"
)

func (d OperationDirection) valid() bool {
	switch d {
	case DirectionUnspecified, DirectionInbound, DirectionOutbound:
		return true
	default:
		return false
	}
}

// TargetCategory classifies what kind of destination a Target is. It is
// optional; the zero value means "unclassified" — a producer may set it or
// leave it unset, matching how Direction is already optional today.
type TargetCategory string

const (
	TargetCategoryUnspecified TargetCategory = ""
	TargetCategoryInternal    TargetCategory = "internal"
	TargetCategoryExternal    TargetCategory = "external"
	TargetCategoryDatabase    TargetCategory = "database"
)

func (c TargetCategory) valid() bool {
	switch c {
	case TargetCategoryUnspecified, TargetCategoryInternal, TargetCategoryExternal, TargetCategoryDatabase:
		return true
	default:
		return false
	}
}

// Operation is what the Actor did: e.g. an HTTP route, a DB query, an RPC
// call, an AI-agent tool invocation, or a call to an external destination.
type Operation struct {
	Category  OperationCategory  `json:"category"`
	Name      string             `json:"name"`
	Direction OperationDirection `json:"direction,omitempty"`
}

func (o Operation) validate() error {
	if !o.Category.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidOperationCategory, o.Category)
	}
	if o.Name == "" {
		return ErrMissingOperationName
	}
	if !o.Direction.valid() {
		return fmt.Errorf("%w: %q", ErrInvalidOperationDirection, o.Direction)
	}
	return nil
}

// Target is the destination of the Operation: a service name, a database,
// or an external host. It is optional — not every Operation has a distinct
// destination. Category, when set, classifies what kind of destination
// this is (see TargetCategory); it is not required for Validate to pass.
type Target struct {
	Name     string         `json:"name"`
	Category TargetCategory `json:"category,omitzero"`
}

// ApprovalStatus records whether an Operation required, and received,
// human approval — a per-event operational fact, not a workflow-engine
// state (Trustvian records this value; it does not manage an approval
// process). The zero value, ApprovalUnspecified, means "not recorded" —
// the identical "typed but optional, no default assumed" precedent
// OperationDirection already established: neither is read by
// features.Extract today, and both are reserved for a later signal to
// consume once one has a concrete design, not because this field is
// speculative — see docs/tasks/014-ai-agent.md.
//
// The five values are one linear state, not two orthogonal booleans:
// ApprovalRequired means a requirement was flagged with no decision
// recorded yet — functionally "pending" — while ApprovalApproved/
// ApprovalDenied mean a decision was recorded. This is untrusted,
// self-reported input: a producer's own claim of ApprovalApproved is
// not verified against any authorization system by this package, and
// must not be treated as automatically trustworthy by a future
// consumer absent a trusted upstream source — see ADR 0014's "Trust
// boundary" section.
type ApprovalStatus string

const (
	ApprovalUnspecified ApprovalStatus = ""
	ApprovalNotRequired ApprovalStatus = "not_required"
	ApprovalRequired    ApprovalStatus = "required"
	ApprovalApproved    ApprovalStatus = "approved"
	ApprovalDenied      ApprovalStatus = "denied"
)

// Context carries correlation and scoping data for an Event: the
// deployment environment, OpenTelemetry trace/span identifiers when
// available, and — for actors that operate across multiple related
// events, most notably AI agents — session, delegation, and approval
// context.
//
// Every field here is correlation/context data, never behavioral
// identity: none of SessionID, DelegatedFrom, or ApprovalStatus is read
// by features.Extract into StableFeatures, for the identical reason
// TraceID/SpanID already aren't (see
// internal/fingerprint/fingerprint_test.go's
// TestFingerprintIDIndependentOfEventIdentifiers, extended by this
// task to cover these three fields too). This is deliberate, not an
// oversight: a SessionID is typically unique per conversation/session,
// and folding it into a Fingerprint would make every session-scoped
// actor generate a fresh, never-reused Fingerprint (and, if it entered
// baseline.Key, a fresh Baseline) — defeating the entire point of
// behavioral profiling across sessions. See
// docs/adr/0014-ai-agents-as-first-class-behavioral-actors.md.
type Context struct {
	Environment string `json:"environment,omitempty"`
	TraceID     string `json:"trace_id,omitempty"`
	SpanID      string `json:"span_id,omitempty"`

	// SessionID groups events belonging to one bounded
	// interaction/session — e.g. one AI-agent conversation. Optional;
	// most non-agent workloads leave it unset. See this type's own doc
	// comment for why it never affects Fingerprint identity.
	SessionID string `json:"session_id,omitempty"`

	// DelegatedFrom is the Actor.ID of the actor that delegated this
	// operation, for a single agent-to-agent delegation hop (e.g. Agent
	// A asks Agent B to perform an operation: B's own Event carries
	// Actor.ID = B, DelegatedFrom = A's Actor.ID). A single hop is
	// sufficient scope — see docs/tasks/014-ai-agent.md's own Non-Goals
	// for why a full delegation graph is not built here. Optional; ""
	// means this event was not the result of delegation.
	DelegatedFrom string `json:"delegated_from,omitempty"`

	// ApprovalStatus records this specific event's human-approval
	// state. See ApprovalStatus's own doc comment.
	ApprovalStatus ApprovalStatus `json:"approval_status,omitempty"`
}

// Event is an immutable, atomic observed action: the input to every stage
// of the Trustvian pipeline.
type Event struct {
	ID         string         `json:"id"`
	Timestamp  time.Time      `json:"timestamp"`
	Actor      Actor          `json:"actor"`
	Operation  Operation      `json:"operation"`
	Target     Target         `json:"target,omitzero"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Context    Context        `json:"context,omitzero"`
}

// validateTimestamp rejects the timestamps time.Time.MarshalJSON refuses.
//
// Not every constructible time.Time is representable in RFC 3339, and the
// standard library reports that as a marshalling error rather than encoding
// something lossy. Two cases exist, both verified against the standard
// library rather than inferred:
//
//   - a year outside [0,9999], because RFC 3339 writes exactly four digits;
//   - a zone offset of 24 hours or more in either direction, because the
//     offset's hour field is two digits and must stay in [0,23].
//
// Catching them here rather than at the serialization boundary is the point.
// A timestamp that cannot cross Trustvian's public JSON boundary is an
// invalid input, and an input error belongs at the input — not in
// persistence code discovering it hours later with no way to reject the
// event that caused it.
//
// The checks are direct field reads: no formatting, no marshalling, no
// allocation. This runs on every Analyze.
func validateTimestamp(t time.Time) error {
	if y := t.Year(); y < 0 || y > 9999 {
		return fmt.Errorf("%w: year %d is outside [0,9999]", ErrInvalidTimestamp, y)
	}
	// t.Year() is the year in t's own location, which is the year RFC 3339
	// writes — so the zone check is independent of the year check above.
	_, offset := t.Zone()
	if offset <= -24*60*60 || offset >= 24*60*60 {
		return fmt.Errorf("%w: zone offset %ds is outside ±24h", ErrInvalidTimestamp, offset)
	}
	return nil
}

// Validate reports whether e has all fields required for pipeline
// processing. Target, Attributes, and Context are optional and are not
// checked.
//
// A missing Timestamp returns ErrMissingTimestamp; a Timestamp that Go's
// standard-library JSON encoder would refuse returns ErrInvalidTimestamp.
// The second check exists because a Result's public projection carries the
// timestamp verbatim, so a value that cannot be encoded would otherwise
// only surface as a marshalling failure downstream of a successful analysis.
func (e Event) Validate() error {
	if e.ID == "" {
		return ErrMissingID
	}
	if e.Timestamp.IsZero() {
		return ErrMissingTimestamp
	}
	if err := validateTimestamp(e.Timestamp); err != nil {
		return err
	}
	if err := e.Actor.validate(); err != nil {
		return err
	}
	if err := e.Operation.validate(); err != nil {
		return err
	}
	return nil
}
