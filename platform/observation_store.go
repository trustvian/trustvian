package platform

// The parts of per-observation history that both backends share.
//
// The read path and the retention decision are written once here, against the
// same evidenceQuerier abstraction the aggregate restore uses, for the reason
// querier.go gives: a differential test finds drift after it happens, and
// sharing one definition means there is nothing to drift. Only the writes stay
// in each backend's file, because this abstraction deliberately carries no
// Exec and the upsert dialects genuinely differ.

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
)

// testHookInObservationRead runs inside a page read, between the history
// metadata and the observation rows. Nil in production, and the call costs a
// nil check.
//
// The same seam testHookInRunMutation opens for the write path, for the same
// reason and with the same reasoning about why an atomic rather than a plain
// variable: a page read is three statements, they complete in microseconds, and
// two goroutines released together will almost always run in series. A test that
// relied on winning that race would pass against a torn read as readily as
// against a consistent one — which is the defect it was written to catch.
//
// With this hook a test can commit an ingest *at* the one instant that matters,
// and the snapshot either holds across it or it does not.
var testHookInObservationRead atomic.Pointer[func()]

// runObservationReadHook invokes the hook if a test installed one.
//
// Nil in production: nothing outside this package's tests can reach the
// variable above, no non-test code path writes it, and no configuration can
// install behaviour into it.
func runObservationReadHook() {
	if hook := testHookInObservationRead.Load(); hook != nil {
		(*hook)()
	}
}

// ObservationFilter narrows a page of retained history to the observations
// that support one finding.
//
// A value rather than free-form criteria: every field maps to one equality
// predicate over a column task 067 already retains, which is what keeps the
// filter expressible as a bounded SQL `WHERE` rather than as a scan the caller
// post-processes. There is deliberately no range, no negation and no
// disjunction — task 085 needs none, and each would be a query shape nothing
// has measured.
//
// The zero value matches everything, which is what the unfiltered page read
// passes.
type ObservationFilter struct {
	// FingerprintID selects one behavioral identity. Matched through task
	// 067's bounded digest index **and** against the original value, because a
	// digest narrows rather than identifies.
	FingerprintID string

	// Decision and RiskLevel select by recorded outcome, for the gate checks
	// that count one.
	Decision  string
	RiskLevel string
}

// empty reports whether this filter constrains nothing.
func (f ObservationFilter) empty() bool {
	return f.FingerprintID == "" && f.Decision == "" && f.RiskLevel == ""
}

// predicate renders the filter as SQL and bind arguments.
//
// Assembled from compile-time constants only; every caller-supplied value is a
// bound parameter, which is the rule this package applies to all of its SQL.
//
// Applied **before** the page limit, by being part of the same statement. A
// filter applied to an already-paginated page would return three rows where
// sixty-four matches exist, and would make the continuation cursor describe the
// unfiltered stream rather than the answer.
func (f ObservationFilter) predicate() (string, []any) {
	var sql strings.Builder
	args := make([]any, 0, 4)

	if f.FingerprintID != "" {
		// Both halves, always. The key is what the index can seek on; the value
		// is what makes a match mean what it says.
		sql.WriteString(" AND fingerprint_key = ? AND fingerprint_id = ?")
		args = append(args, observationDigestKey(f.FingerprintID), f.FingerprintID)
	}
	if f.Decision != "" {
		sql.WriteString(" AND decision = ?")
		args = append(args, f.Decision)
	}
	if f.RiskLevel != "" {
		sql.WriteString(" AND risk_level = ?")
		args = append(args, f.RiskLevel)
	}
	return sql.String(), args
}

// observationRetention is what retaining one observation should do, decided
// from durable state and applied by whichever backend asked.
//
// A value rather than a method on the store, so both backends take the same
// decision from the same inputs and a test can exercise it without a database.
type observationRetention struct {
	// Insert is false once the run has reached MaxRetainedObservations.
	Insert bool

	// Retained and Complete are what the history row should hold afterwards.
	Retained uint64
	Complete bool
}

// planObservationRetention decides what one record's history costs.
//
// present, retained and complete describe the run's history row as it stands
// inside the commit transaction; sequence is the ingest sequence being
// admitted.
//
// Two things make a history incomplete, and this is where both are decided:
//
//   - **Saturation.** At MaxRetainedObservations the run stops retaining and
//     keeps ingesting, because refusing the record would discard sound decision
//     evidence because historical evidence filled up — the rule the behavior
//     collector already applies at 512 distinct behaviors.
//   - **A pre-retention prefix.** A run that began ingesting under schema 6 and
//     resumed under schema 7 creates its history row partway through. It is born
//     incomplete, because sequence 1 is not the record it is holding. Claiming
//     otherwise would report a history that starts in the middle as the whole
//     run.
func planObservationRetention(
	present bool, retained uint64, complete bool, sequence uint64,
) observationRetention {
	if !present {
		// The first record this run retains. Complete only if nothing was
		// admitted before it — which, because sequences are dense and start
		// at 1, is exactly sequence 1.
		return observationRetention{Insert: true, Retained: 1, Complete: sequence == 1}
	}
	if retained >= MaxRetainedObservations {
		return observationRetention{Insert: false, Retained: retained, Complete: false}
	}
	return observationRetention{Insert: true, Retained: retained + 1, Complete: complete}
}

// validateCommitObservation refuses a commit whose observation does not
// describe the record being committed.
//
// The sequence is half the observation's identity and the whole of its page
// key, so a mismatch is not a cosmetic disagreement: a commit carrying the
// zero value would write every record at sequence 0 and collide with itself on
// the second one. Refused at the store edge, before any write, so the
// diagnostic names the contract rather than a unique-constraint violation.
func validateCommitObservation(commit EvaluationIngestCommit) error {
	if commit.Observation.Sequence != commit.Sequence {
		return fmt.Errorf(
			"%w: commit at sequence %d carries an observation for sequence %d",
			ErrInvalidObservation, commit.Sequence, commit.Observation.Sequence)
	}
	return nil
}

// loadObservationHistoryRow reads a run's history row, if it has one.
//
// The absent case is not an error and is load-bearing: a run with records and
// no row here predates retention, and that is a different statement from
// "retained nothing".
func loadObservationHistoryRow(
	ctx context.Context, q rowQuerier, id EvaluationRunID,
) (present bool, retained uint64, complete bool, err error) {
	var retainedText string
	var completeInt int
	scanErr := q.queryRow(ctx, q.rebind(
		`SELECT retained_count, complete FROM `+tableObservationHistory+` WHERE run_id = ?`),
		string(id)).Scan(&retainedText, &completeInt)

	switch {
	case q.noRows(scanErr):
		return false, 0, false, nil
	case scanErr != nil:
		return false, 0, false, fmt.Errorf("platform: load observation history: %w", scanErr)
	}

	retained, err = parseUint64Text("observation retained_count", retainedText)
	if err != nil {
		return false, 0, false, err
	}
	complete, err = parseStoredBool("observation history complete", completeInt)
	if err != nil {
		return false, 0, false, err
	}
	// A retained count above the bound cannot have been written by
	// planObservationRetention, which stops inserting at it. Refusing rather
	// than clamping keeps the corruption visible, the way parseUint64Text
	// refuses a non-canonical counter instead of coercing it.
	if retained > MaxRetainedObservations {
		return false, 0, false, fmt.Errorf(
			"%w: run %s retains %d observations, above the bound of %d",
			ErrStoreCorrupt, preview(string(id)), retained, MaxRetainedObservations)
	}
	return true, retained, complete, nil
}

// observationRecordCount reads how many records a run's aggregate holds.
//
// Read directly rather than through loadEvidence, which would also load the
// behavior snapshot and up to 512 entries to answer a one-column question. A
// run with no aggregate has ingested nothing, which is zero rather than an
// error.
func observationRecordCount(
	ctx context.Context, q rowQuerier, id EvaluationRunID,
) (uint64, error) {
	var text string
	err := q.queryRow(ctx, q.rebind(
		`SELECT record_count FROM `+tableAggregates+` WHERE run_id = ?`),
		string(id)).Scan(&text)
	switch {
	case q.noRows(err):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("platform: load observation record count: %w", err)
	}
	return parseUint64Text("record_count", text)
}

// observationHistoryFor assembles a run's history state from durable rows.
func observationHistoryFor(
	ctx context.Context, q rowQuerier, id EvaluationRunID,
) (ObservationHistory, error) {
	present, retained, complete, err := loadObservationHistoryRow(ctx, q, id)
	if err != nil {
		return ObservationHistory{}, err
	}
	recordCount, err := observationRecordCount(ctx, q, id)
	if err != nil {
		return ObservationHistory{}, err
	}
	return NewObservationHistory(present, retained, complete, recordCount), nil
}

// runObservationPage reads one bounded page of a run's retained history.
//
// Written once for both backends. `after` is exclusive and 0 starts at the
// beginning; ordering is by the zero-padded sequence key, which is byte order
// and numeric order at the same time on both backends.
//
// **The caller must pass a querier bound to a transaction that provides a
// consistent snapshot**, because this reads the history row, the record count
// and the observation rows as three separate statements. Run against a pool, a
// concurrent ingest committing between any two of them produces a page that
// describes no moment that ever existed — a retained count of 1 beside two rows,
// or completeness metadata from before a saturation the rows already show. Each
// backend's RunObservations is what supplies the snapshot, and the difference
// between "these three reads agree" and "these three reads each happened" is not
// visible in the values themselves, which is why it is stated here rather than
// left to be noticed.
//
// The run must exist: "no such run" is a 404 and "that run retained nothing"
// is an empty page, and collapsing them would make a typo indistinguishable
// from an answer.
func runObservationPage(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID,
	filter ObservationFilter, after uint64, limit int,
) (ObservationPage, error) {
	if _, err := loadRun(ctx, q, id); err != nil {
		return ObservationPage{}, err
	}

	history, err := observationHistoryFor(ctx, q, id)
	if err != nil {
		return ObservationPage{}, err
	}

	// The window this function's caller must close. Everything above and
	// everything below has to describe one instant: a retained count of 1 beside
	// two observation rows is a state that never existed, and a caller reading
	// it cannot tell that it never existed.
	runObservationReadHook()

	filterSQL, filterArgs := filter.predicate()
	args := make([]any, 0, len(filterArgs)+3)
	args = append(args, string(id), observationSequenceKey(after))
	args = append(args, filterArgs...)
	args = append(args, limit)

	rows, err := q.query(ctx, q.rebind(
		`SELECT `+observationSelectList()+`
		   FROM `+tableObservations+`
		  WHERE run_id = ? AND sequence > ?`+filterSQL+`
		  ORDER BY sequence
		  LIMIT ?`),
		args...)
	if err != nil {
		return ObservationPage{}, fmt.Errorf("platform: load observations: %w", err)
	}
	defer rows.Close()

	observations := make([]Observation, 0, limit)
	for rows.Next() {
		var scan observationScan
		if err := rows.Scan(scan.targets()...); err != nil {
			return ObservationPage{}, fmt.Errorf("platform: scan observation: %w", err)
		}
		o, err := scan.observation()
		if err != nil {
			return ObservationPage{}, err
		}
		observations = append(observations, o)
	}
	if err := rows.Err(); err != nil {
		return ObservationPage{}, fmt.Errorf("platform: load observations: %w", err)
	}

	// A non-empty page has obviously matched something; only an empty one needs
	// asking, which keeps the extra statement off every page read.
	matched := len(observations) > 0
	if !matched {
		matched, err = observationsMatch(ctx, q, id, filter)
		if err != nil {
			return ObservationPage{}, err
		}
	}

	return ObservationPage{
		Observations: observations,
		History:      history,
		Matched:      matched,
	}, nil
}

// observationsMatch reports whether the filter matches anything in the run,
// ignoring the page cursor.
//
// Bounded to one row and issued inside the caller's transaction, so it answers
// about the same instant the page and the history metadata describe. Asking it
// outside that snapshot would let a concurrent ingest make "nothing matches"
// and "here are the matches" both true of one response.
func observationsMatch(
	ctx context.Context, q rowQuerier, id EvaluationRunID, filter ObservationFilter,
) (bool, error) {
	filterSQL, filterArgs := filter.predicate()
	args := make([]any, 0, len(filterArgs)+1)
	args = append(args, string(id))
	args = append(args, filterArgs...)

	var one int
	err := q.queryRow(ctx, q.rebind(
		`SELECT 1
		   FROM `+tableObservations+`
		  WHERE run_id = ?`+filterSQL+`
		  LIMIT 1`),
		args...).Scan(&one)
	switch {
	case q.noRows(err):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("platform: probe observations: %w", err)
	}
	return true, nil
}
