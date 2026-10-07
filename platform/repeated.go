package platform

// Repeated evaluation: N isolated repetitions per side, reduced to per-behavior
// integer presence counts and gated by six named checks (task 078, ADR 0053).
//
// One run of a model-driven agent is not evidence: task 078's measurement found
// an unchanged agent's behavior set moving between isolated runs, failing a
// single-run gate at zero on 48/90 and 59/90 unchanged pairs. So the question is
// asked over N runs per side, and answered in integers:
//
//	reference_runs_present   how many of the N reference runs showed it   [0, N]
//	candidate_runs_present   how many of the N candidate runs showed it   [0, N]
//
// Integers only — no rate, proportion, interval or significance test, for the
// reason ADR 0029 excluded floats from every verdict.
//
// What this file owns, and what it deliberately does not:
//
//   - It reads each repetition's persisted evidence — the snapshot and the
//     aggregate CompareEvaluations already reads — one run at a time. There is
//     no pairing: a reference repetition and a candidate repetition are never
//     compared with each other, so presence counts are invariant under any
//     permutation of either side by construction rather than by assertion.
//   - It counts presence, classifies, and evaluates six checks. The CLI that
//     drives the repetitions computes none of it (ADR 0033).
//   - The single-run chain — CompareBehaviorSnapshots, NewEvaluationScorecard,
//     EvaluateEvaluationGate, EvaluationGateLimits — is untouched, and at
//     N = 1, k = 1, j = 0 this gate reaches the same verdict, check for check.

import (
	"context"
	"errors"
	"fmt"
	"slices"

	trustvian "github.com/trustvian/trustvian"
)

var (
	// ErrInvalidRepeatedRequest reports a repeated comparison that can never
	// succeed as asked: a run count outside 1..64, unequal sides, a run named
	// twice, or a k or j outside its bounds. A request error, never a verdict.
	ErrInvalidRepeatedRequest = errors.New("platform: invalid repeated evaluation request")

	// ErrRepeatedIsolation reports repetitions that shared a behavioral profile.
	//
	// Each repetition must run under its own learning scope, or repetition i
	// is analyzed against a baseline that already learned from 1..i-1 and the
	// presence counts measure learning order instead of the workload (task 078
	// § Learning isolation). The control plane refuses the evidence rather than
	// trusting the runner to have isolated it.
	ErrRepeatedIsolation = errors.New("platform: repetitions did not run in isolated learning scopes")
)

// MaxRepetitions bounds N, per side. Task 078 § runs is required and bounded.
const MaxRepetitions = 64

// RepeatedClassification is what the control plane calls one behavior across
// the repetitions. Never derived by a consumer.
type RepeatedClassification string

const (
	// RepeatedAdded: candidate_runs_present >= k and reference_runs_present <= j.
	RepeatedAdded RepeatedClassification = "added"
	// RepeatedRemoved: reference_runs_present >= k and candidate_runs_present <= j.
	// Classified and reported; it gates nothing (task 078 § Repeatedly removed).
	RepeatedRemoved RepeatedClassification = "removed"
	// RepeatedNeither is every other behavior, and every behavior is reported.
	RepeatedNeither RepeatedClassification = "neither"
)

// RepeatedEvaluationGateLimits are the caller-owned thresholds.
//
// Every field is required and none has a default — the guidance `k = 1,
// j = 0` is documented guidance, never a value this code supplies. Zero is a
// legitimate strict maximum for the three maxima, so there is no "unset".
type RepeatedEvaluationGateLimits struct {
	// AddedCandidatePresenceMinimum is k: 1 <= k <= N.
	AddedCandidatePresenceMinimum uint64
	// AddedReferencePresenceMaximum is j: 0 <= j < k.
	AddedReferencePresenceMaximum uint64

	// MaxRepeatedAddedBehaviors bounds repeatedly added **behavioral
	// identities** — the unit of max_added_behaviors, which is what makes
	// N = 1, k = 1, j = 0 reproduce task 056 exactly. A tool and the transport
	// child it calls are two identities here, as they are there; the counted
	// change unit of ADR 0052 is per pair and has no stable cross-repetition
	// key, so it is not aggregated (ADR 0053 § The unit).
	MaxRepeatedAddedBehaviors uint64

	// The two engine-evidence maxima, per run: compared against the worst
	// candidate repetition, never a sum or a mean.
	MaxBlockDecisionsPerRun           uint64
	MaxCriticalRiskObservationsPerRun uint64

	// Task 106's optional limits. Unlike the five above they may be omitted,
	// and omitted means the check is not evaluated (issue 131's precedent):
	//
	//	MinCandidateFrequency  every behavior in all N reference runs is in at
	//	                       least this many candidate runs (0..N)
	//	MaxLostBehaviors       at most this many behaviors are lost
	//	MaxCallsPerRun         per named target, at most this many calls in any
	//	                       one candidate run; nil is omitted (ADR 0066)
	MinCandidateFrequency OptionalGateLimit
	MaxLostBehaviors      OptionalGateLimit
	MaxCallsPerRun        []TargetCallLimit
}

// RepeatedEvaluationRequest names the 2N repetitions and the limits.
//
// Reference repetition i and candidate repetition i are not paired: the order
// within each side is correlation only, preserved into the result so a human
// can find the repetition a count came from.
type RepeatedEvaluationRequest struct {
	ReferenceRunIDs []EvaluationRunID
	CandidateRunIDs []EvaluationRunID
	Limits          RepeatedEvaluationGateLimits
}

// Runs is N.
func (r RepeatedEvaluationRequest) Runs() int { return len(r.ReferenceRunIDs) }

// Validate refuses a request that could never be evaluated, before any run is
// loaded.
func (r RepeatedEvaluationRequest) Validate() error {
	n := len(r.ReferenceRunIDs)
	if n < 1 || n > MaxRepetitions {
		return fmt.Errorf("%w: %d reference repetitions; runs must be within 1..%d",
			ErrInvalidRepeatedRequest, n, MaxRepetitions)
	}
	if len(r.CandidateRunIDs) != n {
		return fmt.Errorf("%w: %d reference and %d candidate repetitions; both sides "+
			"run the same number of times", ErrInvalidRepeatedRequest, n, len(r.CandidateRunIDs))
	}
	seen := make(map[EvaluationRunID]struct{}, 2*n)
	for _, id := range append(slices.Clone(r.ReferenceRunIDs), r.CandidateRunIDs...) {
		if err := validateID("repetition run id", string(id)); err != nil {
			return err
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("%w: run %s is named twice; every repetition is its own run",
				ErrInvalidRepeatedRequest, preview(string(id)))
		}
		seen[id] = struct{}{}
	}
	k, j := r.Limits.AddedCandidatePresenceMinimum, r.Limits.AddedReferencePresenceMaximum
	if k < 1 || k > uint64(n) {
		return fmt.Errorf("%w: added_candidate_presence_minimum (k) is %d; it must be "+
			"within 1..%d", ErrInvalidRepeatedRequest, k, n)
	}
	if j >= k {
		return fmt.Errorf("%w: added_reference_presence_maximum (j) is %d; it must be "+
			"below k (%d), or a behavior equally present on both sides would be added",
			ErrInvalidRepeatedRequest, j, k)
	}
	return validateFrequencyLimits(r.Limits, n)
}

// RepetitionEvidence is one repetition as the control plane found it.
//
// Engine evidence is retained per repetition, not only as a worst case, so a
// reader can see which repetition blocked.
type RepetitionEvidence struct {
	Side              ComparisonSide
	Index             int // 1-based position within its side; correlation only
	RunID             EvaluationRunID
	Status            RunStatus
	BehavioralProfile BehavioralProfileRef

	// Present only when Status is completed; zero otherwise.
	RecordCount              uint64
	DistinctBehaviors        int
	BlockDecisions           uint64
	CriticalRiskObservations uint64
}

// Completed reports whether this repetition contributed evidence.
func (r RepetitionEvidence) Completed() bool { return r.Status == RunCompleted }

// RepeatedBehaviorPresence is one behavioral identity across the repetitions.
type RepeatedBehaviorPresence struct {
	FingerprintID        string
	Behavior             trustvian.StableFeatures
	ReferenceRunsPresent uint64
	CandidateRunsPresent uint64
	Classification       RepeatedClassification

	// Task 106: how often, per side, over that side's completed repetitions.
	Reference, Candidate FrequencyStats

	// Lost is task 106's classification: present in all N reference
	// repetitions and missing from at least one candidate repetition.
	//
	// A field beside Classification rather than a fourth value of it. 078's
	// classification is a closed vocabulary that published consumers — the 079
	// renderer among them — refuse to extend, and a document they cannot read
	// would render no verdict at all. A behavior can be both removed and lost.
	Lost bool
}

// RepeatedCheckName is one of the six checks, spelled as the wire spells it.
type RepeatedCheckName string

const (
	CheckReferenceRepetitionsCompleted   RepeatedCheckName = "reference_repetitions_completed"
	CheckCandidateRepetitionsCompleted   RepeatedCheckName = "candidate_repetitions_completed"
	CheckRepetitionsFailingMinimum       RepeatedCheckName = "repetitions_failing_minimum_evidence"
	CheckRepeatedlyAddedBehaviors        RepeatedCheckName = "repeatedly_added_behaviors"
	CheckWorstCandidateBlockDecisions    RepeatedCheckName = "worst_candidate_block_decisions"
	CheckWorstCandidateCriticalRiskCount RepeatedCheckName = "worst_candidate_critical_risk_observations"
)

// RepeatedCheckRule is how a check compares its actual value with its bound.
type RepeatedCheckRule string

const (
	RuleEquals RepeatedCheckRule = "equals"
	RuleAtMost RepeatedCheckRule = "at_most"
)

// AdvisoryFreshScope marks checks 5 and 6 at N > 1: against per-repetition
// learning scopes the engine has learned nothing, so those checks cannot
// answer the learned-policy question. The marker changes no verdict.
const AdvisoryFreshScope = "fresh_scope"

// RepeatedGateCheck is one check's complete evidence.
type RepeatedGateCheck struct {
	Name     RepeatedCheckName
	Actual   uint64
	Rule     RepeatedCheckRule
	Bound    uint64
	Passed   bool
	Advisory string // "" or AdvisoryFreshScope
}

// RepeatedEvaluationGateResult is the six checks, in their stable order, and
// the verdict. Fixed-shape: always six, every one populated on every call.
type RepeatedEvaluationGateResult struct {
	bound   bool
	checks  [6]RepeatedGateCheck
	verdict GateVerdict

	// frequency is task 106's three optional checks, always all three, in
	// their stable order — not_evaluated when the limit was omitted.
	frequency [3]FrequencyGateCheck
}

// FrequencyChecks returns task 106's three checks in their stable order.
func (r RepeatedEvaluationGateResult) FrequencyChecks() []FrequencyGateCheck {
	out := slices.Clone(r.frequency[:])
	for i := range out {
		out[i].Targets = slices.Clone(out[i].Targets)
	}
	return out
}

// Checks returns all six checks in their stable order.
func (r RepeatedEvaluationGateResult) Checks() []RepeatedGateCheck {
	return slices.Clone(r.checks[:])
}

// Verdict is GateVerdictPass only when all six checks passed and no frequency
// check failed or was deferred.
func (r RepeatedEvaluationGateResult) Verdict() GateVerdict { return r.verdict }

// RepeatedEvaluationComparison is the authoritative repeated result.
type RepeatedEvaluationComparison struct {
	Runs        int
	Limits      RepeatedEvaluationGateLimits
	Repetitions []RepetitionEvidence // reference 1..N, then candidate 1..N
	Behaviors   []RepeatedBehaviorPresence
	Gate        RepeatedEvaluationGateResult

	// Operational is task 087's latency, errors and tokens sections, each side
	// summed over its completed repetitions, with how many of them carried
	// that section's evidence. Gates read none of it.
	Operational OperationalComparison

	// Cost is task 087's cost section over the completed repetitions, nil
	// when no pricing is configured.
	Cost *CostComparison

	// Targets is task 106's per-target frequency, ordered by target category
	// then name. Bounded by the execution-wide behavior bound.
	Targets []RepeatedTargetFrequency

	// LostTransitions is always LostTransitionsNotRecorded: nothing records
	// transitions per execution (task 106 § Lost transitions).
	LostTransitions string

	// Suggestions are the comparison rule table's outputs (ADR 0064), beside
	// the gate and read by nothing: no check, no verdict, no stored record.
	Suggestions          []Suggestion
	SuggestionsTruncated bool
}

// classifyPresence applies task 078's rule. j < k is validated before this.
func classifyPresence(referencePresent, candidatePresent, k, j uint64) RepeatedClassification {
	switch {
	case candidatePresent >= k && referencePresent <= j:
		return RepeatedAdded
	case referencePresent >= k && candidatePresent <= j:
		return RepeatedRemoved
	default:
		return RepeatedNeither
	}
}

// repetitionInput is one repetition's evidence, before reduction.
type repetitionInput struct {
	evidence RepetitionEvidence
	entries  []BehaviorEntry // nil unless completed
}

// reduceRepeated is the pure core: N repetitions per side in, the comparison
// out. Separate from the store so permutation invariance and the N = 1
// equivalence are testable over values.
func reduceRepeated(
	limits RepeatedEvaluationGateLimits, runs int, inputs []repetitionInput,
) (RepeatedEvaluationComparison, error) {
	type tally struct {
		behavior             trustvian.StableFeatures
		reference, candidate uint64
	}
	seen := make(map[string]*tally)
	// The reverse direction of the identity contract. A descriptor that
	// arrives under two fingerprints would split one behavior's presence
	// across two tallies, each below k, and a behavior every candidate run
	// added would pass a zero budget. CompareBehaviorSnapshots refuses that
	// across one pair; this refuses it across the execution.
	fingerprintOf := make(map[trustvian.StableFeatures]string)
	var (
		completed           = map[ComparisonSide]uint64{}
		failingMinimum      uint64
		worstBlock, worstCR uint64
	)
	repetitions := make([]RepetitionEvidence, 0, len(inputs))
	operational := map[ComparisonSide]*operationalSide{
		SideReference: {}, SideCandidate: {},
	}
	frequency := map[ComparisonSide]*frequencySide{
		SideReference: newFrequencySide(), SideCandidate: newFrequencySide(),
	}
	for _, in := range inputs {
		repetitions = append(repetitions, in.evidence)
		if !in.evidence.Completed() {
			continue
		}
		completed[in.evidence.Side]++
		if err := operational[in.evidence.Side].addRun(in.entries); err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		if err := frequency[in.evidence.Side].addRun(in.entries); err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		if in.evidence.RecordCount == 0 {
			failingMinimum++
		}
		if in.evidence.Side == SideCandidate {
			worstBlock = max(worstBlock, in.evidence.BlockDecisions)
			worstCR = max(worstCR, in.evidence.CriticalRiskObservations)
		}
		for _, entry := range in.entries {
			if entry.Observations == 0 {
				continue
			}
			t, ok := seen[entry.FingerprintID]
			if !ok {
				// The execution-wide bound, task 054's figure. The 513th
				// distinct identity makes the evidence incomplete, and the
				// gate refuses incomplete evidence rather than counting it.
				if len(seen) >= maxBehaviorEntries {
					return RepeatedEvaluationComparison{}, fmt.Errorf(
						"%w: more than %d distinct behaviors across the repetitions",
						ErrIncompleteSnapshot, maxBehaviorEntries)
				}
				t = &tally{behavior: entry.Behavior}
				seen[entry.FingerprintID] = t
			} else if t.behavior != entry.Behavior {
				return RepeatedEvaluationComparison{}, fmt.Errorf(
					"%w: fingerprint %s carries two descriptors across repetitions",
					ErrFingerprintConflict, preview(entry.FingerprintID))
			}
			if other, ok := fingerprintOf[entry.Behavior]; ok && other != entry.FingerprintID {
				return RepeatedEvaluationComparison{}, fmt.Errorf(
					"%w: behavior %s/%s is fingerprint %s in one repetition and %s in another",
					ErrFingerprintConflict,
					preview(string(entry.Behavior.OperationCategory)), preview(entry.Behavior.OperationName),
					preview(other), preview(entry.FingerprintID))
			}
			fingerprintOf[entry.Behavior] = entry.FingerprintID
			if in.evidence.Side == SideReference {
				t.reference++
			} else {
				t.candidate++
			}
		}
	}

	k, j := limits.AddedCandidatePresenceMinimum, limits.AddedReferencePresenceMaximum
	behaviors := make([]RepeatedBehaviorPresence, 0, len(seen))
	var added uint64
	for fp, t := range seen {
		class := classifyPresence(t.reference, t.candidate, k, j)
		if class == RepeatedAdded {
			added++
		}
		ref, err := frequency[SideReference].behaviorStats(fp)
		if err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		cand, err := frequency[SideCandidate].behaviorStats(fp)
		if err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		behaviors = append(behaviors, RepeatedBehaviorPresence{
			FingerprintID: fp, Behavior: t.behavior,
			ReferenceRunsPresent: t.reference, CandidateRunsPresent: t.candidate,
			Classification: class,
			Reference:      ref, Candidate: cand,
			Lost: isLost(t.reference, t.candidate, uint64(runs)),
		})
	}
	targets, err := targetFrequencies(frequency[SideReference], frequency[SideCandidate])
	if err != nil {
		return RepeatedEvaluationComparison{}, err
	}
	slices.SortFunc(behaviors, func(a, b RepeatedBehaviorPresence) int {
		switch {
		case a.FingerprintID < b.FingerprintID:
			return -1
		case a.FingerprintID > b.FingerprintID:
			return 1
		}
		return 0
	})

	n := uint64(runs)
	advisory := ""
	if runs > 1 {
		advisory = AdvisoryFreshScope
	}
	checks := [6]RepeatedGateCheck{
		equalsCheck(CheckReferenceRepetitionsCompleted, completed[SideReference], n),
		equalsCheck(CheckCandidateRepetitionsCompleted, completed[SideCandidate], n),
		atMostCheck(CheckRepetitionsFailingMinimum, failingMinimum, 0, ""),
		atMostCheck(CheckRepeatedlyAddedBehaviors, added, limits.MaxRepeatedAddedBehaviors, ""),
		atMostCheck(CheckWorstCandidateBlockDecisions, worstBlock, limits.MaxBlockDecisionsPerRun, advisory),
		atMostCheck(CheckWorstCandidateCriticalRiskCount, worstCR, limits.MaxCriticalRiskObservationsPerRun, advisory),
	}
	frequencyChecks := evaluateFrequencyGates(limits, frequencyGateInputs{
		runs: n, referenceRuns: completed[SideReference], candidateRuns: completed[SideCandidate],
		behaviors:       behaviors,
		referenceByName: frequency[SideReference].byName, candidByName: frequency[SideCandidate].byName,
	})
	verdict := GateVerdictPass
	for _, c := range checks {
		if !c.Passed {
			verdict = GateVerdictFail
		}
	}
	for _, c := range frequencyChecks {
		if c.failsVerdict() {
			verdict = GateVerdictFail
		}
	}
	limits.MaxCallsPerRun = cloneTargetLimits(limits.MaxCallsPerRun)
	comparison := RepeatedEvaluationComparison{
		Runs: runs, Limits: limits, Repetitions: repetitions, Behaviors: behaviors,
		Gate: RepeatedEvaluationGateResult{bound: true, checks: checks, verdict: verdict,
			frequency: frequencyChecks},
		Operational:     compareOperational(*operational[SideReference], *operational[SideCandidate]),
		Targets:         targets,
		LostTransitions: LostTransitionsNotRecorded,
	}
	// Last, over the finished comparison: the rules read evidence and the
	// gate never reads them.
	comparison.Suggestions, comparison.SuggestionsTruncated = evaluateComparisonRules(comparison)
	return comparison, nil
}

func equalsCheck(name RepeatedCheckName, actual, bound uint64) RepeatedGateCheck {
	return RepeatedGateCheck{Name: name, Actual: actual, Rule: RuleEquals, Bound: bound,
		Passed: actual == bound}
}

func atMostCheck(name RepeatedCheckName, actual, bound uint64, advisory string) RepeatedGateCheck {
	return RepeatedGateCheck{Name: name, Actual: actual, Rule: RuleAtMost, Bound: bound,
		Passed: actual <= bound, Advisory: advisory}
}

// CompareRepeatedEvaluations evaluates N isolated repetitions per side.
//
// Refused, never gated: a malformed request, a run that does not exist, runs
// from more than one project or environment, two repetitions sharing a
// learning scope, inconsistent fingerprint identity across repetitions, and
// incomplete behavioral evidence in any completed repetition. A repetition
// that is merely not completed is not refused: it fails check 1 or 2, so the
// result still shows everything that was measured.
func (c *ControlPlane) CompareRepeatedEvaluations(
	ctx context.Context, request RepeatedEvaluationRequest,
) (RepeatedEvaluationComparison, error) {
	if err := request.Validate(); err != nil {
		return RepeatedEvaluationComparison{}, err
	}

	type named struct {
		side  ComparisonSide
		index int
		id    EvaluationRunID
	}
	order := make([]named, 0, 2*request.Runs())
	for i, id := range request.ReferenceRunIDs {
		order = append(order, named{SideReference, i + 1, id})
	}
	for i, id := range request.CandidateRunIDs {
		order = append(order, named{SideCandidate, i + 1, id})
	}

	runs := make([]EvaluationRun, len(order))
	profiles := make(map[BehavioralProfileRef]EvaluationRunID, len(order))
	for i, n := range order {
		run, err := c.evaluations.EvaluationRun(ctx, n.id)
		if err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		if other, shared := profiles[run.BehavioralProfile()]; shared {
			return RepeatedEvaluationComparison{}, fmt.Errorf(
				"%w: runs %s and %s share behavioral profile %s",
				ErrRepeatedIsolation, preview(string(other)), preview(string(n.id)),
				preview(string(run.BehavioralProfile())))
		}
		profiles[run.BehavioralProfile()] = n.id
		if i > 0 {
			// One Environment, which is (ProjectID, EnvironmentRef), exactly
			// as CompareEvaluations requires of its pair. Environment is a
			// fingerprint dimension: a candidate repetition in another
			// environment would present each behavior under a fingerprint the
			// other repetitions never use, so presence would split below k
			// and a repeated addition would pass. Checked here, before any
			// evidence is loaded, so a run that ingested nothing is held to
			// it too.
			if run.Environment() != runs[0].Environment() {
				return RepeatedEvaluationComparison{}, fmt.Errorf(
					"%w: run %s is in environment %s, run %s in %s; every repetition "+
						"of a scenario must run in one environment",
					ErrBehaviorEnvironmentMismatch,
					preview(string(runs[0].ID())), preview(string(runs[0].Environment())),
					preview(string(n.id)), preview(string(run.Environment())))
			}
			if err := c.requireSameProject(ctx, runs[0], run); err != nil {
				return RepeatedEvaluationComparison{}, err
			}
		}
		runs[i] = run
	}

	inputs := make([]repetitionInput, 0, len(order))
	for i, n := range order {
		run := runs[i]
		evidence := RepetitionEvidence{
			Side: n.side, Index: n.index, RunID: run.ID(), Status: run.Status(),
			BehavioralProfile: run.BehavioralProfile(),
		}
		if run.Status() != RunCompleted {
			inputs = append(inputs, repetitionInput{evidence: evidence})
			continue
		}
		aggregate, snapshot, err := c.comparisonEvidence(ctx, run)
		if err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		input, err := completedRepetition(evidence, aggregate, snapshot)
		if err != nil {
			return RepeatedEvaluationComparison{}, err
		}
		inputs = append(inputs, input)
	}
	result, err := reduceRepeated(request.Limits, request.Runs(), inputs)
	if err != nil {
		return RepeatedEvaluationComparison{}, err
	}
	var reference, candidate [][]BehaviorEntry
	for _, in := range inputs {
		switch {
		case !in.evidence.Completed():
		case in.evidence.Side == SideReference:
			reference = append(reference, in.entries)
		default:
			candidate = append(candidate, in.entries)
		}
	}
	cost, priced, err := compareCost(c.pricing, reference, candidate)
	if err != nil {
		return RepeatedEvaluationComparison{}, err
	}
	if priced {
		result.Cost = &cost
	}
	return result, nil
}

// completedRepetition turns one completed run's persisted evidence into a
// reduction input. A snapshot that saturated is refused: a confident presence
// count from truncated evidence is the failure task 054 named the worst.
func completedRepetition(
	evidence RepetitionEvidence, aggregate EvaluationAggregate, snapshot BehaviorSnapshot,
) (repetitionInput, error) {
	if !snapshot.Complete() {
		return repetitionInput{}, fmt.Errorf(
			"%w: %s repetition %d (run %s) saturated its behavior snapshot",
			ErrIncompleteSnapshot, evidence.Side, evidence.Index, preview(string(evidence.RunID)))
	}
	evidence.RecordCount = aggregate.RecordCount()
	evidence.DistinctBehaviors = snapshot.DistinctBehaviorCount()
	evidence.BlockDecisions = aggregate.Decisions().Block
	evidence.CriticalRiskObservations = aggregate.Risks().Critical
	return repetitionInput{evidence: evidence, entries: snapshot.Entries()}, nil
}
