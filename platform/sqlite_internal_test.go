package platform

// Storage-level failures driven from inside the package.
//
// Three classes of thing live here because no ordinary caller can reach them.
//
// Schema states: an unknown version, and recognized tables with no version
// metadata. The second is the dangerous one — adopting it as fresh would
// silently take ownership of data this code has never seen.
//
// Full-width counters: SQLite INTEGER is signed 64-bit and platform counters
// are uint64, so anything above MaxInt64 is exactly where a narrowing bug
// hides. A caller cannot easily produce such an aggregate; direct SQL can.
//
// Corruption: rows that violate invariants a live value could never violate.
// Each must fail closed rather than produce a partially trusted value, because
// the bound marker is what every downstream task relies on.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// internalRecord is a complete, valid record for one behavioral shape.
func internalRecord(eventID, fingerprintID, operation string) trustvian.DecisionRecord {
	return trustvian.DecisionRecord{
		EventID:     eventID,
		Timestamp:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		ActorID:     "agent-1",
		ActorType:   event.ActorTypeAIAgent,
		Environment: "staging",
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     operation,
			TargetName:        "build-host",
			TargetCategory:    event.TargetCategoryExternal,
			Environment:       "staging",
		},
		FingerprintID:  fingerprintID,
		RiskLevel:      "low",
		Decision:       "observe_only",
		MatchedDefault: true,
	}
}

func testStore(t *testing.T) (*SQLiteStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store, path
}

// seedRun creates the hierarchy and returns a pending run.
func seedRun(t *testing.T, store *SQLiteStore) EvaluationRun {
	t.Helper()
	ctx := t.Context()
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	project, err := NewProject("proj-1", "Checkout")
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	agent, err := NewAgent("agent-1", "proj-1", "Deploy agent")
	if err != nil {
		t.Fatalf("NewAgent() error = %v", err)
	}
	candidate, err := NewCandidate("cand-1", "agent-1", CandidateMetadata{})
	if err != nil {
		t.Fatalf("NewCandidate() error = %v", err)
	}
	run, err := NewEvaluationRun("run-1", "cand-1", "staging", "profile-1", epoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	for _, err := range []error{
		store.CreateProject(ctx, project),
		store.CreateAgent(ctx, agent),
		store.CreateCandidate(ctx, candidate),
		store.CreateEvaluationRun(ctx, run),
	} {
		if err != nil {
			t.Fatalf("seed error = %v", err)
		}
	}
	return run
}

func exec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("exec %q error = %v", query, err)
	}
}

// ---------------------------------------------------------------------
// Schema states
// ---------------------------------------------------------------------

func TestUnknownSchemaVersionFailsClosed(t *testing.T) {
	store, path := testStore(t)
	seedRun(t, store)
	exec(t, store.db, `UPDATE `+tableSchemaVersion+` SET version = ?`, SchemaVersion+1)
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if !errors.Is(err, ErrStoreSchemaVersion) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrStoreSchemaVersion", err)
	}

	// The refusal must not have touched application data.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer raw.Close()
	var projects int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM ` + tableProjects).Scan(&projects); err != nil {
		t.Fatalf("count projects error = %v", err)
	}
	if projects != 1 {
		t.Errorf("projects = %d, want 1: a failed open modified application tables", projects)
	}
}

// Recognized tables with no version metadata is the ambiguous case. Adopting
// it as fresh would silently take ownership of somebody else's database.
func TestAmbiguousSchemaIsNotAdoptedAsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ambiguous.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	exec(t, raw, `CREATE TABLE `+tableProjects+` (id TEXT PRIMARY KEY, name TEXT NOT NULL)`)
	exec(t, raw, `INSERT INTO `+tableProjects+` (id, name) VALUES ('someone-elses', 'data')`)
	raw.Close()

	store, err := OpenSQLiteStore(t.Context(), path)
	if !errors.Is(err, ErrStoreSchemaVersion) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrStoreSchemaVersion", err)
	}
}

func TestVersionTableWithoutVersionRowFailsClosed(t *testing.T) {
	store, path := testStore(t)
	exec(t, store.db, `DELETE FROM `+tableSchemaVersion)
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if !errors.Is(err, ErrStoreSchemaVersion) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("OpenSQLiteStore() error = %v, want ErrStoreSchemaVersion", err)
	}
}

// The schema owns exactly these tables. An events, scorecard, gate-result or
// baseline table appearing here would be a scope change, and this is what
// makes that structural rather than a matter of review attention.
func TestSchemaContainsNoAccidentalHistory(t *testing.T) {
	store, _ := testStore(t)

	rows, err := store.db.QueryContext(t.Context(),
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query schema error = %v", err)
	}
	defer rows.Close()

	allowed := map[string]bool{}
	for _, name := range schemaTables {
		allowed[name] = true
	}

	var found int
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan error = %v", err)
		}
		found++
		if !allowed[name] {
			t.Errorf("unexpected table %q: this schema stores no event history, "+
				"derived values, gate policy, or core baseline state", name)
		}
	}
	if found != len(schemaTables) {
		t.Errorf("found %d tables, want %d", found, len(schemaTables))
	}

	for _, forbidden := range []string{
		"events", "decision_records", "event_history", "behavior_diffs",
		"scorecards", "gate_results", "gate_policies", "baselines",
		"platform_events", "platform_scorecards", "platform_gate_results",
		"platform_gate_policies", "platform_baselines",
	} {
		if allowed[forbidden] {
			t.Errorf("schema owns %q", forbidden)
		}
	}
}

// ---------------------------------------------------------------------
// Full-width uint64
// ---------------------------------------------------------------------

func TestUint64TextRoundTripsTheWholeDomain(t *testing.T) {
	values := []uint64{
		0,
		1,
		math.MaxInt64,
		math.MaxInt64 + 1, // the first value int64 cannot hold
		math.MaxUint64,
	}
	for _, want := range values {
		t.Run(strconv.FormatUint(want, 10), func(t *testing.T) {
			got, err := parseUint64Text("test", uint64Text(want))
			if err != nil {
				t.Fatalf("parseUint64Text() error = %v", err)
			}
			if got != want {
				t.Errorf("round trip = %d, want %d", got, want)
			}
		})
	}
}

// The values above must survive SQLite itself, not just the helpers: this is
// what fails if the encoding ever becomes int64(value).
func TestFullWidthCountersSurviveTheDatabase(t *testing.T) {
	store, path := testStore(t)
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	// Incomplete and empty: this test is about the aggregate's counters, and a
	// complete snapshot would have to account for every observation with
	// entries, which is a different invariant.
	snapshot := BehaviorSnapshot{
		bound: true, runID: run.ID(), candidateID: run.CandidateID(),
		environment: run.Environment(), profile: run.BehavioralProfile(),
		complete: false,
	}
	if err := store.SaveEvaluationEvidence(t.Context(), aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	for _, count := range []uint64{math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		t.Run(strconv.FormatUint(count, 10), func(t *testing.T) {
			// A consistent aggregate at this count: one categorical bucket and
			// every metric carrying it, so only the encoding is under test.
			exec(t, store.db, `UPDATE `+tableAggregates+` SET
				record_count = ?, decision_allow = ?, risk_low = ?,
				approval_unspecified = ?, policy_matched_rule = ?,
				identity_confidence_count = ?, anomaly_score_count = ?,
				anomaly_confidence_count = ?, trust_score_count = ?, context_risk_count = ?,
				identity_confidence_sum = 0.5, identity_confidence_min = 0.5, identity_confidence_max = 0.5,
				anomaly_score_sum = 0.5, anomaly_score_min = 0.5, anomaly_score_max = 0.5,
				anomaly_confidence_sum = 0.5, anomaly_confidence_min = 0.5, anomaly_confidence_max = 0.5,
				trust_score_sum = 0.5, trust_score_min = 0.5, trust_score_max = 0.5,
				context_risk_sum = 0.5, context_risk_min = 0.5, context_risk_max = 0.5,
				first_observed_at = ?, last_observed_at = ?
				WHERE run_id = ?`,
				uint64Text(count), uint64Text(count), uint64Text(count),
				uint64Text(count), uint64Text(count),
				uint64Text(count), uint64Text(count), uint64Text(count),
				uint64Text(count), uint64Text(count),
				timeText(time.Unix(0, 0).UTC()), timeText(time.Unix(0, 0).UTC()),
				string(run.ID()))

			loaded, _, err := store.EvaluationEvidence(t.Context(), run.ID())
			if err != nil {
				t.Fatalf("EvaluationEvidence() error = %v", err)
			}
			if loaded.RecordCount() != count {
				t.Errorf("RecordCount() = %d, want %d: the counter was narrowed",
					loaded.RecordCount(), count)
			}
			if loaded.Decisions().Allow != count {
				t.Errorf("Decisions().Allow = %d, want %d", loaded.Decisions().Allow, count)
			}
			if loaded.TrustScore().Count != count {
				t.Errorf("TrustScore().Count = %d, want %d", loaded.TrustScore().Count, count)
			}
		})
	}
	_ = path
}

func TestNonCanonicalCountersAreRejected(t *testing.T) {
	for _, raw := range []string{"", " 7", "7 ", "007", "+7", "-1", "0x10", "7.0", "nine",
		"18446744073709551616"} { // MaxUint64 + 1
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			if _, err := parseUint64Text("test", raw); !errors.Is(err, ErrStoreCorrupt) {
				t.Errorf("parseUint64Text(%q) error = %v, want ErrStoreCorrupt", raw, err)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Evidence atomicity
// ---------------------------------------------------------------------

// Aggregate and snapshot are two views of one run. A failure partway through
// must leave the previously committed evidence entirely intact — never
// aggregate B beside snapshot A.
func TestEvidenceWriteIsOneTransaction(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	build := func(records int) (EvaluationAggregate, BehaviorSnapshot) {
		aggregate, err := NewEvaluationAggregate(run)
		if err != nil {
			t.Fatalf("NewEvaluationAggregate() error = %v", err)
		}
		collector, err := NewBehaviorCollector(run)
		if err != nil {
			t.Fatalf("NewBehaviorCollector() error = %v", err)
		}
		for i := range records {
			rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
				fmt.Sprintf("op-%d", i))
			if aggregate, err = aggregate.AddRecord(rec); err != nil {
				t.Fatalf("AddRecord() error = %v", err)
			}
			if err := collector.Observe(rec); err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
		}
		return aggregate, collector.Snapshot()
	}

	versionA, snapshotA := build(2)
	if err := store.SaveEvaluationEvidence(ctx, versionA, snapshotA); err != nil {
		t.Fatalf("SaveEvaluationEvidence(A) error = %v", err)
	}

	// Fail the entry write specifically, after the aggregate and header rows
	// in the same transaction have already been issued.
	exec(t, store.db, `CREATE TRIGGER fail_entries BEFORE INSERT ON `+tableEntries+`
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)

	versionB, snapshotB := build(5)
	err := store.SaveEvaluationEvidence(ctx, versionB, snapshotB)
	if err == nil {
		t.Fatal("SaveEvaluationEvidence(B) succeeded despite the injected failure")
	}

	exec(t, store.db, `DROP TRIGGER fail_entries`)

	loadedAggregate, loadedSnapshot, err := store.EvaluationEvidence(ctx, run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if loadedAggregate.RecordCount() != versionA.RecordCount() {
		t.Errorf("RecordCount() = %d, want %d: a partial write became durable",
			loadedAggregate.RecordCount(), versionA.RecordCount())
	}
	if loadedSnapshot.ObservationCount() != snapshotA.ObservationCount() {
		t.Errorf("ObservationCount() = %d, want %d",
			loadedSnapshot.ObservationCount(), snapshotA.ObservationCount())
	}
	if got := loadedSnapshot.DistinctBehaviorCount(); got != snapshotA.DistinctBehaviorCount() {
		t.Errorf("DistinctBehaviorCount() = %d, want %d", got, snapshotA.DistinctBehaviorCount())
	}
}

// ---------------------------------------------------------------------
// Corruption on read
// ---------------------------------------------------------------------

func TestCorruptedRowsFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, store *SQLiteStore, run EvaluationRun)
	}{
		{
			name: "unknown run status",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableRuns+` SET status = 'teleported' WHERE id = ?`, string(r.ID()))
			},
		},
		{
			name: "completed run finished before it started",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableRuns+`
					SET status = 'completed', started_at = ?, finished_at = ? WHERE id = ?`,
					timeText(time.Unix(2000, 0).UTC()), timeText(time.Unix(1000, 0).UTC()), string(r.ID()))
			},
		},
		{
			name: "pending run carrying a finish time",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableRuns+` SET finished_at = ? WHERE id = ?`,
					timeText(time.Unix(1000, 0).UTC()), string(r.ID()))
			},
		},
		{
			name: "completed run carrying a failure reason",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableRuns+`
					SET status = 'completed', started_at = ?, finished_at = ?, failure_reason = 'invented'
					WHERE id = ?`,
					timeText(time.Unix(1000, 0).UTC()), timeText(time.Unix(2000, 0).UTC()), string(r.ID()))
			},
		},
		{
			name: "unparsable timestamp",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableRuns+` SET created_at = 'yesterday' WHERE id = ?`, string(r.ID()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedRun(t, store)
			tt.corrupt(t, store, run)

			loaded, err := store.EvaluationRun(t.Context(), run.ID())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("EvaluationRun() error = %v, want ErrStoreCorrupt", err)
			}
			if loaded != (EvaluationRun{}) {
				t.Error("a corrupt row produced a non-zero run")
			}
		})
	}
}

func TestCorruptedEvidenceFailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, store *SQLiteStore, run EvaluationRun)
	}{
		{
			name: "decision counts do not total the record count",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableAggregates+` SET decision_allow = ? WHERE run_id = ?`,
					uint64Text(99), string(r.ID()))
			},
		},
		{
			name: "metric count does not match the record count",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableAggregates+` SET trust_score_count = ? WHERE run_id = ?`,
					uint64Text(99), string(r.ID()))
			},
		},
		{
			name: "metric extremes outside the unit interval",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableAggregates+`
					SET trust_score_max = 42.0 WHERE run_id = ?`, string(r.ID()))
			},
		},
		{
			name: "non-canonical counter text",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableAggregates+` SET record_count = '007' WHERE run_id = ?`,
					string(r.ID()))
			},
		},
		{
			name: "behavior entry with an invalid enum",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableEntries+` SET actor_type = 'wizard' WHERE run_id = ?`,
					string(r.ID()))
			},
		},
		{
			name: "behavior entry from a different environment",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableEntries+` SET environment = 'production' WHERE run_id = ?`,
					string(r.ID()))
			},
		},
		{
			name: "behavior entry with zero observations",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableEntries+` SET observations = ? WHERE run_id = ?`,
					uint64Text(0), string(r.ID()))
			},
		},
		{
			name: "entry observations do not account for the header count",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableSnapshots+` SET observation_count = ? WHERE run_id = ?`,
					uint64Text(999), string(r.ID()))
			},
		},
		{
			name: "header distinct count disagrees with stored entries",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableSnapshots+` SET distinct_count = 99 WHERE run_id = ?`,
					string(r.ID()))
			},
		},
		{
			name: "snapshot observed more than the aggregate",
			corrupt: func(t *testing.T, s *SQLiteStore, r EvaluationRun) {
				exec(t, s.db, `UPDATE `+tableAggregates+` SET record_count = ?,
					decision_observe_only = ?, risk_low = ?, approval_unspecified = ?,
					policy_matched_default = ?, identity_confidence_count = ?,
					anomaly_score_count = ?, anomaly_confidence_count = ?,
					trust_score_count = ?, context_risk_count = ? WHERE run_id = ?`,
					uint64Text(1), uint64Text(1), uint64Text(1), uint64Text(1), uint64Text(1),
					uint64Text(1), uint64Text(1), uint64Text(1), uint64Text(1), uint64Text(1),
					string(r.ID()))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			ctx := t.Context()
			run := seedRun(t, store)

			aggregate, err := NewEvaluationAggregate(run)
			if err != nil {
				t.Fatalf("NewEvaluationAggregate() error = %v", err)
			}
			collector, err := NewBehaviorCollector(run)
			if err != nil {
				t.Fatalf("NewBehaviorCollector() error = %v", err)
			}
			for i := range 3 {
				rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
					fmt.Sprintf("op-%d", i))
				if aggregate, err = aggregate.AddRecord(rec); err != nil {
					t.Fatalf("AddRecord() error = %v", err)
				}
				if err := collector.Observe(rec); err != nil {
					t.Fatalf("Observe() error = %v", err)
				}
			}
			if err := store.SaveEvaluationEvidence(ctx, aggregate, collector.Snapshot()); err != nil {
				t.Fatalf("SaveEvaluationEvidence() error = %v", err)
			}

			tt.corrupt(t, store, run)

			loadedAggregate, loadedSnapshot, err := store.EvaluationEvidence(ctx, run.ID())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
			}
			// No partially trusted value escapes: the bound marker is what
			// every downstream task relies on.
			if loadedAggregate.bound || loadedSnapshot.bound {
				t.Error("corrupt storage produced a bound value")
			}
		})
	}
}

// Two fingerprints describing one behavior would make a diff report it as
// both added and removed — the identity relation task 054 added, checked on
// the way out of storage too.
func TestDuplicateBehaviorDescriptorIsCorrupt(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range 2 {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
			fmt.Sprintf("op-%d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, collector.Snapshot()); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	// Give fp-1 the same behavior descriptor as fp-0.
	exec(t, store.db, `UPDATE `+tableEntries+`
		SET operation_name = (SELECT operation_name FROM `+tableEntries+`
		                      WHERE run_id = ? AND fingerprint_id = 'fp-0')
		WHERE run_id = ? AND fingerprint_id = 'fp-1'`,
		string(run.ID()), string(run.ID()))

	if _, _, err := store.EvaluationEvidence(ctx, run.ID()); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
	}
}

// SQLite cannot store NaN in a NOT NULL REAL column — the driver binds it as
// NULL and the constraint rejects the write, so the schema closes this path
// before the guard sees it. The guard still exists because a future backend
// with different numeric handling would not, and a NaN reaching a mean
// propagates silently through everything downstream.
func TestNonFiniteMetricsAreRejected(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		summary := MetricSummary{Count: 1, Sum: value, Min: 0.5, Max: 0.5}
		if err := validateRestoredMetric("trust score", summary, 1); err == nil {
			t.Errorf("validateRestoredMetric() accepted sum %v", value)
		}
	}
	if err := validateRestoredMetric("trust score",
		MetricSummary{Count: 1, Sum: 0.5, Min: 0.5, Max: 0.5}, 1); err != nil {
		t.Errorf("a finite summary was rejected: %v", err)
	}
}

// ---------------------------------------------------------------------
// Foreign keys
// ---------------------------------------------------------------------

// PRAGMA foreign_keys is per-connection, so this asserts the database itself
// enforces the relationship rather than only the explicit parent checks.
func TestForeignKeysAreEnforcedByTheDatabase(t *testing.T) {
	store, _ := testStore(t)
	seedRun(t, store)

	_, err := store.db.ExecContext(t.Context(),
		`INSERT INTO `+tableAgents+` (id, project_id, name) VALUES ('orphan', 'missing', 'Orphan')`)
	if err == nil {
		t.Fatal("inserted an agent under a missing project: foreign keys are not enforced")
	}
}

// ---------------------------------------------------------------------
// Sticky incompleteness
// ---------------------------------------------------------------------

func TestCompletenessIsStickyAcrossWrites(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	rec := internalRecord("evt-0", "fp-0", "op-0")
	if aggregate, err = aggregate.AddRecord(rec); err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}

	incomplete := BehaviorSnapshot{
		bound: true, runID: run.ID(), candidateID: run.CandidateID(),
		environment: run.Environment(), profile: run.BehavioralProfile(),
		observations: 1, complete: false,
		entries: []BehaviorEntry{{
			FingerprintID: "fp-0",
			Behavior:      rec.Behavior,
			Observations:  1,
		}},
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, incomplete); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	// A *later* write, at a higher observation count, claiming completeness.
	//
	// The count must differ. At an equal count the divergence check fires
	// first and this test would pass without the stickiness guard existing at
	// all — which is exactly what a mutation of that guard revealed.
	grown, err := aggregate.AddRecord(internalRecord("evt-1", "fp-0", "op-0"))
	if err != nil {
		t.Fatalf("AddRecord() error = %v", err)
	}
	healed := incomplete
	healed.complete = true
	healed.observations = 2
	healed.entries = []BehaviorEntry{{
		FingerprintID: "fp-0",
		Behavior:      rec.Behavior,
		Observations:  2,
	}}
	if grown.RecordCount() <= aggregate.RecordCount() {
		t.Fatalf("precondition: the second write must carry a higher count")
	}
	if err := store.SaveEvaluationEvidence(ctx, grown, healed); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("restoring completeness error = %v, want ErrStoreConflict", err)
	}

	_, loaded, err := store.EvaluationEvidence(ctx, run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if loaded.Complete() {
		t.Error("Complete() = true: saturation was erased")
	}
}

// ---------------------------------------------------------------------
// Unbound evidence
// ---------------------------------------------------------------------

func TestUnboundEvidenceIsRefused(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	snapshot := BehaviorSnapshot{
		bound: true, runID: run.ID(), candidateID: run.CandidateID(),
		environment: run.Environment(), profile: run.BehavioralProfile(), complete: true,
	}

	if err := store.SaveEvaluationEvidence(ctx, EvaluationAggregate{}, snapshot); !errors.Is(err, ErrUnboundAggregate) {
		t.Errorf("zero aggregate error = %v, want ErrUnboundAggregate", err)
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, BehaviorSnapshot{}); !errors.Is(err, ErrUnboundCollector) {
		t.Errorf("zero snapshot error = %v, want ErrUnboundCollector", err)
	}
}

// ---------------------------------------------------------------------
// Hardening: schema completeness, evidence binding, partial state
// ---------------------------------------------------------------------

// A version row is a claim, not proof. Metadata saying v1 beside a schema
// missing tables is a partial restore, and accepting it would move the
// failure from open to the first write, with the damage already invisible.
func TestMissingRequiredTableFailsSchemaVerification(t *testing.T) {
	for _, table := range []string{
		tableProjects, tableAgents, tableCandidates, tableRuns,
		tableAggregates, tableSnapshots, tableEntries,
	} {
		t.Run(table, func(t *testing.T) {
			store, path := testStore(t)
			seedRun(t, store)
			// Children first where a foreign key would otherwise block it.
			exec(t, store.db, `PRAGMA foreign_keys = OFF`)
			exec(t, store.db, `DROP TABLE `+table)
			store.Close()

			reopened, err := OpenSQLiteStore(t.Context(), path)
			if !errors.Is(err, ErrStoreSchemaVersion) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("OpenSQLiteStore() error = %v, want ErrStoreSchemaVersion", err)
			}

			// Nothing is rebuilt: there is no v1 repair migration, and
			// silently recreating one table would hide whatever else went
			// missing with it.
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("sql.Open() error = %v", err)
			}
			defer raw.Close()
			var found int
			if err := raw.QueryRow(
				`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`,
				table).Scan(&found); err != nil {
				t.Fatalf("count error = %v", err)
			}
			if found != 0 {
				t.Errorf("table %q was recreated by a failed open", table)
			}
		})
	}
}

// storeEvidence writes one run's valid evidence and returns it.
func storeEvidence(t *testing.T, store *SQLiteStore, run EvaluationRun, records int,
) (EvaluationAggregate, BehaviorSnapshot) {
	t.Helper()
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range records {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%d", i),
			fmt.Sprintf("op-%d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}
	snapshot := collector.Snapshot()
	if err := store.SaveEvaluationEvidence(t.Context(), aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}
	return aggregate, snapshot
}

// Two halves agreeing with each other proves only that they were edited
// consistently. The authoritative statement of what a run is lives in the
// runs table.
func TestStoredEvidenceMustMatchPersistedRun(t *testing.T) {
	tests := []struct {
		name   string
		column string
		value  string
	}{
		{"candidate mismatch", "candidate_id", "cand-other"},
		{"environment mismatch", "environment", "production"},
		{"behavioral profile mismatch", "behavioral_profile", "profile-other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			run := seedRun(t, store)
			storeEvidence(t, store, run, 3)

			// Mutate both halves together, so they stay mutually consistent
			// and only disagree with the run.
			exec(t, store.db, `UPDATE `+tableAggregates+` SET `+tt.column+` = ? WHERE run_id = ?`,
				tt.value, string(run.ID()))
			exec(t, store.db, `UPDATE `+tableSnapshots+` SET `+tt.column+` = ? WHERE run_id = ?`,
				tt.value, string(run.ID()))
			if tt.column == "environment" {
				// Keep the entries consistent with their own snapshot, so the
				// only remaining disagreement is with the run.
				exec(t, store.db, `UPDATE `+tableEntries+` SET environment = ? WHERE run_id = ?`,
					tt.value, string(run.ID()))
			}

			aggregate, snapshot, err := store.EvaluationEvidence(t.Context(), run.ID())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
			}
			if aggregate.bound || snapshot.bound {
				t.Error("evidence disagreeing with its run became trusted")
			}
		})
	}
}

// Exactly one half present is a partial write or restore. Calling it
// "no evidence yet" would let the next save complete the pair and erase
// every trace that anything went wrong.
func TestPartialEvidenceIsCorruptNotFirstWrite(t *testing.T) {
	tests := []struct {
		name  string
		table string
	}{
		{"aggregate without snapshot", tableSnapshots},
		{"snapshot without aggregate", tableAggregates},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, _ := testStore(t)
			ctx := t.Context()
			run := seedRun(t, store)
			storeEvidence(t, store, run, 3)

			exec(t, store.db, `PRAGMA foreign_keys = OFF`)
			if tt.table == tableSnapshots {
				exec(t, store.db, `DELETE FROM `+tableEntries+` WHERE run_id = ?`, string(run.ID()))
			}
			exec(t, store.db, `DELETE FROM `+tt.table+` WHERE run_id = ?`, string(run.ID()))

			// Reading is corruption, not NotFound.
			_, _, err := store.EvaluationEvidence(ctx, run.ID())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
			}
			if errors.Is(err, ErrStoreNotFound) {
				t.Error("partial evidence was reported as absent evidence")
			}

			// And a later save must not quietly heal it.
			aggregate, err := NewEvaluationAggregate(run)
			if err != nil {
				t.Fatalf("NewEvaluationAggregate() error = %v", err)
			}
			collector, err := NewBehaviorCollector(run)
			if err != nil {
				t.Fatalf("NewBehaviorCollector() error = %v", err)
			}
			for i := range 9 {
				rec := internalRecord(fmt.Sprintf("new-%d", i), fmt.Sprintf("nfp-%d", i),
					fmt.Sprintf("nop-%d", i))
				if aggregate, err = aggregate.AddRecord(rec); err != nil {
					t.Fatalf("AddRecord() error = %v", err)
				}
				if err := collector.Observe(rec); err != nil {
					t.Fatalf("Observe() error = %v", err)
				}
			}
			if err := store.SaveEvaluationEvidence(ctx, aggregate, collector.Snapshot()); !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("SaveEvaluationEvidence() over partial state error = %v, want ErrStoreCorrupt", err)
			}

			// The partial state is left exactly as found.
			var remaining int
			if err := store.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM `+tt.table+` WHERE run_id = ?`, string(run.ID())).
				Scan(&remaining); err != nil {
				t.Fatalf("count error = %v", err)
			}
			if remaining != 0 {
				t.Errorf("the refused save recreated the missing half (%d rows)", remaining)
			}
		})
	}
}

// `complete == 1` would turn a stored 2 into false, normalizing corruption
// into the safer-looking of two answers.
func TestInvalidStoredSnapshotCompletenessIsCorrupt(t *testing.T) {
	for _, value := range []int{2, -1, 42} {
		t.Run(strconv.Itoa(value), func(t *testing.T) {
			store, _ := testStore(t)
			run := seedRun(t, store)
			storeEvidence(t, store, run, 3)

			exec(t, store.db, `UPDATE `+tableSnapshots+` SET complete = ? WHERE run_id = ?`,
				value, string(run.ID()))

			aggregate, snapshot, err := store.EvaluationEvidence(t.Context(), run.ID())
			if !errors.Is(err, ErrStoreCorrupt) {
				t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
			}
			if aggregate.bound || snapshot.bound {
				t.Error("a snapshot with an invalid complete flag became trusted")
			}
		})
	}
}

// Every live snapshot satisfies sum(entries) == ObservationCount exactly,
// saturated or not: the collector counts an observation only after every
// check passes, and the saturation path returns before that point.
func TestSnapshotEntrySumMustMatchHeaderEvenWhenIncomplete(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	rec := internalRecord("evt-0", "fp-0", "op-0")
	// The aggregate deliberately holds far more records than the snapshot
	// header will claim. Without that headroom the aggregate-vs-snapshot
	// check fires first and this test would pass without the snapshot's own
	// arithmetic guard existing at all — which is what a mutation of that
	// guard revealed.
	for range 20 {
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
	}

	incomplete := BehaviorSnapshot{
		bound: true, runID: run.ID(), candidateID: run.CandidateID(),
		environment: run.Environment(), profile: run.BehavioralProfile(),
		observations: 5, complete: false,
		entries: []BehaviorEntry{{FingerprintID: "fp-0", Behavior: rec.Behavior, Observations: 5}},
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, incomplete); err != nil {
		t.Fatalf("SaveEvaluationEvidence() error = %v", err)
	}

	// Header claims one more observation than the entries account for, and
	// still fewer than the aggregate — so only the snapshot's internal
	// equality can catch it.
	exec(t, store.db, `UPDATE `+tableSnapshots+` SET observation_count = ? WHERE run_id = ?`,
		uint64Text(6), string(run.ID()))

	if _, _, err := store.EvaluationEvidence(ctx, run.ID()); !errors.Is(err, ErrStoreCorrupt) {
		t.Fatalf("EvaluationEvidence() error = %v, want ErrStoreCorrupt", err)
	}
}

// The valid saturation shape must still load: the snapshot observed fewer
// records than the aggregate, while its own arithmetic balances exactly.
func TestValidSaturatedEvidenceStillLoads(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	run := seedRun(t, store)

	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	for i := range 513 {
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%04d", i),
			fmt.Sprintf("op-%04d", i))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil && !errors.Is(err, ErrBehaviorCapacity) {
			t.Fatalf("Observe() error = %v", err)
		}
	}

	snapshot := collector.Snapshot()
	if snapshot.Complete() {
		t.Fatal("precondition: the collector did not saturate")
	}
	if aggregate.RecordCount() != 513 || snapshot.ObservationCount() != 512 {
		t.Fatalf("precondition: aggregate %d, snapshot %d; want 513 and 512",
			aggregate.RecordCount(), snapshot.ObservationCount())
	}
	if err := store.SaveEvaluationEvidence(ctx, aggregate, snapshot); err != nil {
		t.Fatalf("SaveEvaluationEvidence() rejected valid saturated evidence: %v", err)
	}

	loadedAggregate, loadedSnapshot, err := store.EvaluationEvidence(ctx, run.ID())
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	if loadedSnapshot.Complete() {
		t.Error("Complete() = true after restore")
	}
	if loadedAggregate.RecordCount() != 513 || loadedSnapshot.ObservationCount() != 512 {
		t.Errorf("counts = %d/%d, want 513/512",
			loadedAggregate.RecordCount(), loadedSnapshot.ObservationCount())
	}
}

// ---------------------------------------------------------------------
// Concurrent initialization
// ---------------------------------------------------------------------

// A racing initializer can make any statement in schema creation fail, not
// only the commit. Recovery therefore re-inspects the durable state instead
// of matching an error string, and the final schema decides.
func TestConcurrentFreshOpenersConvergeOnValidSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "race.db")
	const openers = 8

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	stores := make([]*SQLiteStore, openers)
	errs := make([]error, openers)

	for i := range openers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait() // a barrier, not a sleep
			stores[i], errs[i] = OpenSQLiteStore(context.Background(), path)
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d failed: %v", i, err)
		}
	}
	for i, store := range stores {
		if store == nil {
			continue
		}
		var version int
		if err := store.db.QueryRow(
			`SELECT version FROM ` + tableSchemaVersion + ` WHERE id = 1`).Scan(&version); err != nil {
			t.Errorf("opener %d: read version: %v", i, err)
		} else if version != SchemaVersion {
			t.Errorf("opener %d: version = %d, want %d", i, version, SchemaVersion)
		}
		store.Close()
	}

	// The converged database is complete and usable afterwards.
	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen after race error = %v", err)
	}
	defer reopened.Close()
	seedRun(t, reopened)
}

// TestUpdateEvaluationRunPredicateRefusesAStaleStatus proves the
// compare-and-swap lives in the UPDATE statement, not only in the Go check
// above it.
//
// That check is sound for SQLite because this store holds one connection, so
// nothing interleaves between the read and the write — which makes the safety a
// property of the configuration rather than of the operation. A backend with a
// connection pool does not have it: two callers would both read `created`, both
// pass the check, and both write, the second silently losing the first.
//
// Driven through the store's own API: two transitions derived from the same
// starting value, the second of which must lose.
func TestUpdateEvaluationRunPredicateRefusesAStaleStatus(t *testing.T) {
	store, _ := testStore(t)
	ctx := t.Context()
	created := seedRun(t, store)

	started, err := created.Start(created.CreatedAt().Add(time.Second))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, created, started); err != nil {
		t.Fatalf("first transition error = %v", err)
	}

	// A second caller still holding `created` retries the same transition. Its
	// view is stale; the durable row is already running.
	err = store.UpdateEvaluationRun(ctx, created, started)
	if !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("stale previous returned %v, want ErrStoreConflict", err)
	}

	// The run transitioned exactly once and was not rewritten.
	final, err := store.EvaluationRun(ctx, created.ID())
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	if final.Status() != RunRunning {
		t.Errorf("Status() = %s, want %s", final.Status(), RunRunning)
	}
	if !final.StartedAt().Equal(started.StartedAt()) {
		t.Errorf("StartedAt() = %v, want the first transition's %v",
			final.StartedAt(), started.StartedAt())
	}
}

// TestLifecycleUpdateCarriesItsPredicateInSQL is a structural guard, and it
// exists because the behavioural test above cannot do this job on SQLite.
//
// Removing the `AND status = ?` predicate does not change what
// TestUpdateEvaluationRunPredicateRefusesAStaleStatus observes: this store holds
// one connection, so the Go-side sameRun check already catches every stale
// caller. The predicate is behaviourally redundant *here* — and is exactly what
// a pooled backend needs, where the read and the write can interleave.
//
// So the invariant is pinned two ways: structurally on SQLite, where a
// behavioural test cannot see it, and behaviourally on PostgreSQL, where
// concurrent connections make its absence observable.
func TestLifecycleUpdateCarriesItsPredicateInSQL(t *testing.T) {
	source, err := os.ReadFile("sqlite.go")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	const want = "WHERE id = ? AND status = ?"
	if !strings.Contains(string(source), want) {
		t.Errorf("the lifecycle UPDATE does not carry %q. Without it the "+
			"compare-and-swap is a property of this store's single connection "+
			"rather than of the operation, and a pooled backend loses it.", want)
	}
	// And the result must be inspected, or the predicate silently becomes a
	// no-op that updates zero rows and reports success.
	if !strings.Contains(string(source), "RowsAffected()") {
		t.Error("the lifecycle UPDATE does not check RowsAffected; a predicate " +
			"that matches nothing would report success")
	}
}

// TestSchemaTablesCoverEveryKnownVersion is the guard on the map the migration
// race recovery consults.
//
// It exists because of a real regression: migrateV1ToV2's recovery switch
// listed schemaVersionV2 and SchemaVersion, which were 2 and 3 when it was
// written and covered every reachable state. Adding v4 left 3 covered by
// neither case, so an opener that lost a race to a concurrent migrator saw a
// perfectly valid v3 database, matched nothing, and returned the transient
// error as a failed open. Nothing failed at the point of the change; the test
// that caught it was a concurrency test several steps away.
//
// A missing entry now fails here, next to the change that caused it.
func TestSchemaTablesCoverEveryKnownVersion(t *testing.T) {
	for version := schemaVersionV1; version <= SchemaVersion; version++ {
		tables, known := schemaTablesByVersion[version]
		if !known {
			t.Errorf("schema version %d has no table list; a migration losing a "+
				"race to a concurrent opener at this version would report the "+
				"transient error as a failed open", version)
			continue
		}
		if len(tables) == 0 {
			t.Errorf("schema version %d maps to an empty table list", version)
		}
	}
	if got := len(schemaTablesByVersion); got != SchemaVersion {
		t.Errorf("schemaTablesByVersion has %d entries, want %d (one per version "+
			"from 1 to SchemaVersion)", got, SchemaVersion)
	}
	// Each version holds everything the one before it did. A list that lost a
	// table would make requireTables accept a database missing one.
	for version := schemaVersionV1 + 1; version <= SchemaVersion; version++ {
		previous := schemaTablesByVersion[version-1]
		current := schemaTablesByVersion[version]
		if len(current) <= len(previous) {
			t.Errorf("v%d holds %d tables, v%d holds %d; every step adds at least one",
				version, len(current), version-1, len(previous))
		}
		for _, table := range previous {
			if !slices.Contains(current, table) {
				t.Errorf("v%d is missing %s, which v%d held", version, table, version-1)
			}
		}
	}
}
