package platform

// The PostgreSQL physical schema and its lifecycle.
//
// One logical schema — `SchemaVersion`, shared with SQLite — expressed in a
// second dialect. The types are chosen to preserve observable behaviour rather
// than to look idiomatic, and each departure from the obvious PostgreSQL type
// has a reason recorded where it is made.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// postgresSchemaStatements is the whole schema.
//
// Three representation decisions carry over from SQLite because they are about
// the data rather than the engine, and one is new.
//
// Counters are TEXT. PostgreSQL BIGINT is signed 64-bit — the identical trap
// SQLite's INTEGER has — so a counter above MaxInt64 would be corrupted
// silently, and only for large values. NUMERIC would hold the range but
// normalizes: '007' reads back as 7, which destroys the corruption signal
// parseUint64Text raises for text this code would not have written. Nothing
// orders or sums these values in SQL, so text costs nothing.
//
// Timestamps are TEXT in RFC3339Nano. TIMESTAMPTZ normalizes to UTC, and
// timeText deliberately preserves the caller's numeric offset — which is
// API-visible, so normalizing would change a published /v1 field. It also
// truncates to microseconds, and these carry nanoseconds.
//
// `complete` is INTEGER, not BOOLEAN — see boolInt for why.
//
// COLLATE "C" is the new one. SQLite compares TEXT by byte value; PostgreSQL
// uses the database's collation, which is usually locale-aware and orders
// punctuation and case differently. `ORDER BY fingerprint_id` is the only
// domain ordering in either backend, and two backends returning behaviour
// entries in different orders is exactly the drift this task exists to
// prevent. Declared on the column so it governs the index and every comparison
// without each query remembering to ask.
func postgresSchemaStatements() []string {
	return []string{
		`CREATE TABLE ` + tableSchemaVersion + ` (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL
		)`,

		`CREATE TABLE ` + tableProjects + ` (
			id   TEXT COLLATE "C" PRIMARY KEY,
			name TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableAgents + ` (
			id         TEXT COLLATE "C" PRIMARY KEY,
			project_id TEXT COLLATE "C" NOT NULL REFERENCES ` + tableProjects + `(id),
			name       TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableCandidates + ` (
			id              TEXT COLLATE "C" PRIMARY KEY,
			agent_id        TEXT COLLATE "C" NOT NULL REFERENCES ` + tableAgents + `(id),
			label           TEXT NOT NULL,
			source_ref      TEXT NOT NULL,
			artifact_digest TEXT NOT NULL,
			model           TEXT NOT NULL,
			toolset_digest  TEXT NOT NULL,
			config_digest   TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableRuns + ` (
			id                 TEXT COLLATE "C" PRIMARY KEY,
			candidate_id       TEXT COLLATE "C" NOT NULL REFERENCES ` + tableCandidates + `(id),
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,
			status             TEXT NOT NULL,
			created_at         TEXT NOT NULL,
			started_at         TEXT,
			finished_at        TEXT,
			failure_reason     TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableAggregates + ` (
			run_id             TEXT COLLATE "C" PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			candidate_id       TEXT COLLATE "C" NOT NULL,
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,

			record_count      TEXT COLLATE "C" NOT NULL,
			first_observed_at TEXT,
			last_observed_at  TEXT,

			decision_allow            TEXT COLLATE "C" NOT NULL,
			decision_observe_only     TEXT COLLATE "C" NOT NULL,
			decision_alert            TEXT COLLATE "C" NOT NULL,
			decision_challenge        TEXT COLLATE "C" NOT NULL,
			decision_require_approval TEXT COLLATE "C" NOT NULL,
			decision_block            TEXT COLLATE "C" NOT NULL,

			risk_low      TEXT COLLATE "C" NOT NULL,
			risk_medium   TEXT COLLATE "C" NOT NULL,
			risk_high     TEXT COLLATE "C" NOT NULL,
			risk_critical TEXT COLLATE "C" NOT NULL,

			approval_unspecified  TEXT COLLATE "C" NOT NULL,
			approval_not_required TEXT COLLATE "C" NOT NULL,
			approval_required     TEXT COLLATE "C" NOT NULL,
			approval_approved     TEXT COLLATE "C" NOT NULL,
			approval_denied       TEXT COLLATE "C" NOT NULL,

			policy_matched_rule    TEXT COLLATE "C" NOT NULL,
			policy_matched_default TEXT COLLATE "C" NOT NULL,

			identity_confidence_count TEXT COLLATE "C" NOT NULL,
			identity_confidence_sum   DOUBLE PRECISION NOT NULL,
			identity_confidence_min   DOUBLE PRECISION NOT NULL,
			identity_confidence_max   DOUBLE PRECISION NOT NULL,

			anomaly_score_count TEXT COLLATE "C" NOT NULL,
			anomaly_score_sum   DOUBLE PRECISION NOT NULL,
			anomaly_score_min   DOUBLE PRECISION NOT NULL,
			anomaly_score_max   DOUBLE PRECISION NOT NULL,

			anomaly_confidence_count TEXT COLLATE "C" NOT NULL,
			anomaly_confidence_sum   DOUBLE PRECISION NOT NULL,
			anomaly_confidence_min   DOUBLE PRECISION NOT NULL,
			anomaly_confidence_max   DOUBLE PRECISION NOT NULL,

			trust_score_count TEXT COLLATE "C" NOT NULL,
			trust_score_sum   DOUBLE PRECISION NOT NULL,
			trust_score_min   DOUBLE PRECISION NOT NULL,
			trust_score_max   DOUBLE PRECISION NOT NULL,

			context_risk_count TEXT COLLATE "C" NOT NULL,
			context_risk_sum   DOUBLE PRECISION NOT NULL,
			context_risk_min   DOUBLE PRECISION NOT NULL,
			context_risk_max   DOUBLE PRECISION NOT NULL
		)`,

		`CREATE TABLE ` + tableSnapshots + ` (
			run_id             TEXT COLLATE "C" PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			candidate_id       TEXT COLLATE "C" NOT NULL,
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,
			observation_count  TEXT COLLATE "C" NOT NULL,
			distinct_count     INTEGER NOT NULL,
			complete           INTEGER NOT NULL
		)`,

		`CREATE TABLE ` + tableEntries + ` (
			run_id             TEXT COLLATE "C" NOT NULL REFERENCES ` + tableSnapshots + `(run_id),
			fingerprint_id     TEXT COLLATE "C" NOT NULL,
			actor_type         TEXT NOT NULL,
			operation_category TEXT NOT NULL,
			operation_name     TEXT NOT NULL,
			target_name        TEXT NOT NULL,
			target_category    TEXT NOT NULL,
			environment        TEXT NOT NULL,
			observations       TEXT COLLATE "C" NOT NULL,
			PRIMARY KEY (run_id, fingerprint_id)
		)`,

		`CREATE TABLE ` + tableIngestState + ` (
			run_id        TEXT COLLATE "C" PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			next_sequence TEXT COLLATE "C" NOT NULL,
			last_digest   TEXT NOT NULL
		)`,

		postgresEnvironmentsStatement(),

		postgresPromotionsStatement(),
		postgresPromotionsIndexStatement(),

		// v5: the child-collection indexes. Shared statement text with
		// SQLite — the indexed columns already carry COLLATE "C", so the
		// index inherits byte ordering and a collation clause here would be a
		// second spelling of the same fact to keep in step.
		agentsByProjectIndexStatement(),
		candidatesByAgentIndexStatement(),
		runsByCandidateIndexStatement(),
	}
}

// postgresPromotionsStatement is v4's only table, kept separate so the
// v3 → v4 migration applies exactly this and nothing else.
//
// COLLATE "C" on id and project_id is load-bearing rather than decorative:
// the collection traverses by id in byte order and pages on it, so a
// locale-aware collation would order two backends' pages differently and
// could place a row on a page a cursor had already passed. The other
// identifier columns carry it for the same reason every other key column
// does.
//
// The gate result is explicit columns, not JSON: a serialized value is a
// schema the database cannot check and a migration cannot see, and this one is
// historical audit evidence. The five …_passed columns are INTEGER rather than
// BOOLEAN, matching platform_behavior_snapshots.complete — written through
// boolInt and read through parseStoredBool, which refuses anything but 0 or 1
// rather than coercing damage into the safer-looking answer.
func postgresPromotionsStatement() string {
	return `CREATE TABLE ` + tablePromotions + ` (
		id                              TEXT COLLATE "C" PRIMARY KEY,
		project_id                      TEXT COLLATE "C" NOT NULL REFERENCES ` + tableProjects + `(id),
		candidate_id                    TEXT COLLATE "C" NOT NULL REFERENCES ` + tableCandidates + `(id),
		reference_candidate_id          TEXT COLLATE "C" NOT NULL REFERENCES ` + tableCandidates + `(id),

		reference_run_id                TEXT COLLATE "C" NOT NULL,
		candidate_run_id                TEXT COLLATE "C" NOT NULL,

		source_environment_ref          TEXT COLLATE "C" NOT NULL,
		source_environment_rank         INTEGER NOT NULL,
		source_environment_revision     TEXT COLLATE "C" NOT NULL,

		target_environment_ref          TEXT COLLATE "C" NOT NULL,
		target_environment_rank         INTEGER NOT NULL,
		target_environment_revision     TEXT COLLATE "C" NOT NULL,

		max_added_behaviors             TEXT COLLATE "C" NOT NULL,
		max_block_decisions             TEXT COLLATE "C" NOT NULL,
		max_critical_risk_observations  TEXT COLLATE "C" NOT NULL,

		gate_reference_evidence_actual  TEXT COLLATE "C" NOT NULL,
		gate_reference_evidence_minimum TEXT COLLATE "C" NOT NULL,
		gate_reference_evidence_passed  INTEGER NOT NULL,
		gate_candidate_evidence_actual  TEXT COLLATE "C" NOT NULL,
		gate_candidate_evidence_minimum TEXT COLLATE "C" NOT NULL,
		gate_candidate_evidence_passed  INTEGER NOT NULL,
		gate_added_behaviors_actual     TEXT COLLATE "C" NOT NULL,
		gate_added_behaviors_passed     INTEGER NOT NULL,
		gate_block_decisions_actual     TEXT COLLATE "C" NOT NULL,
		gate_block_decisions_passed     INTEGER NOT NULL,
		gate_critical_risk_actual       TEXT COLLATE "C" NOT NULL,
		gate_critical_risk_passed       INTEGER NOT NULL,
		gate_verdict                    TEXT NOT NULL,

		outcome                         TEXT NOT NULL,
		decided_at                      TEXT NOT NULL
	)`
}

// postgresPromotionsIndexStatement supports the project-scoped page.
//
// PostgreSQL does not index a foreign key automatically, and the promotions
// primary key is id alone because promotion identity is global — so unlike
// platform_environments the `WHERE project_id = $1 AND id > $2 ORDER BY id`
// range scan is not free from the primary key. This is the one index task 066
// adds, and the one query that needs it.
func postgresPromotionsIndexStatement() string {
	return `CREATE INDEX ` + indexPromotionsByProject +
		` ON ` + tablePromotions + ` (project_id, id)`
}

// postgresEnvironmentsStatement is v3's only addition, kept separate so the
// v2 → v3 migration applies exactly this and nothing else.
//
// COLLATE "C" on ref is load-bearing rather than decorative: the list route
// traverses by ref in byte order and pages on it, so a locale-aware collation
// would order two backends' pages differently and, worse, could place a row
// on a page a cursor had already passed. project_id carries it for the same
// reason every other key column does.
//
// rank is INTEGER and nullable — the only numeric comparison in this schema,
// bounded at 9999 by the domain, with NULL meaning unranked rather than zero.
func postgresEnvironmentsStatement() string {
	return `CREATE TABLE ` + tableEnvironments + ` (
			project_id TEXT COLLATE "C" NOT NULL REFERENCES ` + tableProjects + `(id),
			ref        TEXT COLLATE "C" NOT NULL,
			name       TEXT NOT NULL,
			rank       INTEGER,
			status     TEXT NOT NULL,
			revision   BIGINT NOT NULL,
			PRIMARY KEY (project_id, ref)
		)`
}

// No index beyond the primary keys.
//
// PostgreSQL does not index a foreign key automatically, and none is added,
// because no query searches by agent_id or candidate_id — there is no
// collection route for those, so no such access path exists. An index for a
// query that does not exist would also quietly imply the list capability task
// 063 declined.
//
// The one collection that does exist needs no extra index either: task 065's
// environment page is `WHERE project_id = $1 AND ref > $2 ORDER BY ref LIMIT
// $3`, which is a range scan along the `(project_id, ref)` primary key in its
// own order. Nothing sorts by rank in SQL.

// migrate brings a PostgreSQL schema to SchemaVersion, or refuses it.
//
// Same policy as SQLite, for the same reasons: a newer schema fails closed
// rather than being rewritten, and recognized tables with no version row are
// refused rather than adopted — the safe reading of that state is "something
// else wrote here", not "empty".
func (s *PostgresStore) migrate(ctx context.Context) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		// One migrator at a time, across processes.
		//
		// A transaction-scoped advisory lock, so commit or rollback releases it
		// and a crashed migrator cannot wedge every later start. Taken before
		// anything is inspected: two processes that both read "empty" would
		// otherwise both try to create the schema, and one would fail on a
		// duplicate table for no reason.
		if _, err := tx.Exec(ctx,
			`SELECT pg_advisory_xact_lock($1)`, platformMigrationLockKey); err != nil {
			return mapPostgresError("migration lock", "", err)
		}

		present, err := postgresTablesPresent(ctx, tx)
		if err != nil {
			return err
		}

		switch {
		case len(present) == 0:
			return s.createPostgresSchema(ctx, tx)

		case slices.Equal(present, sortedSchemaTables()):
			// v4 and v5 hold the same tables — v5's migration adds only
			// indexes — so the table set cannot tell them apart and the
			// stamped version is the only distinction. A v4 stamp is migrated
			// forward; a v5 stamp is verified; anything else fails closed
			// inside verifyPostgresVersion, the newer-than-this-binary case
			// included.
			version, err := postgresStoredVersion(ctx, tx)
			if err != nil {
				return err
			}
			if version == schemaVersionV4 {
				return migratePostgresV4ToV5(ctx, tx)
			}
			return verifyPostgresVersion(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV1()):
			// A task 057 database reaches v3 through v2, one step at a time,
			// rather than through a second separately-maintained jump.
			if err := migratePostgresV1ToV2(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV2ToV3(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV3ToV4(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV4ToV5(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV2()):
			if err := migratePostgresV2ToV3(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV3ToV4(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV4ToV5(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV3()):
			if err := migratePostgresV3ToV4(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV4ToV5(ctx, tx)

		default:
			// A recognized subset that is neither version. Nothing here knows
			// what it is, and guessing would mean writing into a schema
			// somebody else owns.
			return fmt.Errorf("%w: database holds %d of the expected platform tables",
				ErrStoreSchemaVersion, len(present))
		}
	})
}

// createPostgresSchema creates every table and stamps the version in one
// transaction.
//
// PostgreSQL has transactional DDL, so this holds the same property the SQLite
// path relies on: a failure cannot leave tables present with the version row
// absent, which is the ambiguous state migrate refuses to adopt.
func (s *PostgresStore) createPostgresSchema(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range postgresSchemaStatements() {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema", "", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO `+tableSchemaVersion+` (id, version) VALUES (1, $1)`,
		SchemaVersion); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV1ToV2 adds the ingest cursor table, exactly as SQLite's
// migration does and nothing else.
func migratePostgresV1ToV2(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, postgresIngestStateStatement()); err != nil {
		return mapPostgresError("schema migration", "", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV2); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV2ToV3 adds the environment registry and backfills what
// history already references, exactly as SQLite's migration does.
//
// The backfill statement is shared between the backends, because "which
// environments did history reference" is one question and two copies of the
// answer is where they would drift. The creation cap is not applied here: a
// valid v2 database may hold a project whose runs reference more
// environments than may now be created, and discarding the excess would
// orphan the evidence naming it.
func migratePostgresV2ToV3(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, postgresEnvironmentsStatement()); err != nil {
		return mapPostgresError("schema migration", "", err)
	}
	if _, err := tx.Exec(ctx, backfillEnvironmentsStatement()); err != nil {
		return mapPostgresError("schema migration", "", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV3); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV3ToV4 adds the promotion history, exactly as SQLite's
// migration does and nothing else.
//
// No backfill. A schema-3 database recorded no promotion decisions because
// none could be made, and synthesizing one from historical evaluation runs
// would fabricate an audit record — a decision nobody made, limits nobody
// chose, a moment nothing happened at. An existing database migrates to an
// empty promotion history, which is the accurate answer.
func migratePostgresV3ToV4(ctx context.Context, tx pgx.Tx) error {
	for _, statement := range []string{
		postgresPromotionsStatement(),
		postgresPromotionsIndexStatement(),
	} {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV4); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV4ToV5 adds the three child-collection indexes and nothing
// else, mirroring SQLite's migrateV4ToV5 statement for statement.
//
// Index-only: no table, no column, no backfill, no row written and no row
// changed. Both backends apply the same three statements from the same
// definitions, so neither can acquire an index the other lacks.
func migratePostgresV4ToV5(ctx context.Context, tx pgx.Tx) error {
	for _, statement := range v5IndexStatements() {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		SchemaVersion); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// postgresIngestStateStatement is v2's only addition, kept separate so the
// v1 → v2 migration applies exactly this.
//
// Located by what it creates rather than by its position in the list. The
// offset spelling broke twice — task 066 appended two statements and task 074
// appended three more, and each time the constant had to be re-counted by
// hand against a fixture guard that caught it. A search cannot drift.
func postgresIngestStateStatement() string {
	return postgresStatementCreating(tableIngestState)
}

// postgresStatementCreating returns the CREATE TABLE for exactly this table.
//
// Matching on the statement's own prefix, not on containment: every child
// table names its parent in a REFERENCES clause, so a substring search would
// find the wrong statement.
func postgresStatementCreating(table string) string {
	want := `CREATE TABLE ` + table + ` (`
	for _, statement := range postgresSchemaStatements() {
		if strings.HasPrefix(strings.TrimSpace(statement), want) {
			return statement
		}
	}
	// Unreachable while the table is in the schema, and a panic rather than a
	// silent empty statement: a migration that executed "" would report
	// success having created nothing.
	panic("platform: no CREATE TABLE statement for " + table)
}

// verifyPostgresVersion refuses a schema this binary does not understand.
// postgresStoredVersion reads the stamped version, refusing a schema that
// carries tables and no version row.
//
// Separate from verifyPostgresVersion because the dispatch now needs the
// number before it can decide whether to verify or to migrate: v4 and v5 are
// distinguishable only by the stamp.
func postgresStoredVersion(ctx context.Context, tx pgx.Tx) (int, error) {
	var version int
	err := tx.QueryRow(ctx,
		`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`).Scan(&version)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Tables without a version. Deliberately refused rather than stamped:
		// the safe reading is that something else wrote here.
		return 0, fmt.Errorf("%w: version table holds no version row", ErrStoreSchemaVersion)
	case err != nil:
		return 0, mapPostgresError("schema version", "", err)
	}
	return version, nil
}

func verifyPostgresVersion(ctx context.Context, tx pgx.Tx) error {
	version, err := postgresStoredVersion(ctx, tx)
	if err != nil {
		return err
	}
	if version != SchemaVersion {
		// Includes the newer-than-this-binary case, which must fail closed.
		return fmt.Errorf("%w: schema version %d, this binary supports %d",
			ErrStoreSchemaVersion, version, SchemaVersion)
	}
	return nil
}

// postgresTablesPresent lists the platform tables in the current schema.
//
// information_schema rather than sqlite_master, and scoped to the current
// schema so a per-test search_path isolates as intended. Sorted, so the
// comparisons in migrate are order-independent.
func postgresTablesPresent(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'
		 ORDER BY table_name`)
	if err != nil {
		return nil, mapPostgresError("schema inspection", "", err)
	}
	defer rows.Close()

	var present []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, mapPostgresError("schema inspection", "", err)
		}
		// Only tables this package owns. A database shared with the engine's
		// baseline store, or with anything else, is not a reason to refuse.
		if slices.Contains(schemaTables, name) {
			present = append(present, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, mapPostgresError("schema inspection", "", err)
	}
	return present, nil
}

// sortedSchemaTables is the complete current table set, sorted.
func sortedSchemaTables() []string {
	sorted := slices.Clone(schemaTables)
	slices.Sort(sorted)
	return sorted
}

// sortedSchemaTablesV1 is what a complete task 057 database holds, sorted.
func sortedSchemaTablesV1() []string {
	sorted := slices.Clone(schemaTablesV1)
	slices.Sort(sorted)
	return sorted
}

// sortedSchemaTablesV2 is what a complete task 058 database holds, sorted.
func sortedSchemaTablesV2() []string {
	sorted := slices.Clone(schemaTablesV2)
	slices.Sort(sorted)
	return sorted
}

// sortedSchemaTablesV3 is what a complete task 065 database holds, sorted.
func sortedSchemaTablesV3() []string {
	sorted := slices.Clone(schemaTablesV3)
	slices.Sort(sorted)
	return sorted
}
