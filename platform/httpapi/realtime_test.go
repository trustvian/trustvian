package httpapi_test

// Task 059: the SSE adapter.
//
// These use a real httptest.Server rather than a recorder. A recorder buffers
// forever and would happily "pass" a handler that never flushed — which is the
// one property a streaming transport has to get right.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
	"trustvian-platform/httpapi"
)

// sseFrame is one parsed SSE event.
type sseFrame struct {
	Event string
	Data  string
}

// stream reads frames from a live connection.
//
// Exactly one goroutine reads the socket, feeding a channel. An earlier
// version started a reader per negative assertion, and the leftover goroutine
// swallowed the next real frame — the kind of bug a shared bufio.Reader makes
// easy and a single owner makes impossible.
type stream struct {
	t        *testing.T
	response *http.Response
	frames   chan sseFrame
	beats    chan struct{}
	cancel   context.CancelFunc
}

func newStream(t *testing.T, response *http.Response, cancel context.CancelFunc) *stream {
	s := &stream{
		t:        t,
		response: response,
		frames:   make(chan sseFrame, 64),
		beats:    make(chan struct{}, 64),
		cancel:   cancel,
	}

	go func() {
		defer close(s.frames)
		reader := bufio.NewReader(response.Body)
		var frame sseFrame
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")

			switch {
			case line == "":
				if frame.Event != "" {
					s.frames <- frame
					frame = sseFrame{}
				}
			case strings.HasPrefix(line, ":"):
				// A keepalive comment: no domain meaning, reported separately
				// so a heartbeat test can see it without it looking like state.
				select {
				case s.beats <- struct{}{}:
				default:
				}
			case strings.HasPrefix(line, "event: "):
				frame.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.Data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, "id:"):
				s.t.Errorf("the server emitted an SSE id, which would imply replay: %q", line)
			}
		}
	}()

	return s
}

// next blocks for the next event frame.
func (s *stream) next() sseFrame {
	s.t.Helper()
	select {
	case frame, open := <-s.frames:
		if !open {
			s.t.Fatal("stream ended before a frame arrived")
		}
		return frame
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for an SSE frame")
		return sseFrame{}
	}
}

// expectNoFrame asserts nothing arrives within a grace period.
//
// A tolerance rather than a correctness mechanism: every positive assertion
// blocks on a real frame.
func (s *stream) expectNoFrame(within time.Duration) {
	s.t.Helper()
	select {
	case frame, open := <-s.frames:
		if open {
			s.t.Fatalf("unexpected frame %q", frame.Event)
		}
	case <-time.After(within):
	}
}

// waitForHeartbeat blocks for one keepalive comment.
func (s *stream) waitForHeartbeat(within time.Duration) {
	s.t.Helper()
	select {
	case <-s.beats:
	case <-time.After(within):
		s.t.Fatal("no keepalive arrived on a quiet stream")
	}
}

func (s *stream) close() {
	s.cancel()
	s.response.Body.Close()
}

// realtimeAPI is a live server with a bus wired to the control plane.
type realtimeAPI struct {
	t      *testing.T
	server *httptest.Server
	plane  *platform.ControlPlane
	bus    *platform.InMemoryRealtimeBus
	store  *platform.SQLiteStore
	client *http.Client
}

func newRealtimeAPI(t *testing.T, options ...httpapi.Option) *realtimeAPI {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	bus := platform.NewInMemoryRealtimeBus()
	t.Cleanup(func() { bus.Close() })

	plane, err := platform.NewControlPlane(store, store, store,
		platform.WithRealtimePublisher(bus))
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}

	options = append([]httpapi.Option{
		httpapi.WithClock(func() time.Time { return testEpoch }),
		httpapi.WithRealtimeSubscriber(bus),
	}, options...)

	handler, err := httpapi.NewHandler(plane, options...)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &realtimeAPI{t: t, server: server, plane: plane, bus: bus, store: store,
		client: server.Client()}
}

// connect opens a stream and consumes the handshake.
func (a *realtimeAPI) connect(query string, headers map[string]string) *stream {
	a.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	request, err := http.NewRequestWithContext(ctx, "GET", a.server.URL+"/v1/realtime"+query, nil)
	if err != nil {
		cancel()
		a.t.Fatalf("NewRequest() error = %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := a.client.Do(request)
	if err != nil {
		cancel()
		a.t.Fatalf("GET /v1/realtime error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		cancel()
		a.t.Fatalf("status = %d, want 200; body = %s", response.StatusCode, body)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		a.t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-cache" {
		a.t.Errorf("Cache-Control = %q, want no-cache", got)
	}
	// No CORS: task 063 has the first browser caller and decides explicitly.
	if got := response.Header.Get("Access-Control-Allow-Origin"); got != "" {
		a.t.Errorf("Access-Control-Allow-Origin = %q, want none", got)
	}

	return newStream(a.t, response, cancel)
}

// post issues a JSON request against the live server.
func (a *realtimeAPI) post(path string, body any) *http.Response {
	a.t.Helper()
	encoded := encodeBody(a.t, body)
	request, err := http.NewRequest("POST", a.server.URL+path, strings.NewReader(string(encoded)))
	if err != nil {
		a.t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		a.t.Fatalf("POST %s error = %v", path, err)
	}
	return response
}

func (a *realtimeAPI) mustPost(path string, body any, wantStatus int) {
	a.t.Helper()
	response := a.post(path, body)
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		payload, _ := io.ReadAll(response.Body)
		a.t.Fatalf("POST %s status = %d, want %d; body = %s",
			path, response.StatusCode, wantStatus, payload)
	}
}

// seedRunning builds the hierarchy and starts a run over HTTP.
func (a *realtimeAPI) seedRunning(runID, candidateID string) {
	a.t.Helper()
	a.mustPost("/v1/projects", map[string]string{"id": "proj-1", "name": "Checkout"}, 201)
	a.mustPost("/v1/agents", map[string]string{
		"id": "agent-1", "project_id": "proj-1", "name": "Deploy agent"}, 201)
	a.mustPost("/v1/candidates", map[string]any{
		"id": candidateID, "agent_id": "agent-1",
		"metadata": map[string]string{"label": "v1"}}, 201)
	// Task 065: a run names an environment its project owns.
	a.mustPost("/v1/environments", map[string]any{
		"project_id": "proj-1", "ref": testEnvironment, "name": "Staging"}, 201)
	a.mustPost("/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": candidateID,
		"environment": testEnvironment, "behavioral_profile": testProfile}, 201)
	a.mustPost("/v1/evaluation-runs/"+runID+"/start", nil, 200)
}

func (a *realtimeAPI) ingest(runID string, sequence uint64, record trustvian.DecisionRecord) {
	a.t.Helper()
	a.mustPost("/v1/evaluation-runs/"+runID+"/records", map[string]any{
		"version":            httpapi.WireVersion,
		"sequence":           platform.FormatSequence(sequence),
		"behavioral_profile": testProfile,
		"record":             record,
	}, 200)
}

func decodeReady(t *testing.T, frame sseFrame) (replay, resync bool) {
	t.Helper()
	if frame.Event != "stream_ready" {
		t.Fatalf("first frame = %q, want stream_ready", frame.Event)
	}
	var payload struct {
		Version         string `json:"version"`
		ReplayAvailable bool   `json:"replay_available"`
		ResyncRequired  bool   `json:"resync_required"`
	}
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		t.Fatalf("decode stream_ready: %v (%s)", err, frame.Data)
	}
	if payload.Version != httpapi.WireVersion {
		t.Errorf("version = %q, want %q", payload.Version, httpapi.WireVersion)
	}
	return payload.ReplayAvailable, payload.ResyncRequired
}

// ---------------------------------------------------------------------
// Handshake
// ---------------------------------------------------------------------

// The handshake says plainly that there is no replay, so a client knows it
// must resynchronize — and it arrives after the subscription is registered, so
// anything happening during that resync is already queued.
func TestSSEHandshakeRequiresResync(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()

	replay, resync := decodeReady(t, s.next())
	if replay {
		t.Error("replay_available = true; the bus retains no history")
	}
	if !resync {
		t.Error("resync_required = false; every connection must resync")
	}
}

// Last-Event-ID is ignored rather than honored: accepting it while being
// unable to replay would promise durability the bus does not have.
func TestSSELastEventIDPromisesNoReplay(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", map[string]string{"Last-Event-ID": "123"})
	defer s.close()

	replay, resync := decodeReady(t, s.next())
	if replay || !resync {
		t.Errorf("with Last-Event-ID: replay=%v resync=%v; want false/true", replay, resync)
	}
}

// Every reconnection is a fresh stream with the same contract.
func TestSSEReconnectRequiresResyncAgain(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	first := a.connect("?run_id=run-1", nil)
	decodeReady(t, first.next())
	first.close()

	second := a.connect("?run_id=run-1", nil)
	defer second.close()
	replay, resync := decodeReady(t, second.next())
	if replay || !resync {
		t.Errorf("on reconnect: replay=%v resync=%v; want false/true", replay, resync)
	}
}

// ---------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------

type observationFrame struct {
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Scope   struct {
		ProjectID   string `json:"project_id"`
		AgentID     string `json:"agent_id"`
		CandidateID string `json:"candidate_id"`
		RunID       string `json:"run_id"`
	} `json:"scope"`
	Observation *struct {
		Sequence         string `json:"sequence"`
		RecordCount      string `json:"record_count"`
		BehaviorComplete bool   `json:"behavior_complete"`
		FingerprintID    string `json:"fingerprint_id"`
		Behavior         struct {
			OperationName string `json:"operation_name"`
		} `json:"behavior"`
		Decision    string `json:"decision"`
		NewBehavior bool   `json:"new_behavior"`
	} `json:"observation"`
	Evaluation *struct {
		Status        string `json:"status"`
		FailureReason string `json:"failure_reason"`
	} `json:"evaluation"`
}

func decodeEvent(t *testing.T, frame sseFrame) observationFrame {
	t.Helper()
	var payload observationFrame
	if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
		t.Fatalf("decode %q: %v (%s)", frame.Event, err, frame.Data)
	}
	if payload.Version != httpapi.WireVersion {
		t.Errorf("version = %q", payload.Version)
	}
	if payload.Kind != frame.Event {
		t.Errorf("frame event %q does not match payload kind %q", frame.Event, payload.Kind)
	}
	return payload
}

func TestSSEDeliversObservations(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	a.ingest("run-1", 1, apiRecord("evt-0", "fp-0", "read"))

	payload := decodeEvent(t, s.next())
	if payload.Kind != "observation" || payload.Observation == nil {
		t.Fatalf("kind = %q with observation %v", payload.Kind, payload.Observation)
	}
	o := payload.Observation
	if o.Sequence != "1" || o.RecordCount != "1" || !o.BehaviorComplete {
		t.Errorf("observation = %+v", o)
	}
	if o.FingerprintID != "fp-0" || o.Behavior.OperationName != "read" {
		t.Errorf("observation does not describe the record: %+v", o)
	}
	if !o.NewBehavior {
		t.Error("new_behavior = false for a first sighting")
	}
	if payload.Scope.RunID != "run-1" || payload.Scope.ProjectID != "proj-1" ||
		payload.Scope.AgentID != "agent-1" {
		t.Errorf("scope = %+v", payload.Scope)
	}
}

func TestSSEDeliversLifecycleEvents(t *testing.T) {
	tests := []struct {
		name string
		path string
		body any
		want string
	}{
		{"completed", "/complete", nil, "evaluation_completed"},
		{"failed", "/fail", map[string]string{"reason": "engine unreachable"}, "evaluation_failed"},
		{"cancelled", "/cancel", nil, "evaluation_cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")

			s := a.connect("?run_id=run-1", nil)
			defer s.close()
			decodeReady(t, s.next())

			a.mustPost("/v1/evaluation-runs/run-1"+tt.path, tt.body, 200)

			payload := decodeEvent(t, s.next())
			if payload.Kind != tt.want {
				t.Fatalf("kind = %q, want %q", payload.Kind, tt.want)
			}
			if payload.Evaluation == nil {
				t.Fatal("lifecycle frame carries no evaluation")
			}
			if tt.name == "failed" && payload.Evaluation.FailureReason != "engine unreachable" {
				t.Errorf("failure_reason = %q", payload.Evaluation.FailureReason)
			}
			// No verdict language on a lifecycle event.
			for _, forbidden := range []string{"passed", "promotable", "\"safe\""} {
				if strings.Contains(frameBody(payload), forbidden) {
					t.Errorf("lifecycle frame contains %q", forbidden)
				}
			}
		})
	}
}

func frameBody(payload observationFrame) string {
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

// A stream watching one run must not see another's activity.
func TestSSEFilteringExcludesOtherRuns(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-a", "cand-a")
	a.mustPost("/v1/candidates", map[string]any{
		"id": "cand-b", "agent_id": "agent-1", "metadata": map[string]string{"label": "b"}}, 201)
	a.mustPost("/v1/evaluation-runs", map[string]string{
		"id": "run-b", "candidate_id": "cand-b",
		"environment": testEnvironment, "behavioral_profile": testProfile}, 201)
	a.mustPost("/v1/evaluation-runs/run-b/start", nil, 200)

	s := a.connect("?run_id=run-a", nil)
	defer s.close()
	decodeReady(t, s.next())

	// Activity on the other run only.
	a.ingest("run-b", 1, apiRecord("b-evt", "b-fp", "b-op"))
	s.expectNoFrame(150 * time.Millisecond)

	// Then its own.
	a.ingest("run-a", 1, apiRecord("a-evt", "a-fp", "a-op"))
	payload := decodeEvent(t, s.next())
	if payload.Scope.RunID != "run-a" {
		t.Errorf("received run %q, want run-a", payload.Scope.RunID)
	}
}

// A replayed ingest produced no second durable record, so it produces no
// second live observation.
func TestSSEReplayedIngestProducesNoSecondEvent(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	record := apiRecord("evt-0", "fp-0", "read")
	a.ingest("run-1", 1, record)
	if payload := decodeEvent(t, s.next()); payload.Kind != "observation" {
		t.Fatalf("kind = %q", payload.Kind)
	}

	// The same sequence and record again.
	a.ingest("run-1", 1, record)
	s.expectNoFrame(150 * time.Millisecond)
}

// ---------------------------------------------------------------------
// Availability and leaks
// ---------------------------------------------------------------------

// Realtime is optional: without a subscriber the route reports unavailable
// while every task 058 route keeps working.
func TestRealtimeUnavailableWithoutASubscriber(t *testing.T) {
	a := newAPI(t) // the task 058 fixture, no realtime option
	a.seedHierarchy()

	r := a.do("GET", "/v1/realtime", nil)
	a.mustStatus(r, 503, "realtime without a subscriber")
	if code := errorCode(t, r); code != "realtime_unavailable" {
		t.Errorf("code = %q, want realtime_unavailable", code)
	}

	// Other routes are unaffected.
	a.mustStatus(a.do("GET", "/v1/projects/proj-1", nil), 200, "get project")
}

// A client that goes away releases its subscription: no slot leak.
func TestSSEDisconnectReleasesSubscription(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	// Establish a baseline by opening and closing a stream.
	s := a.connect("?run_id=run-1", nil)
	decodeReady(t, s.next())

	// Prove the subscription is live by delivering one event.
	a.ingest("run-1", 1, apiRecord("evt-0", "fp-0", "read"))
	decodeEvent(t, s.next())

	s.close()

	// The registration is released. Observed through the public capability
	// rather than an internal counter: once the subscription is gone, a
	// matching publish delivers to nobody.
	//
	// Polled against a deadline because the handler unwinds asynchronously
	// after the client hangs up — the deadline is a tolerance, not the
	// mechanism under test.
	deadline := time.Now().Add(2 * time.Second)
	for {
		result := a.bus.Publish(platform.RealtimeEvent{
			Kind:  platform.RealtimeObservationRecorded,
			Scope: platform.RealtimeScope{RunID: "run-1"},
		})
		if result.Delivered == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a disconnected client's subscription is still receiving events")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------
// Heartbeat
// ---------------------------------------------------------------------

// A quiet stream still proves itself alive. The frame is a comment, not a
// synthetic domain event, so a client cannot mistake it for state.
func TestSSEHeartbeatKeepsAQuietStreamAlive(t *testing.T) {
	a := newRealtimeAPI(t, httpapi.WithHeartbeatInterval(10*time.Millisecond))
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	// A comment frame, tracked separately from events: a client must not be
	// able to mistake a keepalive for state.
	s.waitForHeartbeat(3 * time.Second)
}

// ---------------------------------------------------------------------
// Real Engine through HTTP to SSE
// ---------------------------------------------------------------------

// The whole chain across the public boundary:
//
//	Engine.Analyze → Result.DecisionRecord() → HTTP ingest → SQLite commit
//	→ ControlPlane publisher → bounded bus → SSE
//
// No hand-built record and no second behavioral implementation.
func TestRealEngineThroughHTTPIngestToSSE(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))
	result, err := engine.Analyze(t.Context(), event.Event{
		ID:        "evt-real",
		Timestamp: testEpoch.Add(time.Minute),
		Actor: event.Actor{
			ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
		},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
		Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
		Context:   event.Context{Environment: testEnvironment},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	record := result.DecisionRecord()

	a.ingest("run-1", 1, record)

	payload := decodeEvent(t, s.next())
	if payload.Kind != "observation" || payload.Observation == nil {
		t.Fatalf("kind = %q", payload.Kind)
	}
	o := payload.Observation
	if o.FingerprintID != record.FingerprintID {
		t.Errorf("fingerprint = %q, want %q", o.FingerprintID, record.FingerprintID)
	}
	if o.Behavior.OperationName != record.Behavior.OperationName {
		t.Errorf("operation = %q, want %q", o.Behavior.OperationName, record.Behavior.OperationName)
	}
	if o.Decision != record.Decision {
		t.Errorf("decision = %q, want %q", o.Decision, record.Decision)
	}
	if o.Sequence != "1" || o.RecordCount != "1" {
		t.Errorf("sequence/count = %q/%q, want 1/1", o.Sequence, o.RecordCount)
	}

	// A replay adds nothing live.
	a.ingest("run-1", 1, record)
	s.expectNoFrame(150 * time.Millisecond)

	// And completion arrives.
	a.mustPost("/v1/evaluation-runs/run-1/complete", nil, 200)
	completion := decodeEvent(t, s.next())
	if completion.Kind != "evaluation_completed" {
		t.Errorf("kind = %q, want evaluation_completed", completion.Kind)
	}
}

// Task 050's privacy boundary holds on the realtime path: a distinctive
// attribute on the source event never reaches the wire.
func TestSSECarriesNoRawEventPayload(t *testing.T) {
	const secret = "TOTALLY-DISTINCTIVE-PROMPT-VALUE-8fa3"

	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))
	result, err := engine.Analyze(t.Context(), event.Event{
		ID:        "evt-secret",
		Timestamp: testEpoch.Add(time.Minute),
		Actor: event.Actor{
			ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
		},
		Operation: event.Operation{Category: event.OperationCategoryTool, Name: "shell.execute"},
		Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
		Context:   event.Context{Environment: testEnvironment},
		// The value that must not travel.
		Attributes: map[string]any{"prompt": secret, "tool_argument": secret},
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	a.ingest("run-1", 1, result.DecisionRecord())

	frame := s.next()
	if strings.Contains(frame.Data, secret) {
		t.Fatalf("the SSE frame carries a raw event attribute: %s", frame.Data)
	}
	// And the projection still describes the behavior.
	payload := decodeEvent(t, frame)
	if payload.Observation == nil || payload.Observation.Behavior.OperationName != "shell.execute" {
		t.Errorf("observation lost its behavior: %+v", payload.Observation)
	}
}

// ---------------------------------------------------------------------
// The handler releases its own subscription
// ---------------------------------------------------------------------

// contextIgnoringSubscriber hands out a subscription that does not watch the
// request context.
//
// The in-memory bus releases a subscription on cancellation as well as on
// Close, which is correct defence in depth — and it means the handler's own
// release is never exercised against it. A future subscriber implementation
// need not watch contexts, so the handler must not depend on one that does.
type contextIgnoringSubscriber struct {
	mu         sync.Mutex
	closeCalls int
	events     chan platform.RealtimeEvent
}

// newContextIgnoringSubscriber allocates the channel up front.
//
// Allocating it inside Subscribe would race any test that publishes into it,
// since Subscribe runs on the handler's goroutine.
func newContextIgnoringSubscriber() *contextIgnoringSubscriber {
	return &contextIgnoringSubscriber{events: make(chan platform.RealtimeEvent, 4)}
}

func (s *contextIgnoringSubscriber) Subscribe(
	ctx context.Context, filter platform.RealtimeFilter,
) (platform.RealtimeSubscription, error) {
	return &countingSubscription{owner: s, events: s.events}, nil
}

func (s *contextIgnoringSubscriber) closes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

type countingSubscription struct {
	owner  *contextIgnoringSubscriber
	events chan platform.RealtimeEvent
	once   sync.Once
}

func (s *countingSubscription) Events() <-chan platform.RealtimeEvent { return s.events }

func (s *countingSubscription) Close() error {
	s.owner.mu.Lock()
	s.owner.closeCalls++
	s.owner.mu.Unlock()
	s.once.Do(func() { close(s.events) })
	return nil
}

func TestSSEHandlerClosesItsSubscriptionOnDisconnect(t *testing.T) {
	subscriber := newContextIgnoringSubscriber()

	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	handler, err := httpapi.NewHandler(plane,
		httpapi.WithRealtimeSubscriber(subscriber),
		httpapi.WithHeartbeatInterval(10*time.Millisecond))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/realtime", nil)
	if err != nil {
		cancel()
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		cancel()
		t.Fatalf("GET error = %v", err)
	}
	// Consume the handshake so the handler is definitely inside its loop.
	buffer := make([]byte, 128)
	if _, err := response.Body.Read(buffer); err != nil {
		t.Fatalf("read handshake: %v", err)
	}

	cancel()
	response.Body.Close()

	// The handler must release the subscription itself, since nothing else
	// will. Polled against a deadline because the handler unwinds
	// asynchronously after the client hangs up.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if subscriber.closes() > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the handler never closed its subscription; it relies on the bus to do it")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------
// Filter validation over the wire
// ---------------------------------------------------------------------

// rawGet issues a realtime GET that is expected to be refused.
//
// It takes its own *testing.T because the callers below are subtests sharing
// one fixture, and it cannot hang: a request that wrongly opens a stream is
// reported here rather than left to block ReadAll until the package timeout.
// An earlier version did block, which turned a caught mutation into a
// ten-minute hang and hid which mutation it was.
func (a *realtimeAPI) rawGet(t *testing.T, query string) (int, []byte) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, "GET", a.server.URL+"/v1/realtime"+query, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	response, err := a.client.Do(request)
	if err != nil {
		t.Fatalf("GET /v1/realtime error = %v", err)
	}
	defer response.Body.Close()

	// No stream was claimed. A client that saw text/event-stream on a refusal
	// would have to parse an error envelope as SSE frames — and this body
	// would never end.
	if got := response.Header.Get("Content-Type"); strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("a stream was opened (status %d, Content-Type %q) where the request should have been refused",
			response.StatusCode, got)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, body
}

func wireErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var envelope struct {
		Version string `json:"version"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("error body is not an envelope: %v (%s)", err, body)
	}
	if envelope.Version != httpapi.WireVersion {
		t.Errorf("error envelope version = %q, want %q", envelope.Version, httpapi.WireVersion)
	}
	return envelope.Error.Code
}

// TestSSERejectsMalformedFilterAsClientError proves malformed input is not
// dressed up as infrastructure unavailability.
//
// A 503 realtime_unavailable tells a client to back off and retry an identical
// request that will never succeed. The distinction matters more here than on a
// POST, because an SSE client's natural reaction to a 5xx is to reconnect in a
// loop.
func TestSSERejectsMalformedFilterAsClientError(t *testing.T) {
	overlong := strings.Repeat("a", 257)

	tests := []struct {
		name  string
		query string
	}{
		{"project_id too long", "?project_id=" + overlong},
		{"agent_id too long", "?agent_id=" + overlong},
		{"run_id too long", "?run_id=" + overlong},
		{"project_id leading space", "?project_id=%20proj-1"},
		{"agent_id trailing space", "?agent_id=agent-1%20"},
		{"run_id newline", "?run_id=run%0A1"},
		{"run_id nul", "?run_id=run%001"},
		{"run_id escape", "?run_id=run%1b%5b2J"},
		// One valid dimension does not excuse another: the filter is ANDed,
		// and a partially-validated filter would mean a caller could smuggle
		// an unbounded value past by pairing it with a good one.
		{"one good one bad", "?run_id=run-1&agent_id=" + overlong},
	}

	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// rawGet also asserts no stream was opened.
			status, body := a.rawGet(t, tt.query)

			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", status, body)
			}
			if code := wireErrorCode(t, body); code != "invalid_request" {
				t.Errorf("code = %q, want invalid_request", code)
			}
		})
	}

	// And a valid filter still opens a stream on the same server, so the
	// rejections above did not leave the route or its capacity damaged.
	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())
}

// failingSubscriber returns a fixed error, so each branch of the wire mapping
// can be reached without contriving the condition on a real bus.
type failingSubscriber struct{ err error }

func (s failingSubscriber) Subscribe(
	context.Context, platform.RealtimeFilter,
) (platform.RealtimeSubscription, error) {
	return nil, s.err
}

// TestSSESubscribeFailuresKeepTheirCategories guards the distinction the
// previous test depends on.
//
// Mapping every subscribe failure to one status would make the 400 above an
// accident of ordering rather than a decision. Capacity and shutdown are
// infrastructure conditions a client can only wait out; anything unrecognised
// is a server fault and must not leak its text.
func TestSSESubscribeFailuresKeepTheirCategories(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"capacity", platform.ErrRealtimeCapacity, 503, "realtime_unavailable"},
		{"closed", platform.ErrRealtimeClosed, 503, "realtime_unavailable"},
		{"malformed filter", fmt.Errorf("%w: bad", platform.ErrInvalidID), 400, "invalid_request"},
		{"unclassified", errors.New("SECRET-INTERNAL-DETAIL"), 500, "internal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRealtimeAPI(t, httpapi.WithRealtimeSubscriber(failingSubscriber{err: tt.err}))

			status, body := a.rawGet(t, "?run_id=run-1")
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", status, tt.wantStatus, body)
			}
			if code := wireErrorCode(t, body); code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if strings.Contains(string(body), "SECRET-INTERNAL-DETAIL") {
				t.Errorf("unclassified error text leaked to the client: %s", body)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Per-write deadlines
// ---------------------------------------------------------------------

// stallingWriter is a ResponseWriter that can stop draining mid-stream.
//
// It honors whatever deadline the handler sets rather than a duration of its
// own: a stalled write unblocks exactly when that deadline expires and reports
// os.ErrDeadlineExceeded, which is what a real net.Conn does. That is what
// makes this test about the handler's bound and not about a sleep the test
// picked.
type stallingWriter struct {
	header http.Header

	mu        sync.Mutex
	deadlines []time.Time
	writes    int
	body      []byte

	// stallAfter is the number of writes served normally before one blocks.
	stallAfter int
	stalled    chan struct{}
	stallOnce  sync.Once
}

func newStallingWriter(stallAfter int) *stallingWriter {
	return &stallingWriter{
		header:     make(http.Header),
		stallAfter: stallAfter,
		stalled:    make(chan struct{}),
	}
}

func (s *stallingWriter) Header() http.Header  { return s.header }
func (s *stallingWriter) WriteHeader(code int) { s.header.Set("X-Status", strconv.Itoa(code)) }
func (s *stallingWriter) Flush()               {}

func (s *stallingWriter) SetWriteDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadlines = append(s.deadlines, deadline)
	return nil
}

func (s *stallingWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.writes++
	stall := s.writes > s.stallAfter
	deadline := s.deadlines[len(s.deadlines)-1]
	if !stall {
		s.body = append(s.body, p...)
	}
	s.mu.Unlock()

	if !stall {
		return len(p), nil
	}

	s.stallOnce.Do(func() { close(s.stalled) })

	// The client has stopped reading. A real connection blocks here until its
	// write deadline expires; so does this one.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	return 0, os.ErrDeadlineExceeded
}

func (s *stallingWriter) deadlineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.deadlines)
}

func (s *stallingWriter) sentBody() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.body)
}

// TestSSEStalledWriteIsBoundedByItsDeadline is the point of the whole
// mechanism.
//
// The bus bound does not cover this case: this subscriber's queue is healthy
// and its slot is freed the moment it unsubscribes. What is unbounded without
// a write deadline is the handler goroutine and the connection under it — a
// client that opens streams and never reads them would accumulate them outside
// every count the bus keeps.
func TestSSEStalledWriteIsBoundedByItsDeadline(t *testing.T) {
	subscriber := newContextIgnoringSubscriber()
	plane := newBarePlane(t)

	const writeTimeout = 150 * time.Millisecond
	handler, err := httpapi.NewHandler(plane,
		httpapi.WithRealtimeSubscriber(subscriber),
		httpapi.WithRealtimeWriteTimeout(writeTimeout),
		// Long enough that no heartbeat can be the thing that frees the
		// handler: only the write deadline can.
		httpapi.WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	// stream_ready costs three writes; the client drains those and then stops.
	writer := newStallingWriter(3)
	request := httptest.NewRequest("GET", "/v1/realtime?run_id=run-1", nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, request)
	}()

	// Wait for the handshake to land before publishing, so the frame that
	// stalls is unambiguously the observation.
	waitFor(t, func() bool { return writer.sentBody() != "" }, "handshake written")

	subscriber.events <- platform.RealtimeEvent{
		Kind:        platform.RealtimeObservationRecorded,
		Scope:       platform.RealtimeScope{ProjectID: "proj-1", AgentID: "agent-1", RunID: "run-1"},
		Observation: platform.RealtimeObservation{Sequence: 1},
	}

	select {
	case <-writer.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the stalling write never began")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Generous on purpose: the assertion is that the handler returns at
		// all, and a failure here is a hang, not a slow machine.
		t.Fatal("handler did not return after its write deadline expired")
	}

	// The subscription is released. A handler that exited while leaving a
	// registration behind would leak a slot per stalled client, which is the
	// bound this is protecting.
	if got := subscriber.closes(); got != 1 {
		t.Errorf("subscription Close calls = %d, want 1", got)
	}

	if strings.Contains(writer.sentBody(), "observation") {
		t.Error("the stalled frame was recorded as delivered")
	}
}

// TestSSEDeadlineIsRefreshedBeforeEveryWrite proves it is a per-write bound.
//
// Set once when the connection opens, the same constant would be a
// connection lifetime: a healthy stream would die at an arbitrary moment
// having done nothing wrong, and the failure would look like a network fault.
func TestSSEDeadlineIsRefreshedBeforeEveryWrite(t *testing.T) {
	subscriber := newContextIgnoringSubscriber()
	plane := newBarePlane(t)

	handler, err := httpapi.NewHandler(plane,
		httpapi.WithRealtimeSubscriber(subscriber),
		httpapi.WithRealtimeWriteTimeout(5*time.Second),
		httpapi.WithHeartbeatInterval(time.Hour))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	// Never stalls: every write is served, so the handler keeps looping.
	writer := newStallingWriter(1 << 30)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "/v1/realtime?run_id=run-1", nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, request)
	}()

	// The handshake's deadline proves Subscribe has returned and the handler
	// is in its loop, so the frames below cannot race registration.
	waitFor(t, func() bool { return writer.deadlineCount() >= 1 }, "handshake deadline")

	for range 4 {
		subscriber.events <- platform.RealtimeEvent{
			Kind:        platform.RealtimeObservationRecorded,
			Scope:       platform.RealtimeScope{ProjectID: "proj-1", AgentID: "agent-1", RunID: "run-1"},
			Observation: platform.RealtimeObservation{Sequence: 1},
		}
	}

	// One deadline for the handshake plus one per frame. Waiting on the count
	// rather than sleeping keeps this deterministic.
	waitFor(t, func() bool { return writer.deadlineCount() >= 5 }, "five deadlines set")

	cancel()
	<-done

	writer.mu.Lock()
	defer writer.mu.Unlock()
	for i := 1; i < len(writer.deadlines); i++ {
		// Strictly later each time: a single stored deadline, or one derived
		// from the connection's start, would repeat.
		if !writer.deadlines[i].After(writer.deadlines[i-1]) {
			t.Fatalf("deadline %d (%v) is not after deadline %d (%v) — set once, not per write",
				i, writer.deadlines[i], i-1, writer.deadlines[i-1])
		}
	}
}

// TestSSEHealthyStreamOutlivesItsWriteTimeout is the same property from the
// client's side, over a real socket.
//
// The write timeout here is shorter than the span the stream is observed over.
// A deadline set once at connection time would kill this stream mid-test; a
// per-write deadline never notices.
func TestSSEHealthyStreamOutlivesItsWriteTimeout(t *testing.T) {
	a := newRealtimeAPI(t,
		httpapi.WithRealtimeWriteTimeout(60*time.Millisecond),
		httpapi.WithHeartbeatInterval(20*time.Millisecond))

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	// Several multiples of the write timeout, all served by writes that each
	// individually complete well inside it.
	for range 8 {
		s.waitForHeartbeat(5 * time.Second)
	}
}

// TestSSERequiresWriteDeadlineSupport refuses a stream it cannot bound.
//
// Continuing without a deadline would leave the handler in exactly the state
// the mechanism exists to prevent, while every comment and document claimed
// otherwise. Failing before the 200 keeps that visible to the client instead
// of silent.
func TestSSERequiresWriteDeadlineSupport(t *testing.T) {
	subscriber := newContextIgnoringSubscriber()
	plane := newBarePlane(t)

	handler, err := httpapi.NewHandler(plane, httpapi.WithRealtimeSubscriber(subscriber))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	// A recorder flushes but cannot take a deadline — the precise shape of a
	// wrapper that silently removes the bound.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/v1/realtime", nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", recorder.Code, recorder.Body.String())
	}
	if code := wireErrorCode(t, recorder.Body.Bytes()); code != "internal" {
		t.Errorf("code = %q, want internal", code)
	}
	if got := recorder.Header().Get("Content-Type"); strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q: a stream was claimed and then refused", got)
	}
	// Refused before subscribing, so nothing to release and no slot consumed.
	if got := subscriber.closes(); got != 0 {
		t.Errorf("Close calls = %d, want 0: the handler subscribed before it could bound its writes", got)
	}
}

// TestRealtimeWriteTimeoutOptionIsValidated rejects rather than clamps.
//
// An option silently corrected to a working value lets a caller believe a
// bound they did not get. maxRealtimeWriteTimeout exists for the same reason
// the default does: an unbounded configurable value is not a bound.
func TestRealtimeWriteTimeoutOptionIsValidated(t *testing.T) {
	tests := []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{"zero", 0, true},
		{"negative", -time.Second, true},
		{"one nanosecond over the maximum", 30*time.Second + 1, true},
		{"an hour", time.Hour, true},

		{"one nanosecond", time.Nanosecond, false},
		{"the maximum", 30 * time.Second, false},
		{"a second", time.Second, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plane := newBarePlane(t)
			_, err := httpapi.NewHandler(plane, httpapi.WithRealtimeWriteTimeout(tt.timeout))
			if tt.wantErr != (err != nil) {
				t.Fatalf("NewHandler(WithRealtimeWriteTimeout(%v)) error = %v, wantErr = %v",
					tt.timeout, err, tt.wantErr)
			}
		})
	}
}

// waitFor polls a condition to a generous limit.
//
// The limit is not the assertion — it is the difference between a failing test
// and a hung one. Every condition here is reached by a goroutine that has
// already been handed everything it needs.
func waitFor(t *testing.T, condition func() bool, what string) {
	t.Helper()
	limit := time.After(5 * time.Second)
	for !condition() {
		select {
		case <-limit:
			t.Fatalf("timed out waiting for %s", what)
		default:
			runtime.Gosched()
		}
	}
}

// newBarePlane is a control plane with no realtime publisher, for tests whose
// subject is the transport rather than the pipeline.
func newBarePlane(t *testing.T) *platform.ControlPlane {
	t.Helper()
	store, err := platform.OpenSQLiteStore(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })

	plane, err := platform.NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	return plane
}

// ---------------------------------------------------------------------
// Bounded flushes
// ---------------------------------------------------------------------

// flushStallingWriter accepts every write and stalls in the flush.
//
// This is the half stallingWriter cannot reach. A Write can succeed into a
// local buffer while the flush behind it is what actually touches the socket,
// so a client that stops reading is far more likely to be observed here — and
// http.Flusher.Flush returns nothing, which is why the handler must flush
// through the controller instead.
//
// It implements both Flush and FlushError on purpose. Flush satisfies the
// handler's http.Flusher precondition and is deliberately a no-op, so a
// regression that flushed through it would sail past every assertion below;
// ResponseController prefers FlushError, which is the one that stalls.
type flushStallingWriter struct {
	header http.Header

	mu        sync.Mutex
	deadlines []time.Time
	flushes   int
	body      []byte

	// stallFlushAfter is the number of flushes served normally before one
	// blocks: 0 stalls the handshake, 1 stalls whatever frame follows it.
	stallFlushAfter int
	stalled         chan struct{}
	stallOnce       sync.Once
}

func newFlushStallingWriter(stallFlushAfter int) *flushStallingWriter {
	return &flushStallingWriter{
		header:          make(http.Header),
		stallFlushAfter: stallFlushAfter,
		stalled:         make(chan struct{}),
	}
}

func (s *flushStallingWriter) Header() http.Header  { return s.header }
func (s *flushStallingWriter) WriteHeader(code int) { s.header.Set("X-Status", strconv.Itoa(code)) }

// Flush is the capability, never the delivery. A handler that called this
// would discard exactly the error this test exists to produce.
func (s *flushStallingWriter) Flush() {}

func (s *flushStallingWriter) SetWriteDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadlines = append(s.deadlines, deadline)
	return nil
}

// Write always succeeds: the frame is buffered, not delivered.
func (s *flushStallingWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	s.body = append(s.body, p...)
	s.mu.Unlock()
	return len(p), nil
}

func (s *flushStallingWriter) FlushError() error {
	s.mu.Lock()
	s.flushes++
	stall := s.flushes > s.stallFlushAfter
	deadline := s.deadlines[len(s.deadlines)-1]
	s.mu.Unlock()

	if !stall {
		return nil
	}
	s.stallOnce.Do(func() { close(s.stalled) })

	// Honors the handler's own deadline, exactly as a real net.Conn does.
	// Nothing here picks a duration of its own.
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	<-timer.C
	return os.ErrDeadlineExceeded
}

func (s *flushStallingWriter) flushCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushes
}

// flushStallCase drives one stalled-flush scenario to completion.
//
// The three call sites differ only in which frame stalls, and writing them out
// three times would make it easy for one to drift into asserting less than the
// others.
type flushStallCase struct {
	name string
	// stallFlushAfter selects the frame whose flush stalls.
	stallFlushAfter int
	heartbeat       time.Duration
	// publish sends a domain event once the handshake has been flushed.
	publish bool
}

func runFlushStallCase(t *testing.T, tc flushStallCase) {
	t.Helper()

	const writeTimeout = 150 * time.Millisecond

	subscriber := newContextIgnoringSubscriber()
	handler, err := httpapi.NewHandler(newBarePlane(t),
		httpapi.WithRealtimeSubscriber(subscriber),
		httpapi.WithRealtimeWriteTimeout(writeTimeout),
		httpapi.WithHeartbeatInterval(tc.heartbeat))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}

	writer := newFlushStallingWriter(tc.stallFlushAfter)

	// Background context: the request is never cancelled, so nothing but the
	// write deadline can free this handler.
	request := httptest.NewRequest("GET", "/v1/realtime?run_id=run-1", nil).
		WithContext(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(writer, request)
	}()

	if tc.publish {
		// The handshake's flush proves Subscribe returned and the handler is
		// in its loop, so the frame below cannot race registration.
		waitFor(t, func() bool { return writer.flushCount() >= 1 }, "handshake flushed")
		subscriber.events <- platform.RealtimeEvent{
			Kind:        platform.RealtimeObservationRecorded,
			Scope:       platform.RealtimeScope{ProjectID: "proj-1", AgentID: "agent-1", RunID: "run-1"},
			Observation: platform.RealtimeObservation{Sequence: 1},
		}
	}

	select {
	case <-writer.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("the stalling flush never began")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// Generous on purpose: the assertion is that the handler returns at
		// all, and a failure here is a hang, not a slow machine.
		t.Fatal("handler did not return after its flush deadline expired")
	}

	// Exactly once, and by the handler itself — the subscriber double ignores
	// the request context, so bus-side cancellation cannot stand in for this.
	if got := subscriber.closes(); got != 1 {
		t.Errorf("subscription Close calls = %d, want 1", got)
	}
}

// TestSSEStalledFlushTerminatesTheStream covers every frame kind.
//
// Splitting it this way is the point: a regression that bounded event frames
// but left the heartbeat flushing error-blind would still pass a test that
// only ever stalled an observation.
func TestSSEStalledFlushTerminatesTheStream(t *testing.T) {
	tests := []flushStallCase{
		{
			// The status line is already 200 here, and that is correct:
			// deadline support was verified before it, and this is a runtime
			// transport failure after the stream began. Hanging up is the only
			// honest report — an error envelope written into a live SSE body
			// would be parsed as frames.
			name: "stream_ready", stallFlushAfter: 0, heartbeat: time.Hour,
		},
		{
			name: "observation", stallFlushAfter: 1, heartbeat: time.Hour, publish: true,
		},
		{
			// No domain event at all: the heartbeat is the only thing that can
			// flush, so nothing else can be what terminates the stream.
			name: "heartbeat", stallFlushAfter: 1, heartbeat: 20 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFlushStallCase(t, tt) })
	}
}
