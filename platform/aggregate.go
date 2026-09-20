package platform

// Evaluation result aggregation: the first platform code that consumes
// behavioral evidence from the core.
//
//	Engine ──▶ DecisionRecord ──▶ EvaluationAggregate
//
// The input is trustvian.DecisionRecord and nothing else. Result cannot cross
// this boundary — its stage fields have types from the core's internal
// packages, so a platform could read them but never declare one — which is
// precisely what task 050 built the record for. Task 053 is the first
// occasion to prove that claim rather than assert it: the platform began
// consuming engine evidence without a single core change.
//
// See docs/adr/0026-evaluation-aggregation-is-bounded-evidence.md.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// Sentinel errors, wrapped with fmt.Errorf and matched with errors.Is,
// following the convention the core and the rest of this package use.
//
// Four categories, because those are the four different things a caller can
// do about a failure: fix the record, route it to the aggregate whose
// environment it belongs to, construct the aggregate properly in the first
// place, or stop because the counter is exhausted. Still one per category
// rather than one per field — a caller branches on "the record was
// unusable", never on which of nine fields it was, and the wrapped message
// names the field.
var (
	// ErrInvalidDecisionRecord reports a record whose consumed fields are
	// missing, unrecognized, or out of range.
	ErrInvalidDecisionRecord = errors.New("platform: invalid decision record")

	// ErrEnvironmentMismatch reports a record belonging to a different
	// environment than the evaluation being aggregated.
	ErrEnvironmentMismatch = errors.New("platform: decision record environment does not match the evaluation")

	// ErrAggregateOverflow reports that another record would wrap the
	// observation counter.
	ErrAggregateOverflow = errors.New("platform: evaluation aggregate counter overflow")

	// ErrUnboundAggregate reports an aggregate that did not come from
	// NewEvaluationAggregate and is therefore bound to no evaluation run.
	//
	// Its own sentinel rather than reusing ErrInvalidID because the fault is
	// in the *receiver*, not the record: a caller told that an identifier was
	// invalid would go looking at the record they just passed, and find
	// nothing wrong with it.
	ErrUnboundAggregate = errors.New("platform: evaluation aggregate is not bound to an evaluation run")
)

// Decision values a DecisionRecord may carry.
//
// Re-declared rather than imported because the core keeps policy.Decision
// under internal/ and DecisionRecord deliberately carries it as a string.
// Restating six known constants is the price of the boundary; widening the
// core's public API to avoid restating them would be the expensive mistake.
//
// The rule is not "re-declare everything" — where the core exposes a public
// type, this file uses it. event.ApprovalStatus below is imported for exactly
// that reason.
const (
	decisionAllow           = "allow"
	decisionObserveOnly     = "observe_only"
	decisionAlert           = "alert"
	decisionChallenge       = "challenge"
	decisionRequireApproval = "require_approval"
	decisionBlock           = "block"
)

// Risk levels a DecisionRecord may carry. Same reasoning as the decisions
// above: trust.RiskLevel is internal.
const (
	riskLow      = "low"
	riskMedium   = "medium"
	riskHigh     = "high"
	riskCritical = "critical"
)

// DecisionCounts is how many observations reached each decision.
//
// A fixed struct, never map[string]uint64. The six categories are closed and
// known from the public contract, and a map would silently accept a seventh —
// which is the failure this type exists to make impossible. An unrecognized
// decision is a rejected record, not a new bucket.
type DecisionCounts struct {
	Allow           uint64
	ObserveOnly     uint64
	Alert           uint64
	Challenge       uint64
	RequireApproval uint64
	Block           uint64
}

// Total returns the number of counted decisions.
func (c DecisionCounts) Total() uint64 {
	return c.Allow + c.ObserveOnly + c.Alert + c.Challenge + c.RequireApproval + c.Block
}

// RiskCounts is how many observations landed in each risk level.
type RiskCounts struct {
	Low      uint64
	Medium   uint64
	High     uint64
	Critical uint64
}

// Total returns the number of counted risk levels.
func (c RiskCounts) Total() uint64 {
	return c.Low + c.Medium + c.High + c.Critical
}

// ApprovalCounts is how many observations carried each approval status.
//
// These are **evidence counts, not compliance**. event.Context.ApprovalStatus
// is supplied by whoever produced the event: it records what a producer said
// about approval, and is not proof that an approval happened. Counting a
// Denied is not the same as finding a violation, and this type deliberately
// offers no field that would read like one. Interpreting approval evidence
// needs policy context, and belongs to the scorecard and gate tasks.
type ApprovalCounts struct {
	// Unspecified counts event.ApprovalUnspecified — the zero value, and a
	// valid state meaning the producer said nothing about approval. It is
	// not malformed input.
	Unspecified uint64
	NotRequired uint64
	Required    uint64
	Approved    uint64
	Denied      uint64
}

// Total returns the number of counted approval statuses.
func (c ApprovalCounts) Total() uint64 {
	return c.Unspecified + c.NotRequired + c.Required + c.Approved + c.Denied
}

// PolicySelection is how decisions were reached: by a matching rule, or by the
// policy's default.
//
// Shape only. A rule *name* is an identifier, not severity metadata — a rule
// called "block-prod-shell" proves nothing about criticality, and nothing here
// parses one. There is deliberately no per-rule map: it would be keyed by a
// caller-controlled value, which is unbounded.
type PolicySelection struct {
	MatchedRule    uint64
	MatchedDefault uint64
}

// Total returns the number of counted decisions.
func (s PolicySelection) Total() uint64 { return s.MatchedRule + s.MatchedDefault }

// MetricSummary is the descriptive statistics of one numeric signal across an
// evaluation: how many values, their sum, and the extremes.
//
// No sample slice — that would make the aggregate grow with the stream. Sum
// and Count are enough for a mean, and Min/Max need no history.
//
// Exported fields are safe here because a summary is returned by value:
// mutating a snapshot cannot reach the aggregate it came from.
type MetricSummary struct {
	Count uint64
	Sum   float64
	Min   float64
	Max   float64
}

// Mean returns the arithmetic mean, and false when no values were observed.
//
// The bool matters. An empty mean is *absent*, not zero: returning 0 would
// make "nothing was observed" indistinguishable from "everything scored
// zero", and those lead to opposite conclusions. NaN would be worse — it
// propagates silently through whatever computes with it.
func (s MetricSummary) Mean() (float64, bool) {
	if s.Count == 0 {
		return 0, false
	}
	return s.Sum / float64(s.Count), true
}

// observe folds one value in. The receiver is a value; the caller assigns the
// result.
func (s MetricSummary) observe(v float64) MetricSummary {
	if s.Count == 0 {
		return MetricSummary{Count: 1, Sum: v, Min: v, Max: v}
	}
	s.Count++
	s.Sum += v
	s.Min = math.Min(s.Min, v)
	s.Max = math.Max(s.Max, v)
	return s
}

// EvaluationAggregate is a bounded, fixed-shape summary of what one
// evaluation observed.
//
// It answers factual questions: how many observations, how they were decided,
// at what risk, with what approval evidence, over what time range, and the
// descriptive statistics of the five numeric signals every record carries.
//
// It answers no interpretive question. There is no Passed, Score, Grade,
// Promotable, CriticalPolicyViolations, or NewBehaviorCount, and each absence
// is deliberate: a rule name does not prove severity, no public rule says what
// ContextRisk makes an action sensitive, "new" behavior needs something to
// compare against, and pass/fail needs thresholds that are configuration. Each
// of those belongs to a later task that will have the context this type does
// not.
//
// **Memory is O(1) in the number of records.** Four identifiers captured once,
// a counter, two timestamps, four counter structs, and five summaries. No
// slice, no map, no retained record. Retaining records would make this an
// accidental event archive — the thing the engine already refuses to be, and
// the thing task 067 owns explicitly.
//
// It is a value, not a service: no mutex, no atomic, no goroutine. AddRecord
// returns a new aggregate rather than mutating shared state, so a caller
// chooses its own synchronization. Two goroutines assigning to the same
// aggregate variable is a data race like any other — the value semantics make
// synchronization *possible*, not unnecessary.
type EvaluationAggregate struct {
	// bound records that this value came from NewEvaluationAggregate with a
	// valid run, and is the guard that makes the zero value unusable.
	//
	// Unexported fields stop a caller *mutating* an aggregate; they do not
	// stop `platform.EvaluationAggregate{}`, which any package can write.
	// Without this marker such a value accepts records happily — its empty
	// environment even matches a record whose environment is also empty, so
	// the environment guard waves it through — and the result is an evidence
	// summary belonging to no run.
	//
	// A bool rather than re-validating the four identifiers on every
	// AddRecord: they are immutable once set, so re-proving them per record
	// would spend real time on a per-decision path re-deriving a constant.
	// It also fails in the safer direction. A future second constructor that
	// forgot this field would produce aggregates that reject everything —
	// loud, immediate, easy to find — where a forgotten *field* in a
	// validation list would silently pass.
	//
	// It cannot drift from the identifiers it vouches for: nothing sets it
	// but the constructor, and nothing can reach the fields afterwards.
	bound bool

	// Run identity, captured once at construction. Task 052 made these
	// immutable through EvaluationRun's exported API, so they cannot drift
	// from the run they describe.
	runID       EvaluationRunID
	candidateID CandidateID
	environment EnvironmentRef
	profile     BehavioralProfileRef

	recordCount uint64

	// Evidence timestamps — the minimum and maximum DecisionRecord.Timestamp
	// observed. These are event times, not the run's lifecycle times, and
	// nothing here touches the run's StartedAt or FinishedAt.
	firstObservedAt time.Time
	lastObservedAt  time.Time

	decisions DecisionCounts
	risks     RiskCounts
	approvals ApprovalCounts
	policy    PolicySelection

	identityConfidence MetricSummary
	anomalyScore       MetricSummary
	anomalyConfidence  MetricSummary
	trustScore         MetricSummary
	contextRisk        MetricSummary
}

// NewEvaluationAggregate returns an empty aggregate bound to run, or an error
// if the run could not have come from NewEvaluationRun and its transitions.
//
// The run's identity is copied in because a record cannot supply it:
// DecisionRecord carries no EvaluationRunID, CandidateID, or behavioral
// profile, deliberately, and this task does not add them. Whatever feeds
// records into an aggregate is what knows which run they belong to.
//
// One caveat worth stating plainly: the aggregate carries the run's
// BehavioralProfileRef because the *run* knows it, not because any record
// proves it. Task 051 kept the learning scope out of DecisionRecord on
// purpose, so nothing here can attest that the engine producing these records
// was configured with that scope. See
// docs/tasks/v1.0/053-evaluation-result-aggregation.md.
func NewEvaluationAggregate(run EvaluationRun) (EvaluationAggregate, error) {
	// A zero-value EvaluationRun is constructible from any package —
	// task 052's fields are unexported, which prevents *mutation*, not
	// `platform.EvaluationRun{}`. Copying its accessors blindly would
	// produce an aggregate with four empty identifiers: evidence belonging
	// to no run, in no environment, which the environment check would then
	// happily match against records whose own environment was also empty.
	//
	// One aggregate is evidence for exactly one valid run, so an invalid run
	// yields no aggregate.
	if err := validateID("evaluation run id", string(run.ID())); err != nil {
		return EvaluationAggregate{}, err
	}
	if err := validateID("evaluation run candidate id", string(run.CandidateID())); err != nil {
		return EvaluationAggregate{}, err
	}
	if err := validateID("evaluation run environment", string(run.Environment())); err != nil {
		return EvaluationAggregate{}, err
	}
	if err := validateID("evaluation run behavioral profile", string(run.BehavioralProfile())); err != nil {
		return EvaluationAggregate{}, err
	}
	if run.CreatedAt().IsZero() {
		return EvaluationAggregate{}, fmt.Errorf("%w: evaluation run created_at is not set", ErrInvalidTimestamp)
	}
	// Any lifecycle state is acceptable — aggregation happens *during*
	// execution, so a pending or running run is the common case and a
	// terminal one is fine too. Only a status this package never produces
	// is refused, which is the same fail-closed stance the transitions take
	// for a value that came from somewhere unexpected.
	if !run.Status().valid() {
		return EvaluationAggregate{}, fmt.Errorf("%w: evaluation run is in an unrecognized state %s",
			ErrInvalidTransition, preview(string(run.Status())))
	}

	// Set only here, and only after every check above has passed.
	return EvaluationAggregate{
		bound:       true,
		runID:       run.ID(),
		candidateID: run.CandidateID(),
		environment: run.Environment(),
		profile:     run.BehavioralProfile(),
	}, nil
}

// Run identity, captured at construction.
func (a EvaluationAggregate) RunID() EvaluationRunID                  { return a.runID }
func (a EvaluationAggregate) CandidateID() CandidateID                { return a.candidateID }
func (a EvaluationAggregate) Environment() EnvironmentRef             { return a.environment }
func (a EvaluationAggregate) BehavioralProfile() BehavioralProfileRef { return a.profile }

// RecordCount returns how many observations were aggregated.
//
// One AddRecord call is one observation. The same record supplied twice counts
// twice: deduplication would need a set of every identifier seen, which is
// unbounded, and replay semantics belong to an ingest boundary that can define
// a retention window. See ADR 0026.
func (a EvaluationAggregate) RecordCount() uint64 { return a.recordCount }

// FirstObservedAt and LastObservedAt bound the evidence in event time — the
// minimum and maximum record timestamp, regardless of the order records
// arrived in. Both are the zero time for an empty aggregate.
//
// These are not the run's lifecycle timestamps and say nothing about when the
// execution started or finished.
func (a EvaluationAggregate) FirstObservedAt() time.Time { return a.firstObservedAt }
func (a EvaluationAggregate) LastObservedAt() time.Time  { return a.lastObservedAt }

// Categorical evidence, returned by value.
func (a EvaluationAggregate) Decisions() DecisionCounts        { return a.decisions }
func (a EvaluationAggregate) Risks() RiskCounts                { return a.risks }
func (a EvaluationAggregate) Approvals() ApprovalCounts        { return a.approvals }
func (a EvaluationAggregate) PolicySelection() PolicySelection { return a.policy }

// Numeric evidence, returned by value.
func (a EvaluationAggregate) IdentityConfidence() MetricSummary { return a.identityConfidence }
func (a EvaluationAggregate) AnomalyScore() MetricSummary       { return a.anomalyScore }
func (a EvaluationAggregate) AnomalyConfidence() MetricSummary  { return a.anomalyConfidence }
func (a EvaluationAggregate) TrustScore() MetricSummary         { return a.trustScore }
func (a EvaluationAggregate) ContextRisk() MetricSummary        { return a.contextRisk }

// AddRecord folds one DecisionRecord into the aggregate and returns the
// result. The receiver is unchanged.
//
// The aggregate must have come from NewEvaluationAggregate. A zero value —
// which any package can write, since unexported fields prevent mutation and
// not construction — returns ErrUnboundAggregate and folds nothing: one
// aggregate is evidence for exactly one valid run, and a record must not be
// able to conjure one that belongs to no run at all.
//
// A DecisionRecord normally comes from a successful Engine.Analyze, but it is
// a detached public struct: a caller can build or modify one freely. Every
// field this method consumes is therefore validated as untrusted input, and
// **all validation happens before any state changes** — a rejected record
// returns the original aggregate exactly, with no partial counts and no
// advanced time range.
//
// Malformed input is refused, never repaired. Nothing here clamps a
// non-finite score or coerces an unrecognized decision into a default bucket:
// the core guarantees its own output, so a record failing these checks did not
// come from a healthy engine, and rewriting it would turn a corruption signal
// into a plausible-looking number.
//
// Fields the aggregate does not consume — ActorType, FingerprintID,
// Contributors, PolicyReason, the correlation identifiers — are deliberately
// not validated. This is not a second Event.Validate, and rejecting a record
// over a field nothing reads would fail evaluations that are perfectly usable.
//
// An error is all that happens. A rejected record does not fail, cancel, or
// complete the EvaluationRun: this type has no authority over a run's
// lifecycle.
func (a EvaluationAggregate) AddRecord(record trustvian.DecisionRecord) (EvaluationAggregate, error) {
	// First, before anything about the record is considered: an aggregate
	// that never bound a run has nothing to be evidence *for*, and no record
	// should be able to turn EvaluationAggregate{} into a populated summary.
	if !a.bound {
		return a, fmt.Errorf("%w: use NewEvaluationAggregate", ErrUnboundAggregate)
	}

	if a.recordCount == math.MaxUint64 {
		return a, fmt.Errorf("%w: already at %d records", ErrAggregateOverflow, a.recordCount)
	}

	if record.EventID == "" {
		return a, fmt.Errorf("%w: event id is empty", ErrInvalidDecisionRecord)
	}
	if record.Timestamp.IsZero() {
		return a, fmt.Errorf("%w: event %s has no timestamp", ErrInvalidDecisionRecord, preview(record.EventID))
	}

	// Environment isolation. Counting production evidence into a staging
	// evaluation is a wrong answer rather than a rounding error, and the run's
	// EnvironmentRef is the only thing positioned to notice.
	if record.Environment != string(a.environment) {
		return a, fmt.Errorf("%w: event %s is from %s, evaluation is %s",
			ErrEnvironmentMismatch, preview(record.EventID), preview(record.Environment), preview(string(a.environment)))
	}
	// For genuine engine output these are the same value: features.Extract
	// derives the stable environment from the same Event.Context.Environment
	// the record reports. A disagreement means the record was assembled or
	// tampered with rather than produced.
	if record.Behavior.Environment != record.Environment {
		return a, fmt.Errorf("%w: event %s reports environment %s but its behavior says %s",
			ErrInvalidDecisionRecord, preview(record.EventID), preview(record.Environment), preview(record.Behavior.Environment))
	}

	decisions, err := a.decisions.count(record.Decision, record.EventID)
	if err != nil {
		return a, err
	}
	risks, err := a.risks.count(record.RiskLevel, record.EventID)
	if err != nil {
		return a, err
	}
	approvals, err := a.approvals.count(record.ApprovalStatus, record.EventID)
	if err != nil {
		return a, err
	}

	// MatchedDefault and PolicyRule are one piece of evidence, not two
	// independent fields, and DecisionRecord documents the pairing: a matched
	// rule names itself, the default names nothing. Counting them separately
	// would let a hand-built record assert "a rule matched" while naming no
	// rule — fabricated policy-selection evidence that a later scorecard
	// would read as real.
	//
	// Refused rather than bucketed: there is no "unknown" selection, because
	// inventing one would preserve the fabrication under a different name.
	if record.MatchedDefault && record.PolicyRule != "" {
		return a, fmt.Errorf("%w: event %s matched the policy default but names rule %s",
			ErrInvalidDecisionRecord, preview(record.EventID), preview(record.PolicyRule))
	}
	if !record.MatchedDefault && record.PolicyRule == "" {
		return a, fmt.Errorf("%w: event %s matched a policy rule but names none",
			ErrInvalidDecisionRecord, preview(record.EventID))
	}

	// The five numeric signals, all core-produced values in [0,1]. Validated
	// together so a single malformed one rejects the record before anything
	// is folded in.
	//
	// Values, not pointers to a's own fields. An earlier version paired each
	// name with a *MetricSummary so the fold below could be a loop; that made
	// the receiver escape to the heap and cost one 416-byte allocation per
	// record, on a path that runs once per decision. Five explicit
	// assignments are repetitive and allocation-free.
	metrics := [...]struct {
		name  string
		value float64
	}{
		{"identity_confidence", record.IdentityConfidence},
		{"anomaly_score", record.AnomalyScore},
		{"anomaly_confidence", record.AnomalyConfidence},
		{"trust_score", record.TrustScore},
		{"context_risk", record.ContextRisk},
	}
	for _, m := range metrics {
		if err := validateUnitInterval(m.name, m.value, record.EventID); err != nil {
			return a, err
		}
	}

	// Past this point nothing can fail, so the aggregate is safe to advance.
	// `a` is this function's own copy — the caller's aggregate is untouched
	// whichever way this returns.
	a.recordCount++
	a.decisions = decisions
	a.risks = risks
	a.approvals = approvals

	if record.MatchedDefault {
		a.policy.MatchedDefault++
	} else {
		a.policy.MatchedRule++
	}

	if a.firstObservedAt.IsZero() || record.Timestamp.Before(a.firstObservedAt) {
		a.firstObservedAt = record.Timestamp
	}
	if record.Timestamp.After(a.lastObservedAt) {
		a.lastObservedAt = record.Timestamp
	}

	a.identityConfidence = a.identityConfidence.observe(record.IdentityConfidence)
	a.anomalyScore = a.anomalyScore.observe(record.AnomalyScore)
	a.anomalyConfidence = a.anomalyConfidence.observe(record.AnomalyConfidence)
	a.trustScore = a.trustScore.observe(record.TrustScore)
	a.contextRisk = a.contextRisk.observe(record.ContextRisk)

	return a, nil
}

// count returns a copy of c with the decision counted, or an error naming an
// unrecognized value. The empty string is not a valid decision: every
// engine-produced record carries one.
func (c DecisionCounts) count(decision, eventID string) (DecisionCounts, error) {
	switch decision {
	case decisionAllow:
		c.Allow++
	case decisionObserveOnly:
		c.ObserveOnly++
	case decisionAlert:
		c.Alert++
	case decisionChallenge:
		c.Challenge++
	case decisionRequireApproval:
		c.RequireApproval++
	case decisionBlock:
		c.Block++
	default:
		return c, fmt.Errorf("%w: event %s has unrecognized decision %s",
			ErrInvalidDecisionRecord, preview(eventID), preview(decision))
	}
	return c, nil
}

// count returns a copy of c with the risk level counted, or an error naming an
// unrecognized value.
func (c RiskCounts) count(risk, eventID string) (RiskCounts, error) {
	switch risk {
	case riskLow:
		c.Low++
	case riskMedium:
		c.Medium++
	case riskHigh:
		c.High++
	case riskCritical:
		c.Critical++
	default:
		return c, fmt.Errorf("%w: event %s has unrecognized risk level %s",
			ErrInvalidDecisionRecord, preview(eventID), preview(risk))
	}
	return c, nil
}

// count returns a copy of c with the approval status counted.
//
// event.ApprovalUnspecified is the zero value and a valid state — a producer
// that said nothing about approval — so the empty string counts rather than
// rejecting. Any other unrecognized value is malformed.
func (c ApprovalCounts) count(status event.ApprovalStatus, eventID string) (ApprovalCounts, error) {
	switch status {
	case event.ApprovalUnspecified:
		c.Unspecified++
	case event.ApprovalNotRequired:
		c.NotRequired++
	case event.ApprovalRequired:
		c.Required++
	case event.ApprovalApproved:
		c.Approved++
	case event.ApprovalDenied:
		c.Denied++
	default:
		return c, fmt.Errorf("%w: event %s has unrecognized approval status %s",
			ErrInvalidDecisionRecord, preview(eventID), preview(string(status)))
	}
	return c, nil
}

// validateUnitInterval rejects a value the core could not have produced.
//
// Non-finite values are checked explicitly rather than relying on the range
// comparison: NaN compares false against everything, so `v < 0 || v > 1` alone
// would let a NaN through and poison every sum it reached.
func validateUnitInterval(field string, v float64, eventID string) error {
	if math.IsNaN(v) {
		return fmt.Errorf("%w: event %s has %s = NaN", ErrInvalidDecisionRecord, preview(eventID), field)
	}
	if math.IsInf(v, 0) {
		return fmt.Errorf("%w: event %s has %s = %v", ErrInvalidDecisionRecord, preview(eventID), field, v)
	}
	if v < 0 || v > 1 {
		return fmt.Errorf("%w: event %s has %s = %v, outside [0,1]",
			ErrInvalidDecisionRecord, preview(eventID), field, v)
	}
	return nil
}

// maxPreviewBytes bounds how much of an untrusted string reaches an error
// message. Long enough to identify a value at a glance — an event id, an
// environment name, a misspelled decision — and short enough that a
// pathological input cannot turn a rejection into a memory or log problem.
const maxPreviewBytes = 64

// preview renders untrusted text for a diagnostic, bounded.
//
// DecisionRecord is fixed-*shape*, not size-bounded: task 050 says so
// explicitly, because caller-supplied identifiers and names have no length
// limit. So a rejection path that echoed a field verbatim would let whoever
// constructed the record choose how much memory the error allocates and how
// much output the log absorbs — a malformed record turning into an
// amplification primitive, at exactly the moment the system is already
// unhappy.
//
// The quoting is applied to the truncated prefix, never to the whole string.
// Quoting first and truncating after would produce the full escaped copy —
// potentially several times the original size — before discarding it, which
// is the bound this function exists to provide.
//
// Truncation lands on a rune boundary so the result stays valid UTF-8, and it
// is visible in the output: a silently shortened value is worse than an
// obviously shortened one, because it looks like the whole thing.
func preview(s string) string {
	if len(s) <= maxPreviewBytes {
		return strconv.Quote(s)
	}

	// Back off to the start of the rune straddling the cut. The scan is
	// bounded by UTF-8's maximum encoding length.
	cut := maxPreviewBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		// The prefix is not valid UTF-8 at all. Keep the full budget and let
		// Quote escape it; the output stays bounded either way.
		cut = maxPreviewBytes
	}

	return strconv.Quote(s[:cut]) + "... (truncated, " + strconv.Itoa(len(s)) + " bytes total)"
}
