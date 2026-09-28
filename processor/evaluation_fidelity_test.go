package trustvianprocessor_test

// Fidelity on the Collector ingest path.
//
// Task 075 computed the indicator in the inbound mapping and wrote it onto the
// outbound span, but the processor's ingest envelope carried only version,
// sequence, behavioral_profile and record — so the value stopped at the
// Collector, and every record reaching a control plane this way read as
// transport no matter what the telemetry showed. These tests assert the value
// leaves this module, from a real span through a real Engine.Analyze.

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/trustvian/trustvian/event"
)

// toolTraces builds a span a GenAI producer emits for a tool call: the
// convention supplies the operation identity, so the mapping is semantic.
//
// The transport attributes are kept, exactly as a real instrumented client
// emits them — the convention's identity wins for the operation, and the
// transport still supplies the target.
func toolTraces(actorID, tool, target string) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", actorID)
	rs.Resource().Attributes().PutStr("deployment.environment.name", "local")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("POST")
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(spanStart))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(spanStart.Add(time.Millisecond)))
	span.Attributes().PutStr("gen_ai.operation.name", "execute_tool")
	span.Attributes().PutStr("gen_ai.tool.name", tool)
	span.Attributes().PutStr("http.request.method", "POST")
	span.Attributes().PutStr("server.address", target)
	var traceID pcommon.TraceID
	copy(traceID[:], "trace-"+tool+"0123456789abcdef")
	var spanID pcommon.SpanID
	copy(spanID[:], "span-"+tool+"01234567")
	span.SetTraceID(traceID)
	span.SetSpanID(spanID)
	return td
}

// TestIngestEnvelopeCarriesTheMappedFidelity is the regression: the value the
// inbound mapping established has to reach the control plane, because
// DecisionRecord cannot carry it and nothing downstream can re-derive it.
func TestIngestEnvelopeCarriesTheMappedFidelity(t *testing.T) {
	tests := []struct {
		name   string
		traces ptrace.Traces
		want   event.Fidelity
	}{
		{
			name:   "a convention supplied the operation identity",
			traces: toolTraces("support-agent", "export_customer", "export.localhost"),
			want:   event.FidelitySemantic,
		},
		{
			name:   "only protocol and target were available",
			traces: evaluationTraces("support-agent", "crm.localhost"),
			want:   event.FidelityTransport,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := newIngestAPIServer(t)
			proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
			if err != nil {
				t.Fatalf("CreateTraces() error = %v", err)
			}
			if err := proc.ConsumeTraces(context.Background(), tt.traces); err != nil {
				t.Fatalf("ConsumeTraces() error = %v", err)
			}

			got := cp.recordedFidelities()
			if len(got) != 1 {
				t.Fatalf("the run holds %d records, want 1", len(got))
			}
			if got[0] != string(tt.want) {
				t.Errorf("ingest envelope fidelity = %q, want %q", got[0], tt.want)
			}
		})
	}
}

// TestIngestFidelityMatchesTheOutboundSpanAttribute pins the two reports of one
// mapping to each other.
//
// They are read from the same Result by construction, and this is what keeps it
// that way: an operator reading `trustvian.fidelity` on the span and an operator
// reading the live view must never be told different things about one span.
func TestIngestFidelityMatchesTheOutboundSpanAttribute(t *testing.T) {
	cp := newIngestAPIServer(t)
	next := &consumertest.TracesSink{}
	proc, err := newTestProcessorWithConfig(t, next, cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(),
		toolTraces("support-agent", "export_customer", "export.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	forwarded := next.AllTraces()
	if len(forwarded) != 1 {
		t.Fatalf("forwarded %d batches, want 1", len(forwarded))
	}
	attrs := forwarded[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	onSpan, ok := attrs.Get(event.AttrFidelity)
	if !ok {
		t.Fatalf("the forwarded span carries no %s", event.AttrFidelity)
	}

	sent := cp.recordedFidelities()
	if len(sent) != 1 {
		t.Fatalf("the run holds %d records, want 1", len(sent))
	}
	if sent[0] != onSpan.Str() {
		t.Errorf("ingest envelope fidelity = %q, span attribute = %q; one span was reported two ways",
			sent[0], onSpan.Str())
	}
	if onSpan.Str() != string(event.FidelitySemantic) {
		t.Errorf("fidelity = %q, want semantic", onSpan.Str())
	}
}
