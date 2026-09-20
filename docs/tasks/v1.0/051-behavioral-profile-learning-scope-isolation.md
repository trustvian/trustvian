# 051 — Behavioral Profile: Learning-Scope Isolation

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [049](049-platform-architecture-alignment.md),
[050](050-public-serializable-decision-record.md) ·
**Blocks:** 052 onward · **The second and last core change
[049](049-platform-architecture-alignment.md) expects.**

## Objective

Let two analyses of the same actor, environment, and behavior use
independent learned state.

The effective learned-state identity becomes:

```text
LearningScope + ActorID + Environment
```

Behavioral identity — `StableFeatures`, `Fingerprint.ID` — is unchanged, in
every scope, byte for byte.

## Why

Today the learned-state key is `ActorID + Environment`. Two independent
behavioral profiles for the same actor cannot coexist: each teaches the
other.

[ADR 0022](../../adr/0022-core-platform-boundary.md) states the consequence
for the evaluation work that follows. Evaluating two candidates under one
actor identity trains one baseline, so each candidate's behavior becomes the
other's history, and the evaluation measures a baseline it polluted. Isolation
is a prerequisite for evaluation, and it belongs in the core.

But the motivating use case is not the mechanism. A learning scope is useful
wherever learned history must stay separate under one actor identity:
replaying a corpus without disturbing production learning, running a reference
profile beside a live one, or holding a pre-change baseline for comparison.
The core gets the generic capability; the platform gets to explain why it
chose a particular scope.

### The distinction this task turns on

```text
Fingerprint identity   —  what the actor did
Learning-state identity —  which history that observation belongs to
```

A learning scope **partitions learned history**. It does not **describe
behavior**. Everything in this task follows from keeping those apart:
scope never reaches a fingerprint, and a fingerprint never selects a scope.

## Scope

- `baseline.Key` gains a `Scope` field.
- `trustvian.WithLearningScope(string)` — one Engine option.
- `Baseline.Observe` reports whether the observation was admitted;
  `Store.Observe` propagates it; `Engine.Observe` returns the truth.
- `FileStore` snapshot version 2, reading version 1 as default scope.
- PostgreSQL schema version 2, with a real `1 → 2` migration.
- Tests across every store, plus an external-consumer proof.

## Non-Goals

No `Project`, `Agent`, `Candidate`, `EvaluationRun`, `Scorecard`,
`Promotion`, `Organization`, `Tenant`, behavioral diff, promotion gate,
platform module, platform persistence, control API, realtime bus, CLI
evaluation workflow, TUI, or WebUI. Task 052 is not started.

No scope on `Event`, on `StableFeatures`, on `Fingerprint`, or on
`DecisionRecord`. No change to fingerprint composition, scoring formulas,
policy semantics, learning eligibility, or the 512-fingerprint bound.

No public `Store` interface, no generic database abstraction, no second
mapping layer between a scope and a key.

## Technical Requirements

### Extend the baseline key

```go
type Key struct {
    Scope       string
    ActorID     string
    Environment string
}
```

`Scope` is the outermost partition and reads that way. The alternative —
namespacing by rewriting `ActorID` or `Environment` into `scope + ":" + id`
— is rejected outright. It destroys the domain meaning of two fields that
already have one, invents an escaping problem where none existed (an actor ID
containing `:` collides with a scope boundary), and creates a second identity
that can disagree with the `Baseline.Key` the value carries internally.

Extending the key instead makes every downstream property fall out rather
than needing to be arranged:

- `InMemory` shards by the whole `Key`, so isolation and per-key lock
  granularity are already correct.
- `FileStore` persists `Baseline`, which carries its own `Key`, so there is
  one persisted identity and no wrapper.
- PostgreSQL gets one composite primary key.
- `Freezer` is keyed by `Key`, so freezing becomes scope-aware with no new
  concept.
- `maxFingerprints` applies per `Baseline`, which is now per scope.
- `Result.BaselineKey` already carries the complete learned-state identity
  from `Analyze` to `Observe`, so no scope is ever recomputed.

### Scope is opaque

The core never parses it. No reserved syntax — `candidate/<sha>`,
`project/<id>`, `evaluation/<id>` — has meaning here. A caller may choose
such a string; the engine treats it as an opaque namespace and nothing more.

`WithLearningScope(string)` rather than an exported `LearningScope` type: a
named string type would enforce no invariant the engine actually has. There
is no validation to perform, no construction to guard, and no value the
engine would reject. An exported type would be cosmetic, and a public type
frozen at `v1` is not free.

### The default scope is the empty string

`NewEngine()` without the option uses `Scope: ""`, which is exactly what a
legacy `Key` deserializes to and what a legacy database row backfills to.
Existing baselines are therefore *already* in the default scope; the
migration has nothing to invent.

No generated ID, no clock, no UUID, no per-construction identity. Two engines
built the same way share a profile, which is the behavior every existing
caller has today.

The scope is fixed at construction, like every other Engine configuration.

### Scope may not come from the event

`Event` gains nothing. An event describes an observed action; a learning
scope describes how one Engine organizes history. Those are different
domains, and collapsing them would let whoever produces telemetry choose
which trusted profile they train — see [Security](#security--resource-bounds).

### `Engine.Observe` must tell the truth

`Baseline.Observe` already refuses an unknown fingerprint at capacity
([ADR 0019](../../adr/0019-bounded-fingerprint-admission.md)), but the store
call succeeded, so `Engine.Observe` reported `learned == true` regardless.
Task 049 recorded this as an open conflict, and a scope whose whole purpose
is to be measured cannot silently stop learning while reporting that it did.

The admission outcome now propagates:

```text
Baseline.Observe  → (Baseline, admitted bool)
Store.Observe     → (Baseline, learned bool, error)
Engine.Observe    → (learned bool, error)
```

| Situation | `learned` |
|---|---|
| Decision ineligible for learning | `false` |
| Eligible, fingerprint admitted | `true` |
| Unknown fingerprint, baseline at capacity | `false` |
| Known fingerprint, baseline at capacity | `true` |
| Store frozen | `false` |
| Store error | `false`, with the error |

This is an **observability** change, not an admission-policy change. ADR
0019's "refuse, never evict" stands, capacity refusal is still not an error,
and analysis is unaffected.

## Persistence / Migration

### FileStore

`fileSnapshotVersion` goes to `2`.

A version-1 file loads, and every baseline in it lands in the default scope —
`Key.Scope` is absent from the JSON and deserializes to `""`. Nothing is
invented and nothing is lost.

The bump is not decoration. Adding a field to JSON is normally additive, but
this field changes what a *record* identifies. One version-2 file can hold
`(scope A, actor X, prod)` and `(scope B, actor X, prod)`. A version-1 reader
ignores the unknown field, reads two baselines with the same key, and keeps
whichever it loads last — silently merging two profiles that exist precisely
to be separate. Refusing the file is the only acceptable outcome, and the
existing loader already refuses an unrecognized version.

```text
new binary + version 1 file → loads, all baselines in the default scope
new binary                  → writes version 2
old binary + version 2 file → refuses: "unsupported snapshot version"
```

Downgrade compatibility is deliberately sacrificed. A file an old binary
cannot read is a recoverable operational problem; two profiles silently
collapsed into one is corrupted learned state that nothing detects.

### PostgreSQL

`SchemaVersion` goes to `2`, and `Migrate` gains its first real upgrade path.
Until now it refused every version but its own, because there was no older
release to upgrade from.

The `1 → 2` migration, inside the existing transaction and advisory lock:

1. add `scope text NOT NULL DEFAULT ''` — every existing row backfills to the
   default scope, which is what those baselines already are;
2. drop the default, so a future insert that omits scope fails instead of
   silently landing in the default profile;
3. replace the primary key `(actor_id, environment)` with
   `(scope, actor_id, environment)`;
4. restamp `schema_version` on every row;
5. record version 2.

Step 4 is not cosmetic. `scripts/restore-postgres.sh` verifies that no row's
`schema_version` differs from the recorded version, so leaving old rows at 1
would make every post-migration backup fail its own restore check.

The primary key must include scope. Storing scope only inside the jsonb while
leaving SQL uniqueness on `(actor_id, environment)` would let the second
scope's insert collide with the first's row, and the authoritative row
identity would disagree with the `Key` inside the value it stores.

An older binary still fails closed against version 2 through the existing
`ErrSchemaVersionMismatch` path. Version 0 remains unrecognized — it is not an
older release, it is a version this build has no upgrade path for.

### Key agreement

Where a row carries both SQL key columns and a serialized `Baseline.Key`, the
two must agree. `Get` and `Observe` address rows by the full scoped key, and
the value written into a row is always the value produced for that key, so
disagreement is not constructible through the store's own API.

## Security / Resource Bounds

### Scope selection is a trust boundary

The engine's operator chooses the scope. An event producer must not, and this
is the reason scope is not an `Event` field, not derived from `SessionID` or
`TraceID`, and not read from `Attributes`.

If telemetry could select a profile, anyone who can emit an event could choose
which learned history they train — appending benign behavior to a profile a
policy decision later depends on, or steering their own traffic into a fresh
profile to escape an established baseline. Both are profile poisoning across a
trust boundary, and both are prevented structurally: there is no code path
from event content to `Key.Scope`.

### Capacity

`maxFingerprints` (512) is unchanged, not configurable, and still refuses
rather than evicts. It bounds **one Baseline**, and a Baseline is now
per-scope, so:

```text
scope A / actor X / production   — up to 512 fingerprints
scope B / actor X / production   — its own independent 512
```

Quota is not shared across scopes, and filling one leaves the others
untouched.

This constant has never bounded the *number* of baselines, and does not now.
A store holds as many keys as it is given, and the number of distinct scopes
is bounded by the caller's configuration — an Engine has exactly one — not by
this constant. What changed is that a caller can now create additional
baselines for one actor deliberately.

## Tests

**Default compatibility.** `NewEngine()` behaves exactly as before; a legacy
default-scope baseline is still found. Existing tests stay unmodified except
where a signature genuinely changed.

**Isolation.** Two engines over one store, same actor/environment/event,
different scopes: independent learning, independent maturity.

**Sharing.** Two engines over one store with the same scope see one baseline.

**Fingerprint stability.** Identical events in different scopes produce
identical `StableFeatures` and identical `FingerprintID`.

**Environment composition.** `(A, production)`, `(A, staging)`,
`(B, production)` are three independent identities.

**Session independence.** `SessionID`, `TraceID`, and `Attributes` — including
an attribute literally named `learning_scope` — change nothing.

**Learning gate.** Blocked, challenged, and approval-held decisions stay
excluded from learning in every scope.

**Capacity truthfulness.** Fill one scoped baseline to 512, then observe an
unknown fingerprint: not admitted, `learned == false`, nothing evicted. Then
observe a known one: updated, `learned == true`.

**Capacity isolation.** A full scope A leaves scope B with its own 512.

**File persistence.** Two scopes, same actor and environment, survive a
restart independently.

**Legacy upgrade.** A hand-written version-1 snapshot loads into the default
scope with identical learned behavior, and a version-2 snapshot is refused by
a version-1 reader.

**Store contract.** Scope isolation is added to the shared contract suite, so
every backend — InMemory, FileStore, PostgreSQL — is asserted against the same
guarantee rather than per-backend tests that can drift.

**PostgreSQL.** Isolation, scoped uniqueness, and a `1 → 2` migration that
preserves every existing baseline and lands it in the default scope.

**Freeze isolation.** Freezing one scoped key leaves the other learnable.

**Boundary.** `DecisionRecord`'s JSON gains no scope, candidate, evaluation,
or project field.

## Benchmarks

Run the existing `Analyze`, `Observe`, InMemory, and FileStore benchmarks
before and after. `Key` gains one string field, so it stays comparable and map
lookups keep working; the risk being checked is an accidental allocation on
the `Analyze` path, not a formula cost. No new benchmark unless one of those
shows a regression worth isolating.

## Documentation

New: this file, the ADR, `docs/adr/README.md`, `docs/tasks/v1.0/README.md`.

Updated: `docs/ARCHITECTURE.md` (learned-state identity), `docs/DOMAIN.md`
(Baseline identity), `docs/SECURITY.md` (scope as a trust boundary),
`docs/storage-guide.md` (both migrations), `docs/compatibility.md`,
`docs/sdk-guide.md` (`WithLearningScope`), `CHANGELOG.md`.
`docs/PERFORMANCE.md` only where per-baseline resource wording needs the scope
qualifier.

`ROADMAP.md` is not rewritten.

## Acceptance Criteria

- [ ] `baseline.Key` carries `Scope`; nothing else changed about it.
- [ ] `trustvian.WithLearningScope(string)` exists and is the only public
      surface added.
- [ ] Scope is absent from `StableFeatures`, `Fingerprint`, fingerprint hash
      input, `Event`, and `DecisionRecord`.
- [ ] Identical events in different scopes produce identical `FingerprintID`.
- [ ] Default-scope behavior is byte-identical to `v0.9` for existing callers.
- [ ] Two scopes with the same actor and environment coexist in every store.
- [ ] A version-1 snapshot loads into the default scope; a version-2 snapshot
      is refused by a version-1 reader.
- [ ] PostgreSQL migrates `1 → 2` atomically, preserving every baseline, and
      SQL uniqueness includes scope.
- [ ] `Engine.Observe` returns `false` when admission control refuses.
- [ ] A full scope leaves every other scope with independent capacity.
- [ ] No platform domain type appears in core runtime code.
- [ ] An external module configures a scope without importing `internal/*`.
- [ ] `gofmt`, `go vet`, `go test ./...`, `go test -race ./...` pass; both
      nested modules pass with `GOWORK=off`; PostgreSQL-gated suites pass.
