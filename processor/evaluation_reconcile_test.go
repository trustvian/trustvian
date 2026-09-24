package trustvianprocessor_test

// The processor half of the ambiguous-delivery contract. internal/evaluation
// proves the sink reconciles a lost response against the server's replay
// rule; these prove what the span path does around it — that a record the
// run holds is also a record this Engine learned from, and that a record the
// run does not hold is not.
//
// The two directions of that divergence are not symmetric in how they fail.
// Learning from a record the run never received leaves a baseline that scores
// later spans against history no scorecard can see. Failing to learn from one
// the run does hold leaves the evaluation's evidence describing behavior this
// Engine will not recognize next time. Both are silent; the tests below are
// what makes them loud.

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/collector/consumer/consumererror"

	"trustvian-processor/internal/evaluation"
	tvmetrics "trustvian-processor/internal/metrics"
)

// TestEvaluationLostResponseIsReconciled is the processor-level statement of
// the blocker: the control plane commits the record and its response is
// destroyed. Nothing about that is the operator's problem — the record is
// there, so the span succeeds, the batch is forwarded, and no second record
// is created.
func TestEvaluationLostResponseIsReconciled(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.dropOnAttempt.Store(1) // committed, then the reply is destroyed

	next := &capturingConsumer{}
	proc, err := newTestProcessorWithConfig(t, next, cp.config(t))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}

	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; a lost response must be reconciled, not surfaced", err)
	}
	if got := len(cp.recorded()); got != 1 {
		t.Fatalf("control plane holds %d records, want 1 — reconciliation must not duplicate evidence", got)
	}
	if next.len() != 1 {
		t.Errorf("next consumer received %d batches, want 1; the span path succeeded", next.len())
	}

	// And the run continues from the server's cursor: a second span takes
	// sequence 2 without conflicting against the sequence the first holds.
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "knowledge.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v; sequence 2 must be free", err)
	}
	records := cp.recorded()
	if len(records) != 2 {
		t.Fatalf("control plane holds %d records, want 2", len(records))
	}
	if records[0].Behavior.TargetName != "crm.localhost" ||
		records[1].Behavior.TargetName != "knowledge.localhost" {
		t.Errorf("evidence = [%s %s], want [crm.localhost knowledge.localhost] — "+
			"a record was substituted into a sequence it did not claim",
			records[0].Behavior.TargetName, records[1].Behavior.TargetName)
	}
}

// TestEvaluationLostResponseStillObserves is the divergence test. The record
// is durable; the local Engine must have learned from it too.
//
// Before the recovery mechanism this was exactly the broken case: Record
// returned the transport error, processSpan returned before Observe, and the
// run's evidence held a record whose behavior this Engine had never folded
// into a baseline.
func TestEvaluationLostResponseStillObserves(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.dropOnAttempt.Store(1)

	proc, gather := newMeteredProcessor(t, cp.config(t))
	if err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	if got := len(cp.recorded()); got != 1 {
		t.Fatalf("control plane holds %d records, want 1", got)
	}
	counts := outcomes(t, gather())
	if total := sumValues(counts["trustvian.observations"]); total != 1 {
		t.Errorf("observations total = %d, want 1: %v — the run holds the record, so the "+
			"Engine must hold the learning that goes with it", total, counts["trustvian.observations"])
	}
	// The reconciliation is visible, not hidden: the disposition the server
	// actually returned is what the counter carries.
	if got := counts["trustvian.evaluation.records"][tvmetrics.OutcomeReplayed]; got != 1 {
		t.Errorf("evaluation.records{replayed} = %d, want 1: %v",
			got, counts["trustvian.evaluation.records"])
	}
	if got := counts["trustvian.evaluation.records"][tvmetrics.OutcomeError]; got != 0 {
		t.Errorf("evaluation.records{error} = %d, want 0 — a reconciled record is not a failure", got)
	}
}

// TestEvaluationUnresolvedRecordIsNotObserved covers the case reconciliation
// cannot finish inside the call: every attempt's response is destroyed.
//
// Nothing is learned from that record. "Unknown" is not "committed", and
// with a durable store the difference outlives the process: an observation
// applied here survives a restart the sink's own pending state would
// otherwise have completed, and the run may never have received the record
// at all. The learning waits for the control plane's answer — in this
// process or the next one (see the restart tests in
// evaluation_restart_test.go).
func TestEvaluationUnresolvedRecordIsNotObserved(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.dropAlways.Store(true)

	proc, gather := newMeteredProcessor(t, cp.config(t))
	err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost"))
	if err == nil {
		t.Fatal("ConsumeTraces() error = nil, want an unresolved ingest surfaced")
	}
	if !consumererror.IsPermanent(err) {
		t.Errorf("error is not permanent; a retried batch would resend the record under a new sequence")
	}
	if !errors.Is(err, evaluation.ErrUnresolved) {
		t.Errorf("error = %v, want it reported as unresolved", err)
	}

	counts := outcomes(t, gather())
	if total := sumValues(counts["trustvian.observations"]); total != 0 {
		t.Errorf("observations total = %d, want 0: %v — an unconfirmed record must not be learned from, "+
			"because a durable baseline would keep that learning across the restart that settles it",
			total, counts["trustvian.observations"])
	}
	if got := counts["trustvian.evaluation.records"][tvmetrics.OutcomeError]; got != 1 {
		t.Errorf("evaluation.records{error} = %d, want 1 — an unresolved record is still a failure", got)
	}
}

// TestEvaluationDeclinedRecordIsNotObserved is the other direction. A record
// the control plane refused is not in the run, so folding it into the
// baseline would teach this Engine from behavior no scorecard can account
// for — divergence pointing the other way, and just as silent.
func TestEvaluationDeclinedRecordIsNotObserved(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.rejectOnAttempt.Store(1)

	proc, gather := newMeteredProcessor(t, cp.config(t))
	err := proc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost"))
	if err == nil {
		t.Fatal("ConsumeTraces() error = nil, want the refusal surfaced")
	}
	if !consumererror.IsPermanent(err) {
		t.Error("error is not permanent")
	}
	if got := len(cp.recorded()); got != 0 {
		t.Fatalf("control plane holds %d records, want 0", got)
	}

	counts := outcomes(t, gather())
	if total := sumValues(counts["trustvian.observations"]); total != 0 {
		t.Errorf("observations total = %d, want 0: %v — a declined record must not be learned from",
			total, counts["trustvian.observations"])
	}
	// The span was still analyzed and enriched; only the learning is
	// withheld, so this is not a quiet failure of the whole span path.
	if got := counts["trustvian.analyses"][tvmetrics.OutcomeAnalyzed]; got != 1 {
		t.Errorf("analyses{analyzed} = %d, want 1", got)
	}
}

// TestEvaluationObservesExactlyOncePerSpan pins the other half of the
// Observe contract. A reconciled record is one record and one observation —
// the retry must not fold the same Result into the baseline twice, which
// would be the recovery mechanism inventing behavior that never happened.
func TestEvaluationObservesExactlyOncePerSpan(t *testing.T) {
	cp := newIngestAPIServer(t)
	cp.dropOnAttempt.Store(2) // the second span's response is destroyed

	proc, gather := newMeteredProcessor(t, cp.config(t))
	td := evaluationTracesN("support-agent", "crm.localhost", "knowledge.localhost", "files.localhost")
	if err := proc.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}

	if got := len(cp.recorded()); got != 3 {
		t.Fatalf("control plane holds %d records, want 3", got)
	}
	counts := outcomes(t, gather())
	if total := sumValues(counts["trustvian.observations"]); total != 3 {
		t.Errorf("observations total = %d, want 3 — one per span, never one per attempt: %v",
			total, counts["trustvian.observations"])
	}
	if total := sumValues(counts["trustvian.evaluation.records"]); total != 3 {
		t.Errorf("evaluation.records total = %d, want 3 — the reconciliation is one record's "+
			"outcome, not a second record: %v", total, counts["trustvian.evaluation.records"])
	}
}

// TestEvaluationUnresolvedErrorIsReportedAsSuch keeps the two failure classes
// distinguishable to a caller. The processor branches on it to decide whether
// Observe may run, so a refusal that reported itself as unresolved — or the
// reverse — would silently pick the wrong side of the divergence above.
func TestEvaluationUnresolvedErrorIsReportedAsSuch(t *testing.T) {
	t.Run("lost response is unresolved", func(t *testing.T) {
		cp := newIngestAPIServer(t)
		cp.dropAlways.Store(true)
		proc, err := newTestProcessorWithConfig(t, &capturingConsumer{}, cp.config(t))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		err = proc.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost"))
		if err == nil {
			t.Fatal("ConsumeTraces() error = nil, want a failure")
		}
		if !errors.Is(err, evaluation.ErrUnresolved) {
			t.Errorf("error = %v, want it to report an unresolved ingest", err)
		}
	})

	t.Run("declined record is not unresolved", func(t *testing.T) {
		cp := newIngestAPIServer(t)
		cp.rejectOnAttempt.Store(1)
		proc, err := newTestProcessorWithConfig(t, &capturingConsumer{}, cp.config(t))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		err = proc.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost"))
		if err == nil {
			t.Fatal("ConsumeTraces() error = nil, want a failure")
		}
		if errors.Is(err, evaluation.ErrUnresolved) {
			t.Errorf("error = %v, want a definitive refusal", err)
		}
	})
}
