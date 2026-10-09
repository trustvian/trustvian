package platform

// Task 087: per-behavior operational evidence — the fold, its invariants, its
// persistence and schema 11's migration.

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"
)

// behaviorOperationalColumns lists schema 11's columns, for the guards that
// compare a fresh and a migrated table.
func behaviorOperationalColumns() []string { return []string{columnOperationalCounts} }

// downgradeToV10 turns a current database into the v10 shape: v11's columns
// gone, version 10 stamped. exec runs one statement against it.
func downgradeToV10(t testing.TB, exec func(string) error) {
	t.Helper()
	downgradeToV11(t, exec)
	for _, column := range behaviorOperationalColumns() {
		if err := exec(`ALTER TABLE ` + tableEntries + ` DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop v11 column %s: %v", column, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 10 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v10: %v", err)
	}
}

// operationalRun is a run to build records for; nothing persists it.
func operationalRun(t *testing.T) EvaluationRun {
	t.Helper()
	run, err := NewEvaluationRun("run-1", "cand-1", "staging", "profile-1",
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func usage(in, out int64) event.Usage {
	u := event.Usage{}
	if in >= 0 {
		u.Input, u.HasInput = uint64(in), true
	}
	if out >= 0 {
		u.Output, u.HasOutput = uint64(out), true
	}
	return u
}

func TestDurationBucketBoundariesAreUpperInclusive(t *testing.T) {
	const ms = 1_000_000
	tests := []struct {
		nanos uint64
		want  int
	}{
		{0, 0}, // a measured zero is a measurement
		{1 * ms, 0},
		{1*ms + 1, 1},
		{5 * ms, 1},
		{10 * ms, 2},
		{50 * ms, 3},
		{100 * ms, 4},
		{250 * ms, 5},
		{500 * ms, 6},
		{1000 * ms, 7},
		{2500 * ms, 8},
		{10000 * ms, 9},
		{10000*ms + 1, 10},
		{math.MaxInt64, 10},
	}
	for _, tt := range tests {
		if got := durationBucket(tt.nanos); got != tt.want {
			t.Errorf("durationBucket(%d) = %d, want %d", tt.nanos, got, tt.want)
		}
	}
}

// TestOperationalFoldPartitionsEveryObservation is the documented arithmetic:
// each counter set sums to the observations, a measured zero is counted as a
// duration, and evidence that was not stated is counted as such — never as 0.
func TestOperationalFoldPartitionsEveryObservation(t *testing.T) {
	run := operationalRun(t)
	steps := []struct {
		duration string
		status   event.SpanStatus
		facts    OperationalFacts
	}{
		{"0", event.StatusOK, OperationalFacts{HTTPStatusCode: 200, Usage: usage(100, 20)}},
		{"1000000", event.StatusOK, OperationalFacts{HTTPStatusCode: 429}},
		{"1000001", event.StatusError, OperationalFacts{HTTPStatusCode: 503, Usage: usage(5, -1)}},
		{"", event.StatusUnset, OperationalFacts{Usage: event.Usage{Unsplit: 40, HasUnsplit: true}}},
		{"20000000000", event.StatusUnavailable, OperationalFacts{HTTPStatusCode: 404}},
	}
	var s OperationalSummary
	for i, step := range steps {
		record := operationalTestRecord(run, "e"+strconv.Itoa(i), step.duration, step.status)
		next, err := s.observe(record, step.facts)
		if err != nil {
			t.Fatalf("observe(%d) error = %v", i, err)
		}
		s = next
	}
	want := OperationalSummary{
		Buckets:    DurationBuckets{0: 2, 1: 1, 10: 1},
		Durations:  DurationSummary{Count: 4, Unobserved: 1, Sum: 20_002_000_001, Min: 0, Max: 20_000_000_000},
		SpanStatus: SpanStatusCounts{Unavailable: 1, Unset: 1, OK: 2, Error: 1},
		HTTPStatus: HTTPStatusClassCounts{Success: 1, ClientError: 2, ServerError: 1, Unavailable: 1},
		HTTP429:    1,
		Tokens:     TokenCounts{Input: 105, Output: 20, Unsplit: 40, Observed: 3, Unobserved: 2},
	}
	if s != want {
		t.Fatalf("summary\n got %+v\nwant %+v", s, want)
	}
	if err := validateOperationalSummary(s, uint64(len(steps))); err != nil {
		t.Fatalf("a folded summary fails its own restore check: %v", err)
	}
}

func TestOperationalFoldRefusesAndLeavesTheSummaryUnchanged(t *testing.T) {
	run := operationalRun(t)
	full := OperationalSummary{Tokens: TokenCounts{Input: math.MaxUint64, Observed: 1}}
	tests := []struct {
		name   string
		start  OperationalSummary
		record trustvian.DecisionRecord
		facts  OperationalFacts
		want   error
	}{
		{"token sum overflow", full, operationalTestRecord(run, "e", "", event.StatusOK),
			OperationalFacts{Usage: usage(1, -1)}, ErrBehaviorOverflow},
		{"malformed duration", OperationalSummary{}, operationalTestRecord(run, "e", "01", event.StatusOK),
			OperationalFacts{}, ErrInvalidDecisionRecord},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.start.observe(tt.record, tt.facts)
			if !errors.Is(err, tt.want) {
				t.Fatalf("observe() error = %v, want %v", err, tt.want)
			}
			if got != tt.start {
				t.Fatalf("a refused observation changed the summary: %+v", got)
			}
		})
	}
}

func TestOperationalFactsValidate(t *testing.T) {
	for _, tt := range []struct {
		name  string
		facts OperationalFacts
		ok    bool
	}{
		{"nothing", OperationalFacts{}, true},
		{"a valid code", OperationalFacts{HTTPStatusCode: 599}, true},
		{"a code below range", OperationalFacts{HTTPStatusCode: 99}, false},
		{"a code above range", OperationalFacts{HTTPStatusCode: 600}, false},
		{"unsplit alone", OperationalFacts{Usage: event.Usage{Unsplit: 1, HasUnsplit: true}}, true},
		{"unsplit beside input", OperationalFacts{Usage: event.Usage{
			Input: 1, HasInput: true, Unsplit: 1, HasUnsplit: true}}, false},
		{"tokens at the per-observation maximum", OperationalFacts{Usage: event.Usage{
			Input: event.MaxTokenCount, HasInput: true}}, true},
		{"tokens past it", OperationalFacts{Usage: event.Usage{
			Output: event.MaxTokenCount + 1, HasOutput: true}}, false},
	} {
		if err := tt.facts.Validate(); (err == nil) != tt.ok {
			t.Errorf("%s: Validate() = %v, want ok %v", tt.name, err, tt.ok)
		}
	}
}

func TestOperationalSummaryAdd(t *testing.T) {
	a := OperationalSummary{
		Buckets:    DurationBuckets{2: 1},
		Durations:  DurationSummary{Count: 1, Sum: 7_000_000, Min: 7_000_000, Max: 7_000_000},
		SpanStatus: SpanStatusCounts{OK: 1},
		HTTPStatus: HTTPStatusClassCounts{Success: 1},
		Tokens:     TokenCounts{Input: 3, Observed: 1},
	}
	if err := validateOperationalSummary(a, 1); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	none := unavailableOperational(2)
	got, err := a.Add(none)
	if err != nil {
		t.Fatal(err)
	}
	// The side with no observed duration contributes no extremum.
	if got.Durations.Min != 7_000_000 || got.Durations.Max != 7_000_000 || got.Durations.Unobserved != 2 {
		t.Errorf("Add() durations = %+v", got.Durations)
	}
	if err := validateOperationalSummary(got, 3); err != nil {
		t.Errorf("a sum of valid summaries is invalid: %v", err)
	}
	if _, err := (OperationalSummary{HTTP429: math.MaxUint64}).Add(OperationalSummary{HTTP429: 1}); !errors.Is(err, ErrBehaviorOverflow) {
		t.Errorf("overflowing Add() error = %v, want ErrBehaviorOverflow", err)
	}
}

// TestCorruptOperationalSummariesAreRefused: every invariant observe keeps on
// write is re-proved on restore, and a summary breaking one is refused.
func TestCorruptOperationalSummariesAreRefused(t *testing.T) {
	valid := OperationalSummary{
		Buckets:    DurationBuckets{0: 1, 3: 1},
		Durations:  DurationSummary{Count: 2, Unobserved: 1, Sum: 40_000_000, Min: 0, Max: 40_000_000},
		SpanStatus: SpanStatusCounts{OK: 3},
		HTTPStatus: HTTPStatusClassCounts{ClientError: 2, Unavailable: 1},
		HTTP429:    1,
		Tokens:     TokenCounts{Input: 9, Observed: 1, Unobserved: 2},
	}
	if err := validateOperationalSummary(valid, 3); err != nil {
		t.Fatalf("fixture is invalid: %v", err)
	}
	for name, corrupt := range map[string]func(*OperationalSummary){
		"buckets disagree with the count": func(s *OperationalSummary) { s.Buckets[0] = 2 },
		"an extremum's bucket is empty":   func(s *OperationalSummary) { s.Buckets[0], s.Buckets[1] = 0, 1 },
		"a bucket outside the extrema":    func(s *OperationalSummary) { s.Buckets[3], s.Buckets[10] = 0, 1 },
		"http classes do not partition":   func(s *OperationalSummary) { s.HTTPStatus.Unavailable = 2 },
		"429 above 4xx":                   func(s *OperationalSummary) { s.HTTP429 = 3 },
		"tokens do not partition":         func(s *OperationalSummary) { s.Tokens.Unobserved = 1 },
		"sums with nothing observed": func(s *OperationalSummary) {
			s.Tokens.Observed, s.Tokens.Unobserved = 0, 3
		},
		"span statuses do not partition": func(s *OperationalSummary) { s.SpanStatus.OK = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			s := valid
			corrupt(&s)
			if err := validateOperationalSummary(s, 3); err == nil {
				t.Fatal("a corrupt summary was accepted")
			}
		})
	}
}

// TestBehaviorOperationalEvidenceSurvivesARestart is the SQLite round trip
// through the real ingest path: what the control plane folded is what a
// restarted store reads back, entry for entry.
func TestBehaviorOperationalEvidenceSurvivesARestart(t *testing.T) {
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for i, step := range []struct {
		duration string
		facts    OperationalFacts
	}{
		{"3000000", OperationalFacts{HTTPStatusCode: 200, Usage: usage(10, 2)}},
		{"", OperationalFacts{HTTPStatusCode: 429}},
		{"0", OperationalFacts{}},
	} {
		record := operationalTestRecord(run, "e"+strconv.Itoa(i), step.duration, event.StatusOK)
		if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1), BehavioralProfile: run.BehavioralProfile(),
			Operational: step.facts, Record: record,
		}); err != nil {
			t.Fatalf("ingest %d: %v", i, err)
		}
	}
	_, before, err := store.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	_, after, err := reopened.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	got := after.Entries()[0].Operational
	if got != before.Entries()[0].Operational {
		t.Fatalf("after restart %+v, before %+v", got, before.Entries()[0].Operational)
	}
	if got.Buckets[0] != 1 || got.Buckets[1] != 1 || got.HTTP429 != 1 || got.HTTPStatus.Unavailable != 1 ||
		got.Tokens.Input != 10 || got.Tokens.Observed != 1 || got.Tokens.Unobserved != 2 {
		t.Fatalf("restored summary %+v", got)
	}
}

// TestIngestRefusesInvalidOperationalFacts, before anything is written —
// including into a run whose behavioral evidence is saturated.
func TestIngestRefusesInvalidOperationalFacts(t *testing.T) {
	store, _ := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID: run.ID(), Sequence: 1, BehavioralProfile: run.BehavioralProfile(),
		Operational: OperationalFacts{HTTPStatusCode: 700},
		Record:      operationalTestRecord(run, "e1", "", event.StatusOK),
	})
	if !errors.Is(err, ErrInvalidDecisionRecord) {
		t.Fatalf("error = %v, want ErrInvalidDecisionRecord", err)
	}
	if _, _, err := store.EvaluationEvidence(t.Context(), run.ID()); !errors.Is(err, ErrStoreNotFound) {
		t.Fatalf("a refused record left evidence behind: %v", err)
	}
}

// assertMigratedEntriesUnavailable: every entry a migration carried forward
// reads back with each operational counter set to "not recorded".
func assertMigratedEntriesUnavailable(t *testing.T, store Store, runID EvaluationRunID) {
	t.Helper()
	_, snapshot, err := store.EvaluationEvidence(context.Background(), runID)
	if err != nil {
		t.Fatalf("EvaluationEvidence() after migration error = %v", err)
	}
	for _, entry := range snapshot.Entries() {
		if want := unavailableOperational(entry.Observations); entry.Operational != want {
			t.Errorf("migrated entry %s = %+v, want every counter unavailable: %+v",
				entry.FingerprintID, entry.Operational, want)
		}
	}
}

// TestSQLiteSchemaV10MigratesToV11MarkingBehaviorsNotRecorded: a behavior
// observed before schema 11 reads back with every operational counter
// unavailable — never zero — and keeps every invariant, and a record ingested
// after the migration folds on top of it.
func TestSQLiteSchemaV10MigratesToV11MarkingBehaviorsNotRecorded(t *testing.T) {
	store, path := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1), BehavioralProfile: run.BehavioralProfile(),
			Operational: OperationalFacts{HTTPStatusCode: 200, Usage: usage(1, 1)},
			Record:      operationalTestRecord(run, "e"+strconv.Itoa(i), "1000", event.StatusOK),
		}); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV10(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v10 database error = %v", err)
	}
	defer migrated.Close()
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	assertMigratedEntriesUnavailable(t, migrated, run.ID())

	// Evidence keeps folding on top of the migrated state.
	after, err := NewControlPlane(migrated, migrated, migrated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID: run.ID(), Sequence: 4, BehavioralProfile: run.BehavioralProfile(),
		Operational: OperationalFacts{HTTPStatusCode: 200, Usage: usage(1, 1)},
		Record:      operationalTestRecord(run, "e3", "1000", event.StatusOK),
	}); err != nil {
		t.Fatalf("ingest after migration: %v", err)
	}
	_, snapshot, err := migrated.EvaluationEvidence(t.Context(), run.ID())
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Entries()[0].Operational
	if got.Durations.Count != 1 || got.Durations.Unobserved != 3 || got.Tokens.Observed != 1 ||
		got.Tokens.Unobserved != 3 || got.HTTPStatus.Success != 1 || got.HTTPStatus.Unavailable != 3 {
		t.Fatalf("summary after a post-migration ingest = %+v", got)
	}
}

// TestPostgresSchemaV10MigratesToV11MarkingBehaviorsNotRecorded is the same
// claim on PostgreSQL. It skips without a database.
func TestPostgresSchemaV10MigratesToV11MarkingBehaviorsNotRecorded(t *testing.T) {
	dsn := isolatedSchemaDSN(t, postgresDSN(t))
	ctx := context.Background()
	store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	seedParents(t, store)
	seedCandidate(t, store)
	run := mustRun(t, "run-1", "cand-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := store.CreateEvaluationRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	started, err := run.Start(run.CreatedAt().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateEvaluationRun(ctx, run, started); err != nil {
		t.Fatal(err)
	}
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := plane.IngestDecisionRecord(ctx, IngestRequest{
			RunID: run.ID(), Sequence: uint64(i + 1), BehavioralProfile: run.BehavioralProfile(),
			Operational: OperationalFacts{HTTPStatusCode: 500, Usage: usage(4, 0)},
			Record:      operationalTestRecord(started, "e"+strconv.Itoa(i), "1000", event.StatusError),
		}); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV10(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: dsn})
	if err != nil {
		t.Fatalf("opening a v10 schema: %v", err)
	}
	defer migrated.Close()
	assertMigratedEntriesUnavailable(t, migrated, run.ID())
}

// TestFreshAndMigratedEntryTablesAgree: the fresh v11 entry table and a v10
// table migrated forward hold the same columns, in the same order.
func TestFreshAndMigratedEntryTablesAgree(t *testing.T) {
	fresh, freshPath := testStore(t)
	freshColumns := sqliteTableColumns(t, fresh.db, tableEntries)
	fresh.Close()

	raw, err := sql.Open("sqlite", freshPath)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV10(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()
	migrated, err := OpenSQLiteStore(t.Context(), filepath.Clean(freshPath))
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if got := sqliteTableColumns(t, migrated.db, tableEntries); strings.Join(got, ",") != strings.Join(freshColumns, ",") {
		t.Fatalf("migrated columns %v, fresh %v", got, freshColumns)
	}
}

// TestOperationalCountsEncodingRoundTripsAndRefusesDamage pins the stored form:
// thirty-one canonical decimals, comma-separated, and nothing else.
func TestOperationalCountsEncodingRoundTripsAndRefusesDamage(t *testing.T) {
	s := OperationalSummary{
		Buckets:    DurationBuckets{0: 1, 10: 2},
		Durations:  DurationSummary{Count: 3, Unobserved: 4, Sum: 30_000_000_000, Min: 5, Max: 20_000_000_000},
		SpanStatus: SpanStatusCounts{Unavailable: 1, Unset: 2, OK: 3, Error: 1},
		HTTPStatus: HTTPStatusClassCounts{Informational: 1, Success: 2, Redirection: 1, ClientError: 2, ServerError: 0, Unavailable: 1},
		HTTP429:    1,
		Tokens:     TokenCounts{Input: math.MaxUint64, Output: 0, Unsplit: 9, Observed: 6, Unobserved: 1},
	}
	encoded := encodeOperationalCounts(s)
	if want := "1,0,0,0,0,0,0,0,0,0,2,4,30000000000,5,20000000000,1,2,3,1,1,2,1,2,0,1,1," +
		"18446744073709551615,0,9,6,1"; encoded != want {
		t.Fatalf("encoded %q, want %q", encoded, want)
	}
	decoded, err := decodeOperationalCounts(encoded)
	if err != nil || decoded != s {
		t.Fatalf("round trip = %+v, %v", decoded, err)
	}
	// It runs for every entry of every evidence load on every ingest.
	if allocs := testing.AllocsPerRun(100, func() { _, _ = decodeOperationalCounts(encoded) }); allocs != 0 {
		t.Errorf("decoding allocates %v times, want 0", allocs)
	}
	if got := encodeOperationalCounts(unavailableOperational(7)); got != "0,0,0,0,0,0,0,0,0,0,0,7,0,0,0,7,0,0,0,0,0,0,0,0,7,0,0,0,0,0,7" {
		t.Fatalf("unavailable encoding %q", got)
	}
	for name, damaged := range map[string]string{
		"empty (the column default)": "",
		"one counter short":          strings.Join(strings.Split(encoded, ",")[1:], ","),
		"one counter extra":          encoded + ",0",
		"a leading zero":             "0" + encoded,
		"a sign":                     "+" + encoded,
		"a space":                    strings.Replace(encoded, ",", ", ", 1),
		"an empty counter":           strings.Replace(encoded, ",0,", ",,", 1),
		"past uint64":                strings.Replace(encoded, "18446744073709551615", "18446744073709551616", 1),
	} {
		if _, err := decodeOperationalCounts(damaged); !errors.Is(err, ErrStoreCorrupt) {
			t.Errorf("%s: error = %v, want ErrStoreCorrupt", name, err)
		}
	}
}

// TestMigrationBackfillMatchesTheUnavailableSummary: the SQL backfill writes
// exactly what unavailableOperational encodes, on a real database.
func TestMigrationBackfillMatchesTheUnavailableSummary(t *testing.T) {
	store, _ := testStore(t)
	run := seedRunningRun(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.IngestDecisionRecord(t.Context(), IngestRequest{
		RunID: run.ID(), Sequence: 1, BehavioralProfile: run.BehavioralProfile(),
		Record: operationalTestRecord(run, "e1", "", event.StatusOK),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), behaviorOperationalBackfillStatement()); err != nil {
		t.Fatal(err)
	}
	var stored, observations string
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT `+columnOperationalCounts+`, observations FROM `+tableEntries).Scan(&stored, &observations); err != nil {
		t.Fatal(err)
	}
	if want := encodeOperationalCounts(unavailableOperational(1)); stored != want || observations != "1" {
		t.Fatalf("backfilled %q (observations %s), want %q", stored, observations, want)
	}
}

// TestDurationSumMustFitItsBuckets: a sum the extrema allow but the bucket
// counts do not is refused. Buckets ≤1ms, ≤5ms and ≤10ms, min 0, max 10 ms:
// the extrema allow up to 20 ms, the buckets at most 16 ms.
func TestDurationSumMustFitItsBuckets(t *testing.T) {
	const ms = 1_000_000
	s := OperationalSummary{
		Buckets:    DurationBuckets{0: 1, 1: 1, 2: 1},
		Durations:  DurationSummary{Count: 3, Sum: 16 * ms, Min: 0, Max: 10 * ms},
		SpanStatus: SpanStatusCounts{OK: 3},
		HTTPStatus: HTTPStatusClassCounts{Unavailable: 3},
		Tokens:     TokenCounts{Unobserved: 3},
	}
	if err := validateOperationalSummary(s, 3); err != nil {
		t.Fatalf("a consistent sum was refused: %v", err)
	}
	s.Durations.Sum = 19 * ms
	if err := validateRestoredOperational(s.Durations, s.SpanStatus, 3); err != nil {
		t.Fatalf("precondition: 084's extrema check must accept 19 ms: %v", err)
	}
	if err := validateOperationalSummary(s, 3); err == nil {
		t.Fatal("a sum the buckets cannot produce was accepted")
	}
}
