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
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func (s *contextIgnoringSubscriber) Subscribe(
	ctx context.Context, filter platform.RealtimeFilter,
) (platform.RealtimeSubscription, error) {
	s.events = make(chan platform.RealtimeEvent, 4)
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
	subscriber := &contextIgnoringSubscriber{}

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
