# 057 — Local Platform Persistence

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [052](052-evaluation-domain.md)–[056](056-deterministic-hard-gates.md) ·
**Blocks:** 058 onward

## Objective

Add the first platform persistence adapter, so a process restart does not
erase local control-plane and evaluation state:

```text
Platform domain / evaluation state
              │
              ▼
     narrow store capabilities
              │
              ▼
           SQLite
```

Persisted: `Project`, `Agent`, `Candidate`, `EvaluationRun`,
`EvaluationAggregate`, `BehaviorSnapshot` and its bounded `BehaviorEntry`
rows.

From those authoritative values a later layer reconstructs `BehaviorDiff`,
`EvaluationScorecard` and `EvaluationGateResult` on demand.

No API, ingest server, realtime delivery, CLI, TUI, promotion workflow, event
history, or PostgreSQL backend.

## Why

Everything tasks 052–056 built lives in memory. A developer who restarts the
process loses the run they just executed and the evidence it produced — which
makes the evaluation foundation unusable as a product regardless of how
correct it is.

### Persist the irreproducible, recompute the rest

```text
BehaviorDiff          = CompareBehaviorSnapshots(reference, candidate)
EvaluationScorecard   = NewEvaluationScorecard(refAgg, canAgg, diff)
EvaluationGateResult  = EvaluateEvaluationGate(scorecard, policy)
```

Each is a deterministic function of values that *are* persisted. Storing them
too would create a second source of truth before any caller has asked for
historical materialization — and the first time a stored scorecard disagreed
with a recomputed one, there would be no principled way to say which is right.

The aggregate and the snapshot are different: they are reductions of a record
stream that no longer exists. Nothing can rebuild them after restart, so they
are what must survive.

Raw event history is deliberately still absent — that is
[task 067](README.md), and it has its own retention, privacy and volume
questions that this task must not settle by accident.

## Persistence Boundary

### Persisted

```text
Project, Agent, Candidate, EvaluationRun
EvaluationAggregate            (one latest row per run)
BehaviorSnapshot               (one latest header per run)
BehaviorEntry                  (≤ 512 rows per run)
```

### Not persisted

```text
DecisionRecord / raw Event history      → task 067
BehaviorDiff, EvaluationScorecard,
EvaluationGateResult                    → recomputed, one source of truth
EvaluationGatePolicy                    → not an entity; see below
core Baseline / fingerprints / sequence
  / delegation learned state            → the core's own stores
alerts, notifications, credentials,
promotion records, users/RBAC,
realtime messages                       → later tasks, or not this layer
```

**Gate policy is not persisted** because task 056 introduced a *value*
representing caller-owned limits, not a control-plane entity with an ID,
owner, revision, effective time, or environment assignment. This task cannot
persist an entity that does not exist, and inventing one here would decide a
model task 056 deliberately left open.

**Core baseline storage stays separate.** The roadmap's local-mode table says
behavior state uses the existing store. Duplicating baselines into platform
SQLite would create two learned-state authorities and silently change the
engine's durability model.

## Capability Interfaces

No generic `Database`, `Repository`, `Query`/`Exec`, or `Put(any)`. ADR 0023
is binding: persistence is expressed as domain capabilities.

```go
type ControlStore interface {
    CreateProject(context.Context, Project) error
    Project(context.Context, ProjectID) (Project, error)

    CreateAgent(context.Context, Agent) error
    Agent(context.Context, AgentID) (Agent, error)

    CreateCandidate(context.Context, Candidate) error
    Candidate(context.Context, CandidateID) (Candidate, error)
}

type EvaluationStore interface {
    CreateEvaluationRun(context.Context, EvaluationRun) error
    EvaluationRun(context.Context, EvaluationRunID) (EvaluationRun, error)
    UpdateEvaluationRun(ctx context.Context, previous, next EvaluationRun) error

    SaveEvaluationEvidence(context.Context, EvaluationAggregate, BehaviorSnapshot) error
    EvaluationEvidence(context.Context, EvaluationRunID) (EvaluationAggregate, BehaviorSnapshot, error)
}
```

Two capabilities rather than one because control state and evaluation evidence
have different lifetimes, different write patterns, and different consumers. A
later backend may implement one without the other.

**No list, search, filter, or pagination methods.** Task 058 has not specified
sort order, cursor semantics, limits, or parent scoping, and freezing any of
them here would decide them by accident. **No delete methods** either: nothing
in this milestone deletes, and a delete API implies a retention model that
does not exist.

Every method takes a `context.Context`.

## SQLite Adapter

```go
func OpenSQLiteStore(ctx context.Context, path string) (*SQLiteStore, error)
func (s *SQLiteStore) Close() error
```

Driver: `modernc.org/sqlite` — a pure-Go implementation, so the platform module
keeps building without CGO on every platform CI covers. Requiring a C toolchain
for local developer persistence would be a portability regression the rest of
the repository does not have.

### Package placement, and the privacy constraint

`EvaluationAggregate` and `BehaviorSnapshot` hold unexported fields and a
private `bound` marker. Restoring them requires writing package-private state.

The adapter therefore lives **in package `platform`**, not a subpackage. The
alternative — exporting `RestoreEvaluationAggregate(fields…)` so an adapter
package could call it — would hand every caller a way to forge trusted
evidence, defeating the marker that tasks 053–056 rely on. A public
evidence-forging constructor is a worse outcome than a package that contains
its own adapter.

The cost is real and worth stating: the SQLite driver becomes a dependency of
the package holding the domain types. That is accepted deliberately, and
`check-platform-boundary` still holds — the core cannot see any of it.

## Schema

Platform persistence **schema version 1**, independent of the Trustvian
release version, the core file-store version, the core PostgreSQL baseline
version, and the config schema version. They evolve for different reasons.

```text
platform_schema_version
platform_projects
platform_agents
platform_candidates
platform_evaluation_runs
platform_evaluation_aggregates
platform_behavior_snapshots
platform_behavior_entries
```

Explicit normalized columns, not JSON or gob blobs: a blob makes corruption
undetectable and schema evolution untypeable. `CandidateMetadata` gets six
fixed columns matching its six fields.

Every caller-supplied value is a **bound parameter**. Only compile-time
identifiers appear in assembled SQL.

## Schema Versioning

A singleton version record — one row, enforced by the schema itself — rather
than a table that could hold two versions at once.

| State on open | Behavior |
|---|---|
| no tables, no metadata | transactionally create schema v1 |
| metadata says v1 | verify and continue |
| metadata says anything else | fail closed, `ErrStoreSchemaVersion` |
| data tables exist, metadata absent | fail closed — **never adopted as fresh** |

That last row is the important one. Stamping an unknown database as v1 because
it happens to lack a version row would silently adopt someone else's data.

There is no previous platform SQLite version, so there is no migration path
and no invented v0.

**Initialization is atomic.** Schema creation and version recording commit
together, so a failed first open cannot leave tables present with metadata
absent — exactly the ambiguous state the table above refuses. Two local
openers racing initialization must both end up correct: one commits, the other
sees a valid v1 and continues rather than reporting corruption.

## Identity and Referential Integrity

**The store generates no platform identity.** No `AUTOINCREMENT` domain ID, no
`last_insert_rowid` promoted to an ID, no generated UUID, no timestamp-derived
ID. Every entity keeps the caller-owned identity it already carries — ADR 0025
is binding, and an ID minted by storage would be an identity nobody chose.

Foreign keys, enforced by SQLite:

```text
agents.project_id             → projects.id
candidates.agent_id           → agents.id
evaluation_runs.candidate_id  → candidates.id
evaluation_aggregates.run_id  → evaluation_runs.id
behavior_snapshots.run_id     → evaluation_runs.id
behavior_entries.run_id       → behavior_snapshots.run_id
```

`PRAGMA foreign_keys` is per-connection, so a pragma executed once against a
pool is a guarantee that quietly lapses when the pool opens its second
connection. This task constrains the connection model so the invariant is
actually true, and tests it rather than assuming it.

No cascading delete as product behavior. The one cascade that exists is
private: replacing a behavior snapshot removes its own child rows inside the
same transaction.

Creating a child whose parent does not exist fails with `ErrStoreNotFound`
through an explicit check, not by parsing a driver's error string.

### Create means create

`CreateProject`, `CreateAgent`, `CreateCandidate` and `CreateEvaluationRun`
never upsert. An existing identity returns `ErrStoreAlreadyExists` and the
stored row is left exactly as it was.

This matters most for `Candidate`: the same `CandidateID` arriving with a
different artifact digest must not rewrite what a finished run was evaluated
against. Evaluating a different artifact means a different caller-owned
`CandidateID`.

## EvaluationRun Persistence

Columns: id, candidate_id, environment, behavioral_profile, status,
created_at, started_at (nullable), finished_at (nullable), failure_reason.

No `passed`, `score`, `promotable` or `gate_result` column. Run status is
execution lifecycle only — task 052 was explicit that `Completed` does not
mean the candidate passed.

### Rehydration goes through the domain API

Private lifecycle fields are never written directly and then marked valid.
A stored run is rebuilt by replaying its transitions:

```text
NewEvaluationRun(...)

pending    → no transition
running    → Start(startedAt)
completed  → Start(startedAt); Complete(finishedAt)
failed     → Start(startedAt); Fail(finishedAt, failureReason)
cancelled  → Cancel(finishedAt)                        if startedAt is zero
             Start(startedAt); Cancel(finishedAt)      otherwise
```

So a corrupted chronology, an impossible status, or a failure reason on a
completed run fails the same invariants a live value would — returning
`ErrStoreCorrupt` rather than a silently repaired timestamp.

### Updates are compare-and-swap

`UpdateEvaluationRun(ctx, previous, next)` requires: both values valid; IDs
equal; candidate, environment, profile and created-at unchanged; `next` a
legitimate domain transition from `previous`; and the currently stored value
still equal to `previous`. Otherwise `ErrStoreConflict`.

The transition legitimacy is verified by invoking the domain's own transition
methods and comparing the result, not by re-implementing the state machine in
SQL.

```text
stored = Running
A holds Running → Completed     → commits
B holds stale Running → Failed  → ErrStoreConflict
```

B must never overwrite A's terminal state.

## Evaluation Evidence Persistence

One latest aggregate row and one latest snapshot header per run, with the
current bounded entry set. Updating evidence *replaces* that state
transactionally. It does not append a version per event — that would become
accidental history and quietly turn this task into task 067.

### Aggregate

Persisted: run identity (run, candidate, environment, profile), record count,
first/last observed, all four categorical count groups, and all five
`MetricSummary` values (`Count`, `Sum`, `Min`, `Max`).

`DecisionRecord`s are **not** persisted to recreate it.

### Snapshot

Header: run identity, observation count, completeness, distinct count.
Entries: `(run_id, fingerprint_id)` as natural key, plus the six
`StableFeatures` dimensions and the observation count.

No `EventID`, session, trace, delegation context, or raw arguments. A snapshot
retains behavioral shape, and storage must not widen that.

### Atomicity

Aggregate and snapshot are two views of the *same* run. Persisting them
independently would allow an aggregate from observation N to commit beside a
snapshot from N−1 and become the durable truth.

`SaveEvaluationEvidence` validates both inputs, then writes the aggregate row,
snapshot header, and all entries in **one transaction**. Any failure leaves
the previously committed evidence entirely intact.

### Compatibility before persistence

Identity must agree exactly across aggregate and snapshot — run, candidate,
environment, profile — *and* match the persisted `EvaluationRun`. Evidence is
never stored under a run merely because a RunID string matched.

**Complete snapshot:** `aggregate.RecordCount() == snapshot.ObservationCount()`.
Both consumed the same stream; a mismatch means divergent evidence and is
refused.

**Incomplete snapshot:** task 054 allows saturation for diagnostics, and
persistence must be able to store that. Depending on how a caller handled the
capacity error, the snapshot may hold fewer observations than the aggregate,
so only `snapshot.ObservationCount() <= aggregate.RecordCount()` is required.
`Complete = false` is preserved.

### Stale-write protection

```text
incoming RecordCount <  stored  → ErrStoreConflict
incoming RecordCount == stored  → identical evidence: idempotent success
                                  different evidence: ErrStoreConflict
incoming RecordCount >  stored  → validated, then replaces transactionally
```

Evidence never moves backwards, and two divergent views of the same count are
never silently resolved by picking one. This is local single-node persistence;
no revision vector or distributed machinery is introduced — task 069 owns
multi-node concerns.

**Completeness is sticky.** Once durable evidence reports `Complete == false`,
a later write cannot restore the same run to `Complete == true`. Saturation is
a fact about what was observed, and persistence must not erase it.

## Full-Width Counters

SQLite `INTEGER` is signed 64-bit; platform counters are `uint64`. Writing
`int64(value)` corrupts everything above `MaxInt64`, silently and only for
large values.

Counters are stored as **canonical base-10 TEXT** via `strconv.FormatUint` and
read with `strconv.ParseUint`, round-tripping the entire `0 … MaxUint64`
domain. Non-canonical text (leading zeros, sign, whitespace, empty) is
rejected as corrupt rather than coerced.

This applies to every persisted `uint64`, behavior observation counts
included. Tested at `0`, `MaxInt64`, `MaxInt64+1`, and `MaxUint64`.

## Time Encoding

No database clock touches domain time. No `CURRENT_TIMESTAMP`, no
`datetime('now')`. Every timestamp already exists on the value being stored.

RFC3339Nano text, nullable columns for optional zero times, preserving the
instant, nanosecond precision, and the numeric zone offset. Values are **not**
normalized to UTC — task 052 deliberately avoided that convention, and
rewriting a caller's offset discards information it chose to record.

The Go monotonic clock reading is process state, not durable state, and does
not survive restart. Non-UTC offsets are tested.

## Corruption Handling

Restored aggregates and snapshots are validated **before** the `bound` marker
is set, because the marker is what every downstream task trusts.

Aggregate, with overflow-safe arithmetic:

```text
decision / risk / approval / policy-selection totals == RecordCount
every MetricSummary.Count == RecordCount
all floats finite (no NaN, no Inf)
Count == 0  → Sum, Min, Max all zero
Count  > 0  → Min, Max within [0,1]; Min <= Max; Sum finite and >= 0
```

Snapshot:

```text
identifiers valid; entries <= 512; fingerprint IDs valid and unique
StableFeatures enums recognized; name bounds preserved
entry Behavior.Environment == snapshot Environment
every Observations > 0
StableFeatures → FingerprintID reverse identity unique
sum of Observations does not overflow, and equals ObservationCount
                                     (for a complete snapshot)
Complete() preserved exactly
```

Entries are restored in ascending `FingerprintID` order, matching what
`Entries()` guarantees for a live snapshot.

Corrupt rows return `ErrStoreCorrupt` and **no partial bound value**. Nothing
is clamped, repaired, or normalized on the way out.

## Errors

| Sentinel | Meaning |
|---|---|
| `ErrStoreNotFound` | requested identity does not exist |
| `ErrStoreAlreadyExists` | create attempted for an existing identity |
| `ErrStoreConflict` | stale lifecycle update, or incompatible evidence replacement |
| `ErrStoreCorrupt` | stored rows cannot reconstruct a valid platform value |
| `ErrStoreSchemaVersion` | schema is unknown, unsupported, or ambiguous |

Five, not one per table or constraint. Underlying errors are wrapped so
`errors.Is` works, and context cancellation and deadline errors stay
discoverable through the chain.

## Concurrency

Safe for concurrent use. Writes are serialized; the connection model is
constrained so the foreign-key pragma is true for every connection actually
used. A bounded busy timeout rather than an unbounded wait, and no assumption
of network-filesystem locking semantics.

Durability is not weakened to improve a benchmark.

## Security

Covered in `docs/SECURITY.md`: bound parameters only, foreign-key ownership,
non-upserting creates, no store-generated identity, fail-closed schema
versioning, atomic evidence writes, validation before trust, full-width
counters, absent raw history, undated core baseline separation, the 512 bound,
sticky incompleteness, stale-write rejection, and the absence of any listener.

## Resource Bounds

```text
per run:  1 aggregate row
          1 snapshot header
        ≤ 512 behavior entries
```

Growth with the number of projects, agents, candidates and runs is intentional
durable state. **Growth with every event is not this task** — there is no
per-event row anywhere in the schema, and a test pins that structurally.

No retention policy, pruning, or delete API yet.

## Tests

**Migration.** Fresh open creates v1 and is usable; close/reopen works;
repeated open is idempotent; an unknown version fails with the sentinel and
modifies no application table; data tables without metadata fail closed rather
than being adopted; a failed initialization is not silently accepted as
initialized afterwards.

**Control state.** Round-trip `Project`, `Agent`, `Candidate` including all
six metadata fields, across a close/reopen.

**Uniqueness.** Each of the four creates, twice — second returns
`ErrStoreAlreadyExists`, and the original row is unchanged even when the
second attempt carries different data.

**Referential integrity.** Agent→missing project, candidate→missing agent,
run→missing candidate all fail; the valid hierarchy succeeds.

**Run round-trip.** Every status, including both cancellation paths, with
non-UTC nanosecond timestamps, across a close/reopen.

**Run conflict.** Two loaded copies; A commits Pending→Running; B's stale
update returns `ErrStoreConflict`; the stored value stays A's. Terminal-state
overwrite is refused.

**Evidence happy path.** Real records through a real aggregate and collector;
save; close/reopen; load; verify every field; then prove the loaded values
still drive `CompareBehaviorSnapshots` → `NewEvaluationScorecard` →
`EvaluateEvaluationGate`. **This is the task's most important test**: the full
derived chain must work after a restart.

**Bounded snapshot.** Exactly 512 distinct behaviors survive and stay sorted.

**Incomplete snapshot.** A saturated collector's snapshot persists,
restores with `Complete() == false` and 512 distinct behaviors, and is still
refused by `CompareBehaviorSnapshots`. Persistence never heals saturation.

**Mismatch.** Complete-snapshot count mismatch, and identity mismatches
(different candidate, environment, profile, or aggregate and snapshot from
different runs) are rejected before commit, leaving no partial state.

**Full uint64** — mandatory. Counters at `0`, `MaxInt64`, `MaxInt64+1`,
`MaxUint64` round-trip unaltered. This fails immediately if the encoding ever
becomes `int64`.

**Evidence atomicity.** Store version A; arrange a behavior-entry write in
version B to fail after earlier writes in the same transaction (a temporary
trigger inside an internal test); confirm the error, then confirm exactly
version A remains — never aggregate B beside snapshot A.

**Stale evidence.** Lower count rejected; equal-count identical retry
idempotent; equal-count divergent rejected; original state unchanged.

**Corruption on read.** Direct SQL writes an unknown status, impossible
chronology, category total ≠ record count, metric count ≠ record count,
NaN metrics, invalid enums, mismatched entry environment, duplicate descriptor
under a second fingerprint, inconsistent observation sums, non-canonical
uint64 text, and snapshot count > aggregate count. Each returns
`ErrStoreCorrupt` with no bound value.

**Schema scope.** The table list is asserted against an allowlist, so no
`events`, `decision_records`, `behavior_diffs`, `scorecards`, `gate_results`,
`gate_policies` or `baselines` table can appear without a test failing.

**SQL values stay data.** Names containing quotes, SQL punctuation and
non-ASCII text round-trip exactly and change no SQL structure.

## Mutation Tests

Each must produce a targeted failure, and all are restored before commit:
duplicate-create protection; reference enforcement; run CAS check;
complete-evidence count equality; evidence transaction atomicity; stale
evidence rejection; sticky `Complete=false`; full-width uint64 encoding;
aggregate validation before `bound`; snapshot validation before `bound`;
unknown schema-version refusal; ambiguous-schema refusal.

## Benchmarks

`SaveEvaluationEvidence` and `LoadEvaluationEvidence` at 32 and 512 behaviors.
Persistence is I/O, so no latency gate is asserted; the numbers exist for
regression tracking.

The property documented is the shape, not the speed:

```text
O(1) in event-history length
O(B) in retained behavior cardinality, B <= 512
```

Not "O(1) persistence" — storing a snapshot necessarily handles up to 512
rows. Durability settings are not weakened to improve these numbers.

## Documentation

New: this file, ADR 0030, the ADR index, `docs/tasks/v1.0/README.md`.
Updated: `ARCHITECTURE.md`, `DOMAIN.md`, `SECURITY.md`, `ROADMAP.md`
(including the stale "None is implemented" milestone-sequence wording),
`PERFORMANCE.md`, `CHANGELOG.md`, and the now-stale package comment in
`platform/domain.go`.

## Acceptance Criteria

- [ ] SQLite adapter behind narrow domain capabilities; no generic `Database`.
- [ ] No store-generated platform identity; creates never upsert.
- [ ] Foreign keys enforced on every connection used, and tested.
- [ ] Run updates are compare-and-swap; rehydration replays domain transitions.
- [ ] Aggregate and snapshot persisted and written in one transaction.
- [ ] Complete-evidence equality enforced; incomplete evidence storable and sticky.
- [ ] Stale evidence rejected; evidence never moves backwards.
- [ ] Full `uint64` round-trips; timestamps keep precision and offset.
- [ ] Unknown and ambiguous schema states fail closed; init is atomic.
- [ ] Restored values validated before any `bound` marker is set.
- [ ] No diff, scorecard, gate-result, gate-policy, event, or baseline table.
- [ ] Domain semantics unchanged; no core runtime change.
- [ ] No API, transport, realtime, PostgreSQL backend, or promotion workflow.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass; `GOWORK=off` holds;
      `check-modules` and `check-platform-boundary` pass.
