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
		// Count 3 with Min 2 and Max 4: both extrema are observed, so the
		// multiset is [2, x, 4] with x in [2,4] and the total lies in [8,10].
		// An earlier version of this table used 6 and 12, which no run could
		// produce — see TestDurationExtremaBoundsArePersistedAndEnforced.
		{"a sum exactly at the lower bound", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '8', duration_min = '2', duration_max = '4'`},
		{"a sum between the bounds", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '9', duration_min = '2', duration_max = '4'`},
		{"a sum exactly at the upper bound", `
			duration_count = record_count, duration_unobserved = '0',
			duration_sum = '10', duration_min = '2', duration_max = '4'`},

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

// TestLegitimateTotalIsNotRejectedByIntermediateOverflow keeps a large but
// reachable total loadable.
//
// Three observations with Min 1 and Max the per-observation maximum: the
// smallest reachable total is Max + 2*Min, which this sits exactly on. The
// upper-bound overflow case it used to cover moved to
// TestExtremaBoundOverflowCases, which exercises it against the corrected
// bounds.
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

// seedDurations writes one run whose observed durations are exactly the given
// values, through the real AddRecord path so every counter is consistent by
// construction.
//
// The corruption tests below then edit only the duration columns, which is what
// makes them tests of the validator rather than of the fixture.
func seedDurations(t *testing.T, store *SQLiteStore, durations ...string) EvaluationRun {
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
	for i, duration := range durations {
		record := operationalTestRecord(run, "e"+strconv.Itoa(i), duration, event.StatusUnset)
		if aggregate, err = aggregate.AddRecord(record); err != nil {
			t.Fatalf("AddRecord(%d) error = %v", i, err)
		}
		if err := collector.Observe(record); err != nil {
			t.Fatalf("Observe(%d) error = %v", i, err)
		}
	}
	if err := store.SaveEvaluationEvidence(
		t.Context(), aggregate, collector.Snapshot()); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	return run
}

// TestDurationExtremaBoundsArePersistedAndEnforced is the corrected invariant,
// exercised through the persisted read path.
//
// Min and Max are *observed* extrema, so each appears at least once and the
// remaining Count-2 observations lie between them:
//
//	Max + (Count-1)*Min  <=  Sum  <=  Min + (Count-1)*Max
//
// An earlier version used Min*Count <= Sum <= Max*Count, which admits totals no
// run could produce — Count 3 with Min 2 and Max 4 accepted Sum 6, while the
// smallest such multiset is [2,2,4] and sums to 8.
func TestDurationExtremaBoundsArePersistedAndEnforced(t *testing.T) {
	tests := []struct {
		name     string
		seed     []string
		sum      string
		accepted bool
	}{
		// Three observations, Min 2 and Max 4: only 8, 9 and 10 are reachable.
		{"below the lower bound", []string{"2", "3", "4"}, "6", false},
		{"one below the lower bound", []string{"2", "3", "4"}, "7", false},
		{"exactly the lower bound", []string{"2", "3", "4"}, "8", true},
		{"between the bounds", []string{"2", "3", "4"}, "9", true},
		{"exactly the upper bound", []string{"2", "3", "4"}, "10", true},
		{"one above the upper bound", []string{"2", "3", "4"}, "11", false},
		{"above the upper bound", []string{"2", "3", "4"}, "12", false},

		// The existing fixture's shape: two observed durations, 1000 and 3000.
		// Both extrema are observed and there is no third value, so 4000 is the
		// only possible total.
		{"the only possible total for two observations",
			[]string{"1000", "", "3000"}, "4000", true},
		{"below it", []string{"1000", "", "3000"}, "3000", false},
		{"above it", []string{"1000", "", "3000"}, "5000", false},

		// Identical durations leave no freedom at all.
		{"identical durations", []string{"3", "3", "3"}, "9", true},
		{"identical durations, wrong total", []string{"3", "3", "3"}, "8", false},

		// Zero is a measurement like any other.
		{"every duration zero", []string{"0", "0", "0"}, "0", true},
		{"zeroes cannot sum to anything", []string{"0", "0", "0"}, "1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedDurations(t, store, tt.seed...)

			exec(t, store.db,
				`UPDATE `+tableAggregates+` SET duration_sum = ? WHERE run_id = ?`,
				tt.sum, string(run.ID()))

			_, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if tt.accepted {
				if err != nil {
					t.Fatalf("a reachable total was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("an unreachable total %s was accepted", tt.sum)
			}
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestSingleObservationRequiresAgreement is the Count == 1 case: the extrema and
// the total are all the same measurement.
func TestSingleObservationRequiresAgreement(t *testing.T) {
	tests := []struct {
		name     string
		update   string
		accepted bool
	}{
		{"all three agree", `duration_min = '7', duration_max = '7', duration_sum = '7'`, true},
		{"a zero observation", `duration_min = '0', duration_max = '0', duration_sum = '0'`, true},
		{"the sum disagrees", `duration_min = '7', duration_max = '7', duration_sum = '8'`, false},
		{"the max disagrees", `duration_min = '7', duration_max = '9', duration_sum = '7'`, false},
		{"the min disagrees", `duration_min = '5', duration_max = '7', duration_sum = '7'`, false},
		{"all three differ", `duration_min = '5', duration_max = '9', duration_sum = '7'`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedDurations(t, store, "7")

			exec(t, store.db,
				`UPDATE `+tableAggregates+` SET `+tt.update+` WHERE run_id = ?`,
				string(run.ID()))

			_, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if tt.accepted {
				if err != nil {
					t.Fatalf("a consistent single observation was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("a single observation whose extrema and total disagree was accepted")
			}
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("error = %v, want ErrStoreCorrupt", err)
			}
		})
	}
}

// TestExtremaBoundOverflowCases covers the two directions separately, because
// they mean opposite things.
func TestExtremaBoundOverflowCases(t *testing.T) {
	maxOne := strconv.FormatUint(event.MaxDurationNanos, 10)

	t.Run("a lower bound that overflows is impossible", func(t *testing.T) {
		store, _ := testStore(t)
		run := seedDurations(t, store, "1", "2", "3")

		// Three observations at the per-observation maximum: the smallest total
		// is Max + 2*Min, which cannot be represented, so no stored Sum is valid.
		exec(t, store.db, `UPDATE `+tableAggregates+` SET
			duration_count = '3', duration_unobserved = '0',
			duration_min = ?, duration_max = ?, duration_sum = ?
			WHERE run_id = ?`,
			maxOne, maxOne, strconv.FormatUint(math.MaxUint64, 10), string(run.ID()))

		_, _, err := store.EvaluationEvidence(t.Context(), run.ID())
		if err == nil {
			t.Fatal("a summary whose smallest possible total is unrepresentable was accepted")
		}
		if !errors.Is(err, ErrStoreCorrupt) {
			t.Errorf("error = %v, want ErrStoreCorrupt", err)
		}
	})

	t.Run("an upper bound that overflows constrains nothing", func(t *testing.T) {
		store, _ := testStore(t)
		run := seedDurations(t, store, "1", "2", "3", "4")

		// Four observations, Min 1 and Max the per-observation maximum. The
		// upper bound Min + 3*Max exceeds uint64, so it cannot constrain a
		// stored Sum — and a legitimate total must not be refused because that
		// intermediate calculation would have wrapped.
		exec(t, store.db, `UPDATE `+tableAggregates+` SET
			duration_count = '4', duration_unobserved = '0',
			duration_min = '1', duration_max = ?, duration_sum = ?
			WHERE run_id = ?`,
			maxOne, strconv.FormatUint(math.MaxUint64, 10), string(run.ID()))

		if _, _, err := store.EvaluationEvidence(t.Context(), run.ID()); err != nil {
			t.Fatalf("a legitimate total was refused because its upper bound would "+
				"overflow: %v", err)
		}
	})

	t.Run("a sum above MaxInt64 stays valid", func(t *testing.T) {
		store, _ := testStore(t)
		run := seedDurations(t, store, "1", "2")

		// Two observations at the per-observation maximum sum to MaxUint64-1.
		// The individual-duration limit must not become an aggregate-sum limit.
		exec(t, store.db, `UPDATE `+tableAggregates+` SET
			duration_count = '2', duration_unobserved = '0',
			duration_min = ?, duration_max = ?, duration_sum = ?
			WHERE run_id = ?`,
			maxOne, maxOne, strconv.FormatUint(uint64(math.MaxUint64)-1, 10), string(run.ID()))

		restored, _, err := store.EvaluationEvidence(t.Context(), run.ID())
		if err != nil {
			t.Fatalf("an aggregate sum above MaxInt64 was refused: %v", err)
		}
		if restored.Durations().Sum != math.MaxUint64-1 {
			t.Errorf("restored sum = %d, want MaxUint64-1", restored.Durations().Sum)
		}
	})
}
