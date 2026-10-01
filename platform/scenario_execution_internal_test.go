package platform

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// saturatingEvaluations serves one run's evidence as a saturated snapshot —
// what a run that observed more than 512 distinct behaviors stores — without
// ingesting 513 records.
type saturatingEvaluations struct {
	EvaluationStore
	run       EvaluationRunID
	aggregate EvaluationAggregate
	snapshot  BehaviorSnapshot
}

func (s saturatingEvaluations) EvaluationEvidence(
	ctx context.Context, id EvaluationRunID,
) (EvaluationAggregate, BehaviorSnapshot, error) {
	if id == s.run {
		return s.aggregate, s.snapshot, nil
	}
	return s.EvaluationStore.EvaluationEvidence(ctx, id)
}

// A recorded reference whose evidence saturated is refused when it is
// resolved — before any candidate runs — and not compared from a prefix.
func TestAReferenceWithIncompleteEvidenceIsRefusedAtResolution(t *testing.T) {
	ctx := context.Background()
	store, err := OpenSQLiteStore(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	project, _ := NewProject("proj-1", "P")
	agent, _ := NewAgent("agent-1", "proj-1", "A")
	candidate, _ := NewCandidate("cand-1", "agent-1", CandidateMetadata{Label: "v1"})
	environment, _ := NewEnvironment("staging", "proj-1", "Staging")
	for _, err := range []error{store.CreateProject(ctx, project), store.CreateAgent(ctx, agent),
		store.CreateCandidate(ctx, candidate), store.CreateEnvironment(ctx, environment)} {
		if err != nil {
			t.Fatal(err)
		}
	}

	run, _ := NewEvaluationRun("ref-sat", "cand-1", "staging", "ref-sat-p", scenarioEpoch)
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatal(err)
	}
	for i := range maxBehaviorEntries + 1 {
		_ = collector.Observe(internalRecord(fmt.Sprintf("e%d", i), fmt.Sprintf("fp-%d", i), fmt.Sprintf("op%d", i)))
	}
	if collector.Snapshot().Complete() {
		t.Fatal("the collector did not saturate; this test would pass vacuously")
	}
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatal(err)
	}
	evaluations := saturatingEvaluations{EvaluationStore: store, run: run.ID(),
		aggregate: aggregate, snapshot: collector.Snapshot()}
	plane, err := NewControlPlane(store, evaluations, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.StartEvaluationRun(ctx, run.ID(), scenarioEpoch.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := plane.CompleteEvaluationRun(ctx, run.ID(), scenarioEpoch.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: "staging"}
	if err := store.CreateScenarioExecution(ctx,
		newRunningExecution(t, "e-sat", "support", scope, 1, "", scenarioEpoch)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteScenarioExecution(ctx, "e-sat", []ScenarioRepetition{
		{Side: SideReference, Index: 1, RunID: run.ID(), BehavioralProfile: run.BehavioralProfile()},
		{Side: SideCandidate, Index: 1, RunID: "cand-run", BehavioralProfile: "cand-p"},
	}, GateVerdictFail, scenarioEpoch.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	for name, reference := range map[string]ScenarioReference{
		"explicit": {ExecutionID: "e-sat"},
		"last":     {Last: true},
	} {
		_, _, err := plane.BeginScenarioExecution(ctx, BeginScenarioExecutionRequest{
			ID: ScenarioExecutionID("e-" + name), ScenarioName: "support", Runs: 1, Scope: scope,
			Reference: reference, At: scenarioEpoch.Add(2 * time.Hour),
		})
		if !errors.Is(err, ErrIncompleteSnapshot) {
			t.Errorf("%s: error = %v, want ErrIncompleteSnapshot", name, err)
		}
	}
}

// A stored execution that a live one could not be is corrupt, whatever field
// is wrong.
func TestRestoredScenarioExecutionRefusesImpossibleShapes(t *testing.T) {
	scope := ScenarioScope{ProjectID: "p", AgentID: "a", Environment: "staging"}
	base := func() ScenarioExecution {
		e, err := NewScenarioExecution("e", "support", scope, 2, "", scenarioEpoch)
		if err != nil {
			t.Fatal(err)
		}
		done, err := e.complete(associations("x", 2), GateVerdictPass, 1, scenarioEpoch.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return done
	}
	if _, err := restoreScenarioExecution(base()); err != nil {
		t.Fatalf("a well-formed execution was refused: %v", err)
	}
	for name, mutate := range map[string]func(*ScenarioExecution){
		"unknown status":         func(e *ScenarioExecution) { e.status = "paused" },
		"completed, no sequence": func(e *ScenarioExecution) { e.completionSequence = 0 },
		"completed, no verdict":  func(e *ScenarioExecution) { e.verdict = "" },
		"unknown verdict":        func(e *ScenarioExecution) { e.verdict = "maybe" },
		"truncated associations": func(e *ScenarioExecution) { e.repetitions = e.repetitions[:3] },
		"out-of-order index":     func(e *ScenarioExecution) { e.repetitions[0].Index = 2 },
		"candidate first":        func(e *ScenarioExecution) { e.repetitions[0].Side = SideCandidate },
		"shared profile": func(e *ScenarioExecution) {
			e.repetitions[3].BehavioralProfile = e.repetitions[0].BehavioralProfile
		},
		"running with associations": func(e *ScenarioExecution) {
			e.status, e.finishedAt = ScenarioExecutionRunning, time.Time{}
		},
		"finished before started": func(e *ScenarioExecution) { e.finishedAt = scenarioEpoch.Add(-time.Hour) },
		"runs out of range":       func(e *ScenarioExecution) { e.runs = MaxRepetitions + 1 },
		"self reference":          func(e *ScenarioExecution) { e.reference = e.id },
	} {
		t.Run(name, func(t *testing.T) {
			e := base()
			e.repetitions = append([]ScenarioRepetition(nil), e.repetitions...)
			mutate(&e)
			if _, err := restoreScenarioExecution(e); !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}
