package localruntime_test

// The acceptance proof for task 062: the whole local chain, with nothing
// mocked.
//
// event.Event → real trustvian.Engine → Result.DecisionRecord() → real HTTP
// over a real loopback listener → ControlPlane → SQLite → realtime → progress.
//
// Testing against ControlPlane directly would skip the exact boundary this
// task exists to integrate, so every step below crosses the network.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	"trustvian-platform/localruntime"
)

const behavioralProfile = "checkout-agent"

// apiClient is a minimal HTTP helper; the point is to use the real wire.
type apiClient struct {
	t       *testing.T
	baseURL string
}

func (c *apiClient) post(path string, body any) (int, []byte) {
	c.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		c.t.Fatalf("marshal: %v", err)
	}
	request, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		c.t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	return c.do(request)
}

func (c *apiClient) get(path string) (int, []byte) {
	c.t.Helper()
	request, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		c.t.Fatalf("NewRequest: %v", err)
	}
	return c.do(request)
}

func (c *apiClient) do(request *http.Request) (int, []byte) {
	c.t.Helper()
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		c.t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, body
}

func (c *apiClient) mustPost(path string, body any, wantStatus int) []byte {
	c.t.Helper()
	status, payload := c.post(path, body)
	if status != wantStatus {
		c.t.Fatalf("POST %s: status = %d, want %d; body = %s", path, status, wantStatus, payload)
	}
	return payload
}

// sseReader consumes frames from a live stream.
type sseReader struct {
	t      *testing.T
	body   io.ReadCloser
	reader *bufio.Reader
}

func openSSE(t *testing.T, ctx context.Context, baseURL, runID string) *sseReader {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, "GET",
		baseURL+"/v1/realtime?run_id="+runID, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("realtime: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<10))
		response.Body.Close()
		t.Fatalf("realtime status = %d; body = %s", response.StatusCode, body)
	}
	t.Cleanup(func() { response.Body.Close() })
	return &sseReader{t: t, body: response.Body, reader: bufio.NewReader(response.Body)}
}

// next returns the next named frame, skipping heartbeats.
func (s *sseReader) next() (string, string) {
	s.t.Helper()
	var event, data string
	for {
		line, err := s.reader.ReadString('\n')
		if err != nil {
			s.t.Fatalf("reading SSE: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if event != "" {
				return event, data
			}
		case strings.HasPrefix(line, ":"):
			continue
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// TestLocalRuntimeEndToEnd is the acceptance test.
func TestLocalRuntimeEndToEnd(t *testing.T) {
	stateDir := t.TempDir()

	runtime, err := localruntime.Start(t.Context(), localruntime.Options{StateDir: stateDir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	api := &apiClient{t: t, baseURL: runtime.APIURL()}

	// --- control entities, over the real API ---
	api.mustPost("/v1/projects", map[string]string{
		"id": "proj-1", "name": "Checkout"}, 201)
	api.mustPost("/v1/agents", map[string]string{
		"id": "agent-1", "project_id": "proj-1", "name": "Checkout agent"}, 201)
	api.mustPost("/v1/candidates", map[string]any{
		"id": "cand-1", "agent_id": "agent-1",
		"metadata": map[string]string{"label": "v2"}}, 201)
	api.mustPost("/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": "local", "behavioral_profile": behavioralProfile}, 201)
	api.mustPost("/v1/evaluation-runs/run-1/start", nil, 200)

	// --- subscribe before producing, so nothing committed is missed ---
	streamCtx, cancelStream := context.WithCancel(t.Context())
	defer cancelStream()
	stream := openSSE(t, streamCtx, runtime.APIURL(), "run-1")

	if name, data := stream.next(); name != "stream_ready" {
		t.Fatalf("first frame = %s (%s), want stream_ready", name, data)
	}

	// --- the real engine produces the record ---
	//
	// The engine sees only a generic learning scope. It has no idea a
	// candidate or an evaluation run exists, and task 062 does not teach it.
	engine := trustvian.NewEngine(trustvian.WithLearningScope(behavioralProfile))

	observed := event.Event{
		ID:        "evt-1",
		Timestamp: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		Actor: event.Actor{
			ID: "svc-checkout", Type: event.ActorTypeService, IdentityConfidence: 0.95},
		Operation: event.Operation{Category: "http", Name: "POST /payment"},
		Target:    event.Target{Name: "payment-db"},
		Context:   event.Context{Environment: "local"},
	}

	result, err := engine.Analyze(t.Context(), observed)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if _, err := engine.Observe(t.Context(), result); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	record := result.DecisionRecord()
	if record.FingerprintID == "" {
		t.Fatal("the decision record carries no fingerprint")
	}

	// --- ingest it over HTTP, exactly as a producer would ---
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	ingestBody := api.mustPost("/v1/evaluation-runs/run-1/records", map[string]any{
		"version": "1", "sequence": "1",
		"behavioral_profile": behavioralProfile,
		"record":             json.RawMessage(encodedRecord),
	}, 200)

	var ingest struct {
		Disposition  string `json:"disposition"`
		NextSequence string `json:"next_sequence"`
		RecordCount  string `json:"record_count"`
	}
	if err := json.Unmarshal(ingestBody, &ingest); err != nil {
		t.Fatalf("ingest response: %v", err)
	}
	if ingest.Disposition != "applied" || ingest.RecordCount != "1" ||
		ingest.NextSequence != "2" {
		t.Fatalf("ingest = %+v, want applied/1/2", ingest)
	}

	// --- the realtime observation arrives ---
	name, data := stream.next()
	if name != "observation" {
		t.Fatalf("frame = %s (%s), want observation", name, data)
	}
	var observation struct {
		Observation struct {
			Sequence      string `json:"sequence"`
			FingerprintID string `json:"fingerprint_id"`
			NewBehavior   bool   `json:"new_behavior"`
		} `json:"observation"`
	}
	if err := json.Unmarshal([]byte(data), &observation); err != nil {
		t.Fatalf("observation frame: %v", err)
	}
	if observation.Observation.Sequence != "1" {
		t.Errorf("sequence = %q, want 1", observation.Observation.Sequence)
	}
	if observation.Observation.FingerprintID != record.FingerprintID {
		t.Errorf("realtime fingerprint %q != record %q",
			observation.Observation.FingerprintID, record.FingerprintID)
	}
	if !observation.Observation.NewBehavior {
		t.Error("the first behavior of a run should be new")
	}

	// --- progress reflects the durable evidence ---
	status, progressBody := api.get("/v1/evaluation-runs/run-1/progress")
	if status != 200 {
		t.Fatalf("progress status = %d; body = %s", status, progressBody)
	}
	var progress struct {
		Status                string `json:"status"`
		RecordCount           string `json:"record_count"`
		DistinctBehaviorCount int    `json:"distinct_behavior_count"`
		NextIngestSequence    string `json:"next_ingest_sequence"`
	}
	if err := json.Unmarshal(progressBody, &progress); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if progress.RecordCount != "1" || progress.NextIngestSequence != "2" ||
		progress.DistinctBehaviorCount != 1 || progress.Status != "running" {
		t.Fatalf("progress = %+v", progress)
	}

	// --- complete the run and see the lifecycle event ---
	api.mustPost("/v1/evaluation-runs/run-1/complete", nil, 200)
	if name, data := stream.next(); name != "evaluation_completed" {
		t.Fatalf("frame = %s (%s), want evaluation_completed", name, data)
	}
	cancelStream()

	// --- stop, and restart against the same state directory ---
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	restarted, err := localruntime.Start(t.Context(), localruntime.Options{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer restarted.Stop(context.Background())

	// The endpoint is new — which is exactly why discovery exists.
	if restarted.APIURL() == runtime.APIURL() {
		t.Log("the restarted runtime happened to reuse the same port")
	}

	restartedAPI := &apiClient{t: t, baseURL: restarted.APIURL()}
	status, runBody := restartedAPI.get("/v1/evaluation-runs/run-1")
	if status != 200 {
		t.Fatalf("run after restart: status = %d; body = %s", status, runBody)
	}
	var run struct {
		ID                string `json:"id"`
		Status            string `json:"status"`
		BehavioralProfile string `json:"behavioral_profile"`
	}
	if err := json.Unmarshal(runBody, &run); err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.ID != "run-1" || run.Status != "completed" ||
		run.BehavioralProfile != behavioralProfile {
		t.Fatalf("run after restart = %+v", run)
	}

	status, progressBody = restartedAPI.get("/v1/evaluation-runs/run-1/progress")
	if status != 200 {
		t.Fatalf("progress after restart: status = %d", status)
	}
	if err := json.Unmarshal(progressBody, &progress); err != nil {
		t.Fatalf("progress after restart: %v", err)
	}
	// Durable evidence survives; realtime deliberately does not.
	if progress.RecordCount != "1" || progress.DistinctBehaviorCount != 1 {
		t.Fatalf("evidence did not survive restart: %+v", progress)
	}
	if progress.Status != "completed" {
		t.Errorf("status after restart = %q, want completed", progress.Status)
	}
}

// TestNoRawEventEndpointExists keeps the producer boundary where task 058 put
// it.
//
// A server-side analyze route would be a second engine, with its own
// configuration and its own drift.
func TestNoRawEventEndpointExists(t *testing.T) {
	runtime, err := localruntime.Start(t.Context(), localruntime.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runtime.Stop(context.Background())

	api := &apiClient{t: t, baseURL: runtime.APIURL()}
	for _, path := range []string{"/v1/events", "/v1/analyze", "/v1/engine", "/v1/baselines"} {
		status, _ := api.post(path, map[string]string{"id": "evt-1"})
		if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d; no raw event route may exist", path, status)
		}
	}
}

// TestRestartedRuntimeRepublishesDiscovery shows why clients must re-read it.
func TestRestartedRuntimeRepublishesDiscovery(t *testing.T) {
	stateDir := t.TempDir()

	first, err := localruntime.Start(t.Context(), localruntime.Options{StateDir: stateDir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	firstURL := first.APIURL()
	if err := first.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	second, err := localruntime.Start(t.Context(), localruntime.Options{StateDir: stateDir})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer second.Stop(context.Background())

	discovery, err := localruntime.ReadDiscovery(second.DiscoveryPath())
	if err != nil {
		t.Fatalf("ReadDiscovery: %v", err)
	}
	if discovery.APIURL != second.APIURL() {
		t.Fatalf("discovery = %q, want the current %q", discovery.APIURL, second.APIURL())
	}
	fmt.Fprint(io.Discard, firstURL)
}
