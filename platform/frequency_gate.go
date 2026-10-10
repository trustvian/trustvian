package platform

// The optional frequency gates: task 106's three (ADR 0029 § 7, ADR 0066), and
// task 081's max_llm_calls_per_run, which reads the layer counts 081 persists.
//
// Each follows issue 131's OptionalGateLimit precedent: omitted means
// not_evaluated, and the verdict is then exactly what it was without the limit.
// Present means evaluated — or deferred, when the evidence the limit needs is
// absent. A deferred check is never computed from a substitute, names what was
// missing, and fails the verdict (maintainer decision D1): a developer who
// configured a limit asked a question, and PASS would answer it without
// evidence.
//
// None of them reads a ratio or a mean. They read presence counts and per-run
// maxima, the integers ADR 0029 allows a gate.

import (
	"fmt"
	"slices"
)

// MaxCallsPerRunTargets bounds max_calls_per_run (ADR 0066).
const MaxCallsPerRunTargets = 16

// maxCallTargetLength bounds one target name in max_calls_per_run, in bytes.
const maxCallTargetLength = 255

// GateCheckDeferred means the caller supplied the limit and the evidence it
// needs is absent. The check did not run, and it fails the verdict (D1).
const GateCheckDeferred GateCheckState = "deferred"

// FrequencyCheckName is one of task 106's checks, as the wire spells it.
type FrequencyCheckName string

const (
	CheckMinCandidateFrequency FrequencyCheckName = "min_candidate_frequency"
	CheckMaxLostBehaviors      FrequencyCheckName = "max_lost_behaviors"
	CheckMaxCallsPerRun        FrequencyCheckName = "max_calls_per_run"
	CheckMaxLLMCallsPerRun     FrequencyCheckName = "max_llm_calls_per_run"
)

// FrequencyCheckCount is how many optional checks a gate always reports.
const FrequencyCheckCount = 4

// RuleAtLeast is a check passing when its actual value is at least its bound.
const RuleAtLeast RepeatedCheckRule = "at_least"

// TargetCallLimit is one entry of max_calls_per_run: the most calls one
// candidate repetition may make to the named target. The target is a target
// name, matched across categories; its semantics are fixed and approved
// (ADR 0066) and only the key is configurable.
type TargetCallLimit struct {
	Target string
	Max    uint64
}

// TargetCallOutcome is one target's result within max_calls_per_run.
type TargetCallOutcome string

const (
	TargetCallPass TargetCallOutcome = "pass"
	TargetCallFail TargetCallOutcome = "fail"
	// TargetCallNotObserved means neither side called the target at all. It
	// fails the check: a misspelled target would otherwise pass silently.
	TargetCallNotObserved TargetCallOutcome = "not_observed"
)

// TargetCallCheck is one target's evidence, in configuration order.
type TargetCallCheck struct {
	Target  string
	Actual  uint64 // the most calls any candidate repetition made; 0 when not observed
	Max     uint64
	Outcome TargetCallOutcome
}

// FrequencyGateCheck is one of the three checks. Actual, Bound, Rule and
// Passed mean something only when State is evaluated; MissingEvidence only
// when it is deferred.
type FrequencyGateCheck struct {
	Name  FrequencyCheckName
	State GateCheckState

	Rule   RepeatedCheckRule
	Actual uint64
	Bound  uint64
	Passed bool

	// Targets is max_calls_per_run's per-target evidence.
	Targets []TargetCallCheck

	MissingEvidence string
}

// failsVerdict reports whether this check makes the verdict FAIL.
func (c FrequencyGateCheck) failsVerdict() bool {
	switch c.State {
	case GateCheckDeferred:
		return true
	case GateCheckEvaluated:
		return !c.Passed
	default:
		return false
	}
}

// validateFrequencyLimits refuses limits that could never be evaluated.
func validateFrequencyLimits(l RepeatedEvaluationGateLimits, runs int) error {
	if minimum, set := l.MinCandidateFrequency.Maximum(); set && minimum > uint64(runs) {
		return fmt.Errorf("%w: min_candidate_frequency is %d; a behavior can be present in at most %d runs",
			ErrInvalidRepeatedRequest, minimum, runs)
	}
	if l.MaxCallsPerRun == nil {
		return nil
	}
	if len(l.MaxCallsPerRun) == 0 || len(l.MaxCallsPerRun) > MaxCallsPerRunTargets {
		return fmt.Errorf("%w: max_calls_per_run lists %d targets; it must list 1..%d",
			ErrInvalidRepeatedRequest, len(l.MaxCallsPerRun), MaxCallsPerRunTargets)
	}
	seen := make(map[string]struct{}, len(l.MaxCallsPerRun))
	for i, entry := range l.MaxCallsPerRun {
		if entry.Target == "" || len(entry.Target) > maxCallTargetLength {
			return fmt.Errorf("%w: max_calls_per_run[%d].target must be 1..%d bytes",
				ErrInvalidRepeatedRequest, i, maxCallTargetLength)
		}
		if err := validateText(ErrInvalidRepeatedRequest, "max_calls_per_run target", entry.Target); err != nil {
			return err
		}
		if _, dup := seen[entry.Target]; dup {
			return fmt.Errorf("%w: max_calls_per_run names target %s twice",
				ErrInvalidRepeatedRequest, preview(entry.Target))
		}
		seen[entry.Target] = struct{}{}
	}
	return nil
}

// frequencyGateInputs is what the checks read, from the reduction.
type frequencyGateInputs struct {
	runs                          uint64
	referenceRuns, candidateRuns  uint64
	behaviors                     []RepeatedBehaviorPresence
	referenceByName, candidByName map[string]*frequencyAccumulator

	// candidateLayers is one entry per completed candidate run, in order.
	candidateLayers []runLayerEvidence
}

// runLayerEvidence is what max_llm_calls_per_run reads from one completed
// candidate run.
type runLayerEvidence struct {
	runID EvaluationRunID
	// semantic reports whether any observation was semantically named.
	semantic bool
	// modelCalls is the run's model-layer observations.
	modelCalls uint64
}

// layerEvidenceOf sums one run's entries. Overflow is an error.
func layerEvidenceOf(runID EvaluationRunID, entries []BehaviorEntry) (runLayerEvidence, error) {
	out := runLayerEvidence{runID: runID}
	for _, e := range entries {
		out.semantic = out.semantic || e.Fidelity.Semantic > 0
		if err := addCount(&out.modelCalls, e.Fidelity.LayerModel, "model calls in one run"); err != nil {
			return runLayerEvidence{}, err
		}
	}
	return out, nil
}

func evaluateFrequencyGates(l RepeatedEvaluationGateLimits, in frequencyGateInputs) [FrequencyCheckCount]FrequencyGateCheck {
	missingSide := func() string {
		switch {
		case in.referenceRuns == 0 && in.candidateRuns == 0:
			return "no completed repetition on either side"
		case in.referenceRuns == 0:
			return "no completed reference repetition"
		case in.candidateRuns == 0:
			return "no completed candidate repetition"
		}
		return ""
	}

	checks := [FrequencyCheckCount]FrequencyGateCheck{
		{Name: CheckMinCandidateFrequency, State: GateCheckNotEvaluated},
		{Name: CheckMaxLostBehaviors, State: GateCheckNotEvaluated},
		{Name: CheckMaxCallsPerRun, State: GateCheckNotEvaluated},
		{Name: CheckMaxLLMCallsPerRun, State: GateCheckNotEvaluated},
	}

	if minimum, set := l.MinCandidateFrequency.Maximum(); set {
		c := &checks[0]
		lowest, qualifying := uint64(0), false
		for _, b := range in.behaviors {
			if b.ReferenceRunsPresent != in.runs {
				continue
			}
			if !qualifying || b.CandidateRunsPresent < lowest {
				lowest = b.CandidateRunsPresent
			}
			qualifying = true
		}
		switch missing := missingSide(); {
		case missing != "":
			c.State, c.MissingEvidence = GateCheckDeferred, missing
		case !qualifying:
			c.State, c.MissingEvidence = GateCheckDeferred, "no behavior was present in every reference run"
		default:
			c.State, c.Rule, c.Actual, c.Bound = GateCheckEvaluated, RuleAtLeast, lowest, minimum
			c.Passed = lowest >= minimum
		}
	}

	if maximum, set := l.MaxLostBehaviors.Maximum(); set {
		c := &checks[1]
		if missing := missingSide(); missing != "" {
			c.State, c.MissingEvidence = GateCheckDeferred, missing
		} else {
			var lost uint64
			for _, b := range in.behaviors {
				if b.Lost {
					lost++
				}
			}
			c.State, c.Rule, c.Actual, c.Bound = GateCheckEvaluated, RuleAtMost, lost, maximum
			c.Passed = lost <= maximum
		}
	}

	if l.MaxCallsPerRun != nil {
		c := &checks[2]
		if in.candidateRuns == 0 {
			c.State, c.MissingEvidence = GateCheckDeferred, "no completed candidate repetition"
		} else {
			c.State, c.Rule, c.Passed = GateCheckEvaluated, RuleAtMost, true
			for _, limit := range l.MaxCallsPerRun {
				check := TargetCallCheck{Target: limit.Target, Max: limit.Max}
				candidate, called := in.candidByName[limit.Target]
				_, referenced := in.referenceByName[limit.Target]
				switch {
				case !called && !referenced:
					check.Outcome = TargetCallNotObserved
				default:
					if called {
						check.Actual = candidate.max
					}
					check.Outcome = TargetCallPass
					if check.Actual > limit.Max {
						check.Outcome = TargetCallFail
					}
				}
				if check.Outcome != TargetCallPass {
					c.Passed = false
				}
				c.Targets = append(c.Targets, check)
			}
		}
	}
	// max_llm_calls_per_run (task 081, decision D8): the most model-layer
	// calls any one candidate run made. Deferred unless every candidate run
	// has at least one semantically named observation. A run without one —
	// a producer with no GenAI or OpenInference instrumentation, or a run
	// recorded before schema 13 — cannot show its model calls at all, and
	// reading its 0 as "no model calls" would pass vacuously. With semantic
	// evidence in the run, 0 is a real 0.
	if maximum, set := l.MaxLLMCallsPerRun.Maximum(); set {
		c := &checks[3]
		var (
			worst   uint64
			lacking uint64
			first   EvaluationRunID
		)
		for _, run := range in.candidateLayers {
			if !run.semantic {
				// The lowest identifier, not the first in request order:
				// the evidence a check names must not depend on how the
				// runs were listed.
				if lacking == 0 || run.runID < first {
					first = run.runID
				}
				lacking++
				continue
			}
			worst = max(worst, run.modelCalls)
		}
		switch {
		case lacking > 0:
			c.State = GateCheckDeferred
			c.MissingEvidence = "run " + preview(string(first)) +
				" has no semantically named observation; model calls cannot be counted"
			if lacking > 1 {
				c.MissingEvidence += fmt.Sprintf(" (%d of %d candidate runs have none)", lacking, len(in.candidateLayers))
			}
		case in.candidateRuns == 0:
			c.State, c.MissingEvidence = GateCheckDeferred, "no completed candidate repetition"
		default:
			c.State, c.Rule, c.Actual, c.Bound = GateCheckEvaluated, RuleAtMost, worst, maximum
			c.Passed = worst <= maximum
		}
	}
	return checks
}

// cloneTargetLimits copies a limit list, so a result holds its own.
func cloneTargetLimits(l []TargetCallLimit) []TargetCallLimit {
	if l == nil {
		return nil
	}
	return slices.Clone(l)
}
