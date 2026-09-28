package trustvianprocessor_test

import (
	"testing"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/trustvian/trustvian/event"
	trustvianprocessor "trustvian-processor"
)

// The semantic layer on the pipeline side.
//
// The point of these is that they assert the *same* outcomes as
// internal/otel/semantic_test.go in the core module, from a completely different
// span type. The table is shared; if these two ever disagreed, the shared table
// would not be shared in any useful sense.

func semanticSpan(t *testing.T, kind ptrace.SpanKind, name string, attrs map[string]any) event.Event {
	t.Helper()
	span := ptrace.NewSpan()
	span.SetKind(kind)
	span.SetName(name)
	// A real pipeline span always carries ids; without them the mapped Event
	// fails Validate for a reason that has nothing to do with the mapping.
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{1, 2, 3, 4, 5, 6, 7, 8}))
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(start.Add(5 * time.Millisecond)))
	if err := span.Attributes().FromRaw(attrs); err != nil {
		t.Fatalf("FromRaw: %v", err)
	}

	res := pcommon.NewMap()
	if err := res.FromRaw(map[string]any{
		"service.name":                "support-agent",
		"deployment.environment.name": "local",
	}); err != nil {
		t.Fatalf("resource FromRaw: %v", err)
	}

	return trustvianprocessor.EventFromSpan(res, span)
}

func TestGenAIToolSpanNamesTheTool(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindClient, "POST /export/customers", map[string]any{
		"gen_ai.operation.name":  "execute_tool",
		"gen_ai.tool.name":       "export_customer",
		"gen_ai.conversation.id": "conv-1",
		"http.request.method":    "POST",
		"server.address":         "export.localhost",
	})

	if e.Operation.Category != event.OperationCategoryTool {
		t.Errorf("Operation.Category = %q, want tool", e.Operation.Category)
	}
	if e.Operation.Name != "export_customer" {
		t.Errorf("Operation.Name = %q, want export_customer", e.Operation.Name)
	}
	if e.Target.Name != "export.localhost" {
		t.Errorf("Target.Name = %q, want export.localhost", e.Target.Name)
	}
	if e.Context.SessionID != "conv-1" {
		t.Errorf("Context.SessionID = %q, want conv-1", e.Context.SessionID)
	}
	if got := e.Attributes[event.AttrFidelity]; got != string(event.FidelitySemantic) {
		t.Errorf("fidelity = %v, want semantic", got)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("the mapped Event does not validate: %v", err)
	}
}

func TestOpenInferenceToolSpanNamesTheTool(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindClient, "POST /export", map[string]any{
		"openinference.span.kind": "TOOL",
		"tool.name":               "export_customer",
		"session.id":              "sess-2",
		"server.address":          "export.localhost",
	})
	if e.Operation.Category != event.OperationCategoryTool || e.Operation.Name != "export_customer" {
		t.Errorf("got %q · %q, want tool · export_customer", e.Operation.Category, e.Operation.Name)
	}
	if e.Context.SessionID != "sess-2" {
		t.Errorf("Context.SessionID = %q, want sess-2", e.Context.SessionID)
	}
}

// TestOpenInferenceAdvertisedToolIsNotUsed is the trap, on this side too.
//
// An LLM span listing the tools available to the model must not be recorded as
// having used one. Asserted in both adapters because the table is shared and a
// regression would show up in whichever one a developer's telemetry took.
func TestOpenInferenceAdvertisedToolIsNotUsed(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindClient, "chat", map[string]any{
		"openinference.span.kind": "LLM",
		"llm.model_name":          "gpt-4o",
		"llm.provider":            "openai",
		"llm.tools.0.tool.name":   "delete_account",
		"tool.name":               "delete_account",
	})
	if e.Operation.Name == "delete_account" {
		t.Fatal("an advertised tool definition became a used tool")
	}
	if e.Operation.Name != "gpt-4o" {
		t.Errorf("Operation.Name = %q, want gpt-4o", e.Operation.Name)
	}
}

// TestTransportOnlySpanIsUnchanged is degradation on the pipeline side.
func TestTransportOnlySpanIsUnchanged(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindServer, "POST /orders", map[string]any{
		"http.request.method": "POST",
		"server.address":      "checkout.internal",
	})

	if e.Operation.Category != event.OperationCategoryHTTP {
		t.Errorf("Operation.Category = %q, want http", e.Operation.Category)
	}
	if e.Operation.Name != "POST /orders" {
		t.Errorf("Operation.Name = %q, want the span name", e.Operation.Name)
	}
	if e.Target.Name != "checkout.internal" {
		t.Errorf("Target.Name = %q, want checkout.internal", e.Target.Name)
	}
	if e.Actor.Type != event.ActorTypeService {
		t.Errorf("Actor.Type = %q, want service", e.Actor.Type)
	}
	if e.Context.SessionID != "" {
		t.Errorf("Context.SessionID = %q, want empty", e.Context.SessionID)
	}
	if got := e.Attributes[event.AttrFidelity]; got != string(event.FidelityTransport) {
		t.Errorf("fidelity = %v, want transport", got)
	}
}

// TestOverridesOutrankConventions mirrors the core adapter's precedence test.
func TestOverridesOutrankConventions(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindClient, "POST /export", map[string]any{
		"trustvian.operation.category": "http",
		"trustvian.actor.type":         "service_account",
		"gen_ai.operation.name":        "execute_tool",
		"gen_ai.tool.name":             "export_customer",
		"gen_ai.agent.name":            "support-agent",
	})

	if e.Operation.Category != event.OperationCategoryHTTP {
		t.Errorf("Operation.Category = %q, want http", e.Operation.Category)
	}
	if e.Actor.Type != event.ActorTypeServiceAccount {
		t.Errorf("Actor.Type = %q, want service_account", e.Actor.Type)
	}
	// The name still comes from the convention: overriding one dimension does
	// not discard another.
	if e.Operation.Name != "export_customer" {
		t.Errorf("Operation.Name = %q, want export_customer", e.Operation.Name)
	}
}

// TestBothAdaptersAgree is the reason the table is shared.
//
// One logical behavior, described in both span models, must produce the same
// behavioral dimensions. Only the fields the mapping decides are compared — ids
// and timestamps are inputs.
func TestBothAdaptersAgree(t *testing.T) {
	e := semanticSpan(t, ptrace.SpanKindClient, "POST /export/customers", map[string]any{
		"gen_ai.operation.name":  "execute_tool",
		"gen_ai.tool.name":       "export_customer",
		"gen_ai.conversation.id": "conv-1",
		"server.address":         "export.localhost",
	})

	// The expected values are written out rather than computed, so this test
	// states the contract instead of restating the implementation. The core
	// module's internal/otel/semantic_test.go asserts the identical set from an
	// SDK span.
	if e.Operation.Category != event.OperationCategoryTool ||
		e.Operation.Name != "export_customer" ||
		e.Target.Name != "export.localhost" ||
		e.Context.SessionID != "conv-1" ||
		e.Attributes[event.AttrFidelity] != string(event.FidelitySemantic) {
		t.Errorf("pipeline mapping disagrees with the SDK adapter's documented result:\n"+
			"  category=%q name=%q target=%q session=%q fidelity=%v",
			e.Operation.Category, e.Operation.Name, e.Target.Name,
			e.Context.SessionID, e.Attributes[event.AttrFidelity])
	}
}
