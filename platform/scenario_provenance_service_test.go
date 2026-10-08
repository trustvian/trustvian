package platform_test

// Task 086 through the service: what an execution records about each side it
// ran, and what a reused reference side carries.

import (
	"errors"
	"strings"
	"testing"
	"time"

	platform "trustvian-platform"
)

func digestOf(fill string) string { return "sha256:" + strings.Repeat(fill, 64) }

// provenance is a request recording scenario digest s, input digest i ("" for
// no inputs) and the two sides' models.
func provenance(s, i, referenceModel, candidateModel string) platform.ExecutionProvenance {
	return platform.ExecutionProvenance{
		ScenarioDigest: s, InputsDeclared: i != "", InputDigest: i,
		Reference: platform.SideDeclaration{Model: referenceModel},
		Candidate: platform.SideDeclaration{Model: candidateModel},
	}
}

func (f *controlPlaneFixture) beginWith(t *testing.T, id string, runs int, reference platform.ScenarioReference,
	p platform.ExecutionProvenance) (platform.ScenarioExecution, error) {
	t.Helper()
	f.seedRepeatedHierarchy(t)
	execution, _, err := f.plane.BeginScenarioExecution(t.Context(), platform.BeginScenarioExecutionRequest{
		ID: platform.ScenarioExecutionID(id), ScenarioName: "support", Runs: runs,
		Scope: execScope, Reference: reference, At: execClock, Provenance: p,
	})
	return execution, err
}

// runExecution begins, runs and completes one execution with provenance p.
// A recorded reference runs only the candidate side.
func (f *controlPlaneFixture) runExecution(t *testing.T, id string, reference platform.ScenarioReference,
	p platform.ExecutionProvenance, referenceReps, candidate []repetition,
) (platform.ScenarioExecution, platform.RepeatedEvaluationComparison) {
	t.Helper()
	if _, err := f.beginWith(t, id, len(candidate), reference, p); err != nil {
		t.Fatalf("begin %s: %v", id, err)
	}
	var refs []platform.EvaluationRunID
	if !reference.Recorded() {
		refs = f.sideRuns(t, id, "reference", referenceReps)
	}
	cands := f.sideRuns(t, id, "candidate", candidate)
	execution, comparison, err := f.plane.CompleteScenarioExecution(t.Context(),
		platform.CompleteScenarioExecutionRequest{ID: platform.ScenarioExecutionID(id),
			ReferenceRunIDs: refs, CandidateRunIDs: cands, Limits: lenient, At: execClock.Add(time.Minute)})
	if err != nil {
		t.Fatalf("complete %s: %v", id, err)
	}
	return execution, comparison
}

var lenient = repeatedLimits(1, 0, 100, 100, 100)

// TestASelfContainedExecutionRecordsBothSides: one scenario and one input set
// for both sides, each side's own declarations.
func TestASelfContainedExecutionRecordsBothSides(t *testing.T) {
	f := newFixture(t)
	p := provenance(digestOf("a"), digestOf("b"), "llama3.2", "gemma3:4b")
	p.Candidate.PromptRef = platform.PromptRef{Name: "sys@v14", Digest: digestOf("c")}
	e, _ := f.runExecution(t, "e1", platform.ScenarioReference{}, p, reps(1, "read"), reps(1, "read"))
	stored, err := f.plane.ScenarioExecution(t.Context(), e.ID())
	if err != nil {
		t.Fatal(err)
	}
	want := map[platform.ComparisonSide]platform.SideProvenance{
		platform.SideReference: {ScenarioDigest: digestOf("a"), InputsDeclared: true, InputDigest: digestOf("b"),
			SideDeclaration: platform.SideDeclaration{Model: "llama3.2"}},
		platform.SideCandidate: {ScenarioDigest: digestOf("a"), InputsDeclared: true, InputDigest: digestOf("b"),
			SideDeclaration: p.Candidate},
	}
	for side, w := range want {
		if got := stored.Provenance(side); got != w {
			t.Errorf("%s side = %+v, want %+v", side, got, w)
		}
	}
}

// TestAReusedReferenceSideCarriesTheProvenanceOfTheExecutionThatRanIt: the
// referenced execution's reference side, copied — and through a chain of
// reuse, still the execution that ran those runs.
func TestAReusedReferenceSideCarriesTheProvenanceOfTheExecutionThatRanIt(t *testing.T) {
	f := newFixture(t)
	first, _ := f.runExecution(t, "e1", platform.ScenarioReference{},
		provenance(digestOf("a"), "", "llama3.2", "llama3.2"), reps(1, "read"), reps(1, "read"))
	second, _ := f.runExecution(t, "e2", platform.ScenarioReference{ExecutionID: "e1"},
		provenance(digestOf("d"), digestOf("e"), "", "gemma3:4b"), nil, reps(1, "read"))
	third, _ := f.runExecution(t, "e3", platform.ScenarioReference{Last: true},
		provenance(digestOf("f"), "", "", "qwen3"), nil, reps(1, "read"))

	for _, e := range []platform.ScenarioExecution{second, third} {
		if got, want := e.Provenance(platform.SideReference), first.Provenance(platform.SideReference); got != want {
			t.Errorf("%s reference side = %+v, want e1's %+v", e.ID(), got, want)
		}
	}
	if got := second.Provenance(platform.SideCandidate); got.ScenarioDigest != digestOf("d") ||
		got.InputDigest != digestOf("e") || got.Model != "gemma3:4b" {
		t.Errorf("e2 candidate side = %+v", got)
	}
}

// TestProvenanceIsRefusedBeforeAnythingIsStored: a malformed value, and a
// reference-side declaration on an execution whose reference side another
// execution ran.
func TestProvenanceIsRefusedBeforeAnythingIsStored(t *testing.T) {
	f := newFixture(t)
	f.runExecution(t, "e1", platform.ScenarioReference{}, provenance(digestOf("a"), "", "", ""),
		reps(1, "read"), reps(1, "read"))
	for _, tt := range []struct {
		name      string
		reference platform.ScenarioReference
		p         platform.ExecutionProvenance
		mentions  string
	}{
		{"a malformed scenario digest", platform.ScenarioReference{},
			provenance("sha256:abc", "", "", ""), "scenario_digest"},
		{"prompt text as a model", platform.ScenarioReference{},
			provenance(digestOf("a"), "", "", "Answer politely."), "model"},
		{"a reused side's model", platform.ScenarioReference{ExecutionID: "e1"},
			provenance(digestOf("a"), "", "llama3.2", ""), "referenced execution"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.beginWith(t, "refused", 1, tt.reference, tt.p)
			if !errors.Is(err, platform.ErrInvalidRepeatedRequest) || !strings.Contains(err.Error(), tt.mentions) {
				t.Fatalf("begin error = %v, want one mentioning %q", err, tt.mentions)
			}
			if _, err := f.plane.ScenarioExecution(t.Context(), "refused"); !errors.Is(err, platform.ErrStoreNotFound) {
				t.Fatalf("a refused execution was stored: %v", err)
			}
		})
	}
}
