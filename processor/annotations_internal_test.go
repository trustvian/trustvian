package trustvianprocessor

import (
	"testing"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// TestAnnotationsNeverLeaveFidelityUnstated pins the processor's half of how
// a zero fidelity is read (finding F11 of #175). The control plane reads an
// unstated fidelity as transport on a realtime observation and as unrecorded in
// a behavior's persisted fidelity — pinned on that side by
// TestUnstatedFidelityIsUnrecordedInEvidence in the platform module. The two
// readings never meet a record from this Collector only because annotationsOf
// never produces a zero Fidelity, which is what this asserts, for every source
// a zero could come from. TestEveryEnvelopeStatesACountablePair proves the same
// end to end through ConsumeTraces.
func TestAnnotationsNeverLeaveFidelityUnstated(t *testing.T) {
	bare := ptrace.NewSpan()
	bare.SetName("do-a-thing")
	bare.SetKind(ptrace.SpanKindInternal)

	tests := []struct {
		name   string
		result trustvian.Result
		want   event.Fidelity
	}{
		{"an Event that never passed through EventFromSpan", trustvian.Result{}, event.FidelityTransport},
		{"a span no convention matched", trustvian.Result{Event: EventFromSpan(pcommon.NewMap(), bare)},
			event.FidelityTransport},
		{"an unrecognized recorded value", trustvian.Result{Event: event.Event{
			Attributes: map[string]any{event.AttrFidelity: "bogus"}}}, event.FidelityTransport},
		{"an empty recorded value", trustvian.Result{Event: event.Event{
			Attributes: map[string]any{event.AttrFidelity: ""}}}, event.FidelityTransport},
		{"a semantic mapping", trustvian.Result{Event: event.Event{
			Attributes: map[string]any{event.AttrFidelity: "semantic"}}}, event.FidelitySemantic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := annotationsOf(tt.result).Fidelity; got != tt.want {
				t.Fatalf("annotationsOf(...).Fidelity = %q, want %q", got, tt.want)
			}
		})
	}
}

// BenchmarkAnnotationsOf splits the common case, a span that states no usage
// and no status code, from one that states all of them (task 087). Both read
// a few map keys and must not allocate.
func BenchmarkAnnotationsOf(b *testing.B) {
	cases := []struct {
		name  string
		attrs map[string]any
	}{
		{"no operational facts", map[string]any{
			event.AttrFidelity: "semantic", event.AttrLayer: "model",
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "llama3.2",
		}},
		{"usage and status", map[string]any{
			event.AttrFidelity: "semantic", event.AttrLayer: "model",
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "llama3.2",
			"gen_ai.usage.input_tokens": int64(120), "gen_ai.usage.output_tokens": int64(30),
			"http.response.status_code": int64(200),
		}},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			result := trustvian.Result{Event: event.Event{Attributes: c.attrs}}
			b.ReportAllocs()
			for b.Loop() {
				annotationsOf(result)
			}
		})
	}
}
