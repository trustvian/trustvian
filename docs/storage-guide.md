# Storage Guide

Trustvian learns. What it learns — each actor's `Baseline` — lives in a
`Store`. Which `Store` you choose decides whether that learning survives
a restart, and whether several Trustvian instances agree about what
"normal" means.

Three backends ship: **memory** (the default), **file**, and
**PostgreSQL** (since [task
035](archive/tasks/v0.8/035-postgresql-store-implementation.md)). All three are
selected through one public configuration document,
`config.StorageConfig`, compiled by `config.CompileStorage` — the same
pattern [`policy-guide.md`](policy-guide.md) and
[`anomaly-config-guide.md`](anomaly-config-guide.md) describe for Policy
and anomaly configuration ([ADR
0018](adr/0018-production-store-boundary-and-postgresql-direction.md)).

## Choosing a backend

| | `memory` | `file` | `postgres` |
|---|---|---|---|
| Survives restart | No | Yes | Yes |
| Shared between processes | No | No | **Yes** |
| Inspectable with external tools | No | JSON file | **SQL** |
| Setup required | None | A writable path | A database |
| Concurrent write cost | ~2 µs | ~4 ms (rewrites whole file) | ~0.6 ms (one row) |
| Default | **Yes** | No | No |

**Use `memory`** for tests, experiments, and any run whose learning you
do not need afterwards. It is the default precisely so that nothing
persists unless you asked for it.

**Use `file`** for a single-process deployment — a CLI run, a sidecar, one
long-lived service — where zero setup matters more than sharing. Note the
cost model: `FileStore` holds everything in memory and rewrites the entire
file on *every* `Observe`, so its write cost grows with total store size,
not with the one key that changed.

**Use `postgres`** when more than one process must agree. This is the case
that matters most and the one `file` cannot serve at all: two instances
with two files have two different baselines, so the same actor is
"familiar" to one and "novel" to the other, and the decision an event gets
depends on which replica answered it. That is not a performance problem,
it is an inconsistent security posture.

A useful surprise: PostgreSQL is not the slow option. Under concurrent
writes it is **roughly 6–17× faster than `file`**, because it updates one
row where `FileStore` rewrites everything. See
[`PERFORMANCE.md`](PERFORMANCE.md).

## Configuration

### YAML

```yaml
version: v1
type: memory
```

```yaml
version: v1
type: file
file:
  path: /var/lib/trustvian/baseline.json
```

```yaml
version: v1
type: postgres
postgres:
  dsn: postgres://trustvian:${PGPASSWORD}@db.internal:5432/trustvian?sslmode=require
  max_connections: 25          # optional; 0 = pgx default
  connect_timeout_seconds: 15  # optional; 0 = 10s
```

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `dsn` | string | **Yes** | — | PostgreSQL connection string. **A secret** — see [Credentials](#credentials). |
| `max_connections` | int32 | No | pgx default | Pool ceiling. Must not be negative. |
| `connect_timeout_seconds` | int | No | `10` | Bounds connect + ping + migration at startup. Must not be negative. |

Carrying an unused backend block is valid, so you can switch backends by
editing the `type:` line alone.

### CLI

```bash
trustvian analyze        --storage-config storage.yaml events.json
trustvian baseline build --storage-config storage.yaml corpus.json
```

Without `--storage-config`, the CLI uses the in-memory default and
`baseline build` is effectively a dry run.

### Go

```go
cfg, err := config.LoadStorageFile("storage.yaml")
if err != nil {
    return err
}
s, err := config.CompileStorage(cfg)
if err != nil {
    return err
}
if c, ok := s.(io.Closer); ok {
    defer c.Close()
}
engine := trustvian.NewEngine(trustvian.WithStore(s))
```

`config.CompileStorage` is the **only** way code outside this module can
obtain a `store.Store`: the interface lives in `internal/store` and its
methods reference internal types, so an external module can neither name
nor implement it. Passing the result straight into
`trustvian.WithStore` works by type inference without importing anything
internal — see [ADR
0008](adr/0008-policy-config-boundary.md).

## Lifecycle

`store.Store` has no `Close` method, deliberately: only database-backed
stores hold a releasable resource, and adding `Close` to the port would
force every implementation and every caller to change for a capability
most of them do not need. Releasing is instead an **optional,
type-asserted capability**, exactly like the existing `store.Freezer`:

```go
if c, ok := s.(io.Closer); ok {
    defer c.Close()
}
```

The assertion is a no-op for `memory` and `file`, so that snippet is
correct for every backend — write it once and stop thinking about it. The
PostgreSQL store satisfies `io.Closer` and closes its pool. A long-lived
process that never closes leaks connections at shutdown.

The PostgreSQL store deliberately does **not** implement
`store.Freezer`: freezing is a per-process concept, and a freeze that
silently applied to only one replica of a shared store would be
misleading about what it guaranteed.

## Failure behavior — it fails closed

A storage backend that cannot be built is an **error**, never a
substitution:

- `CompileStorage` returns a **nil `Store` and an error** on every
  failure path — an unreachable database, a refused authentication, a
  mismatched schema version, a missing DSN, an unreadable state file.
- There is **no fallback** from PostgreSQL to `file` or `memory`. Ever.
- The CLI aborts the command rather than analyzing anything.

This is not defensive coding for its own sake. A silent downgrade to
in-memory storage would mean every decision afterwards was made against
state the operator believed was durable and shared, and none of it would
survive the process — data loss disguised as convenience, discovered long
after it mattered. See [`SECURITY.md`](SECURITY.md).

What is *not* an error: a transient connection drop during normal
operation. `pgxpool` reconnects as ordinary pool behavior. Trustvian adds
no retry layer of its own, no reconnect loop, and no degraded mode — a
failed `Observe` returns an error to its caller, who decides.

### Failure semantics, one row at a time

Each of these is asserted by a test, not inferred:

| Condition | Behavior |
|---|---|
| Database unreachable at startup | `CompileStorage` errors, nil Store, **no fallback** |
| Connection lost mid-operation | explicit error wrapping `ErrUnavailable`; stored state equals exactly the acknowledged writes |
| Transaction fails before commit | full rollback; the previous baseline is intact, byte for byte |
| Context cancelled | error satisfying `errors.Is(err, context.Canceled)`; nothing committed |
| Deadline exceeded | error satisfying `errors.Is(err, context.DeadlineExceeded)` |
| Waiting on a row lock, then cancelled | returns promptly (measured 5.4 ms), leaves no lock, no partial write, and no stranded connection |
| Connection pool exhausted | waits for capacity, then fails on the caller's deadline (measured: 2.0001 s against a 2 s deadline) |
| Store used after `Close` | operations fail; `Close` itself is idempotent |
| Stored baseline unreadable | `ErrCorruptState` on `Observe`; the row is **never** silently replaced |

A failed operation never strands a connection. That is verified on every
failure path against a pool sized to one connection, where a single leak
would make the next operation impossible.

## Concurrency semantics

`Observe` for a given key is **atomic and lost-update-free**, including
the very first observation for a key that does not exist yet.

Inside one transaction, `Observe`:

1. `INSERT ... ON CONFLICT DO NOTHING` — materializes the row.
2. `SELECT ... FOR UPDATE` — takes the row lock.
3. Decodes the baseline, applies `Baseline.Observe`, writes it back.

Step 1 is the non-obvious one. `SELECT ... FOR UPDATE` locks *nothing*
when no row matches, so without it two concurrent first observations would
each read empty and one would overwrite the other. Both protections are
verified by mutation testing: removing the lock loses ~160 of 200
concurrent observations, and removing the insert fails the
first-observation race test.

Distinct keys are distinct rows and never contend — different actors do
not serialize against each other. Measured: 32 keys × 100 observations
runs at roughly **2.1× the throughput** of the same load on one key, which
is the evidence that nothing serializes globally. If a table lock or
advisory lock sat on the write path, both figures would match.

Transactions are confined to `Observe`. Nothing else is ever inside one:
not `Analyze`, not policy evaluation, not alert delivery.

### Isolation level and deadlocks

`Observe` runs at **READ COMMITTED**, PostgreSQL's default. It sets no
isolation level, and that is deliberate: correctness comes from the
*explicit* `FOR UPDATE` row lock, not from isolation. A stricter level
would add serialization-failure retries to handle in exchange for a
guarantee the row lock already provides.

**Deadlocks are structurally impossible**, not merely unobserved. A
deadlock needs two transactions each holding a lock the other wants;
`Observe` acquires exactly one row lock and never a second, so no cycle
can form regardless of arrival order.

**Trustvian performs no transaction retries**, and needs none.
Serialization failures (SQLSTATE 40001) cannot occur under READ COMMITTED,
and deadlocks (40P01) cannot occur with single-row locking — the two
transient classes a retry loop would exist for are both unreachable by
construction. Connection-level transience is `pgxpool`'s concern and is
handled there.

### Verified under load

Measured against PostgreSQL 17 (task
[036](archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md)).
Read the rates as ratios and regression signals, not as advertised
throughput — they are dominated by network round-trips:

| Scenario | Result |
|---|---|
| 32 writers × 100 observations, one key, 3 rounds | 3200/3200 each round, **0 lost** |
| 96 concurrent *first* writes, 5 rounds | 96/96 each round, exactly 1 row |
| 32 keys × 100 observations | 3200/3200, ~2.1× same-key rate |
| Mixed committing and cancelled writers | acknowledged count == stored count, exactly |
| 200 actors × 10 observations | 200 rows, 2 tables — row count tracks keys, never observation volume |

## Credentials

**The DSN is a secret.** It normally contains a password.

What Trustvian guarantees:

- The DSN is **never logged**.
- The DSN is **never wrapped into an error**. In particular,
  `pgxpool.ParseConfig`'s error is deliberately *not* wrapped: pgx
  redacts passwords in parseable URLs and in connection errors, but
  echoes an **unparseable** DSN verbatim. `ErrInvalidDSN` therefore
  reports that the DSN could not be parsed and withholds the detail.
  `TestNewStoreUnparseableDSNDoesNotLeakCredentials` is the regression
  test.
- **No credential is ever written to a row.** Nothing from the storage
  config is serialized into stored state; the `baseline` column holds a
  `baseline.Baseline` and nothing else.

What you are responsible for: keeping the DSN out of version control and
out of process listings. Supply it through a secret manager or an
environment variable your config loader expands, not a committed file.

## The schema

Two tables, both created automatically on first connection:

```
trustvian_baseline          one row per {scope, actor_id, environment}
  scope             text        NOT NULL   -- learning scope; '' is the default
  actor_id          text        NOT NULL
  environment       text        NOT NULL
  baseline          jsonb       NOT NULL   -- authoritative state
  schema_version    integer     NOT NULL   -- derived
  fingerprint_count integer     NOT NULL   -- derived
  observation_count bigint      NOT NULL   -- derived
  last_observed     timestamptz            -- derived
  updated_at        timestamptz NOT NULL   -- derived
  PRIMARY KEY (scope, actor_id, environment)

trustvian_schema_version   exactly one row: the version this DB is at
```

`scope` is the learning scope (`v1.0`, [ADR
0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md)): an opaque
namespace letting one actor in one environment hold several independent
learned histories. `''` is the default scope, and every baseline written
before schema version 2 is in it. It is part of the primary key on purpose —
storing it only inside the jsonb would let a second scope's insert collide
with the first scope's row, and would leave the authoritative row identity
disagreeing with the `Key` inside the value it holds.

`baseline` is the single source of truth, and it uses the **identical**
`encoding/json` representation `FileStore` already writes — which is what
makes the two backends structurally equivalent rather than equivalent by
careful hand-matching, and means a future `Baseline` field persists in
both automatically.

Every other column is **derived from that jsonb and exists only for
inspection**. They are recomputed in the same statement that writes the
jsonb, so they cannot drift, and are never read back into a `Baseline`.

### Inspecting state

```sql
-- Which actors does Trustvian know about, and how much has it learned?
SELECT actor_id, environment, fingerprint_count, observation_count, last_observed
  FROM trustvian_baseline
 ORDER BY last_observed DESC NULLS LAST
 LIMIT 20;

-- Baselines that have gone quiet
SELECT actor_id FROM trustvian_baseline
 WHERE last_observed < now() - interval '7 days';
```

This inspectability is a reason to choose PostgreSQL, not a side effect:
no Trustvian-specific tool is needed to answer operational questions about
a live system.

### Migration and versioning

`Migrate` runs automatically at store construction and is:

- **idempotent** — a no-op after the first run;
- **transactional** — DDL and the version row commit together;
- **safe under concurrent startup** — a transaction-scoped advisory lock
  serializes racing processes;
- **fail-closed** — metadata this build cannot interpret with confidence
  aborts startup rather than being guessed at, and **atomic** — a
  migration that fails leaves nothing behind, verified by aborting one
  mid-flight and confirming no table was created.

### Schema compatibility, case by case

| Recorded state | Behavior |
|---|---|
| Matches `SchemaVersion` | proceeds |
| **Newer** than this build | `ErrSchemaVersionMismatch` — startup fails |
| Version 1 (pre-learning-scope) | upgraded in place to version 2 |
| Any other older / unrecognized version | `ErrSchemaVersionMismatch` — startup fails |
| Absent, **and no baseline data** | treated as a fresh database; version recorded |
| Absent, **but baseline data exists** | `ErrAmbiguousSchemaState` — startup fails |
| More than one version row | `ErrAmbiguousSchemaState` — startup fails |

The newer-than-this-build case is the one that matters most: an older
binary must never mutate state whose layout it does not understand.

The last two cases are fail-closed deliberately, and both were
silently-accepted gaps until task
[036](archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md).
"No recorded version" only means "new database" when there is also no
data — otherwise it is data of unknown provenance, which is what a partial
restore or an accidental `DELETE FROM trustvian_schema_version` produces.
Recovery is deliberately your decision, not Trustvian's: restore a
consistent backup, or set the version table to the single correct value.
Guessing is exactly what the version check exists to prevent.

### Migration privileges

Automatic creation is chosen for the smallest safe OSS experience: one
connection string and it works. The cost is explicit: **the runtime role
needs table-creation rights on its first run.** Trustvian never needs
superuser.

To separate migration from runtime identity, run one startup with a role
that can create tables, then switch the DSN to a role with only
`SELECT`, `INSERT`, and `UPDATE` on the two tables. Subsequent startups
only read the version row, so they need no DDL rights. A runtime role
lacking the privileges it needs fails startup with PostgreSQL's own
permission error — actionable, and carrying no credentials.

### The version 1 → 2 upgrade

Schema version 2 (`v1.0`) added `scope` and made it part of the primary key.
A version-1 database upgrades automatically on the next startup, inside the
same transaction and advisory lock the initial migration already used, so it
is atomic and safe against a racing process:

1. `scope text NOT NULL DEFAULT ''` is added — every existing row backfills
   to the default scope, which is what those baselines already are;
2. the default is dropped, so a later insert that omits scope fails loudly
   instead of landing in the default profile by accident;
3. the primary key becomes `(scope, actor_id, environment)`;
4. every row's derived `schema_version` is restamped to 2 — not cosmetic,
   since `restore-postgres.sh` verifies no row disagrees with the recorded
   version;
5. the version row becomes 2.

**No learned state is read, rewritten, or discarded.** Each row keeps its
jsonb exactly as it was; the `Baseline` inside has a `Key` with no `Scope`
field, which deserializes to the default scope and so already agrees with
the backfilled column.

There is no downgrade. An older binary refuses version 2 through the
newer-than-this-build row above, which is the intended outcome — it cannot
see the `scope` column and would merge distinct profiles if it proceeded.
Downgrading means restoring a pre-upgrade backup.

Version 0, or any version other than 1 or 2, is still refused outright: an
unrecognized version is not an older release, it is state this build has no
upgrade path for.

Re-migration against an already-upgraded database is a no-op, which is what
every process restart does. Upgrading a database written by the real
`v0.8.0` release in place is also tested, as is both releases refusing a
newer recorded version — see [Operations § Upgrade](operations.md#upgrade).

### What is *not* stored

No raw events. No `events`, `alerts`, `decisions`, `audit_log`,
`sessions`, or `agent_history` tables. Trustvian stores **bounded
behavioral state**, not history — `PredecessorCounts`, `TrigramCounts`,
and `DelegatorCounts` are all capped by `internal/baseline`, so a hostile
actor cannot grow a row without limit. Storing raw event history would
create a new, far more sensitive data asset than the one Trustvian needs.

Both halves of that are tested: 200 actors × 10 observations produces
exactly 200 rows and exactly 2 tables (row count tracks distinct keys,
never observation volume), and a baseline driven past its cardinality
caps round-trips through a separate store with no truncation.

A single row therefore has a structural ceiling regardless of how long
an actor lives or how hard someone tries to inflate it: at most 512
fingerprint identities per actor, each with independently capped inner
maps. The one measured point is a 120-fingerprint baseline at roughly
13 KB serialized, so a row at the 512 cap is on the order of tens of
kilobytes — an order of magnitude for planning rather than a measured
figure. See
[Observability § in-memory store growth](observability.md#in-memory-store-growth).

### Corrupt or unreadable state

If a row's stored JSON is not a valid `Baseline` — a bad restore, a
hand-edited row — the behavior is deliberately asymmetric:

- **`Observe` fails** with `ErrCorruptState` and **does not overwrite the
  row.** An implementation that "recovered" by writing a fresh baseline
  would silently erase that actor's entire learned history and destroy the
  evidence needed to diagnose the problem.
- **`Get` reports no baseline**, because the `Store` port gives it no
  error return. This is fail-safe in the direction that matters — the
  actor reads as *unfamiliar*, which raises its anomaly score rather than
  suppressing it — and `Get` never writes, so the row survives for
  `Observe` to report on the next learning call.

There is no code path that silently resets a corrupt baseline.

## Backup, restore, and upgrade

The learned baseline is security state that cannot be reconstructed, so
back it up. PostgreSQL's own `pg_dump` is consistent while Trustvian runs —
every write is a single-row transaction and a dump is one snapshot — and
restores belong in a **new, empty** database, never over the live one.
`scripts/backup-postgres.sh` and `scripts/restore-postgres.sh` wrap both
with checksums, refusal of unsafe targets, and quarantine of failed
restores.

The procedures — backup, restore and its verification, upgrade, rollback,
the compatibility matrix, and a recovery drill — live in one place:
[Operations](operations.md).

## Running the integration tests

Tests that need a real server are gated on `TRUSTVIAN_TEST_POSTGRES_DSN`.
Unset, they skip — so `go test ./...` passes on a machine with no
PostgreSQL, which is a hard requirement, not a convenience.

```bash
docker run -d --name trustvian-pg -p 5433:5432 \
  -e POSTGRES_USER=trustvian \
  -e POSTGRES_PASSWORD=trustvian \
  -e POSTGRES_DB=trustvian_test \
  postgres:17

export TRUSTVIAN_TEST_POSTGRES_DSN='postgres://trustvian:trustvian@localhost:5433/trustvian_test?sslmode=disable'

go test -race ./...
(cd examples && go test ./...)

docker rm -f trustvian-pg
```

### Three tiers

Selected with the standard `-short` flag rather than a second gating
mechanism:

```bash
go test ./...                                          # unit only — no database needed
TRUSTVIAN_TEST_POSTGRES_DSN=... go test -short ./...   # + integration
TRUSTVIAN_TEST_POSTGRES_DSN=... go test ./...          # + stress (the release gate)
```

The stress tier drives 32 writers × 100 observations at a single key over
three rounds, 96 concurrent first-writes over five rounds, and a
200-actor row-count check. It takes seconds, not minutes, so the release
gate stays practical.

### Database restart durability

One test restarts the database itself, and it needs to be told how:

```bash
TRUSTVIAN_TEST_POSTGRES_DSN='...' \
TRUSTVIAN_TEST_POSTGRES_RESTART_CMD='docker restart trustvian-pg' \
  go test -run TestDatabaseRestartPreservesCommittedBaseline ./internal/store/postgres/
```

It is a **separate variable** because restarting a server is destructive
to whatever the DSN points at — nobody should bounce a shared database by
exporting one connection string. Leave it unset for ordinary runs.

**Run it on its own**, as above. A restart disrupts every other connection
to that server, and `go test ./...` runs package binaries in parallel, so
enabling it for a whole-repository run can fail unrelated tests through no
fault of their own.

The test verifies the restart actually happened, by comparing
`pg_postmaster_start_time()` before and after. A restart command that
silently does nothing fails the test rather than passing it vacuously.

Each integration test creates a **private PostgreSQL schema** and drops it
afterwards, so tests never disturb each other — `go test ./...` runs
separate packages' binaries concurrently, and two packages here use
PostgreSQL. It also means these tests are safe to point at a database that
already holds data.

A reference Docker Compose environment is [task
037](../CHANGELOG.md#v080--production-runtime--storage)'s deliverable; the
command above is the minimum for running the tests today.

## In the reference deployment

[`deployments/docker-compose/`](../deployments/docker-compose/) is a
runnable local deployment that uses this backend for real: an OTel
Collector with the Trustvian processor, analyzing OTLP telemetry against a
PostgreSQL baseline that survives a restart.

```bash
cd deployments/docker-compose
docker compose up -d --build
docker compose run --rm demo-producer
docker compose exec postgres psql -U trustvian -d trustvian -c \
  "SELECT actor_id, fingerprint_count, observation_count FROM trustvian_baseline;"
```

Its Trustvian configuration is the same `StorageConfig` schema documented
above — the Collector processor decodes a `storage:` block into
`config.StorageConfig` and hands it to `config.CompileStorage`, exactly as
the SDK and CLI do:

```yaml
processors:
  trustvian:
    storage:
      version: v1
      type: postgres
      postgres:
        dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
```

That deployment's PostgreSQL service is also the intended environment for
this repository's own integration and stress tiers — see
[§ Running the integration tests](#running-the-integration-tests) and
`make integration-postgres`.

It is a *reference* deployment: local, unhardened, with placeholder
credentials and TLS off. Its
[README](../deployments/docker-compose/README.md#security) says what to
change before anything resembling production.

## Pool sizing

`max_connections` caps the pool. Left at `0`, pgx uses its own default —
the greater of 4 and `GOMAXPROCS`.

Guidance, deliberately brief because there is little to tune:

- **Too small** throttles a busy Engine. When every connection is busy, a
  further operation waits for capacity and then fails on its caller's
  deadline. That is bounded and reportable, never an unbounded hang — but
  it is still a failed request.
- **Too large** can exhaust PostgreSQL's own `max_connections`, especially
  with several replicas. Total connections across all Trustvian instances
  must fit inside the server's limit with room for everything else
  connecting to it.
- **Contention does not scale with pool size.** Concurrent observations of
  the *same* actor serialize on that row's lock no matter how many
  connections are available; only concurrency across *different* actors
  benefits from a bigger pool.

Start with the default. Raise it only in response to measured waiting, and
never to make a test pass.

## PostgreSQL versions

**Tested:** PostgreSQL 17 — CI, the stress tier, the backup/restore/upgrade
tests, and the reference deployment all run against it.

**Expected to work:** PostgreSQL 13 and newer. Trustvian relies only on
long-established features — `INSERT … ON CONFLICT` (9.5+), `jsonb` (9.4+),
transactional DDL, `pg_advisory_xact_lock` (9.1+), and `SELECT … FOR
UPDATE` — so nothing requires a recent server. That is an assumption from
the features used, not a tested claim; there is no multi-version CI matrix.
If you run an older major version, run the integration tests against it
first.

For backups, use client tools at least as new as the server — see
[Operations § PostgreSQL tool versions](operations.md#postgresql-tool-versions).

## Database TLS

Configure TLS through the DSN, the same way any PostgreSQL client does:

```text
postgres://user:pw@db.internal:5432/trustvian?sslmode=require
```

Trustvian passes the DSN to pgx unmodified. It **never weakens
PostgreSQL's security defaults programmatically** — there is no code that
downgrades `sslmode`, disables verification, or silently retries without
TLS. Whatever the DSN asks for is what the driver does.

For production, prefer `sslmode=verify-full` with a pinned root
certificate, supplied through the DSN and your environment's certificate
store. Certificate management is deliberately out of scope here: it
belongs to your deployment, not to a behavioral security engine.

`sslmode=disable` appears in this document's test commands only, against a
throwaway container on loopback.

## Related reading

- [`ARCHITECTURE.md`](ARCHITECTURE.md) — where `Store` sits in the
  pipeline and why the port is narrow
- [`SECURITY.md`](SECURITY.md) — fail-closed guarantees, credential
  handling, bounded state
- [`PERFORMANCE.md`](PERFORMANCE.md) — measured numbers for all three
  backends
- [ADR 0004](adr/0004-narrow-store-port-in-memory-only.md) — why
  `Observe` takes an observation, not a `Baseline`
- [ADR 0006](adr/0006-file-backed-persistent-store.md) — `FileStore`
- [ADR 0018](adr/0018-production-store-boundary-and-postgresql-direction.md)
  — the public selection boundary and the PostgreSQL direction
- [Task 036](archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md)
  — the hardening evidence behind every guarantee on this page, including
  the two schema-metadata defects it found and fixed
- [`examples/persistent-baseline`](../examples/persistent-baseline/) — a
  runnable external-consumer example
