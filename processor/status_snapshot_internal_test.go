package trustvianprocessor

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

// TestStatusSnapshotCountsNeverContradictEachOther is the review's race: the
// reporter goroutine reads the span counters while processSpan increments
// them, and the reads are not one atomic snapshot. A report whose evaluated,
// invalid or analyze_errors exceeds received is refused by the control plane,
// so every snapshot taken under concurrent load must hold all three bounds.
func TestStatusSnapshotCountsNeverContradictEachOther(t *testing.T) {
	p, err := newTrustvianProcessor(componenttest.NewNopTelemetrySettings(), consumertest.NewNop(), &Config{
		Status: &StatusConfig{APIURL: "http://127.0.0.1:1", Interval: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}

	// One span per worker, so the enrichment each processSpan writes onto its
	// span stays on one goroutine. Even workers send a valid span and odd ones a
	// span with no actor, which is counted invalid — both outcome paths race
	// the total.
	span := func(worker int) (pcommon.Map, ptrace.Span) {
		td := ptrace.NewTraces()
		rs := td.ResourceSpans().AppendEmpty()
		if worker%2 == 0 {
			rs.Resource().Attributes().PutStr("service.name", "svc-"+strconv.Itoa(worker))
		}
		s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		s.SetName("GET /x")
		s.SetKind(ptrace.SpanKindServer)
		s.SetTraceID(pcommon.TraceID{byte(worker + 1), 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1})
		s.SetSpanID(pcommon.SpanID{byte(worker + 1), 1, 1, 1, 1, 1, 1, 1})
		now := time.Now()
		s.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
		s.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Millisecond)))
		s.Attributes().PutStr("http.request.method", "GET")
		return rs.Resource().Attributes(), s
	}

	const workers, spansEach = 8, 3000
	var done atomic.Bool
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resource, s := span(w)
			for range spansEach {
				if err := p.processSpan(context.Background(), resource, s); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); done.Store(true) }()

	started := time.Now()
	snapshots := 0
	check := func() {
		spans := p.statusSnapshot(started, time.Now()).Spans
		received, _ := strconv.ParseUint(spans.Received, 10, 64)
		for name, value := range map[string]string{
			"evaluated": spans.Evaluated, "invalid": spans.Invalid, "analyze_errors": spans.AnalyzeErrors,
		} {
			v, _ := strconv.ParseUint(value, 10, 64)
			if v > received {
				t.Fatalf("snapshot %d: %s %d exceeds received %d", snapshots, name, v, received)
			}
		}
		snapshots++
	}
	for !done.Load() {
		check()
	}
	check()

	final := p.statusSnapshot(started, time.Now()).Spans
	if final.Received != strconv.Itoa(workers*spansEach) {
		t.Fatalf("received %s, want %d", final.Received, workers*spansEach)
	}
	t.Logf("%d snapshots under concurrent load, final %+v", snapshots, final)
}

// TestStatusSnapshotReadOrderSurvivesASpanBetweenLoads lands one whole span
// between the snapshot's counter loads — deterministically, on the reading
// goroutine — which is the interleaving the read order exists for. With the
// total read first, evaluated would come out one above received.
func TestStatusSnapshotReadOrderSurvivesASpanBetweenLoads(t *testing.T) {
	p, err := newTrustvianProcessor(componenttest.NewNopTelemetrySettings(), consumertest.NewNop(), &Config{
		Status: &StatusConfig{APIURL: "http://127.0.0.1:1", Interval: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", "svc")
	s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	s.SetName("GET /x")
	s.SetKind(ptrace.SpanKindServer)
	s.SetTraceID(pcommon.TraceID{9, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1})
	s.SetSpanID(pcommon.SpanID{9, 1, 1, 1, 1, 1, 1, 1})
	now := time.Now()
	s.SetStartTimestamp(pcommon.NewTimestampFromTime(now))
	s.SetEndTimestamp(pcommon.NewTimestampFromTime(now.Add(time.Millisecond)))
	process := func() {
		if err := p.processSpan(context.Background(), rs.Resource().Attributes(), s); err != nil {
			t.Fatal(err)
		}
	}
	// Every span so far is evaluated, so received == evaluated: there is no
	// slack for an out-of-order read to hide in.
	for range 3 {
		process()
	}

	saved := statusSnapshotBetweenLoads
	statusSnapshotBetweenLoads = process
	t.Cleanup(func() { statusSnapshotBetweenLoads = saved })

	spans := p.statusSnapshot(now, now).Spans
	received, _ := strconv.ParseUint(spans.Received, 10, 64)
	evaluated, _ := strconv.ParseUint(spans.Evaluated, 10, 64)
	if evaluated > received {
		t.Fatalf("a span between the loads made evaluated %d exceed received %d", evaluated, received)
	}
	if received != 4 {
		t.Fatalf("received %d, want 4: the span between the loads is counted by the later read", received)
	}
}
