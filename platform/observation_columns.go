package platform

// The retained observation's column list, once — the same discipline
// evidence_columns.go applies to the aggregate, for the same reason.
//
// Thirty-four columns across two backends is six places a column can be added
// to five of. The order lives here and both backends derive their CREATE
// TABLE, INSERT, SELECT and row scan from it; only placeholder syntax and type
// spelling stay in each backend's file.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/trustvian/trustvian/event"
)

// observationSequenceDigits is the fixed width of a stored sequence.
//
// A sequence is a uint64 counter, so it is TEXT for the reason every counter
// in this schema is: both backends' 64-bit integer is *signed*, and a value
// above MaxInt64 would be corrupted silently. But unlike every other counter,
// this one is **ordered and range-scanned in SQL** — it is the page key — and
// plain decimal text orders "10" before "9".
//
// Zero-padding to the full width of MaxUint64 (20 digits) makes byte order and
// numeric order the same thing, on both backends, under COLLATE "C". The
// alternative is a signed BIGINT with a documented upper limit, which is the
// trap this schema already refuses everywhere else.
const observationSequenceDigits = 20

// observationSequenceKey renders a sequence as its sortable storage key.
func observationSequenceKey(sequence uint64) string {
	return fmt.Sprintf("%0*d", observationSequenceDigits, sequence)
}

// parseObservationSequenceKey reads one back, refusing anything this code would
// not have written.
//
// Deliberately not parseUint64Text: that function's canonical form is what
// FormatUint emits, and this column's canonical form is the zero-padded key.
// Using it here rejected every stored sequence, because "00000000000000000001"
// is exactly the non-canonical spelling it exists to refuse. Two encodings need
// two parsers rather than one loosened one.
func parseObservationSequenceKey(field, s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not a sequence key: %q",
			ErrStoreCorrupt, field, preview(s))
	}
	// Round-tripped rather than length-checked, so a wrong width, a sign and a
	// value that does not re-render are all one rule.
	if observationSequenceKey(v) != s {
		return 0, fmt.Errorf("%w: %s is not canonical: %q",
			ErrStoreCorrupt, field, preview(s))
	}
	if v == 0 {
		return 0, fmt.Errorf("%w: %s is zero; sequences start at 1", ErrStoreCorrupt, field)
	}
	return v, nil
}

// observationInsertColumns is the authoritative column order.
//
// run_id and sequence lead because together they are the primary key, and both
// backends treat the key separately from the payload.
func observationInsertColumns() []string {
	return []string{
		"run_id", "sequence",

		"event_id", "timestamp",

		"actor_id", "actor_type", "identity_confidence",

		// Behavioral identity as the engine recorded it. behavior_actor_type
		// is stored beside actor_type rather than derived from it: they are
		// two fields on two types, this schema records what it was given, and
		// a derivation is a recomputation that could be wrong exactly once.
		"fingerprint_id", "behavior_actor_type", "operation_category",
		"operation_name", "target_name", "target_category",
		"behavior_environment",

		"new_behavior",

		"anomaly_score", "anomaly_confidence", "trust_score", "context_risk",
		"risk_level",

		"decision", "policy_rule", "policy_reason", "matched_default",

		"trace_id", "span_id", "session_id", "delegated_from",
		"approval_status",

		// Task 084's four, appended last so every earlier column keeps its
		// position — both backends bind positionally against this order.
		"parent_span_id", "span_lineage", "duration_nanos",
		"duration_observed", "span_status",
	}
}

// observationInsertArgs renders one observation as bind arguments,
// positionally identical to observationInsertColumns.
func observationInsertArgs(runID EvaluationRunID, o Observation) []any {
	return []any{
		string(runID), observationSequenceKey(o.Sequence),

		o.EventID, timeText(o.Timestamp),

		o.ActorID, string(o.ActorType), o.IdentityConfidence,

		o.FingerprintID, string(o.Behavior.ActorType),
		string(o.Behavior.OperationCategory), o.Behavior.OperationName,
		o.Behavior.TargetName, string(o.Behavior.TargetCategory),
		o.Behavior.Environment,

		boolInt(o.NewBehavior),

		o.AnomalyScore, o.AnomalyConfidence, o.TrustScore, o.ContextRisk,
		o.RiskLevel,

		o.Decision, o.PolicyRule, o.PolicyReason, boolInt(o.MatchedDefault),

		o.TraceID, o.SpanID, o.SessionID, o.DelegatedFrom,
		string(o.ApprovalStatus),

		o.ParentSpanID, string(o.SpanLineage), uint64Text(o.DurationNanos),
		boolInt(o.DurationObserved), string(o.SpanStatus),
	}
}

// observationSelectList renders the column list a page read selects, run_id
// excluded because the caller already knows it and passes it as the predicate.
func observationSelectList() string {
	return strings.Join(observationInsertColumns()[1:], ", ")
}

// observationScanTargets returns scan destinations for observationSelectList,
// together with the decoded observation they fill.
//
// The intermediate strings exist because a sequence, a timestamp and three
// booleans are all stored as text or integers this code must validate rather
// than trust — ErrStoreCorrupt is raised by scanObservation, not by the
// driver.
type observationScan struct {
	sequence         string
	timestamp        string
	actorType        string
	behaviorActor    string
	operationCat     string
	targetCat        string
	newBehavior      int
	matchedDefault   int
	approvalStatus   string
	spanLineage      string
	durationNanos    string
	durationObserved int
	spanStatus       string

	out Observation
}

func (s *observationScan) targets() []any {
	return []any{
		&s.sequence,

		&s.out.EventID, &s.timestamp,

		&s.out.ActorID, &s.actorType, &s.out.IdentityConfidence,

		&s.out.FingerprintID, &s.behaviorActor, &s.operationCat,
		&s.out.Behavior.OperationName, &s.out.Behavior.TargetName, &s.targetCat,
		&s.out.Behavior.Environment,

		&s.newBehavior,

		&s.out.AnomalyScore, &s.out.AnomalyConfidence, &s.out.TrustScore,
		&s.out.ContextRisk, &s.out.RiskLevel,

		&s.out.Decision, &s.out.PolicyRule, &s.out.PolicyReason,
		&s.matchedDefault,

		&s.out.TraceID, &s.out.SpanID, &s.out.SessionID, &s.out.DelegatedFrom,
		&s.approvalStatus,

		&s.out.ParentSpanID, &s.spanLineage, &s.durationNanos,
		&s.durationObserved, &s.spanStatus,
	}
}

// observation decodes what was scanned, refusing anything this code would not
// have written.
//
// Every text field that maps onto a closed set is validated rather than cast:
// a span status of "OK" or a lineage of "parent" is corruption, and returning
// it as a typed value would let it reach a consumer looking exactly like a
// value the engine produced.
func (s *observationScan) observation() (Observation, error) {
	sequence, err := parseObservationSequenceKey("observation sequence", s.sequence)
	if err != nil {
		return Observation{}, err
	}
	timestamp, err := parseTimeText("observation timestamp", s.timestamp)
	if err != nil {
		return Observation{}, err
	}
	duration, err := parseUint64Text("observation duration", s.durationNanos)
	if err != nil {
		return Observation{}, err
	}
	if duration > event.MaxDurationNanos {
		return Observation{}, fmt.Errorf(
			"%w: observation duration %d exceeds the maximum a span can express",
			ErrStoreCorrupt, duration)
	}

	// Span status is checked on the way back in; span lineage deliberately is
	// not, and the asymmetry is the ingest path's rather than this one's.
	// AddRecord refuses an unrecognized status, so a stored one outside the
	// vocabulary was not written by this code and is corruption. **Nothing
	// validates lineage at ingest**, so a record carrying an unrecognized value
	// is accepted today — refusing it here would make that run's history
	// unreadable, and rewriting it to "" would silently discard the lineage
	// this task exists to preserve. It is returned exactly as recorded.
	//
	// StatusUnavailable and LineageUnspecified are both the empty string and
	// both legitimate here. SpanStatus.Valid and SpanLineage.Valid deliberately
	// refuse it — they answer "may a producer send this over a wire", which is a
	// different question from "may storage hold it". A run of entirely
	// unstatused spans is the ordinary case.
	lineage := event.SpanLineage(s.spanLineage)
	status := event.SpanStatus(s.spanStatus)
	if status != event.StatusUnavailable && !status.Valid() {
		return Observation{}, fmt.Errorf("%w: observation span status %q",
			ErrStoreCorrupt, preview(s.spanStatus))
	}

	// An unobserved duration must be stored as zero. A non-zero nanosecond
	// count beside "not observed" is two statements that cannot both be true,
	// and silently preferring one would decide which by accident.
	observed := s.durationObserved != 0
	if !observed && duration != 0 {
		return Observation{}, fmt.Errorf(
			"%w: observation records duration %d with no observation flag",
			ErrStoreCorrupt, duration)
	}

	out := s.out
	out.Sequence = sequence
	out.Timestamp = timestamp
	out.ActorType = event.ActorType(s.actorType)
	out.Behavior.ActorType = event.ActorType(s.behaviorActor)
	out.Behavior.OperationCategory = event.OperationCategory(s.operationCat)
	out.Behavior.TargetCategory = event.TargetCategory(s.targetCat)
	out.NewBehavior = s.newBehavior != 0
	out.MatchedDefault = s.matchedDefault != 0
	out.ApprovalStatus = event.ApprovalStatus(s.approvalStatus)
	out.SpanLineage = lineage
	out.DurationNanos = duration
	out.DurationObserved = observed
	out.SpanStatus = status
	return out, nil
}

// observationColumnDDL renders the column definitions, so a fresh schema and a
// migrated one cannot disagree about them.
//
// textType carries each backend's byte-ordering requirement: PostgreSQL needs
// an explicit C collation on the columns this schema compares and orders, and
// the sequence key is one of them.
func observationColumnDDL(textType, realType string) []string {
	text := func(name string) string { return name + " " + textType + " NOT NULL" }
	real := func(name string) string { return name + " " + realType + " NOT NULL" }
	flag := func(name string) string { return name + " INTEGER NOT NULL" }

	out := make([]string, 0, len(observationInsertColumns()))
	for _, name := range observationInsertColumns() {
		switch name {
		case "identity_confidence", "anomaly_score", "anomaly_confidence",
			"trust_score", "context_risk":
			out = append(out, real(name))
		case "new_behavior", "matched_default", "duration_observed":
			out = append(out, flag(name))
		default:
			out = append(out, text(name))
		}
	}
	return out
}

// Compile-time proof that the column list, the bind arguments and the scan
// targets stay the same length. Three lists that must agree, checked where
// they are defined rather than in whichever test happens to run first.
var _ = func() struct{} {
	columns := len(observationInsertColumns())
	args := len(observationInsertArgs("", Observation{}))
	targets := len((&observationScan{}).targets())
	if columns != args || columns != targets+1 {
		panic(fmt.Sprintf(
			"platform: observation column contract disagrees: %d columns, %d args, %d scan targets",
			columns, args, targets))
	}
	return struct{}{}
}()
