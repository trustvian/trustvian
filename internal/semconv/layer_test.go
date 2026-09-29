package semconv_test

// The behavioral layer classification (task 083).
//
// The layer answers "what kind of operation was this", which is a different
// question from fidelity's "did telemetry name this operation". These tests pin
// the difference, and pin the one rule that keeps the two from disagreeing: a
// layer is claimed exactly when fidelity is semantic.

import (
	"testing"

	"github.com/trustvian/trustvian/internal/semconv"
)

func TestLayerVocabularyIsClosed(t *testing.T) {
	tests := []struct {
		layer semconv.Layer
		valid bool
	}{
		{semconv.LayerModel, true},
		{semconv.LayerTool, true},
		{semconv.LayerRetrieval, true},
		{semconv.LayerTransport, true},
		{semconv.LayerUnspecified, false},
		{semconv.Layer("Tool"), false},
		{semconv.Layer("llm"), false},
		{semconv.Layer("outbound"), false},
	}
	for _, tt := range tests {
		if got := tt.layer.Valid(); got != tt.valid {
			t.Errorf("Layer(%q).Valid() = %v, want %v", tt.layer, got, tt.valid)
		}
	}
}

// TestLayerIsClaimedExactlyWhenFidelityIsSemantic is the one-gate rule.
//
// Two indicators derived from one table must not be able to disagree about one
// span. If a row sets a layer without earning semantic fidelity, a consumer could
// be told "this was a tool call" about a behavior whose name came from a
// hostname — the fabrication task 075 exists to prevent, arriving through a
// different field.
func TestLayerIsClaimedExactlyWhenFidelityIsSemantic(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
	}{
		{"genai tool", map[string]any{
			"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "export_customer"}},
		{"genai tool with no tool name", map[string]any{
			"gen_ai.operation.name": "execute_tool"}},
		{"genai model", map[string]any{
			"gen_ai.operation.name": "chat", "gen_ai.request.model": "gemma3:4b"}},
		{"genai model with no model", map[string]any{
			"gen_ai.operation.name": "chat"}},
		{"genai retrieval", map[string]any{
			"gen_ai.operation.name": "retrieval", "gen_ai.data_source.id": "kb"}},
		{"genai operation outside the enum", map[string]any{
			"gen_ai.operation.name": "teleport"}},
		{"openinference tool", map[string]any{
			"openinference.span.kind": "TOOL", "tool.name": "export_customer"}},
		{"openinference tool with no name", map[string]any{
			"openinference.span.kind": "TOOL"}},
		{"openinference llm", map[string]any{
			"openinference.span.kind": "LLM", "llm.model_name": "gpt-4o"}},
		{"openinference retriever", map[string]any{
			"openinference.span.kind": "RETRIEVER"}},
		{"openinference chain, deliberately unmapped", map[string]any{
			"openinference.span.kind": "CHAIN"}},
		{"no convention at all", map[string]any{
			"http.request.method": "POST", "server.address": "export.localhost"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := semconv.Normalize(semconv.Span{Attributes: tt.attrs})
			semantic := n.Fidelity == semconv.FidelitySemantic
			classified := n.Layer.Valid()
			if semantic != classified {
				t.Errorf("fidelity semantic = %v but layer classified = %v (layer %q); "+
					"the two indicators disagree about one span",
					semantic, classified, n.Layer)
			}
		})
	}
}

// TestLayerSeparatesModelFromRetrieval is the distinction that justifies the
// field's existence: both map to the `external` category, and a category cannot
// tell them apart without becoming two categories.
func TestLayerSeparatesModelFromRetrieval(t *testing.T) {
	model := semconv.Normalize(semconv.Span{Attributes: map[string]any{
		"gen_ai.operation.name": "chat", "gen_ai.request.model": "gemma3:4b"}})
	retrieval := semconv.Normalize(semconv.Span{Attributes: map[string]any{
		"gen_ai.operation.name": "retrieval", "gen_ai.data_source.id": "kb"}})

	if model.OperationCategory != retrieval.OperationCategory {
		t.Fatalf("the premise changed: categories are now %q and %q",
			model.OperationCategory, retrieval.OperationCategory)
	}
	if model.Layer != semconv.LayerModel {
		t.Errorf("model layer = %q, want %q", model.Layer, semconv.LayerModel)
	}
	if retrieval.Layer != semconv.LayerRetrieval {
		t.Errorf("retrieval layer = %q, want %q", retrieval.Layer, semconv.LayerRetrieval)
	}
}

// TestLayerAttributeNameIsStable guards the wire name, which a trace backend and
// the WebUI both key on.
func TestLayerAttributeNameIsStable(t *testing.T) {
	if semconv.AttrLayer != "trustvian.behavior.layer" {
		t.Errorf("AttrLayer = %q; renaming it breaks every consumer keying on it",
			semconv.AttrLayer)
	}
}
