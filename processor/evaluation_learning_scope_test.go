package trustvianprocessor_test

import (
	"context"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/collector/processor"

	trustvianprocessor "trustvian-processor"
)

// TestEvaluationSelectsLearningScope covers acceptance criterion 8, the
// branch's one security property (task 051 / ADR 0024): configuring
// `evaluation:` selects the Engine's learning scope, so two candidates
// evaluated against the same durable store never train one shared baseline.
//
// The spec's own mutation list names exactly this as a mutant that must not
// survive — "learning scope left unset so two candidates share a baseline" —
// and until this file, nothing in the suite would have caught it: nothing
// else drives WithLearningScope's argument (processor.go) through a shared
// store and reads the resulting behavioral difference back out.
//
// Design (matching the review's own steps):
//
//  1. `storage:` uses the file backend at one shared temp path.
//  2. ~30 identical spans through a processor with behavioral_profile "a"
//     mature that scope's baseline; the last span's trustvian.anomaly.score
//     is captured as "matured".
//  3. A second processor, behavioral_profile "b", over the SAME store, sees
//     one identical span. Its score must be the cold-start value, not
//     "matured" — proving "b" did not inherit "a"'s learned state.
//  4. A third processor, profile "a" again, over the same store, sees one
//     identical span. Its score must reproduce "matured" — the control that
//     proves the store really was shared and step 3 did not pass vacuously
//     (e.g. because the store was never actually being persisted to or read
//     from at all).
//
// The cold-start reference for step 3 is computed independently, from a
// plain processor with no storage configured at all (NewEngine's own default
// in-memory store) — a never-observed fingerprint scores identically no
// matter which learning scope will eventually own it, since a fresh key
// simply is not found. Comparing against that independent value, rather than
// just asserting "b" != "matured", is what stops this test from passing on
// coincidence.
func TestEvaluationSelectsLearningScope(t *testing.T) {
	const target = "crm.localhost"

	// One shared file-backed store for every evaluation-configured
	// processor below. Each processor construction here reads whatever the
	// previous one wrote — that is the point.
	storagePath := filepath.Join(t.TempDir(), "learning-scope.json")
	fileStorage := func() map[string]any {
		return map[string]any{
			"version": "v1",
			"type":    "file",
			"file":    map[string]any{"path": storagePath},
		}
	}

	newScopedProcessor := func(profile string) (*capturingConsumer, processor.Traces) {
		cp := newIngestAPIServer(t)
		required := true
		next := &capturingConsumer{}
		proc, err := newTestProcessorWithConfig(t, next, &trustvianprocessor.Config{
			Storage: fileStorage(),
			Evaluation: &trustvianprocessor.EvaluationConfig{
				APIURL:            cp.URL,
				RunID:             "run-scope",
				BehavioralProfile: profile,
				Required:          &required,
			},
		})
		if err != nil {
			t.Fatalf("CreateTraces() error = %v", err)
		}
		return next, proc
	}

	anomalyScore := func(next *capturingConsumer, idx int) float64 {
		t.Helper()
		span := firstSpan(next.traces[idx])
		v, ok := span.Attributes().Get("trustvian.anomaly.score")
		if !ok {
			t.Fatal("enriched span missing trustvian.anomaly.score")
		}
		return v.Double()
	}

	// Step 1/2: mature profile "a"'s baseline over ~30 identical spans.
	nextA, procA := newScopedProcessor("a")
	for range 30 {
		if err := procA.ConsumeTraces(context.Background(),
			evaluationTraces("support-agent", target)); err != nil {
			t.Fatalf("ConsumeTraces() error = %v", err)
		}
	}
	matured := anomalyScore(nextA, nextA.len()-1)

	// The independent cold-start reference: a plain processor with no
	// storage configured at all, so its baseline is guaranteed fresh —
	// this is what "cold start" means, independent of any scope name.
	coldNext := &capturingConsumer{}
	coldProc := newTestProcessor(t, coldNext)
	if err := coldProc.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", target)); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	coldStart := anomalyScore(coldNext, 0)

	if matured == coldStart {
		t.Fatalf("matured score %v equals the cold-start reference %v; 30 observations did not "+
			"change anything, so this test cannot distinguish a shared from an isolated baseline",
			matured, coldStart)
	}

	// Step 3: profile "b", same shared store, one span. Must be cold, not
	// matured — proving "b" did not inherit "a"'s learned state.
	nextB, procB := newScopedProcessor("b")
	if err := procB.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", target)); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	fresh := anomalyScore(nextB, 0)
	if fresh != coldStart {
		t.Errorf("profile b's score = %v, want the cold-start value %v — "+
			"two candidates over one store are sharing a baseline", fresh, coldStart)
	}

	// Step 4, the control: profile "a" again, same shared store, one span.
	// Must reproduce "matured" — proving the store really was shared and
	// step 3 above did not pass vacuously.
	nextAAgain, procAAgain := newScopedProcessor("a")
	if err := procAAgain.ConsumeTraces(context.Background(),
		evaluationTraces("support-agent", target)); err != nil {
		t.Fatalf("ConsumeTraces() error = %v", err)
	}
	confirmed := anomalyScore(nextAAgain, 0)
	if confirmed != matured {
		t.Errorf("profile a's score on a fresh processor = %v, want the matured value %v — "+
			"the shared store was not actually read, so step 3's pass proves nothing", confirmed, matured)
	}
}
