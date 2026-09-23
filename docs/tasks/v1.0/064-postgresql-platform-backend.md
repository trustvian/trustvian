# 064 — PostgreSQL Platform Backend

Status: specified
Depends on: [057](057-local-platform-persistence.md),
[058](058-local-control-plane-api-and-ingest.md),
[062](062-integrated-local-developer-workflow.md),
[063](063-minimal-web-control-plane.md)

## Objective

Give the platform a second persistence backend so a control plane can be shared
between people and processes, without changing anything a client can observe.

```text
                       ControlPlane
                            │
                            ▼
              ControlStore · EvaluationStore · EvaluationIngestStore
                    ╱                             ╲
                   ▼                               ▼
            SQLite backend                  PostgreSQL backend
            local · default                 shared · deployed
```

The control plane does not learn which backend is underneath it. The CLI, TUI
and WebUI do not learn. The behavioral engine keeps its own storage concern and
never learns the platform has one.

## Why

`make local` is a single process on one machine, and SQLite is exactly right
for it. A shared sandbox is several processes against one database, which is
exactly what SQLite is not for: `OpenSQLiteStore` sets
`db.SetMaxOpenConns(1)` because SQLite serializes writes anyway, and that
single connection is what makes its foreign-key pragma and its read-then-write
patterns sound.

Task 064 is the milestone where "several processes" becomes possible. It is
deliberately *only* that: correct backend semantics, proven equal to SQLite's.
Load behaviour under many nodes belongs to [task 069](069-*).

## Scope

1. A PostgreSQL implementation of the three existing persistence interfaces.
2. A physical PostgreSQL schema representing the same logical model, with the
   representation decisions below made explicitly rather than by type mapping.
3. Per-run row locking so every read-then-write that SQLite's single connection
   made atomic stays atomic under a connection pool.
4. Schema versioning and migration generalized to cover both backends, with one
   logical version number meaning the same capability on each.
5. Explicit backend selection at the composition root, defaulting to SQLite.
6. A backend-neutral conformance suite both backends must pass, plus
   differential tests comparing logical state after identical operations.
7. Startup that verifies connectivity and schema before the listener binds.

## Non-goals

No environment model (065), no promotion (066), no event history (067), no
ClickHouse (068), no multi-node or load validation (069), no authentication,
RBAC or TLS policy (070), no backup/restore/upgrade tooling (071), no release
gate (072).

No new WebUI capability, no list or search API, no new evaluation semantics, no
new gate or scorecard behaviour, no new realtime transport, no message broker,
no distributed cache. No `LISTEN/NOTIFY`. No ORM. No generic SQL abstraction
framework. No change to `/v1`, the SSE wire format, `DecisionRecord`, CLI exit
codes, TUI or WebUI behaviour.

SQLite is not replaced, and PostgreSQL never becomes a prerequisite for local
development.

---

# Part 1 — Inventory of the current persistence boundary

Everything in this part is read from the code at `9aa329a`, not from the
roadmap. Several of the brief's assumed paths do not exist: there is no
`platform/persistence/`, `platform/api/` or `platform/realtime/`. The real
layout is a flat root package with three subpackages.

## Where things live

| Concern | Location |
|---|---|
| Persistence interfaces | `platform/store.go` (`ControlStore`, `EvaluationStore`), `platform/ingest.go` (`EvaluationIngestStore`) |
| Error sentinels | `platform/store.go` |
| SQLite implementation | `platform/sqlite.go` — same package as the interfaces |
| Domain values | `platform/domain.go` |
| Control-plane service | `platform/controlplane.go` |
| HTTP adapter | `platform/httpapi/` |
| Composition root | `platform/localruntime/` |
| Browser assets | `platform/webui/` |

The interfaces and their only implementation share a package. That is worth
noting because PostgreSQL will not: see [Package placement](#package-placement).

## The three interfaces

```go
type ControlStore interface {
    CreateProject(ctx, Project) error
    Project(ctx, ProjectID) (Project, error)
    CreateAgent(ctx, Agent) error
    Agent(ctx, AgentID) (Agent, error)
    CreateCandidate(ctx, Candidate) error
    Candidate(ctx, CandidateID) (Candidate, error)
}

type EvaluationStore interface {
    CreateEvaluationRun(ctx, EvaluationRun) error
    EvaluationRun(ctx, EvaluationRunID) (EvaluationRun, error)
    UpdateEvaluationRun(ctx, previous, next EvaluationRun) error
    SaveEvaluationEvidence(ctx, EvaluationAggregate, BehaviorSnapshot) error
    EvaluationEvidence(ctx, EvaluationRunID) (EvaluationAggregate, BehaviorSnapshot, error)
}

type EvaluationIngestStore interface {
    EvaluationIngestState(ctx, EvaluationRunID) (EvaluationIngestState, error)
    CommitEvaluationIngest(ctx, EvaluationIngestCommit) (EvaluationIngestCommitResult, error)
}
```

**No list, search, filter, pagination or delete method exists on any of them**,
and `store.go` says why: sort order, cursors, limits and parent scoping have
never been specified, and freezing one here would decide it by accident. Task
063 held the same line at the HTTP layer.

Every method takes a `context.Context`. Every method is expressed in domain
terms — there is no `Query`, `Exec`, or `Get(any)`.

## Schema as it exists

Nine tables, all prefixed `platform_`:

```text
platform_schema_version            id=1 singleton, version INTEGER
platform_projects                  id PK, name
platform_agents                    id PK, project_id FK, name
platform_candidates                id PK, agent_id FK, + 6 flat metadata columns
platform_evaluation_runs           id PK, candidate_id FK, environment,
                                   behavioral_profile, status, created_at,
                                   started_at?, finished_at?, failure_reason
platform_evaluation_aggregates     run_id PK/FK, 6 decision + 4 risk + 5 approval
                                   + 2 policy counters, 5 metric summaries
platform_behavior_snapshots        run_id PK/FK, observation_count,
                                   distinct_count, complete
platform_behavior_entries          (run_id, fingerprint_id) PK, descriptor
                                   columns, observations
platform_evaluation_ingest_state   run_id PK/FK, next_sequence, last_digest
```

`SchemaVersion = 2`. Version 1 is task 057's schema; version 2 added
`platform_evaluation_ingest_state` and nothing else.

## Representation decisions already made, and their reasons

These are not incidental. Each was chosen against a specific failure, and
PostgreSQL has to preserve the property rather than the syntax.

| Concern | SQLite representation | Why |
|---|---|---|
| Identifiers | `TEXT PRIMARY KEY` | Caller-owned; no generated keys anywhere |
| uint64 counters | `TEXT`, canonical base-10 | SQLite `INTEGER` is **signed** 64-bit; `int64(v)` silently corrupts everything above `MaxInt64`. `parseUint64Text` additionally rejects `"007"` and `"+7"` — anything this code would not have written is `ErrStoreCorrupt` |
| Small counts | `INTEGER` (`distinct_count`) | Genuinely an `int`, not a uint64 |
| Timestamps | `TEXT`, RFC3339Nano, NULL for zero | **Deliberately not normalized to UTC**: rewriting a caller's offset discards information it chose to record. No column defaults to a database clock |
| Booleans | `INTEGER` | SQLite has no boolean type |
| Floats | `REAL` | Metric sum/min/max |
| Metadata | **Flat columns** | There is no JSON column anywhere in this schema |
| Foreign keys | Declared **and enforced** | `dsn = path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"`, set in the DSN so it applies to every pooled connection |
| Indexes | None beyond primary keys | Every query is a primary-key or FK-prefix lookup |

**There is no JSON or blob storage.** `Candidate` metadata is six discrete
`TEXT` columns; behaviour descriptors are seven. This matters because the
brief's JSONB question has no subject: there is nothing currently stored as
JSON for PostgreSQL to store differently.

## Query shapes

Reviewed every statement in `sqlite.go`:

- No `LIMIT`, no `OFFSET`, no pagination, anywhere.
- One `ORDER BY` on domain data: `platform_behavior_entries … ORDER BY
  fingerprint_id ASC`. (The other is `sqlite_master … ORDER BY name`, schema
  introspection.)
- No `GROUP BY`, and no SQL aggregate over domain rows.
- **Every counter comparison happens in Go**, not in SQL —
  `aggregate.RecordCount() < storedAggregate.RecordCount()` and friends are
  method calls on domain values.

The last point is what makes `TEXT` counters viable in PostgreSQL too: nothing
asks the database to order or add them.

## Transaction boundaries

`BeginTx(ctx, nil)` for writes; `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`
for multi-statement reads, via `withReadTx`. Transactional units today:

| Operation | What is atomic |
|---|---|
| `createSchema` | All DDL **plus** the version stamp — so tables-present-without-version, the state migration refuses to adopt, cannot occur |
| `migrateV1ToV2` | The new table plus the version bump |
| `UpdateEvaluationRun` | Re-read run, compare to `previous`, write new status |
| `SaveEvaluationEvidence` | Staleness check, aggregate, snapshot, entries |
| `CommitEvaluationIngest` | Cursor re-read, run-status check, evidence write, cursor advance |

Domain rules stay in Go. The database enforces structure — keys, foreign keys,
NOT NULL — and `validTransition` asks the domain's own transition methods
rather than re-implementing the state machine in SQL. Task 064 keeps that
division exactly.

## Concurrency as it actually works today

**`SetMaxOpenConns(1)`.** Every statement this store issues goes through one
connection, so every read-then-write above is atomic by construction. The
comment says so: *"One writer. SQLite serializes writes anyway, and a single
connection makes the foreign-key pragma above true for every statement."*

This is the single most important fact in the inventory, because it is the
property PostgreSQL does not have. See [Part 3](#part-3--concurrency).

## Dialect leakage

**None into the control plane.** Verified: `store.go`, `controlplane.go`,
`ingest.go`, `httpapi/` and `webui/` contain no SQL. Dialect-specific material
is confined to `sqlite.go`:

- `?` positional placeholders (PostgreSQL uses `$1`)
- `sqlite_master` introspection
- `_pragma=` DSN parameters
- `INSERT … ON CONFLICT(col) DO UPDATE SET x = excluded.x` — which SQLite
  borrowed *from* PostgreSQL and which ports unchanged

## Verdict on the existing abstraction

**The interfaces are sufficient.** They are domain-shaped, context-carrying,
free of SQL, and their atomicity requirements are stated as method contracts
(`SaveEvaluationEvidence` "both together, never separately";
`CommitEvaluationIngest` "evidence and cursor in one transaction") rather than
as exposed transaction handles. A second backend can satisfy them without any
signature changing.

Exactly one addition is needed, and it is a composition concern rather than a
persistence one — see [Composite store interface](#composite-store-interface).

---

# Part 2 — PostgreSQL representation

## Package placement

```text
platform/postgres.go          store, pool, lifecycle   ← root package
platform/postgres_schema.go   DDL, version, migration
platform/postgres_test.go     integration tests (DSN-gated)
```

**The root package, not a subpackage — and this is forced, not chosen.**

A subpackage was the obvious design and it does not compile. `EvaluationAggregate`
and `BehaviorSnapshot` carry an unexported `bound` marker that records the value
came from a real constructor, and restoring one from database rows means setting
it:

```go
// platform/sqlite.go:1644
aggregate.bound = true
// platform/sqlite.go:1805
snapshot.bound = true
```

Assignment to an unexported field is legal only from inside package `platform`.
A `platform/postgres` subpackage could read every column correctly and still be
unable to produce a usable value.

`docs/ARCHITECTURE.md` and
[ADR 0030](../../adr/0030-local-persistence-stores-authoritative-bounded-state.md)
already record this as the reason the SQLite adapter is not a subpackage:

> The adapter lives inside package `platform` rather than a subpackage, because
> the aggregate and snapshot carry private bound markers and restoring them from
> outside would require a public evidence-forging constructor.

The alternatives were considered and rejected:

- **Export a restore constructor** (`platform.RestoreEvaluationAggregate(…)`) —
  precisely the "public evidence-forging constructor" ADR 0030 rejected. It would
  let any caller mint evidence that looks durable and bound to a run, which is
  what the marker exists to prevent.
- **An internal decode package** — the types live in the root package, so an
  `internal/` package cannot set their private fields either. It does not help.
- **Return raw rows from a subpackage and decode in the root** — the root package
  would have to import its own subpackage while the subpackage imports the root.
  Import cycle.
- **Move the domain types to a subpackage so both backends can be siblings** — a
  large refactor of merged, working code in five files, to satisfy a layout
  preference. Out of scope for a task that adds a backend.

So the cost is accepted and stated: `pgx` joins `modernc.org/sqlite` in the
dependency graph of everything importing `trustvian-platform`, which in practice
means the repository-internal `trustvian-local` binary carries both drivers.

That cost is bounded. The shipped `trustvian` binary never imports the platform
module at all ([ADR 0022](../../adr/0022-core-platform-boundary.md)), so neither
driver reaches the released artifact. `trustvian-local` is a repository-internal
executable that already carries one driver; carrying two is a size increase, not
a boundary violation.

File naming keeps the two adapters visibly separate within the package —
`sqlite.go` beside `postgres.go` — and the conformance suite is what keeps them
behaviourally aligned.

## Composite store interface

`localruntime.Runtime` currently declares `store *platform.SQLiteStore`. The
only thing it does with the concrete type is `Close()` — verified: five call
sites, all `store.Close()`.

So the smallest change is one interface in `platform/store.go`:

```go
// Store is every persistence capability a control plane needs, plus the
// lifecycle the composition root owns.
//
// Only for composition: nothing in the control plane takes a Store. The
// services take the narrow interfaces, which is what keeps a service from
// reaching a capability it has no business with.
type Store interface {
    ControlStore
    EvaluationStore
    EvaluationIngestStore
    io.Closer
}
```

`*SQLiteStore` already satisfies it. `Runtime.store` becomes `platform.Store`,
and the `OpenSQLiteStore` call becomes a backend switch. That is the entire
change to existing code outside the new package.

## Type mapping, decided rather than translated

| Logical | PostgreSQL | Rationale |
|---|---|---|
| Identifiers | `TEXT COLLATE "C"` | See [collation](#collation-is-a-parity-risk) |
| uint64 counters | `TEXT COLLATE "C"` | **Not `BIGINT`** — it is signed 64-bit, the identical bug SQLite's `INTEGER` has. **Not `NUMERIC`** — see below |
| Small counts | `INTEGER` | Matches the domain's `int` |
| Timestamps | `TEXT` | **Not `TIMESTAMPTZ`** — see [timestamps](#timestamps) |
| Booleans | `BOOLEAN` | No observable difference; Go scans `bool` from either |
| Floats | `DOUBLE PRECISION` | Exactly SQLite `REAL` — both IEEE-754 binary64 |
| Metadata | Flat `TEXT` columns | Mirrors the existing schema; there is no JSON to reconsider |
| Nullable | `TEXT NULL` for zero timestamps | Same three columns: `started_at`, `finished_at`, `first/last_observed_at` |
| Foreign keys | `REFERENCES`, enforced by default | PostgreSQL needs no pragma |

### Why counters are TEXT and not NUMERIC

`NUMERIC(20,0)` would hold the full uint64 range and is the idiomatic answer.
It is rejected because it **normalizes**: `'007'` stored as `NUMERIC` reads back
as `7`. The current code treats non-canonical text as `ErrStoreCorrupt` —

> `parseUint64Text`: *"A value needing coercion was written by something else."*

— which is a tamper and corruption signal, and `NUMERIC` silently launders it
away. `TEXT` preserves both the range and the detection, and since no SQL
orders or sums these values ([query shapes](#query-shapes)), nothing is lost by
keeping them textual.

If a future task needs SQL-side arithmetic on counters, that is the moment to
revisit this with a migration, not now.

### Timestamps

`TIMESTAMPTZ` is rejected. It breaks observable parity twice:

1. **It normalizes to UTC.** `timeText` deliberately preserves the caller's
   numeric offset — *"rewriting a caller's offset discards information it chose
   to record"* — and that offset is **API-visible**. A real response from the
   running runtime:

   ```json
   {"created_at":"2026-09-23T20:04:41.043803+03:00"}
   ```

   Under `TIMESTAMPTZ` the same run would report `2026-09-23T17:04:41.043803Z`.
   Same instant, different bytes, and two backends disagreeing on a `/v1` field.

2. **It truncates to microseconds.** PostgreSQL timestamp precision is 1µs;
   `time.Time` and RFC3339Nano carry nanoseconds. Up to 999ns would be lost on
   write, so a value would not round-trip.

So PostgreSQL stores timestamps as `TEXT` in RFC3339Nano, written by the same
`timeText` and read by the same `parseTimeText` semantics. The contract:

```text
type            TEXT, NULL for a zero time
format          RFC3339Nano, exactly what time.Time.Format produces
zone            the caller's offset, preserved verbatim, never normalized
precision       nanosecond
source          the domain value; no database clock, no DEFAULT now()
comparison      in Go, on time.Time; never in SQL
ordering        not required by any query
```

This is the one place the spec deliberately declines the idiomatic PostgreSQL
type. Compatibility comes first, and the alternative changes a published field.

### Collation is a parity risk

`ORDER BY fingerprint_id ASC` is the only domain ordering. SQLite compares
`TEXT` with `BINARY` collation — byte order. PostgreSQL uses the database's
default collation, typically locale-aware, where punctuation and case can sort
differently.

Two backends could return behaviour entries in different orders for the same
run. Whether that is externally visible depends on downstream consumption, and
"probably not visible" is not a property worth betting parity on.

Therefore: every `TEXT` column that is an identifier or is ordered gets
`COLLATE "C"`, which is byte order and matches SQLite exactly. Declared on the
column so it applies to the index and to every comparison without each query
remembering to ask.

## Schema statements

Same nine tables, same names, same columns, same nullability. Differences are
confined to types and the additions above:

```sql
CREATE TABLE platform_evaluation_runs (
    id                 TEXT COLLATE "C" PRIMARY KEY,
    candidate_id       TEXT COLLATE "C" NOT NULL REFERENCES platform_candidates(id),
    environment        TEXT NOT NULL,
    behavioral_profile TEXT NOT NULL,
    status             TEXT NOT NULL,
    created_at         TEXT NOT NULL,
    started_at         TEXT,
    finished_at        TEXT,
    failure_reason     TEXT NOT NULL
);
```

`platform_schema_version` keeps the singleton shape:
`id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL`.

Status values stay `TEXT`, not a PostgreSQL `ENUM`. An enum type would have to
be migrated in lockstep with the domain's status constants, and the value set
is already validated in Go on read — the database gaining an opinion about it
buys nothing and adds a migration hazard.

## Indexes

Only what an existing query needs. Every index below is justified by a
statement that exists today:

| Index | Query it serves |
|---|---|
| PK `platform_projects(id)` | `Project(ctx, id)` |
| PK `platform_agents(id)` | `Agent(ctx, id)` |
| PK `platform_candidates(id)` | `Candidate(ctx, id)` |
| PK `platform_evaluation_runs(id)` | `EvaluationRun`, and the `FOR UPDATE` lock |
| PK `platform_evaluation_aggregates(run_id)` | evidence load/save |
| PK `platform_behavior_snapshots(run_id)` | evidence load/save |
| PK `platform_behavior_entries(run_id, fingerprint_id)` | entry load, ordered by the PK's second column |
| PK `platform_evaluation_ingest_state(run_id)` | cursor read and advance |

**No other index.** PostgreSQL does not auto-index foreign keys, but no query
searches by `project_id`, `agent_id` or `candidate_id` — there is no list
route, so there is no such access path. Adding indexes for queries that do not
exist would be speculative, and would also quietly imply the list capability
task 063 declined.

If a future task adds a collection route, it brings its own index with its own
justification.

---

# Part 3 — Concurrency

This is the part where the two backends genuinely differ, and where a
mechanical port would be wrong.

## The hazard

SQLite's `SetMaxOpenConns(1)` makes every read-then-write atomic. PostgreSQL
with a pool runs them concurrently at `READ COMMITTED`, the default. Three
operations are affected.

### `UpdateEvaluationRun`

```go
tx := BeginTx(ctx, nil)
stored := loadRun(tx, previous.ID())        // SELECT
if !sameRun(stored, previous) { conflict }  // compared in Go
UPDATE platform_evaluation_runs
   SET status = ?, …
 WHERE id = ?                               // ← no predicate on the old status
tx.Commit()
```

Under `READ COMMITTED`, two concurrent `start` calls both read `created`, both
pass the check, and both write `running`. Both report success; one silently
lost. The compare-and-swap the doc comment promises — *"a caller holding a
stale run cannot overwrite a terminal state somebody else recorded"* — does not
hold, because the guard is in Go and the write is unconditional.

### `CommitEvaluationIngest`

The comment states the assumption outright:

> *"Re-read the cursor inside the transaction. Two requests racing the same
> sequence both computed against the same view; only the one whose view still
> matches durable state may apply."*

Two transactions both re-read `next_sequence = N`, both match, both proceed.
The `ON CONFLICT DO UPDATE` serializes on the row lock at write time, so the
second blocks and then applies — but both have already passed the status and
staleness checks and both return `Committed`. With two *different* records at
the same sequence, one caller is told its record committed while the other's
evidence overwrote it.

### `SaveEvaluationEvidence`

Same shape: staleness compared in Go against a row another transaction may be
rewriting.

## The remedy: one lock rule, not a bigger isolation level

**Every run-scoped write takes a row lock on its run first.**

```sql
SELECT … FROM platform_evaluation_runs WHERE id = $1 FOR UPDATE
```

All three operations already load the run inside their transaction; this makes
that load locking. The run row always exists (it is the FK target for evidence,
snapshots and cursor), it is the natural aggregate root, and locking it
reproduces SQLite's serialization at **per-run** granularity instead of
per-database — strictly more concurrency, identical semantics.

Isolation stays `READ COMMITTED`. A global `SERIALIZABLE` bump would force
every caller to handle `40001` retries for a property two row locks already
give, and the brief is right that locking should be specified locally.

For the cursor row, which may not exist yet, the engine's PostgreSQL store
already established the correct two-step, and `internal/store/postgres`
documents why:

```text
1. INSERT … ON CONFLICT DO NOTHING   — materialize the row if absent,
                                        because FOR UPDATE locks nothing
                                        when no row exists
2. SELECT … FOR UPDATE               — take the row lock
```

Task 064 follows that precedent rather than inventing a variant.

### Predicated updates as defence in depth

Independently of locking, `UpdateEvaluationRun` gains a predicate:

```sql
UPDATE platform_evaluation_runs
   SET status = $1, started_at = $2, finished_at = $3, failure_reason = $4
 WHERE id = $5 AND status = $6      -- $6 = previous.Status()
```

and treats `RowsAffected() != 1` as `ErrStoreConflict`.

**This is applied to the SQLite implementation too.** It is correct there
already, costs nothing, and keeps the two implementations expressing the same
invariant in the same way — which is what stops them drifting. It is the only
change Task 064 makes to `sqlite.go` beyond the migration generalization.

## Invariants that must hold on both backends

| Invariant | Mechanism |
|---|---|
| A run has at most one successful transition out of a given status | Predicated `UPDATE` + `RowsAffected` |
| A sequence commits at most once | Run row lock + in-transaction cursor re-read |
| A duplicate retry of the same record reports `AlreadyCommitted`, not an error | Existing digest comparison, now under the lock |
| Evidence and cursor are durable together or not at all | One transaction, unchanged |
| Evidence never moves backwards | Existing Go comparison, now under the lock |
| Records are refused once a run is terminal | Existing in-transaction status check, now under the lock |
| Creating an entity twice yields `ErrStoreAlreadyExists` | Primary key violation mapped to the sentinel |
| An entity under a missing parent yields `ErrStoreNotFound` | Foreign-key violation mapped to the sentinel |

Concurrency *correctness* is Task 064. Throughput, contention behaviour and
multi-node operational validation are [task 069](069-*).

---

# Part 4 — Driver, connections, configuration

## Driver: pgx/v5, via pgxpool

Already decided by the repository, which is better than deciding it again.

```text
github.com/jackc/pgx/v5 v5.11.0     direct dependency of the root module
                                     already "// indirect" in platform/go.mod
```

`internal/store/postgres` — the engine's baseline store from v0.8 task 035 —
uses `pgxpool` and has `isolation_test.go`, `hardening_test.go`,
`stress_test.go` and `scope_test.go` behind it.

Against the criteria the brief asks for:

| Criterion | pgx/v5 |
|---|---|
| Dependency footprint | **Zero new modules.** Already in `go.sum`; the delta is promoting it from indirect to direct in `platform/go.mod` |
| CGO | Pure Go. `CGO_ENABLED=0` cross-builds unaffected |
| Context cancellation | Native, and honoured by the engine's store already |
| Pooling | `pgxpool` built in; no third-party pool |
| PostgreSQL-native behaviour | Native protocol, real prepared statements, `pgconn.PgError` with SQLSTATE for error mapping |
| `database/sql` compatibility | Available via `stdlib`, and **not used** — the engine's store uses the native API, and matching it keeps one set of idioms |
| Maturity | Established; already load-bearing in this repository |

`lib/pq` is rejected: effectively maintenance-only, and adding a second
PostgreSQL driver to a repository that already has one is a dependency for
nothing.

`database/sql` + `stdlib` is rejected for the platform store even though
`sqlite.go` uses `database/sql`. The engine's PostgreSQL store is native pgx,
and two PostgreSQL access styles in one repository is the more expensive
inconsistency. The two platform backends will differ in access style; they are
already different packages, and the interface is what they share.

**This PR adds no dependency.** The `platform/go.mod` change happens in the
implementation PR.

## Connection configuration

Mirroring `postgres.Config` from the engine's store:

```text
DSN                    required; no default, no assembly from parts
MaxConnections         optional; pgx default when unset
ConnectTimeout         bounds startup dialling
HealthCheckPeriod      pgxpool default unless evidence says otherwise
MaxConnLifetime        pgxpool default
MaxConnIdleTime        pgxpool default
```

`pgxpool.NewWithConfig` is lazy and does not dial, so an explicit `Ping` is
what proves connectivity — the engine's store notes exactly this, and startup
depends on it.

### Credentials must not leak

A DSN normally contains a password. Requirements, each testable:

- Never written to `.trustvian/runtime.json`. The discovery schema stays two
  fields ([ADR 0035](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md)),
  and a credential in a project-local world-readable file is worse than none
  because it looks like security.
- Never logged, never printed by `trustvian-local`, never in a `/v1` response,
  never rendered by the WebUI or CLI.
- Never in an error message. There is a **known driver behaviour** here that
  the engine's store already has a regression test for: pgx redacts the
  password when it can *parse* a DSN (`postgres://u:xxxxx@host/db`) but
  reproduces an **unparseable** string verbatim. So an invalid DSN is the leak
  path, and the platform store must not include the DSN in its own error text
  either. Task 064 ports that regression test.
- TLS is configured through the DSN (`sslmode`, `sslrootcert`). Task 064 must
  not prevent it and must not default it to `disable`; enforcing TLS policy is
  [task 070](070-*).

## Backend selection

The repository already has this pattern, in `config/storage.go` for the
engine's store:

```yaml
type: memory | file | postgres      # required; no default
postgres:
  dsn: …
  max_connections: …
  connect_timeout_seconds: …
```

with *"deliberately no 'default' Type: omitting it is a validation error"* and
`TestCompileStorageUnknownTypeFailsClosedNeverFallsBack`.

Task 064 mirrors that shape for platform persistence. It does **not** reuse
`config.StorageConfig` itself: that configures the engine's baseline store, and
the two are separate concerns with separate lifecycles —
[ADR 0035 §5](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md)
keeps platform state and baseline state apart deliberately, and one config
struct describing both would be the first step toward merging them.

Selection lives in `localruntime.Options`:

```go
type Options struct {
    StateDir      string
    ListenAddress string

    // Backend selects platform persistence. Empty means SQLite, which is
    // what make local uses and what every existing caller gets unchanged.
    Backend  string           // "" or "sqlite" | "postgres"
    Postgres *PostgresOptions // required when Backend is "postgres"
}
```

Requirements:

- **SQLite stays the default.** An empty `Backend` is SQLite, so every existing
  caller and `make local` are unchanged. This is the hard requirement from the
  brief and it is satisfied by absence, not by a flag.
- PostgreSQL requires explicit selection *and* configuration.
- `Backend: "postgres"` with no `Postgres` block fails at startup, before the
  listener binds.
- An unknown `Backend` value fails closed — never falls back to SQLite. The
  engine's config makes the same guarantee and has a test named after it.

`make local` is not redesigned. Whether `trustvian-local` grows a flag or reads
a file is an implementation-PR decision; either way SQLite needs no input.

## Startup order

```text
parse and validate configuration
→ construct the pool                     (lazy; no dial yet)
→ Ping                                   (connectivity proven)
→ read or create schema version
→ migrate per policy                     (under an advisory lock)
→ verify the resulting version and tables
→ construct the store
→ construct the ControlPlane
→ construct the HTTP handler
→ bind the listener
→ serve
→ publish discovery
```

Nothing binds before the database is proven usable. This extends task 062's
existing order — which already opens SQLite before listening — rather than
replacing it, and the failure behaviour is the same: everything opened so far is
closed and startup fails.

A database that becomes unusable *later* is a runtime error mapped per
[Part 7](#part-7--failure-modes), not a startup concern.

---

# Part 5 — Schema lifecycle and parity

## Generalizing the existing mechanism

The current mechanism is small and already has the right shape:

- a `platform_schema_version` singleton row
- `SchemaVersion` as a Go constant
- create-all-plus-stamp in **one transaction**, so tables-without-version
  cannot happen — the ambiguous state `migrate` refuses to adopt
- a v1→v2 step that applies exactly one DDL statement and bumps the version
- recovery that **re-inspects durable state** rather than parsing driver error
  text, because *"a racing opener can make any statement here fail"*

PostgreSQL has transactional DDL, so the atomic create-and-stamp property holds
identically. No migration framework is needed and none is added: an external
dependency to run two ordered statements would be more machinery than the
problem has.

What differs per backend is small and explicit:

| Step | SQLite | PostgreSQL |
|---|---|---|
| Table introspection | `sqlite_master` | `information_schema.tables` filtered to the current schema |
| Concurrent-start safety | single connection + `busy_timeout` | `pg_advisory_xact_lock($key)` taken inside the migration transaction |
| Placeholders | `?` | `$1` |

The advisory-lock approach is the engine store's, key and all:
`migrationAdvisoryLockKey int64 = 0x7275737476696E00`. The platform store needs
its **own distinct key** — sharing one would make a platform migration block on
an engine migration in a database holding both. The implementation PR picks it
and documents the same caveat the engine's does: there is no registry of
advisory-lock keys, so the value is chosen to be unlikely to collide.

## One logical version, two physical schemas

The rule:

```text
logical schema version N  ⇒  equivalent platform capability on both backends
```

The physical SQL may differ — it already must, per the type table. The logical
model may not.

Enforced three ways:

1. **One constant.** `platform.SchemaVersion` is shared. Neither backend
   declares its own version number, so they cannot disagree about what N means.
2. **Conformance suite.** Both backends run the same behavioural suite
   ([Part 6](#part-6--testing)). A capability present on one and absent on the
   other fails there, not in production.
3. **A version-parity test.** A test asserts both backends report
   `SchemaVersion` after a fresh create, and that each backend's table set
   matches a single shared list of logical table names.

Future migrations must add a step to **both** backends in the same change, and
the conformance suite is what proves the step produced equivalent capability.
A migration landing for one backend only is a defect the suite catches.

## Downgrade policy

Unchanged from task 057: a schema version **newer** than the binary understands
is refused. It is not silently read, not partially adopted, and not downgraded.
`ErrStoreSchemaVersion` already carries this, including the deliberately strict
case — *recognized tables with no version metadata is refused rather than
adopted, because the safe reading is "something else wrote here", not
"empty"*.

No downgrade path is provided. Restoring an older schema is a restore
operation, which is [task 071](071-*).

---

# Part 6 — Testing

## Backend-neutral conformance suite

The central defence against drift: one suite, two backends.

```go
// platform/storeconformance_test.go (or an internal test package if the
// implementation finds a better home)
func runStoreConformance(t *testing.T, newStore func(testing.TB) platform.Store)
```

Shape to be confirmed against repository convention during implementation —
`internal/store` already has a `contract_test.go` for the engine's stores, and
matching its idiom is preferable to introducing a second one.

The suite covers only behaviour that exists today. It must not invent CRUD to
look symmetric — there is no update-project, no delete, no list.

| Area | Cases |
|---|---|
| Project | create; read back; duplicate create → `ErrStoreAlreadyExists` with the stored value unchanged; read absent → `ErrStoreNotFound` |
| Agent | as above; create under absent project → `ErrStoreNotFound` |
| Candidate | as above; create under absent agent → `ErrStoreNotFound`; **duplicate ID with different metadata does not overwrite** — the case `store.go` calls out explicitly |
| Run | create; read back; create under absent candidate → `ErrStoreNotFound` |
| Lifecycle | each legal transition; illegal transition → `ErrStoreConflict`; stale `previous` → `ErrStoreConflict`; terminal state is final |
| Evidence | save and load; identical rewrite at the same count is idempotent; divergent rewrite at the same count → `ErrStoreConflict`; lower count → `ErrStoreConflict`; aggregate and snapshot always load as a pair |
| Ingest | first commit; sequence gap → `ErrIngestSequence`; duplicate same-digest retry → `AlreadyCommitted`; duplicate different-digest → conflict; commit against a terminal run → `ErrEvaluationState`; cursor and evidence advance together |
| Cursor migration edge | a run with evidence but no cursor row reports the sequence derived from record count, with an empty digest |
| Boundary values | counter `0`; counter `MaxUint64` round-trips exactly; empty-string optional metadata; zero timestamps as NULL |
| Timestamps | non-UTC offset preserved verbatim; nanosecond precision preserved |
| Corruption | non-canonical counter text → `ErrStoreCorrupt`; unparseable timestamp → `ErrStoreCorrupt` |
| Schema | fresh create reports `SchemaVersion`; a newer version is refused |
| Context | a cancelled context fails the operation and leaves no partial write |
| Restart | close, reopen the same target, state intact |

SQLite runs it against a temp file *and* `:memory:`; PostgreSQL runs it against
a private schema. `:memory:` has no PostgreSQL analogue and that is fine — it
is a SQLite affordance, not a logical capability.

## Differential tests

Drive the same ordered operation sequence against both backends and compare the
resulting **logical** state:

```text
create project → agent → candidate → run → start
→ ingest records 1..N → read progress → complete
→ read run, evidence, ingest state
```

Compared: domain field values, status, counters as exact decimal text,
timestamps byte-for-byte under the precision contract, error class for each
failing step. Not compared: physical types, row order where no order is
specified, or anything the backend is free to choose.

This is the strongest available guard because it fails on *any* observable
divergence, including ones nobody predicted.

## PostgreSQL integration tests

Following the established repository mechanism exactly:

- **Gate:** `TRUSTVIAN_TEST_POSTGRES_DSN`. Absent → skip. A developer running
  `go test ./...` needs no Docker and no PostgreSQL, which is the same deal the
  engine's store already offers.
- **CI:** a `postgres:17-alpine` service container, as `ci.yml`, `nightly.yml`
  and `release.yml` already declare, with the DSN exported. CI runs them
  authoritatively, so "skipped locally" never means "unproven".
- **No substitutes.** SQLite must not stand in for PostgreSQL, and a SQL
  mocking library is not evidence of PostgreSQL compatibility. Neither can
  demonstrate a row lock, a SQLSTATE, or a collation.

## Test isolation

**A private PostgreSQL schema per test**, via `search_path` in the DSN.

This is not a fresh choice; it is the conclusion the engine's store already
reached the hard way, and its comment is worth inheriting rather than
relearning:

> The first version truncated a shared table, reasoning that tests in a package
> run sequentially — true, and it missed that `go test ./...` runs *separate
> packages' binaries concurrently*. A shared-table `TRUNCATE` in one wiped rows
> the other was mid-way through counting, producing phantom "lost update"
> failures in a correct implementation.

A private schema removes the shared resource instead of time-sharing it, keeps
parallel tests safe, and lets the suite run against a database holding real
data without destroying it. Migration runs per schema, so the migration path is
exercised on every test rather than once.

## Concurrency tests

Correctness only — [task 069](069-*) owns load.

| Test | Asserts |
|---|---|
| Concurrent identical `start` on one run | exactly one success, the rest `ErrStoreConflict`; final status `running` |
| Concurrent `complete` and ingest | either order is safe; ingest after terminal is refused; a completed run's evidence never grows afterwards |
| Concurrent same-sequence ingest, same digest | exactly one `Committed`, the rest `AlreadyCommitted`; cursor advances once |
| Concurrent same-sequence ingest, different digests | exactly one `Committed`; the loser is an error, never a silent overwrite |
| Concurrent create of one entity ID | exactly one success, the rest `ErrStoreAlreadyExists` |
| Concurrent migration from N processes | one migrates, all observe the same final version, no partial schema |

Each of these fails today against a naive PostgreSQL port, which is what makes
them worth writing.

## Cross-build gate

`CGO_ENABLED=0` builds must keep working for `linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`, `windows/amd64`. pgx is pure Go, so this is a
gate to verify, not a risk to manage. `make release-dry-run` must stay green.

---

# Part 7 — Failure modes

| Failure | Detected | Internal class | Caller sees | Retry? | Startup fails? |
|---|---|---|---|---|---|
| Invalid DSN | config validation | config error | "platform storage configuration is invalid" — **no DSN text** | No | Yes |
| Missing `postgres` block when selected | config validation | config error | names the missing block | No | Yes |
| Unknown backend name | config validation | config error | names the accepted values | No | Yes |
| Unreachable server | startup `Ping` | `ErrStoreUnavailable` | "platform storage is unavailable" | Operator retries | Yes |
| Authentication failure | startup `Ping` | `ErrStoreUnavailable` | same, no username or host | No | Yes |
| TLS negotiation failure | startup `Ping` | `ErrStoreUnavailable` | same | No | Yes |
| Schema absent | version read | create per policy | n/a | n/a | Only if create fails |
| Schema older than binary | version read | migrate per policy | n/a | n/a | Only if migration fails |
| Schema newer than binary | version read | `ErrStoreSchemaVersion` | "unsupported persistence schema" | No | **Yes** |
| Tables present, version absent | version read | `ErrStoreSchemaVersion` | same | No | **Yes** |
| Migration failure | migration tx | wrapped, transaction rolled back | n/a | No | Yes |
| Connection lost mid-operation | query | `ErrStoreUnavailable` | HTTP 503 | Caller may retry an idempotent op | No |
| Statement timeout | query | `ErrStoreUnavailable` | HTTP 503 | Same | No |
| Unique violation (`23505`) | query | `ErrStoreAlreadyExists` | existing 409 semantics | No | No |
| FK violation (`23503`) | query | `ErrStoreNotFound` | existing 404 semantics | No | No |
| Serialization failure (`40001`) | query | `ErrStoreConflict` | existing 409 semantics | Caller may retry | No |
| Predicated update matched no row | `RowsAffected` | `ErrStoreConflict` | existing 409 semantics | No | No |
| Context cancelled | query | context error | connection closed, request abandoned | n/a | No |
| Row fails domain validation | scan | `ErrStoreCorrupt` | HTTP 500, generic | No | No |

## What must never reach a caller

SQLSTATE codes, constraint names, table or column names, the DSN, the host,
port, database name or username, and raw SQL. Errors map to the existing
sentinels, which the HTTP adapter already translates into the status codes and
envelopes task 058 published. **`/v1` behaviour does not change** — that is the
point of mapping rather than forwarding.

Mapping is by SQLSTATE via `pgconn.PgError`, never by matching error text. The
engine's store already refuses to key on driver English, and for the same
reason: the text is not a contract.

## Context cancellation

pgx honours `context.Context` on every operation. Requirements:

- A cancelled HTTP request cancels its queries; pgx closes the connection
  rather than leaving server-side work running unbounded.
- Runtime shutdown closes the pool after the server stops accepting, in the
  existing shutdown order (bus → server → store → discovery), so no query
  outlives the process it belongs to.
- Every transaction has a deferred rollback, as `sqlite.go` already does.
- Cancellation is never reported as a conflict or a corruption; it is the
  context's own error.

## Observability

Enough to operate the backend, and no more. A metrics platform is out of scope.

At startup, once: the selected backend (`sqlite` or `postgres`) and, for
PostgreSQL, host and database name **without credentials** — or, if any doubt
exists about redaction, the backend name alone. Failures to log: connectivity,
migration, schema mismatch, pool construction.

Never logged: the DSN, the password, or query text with parameters.

---

# Part 8 — Boundaries this task does not cross

## Realtime

The in-process `InMemoryRealtimeBus` is unchanged. Realtime stays notification
over committed state, never authoritative
([ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md)).

`LISTEN/NOTIFY` is **not** introduced, and neither is Kafka, NATS, Redis or
RabbitMQ. A PostgreSQL-backed deployment with two control-plane processes will
have per-process realtime: a client connected to process A does not see events
published by process B. That is a **known, documented limitation of Task 064**,
not an oversight, and cross-node realtime belongs to [task 069](069-*).

Documenting it honestly is required. Claiming shared PostgreSQL gives shared
realtime would be false.

## Event history

No `event_log`, `audit_events`, `observation_history` or `raw_event_history`
table. PostgreSQL being capable of retaining history is not a reason to retain
it. Task 064 persists exactly the state the task 057 contract already owns —
the nine tables, unchanged in purpose. [Task 067](067-*) owns history, and it
should design retention against a clean slate rather than inherit a table
somebody added because it was easy.

## Environments

`environment` stays what it is today: a `TEXT` field on runs, aggregates,
snapshots and entries. No `environment` table, lifecycle, configuration, RBAC or
promotion. [Task 065](065-*) owns the model.

## Promotion

No promotion table, status, transaction, API or UI. [Task 066](066-*) owns it.
Task 064 is backend parity, not workflow expansion.

## Security

Task 064 owns database hygiene: parameterized statements everywhere, no SQL
built from untrusted input, credentials never logged or surfaced, TLS possible,
and guidance that the platform's database role needs only DML on its own tables
plus DDL when it is the process that migrates.

It does **not** own authentication, API tokens, RBAC, sessions or platform
authorization. [Task 070](070-*) does.

### SQL injection

Every value is a bound parameter — `$1`, `$2` — never interpolated. No
identifier comes from caller input: table names are package constants, and the
only dynamic identifier anywhere is the test-only search-path schema, built from
a test-generated name.

This is made testable: a source guard over the PostgreSQL adapter asserting no SQL
string is assembled by concatenating or formatting anything but package-level
constants — the same technique task 063's guards use, and the same reason
(reviewers do not catch this reliably).

## Backup

[Task 071](071-*) owns backup, restore and upgrade. Task 064 may note that
PostgreSQL-native tooling becomes relevant, and implements no `pg_dump`
orchestration, restore command, backup API or upgrade tool.

---

# Part 9 — Compatibility

Adding a backend changes nothing observable. Explicitly unchanged:

```text
/v1 request shapes            SSE wire format          gate semantics
/v1 response shapes           CLI exit-code semantics  scorecard semantics
/v1 error envelopes           TUI behaviour            behavioral diff semantics
DecisionRecord compatibility  WebUI behaviour          discovery schema
```

Backend selection is a deployment concern and must not appear in any of them.
No `/v1` response says which database answered it; no client can tell.

| Surface | Class |
|---|---|
| `platform.ControlStore` / `EvaluationStore` / `EvaluationIngestStore` | repository-internal; the platform module is not published |
| `platform.SchemaVersion` | internal, but governs on-disk and in-database compatibility |
| Physical SQLite schema | operationally stable within a version; migrated forward only |
| Physical PostgreSQL schema | same |
| Backend configuration names | operational surface once shipped — chosen deliberately, per the engine's `type:` precedent |

---

# Part 10 — Implementation plan

Ordered so each step is verifiable before the next depends on it.

### Step 1 — Composition seam (no PostgreSQL yet)

- Add `platform.Store` to `platform/store.go`.
- Change `localruntime.Runtime.store` to `platform.Store`.
- Add `Backend` and `Postgres` to `localruntime.Options`; empty means SQLite.
- Unknown backend fails closed, with a test named for it.

Verifiable alone: all existing tests pass, behaviour identical, no driver added.

### Step 2 — Predicated update on SQLite

- Add `AND status = ?` plus a `RowsAffected` check to `UpdateEvaluationRun`.
- A test proving a stale `previous` is refused by the predicate.

Keeps both implementations expressing one invariant the same way.

### Step 3 — Conformance suite against SQLite

- Write the full suite from [Part 6](#part-6--testing), run it against SQLite
  (file and `:memory:`).
- Every case must pass before PostgreSQL exists, so a later failure is
  unambiguously PostgreSQL's.

### Step 4 — Schema and migration generalization

- Extract the logical table list and version constant.
- Give introspection a per-backend seam (`sqlite_master` vs
  `information_schema`).
- Version-parity test.

### Step 5 — PostgreSQL store

- `platform/postgres.go` and `platform/postgres_schema.go` in the **root
  package** (see [Package placement](#package-placement)); promote pgx to a
  direct dependency of `platform/go.mod`.
- Pool, `Ping`, advisory-locked migration, DDL per [Part 2](#part-2--postgresql-representation).
- Implement the three interfaces with the locking rule from
  [Part 3](#part-3--concurrency).
- SQLSTATE → sentinel mapping.

### Step 6 — Integration and differential tests

- DSN-gated tests, private schema per test.
- Conformance suite against PostgreSQL.
- Differential suite.
- Concurrency suite.

### Step 7 — CI

- PostgreSQL service container for the platform module's job, mirroring the
  existing engine jobs.
- Confirm the suite is skipped without the DSN and runs with it.

### Step 8 — Runtime wiring and docs

- Startup order, failure behaviour, credential redaction, minimal diagnostics.
- `docs/operations.md` or a new deployment section; `docs/ARCHITECTURE.md`;
  `docs/SECURITY.md`; `docs/compatibility.md`; CHANGELOG.
- Document the per-process realtime limitation plainly.

## Expected files

New:

```text
platform/postgres.go                          root package; see Package placement
platform/postgres_schema.go
platform/postgres_test.go
platform/postgres_architecture_test.go        SQL-construction source guard
platform/store_conformance_test.go            shared suite
platform/store_differential_test.go           SQLite vs PostgreSQL
docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md   (this PR)
docs/tasks/v1.0/064-postgresql-platform-backend.md                       (this PR)
```

Modified:

```text
platform/store.go                    + Store composite interface
platform/sqlite.go                   predicated UPDATE; introspection seam
platform/localruntime/runtime.go     backend selection; Store interface
platform/localruntime/runtime_test.go
platform/cmd/trustvian-local/main.go optional configuration input
platform/go.mod                      pgx indirect → direct (no new module)
.github/workflows/ci.yml             PostgreSQL service for the platform job
docs/ARCHITECTURE.md  docs/SECURITY.md  docs/compatibility.md
docs/ROADMAP.md  docs/tasks/v1.0/README.md  CHANGELOG.md
```

Must not change: the core engine, `internal/*`, the root module's `go.mod`,
`/v1` semantics, the realtime protocol, the discovery schema, CLI exit codes.

Dependency delta: **zero new modules.** `pgx/v5 v5.11.0` is already in
`go.sum`; only its status in `platform/go.mod` changes.

---

# Part 11 — Open questions and implementation risks

Stated plainly, with the evidence that would settle each.

### 1. Resolved: are the interfaces sufficient?

**Yes**, with the one composite addition. Atomicity is expressed as method
contracts rather than exposed transactions, so a backend can satisfy them with
whatever locking it needs. Evidence: `SaveEvaluationEvidence` and
`CommitEvaluationIngest` both specify "in one transaction" in their doc
comments without exposing a handle.

### 2. Resolved: can SQLite's semantics be reproduced safely?

**Yes**, and it requires more than a port. `SetMaxOpenConns(1)` is what makes
three read-then-write operations atomic today; per-run `FOR UPDATE` plus
predicated updates reproduce that at finer granularity.
[Part 3](#part-3--concurrency) is the design, and the concurrency tests are
what prove it — each fails against a naive port.

### 3. Resolved: timestamp precision and offset

**TEXT, not `TIMESTAMPTZ`.** `TIMESTAMPTZ` normalizes the offset away and
truncates nanoseconds to microseconds; the offset is API-visible, so either
would be a `/v1` divergence. Evidence in [Part 2](#timestamps), including a
real response body.

### 4. Resolved: unsigned counters

**TEXT, not `BIGINT` or `NUMERIC`.** `BIGINT` is signed 64-bit — the same bug
SQLite's `INTEGER` has. `NUMERIC` normalizes `'007'` to `7`, destroying the
corruption signal `parseUint64Text` exists to raise. No SQL orders or sums
these values, so nothing is lost.

### 5. Resolved: is the migration mechanism reusable?

**Yes.** Both engines have transactional DDL, so create-and-stamp stays atomic.
Two per-backend seams are needed: table introspection and concurrent-start
safety (`busy_timeout` vs `pg_advisory_xact_lock`).

### 6. Resolved: who owns configuration?

`localruntime.Options`, mirroring `config.StorageConfig`'s `type:`-plus-block
shape, as its own type rather than reusing the engine's. Evidence: ADR 0035 §5
keeps platform and baseline persistence separate on purpose.

### 7. Resolved the hard way: PostgreSQL cannot live in a subpackage

The first draft of this specification put the adapter in `platform/postgres/`.
That design does not compile: `EvaluationAggregate` and `BehaviorSnapshot` carry
an unexported `bound` marker, and `sqlite.go` restores it by direct assignment
(`aggregate.bound = true`), which only package `platform` may do.

`ARCHITECTURE.md` and ADR 0030 already documented this as the reason the SQLite
adapter is not a subpackage. The specification was corrected rather than the
constraint worked around, because every workaround is worse: exporting a restore
constructor is the evidence-forging API ADR 0030 rejected, an internal package
cannot reach private fields either, and returning raw rows to the root package is
an import cycle.

Consequence: both drivers sit in the dependency graph of anything importing
`trustvian-platform`. Bounded, because the shipped binary imports none of it.

### 8. Open, low risk: conformance-suite shape

Whether the suite is a helper in `platform`, an internal test package, or
follows `internal/store/contract_test.go`'s idiom. Needs a look at that file
during implementation. Affects file placement, not architecture.

### 9. Open, needs an experiment: advisory-lock key choice

The platform needs a key distinct from the engine's
`0x7275737476696E00`, because a shared key would make a platform migration
block on an engine migration in a database holding both. There is no registry
of advisory-lock keys in PostgreSQL, so the only test is empirical.

**Experiment:** pick a distinct key, then run engine and platform migrations
concurrently against one database and assert both complete and neither blocks
the other. Cheap, and it belongs in the implementation PR.

### 10. Open, needs measurement: `COLLATE "C"` completeness

[Part 2](#collation-is-a-parity-risk) specifies `COLLATE "C"` on identifier and
ordered columns. Whether *every* `TEXT` column needs it depends on whether any
comparison anywhere is collation-sensitive — equality is not, ordering is.

**Experiment:** run the differential suite against a PostgreSQL database
created with a non-C locale (e.g. `en_US.UTF-8`) and assert entry ordering and
all compared values match SQLite. If they do, the specified subset is
sufficient; if not, widen it. The test is worth keeping either way, since a
deployer's locale is not something the code controls.

### 11. Known limitation, documented not solved: realtime is per-process

Two control-plane processes on one PostgreSQL database have independent realtime
buses. A client connected to one does not see the other's events. Authoritative
state is shared and correct; notification is not.

This is inherent to keeping the in-process bus, which Task 064 does
deliberately — the alternative is a cross-node transport, which is
[task 069](069-*)'s subject and would be a much larger change than a persistence
backend. It must be documented as a limitation of PostgreSQL mode rather than
left for a deployer to discover.

---

## Acceptance criteria

1. PostgreSQL implements the three existing interfaces with no signature change
   to any of them.
2. SQLite remains the default; `make local` is unchanged and needs no
   PostgreSQL.
3. PostgreSQL requires explicit selection and configuration; an unknown or
   incomplete selection fails closed before the listener binds.
4. No backend conditional exists in `platform/httpapi`, `platform/webui`, the
   control-plane services, the CLI or the TUI.
5. Counters round-trip `MaxUint64` exactly; timestamps round-trip offset and
   nanosecond precision exactly; both proven on both backends.
6. Every read-then-write is atomic under a connection pool, proven by
   concurrency tests that fail against a naive port.
7. One logical `SchemaVersion` governs both backends, with a parity test.
8. The conformance suite passes on both backends; the differential suite finds
   no divergence.
9. PostgreSQL integration tests run in CI against a real server and skip
   cleanly without `TRUSTVIAN_TEST_POSTGRES_DSN`.
10. No credential appears in any log, error, response, discovery file or UI.
11. `CGO_ENABLED=0` cross-builds and `make release-dry-run` stay green.
12. Zero new modules in any `go.mod`.
13. `/v1`, SSE, CLI exit codes, TUI, WebUI, `DecisionRecord`, gate, scorecard
    and diff semantics are unchanged.
