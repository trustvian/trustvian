package platform

// Evidence resolution, task 085.
//
// The assertions that matter most here are the negative ones: that a resolution
// reads the side it says it read, that an empty answer over partial history is
// never reported as "nothing happened", and that no number returned was derived
// from the observations rather than from the recorded evidence.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// ---------------------------------------------------------------------
// Fixtures: a completed comparison with retained history on both sides
// ---------------------------------------------------------------------

// comparisonFixture is two completed runs under one candidate, each with
// retained observations, ready to be resolved against.
type comparisonFixture struct {
	store     *SQLiteStore
	plane     *ControlPlane
	reference EvaluationRun
	candidate EvaluationRun
}

func (f comparisonFixture) finding(check GateCheckName) FindingRef {
	return FindingRef{
		ReferenceRunID: f.reference.ID(),
		CandidateRunID: f.candidate.ID(),
		Check:          check,
	}
}

func (f comparisonFixture) behaviorFinding(fingerprintID string) FindingRef {
	return FindingRef{
		ReferenceRunID: f.reference.ID(),
		CandidateRunID: f.candidate.ID(),
		Behavior:       fingerprintID,
	}
}

// evidenceRecord builds a record with a chosen behavior and outcome.
func evidenceRecord(
	run EvaluationRun, eventID, fingerprintID, operation, decision, risk string,
) trustvian.DecisionRecord {
	record := observationTestRecord(run, eventID)
	record.FingerprintID = fingerprintID
	record.Behavior.OperationName = operation
	record.Decision = decision
	record.RiskLevel = risk
	return record
}

// newComparisonFixture builds the shape every test here resolves against.
//
// The reference run carries one behavior; the candidate carries that same
// behavior plus two it added, one of which blocked at critical risk. That is
// enough to exercise added_behaviors, block_decisions,
// critical_risk_observations and a shared behavior at once, without any test
// having to construct its own comparison.
func newComparisonFixture(t *testing.T) comparisonFixture {
	t.Helper()
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	// Reference: one shared behavior, observed twice.
	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-shared", "list_customers", "allow", "low"))
	ingestOne(t, plane, reference, 2,
		evidenceRecord(reference, "r2", "fp-shared", "list_customers", "allow", "low"))

	// Candidate: the shared behavior, plus two added ones.
	ingestOne(t, plane, candidate, 1,
		evidenceRecord(candidate, "c1", "fp-shared", "list_customers", "allow", "low"))
	ingestOne(t, plane, candidate, 2,
		evidenceRecord(candidate, "c2", "fp-export", "export_customer", "block", "critical"))
	ingestOne(t, plane, candidate, 3,
		evidenceRecord(candidate, "c3", "fp-delete", "delete_customer", "allow", "high"))
	ingestOne(t, plane, candidate, 4,
		evidenceRecord(candidate, "c4", "fp-export", "export_customer", "block", "low"))

	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	return comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}
}

func completeRun(t *testing.T, store *SQLiteStore, run EvaluationRun) {
	t.Helper()
	completed, err := run.Complete(run.CreatedAt().Add(time.Hour))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(t.Context(), run, completed); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}
}

// ---------------------------------------------------------------------
// Which checks resolve, and to what
// ---------------------------------------------------------------------

// added_behaviors resolves to exactly the behaviors the diff counted.
func TestResolveAddedBehaviors(t *testing.T) {
	f := newComparisonFixture(t)

	resolution, err := f.plane.ResolveFindingBehaviors(
		t.Context(), f.finding(CheckAddedBehaviors), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingBehaviors() error = %v", err)
	}

	if resolution.Status != ResolutionResolved {
		t.Errorf("status = %v, want resolved", resolution.Status)
	}
	if resolution.Side != SideCandidate {
		t.Errorf("side = %v, want candidate; added behaviors exist on the candidate",
			resolution.Side)
	}
	if resolution.RecordedCount != 2 {
		t.Errorf("recorded count = %d, want 2", resolution.RecordedCount)
	}
	if len(resolution.Behaviors) != 2 {
		t.Fatalf("returned %d behaviors, want 2", len(resolution.Behaviors))
	}

	// Sorted by fingerprint id, which is what makes the cursor stable.
	got := []string{resolution.Behaviors[0].FingerprintID, resolution.Behaviors[1].FingerprintID}
	want := []string{"fp-delete", "fp-export"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("behavior %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, b := range resolution.Behaviors {
		if b.Presence != BehaviorAdded {
			t.Errorf("%s has presence %q, want added", b.FingerprintID, b.Presence)
		}
	}

	// The recorded count is the diff's own number, not a count of the page.
	if int(resolution.RecordedCount) != len(resolution.Behaviors) {
		t.Logf("recorded %d, page %d — legitimate only when paging",
			resolution.RecordedCount, len(resolution.Behaviors))
	}
}

// block_decisions and critical_risk_observations resolve to observations
// selected by a recorded field, on the candidate side.
func TestResolveCountingChecksToObservations(t *testing.T) {
	f := newComparisonFixture(t)

	for _, tc := range []struct {
		check        GateCheckName
		wantRecorded uint64
		wantEvents   []string
	}{
		{CheckBlockDecisions, 2, []string{"c2", "c4"}},
		{CheckCriticalRiskObservations, 1, []string{"c2"}},
	} {
		t.Run(string(tc.check), func(t *testing.T) {
			resolution, err := f.plane.ResolveFindingObservations(
				t.Context(), f.finding(tc.check), "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingObservations() error = %v", err)
			}

			if resolution.Status != ResolutionResolved {
				t.Errorf("status = %v, want resolved", resolution.Status)
			}
			if resolution.Side != SideCandidate {
				t.Errorf("side = %v, want candidate", resolution.Side)
			}
			if resolution.RecordedCount != tc.wantRecorded {
				t.Errorf("recorded count = %d, want %d",
					resolution.RecordedCount, tc.wantRecorded)
			}
			if !resolution.Exhaustive {
				t.Error("exhaustive = false on complete history")
			}

			if len(resolution.Observations) != len(tc.wantEvents) {
				t.Fatalf("returned %d observations, want %d",
					len(resolution.Observations), len(tc.wantEvents))
			}
			for i, want := range tc.wantEvents {
				if got := resolution.Observations[i].EventID; got != want {
					t.Errorf("observation %d is %q, want %q", i, got, want)
				}
			}
		})
	}
}

// The evidence checks count an absence, so they resolve to nothing and say why
// rather than returning every observation in the run.
func TestEvidenceChecksAreAggregateOnly(t *testing.T) {
	f := newComparisonFixture(t)

	for _, tc := range []struct {
		check        GateCheckName
		wantRecorded uint64
	}{
		{CheckReferenceEvidence, 2},
		{CheckCandidateEvidence, 4},
	} {
		t.Run(string(tc.check), func(t *testing.T) {
			observations, err := f.plane.ResolveFindingObservations(
				t.Context(), f.finding(tc.check), "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingObservations() error = %v", err)
			}
			if observations.Status != ResolutionAggregateOnly {
				t.Errorf("status = %v, want aggregate_only", observations.Status)
			}
			if len(observations.Observations) != 0 {
				t.Errorf("returned %d observations for an aggregate-only check; "+
					"an absence has no supporting records", len(observations.Observations))
			}
			if observations.RecordedCount != tc.wantRecorded {
				t.Errorf("recorded count = %d, want %d",
					observations.RecordedCount, tc.wantRecorded)
			}
			if observations.Side != "" {
				t.Errorf("side = %q, want empty; the check belongs to no side's "+
					"observations", observations.Side)
			}

			behaviors, err := f.plane.ResolveFindingBehaviors(
				t.Context(), f.finding(tc.check), "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingBehaviors() error = %v", err)
			}
			if behaviors.Status != ResolutionAggregateOnly {
				t.Errorf("behavior status = %v, want aggregate_only", behaviors.Status)
			}
		})
	}
}

// added_behaviors counts behaviors, so resolving it to observations needs one
// named — and says so rather than answering a different question.
func TestAddedBehaviorsNeedsABehaviorToResolveObservations(t *testing.T) {
	f := newComparisonFixture(t)

	_, err := f.plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckAddedBehaviors), "", MaxListPage)
	if !errors.Is(err, ErrInvalidFinding) {
		t.Fatalf("error = %v, want ErrInvalidFinding", err)
	}

	// Naming one resolves it.
	resolution, err := f.plane.ResolveFindingObservations(
		t.Context(), f.behaviorFinding("fp-export"), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if len(resolution.Observations) != 2 {
		t.Fatalf("returned %d observations for fp-export, want 2",
			len(resolution.Observations))
	}
	for _, o := range resolution.Observations {
		if o.FingerprintID != "fp-export" {
			t.Errorf("observation %s carries fingerprint %q; the filter did not "+
				"scope to the behavior", o.EventID, o.FingerprintID)
		}
	}
}

// A counting check cannot be resolved to behaviors: it counts records, and
// "no contributing behaviors" would read as a fact about the run.
func TestCountingChecksDoNotResolveToBehaviors(t *testing.T) {
	f := newComparisonFixture(t)
	for _, check := range []GateCheckName{CheckBlockDecisions, CheckCriticalRiskObservations} {
		if _, err := f.plane.ResolveFindingBehaviors(
			t.Context(), f.finding(check), "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
			t.Errorf("%s behaviors error = %v, want ErrInvalidFinding", check, err)
		}
	}
}

// ---------------------------------------------------------------------
// Side selection
// ---------------------------------------------------------------------

// Presence picks the side, an explicit side may choose between them for a
// shared behavior, and a side the presence contradicts is refused.
func TestBehaviorSideSelection(t *testing.T) {
	f := newComparisonFixture(t)

	// Added: candidate, and the reference has nothing to show.
	added, err := f.plane.ResolveFindingObservations(
		t.Context(), f.behaviorFinding("fp-export"), "", MaxListPage)
	if err != nil {
		t.Fatalf("added: %v", err)
	}
	if added.Side != SideCandidate {
		t.Errorf("added behavior resolved on side %v, want candidate", added.Side)
	}

	contradiction := f.behaviorFinding("fp-export")
	contradiction.Side = SideReference
	if _, err := f.plane.ResolveFindingObservations(
		t.Context(), contradiction, "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
		t.Errorf("asking for an added behavior on the reference side = %v, "+
			"want ErrInvalidFinding", err)
	}

	// Shared: both sides hold it, and each returns its own run's rows.
	for _, tc := range []struct {
		side      ComparisonSide
		wantCount int
		wantFirst string
	}{
		{SideReference, 2, "r1"},
		{SideCandidate, 1, "c1"},
	} {
		t.Run(string(tc.side), func(t *testing.T) {
			finding := f.behaviorFinding("fp-shared")
			finding.Side = tc.side

			resolution, err := f.plane.ResolveFindingObservations(
				t.Context(), finding, "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingObservations() error = %v", err)
			}
			if resolution.Side != tc.side {
				t.Errorf("side = %v, want %v", resolution.Side, tc.side)
			}
			if len(resolution.Observations) != tc.wantCount {
				t.Fatalf("returned %d observations, want %d",
					len(resolution.Observations), tc.wantCount)
			}
			if got := resolution.Observations[0].EventID; got != tc.wantFirst {
				t.Errorf("first observation is %q, want %q — the resolution read "+
					"the wrong run", got, tc.wantFirst)
			}
		})
	}
}

// A shared fingerprint resolves separately in each run: an identifier repeated
// across runs is not a global key.
func TestRunIsolationForRepeatedIdentifiers(t *testing.T) {
	f := newComparisonFixture(t)

	reference := f.behaviorFinding("fp-shared")
	reference.Side = SideReference
	candidate := f.behaviorFinding("fp-shared")
	candidate.Side = SideCandidate

	left, err := f.plane.ResolveFindingObservations(t.Context(), reference, "", MaxListPage)
	if err != nil {
		t.Fatalf("reference: %v", err)
	}
	right, err := f.plane.ResolveFindingObservations(t.Context(), candidate, "", MaxListPage)
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}

	// Both runs number their observations from 1, so a sequence alone would
	// collide. Only the run scope keeps them apart.
	for _, o := range left.Observations {
		for _, other := range right.Observations {
			if o.EventID == other.EventID {
				t.Errorf("event %q appears in both runs' resolutions", o.EventID)
			}
		}
	}
	if left.Observations[0].Sequence != right.Observations[0].Sequence {
		t.Fatal("the two runs did not reuse a sequence, so this proves nothing")
	}
}

// ---------------------------------------------------------------------
// Invalid references
// ---------------------------------------------------------------------

func TestInvalidFindingReferences(t *testing.T) {
	f := newComparisonFixture(t)

	for _, tc := range []struct {
		name    string
		finding FindingRef
	}{
		{"neither check nor behavior", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID()}},
		{"both check and behavior", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID(),
			Check: CheckAddedBehaviors, Behavior: "fp-export"}},
		{"unknown check", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID(),
			Check: "invented_check"}},
		{"behavior this comparison never saw", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID(),
			Behavior: "fp-never-observed"}},
		{"side on a gate check", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID(),
			Check: CheckBlockDecisions, Side: SideCandidate}},
		{"unrecognized side", FindingRef{
			ReferenceRunID: f.reference.ID(), CandidateRunID: f.candidate.ID(),
			Behavior: "fp-shared", Side: "sideways"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.plane.ResolveFindingObservations(
				t.Context(), tc.finding, "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
				t.Errorf("error = %v, want ErrInvalidFinding", err)
			}
		})
	}

	// A run that does not exist is not found, which is a different answer from
	// a finding that is malformed.
	missing := f.finding(CheckBlockDecisions)
	missing.CandidateRunID = "no-such-run"
	if _, err := f.plane.ResolveFindingObservations(
		t.Context(), missing, "", MaxListPage); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("unknown run error = %v, want ErrStoreNotFound", err)
	}
}

// ---------------------------------------------------------------------
// Availability
// ---------------------------------------------------------------------

// A genuinely empty result over complete history is none_found.
//
// The fixture is consistent storage rather than deleted rows: the candidate
// really did ingest records, every one of them really is retained, and none of
// them is critical-risk. Deleting rows while leaving the history row saying
// "complete, four retained" would have described a database state this code
// cannot produce, and a test built on an impossible state proves nothing about
// a reachable one.
func TestGenuinelyEmptyResolutionOverCompleteHistory(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))
	// Nothing critical, and nothing blocked.
	for i := 1; i <= 3; i++ {
		ingestOne(t, plane, candidate, uint64(i), evidenceRecord(candidate,
			fmt.Sprintf("c%d", i), "fp-shared", "list_customers", "allow", "low"))
	}
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	resolution, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckCriticalRiskObservations), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.Status != ResolutionNoneFound {
		t.Errorf("status = %v, want none_found: the run is fully retained and "+
			"observed nothing critical", resolution.Status)
	}
	if !resolution.Exhaustive {
		t.Error("exhaustive = false on complete history")
	}
	if resolution.History.State() != ObservationHistoryComplete {
		t.Errorf("history state = %v, want complete", resolution.History.State())
	}
	if resolution.RecordedCount != 0 {
		t.Errorf("recorded count = %d, want 0", resolution.RecordedCount)
	}
}

// The same empty result over partial history is indeterminate.
//
// Partiality is produced the way saturation produces it — the history row says
// incomplete while its retained count still matches the rows present — so the
// storage state is one the platform can actually reach.
func TestEmptyResolutionOverPartialHistoryIsIndeterminate(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))
	for i := 1; i <= 3; i++ {
		ingestOne(t, plane, candidate, uint64(i), evidenceRecord(candidate,
			fmt.Sprintf("c%d", i), "fp-shared", "list_customers", "allow", "low"))
	}
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	markHistoryPartial(t, store, candidate.ID())

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	resolution, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckCriticalRiskObservations), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.Status != ResolutionIndeterminate {
		t.Errorf("status = %v, want indeterminate; an empty result over partial "+
			"history must never read as none_found", resolution.Status)
	}
	if resolution.Exhaustive {
		t.Error("exhaustive = true over partial history")
	}
	if resolution.History.State() != ObservationHistoryPartial {
		t.Errorf("history state = %v, want partial", resolution.History.State())
	}
}

// markHistoryPartial puts a run's history into the state saturation produces:
// incomplete, with the retained count still describing the rows that are there.
func markHistoryPartial(t *testing.T, store *SQLiteStore, runID EvaluationRunID) {
	t.Helper()
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE `+tableObservationHistory+` SET complete = 0 WHERE run_id = ?`,
		string(runID)); err != nil {
		t.Fatalf("mark the history partial: %v", err)
	}
}

// A run that predates retention stays unavailable, and a resolution over it is
// indeterminate however many matches the recorded count claims.
func TestResolutionOverUnavailableHistoryIsIndeterminate(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	// The state a schema-6 run migrates into: records, and no history at all.
	if _, err := f.store.db.ExecContext(ctx,
		`DELETE FROM `+tableObservations+` WHERE run_id = ?`,
		string(f.candidate.ID())); err != nil {
		t.Fatalf("clear observations: %v", err)
	}
	if _, err := f.store.db.ExecContext(ctx,
		`DELETE FROM `+tableObservationHistory+` WHERE run_id = ?`,
		string(f.candidate.ID())); err != nil {
		t.Fatalf("clear history row: %v", err)
	}

	resolution, err := f.plane.ResolveFindingObservations(
		ctx, f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.Status != ResolutionIndeterminate {
		t.Errorf("status = %v, want indeterminate", resolution.Status)
	}
	if resolution.History.State() != ObservationHistoryUnavailable {
		t.Errorf("history state = %v, want unavailable", resolution.History.State())
	}
	if resolution.Exhaustive {
		t.Error("exhaustive = true over unavailable history")
	}
	// The recorded count still describes what the run actually did — the
	// aggregate is untouched by retention.
	if resolution.RecordedCount != 2 {
		t.Errorf("recorded count = %d, want 2; the aggregate is authoritative "+
			"whatever retention holds", resolution.RecordedCount)
	}
}

// ---------------------------------------------------------------------
// Bounded retrieval
// ---------------------------------------------------------------------

// The filter is applied in storage, so a page holds `limit` matches rather than
// whatever survives filtering an already-truncated page.
func TestFilteringHappensBeforePagination(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-shared", "list_customers", "allow", "low"))

	// Interleave: every other candidate observation blocks. Ten blocks sit
	// among twenty rows, so a page of 4 that filtered after truncating would
	// return 2.
	sequence := uint64(1)
	for i := range 20 {
		decision, risk := "allow", "low"
		if i%2 == 1 {
			decision, risk = "block", "critical"
		}
		ingestOne(t, plane, candidate, sequence, evidenceRecord(candidate,
			fmt.Sprintf("c%02d", i), "fp-shared", "list_customers", decision, risk))
		sequence++
	}
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	const limit = 4
	page, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckBlockDecisions), "", limit)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if len(page.Observations) != limit {
		t.Fatalf("page holds %d observations, want %d — the filter ran after the "+
			"limit rather than inside the query", len(page.Observations), limit)
	}
	for _, o := range page.Observations {
		if o.Decision != "block" {
			t.Errorf("observation %s has decision %q in a block_decisions "+
				"resolution", o.EventID, o.Decision)
		}
	}

	// And paging reaches every match exactly once, in sequence order.
	var seen []uint64
	after := ""
	for {
		next, err := plane.ResolveFindingObservations(
			t.Context(), f.finding(CheckBlockDecisions), after, limit)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if len(next.Observations) == 0 {
			break
		}
		for _, o := range next.Observations {
			seen = append(seen, o.Sequence)
		}
		after = FormatObservationCursor(next.Observations[len(next.Observations)-1].Sequence)
	}
	if len(seen) != 10 {
		t.Fatalf("paged over %d block observations, want 10 (%v)", len(seen), seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] {
			t.Fatalf("sequences are not strictly ascending: %v", seen)
		}
	}
}

// Equal timestamps do not disturb the order, because the page key is the
// sequence rather than the clock.
func TestResolutionOrderingUnderEqualTimestamps(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))
	for i := range 9 {
		record := evidenceRecord(candidate, fmt.Sprintf("c%d", i),
			"fp-shared", "list_customers", "block", "low")
		record.Timestamp = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		ingestOne(t, plane, candidate, uint64(i+1), record)
	}
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	var seen []uint64
	after := ""
	for {
		page, err := plane.ResolveFindingObservations(
			t.Context(), f.finding(CheckBlockDecisions), after, 2)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if len(page.Observations) == 0 {
			break
		}
		for _, o := range page.Observations {
			seen = append(seen, o.Sequence)
		}
		after = FormatObservationCursor(page.Observations[len(page.Observations)-1].Sequence)
	}
	if len(seen) != 9 {
		t.Fatalf("paged over %v, want 9 observations", seen)
	}
	for i, sequence := range seen {
		if sequence != uint64(i+1) {
			t.Fatalf("page order is %v, want ascending sequences", seen)
		}
	}
}

// Behaviors page in fingerprint order with a stable cursor.
func TestBehaviorResolutionPaging(t *testing.T) {
	f := newComparisonFixture(t)

	first, err := f.plane.ResolveFindingBehaviors(
		t.Context(), f.finding(CheckAddedBehaviors), "", 1)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first.Behaviors) != 1 || first.Behaviors[0].FingerprintID != "fp-delete" {
		t.Fatalf("first page = %+v, want one entry fp-delete", first.Behaviors)
	}

	second, err := f.plane.ResolveFindingBehaviors(
		t.Context(), f.finding(CheckAddedBehaviors), "fp-delete", 1)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second.Behaviors) != 1 || second.Behaviors[0].FingerprintID != "fp-export" {
		t.Fatalf("second page = %+v, want one entry fp-export", second.Behaviors)
	}

	third, err := f.plane.ResolveFindingBehaviors(
		t.Context(), f.finding(CheckAddedBehaviors), "fp-export", 1)
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(third.Behaviors) != 0 {
		t.Errorf("third page holds %d entries, want none", len(third.Behaviors))
	}
}

// ---------------------------------------------------------------------
// Digest safety and long identifiers
// ---------------------------------------------------------------------

// A behavioral filter matches on the digest *and* the original value, so a
// planted key collision cannot return another behavior's observations.
func TestBehaviorFilterRequiresTheOriginalValue(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	// Stand in for a collision: give fp-delete's rows the key fp-export hashes
	// to, which is indistinguishable from a real collision to any query.
	if _, err := f.store.db.ExecContext(ctx,
		`UPDATE `+tableObservations+` SET fingerprint_key = ?
		  WHERE run_id = ? AND fingerprint_id = ?`,
		observationDigestKey("fp-export"), string(f.candidate.ID()), "fp-delete"); err != nil {
		t.Fatalf("plant the colliding key: %v", err)
	}

	resolution, err := f.plane.ResolveFindingObservations(
		ctx, f.behaviorFinding("fp-export"), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	for _, o := range resolution.Observations {
		if o.FingerprintID != "fp-export" {
			t.Errorf("a colliding digest returned observation %s carrying %q; the "+
				"original-value comparison is what makes a digest index safe",
				o.EventID, o.FingerprintID)
		}
	}
	if len(resolution.Observations) != 2 {
		t.Errorf("returned %d observations for fp-export, want its own 2",
			len(resolution.Observations))
	}
}

// An identifier the ingest contract already accepts resolves through the digest
// index, at a length no raw B-tree key could hold.
func TestResolutionAcceptsLongIdentifiers(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	trace := incompressibleIdentifier(0x243F6A8885A308D3, 4096)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))

	record := evidenceRecord(candidate, "c1", "fp-shared", "list_customers", "block", "low")
	record.TraceID = trace
	ingestOne(t, plane, candidate, 1, record)

	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	resolution, err := f.plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if len(resolution.Observations) != 1 {
		t.Fatalf("returned %d observations, want 1", len(resolution.Observations))
	}
	if got := resolution.Observations[0].TraceID; got != trace {
		t.Errorf("trace id was not preserved through resolution: %d bytes back, %d in",
			len(got), len(trace))
	}
}

// ---------------------------------------------------------------------
// Nothing is recomputed
// ---------------------------------------------------------------------

// A resolution changes no comparison number, no verdict and no fingerprint.
func TestResolutionChangesNoComparisonResult(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	limits := EvaluationGateLimits{
		MaxAddedBehaviors:           0,
		MaxBlockDecisions:           0,
		MaxCriticalRiskObservations: 0,
	}
	before, err := f.plane.CompareEvaluations(ctx, f.reference.ID(), f.candidate.ID(), limits)
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}

	// Resolve everything resolvable.
	for _, check := range []GateCheckName{
		CheckAddedBehaviors, CheckBlockDecisions, CheckCriticalRiskObservations,
		CheckReferenceEvidence, CheckCandidateEvidence,
	} {
		_, _ = f.plane.ResolveFindingBehaviors(ctx, f.finding(check), "", MaxListPage)
		_, _ = f.plane.ResolveFindingObservations(ctx, f.finding(check), "", MaxListPage)
	}

	after, err := f.plane.CompareEvaluations(ctx, f.reference.ID(), f.candidate.ID(), limits)
	if err != nil {
		t.Fatalf("CompareEvaluations() after resolution error = %v", err)
	}

	if before.Gate.Verdict() != after.Gate.Verdict() {
		t.Errorf("gate verdict changed across resolution: %v then %v",
			before.Gate.Verdict(), after.Gate.Verdict())
	}
	if before.Diff.AddedCount() != after.Diff.AddedCount() {
		t.Errorf("added count changed: %d then %d",
			before.Diff.AddedCount(), after.Diff.AddedCount())
	}
	if before.Gate.BlockDecisions().Actual != after.Gate.BlockDecisions().Actual {
		t.Error("block decision count changed across resolution")
	}

	// And the resolution's recorded numbers are the gate's own.
	blocks, err := f.plane.ResolveFindingObservations(
		ctx, f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("resolve blocks: %v", err)
	}
	if blocks.RecordedCount != after.Gate.BlockDecisions().Actual {
		t.Errorf("resolution reports %d block decisions, the gate reports %d; "+
			"the resolver derived a number instead of reading one",
			blocks.RecordedCount, after.Gate.BlockDecisions().Actual)
	}

	added, err := f.plane.ResolveFindingBehaviors(
		ctx, f.finding(CheckAddedBehaviors), "", MaxListPage)
	if err != nil {
		t.Fatalf("resolve added: %v", err)
	}
	if added.RecordedCount != after.Gate.AddedBehaviors().Actual {
		t.Errorf("resolution reports %d added behaviors, the gate reports %d",
			added.RecordedCount, after.Gate.AddedBehaviors().Actual)
	}
}

// A running evaluation cannot be resolved, for the reason it cannot be
// compared: its evidence is still moving.
func TestResolutionRequiresCompletedRuns(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)
	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-shared", "list_customers", "allow", "low"))
	ingestOne(t, plane, candidate, 1,
		evidenceRecord(candidate, "c1", "fp-shared", "list_customers", "allow", "low"))

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}
	if _, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckBlockDecisions), "", MaxListPage); !errors.Is(
		err, ErrEvaluationState) {
		t.Fatalf("error = %v, want ErrEvaluationState", err)
	}
}

// Every field a resolution returns is one task 067 retains, and the operational
// evidence keeps its meaning through it.
func TestResolutionPreservesOperationalEvidence(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))

	zero := evidenceRecord(candidate, "c1", "fp-shared", "list_customers", "block", "low")
	zero.DurationNanos = "0"
	zero.SpanStatus = event.StatusOK
	unavailable := evidenceRecord(candidate, "c2", "fp-shared", "list_customers", "block", "low")
	unavailable.DurationNanos = ""
	unavailable.SpanStatus = event.StatusUnavailable

	ingestOne(t, plane, candidate, 1, zero)
	ingestOne(t, plane, candidate, 2, unavailable)
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}
	resolution, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if len(resolution.Observations) != 2 {
		t.Fatalf("returned %d observations, want 2", len(resolution.Observations))
	}

	if !resolution.Observations[0].DurationObserved {
		t.Error("a measured zero was resolved as unavailable")
	}
	if resolution.Observations[1].DurationObserved {
		t.Error("an unavailable duration was resolved as observed")
	}
	if resolution.Observations[1].SpanStatus != event.StatusUnavailable {
		t.Errorf("span status = %q, want the unavailable state",
			resolution.Observations[1].SpanStatus)
	}
}

// ---------------------------------------------------------------------
// Consistent reads
// ---------------------------------------------------------------------

// A resolution's page and the history metadata describing it come from one
// snapshot, so a concurrent ingest cannot make a resolution describe a state
// that never existed.
//
// The resolution path reaches storage through FindObservations, which shares
// task 067's snapshot machinery — this asserts that the filtered read inherited
// it rather than opening its own connection per statement.
func TestResolutionPageIsOneSnapshot(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-other", "list_invoices", "allow", "low"))
	ingestOne(t, plane, candidate, 1,
		evidenceRecord(candidate, "c1", "fp-shared", "list_customers", "block", "low"))

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	// The candidate stays running so the hook's ingest is admissible; the
	// resolution needs both runs completed, so the interleaving is driven
	// against the store's filtered read directly.
	var hookErr error
	installObservationReadHook(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		_, hookErr = plane.IngestDecisionRecord(ctx, IngestRequest{
			RunID:             candidate.ID(),
			Sequence:          2,
			BehavioralProfile: candidate.BehavioralProfile(),
			Record: evidenceRecord(candidate, "c2", "fp-shared",
				"list_customers", "block", "low"),
		})
	})

	page, err := store.FindObservations(
		t.Context(), candidate.ID(), ObservationFilter{Decision: "block"}, 0, MaxListPage)
	if err != nil {
		t.Fatalf("FindObservations() error = %v", err)
	}

	if hookErr == nil {
		t.Error("an ingest committed during a filtered read; the read is not " +
			"holding a transaction")
	}
	if uint64(len(page.Observations)) > page.History.RetainedCount() {
		t.Errorf("page holds %d observations beside a retained count of %d; the "+
			"rows and the metadata came from two different snapshots",
			len(page.Observations), page.History.RetainedCount())
	}

	// And the resolution over the completed pair is internally consistent.
	completeRun(t, store, reference)
	completeRun(t, store, candidate)
	resolution, err := plane.ResolveFindingObservations(
		t.Context(), f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.Status != ResolutionResolved {
		t.Errorf("status = %v, want resolved", resolution.Status)
	}
	if !resolution.Exhaustive {
		t.Error("exhaustive = false on complete history")
	}
}

// ---------------------------------------------------------------------
// Exhausted pages are not absent evidence
// ---------------------------------------------------------------------

// Paging to the end of a finding's observations and then asking for one more
// page must not report that the finding has no evidence.
//
// This is the regression: the status used to come from the page length, so the
// request a developer makes *after* reading all the evidence reported that
// there was none — the opposite of the truth, arriving at the worst moment.
func TestExhaustedObservationPageStillReportsResolved(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()
	finding := f.finding(CheckBlockDecisions)

	// Read every page.
	var seen []uint64
	after := ""
	for {
		page, err := f.plane.ResolveFindingObservations(ctx, finding, after, 1)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if page.Status != ResolutionResolved {
			t.Fatalf("page after %q has status %v, want resolved", after, page.Status)
		}
		if len(page.Observations) == 0 {
			break
		}
		for _, o := range page.Observations {
			seen = append(seen, o.Sequence)
		}
		after = FormatObservationCursor(page.Observations[len(page.Observations)-1].Sequence)
	}
	if len(seen) != 2 {
		t.Fatalf("paged over %v, want the run's 2 block decisions", seen)
	}

	// And explicitly: the page after the final sequence.
	beyond, err := f.plane.ResolveFindingObservations(
		ctx, finding, FormatObservationCursor(seen[len(seen)-1]), MaxListPage)
	if err != nil {
		t.Fatalf("beyond the last match: %v", err)
	}
	if len(beyond.Observations) != 0 {
		t.Fatalf("a page past the last match holds %d observations",
			len(beyond.Observations))
	}
	if beyond.Status != ResolutionNoneFound && beyond.Status != ResolutionIndeterminate {
		// Both would be wrong; naming them makes the failure specific.
		if beyond.Status != ResolutionResolved {
			t.Fatalf("status = %v, want resolved", beyond.Status)
		}
	}
	if beyond.Status != ResolutionResolved {
		t.Errorf("status = %v after paging past the last match; the finding's "+
			"evidence did not stop existing because the cursor moved past it",
			beyond.Status)
	}

	// A cursor far beyond anything retained behaves the same way.
	far, err := f.plane.ResolveFindingObservations(
		ctx, finding, FormatObservationCursor(999999), MaxListPage)
	if err != nil {
		t.Fatalf("far cursor: %v", err)
	}
	if far.Status != ResolutionResolved {
		t.Errorf("status = %v for a cursor beyond every match, want resolved",
			far.Status)
	}
}

// The same rule for added-behavior pagination.
func TestExhaustedBehaviorPageStillReportsResolved(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()
	finding := f.finding(CheckAddedBehaviors)

	var seen []string
	after := ""
	for {
		page, err := f.plane.ResolveFindingBehaviors(ctx, finding, after, 1)
		if err != nil {
			t.Fatalf("page after %q: %v", after, err)
		}
		if page.Status != ResolutionResolved {
			t.Fatalf("page after %q has status %v, want resolved", after, page.Status)
		}
		if len(page.Behaviors) == 0 {
			break
		}
		for _, b := range page.Behaviors {
			seen = append(seen, b.FingerprintID)
		}
		after = page.Behaviors[len(page.Behaviors)-1].FingerprintID
	}
	if len(seen) != 2 {
		t.Fatalf("paged over %v, want the 2 added behaviors", seen)
	}

	beyond, err := f.plane.ResolveFindingBehaviors(
		ctx, finding, seen[len(seen)-1], MaxListPage)
	if err != nil {
		t.Fatalf("beyond the last behavior: %v", err)
	}
	if len(beyond.Behaviors) != 0 {
		t.Fatalf("a page past the last behavior holds %d entries", len(beyond.Behaviors))
	}
	if beyond.Status != ResolutionResolved {
		t.Errorf("status = %v after paging past the last behavior, want resolved",
			beyond.Status)
	}
	if beyond.RecordedCount != 2 {
		t.Errorf("recorded count = %d on an exhausted page, want the check's own 2",
			beyond.RecordedCount)
	}
}

// Partial history with matches before the cursor: the evidence exists, so the
// answer is resolved rather than indeterminate.
//
// This is the case where the two corrections meet. Status must come from
// whether the finding has retained evidence at all, not from the page and not
// from the history state alone.
func TestPartialHistoryWithMatchesBeforeTheCursorIsResolved(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	markHistoryPartial(t, f.store, f.candidate.ID())
	finding := f.finding(CheckBlockDecisions)

	all, err := f.plane.ResolveFindingObservations(ctx, finding, "", MaxListPage)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if all.Status != ResolutionResolved {
		t.Fatalf("status = %v with matches present, want resolved", all.Status)
	}
	if all.Exhaustive {
		t.Error("exhaustive = true over partial history")
	}
	last := all.Observations[len(all.Observations)-1].Sequence

	beyond, err := f.plane.ResolveFindingObservations(
		ctx, finding, FormatObservationCursor(last), MaxListPage)
	if err != nil {
		t.Fatalf("beyond: %v", err)
	}
	if len(beyond.Observations) != 0 {
		t.Fatalf("page past the last match holds %d rows", len(beyond.Observations))
	}
	if beyond.Status != ResolutionResolved {
		t.Errorf("status = %v; matches were retained before this cursor, so the "+
			"answer is neither none_found nor indeterminate", beyond.Status)
	}
	if beyond.Exhaustive {
		t.Error("exhaustive = true over partial history")
	}
}

// Status never comes from the recorded count: an aggregate may count records
// retention never kept.
func TestStatusIsNotInferredFromRecordedCount(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	// The run really blocked twice, and neither observation is retained. The
	// aggregate still says 2 — which is the state bounded retention produces.
	if _, err := f.store.db.ExecContext(ctx,
		`DELETE FROM `+tableObservations+` WHERE run_id = ? AND decision = ?`,
		string(f.candidate.ID()), "block"); err != nil {
		t.Fatalf("drop the retained blocks: %v", err)
	}
	markHistoryPartial(t, f.store, f.candidate.ID())

	resolution, err := f.plane.ResolveFindingObservations(
		ctx, f.finding(CheckBlockDecisions), "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.RecordedCount != 2 {
		t.Fatalf("recorded count = %d, want the aggregate's 2", resolution.RecordedCount)
	}
	if resolution.Status != ResolutionIndeterminate {
		t.Errorf("status = %v; a non-zero recorded count is not evidence that "+
			"anything was retained", resolution.Status)
	}
}

// ---------------------------------------------------------------------
// A shared behavior must name its side
// ---------------------------------------------------------------------

// Both resolution methods refuse a shared behavior with no side.
func TestSharedBehaviorRequiresAnExplicitSide(t *testing.T) {
	f := newComparisonFixture(t)
	finding := f.behaviorFinding("fp-shared")

	if _, err := f.plane.ResolveFindingObservations(
		t.Context(), finding, "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
		t.Errorf("observations error = %v, want ErrInvalidFinding", err)
	} else if !strings.Contains(err.Error(), "reference or candidate") {
		t.Errorf("the diagnostic does not say what to supply: %v", err)
	}

	if _, err := f.plane.ResolveFindingBehaviors(
		t.Context(), finding, "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
		t.Errorf("behaviors error = %v, want ErrInvalidFinding", err)
	}
}

// Each stated side returns its own run's recorded count and evidence.
func TestSharedBehaviorResolvesPerSide(t *testing.T) {
	f := newComparisonFixture(t)

	for _, tc := range []struct {
		side         ComparisonSide
		wantRecorded uint64
		wantEvents   []string
	}{
		{SideReference, 2, []string{"r1", "r2"}},
		{SideCandidate, 1, []string{"c1"}},
	} {
		t.Run(string(tc.side), func(t *testing.T) {
			finding := f.behaviorFinding("fp-shared")
			finding.Side = tc.side

			observations, err := f.plane.ResolveFindingObservations(
				t.Context(), finding, "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingObservations() error = %v", err)
			}
			if observations.RecordedCount != tc.wantRecorded {
				t.Errorf("recorded count = %d, want %d — the resolution read the "+
					"wrong side's delta count",
					observations.RecordedCount, tc.wantRecorded)
			}
			if len(observations.Observations) != len(tc.wantEvents) {
				t.Fatalf("returned %d observations, want %d",
					len(observations.Observations), len(tc.wantEvents))
			}
			for i, want := range tc.wantEvents {
				if got := observations.Observations[i].EventID; got != want {
					t.Errorf("observation %d is %q, want %q", i, got, want)
				}
			}

			behaviors, err := f.plane.ResolveFindingBehaviors(
				t.Context(), finding, "", MaxListPage)
			if err != nil {
				t.Fatalf("ResolveFindingBehaviors() error = %v", err)
			}
			if behaviors.Side != tc.side {
				t.Errorf("behaviors side = %v, want %v", behaviors.Side, tc.side)
			}
			if behaviors.RecordedCount != tc.wantRecorded {
				t.Errorf("behaviors recorded count = %d, want %d",
					behaviors.RecordedCount, tc.wantRecorded)
			}
		})
	}
}

// An added or removed behavior still needs no side, and a contradicting one is
// still refused.
//
// Its own fixture, because the shared one has nothing removed and adding one
// there would move the counts every other test in this file asserts.
func TestPresenceDerivedSidesAreUnchanged(t *testing.T) {
	store, _, plane, reference := observationPlane(t)
	candidate := seedSecondRunningRun(t, store, reference)

	// Reference only: removed. Candidate only: added.
	ingestOne(t, plane, reference, 1,
		evidenceRecord(reference, "r1", "fp-gone", "list_invoices", "allow", "low"))
	ingestOne(t, plane, candidate, 1,
		evidenceRecord(candidate, "c1", "fp-new", "export_customer", "allow", "low"))
	completeRun(t, store, reference)
	completeRun(t, store, candidate)

	f := comparisonFixture{store: store, plane: plane,
		reference: reference, candidate: candidate}

	for _, tc := range []struct {
		name        string
		fingerprint string
		wantSide    ComparisonSide
		wantEvent   string
		contradict  ComparisonSide
	}{
		{"added defaults to candidate", "fp-new", SideCandidate, "c1", SideReference},
		{"removed defaults to reference", "fp-gone", SideReference, "r1", SideCandidate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolution, err := plane.ResolveFindingObservations(
				t.Context(), f.behaviorFinding(tc.fingerprint), "", MaxListPage)
			if err != nil {
				t.Fatalf("with no side: %v", err)
			}
			if resolution.Side != tc.wantSide {
				t.Errorf("side = %v, want %v", resolution.Side, tc.wantSide)
			}
			if len(resolution.Observations) != 1 {
				t.Fatalf("returned %d observations, want 1", len(resolution.Observations))
			}
			if got := resolution.Observations[0].EventID; got != tc.wantEvent {
				t.Errorf("observation = %q, want %q — the wrong run was read",
					got, tc.wantEvent)
			}

			behaviors, err := plane.ResolveFindingBehaviors(
				t.Context(), f.behaviorFinding(tc.fingerprint), "", MaxListPage)
			if err != nil {
				t.Fatalf("behaviors with no side: %v", err)
			}
			if behaviors.Side != tc.wantSide {
				t.Errorf("behaviors side = %v, want %v", behaviors.Side, tc.wantSide)
			}

			contradiction := f.behaviorFinding(tc.fingerprint)
			contradiction.Side = tc.contradict
			if _, err := plane.ResolveFindingObservations(
				t.Context(), contradiction, "", MaxListPage); !errors.Is(err, ErrInvalidFinding) {
				t.Errorf("contradicting side = %v, want ErrInvalidFinding", err)
			}
		})
	}
}

// A gate check still cannot carry a side.
func TestGateCheckStillRefusesASide(t *testing.T) {
	f := newComparisonFixture(t)
	finding := f.finding(CheckBlockDecisions)
	finding.Side = SideCandidate

	for name, resolve := range map[string]func() error{
		"observations": func() error {
			_, err := f.plane.ResolveFindingObservations(t.Context(), finding, "", MaxListPage)
			return err
		},
		"behaviors": func() error {
			_, err := f.plane.ResolveFindingBehaviors(t.Context(), finding, "", MaxListPage)
			return err
		},
	} {
		if err := resolve(); !errors.Is(err, ErrInvalidFinding) {
			t.Errorf("%s: error = %v, want ErrInvalidFinding", name, err)
		}
	}
}
