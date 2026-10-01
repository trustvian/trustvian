package platform_test

// Repeated evaluation through the whole service, over a real SQLite store
// (task 078, ADR 0053). Deterministic workloads: every repetition's records are
// chosen by the test, so every count below is arithmetic known in advance.

import (
	"errors"
	"fmt"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
	platform "trustvian-platform"
)

// repetition is one run's deterministic workload: which behaviors it shows,
// and how many block decisions and critical-risk readings the engine recorded.
type repetition struct {
	behaviors []string // operation names; each is its own fingerprint
	blocks    int
	critical  int
}

func (f *controlPlaneFixture) seedRepeatedHierarchy(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := platform.NewCandidate("cand-1", "agent-1", platform.CandidateMetadata{Label: "v1"})
	environment, _ := platform.NewEnvironment(fixtureEnvironment, "proj-1", "Staging")
	for _, err := range []error{
		f.plane.CreateProject(ctx, project), f.plane.CreateAgent(ctx, agent),
		f.plane.CreateCandidate(ctx, candidate), f.plane.CreateEnvironment(ctx, environment),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			t.Fatalf("seed error = %v", err)
		}
	}
}

// runRepetition drives one run to completion under its own profile.
func (f *controlPlaneFixture) runRepetition(
	t *testing.T, runID, profile string, rep repetition, complete bool,
) {
	t.Helper()
	ctx := t.Context()
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
	var records []trustvian.DecisionRecord
	for i, op := range rep.behaviors {
		decision, risk := "allow", "low"
		if i < rep.blocks {
			decision = "block"
		}
		if i < rep.critical {
			risk = "critical"
		}
		records = append(records, scorecardRecord(fmt.Sprintf("%s-e%d", runID, i),
			"fp-"+op, op, fixtureEnvironment, decision, risk, event.ApprovalNotRequired, 0.9))
	}
	for i, rec := range records {
		if _, err := f.plane.IngestDecisionRecord(ctx, platform.IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1),
			BehavioralProfile: platform.BehavioralProfileRef(profile), Record: rec,
		}); err != nil {
			t.Fatalf("ingest %s/%d error = %v", runID, i, err)
		}
	}
	if complete {
		if _, err := f.plane.CompleteEvaluationRun(ctx, run.ID(), aggEpoch.Add(time.Hour)); err != nil {
			t.Fatalf("CompleteEvaluationRun() error = %v", err)
		}
	}
}

// sides runs N reference and N candidate repetitions, each in its own profile,
// and returns their run ids in order.
func (f *controlPlaneFixture) sides(t *testing.T, prefix string, reference, candidate []repetition,
) (refs, cands []platform.EvaluationRunID) {
	t.Helper()
	for i, rep := range reference {
		id := fmt.Sprintf("%s-ref-%d", prefix, i+1)
		f.runRepetition(t, id, id+"-profile", rep, true)
		refs = append(refs, platform.EvaluationRunID(id))
	}
	for i, rep := range candidate {
		id := fmt.Sprintf("%s-can-%d", prefix, i+1)
		f.runRepetition(t, id, id+"-profile", rep, true)
		cands = append(cands, platform.EvaluationRunID(id))
	}
	return refs, cands
}

func repeatedLimits(k, j, added, block, critical uint64) platform.RepeatedEvaluationGateLimits {
	return platform.RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: k, AddedReferencePresenceMaximum: j,
		MaxRepeatedAddedBehaviors: added, MaxBlockDecisionsPerRun: block,
		MaxCriticalRiskObservationsPerRun: critical,
	}
}

func compareRepeated(t *testing.T, f *controlPlaneFixture, refs, cands []platform.EvaluationRunID,
	limits platform.RepeatedEvaluationGateLimits) platform.RepeatedEvaluationComparison {
	t.Helper()
	result, err := f.plane.CompareRepeatedEvaluations(t.Context(), platform.RepeatedEvaluationRequest{
		ReferenceRunIDs: refs, CandidateRunIDs: cands, Limits: limits})
	if err != nil {
		t.Fatalf("CompareRepeatedEvaluations() error = %v", err)
	}
	return result
}

func checkNamed(result platform.RepeatedEvaluationComparison, name platform.RepeatedCheckName,
) platform.RepeatedGateCheck {
	for _, c := range result.Gate.Checks() {
		if c.Name == name {
			return c
		}
	}
	return platform.RepeatedGateCheck{}
}

func presenceOf(result platform.RepeatedEvaluationComparison, op string) platform.RepeatedBehaviorPresence {
	for _, b := range result.Behaviors {
		if b.FingerprintID == "fp-"+op {
			return b
		}
	}
	return platform.RepeatedBehaviorPresence{}
}

// At N = 1, k = 1, j = 0 the repeated gate is task 056's gate, check for check,
// asserted against the real EvaluateEvaluationGate on the same pair.
func TestRepeatedGateAtOneRunReproducesTheSingleRunGate(t *testing.T) {
	cases := []struct {
		name                  string
		reference, candidate  repetition
		maxAdded, block, crit uint64
	}{
		{"identical", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read"}}, 0, 0, 0},
		{"added within limit", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read", "write"}}, 1, 0, 0},
		{"added over limit", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read", "write", "delete"}}, 1, 0, 0},
		{"a block", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read"}, blocks: 1}, 0, 0, 0},
		{"a block allowed", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read"}, blocks: 1}, 0, 1, 0},
		{"critical", repetition{behaviors: []string{"read"}}, repetition{behaviors: []string{"read"}, critical: 1}, 0, 0, 0},
		{"empty candidate", repetition{behaviors: []string{"read"}}, repetition{}, 0, 0, 0},
		{"empty reference", repetition{}, repetition{behaviors: []string{"read"}}, 5, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			refs, cands := f.sides(t, "n1", []repetition{tc.reference}, []repetition{tc.candidate})

			single, err := f.plane.CompareEvaluations(t.Context(), refs[0], cands[0],
				platform.EvaluationGateLimits{MaxAddedBehaviors: tc.maxAdded,
					MaxBlockDecisions: tc.block, MaxCriticalRiskObservations: tc.crit})
			if err != nil {
				t.Fatalf("CompareEvaluations() error = %v", err)
			}
			repeated := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, tc.maxAdded, tc.block, tc.crit))
			g := single.Gate

			if repeated.Gate.Verdict() != g.Verdict() {
				t.Fatalf("verdict %s, single-run gate %s", repeated.Gate.Verdict(), g.Verdict())
			}
			minimum := checkNamed(repeated, platform.CheckRepetitionsFailingMinimum)
			if minimum.Passed != (g.ReferenceEvidence().Passed && g.CandidateEvidence().Passed) {
				t.Errorf("minimum-evidence check passed=%v, single-run evidence checks %v/%v",
					minimum.Passed, g.ReferenceEvidence().Passed, g.CandidateEvidence().Passed)
			}
			for _, pair := range []struct {
				repeated platform.RepeatedGateCheck
				single   platform.MaximumCountGate
			}{
				{checkNamed(repeated, platform.CheckRepeatedlyAddedBehaviors), g.AddedBehaviors()},
				{checkNamed(repeated, platform.CheckWorstCandidateBlockDecisions), g.BlockDecisions()},
				{checkNamed(repeated, platform.CheckWorstCandidateCriticalRiskCount), g.CriticalRiskObservations()},
			} {
				if pair.repeated.Actual != pair.single.Actual || pair.repeated.Bound != pair.single.Maximum ||
					pair.repeated.Passed != pair.single.Passed {
					t.Errorf("%s = %d/%d/%v, single-run %d/%d/%v", pair.repeated.Name,
						pair.repeated.Actual, pair.repeated.Bound, pair.repeated.Passed,
						pair.single.Actual, pair.single.Maximum, pair.single.Passed)
				}
				if pair.repeated.Advisory != "" {
					t.Errorf("%s carries advisory %q at N = 1", pair.repeated.Name, pair.repeated.Advisory)
				}
			}
		})
	}
}

// A behavior in exactly k of N candidate runs FAILs at threshold k and PASSes
// at k + 1, with nothing else changed.
func TestABehaviorInKOfNRunsFailsAtKAndPassesAtKPlusOne(t *testing.T) {
	f := newFixture(t)
	base := repetition{behaviors: []string{"read"}}
	withExport := repetition{behaviors: []string{"read", "export"}}
	refs, cands := f.sides(t, "kk",
		[]repetition{base, base, base, base, base},
		[]repetition{withExport, withExport, withExport, base, base}) // export in 3 of 5

	atK := compareRepeated(t, f, refs, cands, repeatedLimits(3, 0, 0, 0, 0))
	atKPlusOne := compareRepeated(t, f, refs, cands, repeatedLimits(4, 0, 0, 0, 0))

	if p := presenceOf(atK, "export"); p.CandidateRunsPresent != 3 || p.ReferenceRunsPresent != 0 {
		t.Fatalf("export presence = %d/%d, want 0 reference and 3 candidate",
			p.ReferenceRunsPresent, p.CandidateRunsPresent)
	}
	if atK.Gate.Verdict() != platform.GateVerdictFail ||
		presenceOf(atK, "export").Classification != platform.RepeatedAdded {
		t.Errorf("k = 3: verdict %s, classification %s; want fail and added",
			atK.Gate.Verdict(), presenceOf(atK, "export").Classification)
	}
	if atKPlusOne.Gate.Verdict() != platform.GateVerdictPass ||
		presenceOf(atKPlusOne, "export").Classification != platform.RepeatedNeither {
		t.Errorf("k = 4: verdict %s, classification %s; want pass and neither",
			atKPlusOne.Gate.Verdict(), presenceOf(atKPlusOne, "export").Classification)
	}
}

// Presence counts do not depend on the order of either side's repetitions.
func TestPresenceIsInvariantUnderPermutationOfEitherSide(t *testing.T) {
	f := newFixture(t)
	refs, cands := f.sides(t, "perm",
		[]repetition{{behaviors: []string{"a", "b"}}, {behaviors: []string{"a"}}, {behaviors: []string{"a", "c"}}},
		[]repetition{{behaviors: []string{"a", "x"}}, {behaviors: []string{"b"}}, {behaviors: []string{"a", "x", "c"}}})
	limits := repeatedLimits(1, 0, 10, 0, 0)
	want := compareRepeated(t, f, refs, cands, limits)

	permutations := [][2][]platform.EvaluationRunID{
		{{refs[2], refs[0], refs[1]}, cands},
		{refs, {cands[1], cands[2], cands[0]}},
		{{refs[1], refs[2], refs[0]}, {cands[2], cands[1], cands[0]}},
	}
	for i, p := range permutations {
		got := compareRepeated(t, f, p[0], p[1], limits)
		if fmt.Sprint(got.Behaviors) != fmt.Sprint(want.Behaviors) {
			t.Errorf("permutation %d changed presence:\n got  %v\n want %v", i, got.Behaviors, want.Behaviors)
		}
		if got.Gate.Verdict() != want.Gate.Verdict() ||
			fmt.Sprint(got.Gate.Checks()) != fmt.Sprint(want.Gate.Checks()) {
			t.Errorf("permutation %d changed the gate", i)
		}
	}
}

// The documented guidance k = 1, j = 0 is task 054's set semantics at every N.
func TestKOneJZeroIsSetSemanticsAtEveryN(t *testing.T) {
	f := newFixture(t)
	refs, cands := f.sides(t, "set",
		[]repetition{{behaviors: []string{"a"}}, {behaviors: []string{"a", "shared"}}, {behaviors: []string{"a"}},
			{behaviors: []string{"a"}}, {behaviors: []string{"a"}}},
		[]repetition{{behaviors: []string{"a"}}, {behaviors: []string{"a", "once"}}, {behaviors: []string{"a", "shared"}},
			{behaviors: []string{"a"}}, {behaviors: []string{"a"}}})
	result := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 10, 0, 0))
	// In at least one candidate run and no reference run: added. Seen in any
	// reference run: not added, however often the candidate shows it.
	if c := presenceOf(result, "once").Classification; c != platform.RepeatedAdded {
		t.Errorf("once: %s, want added", c)
	}
	if c := presenceOf(result, "shared").Classification; c != platform.RepeatedNeither {
		t.Errorf("shared: %s, want neither", c)
	}
}

// The control plane refuses repetitions that shared a learning scope.
func TestRepetitionsSharingAProfileAreRefused(t *testing.T) {
	f := newFixture(t)
	f.runRepetition(t, "iso-ref-1", "shared-profile", repetition{behaviors: []string{"a"}}, true)
	f.runRepetition(t, "iso-can-1", "shared-profile", repetition{behaviors: []string{"a"}}, true)
	_, err := f.plane.CompareRepeatedEvaluations(t.Context(), platform.RepeatedEvaluationRequest{
		ReferenceRunIDs: []platform.EvaluationRunID{"iso-ref-1"},
		CandidateRunIDs: []platform.EvaluationRunID{"iso-can-1"},
		Limits:          repeatedLimits(1, 0, 0, 0, 0)})
	if !errors.Is(err, platform.ErrRepeatedIsolation) {
		t.Errorf("error = %v, want ErrRepeatedIsolation", err)
	}
}

// A repetition that did not complete fails check 2; it is a result, with all six
// checks populated, not an error.
func TestAnUncompletedRepetitionFailsTheCompletionCheck(t *testing.T) {
	f := newFixture(t)
	refs, cands := f.sides(t, "unc", []repetition{{behaviors: []string{"a"}}, {behaviors: []string{"a"}}},
		[]repetition{{behaviors: []string{"a"}}})
	f.runRepetition(t, "unc-can-2", "unc-can-2-profile", repetition{behaviors: []string{"a"}}, false)
	cands = append(cands, "unc-can-2")

	result := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 0, 0, 0))
	if len(result.Gate.Checks()) != 6 {
		t.Fatalf("%d checks, want all six", len(result.Gate.Checks()))
	}
	c := checkNamed(result, platform.CheckCandidateRepetitionsCompleted)
	if c.Actual != 1 || c.Bound != 2 || c.Passed || result.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("candidate completion = %d of %d passed=%v verdict %s; want 1 of 2, fail",
			c.Actual, c.Bound, c.Passed, result.Gate.Verdict())
	}
	if checkNamed(result, platform.CheckReferenceRepetitionsCompleted).Passed != true {
		t.Error("reference completion should still pass")
	}
}

// One empty repetition fails check 3 rather than being averaged away.
func TestOneEmptyRepetitionFailsMinimumEvidence(t *testing.T) {
	f := newFixture(t)
	refs, cands := f.sides(t, "empty",
		[]repetition{{behaviors: []string{"a"}}, {behaviors: []string{"a"}}, {behaviors: []string{"a"}}},
		[]repetition{{behaviors: []string{"a"}}, {}, {behaviors: []string{"a"}}})
	result := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 0, 0, 0))
	if c := checkNamed(result, platform.CheckRepetitionsFailingMinimum); c.Actual != 1 || c.Passed {
		t.Errorf("failing-minimum = %d passed=%v, want 1 and fail", c.Actual, c.Passed)
	}
}

// Checks 5 and 6 compare the worst candidate repetition: never a sum (two runs
// with one block each pass a limit of 1) and never a mean (one run with two
// blocks fails a limit of 1 however clean the rest are).
func TestEngineChecksTakeTheWorstRepetition(t *testing.T) {
	f := newFixture(t)
	clean := repetition{behaviors: []string{"a"}}
	oneBlock := repetition{behaviors: []string{"a"}, blocks: 1}
	twoBlocks := repetition{behaviors: []string{"a", "b"}, blocks: 2}
	refs, cands := f.sides(t, "max",
		[]repetition{clean, clean, clean, clean, clean},
		[]repetition{oneBlock, oneBlock, clean, clean, clean})
	sumWouldFail := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 10, 1, 0))
	if c := checkNamed(sumWouldFail, platform.CheckWorstCandidateBlockDecisions); c.Actual != 1 || !c.Passed {
		t.Errorf("two runs with one block each: actual %d passed=%v; want 1, pass (not a sum)", c.Actual, c.Passed)
	}

	refs2, cands2 := f.sides(t, "max2",
		[]repetition{clean, clean, clean, clean, clean},
		[]repetition{twoBlocks, clean, clean, clean, clean})
	meanWouldPass := compareRepeated(t, f, refs2, cands2, repeatedLimits(1, 0, 10, 1, 0))
	c := checkNamed(meanWouldPass, platform.CheckWorstCandidateBlockDecisions)
	if c.Actual != 2 || c.Passed || meanWouldPass.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("one run with two blocks: actual %d passed=%v verdict %s; want 2, fail (not a mean)",
			c.Actual, c.Passed, meanWouldPass.Gate.Verdict())
	}
	// And the advisory marker is present at N > 1 without changing that verdict.
	if c.Advisory != platform.AdvisoryFreshScope {
		t.Errorf("advisory = %q at N = 5, want %q", c.Advisory, platform.AdvisoryFreshScope)
	}
	for _, other := range meanWouldPass.Gate.Checks()[:4] {
		if other.Advisory != "" {
			t.Errorf("%s carries an advisory marker; only checks 5 and 6 do", other.Name)
		}
	}
}

// A repeatedly removed behavior is classified and gates nothing.
func TestARepeatedlyRemovedBehaviorChangesNoVerdict(t *testing.T) {
	f := newFixture(t)
	refs, cands := f.sides(t, "rem",
		[]repetition{{behaviors: []string{"a", "gone"}}, {behaviors: []string{"a", "gone"}}},
		[]repetition{{behaviors: []string{"a"}}, {behaviors: []string{"a"}}})
	result := compareRepeated(t, f, refs, cands, repeatedLimits(1, 0, 0, 0, 0))
	if c := presenceOf(result, "gone").Classification; c != platform.RepeatedRemoved {
		t.Errorf("gone: %s, want removed", c)
	}
	if result.Gate.Verdict() != platform.GateVerdictPass {
		t.Errorf("verdict %s; a removed behavior must gate nothing", result.Gate.Verdict())
	}
}

// Every bound is refused before any run is loaded, naming the field.
func TestRepeatedRequestBoundsAreRefused(t *testing.T) {
	f := newFixture(t)
	ids := func(prefix string, n int) []platform.EvaluationRunID {
		out := make([]platform.EvaluationRunID, n)
		for i := range out {
			out[i] = platform.EvaluationRunID(fmt.Sprintf("%s-%d", prefix, i))
		}
		return out
	}
	cases := []struct {
		name       string
		refs, cand []platform.EvaluationRunID
		k, j       uint64
	}{
		{"no runs", nil, nil, 1, 0},
		{"65 runs", ids("r", 65), ids("c", 65), 1, 0},
		{"unequal sides", ids("r", 2), ids("c", 3), 1, 0},
		{"a run named twice", []platform.EvaluationRunID{"same"}, []platform.EvaluationRunID{"same"}, 1, 0},
		{"k = 0", ids("r", 2), ids("c", 2), 0, 0},
		{"k = N + 1", ids("r", 2), ids("c", 2), 3, 0},
		{"j = k", ids("r", 3), ids("c", 3), 2, 2},
		{"j = N + 1", ids("r", 3), ids("c", 3), 2, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.plane.CompareRepeatedEvaluations(t.Context(), platform.RepeatedEvaluationRequest{
				ReferenceRunIDs: tc.refs, CandidateRunIDs: tc.cand,
				Limits: repeatedLimits(tc.k, tc.j, 0, 0, 0)})
			if !errors.Is(err, platform.ErrInvalidRepeatedRequest) {
				t.Errorf("error = %v, want ErrInvalidRepeatedRequest", err)
			}
		})
	}
	// k = N and j = k - 1 are accepted bounds; the runs are then simply missing.
	_, err := f.plane.CompareRepeatedEvaluations(t.Context(), platform.RepeatedEvaluationRequest{
		ReferenceRunIDs: ids("r", 2), CandidateRunIDs: ids("c", 2),
		Limits: repeatedLimits(2, 1, 0, 0, 0)})
	if !errors.Is(err, platform.ErrStoreNotFound) {
		t.Errorf("k = N, j = k - 1 with missing runs: error = %v, want ErrStoreNotFound", err)
	}
}
