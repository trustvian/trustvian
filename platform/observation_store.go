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
)

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
// The run must exist: "no such run" is a 404 and "that run retained nothing"
// is an empty page, and collapsing them would make a typo indistinguishable
// from an answer.
func runObservationPage(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID, after uint64, limit int,
) (ObservationPage, error) {
	if _, err := loadRun(ctx, q, id); err != nil {
		return ObservationPage{}, err
	}

	history, err := observationHistoryFor(ctx, q, id)
	if err != nil {
		return ObservationPage{}, err
	}

	rows, err := q.query(ctx, q.rebind(
		`SELECT `+observationSelectList()+`
		   FROM `+tableObservations+`
		  WHERE run_id = ? AND sequence > ?
		  ORDER BY sequence
		  LIMIT ?`),
		string(id), observationSequenceKey(after), limit)
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

	return ObservationPage{Observations: observations, History: history}, nil
}
