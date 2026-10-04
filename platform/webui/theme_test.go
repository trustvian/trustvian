package webui

// Task 097: the colour-scheme preference.
//
// Behavioral where it matters — the shipped module and the shipped pre-paint
// script are both run under node against fake storage — and structural for
// the stylesheet, where the property is that two copies of the dark palette
// stay identical.

import (
	"regexp"
	"strings"
	"testing"
)

const themeDriver = `
import * as theme from "./core/theme.js";
import fs from "node:fs";
import vm from "node:vm";

const out = { read: {}, resolve: {}, write: {}, boot: {}, apply: {} };

function storage(initial, failing) {
  const map = new Map(initial === undefined ? [] : [["trustvian.theme", initial]]);
  return {
    map,
    getItem(k) { if (failing === "get") throw new Error("blocked"); return map.has(k) ? map.get(k) : null; },
    setItem(k, v) { if (failing === "set") throw new Error("quota"); map.set(k, String(v)); },
    removeItem(k) { if (failing === "set") throw new Error("quota"); map.delete(k); },
  };
}

for (const [name, initial] of Object.entries({
  none: undefined, light: "light", dark: "dark", system: "system", garbage: "Dark", json: "{\"x\":1}",
})) {
  out.read[name] = theme.readThemePreference(storage(initial));
}
out.read["throwing getter"] = theme.readThemePreference(storage("dark", "get"));
out.read["no storage"] = theme.readThemePreference(null);
out.read["blocked window"] = theme.readThemePreference(theme.browserStorage({
  get localStorage() { throw new Error("SecurityError"); },
}));

for (const pref of ["light", "dark", "system"]) {
  for (const sys of [false, true]) {
    out.resolve[pref + "/" + (sys ? "dark" : "light")] = theme.resolveTheme(pref, sys);
  }
}

for (const pref of ["light", "dark", "system", "sepia"]) {
  const s = storage("dark");
  const ok = theme.writeThemePreference(s, pref);
  out.write[pref] = { ok, stored: s.map.has("trustvian.theme") ? s.map.get("trustvian.theme") : null };
}
out.write["failing setter"] = { ok: theme.writeThemePreference(storage(undefined, "set"), "dark"), stored: null };
out.write["no storage"] = { ok: theme.writeThemePreference(null, "dark"), stored: null };

function fakeRoot() {
  const attrs = new Map();
  return {
    attrs,
    setAttribute(k, v) { attrs.set(k, v); },
    removeAttribute(k) { attrs.delete(k); },
  };
}
for (const pref of ["light", "dark", "system"]) {
  const root = fakeRoot();
  root.setAttribute("data-theme", "dark");
  theme.applyThemePreference(root, pref);
  out.apply[pref] = root.attrs.has("data-theme") ? root.attrs.get("data-theme") : null;
}

// The pre-paint script, run exactly as shipped.
const bootSource = fs.readFileSync("./core/theme-boot.js", "utf8");
for (const [name, initial, failing] of [
  ["none", undefined, ""], ["light", "light", ""], ["dark", "dark", ""],
  ["garbage", "Dark", ""], ["system", "system", ""], ["throwing", "dark", "get"],
]) {
  const root = fakeRoot();
  const s = storage(initial, failing);
  const context = { window: { localStorage: s }, document: { documentElement: root } };
  vm.runInNewContext(bootSource, context);
  out.boot[name] = root.attrs.has("data-theme") ? root.attrs.get("data-theme") : null;
}
{
  const root = fakeRoot();
  const context = {
    window: { get localStorage() { throw new Error("SecurityError"); } },
    document: { documentElement: root },
  };
  vm.runInNewContext(bootSource, context);
  out.boot["blocked"] = root.attrs.has("data-theme") ? root.attrs.get("data-theme") : null;
}
process.stdout.write(JSON.stringify(out));
`

// TestThemePreferenceResolvesAndSurvivesStorageFailure covers every stored
// value against every system scheme, and every way storage can fail.
func TestThemePreferenceResolvesAndSurvivesStorageFailure(t *testing.T) {
	var got struct {
		Read    map[string]string `json:"read"`
		Resolve map[string]string `json:"resolve"`
		Write   map[string]struct {
			OK     bool    `json:"ok"`
			Stored *string `json:"stored"`
		} `json:"write"`
		Boot  map[string]*string `json:"boot"`
		Apply map[string]*string `json:"apply"`
	}
	decodeDriver(t, runDriver(t, themeDriver), &got)

	wantRead := map[string]string{
		"none": "system", "light": "light", "dark": "dark",
		// "system" is never stored; finding it means someone else wrote it.
		"system": "system", "garbage": "system", "json": "system",
		"throwing getter": "system", "no storage": "system", "blocked window": "system",
	}
	for name, want := range wantRead {
		if got.Read[name] != want {
			t.Errorf("read %s = %q, want %q", name, got.Read[name], want)
		}
	}

	wantResolve := map[string]string{
		"light/light": "light", "light/dark": "light",
		"dark/light": "dark", "dark/dark": "dark",
		"system/light": "light", "system/dark": "dark",
	}
	for name, want := range wantResolve {
		if got.Resolve[name] != want {
			t.Errorf("resolve %s = %q, want %q", name, got.Resolve[name], want)
		}
	}

	str := func(p *string) string {
		if p == nil {
			return "<absent>"
		}
		return *p
	}
	wantWrite := map[string]struct {
		ok     bool
		stored string
	}{
		"light":          {true, "light"},
		"dark":           {true, "dark"},
		"system":         {true, "<absent>"}, // removal, not a stored "system"
		"sepia":          {false, "dark"},    // refused; the previous choice is untouched
		"failing setter": {false, "<absent>"},
		"no storage":     {false, "<absent>"},
	}
	for name, want := range wantWrite {
		entry := got.Write[name]
		if entry.OK != want.ok || str(entry.Stored) != want.stored {
			t.Errorf("write %s = {ok:%v stored:%s}, want {ok:%v stored:%s}",
				name, entry.OK, str(entry.Stored), want.ok, want.stored)
		}
	}

	wantApply := map[string]string{"light": "light", "dark": "dark", "system": "<absent>"}
	for name, want := range wantApply {
		if str(got.Apply[name]) != want {
			t.Errorf("apply %s left data-theme=%s, want %s", name, str(got.Apply[name]), want)
		}
	}

	// The pre-paint script agrees with the module on every input: it marks
	// exactly what readThemePreference would return as light or dark, and
	// marks nothing otherwise.
	wantBoot := map[string]string{
		"none": "<absent>", "light": "light", "dark": "dark", "garbage": "<absent>",
		"system": "<absent>", "throwing": "<absent>", "blocked": "<absent>",
	}
	for name, want := range wantBoot {
		if str(got.Boot[name]) != want {
			t.Errorf("pre-paint %s set data-theme=%s, want %s", name, str(got.Boot[name]), want)
		}
	}
}

// TestDarkPaletteIsDeclaredIdenticallyForBothTriggers keeps the explicit
// choice and the system preference from drifting into two dark themes.
func TestDarkPaletteIsDeclaredIdenticallyForBothTriggers(t *testing.T) {
	tokens := readAsset(t, "tokens.css")

	body := func(selector string) string {
		start := strings.Index(tokens, selector+" {")
		if start < 0 {
			t.Fatalf("tokens.css has no %s block", selector)
		}
		rest := tokens[start+len(selector)+2:]
		end := strings.Index(rest, "}")
		if end < 0 {
			t.Fatalf("%s block is not terminated", selector)
		}
		lines := []string{}
		for line := range strings.SplitSeq(rest[:end], "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				lines = append(lines, regexp.MustCompile(`\s+`).ReplaceAllString(trimmed, " "))
			}
		}
		return strings.Join(lines, "\n")
	}

	explicit := body(`:root[data-theme="dark"]`)
	system := body(`:root:not([data-theme="light"])`)
	if explicit != system {
		t.Errorf("the two dark blocks differ; choosing Dark and following a dark system "+
			"must render the same palette.\nexplicit:\n%s\n\nsystem:\n%s", explicit, system)
	}
	if !strings.Contains(explicit, "color-scheme: dark") {
		t.Error("the dark blocks do not declare color-scheme: dark; native controls " +
			"and scrollbars would stay light")
	}
	if !strings.Contains(body(":root"), "color-scheme: light") {
		t.Error("the light root does not declare color-scheme: light")
	}
	if !strings.Contains(tokens, "@media (prefers-color-scheme: dark)") {
		t.Error("the system trigger is missing")
	}
}

// TestThemeSwitcherIsANativeRadioGroupAppliedBeforePaint pins the shell.
func TestThemeSwitcherIsANativeRadioGroupAppliedBeforePaint(t *testing.T) {
	shell := readAsset(t, "index.html")

	boot := strings.Index(shell, `<script src="/assets/core/theme-boot.js"></script>`)
	firstSheet := strings.Index(shell, `<link rel="stylesheet"`)
	if boot < 0 || firstSheet < 0 || boot > firstSheet {
		t.Error("the pre-paint script must be a classic script in <head>, before the " +
			"stylesheets, or a dark choice flashes light on load")
	}
	for _, pref := range []string{"light", "dark", "system"} {
		if !regexp.MustCompile(`<input type="radio" name="theme" value="` + pref +
			`" id="theme-` + pref + `"`).MatchString(shell) {
			t.Errorf("the switcher has no native radio for %s", pref)
		}
	}
	if !strings.Contains(shell, `<legend class="theme-legend">Theme</legend>`) {
		t.Error("the switcher has no visible legend; a radio group needs a name")
	}

	module := readAsset(t, "core/theme.js")
	if !strings.Contains(module, `THEME_PREFERENCES = Object.freeze(["system", "light", "dark"])`) {
		t.Error("core/theme.js no longer offers exactly System, Light and Dark")
	}
}
