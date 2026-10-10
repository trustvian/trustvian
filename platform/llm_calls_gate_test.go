package platform

// Task 081's max_llm_calls_per_run, over values: every state, decision D9's
// evidence rule, and order independence.

import (
	"slices"
	"strings"
	"testing"
)

// withFidelity gives every entry of a run the same per-observation pair:
// "model" (semantic, model layer), "tool" (semantic, tool layer), "transport",
// or "" (unrecorded — what a run recorded before schema 13 holds).
func withFidelity(in repetitionInput, pair string) repetitionInput {
	in.entries = slices.Clone(in.entries)
	for i, e := range in.entries {
		n := e.Observations
		switch pair {
		case "model":
			in.entries[i].Fidelity = BehaviorFidelity{Semantic: n, LayerModel: n}
		case "tool":
			in.entries[i].Fidelity = BehaviorFidelity{Semantic: n, LayerTool: n}
		case "transport":
			in.entries[i].Fidelity = BehaviorFidelity{Transport: n, LayerTransport: n}
		default:
			in.entries[i].Fidelity = unrecordedFidelity(n)
		}
	}
	return in
}

func llmCheck(t *testing.T, limit *uint64, inputs ...repetitionInput) (FrequencyGateCheck, GateVerdict) {
	t.Helper()
	limits := lenientLimits()
	if limit != nil {
		limits.MaxLLMCallsPerRun = NewOptionalGateLimit(*limit)
	}
	runs := 0
	for _, in := range inputs {
		if in.evidence.Side == SideCandidate {
			runs++
		}
	}
	c, err := reduceRepeated(limits, runs, inputs)
	if err != nil {
		t.Fatal(err)
	}
	checks := c.Gate.FrequencyChecks()
	if len(checks) != FrequencyCheckCount || checks[3].Name != CheckMaxLLMCallsPerRun {
		t.Fatalf("frequency checks %+v", checks)
	}
	return checks[3], c.Gate.Verdict()
}

func uptr(v uint64) *uint64 { return &v }

func TestMaxLLMCallsPerRunStates(t *testing.T) {
	ref := withFidelity(runWith(SideReference, 1, map[string]uint64{"chat": 2}), "model")
	modelRun := func(index int, calls uint64) repetitionInput {
		return withFidelity(runWith(SideCandidate, index, map[string]uint64{"chat": calls}), "model")
	}
	// toolsNamedRun is the demo as 081's third measured run found it: tools
	// named, model calls plain HTTP. Semantic evidence, no model layer.
	toolsNamedRun := func(index int) repetitionInput {
		tool := withFidelity(runWith(SideCandidate, index, map[string]uint64{"read": 2}), "tool")
		http := withFidelity(runWith(SideCandidate, index, map[string]uint64{"post": 20}), "transport")
		tool.entries = append(tool.entries, http.entries...)
		return tool
	}
	// fullRun has a model call beside a named tool and a transport call.
	fullRun := func(index int, calls uint64) repetitionInput {
		run := modelRun(index, calls)
		tool := withFidelity(runWith(SideCandidate, index, map[string]uint64{"read": 2}), "tool")
		http := withFidelity(runWith(SideCandidate, index, map[string]uint64{"post": 9}), "transport")
		run.entries = append(run.entries, tool.entries...)
		run.entries = append(run.entries, http.entries...)
		return run
	}
	for _, tt := range []struct {
		name     string
		limit    *uint64
		inputs   []repetitionInput
		state    GateCheckState
		actual   uint64
		passed   bool
		mentions string
	}{
		{"omitted", nil, []repetitionInput{ref, modelRun(1, 9)}, GateCheckNotEvaluated, 0, false, ""},
		{"satisfied", uptr(5), []repetitionInput{ref, modelRun(1, 3), modelRun(2, 5)}, GateCheckEvaluated, 5, true, ""},
		{"violated", uptr(4), []repetitionInput{ref, modelRun(1, 3), modelRun(2, 5)}, GateCheckEvaluated, 5, false, ""},
		// Every run has model-layer evidence: the real maximum, counting only
		// model-layer observations, not the tool or transport ones beside them.
		{"every run with model-layer evidence reads its real maximum", uptr(6),
			[]repetitionInput{ref, fullRun(1, 1), fullRun(2, 7), fullRun(3, 4)}, GateCheckEvaluated, 7, false, ""},
		// D9: tools named and model calls transport. D8 evaluated this to a
		// vacuous 0; semantic evidence without a model layer is not enough.
		{"tools named and model calls transport", uptr(50), []repetitionInput{ref, toolsNamedRun(1)},
			GateCheckDeferred, 0, false, `run "candidate-1" has no model-layer observation; model calls cannot be counted`},
		// A producer with no GenAI or OpenInference instrumentation — every
		// behavior transport — cannot show model calls either.
		{"transport only", uptr(100), []repetitionInput{ref,
			withFidelity(runWith(SideCandidate, 1, map[string]uint64{"post": 7}), "transport")},
			GateCheckDeferred, 0, false, "has no model-layer observation; model calls cannot be counted"},
		// A run recorded before schema 13 reads deferred, never 0.
		{"migrated", uptr(100), []repetitionInput{ref,
			withFidelity(runWith(SideCandidate, 1, map[string]uint64{"chat": 3}), "")},
			GateCheckDeferred, 0, false, "no model-layer observation"},
		{"one run of N without model-layer evidence defers the whole check", uptr(100), []repetitionInput{ref,
			modelRun(1, 2), toolsNamedRun(2), modelRun(3, 4)},
			GateCheckDeferred, 0, false, `run "candidate-2" has no model-layer observation; model calls cannot be counted`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			check, verdict := llmCheck(t, tt.limit, tt.inputs...)
			if check.State != tt.state {
				t.Fatalf("state = %s, want %s (%+v)", check.State, tt.state, check)
			}
			switch tt.state {
			case GateCheckEvaluated:
				// D9: an evaluated check has seen a model call in every run.
				if check.Actual == 0 {
					t.Fatalf("an evaluated check reports 0: %+v", check)
				}
				if check.Actual != tt.actual || check.Passed != tt.passed || check.Bound != *tt.limit ||
					check.Rule != RuleAtMost {
					t.Fatalf("check = %+v, want actual %d passed %v", check, tt.actual, tt.passed)
				}
			case GateCheckDeferred:
				if !strings.Contains(check.MissingEvidence, tt.mentions) {
					t.Fatalf("missing evidence %q, want it to mention %q", check.MissingEvidence, tt.mentions)
				}
				if verdict != GateVerdictFail {
					t.Fatalf("a deferred check left the verdict %s (D1)", verdict)
				}
			case GateCheckNotEvaluated:
				if check.MissingEvidence != "" || check.Rule != "" {
					t.Fatalf("a check that did not run carries an outcome: %+v", check)
				}
			}
		})
	}
}

// TestMaxLLMCallsDeferralIsOrderIndependent: the run it names, and the count,
// do not depend on how the candidate runs were listed.
func TestMaxLLMCallsDeferralIsOrderIndependent(t *testing.T) {
	ref := withFidelity(runWith(SideReference, 1, map[string]uint64{"chat": 1}), "model")
	a := withFidelity(runWith(SideCandidate, 1, map[string]uint64{"chat": 1}), "model")
	b := withFidelity(runWith(SideCandidate, 2, map[string]uint64{"post": 1}), "transport")
	c := withFidelity(runWith(SideCandidate, 3, map[string]uint64{"post": 1}), "transport")
	var want string
	for _, order := range [][]repetitionInput{{a, b, c}, {c, b, a}, {b, a, c}} {
		check, _ := llmCheck(t, uptr(9), append([]repetitionInput{ref}, order...)...)
		if want == "" {
			want = check.MissingEvidence
		} else if check.MissingEvidence != want {
			t.Fatalf("order changed the evidence: %q vs %q", check.MissingEvidence, want)
		}
	}
	if !strings.Contains(want, `run "candidate-2"`) || !strings.Contains(want, "2 of 3 candidate runs") {
		t.Fatalf("missing evidence %q", want)
	}
}
