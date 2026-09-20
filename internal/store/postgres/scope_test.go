package postgres_test

// Task 051 against PostgreSQL: scope is part of the row's identity, and a
// version-1 database upgrades into the default scope without losing a
// baseline.
//
// The upgrade test builds a genuine version-1 database by executing the old
// DDL by hand rather than by asking the current code to produce one. That is
// the only way the assertion means anything: migrating a schema the current
// build created would prove the current build round-trips itself, which is
// not the question. The question is whether a database written by the
// *previous release* survives.
//
// DSN-gated like every other integration test here; see requireDSN.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/store/postgres"
)

// legacySchemaStatements is the version-1 schema, reproduced verbatim from
// the shape this package created before learning scopes existed: no scope
// column, and a primary key on (actor_id, environment) alone.
//
// One statement per Exec rather than a single semicolon-separated script.
// pgx chooses its wire protocol based on whether a query has arguments, and
// only one of those protocols accepts multiple statements — depending on
// that here would make the fixture's correctness a property of the driver's
// optimizer. Separate calls also name the failing statement.
var legacySchemaStatements = []string{
	`CREATE TABLE ` + postgres.BaselineTable + ` (
		actor_id          text        NOT NULL,
		environment       text        NOT NULL,
		baseline          jsonb       NOT NULL,
		schema_version    integer     NOT NULL,
		fingerprint_count integer     NOT NULL,
		observation_count bigint      NOT NULL,
		last_observed     timestamptz,
		updated_at        timestamptz NOT NULL,
		PRIMARY KEY (actor_id, environment)
	)`,
	`CREATE TABLE ` + postgres.SchemaVersionTable + ` (
		version    integer     NOT NULL PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`,
	`INSERT INTO ` + postgres.SchemaVersionTable + ` (version) VALUES (1)`,
}

// legacyBaselineJSON is a Baseline as version 1 serialized it: the Key has
// ActorID and Environment and no Scope field at all, which is what makes
// "deserializes to the default scope" a property rather than an assumption.
func legacyBaselineJSON(actor, environment, fingerprintID string, count int) string {
	return `{
		"Key": {"ActorID": "` + actor + `", "Environment": "` + environment + `"},
		"Fingerprints": {"` + fingerprintID + `": {"Count": ` + itoa(count) + `,
			"FirstObserved": "2026-01-01T00:00:00Z", "LastObserved": "2026-01-01T02:00:00Z"}},
		"LastObserved": "2026-01-01T02:00:00Z",
		"LastFingerprintID": "` + fingerprintID + `",
		"LastFingerprintTime": "2026-01-01T02:00:00Z",
		"PreviousFingerprintID": "",
		"DelegatorCounts": {"svc-orchestrator": 3}
	}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// seedLegacyDatabase creates a version-1 schema in an isolated PostgreSQL
// schema and populates it, returning the DSN and the rows it wrote.
func seedLegacyDatabase(t *testing.T, dsn string, rows []struct {
	actor, environment, fingerprint string
	count                           int
}) string {
	t.Helper()
	ctx := context.Background()
	iso := isolatedSchemaDSN(t, dsn)

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	for _, stmt := range legacySchemaStatements {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("create version-1 schema: %v\nstatement: %s", err, stmt)
		}
	}
	for _, r := range rows {
		if _, err := conn.Exec(ctx,
			`INSERT INTO `+postgres.BaselineTable+`
				(actor_id, environment, baseline, schema_version, fingerprint_count, observation_count, last_observed, updated_at)
			 VALUES ($1, $2, $3, 1, 1, $4, now(), now())`,
			r.actor, r.environment, legacyBaselineJSON(r.actor, r.environment, r.fingerprint, r.count), r.count,
		); err != nil {
			t.Fatalf("seed legacy row %s/%s: %v", r.actor, r.environment, err)
		}
	}
	return iso
}

// TestMigrateUnscopedDatabaseIntoDefaultScope is the upgrade path: every
// existing baseline keeps its learned state and lands in the default scope,
// and the row's identity becomes scope-aware.
func TestMigrateUnscopedDatabaseIntoDefaultScope(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()

	rows := []struct {
		actor, environment, fingerprint string
		count                           int
	}{
		{"svc-payment", "production", "legacy-fp-1", 7},
		{"svc-payment", "staging", "legacy-fp-2", 3},
		{"agent-deploy", "production", "legacy-fp-3", 11},
	}
	iso := seedLegacyDatabase(t, dsn, rows)

	// Opening the store runs Migrate, which performs the upgrade.
	s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() against a version-1 database error = %v — the upgrade path is broken", err)
	}
	defer s.Close()

	t.Run("every baseline is preserved in the default scope", func(t *testing.T) {
		for _, r := range rows {
			key := baseline.Key{ActorID: r.actor, Environment: r.environment}
			bl, ok := s.Get(ctx, key)
			if !ok {
				t.Errorf("Get(%+v) ok = false — a baseline was lost in the upgrade", key)
				continue
			}
			if got := bl.Fingerprints[r.fingerprint].Count; got != uint64(r.count) {
				t.Errorf("Get(%+v) Count = %d, want %d — learned state changed", key, got, r.count)
			}
			if bl.Key.Scope != "" {
				t.Errorf("Get(%+v) Key.Scope = %q, want empty — a scope was invented", key, bl.Key.Scope)
			}
			if got := bl.DelegatorCounts["svc-orchestrator"]; got != 3 {
				t.Errorf("Get(%+v) DelegatorCounts = %d, want 3", key, got)
			}
		}
	})

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	t.Run("schema metadata records the new version", func(t *testing.T) {
		var count, version int
		if err := conn.QueryRow(ctx, `SELECT count(*), max(version) FROM `+postgres.SchemaVersionTable).Scan(&count, &version); err != nil {
			t.Fatalf("read version table: %v", err)
		}
		if count != 1 || version != postgres.SchemaVersion {
			t.Errorf("version table = %d row(s) at version %d, want 1 row at %d", count, version, postgres.SchemaVersion)
		}
	})

	t.Run("every row is restamped", func(t *testing.T) {
		// scripts/restore-postgres.sh verifies exactly this invariant, so a
		// row left at version 1 would make the next backup fail its own
		// restore check.
		var stale int
		if err := conn.QueryRow(ctx,
			`SELECT count(*) FROM `+postgres.BaselineTable+
				` WHERE schema_version <> (SELECT version FROM `+postgres.SchemaVersionTable+`)`).Scan(&stale); err != nil {
			t.Fatalf("count stale rows: %v", err)
		}
		if stale != 0 {
			t.Errorf("%d row(s) still carry the old schema_version", stale)
		}

		var nonDefault int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+postgres.BaselineTable+` WHERE scope <> ''`).Scan(&nonDefault); err != nil {
			t.Fatalf("count scoped rows: %v", err)
		}
		if nonDefault != 0 {
			t.Errorf("%d migrated row(s) landed outside the default scope", nonDefault)
		}
	})

	t.Run("the primary key includes scope", func(t *testing.T) {
		var cols []string
		if err := conn.QueryRow(ctx, `
			SELECT array_agg(a.attname ORDER BY k.ord)
			  FROM pg_constraint c
			  JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
			  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
			 WHERE c.conrelid = '`+postgres.BaselineTable+`'::regclass AND c.contype = 'p'`).Scan(&cols); err != nil {
			t.Fatalf("read primary key: %v", err)
		}
		want := []string{"scope", "actor_id", "environment"}
		if len(cols) != len(want) {
			t.Fatalf("primary key = %v, want %v", cols, want)
		}
		for i := range want {
			if cols[i] != want[i] {
				t.Fatalf("primary key = %v, want %v — SQL uniqueness must include scope", cols, want)
			}
		}
	})

	t.Run("scoped rows coexist with migrated ones", func(t *testing.T) {
		// The exact collision the old primary key would have caused.
		scoped := baseline.Key{Scope: "scope-a", ActorID: "svc-payment", Environment: "production"}
		if _, _, err := s.Observe(ctx, scoped, testFingerprint(), features.VolatileFeatures{}, testTime); err != nil {
			t.Fatalf("Observe(scoped) error = %v — a new scope collided with a migrated row", err)
		}

		migrated, ok := s.Get(ctx, baseline.Key{ActorID: "svc-payment", Environment: "production"})
		if !ok {
			t.Fatal("the migrated baseline disappeared after a scoped write")
		}
		if got := migrated.Fingerprints["legacy-fp-1"].Count; got != 7 {
			t.Errorf("migrated Count = %d, want 7 — the scoped write reached the default scope", got)
		}
	})
}

// TestMigrateIsIdempotentAcrossRestarts: once upgraded, repeated startups
// must be no-ops. This is the most frequently executed migration path in any
// deployment, and the one an over-eager upgrade step would corrupt.
func TestMigrateIsIdempotentAcrossRestarts(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()

	iso := seedLegacyDatabase(t, dsn, []struct {
		actor, environment, fingerprint string
		count                           int
	}{{"svc-payment", "production", "legacy-fp-1", 7}})

	key := baseline.Key{ActorID: "svc-payment", Environment: "production"}
	for round := range 3 {
		s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
		if err != nil {
			t.Fatalf("round %d NewStore() error = %v", round, err)
		}
		bl, ok := s.Get(ctx, key)
		if !ok {
			t.Fatalf("round %d: the baseline vanished", round)
		}
		if got := bl.Fingerprints["legacy-fp-1"].Count; got != 7 {
			t.Errorf("round %d: Count = %d, want 7 — a repeated migration altered stored data", round, got)
		}
		_ = s.Close()
	}
}

// TestScopedRowsAreIndependent covers the runtime half against a database
// created fresh at version 2: reads and writes must not cross a scope, and
// two scopes over one actor must be two rows.
func TestScopedRowsAreIndependent(t *testing.T) {
	dsn := requireDSN(t)
	ctx := context.Background()
	iso := isolatedSchemaDSN(t, dsn)

	s, err := postgres.NewStore(ctx, postgres.Config{DSN: iso})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	defer s.Close()

	fp := testFingerprint()
	scopeA := baseline.Key{Scope: "scope-a", ActorID: "svc-payment", Environment: "production"}
	scopeB := baseline.Key{Scope: "scope-b", ActorID: "svc-payment", Environment: "production"}
	def := baseline.Key{ActorID: "svc-payment", Environment: "production"}

	const observations = 5
	for i := range observations {
		if _, _, err := s.Observe(ctx, scopeA, fp, features.VolatileFeatures{}, testTime.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Observe(scope-a) error = %v", err)
		}
	}

	for _, key := range []baseline.Key{scopeB, def} {
		if bl, ok := s.Get(ctx, key); ok || len(bl.Fingerprints) != 0 {
			t.Errorf("Get(%+v) found state written under scope-a: reads crossed a scope", key)
		}
	}

	if _, _, err := s.Observe(ctx, scopeB, fp, features.VolatileFeatures{}, testTime); err != nil {
		t.Fatalf("Observe(scope-b) error = %v", err)
	}
	blA, ok := s.Get(ctx, scopeA)
	if !ok {
		t.Fatal("Get(scope-a) ok = false")
	}
	if got := blA.Fingerprints[fp.ID].Count; got != observations {
		t.Errorf("scope-a Count = %d, want %d — a scope-b write reached scope-a", got, observations)
	}
	if blA.Key != scopeA {
		t.Errorf("stored Key = %+v, want %+v — the serialized key must agree with the row it was written under", blA.Key, scopeA)
	}

	conn, err := pgx.Connect(ctx, iso)
	if err != nil {
		t.Fatalf("pgx.Connect() error = %v", err)
	}
	defer conn.Close(ctx)

	var rows int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM `+postgres.BaselineTable+` WHERE actor_id = $1 AND environment = $2`,
		"svc-payment", "production").Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("one actor/environment holds %d row(s), want 2 — scopes are not separate rows", rows)
	}

	// The row's own scope column agrees with the Key inside its jsonb.
	var mismatched int
	if err := conn.QueryRow(ctx,
		`SELECT count(*) FROM `+postgres.BaselineTable+
			` WHERE scope IS DISTINCT FROM coalesce(baseline->'Key'->>'Scope', '')`).Scan(&mismatched); err != nil {
		t.Fatalf("check key agreement: %v", err)
	}
	if mismatched != 0 {
		t.Errorf("%d row(s) have a scope column disagreeing with their serialized Baseline.Key", mismatched)
	}
}
