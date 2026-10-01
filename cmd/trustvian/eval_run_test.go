package main

// trustvian eval run, without a workload: the repetition runner is replaced by
// a recorder, and the control plane by the fake API every CLI test uses. What is
// asserted is what the runner owns — validation, isolation, sequencing, abort,
// and passing the server's verdict through unchanged.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const runScenarioYAML = `version: v1
name: support
runs: 3
reference:
  command: [sh, ref.sh]
  env: {MODE: reference}
candidate:
  command: [sh, can.sh]
  env: {MODE: candidate}
gate:
  added_candidate_presence_minimum: 2
  added_reference_presence_maximum: 0
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
`

func writeScenario(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// repeatedReply is a server response with the given verdict.
func repeatedReply(verdict string) string {
	return fmt.Sprintf(`{"version":"1","runs":3,
	  "gate_limits":{"added_candidate_presence_minimum":"2","added_reference_presence_maximum":"0",
	    "max_repeated_added_behaviors":"0","max_block_decisions_per_run":"0",
	    "max_critical_risk_observations_per_run":"0"},
	  "repetitions":[],
	  "behaviors":[{"fingerprint_id":"fp-x","behavior":{"actor_type":"ai_agent","operation_category":"tool","operation_name":"export_customer"},
	    "reference_runs_present":"0","candidate_runs_present":"3","classification":"added"}],
	  "gate":{"checks":[
	    {"name":"reference_repetitions_completed","actual":"3","rule":"equals","bound":"3","passed":true},
	    {"name":"candidate_repetitions_completed","actual":"3","rule":"equals","bound":"3","passed":true},
	    {"name":"repetitions_failing_minimum_evidence","actual":"0","rule":"at_most","bound":"0","passed":true},
	    {"name":"repeatedly_added_behaviors","actual":"1","rule":"at_most","bound":"0","passed":false},
	    {"name":"worst_candidate_block_decisions","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":"fresh_scope"},
	    {"name":"worst_candidate_critical_risk_observations","actual":"0","rule":"at_most","bound":"0","passed":true,"advisory":"fresh_scope"}],
	    "verdict":%q},
	  "producer":{"control_plane_version":"cp-test"}}`, verdict)
}

type recordedRepetition struct {
	config devConfig
}

// recorder stands in for `trustvian dev`: it records every repetition and
// returns a scripted exit status.
type recorder struct {
	calls  []recordedRepetition
	failOn int // 1-based call number to fail; 0 never
	// uncompletedOn is the 1-based call whose workload succeeds but whose
	// run is not completed; 0 never.
	uncompletedOn int
}

func (r *recorder) run(s streams, config devConfig) devResult {
	r.calls = append(r.calls, recordedRepetition{config: config})
	fmt.Fprintf(s.out, "workload output for %s\n", config.runID)
	switch len(r.calls) {
	case r.failOn:
		return devResult{code: 7}
	case r.uncompletedOn:
		return devResult{code: 0}
	}
	return devResult{code: 0, runCompleted: true}
}

func runScenario(t *testing.T, rec *recorder, apiURL string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	runner := scenarioRunner{
		run:            rec.run,
		executionID:    func(string) string { return "scn-test" },
		cliVersion:     func() string { return "cli-test" },
		workloadStdout: os.Stderr,
	}
	code := runner.main(streams{out: &out, err: &errOut},
		append([]string{"--api-url", apiURL}, args...), testTimeout)
	return code, out.String(), errOut.String()
}

func TestEvalRunIsolatesEveryRepetitionAndRunsThemInOrder(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, repeatedReply("pass"))
	rec := &recorder{}
	code, _, _ := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if len(rec.calls) != 6 {
		t.Fatalf("%d repetitions, want 6", len(rec.calls))
	}
	wantOrder := []string{"reference-1", "reference-2", "reference-3",
		"candidate-1", "candidate-2", "candidate-3"}
	runIDs, profiles := map[string]bool{}, map[string]bool{}
	for i, call := range rec.calls {
		c := call.config
		if c.runID != "scn-test-"+wantOrder[i] {
			t.Errorf("repetition %d run id %q, want scn-test-%s", i, c.runID, wantOrder[i])
		}
		if c.behavioralProfile == "" || c.behavioralProfile == c.candidate {
			t.Errorf("repetition %d profile %q: every repetition needs its own scope", i, c.behavioralProfile)
		}
		runIDs[c.runID], profiles[c.behavioralProfile] = true, true
		wantMode, wantScript := "reference", "ref.sh"
		if i >= 3 {
			wantMode, wantScript = "candidate", "can.sh"
		}
		if c.env["MODE"] != wantMode || c.command[1] != wantScript {
			t.Errorf("repetition %d ran %v with MODE=%s", i, c.command, c.env["MODE"])
		}
		if c.workloadStdout != os.Stderr {
			t.Errorf("repetition %d workload stdout is %v, want this process's stderr", i, c.workloadStdout)
		}
		if c.apiURL != api.url() && c.apiURL != api.url()+"/" {
			t.Errorf("repetition %d attached to %q, want the one control plane", i, c.apiURL)
		}
	}
	if len(runIDs) != 6 || len(profiles) != 6 {
		t.Errorf("%d distinct run ids, %d distinct profiles; want 6 and 6", len(runIDs), len(profiles))
	}

	var body compareRepeatedBody
	if err := json.Unmarshal(api.only().body, &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if strings.Join(body.ReferenceRunIDs, ",") != "scn-test-reference-1,scn-test-reference-2,scn-test-reference-3" ||
		len(body.CandidateRunIDs) != 3 || body.GateLimits.AddedCandidatePresenceMinimum != "2" ||
		body.GateLimits.AddedReferencePresenceMaximum != "0" {
		t.Errorf("request = %+v", body)
	}
}

func TestEvalRunStopsAtAFailedRepetitionWithExitThreeAndNoVerdict(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, repeatedReply("pass"))
	rec := &recorder{failOn: 2}
	code, out, errOut := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOperational {
		t.Fatalf("exit %d, want 3: a crashed repetition is never a gate result", code)
	}
	if len(rec.calls) != 2 {
		t.Errorf("%d repetitions ran, want 2: the remaining ones must not run", len(rec.calls))
	}
	if n := len(api.captured()); n != 0 {
		t.Errorf("%d requests reached the control plane; no verdict may be asked for", n)
	}
	if strings.Contains(out, "Gate:") {
		t.Errorf("a verdict was printed:\n%s", out)
	}
	if !strings.Contains(errOut, "reference repetition 2 of 3") {
		t.Errorf("stderr does not name the repetition:\n%s", errOut)
	}
}

// A workload that exited 0 whose run the control plane did not complete is a
// failed repetition: exit 3, no further repetition, no verdict requested.
func TestEvalRunStopsWhenARunCannotBeCompleted(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, repeatedReply("fail"))
	rec := &recorder{uncompletedOn: 1}
	code, out, errOut := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOperational {
		t.Fatalf("exit %d, want 3: an uncompleted run is not a behavioral FAIL", code)
	}
	if len(rec.calls) != 1 || len(api.captured()) != 0 {
		t.Errorf("%d repetitions ran and %d requests were made; want 1 and 0", len(rec.calls), len(api.captured()))
	}
	if strings.Contains(out, "Gate:") || !strings.Contains(errOut, "could not be completed") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", out, errOut)
	}
}

func TestEvalRunRefusesAnInvalidScenarioBeforeRunningAnything(t *testing.T) {
	for name, scenario := range map[string]string{
		"missing k":       strings.Replace(runScenarioYAML, "  added_candidate_presence_minimum: 2\n", "", 1),
		"k above N":       strings.Replace(runScenarioYAML, "added_candidate_presence_minimum: 2", "added_candidate_presence_minimum: 4", 1),
		"j = k":           strings.Replace(runScenarioYAML, "added_reference_presence_maximum: 0", "added_reference_presence_maximum: 2", 1),
		"runs 65":         strings.Replace(runScenarioYAML, "runs: 3", "runs: 65", 1),
		"fractional runs": strings.Replace(runScenarioYAML, "runs: 3", "runs: 2.9", 1),
		"fractional k": strings.Replace(runScenarioYAML, "added_candidate_presence_minimum: 2",
			"added_candidate_presence_minimum: 1.9", 1),
		"negative fractional limit": strings.Replace(runScenarioYAML, "max_block_decisions_per_run: 0",
			"max_block_decisions_per_run: -0.5", 1),
		"limit past 64 bits": strings.Replace(runScenarioYAML, "max_repeated_added_behaviors: 0",
			"max_repeated_added_behaviors: 18446744073709551616", 1),
		"merged fractional runs": strings.Replace(runScenarioYAML, "runs: 3", "<<: {runs: 2.9}", 1),
		"merged negative fractional limit": strings.Replace(runScenarioYAML, "  max_block_decisions_per_run: 0",
			"  <<: {max_block_decisions_per_run: -0.5}", 1),
		"aliased limit key past 64 bits": strings.Replace(strings.Replace(runScenarioYAML,
			"  max_repeated_added_behaviors: 0", "  *key : 18446744073709551616", 1),
			"{MODE: reference}", "{MODE: &key max_repeated_added_behaviors}", 1),
		"no runs": strings.Replace(runScenarioYAML, "runs: 3\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t)
			rec := &recorder{}
			code, _, _ := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, scenario))
			if code != exitUsage || len(rec.calls) != 0 || len(api.captured()) != 0 {
				t.Errorf("exit %d after %d repetitions and %d requests; want 2, 0, 0",
					code, len(rec.calls), len(api.captured()))
			}
		})
	}
}

// The verdict is the server's: PASS is 0 and FAIL is 1, whatever the runner
// saw; anything that is not a verdict is 3.
func TestEvalRunPassesTheServersVerdictThrough(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   int
	}{
		{200, repeatedReply("pass"), exitOK},
		{200, repeatedReply("fail"), exitGateFail},
		{200, repeatedReply("maybe"), exitOperational},
		{409, `{"version":"1","error":{"code":"incomplete_evidence","message":"saturated"}}`, exitOperational},
		{400, `{"version":"1","error":{"code":"invalid_request","message":"shared profile"}}`, exitOperational},
	} {
		api := newFakeAPI(t)
		api.reply(tc.status, tc.body)
		code, _, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
		if code != tc.want {
			t.Errorf("status %d verdict body → exit %d, want %d", tc.status, code, tc.want)
		}
	}
}

func TestEvalRunJSONIsTheResultDocumentAndNothingElse(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, repeatedReply("fail"))
	code, out, errOut := runScenario(t, &recorder{}, api.url(),
		"--scenario", writeScenario(t, runScenarioYAML), "--json")
	if code != exitGateFail {
		t.Fatalf("exit %d, want 1", code)
	}
	var doc struct {
		Scenario    scenarioIdentity `json:"scenario"`
		ExecutionID string           `json:"execution_id"`
		Producers   producers        `json:"producers"`
		Comparison  struct {
			Gate struct {
				Verdict string `json:"verdict"`
			} `json:"gate"`
		} `json:"comparison"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc.Scenario.Name != "support" || doc.Scenario.Runs != 3 || doc.ExecutionID != "scn-test" ||
		doc.Producers.CLIVersion != "cli-test" || doc.Producers.ServerVersion != "cp-test" ||
		doc.Comparison.Gate.Verdict != "fail" {
		t.Errorf("document = %+v", doc)
	}
	if !strings.Contains(errOut, "workload output for scn-test-candidate-3") {
		t.Errorf("repetition output did not go to stderr:\n%s", errOut)
	}
}

func TestEvalRunRendersTheAdvisoryMarker(t *testing.T) {
	api := newFakeAPI(t)
	api.reply(200, repeatedReply("fail"))
	_, out, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	for _, want := range []string{
		"tool/export_customer", "0/3", "3/3", "+ added",
		"FAIL repeatedly added behaviors: 1 (<= 0)",
		"PASS worst candidate block decisions: 0 (<= 0)   advisory: fresh scope",
		"Gate (k = 2, j = 0)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
