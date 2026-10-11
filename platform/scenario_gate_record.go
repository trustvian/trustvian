package platform

// The gate-result snapshot a scenario execution records when it completes
// (task 107, decision D10, ADR 0069).
//
// The decision keeps the gate evidence it consumed, by task 066's rule: the
// limits it was evaluated under, every check of gate.checks and
// gate.frequency_checks with its state, actual value, bound, outcome and
// missing evidence, and the verdict. A report of the execution reads this
// record; it never re-evaluates the gate with today's code or with limits
// supplied later. An execution completed before schema 14 has no record, and
// its report says not_recorded.
//
// Stored as one column of fixed-shape canonical text: every field always
// present, in one order, no maps. A stored value that does not re-encode to
// itself byte for byte, or whose checks contradict its limits, its N or its
// verdict, is refused as corrupt.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// scenarioGateRecordVersion is the encoding's own version, inside the text.
const scenarioGateRecordVersion = "1"

// columnGateSnapshot is schema 14's column on the scenario execution table.
const columnGateSnapshot = "gate_snapshot"

// schemaVersionV13 is task 081's schema, the last version without the gate
// snapshot. v14 adds a column only, so v13 and v14 hold the same tables and
// are told apart by the stamp.
const schemaVersionV13 = 13

// ScenarioGateRecord is what one completed execution's gate decided, as it
// decided it. The zero value is "not recorded".
type ScenarioGateRecord struct {
	recorded bool
	limits   RepeatedEvaluationGateLimits
	gate     RepeatedEvaluationGateResult
}

// Recorded reports whether the execution recorded its gate. False for an
// execution completed before schema 14.
func (r ScenarioGateRecord) Recorded() bool { return r.recorded }

// Limits returns the limits the gate was evaluated under.
func (r ScenarioGateRecord) Limits() RepeatedEvaluationGateLimits {
	out := r.limits
	out.MaxCallsPerRun = cloneTargetLimits(r.limits.MaxCallsPerRun)
	return out
}

// Gate returns the checks and the verdict, as recorded.
func (r ScenarioGateRecord) Gate() RepeatedEvaluationGateResult { return r.gate }

// NewScenarioGateRecord builds a record from its parts, refusing any that
// contradict each other: a check whose outcome disagrees with its actual value
// and bound, a bound that is not the limit it names, a frequency check whose
// state disagrees with whether its limit was supplied, or a verdict that is not
// what the checks give. runs is the execution's N.
func NewScenarioGateRecord(
	runs int, limits RepeatedEvaluationGateLimits,
	checks []RepeatedGateCheck, frequency []FrequencyGateCheck, verdict GateVerdict,
) (ScenarioGateRecord, error) {
	if len(checks) != 6 || len(frequency) != FrequencyCheckCount {
		return ScenarioGateRecord{}, fmt.Errorf("%w: a gate record holds 6 checks and %d frequency checks, "+
			"not %d and %d", ErrInvalidRepeatedRequest, FrequencyCheckCount, len(checks), len(frequency))
	}
	r := ScenarioGateRecord{recorded: true, limits: limits}
	r.limits.MaxCallsPerRun = cloneTargetLimits(limits.MaxCallsPerRun)
	r.gate = RepeatedEvaluationGateResult{bound: true, verdict: verdict}
	copy(r.gate.checks[:], checks)
	for i, c := range frequency {
		c.Targets = slices.Clone(c.Targets)
		r.gate.frequency[i] = c
	}
	if err := r.validate(runs); err != nil {
		return ScenarioGateRecord{}, err
	}
	return r, nil
}

// gateRecord is the comparison's gate as an execution records it.
func (c RepeatedEvaluationComparison) gateRecord() (ScenarioGateRecord, error) {
	return NewScenarioGateRecord(c.Runs, c.Limits, c.Gate.Checks(), c.Gate.FrequencyChecks(), c.Gate.Verdict())
}

// validate checks a recorded gate against itself and the execution's N.
func (r ScenarioGateRecord) validate(runs int) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: gate record: "+format, append([]any{ErrInvalidRepeatedRequest}, args...)...)
	}
	if !r.recorded {
		return invalid("not recorded")
	}
	l, n := r.limits, uint64(runs)
	if err := validateRepeatedLimits(l, runs); err != nil {
		return err
	}

	advisory := ""
	if runs > 1 {
		advisory = AdvisoryFreshScope
	}
	want := [6]struct {
		name     RepeatedCheckName
		rule     RepeatedCheckRule
		bound    uint64
		advisory string
	}{
		{CheckReferenceRepetitionsCompleted, RuleEquals, n, ""},
		{CheckCandidateRepetitionsCompleted, RuleEquals, n, ""},
		{CheckRepetitionsFailingMinimum, RuleAtMost, 0, ""},
		{CheckRepeatedlyAddedBehaviors, RuleAtMost, l.MaxRepeatedAddedBehaviors, ""},
		{CheckWorstCandidateBlockDecisions, RuleAtMost, l.MaxBlockDecisionsPerRun, advisory},
		{CheckWorstCandidateCriticalRiskCount, RuleAtMost, l.MaxCriticalRiskObservationsPerRun, advisory},
	}
	passed := true
	for i, c := range r.gate.checks {
		w := want[i]
		if c.Name != w.name || c.Rule != w.rule || c.Bound != w.bound || c.Advisory != w.advisory {
			return invalid("check %d is %s %s %d (advisory %q); want %s %s %d (advisory %q)",
				i+1, preview(string(c.Name)), preview(string(c.Rule)), c.Bound, preview(c.Advisory),
				w.name, w.rule, w.bound, w.advisory)
		}
		if c.Passed != ruleHolds(c.Rule, c.Actual, c.Bound) {
			return invalid("check %s says passed %v at actual %d, bound %d", c.Name, c.Passed, c.Actual, c.Bound)
		}
		passed = passed && c.Passed
	}

	names := [FrequencyCheckCount]FrequencyCheckName{
		CheckMinCandidateFrequency, CheckMaxLostBehaviors, CheckMaxCallsPerRun, CheckMaxLLMCallsPerRun,
	}
	optional := [FrequencyCheckCount]OptionalGateLimit{
		l.MinCandidateFrequency, l.MaxLostBehaviors, {}, l.MaxLLMCallsPerRun,
	}
	if l.MaxCallsPerRun != nil {
		optional[2] = NewOptionalGateLimit(0)
	}
	for i, c := range r.gate.frequency {
		if c.Name != names[i] {
			return invalid("frequency check %d is %s; want %s", i+1, preview(string(c.Name)), names[i])
		}
		limit, set := optional[i].Maximum()
		switch {
		case !set:
			if c.State != GateCheckNotEvaluated {
				return invalid("%s is %s, but its limit was omitted", c.Name, preview(string(c.State)))
			}
			if !c.blank() || c.MissingEvidence != "" {
				return invalid("%s was not evaluated and carries an outcome", c.Name)
			}
		case c.State == GateCheckDeferred:
			if !c.blank() || c.MissingEvidence == "" {
				return invalid("%s is deferred and must name only its missing evidence", c.Name)
			}
			if err := validateText(ErrInvalidRepeatedRequest, "missing_evidence", c.MissingEvidence); err != nil {
				return err
			}
		case c.State == GateCheckEvaluated:
			if c.MissingEvidence != "" {
				return invalid("%s was evaluated and names missing evidence", c.Name)
			}
			if err := c.validateEvaluated(l, limit, n); err != nil {
				return invalid("%v", err)
			}
		default:
			return invalid("%s has state %s, supplied limits are evaluated or deferred",
				c.Name, preview(string(c.State)))
		}
		passed = passed && !c.failsVerdict()
	}

	wantVerdict := GateVerdictFail
	if passed {
		wantVerdict = GateVerdictPass
	}
	if r.gate.verdict != wantVerdict {
		return invalid("verdict %s; the checks give %s", preview(string(r.gate.verdict)), wantVerdict)
	}
	return nil
}

// blank reports whether a frequency check carries no outcome at all.
func (c FrequencyGateCheck) blank() bool {
	return c.Rule == "" && c.Actual == 0 && c.Bound == 0 && !c.Passed && len(c.Targets) == 0
}

// validateEvaluated checks one evaluated frequency check's outcome.
func (c FrequencyGateCheck) validateEvaluated(l RepeatedEvaluationGateLimits, limit, runs uint64) error {
	if c.Name != CheckMaxCallsPerRun {
		rule := RuleAtMost
		if c.Name == CheckMinCandidateFrequency {
			rule = RuleAtLeast
			if c.Actual > runs {
				return fmt.Errorf("%s has actual %d, above N %d", c.Name, c.Actual, runs)
			}
		}
		if c.Rule != rule || c.Bound != limit || len(c.Targets) != 0 {
			return fmt.Errorf("%s is %s bound %d; want %s bound %d", c.Name, preview(string(c.Rule)), c.Bound, rule, limit)
		}
		if c.Passed != ruleHolds(c.Rule, c.Actual, c.Bound) {
			return fmt.Errorf("%s says passed %v at actual %d, bound %d", c.Name, c.Passed, c.Actual, c.Bound)
		}
		return nil
	}
	// max_calls_per_run: one outcome per configured target, in order; the
	// check's own actual and bound are unused.
	if c.Rule != RuleAtMost || c.Actual != 0 || c.Bound != 0 || len(c.Targets) != len(l.MaxCallsPerRun) {
		return fmt.Errorf("%s does not hold one outcome per configured target", c.Name)
	}
	passed := true
	for i, t := range c.Targets {
		if t.Target != l.MaxCallsPerRun[i].Target || t.Max != l.MaxCallsPerRun[i].Max {
			return fmt.Errorf("%s target %d is %s <= %d; the limit is %s <= %d", c.Name, i+1,
				preview(t.Target), t.Max, preview(l.MaxCallsPerRun[i].Target), l.MaxCallsPerRun[i].Max)
		}
		var holds bool
		switch t.Outcome {
		case TargetCallPass:
			holds = t.Actual <= t.Max
		case TargetCallFail:
			holds = t.Actual > t.Max
		case TargetCallNotObserved:
			holds = t.Actual == 0
		}
		if !holds {
			return fmt.Errorf("%s target %s is %s at actual %d, max %d", c.Name, preview(t.Target),
				preview(string(t.Outcome)), t.Actual, t.Max)
		}
		passed = passed && t.Outcome == TargetCallPass
	}
	if c.Passed != passed {
		return fmt.Errorf("%s says passed %v; its targets give %v", c.Name, c.Passed, passed)
	}
	return nil
}

// ruleHolds is a check's rule applied to its own recorded numbers — what
// makes a stored outcome that contradicts them detectable.
func ruleHolds(rule RepeatedCheckRule, actual, bound uint64) bool {
	switch rule {
	case RuleEquals:
		return actual == bound
	case RuleAtMost:
		return actual <= bound
	case RuleAtLeast:
		return actual >= bound
	}
	return false
}

// The stored text's shape. Every field is always written: no omitempty, so the
// shape is fixed and a stored value can be checked by re-encoding it. An
// omitted optional limit is null, a supplied one a canonical decimal.
type gateRecordText struct {
	Version         string                    `json:"version"`
	Limits          gateRecordLimitsText      `json:"limits"`
	Checks          []gateRecordCheckText     `json:"checks"`
	FrequencyChecks []gateRecordFrequencyText `json:"frequency_checks"`
	Verdict         string                    `json:"verdict"`
}

type gateRecordLimitsText struct {
	AddedCandidatePresenceMinimum     string                 `json:"added_candidate_presence_minimum"`
	AddedReferencePresenceMaximum     string                 `json:"added_reference_presence_maximum"`
	MaxRepeatedAddedBehaviors         string                 `json:"max_repeated_added_behaviors"`
	MaxBlockDecisionsPerRun           string                 `json:"max_block_decisions_per_run"`
	MaxCriticalRiskObservationsPerRun string                 `json:"max_critical_risk_observations_per_run"`
	MinCandidateFrequency             *string                `json:"min_candidate_frequency"`
	MaxLostBehaviors                  *string                `json:"max_lost_behaviors"`
	MaxCallsPerRun                    []gateRecordTargetText `json:"max_calls_per_run"`
	MaxLLMCallsPerRun                 *string                `json:"max_llm_calls_per_run"`
}

type gateRecordTargetText struct {
	Target string `json:"target"`
	Max    string `json:"max"`
}

type gateRecordCheckText struct {
	Name     string `json:"name"`
	Actual   string `json:"actual"`
	Rule     string `json:"rule"`
	Bound    string `json:"bound"`
	Passed   bool   `json:"passed"`
	Advisory string `json:"advisory"`
}

type gateRecordFrequencyText struct {
	Name            string                      `json:"name"`
	State           string                      `json:"state"`
	Rule            string                      `json:"rule"`
	Actual          string                      `json:"actual"`
	Bound           string                      `json:"bound"`
	Passed          bool                        `json:"passed"`
	Targets         []gateRecordTargetCheckText `json:"targets"`
	MissingEvidence string                      `json:"missing_evidence"`
}

type gateRecordTargetCheckText struct {
	Target  string `json:"target"`
	Actual  string `json:"actual"`
	Max     string `json:"max"`
	Outcome string `json:"outcome"`
}

// encodeScenarioGateRecord renders a record as gate_snapshot stores it.
func encodeScenarioGateRecord(r ScenarioGateRecord) (string, error) {
	optional := func(l OptionalGateLimit) *string {
		if v, set := l.Maximum(); set {
			text := uint64Text(v)
			return &text
		}
		return nil
	}
	l := r.limits
	text := gateRecordText{
		Version: scenarioGateRecordVersion,
		Limits: gateRecordLimitsText{
			AddedCandidatePresenceMinimum:     uint64Text(l.AddedCandidatePresenceMinimum),
			AddedReferencePresenceMaximum:     uint64Text(l.AddedReferencePresenceMaximum),
			MaxRepeatedAddedBehaviors:         uint64Text(l.MaxRepeatedAddedBehaviors),
			MaxBlockDecisionsPerRun:           uint64Text(l.MaxBlockDecisionsPerRun),
			MaxCriticalRiskObservationsPerRun: uint64Text(l.MaxCriticalRiskObservationsPerRun),
			MinCandidateFrequency:             optional(l.MinCandidateFrequency),
			MaxLostBehaviors:                  optional(l.MaxLostBehaviors),
			MaxLLMCallsPerRun:                 optional(l.MaxLLMCallsPerRun),
		},
		Checks:          make([]gateRecordCheckText, 0, len(r.gate.checks)),
		FrequencyChecks: make([]gateRecordFrequencyText, 0, len(r.gate.frequency)),
		Verdict:         string(r.gate.verdict),
	}
	if l.MaxCallsPerRun != nil {
		text.Limits.MaxCallsPerRun = make([]gateRecordTargetText, 0, len(l.MaxCallsPerRun))
		for _, t := range l.MaxCallsPerRun {
			text.Limits.MaxCallsPerRun = append(text.Limits.MaxCallsPerRun,
				gateRecordTargetText{Target: t.Target, Max: uint64Text(t.Max)})
		}
	}
	for _, c := range r.gate.checks {
		text.Checks = append(text.Checks, gateRecordCheckText{
			Name: string(c.Name), Actual: uint64Text(c.Actual), Rule: string(c.Rule),
			Bound: uint64Text(c.Bound), Passed: c.Passed, Advisory: c.Advisory,
		})
	}
	for _, c := range r.gate.frequency {
		f := gateRecordFrequencyText{
			Name: string(c.Name), State: string(c.State), Rule: string(c.Rule),
			Actual: uint64Text(c.Actual), Bound: uint64Text(c.Bound), Passed: c.Passed,
			Targets: make([]gateRecordTargetCheckText, 0, len(c.Targets)), MissingEvidence: c.MissingEvidence,
		}
		for _, t := range c.Targets {
			f.Targets = append(f.Targets, gateRecordTargetCheckText{
				Target: t.Target, Actual: uint64Text(t.Actual), Max: uint64Text(t.Max), Outcome: string(t.Outcome),
			})
		}
		text.FrequencyChecks = append(text.FrequencyChecks, f)
	}
	raw, err := json.Marshal(text)
	if err != nil {
		return "", fmt.Errorf("platform: encode gate record: %w", err)
	}
	return string(raw), nil
}

// decodeScenarioGateRecord parses gate_snapshot for an execution of N runs.
// Anything but the exact text encodeScenarioGateRecord writes for a valid
// record is corruption.
func decodeScenarioGateRecord(stored string, runs int) (ScenarioGateRecord, error) {
	corrupt := func(format string, args ...any) (ScenarioGateRecord, error) {
		return ScenarioGateRecord{}, fmt.Errorf("%w: %s: "+format,
			append([]any{ErrStoreCorrupt, columnGateSnapshot}, args...)...)
	}
	var text gateRecordText
	decoder := json.NewDecoder(bytes.NewReader([]byte(stored)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&text); err != nil {
		return corrupt("not a gate record: %v", err)
	}
	if decoder.More() {
		return corrupt("trailing data after the record")
	}
	if text.Version != scenarioGateRecordVersion {
		return corrupt("encoding version %q", preview(text.Version))
	}
	number := func(field, s string) (uint64, error) {
		v, ok := parseCanonicalUint64(s)
		if !ok {
			return 0, fmt.Errorf("%w: %s: %s is not a canonical uint64: %q",
				ErrStoreCorrupt, columnGateSnapshot, field, preview(s))
		}
		return v, nil
	}
	var (
		limits RepeatedEvaluationGateLimits
		err    error
	)
	for _, f := range []struct {
		name string
		text string
		into *uint64
	}{
		{"added_candidate_presence_minimum", text.Limits.AddedCandidatePresenceMinimum, &limits.AddedCandidatePresenceMinimum},
		{"added_reference_presence_maximum", text.Limits.AddedReferencePresenceMaximum, &limits.AddedReferencePresenceMaximum},
		{"max_repeated_added_behaviors", text.Limits.MaxRepeatedAddedBehaviors, &limits.MaxRepeatedAddedBehaviors},
		{"max_block_decisions_per_run", text.Limits.MaxBlockDecisionsPerRun, &limits.MaxBlockDecisionsPerRun},
		{"max_critical_risk_observations_per_run", text.Limits.MaxCriticalRiskObservationsPerRun,
			&limits.MaxCriticalRiskObservationsPerRun},
	} {
		if *f.into, err = number(f.name, f.text); err != nil {
			return ScenarioGateRecord{}, err
		}
	}
	for _, f := range []struct {
		name string
		text *string
		into *OptionalGateLimit
	}{
		{"min_candidate_frequency", text.Limits.MinCandidateFrequency, &limits.MinCandidateFrequency},
		{"max_lost_behaviors", text.Limits.MaxLostBehaviors, &limits.MaxLostBehaviors},
		{"max_llm_calls_per_run", text.Limits.MaxLLMCallsPerRun, &limits.MaxLLMCallsPerRun},
	} {
		if f.text == nil {
			continue
		}
		v, err := number(f.name, *f.text)
		if err != nil {
			return ScenarioGateRecord{}, err
		}
		*f.into = NewOptionalGateLimit(v)
	}
	if text.Limits.MaxCallsPerRun != nil {
		limits.MaxCallsPerRun = make([]TargetCallLimit, 0, len(text.Limits.MaxCallsPerRun))
		for _, t := range text.Limits.MaxCallsPerRun {
			v, err := number("max_calls_per_run.max", t.Max)
			if err != nil {
				return ScenarioGateRecord{}, err
			}
			limits.MaxCallsPerRun = append(limits.MaxCallsPerRun, TargetCallLimit{Target: t.Target, Max: v})
		}
	}

	checks := make([]RepeatedGateCheck, 0, len(text.Checks))
	for _, c := range text.Checks {
		actual, err := number("check actual", c.Actual)
		if err != nil {
			return ScenarioGateRecord{}, err
		}
		bound, err := number("check bound", c.Bound)
		if err != nil {
			return ScenarioGateRecord{}, err
		}
		checks = append(checks, RepeatedGateCheck{
			Name: RepeatedCheckName(c.Name), Actual: actual, Rule: RepeatedCheckRule(c.Rule),
			Bound: bound, Passed: c.Passed, Advisory: c.Advisory,
		})
	}
	frequency := make([]FrequencyGateCheck, 0, len(text.FrequencyChecks))
	for _, c := range text.FrequencyChecks {
		actual, err := number("frequency check actual", c.Actual)
		if err != nil {
			return ScenarioGateRecord{}, err
		}
		bound, err := number("frequency check bound", c.Bound)
		if err != nil {
			return ScenarioGateRecord{}, err
		}
		check := FrequencyGateCheck{
			Name: FrequencyCheckName(c.Name), State: GateCheckState(c.State), Rule: RepeatedCheckRule(c.Rule),
			Actual: actual, Bound: bound, Passed: c.Passed, MissingEvidence: c.MissingEvidence,
		}
		for _, t := range c.Targets {
			tActual, err := number("target actual", t.Actual)
			if err != nil {
				return ScenarioGateRecord{}, err
			}
			tMax, err := number("target max", t.Max)
			if err != nil {
				return ScenarioGateRecord{}, err
			}
			check.Targets = append(check.Targets, TargetCallCheck{
				Target: t.Target, Actual: tActual, Max: tMax, Outcome: TargetCallOutcome(t.Outcome),
			})
		}
		frequency = append(frequency, check)
	}

	record, err := NewScenarioGateRecord(runs, limits, checks, frequency, GateVerdict(text.Verdict))
	if err != nil {
		return corrupt("%v", err)
	}
	// The fixed shape, proved: a valid record that was stored in any other
	// form — reordered, padded, a field missing and defaulted — is not what
	// any write produced.
	canonical, err := encodeScenarioGateRecord(record)
	if err != nil {
		return ScenarioGateRecord{}, err
	}
	if canonical != stored {
		return corrupt("not in canonical form")
	}
	return record, nil
}

// scenarioGateSchemaStatements is schema 14's whole change, in either
// dialect: one nullable column, no backfill. NULL is "not recorded", which is
// what every execution completed before schema 14 holds — the step invents no
// gate for a decision it did not see. Not an identifier and never ordered, so
// plain TEXT on PostgreSQL too.
func scenarioGateSchemaStatements() []string {
	return []string{`ALTER TABLE ` + tableScenarioExecutions + ` ADD COLUMN ` + columnGateSnapshot + ` TEXT`}
}
