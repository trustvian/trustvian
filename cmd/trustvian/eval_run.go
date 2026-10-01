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
	"errors"
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
  trustvian eval run --suite <directory> --scenario-timeout <duration>
                     [--fail-fast] [--reference last]
                     [--collector-bin <path>] [--api-url <url>] [--json]

Runs the scenario's reference and candidate sides runs: N times each, one
repetition at a time, each under its own run and its own learning scope, then
asks the control plane for the repeated verdict. Every execution is recorded.

--reference reuses a recorded execution's reference side instead of running
it: an execution id, or last — the most recently completed execution of this
scenario for the same project, agent and environment. Only the N candidate
repetitions run. The scenario's own gate limits apply.

--suite runs every .yaml and .yml file directly inside a directory, in
filename order, one scenario at a time, each within --scenario-timeout (1s to
24h). Every file is validated first. A failing scenario does not stop the
others unless --fail-fast is given; skipped scenarios are reported as skipped.
With a suite, --reference accepts only last, resolved for each scenario.

  0  gate PASS     1  gate FAIL     2  usage — the scenario, before anything runs
  3  operational — the control plane, the network, a reference that is missing,
     unfinished, of another N or incomplete, a repetition that failed, or a
     scenario that ran out of time; no verdict is produced for it
  A suite exits with the most severe of its scenarios: 3, then 2, then 1, then 0.`

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
	// suiteContext is a suite's cancellation: SIGINT and SIGTERM in
	// production. Tests cancel it directly.
	suiteContext func() (context.Context, context.CancelFunc)
	// outputLimit caps the encoded suite document; zero means
	// maxSuiteDocumentBytes. Tests lower it to reach the overflow.
	outputLimit int
}

func runEvalRun(s streams, args []string, timeout time.Duration) int {
	return scenarioRunner{
		run:            composeAndRunResult,
		scope:          deriveExecutionScope,
		executionID:    newExecutionID,
		cliVersion:     cliVersionString,
		workloadStdout: os.Stderr,
		suiteContext:   suiteSignalContext,
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

// Why a scenario has no verdict, for the runner's own failures. Wrapped in
// operational errors, so the exit code stays 3; a suite reads them to name the
// failure in its document.
var (
	errRepetitionFailed  = errors.New("a repetition failed")
	errRunNotCompleted   = errors.New("a run could not be completed")
	errScenarioTimeout   = errors.New("the scenario deadline passed")
	errScenarioCancelled = errors.New("the scenario was cancelled")
)

// scenarioCleanupTimeout bounds the best-effort request that records an
// execution failed. Its own context, never the scenario's: by then the
// scenario's may be the deadline that expired.
const scenarioCleanupTimeout = 10 * time.Second

// scenarioJob is one scenario ready to run: its validated file, and the scope
// its candidate repetitions will run in.
type scenarioJob struct {
	scenario config.ScenarioConfig
	scope    executionScope
}

// scenarioOutcome is how one scenario ended.
type scenarioOutcome struct {
	// exit is the scenario's exit code: 0 or 1 with a verdict, 2 or 3 without.
	exit int
	// document is the result document, present exactly when there is a
	// verdict.
	document []byte
	// err says why there is no verdict.
	err error
	// executionID names the recorded execution, once one was begun.
	executionID string
	render      func(io.Writer) error
}

func (r scenarioRunner) main(s streams, args []string, timeout time.Duration) int {
	// Every repetition is a `trustvian dev` session, so eval run refuses
	// exactly where dev does: a platform that cannot stop what it starts
	// cannot honour a scenario deadline either.
	if err := devPlatformSupported(); err != nil {
		fmt.Fprintf(s.err, "trustvian eval run: %v\n", err)
		return exitUsage
	}
	fs := newFlagSet("eval run")
	common := registerCommonFlags(fs)
	scenarioPath := fs.String("scenario", "", "scenario file")
	suitePath := fs.String("suite", "", "directory of scenario files")
	scenarioTimeout := fs.String("scenario-timeout", "",
		"each suite scenario's deadline, 1s to 24h (required with --suite)")
	failFast := fs.Bool("fail-fast", false, "with --suite: stop scheduling after the first scenario that is not a PASS")
	referenceFlag := fs.String("reference", "",
		"reuse a recorded execution's reference side: an execution id, or "+referenceLast)
	collectorBin := fs.String("collector-bin", "", "path to "+collectorBinary)
	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	switch {
	case set["scenario"] && set["suite"]:
		return usageFailure(s, evalRunUsage, usageErrorf("--scenario and --suite are mutually exclusive"))
	case set["suite"]:
		return r.suiteMain(s, common, suiteOptions{
			directory: *suitePath, timeoutFlag: *scenarioTimeout, timeoutSet: set["scenario-timeout"],
			failFast: *failFast, reference: *referenceFlag, referenceSet: set["reference"],
			collectorBin: *collectorBin,
		}, timeout)
	case set["scenario-timeout"] || set["fail-fast"]:
		return usageFailure(s, evalRunUsage,
			usageErrorf("--scenario-timeout and --fail-fast apply only to --suite"))
	}
	if err := requireAll(fs, map[string]string{"scenario": *scenarioPath}); err != nil {
		return usageFailure(s, evalRunUsage, err)
	}
	reference, err := parseReferenceFlag(*referenceFlag, set["reference"], true)
	if err != nil {
		return usageFailure(s, evalRunUsage, err)
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

	scope, err := r.scope(repetitionConfig(scenario, scenario.Candidate, "", *collectorBin, "", "", r.workloadStdout))
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalRunUsage, err)
		}
		return emitError(s, *common.json, err)
	}

	outcome := r.execute(context.Background(), s, client, scenarioJob{scenario: scenario, scope: scope},
		reference, *collectorBin)
	if outcome.err != nil {
		return emitError(s, *common.json, outcome.err)
	}
	if err := emitSuccess(s, *common.json, outcome.document, outcome.render); err != nil {
		return emitError(s, *common.json, err)
	}
	return outcome.exit
}

// parseReferenceFlag reads --reference. allowExplicit is false for a suite,
// where only last has a meaning for every scenario at once.
func parseReferenceFlag(value string, set, allowExplicit bool) (*scenarioReferenceBody, error) {
	if !set {
		return nil, nil
	}
	switch value = strings.TrimSpace(value); value {
	case "":
		return nil, usageErrorf("--reference needs an execution id or %q", referenceLast)
	case referenceLast:
		return &scenarioReferenceBody{Mode: referenceModeLast}, nil
	}
	if !allowExplicit {
		return nil, usageErrorf("with --suite, --reference accepts only %q: one execution id "+
			"cannot be every scenario's reference", referenceLast)
	}
	return &scenarioReferenceBody{Mode: referenceModeExecution, ExecutionID: value}, nil
}

// repetitionConfig is one repetition's `trustvian dev` configuration.
func repetitionConfig(scenario config.ScenarioConfig, side config.ScenarioSide,
	apiURL, collectorBin, runID, profile string, workloadStdout *os.File) devConfig {
	return devConfig{
		command:           side.Command,
		apiURL:            apiURL,
		collectorBin:      collectorBin,
		project:           scenario.Project,
		agent:             scenario.Agent,
		candidate:         side.Candidate,
		environment:       scenario.Environment,
		runID:             runID,
		instrumentation:   instrumentationOrDefault(scenario.Instrumentation),
		behavioralProfile: profile,
		env:               side.Env,
		workloadStdout:    workloadStdout,
	}
}

// stopReason names why ctx ended a scenario: its deadline, or cancellation.
func stopReason(ctx context.Context, name string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return operationalErrorf("%w: scenario %s did not finish before its deadline; "+
			"no verdict was produced", errScenarioTimeout, name)
	}
	return operationalErrorf("%w: scenario %s was stopped before it finished; "+
		"no verdict was produced", errScenarioCancelled, name)
}

// execute runs one scenario to its outcome: begin the execution, run the
// repetitions, complete it. Single-scenario mode and every suite member run
// through here, so they cannot differ.
//
// ctx bounds the whole scenario. A suite gives it a deadline and its
// cancellation; single-scenario mode gives it none, and nothing about
// `trustvian dev`'s signal handling changes. When ctx ends, no further
// repetition starts, a running workload's process group is terminated, and
// the execution is recorded failed — operationally, whatever any workload
// exited with.
func (r scenarioRunner) execute(ctx context.Context, s streams, client *platformClient,
	job scenarioJob, reference *scenarioReferenceBody, collectorBin string) scenarioOutcome {
	scenario, scope := job.scenario, job.scope
	executionID := r.executionID(scenario.Name)
	runs := *scenario.Runs
	stopped := func(err error) scenarioOutcome {
		return scenarioOutcome{exit: exitCodeFor(err), err: err}
	}

	// Begin before anything runs. With --reference this is where the control
	// plane resolves and validates the recorded execution, so a missing,
	// unfinished, mismatched or incomplete reference costs no workload.
	begun, err := r.begin(ctx, client, beginExecutionBody{
		ID: executionID, ScenarioName: scenario.Name, Runs: runs,
		ProjectID: scope.project, AgentID: scope.agent, Environment: scope.environment,
		Reference: reference,
	})
	if err != nil {
		if ctx.Err() != nil {
			// The begin may or may not have landed; recording a failure is
			// harmless either way, and an execution left running is never a
			// reference.
			r.fail(s, client, executionID)
			return stopped(stopReason(ctx, scenario.Name))
		}
		return stopped(err)
	}

	// From here an execution exists, and every way out that is not a verdict
	// records it failed, so it can never be mistaken for a reference.
	abort := func(err error) scenarioOutcome {
		r.fail(s, client, executionID)
		out := stopped(err)
		out.executionID = executionID
		return out
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
			if ctx.Err() != nil {
				return abort(stopReason(ctx, scenario.Name))
			}
			runID := fmt.Sprintf("%s-%s-%d", executionID, side.name, i)
			profile := fmt.Sprintf("%s-%s-p%d", executionID, side.name, i)
			fmt.Fprintf(s.err, "trustvian eval run: %s repetition %d of %d (run %s)\n",
				side.name, i, runs, runID)
			config := repetitionConfig(scenario, side.spec, client.baseURL.String(), collectorBin,
				runID, profile, r.workloadStdout)
			if ctx.Done() != nil {
				config.deadline = ctx
			}
			result := r.run(repetitionStreams, config)
			if ctx.Err() != nil {
				// The deadline or a cancellation ended this repetition, and
				// whatever it exited with — a workload that traps SIGTERM can
				// exit 0 — it did not finish.
				return abort(stopReason(ctx, scenario.Name))
			}
			if code := result.code; code != 0 {
				// One failed repetition ends the scenario. Its N is the one
				// the limits were written for; a verdict over fewer runs
				// would be a stricter or looser test than the author chose,
				// silently. And it is 3, never 1: a broken workload is not a
				// behavioral regression.
				return abort(operationalErrorf(
					"%w: %s repetition %d of %d (run %s) exited %d; the scenario stopped "+
						"and no verdict was produced", errRepetitionFailed, side.name, i, runs, runID, code))
			}
			if !result.runCompleted {
				// The workload succeeded, but the control plane does not hold
				// the run as completed — dev has said why on stderr. Asking for
				// a verdict anyway would turn a bookkeeping failure into a
				// behavioral FAIL (check 1 or 2), and the remaining repetitions
				// would be measured for a scenario that can no longer pass. The
				// same outcome as a crashed repetition, for the same reason.
				return abort(operationalErrorf(
					"%w: %s repetition %d of %d (run %s) succeeded but its run could not be "+
						"completed; the scenario stopped and no verdict was produced",
					errRunNotCompleted, side.name, i, runs, runID))
			}
			if side.name == "reference" {
				referenceIDs = append(referenceIDs, runID)
			} else {
				candidateIDs = append(candidateIDs, runID)
			}
		}
	}

	gate := scenario.Gate
	result, err := client.post(ctx, completeExecutionBody{
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
	if err != nil && ctx.Err() != nil {
		return abort(stopReason(ctx, scenario.Name))
	}
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
		out := stopped(err)
		out.executionID = executionID
		return out
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
		out := stopped(operationalErrorf("encoding the result: %v", err))
		out.executionID = executionID
		return out
	}
	return scenarioOutcome{
		exit: exit, document: document, executionID: executionID,
		render: func(w io.Writer) error {
			return renderScenarioResult(w, scenario.Name, executionID, provenance, comparison)
		},
	}
}

// begin records the execution, and with a reference, has the control plane
// resolve it.
func (r scenarioRunner) begin(ctx context.Context, client *platformClient,
	body beginExecutionBody) (beginExecutionResponse, error) {
	result, err := client.post(ctx, body, "scenario-executions")
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

// fail records the execution failed. Best effort, under its own bounded
// context: the original error is what the caller reports, and a failure to
// record the failure is said on stderr. An execution left running is never a
// reference either.
func (r scenarioRunner) fail(s streams, client *platformClient, executionID string) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioCleanupTimeout)
	defer cancel()
	result, err := client.post(ctx, struct{}{}, "scenario-executions", executionID, "fail")
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
