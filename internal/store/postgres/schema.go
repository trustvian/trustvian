package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SchemaVersion is the database schema version this package reads and
// writes. It is deliberately independent of both the Trustvian release
// version and config.StorageSchemaVersionV1 (the *configuration* schema)
// — the same decoupling every other schema version in this codebase
// already practices, and for the same reason: a release may ship without
// touching the database layout, and a layout change may land without a
// release bump.
//
// Bump this only when the persisted representation changes in a way a
// previous version's loader could misread. `Migrate` refuses to run
// against a database whose recorded version it has no upgrade path for,
// rather than guessing — mirroring store.FileStore's own refusal to read
// an unknown fileSnapshotVersion instead of silently misparsing it.
//
// Version 2 added the `scope` column and made it part of the primary
// key, so one actor in one environment can hold several independent
// learned histories. See
// docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md.
const SchemaVersion = 2

// schemaVersionUnscoped is the pre-learning-scope layout: one row per
// (actor_id, environment), with no scope column. It is the one older
// version Migrate can upgrade from; every other unrecognized version
// still fails closed.
const schemaVersionUnscoped = 1

// Table names, exported because operational inspection through plain
// SQL is an explicit goal of choosing PostgreSQL at all (see
// docs/ROADMAP.md § v0.8 and docs/storage-guide.md § Inspecting state) —
// a tool or runbook querying these should name them from here rather
// than duplicating the literal.
//
// They are compile-time constants, never interpolated from
// caller-supplied data. Every *value* reaches the database as a bound
// parameter (see postgres.go); these two identifiers are the only parts
// of any statement assembled from Go strings. See docs/SECURITY.md §
// Storage configuration and production persistence.
const (
	BaselineTable      = "trustvian_baseline"
	SchemaVersionTable = "trustvian_schema_version"
)

// Unexported aliases keep the SQL literals below readable.
const (
	baselineTable = BaselineTable
	versionTable  = SchemaVersionTable
)

// migrationAdvisoryLockKey serializes concurrent Migrate calls across
// processes. An arbitrary but fixed 64-bit constant, namespaced to this
// project by construction (there is no registry of advisory-lock keys in
// PostgreSQL; collision with another application using the same literal
// would merely serialize two unrelated migrations, never corrupt
// either).
const migrationAdvisoryLockKey int64 = 0x7275737476696E00 // "trustvin\0"

// createBaselineTable stores one row per baseline.Key.
//
// `baseline` (jsonb) is the **authoritative** state: it holds the
// complete, marshalled baseline.Baseline, using the identical
// encoding/json representation store.FileStore already persists. That
// reuse is the point — it makes PostgreSQL and FileStore structurally
// equivalent rather than equivalent by careful hand-matching, and it
// means a future Baseline field is persisted by both automatically,
// with no risk of this backend silently dropping state FileStore keeps.
//
// Every other column is **derived from that jsonb and exists only for
// operator inspectability** (see docs/ROADMAP.md § v0.8's queryability
// rationale): an operator can answer "which actors does Trustvian know
// about, how much has it learned, and when did it last update?" with
// plain SQL and no JSON decoding. They are never read back into a
// Baseline, so they cannot become a second, drifting source of truth.
// Writes recompute them from the same value they write to `baseline`,
// inside the same transaction.
//
// The primary key is the *complete* learned-state identity, scope
// included. Storing scope only inside the jsonb while leaving SQL
// uniqueness on (actor_id, environment) would make the second scope's
// insert collide with the first scope's row, and would leave the
// authoritative row identity disagreeing with the Key inside the value
// it holds. `scope` is ” for the default scope, which is what every
// row written before version 2 already was.
//
// Deliberately NOT normalized into per-fingerprint / per-transition /
// per-delegator tables. Baseline is a bounded, self-contained value
// keyed by {Scope, ActorID, Environment}; splitting its internal maps across
// tables would (a) reshape the domain model to suit SQL, which ADR 0018
// rules out, (b) turn every single-row atomic update into a multi-table
// write needing its own consistency argument, and (c) invite the
// unbounded-history table this milestone explicitly rejects. There is no
// query today that normalization would serve, and jsonb remains
// queryable in SQL if one appears.
const createBaselineTable = `
CREATE TABLE IF NOT EXISTS ` + baselineTable + ` (
	scope             text        NOT NULL,
	actor_id          text        NOT NULL,
	environment       text        NOT NULL,
	baseline          jsonb       NOT NULL,
	schema_version    integer     NOT NULL,
	fingerprint_count integer     NOT NULL,
	observation_count bigint      NOT NULL,
	last_observed     timestamptz,
	updated_at        timestamptz NOT NULL,
	PRIMARY KEY (scope, actor_id, environment)
)`

// unscopedUpgradeSteps rewrites a version-1 table in place, one
// statement at a time so each failure names itself rather than hiding
// inside a multi-statement batch.
//
// Order matters and each step earns its place:
//
//  1. `scope` is added NOT NULL DEFAULT ” so every existing row
//     backfills to the default scope — which is what those baselines
//     already are, semantically. Nothing is invented and no learned
//     state moves between scopes.
//  2. The default is then dropped, so a future insert that forgets
//     scope fails loudly instead of silently landing in the default
//     profile.
//  3. The old primary key is dropped. Its name is read from
//     pg_constraint rather than assumed to be `<table>_pkey`: a table
//     restored from a dump, or created by an older tool, may carry a
//     different name, and guessing would leave the table with two
//     primary keys or none.
//  4. The scoped primary key replaces it.
//
// Restamping each row's derived schema_version is step 5, and lives in
// Migrate because it binds SchemaVersion as a parameter rather than
// interpolating it into a constant. It is not cosmetic:
// scripts/restore-postgres.sh verifies that no row's schema_version
// differs from the recorded version, so leaving old rows at 1 would
// make every post-migration backup fail its own restore check.
//
// All of it runs inside Migrate's existing transaction and advisory
// lock, so it is atomic and safe against a racing process.
var unscopedUpgradeSteps = []struct{ what, sql string }{
	{"add scope column", `ALTER TABLE ` + baselineTable + ` ADD COLUMN IF NOT EXISTS scope text NOT NULL DEFAULT ''`},
	{"drop scope default", `ALTER TABLE ` + baselineTable + ` ALTER COLUMN scope DROP DEFAULT`},
	{"drop unscoped primary key", `
DO $$
DECLARE pk text;
BEGIN
	SELECT conname INTO pk FROM pg_constraint
	 WHERE conrelid = '` + baselineTable + `'::regclass AND contype = 'p';
	IF pk IS NOT NULL THEN
		EXECUTE format('ALTER TABLE ` + baselineTable + ` DROP CONSTRAINT %I', pk);
	END IF;
END $$`},
	{"add scoped primary key", `ALTER TABLE ` + baselineTable + ` ADD PRIMARY KEY (scope, actor_id, environment)`},
}

// createVersionTable holds exactly one row: the schema version this
// database was initialized at. A table rather than a comment or a
// pg_catalog trick so it is obvious to an operator reading the schema,
// and so Migrate can assert against it transactionally.
const createVersionTable = `
CREATE TABLE IF NOT EXISTS ` + versionTable + ` (
	version    integer     NOT NULL PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// ErrSchemaVersionMismatch is returned when the database was
// initialized by a different schema version than this build expects.
// It is deliberately fatal rather than auto-upgrading: silently
// rewriting a layout written by another version is how state gets
// corrupted. A *newer* recorded version is the dangerous direction —
// an older binary must never mutate state whose layout it does not
// understand — and an older recorded version has no upgrade path to
// take while SchemaVersion is still 1.
var ErrSchemaVersionMismatch = errors.New("store/postgres: database schema version mismatch")

// ErrAmbiguousSchemaState reports a database whose schema metadata
// cannot be interpreted with confidence, as distinct from one whose
// version is simply wrong. Two cases produce it, both found by task
// 036's hardening pass rather than reasoned about in advance:
//
//   - The baseline table holds rows but the version table is empty.
//     Before task 036 this was silently treated as a fresh database and
//     stamped with the current SchemaVersion — meaning an operator who
//     restored a partial backup, or ran `DELETE FROM
//     trustvian_schema_version`, could have an older binary adopt state
//     written by a newer one. "No recorded version" is only safe to
//     interpret as "new database" when there is also no data.
//   - The version table holds more than one row. The version column is
//     a primary key, so several *different* versions can coexist, and
//     reading one with `LIMIT 1` and no ordering picked arbitrarily
//     between them — a database marked version 99 was observed being
//     accepted because a leftover version 1 row was read instead.
//
// Both fail closed. Recovery is an operator decision (restore a
// consistent backup, or set the version table to the single correct
// value); guessing on Trustvian's side risks corrupting learned state,
// which is exactly what a version check exists to prevent.
var ErrAmbiguousSchemaState = errors.New("store/postgres: ambiguous schema metadata")

// Migrate brings an empty database up to SchemaVersion and verifies an
// already-initialized one matches. It is:
//
//   - idempotent — running it repeatedly is a no-op after the first;
//   - transactional — the DDL and the version row commit together, so a
//     failure never leaves tables without a recorded version;
//   - safe under concurrent startup — a transaction-scoped advisory lock
//     serializes racing processes, and the second one observes the first
//     one's committed version rather than re-creating anything;
//   - fail-closed — an unrecognized recorded version aborts with
//     ErrSchemaVersionMismatch instead of being upgraded or ignored, and
//     metadata that cannot be interpreted at all aborts with
//     ErrAmbiguousSchemaState rather than being guessed at. "Absent
//     version" counts as fresh only when the baseline table is also
//     empty; see ErrAmbiguousSchemaState for why that distinction
//     matters.
//
// Automatic initialization is chosen over requiring an operator to run
// DDL by hand because it is the smallest safe OSS experience: one
// connection string and the store works. The cost is that the runtime
// role needs table-creation rights on first run — documented in
// docs/storage-guide.md, along with how to split migration from runtime
// privileges for deployments that care.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store/postgres: begin migration: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	// Held until this transaction ends, so two processes starting
	// simultaneously cannot both run the DDL-and-insert sequence.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationAdvisoryLockKey); err != nil {
		return fmt.Errorf("store/postgres: acquire migration lock: %w", err)
	}

	if _, err := tx.Exec(ctx, createVersionTable); err != nil {
		return fmt.Errorf("store/postgres: create version table: %w", err)
	}
	if _, err := tx.Exec(ctx, createBaselineTable); err != nil {
		return fmt.Errorf("store/postgres: create baseline table: %w", err)
	}

	// Count first, rather than reading one row and inferring from
	// pgx.ErrNoRows. The count distinguishes the three states that
	// matter — none, exactly one, more than one — where a `LIMIT 1` read
	// collapses "none" and "several" into "whatever came back", which is
	// how both ErrAmbiguousSchemaState cases used to slip through.
	var versionRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+versionTable).Scan(&versionRows); err != nil {
		return fmt.Errorf("store/postgres: read schema version: %w", err)
	}

	switch {
	case versionRows > 1:
		return fmt.Errorf("%w: %s holds %d rows, expected exactly 1 — cannot determine the database's schema version",
			ErrAmbiguousSchemaState, versionTable, versionRows)

	case versionRows == 0:
		// No recorded version. Safe to treat as a fresh database *only* if
		// it is genuinely empty — otherwise this is data of unknown
		// provenance and stamping it would be a silent adoption.
		var baselineRows int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+baselineTable).Scan(&baselineRows); err != nil {
			return fmt.Errorf("store/postgres: count existing baselines: %w", err)
		}
		if baselineRows > 0 {
			return fmt.Errorf("%w: %s holds %d baseline row(s) but %s is empty — refusing to assume this data matches schema version %d",
				ErrAmbiguousSchemaState, baselineTable, baselineRows, versionTable, SchemaVersion)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO `+versionTable+` (version) VALUES ($1)`, SchemaVersion); err != nil {
			return fmt.Errorf("store/postgres: record schema version: %w", err)
		}

	default:
		var recorded int
		if err := tx.QueryRow(ctx, `SELECT version FROM `+versionTable).Scan(&recorded); err != nil {
			return fmt.Errorf("store/postgres: read schema version: %w", err)
		}
		switch recorded {
		case SchemaVersion:
			// Already current.
		case schemaVersionUnscoped:
			if err := upgradeUnscoped(ctx, tx); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: database is at version %d, this build expects %d", ErrSchemaVersionMismatch, recorded, SchemaVersion)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store/postgres: commit migration: %w", err)
	}
	return nil
}

// upgradeUnscoped performs the version 1 -> 2 upgrade: give every
// existing baseline the default scope and make scope part of the row's
// identity.
//
// It runs inside Migrate's transaction and advisory lock, so it is
// atomic — a failure at any step rolls the whole thing back, leaving a
// version-1 database untouched rather than half-converted — and a
// second process starting concurrently blocks, then observes the
// committed result instead of repeating the work.
//
// No learned state is read, rewritten, or discarded. Every existing row
// keeps its jsonb exactly as it was; the Baseline inside it has a Key
// with no Scope field, which deserializes to the default scope and so
// already agrees with the column the upgrade backfills.
func upgradeUnscoped(ctx context.Context, tx pgx.Tx) error {
	for _, step := range unscopedUpgradeSteps {
		if _, err := tx.Exec(ctx, step.sql); err != nil {
			return fmt.Errorf("store/postgres: upgrade %d -> %d: %s: %w", schemaVersionUnscoped, SchemaVersion, step.what, err)
		}
	}

	// Derived column, recomputed rather than interpolated — see
	// unscopedUpgradeSteps.
	if _, err := tx.Exec(ctx,
		`UPDATE `+baselineTable+` SET schema_version = $1 WHERE schema_version <> $1`, SchemaVersion,
	); err != nil {
		return fmt.Errorf("store/postgres: upgrade %d -> %d: restamp schema_version: %w", schemaVersionUnscoped, SchemaVersion, err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE `+versionTable+` SET version = $1, applied_at = now() WHERE version = $2`,
		SchemaVersion, schemaVersionUnscoped,
	); err != nil {
		return fmt.Errorf("store/postgres: upgrade %d -> %d: record new version: %w", schemaVersionUnscoped, SchemaVersion, err)
	}
	return nil
}
