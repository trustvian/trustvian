package platform_test

// Task 078 criterion 9: a scenario asserts no ordering and suppresses none of
// the engine's sequence evidence. A reorder reaches the repeated gate only as a
// block decision or a critical-risk reading the engine itself recorded, so this
// is proven where that evidence exists: a real Engine with a learned profile and
// TransitionWeight > 0, whose records go through the real control plane into
// checks 5 and 6.
//
// It is not provable from a scenario run. `trustvian eval run` runs every
// repetition under `trustvian dev`, whose processor takes no anomaly
// configuration, so every sequence weight is 0 there; and each repetition's
// scope is fresh. See docs/tasks/v1.0/078-behavioral-scenario-suites.md.

import (
	"fmt"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/config"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// sequenceEngine is an Engine with transition_deviation at weight and a
// policy that blocks high risk, scoped to one profile.
func sequenceEngine(t *testing.T, profile string, weight float64) *trustvian.Engine {
	t.Helper()
	anomalyCfg, err := config.CompileAnomaly(config.AnomalyConfig{
		Version:          config.AnomalySchemaVersionV1,
		TransitionWeight: weight,
	})
	if err != nil {
		t.Fatalf("CompileAnomaly() error = %v", err)
	}
	pol, err := config.CompilePolicy(config.PolicyConfig{
		Version:         config.SchemaVersionV1,
		DefaultDecision: "allow",
		DefaultReason:   "risk within tolerance",
		Rules: []config.PolicyRule{{
			Name:     "block-high-risk",
			Reason:   "risk too high",
			Decision: "block",
			When:     config.PolicyCondition{MinRiskLevel: "high"},
		}},
	})
	if err != nil {
		t.Fatalf("CompilePolicy() error = %v", err)
	}
	return trustvian.NewEngine(
		trustvian.WithLearningScope(profile),
		trustvian.WithAnomalyConfig(anomalyCfg),
		trustvian.WithPolicy(pol),
	)
}

// learnedRun warms profile on order through the gated Analyze+Observe loop,
// then records one pass over measured as the run's evidence. Only the measured
// pass is ingested: the warm-up is what a deployment's persisted profile
// already holds, not part of this run.
func (f *controlPlaneFixture) learnedRun(t *testing.T, runID, profile string, weight float64,
	order, measured []string,
) {
	t.Helper()
	ctx := t.Context()
	engine := sequenceEngine(t, profile, weight)
	clock := aggEpoch
	analyze := func(id, op string) trustvian.Result {
		t.Helper()
		clock = clock.Add(time.Second)
		result, err := engine.Analyze(ctx, event.Event{
			ID:        id,
			Timestamp: clock,
			Actor:     event.Actor{ID: "agent-deploy", Type: event.ActorTypeAIAgent, IdentityConfidence: 0.9},
			Operation: event.Operation{Category: event.OperationCategoryTool, Name: op},
			Target:    event.Target{Name: "build-host", Category: event.TargetCategoryExternal},
			Context:   event.Context{Environment: string(fixtureEnvironment)},
		})
		if err != nil {
			t.Fatalf("Analyze(%s) error = %v", op, err)
		}
		if _, err := engine.Observe(ctx, result); err != nil {
			t.Fatalf("Observe(%s) error = %v", op, err)
		}
		return result
	}
	for i := range 25 {
		for _, op := range order {
			analyze(fmt.Sprintf("%s-warm-%d-%s", runID, i, op), op)
		}
	}

	f.seedRepeatedHierarchy(t)
	run, err := platform.NewEvaluationRun(platform.EvaluationRunID(runID), "cand-1",
		fixtureEnvironment, platform.BehavioralProfileRef(profile), aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := f.plane.StartEvaluationRun(ctx, run.ID(), aggEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
	for i, op := range measured {
		result := analyze(fmt.Sprintf("%s-e%d", runID, i), op)
		if _, err := f.plane.IngestDecisionRecord(ctx, platform.IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1),
			BehavioralProfile: platform.BehavioralProfileRef(profile), Record: result.DecisionRecord(),
		}); err != nil {
			t.Fatalf("ingest %s/%d error = %v", runID, i, err)
		}
	}
	if _, err := f.plane.CompleteEvaluationRun(ctx, run.ID(), aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}

// A reorder the engine blocks fails; the same reorder, with the sequence
// signal at its default weight of 0, passes. The runner decides neither.
func TestAReorderFailsTheRepeatedGateOnlyThroughEngineEvidence(t *testing.T) {
	learned := []string{"plan", "fetch", "apply"}
	reordered := []string{"apply", "fetch", "plan"}
	cases := []struct {
		name     string
		weight   float64
		wantFail bool
	}{
		{"transition_deviation opted in: the engine blocks, the gate fails", 0.7, true},
		// What `trustvian dev`'s processor runs with: no anomaly configuration.
		{"default weight 0: no gated evidence, the gate passes", 0, false},
	}
	for ci, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			var refs, cands []platform.EvaluationRunID
			for i := range 2 {
				ref := fmt.Sprintf("reorder%d-ref-%d", ci, i+1)
				f.learnedRun(t, ref, ref+"-profile", tc.weight, learned, learned)
				refs = append(refs, platform.EvaluationRunID(ref))
				can := fmt.Sprintf("reorder%d-can-%d", ci, i+1)
				f.learnedRun(t, can, can+"-profile", tc.weight, learned, reordered)
				cands = append(cands, platform.EvaluationRunID(can))
			}

			// Limits that admit no block, no critical reading and no added behavior.
			result := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 0, 0, 0))

			// The reorder adds no behavior: the set is identical, so only the
			// engine's sequence evidence can fail this gate.
			if c := checkNamed(result, platform.CheckRepeatedlyAddedBehaviors); c.Actual != 0 || !c.Passed {
				t.Fatalf("repeatedly added = %d passed=%v, want 0, pass: a reorder changes no behavior set",
					c.Actual, c.Passed)
			}
			for _, rep := range result.Repetitions {
				if rep.Side == platform.SideReference && rep.BlockDecisions != 0 {
					t.Fatalf("reference repetition %d recorded %d blocks, want 0 for the learned order",
						rep.Index, rep.BlockDecisions)
				}
			}
			block := checkNamed(result, platform.CheckWorstCandidateBlockDecisions)
			if !tc.wantFail {
				if block.Actual != 0 || result.Gate.Verdict() != platform.GateVerdictPass {
					t.Fatalf("worst candidate blocks = %d, verdict %s; want 0, pass",
						block.Actual, result.Gate.Verdict())
				}
				return
			}
			if block.Actual == 0 || block.Passed {
				t.Fatalf("worst candidate blocks = %d passed=%v, want > 0 and fail: the engine's "+
					"transition evidence was suppressed", block.Actual, block.Passed)
			}
			if result.Gate.Verdict() != platform.GateVerdictFail {
				t.Fatalf("verdict = %s, want fail", result.Gate.Verdict())
			}

			// The same evidence passes when the limits tolerate it, so the FAIL
			// is the engine's evidence meeting a limit, not the reorder itself.
			tolerant := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 0, block.Actual, 1<<20))
			if tolerant.Gate.Verdict() != platform.GateVerdictPass {
				t.Fatalf("verdict under limits admitting %d blocks = %s, want pass",
					block.Actual, tolerant.Gate.Verdict())
			}
		})
	}
}
