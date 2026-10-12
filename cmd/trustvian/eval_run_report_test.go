package main

// D15 (task 107): after a completion, `eval run --json` prints the
// execution's report — GET /v1/scenario-executions/{id}/report, unchanged —
// so that the run's document, `eval report --json` and the comment's
// artifact are one document. A control plane without the route keeps the
// version 1 document, and says so.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// reportFor is a minimal report of one execution with a recorded gate.
func reportFor(id, name, verdict string, runs int) string {
	return fmt.Sprintf(`{"version":"2","scenario":{"name":%q,"runs":%d},"execution_id":%q,`+
		`"comparison":{"gate":{"verdict":%q,"state":"recorded"}},"served":"by the report route"}`,
		name, runs, id, verdict)
}

func TestEvalRunJSONPrintsTheExecutionsReport(t *testing.T) {
	api := newScenarioAPI(t, "fail")
	report := reportFor("scn-test", "support", "fail", 3)
	api.reports = map[string]string{"scn-test": report + "\n"}
	code, out, errOut := runScenario(t, &recorder{}, api.url(),
		"--scenario", writeScenario(t, runScenarioYAML), "--json")
	if code != exitGateFail {
		t.Fatalf("exit %d, want 1\n%s", code, errOut)
	}
	if out != report+"\n" {
		t.Fatalf("stdout is not the report, unchanged:\n%s", out)
	}
	if strings.Contains(errOut, "version 1 result document") {
		t.Errorf("stderr reports a fallback that did not happen:\n%s", errOut)
	}

	// The human rendering does not change: it is the completion's.
	api = newScenarioAPI(t, "fail")
	api.reports = map[string]string{"scn-test": report}
	_, human, _ := runScenario(t, &recorder{}, api.url(), "--scenario", writeScenario(t, runScenarioYAML))
	if !strings.Contains(human, "FAIL repeatedly added behaviors: 1 (<= 0)") || strings.Contains(human, "served") {
		t.Errorf("the human rendering changed:\n%s", human)
	}
}

func TestEvalRunWithoutAReportRouteKeepsVersionOne(t *testing.T) {
	api := newScenarioAPI(t, "pass")
	code, out, errOut := runScenario(t, &recorder{}, api.url(),
		"--scenario", writeScenario(t, runScenarioYAML), "--json")
	if code != exitOK {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Version != "1" {
		t.Fatalf("stdout is not the version 1 document (%v):\n%s", err, out)
	}
	if !strings.Contains(errOut, "the control plane serves no report route (404); "+
		"printing the version 1 result document") {
		t.Errorf("stderr does not say why:\n%s", errOut)
	}
}

// A report that contradicts the completion is printed nowhere: the command
// fails operationally, though the execution stays completed.
func TestEvalRunRefusesAReportThatContradictsTheCompletion(t *testing.T) {
	for name, report := range map[string]string{
		"another verdict":   reportFor("scn-test", "support", "pass", 3),
		"another execution": reportFor("scn-other", "support", "fail", 3),
		"no recorded gate": `{"version":"2","execution_id":"scn-test",` +
			`"comparison":{"gate":{"state":"not_recorded"}}}`,
		"version 1": `{"version":"1","execution_id":"scn-test","comparison":{"gate":{"verdict":"fail"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			api := newScenarioAPI(t, "fail")
			api.reports = map[string]string{"scn-test": report}
			code, out, errOut := runScenario(t, &recorder{}, api.url(),
				"--scenario", writeScenario(t, runScenarioYAML), "--json")
			if code != exitOperational || strings.Contains(out, `"verdict"`) {
				t.Fatalf("exit %d, stdout %s\nstderr %s", code, out, errOut)
			}
			if !strings.Contains(out+errOut, "contradicts its completion") {
				t.Errorf("the contradiction is not named:\n%s\n%s", out, errOut)
			}
		})
	}
}

// In a suite, each member embeds its execution's report.
func TestSuiteMembersEmbedTheirReports(t *testing.T) {
	dir := writeSuite(t, map[string]string{
		"a.yaml": suiteScenario("alpha", 1), "b.yaml": suiteScenario("bravo", 1),
	})
	api := newScenarioAPI(t, "pass")
	api.reports = map[string]string{
		"scn-alpha": reportFor("scn-alpha", "alpha", "pass", 1),
		"scn-bravo": reportFor("scn-bravo", "bravo", "pass", 1),
	}
	run := runSuite(t, &recorder{}, api, nil, "--suite", dir, "--scenario-timeout", "1m")
	if run.code != exitOK {
		t.Fatalf("exit %d\n%s", run.code, run.stderr)
	}
	for _, m := range run.doc.Members {
		var result struct {
			Version     string `json:"version"`
			ExecutionID string `json:"execution_id"`
			Served      string `json:"served"`
		}
		if err := json.Unmarshal(m.Result, &result); err != nil {
			t.Fatal(err)
		}
		if result.Version != "2" || result.ExecutionID != m.ExecutionID || result.Served != "by the report route" {
			t.Errorf("member %s embeds %s", m.File, m.Result)
		}
	}
}
