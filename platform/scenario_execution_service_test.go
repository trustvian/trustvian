package platform_test

// Persisted scenario executions and recorded-reference reuse, through the
// whole service over a real SQLite store (task 078, ADR 0054).

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	platform "trustvian-platform"
)

var execScope = platform.ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: fixtureEnvironment}

var execClock = aggEpoch.Add(2 * time.Hour)

func (f *controlPlaneFixture) begin(t *testing.T, id, name string, runs int,
	reference platform.ScenarioReference) (platform.ScenarioExecution, platform.ScenarioExecution, error) {
	t.Helper()
	f.seedRepeatedHierarchy(t)
	return f.plane.BeginScenarioExecution(t.Context(), platform.BeginScenarioExecutionRequest{
		ID: platform.ScenarioExecutionID(id), ScenarioName: name, Runs: runs,
		Scope: execScope, Reference: reference, At: execClock,
	})
}

// sideRuns executes one side's repetitions under the runner's naming: run
// <exec>-<side>-<i>, profile <exec>-<side>-p<i>.
func (f *controlPlaneFixture) sideRuns(t *testing.T, exec, side string, reps []repetition) []platform.EvaluationRunID {
	t.Helper()
	var ids []platform.EvaluationRunID
	for i, rep := range reps {
		id := fmt.Sprintf("%s-%s-%d", exec, side, i+1)
		f.runRepetition(t, id, fmt.Sprintf("%s-%s-p%d", exec, side, i+1), rep, true)
		ids = append(ids, platform.EvaluationRunID(id))
	}
	return ids
}

// selfContained runs a whole default-mode execution and completes it.
func (f *controlPlaneFixture) selfContained(t *testing.T, id, name string,
	reference, candidate []repetition, limits platform.RepeatedEvaluationGateLimits,
) (platform.ScenarioExecution, platform.RepeatedEvaluationComparison) {
	t.Helper()
	if _, _, err := f.begin(t, id, name, len(reference), platform.ScenarioReference{}); err != nil {
		t.Fatalf("begin %s: %v", id, err)
	}
	refs := f.sideRuns(t, id, "reference", reference)
	cands := f.sideRuns(t, id, "candidate", candidate)
	execution, comparison, err := f.plane.CompleteScenarioExecution(t.Context(),
		platform.CompleteScenarioExecutionRequest{ID: platform.ScenarioExecutionID(id),
			ReferenceRunIDs: refs, CandidateRunIDs: cands, Limits: limits, At: execClock.Add(time.Minute)})
	if err != nil {
		t.Fatalf("complete %s: %v", id, err)
	}
	return execution, comparison
}

// recorded runs a recorded-reference execution: candidates only.
func (f *controlPlaneFixture) recorded(t *testing.T, id, name string, reference platform.ScenarioReference,
	candidate []repetition, limits platform.RepeatedEvaluationGateLimits,
) (platform.ScenarioExecution, platform.RepeatedEvaluationComparison, error) {
	t.Helper()
	if _, _, err := f.begin(t, id, name, len(candidate), reference); err != nil {
		return platform.ScenarioExecution{}, platform.RepeatedEvaluationComparison{}, err
	}
	cands := f.sideRuns(t, id, "candidate", candidate)
	return f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: platform.ScenarioExecutionID(id), CandidateRunIDs: cands, Limits: limits,
		At: execClock.Add(time.Minute)})
}

func runIDsOf(reps []platform.ScenarioRepetition) []platform.EvaluationRunID {
	var out []platform.EvaluationRunID
	for _, r := range reps {
		out = append(out, r.RunID)
	}
	return out
}

func reps(n int, behaviors ...string) []repetition {
	out := make([]repetition, n)
	for i := range out {
		out[i] = repetition{behaviors: behaviors}
	}
	return out
}

var strict = repeatedLimits(1, 0, 0, 0, 0)

// The recorded reference side is reused whole — its reference runs, never its
// candidate runs — and only N new candidate runs exist. A completed gate FAIL
// is a usable reference: completion is not PASS.
func TestRecordedReferenceReusesTheReferenceSideOfACompletedFail(t *testing.T) {
	f := newFixture(t)
	first, firstCmp := f.selfContained(t, "e1", "support",
		reps(2, "read"), reps(2, "read", "export"), strict)
	if firstCmp.Gate.Verdict() != platform.GateVerdictFail || first.Status() != platform.ScenarioExecutionCompleted {
		t.Fatalf("e1 = %s %s; want a completed FAIL", first.Status(), firstCmp.Gate.Verdict())
	}

	second, cmp, err := f.recorded(t, "e2", "support",
		platform.ScenarioReference{ExecutionID: "e1"}, reps(2, "read"), strict)
	if err != nil {
		t.Fatalf("recorded execution: %v", err)
	}
	wantRefs := runIDsOf(first.SideRepetitions(platform.SideReference))
	if got := runIDsOf(second.SideRepetitions(platform.SideReference)); !slices.Equal(got, wantRefs) {
		t.Errorf("reference side = %v, want e1's reference runs %v", got, wantRefs)
	}
	for _, r := range second.SideRepetitions(platform.SideCandidate) {
		if slices.Contains(runIDsOf(first.Repetitions()), r.RunID) {
			t.Errorf("candidate run %s was reused from e1", r.RunID)
		}
	}
	if second.ReferenceExecution() != "e1" || cmp.Gate.Verdict() != platform.GateVerdictPass {
		t.Errorf("e2 reference %q verdict %s; want e1 and pass", second.ReferenceExecution(), cmp.Gate.Verdict())
	}
	if p := presenceOf(cmp, "export"); p.FingerprintID != "" {
		t.Errorf("e1's candidate behavior leaked into the reused reference: %+v", p)
	}
}

// `last` is the most recently completed execution in scope: not a running
// one, not a failed one, not another scenario's, agent's or environment's.
func TestLastSelectsTheMostRecentCompletedExecutionInScope(t *testing.T) {
	f := newFixture(t)
	f.selfContained(t, "e-old", "support", reps(1, "read"), reps(1, "read"), strict)
	newest, _ := f.selfContained(t, "e-new", "support", reps(1, "search"), reps(1, "search"), strict)
	f.selfContained(t, "e-other-name", "billing", reps(1, "read"), reps(1, "read"), strict)

	if _, _, err := f.begin(t, "e-running", "support", 1, platform.ScenarioReference{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.begin(t, "e-failed", "support", 1, platform.ScenarioReference{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.FailScenarioExecution(t.Context(), "e-failed", execClock); err != nil {
		t.Fatal(err)
	}

	execution, reference, err := f.begin(t, "e-last", "support", 1, platform.ScenarioReference{Last: true})
	if err != nil {
		t.Fatalf("begin with last: %v", err)
	}
	if reference.ID() != "e-new" || execution.ReferenceExecution() != "e-new" {
		t.Fatalf("last resolved to %s; want e-new, the most recent completed", reference.ID())
	}
	cands := f.sideRuns(t, "e-last", "candidate", reps(1, "search"))
	done, cmp, err := f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: "e-last", CandidateRunIDs: cands, Limits: strict, At: execClock.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(runIDsOf(done.SideRepetitions(platform.SideReference)),
		runIDsOf(newest.SideRepetitions(platform.SideReference))) || cmp.Gate.Verdict() != platform.GateVerdictPass {
		t.Errorf("e-last compared against %v (%s)", runIDsOf(done.Repetitions()), cmp.Gate.Verdict())
	}

	// Unfinished executions are never references, explicitly named or not.
	for _, id := range []platform.ScenarioExecutionID{"e-running", "e-failed"} {
		_, _, err := f.begin(t, "explicit-"+string(id), "support", 1, platform.ScenarioReference{ExecutionID: id})
		if !errors.Is(err, platform.ErrScenarioReference) {
			t.Errorf("explicit reference to %s: error = %v, want ErrScenarioReference", id, err)
		}
	}
}

// The latest execution is the reference or nothing is. When it is unusable the
// error names it, and the older usable one is never substituted.
func TestLastNeverFallsBackToAnOlderExecution(t *testing.T) {
	t.Run("mismatched runs", func(t *testing.T) {
		f := newFixture(t)
		f.selfContained(t, "e-two", "support", reps(2, "read"), reps(2, "read"), strict)
		f.selfContained(t, "e-three", "support", reps(3, "read"), reps(3, "read"), strict)
		_, _, err := f.begin(t, "e-now", "support", 2, platform.ScenarioReference{Last: true})
		if !errors.Is(err, platform.ErrScenarioReference) || !containsAll(err, "e-three", "runs 3") {
			t.Fatalf("error = %v; want the latest (e-three) refused for its N, not e-two used", err)
		}
		if _, err := f.plane.ScenarioExecution(t.Context(), "e-now"); !errors.Is(err, platform.ErrStoreNotFound) {
			t.Errorf("a refused begin recorded an execution: %v", err)
		}
	})

	t.Run("associations that no longer match their runs", func(t *testing.T) {
		f := newFixture(t)
		f.selfContained(t, "e-good", "support", reps(1, "read"), reps(1, "read"), strict)
		// A later completed execution whose recorded reference run never
		// existed — the shape a damaged or hand-edited history would have.
		crafted, err := platform.NewScenarioExecution("e-broken", "support", execScope, 1, "", execClock)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.CreateScenarioExecution(t.Context(), crafted); err != nil {
			t.Fatal(err)
		}
		if _, err := f.store.CompleteScenarioExecution(t.Context(), "e-broken", []platform.ScenarioRepetition{
			{Side: platform.SideReference, Index: 1, RunID: "ghost-run", BehavioralProfile: "ghost-p"},
			{Side: platform.SideCandidate, Index: 1, RunID: "ghost-run-2", BehavioralProfile: "ghost-p2"},
		}, platform.GateVerdictPass, execClock.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		_, _, err = f.begin(t, "e-now", "support", 1, platform.ScenarioReference{Last: true})
		if !errors.Is(err, platform.ErrScenarioReference) || !containsAll(err, "e-broken", "ghost-run") {
			t.Fatalf("error = %v; want e-broken refused by name, not e-good used", err)
		}
	})
}

// Missing references are not found, in every form.
func TestMissingReferencesAreNotFound(t *testing.T) {
	f := newFixture(t)
	if _, _, err := f.begin(t, "e1", "support", 1,
		platform.ScenarioReference{ExecutionID: "no-such"}); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("explicit missing reference: error = %v, want ErrStoreNotFound", err)
	}
	if _, _, err := f.begin(t, "e2", "support", 1,
		platform.ScenarioReference{Last: true}); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("last with no history: error = %v, want ErrStoreNotFound", err)
	}
	f.selfContained(t, "e-billing", "billing", reps(1, "read"), reps(1, "read"), strict)
	if _, _, err := f.begin(t, "e3", "support", 1,
		platform.ScenarioReference{Last: true}); !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("last with only another scenario's history: error = %v, want ErrStoreNotFound", err)
	}
	if _, _, err := f.begin(t, "e4", "support", 1,
		platform.ScenarioReference{Last: true, ExecutionID: "e-billing"}); !errors.Is(err, platform.ErrInvalidRepeatedRequest) {
		t.Errorf("last and an id together: error = %v", err)
	}
}

// An explicit reference may name another scenario — the caller chose it — but
// not another environment, which the comparison itself could not span.
func TestExplicitReferenceCompatibility(t *testing.T) {
	f := newFixture(t)
	f.selfContained(t, "e-old-name", "support", reps(1, "read"), reps(1, "read"), strict)
	if _, _, err := f.recorded(t, "e-renamed", "support-v2",
		platform.ScenarioReference{ExecutionID: "e-old-name"}, reps(1, "read"), strict); err != nil {
		t.Errorf("explicit reference across a scenario rename: %v", err)
	}

	f.seedRepeatedHierarchy(t)
	production := execScope
	production.Environment = "production"
	_, _, err := f.plane.BeginScenarioExecution(t.Context(), platform.BeginScenarioExecutionRequest{
		ID: "e-prod", ScenarioName: "support", Runs: 1, Scope: production,
		Reference: platform.ScenarioReference{ExecutionID: "e-old-name"}, At: execClock,
	})
	if !errors.Is(err, platform.ErrScenarioReference) {
		t.Errorf("explicit reference from another environment: error = %v, want ErrScenarioReference", err)
	}
}

// The current scenario's limits decide, whatever the recorded execution used.
func TestCurrentLimitsApplyToARecordedReference(t *testing.T) {
	f := newFixture(t)
	lenient := repeatedLimits(1, 0, 5, 0, 0)
	_, first := f.selfContained(t, "e-lenient", "support", reps(1, "read"), reps(1, "read", "export"), lenient)
	if first.Gate.Verdict() != platform.GateVerdictPass {
		t.Fatalf("e-lenient verdict %s, want pass under a budget of 5", first.Gate.Verdict())
	}
	_, cmp, err := f.recorded(t, "e-strict", "support",
		platform.ScenarioReference{Last: true}, reps(1, "read", "export"), strict)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Gate.Verdict() != platform.GateVerdictFail || !reflect.DeepEqual(cmp.Limits, strict) {
		t.Errorf("verdict %s under limits %+v; want fail under the current strict limits", cmp.Gate.Verdict(), cmp.Limits)
	}

	_, again, err := f.recorded(t, "e-lenient-again", "support",
		platform.ScenarioReference{ExecutionID: "e-lenient"}, reps(1, "read", "export"), lenient)
	if err != nil || again.Gate.Verdict() != platform.GateVerdictPass {
		t.Errorf("reusing under lenient limits: %s, %v; want pass", again.Gate.Verdict(), err)
	}
}

// A completed zero-record reference repetition is evidence of nothing, which
// is check 3's finding, not a missing reference.
func TestAZeroRecordReferenceFailsMinimumEvidenceNotResolution(t *testing.T) {
	f := newFixture(t)
	f.selfContained(t, "e-empty", "support",
		[]repetition{{behaviors: []string{"read"}}, {}}, reps(2, "read"), strict)
	_, cmp, err := f.recorded(t, "e-now", "support",
		platform.ScenarioReference{Last: true}, reps(2, "read"), strict)
	if err != nil {
		t.Fatalf("a zero-record reference was refused at resolution: %v", err)
	}
	if c := checkNamed(cmp, platform.CheckRepetitionsFailingMinimum); c.Actual != 1 || c.Passed {
		t.Errorf("minimum-evidence check = %+v; want 1 failing repetition", c)
	}
}

// Every rule the comparison holds still holds: a candidate profile that
// collides with a reused reference profile is refused, and a candidate run
// outside the execution's scope is refused.
func TestRecordedReferencePreservesComparisonRules(t *testing.T) {
	f := newFixture(t)
	first, _ := f.selfContained(t, "e1", "support", reps(1, "read"), reps(1, "read"), strict)
	reused := first.SideRepetitions(platform.SideReference)[0]

	if _, _, err := f.begin(t, "e-collide", "support", 1, platform.ScenarioReference{ExecutionID: "e1"}); err != nil {
		t.Fatal(err)
	}
	f.runRepetition(t, "e-collide-candidate-1", string(reused.BehavioralProfile), repetition{behaviors: []string{"read"}}, true)
	_, _, err := f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: "e-collide", CandidateRunIDs: []platform.EvaluationRunID{"e-collide-candidate-1"},
		Limits: strict, At: execClock})
	if !errors.Is(err, platform.ErrRepeatedIsolation) {
		t.Errorf("candidate sharing a reused reference profile: error = %v, want ErrRepeatedIsolation", err)
	}
	if e, _ := f.plane.ScenarioExecution(t.Context(), "e-collide"); e.Status() != platform.ScenarioExecutionRunning {
		t.Errorf("a refused completion left the execution %s, want running", e.Status())
	}

	// Reference runs from the client are refused when the server supplies them.
	if _, _, err := f.begin(t, "e-mix", "support", 1, platform.ScenarioReference{ExecutionID: "e1"}); err != nil {
		t.Fatal(err)
	}
	cands := f.sideRuns(t, "e-mix", "candidate", reps(1, "read"))
	_, _, err = f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: "e-mix", ReferenceRunIDs: []platform.EvaluationRunID{"e1-candidate-1"}, CandidateRunIDs: cands,
		Limits: strict, At: execClock})
	if !errors.Is(err, platform.ErrInvalidRepeatedRequest) {
		t.Errorf("client-supplied reference runs: error = %v, want ErrInvalidRepeatedRequest", err)
	}

	// A candidate run in another environment is outside the scope.
	if _, _, err := f.begin(t, "e-scope", "support", 1, platform.ScenarioReference{}); err != nil {
		t.Fatal(err)
	}
	refs := f.sideRuns(t, "e-scope", "reference", reps(1, "read"))
	f.runRepetition(t, "e-scope-candidate-1", "e-scope-candidate-p1",
		repetition{behaviors: []string{"read"}, environment: "production"}, true)
	_, _, err = f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: "e-scope", ReferenceRunIDs: refs, CandidateRunIDs: []platform.EvaluationRunID{"e-scope-candidate-1"},
		Limits: strict, At: execClock})
	if !errors.Is(err, platform.ErrScenarioScope) {
		t.Errorf("candidate in another environment: error = %v, want ErrScenarioScope", err)
	}
}

// The lifecycle: a completion is not repeated, a completed execution does not
// fail, and an execution that is interrupted — begun and never finished —
// stays running and is never a reference.
func TestScenarioExecutionLifecycle(t *testing.T) {
	f := newFixture(t)
	f.selfContained(t, "e1", "support", reps(1, "read"), reps(1, "read"), strict)
	_, _, err := f.plane.CompleteScenarioExecution(t.Context(), platform.CompleteScenarioExecutionRequest{
		ID: "e1", ReferenceRunIDs: []platform.EvaluationRunID{"e1-reference-1"},
		CandidateRunIDs: []platform.EvaluationRunID{"e1-candidate-1"}, Limits: strict, At: execClock})
	if !errors.Is(err, platform.ErrScenarioExecutionState) {
		t.Errorf("second completion: error = %v, want ErrScenarioExecutionState", err)
	}
	if _, err := f.plane.FailScenarioExecution(t.Context(), "e1", execClock); !errors.Is(err, platform.ErrScenarioExecutionState) {
		t.Errorf("failing a completed execution: error = %v", err)
	}
	if _, _, err := f.begin(t, "e1", "support", 1, platform.ScenarioReference{}); !errors.Is(err, platform.ErrStoreAlreadyExists) {
		t.Errorf("re-beginning e1: error = %v, want ErrStoreAlreadyExists", err)
	}

	if _, _, err := f.begin(t, "e-interrupted", "support", 1, platform.ScenarioReference{}); err != nil {
		t.Fatal(err)
	}
	f.sideRuns(t, "e-interrupted", "reference", reps(1, "read"))
	_, reference, err := f.begin(t, "e-after", "support", 1, platform.ScenarioReference{Last: true})
	if err != nil || reference.ID() != "e1" {
		t.Fatalf("last after an interrupted execution = %s, %v; want e1", reference.ID(), err)
	}
}

// Executions survive a restart: a reference recorded before the control
// plane stopped resolves after it starts again.
func TestRecordedReferenceSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	first, _ := f.selfContained(t, "e1", "support", reps(2, "read"), reps(2, "read"), strict)
	if err := f.store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openFixture(t, f.path)
	second, cmp, err := restarted.recorded(t, "e2", "support",
		platform.ScenarioReference{Last: true}, reps(2, "read"), strict)
	if err != nil {
		t.Fatalf("recorded execution after restart: %v", err)
	}
	if second.ReferenceExecution() != "e1" || cmp.Gate.Verdict() != platform.GateVerdictPass ||
		!slices.Equal(runIDsOf(second.SideRepetitions(platform.SideReference)),
			runIDsOf(first.SideRepetitions(platform.SideReference))) {
		t.Errorf("after restart: %+v, %s", second, cmp.Gate.Verdict())
	}
}

func containsAll(err error, parts ...string) bool {
	if err == nil {
		return false
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			return false
		}
	}
	return true
}
