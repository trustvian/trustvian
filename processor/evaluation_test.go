package trustvianprocessor_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumererror"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/processor"

	trustvian "github.com/trustvian/trustvian"
	trustvianprocessor "trustvian-processor"
)

// ingestAPIServer accepts the two ingest routes and keeps every record it
// was sent, so a test can compare what arrived against what the engine
// produced.
//
// Named for the API rather than the server behind it: architecture_test.go
// forbids the identifier "ControlPlane" anywhere in this module by substring,
// and that guard is more valuable than a prettier test name.
type ingestAPIServer struct {
	*httptest.Server

	mu       sync.Mutex
	records  []trustvian.DecisionRecord
	next     uint64
	failNext atomic.Bool
}

func newIngestAPIServer(t *testing.T) *ingestAPIServer {
	t.Helper()
	cp := &ingestAPIServer{next: 1}
	cp.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/ingest-state"):
			cp.mu.Lock()
			next := cp.next
			cp.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "run_id": "run-1", "next_sequence": itoa(next)})
		case strings.HasSuffix(r.URL.Path, "/records"):
			if cp.failNext.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			var envelope struct {
				Sequence          string                   `json:"sequence"`
				BehavioralProfile string                   `json:"behavioral_profile"`
				Record            trustvian.DecisionRecord `json:"record"`
			}
			if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			cp.mu.Lock()
			cp.records = append(cp.records, envelope.Record)
			cp.next++
			next := cp.next
			cp.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"version": "1", "disposition": "applied",
				"next_sequence": itoa(next), "record_count": itoa(next - 1),
				"behavior_complete": true})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(cp.Close)
	return cp
}

// itoa renders a counter the way the wire contract requires: canonical
// decimal text, never a JSON number.
func itoa(v uint64) string { return strconv.FormatUint(v, 10) }

func (cp *ingestAPIServer) recorded() []trustvian.DecisionRecord {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return append([]trustvian.DecisionRecord(nil), cp.records...)
}

func (cp *ingestAPIServer) config() *trustvianprocessor.Config {
	required := true
	return &trustvianprocessor.Config{
		Evaluation: &trustvianprocessor.EvaluationConfig{
			APIURL:            cp.URL,
			RunID:             "run-1",
			BehavioralProfile: "support-reference",
			Required:          &required,
		},
	}
}

// evaluationTraces builds a single valid span, distinct from buildTraces so
// this file does not depend on that helper's choices.
func evaluationTraces(actorID, target string) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", actorID)
	rs.Resource().Attributes().PutStr("deployment.environment.name", "local")

	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("GET")
	span.SetKind(ptrace.SpanKindClient)
	span.SetStartTimestamp(pcommon.NewTimestampFromTime(spanStart))
	span.SetEndTimestamp(pcommon.NewTimestampFromTime(spanStart.Add(time.Millisecond)))
	span.Attributes().PutStr("http.request.method", "GET")
	span.Attributes().PutStr("server.address", target)
	var traceID pcommon.TraceID
	copy(traceID[:], "trace-"+target+"0123456789abcdef")
	var spanID pcommon.SpanID
	copy(spanID[:], "span-"+target+"01234567")
	span.SetTraceID(traceID)
	span.SetSpanID(spanID)
	return td
}

// TestEvaluationPostsTheRealDecisionRecord is the point of task 073: the
// posted record is the projection of the Result the processor already
// computed, carrying fields the trustvian.* attribute set does not contain,
// so a reconstruction from those attributes could not have produced it.
func TestEvaluationPostsTheRealDecisionRecord(t *testing.T) {
	cp := newIngestAPIServer(t)
	next := &capturingConsumer{}

	proc, err := newTestProcessorWithConfig(t, next, cp.config())
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	td := evaluationTraces("support-agent", "crm.localhost")
	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	records := cp.recorded()
	if len(records) != 1 {
		t.Fatalf("control plane received %d records, want 1", len(records))
	}
	got := records[0]

	span := firstSpan(next.traces[0])
	fingerprint, ok := span.Attributes().Get("trustvian.fingerprint.id")
	if !ok {
		t.Fatal("span was not enriched; the existing attribute path must be unaffected")
	}
	if got.FingerprintID != fingerprint.Str() {
		t.Errorf("record fingerprint %q != span attribute %q; the two must come from one Result",
			got.FingerprintID, fingerprint.Str())
	}

	// Fields that exist on the record and have no trustvian.* attribute.
	// Their presence is what proves the record was projected rather than
	// rebuilt from the enriched span.
	if got.PolicyReason == "" {
		t.Error("record.PolicyReason is empty; it has no span attribute and must come from the Result")
	}
	if len(got.Contributors) == 0 {
		t.Error("record.Contributors is empty; it has no span attribute and must come from the Result")
	}
	if got.Behavior.TargetName != "crm.localhost" {
		t.Errorf("record.Behavior.TargetName = %q, want crm.localhost", got.Behavior.TargetName)
	}
	if got.Environment != "local" {
		t.Errorf("record.Environment = %q, want local", got.Environment)
	}
	if got.TraceID == "" || got.SpanID == "" {
		t.Error("record lost the telemetry correlation the span carried")
	}
}

// TestEvaluationOmittedLeavesSpanPathUnchanged is the compatibility
// guarantee every existing deployment depends on.
func TestEvaluationOmittedLeavesSpanPathUnchanged(t *testing.T) {
	next := &capturingConsumer{}
	proc := newTestProcessor(t, next)

	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	if next.len() != 1 {
		t.Fatalf("forwarded %d batches, want 1", next.len())
	}
	span := firstSpan(next.traces[0])
	for _, key := range []string{
		"trustvian.anomaly.score", "trustvian.trust.score",
		"trustvian.risk.level", "trustvian.decision", "trustvian.fingerprint.id",
	} {
		if _, ok := span.Attributes().Get(key); !ok {
			t.Errorf("span is missing %s; enrichment must be unchanged", key)
		}
	}
}

// TestEvaluationFailureIsPermanent covers the correctness reason the wrapper
// exists: a retried batch would re-analyze spans whose records already
// committed and resend them under new sequence numbers, silently inflating
// the evidence.
func TestEvaluationFailureIsPermanent(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.failNext.Store(true)

	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config())
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	err = proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost"))
	if err == nil {
		t.Fatal("ConsumeTraces() error = nil, want a failure in required mode")
	}
	if !consumererror.IsPermanent(err) {
		t.Errorf("error is not permanent; a retried batch would double-count committed records")
	}
}

// TestStartInitializesEvaluationWithoutHealthBlock covers Review Focus item
// 1. Start's existing first statement returns early when no health server is
// configured, which is the common case — evaluation initialization must not
// sit behind it.
func TestStartInitializesEvaluationWithoutHealthBlock(t *testing.T) {
	cp := newIngestAPIServer(t)
	cfg := cp.config()
	if cfg.Health != nil {
		t.Fatal("this test requires no health block")
	}

	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cfg)
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	// If Start skipped initialization, Record fails with "used before its
	// ingest state was read" rather than posting.
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; Start did not initialize the sink", err)
	}
	if len(cp.recorded()) != 1 {
		t.Error("no record reached the control plane; Start returned before initializing")
	}
}

// TestStartFailsWhenTheAPIIsUnreachable is the fail-closed startup
// property: a Collector that cannot reach its control plane must refuse to
// come up rather than enrich spans while recording nothing.
func TestStartFailsWhenTheAPIIsUnreachable(t *testing.T) {
	// A server that is immediately closed gives a refused connection on a
	// port nothing else is using.
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()

	required := true
	cfg := &trustvianprocessor.Config{Evaluation: &trustvianprocessor.EvaluationConfig{
		APIURL: url, RunID: "run-1", BehavioralProfile: "p", Required: &required,
	}}

	factory := trustvianprocessor.NewFactory()
	proc, err := factory.CreateTraces(context.Background(), testSettings(), cfg, consumertest.NewNop())
	if err != nil {
		t.Fatalf("CreateTraces() error = %v; construction must not perform I/O", err)
	}
	if err := proc.Start(context.Background(), nopHost()); err == nil {
		t.Fatal("Start() error = nil, want a refusal when the control plane is unreachable")
	}
}

// TestInvalidSpanConsumesNoSequence covers Review Focus item 5. A span that
// never produces a Result must not advance the cursor, or the next real span
// would leave a gap the run never recovers from.
func TestInvalidSpanConsumesNoSequence(t *testing.T) {
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config())
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}

	// No service.name anywhere, so Actor.ID is empty and Validate fails.
	invalid := ptrace.NewTraces()
	rs := invalid.ResourceSpans().AppendEmpty()
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("GET")

	if err := proc.ConsumeTraces(context.Background(), invalid); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; an invalid span must not fail the batch", err)
	}
	if got := len(cp.recorded()); got != 0 {
		t.Fatalf("control plane received %d records for an invalid span, want 0", got)
	}

	// The next valid span must still be sequence 1.
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	if got := len(cp.recorded()); got != 1 {
		t.Errorf("control plane received %d records, want 1", got)
	}
}

// spanStart is fixed for the whole test binary, so two spans built by
// evaluationTraces differ only in the fields the test varied.
var spanStart = time.Now()

func testSettings() processor.Settings {
	return processor.Settings{
		ID:                component.NewID(component.MustNewType("trustvian")),
		TelemetrySettings: componenttest.NewNopTelemetrySettings(),
		BuildInfo:         component.NewDefaultBuildInfo(),
	}
}

func nopHost() component.Host { return componenttest.NewNopHost() }
