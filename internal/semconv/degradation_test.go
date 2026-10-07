package semconv_test

import (
	"testing"

	"github.com/trustvian/trustvian/internal/semconv"
)

// TestAbsenceEstablishesNothing is the graceful-degradation requirement at this
// layer.
//
// A span carrying no agent-oriented convention must yield a *zero* Normalized, so
// that a caller's existing mapping is left byte-for-byte alone. The adapter-level
// half of this — that today's HTTP/DB/RPC mapping is genuinely unchanged — is
// asserted in slice 2 against internal/otel's own fixtures; this is the half that
// belongs here.
func TestAbsenceEstablishesNothing(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
	}{
		{"no attributes at all", nil},
		{"empty map", map[string]any{}},
		{
			name: "a plain HTTP span",
			attrs: map[string]any{
				"http.request.method": "POST",
				"server.address":      "export.localhost",
				"url.path":            "/export/customers",
			},
		},
		{
			name: "a plain DB span",
			attrs: map[string]any{
				"db.system.name": "postgresql",
				"db.namespace":   "orders",
			},
		},
		{
			name: "attributes that merely look agent-shaped",
			attrs: map[string]any{
				// Near-misses on purpose: none of these is a key either
				// convention defines.
				"gen_ai.operation":     "execute_tool",
				"genai.operation.name": "execute_tool",
				"span.kind":            "TOOL",
				"openinference.kind":   "TOOL",
				"toolname":             "export_customer",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := semconv.Normalize(semconv.Span{
				Kind:       semconv.KindClient,
				Name:       "POST",
				Attributes: tt.attrs,
			})
			if got != (semconv.Normalized{}) {
				t.Errorf("Normalize() = %+v, want the zero value: a span with no "+
					"convention must leave the caller's mapping untouched", got)
			}
			if got.Matched() {
				t.Error("Matched() is true for a span carrying no convention")
			}
		})
	}
}

// TestMalformedAttributesDegradeToTransport covers the renamed-or-broken case.
//
// Task 075: "A renamed or malformed attribute degrades to transport fidelity
// rather than erroring." Every case here would be a plausible bug in somebody
// else's instrumentation, and none of them may produce a semantic name or a
// panic.
func TestMalformedAttributesDegradeToTransport(t *testing.T) {
	tests := []struct {
		name  string
		attrs map[string]any
		want  want
	}{
		{
			name: "execute_tool with no tool name",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "execute_tool",
			},
			// The row does not fire. A category with no name would render as
			// "tool · POST" — a semantic category wearing a transport name.
			want: want{},
		},
		{
			name: "tool name is an integer",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "execute_tool",
				semconv.AttrGenAIToolName:      int64(42),
			},
			want: want{},
		},
		{
			name: "tool name is a list, as a flattening framework might emit",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "execute_tool",
				semconv.AttrGenAIToolName:      []string{"export_customer"},
			},
			want: want{},
		},
		{
			name: "tool name is an empty string",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: "execute_tool",
				semconv.AttrGenAIToolName:      "",
			},
			want: want{},
		},
		{
			name: "operation name is not a string",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName: 7,
				semconv.AttrGenAIToolName:      "export_customer",
			},
			// The discriminator itself is unreadable, so the convention is
			// absent — it does not fall through to a tool reading.
			want: want{},
		},
		{
			name: "span kind is not a string",
			attrs: map[string]any{
				semconv.AttrOISpanKind: true,
				semconv.AttrOIToolName: "export_customer",
			},
			want: want{},
		},
		{
			name: "a renamed upstream operation value",
			attrs: map[string]any{
				// Exactly what a future rename looks like from here.
				semconv.AttrGenAIOperationName: "tool_execution",
				semconv.AttrGenAIToolName:      "export_customer",
			},
			want: want{},
		},
		{
			name: "session id is not a string",
			attrs: map[string]any{
				semconv.AttrGenAIOperationName:  "execute_tool",
				semconv.AttrGenAIToolName:       "export_customer",
				semconv.AttrGenAIConversationID: 12345,
			},
			// The identity still maps; only the unreadable correlation is
			// dropped. One broken attribute must not cost the whole row.
			want: want{
				OperationCategory: "tool",
				OperationName:     "export_customer",
				Fidelity:          semconv.FidelitySemantic,
				Layer:             semconv.LayerTool,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run(t, tt.attrs, tt.want)
			got := semconv.Normalize(semconv.Span{Attributes: tt.attrs})
			if got.Fidelity == semconv.FidelitySemantic && !got.Matched() {
				t.Error("claimed semantic fidelity without an operation identity")
			}
		})
	}
}

// TestNoFabrication is the fixture that would be tempting to upgrade.
//
// A transport-only span that looks exactly like a tool call — it posts to a host
// named after a tool, with a path naming the tool, and a span name a reader would
// happily call semantic. None of that is a convention, so none of it may become
// a tool name. An operator seeing `export_customer` must be able to trust the
// agent's instrumentation actually said so.
func TestNoFabrication(t *testing.T) {
	got := semconv.Normalize(semconv.Span{
		Kind: semconv.KindClient,
		Name: "POST /export/customers export_customer",
		Attributes: map[string]any{
			"http.request.method": "POST",
			"server.address":      "export-customer.localhost",
			"url.path":            "/tools/export_customer",
			"url.full":            "http://export-customer.localhost/tools/export_customer",
			"peer.service":        "export_customer",
		},
	})
	if got != (semconv.Normalized{}) {
		t.Fatalf("Normalize fabricated semantics from a transport-only span: %+v", got)
	}
}

// TestFidelityNeverExceedsTheEvidence is the invariant behind the whole type.
//
// Asserted over every case in this package's tables by construction: semantic
// fidelity requires a matched operation identity, and a matched identity requires
// semantic fidelity. Neither can drift from the other without this failing.
func TestFidelityNeverExceedsTheEvidence(t *testing.T) {
	cases := []map[string]any{
		nil,
		{semconv.AttrGenAIOperationName: "execute_tool"},
		{semconv.AttrGenAIOperationName: "execute_tool", semconv.AttrGenAIToolName: "t"},
		{semconv.AttrGenAIOperationName: "chat"},
		{semconv.AttrGenAIOperationName: "chat", semconv.AttrGenAIRequestModel: "m"},
		{semconv.AttrOISpanKind: "TOOL"},
		{semconv.AttrOISpanKind: "TOOL", semconv.AttrOIToolName: "t"},
		{semconv.AttrOISpanKind: "CHAIN"},
		{semconv.AttrOISpanKind: "RETRIEVER"},
		{semconv.AttrGenAIOperationName: "unknown_future_operation"},
	}
	for i, attrs := range cases {
		got := semconv.Normalize(semconv.Span{Attributes: attrs})
		semantic := got.Fidelity == semconv.FidelitySemantic
		if semantic != got.Matched() {
			t.Errorf("case %d: fidelity=%q but Matched()=%v — the two must agree\n  %+v",
				i, got.Fidelity, got.Matched(), got)
		}
	}
}

// TestFidelityVocabularyIsClosed covers the wire-safety helpers.
func TestFidelityVocabularyIsClosed(t *testing.T) {
	if !semconv.FidelityTransport.Valid() || !semconv.FidelitySemantic.Valid() {
		t.Error("a member of the vocabulary reports itself invalid")
	}
	for _, bogus := range []semconv.Fidelity{"", "high", "SEMANTIC", "0.9", "partial"} {
		if bogus.Valid() {
			t.Errorf("%q reports itself valid", bogus)
		}
		// Unrecognized degrades toward claiming less, never more.
		if got := bogus.OrTransport(); got != semconv.FidelityTransport {
			t.Errorf("%q.OrTransport() = %q, want transport", bogus, got)
		}
	}
	if got := semconv.FidelitySemantic.OrTransport(); got != semconv.FidelitySemantic {
		t.Errorf("a valid value was downgraded: %q", got)
	}
}

// TestContentAttributesAreNeverRead is the deny-list, exercised.
//
// Each content key is fed as the *only* attribute on a span and must establish
// nothing — no category, no name, no target, no actor, no session. A key that
// accidentally became readable identity would show up here rather than in a
// privacy audit three layers down.
func TestContentAttributesAreNeverRead(t *testing.T) {
	const distinctive = "PROMPT-CANARY-do-not-propagate"
	for _, key := range semconv.ContentAttributes() {
		t.Run(key, func(t *testing.T) {
			got := semconv.Normalize(semconv.Span{
				Kind:       semconv.KindClient,
				Attributes: map[string]any{key: distinctive},
			})
			if got != (semconv.Normalized{}) {
				t.Errorf("content attribute %q established %+v", key, got)
			}
		})
	}
}

// TestContentAlongsideIdentityIsIgnored is the paired contract, at this layer.
//
// The identity attribute reaches Operation.Name; the content attribute reaches
// nothing. Slice 3 asserts the second half all the way down to the persisted row;
// this is the part the table itself is responsible for.
func TestContentAlongsideIdentityIsIgnored(t *testing.T) {
	attrs := map[string]any{
		semconv.AttrGenAIOperationName: "execute_tool",
		semconv.AttrGenAIToolName:      "export_customer",
	}
	for _, key := range semconv.ContentAttributes() {
		attrs[key] = "CANARY-" + key
	}

	got := semconv.Normalize(semconv.Span{Kind: semconv.KindClient, Attributes: attrs})
	expected := semconv.Normalized{
		OperationCategory: "tool",
		OperationName:     "export_customer",
		Fidelity:          semconv.FidelitySemantic,
		Layer:             semconv.LayerTool,
	}
	if got != expected {
		t.Fatalf("Normalize()\n got  %+v\n want %+v", got, expected)
	}
}

// TestNoContentKeyIsAlsoAnIdentityKey is a structural guard on the two lists.
//
// If a key ever appeared on both, one of the two would be wrong and the tests
// above could still pass — the deny-list case would fail, but only if someone
// remembered to keep the lists disjoint. This checks it directly.
func TestNoContentKeyIsAlsoAnIdentityKey(t *testing.T) {
	identity := map[string]bool{
		semconv.AttrGenAIOperationName:  true,
		semconv.AttrGenAIToolName:       true,
		semconv.AttrGenAIAgentName:      true,
		semconv.AttrGenAIAgentID:        true,
		semconv.AttrGenAIRequestModel:   true,
		semconv.AttrGenAIDataSourceID:   true,
		semconv.AttrGenAIProviderName:   true,
		semconv.AttrGenAISystemLegacy:   true,
		semconv.AttrGenAIConversationID: true,
		semconv.AttrOISpanKind:          true,
		semconv.AttrOIToolName:          true,
		semconv.AttrOIModelName:         true,
		semconv.AttrOIProvider:          true,
		semconv.AttrOISystem:            true,
		semconv.AttrOISessionID:         true,
		semconv.AttrOIAgentName:         true,
		semconv.AttrOIRerankerName:      true,
	}
	for _, key := range semconv.ContentAttributes() {
		if identity[key] {
			t.Errorf("%q is on both the identity and the content list", key)
		}
	}
}

// TestUsageAndStatusChangeNoNormalization is the degradation contract for task
// 087's keys: adding token usage and a status code to any span — transport,
// GenAI or OpenInference — normalizes exactly as the span without them did.
func TestUsageAndStatusChangeNoNormalization(t *testing.T) {
	spans := []semconv.Span{
		{Kind: semconv.KindClient, Attributes: map[string]any{
			"http.request.method": "POST", "server.address": "export.localhost"}},
		{Kind: semconv.KindClient, Attributes: map[string]any{
			semconv.AttrGenAIOperationName: "chat", "gen_ai.request.model": "llama3.2", "gen_ai.provider.name": "ollama"}},
		{Kind: semconv.KindInternal, Attributes: map[string]any{
			semconv.AttrGenAIOperationName: "execute_tool", semconv.AttrGenAIToolName: "export_customer"}},
		{Kind: semconv.KindInternal, Attributes: map[string]any{
			"openinference.span.kind": "LLM", "llm.model_name": "llama3.2"}},
	}
	for i, span := range spans {
		without := semconv.Normalize(span)
		with := map[string]any{}
		for k, v := range span.Attributes {
			with[k] = v
		}
		for _, key := range semconv.UsageAttributes() {
			with[key] = int64(77)
		}
		if got := semconv.Normalize(semconv.Span{Kind: span.Kind, Attributes: with}); got != without {
			t.Errorf("span %d: usage and status changed the normalization:\n got %+v\nwant %+v", i, got, without)
		}
	}
}
