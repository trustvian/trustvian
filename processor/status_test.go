package trustvianprocessor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
