package platform

// Task 086's sameness answers, field by field.

import "testing"

func TestComparisonSamenessAnswers(t *testing.T) {
	a, b := testDigest("a"), testDigest("b")
	ref := PromptRef{Name: "sys@v14", Digest: testDigest("c")}
	side := func(scenario, input, model string, prompt PromptRef) SideProvenance {
		return SideProvenance{ScenarioDigest: scenario, InputsDeclared: input != "", InputDigest: input,
			SideDeclaration: SideDeclaration{Model: model, PromptRef: prompt}}
	}
	for _, tt := range []struct {
		name                            string
		reference, candidate            SideProvenance
		scenario, inputs, model, prompt SamenessState
		warnings                        []string
	}{
		{"identical", side(a, b, "m", ref), side(a, b, "m", ref), SameTrue, SameTrue, SameTrue, SameTrue, nil},
		{"nothing recorded", SideProvenance{}, SideProvenance{},
			SameNotRecorded, SameNotRecorded, SameNotRecorded, SameNotRecorded, nil},
		{"one side recorded", side(a, b, "m", ref), SideProvenance{},
			SameNotRecorded, SameNotRecorded, SameNotRecorded, SameNotRecorded, nil},
		{"neither declared inputs", side(a, "", "", PromptRef{}), side(a, "", "", PromptRef{}),
			SameTrue, SameTrue, SameNotRecorded, SameNotRecorded, nil},
		{"one declared inputs", side(a, b, "", PromptRef{}), side(a, "", "", PromptRef{}),
			SameTrue, SameFalse, SameNotRecorded, SameNotRecorded, []string{WarningInputsDiffer}},
		// A changed gate limit moves scenario_digest alone (config's
		// TestAGateLimitAloneMovesOnlyTheScenarioDigest): only same_scenario
		// answers false, with one warning.
		{"only a gate limit", side(a, b, "m", ref), side(testDigest("d"), b, "m", ref),
			SameFalse, SameTrue, SameTrue, SameTrue, []string{WarningScenarioDiffers}},
		{"only the model", side(a, b, "llama3.2", ref), side(a, b, "gemma3:4b", ref),
			SameTrue, SameTrue, SameFalse, SameTrue, []string{WarningModelDiffers}},
		{"the prompt's digest", side(a, b, "m", ref), side(a, b, "m", PromptRef{Name: ref.Name, Digest: b}),
			SameTrue, SameTrue, SameTrue, SameFalse, []string{WarningPromptRefDiffers}},
		{"everything", side(a, a, "m", ref), side(b, b, "n", PromptRef{Name: "other", Digest: b}),
			SameFalse, SameFalse, SameFalse, SameFalse,
			[]string{WarningScenarioDiffers, WarningInputsDiffer, WarningModelDiffers, WarningPromptRefDiffers}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, warnings := newComparisonSameness(tt.reference, tt.candidate)
			if s.SameScenario != tt.scenario || s.SameInputs != tt.inputs || s.SameModel != tt.model ||
				s.SamePromptRef != tt.prompt {
				t.Fatalf("sameness = %s %s %s %s, want %s %s %s %s", s.SameScenario, s.SameInputs, s.SameModel,
					s.SamePromptRef, tt.scenario, tt.inputs, tt.model, tt.prompt)
			}
			if s.Reference != tt.reference || s.Candidate != tt.candidate {
				t.Fatal("sameness does not carry both sides' values")
			}
			if len(warnings) != len(tt.warnings) {
				t.Fatalf("warnings %+v, want %v", warnings, tt.warnings)
			}
			for i, w := range warnings {
				if w.Code != tt.warnings[i] || w.Text == "" {
					t.Fatalf("warning %d = %+v, want %s", i, w, tt.warnings[i])
				}
			}
		})
	}
}

// TestSideProvenanceOfRunsNeedsOneAgreedValue: a side states provenance only
// when every run was recorded with the same one value — in any order.
func TestSideProvenanceOfRunsNeedsOneAgreedValue(t *testing.T) {
	x, y := fullProvenance("llama3.2"), fullProvenance("gemma3:4b")
	recorded := map[EvaluationRunID][]SideProvenance{"r1": {x}, "r2": {x}, "r3": {y}, "r4": {x, y}}
	for _, tt := range []struct {
		ids  []EvaluationRunID
		want SideProvenance
	}{
		{[]EvaluationRunID{"r1", "r2"}, x},
		{[]EvaluationRunID{"r2", "r1"}, x},
		{[]EvaluationRunID{"r1", "r3"}, SideProvenance{}},
		{[]EvaluationRunID{"r3", "r1"}, SideProvenance{}},
		{[]EvaluationRunID{"r4"}, SideProvenance{}},
		{[]EvaluationRunID{"r1", "unrecorded"}, SideProvenance{}},
	} {
		if got := sideProvenanceOfRuns(tt.ids, recorded); got != tt.want {
			t.Errorf("%v = %+v, want %+v", tt.ids, got, tt.want)
		}
	}
}
