package main

// Rendering: an artifact, plus what the caller independently knows about the
// run, in; Markdown out — the same Markdown for a pull request comment and a
// job summary.
//
// Three states, and nothing between them:
//
//   - verdict: a single scenario's stored PASS or FAIL, with every behavior,
//     every check, the thresholds and the producers;
//   - report: a suite's recorded members and summary, with each member's own
//     verdict. A suite has no verdict of its own, and none is composed;
//   - no verdict: the head commit, a fixed reason and the run link. No count,
//     no check, no zero, and nothing parsed from a document that was not
//     accepted whole.
//
// Every value taken from the artifact is rendered through inert(), and only
// there. Labels for closed vocabularies come from this file's own tables.

import (
	"errors"
	"fmt"
	"strings"
)

type state string

const (
	stateVerdict   state = "verdict"
	stateReport    state = "report"
	stateNoVerdict state = "no_verdict"
)

// expectedContext is what the caller knows about the run independently of the
// artifact: from the event and the run job's own outputs, never from the
// artifact's files.
type expectedContext struct {
	headSHA    string
	repository string
	runID      string
	runAttempt string
	serverURL  string
	// exitCode is the run job's exit-code output; nil when it reported none,
	// which means the CLI did not run.
	exitCode *int
}

// limits bound the output. stringBytes caps one rendered artifact value;
// totalBytes caps the whole body, below GitHub's 65,536-character comment
// limit.
type limits struct {
	stringBytes int
	totalBytes  int
}

var defaultLimits = limits{stringBytes: 256, totalBytes: 60000}

// rendering is the outcome. rejected is non-nil exactly when the artifact
// failed validation, so a caller can tell a broken artifact from a run that
// produced no verdict, though both render the no-verdict state.
type rendering struct {
	markdown string
	state    state
	reason   string
	rejected error
}

// errRejected wraps every validation failure that rendering reports.
var errRejected = errors.New("the artifact was rejected")

// render reads the artifact directory and renders it. It never fails: what
// cannot be rendered as evidence is rendered as no verdict.
func render(dir string, want expectedContext, lim limits) rendering {
	if want.exitCode == nil {
		return noVerdict(want, "the run job reported no CLI exit code, so the CLI did not run", nil)
	}
	rawMeta, err := readBounded(dir, metadataFile, maxMetadataBytes)
	if errors.Is(err, errMissing) {
		return noVerdict(want, "the trustvian-run artifact is missing, so no result was preserved", nil)
	}
	if err != nil {
		return rejected(want, err)
	}
	meta, err := parseMetadata(rawMeta, want)
	if err != nil {
		return rejected(want, fmt.Errorf("%s: %w", metadataFile, err))
	}

	code := meta.exitCode
	if code > 3 {
		return noVerdict(want, fmt.Sprintf("the CLI exited %d, outside its documented codes 0 to 3; "+
			"no verdict is read from it", code), nil)
	}
	switch meta.resultStatus {
	case statusOversized:
		return noVerdict(want, "the CLI's output was larger than the run action preserves, "+
			"so no result document was kept", nil)
	case statusInvalid:
		return noVerdict(want, "the CLI's output was not one JSON object, so no result document was kept", nil)
	case statusAbsent:
		if exists(dir, resultFile) {
			return rejected(want, schemaErr(resultFile, "is present although trustvian-run.json records none"))
		}
		switch code {
		case 2:
			return noVerdict(want, "the CLI reported a usage error (exit 2): the scenario or its "+
				"configuration was refused before anything ran", nil)
		case 3:
			return noVerdict(want, "the CLI reported an operational error (exit 3): the control plane, "+
				"the network, the reference or a repetition failed", nil)
		}
		// A PASS or FAIL always comes with its document.
		return rejected(want, schemaErr(metadataFile, "records no result for a CLI exit that always has one"))
	}

	rawResult, err := readBounded(dir, resultFile, maxResultBytes)
	if errors.Is(err, errMissing) {
		return rejected(want, schemaErr(resultFile, "is absent although trustvian-run.json records it"))
	}
	if err != nil {
		return rejected(want, err)
	}
	if err := checkDigest(rawResult, meta); err != nil {
		return rejected(want, err)
	}
	doc, err := parseStrict(rawResult)
	if err != nil {
		return rejected(want, fmt.Errorf("%s: %w", resultFile, err))
	}

	switch meta.mode {
	case modeScenario:
		result, err := parseScenarioResult(doc, "")
		if err != nil {
			return rejected(want, fmt.Errorf("%s: %w", resultFile, err))
		}
		// The CLI's exit code is the stored verdict mapped through its
		// contract, so the two must say the same thing.
		if !(code == 0 && result.verdict == verdictPass) && !(code == 1 && result.verdict == verdictFail) {
			return rejected(want, schemaErr("comparison.gate.verdict", "disagrees with the CLI exit code"))
		}
		return fit(want, lim, stateVerdict, func(full bool) string {
			return renderScenario(want, lim, result, full)
		})
	case modeSuite:
		suite, err := parseSuiteResult(doc)
		if err != nil {
			return rejected(want, fmt.Errorf("%s: %w", resultFile, err))
		}
		if suite.exitCode != code {
			return rejected(want, schemaErr("exit_code", "disagrees with the CLI exit code"))
		}
		if !suite.complete {
			return noVerdict(want, "the suite result is incomplete: it was larger than the CLI's "+
				"output limit, so the CLI omitted every member outcome", nil)
		}
		return fit(want, lim, stateReport, func(full bool) string {
			return renderSuite(want, lim, suite, full)
		})
	}
	// "both" and "none" are refused by the CLI before it writes anything.
	return rejected(want, schemaErr("mode", "names no result document, but one was preserved"))
}

// fit renders evidence within the total limit: in full, then with only the
// evidence the verdict rests on, and otherwise not at all. An apparently
// complete result that is missing a check or a classified behavior is never
// produced.
func fit(want expectedContext, lim limits, s state, body func(full bool) string) rendering {
	for _, full := range []bool{true, false} {
		if md := body(full); len(md) <= lim.totalBytes {
			return rendering{markdown: md, state: s}
		}
	}
	return noVerdict(want, "the result's required evidence does not fit in the rendering size limit; "+
		"the artifact's result.json holds it whole", nil)
}

func rejected(want expectedContext, err error) rendering {
	return noVerdict(want, "the artifact failed validation: "+err.Error(), fmt.Errorf("%w: %w", errRejected, err))
}

// noVerdict renders the no-verdict state from the expected context alone. It
// is built from nothing the artifact supplied, so nothing from an artifact —
// or an earlier rendering — can reach it.
func noVerdict(want expectedContext, reason string, rejection error) rendering {
	var b strings.Builder
	b.WriteString("### Trustvian behavioral gate — no verdict\n\n")
	writeIdentity(&b, want)
	b.WriteString("No verdict exists for this commit.\n\n")
	b.WriteString("**Reason:** ")
	b.WriteString(inert(reason, 512))
	b.WriteString("\n")
	return rendering{markdown: b.String(), state: stateNoVerdict, reason: reason, rejected: rejection}
}

// writeIdentity writes the head commit and the run link. Both come from the
// validated expected context, never from the artifact.
func writeIdentity(b *strings.Builder, want expectedContext) {
	fmt.Fprintf(b, "**Commit** `%s` · [workflow run](%s/%s/actions/runs/%s/attempts/%s)\n\n",
		want.headSHA, want.serverURL, want.repository, want.runID, want.runAttempt)
}

var verdictLabel = map[string]string{verdictPass: "PASS", verdictFail: "FAIL"}

var checkLabel = map[string]string{
	"reference_repetitions_completed":            "reference repetitions completed",
	"candidate_repetitions_completed":            "candidate repetitions completed",
	"repetitions_failing_minimum_evidence":       "repetitions failing minimum evidence",
	"repeatedly_added_behaviors":                 "repeatedly added behaviors",
	"worst_candidate_block_decisions":            "worst candidate block decisions per run",
	"worst_candidate_critical_risk_observations": "worst candidate critical-risk observations per run",
}

var ruleLabel = map[string]string{ruleEquals: "equals", ruleAtMost: "at most"}

var classLabel = map[string]string{
	classAdded: "**+ added**", classRemoved: "**− removed**", classNeither: "unclassified",
}

// renderScenario renders a single scenario's verdict. full == false keeps
// only the classified behaviors, and says so.
func renderScenario(want expectedContext, lim limits, r scenarioResult, full bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Trustvian behavioral gate — %s\n\n", verdictLabel[r.verdict])
	writeIdentity(&b, want)
	fmt.Fprintf(&b, "CLI exit code: %d\n\n", *want.exitCode)
	writeScenarioBody(&b, lim, r, full)
	return b.String()
}

// writeScenarioBody is everything a scenario's verdict rests on. It is shared
// by a single scenario and a suite member, so the two cannot render
// differently.
func writeScenarioBody(b *strings.Builder, lim limits, r scenarioResult, full bool) {
	s := lim.stringBytes
	fmt.Fprintf(b, "**Scenario** %s · **runs** %d · **execution** %s\n\n",
		inert(r.name, s), r.runs, inert(r.executionID, s))
	if r.reference != nil {
		fmt.Fprintf(b, "**Reference side** reused from execution %s (`--reference %s`)\n\n",
			inert(r.reference.executionID, s), map[string]string{
				referenceModeLast: "last", referenceModeExecution: "<execution-id>",
			}[r.reference.mode])
	}
	fmt.Fprintf(b, "Added: present in at least %s of %d candidate runs and at most %s of %d reference runs. "+
		"Removed: present in at least %s of %d reference runs and at most %s of %d candidate runs.\n\n",
		r.limits.k, r.runs, r.limits.j, r.runs, r.limits.k, r.runs, r.limits.j, r.runs)

	b.WriteString("| Behavior | Fingerprint | Reference | Candidate | Classification |\n")
	b.WriteString("|---|---|---|---|---|\n")
	omitted := false
	for _, bh := range r.behaviors {
		if !full && bh.classification == classNeither {
			omitted = true
			continue
		}
		parts := make([]string, len(bh.descriptor))
		for i, d := range bh.descriptor {
			parts[i] = inert(d, s)
		}
		fmt.Fprintf(b, "| %s | %s | %s/%d | %s/%d | %s |\n", strings.Join(parts, " · "),
			inert(bh.fingerprintID, s), bh.referenceRuns, r.runs, bh.candidateRuns, r.runs,
			classLabel[bh.classification])
	}
	if len(r.behaviors) == 0 {
		b.WriteString("| *(the result lists no behaviors)* | | | | |\n")
	}
	if omitted {
		b.WriteString("\n> **Shortened:** unclassified behaviors were left out to fit the size limit. " +
			"Every added and removed behavior is shown; `result.json` in the artifact lists them all.\n")
	}

	b.WriteString("\n| Gate check | Actual | Rule | Bound | Outcome | Advisory |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	advised := false
	for _, c := range r.checks {
		outcome := "pass"
		if !c.passed {
			outcome = "**FAIL**"
		}
		advisory := ""
		if c.advisory == advisoryFreshScope {
			advisory = "fresh scope"
			advised = true
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n", checkLabel[c.name], c.actual,
			ruleLabel[c.rule], c.bound, outcome, advisory)
	}
	if advised {
		b.WriteString("\n*Fresh scope:* each repetition learns in a scope of its own, so the engine " +
			"has learned nothing there and these checks cannot answer the learned-policy question. " +
			"The marker changes no verdict.\n")
	}
	fmt.Fprintf(b, "\nThe scenario's limits: at most %s repeatedly added behaviors; at most %s block "+
		"decisions and %s critical-risk observations in any one candidate run.\n\n",
		r.limits.maxAdded, r.limits.maxBlock, r.limits.maxCritical)
	fmt.Fprintf(b, "Produced by `trustvian` %s and control plane %s.\n",
		inert(r.cliVersion, s), inert(r.cpVersion, s))
}

var outcomeLabel = map[string]string{
	outcomePass: "PASS", outcomeFail: "FAIL", outcomeError: "error", outcomeSkipped: "skipped",
}

var skippedLabel = map[string]string{
	skippedFailFast: "skipped after a failure (fail-fast)", skippedCancelled: "skipped: the suite was cancelled",
}

// renderSuite renders a suite report: its recorded summary and members, then
// each member's own verdict. full == false leaves out unclassified behaviors
// in every member section.
func renderSuite(want expectedContext, lim limits, r suiteResult, full bool) string {
	s := lim.stringBytes
	var b strings.Builder
	fmt.Fprintf(&b, "### Trustvian behavioral suite — CLI exit code %d\n\n", r.exitCode)
	writeIdentity(&b, want)
	b.WriteString("A suite has no verdict of its own: each scenario below carries its own, " +
		"and the CLI exit code is the most severe of them.\n\n")
	failFast := "no"
	if r.failFast {
		failFast = "yes"
	}
	fmt.Fprintf(&b, "**Suite** %s · **scenarios** %d · **scenario timeout** %s · **fail-fast** %s",
		inert(r.directory, s), r.scenarioCount, inert(r.scenarioTimeout, s), failFast)
	if r.reference != "" {
		b.WriteString(" · **reference** `last`")
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "**Recorded summary:** passed %d · failed %d · errors %d · skipped %d\n\n",
		r.summary.passed, r.summary.failed, r.summary.errors, r.summary.skipped)

	b.WriteString("| File | Scenario | Runs | Outcome | Exit | Detail |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, m := range r.members {
		exit, detail := "—", ""
		switch m.outcome {
		case outcomePass, outcomeFail:
			exit = fmt.Sprint(m.exitCode)
			detail = "execution " + inert(m.executionID, s)
		case outcomeError:
			exit = fmt.Sprint(m.exitCode)
			detail = inert(m.errorCode, s) + ": " + inert(m.errorMessage, s)
		case outcomeSkipped:
			detail = skippedLabel[m.skippedReason]
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s | %s |\n", inert(m.file, s), inert(m.name, s),
			m.runs, outcomeLabel[m.outcome], exit, detail)
	}
	for _, m := range r.members {
		if m.result == nil {
			continue
		}
		fmt.Fprintf(&b, "\n#### %s — %s\n\n", inert(m.file, s), verdictLabel[m.result.verdict])
		writeScenarioBody(&b, lim, *m.result, full)
	}
	fmt.Fprintf(&b, "\nProduced by `trustvian` %s.\n", inert(r.cliVersion, s))
	return b.String()
}
