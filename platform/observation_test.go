package platform

// The observation value, its history state and its page cursor, with no
// database involved.
//
// These are the decisions the persistence tests would otherwise only exercise
// incidentally: which of three states a run's history is in, and what counts as
// a cursor this contract produced.

import (
	"errors"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// The three states, and the one distinction that exists to prevent a lie: a
// run with records and no history row predates retention, and is not an empty
// complete history.
func TestObservationHistoryStates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		present     bool
		retained    uint64
		complete    bool
		recordCount uint64

		wantState     ObservationHistoryState
		wantAvailable bool
		wantComplete  bool
		wantRetained  uint64
	}{
		{
			name: "a run that has ingested nothing is trivially complete",
			// No history row and no records: there is no history to be missing.
			wantState: ObservationHistoryComplete, wantAvailable: true, wantComplete: true,
		},
		{
			name:        "a run with records and no history row predates retention",
			recordCount: 4,
			wantState:   ObservationHistoryUnavailable,
		},
		{
			name: "a run retaining everything is complete", present: true,
			retained: 3, complete: true, recordCount: 3,
			wantState: ObservationHistoryComplete, wantAvailable: true, wantComplete: true,
			wantRetained: 3,
		},
		{
			name: "a run that stopped retaining is partial", present: true,
			retained: MaxRetainedObservations, complete: false,
			recordCount: MaxRetainedObservations + 10,
			wantState:   ObservationHistoryPartial, wantAvailable: true,
			wantRetained: MaxRetainedObservations,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewObservationHistory(tc.present, tc.retained, tc.complete, tc.recordCount)
			if got.State() != tc.wantState {
				t.Errorf("State() = %v, want %v", got.State(), tc.wantState)
			}
			if got.Available() != tc.wantAvailable {
				t.Errorf("Available() = %v, want %v", got.Available(), tc.wantAvailable)
			}
			if got.Complete() != tc.wantComplete {
				t.Errorf("Complete() = %v, want %v", got.Complete(), tc.wantComplete)
			}
			if got.RetainedCount() != tc.wantRetained {
				t.Errorf("RetainedCount() = %d, want %d", got.RetainedCount(), tc.wantRetained)
			}
		})
	}
}

// The zero value is unavailable, which is the state that cannot overclaim.
func TestZeroObservationHistoryIsUnavailable(t *testing.T) {
	var h ObservationHistory
	if h.State() != ObservationHistoryUnavailable {
		t.Errorf("zero State() = %v, want unavailable", h.State())
	}
	if h.Available() || h.Complete() {
		t.Errorf("zero history reports Available()=%v Complete()=%v, want false and false",
			h.Available(), h.Complete())
	}
}

// Every state renders a distinct, stable name: it is published on the wire.
func TestObservationHistoryStateNames(t *testing.T) {
	names := map[ObservationHistoryState]string{
		ObservationHistoryUnavailable: "unavailable",
		ObservationHistoryComplete:    "complete",
		ObservationHistoryPartial:     "partial",
	}
	seen := map[string]bool{}
	for state, want := range names {
		got := state.String()
		if got != want {
			t.Errorf("State(%d).String() = %q, want %q", state, got, want)
		}
		if seen[got] {
			t.Errorf("two states render as %q", got)
		}
		seen[got] = true
	}
	if got := ObservationHistoryState(99).String(); got != "unknown" {
		t.Errorf("an unrecognized state renders as %q, want unknown", got)
	}
}

// The cursor round-trips, and refuses anything this contract would not produce.
func TestObservationCursorRoundTripAndRefusals(t *testing.T) {
	for _, sequence := range []uint64{1, 9, 10, 4096, 1 << 62} {
		text := FormatObservationCursor(sequence)
		got, err := ParseObservationCursor(text)
		if err != nil {
			t.Fatalf("ParseObservationCursor(%q) error = %v", text, err)
		}
		if got != sequence {
			t.Errorf("round trip of %d produced %d", sequence, got)
		}
	}

	// Empty starts at the beginning and is not an error.
	if got, err := ParseObservationCursor(""); err != nil || got != 0 {
		t.Errorf("ParseObservationCursor(\"\") = %d, %v; want 0 and no error", got, err)
	}

	for _, bad := range []string{"0", "01", "+1", "-1", "1.0", " 1", "1 ", "abc", "1e3"} {
		if _, err := ParseObservationCursor(bad); !errors.Is(err, ErrObservationCursor) {
			t.Errorf("ParseObservationCursor(%q) error = %v, want ErrObservationCursor", bad, err)
		}
	}
}

// The storage key is fixed width and orders numerically under byte comparison,
// which is the whole reason it is padded.
func TestObservationSequenceKeyOrdersNumerically(t *testing.T) {
	// 9 before 10 is the case plain decimal text gets wrong.
	for _, pair := range [][2]uint64{{1, 2}, {9, 10}, {99, 100}, {4095, 4096}} {
		low, high := observationSequenceKey(pair[0]), observationSequenceKey(pair[1])
		if !(low < high) {
			t.Errorf("key(%d)=%q is not ordered before key(%d)=%q",
				pair[0], low, pair[1], high)
		}
		if len(low) != observationSequenceDigits || len(high) != observationSequenceDigits {
			t.Errorf("keys are not fixed width: %q, %q", low, high)
		}
	}

	if _, err := parseObservationSequenceKey("seq", "1"); err == nil {
		t.Error("an unpadded sequence was accepted as a storage key")
	}
	if _, err := parseObservationSequenceKey("seq", observationSequenceKey(0)); err == nil {
		t.Error("a zero sequence was accepted as a storage key")
	}
}

// ObservationFromRecord refuses only what the ingest path already refuses.
//
// This is the property that keeps task 067 from changing which records are
// ingestable: a record the aggregate and the collector accept must be
// retainable, so anything this rejects must already have been rejected.
func TestObservationFromRecordRefusesOnlyWhatIngestAlreadyDoes(t *testing.T) {
	base := trustvian.DecisionRecord{
		EventID:       "evt-1",
		Timestamp:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		FingerprintID: "fp-1",
		Environment:   "staging",
	}

	if _, err := ObservationFromRecord(1, base, true); err != nil {
		t.Fatalf("a minimal valid record was refused: %v", err)
	}

	// A padded identifier and a long one are accepted here because the ingest
	// path accepts them: tightening would be a change to what may be ingested.
	padded := base
	padded.EventID = "  evt-1  "
	if _, err := ObservationFromRecord(1, padded, true); err != nil {
		t.Errorf("a padded event id was refused, which ingest accepts: %v", err)
	}
	long := base
	long.FingerprintID = strings.Repeat("f", 300)
	if _, err := ObservationFromRecord(1, long, true); err != nil {
		t.Errorf("a long fingerprint id was refused, which ingest accepts: %v", err)
	}
	// So is an unrecognized lineage — nothing validates it at ingest, and
	// rewriting it would discard the lineage this task exists to preserve.
	lineage := base
	lineage.SpanLineage = event.SpanLineage("sideways")
	got, err := ObservationFromRecord(1, lineage, true)
	if err != nil {
		t.Errorf("an unrecognized lineage was refused, which ingest accepts: %v", err)
	} else if got.SpanLineage != event.SpanLineage("sideways") {
		t.Errorf("lineage was rewritten to %q", got.SpanLineage)
	}

	for _, tc := range []struct {
		name     string
		sequence uint64
		mutate   func(*trustvian.DecisionRecord)
	}{
		{"sequence zero", 0, func(*trustvian.DecisionRecord) {}},
		{"empty event id", 1, func(r *trustvian.DecisionRecord) { r.EventID = "" }},
		{"no timestamp", 1, func(r *trustvian.DecisionRecord) { r.Timestamp = time.Time{} }},
		{"malformed duration", 1, func(r *trustvian.DecisionRecord) { r.DurationNanos = "1.5" }},
		{"non-canonical duration", 1, func(r *trustvian.DecisionRecord) { r.DurationNanos = "007" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			tc.mutate(&record)
			if _, err := ObservationFromRecord(
				tc.sequence, record, true); !errors.Is(err, ErrInvalidObservation) {
				t.Fatalf("error = %v, want ErrInvalidObservation", err)
			}
		})
	}
}

// A measured zero and an unavailable duration are projected as different facts.
func TestObservationFromRecordSeparatesZeroFromUnavailable(t *testing.T) {
	base := trustvian.DecisionRecord{
		EventID:   "evt-1",
		Timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	unavailable := base
	unavailable.DurationNanos = ""
	measured := base
	measured.DurationNanos = "0"

	a, err := ObservationFromRecord(1, unavailable, false)
	if err != nil {
		t.Fatalf("unavailable: %v", err)
	}
	b, err := ObservationFromRecord(2, measured, false)
	if err != nil {
		t.Fatalf("measured zero: %v", err)
	}

	if a.DurationObserved {
		t.Error("an absent duration was projected as observed")
	}
	if !b.DurationObserved {
		t.Error("a measured zero was projected as unavailable")
	}
	if a.DurationNanos != 0 || b.DurationNanos != 0 {
		t.Errorf("durations = %d and %d, want 0 and 0", a.DurationNanos, b.DurationNanos)
	}
}

// A commit must carry the observation for the record it commits.
func TestCommitObservationMustMatchTheCommitSequence(t *testing.T) {
	if err := validateCommitObservation(EvaluationIngestCommit{
		Sequence:    3,
		Observation: Observation{Sequence: 3},
	}); err != nil {
		t.Errorf("a matching observation was refused: %v", err)
	}

	// The zero value is the case that matters: it would write every record at
	// sequence 0 and collide with itself on the second one.
	if err := validateCommitObservation(EvaluationIngestCommit{
		Sequence: 3,
	}); !errors.Is(err, ErrInvalidObservation) {
		t.Errorf("error = %v, want ErrInvalidObservation", err)
	}
	if err := validateCommitObservation(EvaluationIngestCommit{
		Sequence:    3,
		Observation: Observation{Sequence: 4},
	}); !errors.Is(err, ErrInvalidObservation) {
		t.Errorf("error = %v, want ErrInvalidObservation", err)
	}
}
