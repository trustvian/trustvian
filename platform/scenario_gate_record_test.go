package platform

// The gate-result snapshot (task 107, D10): built from a real reduction,
// stored as canonical text, and refused as corrupt in any other form.

import (
	"database/sql"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

// recordedGate is a valid gate record for an execution of N runs with the
// given verdict, for store tests that complete executions directly. A FAIL
// fails check 4 with one repeatedly added behavior.
func recordedGate(runs int, verdict GateVerdict) ScenarioGateRecord {
	n := uint64(runs)
	advisory := ""
	if runs > 1 {
		advisory = AdvisoryFreshScope
	}
	added := uint64(0)
	if verdict == GateVerdictFail {
		added = 1
	}
	record, err := NewScenarioGateRecord(runs, RepeatedEvaluationGateLimits{AddedCandidatePresenceMinimum: 1},
		[]RepeatedGateCheck{
			equalsCheck(CheckReferenceRepetitionsCompleted, n, n),
			equalsCheck(CheckCandidateRepetitionsCompleted, n, n),
			atMostCheck(CheckRepetitionsFailingMinimum, 0, 0, ""),
			atMostCheck(CheckRepeatedlyAddedBehaviors, added, 0, ""),
			atMostCheck(CheckWorstCandidateBlockDecisions, 0, 0, advisory),
			atMostCheck(CheckWorstCandidateCriticalRiskCount, 0, 0, advisory),
		}, notEvaluatedFrequency(), verdict)
	if err != nil {
		panic(err)
	}
	return record
}

func notEvaluatedFrequency() []FrequencyGateCheck {
	return []FrequencyGateCheck{
		{Name: CheckMinCandidateFrequency, State: GateCheckNotEvaluated},
		{Name: CheckMaxLostBehaviors, State: GateCheckNotEvaluated},
		{Name: CheckMaxCallsPerRun, State: GateCheckNotEvaluated},
		{Name: CheckMaxLLMCallsPerRun, State: GateCheckNotEvaluated},
	}
}

// reducedGateRecord reduces two repetitions per side under every optional
// limit — an evaluated, a deferred and a per-target check among them — and
// returns the record a completion would store.
func reducedGateRecord(t *testing.T) ScenarioGateRecord {
	t.Helper()
	limits := RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: 2, AddedReferencePresenceMaximum: 0,
		MaxRepeatedAddedBehaviors: 0, MaxBlockDecisionsPerRun: 0, MaxCriticalRiskObservationsPerRun: 0,
		MinCandidateFrequency: NewOptionalGateLimit(2),
		MaxLostBehaviors:      NewOptionalGateLimit(0),
		MaxCallsPerRun:        []TargetCallLimit{{Target: "api.example", Max: 3}, {Target: "never.called", Max: 1}},
		MaxLLMCallsPerRun:     NewOptionalGateLimit(5),
	}
	inputs := []repetitionInput{
		completedInput(SideReference, 1, "read"),
		completedInput(SideReference, 2, "read"),
		completedInput(SideCandidate, 1, "read", "export"),
		completedInput(SideCandidate, 2, "read", "export"),
	}
	comparison, err := reduceRepeated(limits, 2, inputs)
	if err != nil {
		t.Fatal(err)
	}
	record, err := comparison.gateRecord()
	if err != nil {
		t.Fatalf("gateRecord() error = %v", err)
	}
	return record
}

func TestGateRecordRoundTripsThroughItsCanonicalText(t *testing.T) {
	record := reducedGateRecord(t)
	frequency := record.Gate().FrequencyChecks()
	if frequency[2].State != GateCheckEvaluated || len(frequency[2].Targets) != 2 ||
		frequency[3].State != GateCheckDeferred || record.Gate().Verdict() != GateVerdictFail {
		t.Fatalf("the fixture does not cover a per-target and a deferred check: %+v", frequency)
	}

	text, err := encodeScenarioGateRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeScenarioGateRecord(text, 2)
	if err != nil {
		t.Fatalf("decode error = %v\n%s", err, text)
	}
	if !reflect.DeepEqual(decoded, record) {
		t.Fatalf("decoded %+v\nwant    %+v", decoded, record)
	}
	again, err := encodeScenarioGateRecord(decoded)
	if err != nil || again != text {
		t.Fatalf("re-encoding changed the text:\n%s\n%s", again, text)
	}
	// Fixed shape: every key present on every check, whatever its state.
	if strings.Count(text, `"missing_evidence":`) != FrequencyCheckCount || strings.Count(text, `"advisory":`) != 6 {
		t.Fatalf("the encoding left a field out:\n%s", text)
	}
}

// TestGateRecordEncodingIsPinned: the stored text is a contract with every
// database already holding one. A change here is a new encoding version.
func TestGateRecordEncodingIsPinned(t *testing.T) {
	text, err := encodeScenarioGateRecord(recordedGate(1, GateVerdictPass))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":"1","limits":{"added_candidate_presence_minimum":"1",` +
		`"added_reference_presence_maximum":"0","max_repeated_added_behaviors":"0",` +
		`"max_block_decisions_per_run":"0","max_critical_risk_observations_per_run":"0",` +
		`"min_candidate_frequency":null,"max_lost_behaviors":null,"max_calls_per_run":null,` +
		`"max_llm_calls_per_run":null},"checks":[` +
		`{"name":"reference_repetitions_completed","actual":"1","rule":"equals","bound":"1","passed":true,"advisory":""},` +
		`{"name":"candidate_repetitions_completed","actual":"1","rule":"equals","bound":"1","passed":true,"advisory":""},` +
		`{"name":"repetitions_failing_minimum_evidence","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":""},` +
		`{"name":"repeatedly_added_behaviors","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":""},` +
		`{"name":"worst_candidate_block_decisions","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":""},` +
		`{"name":"worst_candidate_critical_risk_observations","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":""}],` +
		`"frequency_checks":[` +
		`{"name":"min_candidate_frequency","state":"not_evaluated","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":""},` +
		`{"name":"max_lost_behaviors","state":"not_evaluated","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":""},` +
		`{"name":"max_calls_per_run","state":"not_evaluated","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":""},` +
		`{"name":"max_llm_calls_per_run","state":"not_evaluated","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":""}],` +
		`"verdict":"pass"}`
	if text != want {
		t.Fatalf("encoding\n%s\nwant\n%s", text, want)
	}
}

// TestGateRecordRefusesDamage: text no completion could have written is
// corruption, whether it is malformed, not canonical, or self-contradictory.
func TestGateRecordRefusesDamage(t *testing.T) {
	valid, err := encodeScenarioGateRecord(reducedGateRecord(t))
	if err != nil {
		t.Fatal(err)
	}
	edit := func(old, replacement string) string {
		if !strings.Contains(valid, old) {
			t.Fatalf("fixture lacks %q", old)
		}
		return strings.Replace(valid, old, replacement, 1)
	}
	for _, tt := range []struct {
		name, text string
		runs       int
	}{
		{"empty", "", 2},
		{"not json", "pass", 2},
		{"another encoding version", edit(`"version":"1"`, `"version":"2"`), 2},
		{"unknown field", edit(`"verdict":`, `"note":"x","verdict":`), 2},
		{"trailing value", valid + `{}`, 2},
		{"padded", " " + valid, 2},
		{"a field missing", edit(`,"advisory":""}`, `}`), 2},
		{"leading zero", edit(`"actual":"2"`, `"actual":"02"`), 2},
		{"negative", edit(`"actual":"2"`, `"actual":"-2"`), 2},
		{"another N", valid, 3},
		{"verdict flipped", edit(`"verdict":"fail"`, `"verdict":"pass"`), 2},
		{"outcome contradicts numbers", edit(`"name":"repeatedly_added_behaviors","actual":"1","rule":"at_most","bound":"0","passed":false`,
			`"name":"repeatedly_added_behaviors","actual":"1","rule":"at_most","bound":"0","passed":true`), 2},
		{"bound is not the limit", edit(`"name":"repeatedly_added_behaviors","actual":"1","rule":"at_most","bound":"0"`,
			`"name":"repeatedly_added_behaviors","actual":"1","rule":"at_most","bound":"1"`), 2},
		{"checks reordered", edit(`"name":"reference_repetitions_completed"`, `"name":"candidate_repetitions_completed"`), 2},
		{"evaluated without its limit", edit(`"min_candidate_frequency":"2"`, `"min_candidate_frequency":null`), 2},
		{"deferred without missing evidence", edit(`"state":"deferred","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":"run`,
			`"state":"deferred","rule":"","actual":"0","bound":"0","passed":false,"targets":[],"missing_evidence":"","x":"run`), 2},
		{"target outcome contradicts", edit(`"outcome":"not_observed"`, `"outcome":"fail"`), 2},
		{"target renamed", edit(`"target":"never.called","actual"`, `"target":"other","actual"`), 2},
		{"unknown state", edit(`"state":"deferred"`, `"state":"skipped"`), 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeScenarioGateRecord(tt.text, tt.runs); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("decode error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

func TestNewScenarioGateRecordRefusesContradictions(t *testing.T) {
	good := recordedGate(2, GateVerdictPass)
	checks, frequency := good.Gate().Checks(), good.Gate().FrequencyChecks()
	for _, tt := range []struct {
		name      string
		runs      int
		limits    RepeatedEvaluationGateLimits
		checks    []RepeatedGateCheck
		frequency []FrequencyGateCheck
		verdict   GateVerdict
	}{
		{"five checks", 2, good.Limits(), checks[:5], frequency, GateVerdictPass},
		{"three frequency checks", 2, good.Limits(), checks, frequency[:3], GateVerdictPass},
		{"k above N", 2, RepeatedEvaluationGateLimits{AddedCandidatePresenceMinimum: 3}, checks, frequency, GateVerdictPass},
		{"a FAIL whose checks all passed", 2, good.Limits(), checks, frequency, GateVerdictFail},
		{"no verdict", 2, good.Limits(), checks, frequency, ""},
		{"advisory missing at N > 1", 2, good.Limits(), func() []RepeatedGateCheck {
			c := good.Gate().Checks()
			c[4].Advisory = ""
			return c
		}(), frequency, GateVerdictPass},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewScenarioGateRecord(tt.runs, tt.limits, tt.checks, tt.frequency, tt.verdict); !errors.Is(err, ErrInvalidRepeatedRequest) {
				t.Fatalf("error = %v, want ErrInvalidRepeatedRequest", err)
			}
		})
	}
}

// TestGateRecordIsACopy: a record holds its own limits and targets; changing
// what it was built from or what it returned changes nothing stored.
func TestGateRecordIsACopy(t *testing.T) {
	record := reducedGateRecord(t)
	before, err := encodeScenarioGateRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	limits := record.Limits()
	limits.MaxCallsPerRun[0].Max = 99
	frequency := record.Gate().FrequencyChecks()
	frequency[2].Targets[0].Actual = 99
	after, err := encodeScenarioGateRecord(record)
	if err != nil || after != before {
		t.Fatalf("a returned value changed the record:\n%s\n%s", after, before)
	}
}

// TestCompletionWithoutAGateRecordIsRefused: no committed completion lacks the
// evidence its verdict consumed.
func TestCompletionWithoutAGateRecordIsRefused(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	seedProject(t, store, "proj-1")
	if err := store.CreateScenarioExecution(ctx,
		newRunningExecution(t, "e-1", "support", scenarioScope("proj-1"), 1, "", scenarioEpoch)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteScenarioExecution(ctx, "e-1", associations("e1", 1),
		ScenarioGateRecord{}, scenarioEpoch.Add(time.Minute)); !errors.Is(err, ErrScenarioExecutionState) {
		t.Fatalf("error = %v, want ErrScenarioExecutionState", err)
	}
	loaded, err := store.ScenarioExecution(ctx, "e-1")
	if err != nil || loaded.Status() != ScenarioExecutionRunning {
		t.Fatalf("after a refused completion: %v, %v", loaded.Status(), err)
	}
}

// TestStoredGateRecordIsHeldToItsExecution: a stored record that is valid on
// its own but disagrees with the row beside it — another verdict, another N —
// is refused when the execution loads.
func TestStoredGateRecordIsHeldToItsExecution(t *testing.T) {
	for name, damage := range map[string]string{
		"verdict column disagrees": `UPDATE ` + tableScenarioExecutions + ` SET verdict = 'fail'`,
		"recorded for another N":   `UPDATE ` + tableScenarioExecutions + ` SET runs = 3`,
		"empty":                    `UPDATE ` + tableScenarioExecutions + ` SET ` + columnGateSnapshot + ` = ''`,
		"on a running execution": `UPDATE ` + tableScenarioExecutions + ` SET status = 'running', finished_at = NULL,
		   completion_sequence = NULL, verdict = NULL`,
	} {
		t.Run(name, func(t *testing.T) {
			store, path := testStore(t)
			ctx := t.Context()
			seedProject(t, store, "proj-1")
			if err := store.CreateScenarioExecution(ctx,
				newRunningExecution(t, "e-1", "support", scenarioScope("proj-1"), 2, "", scenarioEpoch)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CompleteScenarioExecution(ctx, "e-1", associations("e1", 2),
				recordedGate(2, GateVerdictPass), scenarioEpoch.Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			store.Close()
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := raw.Exec(damage); err != nil {
				t.Fatal(err)
			}
			raw.Close()
			reopened, err := OpenSQLiteStore(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if _, err := reopened.ScenarioExecution(ctx, "e-1"); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("load error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestGateRecordSizeIsBounded: the largest record a completion can write —
// sixteen targets of 255 bytes, every optional check, a deferral naming a
// maximal run identifier — stays within a few KiB per execution.
func TestGateRecordSizeIsBounded(t *testing.T) {
	small, err := encodeScenarioGateRecord(recordedGate(MaxRepetitions, GateVerdictPass))
	if err != nil {
		t.Fatal(err)
	}
	limits := RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: MaxRepetitions, AddedReferencePresenceMaximum: MaxRepetitions - 1,
		MaxRepeatedAddedBehaviors: math.MaxUint64, MaxBlockDecisionsPerRun: math.MaxUint64,
		MaxCriticalRiskObservationsPerRun: math.MaxUint64,
		MinCandidateFrequency:             NewOptionalGateLimit(MaxRepetitions),
		MaxLostBehaviors:                  NewOptionalGateLimit(math.MaxUint64),
		MaxLLMCallsPerRun:                 NewOptionalGateLimit(math.MaxUint64),
	}
	var targets []TargetCallCheck
	for i := range MaxCallsPerRunTargets {
		name := strings.Repeat(string(rune('a'+i)), maxCallTargetLength)
		limits.MaxCallsPerRun = append(limits.MaxCallsPerRun, TargetCallLimit{Target: name, Max: math.MaxUint64 - 1})
		targets = append(targets, TargetCallCheck{Target: name, Actual: math.MaxUint64, Max: math.MaxUint64 - 1,
			Outcome: TargetCallFail})
	}
	n := uint64(MaxRepetitions)
	frequency := []FrequencyGateCheck{
		{Name: CheckMinCandidateFrequency, State: GateCheckEvaluated, Rule: RuleAtLeast, Actual: n, Bound: n, Passed: true},
		{Name: CheckMaxLostBehaviors, State: GateCheckEvaluated, Rule: RuleAtMost, Actual: math.MaxUint64,
			Bound: math.MaxUint64, Passed: true},
		{Name: CheckMaxCallsPerRun, State: GateCheckEvaluated, Rule: RuleAtMost, Targets: targets},
		{Name: CheckMaxLLMCallsPerRun, State: GateCheckDeferred, MissingEvidence: "run " +
			preview(strings.Repeat("r", maxIdentifierLength)) +
			" has no model-layer observation; model calls cannot be counted (64 of 64 candidate runs have none)"},
	}
	checks := []RepeatedGateCheck{
		equalsCheck(CheckReferenceRepetitionsCompleted, n, n),
		equalsCheck(CheckCandidateRepetitionsCompleted, n, n),
		atMostCheck(CheckRepetitionsFailingMinimum, 0, 0, ""),
		atMostCheck(CheckRepeatedlyAddedBehaviors, math.MaxUint64, math.MaxUint64, ""),
		atMostCheck(CheckWorstCandidateBlockDecisions, math.MaxUint64, math.MaxUint64, AdvisoryFreshScope),
		atMostCheck(CheckWorstCandidateCriticalRiskCount, math.MaxUint64, math.MaxUint64, AdvisoryFreshScope),
	}
	record, err := NewScenarioGateRecord(MaxRepetitions, limits, checks, frequency, GateVerdictFail)
	if err != nil {
		t.Fatal(err)
	}
	large, err := encodeScenarioGateRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("gate_snapshot: %d bytes with no optional limit, %d bytes at every bound", len(small), len(large))
	if len(large) > 16<<10 {
		t.Fatalf("the largest record is %d bytes", len(large))
	}
}
