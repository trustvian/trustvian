package trustvianprocessor_test

// Restart correctness with a durable baseline.
//
// The in-process tests (evaluation_reconcile_test.go) prove a lost response
// is reconciled before Record returns. These prove the part no process can
// finish: a Collector that dies while a record's fate is unknown, and a
// second one that starts against the same durable baseline.
//
// A durable store is what makes this more than bookkeeping. Learning applied
// to an in-memory baseline disappears with the process; learning applied to
// a FileStore is written through to disk and read back by the next one. So a
// Collector that learned from a record the control plane never received
// leaves the two permanently, silently disagreeing — the run's evidence
// without it, the baseline with it — and every later span is scored against
// history no scorecard can account for.
//
// AnomalyConfidence is what these assert on rather than metrics, because it
// is the learning itself observable: it is the baseline's familiarity with a
// fingerprint (internal/anomaly: min(count/MinObservations, 1)), so zero
// means nothing about that fingerprint was ever learned and a positive value
// means something was. Each test therefore analyzes the *same* behavioral
// shape twice, and reads the second record's confidence.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/collector/consumer/consumertest"

	trustvian "github.com/trustvian/trustvian"
	trustvianprocessor "trustvian-processor"
)

// durablePaths is one Collector's two durable files: the learned baseline
// and the pending ingest state. A restart is modelled by building a second
// processor over the same two, which is exactly what a container restart
// with a mounted volume does.
type durablePaths struct {
	baseline string
	pending  string
}

func newDurablePaths(t *testing.T) durablePaths {
	t.Helper()
	dir := t.TempDir()
	return durablePaths{
		baseline: filepath.Join(dir, "baseline.json"),
		pending:  filepath.Join(dir, "pending.json"),
	}
}

func (d durablePaths) storage() map[string]any {
	return map[string]any{
		"version": "v1",
		"type":    "file",
		"file":    map[string]any{"path": d.baseline},
	}
}

// learnedFingerprints reports how many observations the durable baseline
// holds per fingerprint, read from the store's own file.
//
// Decoded as generic JSON on purpose: the file is the core module's format,
// and a test in this module asserting on it through a struct would be
// claiming a compatibility this module does not have. What it needs is only
// the count, which is the one number that says whether a record was learned
// from once, twice, or not at all.
func learnedFingerprints(t *testing.T, path string) map[string]float64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]float64{}
	}
	if err != nil {
		t.Fatalf("reading the baseline file: %v", err)
	}
	var snapshot struct {
		Baselines []struct {
			Fingerprints map[string]struct {
				Count float64 `json:"Count"`
			} `json:"Fingerprints"`
		} `json:"baselines"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("decoding the baseline file: %v", err)
	}
	counts := map[string]float64{}
	for _, bl := range snapshot.Baselines {
		for id, stats := range bl.Fingerprints {
			counts[id] += stats.Count
		}
	}
	return counts
}

func totalObservations(counts map[string]float64) float64 {
	var total float64
	for _, count := range counts {
		total += count
	}
	return total
}

// TestRestartAfterUnreachedRecordLeavesNoLearning is required regression 1:
// the record never reached the control plane, and the process is gone.
//
// Before the durable pending state, the processor observed on ErrUnresolved
// on the theory that the record "may be durable". With a FileStore that
// theory is written to disk: this test fails against that implementation,
// because B would be analyzed against A's learning while the run holds
// neither record.
func TestRestartAfterUnreachedRecordLeavesNoLearning(t *testing.T) {
	paths := newDurablePaths(t)
	cp := newIngestAPIServer(t)

	// Instance 1. The context is already cancelled, so the request provably
	// never reaches the control plane — asserted below against the server's
	// own state, not against the client's report of it.
	first, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := first.ConsumeTraces(cancelled,
		evaluationTraces("support-agent", "crm.localhost")); err == nil {
		t.Fatal("ConsumeTraces() error = nil, want the unresolved ingest surfaced")
	}

	if got := len(cp.recorded()); got != 0 {
		t.Fatalf("control plane holds %d records, want 0 — the request never left", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 0 {
		t.Fatalf("the durable baseline holds %v observations, want 0 — nothing may be learned "+
			"from a record the control plane never confirmed", got)
	}

	// Instance 1 is discarded. Instance 2 inherits both durable files.
	second, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if got := len(cp.recorded()); got != 0 {
		t.Errorf("control plane holds %d records after recovery, want 0 — the run never received it, "+
			"and a dropped batch's record must not arrive alone after a restart", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 0 {
		t.Errorf("the durable baseline holds %v observations after recovery, want 0", got)
	}

	// B is the same behavioral shape A was. Its confidence is the proof:
	// zero means the reloaded baseline knows nothing about that fingerprint.
	if err := second.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	records := cp.recorded()
	if len(records) != 1 {
		t.Fatalf("control plane holds %d records, want 1", len(records))
	}
	if records[0].AnomalyConfidence != 0 {
		t.Errorf("B's anomaly_confidence = %v, want 0 — A was never in the run's evidence, "+
			"so it must not be in the baseline B was analyzed against",
			records[0].AnomalyConfidence)
	}
	if got := cp.nextSequence(); got != 2 {
		t.Errorf("the run's next sequence = %d, want 2 — B must have taken sequence 1, "+
			"the sequence A never consumed", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 1 {
		t.Errorf("the durable baseline holds %v observations, want 1 — B's, and only B's", got)
	}
}

// TestRestartAfterCommittedRecordLearnsItExactlyOnce is required regression
// 2, the hard case: the control plane committed the record, the reply was
// destroyed, and the process died before it could reconcile.
//
// The run holds that record. A restart must therefore end with the baseline
// holding it too — exactly once, learned during recovery rather than
// re-derived — and must not add a second copy to the evidence.
func TestRestartAfterCommittedRecordLearnsItExactlyOnce(t *testing.T) {
	paths := newDurablePaths(t)
	cp := newIngestAPIServer(t)
	cp.dropAlways.Store(true) // every reply is destroyed, including the retry's

	first, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := first.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err == nil {
		t.Fatal("ConsumeTraces() error = nil, want the unresolved ingest surfaced")
	}

	if got := len(cp.recorded()); got != 1 {
		t.Fatalf("control plane holds %d records, want 1 — it committed before the reply was lost", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 0 {
		t.Fatalf("the durable baseline holds %v observations, want 0 — the record was never confirmed "+
			"to this process, so nothing may be learned from it yet", got)
	}

	// Instance 1 is discarded. The control plane is reachable again.
	cp.dropAlways.Store(false)
	second, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}

	if got := len(cp.recorded()); got != 1 {
		t.Errorf("control plane holds %d records after recovery, want 1 — a replay adds no evidence", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 1 {
		t.Fatalf("the durable baseline holds %v observations after recovery, want exactly 1 — "+
			"the run holds that record, so the baseline must too, once", got)
	}

	// The same behavioral shape again: this time the baseline knows it.
	if err := second.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	records := cp.recorded()
	if len(records) != 2 {
		t.Fatalf("control plane holds %d records, want 2", len(records))
	}
	if records[1].AnomalyConfidence <= 0 {
		t.Errorf("the second record's anomaly_confidence = %v, want > 0 — the run holds the first "+
			"record, so the baseline it was analyzed against must hold its learning",
			records[1].AnomalyConfidence)
	}
	if got := cp.nextSequence(); got != 3 {
		t.Errorf("the run's next sequence = %d, want 3 — the recovered record kept sequence 1 "+
			"and the new one took 2", got)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 2 {
		t.Errorf("the durable baseline holds %v observations, want 2 — one per record in the run, "+
			"never one per delivery attempt", got)
	}
}

// TestRestartDoesNotRelearnAConfirmedRecord covers the window a restart
// cannot settle: the previous process died between the control plane
// confirming the record and the sink releasing it, so whether its learning
// was applied is unknowable.
//
// It is not applied again. A missing observation makes a fingerprint look
// less familiar, which fails safe; a doubled one makes it look more familiar
// than the evidence supports, which is a silent weakening of exactly the
// signal this engine decides on.
func TestRestartDoesNotRelearnAConfirmedRecord(t *testing.T) {
	paths := newDurablePaths(t)
	cp := newIngestAPIServer(t)

	// A clean first run, so the record at sequence 1 is genuinely in the run
	// and its learning is genuinely in the baseline.
	first, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if err := first.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "crm.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 1 {
		t.Fatalf("the durable baseline holds %v observations, want 1", got)
	}

	// Rewind the pending state to the instant before that record was
	// released: confirmed, learning already applied, process dies.
	writeConfirmedPendingState(t, paths.pending, "run-1", "1", cp.recorded()[0])

	second, err := newTestProcessorWithConfig(t, consumertest.NewNop(),
		cp.configAt(t, paths.pending, paths.storage()))
	if err != nil {
		t.Fatalf("CreateTraces() error = %v", err)
	}
	if got := totalObservations(learnedFingerprints(t, paths.baseline)); got != 1 {
		t.Errorf("the durable baseline holds %v observations after recovery, want 1 — a record whose "+
			"learning may already have been applied must not be learned from again", got)
	}
	if got := len(cp.recorded()); got != 1 {
		t.Errorf("control plane holds %d records after recovery, want 1", got)
	}

	// And the run continues from the server's cursor.
	if err := second.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", "knowledge.localhost")); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	if got := cp.nextSequence(); got != 3 {
		t.Errorf("the run's next sequence = %d, want 3", got)
	}
}

// writeConfirmedPendingState writes the durable state a process would have
// left behind had it died between the control plane confirming a record and
// the sink releasing it.
//
// Written here rather than produced by interrupting a real call, because the
// window is between two local writes: there is no hook inside it, and a test
// that tried to hit it would be timing-dependent. The format is the sink's
// own, which is why this file's shape is asserted in
// internal/evaluation/restart_test.go rather than trusted here.
func writeConfirmedPendingState(
	t *testing.T, path, runID, sequence string, record trustvian.DecisionRecord,
) {
	t.Helper()
	learning, err := json.Marshal(trustvian.Result{})
	if err != nil {
		t.Fatalf("encoding learning: %v", err)
	}
	entry := map[string]any{
		"version": "1", "run_id": runID, "sequence": sequence, "state": "confirmed",
		"record": record, "learning": json.RawMessage(learning),
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("encoding pending state: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("writing pending state: %v", err)
	}
}

// TestLearningPayloadCarriesWhatObserveNeeds is the fidelity guard under the
// whole restart design: the learning a sink holds is a serialized Result,
// and a restart applies it by decoding it back.
//
// Every field Engine.Observe reads must survive that round trip — the
// decision that gates eligibility, the baseline key, the fingerprint, and
// the volatile features and timestamp the observation is recorded with. If a
// core type stopped round-tripping, recovery would quietly learn the wrong
// thing, and only a test that compares the two ends would notice.
func TestLearningPayloadCarriesWhatObserveNeeds(t *testing.T) {
	// A Result produced the way the processor produces the one it encodes:
	// the same mapping, the same engine call, the same learning scope.
	td := evaluationTraces("support-agent", "crm.localhost")
	rs := td.ResourceSpans().At(0)
	event := trustvianprocessor.EventFromSpan(
		rs.Resource().Attributes(), rs.ScopeSpans().At(0).Spans().At(0))

	engine := trustvian.NewEngine(trustvian.WithLearningScope("support-reference"))
	original, err := engine.Analyze(context.Background(), event)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal(Result) error = %v", err)
	}
	var restored trustvian.Result
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatalf("Unmarshal(Result) error = %v", err)
	}

	if restored.Decision != original.Decision {
		t.Errorf("decision = %q, want %q — it is what gates learning eligibility",
			restored.Decision, original.Decision)
	}
	if restored.BaselineKey != original.BaselineKey {
		t.Errorf("baseline key = %+v, want %+v — it is which baseline the observation lands in",
			restored.BaselineKey, original.BaselineKey)
	}
	if restored.Fingerprint != original.Fingerprint {
		t.Errorf("fingerprint = %+v, want %+v", restored.Fingerprint, original.Fingerprint)
	}
	if !restored.Features.Volatile.Timestamp.Equal(original.Features.Volatile.Timestamp) ||
		restored.Features.Volatile.Latency != original.Features.Volatile.Latency ||
		restored.Features.Volatile.HasLatency != original.Features.Volatile.HasLatency ||
		restored.Features.Volatile.Error != original.Features.Volatile.Error ||
		restored.Features.Volatile.DelegatedFrom != original.Features.Volatile.DelegatedFrom {
		t.Errorf("volatile features = %+v, want %+v — they are what the observation records",
			restored.Features.Volatile, original.Features.Volatile)
	}
	if !restored.Event.Timestamp.Equal(original.Event.Timestamp) {
		t.Errorf("event timestamp = %v, want %v", restored.Event.Timestamp, original.Event.Timestamp)
	}

	// And the decoded Result actually learns: the same assertion the live
	// path makes on every span, since both go through the same bytes.
	learned, err := engine.Observe(context.Background(), restored)
	if err != nil {
		t.Fatalf("Observe(restored) error = %v", err)
	}
	if !learned {
		t.Error("Observe(restored) learned nothing; a recovered record's learning would be lost")
	}
}

// TestRestartWithPostgresBaseline is the same two restart cases against the
// other durable backend.
//
// It is env-gated the way every PostgreSQL test in this repository is
// (TRUSTVIAN_TEST_POSTGRES_DSN, see docs/storage-guide.md), so the FileStore
// tests above remain the deterministic proof that runs on every push: they
// need no service, and the baseline file can be read back byte for byte,
// which is what lets them assert "exactly one observation" rather than
// inferring it. This one exists because the property must hold for any
// durable store, and nothing in the pending-state design knows which store
// is configured — the sink never touches it. If this ever diverges from the
// FileStore result, the implementation has grown a store-specific path it
// should not have.
//
// The behavioral profile is unique per run because a PostgreSQL baseline
// outlives the test: it is the learning scope (ADR 0024), so a fresh one is
// a fresh baseline without truncating a database the developer owns.
func TestRestartWithPostgresBaseline(t *testing.T) {
	dsn := os.Getenv("TRUSTVIAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TRUSTVIAN_TEST_POSTGRES_DSN not set; see docs/storage-guide.md")
	}

	postgres := map[string]any{
		"version":  "v1",
		"type":     "postgres",
		"postgres": map[string]any{"dsn": dsn, "max_connections": 2},
	}

	// config with a scope nobody else has used, so the baseline starts empty.
	newConfig := func(cp *ingestAPIServer, pending, profile string) *trustvianprocessor.Config {
		required := true
		return &trustvianprocessor.Config{
			Storage: postgres,
			Evaluation: &trustvianprocessor.EvaluationConfig{
				APIURL:            cp.URL,
				RunID:             "run-1",
				BehavioralProfile: profile,
				Required:          &required,
				PendingStatePath:  pending,
			},
		}
	}

	t.Run("unreached record leaves no learning", func(t *testing.T) {
		cp := newIngestAPIServer(t)
		pending := filepath.Join(t.TempDir(), "pending.json")
		profile := fmt.Sprintf("restart-unreached-%d", time.Now().UnixNano())

		first, err := newTestProcessorWithConfig(t, consumertest.NewNop(), newConfig(cp, pending, profile))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := first.ConsumeTraces(cancelled,
			evaluationTraces("support-agent", "crm.localhost")); err == nil {
			t.Fatal("ConsumeTraces() error = nil, want the unresolved ingest surfaced")
		}
		if got := len(cp.recorded()); got != 0 {
			t.Fatalf("control plane holds %d records, want 0", got)
		}

		second, err := newTestProcessorWithConfig(t, consumertest.NewNop(), newConfig(cp, pending, profile))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		if err := second.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost")); err != nil {
			t.Fatalf("ConsumeTraces() error = %v", err)
		}
		records := cp.recorded()
		if len(records) != 1 {
			t.Fatalf("control plane holds %d records, want 1", len(records))
		}
		if records[0].AnomalyConfidence != 0 {
			t.Errorf("anomaly_confidence = %v, want 0 — the durable baseline must not hold a record "+
				"the run never received", records[0].AnomalyConfidence)
		}
	})

	t.Run("committed record is learned exactly once", func(t *testing.T) {
		cp := newIngestAPIServer(t)
		cp.dropAlways.Store(true)
		pending := filepath.Join(t.TempDir(), "pending.json")
		profile := fmt.Sprintf("restart-committed-%d", time.Now().UnixNano())

		first, err := newTestProcessorWithConfig(t, consumertest.NewNop(), newConfig(cp, pending, profile))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		if err := first.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost")); err == nil {
			t.Fatal("ConsumeTraces() error = nil, want the unresolved ingest surfaced")
		}
		if got := len(cp.recorded()); got != 1 {
			t.Fatalf("control plane holds %d records, want 1", got)
		}

		cp.dropAlways.Store(false)
		second, err := newTestProcessorWithConfig(t, consumertest.NewNop(), newConfig(cp, pending, profile))
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		if err := second.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", "crm.localhost")); err != nil {
			t.Fatalf("ConsumeTraces() error = %v", err)
		}
		records := cp.recorded()
		if len(records) != 2 {
			t.Fatalf("control plane holds %d records, want 2 — a replay adds no evidence", len(records))
		}
		if records[1].AnomalyConfidence <= 0 {
			t.Errorf("anomaly_confidence = %v, want > 0 — the run holds the first record, so the "+
				"durable baseline must hold its learning", records[1].AnomalyConfidence)
		}
	})
}
