package trustvianprocessor_test

// Behavioral layer classification and counting semantics, end to end through the
// real processor and the real engine (task 083, ADR 0047).
//
// These tests assert the *documented* semantics, not an assumption that adding
// instrumentation must leave a count unchanged. It legitimately need not: a tool
// span carries evidence a transport span does not, and observing it is new
// information. What is pinned is what each observation becomes, and the one case
// where the current counting is misleading is pinned as a known gap rather than
// as correct.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/trustvian/trustvian/event"
)

// ---------------------------------------------------------------------
// Span builders. Nothing here names a demo workload: every tool and host is a
// parameter, so no fixture can quietly become a hardcoded special case.
// ---------------------------------------------------------------------

type spanSpec struct {
	name     string
	kind     ptrace.SpanKind
	spanID   [8]byte
	parentID [8]byte // zero means root
	attrs    map[string]string
	failed   bool
}

func tracesFrom(actor string, specs ...spanSpec) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", actor)
	rs.Resource().Attributes().PutStr("deployment.environment.name", "local")
	spans := rs.ScopeSpans().AppendEmpty().Spans()

	traceID := pcommon.TraceID([16]byte{7, 7, 7, 7, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12})
	for i, spec := range specs {
		s := spans.AppendEmpty()
		s.SetName(spec.name)
		s.SetKind(spec.kind)
		s.SetTraceID(traceID)
		s.SetSpanID(pcommon.SpanID(spec.spanID))
		if spec.parentID != [8]byte{} {
			s.SetParentSpanID(pcommon.SpanID(spec.parentID))
		}
		start := spanStart.Add(time.Duration(i) * time.Millisecond)
		s.SetStartTimestamp(pcommon.NewTimestampFromTime(start))
		s.SetEndTimestamp(pcommon.NewTimestampFromTime(start.Add(5 * time.Millisecond)))
		if spec.failed {
			s.Status().SetCode(ptrace.StatusCodeError)
		}
		for k, v := range spec.attrs {
			s.Attributes().PutStr(k, v)
		}
	}
	return td
}

func toolSpan(tool string, id, parent [8]byte) spanSpec {
	return spanSpec{
		name: tool, kind: ptrace.SpanKindInternal, spanID: id, parentID: parent,
		attrs: map[string]string{
			"gen_ai.operation.name": "execute_tool",
			"gen_ai.tool.name":      tool,
		},
	}
}

func httpSpan(host string, id, parent [8]byte) spanSpec {
	return spanSpec{
		name: "POST", kind: ptrace.SpanKindClient, spanID: id, parentID: parent,
		attrs: map[string]string{
			"http.request.method": "POST",
			"server.address":      host,
		},
	}
}

// observed is one record as it reached the control plane, with what rode beside it.
type observed struct {
	fingerprint string
	category    string
	name        string
	target      string
	layer       string
}

func ingest(t *testing.T, td ptrace.Traces) []observed {
	t.Helper()
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	records := cp.recorded()
	layers := cp.recordedLayers()
	if len(layers) != len(records) {
		t.Fatalf("%d records but %d layers", len(records), len(layers))
	}
	out := make([]observed, 0, len(records))
	for i, r := range records {
		out = append(out, observed{
			fingerprint: r.FingerprintID,
			category:    string(r.Behavior.OperationCategory),
			name:        r.Behavior.OperationName,
			target:      r.Behavior.TargetName,
			layer:       layers[i],
		})
	}
	return out
}

func distinctFingerprints(obs []observed) int {
	seen := map[string]bool{}
	for _, o := range obs {
		seen[o.fingerprint] = true
	}
	return len(seen)
}

// ---------------------------------------------------------------------
// The motivating case
// ---------------------------------------------------------------------

// TestOneActProducesTwoObservationsAndTwoIdentities is the documented current
// semantics, and the defect 083 exists to correct.
func TestOneActProducesTwoObservationsAndTwoIdentities(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
	))

	if len(obs) != 2 {
		t.Fatalf("one act produced %d observations, want 2", len(obs))
	}
	if got := distinctFingerprints(obs); got != 2 {
		t.Fatalf("one act produced %d behavioral identities, want 2 "+
			"(if this is now 1, the counting fold landed and this test must be rewritten)", got)
	}

	// The tool layer names the act; the transport layer names the destination.
	if obs[0].category != "tool" || obs[0].name != "export_customer" || obs[0].layer != "tool" {
		t.Errorf("tool observation = %+v, want tool/export_customer layer=tool", obs[0])
	}
	if obs[1].category != "http" || obs[1].target != "export.localhost" || obs[1].layer != "transport" {
		t.Errorf("transport observation = %+v, want http target=export.localhost layer=transport", obs[1])
	}

	// And the tool behavior carries no target, which is what a renderer must not
	// print a separator for.
	if obs[0].target != "" {
		t.Errorf("tool target = %q, want empty; the convention sets no target and "+
			"the wrapper span carries no server.address", obs[0].target)
	}
}

// TestCountingFoldIsNotImplementedYet records the gap in the suite and fails the
// moment it closes, so the deferral cannot be forgotten.
//
// Replace it with the positive assertion — one act, one counted change — when the
// counting policy lands on top of 084.
func TestCountingFoldIsNotImplementedYet(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
	))
	if distinctFingerprints(obs) == 1 {
		t.Error("one act now produces one behavioral identity: the counting fold has " +
			"landed. Update task 083's status, replace this test with the positive " +
			"assertion, and re-check 078's k-of-N guidance.")
	}
}

// TestParentIsUnreachableAtCountingTime is why the fold is deferred. The record
// the control plane counts from carries no parent identity, so the platform
// cannot know two records describe one act.
func TestParentIsUnreachableAtCountingTime(t *testing.T) {
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	td := tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
	)
	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	for _, r := range cp.recorded() {
		encoded, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(encoded), "parent") {
			t.Errorf("a DecisionRecord now carries a parent field; 084 may have landed "+
				"and the counting fold is unblocked:\n%s", encoded)
		}
	}
}

// TestMissingCorrelationFallsBackToTwoCountedChanges pins the documented
// fallback: a transport span whose parent was never observed is still counted,
// never suppressed and never attributed by guesswork.
func TestMissingCorrelationFallsBackToTwoCountedChanges(t *testing.T) {
	// The tool span is absent entirely — dropped, sampled out, or never emitted.
	orphan := ingest(t, tracesFrom("support-agent",
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
	))
	if len(orphan) != 1 {
		t.Fatalf("orphaned transport span produced %d observations, want 1", len(orphan))
	}
	if orphan[0].layer != "transport" {
		t.Errorf("layer = %q, want transport", orphan[0].layer)
	}

	// And with the tool present but no parent declared at all, both are still
	// observed and counted. Adjacency and name similarity prove nothing.
	unlinked := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{}),
	))
	if got := distinctFingerprints(unlinked); got != 2 {
		t.Errorf("unlinked spans produced %d identities, want 2; nothing may be "+
			"inferred from adjacency or similar names", got)
	}
}

// ---------------------------------------------------------------------
// Detection coverage
// ---------------------------------------------------------------------

// TestToolSwitchingDestinationChangesTheTransportIdentity is the detection
// property that makes keeping identity per observation the right decision.
func TestToolSwitchingDestinationChangesTheTransportIdentity(t *testing.T) {
	before := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
	))
	after := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("attacker.example", [8]byte{2}, [8]byte{1}),
	))

	if before[0].fingerprint != after[0].fingerprint {
		t.Error("the tool identity changed although the tool did not")
	}
	if before[1].fingerprint == after[1].fingerprint {
		t.Fatal("the same tool posting to a new destination produced the same " +
			"behavioral identity: the change is undetectable")
	}
	t.Logf("destination change is visible: %s -> %s", before[1].fingerprint, after[1].fingerprint)
}

// TestOneToolManyDestinationsStayDistinct requires several destinations beneath
// one tool to remain separately identifiable.
func TestOneToolManyDestinationsStayDistinct(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
		httpSpan("audit.localhost", [8]byte{3}, [8]byte{1}),
		httpSpan("mail.localhost", [8]byte{4}, [8]byte{1}),
	))
	if len(obs) != 4 {
		t.Fatalf("got %d observations, want 4", len(obs))
	}
	if got := distinctFingerprints(obs); got != 4 {
		t.Errorf("got %d distinct identities, want 4; destinations beneath one tool "+
			"must stay distinguishable", got)
	}
}

// TestUnrelatedTransportObservationSurvives requires an independent HTTP call to
// remain its own observation.
func TestUnrelatedTransportObservationSurvives(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
		// Not under the tool: a health check, a metrics push, anything.
		httpSpan("telemetry.localhost", [8]byte{5}, [8]byte{}),
	))
	if len(obs) != 3 {
		t.Fatalf("got %d observations, want 3; an unrelated request disappeared", len(obs))
	}
	var found bool
	for _, o := range obs {
		if o.target == "telemetry.localhost" {
			found = true
		}
	}
	if !found {
		t.Error("the unrelated transport observation is gone")
	}
}

// TestNestedToolCallsEachKeepTheirIdentity covers a tool invoked from inside
// another tool.
func TestNestedToolCallsEachKeepTheirIdentity(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("run_playbook", [8]byte{1}, [8]byte{}),
		toolSpan("export_customer", [8]byte{2}, [8]byte{1}),
		httpSpan("export.localhost", [8]byte{3}, [8]byte{2}),
	))
	if got := distinctFingerprints(obs); got != 3 {
		t.Errorf("got %d identities, want 3 for two nested tools and one request", got)
	}
	for _, o := range obs[:2] {
		if o.layer != "tool" {
			t.Errorf("nested tool observation %+v has layer %q, want tool", o, o.layer)
		}
	}
}

// TestConcurrentToolCallsAreNotConflated covers the same tool running twice
// concurrently against different destinations.
func TestConcurrentToolCallsAreNotConflated(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
		toolSpan("export_customer", [8]byte{2}, [8]byte{}),
		httpSpan("export-a.localhost", [8]byte{3}, [8]byte{1}),
		httpSpan("export-b.localhost", [8]byte{4}, [8]byte{2}),
	))
	if len(obs) != 4 {
		t.Fatalf("got %d observations, want 4", len(obs))
	}
	// Two invocations of one tool are one behavioral identity — that is what a
	// behavior *is* — while the two destinations stay separate.
	if obs[0].fingerprint != obs[1].fingerprint {
		t.Error("two invocations of one tool produced two identities; a behavior is " +
			"a kind of action, not an occurrence")
	}
	if obs[2].fingerprint == obs[3].fingerprint {
		t.Error("two destinations collapsed into one identity")
	}
}

// ---------------------------------------------------------------------
// Incomplete and failed observation
// ---------------------------------------------------------------------

// TestFailedToolCallStillClassifies requires a failed span to be classified, not
// dropped: a tool that errored is behavioral evidence.
func TestFailedToolCallStillClassifies(t *testing.T) {
	spec := toolSpan("export_customer", [8]byte{1}, [8]byte{})
	spec.failed = true
	obs := ingest(t, tracesFrom("support-agent", spec))
	if len(obs) != 1 {
		t.Fatalf("a failed tool span produced %d observations, want 1", len(obs))
	}
	if obs[0].layer != "tool" || obs[0].name != "export_customer" {
		t.Errorf("failed tool observation = %+v, want layer=tool name=export_customer", obs[0])
	}
}

// TestToolSpanWithoutIdentityAttributeIsTransport covers incomplete
// instrumentation: the convention declared itself and then named nothing.
func TestToolSpanWithoutIdentityAttributeIsTransport(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent", spanSpec{
		name: "POST", kind: ptrace.SpanKindClient, spanID: [8]byte{1},
		attrs: map[string]string{
			"gen_ai.operation.name": "execute_tool", // and no gen_ai.tool.name
			"http.request.method":   "POST",
			"server.address":        "export.localhost",
		},
	}))
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1", len(obs))
	}
	if obs[0].layer != "transport" {
		t.Errorf("layer = %q, want transport; a category with no name must not claim "+
			"a semantic layer", obs[0].layer)
	}
	if obs[0].category != "http" {
		t.Errorf("category = %q, want http; the span keeps its transport mapping", obs[0].category)
	}
}

// ---------------------------------------------------------------------
// Compatibility and the wire
// ---------------------------------------------------------------------

// TestTransportOnlyProducerIsByteIdentical is task 075's degradation guarantee,
// re-asserted for 083: a producer emitting no convention sees the same identities
// and is classified transport.
func TestTransportOnlyProducerIsByteIdentical(t *testing.T) {
	obs := ingest(t, tracesFrom("plain-service",
		httpSpan("crm.localhost", [8]byte{1}, [8]byte{}),
		httpSpan("mail.localhost", [8]byte{2}, [8]byte{}),
	))
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2", len(obs))
	}
	for _, o := range obs {
		if o.layer != "transport" {
			t.Errorf("observation %+v has layer %q, want transport", o, o.layer)
		}
		if o.category != "http" {
			t.Errorf("observation %+v has category %q, want http", o, o.category)
		}
	}
	if obs[0].fingerprint == obs[1].fingerprint {
		t.Error("two hosts collapsed into one identity")
	}
}

// TestIngestEnvelopeCarriesTheBehaviorLayer is the regression guard for the wire:
// the value the mapping established has to leave this module, because
// DecisionRecord cannot carry it.
func TestIngestEnvelopeCarriesTheBehaviorLayer(t *testing.T) {
	tests := []struct {
		name  string
		specs []spanSpec
		want  []string
	}{
		{"a named tool call", []spanSpec{toolSpan("export_customer", [8]byte{1}, [8]byte{})},
			[]string{string(event.LayerTool)}},
		{"a model call", []spanSpec{{
			name: "chat", kind: ptrace.SpanKindClient, spanID: [8]byte{1},
			attrs: map[string]string{
				"gen_ai.operation.name": "chat",
				"gen_ai.request.model":  "gemma3:4b",
				"gen_ai.provider.name":  "ollama",
			}}}, []string{string(event.LayerModel)}},
		{"a retrieval", []spanSpec{{
			name: "search", kind: ptrace.SpanKindClient, spanID: [8]byte{1},
			attrs: map[string]string{
				"gen_ai.operation.name": "retrieval",
				"gen_ai.data_source.id": "handbook",
			}}}, []string{string(event.LayerRetrieval)}},
		{"an outbound request", []spanSpec{httpSpan("crm.localhost", [8]byte{1}, [8]byte{})},
			[]string{string(event.LayerTransport)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obs := ingest(t, tracesFrom("support-agent", tt.specs...))
			if len(obs) != len(tt.want) {
				t.Fatalf("got %d observations, want %d", len(obs), len(tt.want))
			}
			for i := range obs {
				if obs[i].layer != tt.want[i] {
					t.Errorf("layer = %q, want %q", obs[i].layer, tt.want[i])
				}
			}
		})
	}
}

// TestLayerMatchesTheOutboundSpanAttribute pins the two reports of one mapping to
// each other, the way fidelity's paired test does.
func TestLayerMatchesTheOutboundSpanAttribute(t *testing.T) {
	cp := newIngestAPIServer(t)
	next := &consumertest.TracesSink{}
	proc, err := newTestProcessorWithConfig(t, next, cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(),
		tracesFrom("support-agent", toolSpan("export_customer", [8]byte{1}, [8]byte{}))); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	forwarded := next.AllTraces()
	if len(forwarded) != 1 {
		t.Fatalf("forwarded %d batches, want 1", len(forwarded))
	}
	attrs := forwarded[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	onSpan, ok := attrs.Get(event.AttrLayer)
	if !ok {
		t.Fatalf("the forwarded span carries no %s", event.AttrLayer)
	}
	sent := cp.recordedLayers()
	if len(sent) != 1 {
		t.Fatalf("the run holds %d records, want 1", len(sent))
	}
	if sent[0] != onSpan.Str() {
		t.Errorf("envelope layer = %q, span attribute = %q; one span reported two ways",
			sent[0], onSpan.Str())
	}
	if onSpan.Str() != string(event.LayerTool) {
		t.Errorf("layer = %q, want %q", onSpan.Str(), event.LayerTool)
	}
}
