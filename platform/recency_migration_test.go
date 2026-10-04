package platform

// Task 101: schema v10's recency keys, migrated and backfilled.

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// downgradeToV9 turns a current database into the v9 shape: v10's indexes and
// columns gone, version 9 stamped. exec runs one statement against it.
func downgradeToV9(t testing.TB, exec func(string) error) {
	t.Helper()
	for _, index := range []string{indexRunsRecentByCandidate, indexRunsRecent, indexScenarioRecent} {
		if err := exec(`DROP INDEX ` + index); err != nil {
			t.Fatalf("drop v10 index %s: %v", index, err)
		}
	}
	for _, column := range []struct{ table, name string }{
		{tableRuns, "created_order"}, {tableScenarioExecutions, "started_order"},
	} {
		if err := exec(`ALTER TABLE ` + column.table + ` DROP COLUMN ` + column.name); err != nil {
			t.Fatalf("drop v10 column %s.%s: %v", column.table, column.name, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 9 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v9: %v", err)
	}
}

// TestRecencyKeyIsFixedWidthAndOrdered pins the encoding the whole collection
// rests on: byte order is time order, sub-second precision survives, and a time
// that cannot be keyed is refused rather than given a key that sorts wrongly.
func TestRecencyKeyIsFixedWidthAndOrdered(t *testing.T) {
	base := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	times := []time.Time{
		base,
		base.Add(time.Nanosecond),
		base.Add(9 * time.Nanosecond),
		base.Add(10 * time.Nanosecond),
		base.Add(time.Second),
		base.In(time.FixedZone("UTC+3", 3*3600)).Add(2 * time.Second), // offset is irrelevant
	}
	var previous string
	for i, at := range times {
		key, err := recencyKey(at)
		if err != nil {
			t.Fatalf("recencyKey(%v) error = %v", at, err)
		}
		if len(key) != recencyKeyDigits || !validRecencyKey(key) {
			t.Errorf("key %q is not %d digits", key, recencyKeyDigits)
		}
		if i > 0 && key <= previous {
			t.Errorf("key for %v (%s) does not sort after %s", at, key, previous)
		}
		previous = key
	}
	for _, bad := range []time.Time{
		{},
		time.Date(1969, 12, 31, 23, 59, 59, 0, time.UTC),
		time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC),
		// Past the int64-nanosecond range, where UnixNano wraps rather than fails.
		time.Date(2262, 4, 12, 0, 0, 0, 0, time.UTC),
		time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		if _, err := recencyKey(bad); !errors.Is(err, ErrInvalidID) {
			t.Errorf("recencyKey(%v) error = %v, want ErrInvalidID", bad, err)
		}
	}
}

func TestRecencyCursorRoundTripsAndRefusesWhatItDidNotWrite(t *testing.T) {
	for _, id := range []string{"run-1", "run.with.dots", "a", "ünïcode"} {
		cursor := RecencyCursor{Key: "01759568525123456789", ID: id}
		parsed, err := ParseRecencyCursor(FormatRecencyCursor(cursor))
		if err != nil || parsed != cursor {
			t.Errorf("round trip of %+v = %+v, %v", cursor, parsed, err)
		}
	}
	if c, err := ParseRecencyCursor(""); err != nil || !c.IsZero() {
		t.Errorf("empty cursor = %+v, %v; want the first page", c, err)
	}
	for _, bad := range []string{
		"01759568525123456789",       // no identifier
		"01759568525123456789.",      // empty identifier
		"0175956852512345678.run-1",  // key too short
		"0175956852512345678x.run-1", // key not digits
		"01759568525123456789-run-1", // wrong separator
		"run-1",
		"01759568525123456789. run-1", // identifier with leading space
	} {
		if _, err := ParseRecencyCursor(bad); !errors.Is(err, ErrObservationCursor) {
			t.Errorf("ParseRecencyCursor(%q) error = %v, want ErrObservationCursor", bad, err)
		}
	}
}

// TestSQLiteSchemaV9MigratesToV10BackfillingKeys: every existing run and
// execution gains the key its stored time implies, and the collection reads
// them newest first straight after the migration.
func TestSQLiteSchemaV9MigratesToV10BackfillingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v9.db")
	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	seedParents(t, store)
	seedCandidate(t, store)
	older := mustRun(t, "run-older", "cand-1", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := mustRun(t, "run-newer", "cand-1", time.Date(2026, 1, 1, 0, 0, 0, 500, time.UTC))
	for _, run := range []EvaluationRun{older, newer} {
		if err := store.CreateEvaluationRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
	}
	scope := ScenarioScope{ProjectID: "proj-1", AgentID: "agent-1", Environment: "staging"}
	if err := store.CreateScenarioExecution(t.Context(),
		newRunningExecution(t, "exec-1", "support", scope, 2, "", scenarioEpoch)); err != nil {
		t.Fatal(err)
	}
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV9(t, func(statement string) error {
		_, err := raw.ExecContext(context.Background(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v9 database error = %v", err)
	}
	defer migrated.Close()
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}

	var runKey, execKey string
	if err := migrated.db.QueryRowContext(t.Context(),
		`SELECT created_order FROM `+tableRuns+` WHERE id = 'run-newer'`).Scan(&runKey); err != nil {
		t.Fatal(err)
	}
	wantRun, _ := recencyKey(newer.CreatedAt())
	if runKey != wantRun {
		t.Errorf("backfilled run key = %q, want %q", runKey, wantRun)
	}
	if err := migrated.db.QueryRowContext(t.Context(),
		`SELECT started_order FROM `+tableScenarioExecutions+` WHERE id = 'exec-1'`).Scan(&execKey); err != nil {
		t.Fatal(err)
	}
	wantExec, _ := recencyKey(scenarioEpoch)
	if execKey != wantExec {
		t.Errorf("backfilled execution key = %q, want %q", execKey, wantExec)
	}

	recent, err := migrated.RecentEvaluationRuns(t.Context(), RunScope{ProjectID: "proj-1"}, RecencyCursor{}, MaxListPage)
	if err != nil {
		t.Fatalf("RecentEvaluationRuns() after migration error = %v", err)
	}
	if len(recent) != 2 || recent[0].Run.ID() != "run-newer" || recent[1].Run.ID() != "run-older" {
		t.Errorf("recent runs after migration = %v, want [run-newer run-older]", recentIDs(recent))
	}
}

func recentIDs(runs []RecentRun) []EvaluationRunID {
	out := make([]EvaluationRunID, 0, len(runs))
	for _, r := range runs {
		out = append(out, r.Run.ID())
	}
	return out
}

// recencyFixture is two projects, three agents and four candidates, with runs
// whose creation times interleave across them and repeat exactly, so a scope
// predicate, the order and the tie-break are all visible.
//
//	proj-1 / agent-1 / cand-1: r-a (t0), r-c (t2), r-tie-2 (t3), r-tie-1 (t3)
//	proj-1 / agent-1 / cand-2: r-b (t1)
//	proj-1 / agent-2 / cand-3: r-d (t4)
//	proj-2 / agent-x / cand-x: r-x (t5)
func recencyFixture(t *testing.T, store Store) time.Time {
	t.Helper()
	ctx := context.Background()
	seedParents(t, store)
	seedCandidate(t, store)
	for _, step := range []func() error{
		func() error {
			return store.CreateCandidate(ctx, mustCandidate(t, "cand-2", "agent-1", CandidateMetadata{Label: "v2"}))
		},
		func() error { return store.CreateAgent(ctx, mustAgent(t, "agent-2", "proj-1", "Second agent")) },
		func() error {
			return store.CreateCandidate(ctx, mustCandidate(t, "cand-3", "agent-2", CandidateMetadata{}))
		},
		func() error { return store.CreateProject(ctx, mustProject(t, "proj-2", "Other")) },
		func() error { return store.CreateAgent(ctx, mustAgent(t, "agent-x", "proj-2", "Other agent")) },
		func() error {
			return store.CreateCandidate(ctx, mustCandidate(t, "cand-x", "agent-x", CandidateMetadata{}))
		},
	} {
		if err := step(); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	t0 := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		id        EvaluationRunID
		candidate CandidateID
		offset    time.Duration
	}{
		{"r-a", "cand-1", 0},
		{"r-b", "cand-2", time.Nanosecond},
		{"r-c", "cand-1", 2 * time.Nanosecond},
		{"r-tie-1", "cand-1", 3 * time.Nanosecond},
		{"r-tie-2", "cand-1", 3 * time.Nanosecond},
		{"r-d", "cand-3", 4 * time.Nanosecond},
		{"r-x", "cand-x", 5 * time.Nanosecond},
	} {
		if err := store.CreateEvaluationRun(ctx, mustRun(t, r.id, r.candidate, t0.Add(r.offset))); err != nil {
			t.Fatalf("seed run %s: %v", r.id, err)
		}
	}
	return t0
}

// readRecent pages a scope to its end at `limit` per page.
func readRecent(t *testing.T, store Store, scope RunScope, after RecencyCursor, limit int) []EvaluationRunID {
	t.Helper()
	var seen []EvaluationRunID
	for range 64 {
		page, err := store.RecentEvaluationRuns(t.Context(), scope, after, limit)
		if err != nil {
			t.Fatalf("RecentEvaluationRuns(%+v, %+v) error = %v", scope, after, err)
		}
		if len(page) == 0 {
			return seen
		}
		seen = append(seen, recentIDs(page)...)
		last := page[len(page)-1]
		after = RecencyCursor{Key: last.Key, ID: string(last.Run.ID())}
	}
	t.Fatal("RecentEvaluationRuns never reached an empty page")
	return nil
}

func conformRecentRuns(t *testing.T, open func(testing.TB) Store) {
	store := open(t)
	t0 := recencyFixture(t, store)

	cases := []struct {
		name  string
		scope RunScope
		want  string
	}{
		// Newest first; the two runs created in the same nanosecond are
		// ordered by identifier, descending, on every backend.
		{"project", RunScope{ProjectID: "proj-1"}, "r-d,r-tie-2,r-tie-1,r-c,r-b,r-a"},
		{"agent", RunScope{ProjectID: "proj-1", AgentID: "agent-1"}, "r-tie-2,r-tie-1,r-c,r-b,r-a"},
		{"candidate", RunScope{ProjectID: "proj-1", AgentID: "agent-1", CandidateID: "cand-1"}, "r-tie-2,r-tie-1,r-c,r-a"},
		{"other project", RunScope{ProjectID: "proj-2"}, "r-x"},
	}
	for _, tc := range cases {
		for _, limit := range []int{1, 2, 3, MaxListPage} {
			got := readRecent(t, store, tc.scope, RecencyCursor{}, limit)
			if joinIDs(got) != tc.want {
				t.Errorf("%s at limit %d = %s, want %s", tc.name, limit, joinIDs(got), tc.want)
			}
		}
	}

	// The cursor is exclusive on both components: a boundary between the two
	// equal keys neither repeats nor skips the second.
	tieKey, _ := recencyKey(t0.Add(3 * time.Nanosecond))
	after := readRecent(t, store, RunScope{ProjectID: "proj-1"}, RecencyCursor{Key: tieKey, ID: "r-tie-2"}, 2)
	if joinIDs(after) != "r-tie-1,r-c,r-b,r-a" {
		t.Errorf("after the first of two equal keys = %s, want r-tie-1,r-c,r-b,r-a", joinIDs(after))
	}

	// A run created mid-traversal is newer than the reader's position and
	// does not appear behind it; a fresh read from the top shows it first.
	first, err := store.RecentEvaluationRuns(t.Context(), RunScope{ProjectID: "proj-1"}, RecencyCursor{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateEvaluationRun(context.Background(), mustRun(t, "r-late", "cand-2", t0.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	last := first[len(first)-1]
	rest := readRecent(t, store, RunScope{ProjectID: "proj-1"}, RecencyCursor{Key: last.Key, ID: string(last.Run.ID())}, 2)
	if joinIDs(rest) != "r-tie-1,r-c,r-b,r-a" {
		t.Errorf("continuation after a concurrent insert = %s, want r-tie-1,r-c,r-b,r-a", joinIDs(rest))
	}
	if top := readRecent(t, store, RunScope{ProjectID: "proj-1"}, RecencyCursor{}, 1); top[0] != "r-late" {
		t.Errorf("a fresh read starts with %s, want r-late", top[0])
	}

	// Each listed run is the full, validated run.
	page, err := store.RecentEvaluationRuns(t.Context(), RunScope{CandidateID: "cand-2"}, RecencyCursor{}, MaxListPage)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range page {
		if r.Run.CandidateID() != "cand-2" || r.Run.Environment() != "staging" || r.Run.CreatedAt().IsZero() {
			t.Errorf("listed run %s lost fields: %+v", r.Run.ID(), r.Run)
		}
	}
}

func joinIDs(ids []EvaluationRunID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, string(id))
	}
	return strings.Join(parts, ",")
}

// TestRecentRunScopeIsCheckedBeforeAnythingIsRead is the control plane's half:
// a narrowing that does not belong to the scope above it is refused, a missing
// one is not found, and a bad cursor or limit is invalid — never a widened or
// empty answer.
func TestRecentRunScopeIsCheckedBeforeAnythingIsRead(t *testing.T) {
	store, _ := testStore(t)
	recencyFixture(t, store)
	plane, err := NewControlPlane(store, store, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		scope RunScope
		after string
		limit int
		want  error
	}{
		{"no project", RunScope{}, "", 1, ErrInvalidID},
		{"missing project", RunScope{ProjectID: "nope"}, "", 1, ErrStoreNotFound},
		{"missing agent", RunScope{ProjectID: "proj-1", AgentID: "nope"}, "", 1, ErrStoreNotFound},
		{"missing candidate", RunScope{ProjectID: "proj-1", CandidateID: "nope"}, "", 1, ErrStoreNotFound},
		{"agent of another project", RunScope{ProjectID: "proj-1", AgentID: "agent-x"}, "", 1, ErrRunScope},
		{"candidate of another agent", RunScope{ProjectID: "proj-1", AgentID: "agent-2", CandidateID: "cand-1"}, "", 1, ErrRunScope},
		{"candidate of another project", RunScope{ProjectID: "proj-1", CandidateID: "cand-x"}, "", 1, ErrRunScope},
		{"zero limit", RunScope{ProjectID: "proj-1"}, "", 0, ErrInvalidID},
		{"limit above a page", RunScope{ProjectID: "proj-1"}, "", MaxListPage + 1, ErrInvalidID},
		{"malformed cursor", RunScope{ProjectID: "proj-1"}, "run-1", 1, ErrObservationCursor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := plane.RecentEvaluationRuns(t.Context(), tc.scope, tc.after, tc.limit); !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
	got, err := plane.RecentEvaluationRuns(t.Context(), RunScope{ProjectID: "proj-1", CandidateID: "cand-3"}, "", MaxListPage)
	if err != nil || len(got) != 1 || got[0].Run.ID() != "r-d" {
		t.Errorf("a candidate named without its agent = %v, %v; want [r-d]", recentIDs(got), err)
	}
}

// TestPostgresSchemaV9MigratesToV10BackfillingKeys is the PostgreSQL half:
// v9 and v10 hold the same tables, so only the stamp routes the migration,
// and the backfill must give the same keys SQLite's does.
func TestPostgresSchemaV9MigratesToV10BackfillingKeys(t *testing.T) {
	dsn := strings.TrimSpace(conformancePostgresDSN())
	if dsn == "" {
		t.Skipf("%s is not set; PostgreSQL migration needs a database", postgresDSNEnv)
	}
	isolated := isolatedSchemaDSN(t, dsn)
	ctx := context.Background()

	store, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() error = %v", err)
	}
	t0 := recencyFixture(t, store)
	store.Close()

	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		t.Fatal(err)
	}
	downgradeToV9(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() on a v9 schema error = %v", err)
	}
	defer migrated.Close()
	got := readRecent(t, migrated, RunScope{ProjectID: "proj-1"}, RecencyCursor{}, 2)
	if joinIDs(got) != "r-d,r-tie-2,r-tie-1,r-c,r-b,r-a" {
		t.Errorf("recent runs after migration = %s", joinIDs(got))
	}
	page, err := migrated.RecentEvaluationRuns(ctx, RunScope{CandidateID: "cand-3"}, RecencyCursor{}, 1)
	if err != nil || len(page) != 1 {
		t.Fatalf("RecentEvaluationRuns() = %v, %v", page, err)
	}
	if want, _ := recencyKey(t0.Add(4 * time.Nanosecond)); page[0].Key != want {
		t.Errorf("backfilled key = %q, want %q", page[0].Key, want)
	}
}
