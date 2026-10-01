package main

// trustvian eval run — a behavioral scenario, N isolated repetitions per side
// (task 078, ADR 0053).
//
// A thin adapter, in exactly the sense ADR 0033 means. It parses and validates
// the scenario, executes 2N repetitions through `trustvian dev`, submits the 2N
// run identifiers to POST /v1/evaluations/compare-repeated, and renders what
// came back. It counts no presence, classifies no behavior, evaluates no check
// and composes no verdict: the control plane owns all of it, and the exit code
// is the server's verdict mapped through the contract `eval compare` already
// publishes.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/internal/buildinfo"
)

const evalRunUsage = `usage:
  trustvian eval run --scenario <file> [--collector-bin <path>]
                     [--api-url <url>] [--json]

Runs the scenario's reference and candidate sides runs: N times each, one
repetition at a time, each under its own run and its own learning scope, then
asks the control plane for the repeated verdict.

  0  gate PASS     1  gate FAIL     2  usage — the scenario, before anything runs
  3  operational — the control plane, the network, or a repetition that failed;
     a failed repetition stops the scenario and no verdict is produced`

// repetitionFunc runs one repetition and reports its exit status and whether
// its run was completed. In production it is composeAndRunResult — `trustvian
// dev`, in-process. Tests replace it, which is how isolation, sequencing and
// abort are asserted without a workload.
type repetitionFunc func(s streams, config devConfig) devResult

// scenarioRunner is one `eval run` invocation's collaborators.
type scenarioRunner struct {
	run         repetitionFunc
	executionID func(name string) string
	cliVersion  func() string
	// workloadStdout is the file every repetition's workload writes its
	// standard output to: this process's stderr, never its stdout.
	workloadStdout *os.File
}

func runEvalRun(s streams, args []string, timeout time.Duration) int {
	return scenarioRunner{
		run:            composeAndRunResult,
		executionID:    newExecutionID,
		cliVersion:     cliVersionString,
		workloadStdout: os.Stderr,
	}.main(s, args, timeout)
}

func (r scenarioRunner) main(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval run")
	common := registerCommonFlags(fs)
	scenarioPath := fs.String("scenario", "", "scenario file (required)")
	collectorBin := fs.String("collector-bin", "", "path to "+collectorBinary)
	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}
	if err := requireAll(fs, map[string]string{"scenario": *scenarioPath}); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}

	// Everything about the scenario is decided before anything runs: a
	// malformed file, a missing limit or an out-of-range k is a usage error,
	// and no repetition has started to be thrown away.
	scenario, err := config.LoadScenarioFile(*scenarioPath)
	if err != nil {
		return usageFailure(s, evalRunUsage, usageErrorf("%v", err))
	}

	// One control plane for every repetition, resolved once. dev would start
	// a private one per invocation otherwise, and 2N private databases have
	// nothing to compare across.
	client, err := resolveAPIURL(*common.apiURL, common.apiURLSet(), timeout)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalRunUsage, err)
		}
		return emitError(s, *common.json, err)
	}

	executionID := r.executionID(scenario.Name)
	runs := *scenario.Runs
	var referenceIDs, candidateIDs []string

	// The repetitions' own output — dev's banner, the workload's lines — goes
	// to stderr, so stdout carries only the result and --json stays parseable.
	// Both halves are needed: the streams carry what dev itself prints, and
	// workloadStdout what the workload prints, because a child process inherits
	// descriptors rather than this process's writers.
	repetitionStreams := streams{out: s.err, err: s.err}

	// Sequential, reference side then candidate side. Concurrent repetitions
	// of a workload talking to a shared service would measure contention
	// rather than the workload (task 078, open question 5).
	for _, side := range []struct {
		name string
		spec config.ScenarioSide
		into *[]string
	}{
		{"reference", scenario.Reference, &referenceIDs},
		{"candidate", scenario.Candidate, &candidateIDs},
	} {
		for i := 1; i <= runs; i++ {
			runID := fmt.Sprintf("%s-%s-%d", executionID, side.name, i)
			profile := fmt.Sprintf("%s-%s-p%d", executionID, side.name, i)
			fmt.Fprintf(s.err, "trustvian eval run: %s repetition %d of %d (run %s)\n",
				side.name, i, runs, runID)
			result := r.run(repetitionStreams, devConfig{
				command:           side.spec.Command,
				apiURL:            client.baseURL.String(),
				collectorBin:      *collectorBin,
				project:           scenario.Project,
				agent:             scenario.Agent,
				candidate:         side.spec.Candidate,
				environment:       scenario.Environment,
				runID:             runID,
				instrumentation:   instrumentationOrDefault(scenario.Instrumentation),
				behavioralProfile: profile,
				env:               side.spec.Env,
				workloadStdout:    r.workloadStdout,
			})
			if code := result.code; code != 0 {
				// One failed repetition ends the scenario. Its N is the one
				// the limits were written for; a verdict over fewer runs
				// would be a stricter or looser test than the author chose,
				// silently. And it is 3, never 1: a broken workload is not a
				// behavioral regression.
				return emitError(s, *common.json, operationalErrorf(
					"%s repetition %d of %d (run %s) exited %d; the scenario stopped "+
						"and no verdict was produced", side.name, i, runs, runID, code))
			}
			if !result.runCompleted {
				// The workload succeeded, but the control plane does not hold
				// the run as completed — dev has said why on stderr. Asking for
				// a verdict anyway would turn a bookkeeping failure into a
				// behavioral FAIL (check 1 or 2), and the remaining repetitions
				// would be measured for a scenario that can no longer pass. The
				// same outcome as a crashed repetition, for the same reason.
				return emitError(s, *common.json, operationalErrorf(
					"%s repetition %d of %d (run %s) succeeded but its run could not be "+
						"completed; the scenario stopped and no verdict was produced",
					side.name, i, runs, runID))
			}
			*side.into = append(*side.into, runID)
		}
	}

	gate := scenario.Gate
	result, err := client.post(context.Background(), compareRepeatedBody{
		ReferenceRunIDs: referenceIDs,
		CandidateRunIDs: candidateIDs,
		GateLimits: repeatedLimitsBody{
			AddedCandidatePresenceMinimum:     u64Text(*gate.AddedCandidatePresenceMinimum),
			AddedReferencePresenceMaximum:     u64Text(*gate.AddedReferencePresenceMaximum),
			MaxRepeatedAddedBehaviors:         u64Text(*gate.MaxRepeatedAddedBehaviors),
			MaxBlockDecisionsPerRun:           u64Text(*gate.MaxBlockDecisionsPerRun),
			MaxCriticalRiskObservationsPerRun: u64Text(*gate.MaxCriticalRiskObservationsPerRun),
		},
	}, "evaluations", "compare-repeated")
	if err != nil {
		return emitError(s, *common.json, err)
	}
	if err := checkStatus(result); err != nil {
		return emitError(s, *common.json, err)
	}
	if err := requireJSONBody(result.body); err != nil {
		return emitError(s, *common.json, err)
	}
	var comparison repeatedDTO
	if err := decodeJSON(result.body, &comparison); err != nil {
		return emitError(s, *common.json, err)
	}
	// Classified before anything is written, exactly as eval compare does: a
	// verdict the CLI does not recognize is not publishable CI evidence.
	exit, err := gateExitCode(comparison.Gate.Verdict)
	if err != nil {
		return emitError(s, *common.json, err)
	}

	document, err := json.Marshal(scenarioResultDocument{
		Version:     "1",
		Scenario:    scenarioIdentity{Name: scenario.Name, Runs: runs},
		ExecutionID: executionID,
		Producers: producers{
			CLIVersion:    r.cliVersion(),
			ServerVersion: comparison.Producer.ServerVersion,
		},
		// The server's response, unchanged in content: nothing it said is
		// renamed, dropped or recomputed on the way through. (Embedding it
		// compacts its whitespace; that is the only difference.)
		Comparison: json.RawMessage(result.body),
	})
	if err != nil {
		return emitError(s, *common.json, operationalErrorf("encoding the result: %v", err))
	}
	if err := emitSuccess(s, *common.json, document, func(w io.Writer) error {
		return renderScenarioResult(w, scenario.Name, executionID, comparison)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exit
}

func instrumentationOrDefault(mode string) string {
	if mode == "" {
		return string(modeAuto)
	}
	return mode
}

func u64Text(v uint64) string { return strconv.FormatUint(v, 10) }

// newExecutionID names one scenario execution. Correlation only: it reaches run
// identifiers and profile names, and no fingerprint input or record field.
func newExecutionID(name string) string {
	if len(name) > 40 {
		name = name[:40]
	}
	var random [4]byte
	_, _ = rand.Read(random[:])
	return fmt.Sprintf("scn-%s-%s-%s", name,
		time.Now().UTC().Format("20060102T150405"), hex.EncodeToString(random[:]))
}

func cliVersionString() string {
	info := buildinfo.Read()
	if info.Revision == "" {
		return info.Version
	}
	return info.Version + " " + info.Revision
}

// ---------------------------------------------------------------------
// Wire and result types
// ---------------------------------------------------------------------

type repeatedLimitsBody struct {
	AddedCandidatePresenceMinimum     string `json:"added_candidate_presence_minimum"`
	AddedReferencePresenceMaximum     string `json:"added_reference_presence_maximum"`
	MaxRepeatedAddedBehaviors         string `json:"max_repeated_added_behaviors"`
	MaxBlockDecisionsPerRun           string `json:"max_block_decisions_per_run"`
	MaxCriticalRiskObservationsPerRun string `json:"max_critical_risk_observations_per_run"`
}

type compareRepeatedBody struct {
	ReferenceRunIDs []string           `json:"reference_run_ids"`
	CandidateRunIDs []string           `json:"candidate_run_ids"`
	GateLimits      repeatedLimitsBody `json:"gate_limits"`
}

// repeatedDTO decodes only what the human rendering prints. --json publishes
// the server's body untouched inside the result document instead.
type repeatedDTO struct {
	Runs       int                `json:"runs"`
	GateLimits repeatedLimitsBody `json:"gate_limits"`
	Behaviors  []struct {
		FingerprintID        string             `json:"fingerprint_id"`
		Behavior             behaviorDescriptor `json:"behavior"`
		ReferenceRunsPresent string             `json:"reference_runs_present"`
		CandidateRunsPresent string             `json:"candidate_runs_present"`
		Classification       string             `json:"classification"`
	} `json:"behaviors"`
	Gate struct {
		Checks []struct {
			Name     string `json:"name"`
			Actual   string `json:"actual"`
			Rule     string `json:"rule"`
			Bound    string `json:"bound"`
			Passed   bool   `json:"passed"`
			Advisory string `json:"advisory"`
		} `json:"checks"`
		Verdict string `json:"verdict"`
	} `json:"gate"`
	Producer struct {
		ServerVersion string `json:"control_plane_version"`
	} `json:"producer"`
}

// scenarioResultDocument is task 078's result document: the runner's identity
// for the execution, both producers, and the control plane's repeated result
// verbatim under "comparison".
type scenarioResultDocument struct {
	Version     string           `json:"version"`
	Scenario    scenarioIdentity `json:"scenario"`
	ExecutionID string           `json:"execution_id"`
	Producers   producers        `json:"producers"`
	Comparison  json.RawMessage  `json:"comparison"`
}

type scenarioIdentity struct {
	Name string `json:"name"`
	Runs int    `json:"runs"`
}

type producers struct {
	CLIVersion    string `json:"cli_version"`
	ServerVersion string `json:"control_plane_version"`
}

// renderScenarioResult prints the server's result. Every number is a field of
// the response; this computes none of them.
func renderScenarioResult(w io.Writer, name, executionID string, c repeatedDTO) error {
	fmt.Fprintf(w, "%s   runs %d   (execution %s)\n\n", name, c.Runs, executionID)
	fmt.Fprintf(w, "Behavior                                  reference   candidate\n")
	for _, b := range c.Behaviors {
		label := describeBehavior(b.Behavior)
		if len(label) > 40 {
			label = label[:39] + "…"
		}
		marker := ""
		switch b.Classification {
		case "added":
			marker = "   + added"
		case "removed":
			marker = "   - removed"
		}
		fmt.Fprintf(w, "  %-40s %9s %11s%s\n", label,
			b.ReferenceRunsPresent+"/"+strconv.Itoa(c.Runs),
			b.CandidateRunsPresent+"/"+strconv.Itoa(c.Runs), marker)
	}
	fmt.Fprintf(w, "\nGate (k = %s, j = %s)\n", c.GateLimits.AddedCandidatePresenceMinimum,
		c.GateLimits.AddedReferencePresenceMaximum)
	for _, check := range c.Gate.Checks {
		rule := "<="
		if check.Rule == "equals" {
			rule = "=="
		}
		advisory := ""
		if check.Advisory != "" {
			advisory = "   advisory: " + strings.ReplaceAll(check.Advisory, "_", " ")
		}
		fmt.Fprintf(w, "  %s %s: %s (%s %s)%s\n", checkMark(check.Passed),
			strings.ReplaceAll(check.Name, "_", " "), check.Actual, rule, check.Bound, advisory)
	}
	fmt.Fprintf(w, "\nGate: %s\n", verdictLabel(c.Gate.Verdict))
	return nil
}
