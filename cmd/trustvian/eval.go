package main

// Evaluation commands: the CI surface.
//
// Two things here carry more weight than the rest of the CLI.
//
// `ingest` transports the decision record as raw JSON and never decodes it.
// DecisionRecord is an additive public contract, and task 058 deliberately
// made the server tolerant of record fields it does not know — an older CLI
// round-tripping through its own struct would strip a newer producer's field
// before transmission, defeating that tolerance silently from the client side.
//
// `compare` reads exactly one field to choose its exit code: gate.verdict. It
// counts nothing and compares nothing against a threshold. See
// docs/adr/0033-developer-cli-is-a-thin-http-adapter.md.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// Gate verdicts as they appear on the wire.
const (
	gateVerdictPass = "pass"
	gateVerdictFail = "fail"
)

// wireVersion is the ingest envelope version this build speaks.
//
// Versions the DecisionRecord wrapper, not the API shape — the /v1 path does
// that. Task 050 put record versioning in the transport rather than inside the
// record itself.
const wireVersion = "1"

const evalUsage = `usage:
  trustvian eval create       --api-url <url> --id <id> --candidate-id <id>
                              --environment <ref> --behavioral-profile <ref> [--json]
  trustvian eval get          --api-url <url> --id <id> [--json]
  trustvian eval start        --api-url <url> --id <id> [--json]
  trustvian eval complete     --api-url <url> --id <id> [--json]
  trustvian eval fail         --api-url <url> --id <id> [--reason <text>] [--json]
  trustvian eval cancel       --api-url <url> --id <id> [--json]
  trustvian eval progress     --api-url <url> --id <id> [--json]
  trustvian eval ingest-state --api-url <url> --id <id> [--json]
  trustvian eval ingest       --api-url <url> --id <id> --sequence <n>
                              --behavioral-profile <ref> --record <file> [--json]
  trustvian eval compare      --api-url <url> --reference-run <id> --candidate-run <id>
                              --max-added-behaviors <n> --max-block-decisions <n>
                              --max-critical-risk-observations <n> [--json]`

func runEval(s streams, args []string, timeout time.Duration) int {
	if len(args) == 0 {
		return usageFailure(s, evalUsage, fmt.Errorf("eval requires a subcommand"))
	}
	switch args[0] {
	case "create":
		return runEvalCreate(s, args[1:], timeout)
	case "get":
		return runEvalGet(s, args[1:], timeout)
	case "start", "complete", "cancel":
		return runEvalLifecycle(s, args[0], args[1:], timeout)
	case "fail":
		return runEvalFail(s, args[1:], timeout)
	case "progress":
		return runEvalProgress(s, args[1:], timeout)
	case "ingest-state":
		return runEvalIngestState(s, args[1:], timeout)
	case "ingest":
		return runEvalIngest(s, args[1:], timeout)
	case "compare":
		return runEvalCompare(s, args[1:], timeout)
	default:
		return usageFailure(s, evalUsage, fmt.Errorf("unknown eval command %q", args[0]))
	}
}

func runEvalCreate(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval create")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "caller-owned evaluation run identifier (required)")
	candidateID := fs.String("candidate-id", "", "candidate under evaluation (required)")
	environment := fs.String("environment", "", "environment reference (required)")
	profile := fs.String("behavioral-profile", "", "behavioral profile reference (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"id": *id, "candidate-id": *candidateID,
		"environment": *environment, "behavioral-profile": *profile}); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	// No created_at. The control plane owns lifecycle time; a client clock
	// here would make an evaluation's timeline depend on whichever machine
	// happened to launch it.
	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, map[string]string{
				"id":                 *id,
				"candidate_id":       *candidateID,
				"environment":        *environment,
				"behavioral_profile": *profile,
			}, "evaluation-runs")
		}, renderRunBody)
}

func runEvalGet(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval get")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "evaluation-runs", *id)
		}, renderRunBody)
}

// runEvalLifecycle sends start, complete or cancel.
//
// No client-side state machine. Whether pending → complete is legal is the
// control plane's decision, and a CLI that pre-rejected it would be a second
// implementation of a rule that can drift — refusing a transition the server
// would have allowed, or allowing one it would not. The request goes out; the
// authority answers.
func runEvalLifecycle(s streams, action string, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval " + action)
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, nil, "evaluation-runs", *id, action)
		}, renderRunBody)
}

// runEvalFail carries a reason, so it sends a body where the others do not.
func runEvalFail(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval fail")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")
	// Not required: the API accepts an empty reason, and inventing one here
	// would put CLI-authored text into durable evaluation state.
	reason := fs.String("reason", "", "why the run failed")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, map[string]string{"reason": *reason},
				"evaluation-runs", *id, "fail")
		}, renderRunBody)
}

func renderRunBody(w io.Writer, body []byte) error {
	var dto evaluationRunDTO
	if err := decodeJSON(body, &dto); err != nil {
		return err
	}
	return renderEvaluationRun(w, dto)
}

func runEvalProgress(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval progress")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "evaluation-runs", *id, "progress")
		},
		func(w io.Writer, body []byte) error {
			var dto progressDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderProgress(w, dto)
		})
}

func runEvalIngestState(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval ingest-state")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireFlag("id", *id); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.get(ctx, "evaluation-runs", *id, "ingest-state")
		},
		func(w io.Writer, body []byte) error {
			var dto ingestStateDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderIngestState(w, dto)
		})
}

// ingestBody is the transport envelope.
//
// Record is json.RawMessage on purpose — see this file's header. Changing it
// to trustvian.DecisionRecord would compile, pass every test that only checks
// known fields, and silently truncate records from any newer producer.
type ingestBody struct {
	Version           string          `json:"version"`
	Sequence          string          `json:"sequence"`
	BehavioralProfile string          `json:"behavioral_profile"`
	Record            json.RawMessage `json:"record"`
}

func runEvalIngest(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval ingest")
	common := registerCommonFlags(fs)
	id := fs.String("id", "", "evaluation run identifier (required)")
	profile := fs.String("behavioral-profile", "", "behavioral profile reference (required)")
	recordPath := fs.String("record", "", "path to a decision record JSON file (required)")

	// A flag.Value rather than a uint64 flag, so "not given" is distinct from
	// 0 and the value is parsed with exact uint64 semantics.
	var sequence optionalUint64
	fs.Var(&sequence, "sequence", "ingest sequence, a canonical decimal integer >= 1 (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"id": *id, "behavioral-profile": *profile, "record": *recordPath}); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if !sequence.set {
		return usageFailure(s, evalUsage, usageErrorf("--sequence is required"))
	}
	if sequence.value == 0 {
		return usageFailure(s, evalUsage, usageErrorf("--sequence must be at least 1"))
	}

	record, err := readRecordFile(*recordPath)
	if err != nil {
		return usageFailure(s, evalUsage, err)
	}

	// The sequence is sent exactly as given and kept nowhere. No cursor cache,
	// no state file, no retry database: re-running the command re-sends the
	// same explicit sequence, and task 058's idempotency contract decides what
	// that means.
	return runLeaf(s, common, evalUsage, timeout,
		func(ctx context.Context, c *platformClient) (apiResult, error) {
			return c.post(ctx, ingestBody{
				Version:           wireVersion,
				Sequence:          sequence.canonical(),
				BehavioralProfile: *profile,
				Record:            record,
			}, "evaluation-runs", *id, "records")
		},
		func(w io.Writer, body []byte) error {
			var dto ingestResultDTO
			if err := decodeJSON(body, &dto); err != nil {
				return err
			}
			return renderIngestResult(w, dto)
		})
}

// readRecordFile loads a record as raw, well-formed JSON.
//
// Validated for syntax only. DecisionRecord semantics are the server's, and a
// CLI-side schema check would be a second contract that has to be kept in step
// with the first.
func readRecordFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, usageErrorf("reading --record: %v", err)
	}
	defer file.Close()

	// Bounded at the request limit plus one byte: a record larger than the
	// whole request cap can never produce a valid request, so there is no
	// reason to hold it in memory to find that out.
	data, err := io.ReadAll(io.LimitReader(file, maxPlatformRequestBody+1))
	if err != nil {
		return nil, usageErrorf("reading --record: %v", err)
	}
	if len(data) > maxPlatformRequestBody {
		return nil, usageErrorf(
			"--record is larger than the %d byte request limit", maxPlatformRequestBody)
	}
	if len(data) == 0 {
		return nil, usageErrorf("--record is empty")
	}
	if !json.Valid(data) {
		return nil, usageErrorf("--record is not valid JSON")
	}
	return data, nil
}

func runEvalCompare(s streams, args []string, timeout time.Duration) int {
	fs := newFlagSet("eval compare")
	common := registerCommonFlags(fs)
	referenceRun := fs.String("reference-run", "", "reference evaluation run identifier (required)")
	candidateRun := fs.String("candidate-run", "", "candidate evaluation run identifier (required)")

	var addedBehaviors, blockDecisions, criticalRisk optionalUint64
	fs.Var(&addedBehaviors, "max-added-behaviors", "maximum added behaviors (required)")
	fs.Var(&blockDecisions, "max-block-decisions", "maximum block decisions (required)")
	fs.Var(&criticalRisk, "max-critical-risk-observations",
		"maximum critical-risk observations (required)")

	if err := parseFlags(fs, args); err != nil {
		return usageFailure(s, evalUsage, err)
	}
	if err := requireAll(fs, map[string]string{
		"reference-run": *referenceRun, "candidate-run": *candidateRun}); err != nil {
		return usageFailure(s, evalUsage, err)
	}

	// Every limit is required, and omitted is not zero. Zero is the strictest
	// possible limit, so defaulting to it would fail a caller's build under a
	// policy they never chose — and they would have no way to tell that from a
	// real regression.
	for _, limit := range []struct {
		name  string
		value *optionalUint64
	}{
		{"max-added-behaviors", &addedBehaviors},
		{"max-block-decisions", &blockDecisions},
		{"max-critical-risk-observations", &criticalRisk},
	} {
		if !limit.value.set {
			return usageFailure(s, evalUsage, usageErrorf(
				"--%s is required; zero is a strict limit, not a default", limit.name))
		}
	}

	client, err := newPlatformClient(*common.apiURL, timeout)
	if err != nil {
		return usageFailure(s, evalUsage, err)
	}

	result, err := client.post(context.Background(), compareBody{
		ReferenceRunID: *referenceRun,
		CandidateRunID: *candidateRun,
		GateLimits: gateLimitsBody{
			MaxAddedBehaviors:           addedBehaviors.canonical(),
			MaxBlockDecisions:           blockDecisions.canonical(),
			MaxCriticalRiskObservations: criticalRisk.canonical(),
		},
	}, "evaluations", "compare")
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usageFailure(s, evalUsage, err)
		}
		return emitError(s, *common.json, err)
	}
	// An API failure is exit 3, never exit 1. A 409, a 404 and a timeout are
	// not gate failures, and a CI script reading "non-zero means the gate
	// failed" would otherwise report a broken network as a policy violation.
	if err := checkStatus(result); err != nil {
		return emitError(s, *common.json, err)
	}
	// Redundant here, and deliberately kept: decodeJSON below rejects a
	// malformed or empty body on its own, so a mutation removing this line
	// survives. It stays because every successful platform response goes
	// through one rule rather than two that happen to agree — and because a
	// future edit that moved the decode after the emit would otherwise lose
	// the guarantee silently. It also names the empty-body case specifically,
	// which "not valid JSON" does not.
	if err := requireJSONBody(result.body); err != nil {
		return emitError(s, *common.json, err)
	}

	var comparison compareDTO
	if err := decodeJSON(result.body, &comparison); err != nil {
		return emitError(s, *common.json, err)
	}

	// Classified before anything is written. A response the CLI cannot
	// interpret is not publishable CI evidence, so it must not reach stdout
	// and then be retracted by the exit code — a pipeline redirecting stdout
	// to a file would keep the artifact and lose the retraction.
	exit, err := gateExitCode(comparison.Gate.Verdict)
	if err != nil {
		return emitError(s, *common.json, err)
	}

	// The evidence goes to stdout on both recognized verdicts. A gate FAIL is
	// a result, not an error: CI needs the comparison to publish alongside the
	// failure, and writing it to stderr would separate the verdict from its
	// reasons.
	if err := emitSuccess(s, *common.json, result.body, func(w io.Writer) error {
		return renderComparison(w, comparison)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return exit
}

// gateExitCode maps a server verdict onto this command's exit contract.
//
// Exit 1 means one thing: the control plane successfully returned a verdict of
// "fail". It is the only code in the CLI that a CI job is expected to branch
// on as a policy outcome, so everything that is not that explicit answer has
// to stay out of it.
//
// The earlier shape — pass is 0, everything else is 1 — quietly reported
// "unknown", "pending", "PASS" with the wrong case, a typo, a verdict from a
// newer server, and a missing field decoding to "" all as gate failures. Those
// are responses the CLI does not understand. Calling them policy violations
// fails a build for a reason that never happened, and teaches a team that
// exit 1 is noise.
//
// Only the vocabulary is checked, never the arithmetic. A response reporting
// seven added behaviors under a maximum of zero and a verdict of "pass" is
// still exit 0: "pass" is an authoritative answer, and second-guessing it here
// would make the CLI a second gate implementation.
func gateExitCode(verdict string) (int, error) {
	switch verdict {
	case gateVerdictPass:
		return exitOK, nil
	case gateVerdictFail:
		return exitGateFail, nil
	default:
		return exitOperational, operationalErrorf(
			"server response has unsupported gate verdict %q", verdict)
	}
}

type gateLimitsBody struct {
	MaxAddedBehaviors           string `json:"max_added_behaviors"`
	MaxBlockDecisions           string `json:"max_block_decisions"`
	MaxCriticalRiskObservations string `json:"max_critical_risk_observations"`
}

type compareBody struct {
	ReferenceRunID string         `json:"reference_run_id"`
	CandidateRunID string         `json:"candidate_run_id"`
	GateLimits     gateLimitsBody `json:"gate_limits"`
}
