package trustvianprocessor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/trustvian/trustvian/event"

	trustvianprocessor "trustvian-processor"
)

// fakeStatusPlane records every status report a processor posts.
type fakeStatusPlane struct {
	mu      sync.Mutex
	bodies  []string
	reports []map[string]any
}

func newFakeStatusPlane(t *testing.T) (*fakeStatusPlane, string) {
	t.Helper()
	plane := &fakeStatusPlane{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var report map[string]any
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Errorf("status report is not JSON: %v", err)
		}
		plane.mu.Lock()
		plane.bodies = append(plane.bodies, string(raw))
		plane.reports = append(plane.reports, report)
		plane.mu.Unlock()
		_, _ = io.WriteString(w, `{"version":"1","disposition":"accepted"}`)
	}))
	t.Cleanup(server.Close)
	return plane, server.URL
}

// waitFor returns the first report satisfying ok, or fails after a bound.
func (f *fakeStatusPlane) waitFor(t *testing.T, ok func(map[string]any) bool) (map[string]any, string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for i, report := range f.reports {
			if ok(report) {
				body := f.bodies[i]
				f.mu.Unlock()
				return report, body
			}
		}
		f.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no matching status report arrived")
	return nil, ""
}

func statusTraces(sentinel bool) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr(string(semconv.ServiceNameKey), "support-agent")
	rs.Resource().Attributes().PutStr(string(semconv.TelemetrySDKNameKey), "opentelemetry")
	rs.Resource().Attributes().PutStr(string(semconv.TelemetrySDKLanguageKey), "python")
	rs.Resource().Attributes().PutStr(string(semconv.TelemetrySDKVersionKey), "1.27.0")
	ss := rs.ScopeSpans().AppendEmpty()
	ss.Scope().SetName("openinference.instrumentation.openai")
	ss.Scope().SetVersion("0.1.22")
	now := time.Now()
	for i := range 3 {
		span := ss.Spans().AppendEmpty()
		span.SetName("POST /v1/chat/completions")
		span.SetTraceID(pcommon.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
		span.SetSpanID(pcommon.SpanID{1, 2, 3, 4, 5, 6, 7, byte(i + 1)})
		span.SetKind(ptrace.SpanKindClient)
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Duration(i+1) * time.Millisecond)))
		span.Attributes().PutStr("http.request.method", "POST")
		span.Attributes().PutStr("server.address", "api.example.com")
		if sentinel {
			for _, key := range event.ContentAttributes() {
				span.Attributes().PutStr(key, "SENTINEL-CONTENT-"+key)
			}
		}
	}
	return td
}

func TestStatusReportCarriesProducersAndSpanCounts(t *testing.T) {
	plane, url := newFakeStatusPlane(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), &trustvianprocessor.Config{
		Status: &trustvianprocessor.StatusConfig{
			APIURL: url, CollectorID: "dev", Interval: time.Second,
			ReceiverEndpoints: []string{"127.0.0.1:4318"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The first report is sent at Start, before any span.
	first, _ := plane.waitFor(t, func(map[string]any) bool { return true })
	if first["version"] != "1" || first["collector_id"] != "dev" || first["sequence"] != "1" {
		t.Fatalf("first report identity: %v", first)
	}
	if spans := first["spans"].(map[string]any); spans["received"] != "0" {
		t.Fatalf("the first report already counts spans: %v", spans)
	}

	if err := proc.ConsumeTraces(context.Background(), statusTraces(true)); err != nil {
		t.Fatal(err)
	}

	report, body := plane.waitFor(t, func(r map[string]any) bool {
		return r["spans"].(map[string]any)["received"] == "3"
	})
	spans := report["spans"].(map[string]any)
	if spans["evaluated"] != "3" || spans["invalid"] != "0" || spans["analyze_errors"] != "0" {
		t.Fatalf("span counts: %v", spans)
	}
	if got := report["receiver_endpoints"].([]any); len(got) != 1 || got[0] != "127.0.0.1:4318" {
		t.Fatalf("receiver endpoints: %v", got)
	}
	producers := report["producers"].([]any)
	if len(producers) != 1 {
		t.Fatalf("producers: %v", producers)
	}
	producer := producers[0].(map[string]any)
	if producer["service_name"] != "support-agent" || producer["spans"] != "3" {
		t.Fatalf("producer: %v", producer)
	}
	sdk := producer["sdk"].(map[string]any)
	if sdk["name"] != "opentelemetry" || sdk["language"] != "python" || sdk["version"] != "1.27.0" {
		t.Fatalf("sdk: %v", sdk)
	}
	scopes := producer["scopes"].([]any)
	scope := scopes[0].(map[string]any)
	if len(scopes) != 1 || scope["name"] != "openinference.instrumentation.openai" || scope["version"] != "0.1.22" {
		t.Fatalf("scopes: %v", scopes)
	}

	// The privacy tripwire: every content attribute the conventions define was
	// planted on every span, and none may reach the report — nor may the span
	// name, which is transport detail the status surface never reads.
	for _, key := range event.ContentAttributes() {
		if strings.Contains(body, "SENTINEL-CONTENT") {
			t.Fatalf("content attribute %s reached the status report: %s", key, body)
		}
	}
	if strings.Contains(body, "chat/completions") {
		t.Fatalf("a span name reached the status report: %s", body)
	}
}

// genAITraces is one producer's batch: model calls named by GenAI, and four
// HTTP requests to one host that no convention named.
func genAITraces() ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr(string(semconv.ServiceNameKey), "support-agent")
	ss := rs.ScopeSpans().AppendEmpty()
	now := time.Now()
	add := func(i int, name string, attrs map[string]string) {
		span := ss.Spans().AppendEmpty()
		span.SetName(name)
		span.SetKind(ptrace.SpanKindClient)
		span.SetTraceID(pcommon.TraceID{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9})
		span.SetSpanID(pcommon.SpanID{9, 9, 9, 9, 9, 9, 9, byte(i + 1)})
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Millisecond)))
		for k, v := range attrs {
			span.Attributes().PutStr(k, v)
		}
	}
	for i := range 2 {
		add(i, "chat llama3.2", map[string]string{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "llama3.2", "gen_ai.provider.name": "ollama",
		})
	}
	for i, path := range []string{"/v1/chat", "/v1/embed", "/v1/rerank", "/v1/chat"} {
		add(10+i, "POST "+path, map[string]string{"http.request.method": "POST", "server.address": "api.example.com"})
	}
	return td
}

func TestStatusReportCarriesModelsAndFidelity(t *testing.T) {
	plane, url := newFakeStatusPlane(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), &trustvianprocessor.Config{
		Status: &trustvianprocessor.StatusConfig{APIURL: url, Interval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ConsumeTraces(context.Background(), genAITraces()); err != nil {
		t.Fatal(err)
	}
	report, body := plane.waitFor(t, func(r map[string]any) bool {
		return r["spans"].(map[string]any)["evaluated"] == "6"
	})
	if id, _ := report["collector_id"].(string); !strings.HasPrefix(id, "collector-") {
		t.Fatalf("an unconfigured collector id was not generated: %q", id)
	}
	models, _ := json.Marshal(report["models"])
	if want := `[{"calls":"2","model":"llama3.2","provider":"ollama"}]`; string(models) != want {
		t.Fatalf("models %s, want %s", models, want)
	}
	fidelity, _ := json.Marshal(report["fidelity"])
	if want := `{"semantic":"2","transport":"4"}`; string(fidelity) != want {
		t.Fatalf("fidelity %s, want %s", fidelity, want)
	}
	targets, _ := json.Marshal(report["transport_targets"])
	if want := `[{"distinct_operations":"3","operations_saturated":false,"spans":"4","target":"api.example.com"}]`; string(targets) != want {
		t.Fatalf("transport targets %s, want %s", targets, want)
	}
	if strings.Contains(body, "/v1/embed") {
		t.Fatalf("a transport operation name reached the report: %s", body)
	}
}

// TestStatusReportCountsActorBindingAndLearning covers the three links of the
// actor chain and the learning outcomes Observe reports.
func TestStatusReportCountsActorBindingAndLearning(t *testing.T) {
	plane, url := newFakeStatusPlane(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), &trustvianprocessor.Config{
		Status: &trustvianprocessor.StatusConfig{APIURL: url, Interval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	td := ptrace.NewTraces()
	now := time.Now()
	add := func(rs ptrace.ResourceSpans, i int, override string) {
		span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetName("GET /x")
		span.SetKind(ptrace.SpanKindServer)
		span.SetTraceID(pcommon.TraceID{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7})
		span.SetSpanID(pcommon.SpanID{7, 7, 7, 7, 7, 7, 7, byte(i + 1)})
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Millisecond)))
		span.Attributes().PutStr("http.request.method", "GET")
		if override != "" {
			span.Attributes().PutStr("trustvian.actor.id", override)
		}
	}
	named := td.ResourceSpans().AppendEmpty()
	named.Resource().Attributes().PutStr(string(semconv.ServiceNameKey), "svc")
	add(named, 0, "")
	add(named, 1, "explicit-actor")
	anonymous := td.ResourceSpans().AppendEmpty() // no service.name, no override
	add(anonymous, 2, "")
	add(anonymous, 3, "")

	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatal(err)
	}
	// received == "4" alone is not enough: the reporter ticks concurrently
	// with ConsumeTraces, and processSpan counts a span as received before it
	// counts its actor binding, outcome and learning. A report snapshotted
	// while the last span is in flight says received 4 with that span counted
	// nowhere else. Wait for a report in which every counter has caught up.
	report, _ := plane.waitFor(t, func(r map[string]any) bool {
		n := func(m any, k string) int {
			v, _ := strconv.Atoi(m.(map[string]any)[k].(string))
			return v
		}
		spans, actors, learning := r["spans"], r["actors"], r["learning"]
		return n(spans, "received") == 4 &&
			n(actors, "bound_by_override")+n(actors, "bound_by_service_name")+n(actors, "unbound") == 4 &&
			n(spans, "evaluated")+n(spans, "invalid")+n(spans, "analyze_errors") == 4 &&
			n(learning, "learned")+n(learning, "not_learned")+n(learning, "observe_errors") == n(spans, "evaluated")
	})
	actors, _ := json.Marshal(report["actors"])
	if want := `{"bound_by_override":"1","bound_by_service_name":"1","unbound":"2"}`; string(actors) != want {
		t.Fatalf("actors %s, want %s", actors, want)
	}
	if spans := report["spans"].(map[string]any); spans["invalid"] != "2" || spans["evaluated"] != "2" {
		t.Fatalf("an unbound span must be invalid and unevaluated: %v", spans)
	}
	learning := report["learning"].(map[string]any)
	if learning["learned"] != "2" || learning["not_learned"] != "0" || learning["observe_errors"] != "0" {
		t.Fatalf("learning: %v", learning)
	}
	// The producer that set no service.name is still a producer, reported
	// under the empty name rather than dropped.
	var names []string
	for _, p := range report["producers"].([]any) {
		names = append(names, p.(map[string]any)["service_name"].(string))
	}
	if strings.Join(names, ",") != ",svc" {
		t.Fatalf("producers %q", names)
	}
}

// TestStatusTransportTargetsAreNamedHTTPOnly is the review's case for rule 4:
// transport fidelity also holds DB spans and the RPC fallback — unmapped
// OpenInference kinds such as CHAIN, internal spans — and none of those is an
// HTTP call to a destination. Each must count as transport and never become a
// transport target, or an OpenInference producer would be told to add
// OpenInference.
func TestStatusTransportTargetsAreNamedHTTPOnly(t *testing.T) {
	plane, url := newFakeStatusPlane(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), &trustvianprocessor.Config{
		Status: &trustvianprocessor.StatusConfig{APIURL: url, Interval: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}

	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr(string(semconv.ServiceNameKey), "crew")
	ss := rs.ScopeSpans().AppendEmpty()
	now := time.Now()
	n := 0
	add := func(name string, kind ptrace.SpanKind, attrs map[string]string) {
		n++
		span := ss.Spans().AppendEmpty()
		span.SetName(name)
		span.SetKind(kind)
		span.SetTraceID(pcommon.TraceID{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5})
		span.SetSpanID(pcommon.SpanID{5, 5, 5, 5, 5, 5, 5, byte(n)})
		span.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
		span.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Millisecond)))
		for k, v := range attrs {
			span.Attributes().PutStr(k, v)
		}
	}
	// Unmapped OpenInference CHAIN spans: RPC fallback, no target, a new span
	// name each time — exactly the shape that filled the "" target.
	for _, name := range []string{"RetrievalQA", "StuffDocumentsChain", "LLMChain", "Crew.kickoff"} {
		add(name, ptrace.SpanKindInternal, map[string]string{"openinference.span.kind": "CHAIN"})
	}
	// DB spans: target is db.namespace, operation is the span name.
	for _, name := range []string{"SELECT orders", "INSERT orders", "UPDATE orders"} {
		add(name, ptrace.SpanKindClient, map[string]string{"db.system.name": "postgresql", "db.namespace": "shop"})
	}
	// HTTP spans that named no destination: no server.address, no peer.service.
	for _, path := range []string{"/a", "/b", "/c"} {
		add("POST "+path, ptrace.SpanKindClient, map[string]string{"http.request.method": "POST"})
	}

	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatal(err)
	}
	report, _ := plane.waitFor(t, func(r map[string]any) bool {
		return r["spans"].(map[string]any)["evaluated"] == "10"
	})
	targets, _ := json.Marshal(report["transport_targets"])
	if string(targets) != "[]" {
		t.Fatalf("non-HTTP or unnamed spans became transport targets: %s", targets)
	}
	fidelity, _ := json.Marshal(report["fidelity"])
	if want := `{"semantic":"0","transport":"10"}`; string(fidelity) != want {
		t.Fatalf("fidelity %s, want %s: every one of them is still transport", fidelity, want)
	}
}

func TestStatusConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		status trustvianprocessor.StatusConfig
		ok     bool
	}{
		{name: "minimal", status: trustvianprocessor.StatusConfig{APIURL: "http://127.0.0.1:8080"}, ok: true},
		{name: "no url", status: trustvianprocessor.StatusConfig{}},
		{name: "url with a path", status: trustvianprocessor.StatusConfig{APIURL: "http://127.0.0.1:8080/v1"}},
		{name: "interval too short", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", Interval: time.Millisecond}},
		{name: "interval too long", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", Interval: time.Hour}},
		{name: "collector id with a control character", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", CollectorID: "dev\n"}},
		{name: "collector id with surrounding space", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", CollectorID: " dev"}},
		{name: "too many endpoints", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", ReceiverEndpoints: []string{"a", "b", "c", "d", "e"}}},
		{name: "empty endpoint", status: trustvianprocessor.StatusConfig{
			APIURL: "http://127.0.0.1:8080", ReceiverEndpoints: []string{""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := tt.status
			_, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
				&trustvianprocessor.Config{Status: &status})
			if tt.ok && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
