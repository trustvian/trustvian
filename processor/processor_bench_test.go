package trustvianprocessor_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	trustvianprocessor "trustvian-processor"
)

// noopConsumer discards every batch — used so this benchmark measures
// only this processor's own cost, not a downstream consumer's.
type noopConsumer struct{}

func (noopConsumer) Capabilities() consumer.Capabilities                { return consumer.Capabilities{} }
func (noopConsumer) ConsumeTraces(context.Context, ptrace.Traces) error { return nil }

var _ consumer.Traces = noopConsumer{}

// BenchmarkConsumeTraces measures end-to-end processor throughput —
// span in, mapped, analyzed, enriched, forwarded — for a realistic
// single-span batch (an HTTP server span with a resource and a
// measured duration, the same shape BenchmarkAttributesFromResult and
// BenchmarkEventFromSpan use in the core module). This is the
// "spans/sec under realistic span volume" benchmark task 009 asks for;
// b.N batches are pre-built outside the timed loop so only
// ConsumeTraces itself is measured.
func BenchmarkConsumeTraces(b *testing.B) {
	next := noopConsumer{}
	proc := newTestProcessor(b, next)

	td := buildTraces("svc-payment", 0.95)

	b.ReportAllocs()
	for b.Loop() {
		if err := proc.ConsumeTraces(context.Background(), td); err != nil {
			b.Fatalf("ConsumeTraces() error = %v", err)
		}
	}
}

// BenchmarkConsumeTracesWithMetricsSDK is BenchmarkConsumeTraces with a
// real SDK MeterProvider in place of the Collector's no-op one, so the
// pair measures exactly one variable: what task 043's instrumentation
// costs on the span path.
//
// The no-op case is not a fair "before" on its own — no-op instruments
// short-circuit — so both numbers are needed to say anything honest about
// overhead.
func BenchmarkConsumeTracesWithMetricsSDK(b *testing.B) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	set := processor.Settings{
		ID:                component.NewID(component.MustNewType("trustvian")),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}
	set.TelemetrySettings.MeterProvider = provider

	proc, err := trustvianprocessor.NewFactory().CreateTraces(
		context.Background(), set, &trustvianprocessor.Config{}, noopConsumer{})
	if err != nil {
		b.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		b.Fatalf("Start() error = %v", err)
	}
	b.Cleanup(func() { _ = proc.Shutdown(context.Background()) })

	td := buildTraces("svc-payment", 0.95)

	b.ReportAllocs()
	for b.Loop() {
		if err := proc.ConsumeTraces(context.Background(), td); err != nil {
			b.Fatalf("ConsumeTraces() error = %v", err)
		}
	}
}

// BenchmarkConsumeTracesWithEvaluation is the price of an
// evaluation-configured span, measured rather than asserted.
//
// Against a loopback stub the request itself is cheap, so what this number
// actually shows is the cost of the restart guarantee: two durable writes of
// the pending entry per span — before the request, and after the control
// plane confirms it (ADR 0038 §10) — plus the JSON round trip of the Result
// that entry carries. Each write is fsynced, so this measurement is mostly a
// measurement of the filesystem: on macOS, where Sync means F_FULLFSYNC, it
// is milliseconds; on a Linux SSD it is a fraction of one.
//
// The fsync is the point rather than an oversight. An entry that reached
// only the page cache survives a process dying, which is the common case,
// but not the host dying — and a lost entry is exactly the ambiguity this
// design removes.
//
// Compare it against BenchmarkConsumeTraces, which is the same span path
// with no evaluation block: that one takes no lock, writes no file, and is
// what every deployment that never configures `evaluation:` still pays.
func BenchmarkConsumeTracesWithEvaluation(b *testing.B) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/progress"):
			_, _ = io.WriteString(w, `{"version":"1","run_id":"run-1","status":"running"}`)
		case strings.HasSuffix(r.URL.Path, "/ingest-state"):
			_, _ = io.WriteString(w, `{"version":"1","run_id":"run-1","next_sequence":"1"}`)
		default:
			var envelope struct {
				Sequence string `json:"sequence"`
			}
			_ = json.NewDecoder(r.Body).Decode(&envelope)
			next, _ := strconv.ParseUint(envelope.Sequence, 10, 64)
			_, _ = io.WriteString(w, `{"version":"1","disposition":"applied","next_sequence":"`+
				strconv.FormatUint(next+1, 10)+`","record_count":"1","behavior_complete":true}`)
		}
	}))
	defer stub.Close()

	required := true
	cfg := &trustvianprocessor.Config{Evaluation: &trustvianprocessor.EvaluationConfig{
		APIURL:            stub.URL,
		RunID:             "run-1",
		BehavioralProfile: "support-reference",
		Required:          &required,
		PendingStatePath:  filepath.Join(b.TempDir(), "pending.json"),
	}}
	proc, err := newTestProcessorWithConfig(b, noopConsumer{}, cfg)
	if err != nil {
		b.Fatalf("CreateTraces() error = %v", err)
	}

	td := buildTraces("svc-payment", 0.95)

	b.ReportAllocs()
	for b.Loop() {
		if err := proc.ConsumeTraces(context.Background(), td); err != nil {
			b.Fatalf("ConsumeTraces() error = %v", err)
		}
	}
}
