package webui_test

// Task 109, decisions 5 and 6, in a real browser: pause keeps the connection,
// buffers frames, and resume folds them without a pulse.

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedLive is a /v1 whose stream the driver steers. The first connection
// writes initial frames and then obeys commands: "frames:N" writes N more,
// "close" ends the response so the browser reconnects. A later connection
// writes reconnected frames and holds. Progress and behavior reads are counted.
type scriptedLive struct {
	t           *testing.T
	initial     string
	reconnected string
	commands    chan string
	release     chan struct{}
	releaseOnce sync.Once
	done        <-chan struct{}

	mu          sync.Mutex
	connections int
	progress    int
	behaviors   int
	sequence    int
}

func newScriptedLive(t *testing.T, initial, reconnected string, done <-chan struct{}) *scriptedLive {
	return &scriptedLive{
		t: t, initial: initial, reconnected: reconnected, done: done,
		commands: make(chan string, 4), release: make(chan struct{}), sequence: 100,
	}
}

func (s *scriptedLive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/realtime":
		s.mu.Lock()
		s.connections++
		n := s.connections
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		if n > 1 {
			_, _ = w.Write([]byte(s.reconnected))
			flusher.Flush()
			select {
			case <-r.Context().Done():
			case <-s.done:
			}
			return
		}
		_, _ = w.Write([]byte(s.initial))
		flusher.Flush()
		for {
			select {
			case command := <-s.commands:
				if command == "close" {
					return
				}
				var count int
				_, _ = fmt.Sscanf(command, "frames:%d", &count)
				var batch strings.Builder
				for range count {
					s.mu.Lock()
					s.sequence++
					sequence := s.sequence
					s.mu.Unlock()
					batch.WriteString(actorFrame(s.t, sequence, "billing-agent",
						"fp-paused", "paused.localhost", "alert", sequence == 101))
				}
				_, _ = w.Write([]byte(batch.String()))
				flusher.Flush()
			case <-r.Context().Done():
				return
			case <-s.done:
				return
			}
		}
	case strings.HasPrefix(r.URL.Path, "/v1/__test/"):
		command := strings.TrimPrefix(r.URL.Path, "/v1/__test/")
		if command == "done" {
			s.releaseOnce.Do(func() { close(s.release) })
		} else {
			s.commands <- command
		}
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/progress"):
		s.mu.Lock()
		s.progress++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1","run_id":"run-live","record_count":"9",` +
			`"distinct_behavior_count":3,"behavior_complete":true}`))
	case strings.HasSuffix(r.URL.Path, "/behaviors"):
		s.mu.Lock()
		s.behaviors++
		s.mu.Unlock()
		v1NotFound(w)
	case r.URL.Path == "/v1/projects":
		projectsPage(w)
	case r.URL.Path == "/v1/agents/"+liveScope["agent_id"]:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1","id":"agent-live","project_id":"proj-live","name":"Support fleet"}`))
	default:
		v1NotFound(w)
	}
}

func (s *scriptedLive) counts() (connections, progress, behaviors int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections, s.progress, s.behaviors
}

// pauseDriverPrelude is shared by the pause drivers: it waits for the first
// frames, then wraps EventSource and fetch so what the page opens, closes and
// requests from here on is counted.
const pauseDriverPrelude = `
const original = window.fetch.bind(window);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const counted = { opened: 0, closed: 0, urls: [] };
const NativeEventSource = window.EventSource;
window.EventSource = class extends NativeEventSource {
  constructor(...args) { super(...args); counted.opened += 1; }
};
const nativeClose = NativeEventSource.prototype.close;
NativeEventSource.prototype.close = function () { counted.closed += 1; return nativeClose.call(this); };
const command = (name) => original("/v1/__test/" + name);
const text = (id) => (document.getElementById(id) || {}).textContent || "";
const press = (target, init) => target.dispatchEvent(new KeyboardEvent("keydown", Object.assign({ key: "p", bubbles: true }, init)));
const state = () => ({
  pressed: document.getElementById("live-pause").getAttribute("aria-pressed"),
  pausedShown: !document.getElementById("conn-paused").hidden,
  chip: text("conn-text"),
  line: document.getElementById("live-pause-state").hidden ? "" : text("live-pause-state"),
  announce: text("live-announce"),
  scope: text("canvas-scope"),
  canvas: (document.querySelector("#canvas svg") || {}).outerHTML || "",
  fingerprints: Array.from(document.querySelectorAll("#canvas .edge")).map((e) => e.dataset.fingerprint),
  rows: document.querySelectorAll("#timeline tbody tr").length,
  pulses: document.querySelectorAll("#canvas .pulse").length,
  inspector: text("inspector"),
});
async function ready() {
  const deadline = Date.now() + 8000;
  while (document.querySelectorAll("#canvas .edge").length === 0 && Date.now() < deadline) await sleep(50);
  await sleep(900);  // let the first frames' pulses finish
}
function finish(result) {
  result.counted = counted;
  document.body.dataset.result = JSON.stringify(result);
  command("done");
}
`

const pauseDriver = pauseDriverPrelude + `
async function run() {
  await ready();
  const before = state();
  press(document.body);
  const paused = state();
  // Typing in a field or holding a modifier never toggles it.
  const field = document.getElementById("watch-run-id");
  field.focus();
  press(field);
  press(document.body, { ctrlKey: true });
  press(document.body, { key: "P", shiftKey: true });
  field.blur();
  const stillPaused = state();
  await command("frames:5");
  await sleep(700);
  const buffered = state();
  counted.urls = [];
  const urlsBefore = counted.urls.length;
  window.fetch = (...args) => { counted.urls.push(String(args[0])); return original(...args); };
  document.getElementById("live-pause").click();
  const resumedNow = state();
  await sleep(500);
  const resumed = state();
  finish({ before, paused, stillPaused, buffered, resumedNow, resumed, urlsBefore });
}
run();
`

type pauseState struct {
	Pressed, Chip, Line, Announce, Scope, Canvas, Inspector string
	PausedShown                                             bool
	Fingerprints                                            []string
	Rows, Pulses                                            int
}

type pauseResult struct {
	Counted struct {
		Opened, Closed int
		URLs           []string
	}
	Before, Paused, StillPaused, Buffered, ResumedNow, Resumed pauseState
	Overrun, Reconnected                                       pauseState
}

func decodePauseResult(t *testing.T, dom string) pauseResult {
	t.Helper()
	match := regexp.MustCompile(`<body[^>]*data-result="([^"]*)"`).FindStringSubmatch(dom)
	if match == nil {
		t.Fatalf("the driver recorded nothing:\n%.3000s", dom)
	}
	var got pauseResult
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &got); err != nil {
		t.Fatalf("decode %s: %v", match[1], err)
	}
	return got
}

func initialFleet(t *testing.T) string {
	return streamReady(t) +
		actorFrame(t, 1, "support-agent", "fp-kb", "kb.localhost", "allow", true) +
		actorFrame(t, 2, "billing-agent", "fp-kb", "kb.localhost", "allow", false) +
		actorFrame(t, 3, "accounts-agent", "fp-crm", "crm.localhost", "allow", true)
}

// TestLivePauseKeepsTheConnectionAndResumeDoesNotReplay is task 109's tests
// 7, 8 and 11.
func TestLivePauseKeepsTheConnectionAndResumeDoesNotReplay(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	live := newScriptedLive(t, initialFleet(t), "", done)
	fallback := time.AfterFunc(25*time.Second, func() { live.releaseOnce.Do(func() { close(live.release) }) })
	defer fallback.Stop()

	dom := renderedLive(t, live, live.release, pauseDriver)
	got := decodePauseResult(t, dom)
	connections, progress, behaviors := live.counts()
	t.Logf("paused: %q / %q", got.Buffered.Line, got.Paused.Announce)
	t.Logf("resumed: %q; requests during resume %v; connections %d", got.Resumed.Announce, got.Counted.URLs, connections)

	// Pause.
	if got.Paused.Pressed != "true" || !got.Paused.PausedShown || got.Paused.Chip != "Live" {
		t.Errorf("paused: aria-pressed %q, PAUSED shown %v, chip %q", got.Paused.Pressed, got.Paused.PausedShown, got.Paused.Chip)
	}
	if got.Paused.Announce != "Live view paused. The connection stays open." {
		t.Errorf("pause announced %q", got.Paused.Announce)
	}
	if got.StillPaused.Pressed != "true" {
		t.Error("P in a text field, or with a modifier, toggled pause")
	}
	// Frames while paused are counted and not drawn.
	if got.Buffered.Line != "5 frames since pause · seen live, all runs · 5 in this run." {
		t.Errorf("the pause line reads %q", got.Buffered.Line)
	}
	if got.Buffered.Canvas != got.Before.Canvas || got.Buffered.Rows != got.Before.Rows || got.Buffered.Pulses != 0 {
		t.Error("a frame arriving while paused changed the canvas or the timeline, or pulsed")
	}
	if got.Buffered.Announce != got.Paused.Announce {
		t.Errorf("a frame was announced: %q", got.Buffered.Announce)
	}
	// Pause is never a disconnect.
	if got.Counted.Opened != 0 || got.Counted.Closed != 0 || connections != 1 {
		t.Errorf("pause and resume opened %d and closed %d EventSources (%d connections), want none",
			got.Counted.Opened, got.Counted.Closed, connections)
	}
	// Resume: no pulse, the buffered frames drawn, one progress read, no
	// behaviors read.
	if got.ResumedNow.Pulses != 0 {
		t.Errorf("resume created %d pulse elements", got.ResumedNow.Pulses)
	}
	if !slices.Contains(got.Resumed.Fingerprints, "fp-paused") || got.Resumed.Rows != got.Before.Rows+5 {
		t.Errorf("the buffered frames were not drawn: %v, %d rows", got.Resumed.Fingerprints, got.Resumed.Rows)
	}
	progressURLs := 0
	for _, url := range got.Counted.URLs {
		if strings.HasSuffix(url, "/progress") {
			progressURLs++
		}
		if strings.HasSuffix(url, "/behaviors") {
			t.Errorf("resume read the behaviors: %s", url)
		}
	}
	if progressURLs != 1 || behaviors != 0 {
		t.Errorf("resume made %d progress reads (%d behaviors reads in all), want exactly one and none",
			progressURLs, behaviors)
	}
	_ = progress
	// What was being read stays open: the buffered NEW behavior does not take
	// the inspector from the line opened before the pause.
	if !strings.Contains(got.Resumed.Inspector, "fp-kb") {
		t.Errorf("resume replaced the inspected line: %q", got.Resumed.Inspector)
	}
	if got.Resumed.Announce != "Live view resumed. 5 frames arrived while paused." {
		t.Errorf("resume announced %q", got.Resumed.Announce)
	}
	if got.Resumed.Pressed != "false" || got.Resumed.PausedShown || got.Resumed.Line != "" {
		t.Errorf("resumed: aria-pressed %q, PAUSED shown %v, line %q", got.Resumed.Pressed, got.Resumed.PausedShown, got.Resumed.Line)
	}
}

const overrunDriver = pauseDriverPrelude + `
async function run() {
  await ready();
  document.getElementById("live-pause").click();
  await command("frames:1025");
  const deadline = Date.now() + 8000;
  while (!text("live-pause-state").includes("1025 frames") && Date.now() < deadline) await sleep(50);
  const overrun = state();
  document.getElementById("live-pause").click();
  await sleep(300);
  finish({ overrun, resumed: state() });
}
run();
`

// TestLivePauseOverrunIsStated is task 109's test 9, at the real bound.
func TestLivePauseOverrunIsStated(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	live := newScriptedLive(t, initialFleet(t), "", done)
	fallback := time.AfterFunc(25*time.Second, func() { live.releaseOnce.Do(func() { close(live.release) }) })
	defer fallback.Stop()

	got := decodePauseResult(t, renderedLive(t, live, live.release, overrunDriver))
	t.Logf("while paused: %q", got.Overrun.Line)
	t.Logf("on resume: %q", got.Resumed.Announce)
	if !strings.Contains(got.Overrun.Line, "1025 frames since pause") ||
		!strings.Contains(got.Overrun.Line, "Pause buffer full (1,024): this view will start again from now when you resume.") {
		t.Errorf("the overrun is not stated while paused: %q", got.Overrun.Line)
	}
	want := "Paused longer than this view holds: 1025 frames were not kept. Retained history is in Evidence."
	if got.Resumed.Announce != want || got.Resumed.Line != want {
		t.Errorf("resume after an overrun: announced %q, shown %q", got.Resumed.Announce, got.Resumed.Line)
	}
	if len(got.Resumed.Fingerprints) != 0 || got.Resumed.Rows != 0 {
		t.Errorf("the viewport was not cleared: %v, %d rows", got.Resumed.Fingerprints, got.Resumed.Rows)
	}
}

const reconnectWhilePausedDriver = pauseDriverPrelude + `
async function run() {
  await ready();
  const before = state();
  document.getElementById("live-pause").click();
  await command("close");
  const deadline = Date.now() + 8000;
  while (!text("live-pause-state").includes("previous connection") && Date.now() < deadline) await sleep(50);
  await sleep(300);
  const reconnected = state();
  document.getElementById("live-pause").click();
  await sleep(300);
  finish({ before, reconnected, resumed: state() });
}
run();
`

// TestLiveReconnectWhilePausedKeepsThePreviousPicture is task 109's test 10.
func TestLiveReconnectWhilePausedKeepsThePreviousPicture(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	after := streamReady(t) + actorFrame(t, 1, "support-agent", "fp-after", "after.localhost", "allow", false)
	live := newScriptedLive(t, initialFleet(t), after, done)
	fallback := time.AfterFunc(25*time.Second, func() { live.releaseOnce.Do(func() { close(live.release) }) })
	defer fallback.Stop()

	got := decodePauseResult(t, renderedLive(t, live, live.release, reconnectWhilePausedDriver))
	connections, _, _ := live.counts()
	t.Logf("reconnected while paused: %q / %q", got.Reconnected.Scope, got.Reconnected.Line)
	if connections != 2 {
		t.Fatalf("connections = %d, want the reconnect", connections)
	}
	if got.Reconnected.Canvas != got.Before.Canvas || !strings.Contains(got.Reconnected.Scope, "From the previous connection.") ||
		!strings.Contains(got.Reconnected.Line, "this picture is from the previous connection") {
		t.Errorf("the frozen picture is not kept and labelled: scope %q, line %q", got.Reconnected.Scope, got.Reconnected.Line)
	}
	// Resume applies the reset, then the new connection's frames.
	if strings.Join(got.Resumed.Fingerprints, ",") != "fp-after" || got.Resumed.Rows != 1 {
		t.Errorf("after resume: %v, %d rows; want only the new connection's frame", got.Resumed.Fingerprints, got.Resumed.Rows)
	}
}
