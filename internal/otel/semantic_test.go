package otel_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	trustvianotel "github.com/trustvian/trustvian/internal/otel"
)

// The semantic layer, through the real SDK adapter.
//
// internal/semconv's own tests cover the table exhaustively against plain maps.
// These cover the wiring: that the table's result actually reaches the Event's
// fields, that the transport fallbacks defer to it correctly, and that the
// precedence between an override, a convention and the transport mapping is the
// documented one.

func semanticSpan(t *testing.T, kind trace.SpanKind, name string, attrs []attribute.KeyValue) event.Event {
	t.Helper()
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	span := recordSpan(t, testResource(t, "support-agent", "local"),
		kind, name, attrs, false, start, start.Add(5*time.Millisecond))
	return trustvianotel.EventFromSpan(span)
}

func TestGenAIToolSpanNamesTheTool(t *testing.T) {
	e := semanticSpan(t, trace.SpanKindClient, "POST /export/customers", []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", "export_customer"),
		attribute.String("gen_ai.conversation.id", "conv-1"),
		// The transport attributes the same span carries.
		semconv.HTTPRequestMethodKey.String("POST"),
		semconv.ServerAddressKey.String("export.localhost"),
	})

	if e.Operation.Category != event.OperationCategoryTool {
		t.Errorf("Operation.Category = %q, want tool", e.Operation.Category)
	}
	if e.Operation.Name != "export_customer" {
		t.Errorf("Operation.Name = %q, want export_customer — the span name is transport", e.Operation.Name)
	}
	// The richest row this task can produce: the tool names the operation and
	// the transport still names the target.
	if e.Target.Name != "export.localhost" {
		t.Errorf("Target.Name = %q, want export.localhost — the transport target is kept", e.Target.Name)
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

func TestGenAIToolSpanWithNoTransportTargetLeavesItEmpty(t *testing.T) {
	// The §4 decision: an empty target is stable by construction, and inventing
	// one — the tool name, the agent, the service — would either be a tautology
	// or would put the actor in the target dimension.
	e := semanticSpan(t, trace.SpanKindInternal, "tool", []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", "calculator"),
	})

	if e.Operation.Name != "calculator" {
		t.Errorf("Operation.Name = %q, want calculator", e.Operation.Name)
	}
	if e.Target.Name != "" {
		t.Errorf("Target.Name = %q, want empty: the telemetry named no target", e.Target.Name)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("an Event with an empty target must still validate: %v", err)
	}
}

func TestGenAIModelSpanTargetsTheProvider(t *testing.T) {
	e := semanticSpan(t, trace.SpanKindClient, "POST /api/chat", []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "chat"),
		attribute.String("gen_ai.request.model", "gpt-4o"),
		attribute.String("gen_ai.provider.name", "openai"),
		semconv.HTTPRequestMethodKey.String("POST"),
		semconv.ServerAddressKey.String("api.openai.example"),
	})

	if e.Operation.Category != event.OperationCategoryExternal {
		t.Errorf("Operation.Category = %q, want external", e.Operation.Category)
	}
	if e.Operation.Name != "gpt-4o" {
		t.Errorf("Operation.Name = %q, want gpt-4o", e.Operation.Name)
	}
	// The convention's target wins over the transport's here, because the
	// provider is the more meaningful destination for a model call.
	if e.Target.Name != "openai" {
		t.Errorf("Target.Name = %q, want openai", e.Target.Name)
	}
}

func TestOpenInferenceToolSpanNamesTheTool(t *testing.T) {
	e := semanticSpan(t, trace.SpanKindClient, "POST /export", []attribute.KeyValue{
		attribute.String("openinference.span.kind", "TOOL"),
		attribute.String("tool.name", "export_customer"),
		attribute.String("session.id", "sess-2"),
		semconv.ServerAddressKey.String("export.localhost"),
	})

	if e.Operation.Category != event.OperationCategoryTool || e.Operation.Name != "export_customer" {
		t.Errorf("got %q · %q, want tool · export_customer", e.Operation.Category, e.Operation.Name)
	}
	if e.Context.SessionID != "sess-2" {
		t.Errorf("Context.SessionID = %q, want sess-2", e.Context.SessionID)
	}
}

// TestActorTypeUpgradeNeedsAnAgentIdentity is the §5 decision, at the adapter.
//
// ActorType is one of the six StableFeatures dimensions, so an upgrade changes
// every fingerprint for that actor and discards its learned baseline. A rule that
// fired on a client library's presence would reset baselines the first time a
// service adopted one.
func TestActorTypeUpgradeNeedsAnAgentIdentity(t *testing.T) {
	t.Run("a plain service calling a model stays a service", func(t *testing.T) {
		e := semanticSpan(t, trace.SpanKindClient, "chat", []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "chat"),
			attribute.String("gen_ai.request.model", "gpt-4o"),
			attribute.String("gen_ai.provider.name", "openai"),
		})
		if e.Actor.Type != event.ActorTypeService {
			t.Errorf("Actor.Type = %q, want service: a GenAI operation is not an agent identity", e.Actor.Type)
		}
	})

	t.Run("an agent name establishes the actor", func(t *testing.T) {
		e := semanticSpan(t, trace.SpanKindClient, "invoke", []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", "support-agent"),
		})
		if e.Actor.Type != event.ActorTypeAIAgent {
			t.Errorf("Actor.Type = %q, want ai_agent", e.Actor.Type)
		}
	})
}

// TestOverridesOutrankConventions is the §7 precedence, asserted in both
// directions.
func TestOverridesOutrankConventions(t *testing.T) {
	t.Run("operation.category override wins over a convention", func(t *testing.T) {
		e := semanticSpan(t, trace.SpanKindClient, "POST /export", []attribute.KeyValue{
			attribute.String(trustvianotel.AttrOperationCategory, "http"),
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", "export_customer"),
		})
		if e.Operation.Category != event.OperationCategoryHTTP {
			t.Errorf("Operation.Category = %q, want http: an explicit override outranks a convention", e.Operation.Category)
		}
		// The name still comes from the convention: the override is about the
		// category, and overriding one dimension does not discard another.
		if e.Operation.Name != "export_customer" {
			t.Errorf("Operation.Name = %q, want export_customer", e.Operation.Name)
		}
		// And fidelity still reflects what the table read, not whether an
		// override fired — it describes the *name*'s provenance.
		if got := e.Attributes[event.AttrFidelity]; got != string(event.FidelitySemantic) {
			t.Errorf("fidelity = %v, want semantic", got)
		}
	})

	t.Run("actor.type override wins over an agent identity", func(t *testing.T) {
		e := semanticSpan(t, trace.SpanKindClient, "invoke", []attribute.KeyValue{
			attribute.String(trustvianotel.AttrActorType, "service_account"),
			attribute.String("gen_ai.operation.name", "invoke_agent"),
			attribute.String("gen_ai.agent.name", "support-agent"),
		})
		if e.Actor.Type != event.ActorTypeServiceAccount {
			t.Errorf("Actor.Type = %q, want service_account", e.Actor.Type)
		}
	})
}

// TestFidelityReachesTheOutboundAttributes closes the inbound-to-outbound loop.
func TestFidelityReachesTheOutboundAttributes(t *testing.T) {
	e := semanticSpan(t, trace.SpanKindClient, "POST /export", []attribute.KeyValue{
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", "export_customer"),
	})

	engine := trustvian.NewEngine()
	result, err := engine.Analyze(context.Background(), e)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	attrs := trustvianotel.AttributesFromResult(result)
	v, ok := findAttr(attrs, trustvianotel.AttrFidelity)
	if !ok {
		t.Fatal("trustvian.fidelity missing from the outbound attributes")
	}
	if v.AsString() != string(event.FidelitySemantic) {
		t.Errorf("outbound fidelity = %q, want semantic", v.AsString())
	}
}
