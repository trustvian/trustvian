package main

// The result documents `trustvian eval run --json` writes (task 078; the
// contract is docs/compatibility.md), validated into the fields this renderer
// consumes. Every value is transcribed: counts, limits and thresholds stay
// the decimal text the control plane wrote, and every classification,
// outcome and verdict is the stored one. Nothing here compares a count with a
// limit, recounts a summary or composes a verdict.
//
// Unknown fields are tolerated and ignored, as the compatibility contract
// requires of every consumer of a version-"1" document, and are never
// rendered. A field the renderer consumes is validated whatever else is
// present; a value outside a closed vocabulary, or a different version, is
// refused.

// The stored vocabularies.
const (
	verdictPass = "pass"
	verdictFail = "fail"

	classAdded   = "added"
	classRemoved = "removed"
	classNeither = "neither"

	ruleEquals = "equals"
	ruleAtMost = "at_most"

	advisoryFreshScope = "fresh_scope"

	referenceModeExecution = "execution"
	referenceModeLast      = "last"

	outcomePass    = "pass"
	outcomeFail    = "fail"
	outcomeError   = "error"
	outcomeSkipped = "skipped"

	skippedFailFast  = "fail_fast"
	skippedCancelled = "cancelled"
)

// checkOrder is the six repeated-gate checks, in the stable order the control
// plane always lists them in.
var checkOrder = []string{
	"reference_repetitions_completed",
	"candidate_repetitions_completed",
	"repetitions_failing_minimum_evidence",
	"repeatedly_added_behaviors",
	"worst_candidate_block_decisions",
	"worst_candidate_critical_risk_observations",
}

// maxRuns is the scenario file's own bound on runs per side.
const maxRuns = 64

// maxSuiteMembers is the CLI's bound on scenarios in one suite.
const maxSuiteMembers = 64

type scenarioResult struct {
	name        string
	runs        int64
	executionID string
	reference   *referenceProvenance
	cliVersion  string
	cpVersion   string
	limits      gateLimits
	behaviors   []behavior
	checks      []check
	verdict     string
}

type referenceProvenance struct {
	mode, executionID string
}

// gateLimits are the five thresholds, as the control plane echoed them.
type gateLimits struct {
	k, j, maxAdded, maxBlock, maxCritical string
}

type behavior struct {
	fingerprintID string
	// descriptor is the behavior's identity, in the wire's field order;
	// optional fields that are absent are left out.
	descriptor     []string
	referenceRuns  string
	candidateRuns  string
	classification string
}

type check struct {
	name, actual, rule, bound string
	passed                    bool
	advisory                  string
}

// parseScenarioResult validates a single-scenario result document.
func parseScenarioResult(doc *node, path string) (scenarioResult, error) {
	var r scenarioResult
	if doc.kind != kindObject {
		return r, schemaErr(rootPath(path), "is not an object")
	}
	if err := equalText(doc, path, "version", "1"); err != nil {
		return r, err
	}
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
		if p.mode, err = enum(ref, rp, "mode", referenceModeExecution, referenceModeLast); err != nil {
			return r, err
		}
		if p.executionID, err = nonEmpty(ref, rp, "execution_id"); err != nil {
			return r, err
		}
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
		// The CLI writes scenario.runs and the control plane comparison.runs
		// for the same execution; two values mean the document is not one
		// execution's.
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

	limits, err := object(comparison, cp, "gate_limits")
	if err != nil {
		return r, err
	}
	lp := join(cp, "gate_limits")
	for _, f := range []struct {
		name string
		into *string
	}{
		{"added_candidate_presence_minimum", &r.limits.k},
		{"added_reference_presence_maximum", &r.limits.j},
		{"max_repeated_added_behaviors", &r.limits.maxAdded},
		{"max_block_decisions_per_run", &r.limits.maxBlock},
		{"max_critical_risk_observations_per_run", &r.limits.maxCritical},
	} {
		if *f.into, err = decimal(limits, lp, f.name); err != nil {
			return r, err
		}
	}

	behaviors, err := array(comparison, cp, "behaviors")
	if err != nil {
		return r, err
	}
	for i, item := range behaviors.items {
		b, err := parseBehavior(item, index(join(cp, "behaviors"), i))
		if err != nil {
			return r, err
		}
		r.behaviors = append(r.behaviors, b)
	}

	gate, err := object(comparison, cp, "gate")
	if err != nil {
		return r, err
	}
	gp := join(cp, "gate")
	checks, err := array(gate, gp, "checks")
	if err != nil {
		return r, err
	}
	if len(checks.items) != len(checkOrder) {
		return r, schemaErr(join(gp, "checks"), "does not hold exactly the six repeated-gate checks")
	}
	for i, item := range checks.items {
		c, err := parseCheck(item, index(join(gp, "checks"), i), checkOrder[i])
		if err != nil {
			return r, err
		}
		r.checks = append(r.checks, c)
	}
	if r.verdict, err = enum(gate, gp, "verdict", verdictPass, verdictFail); err != nil {
		return r, err
	}
	return r, nil
}

func rootPath(path string) string {
	if path == "" {
		return "the document"
	}
	return path
}

// descriptorFields are the behavior descriptor's fields in wire order, and
// whether each is required.
var descriptorFields = []struct {
	name     string
	required bool
}{
	{"actor_type", true},
	{"operation_category", true},
	{"operation_name", false},
	{"target_name", false},
	{"target_category", false},
	{"environment", false},
}

func parseBehavior(item *node, path string) (behavior, error) {
	var b behavior
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
	b.classification, err = enum(item, path, "classification", classAdded, classRemoved, classNeither)
	return b, err
}

func parseCheck(item *node, path, wantName string) (check, error) {
	var c check
	if item.kind != kindObject {
		return c, schemaErr(path, "is not an object")
	}
	var err error
	if c.name, err = str(item, path, "name"); err != nil {
		return c, err
	}
	if c.name != wantName {
		// Each check has one place in the stable order; a check that is
		// unknown, repeated or out of place is not the six checks.
		return c, schemaErr(join(path, "name"), "is not the check the stable order defines here")
	}
	if c.actual, err = decimal(item, path, "actual"); err != nil {
		return c, err
	}
	if c.rule, err = enum(item, path, "rule", ruleEquals, ruleAtMost); err != nil {
		return c, err
	}
	if c.bound, err = decimal(item, path, "bound"); err != nil {
		return c, err
	}
	if c.passed, err = boolean(item, path, "passed"); err != nil {
		return c, err
	}
	if advisory, present, err := optionalStr(item, path, "advisory"); err != nil {
		return c, err
	} else if present {
		if advisory != advisoryFreshScope {
			return c, schemaErr(join(path, "advisory"), "is not one of its defined values")
		}
		c.advisory = advisory
	}
	return c, nil
}

type suiteResult struct {
	complete        bool
	directory       string
	scenarioCount   int64
	scenarioTimeout string
	failFast        bool
	reference       string
	members         []suiteMember
	summary         suiteSummary
	exitCode        int64
	cliVersion      string
}

type suiteSummary struct {
	passed, failed, errors, skipped int64
}

type suiteMember struct {
	file          string
	name          string
	runs          int64
	outcome       string
	exitCode      int64
	executionID   string
	result        *scenarioResult
	errorCode     string
	errorMessage  string
	skippedReason string
}

// parseSuiteResult validates a suite result document. An incomplete document
// — the CLI's own output-overflow form — is reported by complete == false
// and carries nothing else this renderer uses.
func parseSuiteResult(doc *node) (suiteResult, error) {
	var s suiteResult
	if doc.kind != kindObject {
		return s, schemaErr("the document", "is not an object")
	}
	if err := equalText(doc, "", "version", "1"); err != nil {
		return s, err
	}
	var err error
	if s.complete, err = boolean(doc, "", "complete"); err != nil {
		return s, err
	}
	if s.exitCode, err = integer(doc, "", "exit_code", 0, 3); err != nil {
		return s, err
	}
	errObj, err := optional(doc, "", "error", kindObject)
	if err != nil {
		return s, err
	}
	if !s.complete {
		// The incomplete form names why and claims no outcome; it is always
		// exit 3.
		if errObj == nil {
			return s, schemaErr("error", "required field is absent from an incomplete document")
		}
		if _, err := nonEmpty(errObj, "error", "code"); err != nil {
			return s, err
		}
		if s.exitCode != 3 {
			return s, schemaErr("exit_code", "is not 3 on an incomplete document")
		}
		return s, nil
	}
	if errObj != nil {
		return s, schemaErr("error", "is present on a complete document")
	}

	suite, err := object(doc, "", "suite")
	if err != nil {
		return s, err
	}
	if s.directory, err = nonEmpty(suite, "suite", "directory"); err != nil {
		return s, err
	}
	if s.scenarioCount, err = integer(suite, "suite", "scenario_count", 1, maxSuiteMembers); err != nil {
		return s, err
	}
	options, err := object(doc, "", "options")
	if err != nil {
		return s, err
	}
	if s.scenarioTimeout, err = nonEmpty(options, "options", "scenario_timeout"); err != nil {
		return s, err
	}
	if s.failFast, err = boolean(options, "options", "fail_fast"); err != nil {
		return s, err
	}
	if ref, present, err := optionalStr(options, "options", "reference"); err != nil {
		return s, err
	} else if present {
		if ref != referenceModeLast {
			return s, schemaErr("options.reference", "is not one of its defined values")
		}
		s.reference = ref
	}
	producers, err := object(doc, "", "producers")
	if err != nil {
		return s, err
	}
	if s.cliVersion, err = nonEmpty(producers, "producers", "cli_version"); err != nil {
		return s, err
	}
	summary, err := object(doc, "", "summary")
	if err != nil {
		return s, err
	}
	for _, f := range []struct {
		name string
		into *int64
	}{
		{"passed", &s.summary.passed}, {"failed", &s.summary.failed},
		{"errors", &s.summary.errors}, {"skipped", &s.summary.skipped},
	} {
		if *f.into, err = integer(summary, "summary", f.name, 0, maxSuiteMembers); err != nil {
			return s, err
		}
	}

	members, err := array(doc, "", "members")
	if err != nil {
		return s, err
	}
	if int64(len(members.items)) != s.scenarioCount {
		// One member per discovered file: a different number means members
		// are missing or invented.
		return s, schemaErr("members", "does not hold suite.scenario_count members")
	}
	for i, item := range members.items {
		m, err := parseMember(item, index("members", i))
		if err != nil {
			return s, err
		}
		s.members = append(s.members, m)
	}
	return s, nil
}

// parseMember validates one member against its outcome's shape: a pass or
// fail carries its exit code and result document, an error its exit code and
// error, and a skipped member only why it was skipped.
func parseMember(item *node, path string) (suiteMember, error) {
	var m suiteMember
	if item.kind != kindObject {
		return m, schemaErr(path, "is not an object")
	}
	var err error
	if m.file, err = nonEmpty(item, path, "file"); err != nil {
		return m, err
	}
	scenario, err := object(item, path, "scenario")
	if err != nil {
		return m, err
	}
	sp := join(path, "scenario")
	if m.name, err = nonEmpty(scenario, sp, "name"); err != nil {
		return m, err
	}
	if m.runs, err = integer(scenario, sp, "runs", 1, maxRuns); err != nil {
		return m, err
	}
	if m.outcome, err = enum(item, path, "outcome", outcomePass, outcomeFail, outcomeError, outcomeSkipped); err != nil {
		return m, err
	}
	executionID, hasExecution, err := optionalStr(item, path, "execution_id")
	if err != nil {
		return m, err
	}
	m.executionID = executionID
	resultNode, err := optional(item, path, "result", kindObject)
	if err != nil {
		return m, err
	}
	errObj, err := optional(item, path, "error", kindObject)
	if err != nil {
		return m, err
	}
	reason, hasReason, err := optionalStr(item, path, "skipped_reason")
	if err != nil {
		return m, err
	}
	_, hasExit := item.fields["exit_code"]

	forbid := func(present bool, name string) error {
		if present {
			return schemaErr(join(path, name), "is present on a member whose outcome has none")
		}
		return nil
	}
	switch m.outcome {
	case outcomePass, outcomeFail:
		want := int64(0)
		if m.outcome == outcomeFail {
			want = 1
		}
		if m.exitCode, err = integer(item, path, "exit_code", want, want); err != nil {
			return m, err
		}
		if !hasExecution || executionID == "" {
			return m, schemaErr(join(path, "execution_id"), "required field is absent")
		}
		if resultNode == nil {
			return m, schemaErr(join(path, "result"), "required field is absent")
		}
		if err := forbid(errObj != nil, "error"); err != nil {
			return m, err
		}
		if err := forbid(hasReason, "skipped_reason"); err != nil {
			return m, err
		}
		result, err := parseScenarioResult(resultNode, join(path, "result"))
		if err != nil {
			return m, err
		}
		// The embedded document is this member's own: same scenario, same
		// execution, and the verdict its outcome reports.
		switch {
		case result.name != m.name || result.runs != m.runs:
			return m, schemaErr(join(path, "result.scenario"), "differs from the member's scenario")
		case result.executionID != m.executionID:
			return m, schemaErr(join(path, "result.execution_id"), "differs from the member's execution_id")
		case result.verdict != m.outcome:
			return m, schemaErr(join(path, "result.comparison.gate.verdict"), "differs from the member's outcome")
		}
		m.result = &result
	case outcomeError:
		if m.exitCode, err = integer(item, path, "exit_code", 2, 3); err != nil {
			return m, err
		}
		if errObj == nil {
			return m, schemaErr(join(path, "error"), "required field is absent")
		}
		ep := join(path, "error")
		if m.errorCode, err = nonEmpty(errObj, ep, "code"); err != nil {
			return m, err
		}
		if m.errorMessage, err = str(errObj, ep, "message"); err != nil {
			return m, err
		}
		if err := forbid(resultNode != nil, "result"); err != nil {
			return m, err
		}
		if err := forbid(hasReason, "skipped_reason"); err != nil {
			return m, err
		}
	case outcomeSkipped:
		if err := forbid(hasExit, "exit_code"); err != nil {
			return m, err
		}
		if err := forbid(hasExecution, "execution_id"); err != nil {
			return m, err
		}
		if err := forbid(resultNode != nil, "result"); err != nil {
			return m, err
		}
		if err := forbid(errObj != nil, "error"); err != nil {
			return m, err
		}
		if !hasReason {
			return m, schemaErr(join(path, "skipped_reason"), "required field is absent")
		}
		if reason != skippedFailFast && reason != skippedCancelled {
			return m, schemaErr(join(path, "skipped_reason"), "is not one of its defined values")
		}
		m.skippedReason = reason
	}
	return m, nil
}
