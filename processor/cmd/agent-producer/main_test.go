package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// fakeExporter answers every export with err, and counts the spans it was
// handed.
type fakeExporter struct {
	err   error
	spans int
}

func (f *fakeExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	f.spans += len(spans)
	return f.err
}

func (f *fakeExporter) Shutdown(context.Context) error { return nil }

// A producer whose exports all failed must not report them sent and exit 0:
// with WithSyncer the SDK hands an export error to the global error handler,
// which only logs it, and Shutdown then succeeds (finding F17 of #175).
func TestEmitFailsWhenNoSpanIsDelivered(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantErr string
	}{
		{"every export fails", errors.New("connection refused"), "0 of 3 spans delivered"},
		{"every export succeeds", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exporter := &fakeExporter{err: tt.err}
			err := emit(context.Background(), exporter, resource.Empty(), "transport", 3)
			if exporter.spans != 3 {
				t.Errorf("exported %d spans, want 3", exporter.spans)
			}
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("emit: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Errorf("emit returned %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}
