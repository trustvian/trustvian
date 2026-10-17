package webui_test

// Task 109 in a real browser: one graph source per actor.
//
// Same harness as live_browser_test.go — the page is served with a /v1 that
// speaks the stream as httpapi writes it, and the DOM is read back from Chrome.

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// actorFrame is one observation for liveScope, performed by actor.
//
// actor "" sends a frame from a server older than task 109: no actor fields.
// The behavior is fixed per fingerprint so two actors' frames of one
// fingerprint describe one behavior, as genuine frames do.
func actorFrame(t *testing.T, sequence int, actor, fingerprint, target, decision string, isNew bool) string {
	t.Helper()
	observation := map[string]any{
		"sequence":          fmt.Sprint(sequence),
		"record_count":      fmt.Sprint(sequence),
		"behavior_complete": true,
		"fingerprint_id":    fingerprint,
		"behavior": map[string]any{
			"actor_type":         "service",
			"operation_category": "tool",
			"operation_name":     "op-" + fingerprint,
			"target_name":        target,
			"target_category":    "service",
		},
		"fidelity":           "semantic",
		"behavior_layer":     "tool",
		"decision":           decision,
		"risk_level":         "low",
		"trust_score":        0.5,
		"anomaly_score":      0.5,
		"anomaly_confidence": 0.5,
		"new_behavior":       isNew,
	}
	if actor != "" {
		observation["actor_id"] = actor
		observation["actor_id_state"] = "carried"
		observation["actor_type"] = "service"
	}
	return sseFrame(t, "observation", map[string]any{
		"version": "1", "kind": "observation", "scope": liveScope, "observation": observation,
	})
}

// liveV1 serves the stream once and answers the selected run's progress with
// the given distinct-behavior count. Every other /v1 route is absent.
func liveV1(frames string, distinct int, release chan struct{}, done <-chan struct{}) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/realtime":
			once.Do(func() { releaseAfter(release) })
			holdStream(w, r, frames, done)
		case "/v1/projects":
			projectsPage(w)
		case "/v1/evaluation-runs/" + liveScope["run_id"] + "/progress":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"version":"1","run_id":%q,"record_count":"%d",`+
				`"distinct_behavior_count":%d,"behavior_complete":true}`,
				liveScope["run_id"], strings.Count(frames, "event: observation"), distinct)
		default:
			v1NotFound(w)
		}
	})
}

var (
	canvasSVG = regexp.MustCompile(`(?s)<svg class="canvas".*?</svg>`)
	// Internal key attributes: how a node is found, not what it shows. Task
	// 109 changed what an edge is keyed by, and its test 3 excludes them.
	internalKeys = regexp.MustCompile(` data-(edge|fingerprint|source|target)="[^"]*"`)
)

// canvasSnapshot is the drawn canvas with its internal key attributes removed:
// every node, edge, text node, accessible name and NEW badge, as markup.
func canvasSnapshot(t *testing.T, dom string) string {
	t.Helper()
	svg := canvasSVG.FindString(dom)
	if svg == "" {
		t.Fatalf("the page drew no canvas:\n%.3000s", dom)
	}
	return internalKeys.ReplaceAllString(svg, "")
}

// singleActorFrames is a run with one actor whose id is the run's agent, the
// shape `trustvian dev` produces when service.name names the agent: three
// behaviors, one of them new, one repeated with a changed decision.
func singleActorFrames(t *testing.T, actor string) string {
	agent := liveScope["agent_id"]
	if actor == "-" {
		agent = ""
	}
	return streamReady(t) +
		actorFrame(t, 1, agent, "fp-crm", "crm.localhost", "allow", false) +
		actorFrame(t, 2, agent, "fp-kb", "kb.localhost", "allow", false) +
		actorFrame(t, 3, agent, "fp-export", "export.localhost", "alert", true) +
		actorFrame(t, 4, agent, "fp-kb", "kb.localhost", "observe_only", false)
}

// TestLiveSingleActorCanvasIsUnchanged is task 109's test 3.
//
// testdata/live-single-actor-canvas.golden is the canvas `main` drew for these
// frames before task 109 (captured at 97e6dbe with this test's snapshot, where
// the frames' actor fields were unknown to the page). A run with one actor
// named like the agent must draw exactly that.
func TestLiveSingleActorCanvasIsUnchanged(t *testing.T) {
	golden := filepath.Join("testdata", "live-single-actor-canvas.golden")
	for _, tt := range []struct{ name, actor string }{
		{"the actor is the agent", liveScope["agent_id"]},
		{"a server older than 109 sends no actor", "-"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			done := make(chan struct{})
			defer close(done)
			release := make(chan struct{})
			dom := renderedLive(t, liveV1(singleActorFrames(t, tt.actor), 3, release, done), release, "")
			got := canvasSnapshot(t, dom)

			if os.Getenv("TRUSTVIAN_UPDATE_LIVE_GOLDEN") == "1" {
				if err := os.WriteFile(golden, []byte(got+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != strings.TrimSuffix(string(want), "\n") {
				t.Errorf("the single-actor canvas differs from main's\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// TestLiveDrawsOneSourcePerActor is task 109's tests 6 and 4 (the source bound).
func TestLiveDrawsOneSourcePerActor(t *testing.T) {
	t.Run("a shared behavior is one line per actor, each its own answer", func(t *testing.T) {
		done := make(chan struct{})
		defer close(done)
		release := make(chan struct{})
		frames := streamReady(t) +
			actorFrame(t, 1, "support-agent", "fp-kb", "kb.localhost", "allow", true) +
			actorFrame(t, 2, "billing-agent", "fp-kb", "kb.localhost", "block", false)
		dom := renderedLive(t, liveV1(frames, 1, release, done), release, "")
		svg := canvasSVG.FindString(dom)

		lines := regexp.MustCompile(`<g class="([^"]*)" data-edge="[^"]+" data-fingerprint="fp-kb"[^>]*aria-label="([^"]*)"`).
			FindAllStringSubmatch(svg, -1)
		if len(lines) != 2 {
			t.Fatalf("drew %d lines for one behavior of two actors, want 2:\n%s", len(lines), svg)
		}
		byActor := map[string]string{}
		for _, line := range lines {
			for _, actor := range []string{"support-agent", "billing-agent"} {
				if strings.Contains(line[2], "by "+actor) {
					byActor[actor] = line[1]
				}
			}
		}
		if !strings.Contains(byActor["support-agent"], "d-allow") ||
			!strings.Contains(byActor["billing-agent"], "d-block") {
			t.Errorf("each line must show its own actor's decision: %v", byActor)
		}
		// NEW only on the line whose frame said so.
		if !strings.Contains(byActor["support-agent"], "edge-new") ||
			strings.Contains(byActor["billing-agent"], "edge-new") {
			t.Errorf("NEW must be on the frame's own line only: %v", byActor)
		}
		if n := strings.Count(svg, `class="node node-target`); n != 1 {
			t.Errorf("drew %d targets, want the one both lines reach", n)
		}
		for _, actor := range []string{"support-agent", "billing-agent"} {
			if !strings.Contains(svg, ">"+actor+"</text>") || !strings.Contains(svg, "<title>Actor "+actor+", service</title>") {
				t.Errorf("no source labelled %s with its type in its title", actor)
			}
		}
		// The header's behavior count is the progress route's, not a count of lines.
		if !regexp.MustCompile(`Behaviors</[^>]+>\s*<[^>]+>1<`).MatchString(dom) {
			t.Errorf("the header does not show the progress route's distinct count 1")
		}
		if !strings.Contains(dom, "One line per actor and behavior") {
			t.Error("the canvas has no legend for several actors")
		}
	})

	t.Run("the source bound evicts and says so", func(t *testing.T) {
		done := make(chan struct{})
		defer close(done)
		release := make(chan struct{})
		frames := streamReady(t)
		for i := range 9 {
			frames += actorFrame(t, i+1, fmt.Sprintf("worker-%d", i), "fp-kb", "kb.localhost", "allow", false)
		}
		dom := renderedLive(t, liveV1(frames, 1, release, done), release, "")
		svg := canvasSVG.FindString(dom)
		if n := strings.Count(svg, `class="node node-agent"`); n != 8 {
			t.Errorf("drew %d sources, want the bound, 8", n)
		}
		if strings.Contains(svg, ">worker-0<") {
			t.Error("the least recently observed actor was not the one evicted")
		}
		if !strings.Contains(dom, "Graph limited to 8 source nodes. 1 additional observed source nodes are not currently drawn.") {
			t.Error("the source bound's saturation is not stated")
		}
	})
}

// narrowingDriver plays the reader of task 109's test 5. It waits for three
// actors, counts every request the page makes from then on, follows
// billing-agent, asks the test server for one more frame from another actor,
// returns to All actors, and writes what it saw to <body data-result>.
const narrowingDriver = `
const original = window.fetch.bind(window);
let requests = 0;
const urls = [];
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const rows = () => Array.from(document.querySelectorAll(".rail-actor"));
const snap = () => ({
  canvas: (document.querySelector("#canvas svg") || {}).outerHTML || "",
  sources: Array.from(document.querySelectorAll("#canvas .node-agent .node-title")).map((n) => n.textContent),
  lines: document.querySelectorAll("#canvas .edge").length,
  timeline: Array.from(document.querySelectorAll("#timeline tbody tr")).map((r) => r.children[1].textContent),
  narrowed: (document.querySelector(".timeline-narrowed") || {}).textContent || "",
  inspector: document.getElementById("inspector").textContent,
  following: Array.from(document.querySelectorAll("#live-strip .strip-item")).map((n) => n.textContent).filter((t) => t.startsWith("Following")),
});
async function run() {
  const deadline = Date.now() + 8000;
  while (rows().length < 3 && Date.now() < deadline) await sleep(50);
  if (rows().length < 3) { document.body.dataset.result = JSON.stringify({ error: "actors not listed", n: rows().length }); return; }
  window.fetch = (...args) => { requests += 1; urls.push(String(args[0])); return original(...args); };
  const all = snap();
  const billing = rows().find((b) => b.querySelector(".rail-actor-name").textContent === "billing-agent");
  billing.click();
  await sleep(150);
  const followed = snap();
  const pressed = rows().filter((b) => b.getAttribute("aria-pressed") === "true").map((b) => b.querySelector(".rail-actor-name").textContent);
  await original("/v1/__test/another-frame");
  await sleep(600);
  const afterOther = snap();
  document.querySelector(".rail-actors-all").click();
  await sleep(150);
  const restored = snap();
  document.body.dataset.result = JSON.stringify({ requests, urls, all, followed, pressed, afterOther, restored });
  await original("/v1/__test/done");
}
run();
`

func TestLiveFollowsOneActorWithoutARequest(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	release := make(chan struct{})
	another := make(chan struct{}, 1)
	var releaseOnce sync.Once
	fallback := time.AfterFunc(20*time.Second, func() { releaseOnce.Do(func() { close(release) }) })
	defer fallback.Stop()

	frames := streamReady(t) +
		actorFrame(t, 1, "support-agent", "fp-kb", "kb.localhost", "allow", true) +
		actorFrame(t, 2, "billing-agent", "fp-kb", "kb.localhost", "allow", false) +
		actorFrame(t, 3, "billing-agent", "fp-export", "export.localhost", "alert", true) +
		actorFrame(t, 4, "accounts-agent", "fp-crm", "crm.localhost", "allow", true) +
		actorFrame(t, 5, "billing-agent", "fp-kb", "kb.localhost", "allow", false)

	v1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/realtime":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte(frames))
			flusher.Flush()
			select {
			case <-another:
				_, _ = w.Write([]byte(actorFrame(t, 6, "support-agent", "fp-new", "new.localhost", "block", true)))
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
			select {
			case <-r.Context().Done():
			case <-done:
			}
		case "/v1/__test/another-frame":
			another <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		case "/v1/__test/done":
			releaseOnce.Do(func() { close(release) })
			w.WriteHeader(http.StatusNoContent)
		case "/v1/projects":
			projectsPage(w)
		case "/v1/agents/" + liveScope["agent_id"]:
			// The run's agent resolves, once: a failed lookup is retried on a
			// later frame, which is a request frames cause, not narrowing.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"version":"1","id":"agent-live","project_id":"proj-live","name":"Support fleet"}`))
		default:
			v1NotFound(w)
		}
	})

	dom := renderedLive(t, v1, release, narrowingDriver)
	match := regexp.MustCompile(`<body[^>]*data-result="([^"]*)"`).FindStringSubmatch(dom)
	if match == nil {
		t.Fatalf("the driver recorded nothing:\n%.3000s", dom)
	}
	var got struct {
		Error                               string
		Requests                            int
		URLs                                []string
		Pressed                             []string
		All, Followed, AfterOther, Restored struct {
			Canvas, Narrowed, Inspector  string
			Sources, Timeline, Following []string
			Lines                        int
		}
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(match[1])), &got); err != nil {
		t.Fatalf("decode %s: %v", match[1], err)
	}
	if got.Error != "" {
		t.Fatalf("driver: %s", got.Error)
	}
	t.Logf("all actors: sources %v, %d lines, timeline %v", got.All.Sources, got.All.Lines, got.All.Timeline)
	t.Logf("following billing-agent: sources %v, %d lines, timeline %v, %q", got.Followed.Sources,
		got.Followed.Lines, got.Followed.Timeline, got.Followed.Narrowed)
	t.Logf("restored: sources %v, %d lines", got.Restored.Sources, got.Restored.Lines)

	if got.Requests != 0 {
		t.Errorf("following an actor and returning made %d requests, want 0: %v", got.Requests, got.URLs)
	}
	if len(got.All.Sources) != 3 || got.All.Lines != 4 {
		t.Errorf("all actors drew %v and %d lines, want three sources and four lines", got.All.Sources, got.All.Lines)
	}
	if strings.Join(got.Followed.Sources, ",") != "billing-agent" || got.Followed.Lines != 2 {
		t.Errorf("following billing-agent drew %v and %d lines, want its one source and two lines",
			got.Followed.Sources, got.Followed.Lines)
	}
	for _, row := range got.Followed.Timeline {
		if row != "billing-agent" {
			t.Errorf("the narrowed timeline shows a row from %q", row)
		}
	}
	if len(got.Followed.Timeline) != 3 || !strings.Contains(got.Followed.Narrowed, "3 of this actor's 3 frames seen live") {
		t.Errorf("narrowed timeline: %v, %q", got.Followed.Timeline, got.Followed.Narrowed)
	}
	// The inspector had opened support-agent's NEW line; following another
	// actor closes it.
	if !strings.Contains(got.All.Inspector, "fp-kb") || strings.Contains(got.Followed.Inspector, "Fingerprint") {
		t.Errorf("the inspector was not closed on another actor's line: before %q, after %q",
			got.All.Inspector, got.Followed.Inspector)
	}
	if strings.Join(got.Pressed, ",") != "billing-agent" {
		t.Errorf("pressed actor rows = %v", got.Pressed)
	}
	if len(got.Followed.Following) != 1 || !strings.Contains(got.Followed.Following[0], "billing-agent") {
		t.Errorf("the header does not say which actor it follows: %v", got.Followed.Following)
	}
	// Another actor's frame, even a NEW one, changes nothing visible while
	// narrowed.
	if got.AfterOther.Canvas != got.Followed.Canvas ||
		strings.Join(got.AfterOther.Timeline, ",") != strings.Join(got.Followed.Timeline, ",") ||
		got.AfterOther.Inspector != got.Followed.Inspector {
		t.Error("another actor's frame changed the narrowed graph, timeline or inspector")
	}
	// All actors restores the run, including the frame that arrived meanwhile.
	if len(got.Restored.Sources) != 3 || got.Restored.Lines != 5 || len(got.Restored.Following) != 0 {
		t.Errorf("All actors restored %v, %d lines, %v; want three sources, five lines, no Following",
			got.Restored.Sources, got.Restored.Lines, got.Restored.Following)
	}
}

func TestLiveActorListIsBoundedAndSaysSo(t *testing.T) {
	done := make(chan struct{})
	defer close(done)
	release := make(chan struct{})
	frames := streamReady(t)
	for i := range 17 {
		frames += actorFrame(t, i+1, fmt.Sprintf("worker-%02d", i), "fp-kb", "kb.localhost", "allow", false)
	}
	dom := renderedLive(t, liveV1(frames, 1, release, done), release, "")
	if n := strings.Count(dom, `class="rail-actor"`); n != 16 {
		t.Errorf("listed %d actors, want the bound, 16", n)
	}
	if strings.Contains(dom, `class="rail-actor-name">worker-00<`) {
		t.Error("the least recently seen actor was not the one evicted")
	}
	if !strings.Contains(dom, "16 actors listed, 1 not listed") || !strings.Contains(dom, "A display limit") {
		t.Error("the actor list's saturation is not stated")
	}
	if !strings.Contains(dom, "Actors seen live") || !strings.Contains(dom, "1 seen live") ||
		!strings.Contains(dom, "last arrival ") {
		t.Error("the list is not labelled as seen live, with counts and arrival times")
	}
}
