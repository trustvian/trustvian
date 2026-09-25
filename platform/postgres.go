package platform

// The shared/deployed platform persistence backend.
//
// Same package as sqlite.go, and that is forced rather than preferred:
// EvaluationAggregate and BehaviorSnapshot carry an unexported `bound` marker,
// restoring one means setting it, and only this package may. A subpackage would
// need a public evidence-forging constructor, which ADR 0030 refused. See
// docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md.
//
// What lives here is the part that genuinely differs from SQLite: the pool, the
// dialect's DDL, migration under an advisory lock, row locking, and SQLSTATE
// mapping. Everything that restores a domain value from rows is shared with
// SQLite through the read seam in querier.go, so the fifty-column aggregate
// mapping has exactly one definition and cannot drift.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStoreUnavailable reports a backend that could not answer.
//
// Distinct from the sentinels in store.go because it is about reachability
// rather than content: nothing is wrong with the request, and a caller may
// reasonably retry an idempotent one. It is never a conflict and never
// corruption, and conflating them would tell a caller to fix something that is
// not broken.
var ErrStoreUnavailable = errors.New("platform: persistence backend is unavailable")

// Postgres SQLSTATE codes this store interprets.
//
// Matched on the code, never on message text. A driver's English is not a
// contract, and keying on it is how an upgrade silently reclassifies a failure.
const (
	sqlStateUniqueViolation     = "23505"
	sqlStateForeignKeyViolation = "23503"
	sqlStateSerializationFail   = "40001"
	sqlStateDeadlockDetected    = "40P01"
)

// platformMigrationLockKey serializes concurrent platform migrations.
//
// A transaction-scoped advisory lock, so it is released by commit or rollback
// and a crashed migrator cannot wedge every later start.
//
// Deliberately distinct from internal/store/postgres's engine key
// (0x7275737476696E00, "trustvin\0"). PostgreSQL has one global advisory-lock
// space and no registry, so two subsystems sharing a key would serialize
// against each other for no reason — an engine migration would block a platform
// migration in any database holding both. The suffix names which subsystem owns
// it: "trustvpl" for the platform.
//
// TestPlatformAndEngineMigrationLocksDoNotCollide proves the two do not block
// one another rather than trusting the arithmetic.
const platformMigrationLockKey int64 = 0x7472757374767090 // "trustv" + platform marker

// PostgresConfig is what a PostgreSQL store needs.
//
// Mirrors internal/store/postgres's Config: every field beyond the DSN is a
// bound rather than a behaviour, and each has a working default.
type PostgresConfig struct {
	// DSN is the connection string. Required.
	//
	// It normally carries a password, so it is never logged, never returned in
	// an error, and never echoed back. See redactDSNError.
	DSN string

	// MaxConnections caps the pool. Zero means pgx's own default.
	MaxConnections int32

	// ConnectTimeout bounds the startup dial. Zero means
	// defaultPostgresConnectTimeout.
	ConnectTimeout time.Duration
}

// defaultPostgresConnectTimeout bounds how long startup waits to prove
// connectivity. Long enough for a cold server, short enough that a wrong host
// fails while somebody is still watching.
const defaultPostgresConnectTimeout = 10 * time.Second

// PostgresStore persists platform state in PostgreSQL.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// OpenPostgresStore connects, verifies, migrates, and returns a usable store.
//
// Nothing is returned until the database is proven usable: the pool is
// constructed, connectivity is proven with a Ping, and the schema is created,
// verified or migrated. A caller that receives a store can serve traffic with
// it — which is what lets the runtime bind its listener only after this
// returns.
func OpenPostgresStore(ctx context.Context, cfg PostgresConfig) (*PostgresStore, error) {
	if strings.TrimSpace(cfg.DSN) == "" {
		// No DSN text in the message: there is none worth quoting, and the
		// habit of quoting it is what leaks one later.
		return nil, fmt.Errorf("%w: no database DSN configured", ErrStoreCorrupt)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// pgx reproduces an unparseable DSN verbatim in its error, password
		// included. Never wrap it.
		return nil, redactDSNError("parse database configuration", err)
	}
	if cfg.MaxConnections > 0 {
		poolCfg.MaxConns = cfg.MaxConnections
	}

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultPostgresConnectTimeout
	}
	startupCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()

	// NewWithConfig is lazy and does not dial, so the Ping below is what
	// actually proves the database is there.
	pool, err := pgxpool.NewWithConfig(startupCtx, poolCfg)
	if err != nil {
		return nil, redactDSNError("connect to the database", err)
	}
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, redactDSNError("reach the database", err)
	}

	store := &PostgresStore{pool: pool}
	if err := store.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the pool.
func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

// querier returns the seam over the pool.
func (s *PostgresStore) querier() pgxQuerier { return pgxQuerier{q: s.pool} }

// ---------------------------------------------------------------------
// Error handling
// ---------------------------------------------------------------------

// redactDSNError wraps a connection-time failure without its input.
//
// The reason this exists rather than `fmt.Errorf("...: %w", err)`: pgx redacts
// the password when it can *parse* a DSN, and reproduces the string verbatim
// when it cannot. So the unparseable case — a typo, a stray quote — is exactly
// the one that would print a secret, and it is the case a caller is most likely
// to hit. internal/store/postgres carries the same regression test for the same
// empirically-confirmed behaviour.
//
// Only the action is reported. Nothing derived from the DSN survives.
func redactDSNError(action string, _ error) error {
	return fmt.Errorf("%w: could not %s; check the configured DSN", ErrStoreUnavailable, action)
}

// mapPostgresError translates a driver failure into a platform sentinel.
//
// kind and id describe what was being written, so a unique violation can say
// which identity already exists without quoting SQL, a constraint name, or a
// column.
func mapPostgresError(kind, id string, err error) error {
	if err == nil {
		return nil
	}
	// Cancellation is the caller's own decision and never a store condition.
	// Classifying it as a conflict would tell somebody to retry a request they
	// abandoned on purpose.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case sqlStateUniqueViolation:
			return fmt.Errorf("%w: %s %s", ErrStoreAlreadyExists, kind, preview(id))
		case sqlStateForeignKeyViolation:
			// A create naming a parent that does not exist is a missing
			// parent, which is what store.go's ErrStoreNotFound documents.
			return fmt.Errorf("%w: %s %s references something that does not exist",
				ErrStoreNotFound, kind, preview(id))
		case sqlStateSerializationFail, sqlStateDeadlockDetected:
			return fmt.Errorf("%w: %s %s could not be serialized against a "+
				"concurrent change", ErrStoreConflict, kind, preview(id))
		}
	}
	// Anything else is the backend failing to answer. Deliberately not wrapped
	// with err: a driver message can carry host, database, user and SQL, and
	// none of those belong in front of a caller.
	return fmt.Errorf("%w: %s %s", ErrStoreUnavailable, kind, preview(id))
}

// ---------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------

// withTx runs fn in a READ COMMITTED transaction, rolling back on any error.
//
// READ COMMITTED is PostgreSQL's default and is left alone. Every invariant
// this store needs is held by a row lock, a predicated update, or a
// constraint — raising isolation globally would instead make 40001 a normal
// outcome of ordinary work and push retry logic onto every caller, for a
// property two row locks already give.
func (s *PostgresStore) withTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return mapPostgresError("transaction", "", err)
	}
	// Rollback after a commit is a no-op, so one deferred call covers both
	// paths — including a panic, which must not leave a transaction open.
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return mapPostgresError("transaction", "", err)
	}
	return nil
}

// testHookInRunMutation runs between the in-transaction read and the write in
// every run-scoped mutation. Nil in production, and the call costs a nil check.
//
// It exists because a concurrency test cannot otherwise force the interleaving
// it is trying to prove. Against a local database one transaction finishes in
// microseconds, so eight goroutines released together still run essentially in
// series: the first commits before the second reads, every loser sees the new
// state, and the test reports the right answer no matter what the code does.
// That is a test passing for the wrong reason, and it hides exactly the defect
// it was written to catch — verified by removing both protections and watching
// it still pass.
//
// With this hook a test can hold every worker inside the read-to-write window
// at once, which is the state a pool makes possible and a fast database rarely
// produces on its own.
// An atomic pointer rather than a plain function variable. Today's tests run
// sequentially so a plain variable would not race, but that is a property of the
// current tests rather than of the seam — the first t.Parallel() added anywhere
// in this package would make installing a hook a write racing every reader. An
// atomic makes the seam safe by construction instead of by convention, and the
// cost is one atomic load on a path that already opens a transaction.
var testHookInRunMutation atomic.Pointer[func()]

// runMutationHook invokes the hook if a test installed one.
//
// Nil in production: nothing outside this package's tests can reach the variable
// above, it is never written by any non-test code path, and no configuration can
// install behaviour into it.
func runMutationHook() {
	if hook := testHookInRunMutation.Load(); hook != nil {
		(*hook)()
	}
}

// lockRun takes the run's row lock, which is this store's whole concurrency
// strategy for run-scoped writes.
//
// SQLite gets its atomicity from holding one connection: a read and the write
// that depends on it cannot interleave with another writer. A pool has no such
// property, so every read-then-write here would otherwise race — two callers
// would both read `created`, both pass a Go-side check, and both write.
//
// Locking the run row reproduces that serialization at per-run granularity
// instead of per-database, which is strictly more concurrency for identical
// semantics. The run row is the right thing to lock: it always exists (it is
// the foreign-key target for evidence, snapshots and the ingest cursor) and
// every one of these operations already reads it.
func lockRun(ctx context.Context, tx pgx.Tx, id EvaluationRunID) error {
	var locked string
	err := tx.QueryRow(ctx,
		`SELECT id FROM `+tableRuns+` WHERE id = $1 FOR UPDATE`, string(id)).Scan(&locked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: evaluation run %s", ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return mapPostgresError("evaluation run", string(id), err)
	}
	return nil
}

// ---------------------------------------------------------------------
// ControlStore
// ---------------------------------------------------------------------

func (s *PostgresStore) CreateProject(ctx context.Context, project Project) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO `+tableProjects+` (id, name) VALUES ($1, $2)`,
		string(project.ID()), project.Name())
	return mapPostgresError("project", string(project.ID()), err)
}

func (s *PostgresStore) Project(ctx context.Context, id ProjectID) (Project, error) {
	var name string
	err := s.pool.QueryRow(ctx,
		`SELECT name FROM `+tableProjects+` WHERE id = $1`, string(id)).Scan(&name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Project{}, fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return Project{}, mapPostgresError("project", string(id), err)
	}
	// Reconstructed through the domain constructor, so a row that cannot make a
	// valid Project is corruption rather than a partially trusted value.
	project, err := NewProject(id, name)
	if err != nil {
		return Project{}, fmt.Errorf("%w: project %s: %v", ErrStoreCorrupt, preview(string(id)), err)
	}
	return project, nil
}

func (s *PostgresStore) CreateAgent(ctx context.Context, agent Agent) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO `+tableAgents+` (id, project_id, name) VALUES ($1, $2, $3)`,
		string(agent.ID()), string(agent.ProjectID()), agent.Name())
	return mapPostgresError("agent", string(agent.ID()), err)
}

func (s *PostgresStore) Agent(ctx context.Context, id AgentID) (Agent, error) {
	var projectID, name string
	err := s.pool.QueryRow(ctx,
		`SELECT project_id, name FROM `+tableAgents+` WHERE id = $1`, string(id)).
		Scan(&projectID, &name)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Agent{}, fmt.Errorf("%w: agent %s", ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return Agent{}, mapPostgresError("agent", string(id), err)
	}
	agent, err := NewAgent(id, ProjectID(projectID), name)
	if err != nil {
		return Agent{}, fmt.Errorf("%w: agent %s: %v", ErrStoreCorrupt, preview(string(id)), err)
	}
	return agent, nil
}

func (s *PostgresStore) CreateCandidate(ctx context.Context, candidate Candidate) error {
	m := candidate.Metadata()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO `+tableCandidates+`
		 (id, agent_id, label, source_ref, artifact_digest, model,
		  toolset_digest, config_digest)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(candidate.ID()), string(candidate.AgentID()),
		m.Label, m.SourceRef, m.ArtifactDigest, m.Model,
		m.ToolsetDigest, m.ConfigDigest)
	return mapPostgresError("candidate", string(candidate.ID()), err)
}

func (s *PostgresStore) Candidate(ctx context.Context, id CandidateID) (Candidate, error) {
	var agentID string
	var m CandidateMetadata
	err := s.pool.QueryRow(ctx,
		`SELECT agent_id, label, source_ref, artifact_digest, model,
		        toolset_digest, config_digest
		 FROM `+tableCandidates+` WHERE id = $1`, string(id)).
		Scan(&agentID, &m.Label, &m.SourceRef, &m.ArtifactDigest, &m.Model,
			&m.ToolsetDigest, &m.ConfigDigest)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Candidate{}, fmt.Errorf("%w: candidate %s", ErrStoreNotFound, preview(string(id)))
	case err != nil:
		return Candidate{}, mapPostgresError("candidate", string(id), err)
	}
	candidate, err := NewCandidate(id, AgentID(agentID), m)
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: candidate %s: %v", ErrStoreCorrupt, preview(string(id)), err)
	}
	return candidate, nil
}

// ---------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------

// pgxTxQuerier adapts a pgx transaction to environmentWriter.
type pgxTxQuerier struct{ tx pgx.Tx }

func (q pgxTxQuerier) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	return q.tx.QueryRow(ctx, query, args...)
}

func (q pgxTxQuerier) noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func (q pgxTxQuerier) rebind(query string) string { return rebindPositional(query) }

func (q pgxTxQuerier) exec(ctx context.Context, query string, args ...any) (int64, error) {
	tag, err := q.tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// lockProject takes a row lock on the owning project for the rest of the
// transaction.
//
// SELECT … FOR UPDATE on the project row, which is the same shape lockRun
// already uses for per-run serialization and for the same reason: the
// invariant spans rows, so no predicate on the row being written can hold it.
// Locking the project rather than the table means creates in different
// projects never wait on each other.
// writeError maps PostgreSQL's SQLSTATE onto the shared sentinels, through
// the same helper every other write on this backend uses.
func (q pgxTxQuerier) writeError(kind, id string, err error) error {
	return mapPostgresError(kind, id, err)
}

func (q pgxTxQuerier) lockProject(ctx context.Context, projectID string) error {
	var locked string
	err := q.tx.QueryRow(ctx,
		`SELECT id FROM `+tableProjects+` WHERE id = $1 FOR UPDATE`, projectID).Scan(&locked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(projectID))
	case err != nil:
		return mapPostgresError("project", projectID, err)
	}
	return nil
}

// CreateEnvironment stores one environment under the cap, atomically.
//
// The count and the insert happen inside one transaction holding the project
// row, so two writers cannot each count 63 and each commit a different ref.
// Checks are ordered — missing project, existing identity, cap — and an
// existing ref is ErrStoreAlreadyExists whatever the project's count.
func (s *PostgresStore) CreateEnvironment(ctx context.Context, env Environment) error {
	if env.Ref() == "" || env.ProjectID() == "" {
		return fmt.Errorf("%w: environment has no identity", ErrInvalidID)
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		writer := pgxTxQuerier{tx}
		if err := writer.lockProject(ctx, string(env.ProjectID())); err != nil {
			return err
		}
		// Between the lock and the cap decision, so a test can measure how
		// many writers are inside the window at once rather than inferring
		// the lock from an outcome a lockless implementation also produces.
		if hook := testHookInEnvironmentCreate.Load(); hook != nil {
			(*hook)()
		}
		rank, ranked := env.Rank()
		return insertEnvironmentLocked(ctx, writer, env, rank, ranked)
	})
}

// testHookInEnvironmentCreate runs between the project lock and the cap
// decision. Nil in production, and the call costs one atomic load on a path
// that already opened a transaction.
//
// The same seam, and the same reasoning, as testHookInRunMutation: a local
// transaction commits in microseconds, so racers released together still run
// essentially in series and a test that only counted winners would pass with
// no lock at all. Occupancy inside this window is what FOR UPDATE actually
// changes.
var testHookInEnvironmentCreate atomic.Pointer[func()]

// Environment loads one environment by (project, ref).
func (s *PostgresStore) Environment(
	ctx context.Context, projectID ProjectID, ref EnvironmentRef,
) (Environment, error) {
	return loadEnvironment(ctx, s.querier(), projectID, ref)
}

// UpdateEnvironment replaces previous with next if the stored revision still
// matches previous's.
//
// One predicated UPDATE and no row lock: the predicate is the revision, so
// the database serializes concurrent writers without anything held across a
// round trip. This is the case task 064's locking rule explicitly does not
// cover, and it does not need to.
func (s *PostgresStore) UpdateEnvironment(ctx context.Context, previous, next Environment) error {
	if err := validateEnvironmentUpdate(previous, next); err != nil {
		return err
	}
	rank, ranked := next.Rank()
	tag, err := s.pool.Exec(ctx,
		`UPDATE `+tableEnvironments+`
		 SET name = $1, rank = $2, status = $3, revision = $4
		 WHERE project_id = $5 AND ref = $6 AND revision = $7`,
		next.Name(), nullRank(rank, ranked), string(next.Status()), int64(next.Revision()),
		string(previous.ProjectID()), string(previous.Ref()), int64(previous.Revision()))
	if err != nil {
		return mapPostgresError("environment", string(previous.Ref()), err)
	}
	if tag.RowsAffected() == 0 {
		return environmentUpdateMiss(ctx, s.querier(), previous)
	}
	return nil
}

// ProjectEnvironments returns one bounded page in ref byte order.
func (s *PostgresStore) ProjectEnvironments(
	ctx context.Context, projectID ProjectID, after EnvironmentRef, limit int,
) ([]Environment, error) {
	if err := validateEnvironmentPage(after, limit); err != nil {
		return nil, err
	}
	var exists string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM `+tableProjects+` WHERE id = $1`, string(projectID)).Scan(&exists)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(string(projectID)))
	case err != nil:
		return nil, mapPostgresError("project", string(projectID), err)
	}
	return queryEnvironmentPage(ctx, s.querier(), projectID, after, limit)
}

// ---------------------------------------------------------------------
// Hierarchy collections (task 074)
// ---------------------------------------------------------------------

// requireRowExists verifies a parent before a child collection is scanned.
//
// The same distinction SQLite's requireExists draws, in this dialect: a parent
// that does not exist is ErrStoreNotFound, and a parent with no children is an
// empty page. An empty array cannot say which happened.
func (s *PostgresStore) requireRowExists(ctx context.Context, table, kind, id string) error {
	var exists string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM `+table+` WHERE id = $1`, id).Scan(&exists)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: %s %s", ErrStoreNotFound, kind, preview(id))
	case err != nil:
		return mapPostgresError(kind, id, err)
	}
	return nil
}

// Projects returns one bounded page of projects in id byte order.
func (s *PostgresStore) Projects(
	ctx context.Context, after ProjectID, limit int,
) ([]Project, error) {
	if err := validateListPage("project", string(after), limit); err != nil {
		return nil, err
	}
	return queryProjectPage(ctx, s.querier(), after, limit)
}

// ProjectAgents returns one bounded page of a project's agents.
func (s *PostgresStore) ProjectAgents(
	ctx context.Context, projectID ProjectID, after AgentID, limit int,
) ([]Agent, error) {
	if err := validateListPage("agent", string(after), limit); err != nil {
		return nil, err
	}
	if err := s.requireRowExists(ctx, tableProjects, "project", string(projectID)); err != nil {
		return nil, err
	}
	return queryAgentPage(ctx, s.querier(), projectID, after, limit)
}

// AgentCandidates returns one bounded page of an agent's candidates.
func (s *PostgresStore) AgentCandidates(
	ctx context.Context, agentID AgentID, after CandidateID, limit int,
) ([]Candidate, error) {
	if err := validateListPage("candidate", string(after), limit); err != nil {
		return nil, err
	}
	if err := s.requireRowExists(ctx, tableAgents, "agent", string(agentID)); err != nil {
		return nil, err
	}
	return queryCandidatePage(ctx, s.querier(), agentID, after, limit)
}

// CandidateEvaluationRuns returns one bounded page of a candidate's runs.
func (s *PostgresStore) CandidateEvaluationRuns(
	ctx context.Context, candidateID CandidateID, after EvaluationRunID, limit int,
) ([]EvaluationRun, error) {
	if err := validateListPage("evaluation run", string(after), limit); err != nil {
		return nil, err
	}
	if err := s.requireRowExists(ctx, tableCandidates, "candidate", string(candidateID)); err != nil {
		return nil, err
	}
	return queryRunPage(ctx, s.querier(), candidateID, after, limit)
}

// ---------------------------------------------------------------------
// Promotions
// ---------------------------------------------------------------------

// lockEnvironment takes a row lock on one environment for the rest of the
// transaction.
//
// SELECT … FOR UPDATE on the environment row, issued once per environment so
// the acquisition order is a property of the code rather than of the query
// plan. A multi-row `ref = ANY(…) ORDER BY ref FOR UPDATE` would leave the
// locking order to the planner, and a specification that depends on a planner
// choice is not a specification.
//
// Two rows, not the project. Unlike task 065's creation cap — a cross-row
// invariant over all of a project's environments — this invariant names
// exactly two rows, so promotions in different projects, and over disjoint
// environment pairs, never wait on each other.
func (q pgxTxQuerier) lockEnvironment(ctx context.Context, projectID, ref string) error {
	var locked string
	err := q.tx.QueryRow(ctx,
		`SELECT ref FROM `+tableEnvironments+`
		 WHERE project_id = $1 AND ref = $2 FOR UPDATE`, projectID, ref).Scan(&locked)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: environment %s in project %s",
			ErrStoreNotFound, preview(ref), preview(projectID))
	case err != nil:
		return mapPostgresError("environment", ref, err)
	}
	return nil
}

// CreatePromotion stores one decision, revalidating the environment state it
// was built against.
//
// Both environment rows are locked in (project_id, ref) byte order before
// anything is read, so two promotions over an overlapping pair reach the
// shared row at the same point in their sequence and one waits rather than
// both proceeding against a state the other is changing. See
// promotionEnvironmentOrder for why the order comes from the rows rather than
// from which environment is the source.
func (s *PostgresStore) CreatePromotion(ctx context.Context, promotion Promotion) error {
	if promotion.ID() == "" || promotion.ProjectID() == "" {
		return fmt.Errorf("%w: promotion has no identity", ErrInvalidID)
	}

	return s.withTx(ctx, func(tx pgx.Tx) error {
		writer := pgxTxQuerier{tx}
		for i, ref := range promotionEnvironmentOrder(promotion) {
			if err := writer.lockEnvironment(
				ctx, string(promotion.ProjectID()), string(ref)); err != nil {
				return err
			}
			if hook := testHookAfterPromotionLock.Load(); hook != nil {
				(*hook)(i)
			}
		}
		return insertPromotionLocked(ctx, writer, promotion)
	})
}

// testHookAfterPromotionLock runs after each environment lock is taken, and is
// passed that lock's index in the acquisition order. Nil in production.
//
// The same seam, and the same reasoning, as testHookInEnvironmentCreate: a
// local transaction commits in microseconds, so racers released together still
// run essentially in series and a result-only test would pass with no locking
// at all. It carries the index because the two points prove different things —
// after index 0 exactly one of the pair is held, which is where a test can
// widen the acquisition window and observe what a second promotion over an
// overlapping pair does; after index 1 the whole revalidate-then-insert window
// is held, which is where occupancy above one would mean two promotions
// decided against the same environment state.
var testHookAfterPromotionLock atomic.Pointer[func(int)]

// Promotion loads one recorded decision by identifier.
func (s *PostgresStore) Promotion(ctx context.Context, id PromotionID) (Promotion, error) {
	if id == "" {
		return Promotion{}, fmt.Errorf("%w: promotion id is empty", ErrInvalidID)
	}
	return loadPromotion(ctx, s.querier(), id)
}

// ProjectPromotions returns one bounded page in identifier byte order.
func (s *PostgresStore) ProjectPromotions(
	ctx context.Context, projectID ProjectID, after PromotionID, limit int,
) ([]Promotion, error) {
	if err := validatePromotionPage(after, limit); err != nil {
		return nil, err
	}
	var exists string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM `+tableProjects+` WHERE id = $1`, string(projectID)).Scan(&exists)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("%w: project %s", ErrStoreNotFound, preview(string(projectID)))
	case err != nil:
		return nil, mapPostgresError("project", string(projectID), err)
	}
	return queryPromotionPage(ctx, s.querier(), projectID, after, limit)
}

// ---------------------------------------------------------------------
// EvaluationStore
// ---------------------------------------------------------------------

func (s *PostgresStore) CreateEvaluationRun(ctx context.Context, run EvaluationRun) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO `+tableRuns+`
		 (id, candidate_id, environment, behavioral_profile, status,
		  created_at, started_at, finished_at, failure_reason)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		string(run.ID()), string(run.CandidateID()), string(run.Environment()),
		string(run.BehavioralProfile()), string(run.Status()),
		timeText(run.CreatedAt()), nullTimeText(run.StartedAt()),
		nullTimeText(run.FinishedAt()), run.FailureReason())
	return mapPostgresError("evaluation run", string(run.ID()), err)
}

func (s *PostgresStore) EvaluationRun(ctx context.Context, id EvaluationRunID) (EvaluationRun, error) {
	return loadRun(ctx, s.querier(), id)
}

// UpdateEvaluationRun applies one transition, or reports a conflict.
//
// Three things make this safe under a pool, and all three are needed:
// the row lock, the re-read, and the status predicate on the UPDATE itself.
// Without the lock two callers interleave; without the predicate a lost update
// reports success.
func (s *PostgresStore) UpdateEvaluationRun(ctx context.Context, previous, next EvaluationRun) error {
	if previous.ID() != next.ID() {
		return fmt.Errorf("%w: update names run %s but carries %s",
			ErrStoreConflict, preview(string(previous.ID())), preview(string(next.ID())))
	}
	// Asked of the domain rather than re-implemented here, exactly as the
	// SQLite store does.
	if err := validTransition(previous, next); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockRun(ctx, tx, previous.ID()); err != nil {
			return err
		}

		stored, err := loadRun(ctx, pgxQuerier{q: tx}, previous.ID())
		if err != nil {
			return err
		}
		if !sameRun(stored, previous) {
			return fmt.Errorf("%w: evaluation run %s is now %s, not %s",
				ErrStoreConflict, preview(string(previous.ID())),
				stored.Status(), previous.Status())
		}
		runMutationHook()

		tag, err := tx.Exec(ctx,
			`UPDATE `+tableRuns+`
			 SET status = $1, started_at = $2, finished_at = $3, failure_reason = $4
			 WHERE id = $5 AND status = $6`,
			string(next.Status()), nullTimeText(next.StartedAt()),
			nullTimeText(next.FinishedAt()), next.FailureReason(),
			string(next.ID()), string(previous.Status()))
		if err != nil {
			return mapPostgresError("evaluation run", string(next.ID()), err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: evaluation run %s is no longer %s",
				ErrStoreConflict, preview(string(previous.ID())), previous.Status())
		}
		return nil
	})
}

func (s *PostgresStore) SaveEvaluationEvidence(
	ctx context.Context, aggregate EvaluationAggregate, snapshot BehaviorSnapshot,
) error {
	if err := validateEvidencePair(aggregate, snapshot); err != nil {
		return err
	}

	return s.withTx(ctx, func(tx pgx.Tx) error {
		runID := aggregate.RunID()
		if err := lockRun(ctx, tx, runID); err != nil {
			return err
		}

		q := pgxQuerier{q: tx}
		run, err := loadRun(ctx, q, runID)
		if err != nil {
			return err
		}
		if run.CandidateID() != aggregate.CandidateID() ||
			run.Environment() != aggregate.Environment() ||
			run.BehavioralProfile() != aggregate.BehavioralProfile() {
			return fmt.Errorf("%w: evidence identity does not match stored run %s",
				ErrStoreConflict, preview(string(runID)))
		}
		if err := checkEvidenceNotStale(ctx, q, aggregate, snapshot); err != nil {
			return err
		}
		runMutationHook()
		return s.writeEvidence(ctx, tx, aggregate, snapshot)
	})
}

func (s *PostgresStore) EvaluationEvidence(
	ctx context.Context, id EvaluationRunID,
) (EvaluationAggregate, BehaviorSnapshot, error) {
	var aggregate EvaluationAggregate
	var snapshot BehaviorSnapshot

	// One transaction, so the two halves cannot be read from either side of a
	// concurrent replacement — the pair must describe the same moment.
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		aggregate, snapshot, err = loadEvidence(ctx, pgxQuerier{q: tx}, id)
		return err
	})
	if err != nil {
		return EvaluationAggregate{}, BehaviorSnapshot{}, err
	}
	return aggregate, snapshot, nil
}

// ---------------------------------------------------------------------
// EvaluationIngestStore
// ---------------------------------------------------------------------

func (s *PostgresStore) EvaluationIngestState(
	ctx context.Context, id EvaluationRunID,
) (EvaluationIngestState, error) {
	var state EvaluationIngestState
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		var err error
		state, err = ingestState(ctx, pgxQuerier{q: tx}, id)
		return err
	})
	if err != nil {
		return EvaluationIngestState{}, err
	}
	return state, nil
}

// CommitEvaluationIngest writes evidence and cursor together, once.
//
// The cursor row may not exist yet, and SELECT ... FOR UPDATE locks nothing
// when there is no row — so two first-commits would both see "no cursor" and
// both proceed. The run row always exists, so locking that instead serializes
// the pair regardless of whether a cursor has ever been written, and the whole
// read-validate-write sequence below happens under it.
func (s *PostgresStore) CommitEvaluationIngest(
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

	var result EvaluationIngestCommitResult
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		runID := commit.Aggregate.RunID()
		if err := lockRun(ctx, tx, runID); err != nil {
			return err
		}

		q := pgxQuerier{q: tx}
		current, err := ingestState(ctx, q, runID)
		if err != nil {
			return err
		}

		if current.NextSequence() != commit.PreviousNextSequence {
			// Identical to the SQLite path: a cursor that advanced past exactly
			// this sequence with this digest means a concurrent retry of the
			// same logical record already landed, which is the retry contract
			// working rather than a failure.
			if current.NextSequence() == next && current.LastDigest() == commit.RecordDigest {
				result = EvaluationIngestCommitResult{
					Disposition:      EvaluationIngestAlreadyCommitted,
					NextSequence:     current.NextSequence(),
					RecordCount:      current.RecordCount(),
					BehaviorComplete: current.BehaviorComplete(),
				}
				return nil
			}
			return fmt.Errorf("%w: run %s now expects sequence %d, not %d",
				ErrIngestSequence, preview(string(runID)),
				current.NextSequence(), commit.PreviousNextSequence)
		}

		run, err := loadRun(ctx, q, runID)
		if err != nil {
			return err
		}
		// Checked here and not only in the service: the service's preflight
		// runs before evidence is computed, so a completion committing in
		// between would otherwise let this write land after the run went
		// terminal.
		if run.Status() != RunRunning {
			return fmt.Errorf("%w: run %s is %s, records are accepted only while running",
				ErrEvaluationState, preview(string(runID)), run.Status())
		}
		if run.CandidateID() != commit.Aggregate.CandidateID() ||
			run.Environment() != commit.Aggregate.Environment() ||
			run.BehavioralProfile() != commit.Aggregate.BehavioralProfile() {
			return fmt.Errorf("%w: evidence identity does not match stored run %s",
				ErrStoreConflict, preview(string(runID)))
		}

		if err := checkEvidenceNotStale(ctx, q, commit.Aggregate, commit.Snapshot); err != nil {
			return err
		}
		runMutationHook()
		if err := s.writeEvidence(ctx, tx, commit.Aggregate, commit.Snapshot); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO `+tableIngestState+` (run_id, next_sequence, last_digest)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (run_id) DO UPDATE SET
				next_sequence = excluded.next_sequence,
				last_digest = excluded.last_digest`,
			string(runID), uint64Text(next), commit.RecordDigest); err != nil {
			return mapPostgresError("ingest cursor", string(runID), err)
		}

		result = EvaluationIngestCommitResult{
			Disposition:      EvaluationIngestCommitted,
			NextSequence:     next,
			RecordCount:      commit.Aggregate.RecordCount(),
			BehaviorComplete: commit.Snapshot.Complete(),
		}
		return nil
	})
	if err != nil {
		return EvaluationIngestCommitResult{}, err
	}
	return result, nil
}

// ---------------------------------------------------------------------
// Evidence writes
// ---------------------------------------------------------------------

// writeEvidence replaces one run's aggregate, snapshot and entries.
//
// Column order matches aggregateInsertColumns and the args come from
// aggregateInsertArgs, both shared with SQLite — so the fifty-column list
// exists once and the two backends cannot disagree about it.
func (s *PostgresStore) writeEvidence(
	ctx context.Context, tx pgx.Tx,
	aggregate EvaluationAggregate, snapshot BehaviorSnapshot,
) error {
	runID := string(aggregate.RunID())

	columns := aggregateInsertColumns()
	_, err := tx.Exec(ctx,
		`INSERT INTO `+tableAggregates+` (`+strings.Join(columns, ", ")+`)
		 VALUES (`+positionalPlaceholders(len(columns))+`)
		 ON CONFLICT (run_id) DO UPDATE SET `+onConflictAssignments(columns[1:]),
		aggregateInsertArgs(aggregate)...)
	if err != nil {
		return mapPostgresError("evaluation aggregate", runID, err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO `+tableSnapshots+`
		 (run_id, candidate_id, environment, behavioral_profile,
		  observation_count, distinct_count, complete)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (run_id) DO UPDATE SET
			candidate_id = excluded.candidate_id,
			environment = excluded.environment,
			behavioral_profile = excluded.behavioral_profile,
			observation_count = excluded.observation_count,
			distinct_count = excluded.distinct_count,
			complete = excluded.complete`,
		runID, string(snapshot.CandidateID()), string(snapshot.Environment()),
		string(snapshot.BehavioralProfile()),
		uint64Text(snapshot.ObservationCount()), snapshot.DistinctBehaviorCount(),
		boolInt(snapshot.Complete())); err != nil {
		return mapPostgresError("behavior snapshot", runID, err)
	}

	// Replaced wholesale rather than merged: the snapshot is one view of a run
	// at one moment, and a leftover entry from an earlier write would make the
	// stored set describe no moment at all.
	if _, err := tx.Exec(ctx,
		`DELETE FROM `+tableEntries+` WHERE run_id = $1`, runID); err != nil {
		return mapPostgresError("behavior entries", runID, err)
	}

	for _, entry := range snapshot.Entries() {
		b := entry.Behavior
		if _, err := tx.Exec(ctx,
			`INSERT INTO `+tableEntries+`
			 (run_id, fingerprint_id, actor_type, operation_category,
			  operation_name, target_name, target_category, environment, observations)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			runID, string(entry.FingerprintID), b.ActorType, b.OperationCategory,
			b.OperationName, b.TargetName, b.TargetCategory, string(b.Environment),
			uint64Text(entry.Observations)); err != nil {
			return mapPostgresError("behavior entry", runID, err)
		}
	}
	return nil
}

// boolInt encodes a flag the way both schemas store it.
//
// INTEGER rather than PostgreSQL's BOOLEAN, which is a deliberate departure
// from ADR 0037's type table and the one place this implementation differs from
// it. The shared restore path scans this column into an int and hands it to
// parseStoredBool, which rejects anything that is not 0 or 1 as ErrStoreCorrupt.
// A BOOLEAN column cannot be scanned into an int, so using one would mean
// either a second restore path — the duplication this design exists to avoid —
// or scanning into a bool and losing the corruption check, since a stored 2
// would silently become true.
//
// The ADR's stated reason for BOOLEAN was that Go scans a bool from either,
// which is true in isolation and not true of the guarded int this code
// actually uses.
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// positionalPlaceholders renders `$1, $2, … $n`.
//
// n comes from a package-level column list, never from caller input.
func positionalPlaceholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(parts, ", ")
}

// onConflictAssignments renders `col = excluded.col, …`.
//
// Every name comes from aggregateInsertColumns, a package-level list of
// constants. No identifier here can originate outside this file.
func onConflictAssignments(columns []string) string {
	parts := make([]string, len(columns))
	for i, column := range columns {
		parts[i] = column + " = excluded." + column
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------
// The read seam
// ---------------------------------------------------------------------

// pgxExecutor is what both a pool and a transaction provide.
type pgxExecutor interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// pgxQuerier adapts pgx to the shared read seam in querier.go.
type pgxQuerier struct{ q pgxExecutor }

func (p pgxQuerier) queryRow(ctx context.Context, query string, args ...any) rowScanner {
	return p.q.QueryRow(ctx, query, args...)
}

func (p pgxQuerier) noRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// rebind rewrites the shared queries' `?` placeholders as `$n`.
func (p pgxQuerier) rebind(query string) string { return rebindPositional(query) }

func (p pgxQuerier) query(ctx context.Context, query string, args ...any) (rowIterator, error) {
	rows, err := p.q.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	// pgx.Rows already has Next, Scan, Err and a no-result Close.
	return rows, nil
}

// Compile-time proof that both backends satisfy the composite and the seam.
var (
	_ Store           = (*PostgresStore)(nil)
	_ evidenceQuerier = pgxQuerier{}
	_ rowIterator     = (pgx.Rows)(nil)
)
