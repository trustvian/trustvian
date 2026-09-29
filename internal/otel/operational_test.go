package otel_test

// The SDK adapter's half of task 084's contract.
//
// The processor's paired suite asserts the same documented rules from the OTLP
// side. Both adapters call event.DurationFrom, so the availability rules cannot
// drift; what each suite pins is that its own timestamp and status types are
// read correctly into it.

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/trustvian/trustvian/event"
	trustvianotel "github.com/trustvian/trustvian/internal/otel"
)

// spanWithStatus records one span with a chosen status code, which recordSpan's
// bool cannot express for the Ok case.
func spanWithStatus(t *testing.T, code codes.Code, start, end time.Time) sdktrace.ReadOnlySpan {
	t.Helper()
	exporter := &capturingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	}()
	_, span := tp.Tracer("t").Start(context.Background(), "op", trace.WithTimestamp(start))
	if code != codes.Unset {
		span.SetStatus(code, "")
	}
	span.End(trace.WithTimestamp(end))
	if len(exporter.spans) != 1 {
		t.Fatalf("captured %d spans, want 1", len(exporter.spans))
	}
	return exporter.spans[0]
}

func TestSDKAdapterReadsDurationAvailability(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		end          time.Time
		wantNanos    uint64
		wantObserved bool
	}{
		{"an ordinary span", start.Add(1500 * time.Microsecond), 1_500_000, true},
		{"a genuine zero", start, 0, true},
		{"end before start", start.Add(-time.Second), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			span := recordSpan(t, nil, trace.SpanKindClient, "op", nil, false, start, tt.end)
			ev := trustvianotel.EventFromSpan(span)
			if ev.Execution.DurationObserved != tt.wantObserved {
				t.Fatalf("DurationObserved = %v, want %v",
					ev.Execution.DurationObserved, tt.wantObserved)
			}
			if ev.Execution.DurationNanos != tt.wantNanos {
				t.Errorf("DurationNanos = %d, want %d", ev.Execution.DurationNanos, tt.wantNanos)
			}
		})
	}
}

func TestSDKAdapterStatusMapping(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Millisecond)

	tests := []struct {
		code codes.Code
		want event.SpanStatus
	}{
		{codes.Unset, event.StatusUnset},
		{codes.Ok, event.StatusOK},
		{codes.Error, event.StatusError},
	}
	for _, tt := range tests {
		ev := trustvianotel.EventFromSpan(spanWithStatus(t, tt.code, start, end))
		if ev.Execution.Status != tt.want {
			t.Errorf("status for %v = %q, want %q", tt.code, ev.Execution.Status, tt.want)
		}
		if ev.Execution.Status == event.StatusUnavailable {
			t.Error("a span produced an unavailable status; every span carries a code")
		}
	}
}

// TestSDKAdapterReadsParentage covers the two states the SDK can establish.
func TestSDKAdapterReadsParentage(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	end := start.Add(time.Millisecond)

	exporter := &capturingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() {
		if err := tp.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown() error = %v", err)
		}
	}()
	tracer := tp.Tracer("t")

	ctx, parent := tracer.Start(context.Background(), "parent", trace.WithTimestamp(start))
	_, child := tracer.Start(ctx, "child", trace.WithTimestamp(start))
	child.End(trace.WithTimestamp(end))
	parent.End(trace.WithTimestamp(end))

	if len(exporter.spans) != 2 {
		t.Fatalf("captured %d spans, want 2", len(exporter.spans))
	}
	// The child ends first, so it is exported first — the ordering the counting
	// work has to handle, recorded here as a fact about the SDK.
	childEvent := trustvianotel.EventFromSpan(exporter.spans[0])
	parentEvent := trustvianotel.EventFromSpan(exporter.spans[1])

	if parentEvent.Context.SpanLineage != event.LineageRoot {
		t.Errorf("parent lineage = %q, want root", parentEvent.Context.SpanLineage)
	}
	if parentEvent.Context.ParentSpanID != "" {
		t.Errorf("a root names parent %q", parentEvent.Context.ParentSpanID)
	}
	if childEvent.Context.SpanLineage != event.LineageChild {
		t.Errorf("child lineage = %q, want child", childEvent.Context.SpanLineage)
	}
	if childEvent.Context.ParentSpanID != parentEvent.Context.SpanID {
		t.Errorf("child parent = %q, want the parent's span id %q",
			childEvent.Context.ParentSpanID, parentEvent.Context.SpanID)
	}
	// A trace-scoped reference: both sit in one trace, which is what makes the
	// parent id resolvable at all.
	if childEvent.Context.TraceID != parentEvent.Context.TraceID {
		t.Error("parent and child are in different traces")
	}
}

// TestSDKVolatileBridgeAndRecordedEvidenceAgree mirrors the processor's
// assertion, including the one documented divergence.
func TestSDKVolatileBridgeAndRecordedEvidenceAgree(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	t.Run("a positive duration reaches both", func(t *testing.T) {
		span := recordSpan(t, nil, trace.SpanKindClient, "op", nil, true,
			start, start.Add(2*time.Millisecond))
		ev := trustvianotel.EventFromSpan(span)

		ms, ok := ev.Attributes["duration_ms"].(float64)
		if !ok {
			t.Fatal("the volatile bridge wrote no duration_ms")
		}
		if want := float64(ev.Execution.DurationNanos) / float64(time.Millisecond); ms != want {
			t.Errorf("duration_ms = %v, recorded nanos imply %v", ms, want)
		}
		if ev.Attributes["error"] != true {
			t.Error("the volatile bridge did not flag an error span")
		}
		if ev.Execution.Status != event.StatusError {
			t.Errorf("recorded status = %q, want error", ev.Execution.Status)
		}
	})

	t.Run("a zero duration is the documented divergence", func(t *testing.T) {
		span := recordSpan(t, nil, trace.SpanKindClient, "op", nil, false, start, start)
		ev := trustvianotel.EventFromSpan(span)

		if _, present := ev.Attributes["duration_ms"]; present {
			t.Error("the volatile bridge now writes a zero duration; task 084 must not " +
				"change the feature path")
		}
		if !ev.Execution.DurationObserved || ev.Execution.DurationNanos != 0 {
			t.Error("the evidence path must record a measured zero")
		}
	})
}
