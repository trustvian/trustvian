package platform

// Promotions: one recorded platform decision that a candidate may advance
// from one environment toward another.
//
// A promotion is evidence, not action. It records that at a moment, on named
// completed runs, under named limits, the platform reached a verdict. Nothing
// here deploys, moves, releases or authorizes anything, and nothing records
// where a candidate now runs — Trustvian has no observer that could know.
//
// Two properties are load-bearing and neither is a convention:
//
// The outcome has no input. NewPromotion derives it from the gate result's
// verdict, so an accepted promotion carrying a FAIL result is unconstructible
// rather than merely discouraged.
//
// The gate result is snapshotted whole, and restored verbatim. Re-deriving it
// later answers "what would today's build decide", which is a different
// question from "what did the platform rely on" — and a corrected gate may
// legitimately answer it differently.
//
// See docs/tasks/v1.0/066-promotion-workflow.md and
// docs/adr/0040-promotions-are-immutable-evidence-backed-decisions.md.

import (
	"errors"
	"fmt"
	"time"
)

// MaxPromotionPage bounds one page of ProjectPromotions.
//
// The same number, for the same reason, as MaxEnvironmentPage: the HTTP layer
// must reject the limit the store enforces, and two copies of a bound is how
// two layers come to disagree about it. This is the whole range — the store
// accepts nothing above it.
//
// Defined as MaxListPage since task 074. The name stays because it is
// published compatibility surface; the value has one definition.
const MaxPromotionPage = MaxListPage

var (
	// ErrPromotionScope reports two runs that are not a promotion-eligible
	// pair: they belong to different agents, or they record different
	// environments.
	//
	// Not ErrComparisonScope, which is specifically "different projects" and
	// is still returned for that. Not a state conflict either: the pairing
	// itself can never succeed, so the caller must change the request rather
	// than retry it.
	ErrPromotionScope = errors.New(
		"platform: runs do not describe one agent evaluated in one environment")

	// ErrPromotionOrder reports a source/target pair CanPromote refuses.
	//
	// Covers every case the relation defines: an archived environment on
	// either side, an unranked one on either side, equal ranks, a backward
	// move, the same environment twice, and two projects. The configuration
	// may legitimately change, so this is a conflict rather than a
	// permanently invalid request.
	ErrPromotionOrder = errors.New(
		"platform: target environment is not forward of the source")
)

// PromotionOutcome is what the platform decided.
//
// A closed two-value vocabulary. An unrecognized persisted value is
// corruption, never coerced — the same rule EnvironmentStatus follows, for
// the same reason: a row saying "approved" means something wrote it that this
// code did not, and guessing which of two states it meant is how a rejected
// decision silently reads as accepted.
type PromotionOutcome string

const (
	// PromotionAccepted means every structural precondition held and the gate
	// verdict was PASS.
	//
	// It does not mean deployed, released, safe or authorized. No field
	// carries those names and nothing acts on this value.
	PromotionAccepted PromotionOutcome = "accepted"

	// PromotionRejected means every structural precondition held and the gate
	// verdict was FAIL.
	//
	// Not an incident, not a fault and not a security event. The evidence was
	// complete and valid; it did not meet the caller's limits.
	PromotionRejected PromotionOutcome = "rejected"
)

func (o PromotionOutcome) valid() bool {
	return o == PromotionAccepted || o == PromotionRejected
}

// outcomeFor maps a verdict to the decision it produces.
//
// The only place this correspondence is written. A caller cannot supply an
// outcome, and this function is what makes "accepted iff PASS" an invariant
// rather than a convention.
func outcomeFor(verdict GateVerdict) (PromotionOutcome, error) {
	switch verdict {
	case GateVerdictPass:
		return PromotionAccepted, nil
	case GateVerdictFail:
		return PromotionRejected, nil
	default:
		return "", fmt.Errorf("%w: gate verdict %q is not a known verdict",
			ErrInvalidGateEvidence, preview(string(verdict)))
	}
}

// EnvironmentPosition is one environment as it stood when a decision was made.
//
// A snapshot, not a reference: rank, and the revision that produced it, are
// configuration a later operator may change, and a promotion has to keep
// saying what it was decided against.
//
// Three fields and no more. No Name — a label humans read, which changes
// without changing anything the decision rested on. No Status — a Promotion
// exists only if both environments were active, so the record's existence is
// the status assertion. No ProjectID — both belong to the promotion's project,
// which the record already carries once.
type EnvironmentPosition struct {
	Ref      EnvironmentRef
	Rank     uint16
	Revision uint64
}

// Promotion is one recorded platform decision.
//
// Immutable: no transition method, no setter, no lifecycle, and the store has
// no update. A Promotion that exists is finished.
type Promotion struct {
	id          PromotionID
	projectID   ProjectID
	candidateID CandidateID

	referenceRunID EvaluationRunID
	candidateRunID EvaluationRunID

	source EnvironmentPosition
	target EnvironmentPosition

	// limits and gateResult are the decision-time evidence, snapshotted.
	// gateResult is what the workflow actually consumed; limits are its three
	// maximums, kept as a named value because that is how a caller supplied
	// them.
	limits     EvaluationGateLimits
	gateResult EvaluationGateResult

	outcome   PromotionOutcome
	decidedAt time.Time
}

func (p Promotion) ID() PromotionID                  { return p.id }
func (p Promotion) ProjectID() ProjectID             { return p.projectID }
func (p Promotion) CandidateID() CandidateID         { return p.candidateID }
func (p Promotion) ReferenceRunID() EvaluationRunID  { return p.referenceRunID }
func (p Promotion) CandidateRunID() EvaluationRunID  { return p.candidateRunID }
func (p Promotion) Source() EnvironmentPosition      { return p.source }
func (p Promotion) Target() EnvironmentPosition      { return p.target }
func (p Promotion) GateLimits() EvaluationGateLimits { return p.limits }

// GateResult is the gate result this decision consumed, exactly as it was
// when the decision was made.
//
// Historical evidence, not a live view. Re-deriving a gate result from the
// same runs and limits under a later build may legitimately differ, and when
// it does, this value is still what the platform relied on. A divergence is a
// corrected implementation, not a corrupt record, and nothing overwrites it.
func (p Promotion) GateResult() EvaluationGateResult { return p.gateResult }

func (p Promotion) Outcome() PromotionOutcome { return p.outcome }
func (p Promotion) DecidedAt() time.Time      { return p.decidedAt }

// PromotionDecision is the complete input to NewPromotion.
//
// Four fields, because almost everything a promotion records is derivable
// from two of them. The gate result is self-describing about which comparison
// it gated — task 056 built it that way — so both run identifiers, both
// candidate identifiers, the environment and the three limits all come out of
// it rather than being supplied alongside it and risking disagreement.
type PromotionDecision struct {
	ID PromotionID

	// Source and Target are the loaded environments, not snapshots. The
	// constructor takes the values so it can ask CanPromote itself and derive
	// the snapshot, rather than trusting a caller-assembled position.
	Source Environment
	Target Environment

	// GateResult is the authoritative result the evaluation path produced. It
	// decides the outcome; no outcome is accepted alongside it.
	GateResult EvaluationGateResult

	DecidedAt time.Time
}

// NewPromotion records one decision.
//
// Everything the service can derive is derived here rather than accepted:
// there is no Outcome input, no project, no candidate and no run identifier.
// A caller supplies an identifier, two environments it loaded, a gate result
// it computed, and a clock the composition root owns.
func NewPromotion(d PromotionDecision) (Promotion, error) {
	if err := validateID("promotion id", string(d.ID)); err != nil {
		return Promotion{}, err
	}
	if d.DecidedAt.IsZero() {
		return Promotion{}, fmt.Errorf("%w: promotion has no decision time", ErrInvalidTimestamp)
	}

	// The bound marker exists for exactly this fail-closed check: a zero
	// EvaluationGateResult is not a FAIL, it is the absence of an evaluation.
	if !d.GateResult.bound {
		return Promotion{}, fmt.Errorf(
			"%w: gate result was not produced by EvaluateEvaluationGate", ErrInvalidGateEvidence)
	}

	// The one ordering question, asked of the one primitive. This constructor
	// compares no ranks itself, so CanPromote stays the only implementation of
	// promotion precedence in the repository.
	if !CanPromote(d.Source, d.Target) {
		return Promotion{}, fmt.Errorf(
			"%w: %s cannot promote toward %s in project %s",
			ErrPromotionOrder, preview(string(d.Source.Ref())),
			preview(string(d.Target.Ref())), preview(string(d.Source.ProjectID())))
	}

	// The result must gate the comparison being recorded, not a different one.
	if d.GateResult.Environment() != d.Source.Ref() {
		return Promotion{}, fmt.Errorf(
			"%w: gate result is for environment %s, promotion source is %s",
			ErrInvalidGateEvidence, preview(string(d.GateResult.Environment())),
			preview(string(d.Source.Ref())))
	}
	if d.GateResult.ReferenceRunID() == d.GateResult.CandidateRunID() {
		return Promotion{}, fmt.Errorf(
			"%w: gate result names run %s on both sides",
			ErrInvalidGateEvidence, preview(string(d.GateResult.ReferenceRunID())))
	}

	outcome, err := outcomeFor(d.GateResult.Verdict())
	if err != nil {
		return Promotion{}, err
	}

	// CanPromote already established that both are ranked and share a project,
	// so neither read below can fail.
	sourceRank, _ := d.Source.Rank()
	targetRank, _ := d.Target.Rank()

	return Promotion{
		id:          d.ID,
		projectID:   d.Source.ProjectID(),
		candidateID: d.GateResult.CandidateCandidateID(),

		referenceRunID: d.GateResult.ReferenceRunID(),
		candidateRunID: d.GateResult.CandidateRunID(),

		source: EnvironmentPosition{
			Ref: d.Source.Ref(), Rank: sourceRank, Revision: d.Source.Revision()},
		target: EnvironmentPosition{
			Ref: d.Target.Ref(), Rank: targetRank, Revision: d.Target.Revision()},

		limits: EvaluationGateLimits{
			MaxAddedBehaviors:           d.GateResult.AddedBehaviors().Maximum,
			MaxBlockDecisions:           d.GateResult.BlockDecisions().Maximum,
			MaxCriticalRiskObservations: d.GateResult.CriticalRiskObservations().Maximum,
		},
		gateResult: d.GateResult,

		outcome:   outcome,
		decidedAt: d.DecidedAt,
	}, nil
}

// restorePromotion rebuilds a stored decision, applying every check a live
// value faced — and deliberately not the ones history is allowed to fail.
//
// Shared by both backends so neither can accept a row the other would refuse.
//
// What it checks: identifiers, a parseable non-zero timestamp, a known
// outcome, that the outcome agrees with the stored verdict, and the ordering
// invariant.
//
// What it does not check: whether a stored Passed flag agrees with arithmetic
// on its own operands, whether the five flags combine to the stored verdict
// under today's rule, or whether today's gate would reach the same verdict.
// None of those is corruption — the record is what the platform produced, not
// what it should have produced.
func restorePromotion(
	id PromotionID, projectID ProjectID, candidateID CandidateID,
	source, target EnvironmentPosition,
	limits EvaluationGateLimits, gate EvaluationGateResult,
	outcome PromotionOutcome, decidedAt time.Time,
) (Promotion, error) {
	corrupt := func(err error) (Promotion, error) {
		return Promotion{}, fmt.Errorf("%w: promotion %s: %w",
			ErrStoreCorrupt, preview(string(id)), err)
	}

	for _, field := range []struct {
		name  string
		value string
	}{
		{"promotion id", string(id)},
		{"promotion project id", string(projectID)},
		{"promotion candidate id", string(candidateID)},
		{"promotion source environment", string(source.Ref)},
		{"promotion target environment", string(target.Ref)},
	} {
		if err := validateID(field.name, field.value); err != nil {
			return corrupt(err)
		}
	}
	if decidedAt.IsZero() {
		return corrupt(errors.New("decided_at is the zero time"))
	}
	if !outcome.valid() {
		return corrupt(fmt.Errorf("outcome %q is not a known outcome", preview(string(outcome))))
	}
	if !gate.bound {
		return corrupt(errors.New("gate result is unbound"))
	}

	// Task 066's own invariant, fixed by NewPromotion: a row that violates it
	// was not written by this code.
	expected, err := outcomeFor(gate.Verdict())
	if err != nil {
		return corrupt(err)
	}
	if expected != outcome {
		return corrupt(fmt.Errorf(
			"outcome %q disagrees with stored gate verdict %q",
			outcome, gate.Verdict()))
	}

	// The ordering invariant, asked of the one primitive. Two environments are
	// rebuilt from the stored positions — active and ranked, since a promotion
	// exists only if both were, and named after their own refs the way
	// migration names a backfilled environment.
	sourceEnv, err := restoreEnvironment(
		source.Ref, projectID, string(source.Ref),
		source.Rank, true, EnvironmentActive, source.Revision)
	if err != nil {
		return corrupt(err)
	}
	targetEnv, err := restoreEnvironment(
		target.Ref, projectID, string(target.Ref),
		target.Rank, true, EnvironmentActive, target.Revision)
	if err != nil {
		return corrupt(err)
	}
	if !CanPromote(sourceEnv, targetEnv) {
		return corrupt(fmt.Errorf(
			"stored ordering is not forward: %s rank %d toward %s rank %d",
			source.Ref, source.Rank, target.Ref, target.Rank))
	}

	return Promotion{
		id:          id,
		projectID:   projectID,
		candidateID: candidateID,

		referenceRunID: gate.ReferenceRunID(),
		candidateRunID: gate.CandidateRunID(),

		source: source,
		target: target,

		limits:     limits,
		gateResult: gate,

		outcome:   outcome,
		decidedAt: decidedAt,
	}, nil
}

// validatePromotionPage bounds a list request before it reaches a query.
//
// 1..MaxPromotionPage is the whole range, at the store edge as well as the
// route. A transport that needs to know whether another page exists asks a
// second bounded question rather than widening this one.
func validatePromotionPage(after PromotionID, limit int) error {
	if limit < 1 || limit > MaxPromotionPage {
		return fmt.Errorf("%w: promotion page limit %d is outside 1..%d",
			ErrInvalidID, limit, MaxPromotionPage)
	}
	if after != "" {
		if err := validateID("promotion page cursor", string(after)); err != nil {
			return err
		}
	}
	return nil
}
