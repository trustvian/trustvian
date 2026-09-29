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

// The four outcomes, and the one that must never be confused with the others.
func TestResolutionStatusReflectsHistoryAvailability(t *testing.T) {
	f := newComparisonFixture(t)
	ctx := t.Context()

	// Complete history, no matching observation: that is a fact about the run.
	empty := f.behaviorFinding("fp-shared")
	empty.Side = SideCandidate
	if _, err := f.store.db.ExecContext(ctx,
		`DELETE FROM `+tableObservations+` WHERE run_id = ? AND fingerprint_id = ?`,
		string(f.candidate.ID()), "fp-shared"); err != nil {
		t.Fatalf("remove the matching rows: %v", err)
	}

	resolution, err := f.plane.ResolveFindingObservations(ctx, empty, "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if resolution.Status != ResolutionNoneFound {
		t.Errorf("status = %v, want none_found on complete history", resolution.Status)
	}
	if !resolution.Exhaustive {
		t.Error("exhaustive = false on complete history")
	}

	// Now mark the candidate's history partial. The same query must stop
	// claiming that nothing happened.
	if _, err := f.store.db.ExecContext(ctx,
		`UPDATE `+tableObservationHistory+` SET complete = 0 WHERE run_id = ?`,
		string(f.candidate.ID())); err != nil {
		t.Fatalf("mark the history partial: %v", err)
	}

	partial, err := f.plane.ResolveFindingObservations(ctx, empty, "", MaxListPage)
	if err != nil {
		t.Fatalf("ResolveFindingObservations() error = %v", err)
	}
	if partial.Status != ResolutionIndeterminate {
		t.Errorf("status = %v, want indeterminate; an empty result over partial "+
			"history must never read as none_found", partial.Status)
	}
	if partial.Exhaustive {
		t.Error("exhaustive = true over partial history")
	}
	if partial.History.State() != ObservationHistoryPartial {
		t.Errorf("history state = %v, want partial", partial.History.State())
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
