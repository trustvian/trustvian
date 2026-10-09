package platform_test

// Task 086 through the service: what an execution records about each side it
// ran, and what a reused reference side carries.

import (
	"errors"
	"reflect"
	"slices"
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

// TestSamenessIsNotRecordedForExecutionsBeforeProvenance: an execution with
// nothing recorded — begun by a client that predates task 086, or migrated
// from schema 11 — answers not_recorded to everything, never false, and
// produces no warning, even against one that recorded everything.
func TestSamenessIsNotRecordedForExecutionsBeforeProvenance(t *testing.T) {
	f := newFixture(t)
	f.runExecution(t, "e1", platform.ScenarioReference{}, platform.ExecutionProvenance{},
		reps(1, "read"), reps(1, "read"))
	full := provenance(digestOf("a"), digestOf("b"), "", "gemma3:4b")
	full.Candidate.PromptRef = platform.PromptRef{Name: "sys@v14", Digest: digestOf("c")}
	_, cmp := f.runExecution(t, "e2", platform.ScenarioReference{ExecutionID: "e1"}, full, nil, reps(1, "read"))
	s := cmp.Sameness
	for name, state := range map[string]platform.SamenessState{
		"scenario": s.SameScenario, "inputs": s.SameInputs, "model": s.SameModel, "prompt_ref": s.SamePromptRef,
	} {
		if state != platform.SameNotRecorded {
			t.Errorf("same_%s = %s, want not_recorded", name, state)
		}
	}
	if len(cmp.Warnings) != 0 || s.Reference != (platform.SideProvenance{}) || s.Candidate.Model != "gemma3:4b" {
		t.Errorf("warnings %+v, sameness %+v", cmp.Warnings, s)
	}
}

// TestSamenessChangesNoGateResult: the same evidence compared under the same
// model and under a different one has the same checks, frequency checks and
// verdict — only sameness and warnings differ.
func TestSamenessChangesNoGateResult(t *testing.T) {
	f := newFixture(t)
	f.runExecution(t, "e1", platform.ScenarioReference{},
		provenance(digestOf("a"), "", "llama3.2", "llama3.2"), reps(2, "read"), reps(2, "read", "export"))
	_, same := f.runExecution(t, "e2", platform.ScenarioReference{ExecutionID: "e1"},
		provenance(digestOf("a"), "", "", "llama3.2"), nil, reps(2, "read", "export"))
	_, changed := f.runExecution(t, "e3", platform.ScenarioReference{ExecutionID: "e1"},
		provenance(digestOf("d"), digestOf("e"), "", "gemma3:4b"), nil, reps(2, "read", "export"))
	if len(same.Warnings) != 0 || len(changed.Warnings) != 3 {
		t.Fatalf("warnings: same %+v, changed %+v", same.Warnings, changed.Warnings)
	}
	if !reflect.DeepEqual(same.Gate, changed.Gate) || !reflect.DeepEqual(same.Behaviors, changed.Behaviors) ||
		!reflect.DeepEqual(same.Suggestions, changed.Suggestions) {
		t.Fatalf("sameness changed the gate:\nsame    %+v\nchanged %+v", same.Gate, changed.Gate)
	}
	codes := []string{changed.Warnings[0].Code, changed.Warnings[1].Code, changed.Warnings[2].Code}
	if want := []string{platform.WarningScenarioDiffers, platform.WarningInputsDiffer,
		platform.WarningModelDiffers}; !slices.Equal(codes, want) {
		t.Fatalf("warning codes %v, want %v", codes, want)
	}
}

// TestProvenanceIsNotBehavioralIdentity: the same workload under a different
// scenario, inputs, model and prompt reference produces the same fingerprints
// and behaviors. Provenance is recorded on the execution and read by
// sameness; it never reaches a record, StableFeatures, a fingerprint or a
// learning scope (identity_fields_test.go pins the types themselves).
func TestProvenanceIsNotBehavioralIdentity(t *testing.T) {
	f := newFixture(t)
	plain, plainCmp := f.runExecution(t, "e1", platform.ScenarioReference{}, platform.ExecutionProvenance{},
		reps(2, "read", "export"), reps(2, "read", "send"))
	full := provenance(digestOf("a"), digestOf("b"), "llama3.2", "gemma3:4b")
	full.Candidate.PromptRef = platform.PromptRef{Name: "sys@v14", Digest: digestOf("c")}
	declared, declaredCmp := f.runExecution(t, "e2", platform.ScenarioReference{}, full,
		reps(2, "read", "export"), reps(2, "read", "send"))
	if !reflect.DeepEqual(plainCmp.Behaviors, declaredCmp.Behaviors) || len(plainCmp.Behaviors) != 3 {
		t.Fatalf("behaviors differ under different provenance:\n%+v\n%+v", plainCmp.Behaviors, declaredCmp.Behaviors)
	}
	for i, r := range declared.Repetitions() {
		if strings.Contains(string(r.BehavioralProfile), "llama") || strings.Contains(string(r.BehavioralProfile), "sha256") ||
			strings.TrimPrefix(string(r.BehavioralProfile), "e2") != strings.TrimPrefix(string(plain.Repetitions()[i].BehavioralProfile), "e1") {
			t.Fatalf("learning scope %s depends on provenance", r.BehavioralProfile)
		}
	}
}
