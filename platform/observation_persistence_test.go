package platform

// Per-observation history on SQLite, task 067.
//
// In-package so it can read the new tables directly: the point of most of
// these assertions is what is *durable*, and a test that only ever asked the
// read route would pass just as happily against a store that wrote nothing and
// remembered everything. The PostgreSQL half is the shared conformance suite,
// which skips without a database.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// ---------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------

// observationTestRecord is one fully-populated record: every field task 067
// retains carries a distinct, recognizable value, so a round trip that drops
// or transposes one is visible rather than plausible.
func observationTestRecord(run EvaluationRun, id string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:            id,
		Timestamp:          time.Date(2026, 3, 4, 5, 6, 7, 89, time.UTC),
		ActorID:            "actor-" + id,
		ActorType:          event.ActorTypeService,
		IdentityConfidence: 0.75,
		Environment:        string(run.Environment()),
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeService,
			OperationCategory: event.OperationCategoryHTTP,
			OperationName:     "POST " + id,
			TargetName:        "export.localhost",
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       string(run.Environment()),
		},
		FingerprintID:     "fp-" + id,
		AnomalyScore:      0.5,
		AnomalyConfidence: 0.25,
		TrustScore:        0.125,
		ContextRisk:       0.0625,
		RiskLevel:         "medium",
		Decision:          "alert",
		PolicyRule:        "rule-" + id,
		PolicyReason:      "matched " + id,
		MatchedDefault:    false,
		TraceID:           "trace-" + id,
		SpanID:            "span-" + id,
		SessionID:         "session-" + id,
		DelegatedFrom:     "parent-actor",
		ApprovalStatus:    event.ApprovalApproved,
		ParentSpanID:      "parentspan-" + id,
		SpanLineage:       event.LineageChild,
		DurationNanos:     "1500",
		SpanStatus:        event.StatusOK,
	}
}

// ingestOne pushes one record through the whole control-plane path.
func ingestOne(
	t *testing.T, plane *ControlPlane, run EvaluationRun,
	sequence uint64, record trustvian.DecisionRecord,
) IngestResult {
	t.Helper()
	result, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID:             run.ID(),
		Sequence:          sequence,
		BehavioralProfile: run.BehavioralProfile(),
		Record:            record,
	})
	if err != nil {
		t.Fatalf("IngestDecisionRecord(seq=%d) error = %v", sequence, err)
	}
	return result
}

// observationPlane wires a control plane over a fresh store with a running run.
func observationPlane(t *testing.T) (*SQLiteStore, string, *ControlPlane, EvaluationRun) {
	t.Helper()
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	return store, path, plane, run
}

// seedSecondRunningRun adds a second running run under the same candidate, so
// isolation can be asserted between two runs that share every scope above them.
func seedSecondRunningRun(t *testing.T, store *SQLiteStore, first EvaluationRun) EvaluationRun {
	t.Helper()
	ctx := t.Context()

	run, err := NewEvaluationRun(
		"run-2", first.CandidateID(), first.Environment(),
		first.BehavioralProfile(), first.CreatedAt())
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	started, err := run.Start(run.CreatedAt().Add(time.Minute))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}
	return started
}

// retainedSequences reads the sequences durably stored for a run, in key
// order, straight out of the table.
func retainedSequences(t *testing.T, store *SQLiteStore, runID EvaluationRunID) []uint64 {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(),
		`SELECT sequence FROM `+tableObservations+` WHERE run_id = ? ORDER BY sequence`,
		string(runID))
	if err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	defer rows.Close()

	var out []uint64
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatalf("scan sequence: %v", err)
		}
		v, err := parseObservationSequenceKey("sequence", text)
		if err != nil {
			t.Fatalf("stored sequence is not canonical: %v", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------
// Round trip and durability
// ---------------------------------------------------------------------

// Every retained field comes back exactly, across a close and reopen.
func TestRetainedObservationSurvivesARestart(t *testing.T) {
	store, path, plane, run := observationPlane(t)
	record := observationTestRecord(run, "e1")
	ingestOne(t, plane, run, 1, record)
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()

	page, err := reopened.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("retained %d observations, want 1", len(page.Observations))
	}
	got := page.Observations[0]

	want, err := ObservationFromRecord(1, record, true)
	if err != nil {
		t.Fatalf("ObservationFromRecord() error = %v", err)
	}
	if !got.Timestamp.Equal(want.Timestamp) {
		t.Errorf("Timestamp = %v, want %v", got.Timestamp, want.Timestamp)
	}
	// Compared with the timestamps normalized, because equality on time.Time
	// compares monotonic and location as well as the instant.
	got.Timestamp, want.Timestamp = time.Time{}, time.Time{}
	if got != want {
		t.Errorf("round trip mismatch:\n got = %+v\nwant = %+v", got, want)
	}

	if state := page.History.State(); state != ObservationHistoryComplete {
		t.Errorf("history state = %v, want complete", state)
	}
	if page.History.RetainedCount() != 1 {
		t.Errorf("retained count = %d, want 1", page.History.RetainedCount())
	}
}

// NewBehavior is recorded, not re-derived: the first record carrying a
// fingerprint is new and the second is not, and both statements survive.
func TestRetainedObservationRecordsNewBehaviorAsItWasAtIngest(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	first := observationTestRecord(run, "e1")
	second := observationTestRecord(run, "e2")
	second.FingerprintID = first.FingerprintID // same behavior, second sighting
	second.Behavior = first.Behavior

	ingestOne(t, plane, run, 1, first)
	ingestOne(t, plane, run, 2, second)

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 2 {
		t.Fatalf("retained %d observations, want 2", len(page.Observations))
	}
	if !page.Observations[0].NewBehavior {
		t.Error("first sighting of a fingerprint is not marked new")
	}
	if page.Observations[1].NewBehavior {
		t.Error("second sighting of the same fingerprint is marked new")
	}
}

// ---------------------------------------------------------------------
// Retry, idempotency and concurrency
// ---------------------------------------------------------------------

// A retry of the last accepted record replays. It must produce no second
// observation — the whole reason the row is written inside the cursor's
// transaction.
func TestRetryDoesNotDuplicateRetainedHistory(t *testing.T) {
	store, _, plane, run := observationPlane(t)
	record := observationTestRecord(run, "e1")

	ingestOne(t, plane, run, 1, record)

	replay, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID:             run.ID(),
		Sequence:          1,
		BehavioralProfile: run.BehavioralProfile(),
		Record:            record,
	})
	if err != nil {
		t.Fatalf("retry error = %v", err)
	}
	if replay.Disposition != IngestReplayed {
		t.Fatalf("disposition = %v, want replayed", replay.Disposition)
	}

	if got := retainedSequences(t, store, run.ID()); len(got) != 1 {
		t.Errorf("retained sequences = %v, want exactly one row", got)
	}
	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if page.History.RetainedCount() != 1 {
		t.Errorf("retained count = %d, want 1", page.History.RetainedCount())
	}
}

// A commit that loses the race reports AlreadyCommitted and writes nothing —
// the same rule the retry follows, decided inside the transaction.
func TestConcurrentIdenticalCommitRetainsOneObservation(t *testing.T) {
	store, _, plane, run := observationPlane(t)
	record := observationTestRecord(run, "e1")

	// The state both requests would have computed against.
	state, err := store.EvaluationIngestState(t.Context(), run.ID())
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	aggregate, err = aggregate.AddRecord(record)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	if err := collector.Observe(record); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	digest, err := RecordDigest(record)
	if err != nil {
		t.Fatalf("RecordDigest() error = %v", err)
	}
	observation, err := ObservationFromRecord(1, record, true)
	if err != nil {
		t.Fatalf("ObservationFromRecord() error = %v", err)
	}
	commit := EvaluationIngestCommit{
		PreviousNextSequence: state.NextSequence(),
		Sequence:             1,
		RecordDigest:         digest,
		Aggregate:            aggregate,
		Snapshot:             collector.Snapshot(),
		Observation:          observation,
	}

	// The first request wins through the ordinary path.
	ingestOne(t, plane, run, 1, record)

	// The second applies the same commit against its now-stale view.
	second, err := store.CommitEvaluationIngest(t.Context(), commit)
	if err != nil {
		t.Fatalf("second CommitEvaluationIngest() error = %v", err)
	}
	if second.Disposition != EvaluationIngestAlreadyCommitted {
		t.Fatalf("disposition = %v, want already-committed", second.Disposition)
	}
	if second.History.RetainedCount() != 1 {
		t.Errorf("replayed history reports %d retained, want 1",
			second.History.RetainedCount())
	}
	if got := retainedSequences(t, store, run.ID()); len(got) != 1 {
		t.Errorf("retained sequences = %v, want exactly one row", got)
	}
}

// ---------------------------------------------------------------------
// Rejection and rollback
// ---------------------------------------------------------------------

// A rejected record retains nothing and moves nothing. Each case is a
// different rejection point, and all three must leave identical state.
func TestRejectedRecordRetainsNoObservation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*IngestRequest)
		wantErr error
	}{
		{
			name:    "sequence gap",
			mutate:  func(r *IngestRequest) { r.Sequence = 7 },
			wantErr: ErrIngestSequence,
		},
		{
			name:    "wrong behavioral profile",
			mutate:  func(r *IngestRequest) { r.BehavioralProfile = "some-other-profile" },
			wantErr: ErrInvalidBehaviorRecord,
		},
		{
			name:    "environment mismatch",
			mutate:  func(r *IngestRequest) { r.Record.Environment = "production" },
			wantErr: ErrEnvironmentMismatch,
		},
		{
			name:    "policy selection contradicts itself",
			mutate:  func(r *IngestRequest) { r.Record.MatchedDefault = true },
			wantErr: ErrInvalidDecisionRecord,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _, plane, run := observationPlane(t)

			request := IngestRequest{
				RunID:             run.ID(),
				Sequence:          1,
				BehavioralProfile: run.BehavioralProfile(),
				Record:            observationTestRecord(run, "e1"),
			}
			tc.mutate(&request)

			if _, err := plane.IngestDecisionRecord(t.Context(), request); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}

			if got := retainedSequences(t, store, run.ID()); len(got) != 0 {
				t.Errorf("retained %v after a rejected record, want none", got)
			}
			// The cursor must not have moved either: a rejected record leaves
			// the run exactly where it was.
			state, err := store.EvaluationIngestState(t.Context(), run.ID())
			if err != nil {
				t.Fatalf("EvaluationIngestState() error = %v", err)
			}
			if state.NextSequence() != 1 || state.RecordCount() != 0 {
				t.Errorf("cursor moved: next=%d records=%d, want 1 and 0",
					state.NextSequence(), state.RecordCount())
			}
			// And the history is unavailable rather than an empty complete —
			// nothing was ever retained for this run.
			page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
			if err != nil {
				t.Fatalf("RunObservations() error = %v", err)
			}
			if len(page.Observations) != 0 {
				t.Errorf("page holds %d observations, want none", len(page.Observations))
			}
			if page.History.State() != ObservationHistoryComplete {
				t.Errorf("history state = %v, want complete for a run that ingested nothing",
					page.History.State())
			}
		})
	}
}

// A transaction that fails inside the commit leaves evidence, history and
// cursor exactly as they were.
//
// The failure is forced the way the existing terminal-run test forces it: the
// run is completed after the evidence was computed, so the commit's own
// in-transaction status check refuses a write that has already done work.
func TestFailedCommitLeavesHistoryAndCursorUnchanged(t *testing.T) {
	store, _, plane, run := observationPlane(t)
	ctx := t.Context()

	// One accepted record, so there is prior state a failure could damage.
	ingestOne(t, plane, run, 1, observationTestRecord(run, "e1"))

	before := retainedSequences(t, store, run.ID())
	beforeState, err := store.EvaluationIngestState(ctx, run.ID())
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}

	// Compute a second record's evidence against the current view.
	record := observationTestRecord(run, "e2")
	aggregate, snapshot, err := loadEvidence(ctx, sqlQuerier{store.db}, run.ID())
	if err != nil {
		t.Fatalf("loadEvidence() error = %v", err)
	}
	aggregate, err = aggregate.AddRecord(record)
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	collector, err := behaviorCollectorFromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("behaviorCollectorFromSnapshot() error = %v", err)
	}
	if err := collector.Observe(record); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	digest, err := RecordDigest(record)
	if err != nil {
		t.Fatalf("RecordDigest() error = %v", err)
	}
	observation, err := ObservationFromRecord(2, record, false)
	if err != nil {
		t.Fatalf("ObservationFromRecord() error = %v", err)
	}

	// Now make the run terminal, so the commit's in-transaction check fails.
	completed, err := run.Complete(run.CreatedAt().Add(time.Hour))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, completed); err != nil {
		t.Fatalf("UpdateEvaluationRun() error = %v", err)
	}

	_, err = store.CommitEvaluationIngest(ctx, EvaluationIngestCommit{
		PreviousNextSequence: beforeState.NextSequence(),
		Sequence:             2,
		RecordDigest:         digest,
		Aggregate:            aggregate,
		Snapshot:             collector.Snapshot(),
		Observation:          observation,
	})
	if !errors.Is(err, ErrEvaluationState) {
		t.Fatalf("commit error = %v, want ErrEvaluationState", err)
	}

	after := retainedSequences(t, store, run.ID())
	if len(after) != len(before) {
		t.Errorf("retained sequences = %v after a failed commit, want %v", after, before)
	}
	afterState, err := store.EvaluationIngestState(ctx, run.ID())
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if afterState.NextSequence() != beforeState.NextSequence() ||
		afterState.RecordCount() != beforeState.RecordCount() {
		t.Errorf("cursor moved on a failed commit: next=%d records=%d, want %d and %d",
			afterState.NextSequence(), afterState.RecordCount(),
			beforeState.NextSequence(), beforeState.RecordCount())
	}
	page, err := store.RunObservations(ctx, run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if page.History.RetainedCount() != 1 || !page.History.Complete() {
		t.Errorf("history = %d retained, complete=%v; want 1 and true",
			page.History.RetainedCount(), page.History.Complete())
	}
}

// ---------------------------------------------------------------------
// Ordering and pagination
// ---------------------------------------------------------------------

// Pagination is deterministic across boundaries, with no duplicate and no
// omission — including when every record carries the *same* timestamp, which
// is the case a timestamp sort would make non-deterministic.
func TestObservationPaginationIsDeterministicUnderEqualTimestamps(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	const total = 17
	for i := 1; i <= total; i++ {
		record := observationTestRecord(run, fmt.Sprintf("e%02d", i))
		// Identical timestamp on every record, deliberately.
		record.Timestamp = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
		ingestOne(t, plane, run, uint64(i), record)
	}

	for _, limit := range []int{1, 2, 5, 16, MaxListPage} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			var seen []uint64
			after := uint64(0)
			for {
				page, err := store.RunObservations(t.Context(), run.ID(), after, limit)
				if err != nil {
					t.Fatalf("RunObservations(after=%d) error = %v", after, err)
				}
				if len(page.Observations) == 0 {
					break
				}
				if len(page.Observations) > limit {
					t.Fatalf("page holds %d observations, above the limit of %d",
						len(page.Observations), limit)
				}
				for _, o := range page.Observations {
					seen = append(seen, o.Sequence)
				}
				after = page.Observations[len(page.Observations)-1].Sequence
			}

			if len(seen) != total {
				t.Fatalf("paged over %d observations, want %d (%v)", len(seen), total, seen)
			}
			for i, sequence := range seen {
				if sequence != uint64(i+1) {
					t.Fatalf("observation %d has sequence %d; the page order is not the "+
						"ingest order (%v)", i, sequence, seen)
				}
			}
		})
	}
}

// A page read is bounded and its cursor is validated rather than coerced.
func TestObservationPageRefusesUnusableBounds(t *testing.T) {
	_, _, plane, run := observationPlane(t)

	for _, tc := range []struct {
		name  string
		after string
		limit int
	}{
		{"limit zero", "", 0},
		{"limit above the page bound", "", MaxListPage + 1},
		{"negative limit", "", -1},
		{"cursor is not a number", "abc", MaxListPage},
		{"cursor is not canonical", "01", MaxListPage},
		{"cursor is zero", "0", MaxListPage},
		{"cursor is signed", "+1", MaxListPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := plane.EvaluationRunObservations(
				t.Context(), run.ID(), tc.after, tc.limit); err == nil {
				t.Fatal("accepted an unusable page request")
			}
		})
	}
}

// One run's history never appears in another's page.
func TestObservationHistoryIsIsolatedBetweenRuns(t *testing.T) {
	store, _, plane, first := observationPlane(t)

	second := seedSecondRunningRun(t, store, first)

	ingestOne(t, plane, first, 1, observationTestRecord(first, "first-1"))
	ingestOne(t, plane, first, 2, observationTestRecord(first, "first-2"))
	ingestOne(t, plane, second, 1, observationTestRecord(second, "second-1"))

	firstPage, err := store.RunObservations(t.Context(), first.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations(first) error = %v", err)
	}
	secondPage, err := store.RunObservations(t.Context(), second.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations(second) error = %v", err)
	}

	if len(firstPage.Observations) != 2 {
		t.Fatalf("first run retained %d, want 2", len(firstPage.Observations))
	}
	if len(secondPage.Observations) != 1 {
		t.Fatalf("second run retained %d, want 1", len(secondPage.Observations))
	}
	for _, o := range firstPage.Observations {
		if o.EventID == "second-1" {
			t.Error("the second run's observation appeared in the first run's page")
		}
	}
	if secondPage.Observations[0].EventID != "second-1" {
		t.Errorf("second run's page holds %q", secondPage.Observations[0].EventID)
	}
	// Both runs restart their own sequence at 1, which is why the identity is
	// (run, sequence) rather than a sequence alone.
	if secondPage.Observations[0].Sequence != 1 {
		t.Errorf("second run's first observation has sequence %d, want 1",
			secondPage.Observations[0].Sequence)
	}
}

// A run that does not exist is an error, not an empty page.
func TestObservationsForAnUnknownRunAreNotFound(t *testing.T) {
	_, _, plane, _ := observationPlane(t)
	if _, err := plane.EvaluationRunObservations(
		t.Context(), "no-such-run", "", MaxListPage); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("error = %v, want ErrStoreNotFound", err)
	}
}

// ---------------------------------------------------------------------
// Lineage, arrival order and operational evidence
// ---------------------------------------------------------------------

// Root, child, missing-parent and out-of-order observations are all retained,
// all report their recorded lineage, and none is rejected.
func TestObservationsRetainLineageWhateverTheArrivalOrder(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	// A child arrives before the parent that started it, which is the ordinary
	// case: a parent span ends after its children.
	child := observationTestRecord(run, "child")
	child.SpanID = "span-child"
	child.ParentSpanID = "span-root"
	child.SpanLineage = event.LineageChild

	root := observationTestRecord(run, "root")
	root.SpanID = "span-root"
	root.ParentSpanID = ""
	root.SpanLineage = event.LineageRoot

	orphan := observationTestRecord(run, "orphan")
	orphan.SpanID = "span-orphan"
	orphan.ParentSpanID = "span-never-arrives"
	orphan.SpanLineage = event.LineageChild

	detached := observationTestRecord(run, "detached")
	detached.SpanID = ""
	detached.ParentSpanID = ""
	detached.SpanLineage = event.LineageUnspecified

	ingestOne(t, plane, run, 1, child)
	ingestOne(t, plane, run, 2, root)
	ingestOne(t, plane, run, 3, orphan)
	ingestOne(t, plane, run, 4, detached)

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 4 {
		t.Fatalf("retained %d observations, want 4", len(page.Observations))
	}

	want := []struct {
		eventID string
		parent  string
		lineage event.SpanLineage
	}{
		{"child", "span-root", event.LineageChild},
		{"root", "", event.LineageRoot},
		{"orphan", "span-never-arrives", event.LineageChild},
		{"detached", "", event.LineageUnspecified},
	}
	for i, w := range want {
		got := page.Observations[i]
		if got.EventID != w.eventID {
			t.Errorf("observation %d is %q, want %q; arrival order is the page order",
				i, got.EventID, w.eventID)
		}
		if got.ParentSpanID != w.parent {
			t.Errorf("%s parent = %q, want %q", w.eventID, got.ParentSpanID, w.parent)
		}
		if got.SpanLineage != w.lineage {
			t.Errorf("%s lineage = %q, want %q", w.eventID, got.SpanLineage, w.lineage)
		}
	}
}

// An unavailable duration and a measured zero stay different facts through
// storage, and every span status round-trips.
func TestObservationPreservesDurationAvailabilityAndStatus(t *testing.T) {
	store, _, plane, run := observationPlane(t)

	cases := []struct {
		id           string
		duration     string
		status       event.SpanStatus
		wantNanos    uint64
		wantObserved bool
	}{
		{"unavailable", "", event.StatusUnavailable, 0, false},
		{"measured-zero", "0", event.StatusUnset, 0, true},
		{"measured", "1500", event.StatusOK, 1500, true},
		{"failed", "42", event.StatusError, 42, true},
	}
	for i, tc := range cases {
		record := observationTestRecord(run, tc.id)
		record.DurationNanos = tc.duration
		record.SpanStatus = tc.status
		ingestOne(t, plane, run, uint64(i+1), record)
	}

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != len(cases) {
		t.Fatalf("retained %d observations, want %d", len(page.Observations), len(cases))
	}
	for i, tc := range cases {
		got := page.Observations[i]
		if got.DurationObserved != tc.wantObserved {
			t.Errorf("%s: DurationObserved = %v, want %v",
				tc.id, got.DurationObserved, tc.wantObserved)
		}
		if got.DurationNanos != tc.wantNanos {
			t.Errorf("%s: DurationNanos = %d, want %d", tc.id, got.DurationNanos, tc.wantNanos)
		}
		if got.SpanStatus != tc.status {
			t.Errorf("%s: SpanStatus = %q, want %q", tc.id, got.SpanStatus, tc.status)
		}
	}

	// The distinction is durable rather than reconstructed: a measured zero and
	// an unavailable duration must not read the same.
	zero, unavailable := page.Observations[1], page.Observations[0]
	if zero.DurationNanos == unavailable.DurationNanos &&
		zero.DurationObserved == unavailable.DurationObserved {
		t.Error("a measured zero and an unavailable duration are indistinguishable after storage")
	}
}

// ---------------------------------------------------------------------
// Bounds
// ---------------------------------------------------------------------

// Retention stops at the bound, ingest keeps going, and the run says its
// history is partial rather than presenting a prefix as the whole thing.
//
// Driven at the store rather than by ingesting 4097 records through the whole
// pipeline: the decision under test is planObservationRetention's, and the
// commit path applying it is covered by every other test in this file.
func TestObservationRetentionStopsAtTheBoundAndSaysSo(t *testing.T) {
	at := MaxRetainedObservations

	for _, tc := range []struct {
		name         string
		present      bool
		retained     uint64
		complete     bool
		sequence     uint64
		wantInsert   bool
		wantRetained uint64
		wantComplete bool
	}{
		{"first record of a fresh run", false, 0, false, 1, true, 1, true},
		{"first retained record of a legacy run", false, 0, false, 9, true, 1, false},
		{"an ordinary advance", true, 5, true, 6, true, 6, true},
		{"an advance on an already-partial history", true, 5, false, 6, true, 6, false},
		{"the last record that fits", true, uint64(at) - 1, true, 4096, true, uint64(at), true},
		{"the first record past the bound", true, uint64(at), true, 4097, false, uint64(at), false},
		{"well past the bound", true, uint64(at), false, 9000, false, uint64(at), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planObservationRetention(tc.present, tc.retained, tc.complete, tc.sequence)
			if got.Insert != tc.wantInsert {
				t.Errorf("Insert = %v, want %v", got.Insert, tc.wantInsert)
			}
			if got.Retained != tc.wantRetained {
				t.Errorf("Retained = %d, want %d", got.Retained, tc.wantRetained)
			}
			if got.Complete != tc.wantComplete {
				t.Errorf("Complete = %v, want %v", got.Complete, tc.wantComplete)
			}
		})
	}
}

// Saturation is degraded evidence, not a failed ingest: the aggregate and the
// cursor keep advancing after retention stops.
func TestIngestContinuesAfterRetentionSaturates(t *testing.T) {
	store, _, plane, run := observationPlane(t)
	ctx := t.Context()

	// Put the run's history at the bound directly — the point under test is
	// what the *next* ingest does, not the four thousand before it.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO `+tableObservationHistory+` (run_id, retained_count, complete)
		 VALUES (?, ?, 1)`,
		string(run.ID()), uint64Text(MaxRetainedObservations)); err != nil {
		t.Fatalf("seed saturated history: %v", err)
	}

	result := ingestOne(t, plane, run, 1, observationTestRecord(run, "e1"))
	if result.Disposition != IngestApplied {
		t.Fatalf("disposition = %v, want applied; saturation must not fail an ingest",
			result.Disposition)
	}
	if result.RecordCount != 1 {
		t.Errorf("record count = %d, want 1; the aggregate must keep advancing",
			result.RecordCount)
	}

	page, err := store.RunObservations(ctx, run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 0 {
		t.Errorf("retained %d observations past the bound, want none", len(page.Observations))
	}
	if page.History.State() != ObservationHistoryPartial {
		t.Errorf("history state = %v, want partial", page.History.State())
	}
	if page.History.Complete() {
		t.Error("a saturated history reports itself complete")
	}
}

// A retained count above the bound cannot have been written by this code, so
// it is refused rather than clamped.
func TestCorruptRetainedCountIsRefused(t *testing.T) {
	store, _, _, run := observationPlane(t)

	if _, err := store.db.ExecContext(t.Context(),
		`INSERT INTO `+tableObservationHistory+` (run_id, retained_count, complete)
		 VALUES (?, ?, 1)`,
		string(run.ID()), uint64Text(MaxRetainedObservations+1)); err != nil {
		t.Fatalf("seed corrupt history: %v", err)
	}

	if _, err := store.RunObservations(
		t.Context(), run.ID(), 0, MaxListPage); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("error = %v, want ErrStoreCorrupt", err)
	}
}

// ---------------------------------------------------------------------
// Migration honesty
// ---------------------------------------------------------------------

// writeSchemaV6 builds a task 084 database: every table v4 has, v5's
// child-collection indexes, v6's operational columns, and no observation
// history — stamped 6.
//
// Composed from the same statement builders the fresh schema uses, so the
// fixture cannot drift into a past that never existed.
func writeSchemaV6(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	db.SetMaxOpenConns(1)

	statements := append(schemaV1Statements(), ingestStateTableStatement())
	statements = append(statements,
		environmentsTableStatement(),
		promotionsTableStatement(),
		promotionsIndexStatement(),
		agentsByProjectIndexStatement(),
		candidatesByAgentIndexStatement(),
		runsByCandidateIndexStatement())
	for _, statement := range statements {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("create v6 schema: %v", err)
		}
	}
	for _, column := range operationalColumnDDL("TEXT") {
		if _, err := db.ExecContext(context.Background(),
			`ALTER TABLE `+tableAggregates+` ADD COLUMN `+column); err != nil {
			t.Fatalf("add v6 operational column: %v", err)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO platform_schema_version (id, version) VALUES (1, 6)`); err != nil {
		t.Fatalf("stamp v6: %v", err)
	}
	return db
}

// seedSchemaV6Run fills a v6 database with one running run that already holds
// evidence, exactly as a real pre-067 database would.
func seedSchemaV6Run(t *testing.T, db *sql.DB, runID string, records uint64) {
	t.Helper()
	seedV1Content(t, db, runID, records)
	// The same backfill v5 → v6 applies to rows that predate the columns: an
	// aggregate written before schema 6 observed no duration and no status.
	if _, err := db.ExecContext(context.Background(),
		operationalBackfillStatement(tableAggregates)); err != nil {
		t.Fatalf("backfill operational columns: %v", err)
	}
}

// A schema-6 database migrates forward and invents no history.
//
// The run kept its four records and gained no observations, and it reports
// *unavailable* rather than an empty complete history — the distinction the
// whole two-table design exists to make, and the same refusal task 066 applied
// when it declined to synthesize promotions from old evaluations.
func TestSchemaV6MigrationInventsNoObservationHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	db := writeSchemaV6(t, path)
	seedSchemaV6Run(t, db, "run-legacy", 4)
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v6 database error = %v", err)
	}
	defer store.Close()

	if version, _ := store.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}

	// Nothing was written into either new table.
	if got := retainedSequences(t, store, "run-legacy"); len(got) != 0 {
		t.Errorf("migration invented observations %v", got)
	}
	var historyRows int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM `+tableObservationHistory).Scan(&historyRows); err != nil {
		t.Fatalf("count history rows: %v", err)
	}
	if historyRows != 0 {
		t.Errorf("migration wrote %d history rows, want 0", historyRows)
	}

	page, err := store.RunObservations(t.Context(), "run-legacy", 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 0 {
		t.Errorf("legacy run reports %d observations, want 0", len(page.Observations))
	}
	if page.History.State() != ObservationHistoryUnavailable {
		t.Errorf("history state = %v, want unavailable; a run whose records predate "+
			"retention must not report an empty complete history", page.History.State())
	}
	if page.History.Complete() {
		t.Error("a legacy run reports its history complete")
	}

	// And the evidence it did hold is untouched.
	state, err := store.EvaluationIngestState(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}
	if state.RecordCount() != 4 {
		t.Errorf("record count after migration = %d, want 4", state.RecordCount())
	}
}

// A migrated run that resumes ingesting reports *partial*, never complete: its
// earliest records were never retained, and a history that starts in the middle
// is not the whole run.
func TestMigratedRunThatResumesIngestingReportsPartialHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	db := writeSchemaV6(t, path)
	seedSchemaV6Run(t, db, "run-legacy", 4)
	db.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	defer store.Close()

	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatalf("NewControlPlane() error = %v", err)
	}
	run, err := store.EvaluationRun(t.Context(), "run-legacy")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}

	// The cursor a migrated run derives is recordCount + 1.
	ingestOne(t, plane, run, 5, observationTestRecord(run, "e5"))

	page, err := store.RunObservations(t.Context(), run.ID(), 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunObservations() error = %v", err)
	}
	if len(page.Observations) != 1 {
		t.Fatalf("retained %d observations, want 1", len(page.Observations))
	}
	if page.Observations[0].Sequence != 5 {
		t.Errorf("retained sequence = %d, want 5", page.Observations[0].Sequence)
	}
	if page.History.State() != ObservationHistoryPartial {
		t.Errorf("history state = %v, want partial; four earlier records were never retained",
			page.History.State())
	}
	if page.History.Complete() {
		t.Error("a history missing its first four records reports itself complete")
	}
}
