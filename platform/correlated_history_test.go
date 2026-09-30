package platform

// Correlated reads over retained history, task 076.
//
// The explorer's session, trace and behavior-detail views are the unfiltered
// history read plus one predicate, and the assertions worth making are the ones
// a wrong predicate would pass: that the narrowing happens inside the query, that
// it stays inside the run, and that an identifier the ingest path accepted is
// still reachable afterwards.

import (
	"errors"
	"strings"
	"testing"

	trustvian "github.com/trustvian/trustvian"
)

// correlatedRecord builds a record carrying a chosen session and trace.
func correlatedRecord(
	run EvaluationRun, eventID, session, trace, fingerprint string,
) trustvian.DecisionRecord {
	record := observationTestRecord(run, eventID)
	record.SessionID = session
	record.TraceID = trace
	record.FingerprintID = fingerprint
	// The descriptor has to agree with the fingerprint: the ingest path refuses
	// a record whose behavior contradicts an identity the run already recorded,
	// which is what stops a fixture from inventing an impossible history.
	record.Behavior.OperationName = "POST " + fingerprint
	return record
}

// correlatedFixture is one run whose eight observations alternate between two
// sessions, two traces and two behaviors.
//
// Alternating rather than grouped, deliberately: a predicate applied to an
// already-paginated page would return roughly half of each page, which grouped
// fixtures hide and this one does not.
func correlatedFixture(t *testing.T) (*ControlPlane, EvaluationRun) {
	t.Helper()
	_, _, plane, run := observationPlane(t)
	for i := 1; i <= 8; i++ {
		session, trace, fingerprint := "session-odd", "trace-odd", "fp-odd"
		if i%2 == 0 {
			session, trace, fingerprint = "session-even", "trace-even", "fp-even"
		}
		ingestOne(t, plane, run, uint64(i), correlatedRecord(
			run, string(rune('a'+i-1)), session, trace, fingerprint))
	}
	return plane, run
}

// readAll pages a correlated read to its end and returns the sequences it saw.
//
// Paged at 3 against 4 matches so a boundary is crossed, which is where a
// predicate outside the query produces short pages and a wrong total.
func readAll(
	t *testing.T, plane *ControlPlane, run EvaluationRun, scope ObservationScope,
) []uint64 {
	t.Helper()
	var seen []uint64
	after := ""
	for {
		page, err := plane.FindEvaluationRunObservations(t.Context(), run.ID(), scope, after, 3)
		if err != nil {
			t.Fatalf("FindEvaluationRunObservations(after=%q) error = %v", after, err)
		}
		if len(page.Observations) == 0 {
			return seen
		}
		for _, o := range page.Observations {
			seen = append(seen, o.Sequence)
		}
		after = FormatObservationCursor(page.Observations[len(page.Observations)-1].Sequence)
	}
}

// A correlated read returns one session's actions, in ingest order.
func TestCorrelatedReadOrdersOneSession(t *testing.T) {
	plane, run := correlatedFixture(t)

	got := readAll(t, plane, run, ObservationScope{SessionID: "session-odd"})
	want := []uint64{1, 3, 5, 7}
	if len(got) != len(want) {
		t.Fatalf("session read returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("session read returned %v, want %v", got, want)
		}
	}
}

// A trace read is the same shape over the other correlation index.
func TestCorrelatedReadGathersOneTrace(t *testing.T) {
	plane, run := correlatedFixture(t)

	got := readAll(t, plane, run, ObservationScope{TraceID: "trace-even"})
	if len(got) != 4 {
		t.Fatalf("trace read returned %v, want four observations", got)
	}
	page, err := plane.FindEvaluationRunObservations(
		t.Context(), run.ID(), ObservationScope{TraceID: "trace-even"}, "", MaxListPage)
	if err != nil {
		t.Fatalf("FindEvaluationRunObservations() error = %v", err)
	}
	for _, o := range page.Observations {
		if o.TraceID != "trace-even" {
			t.Errorf("observation %d carries trace %q", o.Sequence, o.TraceID)
		}
	}
}

// One behavioral identity's observations inside one run, with no comparison.
func TestCorrelatedReadSelectsOneBehavior(t *testing.T) {
	plane, run := correlatedFixture(t)

	got := readAll(t, plane, run, ObservationScope{FingerprintID: "fp-even"})
	if len(got) != 4 {
		t.Fatalf("behavior read returned %v, want four observations", got)
	}
}

// The narrowing is inside the query, so a bounded page holds that many matches
// rather than that many rows of which some matched.
func TestCorrelatedReadFiltersBeforeTheLimit(t *testing.T) {
	plane, run := correlatedFixture(t)

	page, err := plane.FindEvaluationRunObservations(
		t.Context(), run.ID(), ObservationScope{SessionID: "session-odd"}, "", 2)
	if err != nil {
		t.Fatalf("FindEvaluationRunObservations() error = %v", err)
	}
	if len(page.Observations) != 2 {
		t.Fatalf("page holds %d observations, want a full page of 2 matches",
			len(page.Observations))
	}
	for _, o := range page.Observations {
		if o.SessionID != "session-odd" {
			t.Errorf("observation %d carries session %q", o.Sequence, o.SessionID)
		}
	}
}

// A correlation identifier is not a cross-run key: the same session in two runs
// resolves separately in each.
func TestCorrelatedReadIsScopedToOneRun(t *testing.T) {
	store, _, plane, first := observationPlane(t)
	second := seedSecondRunningRun(t, store, first)

	ingestOne(t, plane, first, 1,
		correlatedRecord(first, "f1", "session-shared", "trace-shared", "fp-1"))
	ingestOne(t, plane, second, 1,
		correlatedRecord(second, "s1", "session-shared", "trace-shared", "fp-1"))
	ingestOne(t, plane, second, 2,
		correlatedRecord(second, "s2", "session-shared", "trace-shared", "fp-1"))

	for _, tc := range []struct {
		run  EvaluationRun
		want int
	}{{first, 1}, {second, 2}} {
		page, err := plane.FindEvaluationRunObservations(t.Context(), tc.run.ID(),
			ObservationScope{SessionID: "session-shared"}, "", MaxListPage)
		if err != nil {
			t.Fatalf("FindEvaluationRunObservations(%s) error = %v", tc.run.ID(), err)
		}
		if len(page.Observations) != tc.want {
			t.Errorf("run %s returned %d observations, want %d",
				tc.run.ID(), len(page.Observations), tc.want)
		}
	}
}

// More than one narrowing is refused rather than answered: each combination is
// a query shape nothing measured and a view nobody specified.
func TestCorrelatedReadRefusesMoreThanOneNarrowing(t *testing.T) {
	plane, run := correlatedFixture(t)

	for name, scope := range map[string]ObservationScope{
		"session and trace":    {SessionID: "session-odd", TraceID: "trace-odd"},
		"session and behavior": {SessionID: "session-odd", FingerprintID: "fp-odd"},
		"trace and behavior":   {TraceID: "trace-odd", FingerprintID: "fp-odd"},
		"all three":            {SessionID: "s", TraceID: "t", FingerprintID: "f"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := plane.FindEvaluationRunObservations(
				t.Context(), run.ID(), scope, "", MaxListPage)
			if !errors.Is(err, ErrInvalidID) {
				t.Fatalf("error = %v, want ErrInvalidID", err)
			}
		})
	}
}

// An identifier the ingest path accepted stays reachable afterwards.
//
// trace_id and session_id are validated nowhere at ingest, and a read that
// applied validateID to them would make a legitimately retained row permanently
// invisible — the same defect task 067 refused from the write side when it
// declined to narrow which records the platform accepts.
func TestCorrelatedReadAcceptsAnyRetainedCorrelationValue(t *testing.T) {
	_, _, plane, run := observationPlane(t)

	oversized := strings.Repeat("s", maxIdentifierLength+64)
	record := correlatedRecord(run, "e1", oversized, oversized, "fp-1")
	ingestOne(t, plane, run, 1, record)

	for name, scope := range map[string]ObservationScope{
		"session": {SessionID: oversized},
		"trace":   {TraceID: oversized},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := plane.FindEvaluationRunObservations(
				t.Context(), run.ID(), scope, "", MaxListPage)
			if err != nil {
				t.Fatalf("FindEvaluationRunObservations() error = %v", err)
			}
			if len(page.Observations) != 1 {
				t.Fatalf("returned %d observations, want the one that was retained",
					len(page.Observations))
			}
		})
	}
}

// A correlation nothing retained is an empty page whose history says what is
// known, never an error and never a fabricated absence.
func TestCorrelatedReadOfAnAbsentCorrelationIsAnEmptyPage(t *testing.T) {
	plane, run := correlatedFixture(t)

	page, err := plane.FindEvaluationRunObservations(
		t.Context(), run.ID(), ObservationScope{SessionID: "session-absent"}, "", MaxListPage)
	if err != nil {
		t.Fatalf("FindEvaluationRunObservations() error = %v", err)
	}
	if len(page.Observations) != 0 {
		t.Fatalf("returned %d observations for an absent session", len(page.Observations))
	}
	if page.Matched {
		t.Error("Matched = true for a session with no retained observation")
	}
	if page.History.State() != ObservationHistoryComplete {
		t.Errorf("history state = %v, want complete", page.History.State())
	}
}

// A behavior reference keeps task 085's own validation, so two routes cannot
// disagree about what a fingerprint may be.
func TestCorrelatedReadValidatesTheBehaviorReference(t *testing.T) {
	plane, run := correlatedFixture(t)

	_, err := plane.FindEvaluationRunObservations(t.Context(), run.ID(),
		ObservationScope{FingerprintID: strings.Repeat("f", maxIdentifierLength+1)},
		"", MaxListPage)
	if !errors.Is(err, ErrInvalidID) {
		t.Fatalf("error = %v, want ErrInvalidID", err)
	}
}

// The unfiltered read is the same read with no narrowing, which is what keeps
// the two from drifting.
func TestUnfilteredReadIsTheUnnarrowedCorrelatedRead(t *testing.T) {
	plane, run := correlatedFixture(t)

	narrow, err := plane.FindEvaluationRunObservations(
		t.Context(), run.ID(), ObservationScope{}, "", MaxListPage)
	if err != nil {
		t.Fatalf("FindEvaluationRunObservations() error = %v", err)
	}
	wide, err := plane.EvaluationRunObservations(t.Context(), run.ID(), "", MaxListPage)
	if err != nil {
		t.Fatalf("EvaluationRunObservations() error = %v", err)
	}
	if len(narrow.Observations) != len(wide.Observations) {
		t.Fatalf("unnarrowed read returned %d observations, unfiltered returned %d",
			len(narrow.Observations), len(wide.Observations))
	}
}
