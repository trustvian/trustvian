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
	// designed them for environments and task 066 reused them unchanged for
	// promotions, so the rule is no longer "no collections" — it is "only the
	// two that have a designed contract, both project-scoped and bounded".
	allowed := []string{
		"/v1/projects/${segment(projectID)}/promotions",
		"/v1/projects/${segment(projectID)}/environments",
	}
	for _, route := range allowed {
		if !strings.Contains(raw, route) {
			t.Errorf("api.js no longer calls %s; this check would pass vacuously", route)
		}
	}

	// The unscoped, undesigned shapes stay forbidden. Each would freeze
	// semantics nobody has specified.
	forbidden := []regexp.Regexp{
		*regexp.MustCompile(`"GET",\s*"/v1/projects"`),
		*regexp.MustCompile(`"GET",\s*"/v1/agents"`),
		*regexp.MustCompile(`"GET",\s*"/v1/candidates"`),
		*regexp.MustCompile(`"GET",\s*"/v1/evaluation-runs"`),
		*regexp.MustCompile(`"GET",\s*"/v1/promotions"`),
		*regexp.MustCompile(`[?&](cursor|offset|page|sort|order_by)=`),
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
