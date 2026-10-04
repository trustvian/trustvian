package platform

// The traces in a run's retained history (task 100).
//
// Task 067 retains observations, not traces, and task 076 reads a trace's
// observations by narrowing a run's history to one trace identifier. What was
// missing is the list that identifier is chosen from: without it a trace is
// reachable only by reading the identifier off one observation.
//
// This is that list and nothing more. Each entry is a reduction over retained
// rows that already exist — how many observations carried the trace, the first
// and last ingest sequence among them, and how many recorded an error span
// status — computed by one bounded SQL statement in the same read snapshot as
// the history state. It retains nothing new, infers no structure and names no
// root: parentage is the observation page's to show, from the recorded parent
// reference, exactly as before.
//
// **It describes the evaluated actions of a trace, not the distributed trace.**
// Spans the engine never evaluated were never retained, and a run's retention
// stops at MaxRetainedObservations; the history state says which applies.

import (
	"context"
	"fmt"

	"github.com/trustvian/trustvian/event"
)

// TraceSummary is one trace as the run's retained history holds it.
type TraceSummary struct {
	// TraceID is the producer's identifier, as recorded. Run-scoped: the same
	// identifier in another run is a different entry (task 084).
	TraceID string

	// Observations is how many retained observations carry the trace.
	Observations uint64

	// FirstSequence and LastSequence bound the ingest sequences of those
	// observations. FirstSequence is also the collection's cursor: retention
	// only ever appends, so a trace's first retained sequence never changes
	// once it exists, which is what makes it a safe keyset key (ADR 0041).
	FirstSequence uint64
	LastSequence  uint64

	// ErrorSpans counts observations whose recorded span status is "error".
	// Not a verdict: a status the producer set, counted.
	ErrorSpans uint64
}

// TracePage is one bounded page of a run's traces and what the history they
// were read from is.
type TracePage struct {
	Traces  []TraceSummary
	History ObservationHistory
}

// TraceSummaryStore lists the traces in a run's retained history.
//
// A capability of its own, like ObservationStore, rather than a widening of
// it: a backend can retain history without offering this, and the control
// plane answers "nothing retained" for one that does not.
type TraceSummaryStore interface {
	// RunTraces returns up to limit traces whose first retained sequence is
	// after `after`, ordered by that sequence, with the history state from the
	// same read. after is exclusive and 0 starts at the beginning; limit is
	// 1..MaxListPage. Observations with no trace identifier are not a trace
	// and are not listed.
	RunTraces(ctx context.Context, id EvaluationRunID, after uint64, limit int) (TracePage, error)
}

// correlationColumn is one correlation identifier a run's retained history can
// be summarised by: the value column and the digest key column it is indexed
// through (task 067). A closed set of two, so no caller can turn this into a
// GROUP BY over an arbitrary column.
type correlationColumn struct{ value, key, noun string }

var (
	correlationTrace   = correlationColumn{value: "trace_id", key: "trace_key", noun: "trace"}
	correlationSession = correlationColumn{value: "session_id", key: "session_key", noun: "session"}
)

// runTracePage is task 100's trace list: the correlation summary over traces.
func runTracePage(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID, after uint64, limit int,
) (TracePage, error) {
	return runCorrelationPage(ctx, q, correlationTrace, id, after, limit)
}

// runCorrelationPage is the one query both backends run, inside their own read
// snapshot, for either correlation column.
//
// GROUP BY the digest key *and* the value: the key is what the index holds,
// and grouping on the value beside it means a digest collision splits into two
// entries rather than merging two traces into one. COUNT of a CASE rather than
// SUM, because PostgreSQL's SUM of an integer is NUMERIC and COUNT is BIGINT on
// both backends.
func runCorrelationPage(
	ctx context.Context, q evidenceQuerier, column correlationColumn,
	id EvaluationRunID, after uint64, limit int,
) (TracePage, error) {
	if _, err := loadRun(ctx, q, id); err != nil {
		return TracePage{}, err
	}
	history, err := observationHistoryFor(ctx, q, id)
	if err != nil {
		return TracePage{}, err
	}

	rows, err := q.query(ctx, q.rebind(
		`SELECT `+column.value+`,
		        COUNT(*),
		        MIN(sequence) AS first_sequence,
		        MAX(sequence),
		        COUNT(CASE WHEN span_status = ? THEN 1 END)
		   FROM `+tableObservations+`
		  WHERE run_id = ? AND `+column.key+` <> ''
		  GROUP BY `+column.key+`, `+column.value+`
		 HAVING MIN(sequence) > ?
		  ORDER BY first_sequence
		  LIMIT ?`),
		string(event.StatusError), string(id), observationSequenceKey(after), limit)
	if err != nil {
		return TracePage{}, fmt.Errorf("platform: load %ss: %w", column.noun, err)
	}
	defer rows.Close()

	traces := make([]TraceSummary, 0, limit)
	for rows.Next() {
		var (
			traceID           string
			count, errorSpans int64
			firstKey, lastKey string
		)
		if err := rows.Scan(&traceID, &count, &firstKey, &lastKey, &errorSpans); err != nil {
			return TracePage{}, fmt.Errorf("platform: scan %s: %w", column.noun, err)
		}
		first, err := parseObservationSequenceKey("trace first sequence", firstKey)
		if err != nil {
			return TracePage{}, err
		}
		last, err := parseObservationSequenceKey("trace last sequence", lastKey)
		if err != nil {
			return TracePage{}, err
		}
		if count < 1 || errorSpans < 0 || errorSpans > count {
			return TracePage{}, fmt.Errorf("%w: trace counts %d/%d are not a reduction of retained rows",
				ErrStoreCorrupt, errorSpans, count)
		}
		traces = append(traces, TraceSummary{
			TraceID:       traceID,
			Observations:  uint64(count),
			FirstSequence: first,
			LastSequence:  last,
			ErrorSpans:    uint64(errorSpans),
		})
	}
	if err := rows.Err(); err != nil {
		return TracePage{}, fmt.Errorf("platform: load %ss: %w", column.noun, err)
	}
	return TracePage{Traces: traces, History: history}, nil
}

// SessionSummaryStore lists the sessions in a run's retained history (task
// 103): the same summary as TraceSummaryStore over the session column.
// TraceSummary's TraceID carries the session identifier on these pages; the
// HTTP layer names the field for what it is.
type SessionSummaryStore interface {
	RunSessions(ctx context.Context, id EvaluationRunID, after uint64, limit int) (TracePage, error)
}

// EvaluationRunSessions is EvaluationRunTraces over sessions, with the same
// cursor, bounds and not-found answer.
func (c *ControlPlane) EvaluationRunSessions(
	ctx context.Context, runID EvaluationRunID, after string, limit int,
) (TracePage, error) {
	if err := validateID("evaluation run session run id", string(runID)); err != nil {
		return TracePage{}, err
	}
	if err := validateObservationPage(after, limit); err != nil {
		return TracePage{}, err
	}
	cursor, err := ParseObservationCursor(after)
	if err != nil {
		return TracePage{}, err
	}
	if _, err := c.evaluations.EvaluationRun(ctx, runID); err != nil {
		return TracePage{}, err
	}
	store, ok := c.ingest.(SessionSummaryStore)
	if !ok {
		return TracePage{History: ObservationHistory{}}, nil
	}
	return store.RunSessions(ctx, runID, cursor, limit)
}

// EvaluationRunTraces returns one bounded page of the traces in a run's
// retained history (task 100).
//
// The cursor is the decimal first sequence of the last trace on the previous
// page — the same encoding as the observation cursor, because it is the same
// kind of value. A missing run is ErrStoreNotFound, as for every other run
// read; a store without the capability retained nothing and says so with an
// empty page and an unavailable history.
func (c *ControlPlane) EvaluationRunTraces(
	ctx context.Context, runID EvaluationRunID, after string, limit int,
) (TracePage, error) {
	if err := validateID("evaluation run trace run id", string(runID)); err != nil {
		return TracePage{}, err
	}
	if err := validateObservationPage(after, limit); err != nil {
		return TracePage{}, err
	}
	cursor, err := ParseObservationCursor(after)
	if err != nil {
		return TracePage{}, err
	}
	if _, err := c.evaluations.EvaluationRun(ctx, runID); err != nil {
		return TracePage{}, err
	}
	store, ok := c.ingest.(TraceSummaryStore)
	if !ok {
		return TracePage{History: ObservationHistory{}}, nil
	}
	return store.RunTraces(ctx, runID, cursor, limit)
}
