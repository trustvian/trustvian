# 0037 — PostgreSQL is the shared platform persistence backend

**Status:** Accepted

## Context

[Task 057](../tasks/v1.0/057-local-platform-persistence.md) gave the platform
SQLite, and it was the right choice for what it was asked to do:
[task 062](../tasks/v1.0/062-integrated-local-developer-workflow.md)'s `make
local` is one process on one machine, with a project-local database file that
`rm -rf` ends.

`OpenSQLiteStore` is explicit about the shape that makes this work:

```go
db.SetMaxOpenConns(1)
```

> *One writer. SQLite serializes writes anyway, and a single connection makes
> the foreign-key pragma above true for every statement this store issues.*

That single connection is not a tuning choice. It is what makes three
read-then-write operations — `UpdateEvaluationRun`, `SaveEvaluationEvidence`,
`CommitEvaluationIngest` — atomic, because each re-reads state inside its
transaction and compares it in Go before writing.

A shared sandbox is several processes against one database. SQLite is not for
that, and the property above is the reason: the atomicity is a consequence of
there being exactly one connection.

So the platform needs a second backend. The question this record settles is
what that does to everything built on top — the control plane, the `/v1`
contract, the CLI, the TUI, and the WebUI that
[task 063](../tasks/v1.0/063-minimal-web-control-plane.md) just added — and the
answer has to be "nothing".

[ADR 0023](0023-interfaces-are-adapters.md) anticipated the pressure:

> A single `Database` interface would make those interchangeable on paper and
> leak at the first thing one backend expresses that another does not.

It named SQLite locally, PostgreSQL in a sandbox, and ClickHouse as a
possibility, and said stores are capabilities rather than a database. Task 064
is where that claim is tested against a real second backend.

## Decision

**One logical persistence contract, two implementations. SQLite is the local
default; PostgreSQL is the shared deployed backend. Neither is a mode the layers
above can observe.**

```text
                       ControlPlane
                            │
                            ▼
        ControlStore · EvaluationStore · EvaluationIngestStore
                    ╱                             ╲
                   ▼                               ▼
          platform/sqlite.go              platform/postgres.go
          local · default                 shared · deployed
```

### 1. The existing interfaces are sufficient and do not change

`ControlStore`, `EvaluationStore` and `EvaluationIngestStore` are domain-shaped,
carry a `context.Context`, contain no SQL, and state their atomicity
requirements as method contracts rather than by exposing a transaction handle —
`SaveEvaluationEvidence` says "both together, never separately";
`CommitEvaluationIngest` says "evidence and cursor in one transaction".

That is precisely what lets a second backend satisfy them with whatever locking
it needs. No signature changes.

One addition, and it is a composition concern rather than a persistence one: a
`platform.Store` composite of the three plus `io.Closer`, so
`localruntime.Runtime` can stop naming `*platform.SQLiteStore`. `Close()` is
the only concrete-type use it has. Services keep taking the narrow interfaces,
which is what stops one reaching a capability it has no business with.

### 2. Backend selection lives only at the composition root

`localruntime.Options` gains a backend selector, mirroring the `type:`-plus-block
shape `config/storage.go` already uses for the engine's store — including its
rule that omitting the type is a validation error rather than a default.

No backend conditional may appear in `platform/httpapi`, `platform/webui`, the
control-plane services, the CLI or the TUI. A conditional there would be the
first of the two business-logic branches this decision exists to prevent, and
whichever branch a user was on would be the behaviour they believed.

### 3. Semantic parity is the requirement, not type fidelity

Given the same sequence of valid operations, both backends must expose
equivalent externally observable behaviour. Where PostgreSQL's idiomatic type
would change something observable, the idiom loses.

Two cases decided against idiom, both on evidence:

**Timestamps are `TEXT`, not `TIMESTAMPTZ`.** `timeText` deliberately preserves
the caller's numeric zone offset, and that offset is API-visible — a real
response carries `"created_at":"2026-09-23T20:04:41.043803+03:00"`.
`TIMESTAMPTZ` normalizes to UTC and truncates nanoseconds to microseconds, so it
would change a published field and lose precision. Two backends disagreeing on
a `/v1` value is the failure this whole record exists to prevent.

**uint64 counters are `TEXT`, not `BIGINT` or `NUMERIC`.** `BIGINT` is signed
64-bit — the identical bug SQLite's `INTEGER` has, and the reason the current
schema already stores counters as text. `NUMERIC` would hold the range but
normalizes `'007'` to `7`, destroying the corruption signal `parseUint64Text`
raises for values this code would not have written. No query orders or sums
these values, so text costs nothing.

### 4. Concurrency is designed, not inherited

The three read-then-write operations are atomic today because of the single
connection. Under a pool at `READ COMMITTED` they race: two concurrent `start`
calls both read `created`, both pass their Go-side check, and both write
`running` — the second silently losing the first, because the `UPDATE` carries
no predicate on the previous status.

The remedy is local, not global:

- Every run-scoped write takes `SELECT … FROM platform_evaluation_runs WHERE id
  = $1 FOR UPDATE` first. The run row always exists, it is the aggregate root,
  and locking it reproduces SQLite's serialization at per-run granularity —
  more concurrency, identical semantics.
- For a cursor row that may not exist, `INSERT … ON CONFLICT DO NOTHING` then
  `SELECT … FOR UPDATE`, because `FOR UPDATE` locks nothing when no row exists.
  This is `internal/store/postgres`'s existing pattern, adopted rather than
  reinvented.
- `UpdateEvaluationRun` additionally gains `AND status = $n` with a
  `RowsAffected` check — **on both backends**, so one invariant is expressed one
  way.

Isolation stays `READ COMMITTED`. Raising it globally to `SERIALIZABLE` would
push `40001` retry handling onto every caller for a property two row locks
already provide.

### 5. One logical schema version, two physical schemas

`platform.SchemaVersion` is shared. Neither backend declares its own number, so
they cannot disagree about what version *N* means. The physical SQL differs —
it must, per §3 — and the logical model does not.

Held by three mechanisms: the single constant, a conformance suite both backends
run, and a parity test asserting both report the same version and the same
logical table set after a fresh create.

### 6. The existing migration mechanism is generalized, not replaced

Today: a singleton version row, all DDL plus the version stamp in one
transaction so tables-without-version cannot occur, and race recovery that
re-inspects durable state rather than parsing driver error text.

PostgreSQL has transactional DDL, so that atomicity holds identically. No
migration framework is added — an external dependency to run two ordered
statements is more machinery than the problem has. Two per-backend seams are
needed: table introspection (`sqlite_master` vs `information_schema`) and
concurrent-start safety (`busy_timeout` vs `pg_advisory_xact_lock`).

### 7. pgx/v5, because the repository already chose it

`github.com/jackc/pgx/v5 v5.11.0` is already a direct dependency of the root
module and already `// indirect` in `platform/go.mod`, and
`internal/store/postgres` — the engine's baseline store from v0.8 task 035 —
already uses `pgxpool` with isolation, hardening and stress tests behind it.

Pure Go, so `CGO_ENABLED=0` cross-builds are unaffected. Native context
cancellation. Pooling built in. `pgconn.PgError` exposes SQLSTATE, which is what
makes error mapping possible without matching English.

The dependency delta is **zero new modules**: only pgx's status in
`platform/go.mod` changes.

### 8. Both adapters live in package `platform`, because a subpackage cannot work

`platform/postgres.go` beside `platform/sqlite.go`, in the root package. This is
forced, not preferred.

`EvaluationAggregate` and `BehaviorSnapshot` carry an unexported `bound` marker
recording that the value came from a real constructor, and restoring one from
rows means setting it — `sqlite.go` does exactly that, twice:

```go
aggregate.bound = true
snapshot.bound  = true
```

Only package `platform` may assign an unexported field. A `platform/postgres`
subpackage could decode every column correctly and still not produce a usable
value. ADR 0030 already recorded this as the reason the SQLite adapter is not a
subpackage — *"restoring them from outside would require a public
evidence-forging constructor"* — and that constructor is the thing being
refused, not a missing convenience.

The workarounds are all worse: an exported restore constructor is the forging API
ADR 0030 rejected; an `internal/` package still cannot reach private fields on
types defined elsewhere; returning raw rows from a subpackage to the root package
is an import cycle; and moving the domain types to make the two adapters siblings
is a refactor of merged code to satisfy a layout preference.

The accepted cost: `pgx` joins `modernc.org/sqlite` in the dependency graph of
anything importing `trustvian-platform`, so the repository-internal
`trustvian-local` binary carries both drivers. It is bounded — the shipped
`trustvian` binary imports no platform code at all
([ADR 0022](0022-core-platform-boundary.md)), so neither driver reaches a release
artifact.

### 9. Errors map to existing sentinels and never leak

SQLSTATE codes map to `ErrStoreAlreadyExists` (`23505`), `ErrStoreNotFound`
(`23503`), `ErrStoreConflict` (`40001`) and an unavailability class, keyed on the
code and never on error text. SQLSTATE, constraint, table and column names, the
DSN, host, database, username and raw SQL never reach a caller.

There is a specific known leak path, and `internal/store/postgres` already has a
regression test for it: pgx redacts the password when it can *parse* a DSN but
reproduces an unparseable string verbatim. So an invalid DSN is the dangerous
case, and the platform store must not include the DSN in its own error text
either.

### 10. Realtime is unchanged, and PostgreSQL mode is honest about what that costs

The in-process bus stays. `LISTEN/NOTIFY` is not introduced.

The consequence must be documented rather than discovered: two control-plane
processes on one PostgreSQL database have independent realtime buses, so a
client connected to one does not see events published by the other.
Authoritative state is shared and correct; notification is not.

This is inherent to keeping the in-process bus, which is deliberate. A
cross-node transport is a much larger change than a persistence backend, and it
belongs to [task 069](../tasks/v1.0/).

## Alternatives considered

**Replace SQLite entirely with PostgreSQL.** One backend, one schema, no parity
burden, no conformance suite. Rejected because it destroys the product's
adoption path: `make local` currently needs no database server, no container, no
account and no configuration, and task 062 built the whole local workflow around
that. Requiring PostgreSQL to try Trustvian is a far worse trade than
maintaining two backends.

**Require PostgreSQL for local mode only in "advanced" setups, and drop SQLite
from CI.** Cheaper to maintain, and it would let the two schemas drift
undetected — the local path would be the untested one. Rejected: the default
path must be the best-tested path.

**Maintain separate control-plane implementations per backend.** Each could use
its backend's strengths fully. Rejected as the failure ADR 0023 named
explicitly: evaluation, diff, scorecard and gate logic existing twice means two
behaviours, and the one a user believes is whichever deployment they opened. It
is also the `if postgres { A } else { B }` shape at maximum scale.

**Introduce an ORM.** Rejected on inspection, not reflex. The schema is nine
tables with no relations traversed in SQL, no dynamic queries, and every
statement a primary-key or FK-prefix lookup; there is nothing for an ORM's query
builder to earn. Against that it would add a substantial dependency, generate
queries whose emitted SQL becomes something to audit, couple migrations to its
own tool, and — worst here — paper over exactly the dialect differences §3 shows
are semantically load-bearing. An abstraction that makes `TIMESTAMPTZ` look like
SQLite `TEXT` is an abstraction that loses the offset silently.

**Introduce a generic SQL abstraction framework** (a query builder, or a
`Database` interface with `Query`/`Exec`). Rejected for the reason `store.go`
already gives: *"that is SQL with the type system removed, and it would move
persistence decisions into every call site."* The narrow domain interfaces are
the abstraction, and they are working — a second backend fits them without one
signature changing.

**Use PostgreSQL `LISTEN/NOTIFY` for realtime in Task 064.** Tempting, because
the database is right there and it would fix the per-process limitation §10
documents. Rejected as scope: it changes realtime from an in-process concern to
a distributed one, needs a payload-size policy (`NOTIFY` is capped at 8000
bytes), a reconnection and missed-notification story, and a decision about what
happens when the database is reachable but the bus is behind. Every one of those
is task 069's subject. Adding it here would also make PostgreSQL mode's realtime
*better* than SQLite mode's, which is a parity break in the opposite direction.

**Add event history while adding PostgreSQL.** PostgreSQL can retain history
cheaply and the tables would be easy to add. Rejected because capability is not
a reason: [task 067](../tasks/v1.0/) owns history and should design retention,
bounds and privacy against a clean slate rather than inherit an `event_log`
somebody added opportunistically. Task 064 persists exactly the state task 057
already owns.

**Use `TIMESTAMPTZ` and normalize timestamps to UTC everywhere, including
SQLite.** Idiomatic, and it would make the two schemas converge. Rejected
because it changes a published `/v1` field for every existing caller and
discards the caller's offset, which task 052 deliberately preserved. A
compatibility break to gain a tidier column type is the wrong direction.

**Use `NUMERIC(20,0)` for counters.** Holds the full uint64 range and permits
SQL arithmetic. Rejected because it normalizes non-canonical input, destroying
the `ErrStoreCorrupt` signal the current code raises for text it would not have
written — and no query needs SQL-side arithmetic on these values.

**Raise isolation to `SERIALIZABLE` globally instead of taking row locks.**
Simpler to state, and PostgreSQL would detect the anomalies itself. Rejected
because it makes `40001` a normal outcome of ordinary operations, so every
caller needs retry logic for a property two row locks give directly — and the
retry would have to be in the control plane, which is where backend-specific
concerns are least welcome.

## Consequences

**Semantic parity becomes a standing obligation.** Every future change to
persistence has to land on both backends, and the conformance and differential
suites are what make that enforceable rather than aspirational. A migration that
ships for one backend only is a defect those suites catch.

**One more direct dependency, and no new module.** pgx moves from indirect to
direct in `platform/go.mod`. It is already in `go.sum`, already load-bearing in
the engine's store, and pure Go, so the CGO-free release posture is unaffected.

**Both drivers end up in the platform module's dependency graph**, because §8
forces both adapters into one package. `trustvian-local` grows; the shipped
`trustvian` binary does not change at all, since it imports no platform code.

**PostgreSQL tests need a real PostgreSQL.** Gated on
`TRUSTVIAN_TEST_POSTGRES_DSN` so `go test ./...` still works with no database
and no Docker, and run authoritatively in CI against a `postgres:17-alpine`
service container — the mechanism the engine's store already established. A
SQLite stand-in or a SQL mock would prove nothing about a row lock, a SQLSTATE
or a collation.

**Test isolation costs a schema per test.** Migration therefore runs on every
test, which is a benefit: the migration path is exercised constantly rather
than once. The alternative — truncating shared tables — was already tried in
this repository and broke, because `go test ./...` runs separate package
binaries concurrently.

**Collation becomes something the deployer can affect.** Identifier and ordered
columns are declared `COLLATE "C"` to match SQLite's byte ordering, and the
differential suite should run against a non-C-locale database to prove the
declared subset is sufficient.

**PostgreSQL mode's realtime is per-process.** Documented in §10 and in the task
specification. It is the one place where "the backend is invisible" has a
visible edge, and saying so plainly is better than a deployer finding it.

**Local development is unchanged.** `make local` still starts SQLite with no
configuration, which is the point. Backend selection is opt-in, fails closed on
anything unrecognized, and never falls back.
