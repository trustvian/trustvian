package platform

// Local SQLite persistence for platform control and evaluation state.
//
// Lives in package platform, not a subpackage, because EvaluationAggregate
// and BehaviorSnapshot hold unexported fields and a private bound marker.
// Restoring them from a subpackage would need an exported constructor taking
// stored fields — which would hand every caller a way to forge trusted
// evidence and defeat the marker tasks 053-056 rely on. The cost is that the
// driver becomes a dependency of this package; the alternative was worse.
//
// See docs/adr/0030-local-persistence-stores-authoritative-bounded-state.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	trustvian "github.com/trustvian/trustvian"
	"github.com/trustvian/trustvian/event"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO requirement
)

// SchemaVersion is the platform persistence schema version.
//
// Deliberately independent of the Trustvian release version, the core
// file-store version, the core PostgreSQL baseline version, and the config
// schema version. They change for different reasons, and coupling them would
// force a migration on an unrelated release or hide a real one behind an
// unchanged number.
const SchemaVersion = 3

// Table names. Compile-time constants: these are the only identifiers that
// ever appear in assembled SQL. Every caller-supplied value is a bound
// parameter.
const (
	tableSchemaVersion = "platform_schema_version"
	tableProjects      = "platform_projects"
	tableAgents        = "platform_agents"
	tableCandidates    = "platform_candidates"
	tableRuns          = "platform_evaluation_runs"
	tableAggregates    = "platform_evaluation_aggregates"
	tableSnapshots     = "platform_behavior_snapshots"
	tableEntries       = "platform_behavior_entries"

	// Added by schema v2: one ingest cursor row per run.
	tableIngestState = "platform_evaluation_ingest_state"

	// Added by schema v3: the environment registry a run's EnvironmentRef
	// resolves against, keyed by (project_id, ref).
	tableEnvironments = "platform_environments"
)

// schemaTables is every table this schema owns, and the allowlist a test
// asserts against so an event, scorecard, or gate-result table cannot appear
// without something failing.
var schemaTables = []string{
	tableSchemaVersion, tableProjects, tableAgents, tableCandidates,
	tableRuns, tableAggregates, tableSnapshots, tableEntries,
	tableIngestState, tableEnvironments,
}

// SQLiteStore is the local persistence adapter.
//
// Implements ControlStore and EvaluationStore. Safe for concurrent use.
type SQLiteStore struct {
	db *sql.DB
}

var (
	_ ControlStore    = (*SQLiteStore)(nil)
	_ EvaluationStore = (*SQLiteStore)(nil)
)

// OpenSQLiteStore opens or initializes a platform database at path.
//
// A fresh database gets schema version 1, created and stamped in one
// transaction. An existing version 1 is verified and used. Anything else —
// an unknown version, or recognized tables with no version metadata — fails
// closed with ErrStoreSchemaVersion rather than being adopted.
//
// Pass ":memory:" for an ephemeral database. Each such call owns a private
// one: two stores opened this way share no projects, runs, or evidence, and
// closing one does not disturb the other. It does not survive Close — a
// later ":memory:" open is a new empty database, and a file path is what
// provides persistence across a restart.
func OpenSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrStoreCorrupt)
	}

	// foreign_keys is per-connection, so a pragma run once against a pool is
	// a guarantee that lapses the moment the pool opens a second connection.
	// Setting it in the DSN applies it to every connection the pool creates.
	// busy_timeout bounds lock waiting instead of blocking forever.
	//
	// ":memory:" is passed through unchanged, which is what keeps two stores
	// independent. A shared-cache DSN — file::memory:?cache=shared — names one
	// process-wide database, so every store opened with it would see the same
	// projects, runs and evidence. For platform state that is not a
	// convenience, it is a cross-store identity leak.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("platform: open sqlite: %w", err)
	}

	// One writer. SQLite serializes writes anyway, and a single connection
	// makes the foreign-key pragma above true for every statement this store
	// issues rather than true for whichever connection happened to run it.
	//
	// It also carries the in-memory database. A private ":memory:" database
	// lives in its connection, so the pool must hold that one connection open
	// for the store's lifetime — hence an explicit idle connection and no
	// lifetime expiry. A file-backed store does not depend on this; it is
	// stated so a later pool change cannot silently discard memory state.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("platform: open sqlite: %w", err)
	}

	store := &SQLiteStore{db: db}
	if err := store.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the database handle.
func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ---------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------

// migrate brings an empty database to SchemaVersion, verifies one already at
// it, and refuses everything else.
func (s *SQLiteStore) migrate(ctx context.Context) error {
	present, err := s.tablesPresent(ctx)
	if err != nil {
		return err
	}

	hasVersionTable := slices.Contains(present, tableSchemaVersion)
	hasDataTable := slices.ContainsFunc(present, func(name string) bool {
		return name != tableSchemaVersion
	})

	switch {
	case hasVersionTable:
		return s.verifySchema(ctx)

	case hasDataTable:
		// Recognized tables with no version metadata. Stamping this as fresh
		// would silently adopt data this code has never seen, so it is
		// refused: the safe reading is "something else wrote here".
		return fmt.Errorf(
			"%w: platform tables %v exist without version metadata",
			ErrStoreSchemaVersion, present)

	default:
		return s.createSchema(ctx)
	}
}

// verifySchema accepts a database only when the version says 1 *and* the
// whole schema is actually there.
//
// A version row is a claim, not proof. A partial restore, an interrupted
// copy, or a manual DROP can leave metadata saying v1 beside a schema missing
// half its tables — and accepting that would mean the first write fails
// somewhere deep instead of at open, with the damage already invisible.
//
// Nothing is recreated. There is no v1 repair migration in this task: a
// partial v1 schema is operator-visible damage, and silently rebuilding a
// table would discard whatever else went missing with it.
func (s *SQLiteStore) verifySchema(ctx context.Context) error {
	version, err := s.storedSchemaVersion(ctx)
	if err != nil {
		return err
	}

	switch version {
	case SchemaVersion:
		return s.requireTables(ctx, SchemaVersion, schemaTables)

	case schemaVersionV1:
		// A task 057 database. Its own schema must be complete before it is
		// migrated: a partial v1 is damage, and migrating on top of damage
		// would bury it under a version number claiming everything is fine.
		if err := s.requireTables(ctx, schemaVersionV1, schemaTablesV1); err != nil {
			return err
		}
		if err := s.migrateV1ToV2(ctx); err != nil {
			return err
		}
		// Then forward, one step at a time: a v1 database reaches v3 through
		// v2 rather than through a second, separately-maintained jump.
		return s.migrateV2ToV3(ctx)

	case schemaVersionV2:
		if err := s.requireTables(ctx, schemaVersionV2, schemaTablesV2); err != nil {
			return err
		}
		return s.migrateV2ToV3(ctx)

	default:
		// No path from anything else. Newer is refused too: this binary
		// cannot know what a future schema means.
		return fmt.Errorf("%w: database reports version %d, this build supports %d",
			ErrStoreSchemaVersion, version, SchemaVersion)
	}
}

// requireTables refuses a version claim the schema does not actually back.
//
// A version row is a claim, not proof. A partial restore or a manual DROP can
// leave metadata saying v2 beside a schema missing tables, and accepting it
// moves the failure from open to the first write. Nothing is recreated: there
// is no repair migration, and rebuilding one table would discard whatever
// else went missing with it.
func (s *SQLiteStore) requireTables(ctx context.Context, version int, required []string) error {
	present, err := s.tablesPresent(ctx)
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range required {
		if !slices.Contains(present, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"%w: schema reports version %d but required tables are missing: %v",
			ErrStoreSchemaVersion, version, missing)
	}
	return nil
}

// migrateV1ToV2 adds the ingest cursor table and stamps version 2.
//
// Both in one transaction, so a failure leaves a readable v1 rather than a
// half-stamped hybrid — the same fail-closed discipline initialization uses.
// Existing rows are not touched: the migration adds a table and changes a
// number, and every project, agent, candidate, run and piece of evidence is
// preserved exactly.
//
// Runs that already hold evidence get no cursor row here. Their sequence is
// derived from the aggregate's record count on first read, because one ingest
// is one record — and no digest is invented for a record this code never saw.
func (s *SQLiteStore) migrateV1ToV2(ctx context.Context) error {
	if err := s.migrateV1ToV2Once(ctx); err != nil {
		// A racing opener can make *any* statement here fail — a locked
		// database, or the table another opener just created — not only the
		// commit. So recovery cannot key on which step failed, and must not
		// key on a driver's English error text either.
		//
		// The durable schema decides instead: if a complete, valid v2 now
		// exists, somebody else finished the migration and this opener is
		// done. Anything else keeps the original error. One inspection, no
		// loop, and the transaction is already rolled back before it runs —
		// the store holds a single connection, so verifying inside the
		// transaction would deadlock against itself.
		// A racing opener may have completed v1→v2, or gone all the way to v3
		// behind this one. Either is "somebody else finished the step this
		// opener was taking"; anything else keeps the original error.
		if version, verr := s.storedSchemaVersion(ctx); verr == nil {
			switch version {
			case schemaVersionV2:
				if tablesErr := s.requireTables(ctx, schemaVersionV2, schemaTablesV2); tablesErr == nil {
					return nil
				}
			case SchemaVersion:
				if tablesErr := s.requireTables(ctx, SchemaVersion, schemaTables); tablesErr == nil {
					return nil
				}
			}
		}
		return err
	}
	return nil
}

func (s *SQLiteStore) migrateV1ToV2Once(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: migrate schema v1 to v2: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, ingestStateTableStatement()); err != nil {
		return fmt.Errorf("platform: migrate schema v1 to v2: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = ? WHERE id = 1`, schemaVersionV2); err != nil {
		return fmt.Errorf("platform: migrate schema v1 to v2: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: migrate schema v1 to v2: %w", err)
	}
	return nil
}

// migrateV2ToV3 adds the environment registry and backfills what history
// already references.
//
// One transaction, so a failure leaves a readable v2 rather than a
// half-stamped hybrid — the same discipline migrateV1ToV2 uses. Existing rows
// are not touched: this adds a table, fills it from what runs already say,
// and changes a number.
//
// The backfill is the point. Schema 2 had no registry, so every environment
// a run ever named exists only as a string on that run; creating the registry
// without them would make every historical ref unresolvable and would refuse
// the next run against an environment that has been in use for months. Each
// distinct (project, environment) reachable through runs → candidates →
// agents becomes an environment named after itself, active, and **unranked** —
// inventing a promotion order here would be inventing the one thing task 065
// deliberately makes an operator choose.
//
// The creation cap does not apply. A valid v2 database may hold a project
// whose runs reference far more than maxProjectEnvironments distinct
// environments, and discarding the excess would orphan the evidence that
// names it. The cap governs what may be created from here; it is not a claim
// about what a project already contains.
func (s *SQLiteStore) migrateV2ToV3(ctx context.Context) error {
	if err := s.migrateV2ToV3Once(ctx); err != nil {
		// Same racing-opener recovery as v1→v2: the durable schema decides,
		// never a driver's error text.
		if version, verr := s.storedSchemaVersion(ctx); verr == nil && version == SchemaVersion {
			if tablesErr := s.requireTables(ctx, SchemaVersion, schemaTables); tablesErr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

func (s *SQLiteStore) migrateV2ToV3Once(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: migrate schema v2 to v3: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	if _, err := tx.ExecContext(ctx, environmentsTableStatement()); err != nil {
		return fmt.Errorf("platform: migrate schema v2 to v3: %w", err)
	}
	if _, err := tx.ExecContext(ctx, backfillEnvironmentsStatement()); err != nil {
		return fmt.Errorf("platform: migrate schema v2 to v3: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+tableSchemaVersion+` SET version = ? WHERE id = 1`, SchemaVersion); err != nil {
		return fmt.Errorf("platform: migrate schema v2 to v3: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: migrate schema v2 to v3: %w", err)
	}
	return nil
}

// backfillEnvironmentsStatement derives the registry from history.
//
// Written once and used by both backends: the join is the same shape in both
// dialects, and two copies of "which environments did history reference" is
// exactly where they would drift. DISTINCT does the deduplication, so one
// environment used by a thousand runs is one row, and the same ref under two
// projects is two rows — identity is the pair.
//
// The rank is CAST(NULL AS INTEGER) rather than a bare NULL because PostgreSQL
// types an untyped NULL in a SELECT list as text and then refuses to insert it
// into an integer column. SQLite is indifferent, so the cast that one backend
// requires is what keeps the statement genuinely shared.
func backfillEnvironmentsStatement() string {
	return `INSERT INTO ` + tableEnvironments + `
	            (project_id, ref, name, rank, status, revision)
	        SELECT DISTINCT a.project_id, r.environment, r.environment,
	               CAST(NULL AS INTEGER), '` + string(EnvironmentActive) + `', 1
	        FROM ` + tableRuns + ` r
	        JOIN ` + tableCandidates + ` c ON c.id = r.candidate_id
	        JOIN ` + tableAgents + ` a ON a.id = c.agent_id`
}

func (s *SQLiteStore) tablesPresent(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("platform: inspect schema: %w", err)
	}
	defer rows.Close()

	var present []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("platform: inspect schema: %w", err)
		}
		if slices.Contains(schemaTables, name) {
			present = append(present, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: inspect schema: %w", err)
	}
	return present, nil
}

// schemaVersionV1 is task 057's schema: everything v2 has except the ingest
// cursor table. Named so the migration path reads as a version, not a number.
const schemaVersionV1 = 1

// schemaVersionV2 is task 058's schema: everything v3 has except the
// environment registry.
const schemaVersionV2 = 2

// schemaTablesV1 is what a complete v1 database holds.
var schemaTablesV1 = []string{
	tableSchemaVersion, tableProjects, tableAgents, tableCandidates,
	tableRuns, tableAggregates, tableSnapshots, tableEntries,
}

// schemaTablesV2 is what a complete v2 database holds.
var schemaTablesV2 = append(append([]string{}, schemaTablesV1...), tableIngestState)

func (s *SQLiteStore) storedSchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT version FROM `+tableSchemaVersion+` WHERE id = 1`).Scan(&version)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, fmt.Errorf("%w: version table holds no version row", ErrStoreSchemaVersion)
	case err != nil:
		return 0, fmt.Errorf("platform: read schema version: %w", err)
	}
	return version, nil
}

// createSchema creates every table and stamps the version in one transaction,
// so a failure cannot leave tables present with metadata absent — exactly the
// ambiguous state migrate refuses to adopt.
func (s *SQLiteStore) createSchema(ctx context.Context) error {
	if err := s.createSchemaOnce(ctx); err != nil {
		// A racing initializer can make any statement here fail — a locked
		// database, or a table another opener just created — not only the
		// commit. So recovery cannot key on which step failed, and must not
		// key on a driver's English error text either.
		//
		// The durable state decides instead: if a complete, valid v1 schema
		// now exists, somebody else finished the job and this opener is done.
		// If it does not, the original error stands. One re-inspection, no
		// loop, and the transaction is already rolled back before it runs —
		// the store holds a single connection, so verifying while still
		// inside the transaction would deadlock against itself.
		if verifyErr := s.verifySchema(ctx); verifyErr == nil {
			return nil
		}
		return err
	}
	return nil
}

func (s *SQLiteStore) createSchemaOnce(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: create schema: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	for _, stmt := range schemaStatements() {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("platform: create schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableSchemaVersion+` (id, version) VALUES (1, ?)`,
		SchemaVersion); err != nil {
		return fmt.Errorf("platform: create schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: create schema: %w", err)
	}
	return nil
}

// schemaStatements is the whole schema.
//
// Counters are TEXT, not INTEGER. SQLite INTEGER is signed 64-bit and
// platform counters are uint64, so int64(value) would corrupt everything
// above MaxInt64 — silently, and only for large values. See uint64Text.
//
// Timestamps are RFC3339Nano TEXT, nullable where the domain allows a zero
// time. No column defaults to a database clock: every timestamp already
// exists on the value being stored.
func schemaStatements() []string {
	return []string{
		`CREATE TABLE ` + tableSchemaVersion + ` (
			id      INTEGER PRIMARY KEY CHECK (id = 1),
			version INTEGER NOT NULL
		)`,

		`CREATE TABLE ` + tableProjects + ` (
			id   TEXT PRIMARY KEY,
			name TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableAgents + ` (
			id         TEXT PRIMARY KEY,
			project_id TEXT NOT NULL REFERENCES ` + tableProjects + `(id),
			name       TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableCandidates + ` (
			id              TEXT PRIMARY KEY,
			agent_id        TEXT NOT NULL REFERENCES ` + tableAgents + `(id),
			label           TEXT NOT NULL,
			source_ref      TEXT NOT NULL,
			artifact_digest TEXT NOT NULL,
			model           TEXT NOT NULL,
			toolset_digest  TEXT NOT NULL,
			config_digest   TEXT NOT NULL
		)`,

		environmentsTableStatement(),

		`CREATE TABLE ` + tableRuns + ` (
			id                 TEXT PRIMARY KEY,
			candidate_id       TEXT NOT NULL REFERENCES ` + tableCandidates + `(id),
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,
			status             TEXT NOT NULL,
			created_at         TEXT NOT NULL,
			started_at         TEXT,
			finished_at        TEXT,
			failure_reason     TEXT NOT NULL
		)`,

		`CREATE TABLE ` + tableAggregates + ` (
			run_id             TEXT PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			candidate_id       TEXT NOT NULL,
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,

			record_count      TEXT NOT NULL,
			first_observed_at TEXT,
			last_observed_at  TEXT,

			decision_allow            TEXT NOT NULL,
			decision_observe_only     TEXT NOT NULL,
			decision_alert            TEXT NOT NULL,
			decision_challenge        TEXT NOT NULL,
			decision_require_approval TEXT NOT NULL,
			decision_block            TEXT NOT NULL,

			risk_low      TEXT NOT NULL,
			risk_medium   TEXT NOT NULL,
			risk_high     TEXT NOT NULL,
			risk_critical TEXT NOT NULL,

			approval_unspecified  TEXT NOT NULL,
			approval_not_required TEXT NOT NULL,
			approval_required     TEXT NOT NULL,
			approval_approved     TEXT NOT NULL,
			approval_denied       TEXT NOT NULL,

			policy_matched_rule    TEXT NOT NULL,
			policy_matched_default TEXT NOT NULL,

			identity_confidence_count TEXT NOT NULL,
			identity_confidence_sum   REAL NOT NULL,
			identity_confidence_min   REAL NOT NULL,
			identity_confidence_max   REAL NOT NULL,

			anomaly_score_count TEXT NOT NULL,
			anomaly_score_sum   REAL NOT NULL,
			anomaly_score_min   REAL NOT NULL,
			anomaly_score_max   REAL NOT NULL,

			anomaly_confidence_count TEXT NOT NULL,
			anomaly_confidence_sum   REAL NOT NULL,
			anomaly_confidence_min   REAL NOT NULL,
			anomaly_confidence_max   REAL NOT NULL,

			trust_score_count TEXT NOT NULL,
			trust_score_sum   REAL NOT NULL,
			trust_score_min   REAL NOT NULL,
			trust_score_max   REAL NOT NULL,

			context_risk_count TEXT NOT NULL,
			context_risk_sum   REAL NOT NULL,
			context_risk_min   REAL NOT NULL,
			context_risk_max   REAL NOT NULL
		)`,

		`CREATE TABLE ` + tableSnapshots + ` (
			run_id             TEXT PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			candidate_id       TEXT NOT NULL,
			environment        TEXT NOT NULL,
			behavioral_profile TEXT NOT NULL,
			observation_count  TEXT NOT NULL,
			distinct_count     INTEGER NOT NULL,
			complete           INTEGER NOT NULL
		)`,

		`CREATE TABLE ` + tableEntries + ` (
			run_id             TEXT NOT NULL REFERENCES ` + tableSnapshots + `(run_id),
			fingerprint_id     TEXT NOT NULL,
			actor_type         TEXT NOT NULL,
			operation_category TEXT NOT NULL,
			operation_name     TEXT NOT NULL,
			target_name        TEXT NOT NULL,
			target_category    TEXT NOT NULL,
			environment        TEXT NOT NULL,
			observations       TEXT NOT NULL,
			PRIMARY KEY (run_id, fingerprint_id)
		)`,

		ingestStateTableStatement(),
	}
}

// environmentsTableStatement is the v3 table, written once so the fresh
// schema and the v2 migration cannot disagree about it.
//
// rank is the one nullable column and the one INTEGER that is not a boolean:
// the counters in this schema are TEXT because they can exceed MaxInt64 and
// must round-trip exactly, while a rank is a human-chosen 0..9999 and is the
// only value SQL here ever compares numerically. NULL means unranked, which
// is a different statement from rank 0.
//
// No foreign key from evaluation runs to this table. A run stores a ref and
// no project_id, so the composite cannot be expressed without denormalizing
// the hierarchy onto runs, and a foreign key would make a historical run
// unloadable if its environment ever became unreachable. Creation-time
// validation in ControlPlane is the enforcement point instead.
func environmentsTableStatement() string {
	return `CREATE TABLE ` + tableEnvironments + ` (
			project_id TEXT    NOT NULL REFERENCES ` + tableProjects + `(id),
			ref        TEXT    NOT NULL,
			name       TEXT    NOT NULL,
			rank       INTEGER,
			status     TEXT    NOT NULL,
			revision   INTEGER NOT NULL,
			PRIMARY KEY (project_id, ref)
		)`
}

// ingestStateTableStatement is schema v2's only addition, kept separate
// because the v1 -> v2 migration applies exactly this and nothing else.
//
// next_sequence is canonical uint64 text for the same reason every other
// counter here is: SQLite INTEGER is signed 64-bit. last_digest is hex
// SHA-256, empty when a migrated run has evidence whose digest was never
// recorded — nothing fabricates one.
func ingestStateTableStatement() string {
	return `CREATE TABLE ` + tableIngestState + ` (
			run_id        TEXT PRIMARY KEY REFERENCES ` + tableRuns + `(id),
			next_sequence TEXT NOT NULL,
			last_digest   TEXT NOT NULL
		)`
}

// ---------------------------------------------------------------------
// uint64 and time encoding
// ---------------------------------------------------------------------

// uint64Text encodes a counter as canonical base-10 text.
//
// Not int64(v): SQLite INTEGER is signed 64-bit, so that conversion silently
// corrupts every value above MaxInt64 — correct in small tests, wrong for
// large counters, which is the worst failure profile available.
func uint64Text(v uint64) string { return strconv.FormatUint(v, 10) }

// parseUint64Text reads a counter, rejecting anything this code would not
// have written. A value needing coercion was written by something else.
func parseUint64Text(field, s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s is not a canonical uint64: %q", ErrStoreCorrupt, field, preview(s))
	}
	// ParseUint accepts "007" and "+7"; neither is what FormatUint emits.
	if strconv.FormatUint(v, 10) != s {
		return 0, fmt.Errorf("%w: %s is not canonical: %q", ErrStoreCorrupt, field, preview(s))
	}
	return v, nil
}

// parseStoredBool decodes a stored flag, refusing anything this code would
// not have written.
//
// `complete == 1` would quietly turn a stored 2, -1 or 42 into false, which
// normalizes corruption into the safer-looking of two answers and loses the
// fact that the column was damaged at all. Completeness decides whether a
// snapshot may be compared, so it is not a field to guess at.
func parseStoredBool(field string, v int) (bool, error) {
	switch v {
	case 0:
		return false, nil
	case 1:
		return true, nil
	default:
		return false, fmt.Errorf("%w: %s is %d, expected 0 or 1", ErrStoreCorrupt, field, v)
	}
}

// timeText encodes a timestamp losslessly enough to preserve the instant,
// nanosecond precision, and the numeric zone offset.
//
// Deliberately not normalized to UTC: task 052 avoided that convention, and
// rewriting a caller's offset discards information it chose to record. The Go
// monotonic reading is process state and does not survive restart.
func timeText(t time.Time) string { return t.Format(time.RFC3339Nano) }

func parseTimeText(field, s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %s is not a valid timestamp: %q",
			ErrStoreCorrupt, field, preview(s))
	}
	return t, nil
}

// nullTimeText stores a zero time as NULL rather than as a sentinel string.
func nullTimeText(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return timeText(t)
}

func parseNullTimeText(field string, s sql.NullString) (time.Time, error) {
	if !s.Valid {
		return time.Time{}, nil
	}
	return parseTimeText(field, s.String)
}

// ---------------------------------------------------------------------
// Control state
// ---------------------------------------------------------------------

// CreateProject stores a project. An existing id is ErrStoreAlreadyExists.
func (s *SQLiteStore) CreateProject(ctx context.Context, project Project) error {
	if project.ID() == "" {
		return fmt.Errorf("%w: project has no identity", ErrInvalidID)
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableProjects+` (id, name) VALUES (?, ?)`,
		string(project.ID()), project.Name())
	return s.writeError("project", string(project.ID()), err)
}

// Project loads a project by id.
func (s *SQLiteStore) Project(ctx context.Context, id ProjectID) (Project, error) {
	var name string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM `+tableProjects+` WHERE id = ?`, string(id)).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return Project{}, fmt.Errorf("platform: load project: %w", err)
	}

	// Through the domain constructor, so stored rows face the same validation
	// a live value did rather than being trusted because they are stored.
	project, err := NewProject(id, name)
	if err != nil {
		return Project{}, fmt.Errorf("%w: project %s: %w", ErrStoreCorrupt, preview(string(id)), err)
	}
	return project, nil
}

// CreateAgent stores an agent. A missing project is ErrStoreNotFound.
func (s *SQLiteStore) CreateAgent(ctx context.Context, agent Agent) error {
	if agent.ID() == "" {
		return fmt.Errorf("%w: agent has no identity", ErrInvalidID)
	}
	if err := s.requireExists(ctx, tableProjects, "project", string(agent.ProjectID())); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableAgents+` (id, project_id, name) VALUES (?, ?, ?)`,
		string(agent.ID()), string(agent.ProjectID()), agent.Name())
	return s.writeError("agent", string(agent.ID()), err)
}

// Agent loads an agent by id.
func (s *SQLiteStore) Agent(ctx context.Context, id AgentID) (Agent, error) {
	var projectID, name string
	err := s.db.QueryRowContext(ctx,
		`SELECT project_id, name FROM `+tableAgents+` WHERE id = ?`, string(id)).
		Scan(&projectID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("%w: agent %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return Agent{}, fmt.Errorf("platform: load agent: %w", err)
	}

	agent, err := NewAgent(id, ProjectID(projectID), name)
	if err != nil {
		return Agent{}, fmt.Errorf("%w: agent %s: %w", ErrStoreCorrupt, preview(string(id)), err)
	}
	return agent, nil
}

// CreateCandidate stores a candidate. A missing agent is ErrStoreNotFound.
//
// Never an upsert: the same CandidateID with a different artifact digest must
// not rewrite what a finished run was evaluated against.
func (s *SQLiteStore) CreateCandidate(ctx context.Context, candidate Candidate) error {
	if candidate.ID() == "" {
		return fmt.Errorf("%w: candidate has no identity", ErrInvalidID)
	}
	if err := s.requireExists(ctx, tableAgents, "agent", string(candidate.AgentID())); err != nil {
		return err
	}
	m := candidate.Metadata()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableCandidates+`
		 (id, agent_id, label, source_ref, artifact_digest, model, toolset_digest, config_digest)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(candidate.ID()), string(candidate.AgentID()),
		m.Label, m.SourceRef, m.ArtifactDigest, m.Model, m.ToolsetDigest, m.ConfigDigest)
	return s.writeError("candidate", string(candidate.ID()), err)
}

// Candidate loads a candidate by id.
func (s *SQLiteStore) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	var agentID string
	var m CandidateMetadata
	err := s.db.QueryRowContext(ctx,
		`SELECT agent_id, label, source_ref, artifact_digest, model, toolset_digest, config_digest
		 FROM `+tableCandidates+` WHERE id = ?`, string(id)).
		Scan(&agentID, &m.Label, &m.SourceRef, &m.ArtifactDigest, &m.Model, &m.ToolsetDigest, &m.ConfigDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return Candidate{}, fmt.Errorf("%w: candidate %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return Candidate{}, fmt.Errorf("platform: load candidate: %w", err)
	}

	candidate, err := NewCandidate(id, AgentID(agentID), m)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: candidate %s: %w", ErrStoreCorrupt, preview(string(id)), err)
	}
	return candidate, nil
}

// requireExists turns a missing parent into ErrStoreNotFound explicitly,
// rather than leaving it to a driver's foreign-key error string. The database
// constraint stays as defence; this is what produces a stable error.
func (s *SQLiteStore) requireExists(ctx context.Context, table, kind, id string) error {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s %s", ErrStoreNotFound, kind, preview(id))
	}
	if err != nil {
		return fmt.Errorf("platform: verify %s: %w", kind, err)
	}
	return nil
}

// writeError maps a primary-key collision to ErrStoreAlreadyExists and a
// foreign-key violation to ErrStoreNotFound, without parsing driver text
// beyond the stable SQLite constraint markers.
func (s *SQLiteStore) writeError(kind, id string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "UNIQUE constraint failed"),
		strings.Contains(msg, "PRIMARY KEY constraint failed"):
		return fmt.Errorf("%w: %s %s", ErrStoreAlreadyExists, kind, preview(id))
	case strings.Contains(msg, "FOREIGN KEY constraint failed"):
		return fmt.Errorf("%w: %s %s references a missing parent", ErrStoreNotFound, kind, preview(id))
	}
	return fmt.Errorf("platform: store %s: %w", kind, err)
}

// ---------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------

// sqlExecQuerier adapts a database/sql transaction to environmentWriter.
type sqlExecQuerier struct{ tx *sql.Tx }

func (q sqlExecQuerier) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	return q.tx.QueryRowContext(ctx, query, args...)
}

func (q sqlExecQuerier) noRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func (q sqlExecQuerier) rebind(query string) string { return query }

func (q sqlExecQuerier) exec(ctx context.Context, query string, args ...any) (int64, error) {
	result, err := q.tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// lockProject takes SQLite's write intent on the owning project row.
//
// SQLite has no SELECT ... FOR UPDATE, and a deferred transaction would read
// the environment count as a reader and only discover it cannot write when it
// tried to insert — after the cap decision had already been made. Writing the
// project row moves the transaction to RESERVED here, which is SQLite's
// equivalent of the row lock PostgreSQL takes, and it happens before anything
// is counted.
//
// The row is set to its own value: this takes a lock, it does not change a
// project. RowsAffected doubles as the existence check, so a missing project
// costs no second query.
func lockProjectForWrite(ctx context.Context, w environmentWriter, projectID string) error {
	affected, err := w.exec(ctx, w.rebind(
		`UPDATE `+tableProjects+` SET name = name WHERE id = ?`), projectID)
	if err != nil {
		return fmt.Errorf("platform: lock project: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(projectID))
	}
	return nil
}

func (q sqlExecQuerier) lockProject(ctx context.Context, projectID string) error {
	return lockProjectForWrite(ctx, q, projectID)
}

// CreateEnvironment stores one environment under the cap, atomically.
//
// The whole check-then-insert sequence runs in one write transaction.
// BEGIN IMMEDIATE — expressed here as an immediate write against the project
// row — rather than a deferred transaction, so the write intent is taken
// before the count is read instead of being upgraded after it. Relying on
// SetMaxOpenConns(1) would put the guarantee in a pool setting rather than in
// the code that depends on it, and a later pool change would silently remove
// it.
//
// The checks are ordered, and the order is the contract: missing project,
// then existing identity, then the cap. A ref the project already has is
// ErrStoreAlreadyExists whatever the count, because that request adds nothing.
func (s *SQLiteStore) CreateEnvironment(ctx context.Context, env Environment) error {
	if env.Ref() == "" || env.ProjectID() == "" {
		return fmt.Errorf("%w: environment has no identity", ErrInvalidID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: create environment: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// Take the write lock first. SQLite has no SELECT ... FOR UPDATE, and a
	// deferred transaction would read the count as a reader and only then
	// discover it cannot write. Touching the owning project row establishes
	// the write intent at the same granularity PostgreSQL locks, and it fails
	// here rather than after the decision has been made.
	writer := sqlExecQuerier{tx}
	if err := writer.lockProject(ctx, string(env.ProjectID())); err != nil {
		return err
	}

	rank, ranked := env.Rank()
	if err := insertEnvironmentLocked(ctx, writer, env, rank, ranked); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: create environment: %w", err)
	}
	return nil
}

// Environment loads one environment by (project, ref).
func (s *SQLiteStore) Environment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef,
) (Environment, error) {
	return loadEnvironment(ctx, sqlQuerier{s.db}, projectID, ref)
}

// UpdateEnvironment replaces previous with next if the stored revision still
// matches previous's.
//
// One predicated UPDATE, so the database serializes concurrent writers with
// no lock held across a round trip: the predicate *is* the revision. A
// non-matching row means either the environment is gone or somebody else
// moved it, and the distinction is resolved by a follow-up read rather than
// guessed at.
func (s *SQLiteStore) UpdateEnvironment(ctx context.Context, previous, next Environment) error {
	if err := validateEnvironmentUpdate(previous, next); err != nil {
		return err
	}
	rank, ranked := next.Rank()
	result, err := s.db.ExecContext(ctx,
		`UPDATE `+tableEnvironments+`
		 SET name = ?, rank = ?, status = ?, revision = ?
		 WHERE project_id = ? AND ref = ? AND revision = ?`,
		next.Name(), nullRank(rank, ranked), string(next.Status()), int64(next.Revision()),
		string(previous.ProjectID()), string(previous.Ref()), int64(previous.Revision()))
	if err != nil {
		return fmt.Errorf("platform: update environment: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("platform: update environment: %w", err)
	}
	if affected == 0 {
		return environmentUpdateMiss(ctx, sqlQuerier{s.db}, previous)
	}
	return nil
}

// ProjectEnvironments returns one bounded page in ref byte order.
func (s *SQLiteStore) ProjectEnvironments(
	ctx context.Context, projectID ProjectID, after EnvironmentRef, limit int,
) ([]Environment, error) {
	if err := validateEnvironmentPage(after, limit); err != nil {
		return nil, err
	}
	if err := s.requireExists(ctx, tableProjects, "project", string(projectID)); err != nil {
		return nil, err
	}
	return queryEnvironmentPage(ctx, sqlQuerier{s.db}, projectID, after, limit)
}

// ---------------------------------------------------------------------
// Evaluation runs
// ---------------------------------------------------------------------

// CreateEvaluationRun stores a run. A missing candidate is ErrStoreNotFound.
func (s *SQLiteStore) CreateEvaluationRun(ctx context.Context, run EvaluationRun) error {
	if err := validateRunBinding(run); err != nil {
		return err
	}
	if err := s.requireExists(ctx, tableCandidates, "candidate", string(run.CandidateID())); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+tableRuns+`
		 (id, candidate_id, environment, behavioral_profile, status,
		  created_at, started_at, finished_at, failure_reason)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(run.ID()), string(run.CandidateID()), string(run.Environment()),
		string(run.BehavioralProfile()), string(run.Status()),
		timeText(run.CreatedAt()), nullTimeText(run.StartedAt()),
		nullTimeText(run.FinishedAt()), run.FailureReason())
	return s.writeError("evaluation run", string(run.ID()), err)
}

// EvaluationRun loads a run by id.
func (s *SQLiteStore) EvaluationRun(ctx context.Context, id EvaluationRunID) (EvaluationRun, error) {
	return loadRun(ctx, sqlQuerier{s.db}, id)
}

func loadRun(ctx context.Context, q rowQuerier, id EvaluationRunID) (EvaluationRun, error) {
	var candidateID, environment, profile, status, createdAt, failureReason string
	var startedAt, finishedAt sql.NullString

	err := q.queryRow(ctx, q.rebind(
		`SELECT candidate_id, environment, behavioral_profile, status,
		        created_at, started_at, finished_at, failure_reason
		 FROM `+tableRuns+` WHERE id = ?`), string(id)).
		Scan(&candidateID, &environment, &profile, &status,
			&createdAt, &startedAt, &finishedAt, &failureReason)
	if q.noRows(err) {
		return EvaluationRun{}, fmt.Errorf("%w: evaluation run %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return EvaluationRun{}, fmt.Errorf("platform: load evaluation run: %w", err)
	}

	return rehydrateRun(id, storedRun{
		candidateID:   CandidateID(candidateID),
		environment:   EnvironmentRef(environment),
		profile:       BehavioralProfileRef(profile),
		status:        RunStatus(status),
		createdAt:     createdAt,
		startedAt:     startedAt,
		finishedAt:    finishedAt,
		failureReason: failureReason,
	})
}

type storedRun struct {
	candidateID   CandidateID
	environment   EnvironmentRef
	profile       BehavioralProfileRef
	status        RunStatus
	createdAt     string
	startedAt     sql.NullString
	finishedAt    sql.NullString
	failureReason string
}

// rehydrateRun rebuilds a run by replaying its domain transitions.
//
// Private lifecycle fields are never written directly and then marked valid.
// Replaying means a corrupted chronology, an impossible status, or a failure
// reason on a completed run fails the same invariants a live value would —
// which is why this returns ErrStoreCorrupt rather than a repaired timestamp.
func rehydrateRun(id EvaluationRunID, stored storedRun) (EvaluationRun, error) {
	corrupt := func(format string, args ...any) (EvaluationRun, error) {
		return EvaluationRun{}, fmt.Errorf("%w: evaluation run %s: "+format,
			append([]any{ErrStoreCorrupt, preview(string(id))}, args...)...)
	}

	createdAt, err := parseTimeText("created_at", stored.createdAt)
	if err != nil {
		return EvaluationRun{}, err
	}
	startedAt, err := parseNullTimeText("started_at", stored.startedAt)
	if err != nil {
		return EvaluationRun{}, err
	}
	finishedAt, err := parseNullTimeText("finished_at", stored.finishedAt)
	if err != nil {
		return EvaluationRun{}, err
	}

	run, err := NewEvaluationRun(id, stored.candidateID, stored.environment, stored.profile, createdAt)
	if err != nil {
		return corrupt("%v", err)
	}

	start := func() error {
		run, err = run.Start(startedAt)
		return err
	}

	switch stored.status {
	case RunPending:
		if !startedAt.IsZero() || !finishedAt.IsZero() || stored.failureReason != "" {
			return corrupt("pending run carries lifecycle state")
		}

	case RunRunning:
		if !finishedAt.IsZero() || stored.failureReason != "" {
			return corrupt("running run carries terminal state")
		}
		if err := start(); err != nil {
			return corrupt("%v", err)
		}

	case RunCompleted:
		if stored.failureReason != "" {
			return corrupt("completed run carries a failure reason")
		}
		if err := start(); err != nil {
			return corrupt("%v", err)
		}
		if run, err = run.Complete(finishedAt); err != nil {
			return corrupt("%v", err)
		}

	case RunFailed:
		if err := start(); err != nil {
			return corrupt("%v", err)
		}
		if run, err = run.Fail(finishedAt, stored.failureReason); err != nil {
			return corrupt("%v", err)
		}

	case RunCancelled:
		// Cancellable from pending or running, and the stored start time is
		// what distinguishes them.
		if !startedAt.IsZero() {
			if err := start(); err != nil {
				return corrupt("%v", err)
			}
		}
		if run, err = run.Cancel(finishedAt); err != nil {
			return corrupt("%v", err)
		}
		if stored.failureReason != "" {
			return corrupt("cancelled run carries a failure reason")
		}

	default:
		return corrupt("unknown status %q", preview(string(stored.status)))
	}

	// The replay must reproduce exactly what was stored. If it does not, the
	// row and the domain disagree about what this run is.
	if run.Status() != stored.status {
		return corrupt("replayed status %q does not match stored %q",
			run.Status(), preview(string(stored.status)))
	}
	if !run.StartedAt().Equal(startedAt) || !run.FinishedAt().Equal(finishedAt) {
		return corrupt("replayed lifecycle times do not match stored values")
	}
	return run, nil
}

// UpdateEvaluationRun replaces previous with next under compare-and-swap.
func (s *SQLiteStore) UpdateEvaluationRun(ctx context.Context, previous, next EvaluationRun) error {
	if err := validateRunBinding(previous); err != nil {
		return fmt.Errorf("previous run: %w", err)
	}
	if err := validateRunBinding(next); err != nil {
		return fmt.Errorf("next run: %w", err)
	}
	if previous.ID() != next.ID() {
		return fmt.Errorf("%w: run identity changed from %s to %s",
			ErrStoreConflict, preview(string(previous.ID())), preview(string(next.ID())))
	}
	// Identity a transition may never alter. Checked here rather than trusted
	// because an update is the only place a caller could smuggle one through.
	if previous.CandidateID() != next.CandidateID() ||
		previous.Environment() != next.Environment() ||
		previous.BehavioralProfile() != next.BehavioralProfile() ||
		!previous.CreatedAt().Equal(next.CreatedAt()) {
		return fmt.Errorf("%w: run %s changed immutable identity",
			ErrStoreConflict, preview(string(previous.ID())))
	}
	if err := validTransition(previous, next); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: update evaluation run: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	stored, err := loadRun(ctx, sqlQuerier{tx}, previous.ID())
	if err != nil {
		return err
	}
	if !sameRun(stored, previous) {
		return fmt.Errorf("%w: evaluation run %s is now %s, not %s",
			ErrStoreConflict, preview(string(previous.ID())), stored.Status(), previous.Status())
	}

	// The expected previous status is part of the statement, not only of the
	// Go check above.
	//
	// The check alone is sound here because this store holds one connection, so
	// the read and the write cannot interleave with another writer. That is a
	// property of the SQLite configuration rather than of this operation, and a
	// backend with a connection pool does not have it: two callers would both
	// read `created`, both pass, and both write — the second silently losing
	// the first. Putting the predicate in SQL makes the compare-and-swap the
	// doc comment promises true of the operation itself, on every backend.
	result, err := tx.ExecContext(ctx,
		`UPDATE `+tableRuns+`
		 SET status = ?, started_at = ?, finished_at = ?, failure_reason = ?
		 WHERE id = ? AND status = ?`,
		string(next.Status()), nullTimeText(next.StartedAt()),
		nullTimeText(next.FinishedAt()), next.FailureReason(),
		string(next.ID()), string(previous.Status()))
	if err != nil {
		return fmt.Errorf("platform: update evaluation run: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("platform: update evaluation run: %w", err)
	}
	if affected != 1 {
		// The row moved between the read above and this write. Same meaning as
		// the staleness check, reported the same way.
		return fmt.Errorf("%w: evaluation run %s is no longer %s",
			ErrStoreConflict, preview(string(previous.ID())), previous.Status())
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: update evaluation run: %w", err)
	}
	return nil
}

// validTransition proves next is reachable from previous by asking the domain
// rather than re-implementing its state machine here.
func validTransition(previous, next EvaluationRun) error {
	conflict := func() error {
		return fmt.Errorf("%w: %s cannot become %s",
			ErrStoreConflict, previous.Status(), next.Status())
	}

	var produced EvaluationRun
	var err error
	switch next.Status() {
	case RunRunning:
		produced, err = previous.Start(next.StartedAt())
	case RunCompleted:
		produced, err = previous.Complete(next.FinishedAt())
	case RunFailed:
		produced, err = previous.Fail(next.FinishedAt(), next.FailureReason())
	case RunCancelled:
		produced, err = previous.Cancel(next.FinishedAt())
	case RunPending:
		// Nothing transitions back to pending.
		return conflict()
	default:
		return fmt.Errorf("%w: unknown target status %q", ErrStoreConflict, preview(string(next.Status())))
	}
	if err != nil {
		return fmt.Errorf("%w: %s cannot become %s: %w",
			ErrStoreConflict, previous.Status(), next.Status(), err)
	}
	if !sameRun(produced, next) {
		return conflict()
	}
	return nil
}

// sameRun compares every persisted field, which is what makes the
// compare-and-swap meaningful.
func sameRun(a, b EvaluationRun) bool {
	return a.ID() == b.ID() &&
		a.CandidateID() == b.CandidateID() &&
		a.Environment() == b.Environment() &&
		a.BehavioralProfile() == b.BehavioralProfile() &&
		a.Status() == b.Status() &&
		a.CreatedAt().Equal(b.CreatedAt()) &&
		a.StartedAt().Equal(b.StartedAt()) &&
		a.FinishedAt().Equal(b.FinishedAt()) &&
		a.FailureReason() == b.FailureReason()
}

// ---------------------------------------------------------------------
// Evaluation evidence
// ---------------------------------------------------------------------

// SaveEvaluationEvidence replaces one run's latest evidence, atomically.
func (s *SQLiteStore) SaveEvaluationEvidence(
	ctx context.Context,
	aggregate EvaluationAggregate,
	snapshot BehaviorSnapshot,
) error {
	if err := validateEvidencePair(aggregate, snapshot); err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("platform: save evaluation evidence: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// The run must exist and agree — evidence is never stored under a run
	// merely because a RunID string matched.
	run, err := loadRun(ctx, sqlQuerier{tx}, aggregate.RunID())
	if err != nil {
		return err
	}
	if run.CandidateID() != aggregate.CandidateID() ||
		run.Environment() != aggregate.Environment() ||
		run.BehavioralProfile() != aggregate.BehavioralProfile() {
		return fmt.Errorf("%w: evidence identity does not match stored run %s",
			ErrStoreConflict, preview(string(aggregate.RunID())))
	}

	if err := checkEvidenceNotStale(ctx, sqlQuerier{tx}, aggregate, snapshot); err != nil {
		return err
	}

	if err := s.writeEvidence(ctx, tx, aggregate, snapshot); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("platform: save evaluation evidence: %w", err)
	}
	return nil
}

// validateEvidencePair checks the two halves describe one run and one moment.
func validateEvidencePair(aggregate EvaluationAggregate, snapshot BehaviorSnapshot) error {
	if !aggregate.bound {
		return fmt.Errorf("%w: aggregate did not come from NewEvaluationAggregate", ErrUnboundAggregate)
	}
	if !snapshot.bound {
		return fmt.Errorf("%w: snapshot did not come from a BehaviorCollector", ErrUnboundCollector)
	}

	if aggregate.RunID() != snapshot.RunID() ||
		aggregate.CandidateID() != snapshot.CandidateID() ||
		aggregate.Environment() != snapshot.Environment() ||
		aggregate.BehavioralProfile() != snapshot.BehavioralProfile() {
		return fmt.Errorf("%w: aggregate and snapshot describe different evaluations",
			ErrStoreConflict)
	}

	switch {
	case snapshot.Complete():
		// Both consumed the same stream, so the counts must agree.
		if aggregate.RecordCount() != snapshot.ObservationCount() {
			return fmt.Errorf(
				"%w: complete snapshot observed %d records, aggregate observed %d",
				ErrStoreConflict, snapshot.ObservationCount(), aggregate.RecordCount())
		}
	default:
		// Task 054 allows a saturated snapshot for diagnostics, and depending
		// on how the caller handled the capacity error it may hold fewer
		// observations than the aggregate. Equality is wrong here; going
		// *over* the aggregate still is not.
		if snapshot.ObservationCount() > aggregate.RecordCount() {
			return fmt.Errorf(
				"%w: incomplete snapshot observed %d records, more than the aggregate's %d",
				ErrStoreConflict, snapshot.ObservationCount(), aggregate.RecordCount())
		}
	}
	return nil
}

// checkEvidenceNotStale refuses writes that would move evidence backwards.
func checkEvidenceNotStale(
	ctx context.Context, q evidenceQuerier,
	aggregate EvaluationAggregate, snapshot BehaviorSnapshot,
) error {
	present, err := evidenceRowsPresent(ctx, q, aggregate.RunID())
	if err != nil {
		return err
	}
	switch {
	case present.neither():
		return nil // genuinely the first evidence for this run
	case !present.both():
		// Half a pair. Writing over it would complete the record and destroy
		// the only sign that something went wrong, so it is refused and left
		// exactly as found for an explicit recovery decision.
		return partialEvidenceError(aggregate.RunID(), present)
	}

	storedAggregate, storedSnapshot, err := loadEvidence(ctx, q, aggregate.RunID())
	if err != nil {
		return err
	}

	// Saturation is sticky: once durable evidence records that the collector
	// overflowed, a later write cannot restore the run to complete. That is a
	// fact about what was observed, not a state to be corrected.
	if !storedSnapshot.Complete() && snapshot.Complete() {
		return fmt.Errorf("%w: run %s already recorded an incomplete snapshot",
			ErrStoreConflict, preview(string(aggregate.RunID())))
	}

	switch {
	case aggregate.RecordCount() < storedAggregate.RecordCount():
		return fmt.Errorf("%w: incoming evidence observed %d records, stored evidence %d",
			ErrStoreConflict, aggregate.RecordCount(), storedAggregate.RecordCount())

	case aggregate.RecordCount() == storedAggregate.RecordCount():
		// Identical is an idempotent retry; divergent is two different views
		// of the same moment, and picking one silently would be a guess.
		if !sameAggregate(storedAggregate, aggregate) || !sameSnapshot(storedSnapshot, snapshot) {
			return fmt.Errorf("%w: different evidence already stored at %d records",
				ErrStoreConflict, aggregate.RecordCount())
		}
	}
	return nil
}

func (s *SQLiteStore) writeEvidence(
	ctx context.Context, tx *sql.Tx,
	a EvaluationAggregate, snapshot BehaviorSnapshot,
) error {
	// Columns and args both come from the shared list, so this backend and
	// PostgreSQL cannot disagree about the aggregate's forty-four columns.
	columns := aggregateInsertColumns()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableAggregates+` (`+strings.Join(columns, ", ")+`)
		 VALUES (`+placeholders(len(columns))+`)
		 ON CONFLICT(run_id) DO UPDATE SET `+onConflictAssignments(columns[1:]),
		aggregateInsertArgs(a)...); err != nil {
		return fmt.Errorf("platform: write aggregate: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableSnapshots+`
		 (run_id, candidate_id, environment, behavioral_profile,
		  observation_count, distinct_count, complete)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			observation_count = excluded.observation_count,
			distinct_count = excluded.distinct_count,
			complete = excluded.complete`,
		string(snapshot.RunID()), string(snapshot.CandidateID()),
		string(snapshot.Environment()), string(snapshot.BehavioralProfile()),
		uint64Text(snapshot.ObservationCount()), snapshot.DistinctBehaviorCount(),
		boolInt(snapshot.Complete())); err != nil {
		return fmt.Errorf("platform: store snapshot: %w", err)
	}

	// Replace the bounded entry set wholesale. This is the one cascade in the
	// schema, and it is private to an atomic evidence replacement rather than
	// product-visible deletion.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM `+tableEntries+` WHERE run_id = ?`, string(snapshot.RunID())); err != nil {
		return fmt.Errorf("platform: replace behavior entries: %w", err)
	}

	for _, entry := range snapshot.Entries() {
		b := entry.Behavior
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+tableEntries+`
			 (run_id, fingerprint_id, actor_type, operation_category, operation_name,
			  target_name, target_category, environment, observations)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			string(snapshot.RunID()), entry.FingerprintID,
			string(b.ActorType), string(b.OperationCategory), b.OperationName,
			b.TargetName, string(b.TargetCategory), b.Environment,
			uint64Text(entry.Observations)); err != nil {
			return fmt.Errorf("platform: store behavior entry: %w", err)
		}
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// withReadTx runs a multi-query read inside one transaction.
//
// Loading evidence issues several queries — aggregate, snapshot header,
// entries, and the owning run. Outside a transaction each is its own implicit
// one, so a concurrent ingest committing between them tears the read: a
// snapshot from after the write beside an aggregate from before it, which the
// consistency checks then correctly report as corruption.
//
// The store holds a single connection, so this also serializes against
// writers. That is a real cost and the right one: the alternative is reads
// that occasionally invent an inconsistency that never existed durably.
func (s *SQLiteStore) withReadTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("platform: begin read: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a read transaction is always rolled back
	return fn(tx)
}

// EvaluationEvidence loads one run's latest aggregate and snapshot.
func (s *SQLiteStore) EvaluationEvidence(
	ctx context.Context, id EvaluationRunID,
) (EvaluationAggregate, BehaviorSnapshot, error) {
	var aggregate EvaluationAggregate
	var snapshot BehaviorSnapshot
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		aggregate, snapshot, err = loadEvidence(ctx, sqlQuerier{tx}, id)
		return err
	})
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	return aggregate, snapshot, nil
}

// evidencePresence is which halves of a run's evidence exist, established
// without reading or trusting their content.
//
// It exists because "no aggregate row" is not proof that a run has no
// evidence. Exactly one half present is a partial write or a partial restore,
// and treating it as first-write state would let the next save quietly
// complete the pair and erase every trace that anything went wrong.
type evidencePresence struct {
	aggregate bool
	snapshot  bool
}

func (p evidencePresence) both() bool    { return p.aggregate && p.snapshot }
func (p evidencePresence) neither() bool { return !p.aggregate && !p.snapshot }

func evidenceRowsPresent(ctx context.Context, q rowQuerier, id EvaluationRunID) (evidencePresence, error) {
	exists := func(table string) (bool, error) {
		var one int
		err := q.queryRow(ctx,
			q.rebind(`SELECT 1 FROM `+table+` WHERE run_id = ?`), string(id)).Scan(&one)
		switch {
		case q.noRows(err):
			return false, nil
		case err != nil:
			return false, fmt.Errorf("platform: inspect evidence: %w", err)
		}
		return true, nil
	}

	aggregate, err := exists(tableAggregates)
	if err != nil {
		return evidencePresence{}, err
	}
	snapshot, err := exists(tableSnapshots)
	if err != nil {
		return evidencePresence{}, err
	}
	return evidencePresence{aggregate: aggregate, snapshot: snapshot}, nil
}

// partialEvidenceError describes a half-present pair. Corruption, never
// "not found": the run demonstrably has evidence, and it cannot be trusted.
func partialEvidenceError(id EvaluationRunID, p evidencePresence) error {
	held, missing := "an aggregate", "snapshot"
	if p.snapshot {
		held, missing = "a snapshot", "aggregate"
	}
	return fmt.Errorf("%w: run %s holds %s with no %s",
		ErrStoreCorrupt, preview(string(id)), held, missing)
}

// loadEvidence returns trusted values only when all seven conditions hold:
// both rows exist; the aggregate validates on its own; the snapshot and its
// entries validate on their own; the two agree with each other; and both
// agree with the persisted EvaluationRun. Otherwise nothing bound escapes.
func loadEvidence(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID,
) (EvaluationAggregate, BehaviorSnapshot, error) {
	present, err := evidenceRowsPresent(ctx, q, id)
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	switch {
	case present.neither():
		return EvaluationAggregate{}, BehaviorSnapshot{}, fmt.Errorf(
			"%w: evidence for run %s", ErrStoreNotFound, preview(string(id)))
	case !present.both():
		return EvaluationAggregate{}, BehaviorSnapshot{}, partialEvidenceError(id, present)
	}

	aggregate, err := loadAggregate(ctx, q, id)
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	snapshot, err := loadSnapshot(ctx, q, id)
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}

	// The pair must still agree after restoration. A stored aggregate and a
	// stored snapshot that disagree were never written together by this code.
	if err := validateEvidencePair(aggregate, snapshot); err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, fmt.Errorf(
			"%w: stored evidence for run %s is inconsistent: %w",
			ErrStoreCorrupt, preview(string(id)), err)
	}

	// And with the run itself. Two halves agreeing with each other proves
	// only that they were edited consistently — the authoritative statement
	// of what this run is lives in the runs table, and evidence that
	// contradicts it describes some other evaluation.
	run, err := loadRun(ctx, q, id)
	if err != nil {
		if errors.Is(err, ErrStoreNotFound) {
			return EvaluationAggregate{}, BehaviorSnapshot{}, fmt.Errorf(
				"%w: run %s holds evidence but the run itself is missing",
				ErrStoreCorrupt, preview(string(id)))
		}
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	if err := validateRestoredEvidenceAgainstRun(run, aggregate, snapshot); err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	return aggregate, snapshot, nil
}

// validateRestoredEvidenceAgainstRun binds restored evidence back to the run
// it claims to describe.
//
// Read-path corruption, not a write conflict: ErrStoreConflict describes a
// valid caller operation racing current durable state, and nothing here was
// produced by a caller operation at all.
func validateRestoredEvidenceAgainstRun(
	run EvaluationRun, aggregate EvaluationAggregate, snapshot BehaviorSnapshot,
) error {
	for _, f := range []struct {
		name                          string
		runValue, aggValue, snapValue string
	}{
		{"run id", string(run.ID()), string(aggregate.RunID()), string(snapshot.RunID())},
		{"candidate id", string(run.CandidateID()), string(aggregate.CandidateID()), string(snapshot.CandidateID())},
		{"environment", string(run.Environment()), string(aggregate.Environment()), string(snapshot.Environment())},
		{"behavioral profile", string(run.BehavioralProfile()),
			string(aggregate.BehavioralProfile()), string(snapshot.BehavioralProfile())},
	} {
		if f.aggValue != f.runValue || f.snapValue != f.runValue {
			return fmt.Errorf(
				"%w: run %s records %s %s, but its stored evidence says %s/%s",
				ErrStoreCorrupt, preview(string(run.ID())), f.name,
				preview(f.runValue), preview(f.aggValue), preview(f.snapValue))
		}
	}
	return nil
}

// sameAggregate compares every persisted field.
func sameAggregate(a, b EvaluationAggregate) bool {
	return a.RunID() == b.RunID() &&
		a.CandidateID() == b.CandidateID() &&
		a.Environment() == b.Environment() &&
		a.BehavioralProfile() == b.BehavioralProfile() &&
		a.RecordCount() == b.RecordCount() &&
		a.FirstObservedAt().Equal(b.FirstObservedAt()) &&
		a.LastObservedAt().Equal(b.LastObservedAt()) &&
		a.Decisions() == b.Decisions() &&
		a.Risks() == b.Risks() &&
		a.Approvals() == b.Approvals() &&
		a.PolicySelection() == b.PolicySelection() &&
		a.IdentityConfidence() == b.IdentityConfidence() &&
		a.AnomalyScore() == b.AnomalyScore() &&
		a.AnomalyConfidence() == b.AnomalyConfidence() &&
		a.TrustScore() == b.TrustScore() &&
		a.ContextRisk() == b.ContextRisk()
}

// sameSnapshot compares identity, counts, completeness and every entry.
func sameSnapshot(a, b BehaviorSnapshot) bool {
	if a.RunID() != b.RunID() ||
		a.CandidateID() != b.CandidateID() ||
		a.Environment() != b.Environment() ||
		a.BehavioralProfile() != b.BehavioralProfile() ||
		a.ObservationCount() != b.ObservationCount() ||
		a.Complete() != b.Complete() {
		return false
	}
	return slices.Equal(a.entries, b.entries)
}

// ---------------------------------------------------------------------
// Restoration, with validation before the bound marker
// ---------------------------------------------------------------------

// loadAggregate restores an aggregate, validating before setting bound.
//
// The marker is what tasks 053-056 trust. Setting it on unvalidated storage
// would make every downstream guarantee conditional on the database being
// undamaged, so every invariant a live aggregate satisfies is re-proved here.
func loadAggregate(ctx context.Context, q rowQuerier, id EvaluationRunID) (EvaluationAggregate, error) {
	var candidateID, environment, profile, recordCount string
	var firstObserved, lastObserved sql.NullString
	counts := make([]string, 17)
	metricCounts := make([]string, 5)
	metricValues := make([]float64, 15)

	dest := []any{
		&candidateID, &environment, &profile, &recordCount, &firstObserved, &lastObserved,
	}
	for i := range counts {
		dest = append(dest, &counts[i])
	}
	for i := range 5 {
		dest = append(dest, &metricCounts[i],
			&metricValues[i*3], &metricValues[i*3+1], &metricValues[i*3+2])
	}

	err := q.queryRow(ctx,
		q.rebind(`SELECT candidate_id, environment, behavioral_profile,
		        record_count, first_observed_at, last_observed_at,
		        decision_allow, decision_observe_only, decision_alert,
		        decision_challenge, decision_require_approval, decision_block,
		        risk_low, risk_medium, risk_high, risk_critical,
		        approval_unspecified, approval_not_required, approval_required,
		        approval_approved, approval_denied,
		        policy_matched_rule, policy_matched_default,
		        identity_confidence_count, identity_confidence_sum, identity_confidence_min, identity_confidence_max,
		        anomaly_score_count, anomaly_score_sum, anomaly_score_min, anomaly_score_max,
		        anomaly_confidence_count, anomaly_confidence_sum, anomaly_confidence_min, anomaly_confidence_max,
		        trust_score_count, trust_score_sum, trust_score_min, trust_score_max,
		        context_risk_count, context_risk_sum, context_risk_min, context_risk_max
		 FROM `+tableAggregates+` WHERE run_id = ?`), string(id)).Scan(dest...)
	if q.noRows(err) {
		return EvaluationAggregate{}, fmt.Errorf("%w: evidence for run %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return EvaluationAggregate{}, fmt.Errorf("platform: load aggregate: %w", err)
	}

	parsed := make([]uint64, len(counts))
	for i, raw := range counts {
		if parsed[i], err = parseUint64Text("aggregate count", raw); err != nil {
			return EvaluationAggregate{}, err
		}
	}
	records, err := parseUint64Text("record_count", recordCount)
	if err != nil {
		return EvaluationAggregate{}, err
	}

	firstObservedAt, err := parseNullTimeText("first_observed_at", firstObserved)
	if err != nil {
		return EvaluationAggregate{}, err
	}
	lastObservedAt, err := parseNullTimeText("last_observed_at", lastObserved)
	if err != nil {
		return EvaluationAggregate{}, err
	}

	metrics := make([]MetricSummary, 5)
	for i := range metrics {
		count, err := parseUint64Text("metric count", metricCounts[i])
		if err != nil {
			return EvaluationAggregate{}, err
		}
		metrics[i] = MetricSummary{
			Count: count,
			Sum:   metricValues[i*3],
			Min:   metricValues[i*3+1],
			Max:   metricValues[i*3+2],
		}
	}

	aggregate := EvaluationAggregate{
		runID:       id,
		candidateID: CandidateID(candidateID),
		environment: EnvironmentRef(environment),
		profile:     BehavioralProfileRef(profile),

		recordCount:     records,
		firstObservedAt: firstObservedAt,
		lastObservedAt:  lastObservedAt,

		decisions: DecisionCounts{
			Allow: parsed[0], ObserveOnly: parsed[1], Alert: parsed[2],
			Challenge: parsed[3], RequireApproval: parsed[4], Block: parsed[5],
		},
		risks: RiskCounts{
			Low: parsed[6], Medium: parsed[7], High: parsed[8], Critical: parsed[9],
		},
		approvals: ApprovalCounts{
			Unspecified: parsed[10], NotRequired: parsed[11], Required: parsed[12],
			Approved: parsed[13], Denied: parsed[14],
		},
		policy: PolicySelection{MatchedRule: parsed[15], MatchedDefault: parsed[16]},

		identityConfidence: metrics[0],
		anomalyScore:       metrics[1],
		anomalyConfidence:  metrics[2],
		trustScore:         metrics[3],
		contextRisk:        metrics[4],
	}

	if err := validateRestoredAggregate(aggregate); err != nil {
		return EvaluationAggregate{}, fmt.Errorf("%w: aggregate for run %s: %w",
			ErrStoreCorrupt, preview(string(id)), err)
	}

	aggregate.bound = true
	return aggregate, nil
}

// validateRestoredAggregate re-proves every invariant a live aggregate holds.
func validateRestoredAggregate(a EvaluationAggregate) error {
	for _, f := range []struct{ field, value string }{
		{"run id", string(a.runID)},
		{"candidate id", string(a.candidateID)},
		{"environment", string(a.environment)},
		{"behavioral profile", string(a.profile)},
	} {
		if err := validateID(f.field, f.value); err != nil {
			return err
		}
	}

	// Overflow-safe: each Total() could wrap, so compare against the record
	// count only after confirming the parts cannot exceed it.
	for _, g := range []struct {
		name  string
		parts []uint64
	}{
		{"decision", []uint64{a.decisions.Allow, a.decisions.ObserveOnly, a.decisions.Alert,
			a.decisions.Challenge, a.decisions.RequireApproval, a.decisions.Block}},
		{"risk", []uint64{a.risks.Low, a.risks.Medium, a.risks.High, a.risks.Critical}},
		{"approval", []uint64{a.approvals.Unspecified, a.approvals.NotRequired,
			a.approvals.Required, a.approvals.Approved, a.approvals.Denied}},
		{"policy selection", []uint64{a.policy.MatchedRule, a.policy.MatchedDefault}},
	} {
		total, err := sumNoOverflow(g.parts)
		if err != nil {
			return fmt.Errorf("%s counts overflow", g.name)
		}
		if total != a.recordCount {
			return fmt.Errorf("%s counts total %d, record count is %d", g.name, total, a.recordCount)
		}
	}

	for _, m := range []struct {
		name    string
		summary MetricSummary
	}{
		{"identity confidence", a.identityConfidence},
		{"anomaly score", a.anomalyScore},
		{"anomaly confidence", a.anomalyConfidence},
		{"trust score", a.trustScore},
		{"context risk", a.contextRisk},
	} {
		if err := validateRestoredMetric(m.name, m.summary, a.recordCount); err != nil {
			return err
		}
	}

	if a.recordCount == 0 {
		if !a.firstObservedAt.IsZero() || !a.lastObservedAt.IsZero() {
			return errors.New("empty aggregate carries observation timestamps")
		}
		return nil
	}
	if a.firstObservedAt.IsZero() || a.lastObservedAt.IsZero() {
		return errors.New("non-empty aggregate is missing observation timestamps")
	}
	if a.lastObservedAt.Before(a.firstObservedAt) {
		return errors.New("last observation precedes the first")
	}
	return nil
}

func validateRestoredMetric(name string, m MetricSummary, records uint64) error {
	if m.Count != records {
		return fmt.Errorf("%s count %d does not match record count %d", name, m.Count, records)
	}
	// NaN and Inf are never accepted from storage: they propagate silently
	// through anything that computes with them.
	for _, v := range []struct {
		field string
		value float64
	}{{"sum", m.Sum}, {"min", m.Min}, {"max", m.Max}} {
		if math.IsNaN(v.value) || math.IsInf(v.value, 0) {
			return fmt.Errorf("%s %s is not finite", name, v.field)
		}
	}
	if m.Count == 0 {
		if m.Sum != 0 || m.Min != 0 || m.Max != 0 {
			return fmt.Errorf("%s has no observations but non-zero statistics", name)
		}
		return nil
	}
	// Every aggregated signal is a unit-interval value; task 053 validates
	// that on the way in, so storage must not widen it on the way out.
	if m.Min < 0 || m.Min > 1 || m.Max < 0 || m.Max > 1 {
		return fmt.Errorf("%s extremes outside [0,1]", name)
	}
	if m.Min > m.Max {
		return fmt.Errorf("%s minimum exceeds its maximum", name)
	}
	if m.Sum < 0 {
		return fmt.Errorf("%s sum is negative", name)
	}
	return nil
}

func sumNoOverflow(parts []uint64) (uint64, error) {
	var total uint64
	for _, p := range parts {
		if total > math.MaxUint64-p {
			return 0, errors.New("overflow")
		}
		total += p
	}
	return total, nil
}

// loadSnapshot restores a snapshot, validating before setting bound.
func loadSnapshot(ctx context.Context, q evidenceQuerier, id EvaluationRunID) (BehaviorSnapshot, error) {
	var candidateID, environment, profile, observationCount string
	var distinctCount, complete int

	err := q.queryRow(ctx, q.rebind(
		`SELECT candidate_id, environment, behavioral_profile,
		        observation_count, distinct_count, complete
		 FROM `+tableSnapshots+` WHERE run_id = ?`), string(id)).
		Scan(&candidateID, &environment, &profile, &observationCount, &distinctCount, &complete)
	if q.noRows(err) {
		return BehaviorSnapshot{}, fmt.Errorf("%w: snapshot for run %s", ErrStoreNotFound, preview(string(id)))
	}
	if err != nil {
		return BehaviorSnapshot{}, fmt.Errorf("platform: load snapshot: %w", err)
	}

	observations, err := parseUint64Text("observation_count", observationCount)
	if err != nil {
		return BehaviorSnapshot{}, err
	}

	entries, err := loadBehaviorEntries(ctx, q, id)
	if err != nil {
		return BehaviorSnapshot{}, err
	}

	completeFlag, err := parseStoredBool("snapshot complete", complete)
	if err != nil {
		return BehaviorSnapshot{}, err
	}

	snapshot := BehaviorSnapshot{
		runID:        id,
		candidateID:  CandidateID(candidateID),
		environment:  EnvironmentRef(environment),
		profile:      BehavioralProfileRef(profile),
		observations: observations,
		entries:      entries,
		complete:     completeFlag,
	}

	if err := validateRestoredSnapshot(snapshot, distinctCount); err != nil {
		return BehaviorSnapshot{}, fmt.Errorf("%w: snapshot for run %s: %w",
			ErrStoreCorrupt, preview(string(id)), err)
	}

	snapshot.bound = true
	return snapshot, nil
}

// loadBehaviorEntries reads a run's entries in ascending FingerprintID order,
// matching what Entries() guarantees for a live snapshot.
func loadBehaviorEntries(ctx context.Context, q evidenceQuerier, id EvaluationRunID) ([]BehaviorEntry, error) {
	rows, err := q.query(ctx, q.rebind(
		`SELECT fingerprint_id, actor_type, operation_category, operation_name,
		        target_name, target_category, environment, observations
		 FROM `+tableEntries+` WHERE run_id = ? ORDER BY fingerprint_id ASC`), string(id))
	if err != nil {
		return nil, fmt.Errorf("platform: load behavior entries: %w", err)
	}
	defer rows.Close()

	var entries []BehaviorEntry
	for rows.Next() {
		var entry BehaviorEntry
		var actorType, operationCategory, targetCategory, observations string

		if err := rows.Scan(&entry.FingerprintID, &actorType, &operationCategory,
			&entry.Behavior.OperationName, &entry.Behavior.TargetName,
			&targetCategory, &entry.Behavior.Environment, &observations); err != nil {
			return nil, fmt.Errorf("platform: load behavior entries: %w", err)
		}

		entry.Behavior.ActorType = event.ActorType(actorType)
		entry.Behavior.OperationCategory = event.OperationCategory(operationCategory)
		entry.Behavior.TargetCategory = event.TargetCategory(targetCategory)

		if entry.Observations, err = parseUint64Text("entry observations", observations); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("platform: load behavior entries: %w", err)
	}
	return entries, nil
}

// validateRestoredSnapshot re-proves every invariant a live snapshot holds.
func validateRestoredSnapshot(s BehaviorSnapshot, storedDistinctCount int) error {
	for _, f := range []struct{ field, value string }{
		{"run id", string(s.runID)},
		{"candidate id", string(s.candidateID)},
		{"environment", string(s.environment)},
		{"behavioral profile", string(s.profile)},
	} {
		if err := validateID(f.field, f.value); err != nil {
			return err
		}
	}

	if len(s.entries) > maxBehaviorEntries {
		return fmt.Errorf("holds %d entries, more than the %d bound",
			len(s.entries), maxBehaviorEntries)
	}
	if len(s.entries) != storedDistinctCount {
		return fmt.Errorf("header records %d distinct behaviors, %d entries stored",
			storedDistinctCount, len(s.entries))
	}

	seenFingerprints := make(map[string]struct{}, len(s.entries))
	seenBehaviors := make(map[trustvian.StableFeatures]string, len(s.entries))
	var observed uint64

	for i, entry := range s.entries {
		if err := validateBehaviorIdentity(entry.FingerprintID, entry.Behavior, ""); err != nil {
			return err
		}
		if entry.Observations == 0 {
			return fmt.Errorf("entry %s has no observations", preview(entry.FingerprintID))
		}
		// An entry describing a different environment than the snapshot it
		// belongs to is evidence from two runs stitched together.
		if entry.Behavior.Environment != string(s.environment) {
			return fmt.Errorf("entry %s is from environment %q, snapshot is %q",
				preview(entry.FingerprintID), preview(entry.Behavior.Environment),
				preview(string(s.environment)))
		}
		if i > 0 && s.entries[i-1].FingerprintID >= entry.FingerprintID {
			return errors.New("entries are not sorted by fingerprint id")
		}
		if _, dup := seenFingerprints[entry.FingerprintID]; dup {
			return fmt.Errorf("duplicate fingerprint %s", preview(entry.FingerprintID))
		}
		seenFingerprints[entry.FingerprintID] = struct{}{}

		// Task 054's identity relation runs both ways: one behavior cannot
		// appear under two fingerprints, or a diff would report it as both
		// added and removed.
		if other, dup := seenBehaviors[entry.Behavior]; dup {
			return fmt.Errorf("fingerprints %s and %s describe the same behavior",
				preview(other), preview(entry.FingerprintID))
		}
		seenBehaviors[entry.Behavior] = entry.FingerprintID

		if observed > math.MaxUint64-entry.Observations {
			return errors.New("entry observation counts overflow")
		}
		observed += entry.Observations
	}

	// Exact equality, for complete and incomplete snapshots alike.
	//
	// This is a property of the collector, not of completeness. Observations
	// are counted only after every check has passed, and the saturation path
	// returns before that point — so a refused record increments nothing, and
	// once saturated the collector refuses immediately. Every live snapshot
	// therefore satisfies sum(entries) == ObservationCount exactly.
	//
	// Completeness shows up one layer up instead, between the snapshot and
	// the aggregate: the aggregate may have consumed a record the collector
	// refused, which is why validateEvidencePair allows the snapshot to have
	// observed fewer records than the aggregate. Allowing the same slack
	// *inside* the snapshot would accept arithmetic no collector can produce.
	if observed != s.observations {
		return fmt.Errorf("entries account for %d observations, header records %d",
			observed, s.observations)
	}
	return nil
}

// ---------------------------------------------------------------------
// Evaluation ingest cursor
// ---------------------------------------------------------------------

var _ EvaluationIngestStore = (*SQLiteStore)(nil)

// EvaluationIngestState returns a run's ingest cursor.
//
// Three durable shapes reach here, and they mean different things:
//
//	cursor row present      → the recorded sequence and digest
//	no cursor, no evidence  → a fresh run; next sequence is 1
//	no cursor, has evidence → a task 057 database; next sequence is
//	                          RecordCount+1, with no digest to replay against
//
// The last is the migration edge. One ingest is one aggregate record, so the
// arithmetic is what lets ingest continue against legacy evidence instead of
// restarting the count and double-aggregating everything already stored.
func (s *SQLiteStore) EvaluationIngestState(
	ctx context.Context, id EvaluationRunID,
) (EvaluationIngestState, error) {
	var state EvaluationIngestState
	err := s.withReadTx(ctx, func(tx *sql.Tx) error {
		var err error
		state, err = ingestState(ctx, sqlQuerier{tx}, id)
		return err
	})
	if err != nil {
		return EvaluationIngestState{}, err
	}
	return state, nil
}

func ingestState(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID,
) (EvaluationIngestState, error) {
	// The run must exist: a cursor for a run that does not is not a fresh
	// start, it is a dangling reference.
	if _, err := loadRun(ctx, q, id); err != nil {
		return EvaluationIngestState{}, err
	}

	var nextSequence, lastDigest string
	err := q.queryRow(ctx, q.rebind(
		`SELECT next_sequence, last_digest FROM `+tableIngestState+` WHERE run_id = ?`),
		string(id)).Scan(&nextSequence, &lastDigest)

	switch {
	case q.noRows(err):
		return derivedIngestState(ctx, q, id)
	case err != nil:
		return EvaluationIngestState{}, fmt.Errorf("platform: load ingest state: %w", err)
	}

	sequence, err := parseUint64Text("next_sequence", nextSequence)
	if err != nil {
		return EvaluationIngestState{}, err
	}
	if sequence == 0 {
		return EvaluationIngestState{}, fmt.Errorf(
			"%w: run %s has ingest sequence 0", ErrStoreCorrupt, preview(string(id)))
	}
	// A row here was written by the commit path, which always records a
	// digest. An empty one cannot have been produced legitimately.
	if err := validatePersistedDigest("last_digest", lastDigest); err != nil {
		return EvaluationIngestState{}, err
	}

	// The cursor and the evidence it describes must agree. Disagreement means
	// one of them was written without the other, and there is no principled
	// way to choose which is right.
	aggregate, snapshot, err := loadEvidence(ctx, q, id)
	switch {
	case errors.Is(err, ErrStoreNotFound):
		return EvaluationIngestState{}, fmt.Errorf(
			"%w: run %s has an ingest cursor but no evidence", ErrStoreCorrupt, preview(string(id)))
	case err != nil:
		return EvaluationIngestState{}, err
	}
	expected, err := initialSequenceFor(aggregate.RecordCount())
	if err != nil {
		return EvaluationIngestState{}, fmt.Errorf("%w: run %s: %w",
			ErrStoreCorrupt, preview(string(id)), err)
	}
	if sequence != expected {
		return EvaluationIngestState{}, fmt.Errorf(
			"%w: run %s cursor expects sequence %d but holds %d records",
			ErrStoreCorrupt, preview(string(id)), sequence, aggregate.RecordCount())
	}

	return EvaluationIngestState{
		nextSequence:             sequence,
		lastDigest:               lastDigest,
		recordCount:              aggregate.RecordCount(),
		behaviorObservationCount: snapshot.ObservationCount(),
		distinctBehaviorCount:    snapshot.DistinctBehaviorCount(),
		behaviorComplete:         snapshot.Complete(),
	}, nil
}

// derivedIngestState computes a cursor for a run that has none.
func derivedIngestState(
	ctx context.Context, q evidenceQuerier, id EvaluationRunID,
) (EvaluationIngestState, error) {
	aggregate, snapshot, err := loadEvidence(ctx, q, id)
	switch {
	case errors.Is(err, ErrStoreNotFound):
		// A run that has never ingested: no cursor, no evidence, and an empty
		// snapshot is complete rather than saturated.
		return EvaluationIngestState{nextSequence: 1, behaviorComplete: true}, nil
	case err != nil:
		return EvaluationIngestState{}, err
	}

	next, err := initialSequenceFor(aggregate.RecordCount())
	if err != nil {
		return EvaluationIngestState{}, err
	}
	// No digest: this code never saw the record that produced the evidence,
	// and inventing one would manufacture proof a retry is identical. This is
	// the one legitimate empty digest in the system.
	return EvaluationIngestState{
		nextSequence:             next,
		recordCount:              aggregate.RecordCount(),
		behaviorObservationCount: snapshot.ObservationCount(),
		distinctBehaviorCount:    snapshot.DistinctBehaviorCount(),
		behaviorComplete:         snapshot.Complete(),
	}, nil
}

// CommitEvaluationIngest writes evidence and cursor in one transaction.
func (s *SQLiteStore) CommitEvaluationIngest(
	ctx context.Context, commit EvaluationIngestCommit,
) (EvaluationIngestCommitResult, error) {
	if err := validateEvidencePair(commit.Aggregate, commit.Snapshot); err != nil {
		return EvaluationIngestCommitResult{}, err
	}
	if err := validatePersistedDigest("record digest", commit.RecordDigest); err != nil {
		return EvaluationIngestCommitResult{}, err
	}
	next, err := nextSequenceAfter(commit.Sequence)
	if err != nil {
		return EvaluationIngestCommitResult{}, err
	}
	if commit.Sequence != commit.PreviousNextSequence {
		return EvaluationIngestCommitResult{}, fmt.Errorf(
			"%w: commit carries sequence %d against cursor %d",
			ErrIngestSequence, commit.Sequence, commit.PreviousNextSequence)
	}

	runID := commit.Aggregate.RunID()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return EvaluationIngestCommitResult{}, fmt.Errorf("platform: commit evaluation ingest: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // rollback after commit is a no-op

	// Re-read the cursor inside the transaction. Two requests racing the same
	// sequence both computed against the same view; only the one whose view
	// still matches durable state may apply.
	current, err := ingestState(ctx, sqlQuerier{tx}, runID)
	if err != nil {
		return EvaluationIngestCommitResult{}, err
	}

	if current.NextSequence() != commit.PreviousNextSequence {
		// The cursor moved while this request was computing evidence. That
		// has two causes and only this transaction can tell them apart.
		//
		// If the cursor advanced past exactly this sequence and records this
		// exact digest, another request committed the same logical record
		// first. That is the retry contract working, not a conflict —
		// reporting an error here would make a successful concurrent retry
		// indistinguishable from a real failure.
		//
		// Anything else moved for a different reason and stays a conflict.
		if current.NextSequence() == next && current.LastDigest() == commit.RecordDigest {
			return EvaluationIngestCommitResult{
				Disposition:      EvaluationIngestAlreadyCommitted,
				NextSequence:     current.NextSequence(),
				RecordCount:      current.RecordCount(),
				BehaviorComplete: current.BehaviorComplete(),
			}, nil
		}
		return EvaluationIngestCommitResult{}, fmt.Errorf(
			"%w: run %s now expects sequence %d, not %d",
			ErrIngestSequence, preview(string(runID)),
			current.NextSequence(), commit.PreviousNextSequence)
	}

	// The run must exist and agree, exactly as a direct evidence save requires.
	run, err := loadRun(ctx, sqlQuerier{tx}, runID)
	if err != nil {
		return EvaluationIngestCommitResult{}, err
	}

	// And it must still be running, checked *here* rather than only in the
	// service. The service's preflight happens before the evidence is
	// computed, so a completion committing in between would otherwise let
	// this write land after the run became terminal — leaving a completed
	// evaluation whose evidence kept growing. Whichever of the two commits
	// first wins; what must never happen is completion first, ingest after.
	if run.Status() != RunRunning {
		return EvaluationIngestCommitResult{}, fmt.Errorf(
			"%w: run %s is %s, records are accepted only while running",
			ErrEvaluationState, preview(string(runID)), run.Status())
	}

	if run.CandidateID() != commit.Aggregate.CandidateID() ||
		run.Environment() != commit.Aggregate.Environment() ||
		run.BehavioralProfile() != commit.Aggregate.BehavioralProfile() {
		return EvaluationIngestCommitResult{}, fmt.Errorf(
			"%w: evidence identity does not match stored run %s",
			ErrStoreConflict, preview(string(runID)))
	}

	if err := checkEvidenceNotStale(ctx, sqlQuerier{tx}, commit.Aggregate, commit.Snapshot); err != nil {
		return EvaluationIngestCommitResult{}, err
	}
	if err := s.writeEvidence(ctx, tx, commit.Aggregate, commit.Snapshot); err != nil {
		return EvaluationIngestCommitResult{}, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO `+tableIngestState+` (run_id, next_sequence, last_digest)
		 VALUES (?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET
			next_sequence = excluded.next_sequence,
			last_digest = excluded.last_digest`,
		string(runID), uint64Text(next), commit.RecordDigest); err != nil {
		return EvaluationIngestCommitResult{}, fmt.Errorf("platform: commit ingest cursor: %w", err)
	}

	// Evidence and cursor become durable together, or neither does.
	if err := tx.Commit(); err != nil {
		return EvaluationIngestCommitResult{}, fmt.Errorf("platform: commit evaluation ingest: %w", err)
	}

	return EvaluationIngestCommitResult{
		Disposition:      EvaluationIngestCommitted,
		NextSequence:     next,
		RecordCount:      commit.Aggregate.RecordCount(),
		BehaviorComplete: commit.Snapshot.Complete(),
	}, nil
}
