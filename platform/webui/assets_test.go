package webui

// Guards over the shipped browser source.
//
// This is where Task 063 pays for choosing embedded static assets over
// html/template. Templates would have given contextual auto-escaping for free;
// these tests are the replacement, and ADR 0036 says plainly that they are
// load-bearing rather than decorative. If they are weakened the protection is
// gone and nothing else notices.
//
// Every scan strips comments and string literals first. The assets deliberately
// *name* the primitives they must not use, in prose explaining why — so a naive
// substring match would fire on the explanation, and the fix for that is
// stripping, never loosening the pattern.

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// readAsset returns one shipped asset's source.
func readAsset(t *testing.T, name string) string {
	t.Helper()
	body, err := assetFS.ReadFile("assets/" + name)
	if err != nil {
		t.Fatalf("read embedded asset %s: %v", name, err)
	}
	return string(body)
}

func scriptAssets(t *testing.T) map[string]string {
	t.Helper()
	sources := make(map[string]string)
	for _, name := range assetNames() {
		if strings.HasSuffix(name, ".js") {
			sources[name] = readAsset(t, name)
		}
	}
	if len(sources) == 0 {
		t.Fatal("no JavaScript assets found; these guards would pass vacuously")
	}
	return sources
}

// stripJSNoise removes comments and string/template literals.
//
// A small hand-rolled scanner rather than a dependency: the shipped source is
// plain ES modules, and adding a JavaScript parser to run a Go test would be
// the toolchain ADR 0036 declines. Ranges are blanked rather than deleted so
// line numbers survive for error messages.
//
// Regex is not used for this on purpose — a pattern cannot tell a "//" inside a
// string from the start of a comment, and getting that wrong in either
// direction breaks the guard.
func stripJSNoise(source string) string {
	out := []byte(source)
	blank := func(start, end int) {
		for i := start; i < end && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}

	i := 0
	for i < len(source) {
		switch {
		case strings.HasPrefix(source[i:], "//"):
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				blank(i, len(source))
				return string(out)
			}
			blank(i, i+end)
			i += end
		case strings.HasPrefix(source[i:], "/*"):
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				blank(i, len(source))
				return string(out)
			}
			blank(i, i+2+end+2)
			i += 2 + end + 2
		case source[i] == '"' || source[i] == '\'' || source[i] == '`':
			quote := source[i]
			j := i + 1
			for j < len(source) {
				if source[j] == '\\' {
					j += 2
					continue
				}
				if source[j] == quote {
					break
				}
				j++
			}
			blank(i+1, min(j, len(source)))
			i = min(j+1, len(source))
		default:
			i++
		}
	}
	return string(out)
}

// stripJSComments removes comments but keeps string literals.
//
// The companion to stripJSNoise, and the distinction matters. Scans for a
// forbidden primitive strip strings too, so a literal can never be mistaken for
// code. Scans for *structure* — the order of an addEventListener("stream_ready")
// against an EventSource construction — need the literals intact, because the
// event name is the thing being located.
func stripJSComments(source string) string {
	out := []byte(source)
	blank := func(start, end int) {
		for i := start; i < end && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}

	i := 0
	for i < len(source) {
		switch {
		case strings.HasPrefix(source[i:], "//"):
			end := strings.IndexByte(source[i:], '\n')
			if end < 0 {
				blank(i, len(source))
				return string(out)
			}
			blank(i, i+end)
			i += end
		case strings.HasPrefix(source[i:], "/*"):
			end := strings.Index(source[i+2:], "*/")
			if end < 0 {
				blank(i, len(source))
				return string(out)
			}
			blank(i, i+2+end+2)
			i += 2 + end + 2
		case source[i] == '"' || source[i] == '\'' || source[i] == '`':
			// Skip the literal without blanking it.
			quote := source[i]
			j := i + 1
			for j < len(source) {
				if source[j] == '\\' {
					j += 2
					continue
				}
				if source[j] == quote {
					break
				}
				j++
			}
			i = min(j+1, len(source))
		default:
			i++
		}
	}
	return string(out)
}

// TestShippedScriptsUseNoDangerousRenderingPrimitive is the XSS guard.
//
// Every entry here is a way to turn a server-supplied string into executable
// DOM or executable code. The renderer uses textContent and createElement
// instead, and this is what keeps it that way — including for whoever adds a
// view next, who will not have read ADR 0036.
func TestShippedScriptsUseNoDangerousRenderingPrimitive(t *testing.T) {
	forbidden := []string{
		".innerHTML",
		".outerHTML",
		"insertAdjacentHTML",
		"document.write",
		"eval(",
		"new Function",
		"setHTML",
		"createContextualFragment",
		// javascript: and data: URLs as sinks, and the two attribute setters
		// that would accept one.
		"javascript:",
		"srcdoc",
		"dangerouslySetInnerHTML",
	}

	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, bad := range forbidden {
			if index := strings.Index(stripped, bad); index >= 0 {
				line := strings.Count(stripped[:index], "\n") + 1
				t.Errorf("%s:%d uses %q outside a comment. Server values reach the "+
					"DOM through textContent only — see ADR 0036 §6", name, line, bad)
			}
		}
	}
}

// TestShippedScriptsDoNotEnumerateServerObjects is the privacy guard.
//
// The mechanism the spec calls for: render an allowlist, never whatever arrived.
// Tasks 050 and 059 keep prompts, completions, tool arguments,
// Event.Attributes, PolicyReason, contributors and the raw record out of what
// the platform exposes, and /v1 may add fields at any time. A generic walk would
// put the next one on screen without anyone deciding to — and deciding is what
// the boundary consists of.
func TestShippedScriptsDoNotEnumerateServerObjects(t *testing.T) {
	forbidden := []string{
		"Object.entries",
		"Object.keys",
		"Object.values",
		"Object.getOwnPropertyNames",
		"JSON.stringify(response",
		"for (const key in ",
		"for (let key in ",
		"for (var key in ",
	}

	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, bad := range forbidden {
			if index := strings.Index(stripped, bad); index >= 0 {
				line := strings.Count(stripped[:index], "\n") + 1
				t.Errorf("%s:%d uses %q. Authoritative display iterates an allowlist, "+
					"never a server object — see ADR 0036 §7", name, line, bad)
			}
		}
	}
}

// TestShippedAssetsReferenceNoExternalOrigin keeps the CSP honest.
//
// The policy forbids external origins. An asset that referenced one would
// produce a page that is silently broken rather than obviously wrong, and the
// zero-dependency claim would stop being true.
func TestShippedAssetsReferenceNoExternalOrigin(t *testing.T) {
	// Matches an absolute or protocol-relative URL in a position that would
	// load something. A bare "//" is excluded by the comment stripping above.
	external := regexp.MustCompile(`(?i)(https?:)?//[a-z0-9]`)

	for _, name := range assetNames() {
		source := readAsset(t, name)
		var stripped string
		switch {
		case strings.HasSuffix(name, ".js"):
			stripped = stripJSNoise(source)
		case strings.HasSuffix(name, ".css"):
			// CSS has no // comments; /* */ only, and no string sinks worth
			// stripping for this check.
			stripped = regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(source, "")
		case strings.HasSuffix(name, ".html"):
			stripped = regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(source, "")
		default:
			stripped = source
		}

		if match := external.FindString(stripped); match != "" {
			t.Errorf("%s references an external origin (%q). Task 063 ships no CDN "+
				"script, stylesheet or font — see ADR 0036 §4", name, match)
		}
		for _, bad := range []string{"@import url(", "cdnjs", "unpkg", "jsdelivr", "fonts.googleapis"} {
			if strings.Contains(strings.ToLower(stripped), bad) {
				t.Errorf("%s references %q", name, bad)
			}
		}
	}
}

// TestShellCarriesNoInlineScriptOrStyle is what makes the CSP achievable.
//
// Without this, script-src 'self' would have to become 'unsafe-inline', and the
// policy would stop meaning what it appears to mean.
func TestShellCarriesNoInlineScriptOrStyle(t *testing.T) {
	shell := readAsset(t, "index.html")
	stripped := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(shell, "")

	// A <script> with no src attribute carries a body.
	scripts := regexp.MustCompile(`(?is)<script([^>]*)>`).FindAllStringSubmatch(stripped, -1)
	if len(scripts) == 0 {
		t.Fatal("the shell loads no script at all")
	}
	for _, script := range scripts {
		if !strings.Contains(strings.ToLower(script[1]), "src=") {
			t.Errorf("inline <script%s> in the shell; CSP forbids it", script[1])
		}
	}
	if regexp.MustCompile(`(?is)<style[\s>]`).MatchString(stripped) {
		t.Error("inline <style> in the shell; CSP forbids it")
	}
	// Inline event handlers are the other 'unsafe-inline' requirement.
	if match := regexp.MustCompile(`(?i)\son(click|load|error|submit|change|input|focus)\s*=`).
		FindString(stripped); match != "" {
		t.Errorf("inline event handler %q in the shell; CSP forbids it", strings.TrimSpace(match))
	}
}

// ---------------------------------------------------------------------
// Field allowlists
// ---------------------------------------------------------------------

// allowlistPattern reads the exported literal arrays in render.js.
var allowlistPattern = regexp.MustCompile(
	`(?s)export const ([A-Z][A-Z0-9_]*) = Object\.freeze\(\[(.*?)\]\)`)

// renderAllowlists returns every declared field allowlist.
func renderAllowlists(t *testing.T) map[string][]string {
	t.Helper()

	source := readAsset(t, "render.js")
	matches := allowlistPattern.FindAllStringSubmatch(source, -1)
	if len(matches) == 0 {
		t.Fatal("no field allowlists found in render.js; the privacy guard would " +
			"pass vacuously. Keep the literal Object.freeze([...]) shape — this test " +
			"reads it, and a computed list would defeat it.")
	}

	stringLiteral := regexp.MustCompile(`"([^"]*)"`)
	lists := make(map[string][]string)
	for _, match := range matches {
		var fields []string
		for _, literal := range stringLiteral.FindAllStringSubmatch(match[2], -1) {
			fields = append(fields, literal[1])
		}
		lists[match[1]] = fields
	}
	return lists
}

// TestFieldAllowlistsExcludeEverySensitiveField is the explicit-rendering
// privacy proof.
//
// Combined with TestShippedScriptsDoNotEnumerateServerObjects, this gives the
// property the spec asks for: the renderer reaches only allowlisted keys, and no
// allowlisted key is one of these. A field the API adds is unreachable because
// nothing names it.
func TestFieldAllowlistsExcludeEverySensitiveField(t *testing.T) {
	// Deliberately absent from task 059's realtime projection and task 050's
	// record boundary, plus the additive-field marker the spec names.
	sensitive := []string{
		"prompt",
		"completion",
		"attributes",
		"tool_argument",
		"tool_arguments",
		"policy_reason",
		"contributors",
		"record",
		"raw_record",
		"decision_record",
		"future_field",
		"event",
		"payload",
	}

	lists := renderAllowlists(t)
	for listName, fields := range lists {
		for _, field := range fields {
			for _, bad := range sensitive {
				if field == bad {
					t.Errorf("%s allowlists %q, which the platform deliberately does "+
						"not expose", listName, field)
				}
			}
		}
	}
}

// TestPrivacyFixtureFieldsAreUnreachable drives the spec's named fixture.
//
// The payload below carries the three marker values from the specification. The
// renderer can only reach a field some allowlist names, so proving no allowlist
// names these keys proves the markers cannot be displayed — without adding a
// JavaScript runtime to the toolchain.
func TestPrivacyFixtureFieldsAreUnreachable(t *testing.T) {
	// Field name → the secret it would leak. Mirrors the spec's fixture.
	fixture := map[string]string{
		"prompt":        "SECRET_PROMPT_DO_NOT_RENDER",
		"attributes":    "SECRET_TOOL_ARGUMENT",
		"tool_argument": "SECRET_TOOL_ARGUMENT",
		"future_field":  "SECRET_ADDITIVE_FIELD",
	}

	lists := renderAllowlists(t)
	var allowed []string
	for _, fields := range lists {
		allowed = append(allowed, fields...)
	}

	for field, secret := range fixture {
		for _, name := range allowed {
			if name == field {
				t.Errorf("field %q is allowlisted, so %s would reach the DOM", field, secret)
			}
		}
	}

	// And the markers must not be hard-coded anywhere in the shipped source,
	// which would make the test above true for the wrong reason.
	for _, name := range assetNames() {
		source := readAsset(t, name)
		for _, secret := range fixture {
			if strings.Contains(source, secret) {
				t.Errorf("%s contains the fixture marker %s", name, secret)
			}
		}
	}
}

// TestObservationAllowlistMatchesTheRealtimeProjection pins the live view to
// task 059's contract exactly.
//
// Both directions. A missing field means the view silently stops showing
// something the contract provides; an extra one means it shows something the
// contract does not, which is how a privacy boundary erodes one field at a time.
func TestObservationAllowlistMatchesTheRealtimeProjection(t *testing.T) {
	want := map[string]bool{
		"sequence": true, "record_count": true, "behavior_complete": true,
		"decision": true, "risk_level": true, "approval_status": true,
		"trust_score": true, "anomaly_score": true, "anomaly_confidence": true,
		"new_behavior": true,
	}

	lists := renderAllowlists(t)
	got, ok := lists["OBSERVATION_FIELDS"]
	if !ok {
		t.Fatal("OBSERVATION_FIELDS is not declared in render.js")
	}

	seen := make(map[string]bool, len(got))
	for _, field := range got {
		seen[field] = true
		if !want[field] {
			t.Errorf("OBSERVATION_FIELDS includes %q, which is not in task 059's "+
				"realtime projection", field)
		}
	}
	for field := range want {
		if !seen[field] {
			t.Errorf("OBSERVATION_FIELDS is missing %q from the realtime projection", field)
		}
	}
}

// ---------------------------------------------------------------------
// Client bounds
// ---------------------------------------------------------------------

// numericConstant reads an exported const whose value is an arithmetic literal.
func numericConstant(t *testing.T, source, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`export const ` + regexp.QuoteMeta(name) + ` = ([^;]+);`)
	match := pattern.FindStringSubmatch(source)
	if match == nil {
		t.Fatalf("%s is not declared as an exported const", name)
	}
	return strings.TrimSpace(match[1])
}

// TestBrowserClientBoundsMatchTheServerAndCLI pins every application-owned
// limit.
//
// These are not new policy. Each restates a number the server or the CLI
// already established, on the one client that cannot inherit it from Go — so a
// mutation that removes one has to remove it here too, where a test is looking.
func TestBrowserClientBoundsMatchTheServerAndCLI(t *testing.T) {
	api := readAsset(t, "api.js")
	realtime := readAsset(t, "realtime.js")

	tests := []struct {
		source string
		name   string
		want   string
		why    string
	}{
		{api, "REQUEST_MAX_BYTES", "256 * 1024", "the server's maxAPIRequestBody"},
		{api, "RESPONSE_MAX_BYTES", "4 * 1024 * 1024", "the CLI's maxPlatformResponseBody"},
		{api, "REQUEST_TIMEOUT_MS", "30000", "the CLI's platformRequestTimeout"},
		{realtime, "PENDING_MAX", "64", "the server's per-subscriber queue"},
		{realtime, "DISPLAY_MAX", "100", "the TUI's observation window"},
		{realtime, "HANDSHAKE_TIMEOUT_MS", "30000", "a finite handshake deadline"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := numericConstant(t, tt.source, tt.name); got != tt.want {
				t.Errorf("%s = %s, want %s (%s)", tt.name, got, tt.want, tt.why)
			}
		})
	}

	// The response bound must be enforced while reading, not measured after.
	// response.text() on an unbounded body makes the client's memory safety a
	// property of the server behaving well.
	if !strings.Contains(api, "getReader()") {
		t.Error("api.js does not stream the response body; the size bound would " +
			"be applied after the memory was already spent")
	}
	if strings.Contains(stripJSNoise(api), "response.text()") {
		t.Error("api.js calls response.text(), which reads an unbounded body")
	}
	if !strings.Contains(api, "reader.cancel()") {
		t.Error("api.js never cancels the reader; an over-limit body would be drained")
	}
}

// TestReconnectBackoffIsBoundedAndMatchesTheTUI pins the sequence and the cap.
func TestReconnectBackoffIsBoundedAndMatchesTheTUI(t *testing.T) {
	realtime := readAsset(t, "realtime.js")
	match := regexp.MustCompile(`export const BACKOFF_MS = Object\.freeze\(\[([^\]]*)\]\)`).
		FindStringSubmatch(realtime)
	if match == nil {
		t.Fatal("BACKOFF_MS is not declared as a frozen literal array")
	}
	got := strings.Join(strings.Fields(strings.ReplaceAll(match[1], ",", " ")), ",")
	const want = "250,500,1000,2000,4000,5000"
	if got != want {
		t.Errorf("BACKOFF_MS = [%s], want [%s]", got, want)
	}
}

// ---------------------------------------------------------------------
// Realtime contract
// ---------------------------------------------------------------------

// TestRealtimeSubscribesBeforeResynchronizing is the ordering contract.
//
// Fetching state and then subscribing loses anything committed between the two.
// The order is a property of the design, and asserting it structurally is how it
// survives an edit by someone who has not read ADR 0032.
func TestRealtimeSubscribesBeforeResynchronizing(t *testing.T) {
	// Comments-only: the event name in the listener is exactly what is being
	// located, so string literals must survive.
	source := stripJSComments(readAsset(t, "realtime.js"))

	subscribe := strings.Index(source, "new EventSource(")
	if subscribe < 0 {
		t.Fatal("realtime.js never opens an EventSource")
	}
	// The authoritative read happens in resync, which the stream_ready handler
	// calls — so the subscription must be established first in the source's own
	// control flow.
	readyHandler := strings.Index(source, `addEventListener("stream_ready"`)
	if readyHandler < 0 {
		t.Fatal("realtime.js does not listen for stream_ready")
	}
	if readyHandler < subscribe {
		t.Error("the stream_ready handler is registered before the EventSource exists")
	}

	firstResync := strings.Index(source, "this.resync(")
	if firstResync < 0 {
		t.Fatal("realtime.js never resynchronizes")
	}
	if firstResync < subscribe {
		t.Error("an authoritative read is issued before the stream is opened; " +
			"anything committed in between would be lost")
	}
}

// TestStreamReadyValidationRequiresAllThreeFields keeps the handshake strict.
//
// replay_available is the one that matters most: the bus retains no history, so
// a server claiming replay would be describing a capability that does not exist,
// and believing it would mean skipping the resync that makes the view correct.
func TestStreamReadyValidationRequiresAllThreeFields(t *testing.T) {
	source := stripJSNoise(readAsset(t, "realtime.js"))

	for _, required := range []string{
		`version === `,
		`replay_available === false`,
		`resync_required === true`,
	} {
		if !strings.Contains(source, required) {
			t.Errorf("validateStreamReady does not check %q", required)
		}
	}
}

// TestRealtimeUsesNoPollingReplayOrWebSocket pins what the transport must not
// become.
func TestRealtimeUsesNoPollingReplayOrWebSocket(t *testing.T) {
	forbidden := map[string]string{
		"setInterval":      "periodic polling",
		"Last-Event-ID":    "a replay cursor",
		"lastEventId":      "a replay cursor",
		"WebSocket":        "a second transport",
		"EventSource.OPEN": "reliance on the browser's own retry state",
	}

	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for token, why := range forbidden {
			if strings.Contains(stripped, token) {
				t.Errorf("%s uses %q (%s)", name, token, why)
			}
		}
	}
}

// TestTerminalEventsTriggerOneFinalAuthoritativeRead covers the bug task 061
// already found once.
//
// A terminal lifecycle event arriving while the initial resync is in flight must
// still get its closing read when replayed, or a completed run displays stale
// counts forever.
func TestTerminalEventsTriggerOneFinalAuthoritativeRead(t *testing.T) {
	source := stripJSNoise(readAsset(t, "realtime.js"))

	if !strings.Contains(source, "isTerminalKind(kind)") {
		t.Error("nothing distinguishes a terminal lifecycle event")
	}
	// The deferred-final-read mechanism: a terminal event during a resync is
	// remembered rather than dropped.
	if !strings.Contains(source, "finalResyncPending") {
		t.Error("a terminal event arriving during a resync has no way to schedule " +
			"the final authoritative read; this is the task 061 bug")
	}
	if !strings.Contains(source, "this.finalResyncPending = this.finalResyncPending || final") {
		t.Error("the deferred final-read flag is not latched, so a terminal event " +
			"during a resync could be forgotten")
	}
}

// TestHandshakeDeadlineIsReleasedOnlyByAValidStreamReady is the distinction
// heartbeats make necessary.
//
// Left armed past synchronization it would tear down a healthy stream; refreshed
// by heartbeats it would never fire at all. Both are mutations the spec names.
func TestHandshakeDeadlineIsReleasedOnlyByAValidStreamReady(t *testing.T) {
	source := stripJSComments(readAsset(t, "realtime.js"))

	ready := strings.Index(source, `addEventListener("stream_ready"`)
	if ready < 0 {
		t.Fatal("realtime.js does not listen for stream_ready")
	}
	handler := source[ready:]
	clearInReady := strings.Index(handler, "this.clearHandshakeTimer()")
	if clearInReady < 0 {
		t.Fatal("the handshake timer is not cleared by the stream_ready handler")
	}
	// It must be cleared *after* validation, not on arrival of any frame.
	validate := strings.Index(handler, "validateStreamReady(payload)")
	if validate < 0 || validate > clearInReady {
		t.Error("the handshake timer is cleared before stream_ready is validated, " +
			"so an invalid handshake would disarm the deadline")
	}

	// No other listener may touch it: a heartbeat or arbitrary byte must not
	// extend the deadline.
	occurrences := strings.Count(source, "this.clearHandshakeTimer()")
	if occurrences > 4 {
		t.Errorf("clearHandshakeTimer() is called %d times; every call site must be "+
			"a teardown path or the validated handshake, never an activity signal",
			occurrences)
	}
}

// TestGenerationTokenGuardsEveryAsyncContinuation is the stale-callback rule.
//
// The browser has no way to cancel an in-flight promise, so a late response from
// generation N must be discarded by checking the token rather than prevented.
// Every async continuation needs the check; one missing is one path where an old
// stream can mutate the current view.
func TestGenerationTokenGuardsEveryAsyncContinuation(t *testing.T) {
	source := stripJSNoise(readAsset(t, "realtime.js"))

	const guard = "generation !== this.generation"

	// Adjacency, not proximity and not a total.
	//
	// Both weaker forms were tried and both let a real defect through. A count
	// stays satisfied when one guard of several is deleted. A "guard somewhere in
	// the next N characters" window matches the guard belonging to the *next*
	// read, so deleting the first one still passes. What has to hold is that the
	// check is the statement immediately after the read — nothing may happen in
	// between, because "in between" is where a stale response gets used.
	readThenGuard := regexp.MustCompile(
		`await this\.deps\.\w+\([^)]*\);\s*\n\s*if \(generation !== this\.generation\)`)
	reads := regexp.MustCompile(`await this\.deps\.\w+\(`)

	total := len(reads.FindAllString(source, -1))
	guarded := len(readThenGuard.FindAllString(source, -1))
	if total == 0 {
		t.Fatal("no awaited external reads found; this guard would pass vacuously")
	}
	if guarded != total {
		t.Errorf("realtime.js has %d awaited external reads but only %d are followed "+
			"immediately by a generation check. A response that lands after the "+
			"stream was replaced would mutate the current view", total, guarded)
	}

	// Every listener registered on the stream must check too, since each fires
	// from a socket the session may already have abandoned.
	listeners := strings.Count(source, "addEventListener(")
	guards := strings.Count(source, guard)
	if guards < listeners {
		t.Errorf("%d listeners but only %d generation checks in realtime.js",
			listeners, guards)
	}
}

// TestNoBrowserPersistenceOfPlatformState keeps the database authoritative.
func TestNoBrowserPersistenceOfPlatformState(t *testing.T) {
	forbidden := []string{"localStorage", "sessionStorage", "indexedDB", "document.cookie"}

	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, bad := range forbidden {
			if strings.Contains(stripped, bad) {
				t.Errorf("%s uses %s; browser state is presentation state only and "+
					"the control-plane database stays authoritative", name, bad)
			}
		}
	}
}

// ---------------------------------------------------------------------
// Gate ownership and uint64 handling
// ---------------------------------------------------------------------

// TestGateVerdictComesFromTheServer is the mutation the spec calls out by name.
//
// The browser may format evidence. It may not decide a verdict, and the shape of
// deciding one is arithmetic comparing an actual against a limit.
func TestGateVerdictComesFromTheServer(t *testing.T) {
	render := stripJSNoise(readAsset(t, "render.js"))

	if !strings.Contains(render, "gate.verdict") {
		t.Fatal("render.js never reads gate.verdict; the displayed verdict must be " +
			"the server's")
	}

	// Comparisons between an actual and a bound are the recomputation shape.
	recompute := []*regexp.Regexp{
		regexp.MustCompile(`actual\s*[<>]=?\s*`),
		regexp.MustCompile(`[<>]=?\s*\w*\.?(maximum|minimum)\b`),
		regexp.MustCompile(`added_count\s*[<>]`),
		regexp.MustCompile(`\bmax_added_behaviors\b`),
		regexp.MustCompile(`\bmax_block_decisions\b`),
		regexp.MustCompile(`\bmax_critical_risk_observations\b`),
	}
	for _, source := range map[string]string{"render.js": render} {
		for _, pattern := range recompute {
			if match := pattern.FindString(source); match != "" {
				t.Errorf("render.js appears to compute a gate result (%q). Task 056 "+
					"owns that arithmetic, once", match)
			}
		}
	}
}

// TestUint64CountersAreNeverParsedAsNumbers is the one place a browser client is
// more exposed than the CLI.
//
// JavaScript has no integer type that holds a uint64, so parsing one silently
// rounds it: 18446744073709551615 becomes 18446744073709552000. The API encodes
// these as decimal strings precisely so a client can pass them through.
func TestUint64CountersAreNeverParsedAsNumbers(t *testing.T) {
	// The explicit coercions, plus unary-plus on a counter field. Deliberately
	// not a loose pattern like "* 1": that matched the byte-limit arithmetic
	// "256 * 1024" and a guard that fires on its own constants is a guard
	// somebody weakens rather than fixes.
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`\bparseInt\(`),
		regexp.MustCompile(`\bparseFloat\(`),
		regexp.MustCompile(`\bNumber\(`),
		regexp.MustCompile(`\bBigInt\(`),
		// Unary plus as numeric coercion of a uint64-bearing field.
		regexp.MustCompile(`\+\s*\w*\.?(record_count|sequence|next_sequence|` +
			`next_ingest_sequence|behavior_observation_count|actual|maximum|minimum)\b`),
	}

	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, pattern := range forbidden {
			if location := pattern.FindStringIndex(stripped); location != nil {
				line := strings.Count(stripped[:location[0]], "\n") + 1
				t.Errorf("%s:%d uses %q. uint64 counters and gate limits stay decimal "+
					"strings end to end: JavaScript has no integer type that holds "+
					"18446744073709551615", name, line, stripped[location[0]:location[1]])
			}
		}
	}

	// And the limits must be validated as text.
	api := readAsset(t, "api.js")
	if !strings.Contains(api, "isCanonicalUint64") {
		t.Error("api.js has no canonical-decimal validator for gate limits")
	}
	if !strings.Contains(api, "18446744073709551615") {
		t.Error("api.js does not bound gate limits at the uint64 maximum")
	}
}

// TestOperationalFailureIsNotAGateResult keeps the two kinds of answer apart.
//
// The browser has no exit codes, but the separation is the same one task 060
// encoded as exit 3 versus exit 1: a network error must never read as a rejected
// candidate.
func TestOperationalFailureIsNotAGateResult(t *testing.T) {
	api := readAsset(t, "api.js")
	if !strings.Contains(api, "operational") {
		t.Fatal("api.js does not distinguish an operational failure from a server " +
			"refusal")
	}
	app := stripJSNoise(readAsset(t, "app.js"))
	if !strings.Contains(app, "error.operational") {
		t.Error("app.js never consults the operational flag, so a transport failure " +
			"and a refusal would be presented identically")
	}
}

// TestNoCollectionRouteIsCalled proves the browser invents no list API.
//
// The spec's deliberate omission: adding a list route would freeze scope, sort
// order, cursor and limit under a STABLE contract. A client calling one would
// mean the route exists.
func TestOnlyDesignedCollectionRoutesAreCalled(t *testing.T) {
	raw := readAsset(t, "api.js")

	// Task 063 shipped with no collection route at all, because pagination,
	// ordering, cursor semantics and scoping were undesigned. Task 065
	// designed them for environments, task 066 reused them unchanged for
	// promotions, and task 074 extended the same contract to the control
	// hierarchy once a consumer existed — a browser that reloads with no live
	// traffic and must still find what is there. The rule is therefore "only
	// the designed set", and the forbidden list below is what keeps that from
	// meaning "anything".
	allowed := []string{
		"/v1/projects/${segment(projectID)}/promotions",
		"/v1/projects/${segment(projectID)}/environments",
		"/v1/projects${query}",
		"/v1/projects/${segment(projectID)}/agents${query}",
		"/v1/agents/${segment(agentID)}/candidates${query}",
		"/v1/candidates/${segment(candidateID)}/evaluation-runs${query}",
	}
	for _, route := range allowed {
		if !strings.Contains(raw, route) {
			t.Errorf("api.js no longer calls %s; this check would pass vacuously", route)
		}
	}

	// The unscoped, undesigned shapes stay forbidden. Each would freeze
	// semantics nobody has specified.
	// The unscoped listings that were never designed. `GET /v1/projects` has
	// left this list because task 074 designed it — it is the hierarchy's one
	// entry point, and without it there is no way in that does not require
	// already knowing an identifier. The other three have not: each would be
	// an unscoped listing of an entity whose natural scope is its parent.
	forbidden := []regexp.Regexp{
		*regexp.MustCompile("\"GET\",\\s*[\"`]/v1/agents[\"`]"),
		*regexp.MustCompile("\"GET\",\\s*[\"`]/v1/candidates[\"`]"),
		*regexp.MustCompile("\"GET\",\\s*[\"`]/v1/evaluation-runs[\"`]"),
		*regexp.MustCompile("\"GET\",\\s*[\"`]/v1/promotions[\"`]"),
		*regexp.MustCompile(`[?&](cursor|offset|page|sort|order_by)=`),
		// Nothing task 074 defines, and each would be a route shape this API
		// deliberately does not publish.
		*regexp.MustCompile(`/v1/(everything|hierarchy|active-agents|all-runs)`),
	}
	for _, pattern := range forbidden {
		if match := pattern.FindString(raw); match != "" {
			t.Errorf("api.js calls an undesigned collection route or parameter (%q); "+
				"only the project-scoped environment and promotion pages are specified",
				match)
		}
	}

	// Ingest is producer surface and deliberately unused by the browser.
	for _, route := range []string{"/records", "ingest-state"} {
		if strings.Contains(raw, route) {
			t.Errorf("api.js references %q; ingest is producer behaviour, not a "+
				"control-plane management workflow", route)
		}
	}
}

// The browser renders a promotion; it decides nothing about one.
//
// Three different things it must not do, each of which would move a decision
// out of the control plane: compare two environment ranks, compute a gate, or
// derive an outcome from anything but the server's own field.
func TestPromotionUIDecidesNothing(t *testing.T) {
	render := stripJSNoise(readAsset(t, "render.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	// It reads the server's outcome rather than deriving one.
	if !strings.Contains(render, "promotion.outcome") {
		t.Fatal("render.js never reads promotion.outcome; the displayed decision must " +
			"be the server's")
	}

	// No rank comparison anywhere. CanPromote is the only implementation of
	// promotion precedence and it lives in the platform.
	rankComparison := []*regexp.Regexp{
		regexp.MustCompile(`\.rank\s*[<>]`),
		regexp.MustCompile(`[<>]=?\s*\w*\.?rank\b`),
		regexp.MustCompile(`rank\s*[<>]=?\s*\w*\.?rank`),
	}
	for name, source := range map[string]string{"render.js": render, "app.js": app} {
		for _, pattern := range rankComparison {
			if match := pattern.FindString(source); match != "" {
				t.Errorf("%s compares environment ranks (%q); CanPromote is the only "+
					"implementation of promotion precedence, and it is server-side",
					name, match)
			}
		}
	}

	// And it never derives an outcome from a verdict itself.
	derivation := []*regexp.Regexp{
		regexp.MustCompile(`verdict\s*===?\s*"pass"\s*\?\s*"accepted"`),
		regexp.MustCompile(`accepted\s*=\s*.*verdict`),
	}
	for name, source := range map[string]string{"render.js": render, "app.js": app} {
		for _, pattern := range derivation {
			if match := pattern.FindString(source); match != "" {
				t.Errorf("%s derives an outcome from a verdict (%q); the server does that",
					name, match)
			}
		}
	}
}

// The wording stays factual: a recorded decision, never a deployment.
func TestPromotionUIClaimsNoDeployment(t *testing.T) {
	shell := readAsset(t, "index.html")
	render := readAsset(t, "render.js")

	// The one sentence that keeps the product honest, in both surfaces.
	if !strings.Contains(shell, "deploys nothing") {
		t.Error("index.html does not say that Trustvian deploys nothing")
	}
	if !strings.Contains(render, "Nothing was deployed") {
		t.Error("render.js does not say that nothing was deployed")
	}

	// Phrases that would claim something the platform cannot know.
	//
	// Deployment claims only, and deliberately not safety words.
	//
	// "safe", "unsafe" and "ready" all appear legitimately in the disclaimers
	// that say a gate result is *not* a judgement about any of them — banning
	// those words would ban the sentences doing the work. Gate wording is
	// already pinned by TestGateVerdictComesFromTheServer; what this checks is
	// the claim Trustvian has no way to make at all.
	forbidden := []string{
		"deployed to", "now running in", "is live in", "released to",
		"has been deployed", "was deployed to", "rolled out",
	}
	for name, source := range map[string]string{"index.html": shell, "render.js": render} {
		lower := strings.ToLower(source)
		for _, phrase := range forbidden {
			if strings.Contains(lower, phrase) {
				t.Errorf("%s contains %q; a promotion is a recorded decision, and "+
					"Trustvian observes no deployment", name, phrase)
			}
		}
	}
}

// TestRequestDeadlineCoversBodyConsumption is the regression guard for a
// review blocker, and the ordering it pins is the entire point.
//
// fetch() resolves as soon as response headers arrive; the body may still be
// streaming or stalled. An earlier version released the deadline right there:
//
//	try { response = await fetch(...) } finally { clearTimeout(timer) }
//	const text = await readBounded(response)   // ← unbounded
//
// A server that returned headers promptly and then stopped sending bytes would
// hang the operation forever. The 4 MiB bound cannot catch it, because a stalled
// body never reaches any size limit — the two protections answer different
// failure modes and both have to hold.
//
// Extending the controller over the read is a real bound rather than a hopeful
// one: per the Fetch standard, aborting after headers have arrived errors the
// response body stream, so a pending read rejects with AbortError instead of
// staying blocked.
//
// This asserts source *order*, which is what a deterministic guard can prove
// without a browser: the timer must be cleared only after the body read, from a
// finally that encloses both. Task 063's testing strategy forbids adding Node,
// a headless browser or any frontend tooling, and an executable timing test
// would have to wait the real 30 seconds to observe the defect.
func TestRequestDeadlineCoversBodyConsumption(t *testing.T) {
	// Comments stripped, string literals kept: the comment above request()
	// describes the rejected shape, and that description must not satisfy the
	// check that the shape is absent.
	source := stripJSComments(readAsset(t, "api.js"))

	// One timer, cleared in exactly one place. Two clears would mean two
	// lifetimes to reason about, and the early one would win.
	if got := strings.Count(source, "clearTimeout("); got != 1 {
		t.Fatalf("api.js clears the deadline %d times; there must be exactly one "+
			"clearTimeout, in the finally that encloses the whole operation", got)
	}
	if got := strings.Count(source, "setTimeout("); got != 1 {
		t.Fatalf("api.js arms %d timers; the operation has one deadline", got)
	}

	requestStart := strings.Index(source, "async function request(")
	if requestStart < 0 {
		t.Fatal("request() was not found; this guard would pass vacuously")
	}
	body := source[requestStart:]

	index := func(needle string) int {
		at := strings.Index(body, needle)
		if at < 0 {
			t.Fatalf("request() does not contain %q", needle)
		}
		return at
	}

	arm := index("setTimeout(")
	fetchCall := index("await fetch(")
	readCall := index("await readBounded(")
	clear := index("clearTimeout(")

	// start deadline → fetch → bounded body read → clear deadline
	if !(arm < fetchCall && fetchCall < readCall && readCall < clear) {
		t.Errorf("the deadline lifecycle is out of order.\n"+
			"  got:  setTimeout@%d  fetch@%d  readBounded@%d  clearTimeout@%d\n"+
			"  want: setTimeout < fetch < readBounded < clearTimeout",
			arm, fetchCall, readCall, clear)
	}

	// The specific regression: nothing may clear the deadline between the fetch
	// and the body read. This is the assertion that fails if someone restores
	// `response = await fetch(...); clearTimeout(timer); await readBounded(...)`.
	between := body[fetchCall:readCall]
	if strings.Contains(between, "clearTimeout(") {
		t.Error("the deadline is cleared between fetch() and the body read, so a " +
			"stalled response body would be unbounded. Clear it in the finally " +
			"that encloses both.")
	}

	// And the clear must be reached from a finally, so every exit path — success,
	// HTTP refusal, malformed JSON, size overflow, transport failure, timeout —
	// releases the timer.
	finallyAt := strings.Index(body, "} finally {")
	if finallyAt < 0 || finallyAt > clear || finallyAt < readCall {
		t.Error("clearTimeout is not inside a finally that encloses the body read; " +
			"some failure path would leave the timer armed")
	}
}

// TestAbortDuringBodyReadIsReportedAsATimeout keeps the classification right.
//
// When the deadline fires mid-transfer the body read rejects with an AbortError
// DOMException. That must surface as the existing operational timeout — not as
// malformed JSON, not as an HTTP refusal, not as a size overflow, and never as
// a gate result. A raw AbortError must not reach the UI either.
func TestAbortDuringBodyReadIsReportedAsATimeout(t *testing.T) {
	source := stripJSComments(readAsset(t, "api.js"))

	// The body read is guarded at all.
	readCall := strings.Index(source, "await readBounded(")
	if readCall < 0 {
		t.Fatal("readBounded is not called")
	}
	guarded := strings.LastIndex(source[:readCall], "try {")
	if guarded < 0 {
		t.Fatal("the body read is not inside a try; a stream failure would leak a " +
			"raw DOMException to the UI")
	}

	// AbortError is recognised and converted, in one place.
	if !strings.Contains(source, `name === "AbortError"`) {
		t.Error("nothing recognises an AbortError, so an aborted read could not be " +
			"reported as a timeout")
	}
	if !strings.Contains(source, "operationalFrom(") {
		t.Error("no shared conversion from a transport failure to an ApiError")
	}
	// Both the fetch and the read failure paths must go through it, or one of
	// them reports the wrong kind of error.
	if got := strings.Count(source, "operationalFrom(cause, deadlineExpired)"); got < 2 {
		t.Errorf("operationalFrom is applied %d times; both the fetch failure and "+
			"the body-read failure must convert through it", got)
	}
	// The timeout wording is preserved.
	if !strings.Contains(source, "`no response within ${REQUEST_TIMEOUT_MS}ms`") {
		t.Error("the timeout message changed; the existing contract says " +
			"\"no response within <ms>ms\"")
	}

	// An over-limit body must keep its own meaning rather than being rewritten
	// as a timeout by the catch that handles aborts.
	if !strings.Contains(source, "cause instanceof ApiError") {
		t.Error("the body-read catch does not pass an ApiError through, so a size " +
			"overflow would be reported as a timeout")
	}
}

// TestResponseSizeBoundStillStopsTheTransfer guards the other half.
//
// The deadline and the size bound protect against different failure modes — a
// stalled body and an oversized one — and fixing the first must not have
// weakened the second.
func TestResponseSizeBoundStillStopsTheTransfer(t *testing.T) {
	source := readAsset(t, "api.js")

	if got := numericConstant(t, source, "RESPONSE_MAX_BYTES"); got != "4 * 1024 * 1024" {
		t.Errorf("RESPONSE_MAX_BYTES = %s, want 4 * 1024 * 1024", got)
	}

	stripped := stripJSComments(source)
	// Still enforced while reading, and still cancels rather than draining.
	if !strings.Contains(stripped, "total > RESPONSE_MAX_BYTES") {
		t.Error("the response bound is no longer compared against bytes read")
	}
	if !strings.Contains(stripped, "reader.cancel()") {
		t.Error("an over-limit transfer is no longer cancelled")
	}
	// cancel() must not be able to replace the overflow error with its own
	// failure: on an already-errored stream it can reject.
	//
	// Adjacency, not "inside some enclosing try". Looking for any preceding
	// `try {` matched readBounded's outer block and let an unguarded cancel
	// through — the same too-loose shape that let a generation-guard mutation
	// survive earlier in this task. The cancel has to be the whole try body.
	wrappedCancel := regexp.MustCompile(
		`try\s*\{\s*await reader\.cancel\(\);\s*\}\s*catch`)
	if !wrappedCancel.MatchString(stripped) {
		t.Error("reader.cancel() is not itself wrapped in try/catch; a rejection " +
			"there would replace the size-overflow error with the stream's own " +
			"failure, reporting the wrong reason for a refusal already decided")
	}
	if !strings.Contains(stripped, "response exceeded the ${RESPONSE_MAX_BYTES} byte limit") {
		t.Error("the overflow message changed")
	}
}

// ---------------------------------------------------------------------
// Bounded collection navigation (task 066 follow-up)
// ---------------------------------------------------------------------
//
// These are source guards rather than interaction tests, and the reason is a
// standing constraint rather than convenience: task 063's testing strategy
// forbids adding Node, a headless browser or any frontend tooling, so no test
// in this package executes JavaScript. What a deterministic guard can prove is
// that the traversal exists, that each condition which would make it
// non-terminating is checked, and that the decisions stay server-side. What it
// cannot prove is runtime behaviour — so each guard below pins a specific
// construct whose removal is the regression, not merely a word.

// TestEnvironmentTargetsTraverseEveryPage is the blocker this fixes.
//
// The target picker read one page. Task 065's cap of 64 environments per
// project governs *creation, not existence*: a database migrated from schema 2
// backfills one environment per distinct reference its runs recorded, so a
// project may legitimately hold more than 64 and a promotable target may sit on
// page 2. Offering only page one silently hides valid targets, and a person
// reading the dropdown has no way to tell a missing environment from an absent
// one.
func TestEnvironmentTargetsTraverseEveryPage(t *testing.T) {
	apiSource := stripJSNoise(readAsset(t, "api.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	if !strings.Contains(apiSource, "export async function listAllEnvironments(") {
		t.Fatal("api.js has no listAllEnvironments; the target picker would read one page")
	}

	// It must actually loop, and each iteration must carry the previous page's
	// published cursor. A single call dressed up as a traversal is the bug.
	if !regexp.MustCompile(`for\s*\(let page = 0; page < MAX_ENVIRONMENT_PAGES`).MatchString(apiSource) {
		t.Error("listAllEnvironments does not loop under the page ceiling")
	}
	if !regexp.MustCompile(`listEnvironments\(projectID,\s*after\)`).MatchString(apiSource) {
		t.Error("the traversal does not pass the cursor to listEnvironments; every " +
			"request would fetch page one")
	}
	if !strings.Contains(apiSource, "after = next") {
		t.Error("the traversal never advances its cursor")
	}

	// And the promotion form must use it rather than the single-page helper.
	if !strings.Contains(app, "api.listAllEnvironments(projectID)") {
		t.Error("app.js does not call listAllEnvironments for the target picker")
	}
	if regexp.MustCompile(`api\.listEnvironments\(`).MatchString(app) {
		t.Error("app.js still calls the single-page listEnvironments; a target on a " +
			"later page would be unselectable")
	}
}

// TestEnvironmentTraversalRefusesACursorThatCannotProgress covers the three
// server answers that are impossible from a correct server and
// non-terminating if believed.
//
// The same three the CLI's validatePromotionCursor refuses, for the same
// reason: a browser that followed any of them would loop until the tab died.
func TestEnvironmentTraversalRefusesACursorThatCannotProgress(t *testing.T) {
	apiSource := stripJSNoise(readAsset(t, "api.js"))

	checks := []struct {
		name    string
		pattern *regexp.Regexp
		why     string
	}{
		{
			"empty page carrying a cursor",
			regexp.MustCompile(`rows\.length === 0[\s\S]{0,200}?throw new ApiError`),
			"a cursor on an empty page can never advance",
		},
		{
			"cursor that does not advance",
			regexp.MustCompile(`next <= after[\s\S]{0,200}?throw new ApiError`),
			"a cursor at or behind the previous one repeats a page forever",
		},
		{
			"cursor that is not the page's last row",
			regexp.MustCompile(`last\.ref !== next[\s\S]{0,200}?throw new ApiError`),
			"a cursor unrelated to the page cannot be resumed from",
		},
	}
	for _, check := range checks {
		if !check.pattern.MatchString(apiSource) {
			t.Errorf("listAllEnvironments does not refuse a %s; %s", check.name, check.why)
		}
	}

	// The traversal must also notice the pages stopped describing one listing.
	// Literals kept here: the message is the thing being located.
	if !strings.Contains(stripJSComments(readAsset(t, "api.js")), "changed identity mid-traversal") {
		t.Error("the traversal does not check that version and project_id hold " +
			"across pages; merging two listings would present a set that never existed")
	}
}

// TestEnvironmentTraversalIsBoundedAndSaysSoWhenItStops keeps the defensive
// ceiling from becoming a silent truncation.
//
// A short list is worse than an error here. "Production is not in the
// dropdown" reads as "this project has no production", and acting on that
// belief is how somebody concludes the environment was deleted.
func TestEnvironmentTraversalIsBoundedAndSaysSoWhenItStops(t *testing.T) {
	apiSource := readAsset(t, "api.js")
	stripped := stripJSNoise(apiSource)

	if !regexp.MustCompile(`export const MAX_ENVIRONMENT_PAGES = \d+;`).MatchString(stripped) {
		t.Fatal("api.js declares no MAX_ENVIRONMENT_PAGES; an endless server would " +
			"grow the option list without limit")
	}

	// The ceiling ends the traversal with an error, never a return. A `return`
	// at the end of the loop body's scope would hand back a partial collection
	// that looks complete.
	withLiterals := stripJSComments(apiSource)
	tail := withLiterals[strings.Index(withLiterals, "export async function listAllEnvironments("):]
	if !strings.Contains(tail, "too many environment pages were returned to display safely") {
		t.Error("hitting the page ceiling does not report an error; a truncated " +
			"target list would be indistinguishable from a complete one")
	}

	// It must not raise the server's own page bound to dodge the traversal.
	if regexp.MustCompile(`[?&]limit=`).MatchString(stripped) {
		t.Error("api.js sends a limit parameter; the server's page bound is " +
			"authoritative and must not be argued with from the browser")
	}

	// The comment has to say this is a client-side defence, not a product cap,
	// so nobody later reads 64 pages as an environment limit.
	if !strings.Contains(apiSource, "governs *creation, not existence*") {
		t.Error("MAX_ENVIRONMENT_PAGES is not documented as a browser-only defence " +
			"against a non-terminating server")
	}
}

// TestPromotionHistoryPagesRatherThanAccumulating is the second blocker.
//
// The page rendered one bounded page and told the reader a cursor existed
// without offering any way to use it. The fix has to add navigation without
// adding an unbounded array: the CLI traverses to completion because a shell
// pipeline wants one document, and a browser doing the same would hold a
// project's whole audit trail in memory.
func TestPromotionHistoryPagesRatherThanAccumulating(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))
	render := stripJSNoise(readAsset(t, "render.js"))
	shell := readAsset(t, "index.html")

	// A real control, in the shell.
	for _, id := range []string{
		`id="promotion-history-nav"`,
		`id="promotion-next-page"`,
		`id="promotion-restart"`,
		`id="promotion-page-state"`,
	} {
		if !strings.Contains(shell, id) {
			t.Errorf("index.html has no %s; the reader is told a cursor exists with "+
				"no way to follow it", id)
		}
	}

	// Wired to one bounded request carrying the cursor the previous page
	// published.
	if !strings.Contains(app, `byID("promotion-next-page").addEventListener`) &&
		!strings.Contains(app, "promotionNextButton.addEventListener") {
		t.Error("app.js never wires the next-page control")
	}
	if !strings.Contains(app, "api.listPromotions(projectID, after)") {
		t.Error("app.js does not pass a cursor to listPromotions; every click would " +
			"re-read page one")
	}
	if !strings.Contains(app, "promotionCursor, promotionPage + 1") {
		t.Error("the next-page control does not advance from the stored cursor")
	}

	// Replacement, not accumulation. renderPromotionList clears its target, and
	// nothing pushes pages into a growing array.
	listBody := render[strings.Index(render, "export function renderPromotionList("):]
	if cut := strings.Index(listBody, "\nexport function "); cut > 0 {
		listBody = listBody[:cut]
	}
	if !strings.Contains(listBody, "clear(target)") {
		t.Error("renderPromotionList does not clear its target; pages would stack up " +
			"into an unbounded view")
	}
	for _, accumulator := range []string{".concat(", ".push(", "...rows,"} {
		if strings.Contains(listBody, accumulator) {
			t.Errorf("renderPromotionList uses %q; history must cost one page, however "+
				"far anyone reads", accumulator)
		}
	}

	// And history must not borrow the environment traversal.
	if strings.Contains(app, "listAllPromotions") ||
		strings.Contains(stripJSNoise(readAsset(t, "api.js")), "listAllPromotions") {
		t.Error("a listAllPromotions traversal exists; promotion history grows without " +
			"bound and must be navigated a page at a time")
	}
}

// TestPromotionHistorySurfacesACursorItCannotFollow keeps the next-page button
// from becoming an infinite loop against a broken server.
func TestPromotionHistorySurfacesACursorItCannotFollow(t *testing.T) {
	apiSource := stripJSNoise(readAsset(t, "api.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	if !strings.Contains(apiSource, "export function promotionCursorFault(") {
		t.Fatal("api.js has no promotionCursorFault; a non-progressing cursor would " +
			"be followed")
	}
	for _, condition := range []string{
		"rows.length === 0",
		"next <= previous",
		"last.id !== next",
	} {
		if !strings.Contains(apiSource, condition) {
			t.Errorf("promotionCursorFault does not check %q", condition)
		}
	}

	// The app has to act on it — computing a fault and ignoring it is worse
	// than not computing one, because it reads as protection.
	if !strings.Contains(app, "api.promotionCursorFault(after, response)") {
		t.Error("app.js never calls promotionCursorFault")
	}
	if !regexp.MustCompile(`fault !== ""[\s\S]{0,200}?showProblem\(fault\)`).MatchString(app) {
		t.Error("app.js computes a cursor fault without surfacing it")
	}
}

// TestPromotionHistoryRowsReachTheirRunEvidence is the third blocker.
//
// A promotion names two evaluation runs, and a history that omits them is not
// investigable: the record says a decision was made on evidence and gives the
// reader no way to reach it.
func TestPromotionHistoryRowsReachTheirRunEvidence(t *testing.T) {
	render := stripJSNoise(readAsset(t, "render.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	for _, field := range []string{"row.reference_run_id", "row.candidate_run_id"} {
		if !strings.Contains(render, field) {
			t.Errorf("renderPromotionList does not render %s; the decision's evidence "+
				"is unreachable from its history row", field)
		}
	}

	// A control, not a link: the run is re-read rather than navigated to.
	if !strings.Contains(render, "function runLink(") {
		t.Fatal("render.js builds no run control for history rows")
	}
	// Literals kept: the button's type and accessible name are string values.
	withLiterals := stripJSComments(readAsset(t, "render.js"))
	runLink := withLiterals[strings.Index(withLiterals, "function runLink("):]
	if cut := strings.Index(runLink, "\nexport function "); cut > 0 {
		runLink = runLink[:cut]
	}
	if !strings.Contains(runLink, `button.type = "button"`) {
		t.Error("the run control is not a button; a form-submitting default would " +
			"reload the page")
	}
	for _, navigation := range []string{".href", "window.location", "window.open", "<a "} {
		if strings.Contains(runLink, navigation) {
			t.Errorf("the run control uses %q; opening a run is a same-origin read "+
				"through the existing path, not a navigation", navigation)
		}
	}
	if !strings.Contains(runLink, "aria-label") {
		t.Error(`the run control's label is "Open" with no accessible name saying ` +
			"which run it opens")
	}

	// Wired to the page's existing run path, so GET /v1/evaluation-runs/{id}
	// stays authoritative and the promotion record does not become a second
	// source of truth for the run.
	if !regexp.MustCompile(`onOpenRun:\s*\(runID\)\s*=>\s*\{?\s*void loadRun\(runID`).MatchString(app) {
		t.Error("app.js does not route the history row's run control through loadRun; " +
			"a second run-rendering path would duplicate evaluation logic")
	}
	if regexp.MustCompile(`onOpenRun[\s\S]{0,120}?api\.getPromotion`).MatchString(app) {
		t.Error("opening a run from history reads the promotion instead of the run")
	}
}

// TestCollectionNavigationAddsNoBrowserAuthority re-runs the authority rules
// over the code this follow-up introduced.
//
// The existing guards read whole files and so already cover the new lines;
// this states the intent explicitly, because pagination is exactly where a
// "helpful" client-side filter tends to appear — ranking targets, hiding
// archived environments, or ordering history by outcome.
func TestCollectionNavigationAddsNoBrowserAuthority(t *testing.T) {
	sources := map[string]string{
		"api.js":    stripJSNoise(readAsset(t, "api.js")),
		"app.js":    stripJSNoise(readAsset(t, "app.js")),
		"render.js": stripJSNoise(readAsset(t, "render.js")),
	}

	forbidden := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{regexp.MustCompile(`\.rank\s*[<>]`), "CanPromote is the only implementation of promotion precedence"},
		{regexp.MustCompile(`[<>]=?\s*\w*\.?rank\b`), "CanPromote is the only implementation of promotion precedence"},
		{regexp.MustCompile(`verdict\s*===?\s*"pass"`), "the gate verdict is read, never computed"},
		// An assignment computing an outcome. Reading the server's field into a
		// local (`const outcome = promotion.outcome`) is the correct shape and
		// must not trip this, so the rule targets an outcome derived from
		// anything gate-shaped.
		{regexp.MustCompile(`outcome\s*=[^=][^;\n]*\b(verdict|passed|rank|gate)\b`), "the outcome is the server's field, never derived here"},
		{regexp.MustCompile(`\.sort\(`), "server order is the recorded order"},
		{regexp.MustCompile(`status\s*===?\s*"archived"`), "which environments are valid targets is the server's answer"},
	}
	for name, source := range sources {
		for _, rule := range forbidden {
			if match := rule.pattern.FindString(source); match != "" {
				t.Errorf("%s contains %q; %s", name, match, rule.why)
			}
		}
	}

	// The picker offers what the server returned. Filtering it would be the
	// rank comparison above, written as a predicate.
	render := sources["render.js"]
	options := render[strings.Index(render, "export function renderEnvironmentOptions("):]
	if strings.Contains(options, ".filter(") {
		t.Error("renderEnvironmentOptions filters the collection; which environments " +
			"are promotable is the server's decision")
	}
}

// TestPromotionNavStateIsAppliedAfterTheButtonIsRestored is a regression guard
// for a bug this follow-up introduced and then fixed, and the ordering it pins
// is the whole point.
//
// busy() disables the control that was clicked and, on the way out, restores
// the value it found. When that control is the next-page button, anything the
// work function sets inside the window is overwritten by the restore:
//
//	await busy(nextButton, async () => {
//	  ...
//	  nextButton.disabled = !hasMore   // ← undone by busy's finally
//	})
//
// Reaching the last page would then leave "Load next page" enabled with
// nothing to load. Applying the nav state after busy() returns is what makes
// the control describe the state that actually exists.
//
// This asserts source order, which is what a deterministic guard can prove
// without a browser — the same approach, and the same constraint, as
// TestRequestDeadlineCoversBodyConsumption.
func TestPromotionNavStateIsAppliedAfterTheButtonIsRestored(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))

	loader := app[strings.Index(app, "async function loadPromotionPage("):]
	if cut := strings.Index(loader, "\nfunction syncPromotionNav"); cut > 0 {
		loader = loader[:cut]
	}
	if loader == "" {
		t.Fatal("loadPromotionPage not found")
	}

	sync := strings.Index(loader, "syncPromotionNav()")
	if sync < 0 {
		t.Fatal("loadPromotionPage never syncs the navigation controls")
	}
	// The sync must sit after the closing of the busy() call, so the restore
	// cannot undo it.
	closing := strings.LastIndex(loader[:sync], "});")
	if closing < 0 {
		t.Fatal("could not locate the end of the busy() call in loadPromotionPage")
	}

	// And nothing inside the busy window may set the button's disabled state,
	// because that is precisely what gets overwritten.
	inside := loader[:closing]
	if strings.Contains(inside, "promotionNextButton.disabled") {
		t.Error("loadPromotionPage sets the next-page button's disabled state inside " +
			"busy(); busy's restore would undo it and leave the control enabled on " +
			"the last page")
	}
}

// ---------------------------------------------------------------------
// Zero-input Live view (task 074)
// ---------------------------------------------------------------------
//
// Source guards, for the standing reason task 063's testing strategy gives:
// no Node, no headless browser and no frontend tooling, so nothing in this
// package executes JavaScript. Each guard therefore pins a specific construct
// whose removal is the regression, not merely a word that happens to appear.

// TestLiveIsTheDefaultLandingView is the milestone in one assertion.
//
// A developer whose agent is producing telemetry must be able to open `/` and
// see it. If the shell still opens on the ID form, everything else here is
// decoration.
func TestLiveIsTheDefaultLandingView(t *testing.T) {
	shell := readAsset(t, "index.html")

	// Exactly one tab is selected on load, and it is Live.
	selected := regexp.MustCompile(`id="(tab-[a-z]+)"[^>]*aria-selected="true"`).
		FindAllStringSubmatch(shell, -1)
	if len(selected) != 1 {
		t.Fatalf("%d tabs are selected on load, want exactly 1", len(selected))
	}
	if selected[0][1] != "tab-live" {
		t.Errorf("the default tab is %s, want tab-live; the ID form must not be the "+
			"front door", selected[0][1])
	}

	// And exactly one panel is visible, which must be the Live one.
	visible := regexp.MustCompile(`<section class="panel[a-z -]*" id="(panel-[a-z]+)"([^>]*)>`).
		FindAllStringSubmatch(shell, -1)
	if len(visible) < 4 {
		t.Fatalf("found %d panels; this guard would not be meaningful", len(visible))
	}
	shown := []string{}
	for _, match := range visible {
		if !strings.Contains(match[2], "hidden") {
			shown = append(shown, match[1])
		}
	}
	if len(shown) != 1 || shown[0] != "panel-live" {
		t.Errorf("panels visible on load = %v, want exactly [panel-live]", shown)
	}
}

// TestEveryManualCapabilityIsStillReachable: nothing was removed to make room.
//
// The information architecture changed; the capabilities did not. Create,
// open, drive a lifecycle, compare and promote all remain, because they are
// the right tools for debugging even though they are the wrong front door.
func TestEveryManualCapabilityIsStillReachable(t *testing.T) {
	shell := readAsset(t, "index.html")

	for _, id := range []string{
		"form-open-project", "form-open-agent", "form-open-candidate", "form-open-run",
		"form-create-project", "form-create-agent", "form-create-candidate", "form-create-run",
		"form-compare", "form-promotion-create", "form-promotion-get", "form-promotion-list",
		"form-watch",
	} {
		if !strings.Contains(shell, `id="`+id+`"`) {
			t.Errorf("index.html no longer contains %s; task 074 reorganizes the manual "+
				"surface and removes none of it", id)
		}
	}
	for _, id := range []string{"run-start", "run-complete", "run-fail", "run-cancel"} {
		if !strings.Contains(shell, `id="`+id+`"`) {
			t.Errorf("the run lifecycle control %s is gone", id)
		}
	}
	// Every tab still has a panel and vice versa, so nothing was orphaned by
	// the reordering.
	tabs := regexp.MustCompile(`data-panel="(panel-[a-z]+)"`).FindAllStringSubmatch(shell, -1)
	for _, tab := range tabs {
		if !strings.Contains(shell, `id="`+tab[1]+`"`) {
			t.Errorf("tab points at %s, which does not exist", tab[1])
		}
	}
}

// TestLiveSubscribesUnfilteredAndWithNoRunID is the zero-input premise.
//
// An empty run id means unconstrained on the server — RealtimeFilter treats
// every empty dimension that way and says so. The Live view relies on it, so
// this pins both halves: the client asks for no filter, and the path builder
// really does omit the parameter rather than sending an empty one.
func TestLiveSubscribesUnfilteredAndWithNoRunID(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))
	apiSource := stripJSComments(readAsset(t, "api.js"))

	if !strings.Contains(app, `realtimePath: () => api.realtimePath("")`) {
		t.Error("the Live session does not subscribe with an empty run id")
	}
	if !strings.Contains(app, `liveSession.watch("")`) {
		t.Error("the Live session is not started unfiltered at load")
	}
	// Both halves of the ternary, asserted separately because a Go raw string
	// cannot hold the template literal's backticks.
	if !strings.Contains(apiSource, "runID ?") ||
		!strings.Contains(apiSource, `: "/v1/realtime"`) {
		t.Error("realtimePath does not omit the query for an empty run id; an empty " +
			"filter must send no parameter at all")
	}
	if !strings.Contains(apiSource, "run_id=${encodeURIComponent(runID)}") {
		t.Error("realtimePath no longer sends run_id for a named run; the single-run " +
			"watch depends on it")
	}

	// No new realtime route, parameter or event kind. The protocol is
	// untouched and every addition this task made is elsewhere.
	for _, forbidden := range []string{
		"/v1/realtime/", "/v1/live", "/v1/activity",
		"scope_id=", "unfiltered=", "all=true",
	} {
		if strings.Contains(apiSource, forbidden) {
			t.Errorf("api.js references %q; GET /v1/realtime keeps its route and "+
				"parameters unchanged", forbidden)
		}
	}
}

// TestLiveStartupBudgetIsOneProjectsPage is the resync-loop regression.
//
// 64 projects × 64 agents × 64 candidates × 64 runs is sixteen million rows
// across a quarter of a million requests, during which the resync buffer holds
// 64 frames. Under live traffic that buffer overflows, the client abandons and
// resynchronizes, and the crawl starts again — worse the larger the database
// is, which is precisely backwards. A bounded route is not a bounded workflow.
func TestLiveStartupBudgetIsOneProjectsPage(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))

	// The snapshot is the projects page and nothing else.
	if !regexp.MustCompile(`snapshot:\s*\(\)\s*=>\s*loadRootProjects\(\)`).MatchString(app) {
		t.Fatal("the Live session's authoritative snapshot is not loadRootProjects")
	}
	loader := app[strings.Index(app, "async function loadRootProjects"):]
	if cut := strings.Index(loader, "\nfunction "); cut > 0 {
		loader = loader[:cut]
	}
	for _, child := range []string{
		"listProjectAgents", "listAgentCandidates", "listCandidateRuns",
	} {
		if strings.Contains(loader, child) {
			t.Errorf("loadRootProjects calls %s; startup performs no child traversal", child)
		}
	}
	// It must not follow its own continuation either.
	if strings.Contains(loader, "next_after") {
		t.Error("loadRootProjects reads next_after; startup follows no continuation")
	}
	if strings.Contains(loader, "while") || strings.Contains(loader, "for (") {
		t.Error("loadRootProjects loops; the startup budget is one request")
	}
}

// TestLiveHasNoPollingOrPrefetch: every request is caused by a person, by the
// single startup snapshot, or by a reconnect taking that snapshot again.
func TestLiveHasNoPollingOrPrefetch(t *testing.T) {
	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, timer := range []string{"setInterval(", "requestIdleCallback(", "setImmediate("} {
			if strings.Contains(stripped, timer) {
				t.Errorf("%s uses %s; there is no refresh loop anywhere in this bundle",
					name, timer)
			}
		}
	}

	// setTimeout survives, and only for the two bounded, non-repeating uses
	// realtime.js already had: the handshake deadline and the reconnect
	// backoff. Neither polls, and neither fetches.
	realtime := stripJSNoise(readAsset(t, "realtime.js"))
	if got := strings.Count(realtime, "setTimeout("); got != 2 {
		t.Errorf("realtime.js uses setTimeout %d times, want 2 (handshake deadline, "+
			"reconnect backoff); a third is likely a poll", got)
	}
	app := stripJSNoise(readAsset(t, "app.js"))
	if strings.Contains(app, "setTimeout(") {
		t.Error("app.js schedules a timer; every request must be caused by a person " +
			"or by the startup snapshot")
	}
}

// TestLiveDescentIsLazyAndOnePagePerAction: one click, one request.
func TestLiveDescentIsLazyAndOnePagePerAction(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))

	discovery := stripJSNoise(readAsset(t, "discovery.js"))

	// Each level's loader performs exactly one request. The browser walks the
	// hierarchy only as fast as a person clicks.
	levels := []struct{ fn, call string }{
		{"loadProjects", "this.deps.listProjects(after)"},
		{"loadAgents", "this.deps.listProjectAgents(projectID, after)"},
		{"loadCandidates", "this.deps.listAgentCandidates(agentID, after)"},
		{"loadRuns", "this.deps.listCandidateRuns(candidateID, after)"},
	}
	for _, level := range levels {
		body := functionBodyForTest(t, discovery, "async "+level.fn+"(")
		if !strings.Contains(body, level.call) {
			t.Errorf("HierarchyBrowser.%s does not issue %s", level.fn, level.call)
		}
		if got := strings.Count(body, "await this.deps."); got != 1 {
			t.Errorf("HierarchyBrowser.%s makes %d awaited API calls, want exactly 1 "+
				"per user action", level.fn, got)
		}
		if strings.Contains(body, "while") || strings.Contains(body, "for (") {
			t.Errorf("HierarchyBrowser.%s loops; a level is one bounded page", level.fn)
		}
	}

	// Every rendered level offers an explicit open control and an explicit
	// continuation. Neither is ever followed automatically.
	for _, level := range []string{
		"renderProjectsLevel", "renderAgentsLevel", "renderCandidatesLevel", "renderRunsLevel",
	} {
		body := functionBodyForTest(t, app, "function "+level+"(")
		for _, control := range []string{"onOpen:", "onMore:"} {
			if !strings.Contains(body, control) {
				t.Errorf("%s offers no %s control", level, control)
			}
		}
	}
}

// TestLiveGraphIsScopedToOneRun is the correctness guard behind the whole
// scope-identity section.
//
// fingerprint_id is behavioral identity and carries no project, agent,
// candidate, run or actor — task 051 was explicit that run and candidate
// metadata must never become fingerprint dimensions. Two unrelated agents
// doing the same thing therefore produce the same fingerprint, and merging
// them would let one run's new_behavior, decision and risk overwrite
// another's.
func TestLiveGraphIsScopedToOneRun(t *testing.T) {
	live := stripJSNoise(readAsset(t, "live.js"))

	// The model must refuse to draw a frame from an unselected scope.
	if !regexp.MustCompile(`card\.key !== this\.selectedKey[\s\S]{0,120}?edge:\s*null`).MatchString(live) {
		t.Error("LiveModel.observe does not refuse to draw an unselected scope's " +
			"observation; two runs sharing a fingerprint would share an edge")
	}
	// Selecting a different scope must start a new graph rather than keep the
	// old topology beside the new one.
	if !regexp.MustCompile(`this\.selectedKey = key;[\s\S]{0,120}?new RunGraph`).MatchString(live) {
		t.Error("selecting a scope does not rebuild the graph; the previous run's " +
			"topology would remain as residue")
	}

	// Scope identity is structured, never a delimiter join. `a|b` and `a|b`
	// are the same string whether the parts were ("a","b") or ("a|b",""), and
	// identifiers here may contain almost any printable character.
	if !strings.Contains(live, "JSON.stringify([") {
		t.Error("scopeKey does not build a structured key")
	}
	for _, join := range []string{`.join("|")`, `.join(":")`, `.join("/")`, `+ "|" +`, "`${scope.project_id}|"} {
		if strings.Contains(live, join) {
			t.Errorf("live.js builds a key with %q; a delimiter join can collide "+
				"across scopes", join)
		}
	}
	// All six dimensions participate, so two runs of one candidate and the
	// same env ref under two projects are distinct.
	for _, dimension := range []string{
		"scope.project_id", "scope.agent_id", "scope.candidate_id",
		"scope.run_id", "scope.environment", "scope.behavioral_profile",
	} {
		if !strings.Contains(live, dimension) {
			t.Errorf("scopeKey omits %s; scopes that differ only there would merge",
				dimension)
		}
	}
}

// TestLiveViewDecidesNothing extends the authority scan to the new modules.
func TestLiveViewDecidesNothing(t *testing.T) {
	sources := map[string]string{
		"live.js":      stripJSNoise(readAsset(t, "live.js")),
		"graph.js":     stripJSNoise(readAsset(t, "graph.js")),
		"rail.js":      stripJSNoise(readAsset(t, "rail.js")),
		"timeline.js":  stripJSNoise(readAsset(t, "timeline.js")),
		"inspector.js": stripJSNoise(readAsset(t, "inspector.js")),
		"app.js":       stripJSNoise(readAsset(t, "app.js")),
	}
	forbidden := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{regexp.MustCompile(`\.rank\s*[<>]`), "environment precedence is CanPromote's, server-side"},
		{regexp.MustCompile(`verdict\s*===?\s*"pass"`), "the gate verdict is read, never computed"},
		{regexp.MustCompile(`outcome\s*=[^=][^;\n]*\b(verdict|passed|gate)\b`), "the outcome is the server's field"},
		{regexp.MustCompile(`trust_score\s*[<>]`), "a trust threshold is a policy decision"},
		{regexp.MustCompile(`anomaly_score\s*[<>]`), "an anomaly threshold is a policy decision"},
		{regexp.MustCompile(`risk_level\s*===?\s*"(critical|high)"\s*\?`), "risk severity is not ranked here"},
		{regexp.MustCompile(`new_behavior\s*=\s*[^=]`), "new-behavior is observed, never assigned"},
	}
	for name, source := range sources {
		for _, rule := range forbidden {
			if match := rule.pattern.FindString(source); match != "" {
				t.Errorf("%s contains %q; %s", name, match, rule.why)
			}
		}
	}

	// Every value the graph shows is read from the observation, not derived.
	live := sources["live.js"]
	for _, field := range []string{
		"observation.decision", "observation.risk_level", "observation.trust_score",
		"observation.anomaly_score", "observation.anomaly_confidence",
		"observation.new_behavior", "observation.behavior_complete",
	} {
		if !strings.Contains(live, field) {
			t.Errorf("live.js never reads %s; the graph must show the server's own "+
				"values", field)
		}
	}
}

// TestLiveGraphInventsNoSemanticName pins task 075's boundary.
//
// If telemetry proves only `POST → export.localhost`, that is what is drawn.
// A friendlier verb inferred here would be the browser asserting something no
// evidence supports.
func TestLiveGraphInventsNoSemanticName(t *testing.T) {
	live := stripJSComments(readAsset(t, "live.js"))
	graph := stripJSComments(readAsset(t, "graph.js")) +
		stripJSComments(readAsset(t, "inspector.js")) +
		stripJSComments(readAsset(t, "timeline.js"))

	// The edge is built from the descriptor's own fields, unmodified.
	for _, field := range []string{
		"behavior.operation_category", "behavior.operation_name",
		"behavior.target_name", "behavior.target_category", "behavior.actor_type",
	} {
		if !strings.Contains(live, field) {
			t.Errorf("live.js does not read %s", field)
		}
	}
	// No mapping table, no inference, no vocabulary of invented verbs. Task
	// 075 owns semantic fidelity; task 074 renders what arrived.
	for name, source := range map[string]string{"live.js": live, "the render modules": graph} {
		for _, invented := range []string{
			"export_customer", "data_export", "send_email", "tool.name", "agent.name",
			"openinference", "gen_ai.", "llm.",
		} {
			if strings.Contains(strings.ToLower(source), invented) {
				t.Errorf("%s references %q; semantic normalization is task 075's, and "+
					"task 074 renders only the fidelity the observation carries",
					name, invented)
			}
		}
	}

	// StableFeatures has no Direction, and this task does not add one — that
	// would change behavioral identity for a visualization.
	for name, source := range map[string]string{"live.js": live, "the render modules": graph} {
		if regexp.MustCompile(`\bdirection\b`).MatchString(strings.ToLower(source)) {
			t.Errorf("%s references a direction dimension; StableFeatures carries none "+
				"and adding one would change behavioral identity", name)
		}
	}
}

// TestLiveBoundsAreTheDocumentedValues pins each visualization limit, and the
// two it reuses rather than re-chooses.
func TestLiveBoundsAreTheDocumentedValues(t *testing.T) {
	live := stripJSNoise(readAsset(t, "live.js"))
	realtime := stripJSNoise(readAsset(t, "realtime.js"))

	bounds := map[string]int{
		"MAX_SCOPE_CARDS":  16,
		"MAX_SOURCE_NODES": 8,
		"MAX_TARGET_NODES": 64,
		"MAX_EDGES":        128,
	}
	for name, want := range bounds {
		pattern := regexp.MustCompile(`export const ` + name + ` = (\d+);`)
		match := pattern.FindStringSubmatch(live)
		if match == nil {
			t.Errorf("live.js declares no %s", name)
			continue
		}
		if match[1] != fmt.Sprint(want) {
			t.Errorf("%s = %s, want %d", name, match[1], want)
		}
	}

	// The existing two are reused, not duplicated. A second feed bound or a
	// second resync bound would be two numbers meaning one thing.
	if !regexp.MustCompile(`DISPLAY_MAX = 100`).MatchString(realtime) {
		t.Error("realtime.js no longer declares DISPLAY_MAX = 100")
	}
	if !regexp.MustCompile(`PENDING_MAX = 64`).MatchString(realtime) {
		t.Error("realtime.js no longer declares PENDING_MAX = 64")
	}
	for _, competing := range []string{"MAX_FEED_ROWS", "MAX_OBSERVATIONS", "MAX_PENDING_FRAMES"} {
		if strings.Contains(live, competing) {
			t.Errorf("live.js declares %s; the feed and resync bounds already exist "+
				"in realtime.js and are reused", competing)
		}
	}
}

// TestLiveStatesSaturationRatherThanTruncating, and keeps the three kinds of
// limit distinguishable.
//
// A saturated viewport means the browser chose not to draw everything.
// behavior_complete: false means the platform's behavioral evidence itself
// saturated. A realtime queue overflow means notification continuity was lost.
// Reporting any as another would misdescribe the run.
func TestLiveStatesSaturationRatherThanTruncating(t *testing.T) {
	live := stripJSComments(readAsset(t, "live.js"))
	// rail.js owns both notices, so the two statements live side by side and
	// can be checked against each other.
	graph := stripJSComments(readAsset(t, "rail.js"))

	// Saturation names the bound and how much is missing.
	if !strings.Contains(live, "saturation()") {
		t.Fatal("live.js exposes no saturation state")
	}
	for _, field := range []string{"bound:", "limit:", "omitted:"} {
		if !strings.Contains(live, field) {
			t.Errorf("saturation() omits %s; a bound that does not say what is missing "+
				"is a silent truncation with a label", field)
		}
	}

	// The three statements are separate strings, so one can never be rendered
	// in place of another.
	for _, phrase := range []string{
		"This is a display limit",
		"behavioral evidence as incomplete",
	} {
		if !strings.Contains(graph, phrase) {
			t.Errorf("rail.js does not state %q", phrase)
		}
	}
	// And the viewport notice explicitly disclaims the evidence reading.
	if !strings.Contains(graph, "the run's own evidence is unaffected") {
		t.Error("the viewport-saturation notice does not distinguish itself from " +
			"the platform's evidence bound")
	}
	// The overflow path still abandons rather than dropping.
	realtime := stripJSComments(readAsset(t, "realtime.js"))
	if !strings.Contains(realtime, "buffered more than ") {
		t.Error("realtime.js no longer abandons on resync buffer overflow")
	}
}

// TestLiveHonoursReducedMotion, with no information carried by movement.
func TestLiveHonoursReducedMotion(t *testing.T) {
	css := readAsset(t, "styles.css")
	// Literals kept: the media query is a string value, and stripping strings
	// would blank the very thing being located.
	app := stripJSComments(readAsset(t, "app.js"))
	graph := stripJSNoise(readAsset(t, "graph.js"))

	if !strings.Contains(css, "@media (prefers-reduced-motion: reduce)") {
		t.Fatal("styles.css has no prefers-reduced-motion block")
	}
	// The preference is also read in script, so the animated element is never
	// created rather than merely hidden.
	if !strings.Contains(app, `matchMedia("(prefers-reduced-motion: reduce)")`) {
		t.Error("app.js does not read prefers-reduced-motion")
	}
	if !regexp.MustCompile(`this\.reducedMotion`).MatchString(graph) {
		t.Error("graph.js creates the pulse unconditionally; under reduced motion it " +
			"must not be built at all")
	}
	// Nothing that carries information may live inside the pulse. The
	// operation label, the state text and the NEW badge are all built by
	// render(), which runs whatever the motion preference is.
	pulseBlock := functionBodyForTest(t, graph, "pulse(edge) {")
	for _, informative := range []string{"edge-op", "edge-state", "NEW", "aria-label", "node-badge"} {
		if strings.Contains(pulseBlock, informative) {
			t.Errorf("pulse() builds %q; no information may exist only in movement",
				informative)
		}
	}
	// No strobe: the one animation is a single non-repeating traversal.
	if !strings.Contains(stripJSComments(readAsset(t, "graph.js")), `["repeatCount", "1"]`) {
		t.Error("the pulse is not pinned to a single repetition")
	}
}

// TestLiveRendersWithoutDangerousPrimitives is the escaping rule extended to
// SVG, which is where an animated surface most invites an exception.
func TestLiveRendersWithoutDangerousPrimitives(t *testing.T) {
	for _, name := range []string{"live.js", "graph.js", "rail.js", "timeline.js", "inspector.js", "discovery.js"} {
		source := stripJSNoise(readAsset(t, name))
		for _, forbidden := range []string{
			"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
			"eval(", "new Function(", "srcdoc",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("%s uses %s", name, forbidden)
			}
		}
	}

	graph := stripJSNoise(readAsset(t, "graph.js"))
	// SVG nodes are built, and their text is set as text.
	if !strings.Contains(graph, "document.createElementNS(SVG_NS, tag)") {
		t.Error("graph.js does not build SVG through createElementNS")
	}
	if !strings.Contains(graph, "node.textContent = content") {
		t.Error("graph.js does not set SVG text through textContent")
	}
	// A tooltip and an accessible name are not exemptions: both are text.
	if !strings.Contains(graph, "title.textContent") {
		t.Error("graph.js sets a title by some means other than textContent")
	}
}

// TestLiveStoresNothingInTheBrowser: the collection routes are what make
// browser storage unnecessary, and it stays unused.
func TestLiveStoresNothingInTheBrowser(t *testing.T) {
	for name, source := range scriptAssets(t) {
		stripped := stripJSNoise(source)
		for _, api := range []string{
			"localStorage", "sessionStorage", "indexedDB", "document.cookie",
			"caches.open", "navigator.storage",
		} {
			if strings.Contains(stripped, api) {
				t.Errorf("%s uses %s; the hierarchy comes from /v1, which is what makes "+
					"browser storage unnecessary", name, api)
			}
		}
	}
}

// TestLiveClaimsNoHistory keeps the viewport from reading as a trace explorer.
//
// Task 067 owns event history. Realtime publishes replay_available: false, so
// a page implying that what it draws is a record of what happened would be
// contradicting the protocol it is speaking.
func TestLiveClaimsNoHistory(t *testing.T) {
	shell := strings.ToLower(readAsset(t, "index.html"))

	// Claims of history, not disclaimers of it.
	//
	// The panel legitimately says "the stream keeps no history and replays
	// nothing", which is the sentence doing the work — banning the words
	// "history" and "replay" outright would ban the denial along with the
	// claim. So each phrase below is one that would only appear in an
	// assertion that history exists here.
	for _, phrase := range []string{
		"full history", "complete history", "all past observations",
		"since startup", "complete trace", "every observation ever",
		"replays the", "replayed from", "historical view",
	} {
		if strings.Contains(shell, phrase) {
			t.Errorf("index.html says %q; the Live view is a current viewport and the "+
				"stream replays nothing", phrase)
		}
	}
	// And it says what it is, in the positive.
	if !strings.Contains(shell, "viewport") {
		t.Error("index.html does not describe the Live view as a viewport")
	}
	if !strings.Contains(shell, "keeps no history") {
		t.Error("index.html does not state that the stream keeps no history")
	}

	// No persistence of observations anywhere in the model.
	live := stripJSNoise(readAsset(t, "live.js"))
	for _, forbidden := range []string{"history", "archive", "persist"} {
		if regexp.MustCompile(`\b` + forbidden + `\b`).MatchString(strings.ToLower(live)) {
			t.Errorf("live.js references %q; nothing here retains an observation", forbidden)
		}
	}
}

// TestNoFrontendDependencyOrBuildStep: the stack is unchanged.
func TestNoFrontendDependencyOrBuildStep(t *testing.T) {
	root := "assets"
	entries, err := assetFS.ReadDir(root)
	if err != nil {
		t.Fatalf("read assets: %v", err)
	}
	for _, entry := range entries {
		for _, forbidden := range []string{
			"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
			"vite.config.js", "webpack.config.js", "rollup.config.js", "tsconfig.json",
		} {
			if entry.Name() == forbidden {
				t.Errorf("the bundle contains %s; there is no build step", forbidden)
			}
		}
	}
	for name, source := range scriptAssets(t) {
		// Every import is a relative path to a sibling module.
		for _, match := range regexp.MustCompile(`from\s+"([^"]+)"`).FindAllStringSubmatch(source, -1) {
			if !strings.HasPrefix(match[1], "./") {
				t.Errorf("%s imports %q; only same-directory modules are allowed",
					name, match[1])
			}
		}
	}
}

// functionBodyForTest returns one top-level function's source text.
//
// Stops at the next top-level declaration of any kind, so the last function in
// a file does not swallow everything after it — which it did, and made a
// per-function assertion count the whole module's API calls.
func functionBodyForTest(t *testing.T, source, header string) string {
	t.Helper()
	start := strings.Index(source, header)
	if start < 0 {
		t.Fatalf("no declaration %q", header)
		return ""
	}
	body := source[start+len(header):]
	next := len(body)
	// Top-level declarations and class members both end a body. The method
	// boundaries matter since the live modules are classes: without them the
	// last method of a class swallows the rest of the file, which made a
	// per-method assertion count the whole module's API calls.
	for _, boundary := range []string{
		"\nasync function ", "\nfunction ", "\nconst ", "\n// ---",
		"\n  async ", "\n  }\n\n  ", "\n}\n",
	} {
		if cut := strings.Index(body, boundary); cut >= 0 && cut < next {
			next = cut
		}
	}
	return body[:next]
}

// ---------------------------------------------------------------------
// The Live Observatory (task 074 redesign)
// ---------------------------------------------------------------------
//
// Source guards, for the standing reason task 063's testing strategy gives:
// no Node, no headless browser and no frontend tooling, so nothing in this
// package executes JavaScript. Each pins a construct whose removal is the
// regression.

// TestFrontDoorIsNotAForm is the product claim, asserted structurally.
//
// The old page opened on four text inputs asking for a project, agent,
// candidate and run identifier — which told a developer that Trustvian needed
// something from them before it could be useful. It does not: it needs
// telemetry. This fails if an identifier input ever returns to the landing
// viewport.
func TestFrontDoorIsNotAForm(t *testing.T) {
	shell := readAsset(t, "index.html")

	live := panelBodyForTest(t, shell, "panel-live")
	if live == "" {
		t.Fatal("no Live panel")
	}

	// The Live panel carries exactly one form — the single-run watch is under
	// Investigate, so Live should carry none at all.
	if strings.Contains(live, "<form") {
		t.Error("the Live panel contains a form; the landing viewport must not ask " +
			"for anything before it is useful")
	}
	for _, field := range []string{
		`id="open-project-id"`, `id="open-agent-id"`, `id="open-candidate-id"`,
		`id="open-run-id"`, `id="project-id"`, `id="agent-id"`, `id="candidate-id"`,
	} {
		if strings.Contains(live, field) {
			t.Errorf("the Live panel contains %s; identifier entry belongs to Manage", field)
		}
	}

	// And the primary navigation is the product's, not the database's.
	nav := shell[strings.Index(shell, `<nav class="tabs"`):strings.Index(shell, "</nav>")]
	for _, gone := range []string{`id="tab-project"`, `id="tab-agent"`, `id="tab-candidate"`} {
		if strings.Contains(nav, gone) {
			t.Errorf("the primary navigation still contains %s; entity CRUD is a "+
				"Manage sub-surface, not a top-level destination", gone)
		}
	}
	for _, want := range []string{
		`id="tab-live"`, `id="tab-investigate"`, `id="tab-compare"`,
		`id="tab-promotion"`, `id="tab-manage"`,
	} {
		if !strings.Contains(nav, want) {
			t.Errorf("the primary navigation is missing %s", want)
		}
	}
}

// panelBodyForTest returns one panel's markup.
func panelBodyForTest(t *testing.T, shell, panelID string) string {
	t.Helper()
	start := strings.Index(shell, `id="`+panelID+`"`)
	if start < 0 {
		return ""
	}
	end := strings.Index(shell[start:], "\n</section>")
	if end < 0 {
		return shell[start:]
	}
	return shell[start : start+end]
}

// TestEmptyStateTeachesRatherThanAsking pins the replacement for the old
// four-input landing screen.
func TestEmptyStateTeachesRatherThanAsking(t *testing.T) {
	rail := stripJSComments(readAsset(t, "rail.js"))

	if !strings.Contains(rail, "Waiting for agent activity") {
		t.Error("the empty state does not say what it is waiting for")
	}
	if !strings.Contains(rail, "Run an instrumented agent") {
		t.Error("the empty state does not tell a developer what to do")
	}
	if !strings.Contains(rail, "there is nothing to type") {
		t.Error("the empty state does not say that nothing needs to be typed")
	}
	// It must not send somebody looking for an identifier.
	for _, asking := range []string{"Enter a", "enter an ID", "Provide the", "Paste the"} {
		if strings.Contains(rail, asking) {
			t.Errorf("the empty state says %q; it must ask for telemetry, not input", asking)
		}
	}
}

// TestObservatoryRegionsExist checks the information model rather than the
// layout: active scopes, a canvas, an inspector and a timeline.
func TestObservatoryRegionsExist(t *testing.T) {
	shell := readAsset(t, "index.html")
	for _, id := range []string{
		`id="rail"`, `id="canvas"`, `id="inspector"`, `id="timeline"`,
		`id="canvas-notices"`, `id="conn-chip"`, `id="header-agent"`, `id="header-counts"`,
	} {
		if !strings.Contains(shell, id) {
			t.Errorf("the Live panel is missing %s", id)
		}
	}
	// Responsive collapse rather than three columns squeezed into a phone.
	css := readAsset(t, "styles.css")
	if !strings.Contains(css, ".observatory") {
		t.Fatal("no observatory layout")
	}
	if strings.Count(css, "@media (max-width:") < 2 {
		t.Error("the observatory has fewer than two breakpoints; three columns " +
			"shrunk to phone width is not responsive")
	}
}

// TestHeaderCountsAreAuthoritative is the rule that separates a stream from a
// database.
//
// A frame count and a record count are different facts. The header shows the
// record count read from GET /v1/…/progress; the rail's per-card count is
// labelled "seen live" and is never presented as the run's total.
func TestHeaderCountsAreAuthoritative(t *testing.T) {
	app := stripJSComments(readAsset(t, "app.js"))
	rail := stripJSComments(readAsset(t, "rail.js"))

	if !strings.Contains(app, "api.getProgress(runID)") {
		t.Error("the header never reads authoritative progress")
	}
	if !strings.Contains(rail, "seen live") {
		t.Error("the rail's per-card count is not labelled as a live count")
	}

	// The counters must not be accumulated from frames.
	summary := functionBodyForTest(t, stripJSNoise(readAsset(t, "app.js")),
		"function authoritativeSummary(card)")
	for _, derived := range []string{"seenLive +", "+= 1", "cards.size"} {
		if strings.Contains(summary, derived) {
			t.Errorf("authoritativeSummary derives a count (%q); authoritative counts "+
				"come from /v1", derived)
		}
	}
	if !strings.Contains(summary, "authoritative.recordCount") {
		t.Error("the header's observation count does not come from the authoritative read")
	}
}

// TestSelectionIsPinnedNotStolen covers the follow/pin distinction.
//
// A developer reading one run's graph must not have it replaced because
// another agent became busy. That is the difference between a cockpit and a
// dashboard that changes under you.
func TestSelectionIsPinnedNotStolen(t *testing.T) {
	live := stripJSNoise(readAsset(t, "live.js"))
	rail := stripJSComments(readAsset(t, "rail.js"))

	// Following only adopts a new scope while following.
	if !regexp.MustCompile(`this\.following && card\.key !== this\.selectedKey`).MatchString(live) {
		t.Error("observe() changes the selection without checking the follow mode; " +
			"activity elsewhere would steal the graph being read")
	}
	// An explicit selection pins.
	selectBody := functionBodyForTest(t, live, "select(key) {")
	if !strings.Contains(selectBody, "this.following = false") {
		t.Error("select() does not pin; a click that evaporates on the next frame " +
			"is not an intent")
	}
	// And there is a way back.
	if !strings.Contains(live, "follow()") {
		t.Error("live.js offers no way to return to auto-follow")
	}
	if !strings.Contains(rail, "Follow active") {
		t.Error("the rail offers no Follow active control")
	}
	// The mode is stated in words, not implied by a highlight.
	for _, phrase := range []string{"Following newest activity", "Pinned to your selection"} {
		if !strings.Contains(rail, phrase) {
			t.Errorf("the rail does not state %q", phrase)
		}
	}
}

// TestOneObservationOneAnimation: the canvas reports evidence, never mood.
func TestOneObservationOneAnimation(t *testing.T) {
	app := stripJSNoise(readAsset(t, "app.js"))
	// Literals kept: the attribute pairs and class names being located are
	// string values.
	graph := stripJSComments(readAsset(t, "graph.js"))

	// Exactly one pulse call, on the observation path, after the edge exists.
	if got := strings.Count(app, "canvas.pulse("); got != 1 {
		t.Errorf("app.js calls canvas.pulse %d times, want exactly 1 (one per "+
			"received observation)", got)
	}
	if !regexp.MustCompile(`if \(edge === null\)[\s\S]{0,400}?return;`).MatchString(app) {
		t.Error("an observation outside the selected run is not short-circuited " +
			"before the pulse")
	}

	// The pulse ends. Nothing loops, and nothing is left behind.
	if !strings.Contains(graph, `["repeatCount", "1"]`) {
		t.Error("the pulse repeats; animation reports one arrival")
	}
	if !strings.Contains(graph, "dot.remove()") {
		t.Error("the pulse element is never removed; the layer would grow without bound")
	}
	for _, ambient := range []string{"infinite", "alternate", "steps(", "@keyframes spin"} {
		if strings.Contains(graph, ambient) {
			t.Errorf("graph.js uses %q; there is no ambient or looping animation", ambient)
		}
	}
	css := readAsset(t, "styles.css")
	if strings.Contains(css, "animation: ") && strings.Contains(css, "infinite") {
		t.Error("styles.css declares a looping animation; a quiet agent draws a " +
			"quiet graph")
	}
}

// TestNewBehaviorIsImpossibleToMiss, and never by colour alone.
func TestNewBehaviorIsImpossibleToMiss(t *testing.T) {
	graph := stripJSComments(readAsset(t, "graph.js"))
	inspector := stripJSComments(readAsset(t, "inspector.js"))
	timeline := stripJSComments(readAsset(t, "timeline.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	// A word, in all three surfaces.
	for name, source := range map[string]string{
		"graph.js": graph, "inspector.js": inspector, "timeline.js": timeline,
	} {
		if !strings.Contains(source, `"NEW"`) {
			t.Errorf("%s never renders the word NEW; the state would be colour alone", name)
		}
	}
	// The inspector opens it, but only when nothing else is being read.
	if !regexp.MustCompile(`edge\.newBehavior && inspectedEdgeID === ""`).MatchString(app) {
		t.Error("a new behavior does not focus the inspector, or focuses it even " +
			"when the developer is reading something else")
	}
	// And it is never editorialised into a judgement the server did not make.
	for name, source := range map[string]string{
		"graph.js": graph, "inspector.js": inspector, "timeline.js": timeline,
		"rail.js": stripJSComments(readAsset(t, "rail.js")),
	} {
		for _, verdict := range []string{"dangerous", "malicious", "suspicious", "unsafe", "attack"} {
			if strings.Contains(strings.ToLower(source), verdict) {
				t.Errorf("%s calls a behavior %q; NEW means new, and any other "+
					"judgement is the server's to make", name, verdict)
			}
		}
	}
}

// TestInspectorRendersServerValuesOnly.
//
// Five independent server-produced readings, shown as themselves. No aggregate
// score, no red/amber/green verdict, no threshold — collapsing them into one
// browser-owned judgement would be this page inventing a verdict the platform
// never made.
func TestInspectorRendersServerValuesOnly(t *testing.T) {
	// Literals kept for the field scan — the metric keys are strings in a
	// table — and stripped for the arithmetic scan below, so a forbidden
	// pattern can never match inside a comment or a message.
	inspector := stripJSComments(readAsset(t, "inspector.js"))
	logic := stripJSNoise(readAsset(t, "inspector.js"))

	for _, field := range []string{
		"edge.decision", "edge.riskLevel", "trustScore", "anomalyScore",
		"anomalyConfidence", "edge.newBehavior",
	} {
		if !strings.Contains(inspector, field) {
			t.Errorf("the inspector never reads %s", field)
		}
	}
	forbidden := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{regexp.MustCompile(`trustScore\s*[<>]`), "a trust threshold is a policy decision"},
		{regexp.MustCompile(`anomalyScore\s*[<>]`), "an anomaly threshold is a policy decision"},
		{regexp.MustCompile(`healthScore|overallScore|riskScore\s*=`), "there is no aggregate score"},
		{regexp.MustCompile(`\*\s*anomalyScore|trustScore\s*\*`), "no reading is combined with another"},
	}
	for _, rule := range forbidden {
		if match := rule.pattern.FindString(logic); match != "" {
			t.Errorf("inspector.js contains %q; %s", match, rule.why)
		}
	}

	// The privacy allowlist is unchanged. A richer panel is not permission to
	// widen the surface, and a detail drawer is not an exemption.
	for _, sensitive := range []string{
		"prompt", "completion", "reasoning", "tool_arg", "arguments",
		"request_body", "response_body", "attributes", "decision_record",
		"policy_reason",
	} {
		if strings.Contains(strings.ToLower(logic), sensitive) {
			t.Errorf("inspector.js references %q; the privacy boundary is unchanged", sensitive)
		}
	}
}

// TestTimelineIsAViewportNotHistory.
func TestTimelineIsAViewportNotHistory(t *testing.T) {
	timeline := stripJSComments(readAsset(t, "timeline.js"))
	app := stripJSNoise(readAsset(t, "app.js"))

	if !regexp.MustCompile(`TIMELINE_MAX = 100`).MatchString(timeline) {
		t.Error("the timeline bound is not the existing DISPLAY_MAX of 100")
	}
	if !strings.Contains(timeline, "Live stream") || !strings.Contains(timeline, "Not retained history") {
		t.Error("the timeline does not label itself as the current connection")
	}
	// A reconnect starts a segment rather than concatenating.
	if !strings.Contains(timeline, "breakSegment()") {
		t.Error("timeline.js has no segment break")
	}
	if !strings.Contains(app, "timeline.breakSegment()") {
		t.Error("a reconnect does not break the timeline segment; two connections " +
			"would read as one sequence")
	}
	breakBody := functionBodyForTest(t, stripJSNoise(readAsset(t, "timeline.js")), "breakSegment() {")
	if !strings.Contains(breakBody, "this.rows = []") {
		t.Error("breakSegment keeps the previous connection's rows")
	}
	// A high-rate feed must not be announced continuously.
	if strings.Contains(timeline, `aria-live`) {
		t.Error("the timeline declares aria-live; a busy agent would make a screen " +
			"reader unusable. Connection state is the thing worth announcing.")
	}
}

// TestManageKeepsEveryControl is the regression guard on the migration.
//
// The redesign moves forms; it removes none. Every element id the old wiring
// and the old tests depend on is still present.
func TestManageKeepsEveryControl(t *testing.T) {
	shell := readAsset(t, "index.html")
	manage := panelBodyForTest(t, shell, "panel-manage")
	if manage == "" {
		t.Fatal("no Manage panel")
	}

	// Every create and open form lives under Manage now.
	for _, id := range []string{
		"form-open-project", "form-open-agent", "form-open-candidate", "form-open-run",
		"form-create-project", "form-create-agent", "form-create-candidate", "form-create-run",
	} {
		if !strings.Contains(manage, `id="`+id+`"`) {
			t.Errorf("Manage is missing %s", id)
		}
	}
	// Including the whole run lifecycle.
	for _, id := range []string{"run-start", "run-complete", "run-fail", "run-cancel", "run-refresh"} {
		if !strings.Contains(manage, `id="`+id+`"`) {
			t.Errorf("Manage is missing the lifecycle control %s", id)
		}
	}
	// Result targets the wiring writes into.
	for _, id := range []string{"project-result", "agent-result", "candidate-result", "run-result", "progress-result"} {
		if !strings.Contains(manage, `id="`+id+`"`) {
			t.Errorf("Manage is missing the result target %s", id)
		}
	}
	// Compare and Promotion stay whole, on their own surfaces.
	for _, id := range []string{
		"form-compare", "compare-result",
		"form-promotion-create", "form-promotion-get", "form-promotion-list",
		"promotion-result", "promotion-history",
	} {
		if !strings.Contains(shell, `id="`+id+`"`) {
			t.Errorf("the shell no longer contains %s", id)
		}
	}
	// Each subsection is reachable.
	for _, section := range []string{
		"manage-open", "manage-project", "manage-agent", "manage-candidate", "manage-evaluation",
	} {
		if !strings.Contains(manage, `data-section="`+section+`"`) {
			t.Errorf("no Manage subtab for %s", section)
		}
		if !strings.Contains(manage, `id="`+section+`"`) {
			t.Errorf("no Manage subsection %s", section)
		}
	}
}

// TestFormsExplainTheirIdentifiers.
//
// A field labelled "Candidate ID" with an empty box next to it tells a
// developer nothing about what belongs there. Every caller-owned identifier
// now carries inline help, and the ones that can be chosen from discovered
// entities offer a list.
func TestFormsExplainTheirIdentifiers(t *testing.T) {
	shell := readAsset(t, "index.html")

	// Each caller-owned identifier input is followed by a hint.
	for _, field := range []string{
		"project-id", "agent-id", "candidate-id", "run-id",
		"run-environment", "run-profile", "promotion-id",
	} {
		anchor := `id="` + field + `"`
		at := strings.Index(shell, anchor)
		if at < 0 {
			t.Errorf("no field %s", field)
			continue
		}
		window := shell[at:min(at+400, len(shell))]
		if !strings.Contains(window, `class="hint"`) {
			t.Errorf("%s has no inline help; a developer should not have to guess "+
				"what arbitrary string belongs there", field)
		}
	}

	// Compare and Promotion offer discovered runs rather than only a text box.
	for _, picker := range []string{
		"compare-reference-pick", "compare-candidate-pick",
		"promotion-reference-pick", "promotion-candidate-pick",
	} {
		if !strings.Contains(shell, `id="`+picker+`"`) {
			t.Errorf("no discovered-run selector %s; copying an opaque identifier "+
				"should not be the only way", picker)
		}
	}
	// The gate limits are policy and say so.
	if !strings.Contains(shell, "These are policy, not evidence") {
		t.Error("the gate limits are not explained as caller-owned policy")
	}
}

// TestRedesignAddsNoDependency.
func TestRedesignAddsNoDependency(t *testing.T) {
	for name, source := range scriptAssets(t) {
		for _, match := range regexp.MustCompile(`from\s+"([^"]+)"`).FindAllStringSubmatch(source, -1) {
			if !strings.HasPrefix(match[1], "./") {
				t.Errorf("%s imports %q; only same-directory modules are allowed", name, match[1])
			}
		}
	}
	shell := readAsset(t, "index.html")
	for _, forbidden := range []string{
		"https://", "http://", "//cdn", "<script src=\"http", "@import", "unpkg", "jsdelivr",
	} {
		if strings.Contains(shell, forbidden) {
			t.Errorf("index.html references %q; no CDN, no external font, no build step", forbidden)
		}
	}
	css := readAsset(t, "styles.css")
	for _, forbidden := range []string{"@import", "url(http", "//fonts."} {
		if strings.Contains(css, forbidden) {
			t.Errorf("styles.css references %q", forbidden)
		}
	}
	// The module split is real: one giant app.js is what this replaced.
	for _, module := range []string{
		"graph.js", "rail.js", "timeline.js", "inspector.js", "discovery.js", "live.js",
	} {
		if _, err := assetFS.ReadFile("assets/" + module); err != nil {
			t.Errorf("expected module %s is missing", module)
		}
	}
}

// TestGraphEntitiesAreReachableByKeyboard.
func TestGraphEntitiesAreReachableByKeyboard(t *testing.T) {
	graph := stripJSComments(readAsset(t, "graph.js"))
	timeline := stripJSComments(readAsset(t, "timeline.js"))
	rail := stripJSComments(readAsset(t, "rail.js"))

	// Edges and target nodes are focusable and named.
	if !strings.Contains(graph, `["tabindex", "0"]`) {
		t.Error("graph entities are not focusable")
	}
	if !strings.Contains(graph, `group.setAttribute("aria-label", described)`) {
		t.Error("graph edges carry no accessible name")
	}
	// Enter and Space activate what a pointer can click.
	for name, source := range map[string]string{"graph.js": graph, "timeline.js": timeline} {
		if !strings.Contains(source, `event.key === "Enter"`) ||
			!strings.Contains(source, `event.key === " "`) {
			t.Errorf("%s does not activate on Enter and Space", name)
		}
	}
	// The rail is a listbox with real options.
	if !strings.Contains(rail, `"listbox"`) || !strings.Contains(rail, `"option"`) {
		t.Error("the rail is not exposed as a listbox of options")
	}
	if !strings.Contains(rail, `"aria-selected"`) {
		t.Error("rail cards do not report their selected state")
	}
}
