package httpapi_test

// Task 086 over /v1: sameness and warnings on compare-repeated and on an
// execution's completion.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// task086Members are the members 086 adds to compare-repeated.
var task086Members = []string{"sameness", "warnings"}

// task081Members are the members 081 adds to every behavior row.
var task081Members = []string{"reference_fidelity", "candidate_fidelity"}

const samenessNotRecorded = `{"same_scenario":"not_recorded","same_inputs":"not_recorded",` +
	`"same_model":"not_recorded","same_prompt_ref":"not_recorded",` +
	`"reference":{"inputs":"not_recorded"},"candidate":{"inputs":"not_recorded"}}`

// member returns one top-level member of a JSON object, compact.
func member(t *testing.T, doc []byte, name string) string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(doc, &object); err != nil {
		t.Fatal(err)
	}
	return string(object[name])
}

// TestCompareRepeatedIsUnchangedButForSameness: runs no execution recorded
// compare exactly as they did before 086 (captured from main at a1ee3fd),
// byte for byte, once sameness and warnings are removed — and those say
// nothing was recorded, with no warning.
func TestCompareRepeatedIsUnchangedButForSameness(t *testing.T) {
	before, err := os.ReadFile(filepath.Join("testdata", "compare-repeated-n1-before-086.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := newAPI(t)
	a.completeIsolatedRun("ref-1", []string{"read", "list", "list"})
	a.completeIsolatedRun("can-1", []string{"read", "send"})
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"ref-1"}, "candidate_run_ids": []string{"can-1"},
		"gate_limits": repeatedLimitsBody("1", "0"),
	})
	a.mustStatus(r, 200, "compare-repeated")
	got := r.Body.Bytes()
	if s := member(t, got, "sameness"); s != samenessNotRecorded {
		t.Errorf("sameness = %s, want %s", s, samenessNotRecorded)
	}
	if w := member(t, got, "warnings"); w != `[]` {
		t.Errorf("warnings = %s, want []", w)
	}
	for _, name := range append(slices.Clone(task086Members), task081Members...) {
		got = withoutMember(t, got, name)
	}
	// Task 081's fourth optional check, not evaluated when omitted: the one
	// additive entry in gate.frequency_checks.
	fourth := []byte(`,{"name":"max_llm_calls_per_run","state":"not_evaluated"}`)
	if !bytes.Contains(got, fourth) {
		t.Fatalf("the response does not carry max_llm_calls_per_run as not evaluated:\n%s", got)
	}
	got = bytes.Replace(got, fourth, nil, 1)
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(before)) {
		t.Fatalf("the pre-086 response changed:\n got %s\nwant %s", got, before)
	}
}

// beginWithProvenance begins an execution recording scenario digest "a" and
// the given models; reference is nil for a self-contained execution.
func (a *api) beginWithProvenance(id string, runs int, reference map[string]string, referenceModel, candidateModel string) {
	a.t.Helper()
	a.seedHierarchy()
	body := beginBody(id, runs, reference)
	provenance := map[string]any{"scenario_digest": sha("a"), "candidate": map[string]any{"model": candidateModel}}
	if referenceModel != "" {
		provenance["reference"] = map[string]any{"model": referenceModel}
	}
	body["provenance"] = provenance
	a.mustStatus(a.do("POST", "/v1/scenario-executions", body), 201, "begin "+id)
}

// TestSamenessWhenOnlyTheModelChanged is the change 086 exists for: a second
// execution reusing the first's reference side, differing only in its model,
// reports same_model false and same_scenario true — on its completion, and on
// a later compare-repeated of the two executions' candidate runs, in any run
// order. The verdict is the same either way: no check reads sameness.
func TestSamenessWhenOnlyTheModelChanged(t *testing.T) {
	a := newAPI(t)
	a.beginWithProvenance("e1", 2, nil, "llama3.2", "llama3.2")
	for _, run := range []string{"e1-reference-1", "e1-reference-2", "e1-candidate-1", "e1-candidate-2"} {
		a.completeIsolatedRun(run, []string{"read"})
	}
	first := a.completeExecution("e1", []string{"e1-reference-1", "e1-reference-2"},
		[]string{"e1-candidate-1", "e1-candidate-2"}, 200)
	var completedFirst struct {
		Comparison json.RawMessage `json:"comparison"`
	}
	if err := json.Unmarshal(first, &completedFirst); err != nil {
		t.Fatal(err)
	}
	if s := member(t, completedFirst.Comparison, "sameness"); s != `{"same_scenario":"true","same_inputs":"true",`+
		`"same_model":"true","same_prompt_ref":"not_recorded",`+
		`"reference":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared","model":"llama3.2"},`+
		`"candidate":{"scenario_digest":"`+sha("a")+`","inputs":"not_declared","model":"llama3.2"}}` {
		t.Errorf("e1 sameness = %s", s)
	}

	a.beginWithProvenance("e2", 2, map[string]string{"mode": "execution", "execution_id": "e1"}, "", "gemma3:4b")
	for _, run := range []string{"e2-candidate-1", "e2-candidate-2"} {
		a.completeIsolatedRun(run, []string{"read"})
	}
	second := a.completeExecution("e2", nil, []string{"e2-candidate-1", "e2-candidate-2"}, 200)
	var completed struct {
		Comparison json.RawMessage `json:"comparison"`
	}
	if err := json.Unmarshal(second, &completed); err != nil {
		t.Fatal(err)
	}
	want := `{"same_scenario":"true","same_inputs":"true","same_model":"false","same_prompt_ref":"not_recorded",` +
		`"reference":{"scenario_digest":"` + sha("a") + `","inputs":"not_declared","model":"llama3.2"},` +
		`"candidate":{"scenario_digest":"` + sha("a") + `","inputs":"not_declared","model":"gemma3:4b"}}`
	wantWarnings := `[{"code":"model_differs","text":"The reference and candidate sides declared different models."}]`
	if s := member(t, completed.Comparison, "sameness"); s != want {
		t.Fatalf("e2 sameness =\n%s\nwant\n%s", s, want)
	}
	if w := member(t, completed.Comparison, "warnings"); w != wantWarnings {
		t.Fatalf("e2 warnings = %s, want %s", w, wantWarnings)
	}
	verdict := member(t, []byte(member(t, completed.Comparison, "gate")), "verdict")

	for _, order := range [][2][]string{
		{{"e1-candidate-1", "e1-candidate-2"}, {"e2-candidate-1", "e2-candidate-2"}},
		{{"e1-candidate-2", "e1-candidate-1"}, {"e2-candidate-2", "e2-candidate-1"}},
	} {
		r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
			"reference_run_ids": order[0], "candidate_run_ids": order[1],
			"gate_limits": repeatedLimitsBody("1", "0"),
		})
		a.mustStatus(r, 200, "compare-repeated")
		if s := member(t, r.Body.Bytes(), "sameness"); s != want {
			t.Fatalf("compare-repeated %v sameness =\n%s\nwant\n%s", order, s, want)
		}
		if w := member(t, r.Body.Bytes(), "warnings"); w != wantWarnings {
			t.Fatalf("compare-repeated warnings = %s", w)
		}
		if v := member(t, []byte(member(t, r.Body.Bytes(), "gate")), "verdict"); v != verdict {
			t.Fatalf("verdict %s, the completion's %s", v, verdict)
		}
	}

	// A side mixing runs recorded differently has nothing it can state.
	r := a.do("POST", "/v1/evaluations/compare-repeated", map[string]any{
		"reference_run_ids": []string{"e1-candidate-1", "e2-candidate-1"},
		"candidate_run_ids": []string{"e1-reference-1", "e1-reference-2"},
		"gate_limits":       repeatedLimitsBody("1", "0"),
	})
	a.mustStatus(r, 200, "compare-repeated mixed")
	var mixed struct {
		Sameness struct {
			SameScenario string          `json:"same_scenario"`
			SameModel    string          `json:"same_model"`
			Reference    json.RawMessage `json:"reference"`
		} `json:"sameness"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &mixed); err != nil {
		t.Fatal(err)
	}
	if mixed.Sameness.SameScenario != "not_recorded" || mixed.Sameness.SameModel != "not_recorded" ||
		string(mixed.Sameness.Reference) != `{"inputs":"not_recorded"}` {
		t.Fatalf("mixed side sameness = %+v", mixed.Sameness)
	}
}
