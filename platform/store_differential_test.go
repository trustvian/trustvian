package platform

// Differential testing: the same operations, both backends, compared.
//
// The conformance suite asserts each backend satisfies the contract. This
// asserts they satisfy it *identically* — run one ordered sequence against
// SQLite and PostgreSQL and compare the logical state that results.
//
// The difference matters. A contract test only catches what somebody thought to
// assert; this catches any observable divergence, including ones nobody
// predicted. It is the strongest guard available against the two physical
// schemas drifting apart, and it is why the aggregate's forty-four columns have
// one shared definition rather than two copies that agree today.
//
// Physical detail is deliberately not compared: column types, storage layout and
// row order where no order is specified are each backend's business.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// logicalState is everything about a run that a caller can observe.
type logicalState struct {
	// Run identity and lifecycle.
	runID       EvaluationRunID
	candidateID CandidateID
	environment EnvironmentRef
	profile     BehavioralProfileRef
	status      RunStatus

	// Rendered exactly as /v1 publishes them, so an offset or a truncated
	// nanosecond is a difference rather than something Equal() forgives.
	createdAt  string
	startedAt  string
	finishedAt string

	// Counters as canonical decimal text, never as integers: comparing them as
	// numbers is how a signed-range bug hides.
	recordCount      string
	observationCount string
	distinctCount    int
	behaviorComplete bool
	nextSequence     string
	lastDigest       string

	// The aggregate's whole distribution, so a mis-ordered column list shows up.
	decisions DecisionCounts
	risks     RiskCounts
	approvals ApprovalCounts
	policy    PolicySelection
	metrics   []MetricSummary

	// Behaviour entries in the order the store returned them, which is itself
	// part of the contract — the only ORDER BY in either backend.
	entries []string
}

// captureLogicalState reads everything observable about one run.
func captureLogicalState(t *testing.T, store Store, id EvaluationRunID) logicalState {
	t.Helper()
	ctx := context.Background()

	run, err := store.EvaluationRun(ctx, id)
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	aggregate, snapshot, err := store.EvaluationEvidence(ctx, id)
	if err != nil {
		t.Fatalf("EvaluationEvidence() error = %v", err)
	}
	cursor, err := store.EvaluationIngestState(ctx, id)
	if err != nil {
		t.Fatalf("EvaluationIngestState() error = %v", err)
	}

	entries := make([]string, 0, len(snapshot.Entries()))
	for _, entry := range snapshot.Entries() {
		b := entry.Behavior
		entries = append(entries, fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s",
			entry.FingerprintID, b.ActorType, b.OperationCategory, b.OperationName,
			b.TargetName, b.TargetCategory, b.Environment,
			uint64Text(entry.Observations)))
	}

	return logicalState{
		runID:       run.ID(),
		candidateID: run.CandidateID(),
		environment: run.Environment(),
		profile:     run.BehavioralProfile(),
		status:      run.Status(),

		createdAt:  renderInstant(run.CreatedAt()),
		startedAt:  renderInstant(run.StartedAt()),
		finishedAt: renderInstant(run.FinishedAt()),

		recordCount:      uint64Text(aggregate.RecordCount()),
		observationCount: uint64Text(snapshot.ObservationCount()),
		distinctCount:    snapshot.DistinctBehaviorCount(),
		behaviorComplete: snapshot.Complete(),
		nextSequence:     uint64Text(cursor.NextSequence()),
		lastDigest:       cursor.LastDigest(),

		decisions: aggregate.Decisions(),
		risks:     aggregate.Risks(),
		approvals: aggregate.Approvals(),
		policy:    aggregate.PolicySelection(),
		metrics:   aggregateMetrics(aggregate),

		entries: entries,
	}
}

// renderInstant formats a timestamp the way /v1 does, or reports absence.
//
// A zero time is absent rather than an epoch, which is the distinction task 053
// built the aggregate around.
func renderInstant(t time.Time) string {
	if t.IsZero() {
		return "<absent>"
	}
	return t.Format(time.RFC3339Nano)
}

// TestStoreBackendsAgreeOnLogicalState is the differential suite.
func TestStoreBackendsAgreeOnLogicalState(t *testing.T) {
	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; a differential test needs both backends", postgresDSNEnv)
	}

	sqliteStore, err := OpenSQLiteStore(context.Background(),
		filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = sqliteStore.Close() })

	postgresStore, err := OpenPostgresStore(context.Background(), PostgresConfig{
		DSN: isolatedSchemaDSN(t, dsn),
	})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	t.Cleanup(func() { _ = postgresStore.Close() })

	sqliteState := driveSequence(t, sqliteStore)
	postgresState := driveSequence(t, postgresStore)

	compareLogicalState(t, sqliteState, postgresState)
}

// driveSequence runs one fixed, ordered sequence of real operations.
//
// Deterministic throughout: a fixed clock, fixed identifiers, fixed record
// contents. Anything nondeterministic here would show up as a false divergence
// and teach whoever hit it to distrust this test.
func driveSequence(t *testing.T, store Store) logicalState {
	t.Helper()
	ctx := context.Background()

	// A deliberately awkward instant: non-UTC offset, nanoseconds that are not a
	// whole microsecond. This is the value a native timestamp column would
	// change, and it travels through every timestamp below.
	zone := time.FixedZone("-0230", -(2*3600 + 30*60))
	created := time.Date(2026, 11, 5, 23, 45, 6, 987654321, zone)

	if err := store.CreateProject(ctx, mustProject(t, "proj-diff", "Checkout")); err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if err := store.CreateAgent(ctx, mustAgent(t, "agent-diff", "proj-diff", "Deploy agent")); err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	metadata := CandidateMetadata{
		Label: "v2", SourceRef: "refs/heads/main",
		ArtifactDigest: "sha256:artifact", Model: "claude",
		ToolsetDigest: "sha256:toolset", ConfigDigest: "sha256:config",
	}
	if err := store.CreateCandidate(ctx, mustCandidate(t, "cand-diff", "agent-diff", metadata)); err != nil {
		t.Fatalf("CreateCandidate() error = %v", err)
	}

	run, err := NewEvaluationRun("run-diff", "cand-diff", "staging", "profile-1", created)
	if err != nil {
		t.Fatalf("NewEvaluationRun() error = %v", err)
	}
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatalf("CreateEvaluationRun() error = %v", err)
	}

	started, err := run.Start(created.Add(90 * time.Second))
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatalf("start error = %v", err)
	}

	// Ingest several records through the real protocol, so the cursor, the
	// aggregate and the snapshot all advance the way they do in production.
	previousNext := uint64(1)
	for sequence := uint64(1); sequence <= 4; sequence++ {
		aggregate, snapshot := differentialEvidence(t, started, int(sequence))
		commit := EvaluationIngestCommit{
			Aggregate:            aggregate,
			Snapshot:             snapshot,
			Sequence:             sequence,
			PreviousNextSequence: previousNext,
			// Deterministic per sequence, so both backends record the same
			// digest and a differing one is a real divergence.
			RecordDigest: strings.Repeat(fmt.Sprintf("%d", sequence%10), 64),
		}
		result, err := store.CommitEvaluationIngest(ctx, commit)
		if err != nil {
			t.Fatalf("CommitEvaluationIngest(%d) error = %v", sequence, err)
		}
		previousNext = result.NextSequence
	}

	// An identical retry of the last record, which must be reconciled rather
	// than counted again.
	aggregate, snapshot := differentialEvidence(t, started, 4)
	retry := EvaluationIngestCommit{
		Aggregate:            aggregate,
		Snapshot:             snapshot,
		Sequence:             4,
		PreviousNextSequence: 4,
		RecordDigest:         strings.Repeat("4", 64),
	}
	if _, err := store.CommitEvaluationIngest(ctx, retry); err != nil {
		t.Fatalf("retry error = %v", err)
	}

	// A refused transition, so the durable state reflects the refusal
	// identically on both backends.
	if err := store.UpdateEvaluationRun(ctx, run, started); !errors.Is(err, ErrStoreConflict) {
		t.Fatalf("stale transition = %v, want ErrStoreConflict", err)
	}

	current, err := store.EvaluationRun(ctx, "run-diff")
	if err != nil {
		t.Fatalf("EvaluationRun() error = %v", err)
	}
	completed, err := current.Complete(created.Add(300 * time.Second))
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := store.UpdateEvaluationRun(ctx, current, completed); err != nil {
		t.Fatalf("complete error = %v", err)
	}

	return captureLogicalState(t, store, "run-diff")
}

// differentialEvidence builds evidence over records observations.
func differentialEvidence(t *testing.T, run EvaluationRun, records int) (EvaluationAggregate, BehaviorSnapshot) {
	t.Helper()
	aggregate, err := NewEvaluationAggregate(run)
	if err != nil {
		t.Fatalf("NewEvaluationAggregate() error = %v", err)
	}
	collector, err := NewBehaviorCollector(run)
	if err != nil {
		t.Fatalf("NewBehaviorCollector() error = %v", err)
	}
	// Fingerprints chosen so their lexicographic order is not their insertion
	// order: this is what would expose a collation difference in the one
	// ORDER BY either backend performs.
	names := []string{"zz-last", "Aa-upper", "_underscore", "mid-10", "mid-2"}
	for i := range records {
		name := names[i%len(names)]
		rec := internalRecord(fmt.Sprintf("evt-%d", i), fmt.Sprintf("fp-%s", name),
			fmt.Sprintf("op-%s", name))
		if aggregate, err = aggregate.AddRecord(rec); err != nil {
			t.Fatalf("AddRecord() error = %v", err)
		}
		if err := collector.Observe(rec); err != nil {
			t.Fatalf("Observe() error = %v", err)
		}
	}
	return aggregate, collector.Snapshot()
}

// compareLogicalState reports every field on which the backends disagree.
//
// Field by field rather than one struct comparison, so a failure names what
// diverged instead of printing two large values and leaving the reader to diff
// them.
func compareLogicalState(t *testing.T, sqlite, postgres logicalState) {
	t.Helper()

	check := func(field string, got, want any) {
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s diverged:\n  sqlite   = %v\n  postgres = %v", field, want, got)
		}
	}

	check("run id", postgres.runID, sqlite.runID)
	check("candidate id", postgres.candidateID, sqlite.candidateID)
	check("environment", postgres.environment, sqlite.environment)
	check("behavioral profile", postgres.profile, sqlite.profile)
	check("status", postgres.status, sqlite.status)

	// The headline parity properties: offset and nanosecond precision.
	check("created_at", postgres.createdAt, sqlite.createdAt)
	check("started_at", postgres.startedAt, sqlite.startedAt)
	check("finished_at", postgres.finishedAt, sqlite.finishedAt)

	check("record count", postgres.recordCount, sqlite.recordCount)
	check("observation count", postgres.observationCount, sqlite.observationCount)
	check("distinct behaviour count", postgres.distinctCount, sqlite.distinctCount)
	check("behavior complete", postgres.behaviorComplete, sqlite.behaviorComplete)
	check("next sequence", postgres.nextSequence, sqlite.nextSequence)
	check("last digest", postgres.lastDigest, sqlite.lastDigest)

	check("decision counts", postgres.decisions, sqlite.decisions)
	check("risk counts", postgres.risks, sqlite.risks)
	check("approval counts", postgres.approvals, sqlite.approvals)
	check("policy selection", postgres.policy, sqlite.policy)

	if len(postgres.metrics) != len(sqlite.metrics) {
		t.Fatalf("metric count diverged: sqlite %d, postgres %d",
			len(sqlite.metrics), len(postgres.metrics))
	}
	for i := range sqlite.metrics {
		check(fmt.Sprintf("metric %s", aggregateMetricPrefixes()[i]),
			postgres.metrics[i], sqlite.metrics[i])
	}

	// Entry order is part of the contract: `ORDER BY fingerprint_id` is the only
	// domain ordering, and PostgreSQL's default collation would order these
	// differently from SQLite's byte comparison without COLLATE "C".
	if len(postgres.entries) != len(sqlite.entries) {
		t.Fatalf("entry count diverged: sqlite %d, postgres %d",
			len(sqlite.entries), len(postgres.entries))
	}
	for i := range sqlite.entries {
		if postgres.entries[i] != sqlite.entries[i] {
			t.Errorf("behaviour entry %d diverged (ordering or content):\n"+
				"  sqlite   = %s\n  postgres = %s\n"+
				"A difference in position alone means the collation does not match; "+
				"identifier columns need COLLATE \"C\" to reproduce SQLite's byte order.",
				i, sqlite.entries[i], postgres.entries[i])
		}
	}
}
