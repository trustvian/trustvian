package platform

// Persistence of the operational aggregates (task 084), on SQLite.
//
// The PostgreSQL half is covered by the shared conformance and differential
// suites, which skip without a database. This file is in-package so it can
// reach the store's own connection and read the columns directly — the same
// arrangement the other schema tests use.

import (
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
func TestOperationalColumnsArePersistedAsText(t *testing.T) {
	store, _ := testStore(t)
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	// A sum a signed 64-bit column could not hold.
	huge := strconv.FormatUint(uint64(1)<<63+5, 10)
	record := operationalTestRecord(run, "e1", huge, event.StatusOK)
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
	if sum != huge {
		t.Errorf("duration_sum = %q, want %q; the column must hold the full uint64 range",
			sum, huge)
	}

	restored, _, err := store.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if got := restored.Durations().Sum; strconv.FormatUint(got, 10) != huge {
		t.Errorf("restored sum = %d, want %s", got, huge)
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
