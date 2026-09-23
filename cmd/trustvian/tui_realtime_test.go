package main

// Transport-level behavior: subscription shape, startup failures, and the
// benchmarks.

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

// TestSSESubscriptionShape pins what goes on the wire.
func TestSSESubscriptionShape(t *testing.T) {
	var (
		gotPath    string
		gotQuery   string
		gotAccept  string
		gotEventID string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.EscapedPath(), r.URL.RawQuery
		gotAccept, gotEventID = r.Header.Get("Accept"), r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := newPlatformClient(server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("newPlatformClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// An identifier with characters that would reshape a route or smuggle a
	// second parameter if it were concatenated rather than escaped.
	stream, err := newSSEOpener(client.baseURL).open(ctx, "run/42&project_id=other")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer stream.close()

	if gotPath != "/v1/realtime" {
		t.Errorf("path = %q, want /v1/realtime", gotPath)
	}
	// Run-scoped, never unfiltered, and the identifier cannot add a parameter.
	if gotQuery != "run_id=run%2F42%26project_id%3Dother" {
		t.Errorf("query = %q; the run id must be escaped into exactly one parameter", gotQuery)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotEventID != "" {
		t.Errorf("Last-Event-ID = %q, want none", gotEventID)
	}
}

// TestSSEOpenRejectsBadResponses fails before pretending to be subscribed.
func TestSSEOpenRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"realtime unavailable", 503, "application/json",
			`{"version":"1","error":{"code":"realtime_unavailable","message":"not configured"}}`},
		{"not found", 404, "application/json",
			`{"version":"1","error":{"code":"not_found","message":"no"}}`},
		{"wrong content type", 200, "application/json", `{}`},
		{"html error page", 502, "text/html", "<html>502</html>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()

			client, err := newPlatformClient(server.URL, 5*time.Second)
			if err != nil {
				t.Fatalf("newPlatformClient: %v", err)
			}
			stream, err := newSSEOpener(client.baseURL).open(context.Background(), "run-42")
			if err == nil {
				stream.close()
				t.Fatal("open accepted a response that is not a stream")
			}
			if exitCodeFor(err) != exitOperational {
				t.Errorf("exit = %d, want %d", exitCodeFor(err), exitOperational)
			}
		})
	}
}

// TestSSERefusesRedirect keeps a subscription addressed where it was aimed.
func TestSSERefusesRedirect(t *testing.T) {
	var destinationHits int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
	}))
	defer destination.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/v1/realtime", http.StatusFound)
	}))
	defer origin.Close()

	client, err := newPlatformClient(origin.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("newPlatformClient: %v", err)
	}
	stream, err := newSSEOpener(client.baseURL).open(context.Background(), "run-42")
	if err == nil {
		stream.close()
		t.Fatal("the stream followed a redirect")
	}
	if destinationHits != 0 {
		t.Errorf("redirect destination received %d requests, want 0", destinationHits)
	}
}

// TestStartupFailureIsFatalNotAReconnectLoop covers the initial-failure rule.
//
// Before the dashboard has ever been live, sitting on a quiet subscription to
// a run that does not exist is worse than failing: the developer waits for
// data that is never coming.
func TestStartupFailureIsFatalNotAReconnectLoop(t *testing.T) {
	plane := newFakePlane(t)
	plane.mu.Lock()
	plane.runStatus = 404
	plane.runBodies = []string{`{"version":"1","error":{"code":"not_found","message":"no such run"}}`}
	plane.mu.Unlock()

	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)
	defer driver.stop()

	plane.streamAt(0)

	if !driver.settleUntil(func() bool { return model.quitting }, 10*time.Second) {
		t.Fatalf("a 404 during the first resync must be fatal; state = %v", model.state)
	}
	if model.state != stateFatal {
		t.Errorf("state = %v, want FATAL", model.state)
	}
	if model.finalExitCode() != exitOperational {
		t.Errorf("exit = %d, want %d", model.finalExitCode(), exitOperational)
	}
	if model.finalExitCode() == exitGateFail {
		t.Fatal("the TUI produced exit 1, which means gate FAIL")
	}

	// And it did not start retrying.
	sse, _, _ := plane.counts()
	if sse != 1 {
		t.Errorf("SSE subscriptions = %d; a startup failure must not reconnect", sse)
	}
}

// TestQuitCancelsNetworkResources proves nothing outlives the command.
func TestQuitCancelsNetworkResources(t *testing.T) {
	plane := newFakePlane(t)
	model, _ := newTestModel(t, plane)
	driver := newDriver(t, model)

	stream := plane.streamAt(0)
	if !driver.settleUntil(func() bool { return model.state == stateLive }, 5*time.Second) {
		t.Fatalf("never went live; state = %v", model.state)
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model.releaseStream()
	driver.stop()

	// The server's handler must observe the client going away.
	select {
	case <-stream.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the server handler is still blocked; the SSE body was never closed")
	}
	if model.stream != nil {
		t.Error("the model still holds a stream after quit")
	}
}

// ---------------------------------------------------------------------
// Benchmarks — informational, per the task's resource contract
// ---------------------------------------------------------------------

func BenchmarkTUIObservationUpdate(b *testing.B) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive
	observation := realtimeObservation{
		Sequence: "18", RecordCount: "18", Decision: "allow", RiskLevel: "medium",
		TrustScore: 0.81, AnomalyScore: 0.22, NewBehavior: true,
		Behavior: behaviorDescriptor{OperationCategory: "tool",
			OperationName: "shell.execute", TargetName: "build-host"},
	}

	b.ReportAllocs()
	for b.Loop() {
		model.appendObservation(observation)
	}
}

func BenchmarkTUIRenderFullWindow(b *testing.B) {
	model := newTUIModel(context.Background(), "run-42", nil, nil)
	model.state = stateLive
	model.width, model.height = 120, 120
	model.snapshot = authoritativeSnapshot{taken: true,
		run: evaluationRunDTO{ID: "run-42", CandidateID: "cand-2",
			Environment: "local", BehavioralProfile: "checkout-agent", Status: "running"},
		progress: progressDTO{RecordCount: "180", DistinctBehaviorCount: 11,
			BehaviorComplete: true, NextIngestSequence: "181"}}
	for i := range tuiObservationCapacity {
		model.appendObservation(realtimeObservation{
			Sequence: fmt.Sprint(i), Decision: "allow", RiskLevel: "medium",
			Behavior: behaviorDescriptor{OperationCategory: "tool",
				OperationName: "shell.execute", TargetName: "build-host"}})
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = model.View()
	}
}

func BenchmarkTerminalSanitizeClean(b *testing.B) {
	const text = "tool/shell.execute → build-host"
	b.ReportAllocs()
	for b.Loop() {
		_ = sanitizeTerminalText(text)
	}
}

func BenchmarkTerminalSanitizeHostile(b *testing.B) {
	text := strings.Repeat("\x1b[2J\x1b]0;owned\x07name", 8)
	b.ReportAllocs()
	for b.Loop() {
		_ = sanitizeTerminalText(text)
	}
}

// TestSSEErrorBodyReadIsTimeBounded covers a non-200 whose body never ends.
//
// By the time the status is known, ResponseHeaderTimeout has already been
// satisfied, and there is deliberately no total client timeout — so the only
// thing bounding the diagnostic read was io.LimitReader, which bounds bytes
// and not time. A server that sent 404 headers and then stalled blocked
// startup indefinitely.
func TestSSEErrorBodyReadIsTimeBounded(t *testing.T) {
	requestGone := make(chan struct{})
	var once sync.Once

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		// Headers out, then a body that never completes: fewer bytes than the
		// cap, so the byte bound can never trigger.
		fmt.Fprint(w, `{"version":"1","error":{"code":"not_found"`)
		w.(http.Flusher).Flush()

		<-r.Context().Done()
		once.Do(func() { close(requestGone) })
	}))
	defer server.Close()

	client, err := newPlatformClient(server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("newPlatformClient: %v", err)
	}
	opener := newSSEOpener(client.baseURL)
	opener.errorBodyTimeout = 300 * time.Millisecond

	done := make(chan error, 1)
	go func() {
		stream, err := opener.open(context.Background(), "run-42")
		if stream != nil {
			stream.close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("open accepted a 404")
		}
		if exitCodeFor(err) != exitOperational {
			t.Errorf("exit = %d, want %d", exitCodeFor(err), exitOperational)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("open never returned; the error-body read has no time bound")
	}

	// The request was cancelled and the body closed, so the handler unblocks.
	select {
	case <-requestGone:
	case <-time.After(10 * time.Second):
		t.Fatal("the server never saw the request cancelled")
	}
}

// TestSSEErrorBodyStaysByteBounded keeps the other half honest: the time bound
// must not have replaced the size cap.
//
// The body is a *valid* error envelope carrying an enormous message, which is
// what makes this test able to fail at all. An earlier version flooded
// non-JSON bytes and asserted on the diagnostic length — but a non-JSON body
// never reaches the diagnostic, so the assertion held whether or not the cap
// existed, and a mutation removing io.LimitReader survived it. A vacuous
// assertion is worse than none: it reports coverage it does not have.
func TestSSEErrorBodyStaysByteBounded(t *testing.T) {
	const floodMessage = maxSSEErrorBodyBytes * 4

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		// Well-formed, so an unbounded read would parse it and carry the whole
		// message into the error a user sees.
		fmt.Fprintf(w, `{"version":"1","error":{"code":"internal","message":%q}}`,
			strings.Repeat("x", floodMessage))
	}))
	defer server.Close()

	client, err := newPlatformClient(server.URL, 5*time.Second)
	if err != nil {
		t.Fatalf("newPlatformClient: %v", err)
	}
	opener := newSSEOpener(client.baseURL)

	stream, err := opener.open(context.Background(), "run-42")
	if stream != nil {
		stream.close()
	}
	if err == nil {
		t.Fatal("open accepted a 503")
	}
	// Truncated at the cap, the envelope no longer parses, so the diagnostic
	// falls back to the status alone. Without the cap the full message would
	// be in it.
	if len(err.Error()) > maxSSEErrorBodyBytes {
		t.Errorf("diagnostic is %d bytes; the %d byte cap was not applied",
			len(err.Error()), maxSSEErrorBodyBytes)
	}
}
