package main

// trustvian eval compare-repeated: POST /v1/evaluations/compare-repeated
// (task 078's route, reachable from the CLI since task 086).
//
// `eval run` reaches the repeated comparison through a scenario execution;
// this command asks it about any runs, such as two executions' candidate
// sides. It prints the response body unchanged in both output modes — the
// sameness block included, exactly as the server returned it — and reads one
// field, gate.verdict, for its exit code, as `eval compare` does.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// targetLimitList is the repeatable --max-calls-per-run flag: target=max.
type targetLimitList []targetLimitBody

func (l *targetLimitList) String() string { return "" }

func (l *targetLimitList) Set(v string) error {
	at := strings.LastIndex(v, "=")
	if at <= 0 {
		return fmt.Errorf("must be <target>=<max>")
	}
	var maximum optionalUint64
	if err := maximum.Set(v[at+1:]); err != nil {
		return fmt.Errorf("the maximum %w", err)
	}
	*l = append(*l, targetLimitBody{Target: v[:at], Max: maximum.canonical()})
	return nil
}

type compareRepeatedBody struct {
	ReferenceRunIDs []string           `json:"reference_run_ids"`
	CandidateRunIDs []string           `json:"candidate_run_ids"`
	GateLimits      repeatedLimitsBody `json:"gate_limits"`
}

func runEvalCompareRepeated(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval compare-repeated")
	common := registerCommonFlags(fs)
	var references, candidates runIDList
	fs.Var(&references, "reference-run", "reference evaluation run (required; repeat once per repetition)")
	fs.Var(&candidates, "candidate-run", "candidate evaluation run (required; repeat once per repetition)")
	var k, j, added, block, critical, minFrequency, maxLost, maxLLM optionalUint64
	fs.Var(&k, "added-candidate-presence-minimum", "k (required)")
	fs.Var(&j, "added-reference-presence-maximum", "j (required)")
	fs.Var(&added, "max-repeated-added-behaviors", "maximum repeatedly added behaviors (required)")
	fs.Var(&block, "max-block-decisions-per-run", "maximum block decisions in any candidate run (required)")
	fs.Var(&critical, "max-critical-risk-observations-per-run",
		"maximum critical-risk observations in any candidate run (required)")
	fs.Var(&minFrequency, "min-candidate-frequency", "optional; omitted, the check is not evaluated")
	fs.Var(&maxLost, "max-lost-behaviors", "optional; omitted, the check is not evaluated")
	fs.Var(&maxLLM, "max-llm-calls-per-run",
		"optional; model-layer calls in any one candidate run. Deferred unless every candidate run has a model-layer observation")
	var calls targetLimitList
	fs.Var(&calls, "max-calls-per-run", "optional <target>=<max>; repeat once per target")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if len(references) == 0 || len(candidates) == 0 {
		return usageFailure(s, evalUsage, usageErrorf("--reference-run and --candidate-run are required"))
	}
	// Every required limit, and omitted is not zero, as for eval compare.
	for _, limit := range []struct {
		name  string
		value *optionalUint64
	}{
		{"added-candidate-presence-minimum", &k},
		{"added-reference-presence-maximum", &j},
		{"max-repeated-added-behaviors", &added},
		{"max-block-decisions-per-run", &block},
		{"max-critical-risk-observations-per-run", &critical},
	} {
		if !limit.value.set {
			return usageFailure(s, evalUsage, usageErrorf(
				"--%s is required; zero is a strict limit, not a default", limit.name))
		}
	}
	body := compareRepeatedBody{
		// Forwarded in the order given; which runs can be compared is the
		// control plane's knowledge.
		ReferenceRunIDs: references, CandidateRunIDs: candidates,
		GateLimits: repeatedLimitsBody{
			AddedCandidatePresenceMinimum:     k.canonical(),
			AddedReferencePresenceMaximum:     j.canonical(),
			MaxRepeatedAddedBehaviors:         added.canonical(),
			MaxBlockDecisionsPerRun:           block.canonical(),
			MaxCriticalRiskObservationsPerRun: critical.canonical(),
			MinCandidateFrequency:             minFrequency.optional(),
			MaxLostBehaviors:                  maxLost.optional(),
			MaxLLMCallsPerRun:                 maxLLM.optional(),
			MaxCallsPerRun:                    calls,
		},
	}

	client, err := resolveAPIURL(*common.apiURL, common.apiURLSet(), timeout)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalUsage, err)
		}
		return emitError(s, *common.json, err)
	}
	result, err := client.post(context.Background(), body, "evaluations", "compare-repeated")
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalUsage, err)
		}
		return emitError(s, *common.json, err)
	}
	if err := checkStatus(result); err != nil {
		return emitError(s, *common.json, err)
	}
	if err := requireJSONBody(result.body); err != nil {
		return emitError(s, *common.json, err)
	}
	var comparison struct {
		Gate struct {
			Verdict string `json:"verdict"`
		} `json:"gate"`
	}
	if err := decodeJSON(result.body, &comparison); err != nil {
		return emitError(s, *common.json, err)
	}
	// Classified before anything is written, as eval compare does.
	exit, err := gateExitCode(comparison.Gate.Verdict)
	if err != nil {
		return emitError(s, *common.json, err)
	}
	if err := emitSuccess(s, *common.json, result.body, func(w io.Writer) error {
		return writeStatusDocument(w, result.body)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exit
}
