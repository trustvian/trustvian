// Command agent-producer emits agent-oriented OpenTelemetry spans, so task 075's
// normalization can be proven end to end against a real OTLP pipeline.
//
// It exists because every other test of the semantic mapping works on a span this
// repository built: internal/semconv's tests use plain maps, and both adapters'
// tests construct spans directly. None of them proves that a *producer* using the
// OpenTelemetry SDK, exporting over OTLP, through a real Collector, yields
// behaviors named by tool. That is the one claim task 075's acceptance criteria
// are actually about, and it is the one thing those tests structurally cannot show.
//
// Two modes, because the task has two halves and they are equally load-bearing:
//
//	MODE=semantic    GenAI execute_tool spans -> behaviors named by tool
//	MODE=transport   plain HTTP client spans  -> exactly today's behaviors
//
// The transport mode is not a control for completeness. Acceptance criterion 2 is
// "a producer emitting none sees **no change whatsoever**", and the only honest
// way to check that is to emit the old shape through the new code and compare.
//
// Content is emitted on purpose in semantic mode. A prompt, a completion and tool
// arguments ride on the spans exactly as a real agent framework would send them,
// because the privacy claim is worth nothing if it is only ever tested against
// telemetry that carries nothing to leak.
//
// Configuration, matching demo-producer's conventions so both are driven the same
// way:
//
//	OTLP_ENDPOINT   collector address (default localhost:4317)
//	MODE            "semantic" or "transport" (default semantic)
//	SPAN_COUNT      spans to emit (default 6)
//	SERVICE_NAME    resource service.name, i.e. the Trustvian actor
//	ENVIRONMENT     resource deployment.environment.name
//
// It is a test fixture, not a demo. demo-producer stays the one a reader runs to
// see the pipeline work.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// The tools the simulated agent uses, in order. Fixed, so the behaviors a test
// asserts are the behaviors the producer emitted.
var tools = []string{"crm_lookup", "knowledge_search", "export_customer"}

// Content the producer deliberately emits, so the privacy assertions have
// something to find. Distinctive enough that its appearance anywhere is proof.
const (
	promptCanary     = "CANARY-PROMPT-8fa3-do-not-propagate"
	completionCanary = "CANARY-COMPLETION-8fa3-do-not-propagate"
	argumentCanary   = "CANARY-TOOL-ARGUMENT-8fa3-do-not-propagate"
)

// emitSemantic sends one GenAI execute_tool span.
//
// The attribute names are the convention's, verified in internal/semconv against
// open-telemetry/semantic-conventions-genai at e57c543b4889. The span *name* is
// deliberately transport-shaped — "POST /v1/tools" — so a test asserting the
// behavior is named `export_customer` is proving the convention was read rather
// than that the span name happened to be useful.
func emitSemantic(ctx context.Context, tracer trace.Tracer, tool string) {
	_, span := tracer.Start(ctx, "POST /v1/tools", trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", tool),
		attribute.String("gen_ai.agent.name", "support-agent"),
		attribute.String("gen_ai.conversation.id", "conv-e2e-1"),

		// Transport, on the same span, as a real instrumented HTTP tool call has.
		// This is what lets the behavior read `tool · export_customer →
		// tools.localhost` rather than losing its target.
		semconv.HTTPRequestMethodKey.String("POST"),
		semconv.ServerAddressKey.String("tools.localhost"),

		// Content. Emitted so the privacy claim is tested against telemetry that
		// actually carries something worth protecting.
		attribute.String("gen_ai.input.messages", promptCanary),
		attribute.String("gen_ai.output.messages", completionCanary),
		attribute.String("gen_ai.tool.call.arguments", argumentCanary),
	)
	span.End()
}

// emitTransport sends one plain HTTP client span, carrying no convention at all.
//
// This is the shape every workload emitted before task 075, and the shape
// acceptance criterion 2 says must map identically afterwards.
func emitTransport(ctx context.Context, tracer trace.Tracer, tool string) {
	_, span := tracer.Start(ctx, "POST /v1/tools", trace.WithSpanKind(trace.SpanKindClient))
	span.SetAttributes(
		semconv.HTTPRequestMethodKey.String("POST"),
		semconv.ServerAddressKey.String("tools.localhost"),
		semconv.URLPathKey.String("/v1/tools/"+tool),
	)
	span.End()
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("agent-producer: %v", err)
	}
}

func run() error {
	endpoint := env("OTLP_ENDPOINT", "localhost:4317")
	serviceName := env("SERVICE_NAME", "support-agent")
	environment := env("ENVIRONMENT", "local")

	mode := env("MODE", "semantic")
	if mode != "semantic" && mode != "transport" {
		return fmt.Errorf("MODE must be semantic or transport, got %q", mode)
	}

	count, err := strconv.Atoi(env("SPAN_COUNT", "6"))
	if err != nil || count < 1 {
		return fmt.Errorf("SPAN_COUNT must be a positive integer, got %q", env("SPAN_COUNT", ""))
	}

	ctx := context.Background()

	// WithInsecure: loopback only, to a Collector this test started.
	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return fmt.Errorf("connect to collector at %s: %w", endpoint, err)
	}

	// Empty base rather than resource.Default(), for demo-producer's reason: the
	// default detector adds host and process attributes that differ between runs,
	// which makes the telemetry non-reproducible.
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
		semconv.DeploymentEnvironmentNameKey.String(environment),
	))
	if err != nil {
		return fmt.Errorf("build resource: %w", err)
	}

	// WithSyncer, not batching: it makes the span count this producer reports the
	// span count the Collector received, with no flush timing to reason about.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(exporter),
	)
	tracer := tp.Tracer("trustvian-agent-producer")

	for i := range count {
		tool := tools[i%len(tools)]
		if mode == "semantic" {
			emitSemantic(ctx, tracer, tool)
		} else {
			emitTransport(ctx, tracer, tool)
		}
	}

	// Shutdown flushes, and its error is returned rather than logged: a producer
	// that exits 0 without delivering its spans makes every downstream assertion
	// meaningless.
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := tp.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("flush spans to %s: %w", endpoint, err)
	}

	log.Printf("agent-producer: sent %d %s spans to %s as actor %q in environment %q",
		count, mode, endpoint, serviceName, environment)
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
