package otel_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	trustvianotel "github.com/trustvian/trustvian/internal/otel"
)

// Privacy, at the layers that enforce it.
//
// Task 075's privacy section is precise about where the boundary binds, and this
// file is written against that precision rather than against the naive version:
//
//	Event.Attributes        MAY carry producer attributes, as it does today.
//	                       This is documented behavior ("nothing is silently
//	                       dropped") that task 075 does not change, so these
//	                       tests deliberately assert content IS here.
//	StableFeatures         MUST NOT carry it — six approved dimensions only
//	the fingerprint         MUST NOT depend on it
//	DecisionRecord          MUST NOT carry it — fixed shape, no attribute map
//
// The platform layers — realtime, persistence, /v1, WebUI — are asserted in the
// platform module, where those types live.
//
// Every content attribute gets its own distinctive value, so a failure names
// which one leaked rather than reporting that something did.

// contentCanary returns a value unique to one attribute key.
//
// Keyed rather than a single shared sentinel, because "a prompt leaked" is a much
// weaker failure message than "gen_ai.tool.call.arguments leaked", and the second
// tells you which mapping branch to look at.
func contentCanary(key string) string {
	return "CANARY-" + strings.NewReplacer(".", "-", "_", "-").Replace(key) + "-8fa3"
}

// spanWithAllContent builds one span carrying every content attribute both
// conventions define, plus a legitimate semantic identity.
//
// One span rather than one per attribute: the interesting case is the realistic
// one, where a producer emits identity and content together and the mapping has
// to take exactly one of them.
func spanWithAllContent(t *testing.T) (event.Event, map[string]string) {
	t.Helper()

	canaries := make(map[string]string)
	attrs := []attribute.KeyValue{
		// Identity, which must survive.
		attribute.String("gen_ai.operation.name", "execute_tool"),
		attribute.String("gen_ai.tool.name", "export_customer"),
		attribute.String("gen_ai.agent.name", "support-agent"),
		attribute.String("gen_ai.conversation.id", "conv-1"),
		// Transport, which must survive.
		semconv.HTTPRequestMethodKey.String("POST"),
		semconv.ServerAddressKey.String("export.localhost"),
	}
	for _, key := range event.ContentAttributes() {
		value := contentCanary(key)
		canaries[key] = value
		attrs = append(attrs, attribute.String(key, value))
	}

	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	span := recordSpan(t, testResource(t, "support-agent", "local"),
		trace.SpanKindClient, "POST /export/customers", attrs, false,
		start, start.Add(7*time.Millisecond))

	return trustvianotel.EventFromSpan(span), canaries
}

// TestContentReachesNoEnforcingLayerInTheCore is the whole-pipeline assertion.
func TestContentReachesNoEnforcingLayerInTheCore(t *testing.T) {
	e, canaries := spanWithAllContent(t)
	if len(canaries) == 0 {
		t.Fatal("no content attributes to test — the deny-list is empty")
	}

	engine := trustvian.NewEngine()
	result, err := engine.Analyze(context.Background(), e)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	record := result.DecisionRecord()

	// Serialized, because JSON is what actually travels. A field that held a
	// canary but was excluded from the wire would still be a leak waiting for
	// somebody to add a tag.
	recordJSON, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal DecisionRecord: %v", err)
	}
	featuresText := fmt.Sprintf("%+v", result.Features.Stable)

	layers := []struct {
		name string
		text string
	}{
		{"StableFeatures", featuresText},
		{"the fingerprint id", result.Fingerprint.ID},
		{"DecisionRecord (JSON)", string(recordJSON)},
		{"DecisionRecord.Behavior", fmt.Sprintf("%+v", record.Behavior)},
	}

	for key, canary := range canaries {
		for _, layer := range layers {
			if strings.Contains(layer.text, canary) {
				t.Errorf("content attribute %q reached %s\n  value: %s",
					key, layer.name, canary)
			}
		}
	}
}

// TestContentIsPresentInEventAttributes is the half task 075 insists on NOT
// asserting the other way.
//
// The adapter preserves every span attribute — its own package comment says
// "nothing is silently dropped" — and a consumer may rely on that. Asserting
// absence here would encode a change this task is explicitly not making, and
// would quietly turn a future adapter-sanitization proposal into something that
// already happened.
//
// So this test exists to pin the documented behavior, and to make the boundary
// visible: content is in the transient map and nowhere below it.
func TestContentIsPresentInEventAttributes(t *testing.T) {
	e, canaries := spanWithAllContent(t)
	for key, canary := range canaries {
		got, ok := e.Attributes[key]
		if !ok {
			t.Errorf("attribute %q was dropped from Event.Attributes; the adapter's "+
				"documented behavior is that nothing is silently dropped", key)
			continue
		}
		if got != canary {
			t.Errorf("attribute %q = %v, want %q", key, got, canary)
		}
	}
}

// TestIdentityInfluencesAndContentDoesNot is the paired contract task 075 asks
// for as one test, because the pair is what makes it meaningful.
//
// Half one: the tool name reached Operation.Name, so the semantic attribute
// genuinely influenced behavioral identity. Half two: no content value reached
// any enforcing layer. Either half alone is satisfiable by a mapping that reads
// nothing at all, or by one that reads everything.
func TestIdentityInfluencesAndContentDoesNot(t *testing.T) {
	e, canaries := spanWithAllContent(t)

	// Half one.
	if e.Operation.Category != event.OperationCategoryTool {
		t.Errorf("Operation.Category = %q, want tool", e.Operation.Category)
	}
	if e.Operation.Name != "export_customer" {
		t.Fatalf("Operation.Name = %q, want export_customer — the semantic attribute "+
			"did not reach behavioral identity, so half two proves nothing", e.Operation.Name)
	}

	engine := trustvian.NewEngine()
	result, err := engine.Analyze(context.Background(), e)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// The behavior that travels names the tool.
	if result.Features.Stable.OperationName != "export_customer" {
		t.Errorf("StableFeatures.OperationName = %q, want export_customer",
			result.Features.Stable.OperationName)
	}

	// Half two, on the serialized record.
	recordJSON, err := json.Marshal(result.DecisionRecord())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for key, canary := range canaries {
		if strings.Contains(string(recordJSON), canary) {
			t.Errorf("content attribute %q reached the DecisionRecord", key)
		}
	}
}

// TestContentDoesNotChangeTheFingerprint is the strongest form of "arbitrary
// attributes are not identity".
//
// Two spans identical except for their content attributes must share a
// fingerprint. If they did not, an agent would appear to invent a new behavior
// every time a prompt changed — which is every request — and its baseline would
// never mature.
func TestContentDoesNotChangeTheFingerprint(t *testing.T) {
	build := func(suffix string) event.Event {
		attrs := []attribute.KeyValue{
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", "export_customer"),
			semconv.ServerAddressKey.String("export.localhost"),
		}
		for _, key := range event.ContentAttributes() {
			attrs = append(attrs, attribute.String(key, "value-"+suffix))
		}
		start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		span := recordSpan(t, testResource(t, "support-agent", "local"),
			trace.SpanKindClient, "POST /export/customers", attrs, false,
			start, start.Add(time.Millisecond))
		return trustvianotel.EventFromSpan(span)
	}

	engine := trustvian.NewEngine()
	first, err := engine.Analyze(context.Background(), build("alpha"))
	if err != nil {
		t.Fatalf("Analyze first: %v", err)
	}
	second, err := engine.Analyze(context.Background(), build("omega"))
	if err != nil {
		t.Fatalf("Analyze second: %v", err)
	}

	if first.Fingerprint.ID != second.Fingerprint.ID {
		t.Errorf("two spans differing only in content produced different fingerprints:\n"+
			"  %s\n  %s\nAn agent would invent a new behavior on every request.",
			first.Fingerprint.ID, second.Fingerprint.ID)
	}
	if first.Features.Stable != second.Features.Stable {
		t.Errorf("StableFeatures differ on content alone:\n  %+v\n  %+v",
			first.Features.Stable, second.Features.Stable)
	}
}

// TestFidelityIsNotBehavioralIdentity protects the baseline from an
// instrumentation upgrade.
//
// The same logical behavior at two fidelities is still the same behavior. If
// fidelity entered the fingerprint, the day a team upgraded its instrumentation
// every behavior would look new and every baseline would reset — reported as a
// wave of novel behavior rather than as the configuration change it was.
//
// The operation *name* legitimately differs between the two, so this compares the
// dimensions that must not move: actor type, category, target and environment.
func TestFidelityIsNotBehavioralIdentity(t *testing.T) {
	start := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	res := testResource(t, "support-agent", "local")

	transport := trustvianotel.EventFromSpan(recordSpan(t, res, trace.SpanKindClient,
		"POST /export/customers", []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String("POST"),
			semconv.ServerAddressKey.String("export.localhost"),
			attribute.String(trustvianotel.AttrOperationCategory, "tool"),
		}, false, start, start.Add(time.Millisecond)))

	semantic := trustvianotel.EventFromSpan(recordSpan(t, res, trace.SpanKindClient,
		"POST /export/customers", []attribute.KeyValue{
			semconv.HTTPRequestMethodKey.String("POST"),
			semconv.ServerAddressKey.String("export.localhost"),
			attribute.String("gen_ai.operation.name", "execute_tool"),
			attribute.String("gen_ai.tool.name", "export_customer"),
		}, false, start, start.Add(time.Millisecond)))

	if transport.Attributes[event.AttrFidelity] == semantic.Attributes[event.AttrFidelity] {
		t.Fatal("test setup: both spans reported the same fidelity")
	}

	engine := trustvian.NewEngine()
	a, err := engine.Analyze(context.Background(), transport)
	if err != nil {
		t.Fatalf("Analyze transport: %v", err)
	}
	b, err := engine.Analyze(context.Background(), semantic)
	if err != nil {
		t.Fatalf("Analyze semantic: %v", err)
	}

	if a.Features.Stable.ActorType != b.Features.Stable.ActorType ||
		a.Features.Stable.OperationCategory != b.Features.Stable.OperationCategory ||
		a.Features.Stable.TargetName != b.Features.Stable.TargetName ||
		a.Features.Stable.Environment != b.Features.Stable.Environment {
		t.Errorf("fidelity changed a behavioral dimension other than the name:\n  %+v\n  %+v",
			a.Features.Stable, b.Features.Stable)
	}

	// And the fidelity value itself is not a StableFeatures field at all, which
	// a struct comparison cannot show — so this asserts it by exhaustion.
	if strings.Contains(fmt.Sprintf("%+v", b.Features.Stable), string(event.FidelitySemantic)) {
		t.Error("the fidelity value appears inside StableFeatures")
	}
}
