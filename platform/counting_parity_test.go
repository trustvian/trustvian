package platform

// Storage-backend parity for the counting fold (task 083, ADR 0052).
//
// In the internal package because the PostgreSQL schema isolation this needs
// lives here. The fold reads `trace_id`, `span_id`, `parent_span_id` and
// `fingerprint_id` from retained observations, and both backends bind those
// positionally against one shared column list — so parity ought to be
// structural. "Ought to be" is the reason for the test: the point of a parity
// suite is that a backend can drift in a way nobody notices until a
// production database reports a different number from a developer's laptop.

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// countingParityEpoch is fixed, so nothing here depends on wall-clock time.
var countingParityEpoch = time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC)

// countingParityRecord builds one correlated record.
func countingParityRecord(
	eventID, fingerprintID, operation, target, trace, span, parent string,
) trustvian.DecisionRecord {
	rec := trustvian.DecisionRecord{
		EventID:   eventID,
		Timestamp: countingParityEpoch,
		ActorID:   "agent-1",
		ActorType: event.ActorTypeAIAgent,

		FingerprintID: fingerprintID,
		Behavior: trustvian.StableFeatures{
			ActorType:         event.ActorTypeAIAgent,
			OperationCategory: event.OperationCategoryTool,
			OperationName:     operation,
			TargetName:        target,
			TargetCategory:    event.TargetCategoryInternal,
			Environment:       "staging",
		},
		Environment: "staging",

		Decision:   "allow",
		RiskLevel:  "low",
		PolicyRule: "allow-known",

		IdentityConfidence: 0.9,
		AnomalyScore:       0.1,
		AnomalyConfidence:  0.9,
		TrustScore:         0.9,
		ContextRisk:        0.1,

		TraceID:      trace,
		SpanID:       span,
		ParentSpanID: parent,
	}
	if parent == "" {
		rec.SpanLineage = event.LineageRoot
	} else {
		rec.SpanLineage = event.LineageChild
	}
	return rec
}

// driveCountingRun seeds the hierarchy and drives one run to completion.
func driveCountingRun(
	t *testing.T, plane *ControlPlane,
	runID EvaluationRunID, candidateID CandidateID, records []trustvian.DecisionRecord,
) {
	t.Helper()
	ctx := context.Background()

	project, _ := NewProject("proj-1", "Checkout")
	agent, _ := NewAgent("agent-1", "proj-1", "Deploy agent")
	candidate, _ := NewCandidate(candidateID, "agent-1", CandidateMetadata{Label: "v1"})
	environment, _ := NewEnvironment("staging", "proj-1", "Staging")
	for _, err := range []error{
		plane.CreateProject(ctx, project),
		plane.CreateAgent(ctx, agent),
		plane.CreateCandidate(ctx, candidate),
		plane.CreateEnvironment(ctx, environment),
	} {
		if err != nil && !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("seed error = %v", err)
		}
	}

	run, err := NewEvaluationRun(runID, candidateID, "staging", "profile-1", countingParityEpoch)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := plane.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}
	if _, err := plane.StartEvaluationRun(ctx, runID, countingParityEpoch.Add(time.Minute)); err != nil {
		t.Fatalf("StartEvaluationRun() error = %v", err)
	}
	for i, rec := range records {
		if _, err := plane.IngestDecisionRecord(ctx, IngestRequest{
			RunID:             runID,
			Sequence:          uint64(i + 1),
			BehavioralProfile: "profile-1",
			Record:            rec,
		}); err != nil {
			t.Fatalf("ingest %d error = %v", i, err)
		}
	}
	if _, err := plane.CompleteEvaluationRun(ctx, runID, countingParityEpoch.Add(time.Hour)); err != nil {
		t.Fatalf("CompleteEvaluationRun() error = %v", err)
	}
}

type countingParityResult struct {
	added   int
	changes int
	state   CorrelationState
	roots   []string
}

// measureCounting drives the same evidence through one backend.
func measureCounting(t *testing.T, plane *ControlPlane) countingParityResult {
	t.Helper()
	driveCountingRun(t, plane, "run-ref", "cand-ref", []trustvian.DecisionRecord{
		countingParityRecord("ref-1", "fp-read", "read", "store", "trace-ref", "span-a", ""),
	})
	driveCountingRun(t, plane, "run-can", "cand-can", []trustvian.DecisionRecord{
		countingParityRecord("can-1", "fp-read", "read", "store", "trace-can", "span-a", ""),
		countingParityRecord("can-2", "fp-tool", "export_customer", "", "trace-can", "span-t", "span-a"),
		countingParityRecord("can-3", "fp-http", "post", "export.localhost", "trace-can", "span-h", "span-t"),
	})

	comparison, err := plane.CompareEvaluations(context.Background(), "run-ref", "run-can",
		EvaluationGateLimits{
			MaxAddedBehaviors:           10,
			MaxBlockDecisions:           10,
			MaxCriticalRiskObservations: 10,
		})
	if err != nil {
		t.Fatalf("CompareEvaluations() error = %v", err)
	}
	diff := comparison.Diff
	roots := make([]string, 0, diff.AddedChangeCount())
	for _, c := range diff.AddedChanges() {
		roots = append(roots, c.RootFingerprintID)
	}
	return countingParityResult{
		added:   diff.AddedCount(),
		changes: diff.AddedChangeCount(),
		state:   diff.CorrelationState(),
		roots:   roots,
	}
}

func TestBothBackendsCountTheSameChanges(t *testing.T) {
	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; a parity test needs both backends", postgresDSNEnv)
	}

	sqliteStore, err := OpenSQLiteStore(context.Background(),
		filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })
	sqlitePlane, err := NewControlPlane(sqliteStore, sqliteStore, sqliteStore)
	if err != nil {
		t.Fatalf("NewControlPlane(sqlite) error = %v", err)
	}

	postgresStore, err := OpenPostgresStore(context.Background(), PostgresConfig{
		DSN: isolatedSchemaDSN(t, dsn),
	})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	t.Cleanup(func() { _ = postgresStore.Close() })
	postgresPlane, err := NewControlPlane(postgresStore, postgresStore, postgresStore)
	if err != nil {
		t.Fatalf("NewControlPlane(postgres) error = %v", err)
	}

	sqliteResult := measureCounting(t, sqlitePlane)
	postgresResult := measureCounting(t, postgresPlane)

	// The value both must agree on, asserted first: a backend returning
	// nothing must not pass by matching another backend returning nothing.
	if sqliteResult.added != 2 || sqliteResult.changes != 1 {
		t.Fatalf("sqlite reported added=%d changes=%d, want 2 and 1",
			sqliteResult.added, sqliteResult.changes)
	}
	if sqliteResult.state != CorrelationComplete {
		t.Fatalf("sqlite correlation = %q, want complete", sqliteResult.state)
	}

	if sqliteResult.added != postgresResult.added ||
		sqliteResult.changes != postgresResult.changes {
		t.Errorf("counts differ by backend: sqlite added=%d changes=%d, "+
			"postgres added=%d changes=%d",
			sqliteResult.added, sqliteResult.changes,
			postgresResult.added, postgresResult.changes)
	}
	if sqliteResult.state != postgresResult.state {
		t.Errorf("correlation state differs: sqlite %q, postgres %q",
			sqliteResult.state, postgresResult.state)
	}
	if !slices.Equal(sqliteResult.roots, postgresResult.roots) {
		t.Errorf("change roots differ: sqlite %v, postgres %v",
			sqliteResult.roots, postgresResult.roots)
	}
}
