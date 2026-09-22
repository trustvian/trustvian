package main

// Task 061: the ordering and bounding guarantees.
//
// These drive the real model against a real httptest.Server, but never a real
// terminal. tea.Cmd is just func() tea.Msg, so the harness below runs the
// program loop itself — which makes every ordering assertion deterministic
// instead of a race against a rendering goroutine.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ---------------------------------------------------------------------
// A deterministic driver for the model
// ---------------------------------------------------------------------

// modelDriver runs Update/Cmd cycles by hand.
//
// Commands run in goroutines, as the framework runs them, so concurrency
// between "read the next frame" and "fetch authoritative state" is real — that
// concurrency is the property under test. Messages are serialized through one
// channel and applied one at a time, which is also what the framework does.
type modelDriver struct {
	t     *testing.T
	model *tuiModel

	msgs chan tea.Msg
	wg   sync.WaitGroup
}

func newDriver(t *testing.T, model *tuiModel) *modelDriver {
	t.Helper()
	d := &modelDriver{t: t, model: model, msgs: make(chan tea.Msg, 256)}
	d.run(model.Init())
	return d
}

// run executes a command off the update loop, like the framework does.
func (d *modelDriver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		if msg := cmd(); msg != nil {
			d.msgs <- msg
		}
	}()
}

// step applies exactly one message, returning false on timeout.
func (d *modelDriver) step(within time.Duration) bool {
	d.t.Helper()
	select {
	case msg := <-d.msgs:
		// tea.Batch produces a BatchMsg carrying several commands; the
		// framework runs each, and so does this.
		if batch, isBatch := msg.(tea.BatchMsg); isBatch {
			for _, cmd := range batch {
				d.run(cmd)
			}
			return true
		}
		_, cmd := d.model.Update(msg)
		d.run(cmd)
		return true
	case <-time.After(within):
		return false
	}
}

// settleUntil steps until the condition holds or the budget runs out.
func (d *modelDriver) settleUntil(condition func() bool, budget time.Duration) bool {
	d.t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		if !d.step(100 * time.Millisecond) {
			continue
		}
	}
	return condition()
}

func (d *modelDriver) stop() {
	d.model.releaseStream()
	d.wg.Wait()
}

// ---------------------------------------------------------------------
// A controllable fake control plane
// ---------------------------------------------------------------------

type fakePlane struct {
	t      *testing.T
	server *httptest.Server

	mu           sync.Mutex
	runRequests  int
	progRequests int
	sseRequests  int
	lastEventIDs []string
	runBody      string
	progressBody string
	runStatus    int
	holdProgress chan struct{}
	streams      []*fakeStream
	sseHandler   func(w http.ResponseWriter, r *http.Request, s *fakeStream)

	// heartbeat mirrors the real server: task 059 sends comment frames on a
	// quiet healthy stream, so a fake that sends nothing at all is not a
	// quiet healthy stream — it is a dead one.
	heartbeat time.Duration
}

// fakeStream is one live SSE connection the test can write into.
//
// Frames are posted to the handler rather than written directly. An
// http.ResponseWriter belongs to its handler goroutine and is only valid while
// that handler is running — writing from the test goroutine races the server
// finishing the request, which the race detector caught here before this was
// restructured. Everything below hands bytes to the handler; the handler
// writes them.
type fakeStream struct {
	outbox chan string
	closed chan struct{}
	done   chan struct{}
	once   sync.Once
}

func newFakeStream() *fakeStream {
	return &fakeStream{
		outbox: make(chan string, 512),
		closed: make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (s *fakeStream) send(event, data string) {
	s.sendRaw(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))
}

func (s *fakeStream) sendRaw(raw string) {
	select {
	case s.outbox <- raw:
	case <-s.done:
		// The handler is gone; there is nobody to write it.
	case <-s.closed:
	}
}

func (s *fakeStream) hangUp() { s.once.Do(func() { close(s.closed) }) }

// serve is the handler-side loop. Every write happens here.
func (s *fakeStream) serve(w http.ResponseWriter, flusher http.Flusher, requestDone <-chan struct{}) {
	for {
		select {
		case frame := <-s.outbox:
			fmt.Fprint(w, frame)
			flusher.Flush()
		case <-s.closed:
			return
		case <-requestDone:
			return
		}
	}
}

func newFakePlane(t *testing.T) *fakePlane {
	t.Helper()
	p := &fakePlane{
		t:            t,
		runStatus:    200,
		runBody:      `{"version":"1","id":"run-42","candidate_id":"cand-2","environment":"local","behavioral_profile":"checkout-agent","status":"running"}`,
		progressBody: `{"version":"1","run_id":"run-42","status":"running","record_count":"18","behavior_observation_count":"18","distinct_behavior_count":11,"behavior_complete":true,"next_ingest_sequence":"19"}`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/realtime", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.sseRequests++
		p.lastEventIDs = append(p.lastEventIDs, r.Header.Get("Last-Event-ID"))
		handler := p.sseHandler
		p.mu.Unlock()

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server response is not flushable")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher.Flush()

		stream := newFakeStream()
		p.mu.Lock()
		p.streams = append(p.streams, stream)
		p.mu.Unlock()
		defer close(stream.done)

		if handler != nil {
			handler(w, r, stream)
			return
		}
		stream.send("stream_ready", `{"version":"1","replay_available":false,"resync_required":true}`)

		p.mu.Lock()
		heartbeat := p.heartbeat
		p.mu.Unlock()
		if heartbeat > 0 {
			stopBeat := make(chan struct{})
			defer close(stopBeat)
			go func() {
				ticker := time.NewTicker(heartbeat)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						stream.sendRaw(": keepalive\n\n")
					case <-stopBeat:
						return
					}
				}
			}()
		}

		stream.serve(w, flusher, r.Context().Done())
	})

	mux.HandleFunc("GET /v1/evaluation-runs/{id}", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.runRequests++
		status, body := p.runStatus, p.runBody
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})

	mux.HandleFunc("GET /v1/evaluation-runs/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.progRequests++
		hold, body := p.holdProgress, p.progressBody
		p.mu.Unlock()

		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		fmt.Fprint(w, body)
	})

	p.server = httptest.NewServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

// serveSSE replaces the default SSE handler.
func (p *fakePlane) serveSSE(handler func(w http.ResponseWriter, r *http.Request, s *fakeStream)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sseHandler = handler
}

func (p *fakePlane) counts() (sse, run, progress int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sseRequests, p.runRequests, p.progRequests
}

// awaitStream waits for a connection while keeping the model loop running.
//
// streamAt alone busy-waits, which is fine for the first connection (Init's
// command opens it without needing an Update) but deadlocks for a reconnect:
// the tick message sits in the queue with nobody to apply it.
func (d *modelDriver) awaitStream(p *fakePlane, index int) *fakeStream {
	d.t.Helper()
	if !d.settleUntil(func() bool {
		sse, _, _ := p.counts()
		return sse > index
	}, 15*time.Second) {
		sse, _, _ := p.counts()
		d.t.Fatalf("stream %d never opened; %d subscriptions so far", index, sse)
	}
	return p.streamAt(index)
}

func (p *fakePlane) streamAt(index int) *fakeStream {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		if len(p.streams) > index {
			s := p.streams[index]
			p.mu.Unlock()
			return s
		}
		p.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	p.t.Fatalf("stream %d never opened", index)
	return nil
}

func (p *fakePlane) eventIDHeaders() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lastEventIDs...)
}

// newTestModel wires the model against a fake plane.
func newTestModel(t *testing.T, p *fakePlane) (*tuiModel, context.CancelFunc) {
	t.Helper()
	return newTestModelIdle(t, p, sseReadIdleTimeout)
}

// newTestModelIdle shortens the active-stream liveness bound.
//
// Reaching into the opener's unexported field rather than adding a flag: a
// liveness tolerance is not something a user should configure, and a flag
// added for tests becomes operational surface nobody asked for.
func newTestModelIdle(t *testing.T, p *fakePlane, idle time.Duration) (*tuiModel, context.CancelFunc) {
	t.Helper()
	client, err := newPlatformClient(p.server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("newPlatformClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	opener := newSSEOpener(client.baseURL)
	opener.idleTimeout = idle

	model := newTUIModel(ctx, "run-42", opener, &httpAuthoritativeReader{client: client})
	return model, cancel
}

func observationFrame(sequence string, isNew bool) string {
	return fmt.Sprintf(`{"version":"1","kind":"observation","scope":{"run_id":"run-42"},`+
		`"observation":{"sequence":"%s","record_count":"%s","behavior_complete":true,`+
		`"fingerprint_id":"fp-1","behavior":{"operation_category":"tool",`+
		`"operation_name":"shell.execute","target_name":"build-host"},`+
		`"decision":"allow","risk_level":"medium","trust_score":0.81,`+
		`"anomaly_score":0.22,"anomaly_confidence":0.9,"new_behavior":%t}}`,
		sequence, sequence, isNew)
}

// ---------------------------------------------------------------------
// The ordering guarantee
// ---------------------------------------------------------------------

// TestSubscribeBeforeResyncAndKeepDraining is the load-bearing test.
//
// It holds /progress open, emits an observation while the resync is in flight,
// then releases it. A fetch-first implementation loses that observation
// entirely; an implementation that stops draining during resync never receives
// it. Both fail here.
func TestSubscribeBeforeResyncAndKeepDraining(t *testing.T) {
	plane := newFakePlane(t)
	plane.holdProgress = make(chan struct{})

	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)
	defer driver.stop()

	// The subscription must exist before any authoritative read is issued.
	stream := plane.streamAt(0)
	if _, runs, _ := plane.counts(); runs != 0 {
		t.Fatalf("%d run requests before the stream opened; subscribe must come first", runs)
	}

	if !driver.settleUntil(func() bool { return model.state == stateResyncing },
		5*time.Second) {
		t.Fatalf("never reached resyncing; state = %v", model.state)
	}

	// Emitted while /progress is deliberately held. This is the frame a
	// fetch-first client would never see.
	stream.send("observation", observationFrame("18", true))

	// It must be buffered, not applied, and definitely not lost: the model is
	// still draining while its own HTTP is blocked.
	if !driver.settleUntil(func() bool { return len(model.pending) == 1 },
		5*time.Second) {
		t.Fatalf("observation was not buffered during resync; pending = %d", len(model.pending))
	}

	close(plane.holdProgress)

	if !driver.settleUntil(func() bool {
		return model.state == stateLive && len(model.live) == 1
	}, 5*time.Second) {
		t.Fatalf("state = %v, live rows = %d; want LIVE with the buffered row applied",
			model.state, len(model.live))
	}

	if model.live[0].sequence != "18" {
		t.Errorf("live[0].sequence = %q, want 18", model.live[0].sequence)
	}
	if !model.snapshot.taken {
		t.Error("authoritative snapshot was never applied")
	}
	if model.snapshot.progress.DistinctBehaviorCount != 11 {
		t.Errorf("distinct behaviors = %d, want the authoritative 11",
			model.snapshot.progress.DistinctBehaviorCount)
	}
}

// TestReconnectResyncsAndClearsRows covers the full reconnect cycle.
func TestReconnectResyncsAndClearsRows(t *testing.T) {
	plane := newFakePlane(t)
	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)
	defer driver.stop()

	first := plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	first.send("observation", observationFrame("1", true))
	if !driver.settleUntil(func() bool { return len(model.live) == 1 }, 5*time.Second) {
		t.Fatal("observation A never appeared")
	}

	// Drop the connection the way task 059 drops a slow consumer.
	first.hangUp()

	if !driver.settleUntil(func() bool { return model.state == stateReconnecting },
		5*time.Second) {
		t.Fatalf("state = %v, want RECONNECTING after the stream closed", model.state)
	}
	// Rows clear immediately: the screen must stop implying the list is
	// current the moment the stream is gone.
	if len(model.live) != 0 {
		t.Errorf("live rows = %d after disconnect, want 0", len(model.live))
	}

	second := driver.awaitStream(plane, 1)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 10*time.Second) {
		t.Fatalf("never returned to LIVE; state = %v", model.state)
	}

	second.send("observation", observationFrame("2", false))
	if !driver.settleUntil(func() bool { return len(model.live) == 1 }, 5*time.Second) {
		t.Fatalf("observation B never appeared; live = %d", len(model.live))
	}
	if model.live[0].sequence != "2" {
		t.Errorf("live[0] = %q, want only B; the old row was retained across a gap",
			model.live[0].sequence)
	}

	sse, runs, progress := plane.counts()
	if sse < 2 {
		t.Errorf("SSE subscriptions = %d, want at least 2", sse)
	}
	if runs < 2 || progress < 2 {
		t.Errorf("authoritative reads = %d/%d, want a resync on every connection",
			runs, progress)
	}

	// No replay cursor, ever.
	for i, header := range plane.eventIDHeaders() {
		if header != "" {
			t.Errorf("connection %d sent Last-Event-ID %q; task 059 has no replay", i, header)
		}
	}
}

// TestQuietStreamDoesNotPoll proves realtime drives the dashboard.
func TestQuietStreamDoesNotPoll(t *testing.T) {
	plane := newFakePlane(t)
	// A real quiet stream still heartbeats. Without this the fake would be a
	// dead connection, and the test would pass for the wrong reason once the
	// client learned to notice silence.
	plane.heartbeat = 20 * time.Millisecond

	model, _ := newTestModelIdle(t, plane, 2*time.Second)
	driver := newDriver(t, model)
	defer driver.stop()

	plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	_, runsAfterResync, progressAfterResync := plane.counts()
	if runsAfterResync != 1 || progressAfterResync != 1 {
		t.Fatalf("resync issued %d run / %d progress requests, want exactly 1 each",
			runsAfterResync, progressAfterResync)
	}

	// Leave the stream quiet and keep the loop running. A refresh ticker would
	// show up as additional requests here.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		driver.step(50 * time.Millisecond)
	}

	_, runs, progress := plane.counts()
	if runs != 1 || progress != 1 {
		t.Fatalf("a quiet stream issued %d run / %d progress requests; "+
			"the dashboard is polling", runs, progress)
	}
}

// TestTerminalLifecycleTriggersExactlyOneResync keeps the final read
// event-driven rather than the start of a loop.
func TestTerminalLifecycleTriggersExactlyOneResync(t *testing.T) {
	for _, kind := range []string{
		kindEvaluationCompleted, kindEvaluationFailed, kindEvaluationCancelled,
	} {
		t.Run(kind, func(t *testing.T) {
			plane := newFakePlane(t)
			model, _ := newTestModel(t, plane)
			driver := newDriver(t, model)
			defer driver.stop()

			stream := plane.streamAt(0)
			if !driver.settleUntil(func() bool { return model.state == stateLive },
				5*time.Second) {
				t.Fatalf("never went live; state = %v", model.state)
			}

			plane.mu.Lock()
			plane.runBody = strings.Replace(plane.runBody, `"status":"running"`,
				`"status":"completed"`, 1)
			plane.mu.Unlock()

			stream.send(kind, fmt.Sprintf(
				`{"version":"1","kind":%q,"scope":{"run_id":"run-42"},`+
					`"evaluation":{"status":"completed"}}`, kind))

			if !driver.settleUntil(func() bool {
				_, runs, _ := plane.counts()
				return runs == 2
			}, 5*time.Second) {
				_, runs, _ := plane.counts()
				t.Fatalf("run requests = %d, want 2 (one resync, one final)", runs)
			}

			// And nothing periodic follows it.
			deadline := time.Now().Add(1200 * time.Millisecond)
			for time.Now().Before(deadline) {
				driver.step(50 * time.Millisecond)
			}
			_, runs, progress := plane.counts()
			if runs != 2 || progress != 2 {
				t.Fatalf("after the terminal event: %d run / %d progress requests, want 2 each",
					runs, progress)
			}
		})
	}
}

// TestPendingOverflowReconnectsRatherThanDropping proves the transport bound
// never silently loses a notification.
func TestPendingOverflowReconnectsRatherThanDropping(t *testing.T) {
	plane := newFakePlane(t)
	plane.holdProgress = make(chan struct{})

	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)
	defer driver.stop()

	stream := plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateResyncing },
		5*time.Second) {
		t.Fatalf("never reached resyncing; state = %v", model.state)
	}

	// More than the client will hold while its resync is blocked.
	for i := range tuiPendingEventCapacity * 3 {
		stream.send("observation", observationFrame(fmt.Sprint(i+1), false))
	}

	if !driver.settleUntil(func() bool {
		return model.state == stateReconnecting || model.state == stateFatal
	}, 10*time.Second) {
		t.Fatalf("state = %v with %d pending; overflow must abandon the stream",
			model.state, len(model.pending))
	}
	if len(model.pending) > tuiPendingEventCapacity {
		t.Errorf("pending grew to %d, over the %d bound",
			len(model.pending), tuiPendingEventCapacity)
	}
	close(plane.holdProgress)
}

// ---------------------------------------------------------------------
// Blocker 1: a terminal event buffered during resync still gets its
// final authoritative read
// ---------------------------------------------------------------------

// TestBufferedTerminalEventStillTriggersFinalResync covers the case the
// reviewed head lost.
//
// A run that finishes while the *first* resync is still in flight has its
// completion buffered like any other frame. Replaying that frame updated the
// status from the event and dropped the command that performs the final
// authoritative read — so the counts stayed at whatever the stale first
// snapshot said, forever, with m.resyncing stuck true so nothing could re-arm
// it. The screen showed "completed" beside numbers from before completion.
func TestBufferedTerminalEventStillTriggersFinalResync(t *testing.T) {
	for _, terminal := range []struct{ kind, status string }{
		{kindEvaluationCompleted, "completed"},
		{kindEvaluationFailed, "failed"},
		{kindEvaluationCancelled, "cancelled"},
	} {
		t.Run(terminal.kind, func(t *testing.T) {
			plane := newFakePlane(t)
			plane.holdProgress = make(chan struct{})

			model, _ := newTestModel(t, plane)
			driver := newDriver(t, model)
			defer driver.stop()

			stream := plane.streamAt(0)
			if !driver.settleUntil(func() bool { return model.state == stateResyncing },
				5*time.Second) {
				t.Fatalf("never reached resyncing; state = %v", model.state)
			}

			// The run finishes while the first resync is blocked.
			stream.send(terminal.kind, fmt.Sprintf(
				`{"version":"1","kind":%q,"scope":{"run_id":"run-42"},`+
					`"evaluation":{"status":%q}}`, terminal.kind, terminal.status))

			if !driver.settleUntil(func() bool { return len(model.pending) == 1 },
				5*time.Second) {
				t.Fatalf("terminal event was not buffered; pending = %d", len(model.pending))
			}

			// The authoritative answer moves on while the first read is held,
			// so a stale snapshot is distinguishable from a fresh one.
			plane.mu.Lock()
			plane.runBody = fmt.Sprintf(`{"version":"1","id":"run-42","candidate_id":"cand-2",`+
				`"environment":"local","behavioral_profile":"checkout-agent","status":%q}`,
				terminal.status)
			plane.progressBody = `{"version":"1","run_id":"run-42","status":"` + terminal.status +
				`","record_count":"19","behavior_observation_count":"19",` +
				`"distinct_behavior_count":12,"behavior_complete":true,"next_ingest_sequence":"20"}`
			plane.mu.Unlock()

			close(plane.holdProgress)

			// A second authoritative read must happen, and its values must win.
			if !driver.settleUntil(func() bool {
				_, runs, progress := plane.counts()
				return runs >= 2 && progress >= 2 && model.snapshot.progress.RecordCount == "19"
			}, 10*time.Second) {
				_, runs, progress := plane.counts()
				t.Fatalf("no final resync after a buffered %s: runs=%d progress=%d records=%q",
					terminal.kind, runs, progress, model.snapshot.progress.RecordCount)
			}

			if model.snapshot.run.Status != terminal.status {
				t.Errorf("status = %q, want %q", model.snapshot.run.Status, terminal.status)
			}
			if model.snapshot.progress.DistinctBehaviorCount != 12 {
				t.Errorf("distinct behaviors = %d, want the final authoritative 12",
					model.snapshot.progress.DistinctBehaviorCount)
			}
			// And the guard is released, not stuck.
			if model.resyncing {
				t.Error("m.resyncing is still true; a later terminal event could never resync")
			}

			// Exactly one final read, and nothing periodic after it.
			deadline := time.Now().Add(1200 * time.Millisecond)
			for time.Now().Before(deadline) {
				driver.step(50 * time.Millisecond)
			}
			_, runs, progress := plane.counts()
			if runs != 2 || progress != 2 {
				t.Fatalf("authoritative reads = %d run / %d progress, want exactly 2 each",
					runs, progress)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Blocker 2: active-stream liveness
// ---------------------------------------------------------------------

// TestSilentActiveStreamIsDetected proves a half-open connection cannot leave
// the dashboard showing LIVE forever.
//
// http.Transport.IdleConnTimeout does not cover this: it governs unused
// keep-alive connections in the pool, not an active response body. A stream
// that stops delivering bytes without closing produces no error and no EOF,
// so without a read-inactivity bound the reader simply blocks and the screen
// keeps asserting state it can no longer refresh.
func TestSilentActiveStreamIsDetected(t *testing.T) {
	plane := newFakePlane(t)
	// Deliberately no heartbeat: this is the dead-but-open case.
	model, _ := newTestModelIdle(t, plane, 250*time.Millisecond)
	driver := newDriver(t, model)
	defer driver.stop()

	plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	// Nothing more is sent. The client must notice.
	if !driver.settleUntil(func() bool { return model.state != stateLive }, 10*time.Second) {
		t.Fatal("the dashboard stayed LIVE against a silent stream")
	}

	// And it reconnects rather than giving up, since it had been live.
	if !driver.settleUntil(func() bool {
		sse, _, _ := plane.counts()
		return sse >= 2
	}, 15*time.Second) {
		sse, _, _ := plane.counts()
		t.Fatalf("subscriptions = %d; a silent stream must be resubscribed", sse)
	}
}

// TestHeartbeatKeepsAQuietStreamLive is the other half: liveness must
// tolerate a genuinely quiet evaluation.
//
// Comment frames never become domain events, so a client counting only events
// would disconnect a healthy run that simply had nothing to report.
func TestHeartbeatKeepsAQuietStreamLive(t *testing.T) {
	plane := newFakePlane(t)
	plane.heartbeat = 40 * time.Millisecond

	model, _ := newTestModelIdle(t, plane, 400*time.Millisecond)
	driver := newDriver(t, model)
	defer driver.stop()

	plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	// Several multiples of the inactivity bound, carried only by heartbeats.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		driver.step(25 * time.Millisecond)
		if model.state != stateLive {
			t.Fatalf("state = %v; heartbeats must refresh liveness", model.state)
		}
	}

	sse, runs, progress := plane.counts()
	if sse != 1 {
		t.Errorf("subscriptions = %d, want 1: heartbeats must not cause a reconnect", sse)
	}
	if runs != 1 || progress != 1 {
		t.Errorf("authoritative reads = %d/%d, want 1 each: a heartbeat is not a poll trigger",
			runs, progress)
	}
}

// TestSilenceBeforeStreamReadyIsFatal covers the pre-synchronization case.
//
// Headers arrive, then nothing. Without the bound the command would hang
// indefinitely on a connection that will never say anything.
func TestSilenceBeforeStreamReadyIsFatal(t *testing.T) {
	plane := newFakePlane(t)
	plane.serveSSE(func(w http.ResponseWriter, r *http.Request, s *fakeStream) {
		// Headers only — no stream_ready, no bytes, no close.
		<-r.Context().Done()
	})

	model, _ := newTestModelIdle(t, plane, 250*time.Millisecond)
	driver := newDriver(t, model)
	defer driver.stop()

	if !driver.settleUntil(func() bool { return model.quitting }, 15*time.Second) {
		t.Fatalf("silence before stream_ready did not terminate; state = %v", model.state)
	}
	if model.state != stateFatal {
		t.Errorf("state = %v, want FATAL", model.state)
	}
	if model.finalExitCode() != exitOperational {
		t.Errorf("exit = %d, want %d", model.finalExitCode(), exitOperational)
	}
}

// ---------------------------------------------------------------------
// Blocker 3: wrong-run events, end to end
// ---------------------------------------------------------------------

// TestObservationForAnotherRunInvalidatesStream proves a filter failure is not
// rendered.
//
// The subscription is run-scoped server-side, so an event for another run
// means the filter did not hold. Displaying it would attribute one agent's
// behavior to another — worse than showing nothing.
func TestObservationForAnotherRunInvalidatesStream(t *testing.T) {
	plane := newFakePlane(t)
	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)
	defer driver.stop()

	stream := plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	stream.send("observation", `{"version":"1","kind":"observation",`+
		`"scope":{"run_id":"run-99"},"observation":{"sequence":"1",`+
		`"record_count":"1","fingerprint_id":"fp-x","behavior":{"operation_category":"tool"},`+
		`"decision":"allow","risk_level":"low","new_behavior":true}}`)

	if !driver.settleUntil(func() bool { return model.state != stateLive }, 5*time.Second) {
		t.Fatalf("state = %v; a wrong-run event must invalidate the stream", model.state)
	}
	for _, row := range model.live {
		if row.sequence == "1" {
			t.Fatal("an observation from another run was displayed")
		}
	}
}
