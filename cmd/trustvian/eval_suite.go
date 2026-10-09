package main

// trustvian eval run --suite — a directory of behavioral scenarios, run one at
// a time, reported as one document (task 078, ADR 0055).
//
// A suite is a schedule and a report, nothing more. Every member runs through
// the same execute path single-scenario mode uses, with its own N, its own
// limits, its own recorded execution and its own server-owned verdict. The
// suite pools no evidence, combines no presence counts and recomputes no
// verdict: its exit code is the most severe of its members', and its document
// embeds each member's result document unchanged.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/trustvian/trustvian/config"
)

// Suite bounds, published in docs/platform-cli.md and docs/compatibility.md.
const (
	// maxSuiteScenarios bounds the scenario files one suite runs — the same
	// 64 as runs per side, so a suite is at most 64 × 2 × 64 repetitions.
	maxSuiteScenarios = 64

	// maxSuiteDirectoryEntries bounds how many directory entries discovery
	// will examine, scenario or not, so a directory of millions of files is
	// refused rather than read.
	maxSuiteDirectoryEntries = 4096

	// suiteDirectoryBatch is how many entries discovery reads at a time.
	suiteDirectoryBatch = 256

	// minScenarioTimeout and maxScenarioTimeout bound --scenario-timeout.
	minScenarioTimeout = time.Second
	maxScenarioTimeout = 24 * time.Hour

	// maxSuiteDocumentBytes caps the encoded suite document. Over it, the
	// suite exits 3 with a bounded document that says the output is
	// incomplete; member results are never truncated into a shorter one.
	maxSuiteDocumentBytes = 32 << 20

	// maxMemberErrorBytes bounds each member's error message.
	maxMemberErrorBytes = 1024
)

// Member outcomes. A skipped member never ran, and is never a pass.
const (
	outcomePass    = "pass"
	outcomeFail    = "fail"
	outcomeError   = "error"
	outcomeSkipped = "skipped"
)

// Why a member was skipped.
const (
	skippedFailFast  = "fail_fast"
	skippedCancelled = "cancelled"
)

type suiteOptions struct {
	directory    string
	timeoutFlag  string
	timeoutSet   bool
	failFast     bool
	reference    string
	referenceSet bool
	collectorBin string
}

// suiteFile is one discovered, validated member.
type suiteFile struct {
	name string // the file's base name
	job  scenarioJob
}

// suiteDocument is the suite's one result document.
type suiteDocument struct {
	Version  string `json:"version"`
	Complete bool   `json:"complete"`
	Suite    struct {
		Directory     string `json:"directory"`
		ScenarioCount int    `json:"scenario_count"`
	} `json:"suite"`
	Options struct {
		ScenarioTimeout string `json:"scenario_timeout"`
		FailFast        bool   `json:"fail_fast"`
		Reference       string `json:"reference,omitempty"`
	} `json:"options"`
	Members   []suiteMember `json:"members"`
	Summary   suiteSummary  `json:"summary"`
	ExitCode  int           `json:"exit_code"`
	Producers struct {
		CLIVersion string `json:"cli_version"`
	} `json:"producers"`
	// Error is present only on an incomplete document.
	Error *suiteError `json:"error,omitempty"`
}

type suiteMember struct {
	File     string           `json:"file"`
	Scenario scenarioIdentity `json:"scenario"`
	Outcome  string           `json:"outcome,omitempty"`
	// ExitCode is the member's own exit code; null for a skipped member,
	// which has none.
	ExitCode    *int   `json:"exit_code,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
	// Result is the member's result document, exactly as single-scenario
	// --json writes it, for a PASS or FAIL.
	Result        json.RawMessage `json:"result,omitempty"`
	Error         *suiteError     `json:"error,omitempty"`
	SkippedReason string          `json:"skipped_reason,omitempty"`
}

type suiteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type suiteSummary struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errors  int `json:"errors"`
	Skipped int `json:"skipped"`
}

func (r scenarioRunner) suiteMain(s streams, common commonFlags, opts suiteOptions,
	httpTimeout time.Duration) int {
	usage := func(err error) int { return usageFailure(s, evalRunUsage, err) }

	// A suite's deadlines are enforced by terminating process groups, which
	// only a platform `trustvian dev` supports can do. Refused there before
	// anything else; single-scenario mode keeps its earlier behavior.
	supported := r.platformSupported
	if supported == nil {
		supported = devPlatformSupported
	}
	if err := supported(); err != nil {
		return usage(usageErrorf("--suite is not supported on this platform: %v", err))
	}

	// Global preflight: every check here exits 2 before any workload runs.
	if strings.TrimSpace(opts.directory) == "" {
		return usage(usageErrorf("--suite needs a directory"))
	}
	if !opts.timeoutSet {
		return usage(usageErrorf("--suite requires --scenario-timeout (%s to %s)",
			minScenarioTimeout, maxScenarioTimeout))
	}
	scenarioTimeout, err := time.ParseDuration(strings.TrimSpace(opts.timeoutFlag))
	if err != nil || scenarioTimeout < minScenarioTimeout || scenarioTimeout > maxScenarioTimeout {
		return usage(usageErrorf("--scenario-timeout %q must be a duration from %s to %s",
			opts.timeoutFlag, minScenarioTimeout, maxScenarioTimeout))
	}
	reference, err := parseReferenceFlag(opts.reference, opts.referenceSet, false)
	if err != nil {
		return usage(err)
	}

	names, err := discoverSuite(opts.directory)
	if err != nil {
		return usage(err)
	}
	files := make([]suiteFile, 0, len(names))
	seen := make(map[string]string, len(names))
	for _, name := range names {
		scenario, err := config.LoadScenarioFile(filepath.Join(opts.directory, name))
		if err != nil {
			return usage(usageErrorf("%s: %v", name, err))
		}
		if other, dup := seen[scenario.Name]; dup {
			return usage(usageErrorf("%s and %s both declare scenario name %q; a suite's names "+
				"must be distinct, because a name is what a recorded reference is found by",
				other, name, scenario.Name))
		}
		seen[scenario.Name] = name
		scope, err := r.scope(repetitionConfig(scenario, scenario.Candidate, "", opts.collectorBin,
			"", "", r.workloadStdout))
		if err != nil {
			if exitCodeFor(err) == exitUsage {
				return usage(usageErrorf("%s: %v", name, err))
			}
			return emitError(s, *common.json, err)
		}
		provenance, err := scenarioProvenance(scenario, filepath.Join(opts.directory, name), opts.collectorBin,
			opts.referenceSet)
		if err != nil {
			return usage(usageErrorf("%s: %v", name, err))
		}
		files = append(files, suiteFile{name: name,
			job: scenarioJob{scenario: scenario, scope: scope, provenance: provenance}})
	}

	client, err := resolveAPIURL(*common.apiURL, common.apiURLSet(), httpTimeout)
	if err != nil {
		if exitCodeFor(err) == exitUsage {
			return usage(err)
		}
		return emitError(s, *common.json, err)
	}

	// The suite's own cancellation: SIGINT or SIGTERM stops scheduling, and
	// the running member stops through the same signal its repetition's
	// relay forwards. Installed only in suite mode.
	suiteCtx, stopSignals := r.suiteContext()
	defer stopSignals()

	var doc suiteDocument
	doc.Version = "1"
	doc.Complete = true
	doc.Suite.Directory = opts.directory
	doc.Suite.ScenarioCount = len(files)
	doc.Options.ScenarioTimeout = scenarioTimeout.String()
	doc.Options.FailFast = opts.failFast
	if reference != nil {
		doc.Options.Reference = reference.Mode
	}
	doc.Producers.CLIVersion = r.cliVersion()

	limit := r.outputLimit
	if limit <= 0 {
		limit = maxSuiteDocumentBytes
	}
	retained := 0
	overflowed := false
	stopScheduling := ""

	for i, file := range files {
		member := suiteMember{File: file.name,
			Scenario: scenarioIdentity{Name: file.job.scenario.Name, Runs: *file.job.scenario.Runs}}
		if stopScheduling == "" && suiteCtx.Err() != nil {
			stopScheduling = skippedCancelled
		}
		if stopScheduling != "" {
			member.Outcome, member.SkippedReason = outcomeSkipped, stopScheduling
			doc.Summary.Skipped++
			doc.Members = append(doc.Members, member)
			continue
		}

		fmt.Fprintf(s.err, "trustvian eval run: suite scenario %d of %d: %s (%s)\n",
			i+1, len(files), file.name, file.job.scenario.Name)
		ctx, cancel := context.WithTimeout(suiteCtx, scenarioTimeout)
		outcome := r.execute(ctx, s, client, file.job, reference, opts.collectorBin)
		cancel()

		exit := outcome.exit
		member.ExitCode = &exit
		member.ExecutionID = outcome.executionID
		switch {
		case outcome.err != nil:
			member.Outcome = outcomeError
			member.Error = memberError(outcome.err)
			doc.Summary.Errors++
		case exit == exitOK:
			member.Outcome = outcomePass
			doc.Summary.Passed++
		default:
			member.Outcome = outcomeFail
			doc.Summary.Failed++
		}
		if outcome.err == nil {
			// Retained only while the document can still fit, so memory is
			// bounded by the cap however large the members are.
			retained += len(outcome.document)
			if retained > limit {
				overflowed = true
			}
			if !overflowed {
				member.Result = outcome.document
			}
		}
		doc.ExitCode = moreSevere(doc.ExitCode, exit)
		doc.Members = append(doc.Members, member)

		// Cancellation first: a member stopped because the suite was
		// cancelled leaves the rest skipped as cancelled, fail-fast or not.
		// A member's own deadline never cancels the suite.
		switch {
		case suiteCtx.Err() != nil:
			stopScheduling = skippedCancelled
		case exit != exitOK && opts.failFast:
			stopScheduling = skippedFailFast
		}
	}
	if suiteCtx.Err() != nil {
		// A cancelled suite never reports success for what it did not run.
		doc.ExitCode = moreSevere(doc.ExitCode, exitOperational)
	}

	encoded, err := json.Marshal(doc)
	if err != nil {
		return emitError(s, *common.json, operationalErrorf("encoding the suite result: %v", err))
	}
	if overflowed || len(encoded) > limit {
		encoded, err = json.Marshal(incompleteSuiteDocument(doc, len(encoded), overflowed, limit))
		if err != nil {
			return emitError(s, *common.json, operationalErrorf("encoding the suite result: %v", err))
		}
		doc.ExitCode = exitOperational
		fmt.Fprintf(s.err, "trustvian eval run: the suite result exceeds %d bytes; "+
			"member results were omitted and the suite exits 3\n", limit)
	}
	if err := emitSuccess(s, *common.json, encoded, func(w io.Writer) error {
		return renderSuiteResult(w, doc)
	}); err != nil {
		return emitError(s, *common.json, err)
	}
	return doc.ExitCode
}

// discoverSuite lists the suite's scenario files: the directory's immediate
// regular .yaml and .yml files, in byte order of their names. Bounded: at
// most maxSuiteDirectoryEntries entries are examined, read in batches, and at
// most maxSuiteScenarios kept.
func discoverSuite(directory string) ([]string, error) {
	dir, err := os.Open(directory)
	if err != nil {
		return nil, usageErrorf("--suite %s: %v", directory, err)
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return nil, usageErrorf("--suite %s: %v", directory, err)
	}
	if !info.IsDir() {
		return nil, usageErrorf("--suite %s is not a directory", directory)
	}

	var names []string
	examined := 0
	for {
		entries, err := dir.ReadDir(suiteDirectoryBatch)
		for _, entry := range entries {
			examined++
			if examined > maxSuiteDirectoryEntries {
				return nil, usageErrorf("--suite %s holds more than %d entries; a suite directory "+
					"is read whole, so it is bounded", directory, maxSuiteDirectoryEntries)
			}
			name := entry.Name()
			ext := strings.ToLower(filepath.Ext(name))
			if ext != ".yaml" && ext != ".yml" {
				continue
			}
			switch mode := entry.Type(); {
			case mode&os.ModeSymlink != 0:
				return nil, usageErrorf("%s is a symbolic link; suite scenarios must be regular "+
					"files in the directory itself", name)
			case !mode.IsRegular():
				return nil, usageErrorf("%s is not a regular file", name)
			}
			names = append(names, name)
			if len(names) > maxSuiteScenarios {
				return nil, usageErrorf("--suite %s holds more than %d scenario files",
					directory, maxSuiteScenarios)
			}
		}
		if errors.Is(err, io.EOF) || (err == nil && len(entries) == 0) {
			break
		}
		if err != nil {
			return nil, usageErrorf("--suite %s: %v", directory, err)
		}
	}
	if len(names) == 0 {
		return nil, usageErrorf("--suite %s holds no .yaml or .yml scenario files", directory)
	}
	slices.Sort(names)
	return names, nil
}

// moreSevere is the suite's exit precedence: 3, then 2, then 1, then 0.
func moreSevere(a, b int) int {
	rank := func(code int) int {
		switch code {
		case exitOperational:
			return 3
		case exitUsage:
			return 2
		case exitGateFail:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// memberError names why a member has no verdict: the server's error code
// when it answered with one, the runner's reason otherwise, and a bounded
// message.
func memberError(err error) *suiteError {
	code := "operational"
	if exitCodeFor(err) == exitUsage {
		code = "usage"
	}
	var perr *platformError
	if errors.As(err, &perr) && perr.envelope != nil {
		var envelope apiErrorEnvelope
		if json.Unmarshal(perr.envelope, &envelope) == nil && envelope.Error.Code != "" {
			code = envelope.Error.Code
		}
	}
	for sentinel, name := range map[error]string{
		errScenarioTimeout:          "scenario_timeout",
		errScenarioCancelled:        "cancelled",
		errRepetitionFailed:         "repetition_failed",
		errRunNotCompleted:          "run_not_completed",
		errCompletedWithoutResponse: "completed_without_response",
		errExecutionStateUnknown:    "execution_state_unknown",
	} {
		if errors.Is(err, sentinel) {
			code = name
		}
	}
	return &suiteError{Code: code, Message: boundText(err.Error(), maxMemberErrorBytes)}
}

// truncationMarker ends a shortened message, and counts against its bound.
const truncationMarker = "…"

// boundText returns s as valid UTF-8 of at most limit bytes — the marker
// included when s is shortened. Invalid bytes are replaced first, because an
// encoder would replace them anyway, with three bytes each.
func boundText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) <= limit {
		return s
	}
	marker := truncationMarker
	if limit < len(marker) {
		marker = ""
	}
	cut := limit - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

// incompleteSuiteDocument is what an overflowing suite writes instead: the
// identity of every member and nothing that claims an outcome, so no PASS can
// be read from evidence that was not delivered.
func incompleteSuiteDocument(doc suiteDocument, encoded int, retainedOver bool, limit int) suiteDocument {
	out := doc
	out.Complete = false
	out.ExitCode = exitOperational
	out.Summary = suiteSummary{}
	out.Members = make([]suiteMember, 0, len(doc.Members))
	for _, m := range doc.Members {
		out.Members = append(out.Members, suiteMember{File: m.File, Scenario: m.Scenario})
	}
	size := fmt.Sprintf("%d bytes", encoded)
	if retainedOver {
		size = "more than the limit"
	}
	out.Error = &suiteError{
		Code: "output_too_large",
		Message: fmt.Sprintf("the suite result is %s, over the %d-byte limit; member outcomes "+
			"and results are omitted, and this document is incomplete", size, limit),
	}
	return out
}

// renderSuiteResult prints one line per member and the summary. Every
// outcome is the member's own.
func renderSuiteResult(w io.Writer, doc suiteDocument) error {
	fmt.Fprintf(w, "suite %s   %d scenarios\n\n", doc.Suite.Directory, doc.Suite.ScenarioCount)
	for _, m := range doc.Members {
		label := strings.ToUpper(m.Outcome)
		if label == "" {
			label = "?"
		}
		detail := ""
		switch {
		case m.Error != nil:
			detail = "   " + m.Error.Code
		case m.SkippedReason != "":
			detail = "   " + m.SkippedReason
		case m.ExecutionID != "":
			detail = "   execution " + m.ExecutionID
		}
		fmt.Fprintf(w, "  %-7s %-32s %s%s\n", label, m.Scenario.Name, m.File, detail)
	}
	if doc.Error != nil {
		fmt.Fprintf(w, "\nIncomplete: %s\n", doc.Error.Message)
	}
	fmt.Fprintf(w, "\n%d passed, %d failed, %d errors, %d skipped   exit %d\n",
		doc.Summary.Passed, doc.Summary.Failed, doc.Summary.Errors, doc.Summary.Skipped, doc.ExitCode)
	return nil
}

// suiteSignalContext is cancelled by SIGINT or SIGTERM for the life of a
// suite.
func suiteSignalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
