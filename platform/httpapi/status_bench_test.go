package httpapi_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// worstStatusReport is a report at every bound the processor would actually
// send: 64 producers with maximal names and no scope lists (fitReport drops
// those first), 64 models and 64 transport targets, every string 256 bytes.
func worstStatusReport(collector string, sequence int) string {
	long := func(prefix string, i int) string {
		return fmt.Sprintf("%s-%04d-%s", prefix, i, strings.Repeat("x", 245-len(prefix)))
	}
	type m = map[string]any
	var producers, models, targets []m
	for i := range 64 {
		producers = append(producers, m{
			"service_name": long("svc", i), "spans": "1000", "last_seen_age_ms": "500",
			"scopes": []m{}, "scopes_truncated": true,
			"sdk": m{"name": long("n", i), "language": long("l", i), "version": long("v", i)},
		})
		models = append(models, m{"provider": long("p", i), "model": long("m", i), "calls": "10"})
		targets = append(targets, m{"target": long("t", i), "spans": "100", "distinct_operations": "32",
			"operations_saturated": true})
	}
	body, _ := json.Marshal(m{
		"version": "1", "collector_id": collector, "instance": "00000000000000aa",
		"sequence": fmt.Sprint(sequence), "uptime_ms": "100000",
		"receiver_endpoints": []string{"127.0.0.1:4318", "127.0.0.1:4317"}, "evaluation_run_id": "run-1",
		"spans":     m{"received": "64000", "evaluated": "63000", "invalid": "1000", "analyze_errors": "0"},
		"producers": producers, "producers_truncated": true,
		"models": models, "models_truncated": false,
		"fidelity":          m{"semantic": "30000", "transport": "33000"},
		"transport_targets": targets, "transport_targets_truncated": false,
		"actors": m{"bound_by_override": "0", "bound_by_service_name": "63000", "unbound": "1000"},
		"learning": m{"learned": "60000", "not_learned": "3000", "observe_errors": "0",
			"not_learned_by_decision": []m{{"decision": "allow", "count": "1000"}, {"decision": "block", "count": "2000"}}},
	})
	return string(body)
}

func typicalStatusReport(sequence int) string {
	body := strings.TrimSuffix(strings.TrimSpace(statusReportBody), "}") + slice2Sections
	return strings.Replace(body, `"sequence": "3"`, fmt.Sprintf(`"sequence": "%d"`, sequence), 1)
}

func benchmarkReport(b *testing.B, report func(int) string) {
	s := newStatusAPI(b)
	sequence := 0
	b.ReportAllocs()
	b.SetBytes(int64(len(report(1))))
	for b.Loop() {
		sequence++
		request := httptest.NewRequest(http.MethodPost, "/v1/collectors/dev/status", strings.NewReader(report(sequence)))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		s.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			b.Fatalf("POST = %d %s", recorder.Code, recorder.Body.String())
		}
	}
}

// BenchmarkStatusReportTypical is one Collector's report as `trustvian dev`
// sends it every ten seconds: one producer, a model, two targets.
func BenchmarkStatusReportTypical(b *testing.B) { benchmarkReport(b, typicalStatusReport) }

// BenchmarkStatusReportWorst is a report at every bound with maximal strings.
func BenchmarkStatusReportWorst(b *testing.B) {
	benchmarkReport(b, func(sequence int) string { return worstStatusReport("dev", sequence) })
}

func benchmarkRead(b *testing.B, seed func(*statusAPI)) {
	s := newStatusAPI(b)
	seed(s)
	b.ReportAllocs()
	var size int
	for b.Loop() {
		recorder := httptest.NewRecorder()
		s.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
		if recorder.Code != http.StatusOK {
			b.Fatalf("GET = %d", recorder.Code)
		}
		size = recorder.Body.Len()
	}
	b.ReportMetric(float64(size), "doc-bytes")
}

// BenchmarkPipelineStatusEmpty is the read a WebUI makes at startup when
// nothing has reported yet.
func BenchmarkPipelineStatusEmpty(b *testing.B) { benchmarkRead(b, func(*statusAPI) {}) }

// BenchmarkPipelineStatusTypical is the startup read with one dev Collector.
func BenchmarkPipelineStatusTypical(b *testing.B) {
	benchmarkRead(b, func(s *statusAPI) { s.post("/v1/collectors/dev/status", typicalStatusReport(1)) })
}

// BenchmarkPipelineStatusWorst is 16 Collectors, each at every bound: the
// largest document the route can produce.
func BenchmarkPipelineStatusWorst(b *testing.B) {
	benchmarkRead(b, func(s *statusAPI) {
		for i := range 16 {
			s.post(fmt.Sprintf("/v1/collectors/c%d/status", i), worstStatusReport(fmt.Sprintf("c%d", i), 1))
		}
	})
}

// TestWorstStatusDocumentFitsEveryClient: the largest document the route can
// produce — 16 Collectors at every bound — must stay inside the response bound
// the CLI and the WebUI both enforce (4 MiB), or a reader at the bounds would
// get an error instead of the status.
func TestWorstStatusDocumentFitsEveryClient(t *testing.T) {
	s := newStatusAPI(t)
	for i := range 16 {
		if r := s.post(fmt.Sprintf("/v1/collectors/c%d/status", i), worstStatusReport(fmt.Sprintf("c%d", i), 1)); r.Code != http.StatusOK {
			t.Fatalf("POST = %d %s", r.Code, r.Body.String())
		}
	}
	const clientResponseBound = 4 << 20
	if size := s.get("/v1/status").Body.Len(); size > clientResponseBound {
		t.Fatalf("the worst status document is %d bytes, over the clients' %d byte bound", size, clientResponseBound)
	}
}
