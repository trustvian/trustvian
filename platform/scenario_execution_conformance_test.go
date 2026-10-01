package platform

// Scenario execution storage, proven identically on every backend (schema v9).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var scenarioEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func scenarioScope(project string) ScenarioScope {
	return ScenarioScope{ProjectID: ProjectID(project), AgentID: "agent-a", Environment: "staging"}
}

func newRunningExecution(
	t testing.TB, id, name string, scope ScenarioScope, runs int, reference string, at time.Time,
) ScenarioExecution {
	t.Helper()
	e, err := NewScenarioExecution(ScenarioExecutionID(id), name, scope, runs,
		ScenarioExecutionID(reference), at)
	if err != nil {
		t.Fatalf("NewScenarioExecution(%s) error = %v", id, err)
	}
	return e
}

// associations is N reference and N candidate repetitions with distinct runs
// and profiles derived from prefix.
func associations(prefix string, runs int) []ScenarioRepetition {
	var out []ScenarioRepetition
	for _, side := range []ComparisonSide{SideReference, SideCandidate} {
		for i := 1; i <= runs; i++ {
			out = append(out, ScenarioRepetition{
				Side: side, Index: i,
				RunID:             EvaluationRunID(fmt.Sprintf("%s-%s-%d", prefix, side, i)),
				BehavioralProfile: BehavioralProfileRef(fmt.Sprintf("%s-%s-p%d", prefix, side, i)),
			})
		}
	}
	return out
}

func conformScenarioExecutions(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-2")
	scope := scenarioScope("proj-1")

	first := newRunningExecution(t, "exec-1", "support", scope, 2, "", scenarioEpoch)
	if err := store.CreateScenarioExecution(ctx, first); err != nil {
		t.Fatalf("CreateScenarioExecution() error = %v", err)
	}
	// Create means create: the same identity with other data changes nothing.
	other := newRunningExecution(t, "exec-1", "other", scope, 3, "", scenarioEpoch.Add(time.Hour))
	if err := store.CreateScenarioExecution(ctx, other); !errors.Is(err, ErrStoreAlreadyExists) {
		t.Fatalf("duplicate create error = %v, want ErrStoreAlreadyExists", err)
	}
	loaded, err := store.ScenarioExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("ScenarioExecution() error = %v", err)
	}
	if loaded.ScenarioName() != "support" || loaded.Runs() != 2 ||
		loaded.Status() != ScenarioExecutionRunning || len(loaded.Repetitions()) != 0 ||
		loaded.CompletionSequence() != 0 || !loaded.StartedAt().Equal(scenarioEpoch) {
		t.Fatalf("stored running execution = %+v", loaded)
	}

	// A running execution is never a reference.
	if _, err := store.LatestCompletedScenarioExecution(ctx, "support", scope); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("latest with only a running execution: error = %v, want ErrStoreNotFound", err)
	}

	// A completed FAIL is a completed execution.
	finished := scenarioEpoch.Add(time.Minute)
	completed, err := store.CompleteScenarioExecution(ctx, "exec-1", associations("e1", 2),
		GateVerdictFail, finished)
	if err != nil {
		t.Fatalf("CompleteScenarioExecution() error = %v", err)
	}
	if completed.Status() != ScenarioExecutionCompleted || completed.CompletionSequence() != 1 ||
		completed.Verdict() != GateVerdictFail {
		t.Fatalf("completed = %+v", completed)
	}
	reloaded, err := store.ScenarioExecution(ctx, "exec-1")
	if err != nil {
		t.Fatalf("reload error = %v", err)
	}
	if fmt.Sprint(reloaded) != fmt.Sprint(completed) {
		t.Fatalf("reloaded %+v\nwant     %+v", reloaded, completed)
	}
	latest, err := store.LatestCompletedScenarioExecution(ctx, "support", scope)
	if err != nil || latest.ID() != "exec-1" {
		t.Fatalf("latest = %v, %v; want exec-1", latest.ID(), err)
	}

	// No second completion, and no second set of associations.
	if _, err := store.CompleteScenarioExecution(ctx, "exec-1", associations("again", 2),
		GateVerdictPass, finished); !errors.Is(err, ErrScenarioExecutionState) {
		t.Fatalf("second completion error = %v, want ErrScenarioExecutionState", err)
	}
	if _, err := store.FailScenarioExecution(ctx, "exec-1", finished); !errors.Is(err, ErrScenarioExecutionState) {
		t.Fatalf("failing a completed execution: error = %v, want ErrScenarioExecutionState", err)
	}
	if again, _ := store.ScenarioExecution(ctx, "exec-1"); fmt.Sprint(again) != fmt.Sprint(completed) {
		t.Fatalf("a refused transition changed the execution: %+v", again)
	}

	// Failing is idempotent; a failed execution never completes.
	failedOne := newRunningExecution(t, "exec-failed", "support", scope, 2, "", scenarioEpoch)
	if err := store.CreateScenarioExecution(ctx, failedOne); err != nil {
		t.Fatal(err)
	}
	failed, err := store.FailScenarioExecution(ctx, "exec-failed", finished)
	if err != nil || failed.Status() != ScenarioExecutionFailed {
		t.Fatalf("fail = %+v, %v", failed, err)
	}
	if retried, err := store.FailScenarioExecution(ctx, "exec-failed", finished.Add(time.Hour)); err != nil ||
		!retried.FinishedAt().Equal(finished) {
		t.Fatalf("retried fail = %+v, %v; want the stored failure unchanged", retried, err)
	}
	if _, err := store.CompleteScenarioExecution(ctx, "exec-failed", associations("f", 2),
		GateVerdictPass, finished); !errors.Is(err, ErrScenarioExecutionState) {
		t.Fatalf("completing a failed execution: error = %v", err)
	}

	// A completion the domain refuses writes nothing.
	malformed := newRunningExecution(t, "exec-malformed", "support", scope, 2, "", scenarioEpoch)
	if err := store.CreateScenarioExecution(ctx, malformed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteScenarioExecution(ctx, "exec-malformed", associations("m", 1),
		GateVerdictPass, finished); err == nil {
		t.Fatal("a completion with N = 1 associations for runs 2 was accepted")
	}
	if still, _ := store.ScenarioExecution(ctx, "exec-malformed"); still.Status() != ScenarioExecutionRunning {
		t.Fatalf("a refused completion moved the execution to %s", still.Status())
	}

	if _, err := store.CompleteScenarioExecution(ctx, "no-such", associations("n", 2),
		GateVerdictPass, finished); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("completing a missing execution: error = %v, want ErrStoreNotFound", err)
	}
	if _, err := store.ScenarioExecution(ctx, "no-such"); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("loading a missing execution: error = %v", err)
	}
}

// `last` is the completion order, and nothing else: not the identifier's byte
// order, not the start time, and not the RFC3339Nano text — which here sorts
// the other way, because the later instant is written in an earlier-sorting
// offset.
func conformScenarioExecutionOrdering(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	seedProject(t, store, "proj-2")
	scope := scenarioScope("proj-1")

	west := time.FixedZone("UTC-12", -12*60*60)
	// "z-first" completes first and starts last; its finish text sorts after
	// "a-second"'s although it is the earlier instant.
	starts := map[string]time.Time{
		"z-first":  scenarioEpoch.Add(2 * time.Hour),
		"a-second": scenarioEpoch,
	}
	finishes := map[string]time.Time{
		"z-first":  scenarioEpoch.Add(3 * time.Hour).In(time.UTC),
		"a-second": scenarioEpoch.Add(4 * time.Hour).In(west),
	}
	if timeText(finishes["a-second"]) > timeText(finishes["z-first"]) {
		t.Fatal("fixture: the later finish must sort earlier as text")
	}
	for _, id := range []string{"z-first", "a-second"} {
		if err := store.CreateScenarioExecution(ctx,
			newRunningExecution(t, id, "support", scope, 1, "", starts[id])); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"z-first", "a-second"} {
		if _, err := store.CompleteScenarioExecution(ctx, ScenarioExecutionID(id),
			associations(id, 1), GateVerdictPass, finishes[id]); err != nil {
			t.Fatalf("complete %s: %v", id, err)
		}
	}
	latest, err := store.LatestCompletedScenarioExecution(ctx, "support", scope)
	if err != nil || latest.ID() != "a-second" || latest.CompletionSequence() != 2 {
		t.Fatalf("latest = %s (sequence %d), %v; want a-second, the later completion",
			latest.ID(), latest.CompletionSequence(), err)
	}

	// Scope: a newer completion of the same scenario anywhere else is not
	// this scope's last, and the sequence is per project.
	for _, c := range []struct {
		id    string
		name  string
		scope ScenarioScope
	}{
		{"other-name", "billing", scope},
		{"other-agent", "support", ScenarioScope{ProjectID: "proj-1", AgentID: "agent-b", Environment: "staging"}},
		{"other-env", "support", ScenarioScope{ProjectID: "proj-1", AgentID: "agent-a", Environment: "production"}},
		{"other-project", "support", scenarioScope("proj-2")},
	} {
		if err := store.CreateScenarioExecution(ctx,
			newRunningExecution(t, c.id, c.name, c.scope, 1, "", scenarioEpoch)); err != nil {
			t.Fatal(err)
		}
		done, err := store.CompleteScenarioExecution(ctx, ScenarioExecutionID(c.id),
			associations(c.id, 1), GateVerdictPass, scenarioEpoch.Add(10*time.Hour))
		if err != nil {
			t.Fatalf("complete %s: %v", c.id, err)
		}
		if c.id == "other-project" && done.CompletionSequence() != 1 {
			t.Errorf("proj-2's first completion has sequence %d, want 1", done.CompletionSequence())
		}
	}
	if latest, err := store.LatestCompletedScenarioExecution(ctx, "support", scope); err != nil ||
		latest.ID() != "a-second" {
		t.Fatalf("latest after out-of-scope completions = %s, %v; want a-second", latest.ID(), err)
	}
}

// Concurrent completions: within one project each gets its own sequence, and
// two completions of one execution produce exactly one.
func conformScenarioExecutionContention(t *testing.T, open func(testing.TB) Store) {
	ctx := context.Background()
	store := open(t)
	seedProject(t, store, "proj-1")
	scope := scenarioScope("proj-1")

	const executions = 6
	for i := 0; i < executions; i++ {
		if err := store.CreateScenarioExecution(ctx, newRunningExecution(t,
			fmt.Sprintf("exec-%d", i), "support", scope, 1, "", scenarioEpoch)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make([]ScenarioExecution, executions)
	errs := make([]error, executions)
	for i := 0; i < executions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("exec-%d", i)
			results[i], errs[i] = store.CompleteScenarioExecution(ctx, ScenarioExecutionID(id),
				associations(id, 1), GateVerdictPass, scenarioEpoch.Add(time.Minute))
		}(i)
	}
	wg.Wait()
	sequences := map[uint64]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent completion %d: %v", i, err)
		}
		sequences[results[i].CompletionSequence()] = true
	}
	for want := uint64(1); want <= executions; want++ {
		if !sequences[want] {
			t.Errorf("sequences %v are not exactly 1..%d", sequences, executions)
			break
		}
	}

	if err := store.CreateScenarioExecution(ctx, newRunningExecution(t,
		"contended", "support", scope, 1, "", scenarioEpoch)); err != nil {
		t.Fatal(err)
	}
	var won, lost int
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.CompleteScenarioExecution(ctx, "contended",
				associations(fmt.Sprintf("c%d", i), 1), GateVerdictPass, scenarioEpoch.Add(time.Minute))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrScenarioExecutionState), errors.Is(err, ErrStoreConflict):
				lost++
			default:
				t.Errorf("racing completion: unexpected error %v", err)
			}
		}(i)
	}
	wg.Wait()
	if won != 1 || lost != 3 {
		t.Fatalf("racing completions: %d won and %d lost, want 1 and 3", won, lost)
	}
	stored, err := store.ScenarioExecution(ctx, "contended")
	if err != nil || len(stored.Repetitions()) != 2 {
		t.Fatalf("contended execution = %+v, %v; want exactly one set of associations", stored, err)
	}
}
