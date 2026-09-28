package semconv_test

import (
	"testing"

	"github.com/trustvian/trustvian/internal/semconv"
)

func TestOpenInferenceMapping(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
		want  want
	}{
		{
			name: "TOOL names the tool",
			attrs: map[string]any{
				semconv.AttrOISpanKind: "TOOL",
				semconv.AttrOIToolName: "export_customer",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "export_customer",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "AGENT names the agent and establishes the actor",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "AGENT",
				semconv.AttrOIAgentName: "researcher",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "researcher",
				ActorType:         "ai_agent",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "LLM names the model and targets the provider",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "LLM",
				semconv.AttrOIModelName: "gpt-4o",
				semconv.AttrOIProvider:  "openai",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "gpt-4o",
				TargetName:        "openai",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "llm.system is the fallback provider — both are defined here",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "LLM",
				semconv.AttrOIModelName: "claude-3",
				semconv.AttrOISystem:    "anthropic",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "claude-3",
				TargetName:        "anthropic",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "llm.provider wins over llm.system",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "LLM",
				semconv.AttrOIModelName: "claude-3",
				semconv.AttrOIProvider:  "azure",
				semconv.AttrOISystem:    "anthropic",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "claude-3",
				TargetName:        "azure",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "EMBEDDING maps like LLM",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "EMBEDDING",
				semconv.AttrOIModelName: "text-embedding-3-small",
				semconv.AttrOIProvider:  "openai",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "text-embedding-3-small",
				TargetName:        "openai",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "RETRIEVER has no identity attribute, so the kind is the name",
			attrs: map[string]any{
				semconv.AttrOISpanKind: "RETRIEVER",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "retriever",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "RERANKER names the reranker model",
			attrs: map[string]any{
				semconv.AttrOISpanKind:     "RERANKER",
				semconv.AttrOIRerankerName: "cross-encoder/ms-marco",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "cross-encoder/ms-marco",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "session.id becomes the session",
			attrs: map[string]any{
				semconv.AttrOISpanKind:  "TOOL",
				semconv.AttrOIToolName:  "export_customer",
				semconv.AttrOISessionID: "sess-9",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "export_customer",
				SessionID:         "sess-9",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { run(t, tt.attrs, tt.want) })
	}
}

// TestOpenInferenceToolNameNeedsAToolSpan is the subtlest rule in either table.
//
// `tool.name` also appears under llm.tools.<index>.tool.name as an *advertised
// tool definition* on an LLM span. A model span that merely lists the tools
// available to it must not be recorded as having used one — that is a fabricated
// behavior, and acceptance criterion 4 forbids it.
//
// The bug this prevents is worth spelling out: without the kind check, an agent
// whose model span advertises `delete_account` would produce the behavior
// "used delete_account" on every single turn, including turns where the model
// chose nothing.
func TestOpenInferenceToolNameNeedsAToolSpan(t *testing.T) {
	t.Run("an LLM span advertising a tool does not use it", func(t *testing.T) {
		run(t, map[string]any{
			semconv.AttrOISpanKind:  "LLM",
			semconv.AttrOIModelName: "gpt-4o",
			semconv.AttrOIProvider:  "openai",
			// The flattened advertised-definition form, plus the bare key a
			// naive reader would pick up.
			"llm.tools.0.tool.name": "delete_account",
			semconv.AttrOIToolName:  "delete_account",
		}, want{
			OperationCategory: "external",
			OperationName:     "gpt-4o",
			TargetName:        "openai",
			Fidelity:          semconv.FidelitySemantic,
		})
	})

	t.Run("a CHAIN span carrying tool.name does not use it either", func(t *testing.T) {
		// CHAIN is unmapped, so this must yield nothing at all — not a tool.
		run(t, map[string]any{
			semconv.AttrOISpanKind: "CHAIN",
			semconv.AttrOIToolName: "delete_account",
		}, want{})
	})
}

// TestOpenInferenceUnmappedKinds covers the four defined kinds this table
// deliberately does not read, plus an unknown one.
//
// CHAIN is unmapped on purpose rather than for want of an attribute: its own spec
// calls it the glue code between steps, so it names no operation the actor
// performed. The other three have no identity attribute defined.
func TestOpenInferenceUnmappedKinds(t *testing.T) {
	for _, kind := range []string{"CHAIN", "GUARDRAIL", "EVALUATOR", "PROMPT", "SOMETHING_NEW", "tool"} {
		t.Run(kind, func(t *testing.T) {
			// session.id is still correlation and is still read; only identity
			// is withheld. Casing matters: "tool" is not "TOOL".
			run(t, map[string]any{
				semconv.AttrOISpanKind:  kind,
				semconv.AttrOIToolName:  "export_customer",
				semconv.AttrOISessionID: "sess-1",
			}, want{SessionID: "sess-1"})
		})
	}
}

// TestGenAIWinsOverOpenInference pins the precedence decision.
//
// A coin-flip made explicit rather than left to map iteration order. GenAI is the
// vendor-neutral OpenTelemetry convention, and a span carrying both is in practice
// an OpenInference producer that also emits GenAI — so preferring GenAI picks the
// more portable reading of the same event.
func TestGenAIWinsOverOpenInference(t *testing.T) {
	run(t, map[string]any{
		semconv.AttrGenAIOperationName: "execute_tool",
		semconv.AttrGenAIToolName:      "genai_tool",
		semconv.AttrOISpanKind:         "TOOL",
		semconv.AttrOIToolName:         "openinference_tool",
	}, want{
		OperationCategory: "tool",
		OperationName:     "genai_tool",
		Fidelity:          semconv.FidelitySemantic,
	})

	t.Run("OpenInference is used when GenAI declares nothing", func(t *testing.T) {
		run(t, map[string]any{
			semconv.AttrOISpanKind: "TOOL",
			semconv.AttrOIToolName: "openinference_tool",
		}, want{
			OperationCategory: "tool",
			OperationName:     "openinference_tool",
			Fidelity:          semconv.FidelitySemantic,
		})
	})

	t.Run("a GenAI operation this table does not map does not fall through", func(t *testing.T) {
		// The span declared which convention it speaks. Falling through to
		// OpenInference would mean one span read under two conventions, and the
		// result would depend on which attributes each happened to carry.
		run(t, map[string]any{
			semconv.AttrGenAIOperationName: "create_memory_store",
			semconv.AttrOISpanKind:         "TOOL",
			semconv.AttrOIToolName:         "openinference_tool",
		}, want{})
	})
}
