package platform

// Per-observation history: the durable row task 067 adds, and the bound that
// keeps it from becoming an archive.
//
// The platform retained two reductions per run — a fixed-shape aggregate and a
// capped behavior snapshot — and neither can say *which* observation scored
// what, when it happened relative to its neighbours, or what trace it belonged
// to. Realtime carried that and dropped it when the connection closed
// (ADR 0032). This is the row that outlives the connection.
//
// It reads nothing back into the engine. A retained observation influences no
// decision, no baseline and no fingerprint: history is evidence, not input.
//
// See docs/tasks/v1.0/067-event-history-capability-boundary.md.

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

var (
	// ErrInvalidObservation reports an observation that cannot be retained:
	// a zero sequence, an unusable identifier, or a field the record itself
	// would have been refused for.
	ErrInvalidObservation = errors.New("platform: invalid retained observation")

	// ErrObservationCursor reports a page cursor that was not produced by
	// this contract. Distinct from ErrInvalidID because the cursor here is a
	// sequence rather than an identifier, and "not canonical decimal" is a
	// different diagnostic from "not a valid id".
	ErrObservationCursor = errors.New("platform: invalid observation page cursor")
)

// MaxRetainedObservations bounds one run's retained history.
//
// The same rule the behavior collector applies at 512 distinct behaviors, for
// the same reason: saturation is degraded evidence, not a failed ingest.
// Reaching it stops retention and sets ObservationHistoryPartial; the
// aggregate, the snapshot and the cursor keep advancing, because refusing the
// record would discard sound decision evidence because *historical* evidence
// filled up.
//
// 4096 is chosen against the volume task 082 states — one run of a chatty
// agent produces thousands of observations — and bounds a run's history at
// roughly a megabyte of fixed-shape rows. A constant rather than a knob, like
// 512 and MaxListPage: a per-deployment retention setting is a decision task
// 068 and task 071 should make with measured volume in hand.
const MaxRetainedObservations = 4096

// Observation is one retained DecisionRecord, projected onto the field set
// task 076 named as its requirement plus task 084's four.
//
// An allowlist expressed as a struct, and then as columns. There is no
// attribute map, no span-event list, no link list and no payload field — not
// as an omission but as the boundary: a field that cannot be declared here
// cannot be written, which is a stronger guarantee than a filter applied on
// the way past.
//
// Contributors is deliberately absent. It is DecisionRecord's one
// variable-length field, it grows with the number of signals that fired, and
// retaining it would make a row's size a function of producer behaviour.
// Fixed shape is what keeps the storage profile predictable.
type Observation struct {
	// Sequence is the ingest sequence this record was admitted under, and
	// half of the observation's identity. Dense, monotonic per run, and
	// assigned by the transaction that admits the record — so it cannot
	// collide and does not depend on any producer behaving well.
	Sequence uint64

	// EventID and Timestamp are the producer's own identity and clock. Both
	// are retained and **neither is an ordering key**: EventID is
	// caller-supplied and unconstrained, and equal timestamps are ordinary.
	EventID   string
	Timestamp time.Time

	ActorID            string
	ActorType          event.ActorType
	IdentityConfidence float64

	// FingerprintID and Behavior are behavioral identity as the engine
	// recorded it. Retained, never recomputed.
	FingerprintID string
	Behavior      trustvian.StableFeatures

	// NewBehavior is the one value not carried on the record: whether this
	// fingerprint was new to the run when it arrived. The ingest path already
	// derives it from the trusted snapshot before folding, and publishes it to
	// realtime. It is recorded rather than re-derived because after the fold
	// it can no longer be answered.
	NewBehavior bool

	AnomalyScore      float64
	AnomalyConfidence float64
	TrustScore        float64
	ContextRisk       float64
	RiskLevel         string

	Decision       string
	PolicyRule     string
	PolicyReason   string
	MatchedDefault bool

	TraceID        string
	SpanID         string
	SessionID      string
	DelegatedFrom  string
	ApprovalStatus event.ApprovalStatus

	// ParentSpanID is trace-scoped and never a standalone key — task 084
	// states that nothing indexes it and that this task must not either.
	ParentSpanID string
	SpanLineage  event.SpanLineage

	// DurationNanos is meaningful only when DurationObserved. A measured zero
	// and an unavailable duration are different facts and stay different
	// through storage: a span with no end timestamp did not take no time.
	DurationNanos    uint64
	DurationObserved bool

	SpanStatus event.SpanStatus
}

// ObservationFromRecord projects an accepted record onto a retainable row.
//
// Called only on the ingest path, after the record has already satisfied the
// aggregate and the collector, so this validates identity and the fields those
// two do not — it is a last guard against writing a row the read path could
// not return, not the primary validation point.
func ObservationFromRecord(
	sequence uint64, record trustvian.DecisionRecord, newBehavior bool,
) (Observation, error) {
	if sequence == 0 {
		return Observation{}, fmt.Errorf(
			"%w: sequences start at 1", ErrInvalidObservation)
	}

	// Exactly what the ingest path already enforces, and deliberately not one
	// rule more. A record the aggregate and the collector accept must be
	// retainable: a stricter check here would turn task 067 into a change in
	// which records are *ingestable*, which is task 084's surface and not
	// this one's. The engine's own validation is upstream of both.
	if record.EventID == "" {
		return Observation{}, fmt.Errorf("%w: event id is empty", ErrInvalidObservation)
	}
	if record.Timestamp.IsZero() {
		return Observation{}, fmt.Errorf(
			"%w: observation %s has no timestamp",
			ErrInvalidObservation, preview(record.EventID))
	}

	// The same reading the aggregate takes, so the two cannot disagree about
	// whether a duration was measured. A malformed value is refused here for
	// the reason DecisionRecord.DurationNanosValue documents: downgrading it
	// to "unavailable" would let a submitter erase its own evidence by
	// corrupting it. AddRecord has already refused it by the time this runs —
	// this is the guard that keeps that true if the order ever changes.
	duration, err := record.ValidateDurationNanos()
	if err != nil {
		return Observation{}, fmt.Errorf("%w: observation %s: %s",
			ErrInvalidObservation, preview(record.EventID), err)
	}

	return Observation{
		Sequence:           sequence,
		EventID:            record.EventID,
		Timestamp:          record.Timestamp.UTC(),
		ActorID:            record.ActorID,
		ActorType:          record.ActorType,
		IdentityConfidence: record.IdentityConfidence,
		FingerprintID:      record.FingerprintID,
		Behavior:           record.Behavior,
		NewBehavior:        newBehavior,
		AnomalyScore:       record.AnomalyScore,
		AnomalyConfidence:  record.AnomalyConfidence,
		TrustScore:         record.TrustScore,
		ContextRisk:        record.ContextRisk,
		RiskLevel:          record.RiskLevel,
		Decision:           record.Decision,
		PolicyRule:         record.PolicyRule,
		PolicyReason:       record.PolicyReason,
		MatchedDefault:     record.MatchedDefault,
		TraceID:            record.TraceID,
		SpanID:             record.SpanID,
		SessionID:          record.SessionID,
		DelegatedFrom:      record.DelegatedFrom,
		ApprovalStatus:     record.ApprovalStatus,
		ParentSpanID:       record.ParentSpanID,
		SpanLineage:        record.SpanLineage,
		DurationNanos:      duration,
		DurationObserved:   record.DurationNanos != "",
		SpanStatus:         record.SpanStatus,
	}, nil
}

// ---------------------------------------------------------------------
// History state
// ---------------------------------------------------------------------

// ObservationHistoryState describes what a run's retained history *is*, and it
// has three values because two would force a lie.
//
// A schema-6 database's runs have aggregates and behavior snapshots and no
// observations. Reporting "complete, zero rows" for one would be a fabricated
// historical fact — the same error task 066 refused when it declined to
// synthesize promotions from old evaluations.
type ObservationHistoryState uint8

const (
	// ObservationHistoryUnavailable means this run has records and none of
	// their history was ever retained: it was ingested before schema 7.
	// Not an error, and not an empty history — an absence with a known cause.
	ObservationHistoryUnavailable ObservationHistoryState = iota

	// ObservationHistoryComplete means every accepted record is retained.
	ObservationHistoryComplete

	// ObservationHistoryPartial means some of this run's records are retained
	// and some are not. Two causes produce it, and both are the same fact to
	// a reader — *do not treat this history as the whole run*:
	//
	//   - the run reached MaxRetainedObservations and retention stopped;
	//   - the run began ingesting before schema 7 and resumed after it, so
	//     its earliest records were never retained.
	//
	// They are not separate states because no consumer can act on the
	// difference, and a state nobody can act on is a state somebody will
	// eventually read as "complete enough". The retained count is exposed for
	// a caller that wants to say how much is here.
	ObservationHistoryPartial
)

// String renders the state for a wire payload and a diagnostic.
func (s ObservationHistoryState) String() string {
	switch s {
	case ObservationHistoryComplete:
		return "complete"
	case ObservationHistoryPartial:
		return "partial"
	case ObservationHistoryUnavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// ObservationHistory is what a run's history is, and how much of it there is.
//
// Unexported fields and accessors, like EvaluationIngestState and
// BehaviorSnapshot: a zero value is the unavailable state, which is the safe
// default for a type whose whole purpose is to avoid overclaiming.
type ObservationHistory struct {
	state    ObservationHistoryState
	retained uint64
}

// State is what this run's history is.
func (h ObservationHistory) State() ObservationHistoryState { return h.state }

// RetainedCount is how many observations are durably retained. Always 0 when
// the history is unavailable.
func (h ObservationHistory) RetainedCount() uint64 { return h.retained }

// Available reports whether any history was retained for this run.
func (h ObservationHistory) Available() bool { return h.state != ObservationHistoryUnavailable }

// Complete reports whether the retained history describes the whole run.
//
// False for a partial history *and* for an unavailable one — a caller that
// only asks this question is told "no" in both cases, which is the answer that
// cannot mislead. A caller that must tell them apart reads State.
func (h ObservationHistory) Complete() bool { return h.state == ObservationHistoryComplete }

// NewObservationHistory builds a history value from durable state.
//
// recordCount resolves the one ambiguity a missing history row carries: a run
// that ingested nothing has no history to be missing, so it is trivially
// complete, while a run with records and no row predates retention.
func NewObservationHistory(
	present bool, retained uint64, complete bool, recordCount uint64,
) ObservationHistory {
	if !present {
		if recordCount == 0 {
			return ObservationHistory{state: ObservationHistoryComplete}
		}
		return ObservationHistory{state: ObservationHistoryUnavailable}
	}
	state := ObservationHistoryPartial
	if complete {
		state = ObservationHistoryComplete
	}
	return ObservationHistory{state: state, retained: retained}
}

// ---------------------------------------------------------------------
// Paging
// ---------------------------------------------------------------------

// ObservationPage is one bounded page of a run's retained history, together
// with what that history is.
//
// The history travels with the page for the reason the ingest counts travel
// with the cursor: read separately, a caller could show a full page beside a
// state that had meanwhile changed, and describe something that never existed.
type ObservationPage struct {
	Observations []Observation
	History      ObservationHistory

	// Matched reports whether **any** retained observation satisfies the read's
	// filter, independent of the page cursor.
	//
	// It exists because an empty page has two unrelated causes that look
	// identical: nothing matches, or the caller has paged past the last match.
	// A reader that inferred "no evidence" from a short page would report the
	// second as the first — and would do so precisely when a developer had just
	// finished reading all the evidence there is.
	//
	// Read in the same transaction as the rows and the history above, so the
	// three describe one instant.
	Matched bool
}

// FormatObservationCursor renders a page cursor.
//
// The cursor *is* the sequence, in the same canonical decimal text the ingest
// protocol uses on the wire, and for the same reason: uint64 exceeds what a
// JSON double represents exactly.
func FormatObservationCursor(sequence uint64) string { return strconv.FormatUint(sequence, 10) }

// ParseObservationCursor decodes a page cursor, refusing anything this
// contract would not have produced.
//
// An empty cursor is valid and starts at the beginning; it is the caller's
// job to distinguish that, which is why this returns the sentinel 0 for it
// rather than an error.
func ParseObservationCursor(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || strconv.FormatUint(v, 10) != s {
		return 0, fmt.Errorf("%w: %q is not a canonical uint64 sequence",
			ErrObservationCursor, preview(s))
	}
	if v == 0 {
		return 0, fmt.Errorf("%w: sequences start at 1", ErrObservationCursor)
	}
	return v, nil
}

// validateObservationPage applies the page rules this collection shares with
// every other, plus the cursor rule that is its own.
func validateObservationPage(after string, limit int) error {
	if limit < 1 || limit > MaxListPage {
		return fmt.Errorf("%w: observation page limit %d is outside 1..%d",
			ErrInvalidID, limit, MaxListPage)
	}
	_, err := ParseObservationCursor(after)
	return err
}
