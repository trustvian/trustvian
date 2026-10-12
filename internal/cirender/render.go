package cirender

// Rendering: a validated result document, plus what the caller independently
// knows about the run, in; Markdown out — the same Markdown for a pull request
// comment and a job summary (task 079, ADR 0057). Reading and validating the
// artifact that carries the document is cmd/trustvian-ci-render's.
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

// State is which of the three states a rendering is in.
type State string

const (
	StateVerdict   State = "verdict"
	StateReport    State = "report"
	StateNoVerdict State = "no_verdict"
)

// Frame is what the caller knows about the run independently of the
// artifact: from the event and the run job's own outputs, never from the
// artifact's files.
type Frame struct {
	HeadSHA    string
	Repository string
	RunID      string
	RunAttempt string
	ServerURL  string
	// ExitCode is the run job's exit-code output; nil when it reported none,
	// which means the CLI did not run.
	ExitCode *int
}

// limits bound the output. stringBytes caps one rendered artifact value;
// totalBytes caps the whole body, below GitHub's 65,536-character comment
// limit.
type Limits struct {
	StringBytes int
	TotalBytes  int
}

// DefaultLimits are the comment action's bounds.
var DefaultLimits = Limits{StringBytes: 256, TotalBytes: 60000}

// Rendering is the outcome. Rejected is non-nil exactly when the artifact
// failed validation, so a caller can tell a broken artifact from a run that
// produced no verdict, though both render the no-verdict state.
type Rendering struct {
	Markdown string
	State    State
	Reason   string
	Rejected error
}

// ErrRejected wraps every validation failure that rendering reports.
var ErrRejected = errors.New("the artifact was rejected")

// Detail levels, from the whole document down to the evidence a verdict
// rests on. A version 1 document has two: everything, then without its
// unclassified behaviors. A report sheds more before it gives up (see
// writeReportBody); each shed part is named where it would have been.
const (
	levelFull = iota
	levelShort
	levelShortest
)

// fit renders evidence within the total limit: in full, then with less detail
// at each level, and otherwise not at all. An apparently complete result that
// is missing a check or a classified behavior is never produced.
func fit(want Frame, lim Limits, s State, body func(level int) string) Rendering {
	for _, level := range []int{levelFull, levelShort, levelShortest} {
		if md := body(level); len(md) <= lim.TotalBytes {
			return Rendering{Markdown: md, State: s}
		}
	}
	whole := "the artifact's result.json holds it whole"
	if !want.framed() {
		whole = "the document holds it whole"
	}
	return noVerdict(want, "the result's required evidence does not fit in the rendering size limit; "+whole, nil)
}

// framed reports whether the rendering is for a run the caller identified —
// the pull request comment and job summary — rather than a bare document, as
// `trustvian eval report --format md` renders one.
func (f Frame) framed() bool { return f.HeadSHA != "" }

func rejected(want Frame, err error) Rendering {
	what := "the artifact"
	if !want.framed() {
		what = "the document"
	}
	return noVerdict(want, what+" failed validation: "+err.Error(), fmt.Errorf("%w: %w", ErrRejected, err))
}

// noVerdict renders the no-verdict state from the expected context alone. It
// is built from nothing the artifact supplied, so nothing from an artifact —
// or an earlier rendering — can reach it.
func noVerdict(want Frame, reason string, rejection error) Rendering {
	var b strings.Builder
	b.WriteString("### Trustvian behavioral gate — no verdict\n\n")
	if want.framed() {
		writeIdentity(&b, want)
		b.WriteString("No verdict exists for this commit.\n\n")
	} else {
		b.WriteString("No rendering exists for this document.\n\n")
	}
	b.WriteString("**Reason:** ")
	b.WriteString(inert(reason, 512))
	b.WriteString("\n")
	return Rendering{Markdown: b.String(), State: StateNoVerdict, Reason: reason, Rejected: rejection}
}

// writeIdentity writes the head commit and the run link. Both come from the
// validated expected context, never from the artifact.
func writeIdentity(b *strings.Builder, want Frame) {
	fmt.Fprintf(b, "**Commit** `%s` · [workflow run](%s/%s/actions/runs/%s/attempts/%s)\n\n",
		want.HeadSHA, want.ServerURL, want.Repository, want.RunID, want.RunAttempt)
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

// renderScenario renders a single scenario's verdict. Below levelFull it
// keeps only the classified behaviors, and says so.
func renderScenario(want Frame, lim Limits, r scenarioResult, level int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### Trustvian behavioral gate — %s\n\n", headingVerdict(r))
	if want.framed() {
		writeIdentity(&b, want)
		fmt.Fprintf(&b, "CLI exit code: %d\n\n", *want.ExitCode)
	}
	writeScenarioBody(&b, lim, r, level)
	return b.String()
}

// headingVerdict is the stored verdict, or, for a report whose execution kept
// no gate, that it was not recorded.
func headingVerdict(r scenarioResult) string {
	if r.report != nil && !r.report.gateRecorded {
		return "gate not recorded"
	}
	return verdictLabel[r.verdict]
}

// writeScenarioBody is everything a scenario's verdict rests on. It is shared
// by a single scenario and a suite member, so the two cannot render
// differently.
func writeScenarioBody(b *strings.Builder, lim Limits, r scenarioResult, level int) {
	if r.report != nil {
		writeReportBody(b, lim, r, level)
		return
	}
	full := level == levelFull
	s := lim.StringBytes
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
// each member's own verdict. Below levelFull it leaves out unclassified
// behaviors in every member section.
func renderSuite(want Frame, lim Limits, r suiteResult, level int) string {
	s := lim.StringBytes
	var b strings.Builder
	fmt.Fprintf(&b, "### Trustvian behavioral suite — CLI exit code %d\n\n", r.exitCode)
	if want.framed() {
		writeIdentity(&b, want)
	}
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
		fmt.Fprintf(&b, "\n#### %s — %s\n\n", inert(m.file, s), headingVerdict(*m.result))
		writeScenarioBody(&b, lim, *m.result, level)
	}
	fmt.Fprintf(&b, "\nProduced by `trustvian` %s.\n", inert(r.cliVersion, s))
	return b.String()
}
