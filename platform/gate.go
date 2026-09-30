package platform

// Deterministic hard gates: one valid scorecard plus explicit caller-owned
// limits, producing a pass/fail verdict.
//
//	EvaluationScorecard ──┐
//	                      ├──▶ EvaluateEvaluationGate ──▶ EvaluationGateResult
//	EvaluationGatePolicy ─┘                                   PASS | FAIL
//
// This is the first layer allowed to answer "did the evaluation satisfy the
// configured hard gates?" It answers no question of promotion: a PASS has no
// side effect, and task 066 owns deployment workflow.
//
// Every comparison is integer. No average, rate or delta participates in a
// verdict, because a high average must never override one failed hard gate.
//
// See docs/adr/0029-hard-gates-use-explicit-integer-evidence.md.

import (
	"errors"
	"fmt"
)

var (
	// ErrInvalidGatePolicy reports a policy that is zero-valued, unbound, or
	// not produced by NewEvaluationGatePolicy.
	ErrInvalidGatePolicy = errors.New("platform: invalid evaluation gate policy")

	// ErrInvalidGateEvidence reports a scorecard that is zero-valued,
	// unbound, or structurally impossible.
	//
	// A valid scorecard that fails its gates is not this. That is a FAIL
	// verdict with a nil error.
	ErrInvalidGateEvidence = errors.New("platform: invalid evaluation gate evidence")
)

// GateVerdict is the outcome of evaluating every configured hard gate.
type GateVerdict string

const (
	// GateVerdictPass means exactly: all five task 056 hard gates passed,
	// and — when the caller supplied one — the optional counted-change limit
	// of ADR 0052 passed too.
	//
	// It does not mean safe, secure, approved, deployable or promotable. No
	// field carries those names, and nothing acts on this value.
	GateVerdictPass GateVerdict = "pass"

	// GateVerdictFail means at least one gate did not pass.
	GateVerdictFail GateVerdict = "fail"
)

// EvaluationGateLimits are the caller-owned maximums.
//
// Zero is a legitimate, meaningful maximum: MaxBlockDecisions of 0 accepts no
// candidate block decision at all, which is the strictest policy expressible.
// So zero never means "unset" — see EvaluationGatePolicy — and there is no
// Enabled flag, nil threshold or infinity sentinel. A caller indifferent to a
// maximum chooses a sufficiently high uint64 explicitly.
type EvaluationGateLimits struct {
	// MaxAddedBehaviors limits behaviors the candidate exhibited that the
	// reference never did. The platform makes no claim that added behavior
	// is bad; the limit is the caller's acceptance policy.
	MaxAddedBehaviors uint64

	// MaxBlockDecisions limits candidate block decisions.
	//
	// A block is the policy engine doing what it was configured to do. This
	// is not a policy-violation count.
	MaxBlockDecisions uint64

	// MaxCriticalRiskObservations limits candidate critical-risk readings.
	//
	// A risk classification, not a critical policy violation and not a
	// security incident.
	MaxCriticalRiskObservations uint64

	// MaxAddedBehaviorChanges optionally limits counted behavioral changes —
	// ADR 0052's unit, the diff's AddedChangeCount — alongside
	// MaxAddedBehaviors, which keeps counting identities.
	//
	// The one optional limit, and the one departure from "zero is never
	// unset" above. The three limits before it are mandatory because they
	// always were; this one arrived after callers and stored promotions
	// already existed, and a mandatory limit would default every one of them
	// to a maximum of zero and fail any candidate that added anything. Its
	// zero value is therefore *absent*, and absent means the check is not
	// evaluated — which the result reports as not evaluated, never as passed.
	// An explicit zero is NewOptionalGateLimit(0) and is the strictest limit.
	MaxAddedBehaviorChanges OptionalGateLimit
}

// OptionalGateLimit is a maximum a caller may leave unset.
//
// A value type with a set marker rather than a *uint64: a limit is copied
// into policies, results and promotions, and a shared pointer would let one
// holder change what another recorded. The zero value is unset.
type OptionalGateLimit struct {
	maximum uint64
	set     bool
}

// NewOptionalGateLimit returns a set limit. Every uint64 is a legitimate
// maximum, 0 and MaxUint64 included.
func NewOptionalGateLimit(maximum uint64) OptionalGateLimit {
	return OptionalGateLimit{maximum: maximum, set: true}
}

// Maximum reports the limit and whether one was set. An unset limit reports
// 0 and false, and the 0 means nothing.
func (l OptionalGateLimit) Maximum() (uint64, bool) { return l.maximum, l.set }

// IsSet reports whether the caller supplied this limit.
func (l OptionalGateLimit) IsSet() bool { return l.set }

// EvaluationGatePolicy is a validated set of caller-owned limits.
//
// The bound marker exists because zero is a legitimate limit. Without it the
// strictest possible policy and an absent policy would be the same value, and
// the failure direction would be toward looking permissive.
type EvaluationGatePolicy struct {
	// bound marks a policy this package's constructor produced.
	bound bool

	limits EvaluationGateLimits
}

// NewEvaluationGatePolicy returns a policy carrying the given limits.
//
// Every uint64 is a legitimate maximum, including 0 and MaxUint64, so there
// is nothing to reject and no error to return. The zero EvaluationGateLimits
// produces a valid policy with three maximums of exactly zero — strict, not
// disabled.
func NewEvaluationGatePolicy(limits EvaluationGateLimits) EvaluationGatePolicy {
	return EvaluationGatePolicy{bound: true, limits: limits}
}

// Limits reports the configured maximums.
func (p EvaluationGatePolicy) Limits() EvaluationGateLimits { return p.limits }

// GateCheckState says whether an optional check contributed to a verdict.
//
// Only the counted-change check has one; the five task 056 checks are always
// evaluated and carry no state.
type GateCheckState string

const (
	// GateCheckEvaluated means the caller supplied the limit and the check's
	// outcome is part of the verdict.
	GateCheckEvaluated GateCheckState = "evaluated"

	// GateCheckNotEvaluated means the caller did not supply the limit. The
	// check did not run, did not pass and did not fail, and the verdict is
	// the five task 056 checks alone — exactly what it was before the limit
	// existed.
	GateCheckNotEvaluated GateCheckState = "not_evaluated"

	// GateCheckNotRecorded means the result was recorded before this check
	// existed: a promotion stored under schema 7 or earlier. Nobody could
	// supply the limit, so nothing is known about it and nothing is invented.
	// Only a restored result can carry this state.
	GateCheckNotRecorded GateCheckState = "not_recorded"
)

func (s GateCheckState) valid() bool {
	switch s {
	case GateCheckEvaluated, GateCheckNotEvaluated, GateCheckNotRecorded:
		return true
	default:
		return false
	}
}

// ChangeCountGate is the optional check of counted behavioral changes against
// MaxAddedBehaviorChanges (ADR 0052, issue 131).
//
// Actual, Maximum and Passed mean something only when State is
// GateCheckEvaluated, and are zero otherwise — a check that did not run has
// no outcome, and a false Passed there would read as a failure that never
// happened.
//
// CorrelationState and CountingPolicyVersion record what the count rested on
// when it was evaluated, so a stored decision explains itself: a `partial`
// count is the unfolded identity count (ADR 0052), and the policy version
// names the rule that produced it.
type ChangeCountGate struct {
	State GateCheckState

	Actual  uint64
	Maximum uint64
	Passed  bool

	CorrelationState      CorrelationState
	CountingPolicyVersion string
}

// Evaluated reports whether this check contributed to the verdict.
func (g ChangeCountGate) Evaluated() bool { return g.State == GateCheckEvaluated }

// MinimumCountGate is a check that an observed count met a required minimum.
type MinimumCountGate struct {
	Actual  uint64
	Minimum uint64
	Passed  bool
}

// MaximumCountGate is a check that an observed count stayed within a limit.
type MaximumCountGate struct {
	Actual  uint64
	Maximum uint64
	Passed  bool
}

func minimumGate(actual, minimum uint64) MinimumCountGate {
	return MinimumCountGate{Actual: actual, Minimum: minimum, Passed: actual >= minimum}
}

func maximumGate(actual, maximum uint64) MaximumCountGate {
	return MaximumCountGate{Actual: actual, Maximum: maximum, Passed: actual <= maximum}
}

// EvaluationGateResult is the complete, auditable outcome of one gate
// evaluation.
//
// Fixed-shape: no slice, map, pointer, interface, retained scorecard or
// policy, or string reason. The field names describe the known checks, which
// is what makes the result both auditable and closed — a bare Passed bool
// would not say which check failed, and a []Failure would accept anything.
//
// Every check is populated on every call, including when an earlier one
// failed. An auditor reading a FAIL needs everything that was measured, not
// everything up to the first problem.
type EvaluationGateResult struct {
	// bound marks a result this package's evaluator produced, so a later
	// layer can fail closed on EvaluationGateResult{}.
	bound bool

	referenceRunID     EvaluationRunID
	referenceCandidate CandidateID
	candidateRunID     EvaluationRunID
	candidateCandidate CandidateID
	environment        EnvironmentRef

	referenceEvidence MinimumCountGate
	candidateEvidence MinimumCountGate

	addedBehaviors           MaximumCountGate
	blockDecisions           MaximumCountGate
	criticalRiskObservations MaximumCountGate

	// addedBehaviorChanges is the optional sixth check. Always populated with
	// a state, so "not evaluated" is said rather than implied by a zero.
	addedBehaviorChanges ChangeCountGate

	verdict GateVerdict
}

// Comparison identity, so a result is self-describing about which comparison
// was gated. The two sides may legitimately name different candidates and
// different behavioral profiles.
func (r EvaluationGateResult) ReferenceRunID() EvaluationRunID   { return r.referenceRunID }
func (r EvaluationGateResult) ReferenceCandidateID() CandidateID { return r.referenceCandidate }
func (r EvaluationGateResult) CandidateRunID() EvaluationRunID   { return r.candidateRunID }
func (r EvaluationGateResult) CandidateCandidateID() CandidateID { return r.candidateCandidate }
func (r EvaluationGateResult) Environment() EnvironmentRef       { return r.environment }

// The five checks, in their stable documented order.

// ReferenceEvidence reports whether the reference evaluation observed
// anything at all.
func (r EvaluationGateResult) ReferenceEvidence() MinimumCountGate { return r.referenceEvidence }

// CandidateEvidence reports whether the candidate evaluation observed
// anything at all.
func (r EvaluationGateResult) CandidateEvidence() MinimumCountGate { return r.candidateEvidence }

// AddedBehaviors reports the added-behavior count against its limit.
func (r EvaluationGateResult) AddedBehaviors() MaximumCountGate { return r.addedBehaviors }

// BlockDecisions reports the candidate block-decision count against its limit.
func (r EvaluationGateResult) BlockDecisions() MaximumCountGate { return r.blockDecisions }

// CriticalRiskObservations reports the candidate critical-risk count against
// its limit.
func (r EvaluationGateResult) CriticalRiskObservations() MaximumCountGate {
	return r.criticalRiskObservations
}

// AddedBehaviorChanges reports the optional counted-change check. Its State
// says whether it was evaluated; see ChangeCountGate.
func (r EvaluationGateResult) AddedBehaviorChanges() ChangeCountGate {
	return r.addedBehaviorChanges
}

// Verdict is GateVerdictPass only when all five checks passed and the
// counted-change check, if evaluated, passed too.
func (r EvaluationGateResult) Verdict() GateVerdict { return r.verdict }

// EvaluateEvaluationGate applies a gate policy to a scorecard.
//
// A failed gate is a normal result, not an error: a valid evaluation may fail
// policy, and that returns a complete result with a FAIL verdict and a nil
// error. An error means the evaluation itself could not be trusted — an
// unbound policy, an unbound scorecard, or structurally impossible evidence.
//
// The verb is deliberate. This evaluates a gate; it does not promote,
// approve, release or deploy anything.
func EvaluateEvaluationGate(
	scorecard EvaluationScorecard,
	policy EvaluationGatePolicy,
) (EvaluationGateResult, error) {
	if !policy.bound {
		return EvaluationGateResult{}, fmt.Errorf(
			"%w: policy was not produced by NewEvaluationGatePolicy", ErrInvalidGatePolicy)
	}
	if !scorecard.bound {
		return EvaluationGateResult{}, fmt.Errorf(
			"%w: scorecard was not produced by NewEvaluationScorecard", ErrInvalidGateEvidence)
	}

	// The behavior summary's counts are int and the gates are uint64. Check
	// the arithmetic before any conversion, so corrupted evidence can never
	// become an enormous passing count.
	behavior := scorecard.Behavior()
	if err := validateBehaviorSummaryArithmetic(behavior); err != nil {
		return EvaluationGateResult{}, err
	}

	limits := policy.limits

	// Every gate is evaluated. No short-circuit, even once one has failed.
	referenceEvidence := minimumGate(scorecard.ReferenceRecordCount(), 1)
	candidateEvidence := minimumGate(scorecard.CandidateRecordCount(), 1)

	addedBehaviors := maximumGate(uint64(behavior.AddedCount), limits.MaxAddedBehaviors)
	blockDecisions := maximumGate(
		scorecard.Decisions().Block.Candidate().Count(), limits.MaxBlockDecisions)
	criticalRisk := maximumGate(
		scorecard.Risks().Critical.Candidate().Count(), limits.MaxCriticalRiskObservations)

	changes := ChangeCountGate{State: GateCheckNotEvaluated}
	if maximum, set := limits.MaxAddedBehaviorChanges.Maximum(); set {
		// Validated only when used, so a caller who omits the limit gets
		// exactly the evaluation that existed before it — no new error path.
		if err := validateChangeCountEvidence(behavior); err != nil {
			return EvaluationGateResult{}, err
		}
		actual := uint64(behavior.AddedChangeCount)
		changes = ChangeCountGate{
			State:                 GateCheckEvaluated,
			Actual:                actual,
			Maximum:               maximum,
			Passed:                actual <= maximum,
			CorrelationState:      behavior.CorrelationState,
			CountingPolicyVersion: behavior.CountingPolicyVersion,
		}
	}

	verdict := GateVerdictFail
	if referenceEvidence.Passed &&
		candidateEvidence.Passed &&
		addedBehaviors.Passed &&
		blockDecisions.Passed &&
		criticalRisk.Passed &&
		(!changes.Evaluated() || changes.Passed) {
		verdict = GateVerdictPass
	}

	return EvaluationGateResult{
		bound: true,

		referenceRunID:     scorecard.ReferenceRunID(),
		referenceCandidate: scorecard.ReferenceCandidateID(),
		candidateRunID:     scorecard.CandidateRunID(),
		candidateCandidate: scorecard.CandidateCandidateID(),
		environment:        scorecard.Environment(),

		referenceEvidence: referenceEvidence,
		candidateEvidence: candidateEvidence,

		addedBehaviors:           addedBehaviors,
		blockDecisions:           blockDecisions,
		criticalRiskObservations: criticalRisk,

		addedBehaviorChanges: changes,

		verdict: verdict,
	}, nil
}

// validateChangeCountEvidence defends the counted-change check the way
// validateBehaviorSummaryArithmetic defends the other conversions.
//
// ADR 0052's arithmetic, restated at the point a limit consumes it: the fold
// may only lower a count, never to zero over a positive added count, and a
// correlation that is not complete means the unfolded identity count. A
// summary breaking any of these came from somewhere other than the fold, and a
// wrong count here errs in whichever direction the corruption points — so it
// is refused rather than gated.
func validateChangeCountEvidence(s BehaviorSummary) error {
	changes, added := s.AddedChangeCount, s.AddedCount
	switch {
	case changes < 0:
		return fmt.Errorf("%w: counted change count %d is negative",
			ErrInvalidGateEvidence, changes)
	case changes > added:
		return fmt.Errorf("%w: %d counted changes over %d added identities",
			ErrInvalidGateEvidence, changes, added)
	case added > 0 && changes == 0:
		return fmt.Errorf("%w: %d added identities counted as zero changes",
			ErrInvalidGateEvidence, added)
	}
	switch s.CorrelationState {
	case CorrelationComplete:
	case CorrelationPartial, CorrelationUnavailable:
		if changes != added {
			return fmt.Errorf("%w: correlation %s reports %d changes over %d added "+
				"identities; an incomplete correlation is the identity count",
				ErrInvalidGateEvidence, s.CorrelationState, changes, added)
		}
	default:
		return fmt.Errorf("%w: correlation state %q is not a known state",
			ErrInvalidGateEvidence, preview(string(s.CorrelationState)))
	}
	if s.CountingPolicyVersion == "" {
		return fmt.Errorf("%w: counted change count names no counting policy",
			ErrInvalidGateEvidence)
	}
	return nil
}

// validateBehaviorSummaryArithmetic defends the int-to-uint64 conversion.
//
// A constructor-produced scorecard is trusted, so this is not a
// re-validation of every task 055 field — only the arithmetic a lossy
// conversion sits on.
//
// The direction of that failure is what matters. Converting a negative int
// yields math.MaxUint64, and a maximum gate passes when actual <= maximum —
// so a corrupt count fails every ordinary limit but *passes* a permissive
// MaxUint64 one, which is exactly the limit a caller indifferent to a gate
// is told to choose. Corrupted evidence would then read as a clean PASS.
// Rejecting before the conversion is what closes that path.
func validateBehaviorSummaryArithmetic(s BehaviorSummary) error {
	if s.AddedCount < 0 || s.RemovedCount < 0 || s.SharedCount < 0 ||
		s.ReferenceDistinctCount < 0 || s.CandidateDistinctCount < 0 {
		return fmt.Errorf(
			"%w: behavior summary holds a negative count", ErrInvalidGateEvidence)
	}
	if s.AddedCount+s.SharedCount != s.CandidateDistinctCount {
		return fmt.Errorf(
			"%w: behavior summary added+shared (%d) does not equal candidate distinct (%d)",
			ErrInvalidGateEvidence, s.AddedCount+s.SharedCount, s.CandidateDistinctCount)
	}
	if s.RemovedCount+s.SharedCount != s.ReferenceDistinctCount {
		return fmt.Errorf(
			"%w: behavior summary removed+shared (%d) does not equal reference distinct (%d)",
			ErrInvalidGateEvidence, s.RemovedCount+s.SharedCount, s.ReferenceDistinctCount)
	}
	return nil
}

// restoreEvaluationGateResult rebuilds a stored gate result verbatim.
//
// It is not an evaluator, and that is the point. Every observable component —
// the five identity values, each check's operands, **each check's Passed
// flag**, and the verdict — is taken from storage exactly as written. It must
// never call minimumGate, maximumGate or EvaluateEvaluationGate, and must
// never re-derive a flag or a verdict from anything.
//
// Why that matters: a promotion records what the platform relied on, not what
// today's build would compute. Suppose an older build had a defect and wrote
//
//	actual = 1, minimum = 1, passed = false
//
// and recorded the promotion as rejected, because that is what it decided. A
// later build with a corrected helper that recomputed the flag would hand back
// passed = true beside the stored fail verdict — a value that existed at no
// point in time. A hybrid is worse evidence than either half alone.
//
// Validation here is structural only: the caller has already parsed the
// scalars, and the two invariants that are task 066's own — a known verdict,
// and outcome agreeing with it — are checked in restorePromotion.
func restoreEvaluationGateResult(
	referenceRunID EvaluationRunID, referenceCandidate CandidateID,
	candidateRunID EvaluationRunID, candidateCandidate CandidateID,
	environment EnvironmentRef,
	referenceEvidence, candidateEvidence MinimumCountGate,
	addedBehaviors, blockDecisions, criticalRisk MaximumCountGate,
	changes ChangeCountGate,
	verdict GateVerdict,
) (EvaluationGateResult, error) {
	if verdict != GateVerdictPass && verdict != GateVerdictFail {
		return EvaluationGateResult{}, fmt.Errorf(
			"%w: stored gate verdict %q is not a known verdict",
			ErrInvalidGateEvidence, preview(string(verdict)))
	}
	if err := validateStoredChangeGate(changes); err != nil {
		return EvaluationGateResult{}, err
	}
	return EvaluationGateResult{
		bound: true,

		referenceRunID:     referenceRunID,
		referenceCandidate: referenceCandidate,
		candidateRunID:     candidateRunID,
		candidateCandidate: candidateCandidate,
		environment:        environment,

		referenceEvidence: referenceEvidence,
		candidateEvidence: candidateEvidence,

		addedBehaviors:           addedBehaviors,
		blockDecisions:           blockDecisions,
		criticalRiskObservations: criticalRisk,

		addedBehaviorChanges: changes,

		verdict: verdict,
	}, nil
}

// validateStoredChangeGate checks a restored counted-change check for shape
// only, never for arithmetic: a stored Passed flag is taken as written, like
// every other stored flag (see restoreEvaluationGateResult).
//
// Shape is what separates the three states. An evaluated check carries its
// counting context; a check that was not evaluated or not recorded carries
// nothing, because an outcome beside a check that never ran would be an
// outcome nobody produced.
func validateStoredChangeGate(g ChangeCountGate) error {
	if !g.State.valid() {
		return fmt.Errorf("%w: stored counted-change check state %q is not a known state",
			ErrInvalidGateEvidence, preview(string(g.State)))
	}
	if g.Evaluated() {
		switch g.CorrelationState {
		case CorrelationComplete, CorrelationPartial, CorrelationUnavailable:
		default:
			return fmt.Errorf("%w: stored counted-change correlation state %q is not a known state",
				ErrInvalidGateEvidence, preview(string(g.CorrelationState)))
		}
		if g.CountingPolicyVersion == "" {
			return fmt.Errorf("%w: stored counted-change check names no counting policy",
				ErrInvalidGateEvidence)
		}
		return nil
	}
	if g != (ChangeCountGate{State: g.State}) {
		return fmt.Errorf("%w: counted-change check is %s but carries an outcome",
			ErrInvalidGateEvidence, g.State)
	}
	return nil
}
