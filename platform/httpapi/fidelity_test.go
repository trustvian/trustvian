package httpapi_test

import (
	"encoding/json"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// Fidelity on the wire: task 075's acceptance criterion 9, "fidelity is reported
// rather than implied".
//
// The value travels in the ingest envelope beside the record, for the same reason
// behavioral_profile does — it is metadata about how the record was *produced*,
// and the engine has no opinion about it. It then reaches the realtime
// observation, which is where a live view reads it.

func fidelityRecord(operation string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     "evt-" + operation,
		Timestamp:   testEpoch,
		ActorID:     "agent-1",
		ActorType:   event.ActorTypeAIAgent,
		Environment: testEnvironment,
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     operation,
			TargetName:        "export.localhost",
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       testEnvironment,
		},
		FingerprintID:      "fp-" + operation,
		IdentityConfidence: 0.9,
		AnomalyScore:       0.2,
		AnomalyConfidence:  0.5,
		TrustScore:         0.8,
		ContextRisk:        0.1,
		RiskLevel:          "low",
		Decision:           "allow",
		PolicyReason:       "default allow",
		MatchedDefault:     true,
	}
}

// envelopeWithFidelity is the ingest body with the optional field set.
func envelopeWithFidelity(sequence uint64, record trustvian.DecisionRecord, fidelity string) map[string]any {
	body := envelope(sequence, record)
	if fidelity != "" {
		body["fidelity"] = fidelity
	}
	return body
}

// TestFidelityReachesTheRealtimeObservation is the delivered half of criterion 9.
func TestFidelityReachesTheRealtimeObservation(t *testing.T) {
	tests := []struct {
		name string
		sent string
		want string
	}{
		{"semantic is carried through", "semantic", "semantic"},
		{"transport is carried through", "transport", "transport"},
		// The compatibility case: a producer built before task 075 sends no
		// field, and must be read as having proved nothing rather than as
		// unknown.
		{"absent reads as transport", "", "transport"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")

			s := a.connect("?run_id=run-1", nil)
			defer s.close()
			decodeReady(t, s.next())

			a.mustPost("/v1/evaluation-runs/run-1/records",
				envelopeWithFidelity(1, fidelityRecord("export_customer"), tt.sent), 200)

			frame := s.next()
			var payload struct {
				Observation *struct {
					Fidelity string `json:"fidelity"`
					Behavior struct {
						OperationCategory string `json:"operation_category"`
						OperationName     string `json:"operation_name"`
					} `json:"behavior"`
				} `json:"observation"`
			}
			if err := json.Unmarshal([]byte(frame.Data), &payload); err != nil {
				t.Fatalf("decode frame: %v\n%s", err, frame.Data)
			}
			if payload.Observation == nil {
				t.Fatalf("no observation in the frame: %s", frame.Data)
			}
			if payload.Observation.Fidelity != tt.want {
				t.Errorf("fidelity = %q, want %q", payload.Observation.Fidelity, tt.want)
			}
			// And the behavior it qualifies is still there, so the field is
			// describing something.
			if got := payload.Observation.Behavior.OperationName; got != "export_customer" {
				t.Errorf("operation_name = %q, want export_customer", got)
			}
		})
	}
}

// TestFidelityIsAlwaysPresentOnTheWire is why the DTO field has no omitempty.
//
// A consumer that had to interpret an absent field would be guessing at exactly
// the thing this field exists to remove the guess from — and "absent" would mean
// two different things depending on whether the producer or the server omitted it.
func TestFidelityIsAlwaysPresentOnTheWire(t *testing.T) {
	a := newRealtimeAPI(t)
	a.seedRunning("run-1", "cand-1")

	s := a.connect("?run_id=run-1", nil)
	defer s.close()
	decodeReady(t, s.next())

	// Nothing sent.
	a.mustPost("/v1/evaluation-runs/run-1/records",
		envelope(1, fidelityRecord("export_customer")), 200)

	frame := s.next()
	if !strings.Contains(frame.Data, `"fidelity"`) {
		t.Errorf("the observation omits the fidelity field entirely:\n%s", frame.Data)
	}
}

// TestUnknownFidelityIsRefused: the vocabulary is Trustvian's own, not an
// external convention.
//
// Unlike the GenAI and OpenInference attributes the indicator describes — which
// degrade, because Trustvian does not control them — this value is a closed
// two-member set Trustvian defines. An unrecognized value is a client bug, and
// accepting it would put a string on the wire that no consumer can interpret.
//
// Absent stays legitimate and is tested above: that is "not stated", not invalid.
func TestUnknownFidelityIsRefused(t *testing.T) {
	for _, bogus := range []string{"high", "SEMANTIC", "0.9", "partial", "unknown"} {
		t.Run(bogus, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")

			response := a.post("/v1/evaluation-runs/run-1/records",
				envelopeWithFidelity(1, fidelityRecord("export_customer"), bogus))
			defer response.Body.Close()
			if response.StatusCode != 400 {
				t.Errorf("status = %d, want 400 for fidelity %q", response.StatusCode, bogus)
			}
		})
	}
}

// TestFidelityDoesNotEnterBehavioralIdentity is the invariant that protects
// baselines from an instrumentation upgrade.
//
// Two records for the same behavior at different fidelities share a fingerprint
// and a behavior descriptor. If they did not, the day a team upgraded its
// instrumentation every behavior would look new.
func TestFidelityDoesNotEnterBehavioralIdentity(t *testing.T) {
	a := newAPI(t)
	a.seedHierarchy()
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": "run-1", "candidate_id": "cand-1",
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/start", nil), 200, "start")

	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelopeWithFidelity(1, fidelityRecord("export_customer"), "semantic")), 200, "semantic")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/run-1/records",
		envelopeWithFidelity(2, fidelityRecord("export_customer"), "transport")), 200, "transport")

	progress := a.do("GET", "/v1/evaluation-runs/run-1/progress", nil).Body.String()
	// Two records, one behavior: fidelity did not split the fingerprint.
	if !strings.Contains(progress, `"record_count":"2"`) {
		t.Errorf("record_count is not 2:\n%s", progress)
	}
	if !strings.Contains(progress, `"distinct_behavior_count":1`) {
		t.Errorf("fidelity split one behavior into two:\n%s", progress)
	}
}

// TestFidelityIsNotPersistedYet records a limitation rather than hiding it.
//
// The compare response's behavior deltas carry no fidelity, because a delta is
// built from persisted behavioral evidence and fidelity is not stored per
// behavior — that needs a schema step, which is called out in this task's report
// rather than smuggled into it.
//
// The test exists so the gap is visible in the suite instead of being discovered
// by whoever first looks for the field. It fails, deliberately, the moment
// fidelity IS persisted — at which point it should be replaced by the positive
// assertion rather than deleted.
func TestFidelityIsNotPersistedYet(t *testing.T) {
	a := newAPI(t)
	a.completeRun("run-ref", "cand-1", []string{"read"})

	response := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref",
		"candidate_run_id": "run-ref",
		"gate_limits":      limitsBody("0", "0", "0"),
	})
	a.mustStatus(response, 200, "compare")

	if strings.Contains(response.Body.String(), `"fidelity"`) {
		t.Error("the compare response now carries fidelity — persisted per-behavior " +
			"fidelity has landed, so replace this test with the positive assertion " +
			"that a delta reports it")
	}
}
