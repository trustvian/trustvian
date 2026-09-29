package platform

// Persistence of the operational aggregates (task 084), on SQLite.
//
// The PostgreSQL half is covered by the shared conformance and differential
// suites, which skip without a database. This file is in-package so it can
// reach the store's own connection and read the columns directly — the same
// arrangement the other schema tests use.

import (
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

func operationalTestRecord(
	run EvaluationRun, id, duration string, status event.SpanStatus,
) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     id,
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ActorID:     "actor-1",
		ActorType:   event.ActorTypeService,
		Environment: string(run.Environment()),
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeService,
			OperationCategory: event.OperationCategoryHTTP,
			OperationName:     "GET",
			Environment:       string(run.Environment()),
		},
		FingerprintID:      "fp-1",
		IdentityConfidence: 1,
		RiskLevel:          "low",
		Decision:           "allow",
		PolicyReason:       "default",
		MatchedDefault:     true,
		DurationNanos:      duration,
		SpanStatus:         status,
	}
}

// TestOperationalAggregatesSurviveARestart is the round trip: every counter
// comes back exactly, including the distinction between a measured zero and an
// unobserved duration.
func TestOperationalAggregatesSurviveARestart(t *testing.T) {
	store, path := testStore(t)
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for _, spec := range []struct {
		id, duration string
		status       event.SpanStatus
	}{
		{"e1", "1500", event.StatusOK},
		{"e2", "", event.StatusUnset},
		{"e3", "0", event.StatusError},
		{"e4", "2500", event.StatusUnavailable},
		{"e5", "", event.StatusError},
	} {
		record := operationalTestRecord(run, spec.id, spec.duration, spec.status)
		aggregate, err = aggregate.AddRecord(record)
		if err != nil {
			t.Fatalf("AddRecord(%s) error = %v", spec.id, err)
		}
		if err := collector.Observe(record); err != nil {
			t.Fatalf("Observe(%s) error = %v", spec.id, err)
		}
	}

	if err := store.SaveEvaluationEvidence(t.Context(), aggregate, collector.Snapshot()); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()

	restored, _, err := reopened.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}

	wantD, gotD := aggregate.Durations(), restored.Durations()
	if wantD != gotD {
		t.Errorf("durations after restart = %+v, want %+v", gotD, wantD)
	}
	wantS, gotS := aggregate.SpanStatuses(), restored.SpanStatuses()
	if wantS != gotS {
		t.Errorf("statuses after restart = %+v, want %+v", gotS, wantS)
	}

	// Specifically: the measured zero is still a measurement, not an absence.
	if gotD.Count != 3 || gotD.Unobserved != 2 || gotD.Min != 0 {
		t.Errorf("durations = %+v, want Count 3 Unobserved 2 Min 0", gotD)
	}
	if gotD.Count+gotD.Unobserved != restored.RecordCount() {
		t.Error("the restored duration buckets do not sum to the record count")
	}
	if gotS.Total() != restored.RecordCount() {
		t.Error("the restored status buckets do not sum to the record count")
	}
}

// TestOperationalColumnsArePersistedAsText checks the storage form directly,
// because a nanosecond sum in a signed 64-bit column would corrupt silently.
//
// The sum is driven past the signed range by two **individually valid**
// observations, each exactly event.MaxDurationNanos. That is the distinction the
// schema depends on: no single observation may exceed the signed range, and a
// run's total legitimately does.
func TestOperationalColumnsArePersistedAsText(t *testing.T) {
	store, _ := testStore(t)
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}

	maxOne := strconv.FormatUint(event.MaxDurationNanos, 10)
	for _, id := range []string{"e1", "e2"} {
		record := operationalTestRecord(run, id, maxOne, event.StatusOK)
		if aggregate, err = aggregate.AddRecord(record); err != nil {
			t.Fatalf("AddRecord(%s) error = %v", id, err)
		}
		if err := collector.Observe(record); err != nil {
			t.Fatalf("Observe(%s) error = %v", id, err)
		}
	}

	// Two maxima: beyond the signed range, inside the unsigned one.
	wantSum := strconv.FormatUint(uint64(math.MaxUint64)-1, 10)
	if got := strconv.FormatUint(aggregate.Durations().Sum, 10); got != wantSum {
		t.Fatalf("Sum = %s, want %s", got, wantSum)
	}

	if err := store.SaveEvaluationEvidence(
		t.Context(), aggregate, collector.Snapshot()); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	var sum string
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT duration_sum FROM `+tableAggregates+` WHERE run_id = ?`,
		string(run.ID())).Scan(&sum); err != nil {
		t.Fatalf("read duration_sum: %v", err)
	}
	if sum != wantSum {
		t.Errorf("duration_sum = %q, want %q; the column must hold the full uint64 range",
			sum, wantSum)
	}

	restored, _, err := store.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if got := strconv.FormatUint(restored.Durations().Sum, 10); got != wantSum {
		t.Errorf("restored sum = %s, want %s", got, wantSum)
	}
}

// TestFreshAndMigratedSchemasHoldTheSameColumns is the backend-equivalence
// property this task can assert without a database: a v4 database migrated
// forward and a database created fresh must be column-for-column identical.
func TestFreshAndMigratedSchemasHoldTheSameColumns(t *testing.T) {
	fresh, _ := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableAggregates)

	path := t.TempDir() + "/v4.db"
	db := writeSchemaV4(t, path)
	db.Close()
	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v4 database error = %v", err)
	}
	defer migrated.Close()
	migratedColumns := sqliteTableColumns(t, migrated.db, tableAggregates)

	if len(freshColumns) != len(migratedColumns) {
		t.Fatalf("fresh has %d columns, migrated has %d", len(freshColumns), len(migratedColumns))
	}
	for i := range freshColumns {
		if freshColumns[i] != migratedColumns[i] {
			t.Errorf("column %d: fresh %q, migrated %q", i, freshColumns[i], migratedColumns[i])
		}
	}
}

// ---------------------------------------------------------------------
// Corruption of the operational columns
// ---------------------------------------------------------------------

// seedOperationalEvidence writes one run's valid evidence and returns its run.
func seedOperationalEvidence(t *testing.T, store *SQLiteStore) EvaluationRun {
	t.Helper()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for _, spec := range []struct {
		id, duration string
		status       event.SpanStatus
	}{
		{"e1", "1000", event.StatusOK},
		{"e2", "", event.StatusUnset},
		{"e3", "3000", event.StatusError},
	} {
		record := operationalTestRecord(run, spec.id, spec.duration, spec.status)
		if aggregate, err = aggregate.AddRecord(record); err != nil {
			t.Fatalf("AddRecord(%s) error = %v", spec.id, err)
		}
		if err := collector.Observe(record); err != nil {
			t.Fatalf("Observe(%s) error = %v", spec.id, err)
		}
	}
	if err := store.SaveEvaluationEvidence(
		t.Context(), aggregate, collector.Snapshot()); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	return run
}

// TestCorruptOperationalEvidenceIsRefused writes valid evidence, edits one
// column, and requires the read to refuse rather than to trust or repair it.
func TestCorruptOperationalEvidenceIsRefused(t *testing.T) {
	tests := []struct {
		name   string
		column string
		value  string
	}{
		// Bucket totals that no longer partition the run.
		{"too few observed durations", "duration_count", "0"},
		{"too many observed durations", "duration_count", "9"},
		{"too few unobserved durations", "duration_unobserved", "0"},
		{"status buckets do not add up", "span_status_ok", "7"},
		{"a status bucket is emptied", "span_status_error", "0"},

		// Bucket arithmetic that would wrap.
		{"duration buckets overflow", "duration_unobserved", "18446744073709551615"},
		{"status buckets overflow", "span_status_unset", "18446744073709551615"},

		// Statistics that contradict the counts.
		{"min exceeds max", "duration_min", "9000"},
		{"sum below its own maximum", "duration_sum", "1"},
		{"sum below the minimum across the count", "duration_sum", "1500"},
		{"sum above the maximum across the count", "duration_sum", "999999"},

		// An extremum beyond what any single observation may report.
		{"max beyond the per-observation bound", "duration_max", "18446744073709551615"},

		// Malformed text in a counter column.
		{"a non-canonical counter", "duration_sum", "04000"},
		{"a negative counter", "duration_count", "-1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedOperationalEvidence(t, store)

			exec(t, store.db,
				`UPDATE `+tableAggregates+` SET `+tt.column+` = ? WHERE run_id = ?`,
				tt.value, string(run.ID()))

			_, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if err == nil {
				t.Fatalf("corrupt %s = %q was accepted", tt.column, tt.value)
			}
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestNonZeroStatisticsWithNoObservedDurationsAreRefused is its own case
// because it is the shape a naive migration would produce.
func TestNonZeroStatisticsWithNoObservedDurationsAreRefused(t *testing.T) {
	for _, column := range []string{"duration_sum", "duration_min", "duration_max"} {
		t.Run(column, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedOperationalEvidence(t, store)

			// Every record unobserved, but one statistic left behind.
			exec(t, store.db, `UPDATE `+tableAggregates+`
				SET duration_count = '0', duration_unobserved = record_count,
				    duration_sum = '0', duration_min = '0', duration_max = '0'
				WHERE run_id = ?`, string(run.ID()))
			exec(t, store.db,
				`UPDATE `+tableAggregates+` SET `+column+` = '5' WHERE run_id = ?`,
				string(run.ID()))

			_, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if err == nil {
				t.Fatalf("no observed durations but %s = 5 was accepted", column)
			}
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestValidOperationalEvidenceStillLoads is the other half: the shapes that are
// legitimate must keep loading, including a migrated legacy run.
func TestValidOperationalEvidenceStillLoads(t *testing.T) {
	tests := []struct {
		name   string
		update string
	}{
		{"all unknown, as a migrated legacy run looks", `
			duration_count = '0', duration_unobserved = record_count,
			duration_sum = '0', duration_min = '0', duration_max = '0',
			span_status_unavailable = record_count, span_status_unset = '0',
			span_status_ok = '0', span_status_error = '0'`},
		{"every duration a measured zero", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '0', duration_min = '0', duration_max = '0'`},
		{"every duration identical", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '9', duration_min = '3', duration_max = '3'`},
		{"a sum exactly at the lower bound", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '6', duration_min = '2', duration_max = '4'`},
		{"a sum exactly at the upper bound", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '12', duration_min = '2', duration_max = '4'`},
		{"extrema at the per-observation maximum", `
			duration_count = '2', duration_unobserved = '1',
			duration_sum = '18446744073709551614',
			duration_min = '9223372036854775807', duration_max = '9223372036854775807'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedOperationalEvidence(t, store)

			exec(t, store.db,
				`UPDATE `+tableAggregates+` SET `+tt.update+` WHERE run_id = ?`,
				string(run.ID()))

			restored, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if err != nil {
				t.Fatalf("valid evidence was refused: %v", err)
			}
			d := restored.Durations()
			if d.Count+d.Unobserved != restored.RecordCount() {
				t.Errorf("restored buckets do not partition the run: %+v", d)
			}
		})
	}
}

// TestLegitimateTotalIsNotRejectedByIntermediateOverflow is the requirement that
// the upper-bound check must not reject a real total just because Max*Count
// would wrap.
func TestLegitimateTotalIsNotRejectedByIntermediateOverflow(t *testing.T) {
	store, _ := testStore(t)
	run := seedOperationalEvidence(t, store)

	// Three observations, the largest at the per-observation maximum. Max*Count
	// overflows uint64; the sum itself is perfectly ordinary.
	exec(t, store.db, `UPDATE `+tableAggregates+` SET
		duration_count = '3', duration_unobserved = ?,
		duration_sum = '9223372036854775809',
		duration_min = '1', duration_max = '9223372036854775807'
		WHERE run_id = ?`,
		uint64Text(0), string(run.ID()))
	exec(t, store.db,
		`UPDATE `+tableAggregates+` SET record_count = '3' WHERE run_id = ?`, string(run.ID()))

	if _, _, err := store.EvaluationEvidence(t.Context(), run.ID()); err != nil {
		t.Fatalf("a legitimate total was refused because an intermediate product "+
			"would overflow: %v", err)
	}
}
