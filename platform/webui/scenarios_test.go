package webui

// Task 102: Evidence → Scenarios.

import (
	"regexp"
	"strings"
	"testing"
)

const scenariosDriver = `
import { shellQuote, referenceCommand, executionMatches } from "./views/scenarios.js";
const out = {
  quote: ["exec-2026-10-04.a", "has space", "it's", "a;rm -rf /", "$(id)", "plain:id@1/x+y=z"].map(shellQuote),
  command: referenceCommand("exec 1"),
  match: [
    executionMatches({ id: "e1", scenario_name: "Support", status: "completed" }, "supp"),
    executionMatches({ id: "e1", scenario_name: "support" }, "COMPLETED"),
    executionMatches({ id: "e1" }, "  "),
  ],
};
process.stdout.write(JSON.stringify(out));
`

func TestScenarioCommandIsQuotedForAShell(t *testing.T) {
	var got struct {
		Quote   []string
		Command string
		Match   []bool
	}
	decodeDriver(t, runDriver(t, scenariosDriver), &got)

	want := []string{
		"exec-2026-10-04.a",
		"'has space'",
		`'it'\''s'`,
		"'a;rm -rf /'",
		"'$(id)'",
		"plain:id@1/x+y=z",
	}
	for i := range want {
		if got.Quote[i] != want[i] {
			t.Errorf("shellQuote case %d = %s, want %s", i, got.Quote[i], want[i])
		}
	}
	if got.Command != "trustvian eval run --scenario <scenario.yaml> --reference='exec 1'" {
		t.Errorf("referenceCommand = %q", got.Command)
	}
	if !got.Match[0] || got.Match[1] || !got.Match[2] {
		t.Errorf("executionMatches = %v, want [true false true]", got.Match)
	}
}

// TestScenariosDecidesNoEligibilityAndRunsNothing: whether an execution is a
// usable reference is the server's answer, read from the check's response;
// the page derives nothing from a status, and nothing executes from it.
func TestScenariosDecidesNoEligibilityAndRunsNothing(t *testing.T) {
	raw := readAsset(t, "views/scenarios.js")
	source := stripJSNoise(raw)

	if !strings.Contains(source, "answer.usable") {
		t.Error("scenarios.js does not render the server's usable answer")
	}
	// No eligibility derived from status: the only `usable` value read is the
	// response's.
	if regexp.MustCompile(`usable\s*=\s*[^=]`).MatchString(source) {
		t.Error("scenarios.js assigns an eligibility of its own")
	}
	for _, forbidden := range []string{"fetch(", "/v1/", "localStorage", "innerHTML", "setTimeout(", "setInterval("} {
		if strings.Contains(source, forbidden) {
			t.Errorf("scenarios.js contains %q", forbidden)
		}
	}
	awaits := regexp.MustCompile(`await deps\.[^;]*;`).FindAllStringIndex(source, -1)
	if len(awaits) != 3 {
		t.Fatalf("found %d awaited reads, want 3 (list, detail, check)", len(awaits))
	}
	for _, at := range awaits {
		next := source[at[1]:min(at[1]+120, len(source))]
		if !strings.Contains(next, "Surface.owns(ticket)") {
			t.Errorf("an awaited read in scenarios.js is not followed by an ownership check: %s",
				source[at[0]:at[1]])
		}
	}
	api := stripJSComments(readAsset(t, "v1/api.js"))
	for _, route := range []string{"/reference-check", "/scenario-executions${query}"} {
		if !strings.Contains(api, route) {
			t.Errorf("api.js does not call %s", route)
		}
	}
	// Discovery only: the browser never begins, completes or fails one.
	for _, write := range []string{
		`request("POST", "/v1/scenario-executions`,
		`/complete`, `/fail`,
	} {
		if strings.Contains(api, write) && (strings.Contains(write, "scenario") ||
			strings.Contains(api, "scenario-executions/${segment(id)}"+write)) {
			t.Errorf("api.js writes scenario executions (%s); starting one stays in the CLI", write)
		}
	}
	if !strings.Contains(raw, "nothing executes here") {
		t.Error("scenarios.js no longer states that nothing executes from the page")
	}
}

// TestScenariosFollowEveryWayTheSectionIsShown is the regression for a review
// finding: Scenarios was entered only on a subtab click, so arrowing onto it
// showed an empty section, and returning to Evidence showed the previous
// project's executions. Its visibility is now tied to the subtab switcher's
// own show() — every path — and to entering Evidence.
func TestScenariosFollowEveryWayTheSectionIsShown(t *testing.T) {
	app := stripJSComments(readAsset(t, "app.js"))
	if !strings.Contains(app, `setupSubtabs("view-evidence", "evidence-finding", syncScenariosSection)`) {
		t.Error("the Evidence subtabs do not report every section change to Scenarios")
	}
	body := wholeFunctionBodyForTest(t, "app.js", "function setupSubtabs(")
	if !strings.Contains(body, "onShow(id)") {
		t.Error("setupSubtabs' show() does not call onShow; an arrow key would bypass it")
	}
	if !strings.Contains(app, "queueMicrotask(syncScenariosSection)") {
		t.Error("returning to Evidence does not re-sync Scenarios")
	}
	if strings.Contains(app, `tab.dataset.section === "evidence-scenarios"`) {
		t.Error("Scenarios is still wired to a click handler of its own")
	}
}
