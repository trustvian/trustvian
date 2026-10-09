package platform

// Task 106's three optional gates: every state of every check, and the
// verdict each produces.

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func frequencyCheck(t *testing.T, c RepeatedEvaluationComparison, name FrequencyCheckName) FrequencyGateCheck {
	t.Helper()
	for _, check := range c.Gate.FrequencyChecks() {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check %s not reported", name)
	return FrequencyGateCheck{}
}

// lenientLimits passes the six 078 checks on the fixture, so the verdict
// moves only with the frequency check under test.
func lenientLimits() RepeatedEvaluationGateLimits {
	return RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: 2, AddedReferencePresenceMaximum: 0,
		MaxRepeatedAddedBehaviors: 100, MaxBlockDecisionsPerRun: 100, MaxCriticalRiskObservationsPerRun: 100,
	}
}

func reduceFixture(t *testing.T, limits RepeatedEvaluationGateLimits) RepeatedEvaluationComparison {
	t.Helper()
	refs, cands := frequencyFixture()
	c, err := reduceRepeated(limits, 3, append(refs, cands...))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFrequencyGateStates(t *testing.T) {
	// The fixture: crm in 3/3 both sides; send in 3/3 reference and 1/3
	// candidate (lost); kb in 2/3 reference; new in 3/3 candidate only.
	// Candidate calls per run to t-c: 9, 9, 8.
	with := func(set func(*RepeatedEvaluationGateLimits)) RepeatedEvaluationGateLimits {
		l := lenientLimits()
		set(&l)
		return l
	}
	tests := []struct {
		name    string
		limits  RepeatedEvaluationGateLimits
		check   FrequencyCheckName
		state   GateCheckState
		actual  uint64
		passed  bool
		verdict GateVerdict
	}{
		{"min frequency omitted", lenientLimits(), CheckMinCandidateFrequency, GateCheckNotEvaluated, 0, false, GateVerdictPass},
		// Behaviors in every reference run: crm (3) and send (1). Minimum 1.
		{"min frequency satisfied", with(func(l *RepeatedEvaluationGateLimits) {
			l.MinCandidateFrequency = NewOptionalGateLimit(1)
		}), CheckMinCandidateFrequency, GateCheckEvaluated, 1, true, GateVerdictPass},
		{"min frequency violated", with(func(l *RepeatedEvaluationGateLimits) {
			l.MinCandidateFrequency = NewOptionalGateLimit(2)
		}), CheckMinCandidateFrequency, GateCheckEvaluated, 1, false, GateVerdictFail},
		{"lost omitted", lenientLimits(), CheckMaxLostBehaviors, GateCheckNotEvaluated, 0, false, GateVerdictPass},
		{"lost satisfied", with(func(l *RepeatedEvaluationGateLimits) {
			l.MaxLostBehaviors = NewOptionalGateLimit(1)
		}), CheckMaxLostBehaviors, GateCheckEvaluated, 1, true, GateVerdictPass},
		{"lost violated at zero", with(func(l *RepeatedEvaluationGateLimits) {
			l.MaxLostBehaviors = NewOptionalGateLimit(0)
		}), CheckMaxLostBehaviors, GateCheckEvaluated, 1, false, GateVerdictFail},
		{"calls omitted", lenientLimits(), CheckMaxCallsPerRun, GateCheckNotEvaluated, 0, false, GateVerdictPass},
		{"calls satisfied", with(func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: "t-c", Max: 9}}
		}), CheckMaxCallsPerRun, GateCheckEvaluated, 0, true, GateVerdictPass},
		{"calls violated", with(func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: "t-c", Max: 8}}
		}), CheckMaxCallsPerRun, GateCheckEvaluated, 0, false, GateVerdictFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := reduceFixture(t, tt.limits)
			got := frequencyCheck(t, c, tt.check)
			if got.State != tt.state || got.Passed != tt.passed ||
				(tt.check != CheckMaxCallsPerRun && got.Actual != tt.actual) {
				t.Errorf("check = %+v; want state %s actual %d passed %v", got, tt.state, tt.actual, tt.passed)
			}
			if v := c.Gate.Verdict(); v != tt.verdict {
				t.Errorf("verdict = %s, want %s", v, tt.verdict)
			}
			if got.State == GateCheckNotEvaluated && (got.Rule != "" || got.MissingEvidence != "" || got.Targets != nil) {
				t.Errorf("a check that did not run carries an outcome: %+v", got)
			}
		})
	}
}

// TestDeferredChecksFailTheVerdict is D1: a configured limit whose evidence is
// absent is deferred, names what was missing, and fails.
func TestDeferredChecksFailTheVerdict(t *testing.T) {
	failedCandidate := completedInput(SideCandidate, 1)
	failedCandidate.evidence.Status = RunFailed
	failedCandidate.entries = nil
	all := RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: 1, MaxRepeatedAddedBehaviors: 100,
		MaxBlockDecisionsPerRun: 100, MaxCriticalRiskObservationsPerRun: 100,
		MinCandidateFrequency: NewOptionalGateLimit(1), MaxLostBehaviors: NewOptionalGateLimit(5),
		MaxCallsPerRun:    []TargetCallLimit{{Target: "t-c", Max: 100}},
		MaxLLMCallsPerRun: NewOptionalGateLimit(10),
	}
	c, err := reduceRepeated(all, 1, []repetitionInput{
		runWith(SideReference, 1, map[string]uint64{"crm": 1}), failedCandidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range c.Gate.FrequencyChecks() {
		if check.State != GateCheckDeferred || !strings.Contains(check.MissingEvidence, "candidate") {
			t.Errorf("%s = %+v, want deferred naming the candidate side", check.Name, check)
		}
	}
	if c.Gate.Verdict() != GateVerdictFail {
		t.Errorf("verdict = %s, want fail", c.Gate.Verdict())
	}

	// Evidence present on both sides, but no behavior in every reference run:
	// min_candidate_frequency has nothing to measure, and says so.
	c, err = reduceRepeated(RepeatedEvaluationGateLimits{
		AddedCandidatePresenceMinimum: 1, MaxRepeatedAddedBehaviors: 100,
		MaxBlockDecisionsPerRun: 100, MaxCriticalRiskObservationsPerRun: 100,
		MinCandidateFrequency: NewOptionalGateLimit(1),
	}, 2, []repetitionInput{
		runWith(SideReference, 1, map[string]uint64{"crm": 1}),
		runWith(SideReference, 2, map[string]uint64{"kb": 1}),
		runWith(SideCandidate, 1, map[string]uint64{"crm": 1}),
		runWith(SideCandidate, 2, map[string]uint64{"kb": 1}),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := frequencyCheck(t, c, CheckMinCandidateFrequency)
	if got.State != GateCheckDeferred || !strings.Contains(got.MissingEvidence, "every reference run") {
		t.Errorf("min frequency = %+v, want deferred", got)
	}
	if c.Gate.Verdict() != GateVerdictFail {
		t.Errorf("verdict = %s, want fail", c.Gate.Verdict())
	}
}

// TestMaxCallsPerRunReportsEveryTargetInConfigurationOrder: one named check,
// per-target evidence in the order configured, and a target neither side
// called — a typo — is not_observed and fails the check.
func TestMaxCallsPerRunReportsEveryTargetInConfigurationOrder(t *testing.T) {
	l := lenientLimits()
	l.MaxCallsPerRun = []TargetCallLimit{
		{Target: "t-s", Max: 1},       // the candidate called send once, in one run
		{Target: "t-c", Max: 10},      // at most 9 per run
		{Target: "t-k", Max: 0},       // only the reference called kb
		{Target: "t-typo", Max: 1000}, // nobody called it
	}
	c := reduceFixture(t, l)
	got := frequencyCheck(t, c, CheckMaxCallsPerRun)
	want := []TargetCallCheck{
		{Target: "t-s", Actual: 1, Max: 1, Outcome: TargetCallPass},
		{Target: "t-c", Actual: 9, Max: 10, Outcome: TargetCallPass},
		{Target: "t-k", Actual: 0, Max: 0, Outcome: TargetCallPass},
		{Target: "t-typo", Actual: 0, Max: 1000, Outcome: TargetCallNotObserved},
	}
	if fmt.Sprint(got.Targets) != fmt.Sprint(want) {
		t.Errorf("targets = %+v\nwant %+v", got.Targets, want)
	}
	if got.Passed || c.Gate.Verdict() != GateVerdictFail {
		t.Errorf("a not-observed target passed the check: %+v, verdict %s", got, c.Gate.Verdict())
	}
}

func TestFrequencyLimitsAreValidated(t *testing.T) {
	base := RepeatedEvaluationRequest{
		ReferenceRunIDs: []EvaluationRunID{"r1", "r2"}, CandidateRunIDs: []EvaluationRunID{"c1", "c2"},
		Limits: repeatedTestLimits(1, 0),
	}
	targets := func(n int) []TargetCallLimit {
		out := make([]TargetCallLimit, n)
		for i := range out {
			out[i] = TargetCallLimit{Target: fmt.Sprintf("t%d", i), Max: 1}
		}
		return out
	}
	tests := []struct {
		name string
		set  func(*RepeatedEvaluationGateLimits)
		ok   bool
	}{
		{"min frequency at N", func(l *RepeatedEvaluationGateLimits) { l.MinCandidateFrequency = NewOptionalGateLimit(2) }, true},
		{"min frequency past N", func(l *RepeatedEvaluationGateLimits) { l.MinCandidateFrequency = NewOptionalGateLimit(3) }, false},
		{"sixteen targets", func(l *RepeatedEvaluationGateLimits) { l.MaxCallsPerRun = targets(16) }, true},
		{"the seventeenth target", func(l *RepeatedEvaluationGateLimits) { l.MaxCallsPerRun = targets(17) }, false},
		{"an empty list", func(l *RepeatedEvaluationGateLimits) { l.MaxCallsPerRun = []TargetCallLimit{} }, false},
		{"a duplicate target", func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: "a", Max: 1}, {Target: "a", Max: 2}}
		}, false},
		{"an empty target", func(l *RepeatedEvaluationGateLimits) { l.MaxCallsPerRun = []TargetCallLimit{{Target: ""}} }, false},
		{"a 255-byte target", func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: strings.Repeat("a", 255)}}
		}, true},
		{"a 256-byte target", func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: strings.Repeat("a", 256)}}
		}, false},
		{"a control character", func(l *RepeatedEvaluationGateLimits) {
			l.MaxCallsPerRun = []TargetCallLimit{{Target: "a\nb"}}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.set(&r.Limits)
			err := r.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok && !errors.Is(err, ErrInvalidRepeatedRequest) {
				t.Fatalf("Validate() = %v, want ErrInvalidRepeatedRequest", err)
			}
		})
	}
}

// TestOmittedFrequencyLimitsLeaveTheVerdictAlone is the degradation contract:
// with the new limits omitted, every fixture's verdict is the six 078 checks'
// verdict, check for check.
func TestOmittedFrequencyLimitsLeaveTheVerdictAlone(t *testing.T) {
	refs, cands := frequencyFixture()
	for _, limits := range []RepeatedEvaluationGateLimits{
		repeatedTestLimits(1, 0), repeatedTestLimits(2, 0), repeatedTestLimits(3, 2), lenientLimits(),
	} {
		c, err := reduceRepeated(limits, 3, append(refs, cands...))
		if err != nil {
			t.Fatal(err)
		}
		sixPass := true
		for _, check := range c.Gate.Checks() {
			sixPass = sixPass && check.Passed
		}
		want := GateVerdictFail
		if sixPass {
			want = GateVerdictPass
		}
		if c.Gate.Verdict() != want {
			t.Errorf("limits %+v: verdict %s, the six checks say %s", limits, c.Gate.Verdict(), want)
		}
		for _, check := range c.Gate.FrequencyChecks() {
			if check.State != GateCheckNotEvaluated {
				t.Errorf("limits %+v: %s is %s with its limit omitted", limits, check.Name, check.State)
			}
		}
	}
}
