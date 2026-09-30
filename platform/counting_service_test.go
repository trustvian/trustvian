package platform_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"trustvian-platform"
)

// The counting correction through the whole service, over a real SQLite
// store (task 083, ADR 0052).
//
// counting_test.go exercises the fold over the relation directly. These
// exercise the path a caller actually takes: records are ingested, retained,
// read back from durable rows, correlated, and reported alongside the gate —
// which is where a fold that worked in isolation could still fail to reach
// the number a developer sees.

// correlatedRecord builds a record carrying the span identity the fold reads.
//
// target is explicit because two fingerprints must describe two different
// behaviors: the platform refuses a pair whose descriptors are identical, and
// it is right to — that pair would be one behavior under two identities. In
// the case this file is about, the target is exactly what differs.
func correlatedRecord(
	eventID, fingerprintID, operation, target, trace, span, parent string,
) trustvian.DecisionRecord {
	rec := ingestRecord(eventID, fingerprintID, operation)
	rec.Behavior.TargetName = target
	rec.TraceID = trace
	rec.SpanID = span
	rec.ParentSpanID = parent
	if parent == "" {
		rec.SpanLineage = "root"
	} else {
		rec.SpanLineage = "child"
	}
	return rec
}

// completeCorrelatedRun drives one run end to end with records that carry
// span identity, which completeEvaluation's operation-name helper cannot.
func (f *controlPlaneFixture) completeCorrelatedRun(
	t *testing.T, runID platform.EvaluationRunID, candidateID platform.CandidateID,
	records []trustvian.DecisionRecord,
) {
	t.Helper()
	ctx := t.Context()

	project, _ := platform.NewProject("proj-1", "Checkout")
	agent, _ := platform.NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := platform.NewCandidate(candidateID, "agent-1", platform.CandidateMetadata{Label: "v1"})
	environment, _ := platform.NewEnvironment(fixtureEnvironment, "proj-1", "Staging")
	for _, err := range []error{
		f.plane.CreateProject(ctx, project),
		f.plane.CreateAgent(ctx, agent),
		f.plane.CreateCandidate(ctx, candidate),
		f.plane.CreateEnvironment(ctx, environment),
	} {
		if err != nil && !errors.Is(err, platform.ErrStoreAlreadyExists) {
			t.Fatalf("seed error = %v", err)
		}
	}

	run, err := platform.NewEvaluationRun(runID, candidateID, fixtureEnvironment, fixtureProfile, aggEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := f.plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := f.plane.StartEvaluationRun(ctx, runID, aggEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}

	for i, rec := range records {
		if _, err := f.ingest(t, runID, uint64(i+1), rec); err != nil {
			t.Fatalf("ingest %d error = %v", i, err)
		}
	}
	if _, err := f.plane.CompleteEvaluationRun(ctx, runID, aggEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}

func compareRuns(
	t *testing.T, f *controlPlaneFixture, reference, candidate platform.EvaluationRunID,
) platform.EvaluationComparison {
	t.Helper()
	comparison, err := f.plane.CompareEvaluations(t.Context(), reference, candidate,
		platform.EvaluationGateLimits{
			MaxAddedBehaviors:           1,
			MaxBlockDecisions:           10,
			MaxCriticalRiskObservations: 10,
		})
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}
	return comparison
}

// TestOneNewToolCountsAsOneChangeThroughTheService is the defect, closed end
// to end.
//
// The reference observed one unrelated behavior. The candidate added a tool
// and the transport child it calls — two identities, one act. The comparison
// must report both numbers, and they must disagree, because that disagreement
// is the whole correction.
func TestOneNewToolCountsAsOneChangeThroughTheService(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", []trustvian.DecisionRecord{
		correlatedRecord("ref-1", "fp-read", "read", "store", "trace-r", "span-r", ""),
	})
	f.completeCorrelatedRun(t, "run-can", "cand-can", []trustvian.DecisionRecord{
		correlatedRecord("can-1", "fp-read", "read", "store", "trace-c", "span-c", ""),
		correlatedRecord("can-2", "fp-tool", "export_customer", "", "trace-c", "span-tool", "span-c"),
		correlatedRecord("can-3", "fp-http", "post", "export.localhost", "trace-c", "span-http", "span-tool"),
	})

	comparison := compareRuns(t, f, "run-ref", "run-can")
	diff := comparison.Diff

	if got := diff.AddedCount(); got != 2 {
		t.Fatalf("added identities = %d, want 2; identity must not have folded", got)
	}
	if got := diff.AddedChangeCount(); got != 1 {
		t.Errorf("counted changes = %d, want 1; one act is one change", got)
	}
	if got := diff.CorrelationState(); got != platform.CorrelationComplete {
		t.Errorf("correlation = %q, want complete; the run's history is fully retained", got)
	}
	if got := diff.CountingPolicyVersion(); got != platform.CountingPolicyVersion {
		t.Errorf("policy version = %q, want %q", got, platform.CountingPolicyVersion)
	}

	// The change names both contributing identities, so each resolves to its
	// observations through the existing evidence routes.
	changes := diff.AddedChanges()
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(changes))
	}
	if changes[0].RootFingerprintID != "fp-tool" {
		t.Errorf("root = %q, want fp-tool", changes[0].RootFingerprintID)
	}
	if want := []string{"fp-http", "fp-tool"}; !slices.Equal(changes[0].ContributingFingerprintIDs, want) {
		t.Errorf("contributors = %v, want %v", changes[0].ContributingFingerprintIDs, want)
	}

	// The gate still counts identities, so it still fails a limit of one.
	// That is the compatibility promise: the correction reports a second
	// number and moves no existing verdict.
	if comparison.Gate.Verdict() != platform.GateVerdictFail {
		t.Errorf("verdict = %q, want fail; max_added_behaviors=1 against 2 added "+
			"identities must still fail", comparison.Gate.Verdict())
	}
	if got := comparison.Gate.AddedBehaviors().Actual; got != 2 {
		t.Errorf("gate saw %d added behaviors, want the identity count 2; the gate "+
			"unit is unchanged", got)
	}
}

// TestAKnownToolChangingDestinationStillReachesTheGate is ADR 0047's case,
// end to end.
func TestAKnownToolChangingDestinationStillReachesTheGate(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", []trustvian.DecisionRecord{
		correlatedRecord("ref-1", "fp-tool", "export_customer", "", "trace-r", "span-tool", ""),
		correlatedRecord("ref-2", "fp-http-good", "post", "export.localhost", "trace-r", "span-http", "span-tool"),
	})
	// Same tool, new destination: only the transport identity is added.
	f.completeCorrelatedRun(t, "run-can", "cand-can", []trustvian.DecisionRecord{
		correlatedRecord("can-1", "fp-tool", "export_customer", "", "trace-c", "span-tool", ""),
		correlatedRecord("can-2", "fp-http-bad", "post", "attacker.example", "trace-c", "span-http", "span-tool"),
	})

	diff := compareRuns(t, f, "run-ref", "run-can").Diff

	if got := diff.AddedCount(); got != 1 {
		t.Fatalf("added identities = %d, want 1", got)
	}
	if got := diff.AddedChangeCount(); got != 1 {
		t.Errorf("counted changes = %d, want 1. The new transport identity's parent "+
			"is present in both runs, so it is not an *added* parent and the change "+
			"must not fold into it — this is the detection ADR 0047 protected", got)
	}
	changes := diff.AddedChanges()
	if len(changes) != 1 || changes[0].RootFingerprintID != "fp-http-bad" {
		t.Errorf("changes = %+v, want one rooted at the new destination", changes)
	}
}

// TestAComparisonIsReproducibleAfterRestart covers durability.
//
// Correlation is derived from retained rows rather than held in memory, so
// reopening the store and comparing again must produce the same counts. If it
// did not, a stored gate result and a re-run of the same comparison could
// disagree.
func TestAComparisonIsReproducibleAfterRestart(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", []trustvian.DecisionRecord{
		correlatedRecord("ref-1", "fp-read", "read", "store", "trace-r", "span-r", ""),
	})
	f.completeCorrelatedRun(t, "run-can", "cand-can", []trustvian.DecisionRecord{
		correlatedRecord("can-1", "fp-read", "read", "store", "trace-c", "span-c", ""),
		correlatedRecord("can-2", "fp-tool", "export_customer", "", "trace-c", "span-tool", "span-c"),
		correlatedRecord("can-3", "fp-http", "post", "export.localhost", "trace-c", "span-http", "span-tool"),
	})
	before := compareRuns(t, f, "run-ref", "run-can").Diff

	// Reopen the same database through a new service.
	f.store.Close()
	reopened := openFixture(t, f.path)
	after := compareRuns(t, reopened, "run-ref", "run-can").Diff

	if before.AddedCount() != after.AddedCount() {
		t.Errorf("added identities %d then %d", before.AddedCount(), after.AddedCount())
	}
	if before.AddedChangeCount() != after.AddedChangeCount() {
		t.Errorf("counted changes %d then %d; correlation is derived from durable "+
			"rows and must survive a restart",
			before.AddedChangeCount(), after.AddedChangeCount())
	}
	if before.CorrelationState() != after.CorrelationState() {
		t.Errorf("correlation %q then %q", before.CorrelationState(), after.CorrelationState())
	}
	if !slices.EqualFunc(before.AddedChanges(), after.AddedChanges(),
		func(a, b platform.BehaviorChange) bool {
			return a.RootFingerprintID == b.RootFingerprintID &&
				slices.Equal(a.ContributingFingerprintIDs, b.ContributingFingerprintIDs)
		}) {
		t.Errorf("changes differ after restart:\n before %+v\n after  %+v",
			before.AddedChanges(), after.AddedChanges())
	}
}

// TestRecordsWithNoSpanIdentityCountAsIdentities covers the hand-built Event.
//
// A caller constructing records without span identity gets no parentage, and
// the change count is the identity count. Not an error and not a fold — the
// documented fallback, reported as complete correlation because the run's
// history *is* fully retained; there is simply nothing in it to correlate.
func TestRecordsWithNoSpanIdentityCountAsIdentities(t *testing.T) {
	f := newFixture(t)
	f.completeCorrelatedRun(t, "run-ref", "cand-ref", []trustvian.DecisionRecord{
		ingestRecord("ref-1", "fp-read", "read"),
	})
	f.completeCorrelatedRun(t, "run-can", "cand-can", []trustvian.DecisionRecord{
		ingestRecord("can-1", "fp-read", "read"),
		ingestRecord("can-2", "fp-tool", "export_customer"),
		ingestRecord("can-3", "fp-http", "post"),
	})

	diff := compareRuns(t, f, "run-ref", "run-can").Diff
	if got := diff.AddedChangeCount(); got != diff.AddedCount() {
		t.Errorf("counted changes = %d over %d added identities; with no recorded "+
			"parentage nothing may fold", got, diff.AddedCount())
	}
	if got := diff.AddedChangeCount(); got != 2 {
		t.Errorf("counted changes = %d, want 2", got)
	}
}
