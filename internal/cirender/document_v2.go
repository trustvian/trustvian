package cirender

// Result document version 2: the report of one scenario execution (task 107;
// the contract is docs/compatibility.md § "Result document version 2"). It is
// version 1 member for member plus the report's additions, and is validated
// here into what the renderer consumes.
//
// The rules are version 1's. Every count stays the decimal text the control
// plane wrote, every display string is checked against the one shape the
// control plane produces and transcribed, and every state, outcome and verdict
// is the stored one. Absent, null, zero and false stay distinct: a member the
// contract makes optional is absent or present with its kind, never null.

import (
	"fmt"
	"regexp"
)

// The report's closed vocabularies, beyond version 1's.
const (
	notRecorded = "not_recorded"

	gateStateRecorded = "recorded"

	classNotRecorded = notRecorded

	checkEvaluated    = "evaluated"
	checkNotEvaluated = "not_evaluated"
	checkDeferred     = "deferred"

	ruleAtLeast = "at_least"

	targetPass        = "pass"
	targetFail        = "fail"
	targetNotObserved = "not_observed"

	costReasonNoPricing = "pricing_not_configured"
)

// frequencyCheckOrder is the four frequency checks, in the order the control
// plane always lists them: task 106's three, then task 081's.
var frequencyCheckOrder = []string{
	"min_candidate_frequency",
	"max_lost_behaviors",
	"max_calls_per_run",
	"max_llm_calls_per_run",
}

// durationBucketOrder is the eleven latency buckets, in order.
var durationBucketOrder = []string{
	"le_1ms", "le_5ms", "le_10ms", "le_50ms", "le_100ms", "le_250ms",
	"le_500ms", "le_1000ms", "le_2500ms", "le_10000ms", "gt_10000ms",
}

var (
	// signedText is a delta: candidate minus reference, "-" for a negative
	// one, never "-0".
	signedText = regexp.MustCompile(`^(0|-?[1-9][0-9]{0,19})$`)

	// The display strings' shapes, exactly as the control plane formats them.
	presenceText = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})/(0|[1-9][0-9]{0,19})$`)
	rangeText    = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})–(0|[1-9][0-9]{0,19})$`)
	meanText     = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})(\.[0-9]{0,2}[1-9])?$`)
	ratioText    = regexp.MustCompile(`^(0|[1-9][0-9]{0,19})\.[0-9]×$`)
)

const notAvailableText = "not available"

// report is what version 2 adds to the scenario result.
type report struct {
	// referenceMode is the recorded mode, or not_recorded; the reference's
	// execution id is the version 1 field.
	referenceMode string

	gateRecorded bool
	limits       *reportLimits

	behaviors []reportBehavior
	frequency []frequencyCheck
	tokens    tokensSection
	cost      *costSection
	costNone  string // cost_reason when cost is absent
	targets   []reportTarget

	suggestions          []suggestion
	suggestionsTruncated bool

	sameness sameness
	warnings []warning
}

// reportLimits are the recorded limits: version 1's five and the optional
// ones that were supplied.
type reportLimits struct {
	gateLimits
	minCandidateFrequency, maxLostBehaviors, maxLLMCallsPerRun string
	maxCallsPerRun                                             []targetLimit
}

type targetLimit struct{ target, max string }

type reportBehavior struct {
	behavior
	lost bool

	referenceFidelity, candidateFidelity *fidelity

	// The display strings, transcribed.
	referencePresence, candidatePresence string
	referenceCalls, candidateCalls       string
	referenceMean, candidateMean         string
}

type fidelity struct {
	level                           string
	mixed                           bool
	semantic, transport, unrecorded string
}

type frequencyCheck struct {
	name, state           string
	rule, actual, bound   string
	passed                bool
	missingEvidence       string
	targets               []targetCheck
	hasActual, hasOutcome bool
}

type targetCheck struct{ target, actual, max, outcome string }

// section is one operational section's comparability.
type section struct {
	comparable bool
	reason     string
}

type tokensSection struct {
	section
	reference, candidate tokensSide
	delta                *tokensSide
}

type tokensSide struct {
	runs, input, output, unsplit, observed, unobserved string
}

type costSection struct {
	section
	pricingVersion, pricingDigest, source, currency string
	reference, candidate                            costSide
	delta                                           string
}

type costSide struct {
	runs, costMicros       string
	hasCost                bool
	priced, unpriced       string
	tokenObs, withoutToken string
}

type reportTarget struct {
	name, category string
	referenceCalls string
	candidateCalls string
	referenceMean  string
	candidateMean  string
	ratio          string
	latency        latencySection
	errors         errorsSection
}

type latencySection struct {
	section
	reference, candidate latencySide
}

type latencySide struct {
	runs, observed, unobserved string
	buckets                    []string // counts, in durationBucketOrder
}

type errorsSection struct {
	section
	reference, candidate errorsSide
	delta                *errorsSide
}

type errorsSide struct {
	runs                                   string
	spanUnavailable, unset, ok, spanError  string
	class1, class2, class3, class4, class5 string
	httpUnavailable, http429               string
}

type suggestion struct {
	rule, text string
	evidence   []evidenceItem
}

type evidenceItem struct{ name, value string }

type sameness struct {
	scenario, inputs, model, promptRef string
	reference, candidate               sideProvenance
}

type sideProvenance struct {
	scenarioDigest, inputs, inputDigest, model string
	promptName, promptDigest                   string
	hasPrompt                                  bool
}

type warning struct{ code, text string }

var sameValues = []string{"true", "false", notRecorded}

// parseReport validates a version 2 document. Version 1's members are
// validated by the same readers version 1 uses; the gate, limits and
// classifications follow the report's own rules.
func parseReport(doc *node, path string) (scenarioResult, error) {
	var r scenarioResult
	rep := &report{}
	r.report = rep
	var err error

	scenario, err := object(doc, path, "scenario")
	if err != nil {
		return r, err
	}
	sp := join(path, "scenario")
	if r.name, err = nonEmpty(scenario, sp, "name"); err != nil {
		return r, err
	}
	if r.runs, err = integer(scenario, sp, "runs", 1, maxRuns); err != nil {
		return r, err
	}
	if r.executionID, err = nonEmpty(doc, path, "execution_id"); err != nil {
		return r, err
	}
	if ref, err := optional(doc, path, "reference", kindObject); err != nil {
		return r, err
	} else if ref != nil {
		rp := join(path, "reference")
		var p referenceProvenance
		if p.mode, err = enum(ref, rp, "mode", referenceModeExecution, referenceModeLast, notRecorded); err != nil {
			return r, err
		}
		if p.executionID, err = nonEmpty(ref, rp, "execution_id"); err != nil {
			return r, err
		}
		rep.referenceMode = p.mode
		r.reference = &p
	}
	producers, err := object(doc, path, "producers")
	if err != nil {
		return r, err
	}
	pp := join(path, "producers")
	if r.cliVersion, err = nonEmpty(producers, pp, "cli_version"); err != nil {
		return r, err
	}
	if r.cpVersion, err = nonEmpty(producers, pp, "control_plane_version"); err != nil {
		return r, err
	}
	if _, err := object(doc, path, "provenance"); err != nil {
		return r, err
	}

	comparison, err := object(doc, path, "comparison")
	if err != nil {
		return r, err
	}
	cp := join(path, "comparison")
	if err := equalText(comparison, cp, "version", "1"); err != nil {
		return r, err
	}
	runs, err := integer(comparison, cp, "runs", 1, maxRuns)
	if err != nil {
		return r, err
	}
	if runs != r.runs {
		return r, schemaErr(join(cp, "runs"), "differs from scenario.runs")
	}
	producer, err := object(comparison, cp, "producer")
	if err != nil {
		return r, err
	}
	if v, err := nonEmpty(producer, join(cp, "producer"), "control_plane_version"); err != nil {
		return r, err
	} else if v != r.cpVersion {
		return r, schemaErr(join(cp, "producer.control_plane_version"),
			"differs from producers.control_plane_version")
	}

	gate, err := object(comparison, cp, "gate")
	if err != nil {
		return r, err
	}
	gp := join(cp, "gate")
	state, err := enum(gate, gp, "state", gateStateRecorded, notRecorded)
	if err != nil {
		return r, err
	}
	rep.gateRecorded = state == gateStateRecorded
	limitsNode, err := optional(comparison, cp, "gate_limits", kindObject)
	if err != nil {
		return r, err
	}
	if rep.gateRecorded {
		if limitsNode == nil {
			return r, schemaErr(join(cp, "gate_limits"), "required field is absent from a recorded gate")
		}
		if rep.limits, err = parseReportLimits(limitsNode, join(cp, "gate_limits")); err != nil {
			return r, err
		}
		r.limits = rep.limits.gateLimits
		if err := parseRecordedGate(gate, gp, &r); err != nil {
			return r, err
		}
	} else {
		// A gate that was not recorded carries nothing else: no check, no
		// verdict and no limits, so none can be rendered by mistake.
		if limitsNode != nil {
			return r, schemaErr(join(cp, "gate_limits"), "is present although the gate was not recorded")
		}
		for _, name := range []string{"checks", "verdict", "frequency_checks"} {
			if _, ok := gate.fields[name]; ok {
				return r, schemaErr(join(gp, name), "is present although the gate was not recorded")
			}
		}
	}

	behaviors, err := array(comparison, cp, "behaviors")
	if err != nil {
		return r, err
	}
	for i, item := range behaviors.items {
		b, err := parseReportBehavior(item, index(join(cp, "behaviors"), i), r.runs, rep.gateRecorded)
		if err != nil {
			return r, err
		}
		rep.behaviors = append(rep.behaviors, b)
		r.behaviors = append(r.behaviors, b.behavior)
	}

	operational, err := object(comparison, cp, "operational")
	if err != nil {
		return r, err
	}
	op := join(cp, "operational")
	tokens, err := object(operational, op, "tokens")
	if err != nil {
		return r, err
	}
	if rep.tokens, err = parseTokens(tokens, join(op, "tokens")); err != nil {
		return r, err
	}
	costNode, err := optional(operational, op, "cost", kindObject)
	if err != nil {
		return r, err
	}
	reason, hasReason, err := optionalStr(operational, op, "cost_reason")
	if err != nil {
		return r, err
	}
	switch {
	case costNode != nil && hasReason:
		return r, schemaErr(join(op, "cost_reason"), "is present beside a cost section")
	case costNode != nil:
		if rep.cost, err = parseCost(costNode, join(op, "cost")); err != nil {
			return r, err
		}
	case !hasReason:
		return r, schemaErr(join(op, "cost_reason"), "required field is absent when there is no cost section")
	case reason != costReasonNoPricing:
		return r, schemaErr(join(op, "cost_reason"), "is not one of its defined values")
	default:
		rep.costNone = reason
	}

	targets, err := array(comparison, cp, "targets")
	if err != nil {
		return r, err
	}
	for i, item := range targets.items {
		t, err := parseReportTarget(item, index(join(cp, "targets"), i))
		if err != nil {
			return r, err
		}
		rep.targets = append(rep.targets, t)
	}

	suggestions, err := array(comparison, cp, "suggestions")
	if err != nil {
		return r, err
	}
	for i, item := range suggestions.items {
		s, err := parseSuggestion(item, index(join(cp, "suggestions"), i))
		if err != nil {
			return r, err
		}
		rep.suggestions = append(rep.suggestions, s)
	}
	if rep.suggestionsTruncated, err = boolean(comparison, cp, "suggestions_truncated"); err != nil {
		return r, err
	}

	same, err := object(comparison, cp, "sameness")
	if err != nil {
		return r, err
	}
	if rep.sameness, err = parseSameness(same, join(cp, "sameness")); err != nil {
		return r, err
	}
	warnings, err := array(comparison, cp, "warnings")
	if err != nil {
		return r, err
	}
	for i, item := range warnings.items {
		wp := index(join(cp, "warnings"), i)
		if item.kind != kindObject {
			return r, schemaErr(wp, "is not an object")
		}
		var w warning
		if w.code, err = nonEmpty(item, wp, "code"); err != nil {
			return r, err
		}
		if w.text, err = nonEmpty(item, wp, "text"); err != nil {
			return r, err
		}
		rep.warnings = append(rep.warnings, w)
	}
	return r, nil
}

func parseReportLimits(limits *node, lp string) (*reportLimits, error) {
	l := &reportLimits{}
	var err error
	for _, f := range []struct {
		name string
		into *string
	}{
		{"added_candidate_presence_minimum", &l.k},
		{"added_reference_presence_maximum", &l.j},
		{"max_repeated_added_behaviors", &l.maxAdded},
		{"max_block_decisions_per_run", &l.maxBlock},
		{"max_critical_risk_observations_per_run", &l.maxCritical},
	} {
		if *f.into, err = decimal(limits, lp, f.name); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct {
		name string
		into *string
	}{
		{"min_candidate_frequency", &l.minCandidateFrequency},
		{"max_lost_behaviors", &l.maxLostBehaviors},
		{"max_llm_calls_per_run", &l.maxLLMCallsPerRun},
	} {
		if _, ok := limits.fields[f.name]; ok {
			if *f.into, err = decimal(limits, lp, f.name); err != nil {
				return nil, err
			}
		}
	}
	targets, err := optional(limits, lp, "max_calls_per_run", kindArray)
	if err != nil {
		return nil, err
	}
	if targets != nil {
		for i, item := range targets.items {
			tp := index(join(lp, "max_calls_per_run"), i)
			if item.kind != kindObject {
				return nil, schemaErr(tp, "is not an object")
			}
			var t targetLimit
			if t.target, err = nonEmpty(item, tp, "target"); err != nil {
				return nil, err
			}
			if t.max, err = decimal(item, tp, "max"); err != nil {
				return nil, err
			}
			l.maxCallsPerRun = append(l.maxCallsPerRun, t)
		}
	}
	return l, nil
}

// parseRecordedGate reads the six checks, the verdict and the four frequency
// checks, each in its fixed place.
func parseRecordedGate(gate *node, gp string, r *scenarioResult) error {
	checks, err := array(gate, gp, "checks")
	if err != nil {
		return err
	}
	if len(checks.items) != len(checkOrder) {
		return schemaErr(join(gp, "checks"), "does not hold exactly the six repeated-gate checks")
	}
	for i, item := range checks.items {
		c, err := parseCheck(item, index(join(gp, "checks"), i), checkOrder[i])
		if err != nil {
			return err
		}
		r.checks = append(r.checks, c)
	}
	if r.verdict, err = enum(gate, gp, "verdict", verdictPass, verdictFail); err != nil {
		return err
	}
	frequency, err := array(gate, gp, "frequency_checks")
	if err != nil {
		return err
	}
	if len(frequency.items) != len(frequencyCheckOrder) {
		return schemaErr(join(gp, "frequency_checks"), "does not hold exactly the four frequency checks")
	}
	for i, item := range frequency.items {
		c, err := parseFrequencyCheck(item, index(join(gp, "frequency_checks"), i), frequencyCheckOrder[i])
		if err != nil {
			return err
		}
		r.report.frequency = append(r.report.frequency, c)
	}
	return nil
}

// parseFrequencyCheck reads one frequency check against its state's shape:
// evaluated carries its rule, outcome and actual and bound (per target for
// max_calls_per_run); deferred carries what evidence is missing; not
// evaluated carries nothing.
func parseFrequencyCheck(item *node, path, wantName string) (frequencyCheck, error) {
	var c frequencyCheck
	if item.kind != kindObject {
		return c, schemaErr(path, "is not an object")
	}
	var err error
	if c.name, err = str(item, path, "name"); err != nil {
		return c, err
	}
	if c.name != wantName {
		return c, schemaErr(join(path, "name"), "is not the check the stable order defines here")
	}
	if c.state, err = enum(item, path, "state", checkEvaluated, checkNotEvaluated, checkDeferred); err != nil {
		return c, err
	}
	forbid := func(names ...string) error {
		for _, name := range names {
			if _, ok := item.fields[name]; ok {
				return schemaErr(join(path, name), "is present on a check whose state has none")
			}
		}
		return nil
	}
	perTarget := c.name == "max_calls_per_run"
	switch c.state {
	case checkNotEvaluated:
		return c, forbid("rule", "actual", "bound", "passed", "targets", "missing_evidence")
	case checkDeferred:
		if err := forbid("rule", "actual", "bound", "passed", "targets"); err != nil {
			return c, err
		}
		c.missingEvidence, err = nonEmpty(item, path, "missing_evidence")
		return c, err
	}
	if err := forbid("missing_evidence"); err != nil {
		return c, err
	}
	if c.rule, err = enum(item, path, "rule", ruleAtLeast, ruleAtMost); err != nil {
		return c, err
	}
	if c.passed, err = boolean(item, path, "passed"); err != nil {
		return c, err
	}
	c.hasOutcome = true
	if perTarget {
		if err := forbid("actual", "bound"); err != nil {
			return c, err
		}
		targets, err := array(item, path, "targets")
		if err != nil {
			return c, err
		}
		for i, t := range targets.items {
			tp := index(join(path, "targets"), i)
			if t.kind != kindObject {
				return c, schemaErr(tp, "is not an object")
			}
			var tc targetCheck
			if tc.target, err = nonEmpty(t, tp, "target"); err != nil {
				return c, err
			}
			if tc.actual, err = decimal(t, tp, "actual"); err != nil {
				return c, err
			}
			if tc.max, err = decimal(t, tp, "max"); err != nil {
				return c, err
			}
			if tc.outcome, err = enum(t, tp, "outcome", targetPass, targetFail, targetNotObserved); err != nil {
				return c, err
			}
			c.targets = append(c.targets, tc)
		}
		return c, nil
	}
	if err := forbid("targets"); err != nil {
		return c, err
	}
	if c.actual, err = decimal(item, path, "actual"); err != nil {
		return c, err
	}
	if c.bound, err = decimal(item, path, "bound"); err != nil {
		return c, err
	}
	c.hasActual = true
	return c, nil
}

func parseReportBehavior(item *node, path string, runs int64, gateRecorded bool) (reportBehavior, error) {
	var b reportBehavior
	if item.kind != kindObject {
		return b, schemaErr(path, "is not an object")
	}
	var err error
	if b.fingerprintID, err = nonEmpty(item, path, "fingerprint_id"); err != nil {
		return b, err
	}
	desc, err := object(item, path, "behavior")
	if err != nil {
		return b, err
	}
	dp := join(path, "behavior")
	for _, f := range descriptorFields {
		var value string
		if f.required {
			value, err = nonEmpty(desc, dp, f.name)
		} else {
			value, _, err = optionalStr(desc, dp, f.name)
		}
		if err != nil {
			return b, err
		}
		if value != "" {
			b.descriptor = append(b.descriptor, value)
		}
	}
	if b.referenceRuns, err = decimal(item, path, "reference_runs_present"); err != nil {
		return b, err
	}
	if b.candidateRuns, err = decimal(item, path, "candidate_runs_present"); err != nil {
		return b, err
	}
	if gateRecorded {
		b.classification, err = enum(item, path, "classification", classAdded, classRemoved, classNeither)
	} else {
		b.classification, err = enum(item, path, "classification", classNotRecorded)
	}
	if err != nil {
		return b, err
	}
	if b.lost, err = boolean(item, path, "lost"); err != nil {
		return b, err
	}
	for _, side := range []struct {
		name string
		into **fidelity
	}{{"reference_fidelity", &b.referenceFidelity}, {"candidate_fidelity", &b.candidateFidelity}} {
		n, err := optional(item, path, side.name, kindObject)
		if err != nil {
			return b, err
		}
		if n != nil {
			f, err := parseFidelity(n, join(path, side.name))
			if err != nil {
				return b, err
			}
			*side.into = &f
		}
	}

	display, err := object(item, path, "display")
	if err != nil {
		return b, err
	}
	dsp := join(path, "display")
	if b.referencePresence, err = shaped(display, dsp, "reference_presence", presenceText, false); err != nil {
		return b, err
	}
	if b.candidatePresence, err = shaped(display, dsp, "candidate_presence", presenceText, false); err != nil {
		return b, err
	}
	// The presence string formats the count beside it over N: a string that
	// says otherwise is a document contradicting itself.
	n := fmt.Sprint(runs)
	if b.referencePresence != b.referenceRuns+"/"+n || b.candidatePresence != b.candidateRuns+"/"+n {
		return b, schemaErr(dsp, "presence differs from the counts beside it")
	}
	for _, f := range []struct {
		name  string
		shape *regexp.Regexp
		into  *string
	}{
		{"reference_calls_per_run", rangeText, &b.referenceCalls},
		{"candidate_calls_per_run", rangeText, &b.candidateCalls},
		{"reference_calls_per_run_mean", meanText, &b.referenceMean},
		{"candidate_calls_per_run_mean", meanText, &b.candidateMean},
	} {
		if *f.into, err = shaped(display, dsp, f.name, f.shape, true); err != nil {
			return b, err
		}
	}
	if _, err := object(item, path, "present_in"); err != nil {
		return b, err
	}
	return b, nil
}

// shaped reads a display string that must match its shape, or be "not
// available" when allowed.
func shaped(obj *node, path, name string, shape *regexp.Regexp, unavailable bool) (string, error) {
	s, err := str(obj, path, name)
	if err != nil {
		return "", err
	}
	if (unavailable && s == notAvailableText) || shape.MatchString(s) {
		return s, nil
	}
	return "", schemaErr(join(path, name), "is not a display string the control plane writes")
}

func parseFidelity(n *node, path string) (fidelity, error) {
	var f fidelity
	var err error
	if f.level, err = enum(n, path, "level", "semantic", "transport", "unrecorded"); err != nil {
		return f, err
	}
	if f.mixed, err = boolean(n, path, "mixed"); err != nil {
		return f, err
	}
	for _, c := range []struct {
		name string
		into *string
	}{{"semantic", &f.semantic}, {"transport", &f.transport}, {"unrecorded", &f.unrecorded}} {
		if *c.into, err = decimal(n, path, c.name); err != nil {
			return f, err
		}
	}
	_, err = object(n, path, "layer")
	return f, err
}

func parseSection(n *node, path string) (section, error) {
	var s section
	var err error
	if s.comparable, err = boolean(n, path, "comparable"); err != nil {
		return s, err
	}
	reason, hasReason, err := optionalStr(n, path, "reason")
	if err != nil {
		return s, err
	}
	_, hasDelta := n.fields["delta"]
	switch {
	case s.comparable && (hasReason || !hasDelta):
		return s, schemaErr(path, "a comparable section has a delta and no reason")
	case !s.comparable && (!hasReason || hasDelta):
		return s, schemaErr(path, "a section that is not comparable has a reason and no delta")
	}
	if hasReason {
		switch reason {
		case "reference_unavailable", "candidate_unavailable", "both_unavailable":
			s.reason = reason
		default:
			return s, schemaErr(join(path, "reason"), "is not one of its defined values")
		}
	}
	return s, nil
}

func signed(obj *node, path, name string) (string, error) {
	s, err := str(obj, path, name)
	if err == nil && !signedText.MatchString(s) {
		err = schemaErr(join(path, name), "is not a canonical signed decimal string")
	}
	return s, err
}

func parseTokens(n *node, path string) (tokensSection, error) {
	var t tokensSection
	var err error
	if t.section, err = parseSection(n, path); err != nil {
		return t, err
	}
	side := func(obj *node, sp string, read func(*node, string, string) (string, error)) (tokensSide, error) {
		var s tokensSide
		fields := []struct {
			name string
			into *string
		}{
			{"input", &s.input}, {"output", &s.output}, {"unsplit", &s.unsplit},
			{"observed", &s.observed}, {"unobserved", &s.unobserved},
		}
		for _, f := range fields {
			if *f.into, err = read(obj, sp, f.name); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	for _, s := range []struct {
		name string
		into *tokensSide
	}{{"reference", &t.reference}, {"candidate", &t.candidate}} {
		obj, err := object(n, path, s.name)
		if err != nil {
			return t, err
		}
		sp := join(path, s.name)
		if *s.into, err = side(obj, sp, decimal); err != nil {
			return t, err
		}
		if s.into.runs, err = decimal(obj, sp, "runs_with_evidence"); err != nil {
			return t, err
		}
	}
	if t.comparable {
		obj, err := object(n, path, "delta")
		if err != nil {
			return t, err
		}
		d, err := side(obj, join(path, "delta"), signed)
		if err != nil {
			return t, err
		}
		t.delta = &d
	}
	return t, nil
}

func parseCost(n *node, path string) (*costSection, error) {
	c := &costSection{}
	var err error
	if c.section, err = parseSection(n, path); err != nil {
		return nil, err
	}
	// A cost is never rendered without the pricing it came from.
	for _, f := range []struct {
		name string
		into *string
	}{
		{"pricing_version", &c.pricingVersion}, {"pricing_digest", &c.pricingDigest},
		{"source", &c.source}, {"currency", &c.currency},
	} {
		if *f.into, err = nonEmpty(n, path, f.name); err != nil {
			return nil, err
		}
	}
	for _, s := range []struct {
		name string
		into *costSide
	}{{"reference", &c.reference}, {"candidate", &c.candidate}} {
		obj, err := object(n, path, s.name)
		if err != nil {
			return nil, err
		}
		sp := join(path, s.name)
		for _, f := range []struct {
			name string
			into *string
		}{
			{"runs_with_evidence", &s.into.runs}, {"priced_tokens", &s.into.priced},
			{"unpriced_tokens", &s.into.unpriced}, {"token_observations", &s.into.tokenObs},
			{"observations_without_tokens", &s.into.withoutToken},
		} {
			if *f.into, err = decimal(obj, sp, f.name); err != nil {
				return nil, err
			}
		}
		if _, ok := obj.fields["cost_micros"]; ok {
			if s.into.costMicros, err = decimal(obj, sp, "cost_micros"); err != nil {
				return nil, err
			}
			s.into.hasCost = true
		}
	}
	if c.comparable {
		delta, err := object(n, path, "delta")
		if err != nil {
			return nil, err
		}
		if c.delta, err = signed(delta, join(path, "delta"), "cost_micros"); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func parseReportTarget(item *node, path string) (reportTarget, error) {
	var t reportTarget
	if item.kind != kindObject {
		return t, schemaErr(path, "is not an object")
	}
	var err error
	if t.name, _, err = optionalStr(item, path, "target_name"); err != nil {
		return t, err
	}
	if t.category, _, err = optionalStr(item, path, "target_category"); err != nil {
		return t, err
	}
	display, err := object(item, path, "display")
	if err != nil {
		return t, err
	}
	dsp := join(path, "display")
	for _, f := range []struct {
		name  string
		shape *regexp.Regexp
		into  *string
	}{
		{"reference_calls_per_run", rangeText, &t.referenceCalls},
		{"candidate_calls_per_run", rangeText, &t.candidateCalls},
		{"reference_calls_per_run_mean", meanText, &t.referenceMean},
		{"candidate_calls_per_run_mean", meanText, &t.candidateMean},
		{"call_ratio", ratioText, &t.ratio},
	} {
		if *f.into, err = shaped(display, dsp, f.name, f.shape, true); err != nil {
			return t, err
		}
	}
	operational, err := object(item, path, "operational")
	if err != nil {
		return t, err
	}
	op := join(path, "operational")
	latency, err := object(operational, op, "latency")
	if err != nil {
		return t, err
	}
	if t.latency, err = parseLatency(latency, join(op, "latency")); err != nil {
		return t, err
	}
	errs, err := object(operational, op, "errors")
	if err != nil {
		return t, err
	}
	if t.errors, err = parseErrors(errs, join(op, "errors")); err != nil {
		return t, err
	}
	return t, nil
}

func parseLatency(n *node, path string) (latencySection, error) {
	var l latencySection
	var err error
	if l.section, err = parseSection(n, path); err != nil {
		return l, err
	}
	for _, s := range []struct {
		name string
		into *latencySide
	}{{"reference", &l.reference}, {"candidate", &l.candidate}} {
		obj, err := object(n, path, s.name)
		if err != nil {
			return l, err
		}
		sp := join(path, s.name)
		if s.into.runs, err = decimal(obj, sp, "runs_with_evidence"); err != nil {
			return l, err
		}
		if s.into.observed, err = decimal(obj, sp, "observed"); err != nil {
			return l, err
		}
		if s.into.unobserved, err = decimal(obj, sp, "unobserved"); err != nil {
			return l, err
		}
		buckets, err := array(obj, sp, "buckets")
		if err != nil {
			return l, err
		}
		if len(buckets.items) != len(durationBucketOrder) {
			return l, schemaErr(join(sp, "buckets"), "does not hold exactly the eleven duration buckets")
		}
		for i, b := range buckets.items {
			bp := index(join(sp, "buckets"), i)
			if b.kind != kindObject {
				return l, schemaErr(bp, "is not an object")
			}
			if err := equalText(b, bp, "bound", durationBucketOrder[i]); err != nil {
				return l, err
			}
			count, err := decimal(b, bp, "count")
			if err != nil {
				return l, err
			}
			s.into.buckets = append(s.into.buckets, count)
		}
	}
	return l, nil
}

func parseErrors(n *node, path string) (errorsSection, error) {
	var e errorsSection
	var err error
	if e.section, err = parseSection(n, path); err != nil {
		return e, err
	}
	side := func(obj *node, sp string, read func(*node, string, string) (string, error)) (errorsSide, error) {
		var s errorsSide
		span, err := object(obj, sp, "span_status")
		if err != nil {
			return s, err
		}
		http, err := object(obj, sp, "http_status")
		if err != nil {
			return s, err
		}
		for _, f := range []struct {
			obj        *node
			path, name string
			into       *string
		}{
			{span, join(sp, "span_status"), "unavailable", &s.spanUnavailable},
			{span, join(sp, "span_status"), "unset", &s.unset},
			{span, join(sp, "span_status"), "ok", &s.ok},
			{span, join(sp, "span_status"), "error", &s.spanError},
			{http, join(sp, "http_status"), "1xx", &s.class1},
			{http, join(sp, "http_status"), "2xx", &s.class2},
			{http, join(sp, "http_status"), "3xx", &s.class3},
			{http, join(sp, "http_status"), "4xx", &s.class4},
			{http, join(sp, "http_status"), "5xx", &s.class5},
			{http, join(sp, "http_status"), "unavailable", &s.httpUnavailable},
			{obj, sp, "http_429", &s.http429},
		} {
			if *f.into, err = read(f.obj, f.path, f.name); err != nil {
				return s, err
			}
		}
		return s, nil
	}
	for _, s := range []struct {
		name string
		into *errorsSide
	}{{"reference", &e.reference}, {"candidate", &e.candidate}} {
		obj, err := object(n, path, s.name)
		if err != nil {
			return e, err
		}
		sp := join(path, s.name)
		if *s.into, err = side(obj, sp, decimal); err != nil {
			return e, err
		}
		if s.into.runs, err = decimal(obj, sp, "runs_with_evidence"); err != nil {
			return e, err
		}
	}
	if e.comparable {
		obj, err := object(n, path, "delta")
		if err != nil {
			return e, err
		}
		d, err := side(obj, join(path, "delta"), signed)
		if err != nil {
			return e, err
		}
		e.delta = &d
	}
	return e, nil
}

func parseSuggestion(item *node, path string) (suggestion, error) {
	var s suggestion
	if item.kind != kindObject {
		return s, schemaErr(path, "is not an object")
	}
	var err error
	if s.rule, err = nonEmpty(item, path, "rule"); err != nil {
		return s, err
	}
	if _, err := integer(item, path, "rule_version", 1, 1<<31-1); err != nil {
		return s, err
	}
	if s.text, err = nonEmpty(item, path, "text"); err != nil {
		return s, err
	}
	evidence, err := array(item, path, "evidence")
	if err != nil {
		return s, err
	}
	for i, e := range evidence.items {
		ep := index(join(path, "evidence"), i)
		if e.kind != kindObject {
			return s, schemaErr(ep, "is not an object")
		}
		var ev evidenceItem
		if ev.name, err = nonEmpty(e, ep, "name"); err != nil {
			return s, err
		}
		if ev.value, err = str(e, ep, "value"); err != nil {
			return s, err
		}
		s.evidence = append(s.evidence, ev)
	}
	return s, nil
}

func parseSameness(n *node, path string) (sameness, error) {
	var s sameness
	var err error
	for _, f := range []struct {
		name string
		into *string
	}{
		{"same_scenario", &s.scenario}, {"same_inputs", &s.inputs},
		{"same_model", &s.model}, {"same_prompt_ref", &s.promptRef},
	} {
		if *f.into, err = enum(n, path, f.name, sameValues...); err != nil {
			return s, err
		}
	}
	for _, side := range []struct {
		name string
		into *sideProvenance
	}{{"reference", &s.reference}, {"candidate", &s.candidate}} {
		obj, err := object(n, path, side.name)
		if err != nil {
			return s, err
		}
		sp := join(path, side.name)
		p := side.into
		if p.inputs, err = enum(obj, sp, "inputs", "declared", "not_declared", notRecorded); err != nil {
			return s, err
		}
		for _, f := range []struct {
			name string
			into *string
		}{{"scenario_digest", &p.scenarioDigest}, {"input_digest", &p.inputDigest}, {"model", &p.model}} {
			value, present, err := optionalStr(obj, sp, f.name)
			if err != nil {
				return s, err
			}
			if present && value == "" {
				return s, schemaErr(join(sp, f.name), "is empty")
			}
			*f.into = value
		}
		prompt, err := optional(obj, sp, "prompt_ref", kindObject)
		if err != nil {
			return s, err
		}
		if prompt != nil {
			pp := join(sp, "prompt_ref")
			if p.promptName, err = nonEmpty(prompt, pp, "name"); err != nil {
				return s, err
			}
			if p.promptDigest, err = nonEmpty(prompt, pp, "digest"); err != nil {
				return s, err
			}
			p.hasPrompt = true
		}
	}
	return s, nil
}
