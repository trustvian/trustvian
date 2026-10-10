package httpapi_test

import (
	"encoding/json"
	"fmt"
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

// observedOp is one ingested record of operation op, with the envelope's
// fidelity and layer ("" for absent).
type observedOp struct{ op, fidelity, layer string }

// completeRunWithFidelity creates, fills and completes one isolated run whose
// records carry the given envelope fidelity and layer.
func (a *api) completeRunWithFidelity(runID string, ops []observedOp) {
	a.t.Helper()
	a.seedHierarchy()
	profile := runID + "-profile"
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": "cand-1", "environment": testEnvironment, "behavioral_profile": profile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	for i, o := range ops {
		body := envelope(uint64(i+1), apiRecord(fmt.Sprintf("%s-e%d", runID, i), "fp-"+o.op, o.op))
		body["behavioral_profile"] = profile
		if o.fidelity != "" {
			body["fidelity"] = o.fidelity
		}
		if o.layer != "" {
			body["behavior_layer"] = o.layer
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records", body), 200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

// deltaMember is one delta's member, compact, by fingerprint.
func deltaMember(t *testing.T, body []byte, path []string, fingerprint, member string) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	raw := []byte(body)
	for _, p := range path {
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		raw = doc[p]
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if string(row["fingerprint_id"]) == `"`+fingerprint+`"` {
			return string(row[member])
		}
	}
	t.Fatalf("no row for %s", fingerprint)
	return ""
}

// TestComparisonsReportPersistedFidelity replaces TestFidelityIsNotPersistedYet
// with the positive assertion it asked for: a delta reports each side's
// fidelity, by the disagreement rule, with the counts that decided it — and a
// side that never observed the behavior reports none. The repeated comparison
// sums each side's runs first.
func TestComparisonsReportPersistedFidelity(t *testing.T) {
	a := newAPI(t)
	a.completeRunWithFidelity("run-ref", []observedOp{
		{"read", "semantic", "tool"}, {"read", "semantic", "tool"},
	})
	a.completeRunWithFidelity("run-cand", []observedOp{
		{"read", "semantic", "tool"}, {"read", "semantic", "tool"}, {"read", "transport", "transport"},
		{"chat", "semantic", "model"}, {"legacy", "", ""},
	})
	response := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref", "candidate_run_id": "run-cand",
		"gate_limits": limitsBody("10", "10", "10"),
	})
	a.mustStatus(response, 200, "compare")
	body := response.Body.Bytes()
	path := []string{"behavior_diff", "deltas"}
	for _, tt := range []struct{ fingerprint, member, want string }{
		{"fp-read", "reference_fidelity", `{"level":"semantic","mixed":false,"semantic":"2","transport":"0",` +
			`"unrecorded":"0","layer":{"model":"0","tool":"2","retrieval":"0","transport":"0",` +
			`"unclassified":"0","unrecorded":"0"}}`},
		// One transport observation makes the behavior transport, and mixed.
		{"fp-read", "candidate_fidelity", `{"level":"transport","mixed":true,"semantic":"2","transport":"1",` +
			`"unrecorded":"0","layer":{"model":"0","tool":"2","retrieval":"0","transport":"1",` +
			`"unclassified":"0","unrecorded":"0"}}`},
		{"fp-chat", "candidate_fidelity", `{"level":"semantic","mixed":false,"semantic":"1","transport":"0",` +
			`"unrecorded":"0","layer":{"model":"1","tool":"0","retrieval":"0","transport":"0",` +
			`"unclassified":"0","unrecorded":"0"}}`},
		{"fp-chat", "reference_fidelity", ``},
		{"fp-legacy", "candidate_fidelity", `{"level":"unrecorded","mixed":false,"semantic":"0","transport":"0",` +
			`"unrecorded":"1","layer":{"model":"0","tool":"0","retrieval":"0","transport":"0",` +
			`"unclassified":"0","unrecorded":"1"}}`},
	} {
		if got := deltaMember(t, body, path, tt.fingerprint, tt.member); got != tt.want {
			t.Errorf("%s %s =\n%s\nwant\n%s", tt.fingerprint, tt.member, got, tt.want)
		}
	}

	// Repeated: two candidate runs, each pure, sum to one mixed side.
	a.completeRunWithFidelity("rep-ref-1", []observedOp{{"read", "semantic", "tool"}})
	a.completeRunWithFidelity("rep-ref-2", []observedOp{{"read", "semantic", "tool"}})
	a.completeRunWithFidelity("rep-cand-1", []observedOp{{"read", "semantic", "tool"}})
	a.completeRunWithFidelity("rep-cand-2", []observedOp{{"read", "transport", "transport"}})
	repeated := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"rep-ref-1", "rep-ref-2"},
		"candidate_run_ids": []string{"rep-cand-1", "rep-cand-2"},
		"gate_limits":       repeatedLimitsBody("1", "0"),
	})
	a.mustStatus(repeated, 200, "compare-repeated")
	rbody := repeated.Body.Bytes()
	if got, want := deltaMember(t, rbody, []string{"behaviors"}, "fp-read", "candidate_fidelity"),
		`{"level":"transport","mixed":true,"semantic":"1","transport":"1","unrecorded":"0",`+
			`"layer":{"model":"0","tool":"1","retrieval":"0","transport":"1","unclassified":"0","unrecorded":"0"}}`; got != want {
		t.Errorf("repeated candidate fidelity =\n%s\nwant\n%s", got, want)
	}
	if got := deltaMember(t, rbody, []string{"behaviors"}, "fp-read", "reference_fidelity"); !strings.Contains(got, `"level":"semantic","mixed":false,"semantic":"2"`) {
		t.Errorf("repeated reference fidelity = %s", got)
	}
}

// TestMaxLLMCallsPerRunOverHTTP: the limit is accepted, echoed and evaluated
// from the persisted layer counts, and a candidate run with no model-layer
// observation defers it (decision D9) — including one whose tools are named.
func TestMaxLLMCallsPerRunOverHTTP(t *testing.T) {
	a := newAPI(t)
	a.completeRunWithFidelity("llm-ref", []observedOp{{"chat", "semantic", "model"}})
	a.completeRunWithFidelity("llm-cand-1", []observedOp{
		{"chat", "semantic", "model"}, {"chat", "semantic", "model"}, {"read", "semantic", "tool"}})
	a.completeRunWithFidelity("llm-cand-plain", []observedOp{{"chat", "transport", "transport"}})
	a.completeRunWithFidelity("llm-cand-tools", []observedOp{{"read", "semantic", "tool"}, {"chat", "transport", "transport"}})
	compare := func(candidate, limit string) map[string]any {
		limits := repeatedLimitsBody("1", "0")
		limits["max_llm_calls_per_run"] = limit
		r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
			"reference_run_ids": []string{"llm-ref"}, "candidate_run_ids": []string{candidate},
			"gate_limits": limits,
		})
		a.mustStatus(r, 200, "compare-repeated")
		var body struct {
			GateLimits map[string]any `json:"gate_limits"`
			Gate       struct {
				Verdict         string           `json:"verdict"`
				FrequencyChecks []map[string]any `json:"frequency_checks"`
			} `json:"gate"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.GateLimits["max_llm_calls_per_run"] != limit || len(body.Gate.FrequencyChecks) != 4 {
			t.Fatalf("echo %v, checks %v", body.GateLimits, body.Gate.FrequencyChecks)
		}
		check := body.Gate.FrequencyChecks[3]
		check["verdict"] = body.Gate.Verdict
		return check
	}
	if c := compare("llm-cand-1", "2"); c["name"] != "max_llm_calls_per_run" || c["state"] != "evaluated" ||
		c["actual"] != "2" || c["bound"] != "2" || c["passed"] != true {
		t.Errorf("evaluated check = %v", c)
	}
	if c := compare("llm-cand-1", "1"); c["passed"] != false || c["verdict"] != "fail" {
		t.Errorf("violated check = %v", c)
	}
	for _, candidate := range []string{"llm-cand-plain", "llm-cand-tools"} {
		want := `run "` + candidate + `" has no model-layer observation; model calls cannot be counted`
		if c := compare(candidate, "100"); c["state"] != "deferred" || c["verdict"] != "fail" ||
			c["missing_evidence"] != want {
			t.Errorf("%s: deferred check = %v, want missing evidence %q", candidate, c, want)
		}
	}
}
