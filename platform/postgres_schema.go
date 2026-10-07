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
	return append([]string{
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
			context_risk_max   DOUBLE PRECISION NOT NULL,

			` + strings.Join(operationalColumnDDL(`TEXT COLLATE "C"`), ",\n\t\t\t") + `
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
	}, append(observationSchemaStatements(`TEXT COLLATE "C"`, "DOUBLE PRECISION"),
		// v8: issue 131's promotion columns, the same statements the
		// v7 -> v8 migration applies.
		append(promotionChangeGateColumnStatements(`TEXT COLLATE "C"`),
			// v9: task 078's scenario executions, the statements the
			// v8 -> v9 migration applies.
			append(scenarioExecutionSchemaStatements(`TEXT COLLATE "C"`, "BIGINT"),
				// v10: task 101's recency keys and indexes.
				append(recencySchemaStatements(`TEXT COLLATE "C"`),
					// v11: task 087's per-behavior operational columns.
					behaviorOperationalSchemaStatements(`TEXT COLLATE "C"`)...)...)...)...)...)
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

		case slices.Equal(present, sortedSchemaTablesV6()):
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
			// v4, v5 and v6 hold the same tables — v5 added indexes and v6 adds
			// columns — so the stamped version is the only distinction. Each
			// older stamp migrates forward one step at a time; anything else
			// fails closed inside verifyPostgresVersion, newer-than-this-binary
			// included.
			switch version {
			case schemaVersionV4:
				if err := migratePostgresV4ToV5(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV5ToV6(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV6ToV7(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV7ToV8(ctx, tx); err != nil {
					return err
				}
				return migratePostgresV8ToCurrent(ctx, tx)
			case schemaVersionV5:
				if err := migratePostgresV5ToV6(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV6ToV7(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV7ToV8(ctx, tx); err != nil {
					return err
				}
				return migratePostgresV8ToCurrent(ctx, tx)
			case schemaVersionV6:
				if err := migratePostgresV6ToV7(ctx, tx); err != nil {
					return err
				}
				if err := migratePostgresV7ToV8(ctx, tx); err != nil {
					return err
				}
				return migratePostgresV8ToCurrent(ctx, tx)
			}
			// Not v4, v5 or v6 and holding exactly their tables: a v7 stamp
			// without v7's tables is damage, and verifyPostgresVersion refuses
			// it rather than creating what is missing.
			return verifyPostgresVersion(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV8()):
			// v7's and v8's table set — v8 added columns only — and the last
			// one without task 078's scenario executions. A v7 stamp migrates
			// forward through v8; a v8 stamp migrates to v9.
			version, err := postgresStoredVersion(ctx, tx)
			if err != nil {
				return err
			}
			switch version {
			case schemaVersionV7:
				if err := migratePostgresV7ToV8(ctx, tx); err != nil {
					return err
				}
				return migratePostgresV8ToCurrent(ctx, tx)
			case schemaVersionV8:
				return migratePostgresV8ToCurrent(ctx, tx)
			case schemaVersionV9, SchemaVersion:
				// The current stamp without the current tables is damage, and
				// verifyPostgresVersion would accept the stamp alone. Refused
				// rather than repaired: whatever removed two tables may have
				// removed more.
				return fmt.Errorf("%w: schema version %d is missing its scenario execution tables",
					ErrStoreSchemaVersion, version)
			}
			// Anything else — newer than this binary included — fails closed.
			return verifyPostgresVersion(ctx, tx)

		case slices.Equal(present, sortedSchemaTables()):
			// The current table set, which v9 and v10 share: v10 added columns
			// and indexes and v11 added columns only. A v9 or v10 stamp
			// migrates forward; otherwise the stamp must be the current
			// version, and anything else fails closed.
			version, err := postgresStoredVersion(ctx, tx)
			if err != nil {
				return err
			}
			switch version {
			case schemaVersionV9:
				if err := migratePostgresV9ToV10(ctx, tx); err != nil {
					return err
				}
				return migratePostgresV10ToV11(ctx, tx)
			case schemaVersionV10:
				return migratePostgresV10ToV11(ctx, tx)
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
			if err := migratePostgresV4ToV5(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV5ToV6(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV6ToV7(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV7ToV8(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV8ToCurrent(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV2()):
			if err := migratePostgresV2ToV3(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV3ToV4(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV4ToV5(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV5ToV6(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV6ToV7(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV7ToV8(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV8ToCurrent(ctx, tx)

		case slices.Equal(present, sortedSchemaTablesV3()):
			if err := migratePostgresV3ToV4(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV4ToV5(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV5ToV6(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV6ToV7(ctx, tx); err != nil {
				return err
			}
			if err := migratePostgresV7ToV8(ctx, tx); err != nil {
				return err
			}
			return migratePostgresV8ToCurrent(ctx, tx)

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
	// Literal schemaVersionV5, not SchemaVersion: they were the same number when
	// this migration was written, and schema 6 made them different. Stamping the
	// current version here would mark a v5 database fully migrated while skipping
	// every later step. SQLite's migrateV4ToV5 carries the same correction.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV5); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV5ToV6 adds the operational aggregate columns, mirroring
// SQLite's migrateV5ToV6 statement for statement.
//
// Task 084. Nine columns on one table, then one backfill that replaces the
// DEFAULT '0' for rows that already existed — see operationalBackfillStatement
// for why a pre-084 run must say *unknown* rather than *zero*. Both backends
// derive their column definitions from aggregateOperationalColumns, so neither
// can acquire a column the other lacks.
func migratePostgresV5ToV6(ctx context.Context, tx pgx.Tx) error {
	for _, column := range operationalColumnDDL(`TEXT COLLATE "C"`) {
		if _, err := tx.Exec(ctx,
			`ALTER TABLE `+tableAggregates+` ADD COLUMN `+column); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	if _, err := tx.Exec(ctx, operationalBackfillStatement(tableAggregates)); err != nil {
		return mapPostgresError("schema migration", "", err)
	}
	// schemaVersionV6, not SchemaVersion: every step stamps the version *it*
	// produces. While v6 was the newest these were the same number, and v7 is
	// where they stop being — stamping SchemaVersion here would mark a
	// database as current before v7's tables existed in it.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV6); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV6ToV7 adds per-observation history, task 067.
//
// The same step SQLite's migrateV6ToV7 applies, derived from the same
// statement list so the two backends cannot disagree about a column, an index
// or a key. **It adds an empty history and invents nothing** — a schema-6
// run's observations were never recorded, and synthesizing them from the
// aggregate would fabricate evidence nobody produced.
func migratePostgresV6ToV7(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range observationSchemaStatements(`TEXT COLLATE "C"`, "DOUBLE PRECISION") {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	// schemaVersionV7, not SchemaVersion: v8 is where they stop being equal.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV7); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV7ToV8 adds the optional counted-change gate columns to the
// promotion history, mirroring SQLite's migrateV7ToV8 statement for statement
// and from the same definitions. It invents no check outcome: every existing
// row reads back `not_recorded` with NULL evidence, and its stored verdict and
// outcome are untouched.
func migratePostgresV7ToV8(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range promotionChangeGateColumnStatements(`TEXT COLLATE "C"`) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	// schemaVersionV8, not SchemaVersion: v9 is where they stop being equal.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV8); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV8ToV9 adds task 078's scenario execution tables, mirroring
// SQLite's migrateV8ToV9 from the same definitions. It invents no execution:
// existing runs were never recorded as part of one, and are untouched.
func migratePostgresV8ToV9(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range scenarioExecutionSchemaStatements(`TEXT COLLATE "C"`, "BIGINT") {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	// Literal schemaVersionV9: v10 is its own step.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV9); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV8ToCurrent is every step from v8 forward.
func migratePostgresV8ToCurrent(ctx context.Context, tx pgx.Tx) error {
	if err := migratePostgresV8ToV9(ctx, tx); err != nil {
		return err
	}
	if err := migratePostgresV9ToV10(ctx, tx); err != nil {
		return err
	}
	return migratePostgresV10ToV11(ctx, tx)
}

// migratePostgresV9ToV10 mirrors SQLite's migrateV9ToV10 from the same
// definitions: two columns, three indexes, and a backfill from stored times.
func migratePostgresV9ToV10(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range recencySchemaStatements(`TEXT COLLATE "C"`) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	exec := func(ctx context.Context, query string, args ...any) error {
		_, err := tx.Exec(ctx, query, args...)
		return err
	}
	if err := recencyBackfill(ctx, pgxQuerier{q: tx}, exec); err != nil {
		return err
	}
	// Literal schemaVersionV10: v11 is its own step.
	if _, err := tx.Exec(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = $1 WHERE id = 1`,
		schemaVersionV10); err != nil {
		return mapPostgresError("schema version", "", err)
	}
	return nil
}

// migratePostgresV10ToV11 mirrors SQLite's migrateV10ToV11 from the same
// definitions: one column on the behavior entry table holding 31 counters, then a backfill
// marking every existing behavior's operational evidence as not recorded.
func migratePostgresV10ToV11(ctx context.Context, tx pgx.Tx) error {
	for _, stmt := range behaviorOperationalSchemaStatements(`TEXT COLLATE "C"`) {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return mapPostgresError("schema migration", "", err)
		}
	}
	if _, err := tx.Exec(ctx, behaviorOperationalBackfillStatement()); err != nil {
		return mapPostgresError("schema migration", "", err)
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

// sortedSchemaTablesV6 is what a complete task 066-through-084 database holds,
// sorted: v4's tables, which v5 and v6 did not change.
//
// v7 is the first step since v4 to add a table, which is what lets this
// backend's table-set dispatch tell a pre-067 database apart from a current
// one at all.
func sortedSchemaTablesV6() []string {
	sorted := slices.Clone(schemaTablesV6)
	slices.Sort(sorted)
	return sorted
}

// sortedSchemaTablesV8 is what a complete v7 or v8 database holds, sorted:
// task 067's tables, before task 078's scenario executions.
func sortedSchemaTablesV8() []string {
	sorted := slices.Clone(schemaTablesV8)
	slices.Sort(sorted)
	return sorted
}

// sortedSchemaTablesV3 is what a complete task 065 database holds, sorted.
func sortedSchemaTablesV3() []string {
	sorted := slices.Clone(schemaTablesV3)
	slices.Sort(sorted)
	return sorted
}
