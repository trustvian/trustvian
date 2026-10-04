package platform

// Task 100: the traces in a run's retained history, on every backend.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/trustvian/trustvian/event"
)

// traceFixtureCommit commits one observation carrying `trace` (empty for an
// untraced action) and `status`.
func traceFixtureCommit(
	t *testing.T, store Store, run EvaluationRun, sequence int, trace string, status event.SpanStatus,
) {
	t.Helper()
	aggregate, snapshot := conformanceEvidence(t, run, sequence)
	observation := conformanceObservation(t, uint64(sequence))
	observation.TraceID = trace
	if trace == "" {
		observation.SpanID = ""
		observation.ParentSpanID = ""
		observation.SpanLineage = event.LineageUnspecified
	}
	observation.SpanStatus = status
	commit := EvaluationIngestCommit{
		Aggregate:            aggregate,
		Snapshot:             snapshot,
		Sequence:             uint64(sequence),
		PreviousNextSequence: uint64(sequence),
		RecordDigest:         strings.Repeat(fmt.Sprintf("%x", sequence%16), 64),
		Observation:          observation,
	}
	if _, err := store.CommitEvaluationIngest(t.Context(), commit); err != nil {
		t.Fatalf("CommitEvaluationIngest(%s, %d) error = %v", run.ID(), sequence, err)
	}
}

// readTraces pages a run's traces to the end at `limit` per page.
func readTraces(t *testing.T, store Store, run EvaluationRunID, after uint64, limit int) []TraceSummary {
	t.Helper()
	var seen []TraceSummary
	for range 64 {
		page, err := store.RunTraces(t.Context(), run, after, limit)
		if err != nil {
			t.Fatalf("RunTraces(after=%d) error = %v", after, err)
		}
		if len(page.Traces) == 0 {
			return seen
		}
		seen = append(seen, page.Traces...)
		after = page.Traces[len(page.Traces)-1].FirstSequence
	}
	t.Fatal("RunTraces never reached an empty page")
	return nil
}

func conformTraceSummaries(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	ctx := t.Context()
	run := startedRun(t, store, "run-1")

	empty, err := store.RunTraces(ctx, "run-1", 0, MaxListPage)
	if err != nil {
		t.Fatalf("RunTraces() on an empty run error = %v", err)
	}
	if len(empty.Traces) != 0 || empty.History.State() != ObservationHistoryComplete {
		t.Errorf("empty run = %d traces, history %v; want none, complete",
			len(empty.Traces), empty.History.State())
	}

	// Interleaved, with two untraced actions and two error spans, so order by
	// first appearance, the counts and the exclusion are all visible.
	plan := []struct {
		trace  string
		status event.SpanStatus
	}{
		{"t-b", event.StatusOK}, {"t-a", event.StatusOK}, {"", event.StatusOK},
		{"t-b", event.StatusError}, {"t-c", event.StatusUnset}, {"t-a", event.StatusError},
		{"t-d", event.StatusUnavailable}, {"t-b", event.StatusOK}, {"", event.StatusError},
		{"t-e", event.StatusOK},
	}
	for i, step := range plan {
		traceFixtureCommit(t, store, run, i+1, step.trace, step.status)
	}

	// A second run carrying the same identifier: run-scoped, never merged.
	other := startedSiblingRun(t, store, "run-2")
	traceFixtureCommit(t, store, other, 1, "t-a", event.StatusError)

	want := []TraceSummary{
		{TraceID: "t-b", Observations: 3, FirstSequence: 1, LastSequence: 8, ErrorSpans: 1},
		{TraceID: "t-a", Observations: 2, FirstSequence: 2, LastSequence: 6, ErrorSpans: 1},
		{TraceID: "t-c", Observations: 1, FirstSequence: 5, LastSequence: 5, ErrorSpans: 0},
		{TraceID: "t-d", Observations: 1, FirstSequence: 7, LastSequence: 7, ErrorSpans: 0},
		{TraceID: "t-e", Observations: 1, FirstSequence: 10, LastSequence: 10, ErrorSpans: 0},
	}
	for _, limit := range []int{1, 2, 3, MaxListPage} {
		got := readTraces(t, store, "run-1", 0, limit)
		if len(got) != len(want) {
			t.Fatalf("limit %d: read %d traces, want %d: %+v", limit, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("limit %d: trace %d = %+v, want %+v", limit, i, got[i], want[i])
			}
		}
	}

	page, err := store.RunTraces(ctx, "run-1", 0, 2)
	if err != nil {
		t.Fatalf("RunTraces() error = %v", err)
	}
	if page.History.RetainedCount() != 10 || !page.History.Complete() {
		t.Errorf("history = %d retained complete=%v, want 10 and true",
			page.History.RetainedCount(), page.History.Complete())
	}

	// The cursor is a position, not a snapshot: a later observation of a
	// trace already listed does not move it, and a new trace appears after
	// the reader's position.
	traceFixtureCommit(t, store, run, 11, "t-a", event.StatusOK)
	traceFixtureCommit(t, store, run, 12, "t-f", event.StatusOK)
	rest := readTraces(t, store, "run-1", page.Traces[len(page.Traces)-1].FirstSequence, 2)
	var ids []string
	for _, trace := range rest {
		ids = append(ids, trace.TraceID)
	}
	if strings.Join(ids, ",") != "t-c,t-d,t-e,t-f" {
		t.Errorf("continuation after concurrent ingest = %v, want [t-c t-d t-e t-f]", ids)
	}

	isolated := readTraces(t, store, "run-2", 0, MaxListPage)
	if len(isolated) != 1 || isolated[0].Observations != 1 || isolated[0].ErrorSpans != 1 {
		t.Errorf("run-2 traces = %+v; a trace identifier is run-scoped", isolated)
	}

	if _, err := store.RunTraces(ctx, "no-such-run", 0, MaxListPage); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("RunTraces() on an unknown run = %v, want ErrStoreNotFound", err)
	}
}

// TestEvaluationRunTracesValidatesItsCursorAndLimit covers the control-plane
// half: the same cursor and limit rules as the observation page.
func TestEvaluationRunTracesValidatesItsCursorAndLimit(t *testing.T) {
	_, _, plane, run := observationPlane(t)

	for _, tc := range []struct {
		name  string
		after string
		limit int
		want  error
	}{
		{"zero limit", "", 0, ErrInvalidID},
		{"limit above a page", "", MaxListPage + 1, ErrInvalidID},
		{"non-canonical cursor", "01", 1, ErrObservationCursor},
		{"zero cursor", "0", 1, ErrObservationCursor},
		{"negative cursor", "-1", 1, ErrObservationCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := plane.EvaluationRunTraces(t.Context(), run.ID(), tc.after, tc.limit); !errors.Is(err, tc.want) {
				t.Errorf("EvaluationRunTraces() error = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := plane.EvaluationRunTraces(t.Context(), "missing", "", 1); !errors.Is(err, ErrStoreNotFound) {
		t.Errorf("unknown run error = %v, want ErrStoreNotFound", err)
	}
	page, err := plane.EvaluationRunTraces(t.Context(), run.ID(), "", MaxListPage)
	if err != nil {
		t.Fatalf("EvaluationRunTraces() error = %v", err)
	}
	if len(page.Traces) != 0 {
		t.Errorf("a run with no ingest listed %d traces", len(page.Traces))
	}
}
