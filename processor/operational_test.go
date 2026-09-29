package trustvianprocessor_test

// The OTLP adapter's half of task 084's contract: correlation read from the
// span's own parent reference, and duration and status read from the span's own
// fields. internal/otel's paired suite asserts the same documented rules from
// the SDK side.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	trustvianprocessor "trustvian-processor"
)

// timedSpan builds one span with chosen timing, status and parentage.
func timedSpan(
	t *testing.T, parent [8]byte, start, end pcommon.Timestamp, status ptrace.StatusCode,
) (pcommon.Map, ptrace.Span) {
	t.Helper()
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "svc")
	rs.Resource().Attributes().PutStr("deployment.environment.name", "local")
	span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	span.SetName("POST")
	span.SetKind(ptrace.SpanKindClient)
	span.SetTraceID(pcommon.TraceID([16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}))
	span.SetSpanID(pcommon.SpanID([8]byte{9, 9, 9, 9, 9, 9, 9, 9}))
	if parent != [8]byte{} {
		span.SetParentSpanID(pcommon.SpanID(parent))
	}
	span.SetStartTimestamp(start)
	span.SetEndTimestamp(end)
	span.Status().SetCode(status)
	span.Attributes().PutStr("http.request.method", "POST")
	span.Attributes().PutStr("server.address", "svc.localhost")
	return rs.Resource().Attributes(), span
}

func TestAdapterReadsDurationAvailability(t *testing.T) {
	const base = pcommon.Timestamp(1_700_000_000_000_000_000)

	tests := []struct {
		name         string
		start, end   pcommon.Timestamp
		wantNanos    uint64
		wantObserved bool
	}{
		{"an ordinary span", base, base + 1_500_000, 1_500_000, true},
		{"a genuine zero", base, base, 0, true},
		{"no end timestamp", base, 0, 0, false},
		{"no start timestamp", 0, base, 0, false},
		{"neither timestamp", 0, 0, 0, false},
		{"end before start", base + 10, base, 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, span := timedSpan(t, [8]byte{}, tt.start, tt.end, ptrace.StatusCodeUnset)
			ev := trustvianprocessor.EventFromSpan(res, span)

			if ev.Execution.DurationObserved != tt.wantObserved {
				t.Fatalf("DurationObserved = %v, want %v",
					ev.Execution.DurationObserved, tt.wantObserved)
			}
			if ev.Execution.DurationNanos != tt.wantNanos {
				t.Errorf("DurationNanos = %d, want %d", ev.Execution.DurationNanos, tt.wantNanos)
			}
		})
	}
}

// TestAdapterStatusMapping covers all three span statuses. There is no fourth:
// a span always carries a code, so the adapter never produces "unavailable".
func TestAdapterStatusMapping(t *testing.T) {
	const base = pcommon.Timestamp(1_700_000_000_000_000_000)
	tests := []struct {
		code ptrace.StatusCode
		want event.SpanStatus
	}{
		{ptrace.StatusCodeUnset, event.StatusUnset},
		{ptrace.StatusCodeOk, event.StatusOK},
		{ptrace.StatusCodeError, event.StatusError},
	}
	for _, tt := range tests {
		res, span := timedSpan(t, [8]byte{}, base, base+1, tt.code)
		ev := trustvianprocessor.EventFromSpan(res, span)
		if ev.Execution.Status != tt.want {
			t.Errorf("status for code %v = %q, want %q", tt.code, ev.Execution.Status, tt.want)
		}
		if ev.Execution.Status == event.StatusUnavailable {
			t.Error("a span produced an unavailable status; every span carries a code")
		}
	}
}

// TestAdapterReadsParentage covers root, child, and the fact that nothing
// checks whether the parent exists.
func TestAdapterReadsParentage(t *testing.T) {
	const base = pcommon.Timestamp(1_700_000_000_000_000_000)

	res, root := timedSpan(t, [8]byte{}, base, base+1, ptrace.StatusCodeUnset)
	rootEvent := trustvianprocessor.EventFromSpan(res, root)
	if rootEvent.Context.SpanLineage != event.LineageRoot {
		t.Errorf("root lineage = %q, want root", rootEvent.Context.SpanLineage)
	}
	if rootEvent.Context.ParentSpanID != "" {
		t.Errorf("a root names parent %q", rootEvent.Context.ParentSpanID)
	}

	res, child := timedSpan(t, [8]byte{7, 7, 7, 7, 7, 7, 7, 7}, base, base+1, ptrace.StatusCodeUnset)
	childEvent := trustvianprocessor.EventFromSpan(res, child)
	if childEvent.Context.SpanLineage != event.LineageChild {
		t.Errorf("child lineage = %q, want child", childEvent.Context.SpanLineage)
	}
	if childEvent.Context.ParentSpanID != "0707070707070707" {
		t.Errorf("child parent = %q, want 0707070707070707", childEvent.Context.ParentSpanID)
	}
	// The parent named here was never sent through the processor, and that is
	// not an error: a parent may be sampled away or arrive later.
	if childEvent.Context.TraceID != rootEvent.Context.TraceID {
		t.Error("the fixture is wrong: both spans should share a trace")
	}
}

// TestChildArrivingBeforeItsParentIsAccepted is the ordering that actually
// happens: a parent span ends after the children it started, so the child is
// exported first.
func TestChildArrivingBeforeItsParentIsAccepted(t *testing.T) {
	obs := ingest(t, tracesFrom("support-agent",
		// Child first, deliberately.
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}),
		toolSpan("export_customer", [8]byte{1}, [8]byte{}),
	))
	if len(obs) != 2 {
		t.Fatalf("got %d observations, want 2; an out-of-order child was rejected", len(obs))
	}
}

// TestOrphanedChildIsAccepted: the named parent never arrives at all.
func TestOrphanedChildIsAccepted(t *testing.T) {
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(), tracesFrom("svc",
		httpSpan("export.localhost", [8]byte{2}, [8]byte{99}))); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	records := cp.recorded()
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1; a child whose parent was never sent was dropped",
			len(records))
	}
	if records[0].SpanLineage != event.LineageChild {
		t.Errorf("lineage = %q, want child", records[0].SpanLineage)
	}
	if records[0].ParentSpanID != "6300000000000000" {
		t.Errorf("parent = %q, want the id the producer set", records[0].ParentSpanID)
	}
}

// TestVolatileBridgeAndRecordedEvidenceAgree is 082's validation requirement,
// stated precisely: the two paths agree wherever both state a value, and the
// zero case is the one documented divergence.
func TestVolatileBridgeAndRecordedEvidenceAgree(t *testing.T) {
	const base = pcommon.Timestamp(1_700_000_000_000_000_000)

	t.Run("a positive duration reaches both", func(t *testing.T) {
		res, span := timedSpan(t, [8]byte{}, base, base+2_000_000, ptrace.StatusCodeError)
		ev := trustvianprocessor.EventFromSpan(res, span)

		ms, ok := ev.Attributes["duration_ms"].(float64)
		if !ok {
			t.Fatal("the volatile bridge wrote no duration_ms")
		}
		if !ev.Execution.DurationObserved {
			t.Fatal("the evidence path recorded no duration")
		}
		if want := float64(ev.Execution.DurationNanos) / float64(time.Millisecond); ms != want {
			t.Errorf("duration_ms = %v, recorded nanos imply %v", ms, want)
		}
		if ev.Attributes["error"] != true {
			t.Error("the volatile bridge did not flag an error span")
		}
		if ev.Execution.Status != event.StatusError {
			t.Errorf("recorded status = %q, want error", ev.Execution.Status)
		}
	})

	t.Run("a zero duration is the documented divergence", func(t *testing.T) {
		res, span := timedSpan(t, [8]byte{}, base, base, ptrace.StatusCodeUnset)
		ev := trustvianprocessor.EventFromSpan(res, span)

		if _, present := ev.Attributes["duration_ms"]; present {
			t.Error("the volatile bridge now writes a zero duration; task 084 must not " +
				"change the feature path")
		}
		if !ev.Execution.DurationObserved || ev.Execution.DurationNanos != 0 {
			t.Error("the evidence path must record a measured zero")
		}
	})

	t.Run("a non-error status writes no error attribute", func(t *testing.T) {
		for _, code := range []ptrace.StatusCode{ptrace.StatusCodeUnset, ptrace.StatusCodeOk} {
			res, span := timedSpan(t, [8]byte{}, base, base+1, code)
			ev := trustvianprocessor.EventFromSpan(res, span)
			if _, present := ev.Attributes["error"]; present {
				t.Errorf("code %v wrote an error attribute", code)
			}
		}
	})
}

// TestRetryPreservesOperationalEvidence is the idempotency requirement: a
// re-presented record must be byte-identical, or the control plane's digest
// rule sees a different record at the same sequence and conflicts.
func TestRetryPreservesOperationalEvidence(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.dropOnAttempt.Store(1) // commit, then destroy the reply

	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(), tracesFrom("svc",
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}))); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	if cp.recordAttempts.Load() < 2 {
		t.Fatalf("only %d attempts; the retry path was not exercised", cp.recordAttempts.Load())
	}
	records := cp.recorded()
	if len(records) != 1 {
		t.Fatalf("the run holds %d records, want 1 (a replay must not duplicate)", len(records))
	}
	// The record the server kept still carries everything the first attempt sent.
	if records[0].ParentSpanID == "" || records[0].SpanLineage != event.LineageChild {
		t.Errorf("the retried record lost its correlation: parent %q lineage %q",
			records[0].ParentSpanID, records[0].SpanLineage)
	}
	if records[0].DurationNanos == "" {
		t.Error("the retried record lost its duration")
	}
	if records[0].SpanStatus == event.StatusUnavailable {
		t.Error("the retried record lost its status")
	}
}

// TestJournalledRecordKeepsItsEvidence is the same property across a restart:
// the pending entry round-trips through JSON, and a record whose evidence
// changed would produce a different digest and conflict.
func TestJournalledRecordKeepsItsEvidence(t *testing.T) {
	cp := newIngestAPIServer(t)
	proc, err := newTestProcessorWithConfig(t, consumertest.NewNop(), cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := proc.ConsumeTraces(context.Background(), tracesFrom("svc",
		httpSpan("export.localhost", [8]byte{2}, [8]byte{1}))); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	original := cp.recorded()[0]
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var round trustvian.DecisionRecord
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	reencoded, err := json.Marshal(round)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(encoded) != string(reencoded) {
		t.Errorf("a record does not survive a JSON round trip byte-identically, so a "+
			"journalled retry would produce a different digest:\n before %s\n after  %s",
			encoded, reencoded)
	}
}
