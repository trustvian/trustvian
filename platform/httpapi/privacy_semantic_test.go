package httpapi_test

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// Privacy at the platform's enforcing layers, for task 075's semantic path.
//
// The core module asserts StableFeatures, the fingerprint and the DecisionRecord
// (internal/otel/privacy_test.go). These are the layers that live here: the
// realtime frame, what the store persists, and every /v1 response a client can
// read back.
//
// TestSSECarriesNoRawEventPayload already covers one distinctive attribute on the
// realtime path. This extends it to *every* content attribute both agent-oriented
// conventions define — twenty-two of them — because task 075 adds a path on which
// a producer is far more likely to emit one, and names each individually so a
// failure says which leaked rather than that something did.

// contentCanaries returns one distinctive value per content attribute.
func contentCanaries() map[string]string {
	out := make(map[string]string)
	replacer := strings.NewReplacer(".", "-", "_", "-")
	for _, key := range event.ContentAttributes() {
		out[key] = "CANARY-" + replacer.Replace(key) + "-8fa3"
	}
	return out
}

// semanticRecord is the DecisionRecord a task 075 tool span produces, from an
// event carrying every content attribute.
//
// Built through the real engine rather than by hand: the question is what survives
// the chain, and a hand-built record would only prove that this test remembered to
// leave the canaries out itself.
func semanticRecord(t *testing.T, canaries map[string]string) trustvian.DecisionRecord {
	t.Helper()

	attrs := make(map[string]any, len(canaries))
	for key, value := range canaries {
		attrs[key] = value
	}

	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))
	result, err := engine.Analyze(t.Context(), event.Event{
		ID:        "evt-semantic",
		Timestamp: testEpoch.Add(time.Minute),
		Actor: event.Actor{
			ID: "agent-1", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
		},
		// What a tool span maps to under task 075: the tool names the operation.
		Operation: event.Operation{
			Category: event.OperationCategoryTool,
			Name:     "export_customer",
		},
		Target: event.Target{Name: "export.localhost", Category: event.TargetCategoryExternal},
		Context: event.Context{
			Environment: testEnvironment,
			// Correlation, newly reachable from telemetry in this task.
			SessionID: "conv-1",
		},
		Attributes: attrs,
	})
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	return result.DecisionRecord()
}

// TestSemanticPathLeaksNoContentToAnyV1Payload sweeps every readable response.
func TestSemanticPathLeaksNoContentToAnyV1Payload(t *testing.T) {
	canaries := contentCanaries()
	if len(canaries) == 0 {
		t.Fatal("the content deny-list is empty; this test would pass vacuously")
	}
	record := semanticRecord(t, canaries)

	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/start", nil), 200, "start")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, record)), 200, "ingest")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/complete", nil), 200, "complete")

	maxUint := platform.FormatSequence(math.MaxUint64)
	comparison := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-1",
		"candidate_run_id": "run-1",
		"gate_limits":      limitsBody(maxUint, maxUint, maxUint),
	})
	a.mustStatus(comparison, 200, "compare")

	// Raw bodies, because the question is whether a canary appears anywhere in
	// the bytes. A structured check would only look where somebody thought to.
	bodies := map[string]string{
		"GET /v1/evaluation-runs/run-1":              a.do("GET", "/v1/evaluation-runs/run-1", nil).Body.String(),
		"GET /v1/evaluation-runs/run-1/progress":     a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String(),
		"GET /v1/evaluation-runs/run-1/ingest-state": a.do("GET", "/v1/evaluation-runs/run-1/ingest-state", nil).Body.String(),
		// Task 067. The route with the largest published field set, and the
		// first that survives the run that produced it — which is exactly why
		// it is swept here rather than trusted to its own contract.
		"GET /v1/evaluation-runs/run-1/observations": a.do("GET", "/v1/evaluation-runs/run-1/observations", nil).Body.String(),
		// Task 085. Resolution is the route whose whole purpose is to make
		// evidence reachable, so it is the one most worth sweeping rather than
		// trusting to the allowlist it inherits.
		"GET /v1/evidence/behaviors": a.do("GET",
			"/v1/evidence/behaviors?reference_run_id=run-1&candidate_run_id=run-1&check=added_behaviors", nil).Body.String(),
		"GET /v1/evidence/observations": a.do("GET",
			// A self-compare makes every behavior shared, so the side is stated: a
			// shared behavior has no natural side and the control plane refuses to
			// pick one.
			"/v1/evidence/observations?reference_run_id=run-1&candidate_run_id=run-1&side=candidate&behavior="+record.FingerprintID, nil).Body.String(),
		"GET /v1/candidates/cand-1":                 a.do("GET", "/v1/candidates/cand-1", nil).Body.String(),
		"GET /v1/agents/agent-1/candidates":         a.do("GET", "/v1/agents/agent-1/candidates", nil).Body.String(),
		"GET /v1/candidates/cand-1/evaluation-runs": a.do("GET", "/v1/candidates/cand-1/evaluation-runs", nil).Body.String(),
		"POST /v1/evaluations/compare":              comparison.Body.String(),
	}

	for key, canary := range canaries {
		for name, body := range bodies {
			if strings.Contains(body, canary) {
				t.Errorf("content attribute %q reached %s\n  value: %s", key, name, canary)
			}
		}
	}

	// The resolution route must have returned the behavior it was asked about,
	// or its silence proves nothing either.
	if !strings.Contains(bodies["GET /v1/evidence/observations"], "export_customer") {
		t.Errorf("the evidence resolution holds no behavior, so its absence of "+
			"canaries proves nothing:\n%s", bodies["GET /v1/evidence/observations"])
	}

	// The observation route must have returned the behavior, or its silence
	// above proves nothing.
	if !strings.Contains(bodies["GET /v1/evaluation-runs/run-1/observations"], "export_customer") {
		t.Errorf("the observation page holds no behavior, so its absence of canaries "+
			"proves nothing:\n%s", bodies["GET /v1/evaluation-runs/run-1/observations"])
	}

	// Absence must not have been achieved by losing the behavior with it.
	if !strings.Contains(comparison.Body.String(), "export_customer") {
		t.Errorf("the comparison lost the tool name, so the absence above proves nothing:\n%s",
			comparison.Body.String())
	}
}

// TestSemanticPathLeaksNoContentToTheRealtimeFrame is the SSE half.
func TestSemanticPathLeaksNoContentToTheRealtimeFrame(t *testing.T) {
	canaries := contentCanaries()
	record := semanticRecord(t, canaries)

	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	a.ingest("run-1", 1, record)
	frame := s.next()

	for key, canary := range canaries {
		if strings.Contains(frame.Data, canary) {
			t.Errorf("content attribute %q reached the realtime frame\n  value: %s", key, canary)
		}
	}

	// And the projection still describes the behavior by its tool name, which is
	// what task 075 exists to deliver on this surface.
	payload := decodeEvent(t, frame)
	if payload.Observation == nil {
		t.Fatalf("no observation in the frame: %s", frame.Data)
	}
	if got := payload.Observation.Behavior.OperationName; got != "export_customer" {
		t.Errorf("Behavior.OperationName = %q, want export_customer", got)
	}
}

// TestSemanticPathPersistsNoContent reads what the store holds.
//
// A response can be clean while the row behind it holds the value, and that is the
// leak that matters most: it survives a restart, reaches a backup, and turns up in
// whatever query somebody runs later.
func TestSemanticPathPersistsNoContent(t *testing.T) {
	canaries := contentCanaries()
	record := semanticRecord(t, canaries)

	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/start", nil), 200, "start")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelope(1, record)), 200, "ingest")

	// Read back through the store the handler was built on, at the level the
	// platform actually retains — behavioral evidence, not raw records. Both
	// halves are checked: the aggregate holds the numeric summaries and the
	// snapshot holds the per-behavior rows, and a leak could be in either.
	aggregate, snapshot, err := a.store.EvaluationEvidence(t.Context(), "run-1")
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	// Task 067's retained history is read the same way and for the same
	// reason: it is durable, it reaches a backup, and it is the newest place a
	// content value could come to rest.
	page, err := a.store.RunObservations(t.Context(), "run-1", 0, platform.MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) == 0 {
		t.Fatal("no observation was retained, so the assertion below proves nothing")
	}

	text := fmt.Sprintf("%+v\n%+v\n%+v", aggregate, snapshot, page.Observations)
	if !strings.Contains(text, "export_customer") {
		t.Fatalf("the persisted evidence holds no behavior, so this assertion proves "+
			"nothing:\n%s", text)
	}

	for key, canary := range canaries {
		if strings.Contains(text, canary) {
			t.Errorf("content attribute %q is persisted in the evaluation evidence\n  value: %s",
				key, canary)
		}
	}
}

// TestSessionCorrelationIsNotBehavioralIdentity guards the field task 075 newly
// populates from telemetry.
//
// event.Context's own doc comment explains why: a session id is unique per
// conversation, so folding it into a fingerprint would give every session a fresh
// Fingerprint and a fresh Baseline, defeating behavioral profiling across sessions
// entirely. Before this task a producer had to set SessionID by hand; now a
// gen_ai.conversation.id supplies it, so the guard earns its place on this path.
func TestSessionCorrelationIsNotBehavioralIdentity(t *testing.T) {
	engine := trustvian.NewEngine(trustvian.WithLearningScope(testProfile))

	build := func(session string) trustvian.Result {
		result, err := engine.Analyze(t.Context(), event.Event{
			ID:        "evt-" + session,
			Timestamp: testEpoch.Add(time.Minute),
			Actor: event.Actor{
				ID: "agent-1", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9,
			},
			Operation: event.Operation{Category: event.OperationCategoryTool, Name: "export_customer"},
			Target:    event.Target{Name: "export.localhost"},
			Context:   event.Context{Environment: testEnvironment, SessionID: session},
		})
		if err != nil {
			t.Fatalf("Analyze() error = %v", err)
		}
		return result
	}

	first, second := build("conv-1"), build("conv-99999")

	if first.Fingerprint.ID != second.Fingerprint.ID {
		t.Errorf("two conversations produced different fingerprints:\n  %s\n  %s\n"+
			"Every session would start from an empty baseline.",
			first.Fingerprint.ID, second.Fingerprint.ID)
	}
	if strings.Contains(fmt.Sprintf("%+v", first.Features.Stable), "conv-1") {
		t.Error("the session id appears in StableFeatures")
	}
}
