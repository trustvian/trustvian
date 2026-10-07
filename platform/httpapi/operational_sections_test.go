package httpapi_test

// Task 087 over /v1: the envelope's operational fields in, the latency, errors
// and tokens sections out.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/event"
)

// operationalStep is one record and the task 087 fields its envelope carries.
type operationalStep struct {
	op       string
	duration string
	status   event.SpanStatus
	fields   map[string]string
	target   string // "" keeps apiRecord's build-host
}

func (a *api) completeOperationalRun(runID, candidateID string, steps []operationalStep) {
	a.t.Helper()
	a.seedHierarchy()
	if candidateID != "cand-1" {
		a.mustStatus(a.do("POST", "/v1/candidates", map[string]any{
			"id": candidateID, "agent_id": "agent-1", "metadata": map[string]string{"label": candidateID},
		}), 201, "create candidate")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs", map[string]string{
		"id": runID, "candidate_id": candidateID,
		"environment": testEnvironment, "behavioral_profile": testProfile,
	}), 201, "create run")
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/start", nil), 200, "start")
	for i, step := range steps {
		record := apiRecord(fmt.Sprintf("%s-evt-%d", runID, i), "fp-"+step.op, step.op)
		record.DurationNanos, record.SpanStatus = step.duration, step.status
		if step.target != "" {
			record.Behavior.TargetName = step.target
			record.FingerprintID += "@" + step.target
		}
		body := envelope(uint64(i+1), record)
		for k, v := range step.fields {
			body[k] = v
		}
		a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/records", body), 200, "ingest")
	}
	a.mustStatus(a.do("POST", "/v1/evaluation-runs/"+runID+"/complete", nil), 200, "complete")
}

// TestCompareCarriesOperationalSections pins the three sections byte for byte:
// a comparable section carries a delta, and a section one side did not report
// says which side and carries no delta field at all.
func TestCompareCarriesOperationalSections(t *testing.T) {
	a := newAPI(t)
	a.completeOperationalRun("run-ref", "cand-1", []operationalStep{
		{"read", "3000000", event.StatusOK, map[string]string{
			"http_status_code": "200", "tokens_input": "100", "tokens_output": "10"}, ""},
		{"list", "", event.StatusUnset, map[string]string{"http_status_code": "200"}, ""},
	})
	a.completeOperationalRun("run-can", "cand-2", []operationalStep{
		{"read", "12000000000", event.StatusError, map[string]string{"http_status_code": "429"}, ""},
		{"list", "40000000", event.StatusOK, nil, ""},
	})
	r := a.do("POST", "/v1/evaluations/compare", map[string]any{
		"reference_run_id": "run-ref", "candidate_run_id": "run-can",
		"gate_limits": limitsBody("10", "10", "10"),
	})
	a.mustStatus(r, 200, "compare")

	var body struct {
		Scorecard map[string]json.RawMessage `json:"scorecard"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	buckets := func(counts ...string) string {
		labels := []string{"le_1ms", "le_5ms", "le_10ms", "le_50ms", "le_100ms", "le_250ms", "le_500ms",
			"le_1000ms", "le_2500ms", "le_10000ms", "gt_10000ms"}
		parts := make([]string, len(labels))
		for i, l := range labels {
			parts[i] = `{"bound":"` + l + `","count":"` + counts[i] + `"}`
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	want := map[string]string{
		"latency": `{"comparable":true,` +
			`"reference":{"runs_with_evidence":"1","buckets":` + buckets("0", "1", "0", "0", "0", "0", "0", "0", "0", "0", "0") +
			`,"observed":"1","unobserved":"1","sum_nanos":"3000000","min_nanos":"3000000","max_nanos":"3000000"},` +
			`"candidate":{"runs_with_evidence":"1","buckets":` + buckets("0", "0", "0", "1", "0", "0", "0", "0", "0", "0", "1") +
			`,"observed":"2","unobserved":"0","sum_nanos":"12040000000","min_nanos":"40000000","max_nanos":"12000000000"},` +
			`"delta":{"buckets":` + buckets("0", "-1", "0", "1", "0", "0", "0", "0", "0", "0", "1") +
			`,"observed":"1","unobserved":"-1","sum_nanos":"12037000000","min_nanos":"37000000","max_nanos":"11997000000"}}`,
		"errors": `{"comparable":true,` +
			`"reference":{"runs_with_evidence":"1","span_status":{"unavailable":"0","unset":"1","ok":"1","error":"0"},` +
			`"http_status":{"1xx":"0","2xx":"2","3xx":"0","4xx":"0","5xx":"0","unavailable":"0"},"http_429":"0"},` +
			`"candidate":{"runs_with_evidence":"1","span_status":{"unavailable":"0","unset":"0","ok":"1","error":"1"},` +
			`"http_status":{"1xx":"0","2xx":"0","3xx":"0","4xx":"1","5xx":"0","unavailable":"1"},"http_429":"1"},` +
			`"delta":{"span_status":{"unavailable":"0","unset":"-1","ok":"0","error":"1"},` +
			`"http_status":{"1xx":"0","2xx":"-2","3xx":"0","4xx":"1","5xx":"0","unavailable":"1"},"http_429":"1"}}`,
		"tokens": `{"comparable":false,"reason":"candidate_unavailable",` +
			`"reference":{"runs_with_evidence":"1","input":"100","output":"10","unsplit":"0","observed":"1","unobserved":"1"},` +
			`"candidate":{"runs_with_evidence":"0","input":"0","output":"0","unsplit":"0","observed":"0","unobserved":"2"}}`,
	}
	for section, golden := range want {
		if got := string(body.Scorecard[section]); got != golden {
			t.Errorf("%s:\n got %s\nwant %s", section, got, golden)
		}
	}
}

// TestRepeatedComparisonCarriesOperationalSections: the sections reach the
// repeated response, and runs that reported nothing are unavailable with no
// delta and no run counted as carrying evidence.
func TestRepeatedComparisonCarriesOperationalSections(t *testing.T) {
	a := newAPI(t)
	for _, id := range []string{"ref-1", "ref-2", "can-1", "can-2"} {
		a.completeIsolatedRun(id, []string{"read"})
	}
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"ref-1", "ref-2"},
		"candidate_run_ids": []string{"can-1", "can-2"},
		"gate_limits":       repeatedLimitsBody("2", "0"),
	})
	a.mustStatus(r, 200, "compare repeated")
	var body struct {
		Operational map[string]struct {
			Comparable bool            `json:"comparable"`
			Reason     string          `json:"reason"`
			Delta      json.RawMessage `json:"delta"`
			Reference  struct {
				RunsWithEvidence string `json:"runs_with_evidence"`
			} `json:"reference"`
		} `json:"operational"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{"latency", "errors", "tokens"} {
		s, ok := body.Operational[section]
		if !ok {
			t.Fatalf("operational.%s is missing: %s", section, r.Body.String())
		}
		if s.Comparable || s.Reason != "both_unavailable" || s.Delta != nil || s.Reference.RunsWithEvidence != "0" {
			t.Errorf("operational.%s = %+v, want unavailable on both sides with no delta", section, s)
		}
	}
}
