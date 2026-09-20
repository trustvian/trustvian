// Package postgres implements internal/store.Store over PostgreSQL —
// the production-concurrency counterpart to store.InMemory (ephemeral)
// and store.FileStore (local, single-process durable). It is a
// persistence adapter and nothing more: no scoring, no policy, no
// behavioral logic lives here, and no behavioral package imports it.
//
// It deliberately lives in its own package rather than inside
// internal/store. Putting it there would make every consumer of
// internal/store — which is to say, every consumer of the root
// trustvian package — pull pgx into its build, including deployments
// using only the in-memory store. Here, the pgx dependency is reachable
// only from config (which must construct the store) and from this
// package's own tests. internal/anomaly, internal/policy,
// internal/trust, internal/baseline, and event never import it. See
// docs/adr/0018-production-store-boundary-and-postgresql-direction.md.
//
// Construction from outside this module goes through the public boundary
// task 034 established — config.StorageConfig -> config.CompileStorage
// -> trustvian.WithStore — never by importing this package, which Go's
// internal/ rule forbids anyway.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/trustvian/trustvian/internal/baseline"
	"github.com/trustvian/trustvian/internal/features"
	"github.com/trustvian/trustvian/internal/fingerprint"
)

// Sentinel errors, wrapped with fmt.Errorf and checked with errors.Is,
// per .claude/rules/go.md. Kept to the few categories a caller can act
// on differently — a configuration mistake, an unreachable database, a
// corrupt row — rather than a per-statement hierarchy.
var (
	// ErrInvalidDSN reports a connection string this driver cannot
	// parse. Its message deliberately carries **no detail from the
	// underlying parse error**: pgx echoes the raw input verbatim when
	// it cannot identify the string's structure (empirically verified
	// against pgx v5 — a well-formed `postgres://user:pw@host/db` has
	// its password redacted to `xxxxx`, but an unparseable string is
	// reproduced in full, credentials included). Since an invalid DSN
	// is by definition the case pgx cannot structure, wrapping its
	// message is exactly when leaking would happen. See
	// docs/SECURITY.md § Storage configuration and production
	// persistence.
	ErrInvalidDSN = errors.New("store/postgres: invalid DSN")

	// ErrUnavailable reports a database that could not be reached or
	// authenticated against. pgx's own connection errors identify the
	// user and database but not the password (also empirically
	// verified), so they are safe to wrap and genuinely useful for
	// diagnosis.
	ErrUnavailable = errors.New("store/postgres: database unavailable")

	// ErrCorruptState reports a stored row whose jsonb could not be
	// decoded into a Baseline. Surfaced rather than silently treated as
	// "no baseline": quietly discarding unreadable learned state is the
	// same failure mode store.FileStore's version check exists to
	// prevent.
	ErrCorruptState = errors.New("store/postgres: corrupt stored baseline")
)

// Config holds the minimum a PostgreSQL store needs. Every field beyond
// DSN is optional with a documented default — this is not a passthrough
// for pgx's full tuning surface (see docs/ROADMAP.md § v0.8's "good
// defaults are preferred"), and each knob here exists because an
// operator genuinely cannot set it any other way.
type Config struct {
	// DSN is the connection string, in either URL
	// (`postgres://user:pw@host:5432/db`) or keyword/value
	// (`host=... user=...`) form — whatever pgx accepts. Required.
	//
	// It typically contains a password. Nothing in this package logs it,
	// returns it in an error, or writes it to persisted state.
	DSN string

	// MaxConnections caps the pool. Zero means pgx's own default
	// (greater of 4 and GOMAXPROCS). Exposed because an undersized pool
	// throttles a busy Engine while an oversized one can exhaust
	// PostgreSQL's own connection limit — neither is reachable any other
	// way, and the right value depends on deployment shape.
	MaxConnections int32

	// ConnectTimeout bounds the initial connectivity check. Zero means
	// defaultConnectTimeout. Exposed because it is what makes fail-fast
	// startup *fast*: without a bound, an unreachable database leaves
	// construction hanging on the OS-level TCP timeout.
	ConnectTimeout time.Duration
}

// defaultConnectTimeout bounds NewStore's connectivity check. Long
// enough for a cold container or a TLS handshake over a slow link, short
// enough that an unreachable database fails startup promptly rather than
// waiting out a multi-minute OS TCP timeout.
const defaultConnectTimeout = 10 * time.Second

// Store is a Store backed by PostgreSQL. Safe for concurrent use by
// multiple goroutines, and — unlike InMemory and FileStore — safe for
// concurrent use by multiple *processes* against the same database,
// which is the reason it exists.
//
// Deliberately does **not** implement store.Freezer, even though both
// other implementations do. Freezing is defined as "a live,
// current-process operational flag, not part of the learned behavioral
// history" (see store.Freezer), and FileStore pointedly does not persist
// it. Carrying that per-process semantic into a store whose entire
// purpose is sharing state across processes would be actively
// misleading: an operator freezing one replica would believe learning
// had stopped fleet-wide while every other replica kept learning.
// Freezer is an optional, type-asserted capability precisely so an
// implementation can decline it; a correct fleet-wide freeze needs
// coordinated state and belongs to a later slice, not a half-truth here.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore connects to PostgreSQL, verifies connectivity, and ensures
// the schema exists — all before returning. Any failure returns a nil
// Store and an error: there is no partially-usable state, and no
// fallback to a non-durable store. A caller that explicitly asked for
// PostgreSQL and cannot have it must fail, not silently persist
// nowhere (docs/SECURITY.md § Storage configuration; ADR 0018).
//
// The caller owns the returned Store's lifetime and must Close it to
// release the connection pool.
func NewStore(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("%w: empty DSN", ErrInvalidDSN)
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		// Intentionally does not wrap err — see ErrInvalidDSN.
		return nil, fmt.Errorf("%w: could not be parsed (detail withheld: the driver's parse error can echo the connection string, including credentials)", ErrInvalidDSN)
	}
	if cfg.MaxConnections > 0 {
		poolCfg.MaxConns = cfg.MaxConnections
	}

	timeout := cfg.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	// Bound only the startup check. The caller's ctx still governs it,
	// so a caller cancelling startup cancels this too.
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(startupCtx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	// pgxpool.NewWithConfig is lazy — it does not dial. Ping is what
	// turns "configured" into "verified", and therefore what makes
	// fail-fast real rather than deferred to the first Observe.
	if err := pool.Ping(startupCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	if err := Migrate(startupCtx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	return &Store{pool: pool}, nil
}

// Close releases the connection pool. Safe to call more than once.
//
// Store satisfies io.Closer rather than extending store.Store with a
// Close method: adding it to the port would force every implementation
// and every caller to change for a capability only database-backed
// stores need — the same reasoning that keeps store.Freezer an optional,
// type-asserted capability instead of part of Store. A caller holding a
// store.Store releases resources with:
//
//	if c, ok := s.(io.Closer); ok {
//		_ = c.Close()
//	}
//
// which is a no-op for InMemory and FileStore, neither of which holds
// an OS resource beyond the file it rewrites synchronously.
func (s *Store) Close() error {
	if s.pool != nil {
		s.pool.Close()
	}
	return nil
}

// Ping reports whether the database is usable right now: one bounded
// round trip through the pool, and nothing more.
//
// Deliberately **not** added to store.Store. Only a database-backed store
// has an external dependency that can be unavailable, so widening the port
// would force InMemory and FileStore to answer a question that cannot
// apply to them — the same reasoning that keeps store.Freezer and io.Closer
// optional, type-asserted capabilities rather than port methods.
//
// A caller discovers it structurally, declaring the one-method interface
// itself:
//
//	type pinger interface{ Ping(context.Context) error }
//	if p, ok := s.(pinger); ok { err = p.Ping(ctx) }
//
// which is what lets the Collector processor — a separate module that
// cannot import internal/store — use it with no public API involved.
//
// Cheap on purpose: a readiness probe may be called every few seconds, so
// this must never run a migration, scan a table, or load behavioral state.
// The caller supplies the deadline; this method adds none of its own.
func (s *Store) Ping(ctx context.Context) error {
	if s.pool == nil {
		return fmt.Errorf("%w: store is closed", ErrUnavailable)
	}
	if err := s.pool.Ping(ctx); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

// Get returns the current Baseline for key, matching the Store
// contract exactly: a missing key yields an empty baseline.New(key) and
// false, never an error. A single SELECT is already atomic, so no
// transaction is needed.
//
// Note the contract gives Get no error return, so a genuine failure
// (database down mid-run, corrupt row) can only be reported as "no
// baseline". That is a real limitation of the port rather than of this
// implementation — InMemory and FileStore have no failure mode here, so
// the signature never needed one. The consequence is bounded and
// fail-safe in the direction that matters: a failed read makes an actor
// look *unfamiliar*, which raises its anomaly score rather than
// suppressing it, and Observe (which does return an error) surfaces the
// same underlying failure on the very next call. Widening the port is
// task 036's call, not this slice's.
func (s *Store) Get(ctx context.Context, key baseline.Key) (baseline.Baseline, bool) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT baseline FROM `+baselineTable+` WHERE scope = $1 AND actor_id = $2 AND environment = $3`,
		key.Scope, key.ActorID, key.Environment,
	).Scan(&raw)
	if err != nil {
		return baseline.New(key), false
	}

	var bl baseline.Baseline
	if err := json.Unmarshal(raw, &bl); err != nil {
		return baseline.New(key), false
	}
	return bl, true
}

// Observe applies one observation to key's Baseline and returns the
// result, losing no concurrent update.
//
// The whole read-modify-write cycle runs inside one transaction, which
// is possible only because the Store port takes an *observation*
// (fp, vol, now) rather than a caller-computed Baseline — see ADR 0018 §
// Decision 3. The sequence is:
//
//  1. INSERT ... ON CONFLICT DO NOTHING — materialize the row if absent.
//     This is what makes the *first* concurrent observation for a new
//     key correct: SELECT ... FOR UPDATE locks nothing when no row
//     exists, so two racing first-observations would otherwise both read
//     empty and one would overwrite the other. With the insert first,
//     the loser of the insert race blocks on the winner's uncommitted
//     row, then proceeds to the row lock below.
//  2. SELECT ... FOR UPDATE — take the row lock, serializing concurrent
//     observations for this key and only this key. Different keys are
//     different rows and never contend, so unrelated actors update
//     concurrently (no table or advisory lock is taken on this path).
//  3. Apply the observation in Go via Baseline.Observe.
//  4. UPDATE the row, then COMMIT.
//
// Transaction scope is exactly this and nothing more: no scoring, policy
// evaluation, or alert delivery happens inside it.
func (s *Store) Observe(ctx context.Context, key baseline.Key, fp fingerprint.Fingerprint, vol features.VolatileFeatures, now time.Time) (baseline.Baseline, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return baseline.New(key), false, fmt.Errorf("%w: begin: %w", ErrUnavailable, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	empty, err := json.Marshal(baseline.New(key))
	if err != nil {
		return baseline.New(key), false, fmt.Errorf("store/postgres: encode empty baseline: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO `+baselineTable+`
			(scope, actor_id, environment, baseline, schema_version, fingerprint_count, observation_count, last_observed, updated_at)
		 VALUES ($1, $2, $3, $4, $5, 0, 0, NULL, now())
		 ON CONFLICT (scope, actor_id, environment) DO NOTHING`,
		key.Scope, key.ActorID, key.Environment, empty, SchemaVersion,
	); err != nil {
		return baseline.New(key), false, fmt.Errorf("%w: ensure row: %w", ErrUnavailable, err)
	}

	var raw []byte
	if err := tx.QueryRow(ctx,
		`SELECT baseline FROM `+baselineTable+`
		 WHERE scope = $1 AND actor_id = $2 AND environment = $3
		 FOR UPDATE`,
		key.Scope, key.ActorID, key.Environment,
	).Scan(&raw); err != nil {
		// pgx.ErrNoRows is unreachable here: the insert above
		// guarantees the row exists and the lock holds it for this
		// transaction. Reported rather than ignored so a future change
		// that breaks that invariant fails loudly.
		if errors.Is(err, pgx.ErrNoRows) {
			return baseline.New(key), false, fmt.Errorf("%w: row vanished after insert for actor %q", ErrCorruptState, key.ActorID)
		}
		return baseline.New(key), false, fmt.Errorf("%w: lock row: %w", ErrUnavailable, err)
	}

	var bl baseline.Baseline
	if err := json.Unmarshal(raw, &bl); err != nil {
		return baseline.New(key), false, fmt.Errorf("%w: actor %q: %w", ErrCorruptState, key.ActorID, err)
	}

	updated, learned := bl.Observe(fp, vol, now)

	encoded, err := json.Marshal(updated)
	if err != nil {
		return baseline.New(key), false, fmt.Errorf("store/postgres: encode baseline: %w", err)
	}

	// The derived columns are recomputed from the very value written to
	// `baseline`, in this same statement — they cannot drift from it.
	var lastObserved any
	if !updated.LastObserved.IsZero() {
		lastObserved = updated.LastObserved
	}
	if _, err := tx.Exec(ctx,
		`UPDATE `+baselineTable+`
		 SET baseline = $4, schema_version = $5, fingerprint_count = $6,
		     observation_count = $7, last_observed = $8, updated_at = now()
		 WHERE scope = $1 AND actor_id = $2 AND environment = $3`,
		key.Scope, key.ActorID, key.Environment, encoded, SchemaVersion,
		len(updated.Fingerprints), totalObservations(updated), lastObserved,
	); err != nil {
		return baseline.New(key), false, fmt.Errorf("%w: update row: %w", ErrUnavailable, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return baseline.New(key), false, fmt.Errorf("%w: commit: %w", ErrUnavailable, err)
	}
	return updated, learned, nil
}

// totalObservations sums Count across every Fingerprint — the
// "how much has Trustvian learned about this actor" number an operator
// wants when inspecting the table, and not otherwise derivable without
// decoding jsonb. Inspection only; never read back into a Baseline.
func totalObservations(bl baseline.Baseline) int64 {
	var total int64
	for _, stats := range bl.Fingerprints {
		total += int64(stats.Count)
	}
	return total
}
