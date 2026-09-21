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
	// GateVerdictPass means exactly: all five task 056 hard gates passed.
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
}

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

// Verdict is GateVerdictPass only when all five checks passed.
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

	verdict := GateVerdictFail
	if referenceEvidence.Passed &&
		candidateEvidence.Passed &&
		addedBehaviors.Passed &&
		blockDecisions.Passed &&
		criticalRisk.Passed {
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

		verdict: verdict,
	}, nil
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
