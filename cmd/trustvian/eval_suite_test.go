package main

// trustvian eval run --suite, without workloads: discovery, preflight, the
// schedule, the report and its bounds. The repetition runner is the recorder
// and the control plane the scenario fake, as in eval_run_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// suiteScenario is runScenarioYAML under another name and N.
func suiteScenario(name string, runs int) string {
	s := strings.Replace(runScenarioYAML, "name: support", "name: "+name, 1)
	s = strings.Replace(s, "runs: 3", fmt.Sprintf("runs: %d", runs), 1)
	return strings.Replace(s, "added_candidate_presence_minimum: 2", "added_candidate_presence_minimum: 1", 1)
}

func writeSuite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

type suiteRun struct {
	code   int
	stdout string
	stderr string
	doc    suiteDocument
}

func runSuite(t *testing.T, rec *recorder, api *scenarioAPI, configure func(*scenarioRunner),
	args ...string) suiteRun {
	t.Helper()
	var out, errOut bytes.Buffer
	runner := scenarioRunner{
		run:            rec.run,
		scope:          func(devConfig) (executionScope, error) { return testScope, nil },
		executionID:    func(name string) string { return "scn-" + name },
		cliVersion:     func() string { return "cli-test" },
		workloadStdout: os.Stderr,
		suiteContext:   func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
	}
	if configure != nil {
		configure(&runner)
	}
	code := runner.main(streams{out: &out, err: &errOut},
		append([]string{"--api-url", api.url(), "--json"}, args...), testTimeout)
	run := suiteRun{code: code, stdout: out.String(), stderr: errOut.String()}
	if strings.TrimSpace(run.stdout) != "" {
		dec := json.NewDecoder(strings.NewReader(run.stdout))
		if err := dec.Decode(&run.doc); err != nil {
			t.Fatalf("stdout is not one suite document: %v\n%s", err, run.stdout)
		}
		if dec.More() {
			t.Fatalf("stdout holds more than one document:\n%s", run.stdout)
		}
	}
	return run
}

func (r suiteRun) outcomes() []string {
	var out []string
	for _, m := range r.doc.Members {
		out = append(out, m.File+":"+m.Outcome)
	}
	return out
}

// Discovery is the directory's immediate regular .yaml and .yml files, in
// name order, and nothing else.
func TestSuiteDiscoversScenarioFilesInNameOrder(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"b.yaml": suiteScenario("bravo", 1), "a.yml": suiteScenario("alpha", 1),
		"C.yaml": suiteScenario("charlie", 1), "notes.txt": "not a scenario", "README": "",
	})
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "z.yaml"), []byte(suiteScenario("zulu", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	api := newScenarioAPI(t, "pass")
	rec := &recorder{}
	run := runSuite(t, rec, api, nil, "--suite", dir, "--scenario-timeout", "1m")
	if run.code != exitOK {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	want := []string{"C.yaml:pass", "a.yml:pass", "b.yaml:pass"}
	if got := run.outcomes(); !slices.Equal(got, want) {
		t.Errorf("members %v, want %v: byte order of names, no recursion, no other files", got, want)
	}
	if len(rec.calls) != 6 {
		t.Errorf("%d repetitions, want 2 per scenario", len(rec.calls))
	}
}

// Every global problem is a usage error before any request or workload.
func TestSuitePreflightRefusesBeforeAnythingRuns(t *testing.T) {
	valid := map[string]string{"a.yaml": suiteScenario("alpha", 1), "b.yaml": suiteScenario("bravo", 1)}
	symlinked := func(t *testing.T) string {
		dir := writeSuite(t, valid)
		target := filepath.Join(t.TempDir(), "elsewhere.yaml")
		if err := os.WriteFile(target, []byte(suiteScenario("echo", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "c.yaml")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		return dir
	}
	many := func(t *testing.T, n int, ext string, body func(i int) string) string {
		dir := t.TempDir()
		for i := 0; i < n; i++ {
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("s%04d%s", i, ext)), []byte(body(i)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	for name, tc := range map[string]struct {
		dir  func(t *testing.T) string
		args []string
	}{
		"symlinked scenario": {symlinked, nil},
		"empty suite":        {func(t *testing.T) string { return t.TempDir() }, nil},
		"no scenario files":  {func(t *testing.T) string { return writeSuite(t, map[string]string{"x.txt": ""}) }, nil},
		"more than 64 scenarios": {func(t *testing.T) string {
			return many(t, maxSuiteScenarios+1, ".yaml", func(i int) string { return suiteScenario(fmt.Sprintf("s%d", i), 1) })
		}, nil},
		"more than the entry bound": {func(t *testing.T) string {
			return many(t, maxSuiteDirectoryEntries+1, ".txt", func(int) string { return "" })
		}, nil},
		"duplicate scenario names": {func(t *testing.T) string {
			return writeSuite(t, map[string]string{"a.yaml": suiteScenario("same", 1), "b.yaml": suiteScenario("same", 1)})
		}, nil},
		"one invalid scenario among valid ones": {func(t *testing.T) string {
			return writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1),
				"z.yaml": strings.Replace(suiteScenario("zulu", 1), "runs: 1", "runs: 65", 1)})
		}, nil},
		"not a directory": {func(t *testing.T) string {
			return filepath.Join(writeSuite(t, valid), "a.yaml")
		}, nil},
		"no timeout":             {func(t *testing.T) string { return writeSuite(t, valid) }, []string{"--omit-timeout"}},
		"timeout below 1s":       {func(t *testing.T) string { return writeSuite(t, valid) }, []string{"--scenario-timeout", "500ms"}},
		"timeout above 24h":      {func(t *testing.T) string { return writeSuite(t, valid) }, []string{"--scenario-timeout", "25h"}},
		"timeout not a duration": {func(t *testing.T) string { return writeSuite(t, valid) }, []string{"--scenario-timeout", "soon"}},
		"explicit reference id": {func(t *testing.T) string { return writeSuite(t, valid) },
			[]string{"--reference", "scn-prior"}},
		"suite and scenario": {func(t *testing.T) string { return writeSuite(t, valid) },
			[]string{"--scenario", "x.yaml"}},
	} {
		t.Run(name, func(t *testing.T) {
			args := []string{"--suite", tc.dir(t)}
			switch {
			case slices.Contains(tc.args, "--omit-timeout"):
			case slices.Contains(tc.args, "--scenario-timeout"):
				args = append(args, tc.args...)
			default:
				args = append(append(args, "--scenario-timeout", "1m"), tc.args...)
			}
			api := newScenarioAPI(t, "pass")
			rec := &recorder{}
			run := runSuite(t, rec, api, nil, args...)
			if run.code != exitUsage || len(rec.calls) != 0 || len(api.captured()) != 0 {
				t.Errorf("exit %d after %d workloads and %d requests; want 2, 0, 0\n%s",
					run.code, len(rec.calls), len(api.captured()), run.stderr)
			}
		})
	}
}

// The suite-only flags are refused in single-scenario mode.
func TestSuiteFlagsAreRefusedWithAScenario(t *testing.T) {
	for _, extra := range [][]string{{"--scenario-timeout", "1m"}, {"--fail-fast"}} {
		api := newScenarioAPI(t, "pass")
		rec := &recorder{}
		code, _, _ := runScenario(t, rec, api.url(),
			append([]string{"--scenario", writeScenario(t, runScenarioYAML)}, extra...)...)
		if code != exitUsage || len(rec.calls) != 0 {
			t.Errorf("%v with --scenario: exit %d after %d workloads, want 2 and none", extra, code, len(rec.calls))
		}
	}
}

// By default a gate FAIL and an operational error do not stop the suite; the
// suite exits with the most severe outcome, and every verdict is a member's.
func TestSuiteContinuesAfterFailuresAndExitsWithTheMostSevere(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"a.yaml": suiteScenario("alpha", 2), "b.yaml": suiteScenario("bravo", 2),
		"c.yaml": suiteScenario("charlie", 1), "d.yaml": suiteScenario("delta", 1),
	})
	api := newScenarioAPI(t, "pass")
	api.verdicts = map[string]string{"scn-charlie": "fail"}
	rec := &recorder{crashRunsWith: "scn-bravo-reference-2"}
	run := runSuite(t, rec, api, nil, "--suite", dir, "--scenario-timeout", "1m")

	want := []string{"a.yaml:pass", "b.yaml:error", "c.yaml:fail", "d.yaml:pass"}
	if got := run.outcomes(); !slices.Equal(got, want) {
		t.Fatalf("members %v, want %v", got, want)
	}
	if run.code != exitOperational || run.doc.ExitCode != exitOperational || !run.doc.Complete {
		t.Errorf("exit %d (document %d, complete %v), want 3", run.code, run.doc.ExitCode, run.doc.Complete)
	}
	b := run.doc.Members[1]
	if b.Error == nil || b.Error.Code != "repetition_failed" || *b.ExitCode != exitOperational ||
		b.Result != nil || b.ExecutionID != "scn-bravo" {
		t.Errorf("crashed member = %+v", b)
	}
	if !slices.Contains(api.paths(), "POST /v1/scenario-executions/scn-bravo/fail") {
		t.Errorf("the crashed member's execution was not recorded failed: %v", api.paths())
	}
	// Each verdict member embeds its own result document, untouched.
	for _, m := range []suiteMember{run.doc.Members[0], run.doc.Members[2]} {
		var result struct {
			ExecutionID string           `json:"execution_id"`
			Scenario    scenarioIdentity `json:"scenario"`
			Comparison  struct {
				Gate struct {
					Verdict string `json:"verdict"`
				} `json:"gate"`
			} `json:"comparison"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			t.Fatalf("%s result: %v", m.File, err)
		}
		if result.ExecutionID != m.ExecutionID || result.Scenario != m.Scenario ||
			result.Comparison.Gate.Verdict != m.Outcome {
			t.Errorf("%s: result %+v does not match member %+v", m.File, result, m)
		}
	}
	if s := run.doc.Summary; s != (suiteSummary{Passed: 2, Failed: 1, Errors: 1}) {
		t.Errorf("summary %+v", s)
	}
	// N is each scenario's own: 2+2, 2+2 until the crash, 1+1, 1+1.
	if len(rec.calls) != 4+2+2+2 {
		t.Errorf("%d repetitions ran, want 10", len(rec.calls))
	}
}

func TestSuiteFailFastSkipsTheRestExplicitly(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"a.yaml": suiteScenario("alpha", 1), "b.yaml": suiteScenario("bravo", 1),
		"c.yaml": suiteScenario("charlie", 1),
	})
	api := newScenarioAPI(t, "pass")
	api.verdicts = map[string]string{"scn-bravo": "fail"}
	rec := &recorder{}
	run := runSuite(t, rec, api, nil, "--suite", dir, "--scenario-timeout", "1m", "--fail-fast")
	if got := run.outcomes(); !slices.Equal(got, []string{"a.yaml:pass", "b.yaml:fail", "c.yaml:skipped"}) {
		t.Fatalf("members %v", got)
	}
	c := run.doc.Members[2]
	if c.SkippedReason != skippedFailFast || c.ExitCode != nil || c.Result != nil || c.ExecutionID != "" {
		t.Errorf("skipped member = %+v", c)
	}
	if run.code != exitGateFail || len(rec.calls) != 4 || run.doc.Summary.Skipped != 1 {
		t.Errorf("exit %d after %d repetitions, summary %+v; want 1, 4, one skipped",
			run.code, len(rec.calls), run.doc.Summary)
	}
}

func TestSuiteExitPrecedence(t *testing.T) {
	for _, tc := range []struct {
		codes []int
		want  int
	}{
		{[]int{0, 0}, 0}, {[]int{0, 1}, 1}, {[]int{1, 2}, 2}, {[]int{2, 3, 1}, 3}, {[]int{3, 0}, 3},
	} {
		got := 0
		for _, c := range tc.codes {
			got = moreSevere(got, c)
		}
		if got != tc.want {
			t.Errorf("%v → %d, want %d", tc.codes, got, tc.want)
		}
	}
}

// --reference last is resolved for each scenario, by its own name, scope and
// N, and runs only candidates. A missing reference is that member's
// operational error, before its workloads; the others run.
func TestSuiteResolvesLastIndependentlyPerScenario(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"a.yaml": suiteScenario("alpha", 2), "b.yaml": suiteScenario("bravo", 3),
		"c.yaml": suiteScenario("charlie", 1),
	})
	api := newScenarioAPI(t, "pass")
	api.beginErrors = map[string]string{
		"scn-bravo": `{"version":"1","error":{"code":"not_found","message":"no completed execution of scenario bravo"}}`,
	}
	rec := &recorder{}
	run := runSuite(t, rec, api, nil, "--suite", dir, "--scenario-timeout", "1m", "--reference", "last")
	if got := run.outcomes(); !slices.Equal(got, []string{"a.yaml:pass", "b.yaml:error", "c.yaml:pass"}) {
		t.Fatalf("members %v", got)
	}
	if b := run.doc.Members[1]; b.Error == nil || b.Error.Code != "not_found" {
		t.Errorf("missing reference member = %+v", b)
	}
	var begins []beginExecutionBody
	for _, r := range api.captured() {
		if r.escapedPath == "/v1/scenario-executions" {
			var body beginExecutionBody
			if err := json.Unmarshal(r.body, &body); err != nil {
				t.Fatal(err)
			}
			begins = append(begins, body)
		}
	}
	if len(begins) != 3 {
		t.Fatalf("%d begins, want one per scenario", len(begins))
	}
	for i, want := range []struct {
		name string
		runs int
	}{{"alpha", 2}, {"bravo", 3}, {"charlie", 1}} {
		b := begins[i]
		if b.ScenarioName != want.name || b.Runs != want.runs || b.Reference == nil ||
			b.Reference.Mode != "last" || b.ProjectID != testScope.project {
			t.Errorf("begin %d = %+v", i, b)
		}
	}
	for _, call := range rec.calls {
		if call.config.env["MODE"] != "candidate" || strings.HasPrefix(call.config.runID, "scn-bravo") {
			t.Errorf("repetition %s (%s) ran", call.config.runID, call.config.env["MODE"])
		}
	}
	if len(rec.calls) != 2+1 {
		t.Errorf("%d workloads, want 3 candidate repetitions", len(rec.calls))
	}
}

// Over the cap, the suite writes a valid, bounded document that claims no
// outcome and says it is incomplete, and exits 3 — never a truncated PASS.
func TestSuiteOutputOverflowIsAnIncompleteDocument(t *testing.T) {
	dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 1), "b.yaml": suiteScenario("bravo", 1)})
	api := newScenarioAPI(t, "pass")
	run := runSuite(t, &recorder{}, api, func(r *scenarioRunner) { r.outputLimit = 1500 },
		"--suite", dir, "--scenario-timeout", "1m")
	if run.code != exitOperational || run.doc.Complete || run.doc.ExitCode != exitOperational {
		t.Fatalf("exit %d, complete %v; want 3 and incomplete", run.code, run.doc.Complete)
	}
	if run.doc.Error == nil || run.doc.Error.Code != "output_too_large" {
		t.Errorf("error = %+v", run.doc.Error)
	}
	if len(run.stdout) > 1500 {
		t.Errorf("the incomplete document is %d bytes, over the limit", len(run.stdout))
	}
	for _, m := range run.doc.Members {
		if m.Outcome != "" || m.Result != nil || m.ExitCode != nil {
			t.Errorf("an incomplete document claims an outcome: %+v", m)
		}
	}
	if len(run.doc.Members) != 2 || run.doc.Summary != (suiteSummary{}) {
		t.Errorf("members %+v summary %+v", run.doc.Members, run.doc.Summary)
	}
}

// A scenario past its deadline is an operational error even when its
// workload exits 0, its execution is recorded failed, and the suite goes on.
// Single-scenario mode gives no deadline.
func TestSuiteScenarioTimeoutIsOperationalAndTheSuiteContinues(t *testing.T) {
	dir := writeSuite(t, map[string]string{"a.yaml": suiteScenario("alpha", 2), "b.yaml": suiteScenario("bravo", 1)})
	api := newScenarioAPI(t, "pass")
	rec := &recorder{onCall: func(n int, c devConfig) bool { return c.runID == "scn-alpha-reference-1" }}
	start := time.Now()
	run := runSuite(t, rec, api, nil, "--suite", dir, "--scenario-timeout", "1s")
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("the suite took %v; the deadline did not end the scenario", elapsed)
	}
	if got := run.outcomes(); !slices.Equal(got, []string{"a.yaml:error", "b.yaml:pass"}) {
		t.Fatalf("members %v", got)
	}
	a := run.doc.Members[0]
	if a.Error == nil || a.Error.Code != "scenario_timeout" || *a.ExitCode != exitOperational {
		t.Errorf("timed-out member = %+v", a)
	}
	if !slices.Contains(api.paths(), "POST /v1/scenario-executions/scn-alpha/fail") {
		t.Errorf("the timed-out execution was not recorded failed: %v", api.paths())
	}
	for _, call := range rec.calls {
		if strings.HasPrefix(call.config.runID, "scn-alpha") && call.config.runID != "scn-alpha-reference-1" {
			t.Errorf("repetition %s ran after its scenario's deadline", call.config.runID)
		}
		if call.config.deadline == nil {
			t.Errorf("suite repetition %s has no deadline", call.config.runID)
		}
	}
	if run.code != exitOperational {
		t.Errorf("exit %d, want 3", run.code)
	}

	single := &recorder{}
	runScenario(t, single, newScenarioAPI(t, "pass").url(), "--scenario", writeScenario(t, runScenarioYAML))
	for _, call := range single.calls {
		if call.config.deadline != nil {
			t.Errorf("single-scenario repetition %s was given a deadline", call.config.runID)
		}
	}
}

// Cancelling the suite stops the current scenario — recorded failed — and
// skips everything not yet scheduled.
func TestSuiteCancellationSkipsTheRest(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"a.yaml": suiteScenario("alpha", 2), "b.yaml": suiteScenario("bravo", 1),
		"c.yaml": suiteScenario("charlie", 1),
	})
	api := newScenarioAPI(t, "pass")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := &recorder{onCall: func(n int, c devConfig) bool {
		if c.runID == "scn-alpha-reference-2" {
			cancel()
		}
		return false
	}}
	run := runSuite(t, rec, api, func(r *scenarioRunner) {
		r.suiteContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
	}, "--suite", dir, "--scenario-timeout", "1m")
	if got := run.outcomes(); !slices.Equal(got, []string{"a.yaml:error", "b.yaml:skipped", "c.yaml:skipped"}) {
		t.Fatalf("members %v", got)
	}
	if a := run.doc.Members[0]; a.Error == nil || a.Error.Code != "cancelled" {
		t.Errorf("cancelled member = %+v", a)
	}
	for _, m := range run.doc.Members[1:] {
		if m.SkippedReason != skippedCancelled {
			t.Errorf("%s skipped for %q, want cancelled", m.File, m.SkippedReason)
		}
	}
	if !slices.Contains(api.paths(), "POST /v1/scenario-executions/scn-alpha/fail") {
		t.Errorf("the cancelled execution was not recorded failed: %v", api.paths())
	}
	if run.code != exitOperational || len(rec.calls) != 2 {
		t.Errorf("exit %d after %d repetitions; want 3 and 2", run.code, len(rec.calls))
	}
}
