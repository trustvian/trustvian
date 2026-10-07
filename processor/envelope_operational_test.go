package trustvianprocessor_test

import (
	"bytes"
	"context"
	"maps"
	"testing"

	"github.com/trustvian/trustvian/event"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// Task 087: the HTTP status code and token usage ride beside the record on the
// ingest envelope, read from the span's own attributes. They never reach the
// record or its behavioral identity, and an absent or malformed value is an
// absent field, never "0".

// modelCallSpan is a GenAI chat span whose usage and status attributes are set
// by the caller with their real OTLP types.
func modelCallSpan(set func(pcommon.Map)) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "support-agent")
	s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	s.SetName("chat llama3.2")
	s.SetKind(ptrace.SpanKindClient)
	s.SetTraceID(pcommon.TraceID([16]byte{1}))
	s.SetSpanID(pcommon.SpanID([8]byte{1}))
	s.SetStartTimestamp(pcommon.Timestamp(1_700_000_000_000_000_000))
	s.SetEndTimestamp(pcommon.Timestamp(1_700_000_000_250_000_000))
	attrs := s.Attributes()
	attrs.PutStr("gen_ai.operation.name", "chat")
	attrs.PutStr("gen_ai.request.model", "llama3.2")
	attrs.PutStr("gen_ai.provider.name", "ollama")
	if set != nil {
		set(attrs)
	}
	return td
}

func TestOperationalFactsRideOnTheEnvelope(t *testing.T) {
	tests := []struct {
		name string
		set  func(pcommon.Map)
		want map[string]string
	}{
		{name: "nothing stated: no field at all", want: map[string]string{}},
		{name: "GenAI usage and a stable status code",
			set: func(m pcommon.Map) {
				m.PutInt("gen_ai.usage.input_tokens", 120)
				m.PutInt("gen_ai.usage.output_tokens", 0)
				m.PutInt("http.response.status_code", 429)
			},
			want: map[string]string{"tokens_input": "120", "tokens_output": "0", "http_status_code": "429"}},
		{name: "OpenInference total alone is unsplit",
			set:  func(m pcommon.Map) { m.PutInt("llm.token_count.total", 77) },
			want: map[string]string{"tokens_unsplit": "77"}},
		{name: "legacy status code",
			set:  func(m pcommon.Map) { m.PutInt("http.status_code", 503) },
			want: map[string]string{"http_status_code": "503"}},
		{name: "malformed values are absent, never zero",
			set: func(m pcommon.Map) {
				m.PutStr("gen_ai.usage.input_tokens", "120")
				m.PutInt("gen_ai.usage.output_tokens", -4)
				m.PutInt("http.response.status_code", 700)
			},
			want: map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := newIngestAPIServer(t)
			proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
			if err != nil {
				t.Fatalf("CreateTraces() error = %v", err)
			}
			if err := proc.ConsumeTraces(context.Background(), modelCallSpan(tt.set)); err != nil {
				t.Fatalf("ConsumeTraces() error = %v", err)
			}
			sent := cp.recordedOperational()
			if len(sent) != 1 {
				t.Fatalf("%d records reached the control plane, want 1", len(sent))
			}
			if !maps.Equal(sent[0], tt.want) {
				t.Fatalf("envelope carried %v, want %v", sent[0], tt.want)
			}
		})
	}
}

// TestOperationalFactsAreNotIdentity: the same model call with and without
// usage and status attributes is the same behavior, decided the same way.
func TestOperationalFactsAreNotIdentity(t *testing.T) {
	record := func(set func(pcommon.Map)) (string, string, string) {
		t.Helper()
		cp := newIngestAPIServer(t)
		proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
		if err != nil {
			t.Fatal(err)
		}
		if err := proc.ConsumeTraces(context.Background(), modelCallSpan(set)); err != nil {
			t.Fatal(err)
		}
		r := cp.recorded()[0]
		return r.FingerprintID, string(r.Decision), r.Behavior.OperationName
	}
	plainFP, plainDecision, plainName := record(nil)
	richFP, richDecision, richName := record(func(m pcommon.Map) {
		m.PutInt("gen_ai.usage.input_tokens", 5000)
		m.PutInt("gen_ai.usage.output_tokens", 900)
		m.PutInt("http.response.status_code", 500)
	})
	if plainFP != richFP || plainDecision != richDecision || plainName != richName {
		t.Fatalf("usage and status changed the behavior: (%s %s %s) vs (%s %s %s)",
			plainFP, plainDecision, plainName, richFP, richDecision, richName)
	}
}

// TestTheEnvelopeCarriesNoContent is the privacy tripwire on the new fields:
// a model call carrying every content attribute both conventions define, and
// usage beside them, sends the token counts and none of the content — checked
// on the raw request bytes, not on the fields this test thought to decode.
func TestTheEnvelopeCarriesNoContent(t *testing.T) {
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatal(err)
	}
	canaries := map[string]string{}
	td := modelCallSpan(func(m pcommon.Map) {
		m.PutInt("gen_ai.usage.input_tokens", 120)
		m.PutInt("gen_ai.usage.output_tokens", 30)
		for _, key := range event.ContentAttributes() {
			canaries[key] = "CANARY-087-" + key
			m.PutStr(key, canaries[key])
		}
	})
	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatal(err)
	}
	bodies := cp.recordedBodies()
	if len(bodies) != 1 || !bytes.Contains(bodies[0], []byte(`"tokens_input":"120"`)) {
		t.Fatalf("the envelope did not carry the tokens, so its silence proves nothing: %s", bodies)
	}
	for key, canary := range canaries {
		if bytes.Contains(bodies[0], []byte(canary)) {
			t.Errorf("content attribute %s reached the envelope", key)
		}
	}
}
