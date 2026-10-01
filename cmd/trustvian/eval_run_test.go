package main

// trustvian eval run, without a workload: the repetition runner is replaced by
// a recorder, and the control plane by the fake API every CLI test uses. What is
// asserted is what the runner owns — validation, isolation, sequencing, abort,
// the execution lifecycle it reports, which side it runs with --reference, and
// passing the server's verdict through unchanged.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
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
	// crashRunsWith fails every repetition whose run id has this prefix.
	crashRunsWith string
	// onCall runs before each repetition returns, with its 1-based number;
	// returning true makes the repetition wait for its deadline and then
	// exit 0, as a workload trapping SIGTERM would.
	onCall func(n int, config devConfig) (hang bool)
}

func (r *recorder) run(s streams, config devConfig) devResult {
	r.calls = append(r.calls, recordedRepetition{config: config})
	fmt.Fprintf(s.out, "workload output for %s\n", config.runID)
	if r.onCall != nil && r.onCall(len(r.calls), config) {
		if config.deadline == nil {
			panic("a hanging repetition was given no deadline")
		}
		<-config.deadline.Done()
		return devResult{code: 0}
	}
	if r.crashRunsWith != "" && strings.HasPrefix(config.runID, r.crashRunsWith) {
		return devResult{code: 9}
	}
	switch len(r.calls) {
	case r.failOn:
		return devResult{code: 7}
	case r.uncompletedOn:
		return devResult{code: 0}
	}
	return devResult{code: 0, runCompleted: true}
}

// scenarioAPI serves the execution lifecycle: begin, complete and fail. The
// complete reply carries repeatedReply(verdict) as its comparison; reference
// names the execution a recorded begin resolves to.
type scenarioAPI struct {
	*fakeAPI
	beginStatus, completeStatus int
	beginError, completeError   string
	verdict                     string
	reference                   string
	// Per-execution overrides, keyed by execution id, for suites.
	verdicts          map[string]string
	completeStatusFor map[string]int
	beginErrors       map[string]string
}

func newScenarioAPI(t *testing.T, verdict string) *scenarioAPI {
	t.Helper()
	api := &scenarioAPI{fakeAPI: newFakeAPI(t), beginStatus: 201, completeStatus: 200,
		verdict: verdict, reference: "scn-prior"}
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/scenario-executions":
			var body struct {
				ID        string          `json:"id"`
				Reference json.RawMessage `json:"reference"`
			}
			_ = json.Unmarshal(api.lastBody(), &body)
			if e, ok := api.beginErrors[body.ID]; ok {
				w.WriteHeader(404)
				fmt.Fprint(w, e)
				return
			}
			if api.beginStatus != 201 {
				w.WriteHeader(api.beginStatus)
				fmt.Fprint(w, api.beginError)
				return
			}
			reference := ""
			if len(body.Reference) > 0 {
				reference = fmt.Sprintf(`,"reference_execution":{"id":%q,"status":"completed"}`, api.reference)
			}
			w.WriteHeader(201)
			fmt.Fprintf(w, `{"version":"1","execution":{"id":%q,"status":"running"}%s}`, body.ID, reference)
		case strings.HasSuffix(r.URL.Path, "/complete"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/scenario-executions/"), "/complete")
			if status, ok := api.completeStatusFor[id]; ok && status != 200 {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"version":"1","error":{"code":"incomplete_evidence","message":"saturated"}}`)
				return
			}
			if api.completeStatus != 200 {
				w.WriteHeader(api.completeStatus)
				fmt.Fprint(w, api.completeError)
				return
			}
			verdict := api.verdict
			if v, ok := api.verdicts[id]; ok {
				verdict = v
			}
			w.WriteHeader(200)
			fmt.Fprintf(w, `{"version":"1","execution":{"id":%q,"status":"completed"},"comparison":%s}`,
				id, repeatedReply(verdict))
		case strings.HasSuffix(r.URL.Path, "/fail"):
			w.WriteHeader(200)
			fmt.Fprint(w, `{"version":"1","execution":{"id":"scn-test","status":"failed"}}`)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"version":"1","error":{"code":"not_found","message":"no route"}}`)
		}
	})
	return api
}

func (a *scenarioAPI) lastBody() []byte {
	got := a.captured()
	return got[len(got)-1].body
}

// paths lists the requests made, in order.
func (a *scenarioAPI) paths() []string {
	var out []string
	for _, r := range a.captured() {
		out = append(out, r.method+" "+r.escapedPath)
	}
	return out
}

func (a *scenarioAPI) request(t *testing.T, suffix string) []byte {
	t.Helper()
	for _, r := range a.captured() {
		if strings.HasSuffix(r.escapedPath, suffix) {
			return r.body
		}
	}
	t.Fatalf("no request to %s among %v", suffix, a.paths())
	return nil
}

var testScope = executionScope{project: "proj-x", agent: "agent-x", environment: "staging"}

func runScenario(t *testing.T, rec *recorder, apiURL string, args ...string) (int, string, string) {
	t.Helper()
	return runScenarioScoped(t, rec, func(devConfig) (executionScope, error) { return testScope, nil },
		apiURL, args...)
}

func runScenarioScoped(t *testing.T, rec *recorder, scope scopeFunc, apiURL string,
	args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	runner := scenarioRunner{
		run:            rec.run,
		scope:          scope,
		executionID:    func(string) string { return "scn-test" },
		cliVersion:     func() string { return "cli-test" },
		workloadStdout: os.Stderr,
	}
	code := runner.main(streams{out: &out, err: &errOut},
		append([]string{"--api-url", apiURL}, args...), testTimeout)
	return code, out.String(), errOut.String()
}

const (
	beginPath    = "POST /v1/scenario-executions"
	completePath = "POST /v1/scenario-executions/scn-test/complete"
	failPath     = "POST /v1/scenario-executions/scn-test/fail"
)

func TestEvalRunIsolatesEveryRepetitionAndRunsThemInOrder(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	rec := &recorder{}
	code, _, _ := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOK {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := api.paths(); !slices.Equal(got, []string{beginPath, completePath}) {
		t.Fatalf("requests %v, want begin then complete", got)
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

	var begin beginExecutionBody
	if err := json.Unmarshal(api.request(t, "/v1/scenario-executions"), &begin); err != nil {
		t.Fatal(err)
	}
	if begin.ID != "scn-test" || begin.ScenarioName != "support" || begin.Runs != 3 ||
		begin.ProjectID != "proj-x" || begin.AgentID != "agent-x" || begin.Environment != "staging" ||
		begin.Reference != nil {
		t.Errorf("begin = %+v", begin)
	}
	var body completeExecutionBody
	if err := json.Unmarshal(api.request(t, "/complete"), &body); err != nil {
		t.Fatalf("request body: %v", err)
	}
	if strings.Join(body.ReferenceRunIDs, ",") != "scn-test-reference-1,scn-test-reference-2,scn-test-reference-3" ||
		len(body.CandidateRunIDs) != 3 || body.GateLimits.AddedCandidatePresenceMinimum != "2" ||
		body.GateLimits.AddedReferencePresenceMaximum != "0" {
		t.Errorf("complete = %+v", body)
	}
}

// --reference runs the candidate side only — exactly N workloads, none of
// them reference workloads — and the control plane supplies the reference.
func TestEvalRunWithAReferenceRunsOnlyTheCandidateSide(t *testing.T) {
	for _, tc := range []struct {
		flag, mode, id string
	}{
		{"last", "last", ""},
		{"scn-support-20261001T120000-abcd1234", "execution", "scn-support-20261001T120000-abcd1234"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			api := newScenarioAPI(t, "fail")
			rec := &recorder{}
			code, out, errOut := runScenario(t, rec, api.url(),
				"--scenario", writeScenario(t, runScenarioYAML), "--reference", tc.flag, "--json")
			if code != exitGateFail {
				t.Fatalf("exit %d, want 1\n%s", code, errOut)
			}
			if len(rec.calls) != 3 {
				t.Fatalf("%d workloads ran, want exactly 3", len(rec.calls))
			}
			for i, call := range rec.calls {
				if call.config.env["MODE"] != "candidate" || call.config.runID != fmt.Sprintf("scn-test-candidate-%d", i+1) {
					t.Errorf("workload %d is %s (%s); with --reference only candidates run",
						i, call.config.runID, call.config.env["MODE"])
				}
			}
			var begin beginExecutionBody
			if err := json.Unmarshal(api.request(t, "/v1/scenario-executions"), &begin); err != nil {
				t.Fatal(err)
			}
			if begin.Reference == nil || begin.Reference.Mode != tc.mode || begin.Reference.ExecutionID != tc.id {
				t.Errorf("begin reference = %+v, want mode %s id %q", begin.Reference, tc.mode, tc.id)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(api.request(t, "/complete"), &body); err != nil {
				t.Fatal(err)
			}
			if _, sent := body["reference_run_ids"]; sent {
				t.Errorf("reference runs were sent with --reference: %s", body["reference_run_ids"])
			}
			var doc struct {
				ExecutionID string               `json:"execution_id"`
				Reference   *referenceProvenance `json:"reference"`
			}
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
			}
			if doc.ExecutionID != "scn-test" || doc.Reference == nil || doc.Reference.Mode != tc.mode ||
				doc.Reference.ExecutionID != "scn-prior" {
				t.Errorf("document identity %q, reference %+v", doc.ExecutionID, doc.Reference)
			}
		})
	}
}

// A reference the control plane refuses — missing, unfinished, another N,
// incomplete — is exit 3 before any workload runs, and nothing is begun.
func TestEvalRunRefusedReferenceRunsNothing(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{404, `{"version":"1","error":{"code":"not_found","message":"no completed execution of scenario support"}}`},
		{409, `{"version":"1","error":{"code":"conflict","message":"execution e1 has runs 2 and this scenario has runs 3"}}`},
		{409, `{"version":"1","error":{"code":"incomplete_evidence","message":"saturated"}}`},
	} {
		api := newScenarioAPI(t, "pass")
		api.beginStatus, api.beginError = tc.status, tc.body
		rec := &recorder{}
		code, _, errOut := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML),
			"--reference", "last")
		if code != exitOperational || len(rec.calls) != 0 {
			t.Errorf("HTTP %d: exit %d after %d workloads; want 3 and none", tc.status, code, len(rec.calls))
		}
		if got := api.paths(); !slices.Equal(got, []string{beginPath}) {
			t.Errorf("HTTP %d: requests %v, want the begin alone", tc.status, got)
		}
		if !strings.Contains(errOut, tc.body[strings.Index(tc.body, `"code":"`)+8:strings.Index(tc.body, `","message`)]) {
			t.Errorf("HTTP %d: the server's named error is not reported:\n%s", tc.status, errOut)
		}
	}
}

func TestEvalRunReferenceFlagIsValidatedBeforeAnythingRuns(t *testing.T) {
	for name, args := range map[string][]string{
		"empty reference":                       {"--reference", ""},
		"blank reference":                       {"--reference", "  "},
		"reference without a reference command": {"--reference", "last"},
	} {
		t.Run(name, func(t *testing.T) {
			api := newScenarioAPI(t, "pass")
			rec := &recorder{}
			scenario := runScenarioYAML
			if name == "reference without a reference command" {
				scenario = strings.Replace(scenario, "  command: [sh, ref.sh]\n", "", 1)
			}
			code, _, _ := runScenario(t, rec, api.url(),
				append([]string{"--scenario", writeScenario(t, scenario)}, args...)...)
			if code != exitUsage || len(rec.calls) != 0 || len(api.captured()) != 0 {
				t.Errorf("exit %d after %d workloads and %d requests; want 2, 0, 0",
					code, len(rec.calls), len(api.captured()))
			}
		})
	}
}

// An identity the runner cannot derive is the usage error dev would report,
// before anything is begun.
func TestEvalRunUnderivableScopeIsAUsageError(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	rec := &recorder{}
	code, _, _ := runScenarioScoped(t, rec, func(devConfig) (executionScope, error) {
		return executionScope{}, usageErrorf("no agent identity")
	}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitUsage || len(rec.calls) != 0 || len(api.captured()) != 0 {
		t.Errorf("exit %d after %d workloads and %d requests; want 2, 0, 0",
			code, len(rec.calls), len(api.captured()))
	}
}

func TestEvalRunStopsAtAFailedRepetitionWithExitThreeAndNoVerdict(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	rec := &recorder{failOn: 2}
	code, out, errOut := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOperational {
		t.Fatalf("exit %d, want 3: a crashed repetition is never a gate result", code)
	}
	if len(rec.calls) != 2 {
		t.Errorf("%d repetitions ran, want 2: the remaining ones must not run", len(rec.calls))
	}
	if got := api.paths(); !slices.Equal(got, []string{beginPath, failPath}) {
		t.Errorf("requests %v; want begin, then the execution recorded failed, and no completion", got)
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
	api := newScenarioAPI(t, "fail")
	rec := &recorder{uncompletedOn: 1}
	code, out, errOut := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if code != exitOperational {
		t.Fatalf("exit %d, want 3: an uncompleted run is not a behavioral FAIL", code)
	}
	if len(rec.calls) != 1 || !slices.Equal(api.paths(), []string{beginPath, failPath}) {
		t.Errorf("%d repetitions ran and requests were %v; want 1, and begin then fail", len(rec.calls), api.paths())
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
		"no runs":         strings.Replace(runScenarioYAML, "runs: 3\n", "", 1),
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
	} {
		t.Run(name, func(t *testing.T) {
			api := newScenarioAPI(t, "pass")
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
// saw; anything that is not a verdict is 3, and a refused completion records
// the execution failed.
func TestEvalRunPassesTheServersVerdictThrough(t *testing.T) {
	for _, tc := range []struct {
		status   int
		body     string
		verdict  string
		want     int
		wantFail bool
	}{
		{200, "", "pass", exitOK, false},
		{200, "", "fail", exitGateFail, false},
		{200, "", "maybe", exitOperational, false},
		{409, `{"version":"1","error":{"code":"incomplete_evidence","message":"saturated"}}`, "", exitOperational, true},
		{400, `{"version":"1","error":{"code":"invalid_request","message":"shared profile"}}`, "", exitOperational, true},
	} {
		api := newScenarioAPI(t, tc.verdict)
		api.completeStatus, api.completeError = tc.status, tc.body
		code, _, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
		if code != tc.want {
			t.Errorf("status %d verdict %q → exit %d, want %d", tc.status, tc.verdict, code, tc.want)
		}
		if failed := slices.Contains(api.paths(), failPath); failed != tc.wantFail {
			t.Errorf("status %d: execution recorded failed = %v, want %v", tc.status, failed, tc.wantFail)
		}
	}
}

func TestEvalRunJSONIsTheResultDocumentAndNothingElse(t *testing.T) {
	api := newScenarioAPI(t, "fail")
	code, out, errOut := runScenario(t, &recorder{}, api.url(),
		"--scenario", writeScenario(t, runScenarioYAML), "--json")
	if code != exitGateFail {
		t.Fatalf("exit %d, want 1", code)
	}
	var doc struct {
		Scenario    scenarioIdentity           `json:"scenario"`
		ExecutionID string                     `json:"execution_id"`
		Reference   json.RawMessage            `json:"reference"`
		Producers   producers                  `json:"producers"`
		Comparison  map[string]json.RawMessage `json:"comparison"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if doc.Scenario.Name != "support" || doc.Scenario.Runs != 3 || doc.ExecutionID != "scn-test" ||
		doc.Producers.CLIVersion != "cli-test" || doc.Producers.ServerVersion != "cp-test" ||
		doc.Reference != nil {
		t.Errorf("document = %+v", doc)
	}
	// The comparison is the server's, member for member.
	var server map[string]json.RawMessage
	if err := json.Unmarshal([]byte(repeatedReply("fail")), &server); err != nil {
		t.Fatal(err)
	}
	for key, value := range server {
		var a, b any
		_ = json.Unmarshal(value, &a)
		_ = json.Unmarshal(doc.Comparison[key], &b)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("comparison %s = %s, want the server's %s", key, doc.Comparison[key], value)
		}
	}
	if len(doc.Comparison) != len(server) {
		t.Errorf("comparison has %d members, the server sent %d", len(doc.Comparison), len(server))
	}
	if !strings.Contains(errOut, "workload output for scn-test-candidate-3") {
		t.Errorf("repetition output did not go to stderr:\n%s", errOut)
	}
}

func TestEvalRunRendersTheAdvisoryMarker(t *testing.T) {
	api := newScenarioAPI(t, "fail")
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

func TestEvalRunRendersTheReusedReference(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	_, out, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML),
		"--reference", "last")
	if !strings.Contains(out, "Reference side reused from execution scn-prior (last)") {
		t.Errorf("output does not name the reused reference:\n%s", out)
	}
}

// A begin that answers without the reference it was asked to resolve is not
// trusted: exit 3, no workload, and the execution it created recorded failed.
func TestEvalRunFailsAnExecutionWhoseReferenceWasNotNamed(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	api.serve(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/scenario-executions" {
			w.WriteHeader(201)
			fmt.Fprint(w, `{"version":"1","execution":{"id":"scn-test","status":"running"}}`)
			return
		}
		w.WriteHeader(200)
		fmt.Fprint(w, `{"version":"1","execution":{"id":"scn-test","status":"failed"}}`)
	})
	rec := &recorder{}
	code, _, _ := runScenario(t, rec, api.url(), "--scenario", writeScenario(t, runScenarioYAML),
		"--reference", "last")
	if code != exitOperational || len(rec.calls) != 0 {
		t.Fatalf("exit %d after %d workloads; want 3 and none", code, len(rec.calls))
	}
	if got := api.paths(); !slices.Equal(got, []string{beginPath, failPath}) {
		t.Errorf("requests %v; want begin, then the execution recorded failed", got)
	}
}
