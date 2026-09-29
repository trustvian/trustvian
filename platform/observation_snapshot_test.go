package platform

// A page of retained history must describe one instant.
//
// The read is three statements — the history row, the record count and the
// observation rows — and a concurrent ingest committing between any two of them
// produces a page that describes a state which never existed: a retained count
// of 1 beside two rows, or completeness metadata from before a saturation the
// rows already show. A caller cannot tell such a page from a real one.
//
// Both tests below drive that interleaving with testHookInObservationRead rather
// than with timing, for the reason testHookInRunMutation documents: the window
// is microseconds wide, two goroutines released together run essentially in
// series, and a test that had to win the race would pass against a torn read as
// readily as against a consistent one.
//
// Each was verified against the pre-fix code — SQLite reading through the pool,
// PostgreSQL reading under withTx's default isolation — and each failed there.

import (
	"context"
	"errors"
	"testing"
	"time"
)

// installObservationReadHook installs a hook for one test and removes it after.
//
// The hook fires once. A page read runs three statements and the hook sits in
// one gap, but a test that also probes for continuation would otherwise re-enter
// its own ingest and stop describing the interleaving it set up.
func installObservationReadHook(t *testing.T, fn func()) {
	t.Helper()
	fired := false
	once := func() {
		if fired {
			return
		}
		fired = true
		fn()
	}
	testHookInObservationRead.Store(&once)
	t.Cleanup(func() { testHookInObservationRead.Store(nil) })
}

// assertPageDescribesOneInstant is the property both backends must hold.
//
// The page's own metadata and its rows have to agree. Which of the two states
// the snapshot caught is a timing detail and is deliberately not asserted —
// *either* is correct, and a mixture is not.
func assertPageDescribesOneInstant(t *testing.T, page ObservationPage, limit int) {
	t.Helper()

	if len(page.Observations) > limit {
		t.Fatalf("page holds %d observations, above the limit of %d",
			len(page.Observations), limit)
	}
	// Only meaningful when the limit is not what truncated the page.
	if len(page.Observations) < limit &&
		uint64(len(page.Observations)) != page.History.RetainedCount() {
		t.Errorf("page holds %d observations beside a retained count of %d; "+
			"that is a state the database never contained — the metadata and the "+
			"rows were read from two different snapshots",
			len(page.Observations), page.History.RetainedCount())
	}
	for i, o := range page.Observations {
		if o.Sequence != uint64(i+1) {
			t.Errorf("observation %d has sequence %d; the page is not the dense "+
				"prefix a single snapshot would return", i, o.Sequence)
		}
	}
}

// On SQLite the read holds the store's one connection for the whole page, so an
// ingest attempted from inside the window cannot acquire it and fails on its own
// deadline. That refusal *is* the property: the writer could not commit into the
// middle of the read.
//
// Against the pre-fix code the connection was free between statements, the
// ingest committed, and the page came back with one retained count and two rows.
func TestSQLiteObservationPageIsOneSnapshot(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	ingestOne(t, plane, run, 1, observationTestRecord(run, "e1"))

	var hookErr error
	installObservationReadHook(t, func() {
		// A bounded context, because the point is that this write cannot make
		// progress while the page is being read — not how long it waits. It is a
		// cap on blocking, never the synchronisation mechanism: the hook itself
		// is what puts this call at the one instant that matters.
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()

		_, hookErr = plane.IngestDecisionRecord(ctx, IngestRequest{
			RunID:             run.ID(),
			Sequence:          2,
			BehavioralProfile: run.BehavioralProfile(),
			Record:            observationTestRecord(run, "e2"),
		})
	})

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}

	assertPageDescribesOneInstant(t, page, MaxListPage)

	if hookErr == nil {
		t.Error("an ingest committed while a page read was open; the read is not " +
			"holding a transaction")
	} else if !errors.Is(hookErr, context.DeadlineExceeded) {
		t.Logf("concurrent ingest was refused with %v", hookErr)
	}

	// And the run is unharmed: the refused ingest retained nothing, so the next
	// one still takes sequence 2.
	after, err := store.EvaluationIngestState(t.Context(), run.ID())
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if after.NextSequence() != 2 {
		t.Errorf("next sequence = %d, want 2; a refused concurrent ingest must "+
			"leave the cursor where it was", after.NextSequence())
	}
	if got := retainedSequences(t, store, run.ID()); len(got) != 1 {
		t.Errorf("retained sequences = %v, want exactly the one committed record", got)
	}
}

// On PostgreSQL the concurrent ingest genuinely commits — MVCC lets it, the
// reader holds no lock it conflicts with — so this is the stronger test: the
// read must still return one coherent state afterwards.
//
// Under REPEATABLE READ the snapshot is taken once and the page is the before
// state. Against the pre-fix code, withTx's READ COMMITTED took a fresh snapshot
// per statement, the rows query saw the committed insert, and the page reported
// one retained observation beside two rows.
func TestPostgresObservationPageIsOneSnapshot(t *testing.T) {
	store := newPostgresStore(t)
	ctx := t.Context()
	run := startedRun(t, store, "run-1")

	first := conformanceCommit(t, run, 1, 1, "a0b1c2d3e4f50617"+
		"a0b1c2d3e4f50617a0b1c2d3e4f50617a0b1c2d3e4f50617")
	if _, err := store.CommitEvaluationIngest(ctx, first); err != nil {
		t.Fatalf("seed commit error = %v", err)
	}

	committed := false
	installObservationReadHook(t, func() {
		second := conformanceCommit(t, run, 2, 2, "b0c1d2e3f4051627"+
			"b0c1d2e3f4051627b0c1d2e3f4051627b0c1d2e3f4051627")
		result, err := store.CommitEvaluationIngest(context.Background(), second)
		if err != nil {
			t.Errorf("the concurrent ingest failed: %v. PostgreSQL should let it "+
				"commit — if it blocked, the read is taking a lock it should not", err)
			return
		}
		committed = result.Disposition == EvaluationIngestCommitted
	})

	page, err := store.RunObservations(ctx, "run-1", 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}

	if !committed {
		t.Fatal("the concurrent ingest did not commit, so this test proves nothing " +
			"about reading across one")
	}
	assertPageDescribesOneInstant(t, page, MaxListPage)

	// The snapshot is taken at the first statement, so the page is the state
	// before the concurrent commit. Asserted specifically here — on this backend
	// the writer demonstrably committed, so "either state" would let a torn read
	// that happened to agree with itself pass.
	if page.History.RetainedCount() != 1 || len(page.Observations) != 1 {
		t.Errorf("page reports %d retained and %d rows, want 1 and 1: the snapshot "+
			"was taken before the concurrent commit and must not have moved",
			page.History.RetainedCount(), len(page.Observations))
	}

	// A read issued afterwards sees the new state, so the snapshot bounded one
	// read rather than pinning the connection to a stale view.
	next, err := store.RunObservations(ctx, "run-1", 0, MaxListPage)
	if err != nil {
		t.Fatalf("second RunObservations() error = %v", err)
	}
	if next.History.RetainedCount() != 2 || len(next.Observations) != 2 {
		t.Errorf("a later read reports %d retained and %d rows, want 2 and 2",
			next.History.RetainedCount(), len(next.Observations))
	}
	assertPageDescribesOneInstant(t, next, MaxListPage)
}
