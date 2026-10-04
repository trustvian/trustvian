package webui

// Guards over the design system.
//
// Task 096 made the browser surface a console; this file is what keeps it a
// *system* rather than a directory of files that happen to sit together. Each
// guard below pins one of the four properties that make the difference:
//
//   1. Values are named once, in one file.
//   2. The layers only depend downward.
//   3. A referenced icon exists.
//   4. Loading and empty are different answers, and stay different.
//
// Every one of them is a property a reviewer would otherwise have to hold in
// their head across twenty files.

import (
	"path"
	"regexp"
	"strings"
	"testing"
)

// TestOnlyTheTokenSheetNamesARawValue is what makes the palette a palette.
//
// A hex literal in a component is a decision made twice: the dark-mode value
// is somewhere else, the contrast was checked against a surface that may have
// moved, and the next person changing the colour finds one of the two copies.
// Every other sheet reads var(--…), and this is why that stays true.
func TestOnlyTheTokenSheetNamesARawValue(t *testing.T) {
	literal := regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(`)

	sheets := 0
	for _, name := range assetNames() {
		if !strings.HasSuffix(name, ".css") || path.Base(name) == "tokens.css" {
			continue
		}
		sheets++
		for index, line := range strings.Split(readAsset(t, name), "\n") {
			// Comments explain the values they are about, and a comment is not
			// a declaration.
			if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "*") ||
				strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if match := literal.FindString(line); match != "" {
				t.Errorf("%s:%d declares %s; every value is named in tokens.css and "+
					"read as var(--…), so light and dark cannot drift apart",
					name, index+1, match)
			}
		}
	}
	if sheets < 3 {
		t.Fatalf("only %d non-token sheets found; this guard would not be meaningful", sheets)
	}

	// And the token sheet defines both schemes, so every name resolves in
	// either one.
	tokens := readAsset(t, "tokens.css")
	if !strings.Contains(tokens, "prefers-color-scheme: dark") {
		t.Error("tokens.css defines no dark scheme; a token that only resolves in " +
			"one scheme is a missing value in the other")
	}
}

// TestAssetLayersDependOnlyDownward pins the module tree.
//
// The bundle is five layers, and the direction is the whole point:
//
//	core/   → nothing.              DOM and formatting, no Trustvian at all.
//	v1/     → core.                 The control-plane contract.
//	ui/     → core.                 Presentation; reaches no API.
//	live/   → live.                 The realtime observatory.
//	views/  → core, v1, ui, views.  One destination each.
//	app.js  → anything.             The composition root, and the only one.
//
// `ui/` importing `v1/` is the failure this is really about: the moment a
// component can read a route, "presentation" stops being a layer and the
// design system starts carrying domain knowledge.
func TestAssetLayersDependOnlyDownward(t *testing.T) {
	allowed := map[string]map[string]bool{
		"core":  {},
		"v1":    {"core": true},
		"ui":    {"core": true, "ui": true},
		"live":  {"core": true, "live": true},
		"views": {"core": true, "v1": true, "ui": true, "views": true},
	}

	seen := make(map[string]bool)
	for name, source := range scriptAssets(t) {
		layer := path.Dir(name)
		if layer == "." {
			// app.js is the composition root and may import anything. That is
			// what a composition root is for, and there is exactly one.
			continue
		}
		permitted, known := allowed[layer]
		if !known {
			t.Errorf("%s is in %q, which is not one of the declared layers", name, layer)
			continue
		}
		seen[layer] = true
		for _, match := range regexp.MustCompile(`from\s+"([^"]+)"`).
			FindAllStringSubmatch(stripJSComments(source), -1) {
			target := path.Join(path.Dir(name), match[1])
			targetLayer := path.Dir(target)
			if targetLayer == "." {
				t.Errorf("%s imports %s from the bundle root; only app.js lives there",
					name, target)
				continue
			}
			if !permitted[targetLayer] {
				t.Errorf("%s imports %s: %q may not depend on %q",
					name, target, layer, targetLayer)
			}
		}
	}
	for layer := range allowed {
		if !seen[layer] {
			t.Errorf("no module found in %q; the layer is declared and empty", layer)
		}
	}
}

// TestEveryIconNameResolves keeps a renamed glyph from silently disappearing.
//
// icon() returns an empty node for a name it does not know, which is the
// right runtime behaviour — a missing icon should vanish rather than break a
// page — and exactly the behaviour that would let a typo ship unnoticed.
func TestEveryIconNameResolves(t *testing.T) {
	source := readAsset(t, "icons.js")
	declared := make(map[string]bool)
	for _, match := range regexp.MustCompile(`name:\s*"(\w+)"`).FindAllStringSubmatch(source, -1) {
		declared[match[1]] = true
	}
	if len(declared) < 8 {
		t.Fatalf("found %d icons; this guard would not be meaningful", len(declared))
	}

	used := 0
	// Referenced from the shell, where the markup says which destination
	// carries which glyph.
	for _, match := range regexp.MustCompile(`data-icon="(\w+)"`).
		FindAllStringSubmatch(readAsset(t, "index.html"), -1) {
		used++
		if !declared[match[1]] {
			t.Errorf("index.html asks for the icon %q, which is not declared", match[1])
		}
	}
	// And from the scripts, where a view picks one per row.
	for name, script := range scriptAssets(t) {
		if path.Base(name) == "icons.js" {
			continue
		}
		for _, match := range regexp.MustCompile(`\bicon\("(\w+)"`).
			FindAllStringSubmatch(stripJSComments(script), -1) {
			used++
			if !declared[match[1]] {
				t.Errorf("%s asks for the icon %q, which is not declared", name, match[1])
			}
		}
	}
	if used == 0 {
		t.Fatal("no icon is referenced anywhere; this guard would pass vacuously")
	}

	// Icons are decoration beside a word, never the word itself.
	if !strings.Contains(source, `"aria-hidden", "true"`) {
		t.Error("icons.js does not hide its glyphs from assistive technology; an " +
			"icon sits beside a label and must not be read as a second one")
	}
	// And they are built, never parsed. A sprite string would be the one
	// place markup entered this bundle.
	if !strings.Contains(source, "createElementNS") {
		t.Error("icons.js does not build its nodes; markup is never parsed here")
	}
}

// TestLoadingIsNotTheSameAnswerAsEmpty is the state distinction, pinned.
//
// Before the design system these two rendered identically — one line of
// italic text — so a reader could not tell a table that was still fetching
// from a table with nothing in it. The skeleton exists to keep them apart,
// and the order of the branches is what makes it work: a surface that is
// loading has no rows yet, so an empty check first would always win.
func TestLoadingIsNotTheSameAnswerAsEmpty(t *testing.T) {
	dashboard := wholeFunctionBodyForTest(t, "dashboard.js", "export function dataTable(")
	loadingAt := strings.Index(dashboard, "spec.loading")
	emptyAt := strings.Index(dashboard, "rows.length === 0")
	if loadingAt < 0 {
		t.Fatal("dataTable has no loading state; a fetching table reads as an empty one")
	}
	if emptyAt < 0 {
		t.Fatal("dataTable has no empty state")
	}
	if loadingAt > emptyAt {
		t.Error("dataTable checks for empty before loading; a table that is still " +
			"fetching has no rows, so the empty branch would always win")
	}

	// Every collection that can be fetched says so while it is fetching.
	// The hierarchy's four flags live in app.js; the run workspace's and
	// Compare's live in their state modules, because lowering one is an
	// ownership decision rather than a step in a request.
	app := stripJSComments(readAsset(t, "app.js"))
	if !strings.Contains(app, "loading.projects") {
		t.Error("no loading state for projects; its table cannot tell a reader to wait")
	}
	// The Runs destination's three lists read the shared context's pages
	// (task 104), whose loading flag only the owning read lowers
	// (views/context.js). Each render must hand that flag to its list.
	for _, collection := range []struct{ render, page string }{
		{"function renderAgentsList(", "selection.pages.agents"},
		{"function renderCandidatesList(", "selection.pages.candidates"},
		{"function renderRunsView(", "selection.pages.runs"},
	} {
		body := wholeFunctionBodyForTest(t, "app.js", collection.render)
		if !strings.Contains(body, collection.page) || !strings.Contains(body, ".loading") {
			t.Errorf("%s does not draw %s's loading state; its list cannot tell a reader to wait",
				collection.render, collection.page)
		}
	}
	for _, surface := range []string{
		"runState.observations.loading",
		"runState.behaviors.loading",
		"projectScope.compareLoading.runs",
		"projectScope.compareLoading.agents",
	} {
		if !strings.Contains(app, surface) {
			t.Errorf("no loading state read for %s; its table cannot tell a reader "+
				"to wait", surface)
		}
	}

	// A flag that is raised and never lowered is a skeleton that never
	// resolves — and one lowered by the wrong request is a skeleton that
	// vanishes while a newer read is still outstanding. The second is what a
	// `finally` produced here, and it is why there is no longer one: raising
	// the flag issues a ticket, and every path that lowers it must present
	// that ticket back.
	//
	// TestASupersededResponseLeavesTheLoadingIndicatorAlone in
	// request_ownership_test.go proves the behaviour; this keeps the
	// mechanism from being replaced by an unguarded assignment.
	lowers := map[string]string{
		"run-state.js":     "loading: false",
		"project-scope.js": "[ticket.level]: false",
	}
	for module, clears := range lowers {
		if !strings.Contains(stripJSComments(readAsset(t, module)), clears) {
			t.Errorf("%s never lowers a loading flag; a skeleton would never resolve",
				module)
		}
	}
	appLogic := stripJSNoise(readAsset(t, "app.js"))
	if regexp.MustCompile(`loading\.(observations|behaviors)\s*=`).MatchString(appLogic) {
		t.Error("app.js assigns a run-workspace loading flag directly; it must go " +
			"through the ticket that raised it, or an older request can clear a " +
			"newer request's indicator")
	}
	if strings.Contains(appLogic, "compareLoading[") ||
		regexp.MustCompile(`compareLoading\.\w+\s*=`).MatchString(appLogic) {
		t.Error("app.js assigns a Compare loading flag directly; it must go through " +
			"the ticket that raised it")
	}

	// And an empty state says what to do, rather than only that there is
	// nothing. A bare "Nothing to show." is the thing this replaced.
	feedback := readAsset(t, "feedback.js")
	if !strings.Contains(feedback, "emptyState") || !strings.Contains(feedback, "hint") {
		t.Error("feedback.js offers no empty state with a next action")
	}
}
