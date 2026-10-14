package webui_test

// The Live view against a real browser: the page is served with a /v1 whose
// GET /v1/realtime speaks the stream exactly as httpapi writes it — a
// stream_ready handshake, then named event frames carrying the version 1
// payload — and the rendered DOM is read back from Chrome.
//
// Needs a Chrome or Chromium, found the way chromePath finds one; without it
// these tests skip.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"trustvian-platform/webui"
)

// liveScope is the one run every frame in these tests belongs to.
var liveScope = map[string]string{
	"project_id":   "proj-live",
	"agent_id":     "agent-live",
	"candidate_id": "cand-live",
	"run_id":       "run-live",
	"environment":  "dev",
}

// sseFrame is one named event, framed as httpapi's sseWriter frames it.
func sseFrame(t *testing.T, name string, payload any) string {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return "event: " + name + "\ndata: " + string(encoded) + "\n\n"
}

// streamReady is the handshake the browser client validates.
func streamReady(t *testing.T) string {
	return sseFrame(t, "stream_ready", map[string]any{
		"version": "1", "replay_available": false, "resync_required": true,
	})
}

// observationFrame is one observation event for liveScope.
func observationFrame(t *testing.T, sequence int, fingerprint, target, decision, risk string) string {
	return sseFrame(t, "observation", map[string]any{
		"version": "1",
		"kind":    "observation",
		"scope":   liveScope,
		"observation": map[string]any{
			"sequence":          fmt.Sprint(sequence),
			"record_count":      fmt.Sprint(sequence),
			"behavior_complete": true,
			"fingerprint_id":    fingerprint,
			"behavior": map[string]any{
				"actor_type":         "agent",
				"operation_category": "http",
				"operation_name":     "POST",
				"target_name":        target,
				"target_category":    "service",
			},
			"fidelity":           "transport",
			"behavior_layer":     "",
			"decision":           decision,
			"risk_level":         risk,
			"trust_score":        0.5,
			"anomaly_score":      0.5,
			"anomaly_confidence": 0.5,
			"new_behavior":       false,
		},
	})
}

// holdStream writes frames as one SSE response and keeps it open until the
// browser leaves or the test's server closes. Closing it early would make the
// client reconnect, and a reconnect clears the view by design.
func holdStream(w http.ResponseWriter, r *http.Request, frames string, done <-chan struct{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(frames))
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	select {
	case <-r.Context().Done():
	case <-done:
	}
}

// v1NotFound answers every other /v1 route the page touches as absent. The
// Live view must still render what the stream told it.
func v1NotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"version":"1","error":{"code":"not_found","message":"not in this test"}}`))
}

// projectsPage is the Live session's authoritative snapshot: one empty page.
func projectsPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"version":"1","projects":[],"next_after":""}`))
}

// renderedLive serves the WebUI with the given /v1 and returns the page's
// serialized DOM once release is closed.
//
// Chrome's --dump-dom serializes at the load event, and --virtual-time-budget
// cannot be used here: it waits for pending fetches, and an open event stream
// is one forever. So the shell — the handler's own document, unchanged
// otherwise — gains one image whose response the test holds until release.
// An image still loading delays the load event, which is what lets the stream
// deliver its frames before the DOM is read. driver, when non-empty, is
// served as a same-origin module the shell also loads: the page's own CSP
// (script-src 'self') admits it and nothing is relaxed.
func renderedLive(t *testing.T, v1 http.Handler, release <-chan struct{}, driver string) string {
	t.Helper()
	chrome := chromePath(t)
	ui, err := webui.NewHandler()
	if err != nil {
		t.Fatal(err)
	}
	shell := httptest.NewRecorder()
	ui.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/", nil))
	if shell.Code != http.StatusOK || !strings.Contains(shell.Body.String(), "</body>") {
		t.Fatalf("the shell did not render: %d", shell.Code)
	}
	injected := `<img src="/test-hold" alt="" hidden>`
	if driver != "" {
		injected += `<script type="module" src="/test-driver.js"></script>`
	}
	page := strings.Replace(shell.Body.String(), "</body>", injected+"</body>", 1)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mux := http.NewServeMux()
	mux.Handle("/v1/", v1)
	mux.HandleFunc("/test-hold", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-ctx.Done():
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/test-driver.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write([]byte(driver))
	})
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range shell.Header() {
			if name != "Content-Length" {
				w.Header()[name] = values
			}
		}
		_, _ = w.Write([]byte(page))
	})
	mux.Handle("/", ui)
	server := httptest.NewServer(mux)
	defer server.Close()

	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox",
		"--no-first-run", "--user-data-dir="+t.TempDir(), "--dump-dom", server.URL+"/")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("chrome: %v", err)
	}
	return string(out)
}

// releaseAfter closes a channel a moment after now: long enough for the page
// to apply frames already written, which takes milliseconds.
func releaseAfter(release chan struct{}) {
	time.AfterFunc(1500*time.Millisecond, func() { close(release) })
}

var (
	edgeGroup = regexp.MustCompile(`<g class="([^"]*)" data-edge="([^"]+)"[^>]*>(.*?)</g>`)
	edgeState = regexp.MustCompile(`<text[^>]*class="edge-state"[^>]*>([^<]*)</text>`)
)

// TestLiveStylesEveryDecisionTheStreamSends is finding F4 of #175.
//
// The stream carries policy's wire values — lowercase, as internal/policy
// defines them and the realtime projection copies them. Every one of them
// must reach the canvas as its decision class, the same class the stylesheet
// is written against, and as its short token.
func TestLiveStylesEveryDecisionTheStreamSends(t *testing.T) {
	decisions := []struct {
		wire, class, token string
	}{
		{"allow", "d-allow", "allow"},
		{"observe_only", "d-observe_only", "observe"},
		{"alert", "d-alert", "alert"},
		{"challenge", "d-challenge", "challenge"},
		{"require_approval", "d-require_approval", "approval"},
		{"block", "d-block", "block"},
	}

	done := make(chan struct{})
	defer close(done)
	release := make(chan struct{})
	var once sync.Once
	frames := streamReady(t)
	for i, d := range decisions {
		frames += observationFrame(t, i+1, "fp-"+d.wire, "target-"+d.wire, d.wire, "")
	}
	v1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/realtime":
			once.Do(func() { releaseAfter(release) })
			holdStream(w, r, frames, done)
		case "/v1/projects":
			projectsPage(w)
		default:
			v1NotFound(w)
		}
	})

	dom := renderedLive(t, v1, release, "")
	edges := map[string][2]string{}
	for _, match := range edgeGroup.FindAllStringSubmatch(dom, -1) {
		state := ""
		if s := edgeState.FindStringSubmatch(match[3]); s != nil {
			state = s[1]
		}
		edges[match[2]] = [2]string{match[1], state}
	}
	if len(edges) == 0 {
		t.Fatalf("the canvas drew no edge:\n%.3000s", dom)
	}

	for _, d := range decisions {
		t.Run(d.wire, func(t *testing.T) {
			got, ok := edges["fp-"+d.wire]
			if !ok {
				t.Fatalf("no edge for decision %q", d.wire)
			}
			t.Logf("decision %q -> edge class %q, state %q", d.wire, got[0], got[1])
			if !strings.Contains(" "+got[0]+" ", " "+d.class+" ") {
				t.Errorf("edge for decision %q has class %q, want it to carry %q", d.wire, got[0], d.class)
			}
			if got[1] != d.token {
				t.Errorf("edge for decision %q reads %q, want %q", d.wire, got[1], d.token)
			}
			cell := `<td class="` + d.class + `">` + d.token + `</td>`
			if !strings.Contains(dom, cell) {
				t.Errorf("the timeline has no %s cell for decision %q", cell, d.wire)
			}
		})
	}
}

// reconnectDriver plays the reader: it waits for the global Live stream to
// report Disconnected, then presses the Live view's Reconnect, recording what
// it did on <body> so the dumped DOM says. Test-only, served beside the shell.
const reconnectDriver = `
const deadline = Date.now() + 8000;
function step() {
  const button = document.getElementById("live-reconnect");
  const state = document.getElementById("conn-text").textContent;
  if (button !== null && !button.disabled && state === "Disconnected") {
    document.body.dataset.driver = "clicked";
    button.click();
    return;
  }
  if (Date.now() < deadline) {
    setTimeout(step, 50);
    return;
  }
  document.body.dataset.driver = button === null
    ? "no-live-reconnect-control"
    : "control-not-usable:" + state + ":" + (button.disabled ? "disabled" : "enabled");
}
step();
`

// TestLiveReconnectsAfterTheStreamDrops is finding F5 of #175, in a browser.
//
// The first subscription fails before it ever synchronizes — the case where
// the client stops retrying by design. The reader's Reconnect must open a new
// subscription, and the view must go live on it.
func TestLiveReconnectsAfterTheStreamDrops(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	release := make(chan struct{})
	var once sync.Once
	// Without a reconnect nothing would ever release the page; this bounds
	// that case so it fails with what the page shows instead of a timeout.
	fallback := time.AfterFunc(12*time.Second, func() { once.Do(func() { close(release) }) })
	defer fallback.Stop()

	var mu sync.Mutex
	connections := 0
	frames := streamReady(t) + observationFrame(t, 1, "fp-after", "target-after", "allow", "low")
	v1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/realtime":
			mu.Lock()
			connections++
			n := connections
			mu.Unlock()
			if n == 1 {
				// The stream drops before stream_ready.
				v1NotFound(w)
				return
			}
			once.Do(func() { releaseAfter(release) })
			holdStream(w, r, frames, done)
		case "/v1/projects":
			projectsPage(w)
		default:
			v1NotFound(w)
		}
	})

	dom := renderedLive(t, v1, release, reconnectDriver)
	mu.Lock()
	got := connections
	mu.Unlock()

	driver := regexp.MustCompile(`<body[^>]*data-driver="([^"]*)"`).FindStringSubmatch(dom)
	state := regexp.MustCompile(`<span id="conn-text">([^<]*)</span>`).FindStringSubmatch(dom)
	if driver == nil || state == nil {
		t.Fatalf("the page carries no driver mark or connection state:\n%.3000s", dom)
	}
	t.Logf("driver %q, connections %d, connection state %q", driver[1], got, state[1])
	if driver[1] != "clicked" {
		t.Fatalf("the Live view offered no usable Reconnect after the stream dropped: %s", driver[1])
	}
	if got != 2 {
		t.Errorf("Reconnect opened %d subscriptions in total, want 2", got)
	}
	if state[1] != "Live" {
		t.Errorf("after Reconnect the connection reads %q, want Live", state[1])
	}
	if !strings.Contains(dom, `data-edge="fp-after"`) {
		t.Error("the observation sent on the new subscription was not drawn")
	}
}
