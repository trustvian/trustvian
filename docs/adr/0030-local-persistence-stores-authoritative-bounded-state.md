# 0030 — Local persistence stores authoritative bounded state

**Status:** Accepted

## Context

[Task 057](../tasks/v1.0/057-local-platform-persistence.md) adds the first
platform persistence adapter. Everything tasks 052–056 built lives in memory,
so a restart erases the run a developer just executed and the evidence it
produced.

The question this decision settles is not "which database" — it is **what is
authoritative**. Several values in the evaluation chain are deterministic
functions of others, and persisting a derived value creates a second thing
that can be true.

## Decision

Local platform persistence uses SQLite behind narrow control and evaluation
capabilities. It persists caller-owned domain entities plus the bounded
aggregate and behavioral snapshot evidence that cannot be reconstructed after
restart. Deterministic diff, scorecard, and gate outputs are recomputed rather
than stored, and raw event history remains a separate future capability.

### 1. SQLite is the local backend

The roadmap's local-developer stage requires no cloud account and no mandatory
PostgreSQL. A single file, no server, no daemon, and no port is the only shape
that meets "one command to start" without asking a developer to run
infrastructure to see what their agent did.

The driver is `modernc.org/sqlite` — pure Go, so the platform module keeps
building without CGO everywhere CI already builds it. Requiring a C toolchain
for local persistence would be a portability regression the rest of the
repository does not have.

### 2. No generic Database interface

ADR 0023 already says interfaces, transports and stores are adapters
expressed in domain terms. A `Database` with `Query`, `Exec`, `Put(any)` and
`Get(any)` is not an adapter — it is SQL with the type system removed. It
would let any caller write anything, move persistence decisions into call
sites, and make the storage contract unreviewable.

The capabilities name what the platform actually does: create and read a
project, an agent, a candidate, a run; save and load one run's evidence.

### 3. Control state and evaluation evidence are separate capabilities

They have different lifetimes, different write patterns and different
consumers. Control entities are created once and read many times; evidence is
rewritten as a run progresses and is read as a pair. A later backend may
reasonably implement one and not the other, and a single fused interface would
force both.

### 4. Aggregate and snapshot are persisted

They are reductions of a `DecisionRecord` stream that no longer exists.
Nothing can rebuild them from what else is stored, so if they are lost, the
run's evidence is lost. That is the definition this decision uses for
authoritative.

### 5. Diff, scorecard and gate result are not

```text
BehaviorDiff         = CompareBehaviorSnapshots(reference, candidate)
EvaluationScorecard  = NewEvaluationScorecard(refAgg, canAgg, diff)
EvaluationGateResult = EvaluateEvaluationGate(scorecard, policy)
```

Each is a pure function of values that *are* persisted. Storing them would
create two sources of truth before any caller has demonstrated a need for
historical materialization — and the first time a stored scorecard disagreed
with a recomputed one, there would be no principled way to say which is right.

A gate result additionally depends on a policy the caller supplies at
evaluation time. Storing a verdict without the limits that produced it records
an answer whose question is missing.

If a later product requirement genuinely needs materialized historical gate
decisions, that needs its own identity and audit model — a decision to make
deliberately, not something that should arrive as a table added here.

### 6. Raw records and events are not persisted

Event history is [task 067](../tasks/v1.0/README.md), and it carries retention,
privacy and volume questions this task must not settle by accident. Persisting
records to rebuild an aggregate would also invert the design: the aggregate
exists precisely so the stream does not have to be kept.

The structural consequence is worth stating plainly: **no table in this schema
grows per event.** Growth tracks entities and runs, which is intentional
durable state.

### 7. Platform SQLite does not replace the core baseline store

The roadmap's local-mode table says behavior state uses the existing store.
The core's file and PostgreSQL stores keep owning `baseline.Key`,
`baseline.Baseline`, fingerprint learning, sequence and delegation state.

Duplicating any of it here would create two learned-state authorities and
silently change the engine's durability model — and the engine has a
compatibility promise this task is not entitled to alter. Platform persistence
stores *evaluation evidence*, not learned engine state.

### 8. Identity stays caller-owned

ADR 0025 is binding. Identity belongs to whichever adapter admits an entity
into a system, and the platform never generates one.

### 9. No database-generated value becomes platform identity

No `AUTOINCREMENT` domain ID, no `last_insert_rowid` promoted to an ID, no
generated UUID, no timestamp-derived ID. An identity minted by storage is an
identity nobody chose, and it would differ between two backends holding the
same logical data — making a value's identity depend on where it happened to
be written.

Internal relational machinery may exist; none of it surfaces as a domain
identifier.

### 10. Schema versioning is independent of release versions

Platform schema version 1 is unrelated to the Trustvian release version, the
core file-store version, the core PostgreSQL baseline version, and the config
schema version. They change for different reasons, and coupling them would
force a schema migration on an unrelated release or hide a real one behind an
unchanged number.

### 11. Ambiguous schema state fails closed

| On open | Behavior |
|---|---|
| no tables, no metadata | create v1 |
| metadata says v1 | continue |
| metadata says anything else | refuse |
| data tables present, metadata absent | **refuse** |

The last row is the one that matters. Adopting an unversioned database as
fresh because it lacks a version row would silently take ownership of data
this code has never seen — and the safe reading of "tables I recognize with no
version" is "something else wrote here", not "empty".

Schema creation and version recording commit together, so a failed first open
cannot produce that ambiguous state in the first place.

### 12. Evidence writes are transactional

The aggregate and the snapshot are two views of the same run. Written
independently, an aggregate from observation N could commit beside a snapshot
from N−1 and become durable truth — a decision distribution and a behavioral
summary describing different moments, with nothing recording that they
disagree.

One transaction covers the aggregate row, the snapshot header and every entry.
The evidence a reader loads is always one coherent view.

Stale writes are refused for the same reason: evidence never moves backwards,
and two divergent views at the same observation count are never resolved by
silently picking one. Completeness is sticky, because saturation is a fact
about what was observed and restoring `Complete = true` would erase it.

### 13. Full uint64 must round-trip

SQLite `INTEGER` is signed 64-bit; platform counters are `uint64`. The obvious
`int64(value)` conversion corrupts everything above `MaxInt64` — silently, and
only for large values, which is the worst failure profile available: correct
in every small test, wrong in production.

Counters are stored as canonical base-10 text and parsed with `ParseUint`,
covering `0 … MaxUint64` exactly. Non-canonical text is rejected rather than
coerced, because a value that needed coercion was not written by this code.

### 14. Corrupted rows are rejected, never normalized

Restored values are validated before the private `bound` marker is set —
category totals against the record count, metric counts and ranges, finite
floats, entry uniqueness and environment agreement, observation sums.

The marker is what tasks 053–056 trust; setting it on unvalidated storage
would make every downstream guarantee conditional on the database being
undamaged. Silent repair would be worse than an error: a clamped metric or a
patched timestamp produces evidence that looks measured and is invented.

Runs are rehydrated by replaying their domain transitions rather than by
writing private fields, so a corrupt chronology fails the same invariant a
live value would.

### 15. PostgreSQL can implement the same capabilities later

Task 064 can supply a PostgreSQL platform backend behind these exact
capability semantics: create-not-upsert, caller-owned identity, compare-and-
swap run updates, atomic evidence replacement, validated restoration, and
fail-closed schema versioning.

Nothing in the interfaces mentions SQLite, files, or connection handling. The
one genuinely SQLite-shaped decision — text-encoded `uint64` — is a
representation detail a backend with native unsigned or numeric types would
implement differently while preserving the same contract: the full domain
round-trips, and anything else is corrupt.

## Alternatives considered

**A blob column holding a serialized aggregate or snapshot.** Rejected:
corruption becomes undetectable, schema evolution untypeable, and the
validation in point 14 would have nothing to validate against.

**Storing scorecards and gate results for history.** Rejected here as
premature; see point 5. It is a real future requirement with a real design
cost, and it should arrive as its own decision.

**Putting the adapter in a `platform/sqlite` subpackage.** Rejected, and this
is the uncomfortable one. `EvaluationAggregate` and `BehaviorSnapshot` hold
unexported fields and a private `bound` marker; a subpackage cannot write them
without an exported restore constructor — which would hand every caller a way
to forge trusted evidence and defeat the marker entirely.

So the adapter lives in package `platform`, and the SQLite driver becomes a
dependency of the package that holds the domain types. That cost is real. It
is accepted because a public evidence-forging API is a worse outcome than a
package containing its own adapter, and because architectural adapter
separation does not require sacrificing Go's package privacy.

**A `list` or `query` capability.** Deferred. Task 058 has not specified sort
order, cursor semantics, limits or parent scoping, and freezing any of them in
persistence now would decide them by accident.

## Consequences

Local state survives restart, which is what makes the evaluation foundation
usable as a product rather than a library demonstration.

Callers wanting a historical scorecard recompute it from stored evidence and a
policy they supply. That is more work at read time and one fewer thing that
can be stale.

The platform module gains a database driver. `check-platform-boundary` still
holds — the core's build graph contains no platform package, and the driver
never reaches it.

Adding a table later is a schema-version decision with a migration, not an
incidental change. That is the intended friction: this schema's shape is a
statement about what the platform considers authoritative.
