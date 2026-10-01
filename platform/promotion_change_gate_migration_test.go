package platform

// Schema v8, issue 131: the optional counted-change check on the promotion
// history, upgraded from v7 and round-tripped across a restart, on both
// backends.
//
// The v7 fixture is built from a real v7-shaped promotion rather than from a
// hand-written INSERT: promotions are written by today's store, then the six
// v8 columns are dropped and the stamp set back to 7. What remains is exactly
// a v7 table holding rows a v7 build could have written, which is what the
// migration has to be honest about.

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// historicalPromotions are the decisions a v7 database holds: one accepted
// and one rejected, so the migration is seen to leave both verdicts alone.
func historicalPromotions(t testing.TB, store Store) (accepted, rejected Promotion) {
	t.Helper()
	seedPromotionFixture(t, store, "proj-1")
	accepted = promotionWithChangeGate(t, store, "proj-1", "promo-old-pass",
		ChangeCountGate{State: GateCheckNotEvaluated}, GateVerdictPass)
	rejected = promotionWithChangeGate(t, store, "proj-1", "promo-old-fail",
		ChangeCountGate{State: GateCheckNotEvaluated}, GateVerdictFail)
	for _, p := range []Promotion{accepted, rejected} {
		if err := store.CreatePromotion(context.Background(), p); err != nil {
			t.Fatalf("CreatePromotion(%s) error = %v", p.ID(), err)
		}
	}
	return accepted, rejected
}

// downgradeToV7 turns a current database into the v7 shape: v8's columns
// gone, version 7 stamped. exec runs one statement against it.
func downgradeToV7(t testing.TB, exec func(string) error) {
	t.Helper()
	downgradeToV8(t, exec)
	for _, column := range promotionChangeGateColumnNames() {
		if err := exec(`ALTER TABLE ` + tablePromotions + ` DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop v8 column %s: %v", column, err)
		}
	}
	if err := exec(`UPDATE ` + tableSchemaVersion + ` SET version = 7 WHERE id = 1`); err != nil {
		t.Fatalf("stamp v7: %v", err)
	}
}

// evaluatedChangeGate is a counted-change check a v8 build records.
func evaluatedChangeGate(actual, maximum uint64, correlation CorrelationState) ChangeCountGate {
	return ChangeCountGate{
		State: GateCheckEvaluated, Actual: actual, Maximum: maximum,
		Passed:                actual <= maximum,
		CorrelationState:      correlation,
		CountingPolicyVersion: CountingPolicyVersion,
	}
}

// assertHistoricalRowsMigrated is the whole claim about existing decisions:
// every field they had reads back unchanged, the check reads not_recorded
// with no outcome, and the optional limit reads absent — never a zero.
func assertHistoricalRowsMigrated(t *testing.T, store Store, originals ...Promotion) {
	t.Helper()
	for _, original := range originals {
		stored, err := store.Promotion(context.Background(), original.ID())
		if err != nil {
			t.Fatalf("Promotion(%s) after migration error = %v", original.ID(), err)
		}
		changes := stored.GateResult().AddedBehaviorChanges()
		if changes != (ChangeCountGate{State: GateCheckNotRecorded}) {
			t.Errorf("%s: check = %+v, want not_recorded with no outcome; the "+
				"migration must not invent one", original.ID(), changes)
		}
		if stored.GateLimits().MaxAddedBehaviorChanges.IsSet() {
			t.Errorf("%s: a historical promotion reads back a counted-change limit; "+
				"nobody could have supplied one", original.ID())
		}
		if stored.GateResult().Verdict() != original.GateResult().Verdict() ||
			stored.Outcome() != original.Outcome() {
			t.Errorf("%s: verdict/outcome %s/%s, want %s/%s unchanged", original.ID(),
				stored.GateResult().Verdict(), stored.Outcome(),
				original.GateResult().Verdict(), original.Outcome())
		}
		// Every other field, exactly.
		if stored.GateResult().AddedBehaviors() != original.GateResult().AddedBehaviors() ||
			stored.GateResult().BlockDecisions() != original.GateResult().BlockDecisions() ||
			stored.GateResult().CriticalRiskObservations() !=
				original.GateResult().CriticalRiskObservations() ||
			stored.GateResult().ReferenceEvidence() != original.GateResult().ReferenceEvidence() ||
			stored.GateResult().CandidateEvidence() != original.GateResult().CandidateEvidence() ||
			stored.Source() != original.Source() || stored.Target() != original.Target() ||
			!stored.DecidedAt().Equal(original.DecidedAt()) {
			t.Errorf("%s: a pre-existing field changed across the migration", original.ID())
		}
		limits := stored.GateLimits()
		limits.MaxAddedBehaviorChanges = OptionalGateLimit{}
		if limits != original.GateLimits() {
			t.Errorf("%s: limits = %+v, want %+v", original.ID(), limits, original.GateLimits())
		}
	}
}

// assertEvaluatedRoundTrip writes decisions under v8 and reads them back, so
// the same test covers both "a migrated database accepts the new check" and,
// after a reopen, "a restart changes nothing".
func writeEvaluatedPromotions(t *testing.T, store Store) []Promotion {
	t.Helper()
	written := []Promotion{
		promotionWithChangeGate(t, store, "proj-1", "promo-new-absent",
			ChangeCountGate{State: GateCheckNotEvaluated}, GateVerdictPass),
		promotionWithChangeGate(t, store, "proj-1", "promo-new-zero",
			evaluatedChangeGate(0, 0, CorrelationComplete), GateVerdictPass),
		promotionWithChangeGate(t, store, "proj-1", "promo-new-over",
			evaluatedChangeGate(3, 2, CorrelationPartial), GateVerdictFail),
		promotionWithChangeGate(t, store, "proj-1", "promo-new-max",
			evaluatedChangeGate(7, math.MaxUint64, CorrelationUnavailable), GateVerdictPass),
	}
	for _, p := range written {
		if err := store.CreatePromotion(context.Background(), p); err != nil {
			t.Fatalf("CreatePromotion(%s) error = %v", p.ID(), err)
		}
	}
	return written
}

func assertPromotionsVerbatim(t *testing.T, store Store, want []Promotion) {
	t.Helper()
	for _, p := range want {
		stored, err := store.Promotion(context.Background(), p.ID())
		if err != nil {
			t.Fatalf("Promotion(%s) error = %v", p.ID(), err)
		}
		if stored.GateResult() != p.GateResult() {
			t.Errorf("%s: gate result = %+v, want %+v verbatim", p.ID(),
				stored.GateResult(), p.GateResult())
		}
		if stored.GateLimits() != p.GateLimits() || stored.Outcome() != p.Outcome() {
			t.Errorf("%s: limits/outcome = %+v/%s, want %+v/%s", p.ID(),
				stored.GateLimits(), stored.Outcome(), p.GateLimits(), p.Outcome())
		}
	}
}

func TestSQLiteSchemaV7PromotionsMigrateWithoutInventedCheckOutcomes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")

	store, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() error = %v", err)
	}
	accepted, rejected := historicalPromotions(t, store)
	store.Close()

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	downgradeToV7(t, func(statement string) error {
		_, err := raw.ExecContext(t.Context(), statement)
		return err
	})
	raw.Close()

	migrated, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("OpenSQLiteStore() on a v7 database error = %v", err)
	}
	if version, _ := migrated.storedSchemaVersion(t.Context()); version != SchemaVersion {
		t.Fatalf("version after migration = %d, want %d", version, SchemaVersion)
	}
	assertHistoricalRowsMigrated(t, migrated, accepted, rejected)
	written := writeEvaluatedPromotions(t, migrated)
	migrated.Close()

	// A restart: the same file through a new store.
	reopened, err := OpenSQLiteStore(t.Context(), path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	assertHistoricalRowsMigrated(t, reopened, accepted, rejected)
	assertPromotionsVerbatim(t, reopened, written)
}

func TestPostgresSchemaV7PromotionsMigrateWithoutInventedCheckOutcomes(t *testing.T) {
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
	accepted, rejected := historicalPromotions(t, store)
	store.Close()

	pool, err := pgxpool.New(ctx, isolated)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	downgradeToV7(t, func(statement string) error {
		_, err := pool.Exec(ctx, statement)
		return err
	})
	pool.Close()

	migrated, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("OpenPostgresStore() on a v7 schema error = %v", err)
	}
	assertHistoricalRowsMigrated(t, migrated, accepted, rejected)
	written := writeEvaluatedPromotions(t, migrated)
	migrated.Close()

	reopened, err := OpenPostgresStore(ctx, PostgresConfig{DSN: isolated})
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer reopened.Close()
	assertHistoricalRowsMigrated(t, reopened, accepted, rejected)
	assertPromotionsVerbatim(t, reopened, written)
}

// A row whose shape contradicts its state is corrupt, not reinterpreted: an
// outcome beside a check that did not run, or an evaluated check missing its
// evidence, is a row this code did not write.
func TestRestoredChangeGateRefusesContradictoryShapes(t *testing.T) {
	text := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	flag := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }

	evaluated := func() promotionRow {
		row := storedGateRow()
		row.changesState = string(GateCheckEvaluated)
		row.changesMaximum, row.changesActual = text("2"), text("1")
		row.changesPassed = flag(1)
		row.changesCorrelation, row.changesPolicy = text("complete"), text("1")
		return row
	}
	if _, err := restorePromotionRow(evaluated()); err != nil {
		t.Fatalf("a well-formed evaluated row was refused: %v", err)
	}

	cases := map[string]func(*promotionRow){
		"unknown state":                func(r *promotionRow) { r.changesState = "maybe" },
		"empty state":                  func(r *promotionRow) { r.changesState = "" },
		"not evaluated with threshold": func(r *promotionRow) { *r = storedGateRow(); r.changesMaximum = text("0") },
		"not recorded with outcome": func(r *promotionRow) {
			*r = storedGateRow()
			r.changesState = string(GateCheckNotRecorded)
			r.changesPassed = flag(1)
		},
		"evaluated missing threshold":   func(r *promotionRow) { r.changesMaximum = sql.NullString{} },
		"evaluated missing flag":        func(r *promotionRow) { r.changesPassed = sql.NullInt64{} },
		"evaluated missing correlation": func(r *promotionRow) { r.changesCorrelation = sql.NullString{} },
		"evaluated unknown correlation": func(r *promotionRow) { r.changesCorrelation = text("mostly") },
		"evaluated empty policy":        func(r *promotionRow) { r.changesPolicy = text("") },
		"non-canonical threshold":       func(r *promotionRow) { r.changesMaximum = text("02") },
		"flag out of range":             func(r *promotionRow) { r.changesPassed = flag(2) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			row := evaluated()
			mutate(&row)
			if _, err := restorePromotionRow(row); err == nil {
				t.Errorf("restorePromotionRow accepted a contradictory counted-change row")
			}
		})
	}
}

// The stored flag is taken as written, never recomputed: a row recording
// actual 5, maximum 2, passed true comes back exactly so. The record is what
// the platform relied on, which restoreEvaluationGateResult already promises
// for the other five checks.
func TestRestoredChangeGateFlagIsNotRecomputed(t *testing.T) {
	row := storedGateRow()
	row.changesState = string(GateCheckEvaluated)
	row.changesMaximum = sql.NullString{String: "2", Valid: true}
	row.changesActual = sql.NullString{String: "5", Valid: true}
	row.changesPassed = sql.NullInt64{Int64: 1, Valid: true}
	row.changesCorrelation = sql.NullString{String: "complete", Valid: true}
	row.changesPolicy = sql.NullString{String: "1", Valid: true}

	promotion, err := restorePromotionRow(row)
	if err != nil {
		t.Fatalf("restorePromotionRow() error = %v", err)
	}
	got := promotion.GateResult().AddedBehaviorChanges()
	if !got.Passed || got.Actual != 5 || got.Maximum != 2 {
		t.Errorf("check = %+v, want the stored actual 5, maximum 2, passed true verbatim", got)
	}
}
