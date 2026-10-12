package cirender

// Rendering result document version 2: the report of one scenario execution
// (task 107). Every figure is a string or integer the document carries — a
// count, a display string the control plane formatted, a delta it computed —
// and every label for a closed vocabulary comes from this file's tables.
// Nothing here divides, rounds, compares two counts or composes a verdict.
//
// The sections, in order: the execution, sameness and its warnings, the
// behaviors, the targets with their latency and status evidence, tokens and
// cost, the gate as the execution recorded it, the suggestions, and the
// producers. Below the full level the rendering sheds, in this order, the
// unclassified behaviors that are not lost, then the per-target latency and
// status detail and the suggestions; each says where it went. The gate, the
// classified and lost behaviors and the sameness block are never shed.

import (
	"fmt"
	"strings"
)

var referenceModeLabel = map[string]string{
	referenceModeLast:      "`--reference last`",
	referenceModeExecution: "`--reference <execution-id>`",
	notRecorded:            "how it was asked for was not recorded",
}

var sameLabel = map[string]string{"true": "same", "false": "**differs**", notRecorded: "not recorded"}

var inputsLabel = map[string]string{
	"declared": "declared", "not_declared": "none declared", notRecorded: "not recorded",
}

var reportClassLabel = map[string]string{
	classAdded: "**+ added**", classRemoved: "**− removed**", classNeither: "unclassified",
	classNotRecorded: "not recorded",
}

var reasonLabel = map[string]string{
	"reference_unavailable": "not available on the reference side",
	"candidate_unavailable": "not available on the candidate side",
	"both_unavailable":      "not available on either side",
}

var checkStateLabel = map[string]string{
	checkNotEvaluated: "not configured", checkDeferred: "**deferred**",
}

var frequencyCheckLabel = map[string]string{
	"min_candidate_frequency": "minimum candidate presence of reference behaviors",
	"max_lost_behaviors":      "lost behaviors",
	"max_calls_per_run":       "calls per run",
	"max_llm_calls_per_run":   "model calls per run",
}

var frequencyRuleLabel = map[string]string{ruleAtLeast: "at least", ruleAtMost: "at most"}

var targetOutcomeLabel = map[string]string{
	targetPass: "pass", targetFail: "**FAIL**", targetNotObserved: "not observed",
}

var bucketLabel = map[string]string{
	"le_1ms": "≤1 ms", "le_5ms": "≤5 ms", "le_10ms": "≤10 ms", "le_50ms": "≤50 ms",
	"le_100ms": "≤100 ms", "le_250ms": "≤250 ms", "le_500ms": "≤500 ms", "le_1000ms": "≤1 s",
	"le_2500ms": "≤2.5 s", "le_10000ms": "≤10 s", "gt_10000ms": "over 10 s",
}

func writeReportBody(b *strings.Builder, lim Limits, r scenarioResult, level int) {
	s := lim.StringBytes
	rep := r.report
	fmt.Fprintf(b, "**Scenario** %s · **runs** %d · **execution** %s\n\n",
		inert(r.name, s), r.runs, inert(r.executionID, s))
	if r.reference != nil {
		fmt.Fprintf(b, "**Reference side** reused from execution %s (%s)\n\n",
			inert(r.reference.executionID, s), referenceModeLabel[rep.referenceMode])
	}

	writeSameness(b, s, rep)
	writeReportBehaviors(b, s, r, level)
	writeTargets(b, s, rep, level)
	writeTokensAndCost(b, s, rep)
	writeReportGate(b, r)
	if level != levelShortest {
		writeSuggestions(b, s, rep)
	} else if len(rep.suggestions) > 0 {
		b.WriteString("\n#### Suggestions\n\n> **Shortened:** the suggestions were left out to fit the size " +
			"limit; the JSON document lists them.\n")
	}

	cli := inert(r.cliVersion, s)
	if r.cliVersion == notRecorded {
		cli = "(version not recorded)"
	}
	fmt.Fprintf(b, "\nProduced by `trustvian` %s and control plane %s.\n", cli, inert(r.cpVersion, s))
}

func writeSameness(b *strings.Builder, s int, rep *report) {
	same := rep.sameness
	b.WriteString("#### Sameness\n\n")
	b.WriteString("| | Reference | Candidate | Same |\n|---|---|---|---|\n")
	digest := func(v string) string {
		if v == "" {
			return "not recorded"
		}
		return inert(v, s)
	}
	inputs := func(p sideProvenance) string {
		if p.inputDigest == "" {
			return inputsLabel[p.inputs]
		}
		return inputsLabel[p.inputs] + " " + inert(p.inputDigest, s)
	}
	prompt := func(p sideProvenance) string {
		if !p.hasPrompt {
			return "not recorded"
		}
		return inert(p.promptName, s) + " " + inert(p.promptDigest, s)
	}
	fmt.Fprintf(b, "| Scenario | %s | %s | %s |\n", digest(same.reference.scenarioDigest),
		digest(same.candidate.scenarioDigest), sameLabel[same.scenario])
	fmt.Fprintf(b, "| Inputs | %s | %s | %s |\n", inputs(same.reference), inputs(same.candidate),
		sameLabel[same.inputs])
	fmt.Fprintf(b, "| Model | %s | %s | %s |\n", digest(same.reference.model), digest(same.candidate.model),
		sameLabel[same.model])
	fmt.Fprintf(b, "| Prompt reference | %s | %s | %s |\n", prompt(same.reference), prompt(same.candidate),
		sameLabel[same.promptRef])
	if len(rep.warnings) == 0 {
		b.WriteString("\nNo sameness warning.\n")
		return
	}
	b.WriteString("\n")
	for _, w := range rep.warnings {
		fmt.Fprintf(b, "> **Warning** %s: %s\n", inert(w.code, s), inert(w.text, 1024))
	}
	b.WriteString("\nA warning is beside the gate: no check reads it.\n")
}

func fidelityText(f *fidelity, s int) string {
	if f == nil {
		return "—"
	}
	text := fmt.Sprintf("%s · semantic %s · transport %s · unrecorded %s",
		f.level, f.semantic, f.transport, f.unrecorded)
	if f.mixed {
		return "**mixed** " + text
	}
	return text
}

func writeReportBehaviors(b *strings.Builder, s int, r scenarioResult, level int) {
	rep := r.report
	b.WriteString("\n#### Behaviors\n\n")
	if rep.gateRecorded {
		l := rep.limits
		fmt.Fprintf(b, "Added: present in at least %s of %d candidate runs and at most %s of %d reference runs. "+
			"Removed: present in at least %s of %d reference runs and at most %s of %d candidate runs.\n\n",
			l.k, r.runs, l.j, r.runs, l.k, r.runs, l.j, r.runs)
	} else {
		b.WriteString("Classification is not recorded: it needs the limits the execution did not keep.\n\n")
	}
	b.WriteString("| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | " +
		"Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|\n")
	omitted := false
	for _, bh := range rep.behaviors {
		if level != levelFull && (bh.classification == classNeither || bh.classification == classNotRecorded) &&
			!bh.lost {
			omitted = true
			continue
		}
		parts := make([]string, len(bh.descriptor))
		for i, d := range bh.descriptor {
			parts[i] = inert(d, s)
		}
		lost := "no"
		if bh.lost {
			lost = "**lost**"
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s, mean %s | %s, mean %s | %s | %s | %s | %s |\n",
			strings.Join(parts, " · "), inert(bh.fingerprintID, s),
			bh.referencePresence, bh.candidatePresence,
			bh.referenceCalls, bh.referenceMean, bh.candidateCalls, bh.candidateMean,
			reportClassLabel[bh.classification], lost,
			fidelityText(bh.referenceFidelity, s), fidelityText(bh.candidateFidelity, s))
	}
	if len(rep.behaviors) == 0 {
		b.WriteString("| *(the report lists no behaviors)* | | | | | | | | | |\n")
	}
	if omitted {
		b.WriteString("\n> **Shortened:** behaviors that are neither classified nor lost were left out to fit " +
			"the size limit; the JSON document lists them all.\n")
	}
}

func targetName(t reportTarget, s int) string {
	name := "*(no target)*"
	if t.name != "" {
		name = inert(t.name, s)
	}
	if t.category != "" {
		name += " (" + inert(t.category, s) + ")"
	}
	return name
}

func writeTargets(b *strings.Builder, s int, rep *report, level int) {
	b.WriteString("\n#### Targets\n\n")
	if len(rep.targets) == 0 {
		b.WriteString("The report lists no targets.\n")
		return
	}
	b.WriteString("| Target | Calls per run, reference | Calls per run, candidate | Call ratio |\n")
	b.WriteString("|---|---|---|---|\n")
	for _, t := range rep.targets {
		fmt.Fprintf(b, "| %s | %s, mean %s | %s, mean %s | %s |\n", targetName(t, s),
			t.referenceCalls, t.referenceMean, t.candidateCalls, t.candidateMean, t.ratio)
	}
	if level == levelShortest {
		b.WriteString("\n> **Shortened:** each target's latency and status evidence was left out to fit the " +
			"size limit; the JSON document holds it.\n")
		return
	}
	b.WriteString("\nLatency and status per target, each side summed over its completed repetitions. " +
		"Latency buckets with no observation are not listed.\n\n")
	b.WriteString("| Target | Side | Latency | Span status | HTTP status | 429 |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	for _, t := range rep.targets {
		name := targetName(t, s)
		for _, side := range []struct {
			label            string
			latency          latencySide
			errors           errorsSide
			latencyAvailable bool
			errorsAvailable  bool
		}{
			{"reference", t.latency.reference, t.errors.reference,
				sideAvailable(t.latency.section, "reference"), sideAvailable(t.errors.section, "reference")},
			{"candidate", t.latency.candidate, t.errors.candidate,
				sideAvailable(t.latency.section, "candidate"), sideAvailable(t.errors.section, "candidate")},
		} {
			latency, span, http, http429 := notAvailableText, notAvailableText, notAvailableText, notAvailableText
			if side.latencyAvailable {
				latency = latencyText(side.latency)
			}
			if side.errorsAvailable {
				e := side.errors
				span = fmt.Sprintf("ok %s · error %s · unset %s · unavailable %s (%s runs)",
					e.ok, e.spanError, e.unset, e.spanUnavailable, e.runs)
				http = fmt.Sprintf("1xx %s · 2xx %s · 3xx %s · 4xx %s · 5xx %s · unavailable %s",
					e.class1, e.class2, e.class3, e.class4, e.class5, e.httpUnavailable)
				http429 = e.http429
			}
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n", name, side.label, latency, span, http, http429)
		}
		if d := t.errors.delta; d != nil {
			fmt.Fprintf(b, "| %s | change | | ok %s · error %s | 2xx %s · 4xx %s · 5xx %s | %s |\n",
				name, d.ok, d.spanError, d.class2, d.class4, d.class5, d.http429)
		}
	}
}

// sideAvailable is the section's own statement about one side.
func sideAvailable(sec section, side string) bool {
	switch sec.reason {
	case "both_unavailable":
		return false
	case side + "_unavailable":
		return false
	}
	return true
}

func latencyText(l latencySide) string {
	var parts []string
	for i, count := range l.buckets {
		if count != "0" {
			parts = append(parts, bucketLabel[durationBucketOrder[i]]+" "+count)
		}
	}
	return fmt.Sprintf("%s (observed %s, unobserved %s, %s runs)",
		strings.Join(parts, " · "), l.observed, l.unobserved, l.runs)
}

func writeTokensAndCost(b *strings.Builder, s int, rep *report) {
	t := rep.tokens
	b.WriteString("\n#### Tokens and cost\n\n")
	if !t.comparable {
		fmt.Fprintf(b, "Tokens: %s (`%s`); no change is shown.\n\n", reasonLabel[t.reason], t.reason)
	}
	b.WriteString("| | Reference | Candidate | Change |\n|---|---|---|---|\n")
	tokenSide := func(side tokensSide, available bool, field func(tokensSide) string) string {
		if !available {
			return notAvailableText
		}
		return field(side)
	}
	for _, row := range []struct {
		label string
		field func(tokensSide) string
	}{
		{"Input tokens", func(x tokensSide) string { return x.input }},
		{"Output tokens", func(x tokensSide) string { return x.output }},
		{"Unsplit tokens", func(x tokensSide) string { return x.unsplit }},
		{"Observations with usage", func(x tokensSide) string { return x.observed }},
		{"Observations without usage", func(x tokensSide) string { return x.unobserved }},
	} {
		change := ""
		if t.delta != nil {
			change = row.field(*t.delta)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s |\n", row.label,
			tokenSide(t.reference, sideAvailable(t.section, "reference"), row.field),
			tokenSide(t.candidate, sideAvailable(t.section, "candidate"), row.field), change)
	}

	c := rep.cost
	if c == nil {
		fmt.Fprintf(b, "\nCost: not available — no pricing table is configured (`%s`).\n", rep.costNone)
		return
	}
	fmt.Fprintf(b, "\nCost in micro-%s, priced by %s (digest %s, source %s):\n\n",
		inert(c.currency, s), inert(c.pricingVersion, s), inert(c.pricingDigest, s), inert(c.source, s))
	b.WriteString("| | Reference | Candidate | Change |\n|---|---|---|---|\n")
	costText := func(side costSide) string {
		if !side.hasCost {
			return notAvailableText
		}
		return side.costMicros
	}
	fmt.Fprintf(b, "| Cost | %s | %s | %s |\n", costText(c.reference), costText(c.candidate), c.delta)
	fmt.Fprintf(b, "| Priced tokens | %s | %s | |\n", c.reference.priced, c.candidate.priced)
	fmt.Fprintf(b, "| Unpriced tokens | %s | %s | |\n", c.reference.unpriced, c.candidate.unpriced)
	if !c.comparable {
		fmt.Fprintf(b, "\nCost is %s (`%s`); no change is shown.\n", reasonLabel[c.reason], c.reason)
	}
}

func writeReportGate(b *strings.Builder, r scenarioResult) {
	rep := r.report
	b.WriteString("\n#### Gate\n\n")
	if !rep.gateRecorded {
		b.WriteString("**Gate: not recorded.** This execution completed before its gate result was kept " +
			"(schema 14). No check, limit or verdict is shown, and none is re-derived from today's code.\n")
		return
	}
	fmt.Fprintf(b, "**Verdict: %s**, as the execution recorded it when it completed.\n\n", verdictLabel[r.verdict])
	b.WriteString("| Gate check | Actual | Rule | Bound | Outcome | Note |\n")
	b.WriteString("|---|---|---|---|---|---|\n")
	advised := false
	for _, c := range r.checks {
		outcome := "pass"
		if !c.passed {
			outcome = "**FAIL**"
		}
		note := ""
		if c.advisory == advisoryFreshScope {
			note = "fresh scope"
			advised = true
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n", checkLabel[c.name], c.actual,
			ruleLabel[c.rule], c.bound, outcome, note)
	}
	for _, c := range rep.frequency {
		label := frequencyCheckLabel[c.name]
		switch c.state {
		case checkNotEvaluated:
			fmt.Fprintf(b, "| %s | | | | %s | |\n", label, checkStateLabel[c.state])
			continue
		case checkDeferred:
			fmt.Fprintf(b, "| %s | | | | %s | missing evidence: %s |\n", label, checkStateLabel[c.state],
				inert(c.missingEvidence, 512))
			continue
		}
		outcome := "pass"
		if !c.passed {
			outcome = "**FAIL**"
		}
		if c.hasActual {
			fmt.Fprintf(b, "| %s | %s | %s | %s | %s | |\n", label, c.actual, frequencyRuleLabel[c.rule],
				c.bound, outcome)
			continue
		}
		fmt.Fprintf(b, "| %s | | | | %s | per target, below |\n", label, outcome)
		for _, t := range c.targets {
			fmt.Fprintf(b, "| %s · %s | %s | %s | %s | %s | |\n", label, inert(t.target, 256), t.actual,
				frequencyRuleLabel[c.rule], t.max, targetOutcomeLabel[t.outcome])
		}
	}
	if advised {
		b.WriteString("\n*Fresh scope:* each repetition learns in a scope of its own, so the engine " +
			"has learned nothing there and these checks cannot answer the learned-policy question. " +
			"The marker changes no verdict.\n")
	}
	l := rep.limits
	fmt.Fprintf(b, "\nThe recorded limits: at most %s repeatedly added behaviors; at most %s block "+
		"decisions and %s critical-risk observations in any one candidate run.", l.maxAdded, l.maxBlock, l.maxCritical)
	if l.minCandidateFrequency != "" {
		fmt.Fprintf(b, " Every behavior in all %d reference runs is in at least %s candidate runs.",
			r.runs, l.minCandidateFrequency)
	}
	if l.maxLostBehaviors != "" {
		fmt.Fprintf(b, " At most %s lost behaviors.", l.maxLostBehaviors)
	}
	for _, t := range l.maxCallsPerRun {
		fmt.Fprintf(b, " At most %s calls per run to %s.", t.max, inert(t.target, 256))
	}
	if l.maxLLMCallsPerRun != "" {
		fmt.Fprintf(b, " At most %s model calls per run.", l.maxLLMCallsPerRun)
	}
	b.WriteString("\n")
}

func writeSuggestions(b *strings.Builder, s int, rep *report) {
	b.WriteString("\n#### Suggestions\n\n")
	if len(rep.suggestions) == 0 {
		b.WriteString("No suggestion.\n")
		return
	}
	b.WriteString("Rule outputs beside the evidence; none changes the verdict.\n\n")
	for _, sg := range rep.suggestions {
		evidence := make([]string, 0, len(sg.evidence))
		for _, e := range sg.evidence {
			evidence = append(evidence, inert(e.name, s)+" "+inert(e.value, s))
		}
		fmt.Fprintf(b, "- %s: %s\n  Evidence: %s\n", inert(sg.rule, s), inert(sg.text, 1024),
			strings.Join(evidence, " · "))
	}
	if rep.suggestionsTruncated {
		b.WriteString("\nThe control plane listed only the first suggestions; more applied.\n")
	}
}
