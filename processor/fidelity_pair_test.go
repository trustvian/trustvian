package trustvianprocessor_test

// Task 081: every envelope this Collector sends states a fidelity/layer pair
// the control plane counts as stated — never a partial pair and never a
// contradictory one — across named, unnamed, malformed and out-of-vocabulary
// conventions. This is the evidence for the invariant the platform keeps:
//
//	layer transport               == fidelity transport
//	model + tool + retrieval + "" == fidelity semantic

import (
	"context"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

func TestEveryEnvelopeStatesACountablePair(t *testing.T) {
	specs := []spanSpec{
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("crm.localhost", [8]byte{2}, [8]byte{}),
		{name: "chat", kind: ptrace.SpanKindClient, spanID: [8]byte{3}, attrs: map[string]string{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gemma3:4b"}},
		{name: "search", kind: ptrace.SpanKindClient, spanID: [8]byte{4}, attrs: map[string]string{
			"gen_ai.operation.name": "retrieval", "gen_ai.data_source.id": "kb"}},
		{name: "teleport", kind: ptrace.SpanKindClient, spanID: [8]byte{5}, attrs: map[string]string{
			"gen_ai.operation.name": "teleport"}},
		{name: "tool-no-name", kind: ptrace.SpanKindInternal, spanID: [8]byte{6}, attrs: map[string]string{
			"gen_ai.operation.name": "execute_tool"}},
		{name: "chain", kind: ptrace.SpanKindInternal, spanID: [8]byte{7}, attrs: map[string]string{
			"openinference.span.kind": "CHAIN"}},
		{name: "llm", kind: ptrace.SpanKindClient, spanID: [8]byte{8}, attrs: map[string]string{
			"openinference.span.kind": "LLM", "llm.model_name": "gpt-4o"}},
		{name: "bare", kind: ptrace.SpanKindInternal, spanID: [8]byte{9}},
	}
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ConsumeTraces(context.Background(), tracesFrom("support-agent", specs...)); err != nil {
		t.Fatal(err)
	}
	fidelities, layers := cp.recordedFidelities(), cp.recordedLayers()
	if len(fidelities) != len(specs) || len(layers) != len(specs) {
		t.Fatalf("%d envelopes, %d fidelities, %d layers for %d spans", len(cp.recorded()), len(fidelities),
			len(layers), len(specs))
	}
	countable := map[[2]string]bool{
		{"transport", "transport"}: true, {"semantic", "model"}: true, {"semantic", "tool"}: true,
		{"semantic", "retrieval"}: true, {"semantic", ""}: true,
	}
	for i := range specs {
		pair := [2]string{fidelities[i], layers[i]}
		if !countable[pair] {
			t.Errorf("span %q sent (%q, %q): not a pair the control plane counts as stated",
				specs[i].name, pair[0], pair[1])
		}
	}
}
