package webui

// Task 105: the Status destination.
//
// Held to the rules every other surface is, by reading its source: it renders
// GET /v1/status and computes nothing, it owns its one read, it runs no timer,
// and it is the landing view only when the control plane says so.

import (
	"regexp"
	"strings"
	"testing"
)

func TestStatusDestinationExistsAndIsHiddenOnLoad(t *testing.T) {
	shell := readAsset(t, "index.html")
	if !strings.Contains(shell, `id="nav-status" data-view="view-status" data-icon="status"`) {
		t.Error("no Status destination in the sidebar")
	}
	// Hidden in the shell: Live is the static landing view, and only the
	// control plane's answer moves the page to Status (see the next test).
	if !regexp.MustCompile(`<section class="view" id="view-status" data-nav="nav-status" hidden>`).MatchString(shell) {
		t.Error("the Status view is missing or visible before the status document has been read")
	}
	for _, host := range []string{
		"status-read", "status-refresh", "status-landing", "status-suggestions",
		"status-collectors", "status-engine", "status-bounds",
	} {
		if !strings.Contains(shell, `id="`+host+`"`) {
			t.Errorf("no %s host in the shell", host)
		}
	}
}

// TestStatusIsTheLandingViewOnlyWhenTheServerSaysSo: the browser reads the
// document's `landing` field and applies no rule of its own.
func TestStatusIsTheLandingViewOnlyWhenTheServerSaysSo(t *testing.T) {
	app := stripJSComments(readAsset(t, "app.js"))

	startup := app[strings.LastIndex(app, "liveSession.watch(\"\");"):]
	if got := strings.Count(startup, "api.getStatus()"); got != 1 {
		t.Fatalf("startup reads the status %d times, want exactly once", got)
	}
	if !strings.Contains(startup, `document.landing === "status" && currentView === "view-live"`) {
		t.Error("startup does not open Status from the server's landing field, or would steal the " +
			"reader from a destination they already chose")
	}
	// The rule itself — a fresh Collector and a fresh producer — must not
	// exist in the browser in any form.
	for _, name := range []string{"app.js", "views/status.js"} {
		source := stripJSNoise(readAsset(t, name))
		for _, rule := range []string{"fresh_window_seconds <", "last_seen_at <", "last_report_at <",
			"Date.now(", "new Date(", ".getTime("} {
			if strings.Contains(source, rule) && name == "views/status.js" {
				t.Errorf("%s contains %q; whether anything is active is the control plane's answer", name, rule)
			}
		}
		if strings.Contains(source, `state === "reporting" &&`) {
			t.Errorf("%s decides activity from a Collector's state; it reads `landing`", name)
		}
	}
}

func TestStatusViewOwnsItsReadAndRunsNoTimer(t *testing.T) {
	source := stripJSNoise(readAsset(t, "views/status.js"))
	for _, timer := range []string{"setTimeout(", "setInterval(", "requestAnimationFrame("} {
		if strings.Contains(source, timer) {
			t.Errorf("status.js uses %s; freshness is a read time, a Refresh control and the realtime hint", timer)
		}
	}
	for _, forbidden := range []string{"fetch(", "/v1/", "localStorage", "sessionStorage", "EventSource"} {
		if strings.Contains(source, forbidden) {
			t.Errorf("status.js contains %q; it reads through its injected call only", forbidden)
		}
	}
	reads := regexp.MustCompile(`await deps\.[a-zA-Z]+\([^)]*\);`).FindAllStringIndex(source, -1)
	if len(reads) != 1 {
		t.Fatalf("found %d awaited reads in status.js, want exactly one", len(reads))
	}
	next := source[reads[0][1]:min(reads[0][1]+80, len(source))]
	if !strings.Contains(next, "surface.owns(ticket)") {
		t.Error("the status read is not followed by an ownership check, so a late answer could overwrite a newer one")
	}
	// A hint arriving mid-read asks for one more read, never a queue.
	if !strings.Contains(source, "again = true") {
		t.Error("status.js does not coalesce hints that arrive during a read")
	}
}

// TestStatusViewComputesNothing: every figure is a field of the document, so
// none may be combined, compared numerically or derived.
func TestStatusViewComputesNothing(t *testing.T) {
	source := stripJSNoise(readAsset(t, "views/status.js"))
	fields := `(received|evaluated|invalid|analyze_errors|spans|calls|semantic|transport|unbound|` +
		`bound_by_override|bound_by_service_name|learned|not_learned|observe_errors|count|distinct_operations)`
	// An operator on either side of a counter field. `=>` is an arrow, not a
	// comparison, so a `>` preceded by `=` is excluded.
	arithmetic := regexp.MustCompile(`\.` + fields + `\s*[-+*/%<>]|(^|[^=!<>])[-+*/%<>]=?\s*\w+\.` + fields + `\b`)
	if location := arithmetic.FindStringIndex(source); location != nil {
		t.Errorf("status.js applies an operator to a counter: %q", source[location[0]:location[1]])
	}
	for _, derived := range []string{".reduce(", "Math.", ".sort(", "toFixed("} {
		if strings.Contains(source, derived) {
			t.Errorf("status.js uses %s; it renders the server's figures in the server's order", derived)
		}
	}
	raw := strings.ToLower(readAsset(t, "views/status.js"))
	for _, verdict := range []string{"\"healthy\"", "\"unhealthy\"", "health score", "\"ok\"", "\"degraded\""} {
		if strings.Contains(raw, verdict) {
			t.Errorf("status.js renders %s; the page shows facts and named suggestions, never a verdict", verdict)
		}
	}
	// Unreported is said in words, never shown as an empty cell or a zero.
	literal := stripJSComments(readAsset(t, "views/status.js"))
	if !strings.Contains(literal, `const NOT_REPORTED = "not reported by this Collector"`) {
		t.Error("status.js has no explicit wording for a section the Collector did not report")
	}
	// Suggestions are rendered as given: the text, the rule and its evidence.
	for _, field := range []string{"s.text", "s.rule", "s.rule_version", "s.evidence"} {
		if !strings.Contains(literal, field) {
			t.Errorf("status.js does not render %s", field)
		}
	}
}

// TestStatusHintIsNotADomainEvent: the hint rides the unfiltered Live stream,
// outside the domain kinds, and never fails the stream it rides on.
func TestStatusHintIsNotADomainEvent(t *testing.T) {
	realtime := stripJSNoise(readAsset(t, "v1/realtime.js"))
	kinds := realtime[strings.Index(realtime, "export const KNOWN_KINDS"):]
	kinds = kinds[:strings.Index(kinds, "]);")]
	if strings.Contains(kinds, "status_changed") {
		t.Error("status_changed is a domain kind; it describes no run and must not go through domain validation")
	}
	hint := realtime[strings.Index(realtime, "addEventListener(STATUS_CHANGED_KIND"):]
	hint = hint[:strings.Index(hint, "for (const kind of KNOWN_KINDS)")]
	if strings.Contains(hint, "failStream") {
		t.Error("a malformed status hint fails the Live stream")
	}
	if !strings.Contains(stripJSComments(readAsset(t, "v1/realtime.js")),
		`typeof this.callbacks.onStatusChanged === "function"`) {
		t.Error("the hint listener is registered even for a session that did not ask for it")
	}
	app := stripJSNoise(readAsset(t, "app.js"))
	if !strings.Contains(app, "onStatusChanged: () =>") {
		t.Error("the Live session does not pass status hints to the Status view")
	}
}
