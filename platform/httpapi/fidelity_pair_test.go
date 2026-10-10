package httpapi_test

// Task 081 on the ingest route: a contradictory fidelity/layer pair is refused
// with 400, and every other pair the contract allows — partial ones included —
// is accepted exactly as before.

import "testing"

func TestIngestRefusesOnlyContradictoryFidelityPairs(t *testing.T) {
	for _, tt := range []struct {
		fidelity, layer string
		status          int
	}{
		{"transport", "model", 400}, {"transport", "tool", 400}, {"transport", "retrieval", 400},
		{"semantic", "transport", 400},
		{"", "", 200}, {"transport", "transport", 200}, {"semantic", "model", 200},
		{"semantic", "", 200}, {"transport", "", 200}, {"", "tool", 200},
	} {
		t.Run(tt.fidelity+"/"+tt.layer, func(t *testing.T) {
			a := newRealtimeAPI(t)
			a.seedRunning("run-1", "cand-1")
			body := envelope(1, fidelityRecord("export_customer"))
			if tt.fidelity != "" {
				body["fidelity"] = tt.fidelity
			}
			if tt.layer != "" {
				body["behavior_layer"] = tt.layer
			}
			response := a.post("/v1/evaluation-runs/run-1/records", body)
			defer response.Body.Close()
			if response.StatusCode != tt.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tt.status)
			}
		})
	}
}
