package httpapi_test

// The behavioral layer on the wire (task 083).
//
// The layer rides beside the record like fidelity, and reaches the realtime
// observation a live view reads. One difference is asserted repeatedly because it
// is the whole point of the type: absent means "not classified" and is **not**
// read as transport. Fidelity answers "did telemetry name this operation", where
// no is honestly transport; the layer answers "what kind of operation was this",
// where silence is silence.

import (
	"encoding/json"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// envelopeWithLayer is the ingest body with the optional layer field set.
func envelopeWithLayer(sequence uint64, record trustvian.DecisionRecord, layer string) map[string]any {
	body := envelope(sequence, record)
	if layer != "" {
		body["behavior_layer"] = layer
	}
	return body
}

type observationLayer struct {
	Observation *struct {
		BehaviorLayer string `json:"behavior_layer"`
		Fidelity      string `json:"fidelity"`
		Behavior      struct {
			OperationName string `json:"operation_name"`
		} `json:"behavior"`
	} `json:"observation"`
}

func TestBehaviorLayerReachesTheRealtimeObservation(t *testing.T) {
	tests := []struct {
		name string
		sent string
		want string
	}{
		{"model is carried through", "model", "model"},
		{"tool is carried through", "tool", "tool"},
		{"retrieval is carried through", "retrieval", "retrieval"},
		{"transport is carried through", "transport", "transport"},
		// The compatibility case, and the one that differs from fidelity: a
		// producer built before task 083 sends no field, and that is published as
		// "not classified" rather than being defaulted to transport.
		{"absent stays unclassified", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")

			s := a.connect("?run_id=run-1", nil)
			defer s.close()
			decodeReady(t, s.next())

			a.mustPost("/v1/evaluation-runs/run-1/records",
				envelopeWithLayer(1, fidelityRecord("export_customer"), tt.sent), 200)

			frame := s.next()
			var payload observationLayer
			if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
				t.Fatalf("decode frame: %v\n%s", err, frame.Data)
			}
			if payload.Observation == nil {
				t.Fatalf("no observation in the frame: %s", frame.Data)
			}
			if payload.Observation.BehaviorLayer != tt.want {
				t.Errorf("behavior_layer = %q, want %q",
					payload.Observation.BehaviorLayer, tt.want)
			}
			if got := payload.Observation.Behavior.OperationName; got != "export_customer" {
				t.Errorf("operation_name = %q, want export_customer", got)
			}
		})
	}
}

// TestBehaviorLayerIsAlwaysPresentOnTheWire is why the DTO field has no
// omitempty: a consumer must be able to tell "not classified" from "this server
// does not publish the field", and an omitted field conflates them.
func TestBehaviorLayerIsAlwaysPresentOnTheWire(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	a.mustPost("/v1/evaluation-runs/run-1/records",
		envelope(1, fidelityRecord("export_customer")), 200)

	frame := s.next()
	if !strings.Contains(frame.Data, `"behavior_layer"`) {
		t.Errorf("the observation omits the behavior_layer field entirely:\n%s", frame.Data)
	}
}

// TestUnknownBehaviorLayerIsRefused: the vocabulary is Trustvian's own closed
// set, so an unrecognized value is a client bug and is refused rather than
// degraded.
func TestUnknownBehaviorLayerIsRefused(t *testing.T) {
	for _, bogus := range []string{"Tool", "llm", "outbound", "http", "unknown", " tool"} {
		t.Run(bogus, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")
			response := a.post("/v1/evaluation-runs/run-1/records",
				envelopeWithLayer(1, fidelityRecord("export_customer"), bogus))
			if response.StatusCode != 400 {
				t.Errorf("status = %d, want 400 for behavior_layer %q",
					response.StatusCode, bogus)
			}
		})
	}
}

// TestBehaviorLayerDoesNotEnterBehavioralIdentity is the invariant that protects
// baselines, asserted on the platform side of the boundary as well as the
// engine's.
//
// Two records for one behavior that disagree about their layer share a
// fingerprint and a behavior descriptor. If they did not, a classification added
// for the sake of a label would have re-partitioned every behavior — which is the
// outcome ADR 0047 refuses.
func TestBehaviorLayerDoesNotEnterBehavioralIdentity(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/start", nil), 200, "start")

	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelopeWithLayer(1, fidelityRecord("export_customer"), "tool")), 200, "tool")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelopeWithLayer(2, fidelityRecord("export_customer"), "transport")), 200, "transport")

	progress := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String()
	if !strings.Contains(progress, `"record_count":"2"`) {
		t.Errorf("record_count is not 2:\n%s", progress)
	}
	if !strings.Contains(progress, `"distinct_behavior_count":1`) {
		t.Errorf("the layer split one behavior into two:\n%s", progress)
	}
}
