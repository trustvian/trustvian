package main

// trustvian eval run — a behavioral scenario, N isolated repetitions per side
// (task 078, ADR 0053, ADR 0054).
//
// A thin adapter, in exactly the sense ADR 0033 means. It parses and validates
// the scenario, begins a persisted scenario execution, executes the
// repetitions through `trustvian dev`, submits their run identifiers to
// complete the execution, and renders what came back. It counts no presence,
// classifies no behavior, evaluates no check, composes no verdict and resolves
// no reference: the control plane owns all of it, and the exit code is the
// server's verdict mapped through the contract `eval compare` already
// publishes.
//
// With --reference, the control plane resolves a recorded execution before any
// workload runs, and only the N candidate repetitions execute: the reference
// side is that execution's, reused whole.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
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
  trustvian eval run --scenario <file> [--reference <execution-id>|last]
                     [--collector-bin <path>] [--api-url <url>] [--json]

Runs the scenario's reference and candidate sides runs: N times each, one
repetition at a time, each under its own run and its own learning scope, then
asks the control plane for the repeated verdict. Every execution is recorded.

--reference reuses a recorded execution's reference side instead of running
it: an execution id, or last — the most recently completed execution of this
scenario for the same project, agent and environment. Only the N candidate
repetitions run. The scenario's own gate limits apply.

  0  gate PASS     1  gate FAIL     2  usage — the scenario, before anything runs
  3  operational — the control plane, the network, a reference that is missing,
     unfinished, of another N or incomplete, or a repetition that failed; a
     failed repetition stops the scenario and no verdict is produced`

// referenceLast is the --reference value that asks for the most recent
// completed execution. Any other value names one execution.
const referenceLast = "last"

// repetitionFunc runs one repetition and reports its exit status and whether
// its run was completed. In production it is composeAndRunResult — `trustvian
// dev`, in-process. Tests replace it, which is how isolation, sequencing and
// abort are asserted without a workload.
type repetitionFunc func(s streams, config devConfig) devResult

// executionScope is where the candidate repetitions will run: what `last`
// matches on and what the control plane holds every candidate run to.
type executionScope struct {
	project, agent, environment string
}

// scopeFunc derives the execution scope from the candidate side's
// configuration, before anything runs.
type scopeFunc func(config devConfig) (executionScope, error)

// scenarioRunner is one `eval run` invocation's collaborators.
type scenarioRunner struct {
	run         repetitionFunc
	scope       scopeFunc
	executionID func(name string) string
	cliVersion  func() string
	// workloadStdout is the file every repetition's workload writes its
	// standard output to: this process's stderr, never its stdout.
	workloadStdout *os.File
}

func runEvalRun(s streams, args []string, timeout time.Duration) int {
	return scenarioRunner{
		run:            composeAndRunResult,
		scope:          deriveExecutionScope,
		executionID:    newExecutionID,
		cliVersion:     cliVersionString,
		workloadStdout: os.Stderr,
	}.main(s, args, timeout)
}

// deriveExecutionScope is the identity derivation every candidate repetition
// will perform, done once beforehand: the same function `trustvian dev` calls,
// over the same configuration, environment and working directory. It decides
// no evaluation question — it names where the runs will be — and the control
// plane checks every candidate run against it when the execution completes.
func deriveExecutionScope(config devConfig) (executionScope, error) {
	workloadDir, err := resolveWorkloadDir()
	if err != nil {
		return executionScope{}, operationalErrorf("%v", err)
	}
	identity, err := deriveIdentity(config, workloadDir, config.inheritedEnvironment(), time.Now())
	if err != nil {
		return executionScope{}, usageErrorf("%v", err)
	}
	return executionScope{
		project: identity.Project, agent: identity.Agent, environment: identity.Environment,
	}, nil
}

func (r scenarioRunner) main(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval run")
	common := registerCommonFlags(fs)
	scenarioPath := fs.String("scenario", "", "scenario file (required)")
	referenceFlag := fs.String("reference", "",
		"reuse a recorded execution's reference side: an execution id, or "+referenceLast)
	collectorBin := fs.String("collector-bin", "", "path to "+collectorBinary)
	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}
	if err := requireAll(fs, map[string]string{"scenario": *scenarioPath}); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}
	var reference *scenarioReferenceBody
	referenceSet := false
	fs.Visit(func(f *flag.Flag) { referenceSet = referenceSet || f.Name == "reference" })
	if referenceSet {
		switch value := strings.TrimSpace(*referenceFlag); value {
		case "":
			return usageFailure(s, evalRunUsage,
				usageErrorf("--reference needs an execution id or %q", referenceLast))
		case referenceLast:
			reference = &scenarioReferenceBody{Mode: referenceModeLast}
		default:
			reference = &scenarioReferenceBody{Mode: referenceModeExecution, ExecutionID: value}
		}
	}

	// Everything about the scenario is decided before anything runs: a
	// malformed file, a missing limit or an out-of-range k is a usage error,
	// and no repetition has started to be thrown away. The whole v1 file is
	// validated with --reference too, reference.command included: the file
	// does not become a different document because one invocation reuses a
	// recorded reference side.
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
	repetitionConfig := func(side config.ScenarioSide, runID, profile string) devConfig {
		return devConfig{
			command:           side.Command,
			apiURL:            client.baseURL.String(),
			collectorBin:      *collectorBin,
			project:           scenario.Project,
			agent:             scenario.Agent,
			candidate:         side.Candidate,
			environment:       scenario.Environment,
			runID:             runID,
			instrumentation:   instrumentationOrDefault(scenario.Instrumentation),
			behavioralProfile: profile,
			env:               side.Env,
			workloadStdout:    r.workloadStdout,
		}
	}

	scope, err := r.scope(repetitionConfig(scenario.Candidate, "", ""))
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalRunUsage, err)
		}
		return emitError(s, *common.json, err)
	}

	// Begin before anything runs. With --reference this is where the control
	// plane resolves and validates the recorded execution, so a missing,
	// unfinished, mismatched or incomplete reference costs no workload.
	begun, err := r.begin(client, beginExecutionBody{
		ID: executionID, ScenarioName: scenario.Name, Runs: runs,
		ProjectID: scope.project, AgentID: scope.agent, Environment: scope.environment,
		Reference: reference,
	})
	if err != nil {
		return emitError(s, *common.json, err)
	}

	// From here an execution exists, and every way out that is not a verdict
	// records it failed, so it can never be mistaken for a reference.
	abort := func(err error) int {
		r.fail(s, client, executionID)
		return emitError(s, *common.json, err)
	}
	if reference != nil {
		if begun.ReferenceExecution == nil {
			return abort(operationalErrorf(
				"the control plane began execution %s without naming its reference", executionID))
		}
		fmt.Fprintf(s.err, "trustvian eval run: reusing the reference side of execution %s; "+
			"only the candidate side runs\n", begun.ReferenceExecution.ID)
	}

	// The repetitions' own output — dev's banner, the workload's lines — goes
	// to stderr, so stdout carries only the result and --json stays parseable.
	// Both halves are needed: the streams carry what dev itself prints, and
	// workloadStdout what the workload prints, because a child process inherits
	// descriptors rather than this process's writers.
	repetitionStreams := streams{out: s.err, err: s.err}

	sides := []struct {
		name string
		spec config.ScenarioSide
	}{{"reference", scenario.Reference}, {"candidate", scenario.Candidate}}
	if reference != nil {
		sides = sides[1:]
	}
	var referenceIDs, candidateIDs []string

	// Sequential, reference side then candidate side. Concurrent repetitions
	// of a workload talking to a shared service would measure contention
	// rather than the workload (task 078, open question 5).
	for _, side := range sides {
		for i := 1; i <= runs; i++ {
			runID := fmt.Sprintf("%s-%s-%d", executionID, side.name, i)
			profile := fmt.Sprintf("%s-%s-p%d", executionID, side.name, i)
			fmt.Fprintf(s.err, "trustvian eval run: %s repetition %d of %d (run %s)\n",
				side.name, i, runs, runID)
			result := r.run(repetitionStreams, repetitionConfig(side.spec, runID, profile))
			if code := result.code; code != 0 {
				// One failed repetition ends the scenario. Its N is the one
				// the limits were written for; a verdict over fewer runs
				// would be a stricter or looser test than the author chose,
				// silently. And it is 3, never 1: a broken workload is not a
				// behavioral regression.
				return abort(operationalErrorf(
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
				return abort(operationalErrorf(
					"%s repetition %d of %d (run %s) succeeded but its run could not be "+
						"completed; the scenario stopped and no verdict was produced",
					side.name, i, runs, runID))
			}
			if side.name == "reference" {
				referenceIDs = append(referenceIDs, runID)
			} else {
				candidateIDs = append(candidateIDs, runID)
			}
		}
	}

	gate := scenario.Gate
	result, err := client.post(context.Background(), completeExecutionBody{
		// Empty with --reference: the control plane supplies the recorded
		// reference side itself, so this client cannot mix it with others.
		ReferenceRunIDs: referenceIDs,
		CandidateRunIDs: candidateIDs,
		GateLimits: repeatedLimitsBody{
			AddedCandidatePresenceMinimum:     u64Text(*gate.AddedCandidatePresenceMinimum),
			AddedReferencePresenceMaximum:     u64Text(*gate.AddedReferencePresenceMaximum),
			MaxRepeatedAddedBehaviors:         u64Text(*gate.MaxRepeatedAddedBehaviors),
			MaxBlockDecisionsPerRun:           u64Text(*gate.MaxBlockDecisionsPerRun),
			MaxCriticalRiskObservationsPerRun: u64Text(*gate.MaxCriticalRiskObservationsPerRun),
		},
	}, "scenario-executions", executionID, "complete")
	if err != nil {
		return abort(err)
	}
	if err := checkStatus(result); err != nil {
		return abort(err)
	}
	if err := requireJSONBody(result.body); err != nil {
		return abort(err)
	}
	var completed completeExecutionResponse
	if err := decodeJSON(result.body, &completed); err != nil {
		return abort(err)
	}
	if len(completed.Comparison) == 0 {
		return abort(operationalErrorf("the control plane completed execution %s without a comparison",
			executionID))
	}
	var comparison repeatedDTO
	if err := decodeJSON(completed.Comparison, &comparison); err != nil {
		return abort(err)
	}
	// Classified before anything is written, exactly as eval compare does: a
	// verdict the CLI does not recognize is not publishable CI evidence. The
	// execution is completed by now — the control plane recorded its verdict
	// — so an unrecognized one is reported, not failed.
	exit, err := gateExitCode(comparison.Gate.Verdict)
	if err != nil {
		return emitError(s, *common.json, err)
	}

	var provenance *referenceProvenance
	if reference != nil && begun.ReferenceExecution != nil {
		provenance = &referenceProvenance{
			Mode: reference.Mode, ExecutionID: begun.ReferenceExecution.ID,
		}
	}
	document, err := json.Marshal(scenarioResultDocument{
		Version:     "1",
		Scenario:    scenarioIdentity{Name: scenario.Name, Runs: runs},
		ExecutionID: executionID,
		Reference:   provenance,
		Producers: producers{
			CLIVersion:    r.cliVersion(),
			ServerVersion: comparison.Producer.ServerVersion,
		},
		// The server's comparison, unchanged in content: nothing it said is
		// renamed, dropped or recomputed on the way through. (Embedding it
		// compacts its whitespace; that is the only difference.)
		Comparison: completed.Comparison,
	})
	if err != nil {
		return emitError(s, *common.json, operationalErrorf("encoding the result: %v", err))
	}
	if err := emitSuccess(s, *common.json, document, func(w io.Writer) error {
		return renderScenarioResult(w, scenario.Name, executionID, provenance, comparison)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exit
}

// begin records the execution, and with a reference, has the control plane
// resolve it.
func (r scenarioRunner) begin(client *platformClient, body beginExecutionBody) (beginExecutionResponse, error) {
	result, err := client.post(context.Background(), body, "scenario-executions")
	if err != nil {
		return beginExecutionResponse{}, err
	}
	if err := checkStatus(result); err != nil {
		return beginExecutionResponse{}, err
	}
	if err := requireJSONBody(result.body); err != nil {
		return beginExecutionResponse{}, err
	}
	var begun beginExecutionResponse
	if err := decodeJSON(result.body, &begun); err != nil {
		return beginExecutionResponse{}, err
	}
	return begun, nil
}

// fail records the execution failed. Best effort: the original error is what
// the caller reports, and a failure to record the failure is said on stderr.
// An execution left running is never a reference either.
func (r scenarioRunner) fail(s streams, client *platformClient, executionID string) {
	result, err := client.post(context.Background(), struct{}{},
		"scenario-executions", executionID, "fail")
	if err == nil {
		err = checkStatus(result)
	}
	if err != nil {
		fmt.Fprintf(s.err, "trustvian eval run: WARNING: execution %s could not be recorded "+
			"failed (%v); it stays running and is never used as a reference\n", executionID, err)
	}
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

// Reference modes on the begin request.
const (
	referenceModeExecution = "execution"
	referenceModeLast      = "last"
)

type scenarioReferenceBody struct {
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id,omitempty"`
}

type beginExecutionBody struct {
	ID           string                 `json:"id"`
	ScenarioName string                 `json:"scenario_name"`
	Runs         int                    `json:"runs"`
	ProjectID    string                 `json:"project_id"`
	AgentID      string                 `json:"agent_id"`
	Environment  string                 `json:"environment"`
	Reference    *scenarioReferenceBody `json:"reference,omitempty"`
}

// executionSummary decodes only what the runner reports about an execution.
type executionSummary struct {
	ID string `json:"id"`
}

type beginExecutionResponse struct {
	Execution          executionSummary  `json:"execution"`
	ReferenceExecution *executionSummary `json:"reference_execution"`
}

type completeExecutionBody struct {
	ReferenceRunIDs []string           `json:"reference_run_ids,omitempty"`
	CandidateRunIDs []string           `json:"candidate_run_ids"`
	GateLimits      repeatedLimitsBody `json:"gate_limits"`
}

// completeExecutionResponse keeps the comparison raw, so the result document
// can carry it unchanged.
type completeExecutionResponse struct {
	Execution  executionSummary `json:"execution"`
	Comparison json.RawMessage  `json:"comparison"`
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
	// Reference is present only when the reference side was reused: the
	// mode asked for and the execution it resolved to. execution_id stays
	// this invocation's own.
	Reference  *referenceProvenance `json:"reference,omitempty"`
	Producers  producers            `json:"producers"`
	Comparison json.RawMessage      `json:"comparison"`
}

type referenceProvenance struct {
	Mode        string `json:"mode"`
	ExecutionID string `json:"execution_id"`
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
func renderScenarioResult(w io.Writer, name, executionID string,
	reference *referenceProvenance, c repeatedDTO) error {
	fmt.Fprintf(w, "%s   runs %d   (execution %s)\n", name, c.Runs, executionID)
	if reference != nil {
		fmt.Fprintf(w, "Reference side reused from execution %s (%s)\n", reference.ExecutionID,
			reference.Mode)
	}
	fmt.Fprintln(w)
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
