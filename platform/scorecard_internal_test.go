package platform

// Structurally corrupt aggregate evidence, driven from inside the package.
//
// A scorecard is a trust boundary *between* evidence components, not only
// between the platform and its callers. Each aggregate is internally sound by
// construction — task 053 guarantees that its category counts and metric
// observation counts equal its record count — but the scorecard verifies
// those identities anyway, because an aggregate arriving from somewhere
// unexpected (a future deserializer, a hand-built value inside this package)
// would otherwise produce rates whose denominators disagree with their
// numerators.
//
// External callers cannot build these values, which is why the tests live
// here. Without them the guard would be unreachable code that looks like
// protection: removing it entirely leaves every external test passing.

import (
	"errors"
	"testing"
	"time"
)

const corruptEnvironment EnvironmentRef = "staging"

// corruptAggregate builds a bound aggregate whose internal counts can be
// tampered with independently of one another.
func corruptAggregate(runID EvaluationRunID, candidateID CandidateID, profile BehavioralProfileRef) EvaluationAggregate {
	return EvaluationAggregate{
		bound:       true,
		runID:       runID,
		candidateID: candidateID,
		environment: corruptEnvironment,
		profile:     profile,
	}
}

// pairedDiff returns a diff describing the two aggregates, so the only thing
// wrong in each case below is the aggregate's own arithmetic.
func pairedDiff(reference, candidate EvaluationAggregate) BehaviorDiff {
	return BehaviorDiff{
		referenceRunID:        reference.runID,
		referenceCandidateID:  reference.candidateID,
		referenceProfile:      reference.profile,
		referenceObservations: reference.recordCount,
		candidateRunID:        candidate.runID,
		candidateCandidateID:  candidate.candidateID,
		candidateProfile:      candidate.profile,
		candidateObservations: candidate.recordCount,
		environment:           corruptEnvironment,
	}
}

func TestScorecardRejectsStructurallyImpossibleAggregates(t *testing.T) {
	// A consistent baseline: one record, counted once everywhere.
	consistent := func(runID EvaluationRunID, candidateID CandidateID, profile BehavioralProfileRef) EvaluationAggregate {
		a := corruptAggregate(runID, candidateID, profile)
		a.recordCount = 1
		a.decisions.Allow = 1
		a.risks.Low = 1
		a.approvals.NotRequired = 1
		a.policy.MatchedDefault = 1
		one := MetricSummary{Count: 1, Sum: 0.5, Min: 0.5, Max: 0.5}
		a.identityConfidence, a.anomalyScore, a.anomalyConfidence = one, one, one
		a.trustScore, a.contextRisk = one, one
		return a
	}

	t.Run("the baseline is accepted", func(t *testing.T) {
		ref := consistent("run-r", "cand-r", "p-r")
		cand := consistent("run-c", "cand-c", "p-c")
		if _, err := NewEvaluationScorecard(ref, cand, pairedDiff(ref, cand)); err != nil {
			t.Fatalf("premise broken, the baseline should be valid: %v", err)
		}
	})

	corruptions := map[string]func(*EvaluationAggregate){
		"decision counts disagree with record count": func(a *EvaluationAggregate) {
			a.decisions.Allow = 0
		},
		"risk counts disagree": func(a *EvaluationAggregate) {
			a.risks.Low = 2
		},
		"approval counts disagree": func(a *EvaluationAggregate) {
			a.approvals.NotRequired = 0
		},
		"policy selection disagrees": func(a *EvaluationAggregate) {
			a.policy.MatchedDefault = 3
		},
		"a metric observed fewer records": func(a *EvaluationAggregate) {
			a.trustScore.Count = 0
		},
		"a metric observed more records": func(a *EvaluationAggregate) {
			a.anomalyScore.Count = 2
		},
	}

	for name, corrupt := range corruptions {
		t.Run("reference: "+name, func(t *testing.T) {
			ref := consistent("run-r", "cand-r", "p-r")
			cand := consistent("run-c", "cand-c", "p-c")
			diff := pairedDiff(ref, cand)
			corrupt(&ref)

			got, err := NewEvaluationScorecard(ref, cand, diff)
			if !errors.Is(err, ErrInvalidScorecardEvidence) {
				t.Fatalf("error = %v, want one wrapping ErrInvalidScorecardEvidence", err)
			}
			if got != (EvaluationScorecard{}) {
				t.Error("a refused construction returned a non-zero scorecard")
			}
		})

		t.Run("candidate: "+name, func(t *testing.T) {
			ref := consistent("run-r", "cand-r", "p-r")
			cand := consistent("run-c", "cand-c", "p-c")
			diff := pairedDiff(ref, cand)
			corrupt(&cand)

			if _, err := NewEvaluationScorecard(ref, cand, diff); !errors.Is(err, ErrInvalidScorecardEvidence) {
				t.Fatalf("error = %v, want one wrapping ErrInvalidScorecardEvidence", err)
			}
		})
	}
}

// TestScorecardBoundMarkerIsNotForgeable: a card is valid only when this
// package's constructor produced it, so task 056 can fail closed on one that
// arrived from anywhere else. Identity fields that merely look right are not
// evidence that validation ran.
func TestScorecardBoundMarkerIsNotForgeable(t *testing.T) {
	forged := EvaluationScorecard{
		referenceRunID:     "run-r",
		candidateRunID:     "run-c",
		referenceCandidate: "cand-r",
		candidateCandidate: "cand-c",
		environment:        corruptEnvironment,
	}
	if forged.bound {
		t.Fatal("a hand-built scorecard reports itself bound")
	}

	ref := corruptAggregate("run-r", "cand-r", "p-r")
	cand := corruptAggregate("run-c", "cand-c", "p-c")
	produced, err := NewEvaluationScorecard(ref, cand, pairedDiff(ref, cand))
	if err != nil {
		t.Fatalf("NewEvaluationScorecard() error = %v", err)
	}
	if !produced.bound {
		t.Error("the constructor produced an unbound scorecard")
	}

	// Copying preserves validity: the marker travels with the value.
	copied := produced
	if !copied.bound {
		t.Error("copying a scorecard lost its bound marker")
	}
}

// TestZeroAggregateIsNotAnEmptyEvaluation states the distinction that makes
// the bound check necessary: by value the two are identical, and by meaning
// they are opposites.
func TestZeroAggregateIsNotAnEmptyEvaluation(t *testing.T) {
	var zero EvaluationAggregate

	run, err := NewEvaluationRun("run-1", "cand-1", corruptEnvironment, "p-1",
		time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	empty, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}

	// Every count agrees; only the marker and identity differ.
	if zero.RecordCount() != empty.RecordCount() {
		t.Fatal("premise broken: the two differ in record count")
	}
	if zero.Decisions() != empty.Decisions() || zero.Risks() != empty.Risks() {
		t.Fatal("premise broken: the two differ in category counts")
	}
	if zero.bound == empty.bound {
		t.Fatal("the bound marker does not distinguish them, so nothing does")
	}
}
