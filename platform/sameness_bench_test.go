package platform

// The lookup sameness adds to compare-repeated, at its bound: 64 repetitions
// a side, every run recorded by one execution.

import (
	"context"
	"testing"
	"time"
)

func BenchmarkRunProvenanceAtTheBound(b *testing.B) {
	store, err := OpenSQLiteStore(context.Background(), b.TempDir()+"/bench.db")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	seedProject(b, store, "proj-1")
	e, err := newRunningExecution(b, "exec-bench", "support", scenarioScope("proj-1"), MaxRepetitions, "",
		scenarioEpoch).withProvenance(fullProvenance("llama3.2"), fullProvenance("gemma3:4b"))
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	if err := store.CreateScenarioExecution(ctx, e); err != nil {
		b.Fatal(err)
	}
	reps := associations("exec-bench", MaxRepetitions)
	if _, err := store.CompleteScenarioExecution(ctx, e.ID(), reps, GateVerdictPass,
		scenarioEpoch.Add(time.Minute)); err != nil {
		b.Fatal(err)
	}
	ids := make([]EvaluationRunID, 0, len(reps))
	for _, r := range reps {
		ids = append(ids, r.RunID)
	}
	b.ReportAllocs()
	for b.Loop() {
		recorded, complete, err := store.RunProvenance(ctx, ids)
		if err != nil || !complete || len(recorded) != len(ids) {
			b.Fatalf("RunProvenance = %d runs, %v, %v", len(recorded), complete, err)
		}
	}
}
