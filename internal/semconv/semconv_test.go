package semconv_test

import (
	"maps"
	"reflect"
	"testing"

	"github.com/trustvian/trustvian/event"
	"github.com/trustvian/trustvian/internal/semconv"
)

// want is the expected Normalized, spelled out per case.
//
// A full struct rather than field-by-field assertions: the interesting failures
// are the ones where a field is populated that should not have been, and
// comparing whole values catches those without a test having to predict them.
type want = semconv.Normalized

func run(t *testing.T, attrs map[string]any, expected want) {
	t.Helper()
	// Copied, so the purity assertion below is about the caller's map.
	input := maps.Clone(attrs)
	got := semconv.Normalize(semconv.Span{
		Kind:       semconv.KindClient,
		Name:       "POST",
		Attributes: input,
	})
	if got != expected {
		t.Errorf("Normalize()\n got  %+v\n want %+v", got, expected)
	}
	// reflect.DeepEqual rather than maps.Equal: a span attribute can legitimately
	// be a slice (a framework flattening a list), and maps.Equal panics on
	// incomparable values — which is how this helper first failed rather than the
	// package it tests.
	//
	// maps.Clone is a shallow copy, so this proves Normalize replaced no entry.
	// It would not catch mutation *inside* a slice value; Normalize only ever
	// reads values, and a type assertion cannot mutate one.
	if !reflect.DeepEqual(input, attrs) {
		t.Errorf("Normalize mutated its input map:\n got  %v\n want %v", input, attrs)
	}
}

// ---------------------------------------------------------------------
// The core vocabulary this package emits as strings
// ---------------------------------------------------------------------

// TestEmittedVocabularyMatchesTheCore is what makes the string decoupling safe.
//
// This package deliberately emits plain strings so its signature carries no
// opinion about core types. That is only safe if the strings are the same ones —
// a core rename would otherwise leave this table emitting a category
// event.Validate rejects, and the failure would surface as an invalid Event far
// from its cause.
func TestEmittedVocabularyMatchesTheCore(t *testing.T) {
	n := semconv.Normalize(semconv.Span{Attributes: map[string]any{
		semconv.AttrGenAIOperationName: "execute_tool",
		semconv.AttrGenAIToolName:      "export_customer",
		semconv.AttrGenAIAgentName:     "support-agent",
	}})
	if event.OperationCategory(n.OperationCategory) != event.OperationCategoryTool {
		t.Errorf("tool category = %q, want %q", n.OperationCategory, event.OperationCategoryTool)
	}
	if event.ActorType(n.ActorType) != event.ActorTypeAIAgent {
		t.Errorf("agent actor type = %q, want %q", n.ActorType, event.ActorTypeAIAgent)
	}

	model := semconv.Normalize(semconv.Span{Attributes: map[string]any{
		semconv.AttrGenAIOperationName: "chat",
		semconv.AttrGenAIRequestModel:  "gpt-4o",
	}})
	if event.OperationCategory(model.OperationCategory) != event.OperationCategoryExternal {
		t.Errorf("model category = %q, want %q", model.OperationCategory, event.OperationCategoryExternal)
	}
}

// ---------------------------------------------------------------------
// GenAI: one case per attribute
// ---------------------------------------------------------------------

func TestGenAIMapping(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
		want  want
	}{
		{
			name: "execute_tool names the tool",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "execute_tool",
				semconv.AttrGenAIToolName:      "export_customer",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "export_customer",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "invoke_agent names the agent, and establishes the actor",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "invoke_agent",
				semconv.AttrGenAIAgentName:     "researcher",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "researcher",
				ActorType:         "ai_agent",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "create_agent maps like invoke_agent",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "create_agent",
				semconv.AttrGenAIAgentName:     "researcher",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "researcher",
				ActorType:         "ai_agent",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "agent.id alone establishes the actor without naming the operation",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "invoke_agent",
				semconv.AttrGenAIAgentID:       "agent-7",
			},
			// No agent.name, so no operation identity — but the actor is
			// established, which is a different question.
			want: want{ActorType: "ai_agent"},
		},
		{
			name: "invoke_workflow falls back to the operation as its own name",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "invoke_workflow",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "invoke_workflow",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "plan prefers the agent name when it has one",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "plan",
				semconv.AttrGenAIAgentName:     "planner",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "planner",
				ActorType:         "ai_agent",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "chat names the model and targets the provider",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "chat",
				semconv.AttrGenAIRequestModel:  "gpt-4o",
				semconv.AttrGenAIProviderName:  "openai",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "gpt-4o",
				TargetName:        "openai",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "text_completion and generate_content map like chat",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "text_completion",
				semconv.AttrGenAIRequestModel:  "davinci",
				semconv.AttrGenAIProviderName:  "openai",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "davinci",
				TargetName:        "openai",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "embeddings maps like an inference call",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "embeddings",
				semconv.AttrGenAIRequestModel:  "text-embedding-3-small",
				semconv.AttrGenAIProviderName:  "openai",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "text-embedding-3-small",
				TargetName:        "openai",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "retrieval names and targets the data source",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "retrieval",
				semconv.AttrGenAIDataSourceID:  "policies-index",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "policies-index",
				TargetName:        "policies-index",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "retrieval with no source is still a retrieval",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "retrieval",
			},
			want: want{
				OperationCategory: "external",
				OperationName:     "retrieval",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
		{
			name: "conversation.id becomes the session, on any row",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName:  "execute_tool",
				semconv.AttrGenAIToolName:       "export_customer",
				semconv.AttrGenAIConversationID: "conv-42",
			},
			want: want{
				OperationCategory: "tool",
				OperationName:     "export_customer",
				SessionID:         "conv-42",
				Fidelity:          semconv.FidelitySemantic,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { run(t, tt.attrs, tt.want) })
	}
}

// TestGenAISystemIsReadOnlyAsALegacyAlias pins the one attribute that is not in
// the specification.
//
// gen_ai.system appears nowhere in the convention as of the commit genai.go
// records — not in the registry, and not in a deprecated registry, because that
// repository has none. It is read because producers pinned to an older convention
// still emit it, which is the version tolerance task 075 requires.
func TestGenAISystemIsReadOnlyAsALegacyAlias(t *testing.T) {
	run(t, map[string]any{
		semconv.AttrGenAIOperationName: "chat",
		semconv.AttrGenAIRequestModel:  "claude-3",
		semconv.AttrGenAISystemLegacy:  "anthropic",
	}, want{
		OperationCategory: "external",
		OperationName:     "claude-3",
		TargetName:        "anthropic",
		Fidelity:          semconv.FidelitySemantic,
	})

	t.Run("provider.name wins when both are present", func(t *testing.T) {
		run(t, map[string]any{
			semconv.AttrGenAIOperationName: "chat",
			semconv.AttrGenAIRequestModel:  "claude-3",
			semconv.AttrGenAIProviderName:  "anthropic",
			semconv.AttrGenAISystemLegacy:  "stale-value",
		}, want{
			OperationCategory: "external",
			OperationName:     "claude-3",
			TargetName:        "anthropic",
			Fidelity:          semconv.FidelitySemantic,
		})
	})
}

// TestGenAISystemInstructionsIsNotAProviderName is the confusion this table must
// not make.
//
// gen_ai.system_instructions is the system prompt — content, Opt-In, and on the
// deny-list. gen_ai.system was the provider. Reading the first as the second would
// put a prompt in Target.Name, which is a durable behavioral dimension.
func TestGenAISystemInstructionsIsNotAProviderName(t *testing.T) {
	run(t, map[string]any{
		semconv.AttrGenAIOperationName: "chat",
		semconv.AttrGenAIRequestModel:  "gpt-4o",
		"gen_ai.system_instructions":   "You are a helpful assistant. The passphrase is hunter2.",
	}, want{
		OperationCategory: "external",
		OperationName:     "gpt-4o",
		// TargetName deliberately empty: no provider was supplied.
		Fidelity: semconv.FidelitySemantic,
	})
}
