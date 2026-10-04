package webui

// Task 098: the searchable selector and the fields it replaced.

import (
	"strings"
	"testing"
)

const selectorDriver = `
import { filterOptions, moveActive, statusText } from "./ui/selector.js";
import { describe } from "./views/selectors.js";

const options = [
  { id: "support-demo", name: "Support assistant", detail: "" },
  { id: "billing-demo", name: "Billing reconciler", detail: "finance" },
  { id: "git:43af19c", name: "baseline 43af19c", detail: "gpt-4.1-mini · main" },
];
const out = {
  filter: {
    empty: filterOptions(options, "").map((o) => o.id),
    byName: filterOptions(options, "SUPPORT").map((o) => o.id),
    byID: filterOptions(options, "43af").map((o) => o.id),
    byDetail: filterOptions(options, "finance").map((o) => o.id),
    none: filterOptions(options, "zzz").map((o) => o.id),
    spaces: filterOptions(options, "  billing  ").map((o) => o.id),
  },
  move: {
    downFromNone: moveActive(-1, 3, "ArrowDown"),
    upFromNone: moveActive(-1, 3, "ArrowUp"),
    wrapDown: moveActive(2, 3, "ArrowDown"),
    wrapUp: moveActive(0, 3, "ArrowUp"),
    home: moveActive(2, 3, "Home"),
    end: moveActive(0, 3, "End"),
    emptyList: moveActive(0, 0, "ArrowDown"),
    otherKey: moveActive(1, 3, "a"),
  },
  status: {
    disabled: statusText({ disabled: true, disabledText: "Choose a project first.", total: 0 }, 0),
    loading: statusText({ loading: true, total: 0 }, 0),
    error: statusText({ error: "unreachable", total: 0 }, 0),
    empty: statusText({ total: 0, emptyText: "This project has no agents." }, 0),
    whole: statusText({ total: 3, whole: true }, 3),
    partial: statusText({ total: 64, whole: false }, 64),
    filtered: statusText({ total: 64, whole: false }, 2),
    filteredWhole: statusText({ total: 3, whole: true }, 1),
    capped: statusText({ total: 512, whole: false, capped: true }, 512),
  },
  describe: {
    project: describe("project", { id: "p", name: "Proj" }),
    unnamedAgent: describe("agent", { id: "a" }),
    candidate: describe("candidate", { id: "c", metadata: { label: "v2", model: "m", source_ref: "main", config_digest: "SECRET" } }),
    run: describe("run", { id: "r", status: "completed", environment: "local", created_at: "2026-10-04T09:00:00Z", behavioral_profile: "SECRET" }),
    environment: describe("environment", { ref: "staging", name: "Staging", rank: 1, status: "active" }),
  },
};
for (const value of Object.values(out.describe)) { delete value.row; }
process.stdout.write(JSON.stringify(out));
`

func TestSelectorFiltersMovesAndExplainsItsScope(t *testing.T) {
	type described struct {
		ID, Name, Detail string
	}
	var got struct {
		Filter   map[string][]string
		Move     map[string]int
		Status   map[string]string
		Describe map[string]described
	}
	decodeDriver(t, runDriver(t, selectorDriver), &got)

	wantFilter := map[string][]string{
		"empty":    {"support-demo", "billing-demo", "git:43af19c"},
		"byName":   {"support-demo"},
		"byID":     {"git:43af19c"},
		"byDetail": {"billing-demo"},
		"none":     {},
		"spaces":   {"billing-demo"},
	}
	for name, want := range wantFilter {
		if strings.Join(got.Filter[name], ",") != strings.Join(want, ",") {
			t.Errorf("filter %s = %v, want %v", name, got.Filter[name], want)
		}
	}

	wantMove := map[string]int{
		"downFromNone": 0, "upFromNone": 2, "wrapDown": 0, "wrapUp": 2,
		"home": 0, "end": 2, "emptyList": -1, "otherKey": 1,
	}
	for name, want := range wantMove {
		if got.Move[name] != want {
			t.Errorf("move %s = %d, want %d", name, got.Move[name], want)
		}
	}

	// The footer says what the options are: the whole collection, one page
	// of a larger one, or a filter over what was loaded. A filter finding
	// nothing on a partial page must never read as "does not exist".
	wantStatus := map[string]string{
		"disabled":      "Choose a project first.",
		"loading":       "Loading…",
		"error":         "Could not load: unreachable",
		"empty":         "This project has no agents.",
		"whole":         "3 in all.",
		"partial":       "64 loaded; more exist.",
		"filtered":      "2 of 64 loaded match; more exist.",
		"filteredWhole": "1 of 3 match.",
		"capped":        "512 loaded; more exist. Paste an ID to reach one further along.",
	}
	for name, want := range wantStatus {
		if got.Status[name] != want {
			t.Errorf("status %s = %q, want %q", name, got.Status[name], want)
		}
	}

	// Names lead, the identifier is always the value, and nothing outside
	// the named fields reaches a selector.
	wantDescribe := map[string]described{
		"project":      {"p", "Proj", ""},
		"unnamedAgent": {"a", "a", ""},
		"candidate":    {"c", "v2", "m · main"},
		"run":          {"r", "r", "completed · local · created 10-04 09:00:00"},
		"environment":  {"staging", "Staging", "rank 1 · active"},
	}
	for name, want := range wantDescribe {
		if got.Describe[name] != want {
			t.Errorf("describe %s = %+v, want %+v", name, got.Describe[name], want)
		}
		if strings.Contains(got.Describe[name].Detail, "SECRET") {
			t.Errorf("describe %s leaked an unnamed field: %q", name, got.Describe[name].Detail)
		}
	}
}

// TestIdentifierFieldsOfferASelectorAndKeepAPastePath is the audit in task
// 098, pinned: every field that has a collection behind it gains a selector
// host, and its identifier input survives inside an "Advanced" disclosure as
// the paste path, so a value from a log still works.
func TestIdentifierFieldsOfferASelectorAndKeepAPastePath(t *testing.T) {
	shell := readAsset(t, "index.html")

	cases := []struct{ selector, input string }{
		{"watch-run-select", "watch-run-id"},
		{"evidence-run-select", "evidence-run-id"},
		{"evidence-provenance-reference-select", "evidence-provenance-reference"},
		{"evidence-provenance-candidate-select", "evidence-provenance-candidate"},
		{"promotion-project-select", "promotion-list-project"},
		{"promotion-reference-pick", "promotion-reference"},
		{"promotion-candidate-pick", "promotion-candidate"},
		{"agent-project-select", "agent-project-id"},
		{"candidate-agent-select", "candidate-agent-id"},
		{"run-candidate-select", "run-candidate-id"},
		{"run-environment-select", "run-environment"},
	}
	for _, c := range cases {
		if !strings.Contains(shell, `<div id="`+c.selector+`"></div>`) {
			t.Errorf("no selector host %s", c.selector)
		}
		at := strings.Index(shell, `id="`+c.input+`"`)
		if at < 0 {
			t.Errorf("the paste field %s is gone; an identifier from a log must still work", c.input)
			continue
		}
		opened := strings.LastIndex(shell[:at], `<details class="paste-path">`)
		closed := strings.LastIndex(shell[:at], `</details>`)
		if opened < 0 || closed > opened {
			t.Errorf("%s is not inside an Advanced paste disclosure", c.input)
		}
		// A required input inside a closed disclosure cannot be focused by the
		// browser's own validation, which then blocks the submit silently.
		tag := shell[at:min(at+200, len(shell))]
		tag = tag[:strings.Index(tag, ">")]
		if strings.Contains(tag, "required") {
			t.Errorf("%s is required inside a disclosure; validate it in the handler instead", c.input)
		}
	}

	// Creation fields stay text: they name something new.
	for _, field := range []string{"project-id", "agent-id", "candidate-id", "run-id", "run-profile", "promotion-id"} {
		at := strings.Index(shell, `id="`+field+`"`)
		if at < 0 {
			t.Errorf("creation field %s is gone", field)
			continue
		}
		if strings.LastIndex(shell[:at], `<details class="paste-path">`) > strings.LastIndex(shell[:at], `</details>`) {
			t.Errorf("creation field %s was hidden in a paste disclosure", field)
		}
	}

	app := stripJSComments(readAsset(t, "app.js"))
	for _, host := range []string{"lifecycle-run-select", "watch-run-select", "run-environment-select"} {
		if !strings.Contains(app, `bindSelector(byID("`+host+`")`) {
			t.Errorf("%s is never bound", host)
		}
	}
}

// TestSelectorDrawsAndDoesNotFetch keeps the component in its layer.
func TestSelectorDrawsAndDoesNotFetch(t *testing.T) {
	source := stripJSNoise(readAsset(t, "ui/selector.js"))
	for _, forbidden := range []string{"fetch(", "api.", "/v1"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("ui/selector.js contains %q; a selector draws options it is handed", forbidden)
		}
	}
	if strings.Contains(source, "innerHTML") {
		t.Error("ui/selector.js uses innerHTML")
	}
	for _, aria := range []string{`"role", "combobox"`, `"aria-expanded"`, `"aria-controls"`,
		`"aria-activedescendant"`, `"role", "listbox"`, `"role", "option"`, `"aria-selected"`} {
		if !strings.Contains(readAsset(t, "ui/selector.js"), aria) {
			t.Errorf("ui/selector.js does not set %s; it is an ARIA 1.2 combobox", aria)
		}
	}
}

// TestSelectorWritesItsFieldOnlyWhenTheChoiceChanges is the regression for a
// review finding: render() ran on every context notification and wrote ""
// into the form's field whenever its level had no selection, so a pasted
// identifier — or the #run= prefill — was wiped by an unrelated page load.
func TestSelectorWritesItsFieldOnlyWhenTheChoiceChanges(t *testing.T) {
	body := wholeFunctionBodyForTest(t, "views/selectors.js", "function render()")
	if !strings.Contains(body, "value !== lastPushed") {
		t.Error("render() writes the form field without checking the choice changed")
	}
	if strings.Count(body, "options.input.value =") != 1 {
		t.Error("render() should write the form field in exactly one guarded place")
	}
}
