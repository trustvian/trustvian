# 0024 — Learning scope is a baseline-key dimension, not behavioral identity

**Status:** Accepted

## Context

Learned state is keyed by `baseline.Key{ActorID, Environment}`. One actor in
one environment therefore has exactly one behavioral history, and anything
that needs two independent histories for that actor has nowhere to put the
second.

[ADR 0022](0022-core-platform-boundary.md) established that isolation is a
core concern and deliberately left the mechanism open:

> `baseline.Key` was made composite in `v0.4` against this kind of need, but
> whether isolation extends that key, scopes the store, or takes a third shape
> is a design question with its own task.

This is that decision. The motivating consumer is evaluation — two candidates
evaluated under one actor identity would each become the other's learned
history, and the evaluation would measure a baseline it polluted — but the
mechanism must not know that. ADR 0022 also fixed the boundary conditions:
`SessionID` must not become baseline identity, `EvaluationRunID` must not
become fingerprint identity, and candidate metadata must never become a
fingerprint dimension.

## Decision

`baseline.Key` gains a third field:

```go
type Key struct {
    Scope       string
    ActorID     string
    Environment string
}
```

`Scope` partitions learned history. It is selected once, at Engine
construction, through `trustvian.WithLearningScope(string)`, and the empty
string is the default scope — the one every existing caller and every existing
persisted baseline is already in.

Four consequences are load-bearing.

### Scope is not behavioral identity

The whole decision rests on one separation:

```text
Fingerprint identity    — what the actor did
Learning-state identity — which history that observation belongs to
```

`Scope` appears in neither `StableFeatures` nor `Fingerprint`, and not in the
fingerprint hash input. The same event analyzed in two scopes produces the
same `FingerprintID`; only the learned evidence compared against it differs.

That is not a detail to preserve incidentally. If scope entered fingerprint
identity, every new scope would make an actor's entire behavioral repertoire
look brand new — which is exactly the failure ADR 0022 predicted for treating
a git SHA as a behavioral dimension, arrived at from a different direction.
Scope changes *which history you compare against*, never *what you are
comparing*.

### Scope is configuration, never telemetry

`Event` gains no scope field, and no scope is derived from `SessionID`,
`TraceID`, or `Attributes`. There is no code path from event content to
`Key.Scope`.

This is a trust boundary, not tidiness. An event producer that could select
its own profile could append benign behavior to whichever history a later
policy decision depends on, or route its own traffic into a fresh profile to
escape an established baseline. Both are profile poisoning, and both are
prevented structurally rather than by validation. The engine's operator
configures scope; whoever emits events does not.

### Scope is opaque

The core never parses it. `candidate/<sha>`, `project/<id>` and similar
conventions have no meaning here — a caller may choose such a string, and the
engine treats it as a namespace with no structure. The option takes a plain
`string` rather than an exported `LearningScope` type, because there is no
invariant such a type would enforce: nothing to validate, nothing to
construct, no value the engine rejects. A cosmetic public type frozen at `v1`
is not free.

This is what keeps the core generic. The platform may later map a
`BehavioralProfile` onto a scope string; the engine will not know it did.

### The empty scope is the default, and therefore the migration

Legacy `Key` JSON has no `Scope` field and deserializes to `""`. A legacy
PostgreSQL row backfills to `''`. Existing baselines are already in the
default scope, so the migration invents nothing and moves no learned state
between profiles.

## Alternatives considered

**Namespace by rewriting `ActorID` or `Environment`** — `actor = scope + ":" +
actorID`. Rejected. It destroys the domain meaning of two fields that have
one, invents an escaping problem where none existed (an actor ID containing
the separator collides with a scope boundary), and creates a second identity
that can disagree with the `Key` the `Baseline` value already carries.
Everything downstream would then need to remember to apply the same rewrite,
and any place that forgot would read the wrong profile silently.

**A separate scoped store wrapper, or a store-per-scope.** Rejected. It makes
scope a property of *where state lives* rather than *what state is*, which
splits identity across two layers that can disagree. It also multiplies
resources per scope — a PostgreSQL pool per profile — and leaves `Freezer`,
the 512-fingerprint bound, and the file snapshot each needing their own
scope story. Extending the key gives all of those the right behavior without
any of them being told scope exists.

**A per-call scope argument on `Analyze`.** Rejected. It would put profile
selection on the hot path and, worse, in reach of whatever assembles the call
from telemetry — reintroducing the trust-boundary problem the `Event`
exclusion exists to close. Engine configuration is the same immutability
every other option already has.

**Add scope to `DecisionRecord`.** Rejected for now. A caller that selected a
scope already knows it and can store it alongside the record, which is
precisely the division [task 050](../tasks/v1.0/050-public-serializable-decision-record.md)
drew between engine evidence and consumer metadata. Adding a field to a
frozen public type needs a requirement that cannot be met outside it, and no
such requirement exists yet. The field can be added in a minor release if one
appears; it cannot be removed.

**Keep writing FileStore snapshot version 1.** Rejected. Adding a JSON field
is normally additive, but this field changes what a record *identifies*: one
file can now hold two baselines that an unscoped reader sees as the same key,
and it will keep whichever it happens to load last. Silent profile merging is
corrupted learned state that nothing detects, so the version is bumped and an
older reader refuses the file instead. Downgrade compatibility is the lesser
loss.

A conditional bump — write version 1 while every scope is default, version 2
once any is not — would preserve downgrades in the common case and is equally
correct. It was rejected for predictability: it makes the on-disk version a
function of runtime data, so a downgrade path that tested clean can stop
working later, at the moment the first scope is introduced, rather than
immediately after the upgrade when someone is still watching.

## Consequences

**Capacity is now per scope.** `maxFingerprints` (512) bounds one `Baseline`,
and a `Baseline` is now per scope. `(A, actor X, prod)` and `(B, actor X,
prod)` each hold their own 512, and filling one leaves the other untouched.
[ADR 0019](0019-bounded-fingerprint-admission.md)'s refuse-never-evict policy
is unchanged. The constant has never bounded the number of baselines and does
not now — a store holds as many keys as it is given.

**`Engine.Observe` now tells the truth about capacity.** Admission refusal
used to be invisible: `Baseline.Observe` declined an unknown fingerprint at
capacity, the store call succeeded, and `Engine.Observe` reported
`learned == true` anyway. Task 049 recorded this as an open conflict, and a
scope that exists to be measured cannot silently stop learning while reporting
that it did. The outcome now propagates from `Baseline.Observe` through
`Store.Observe` to `Engine.Observe`. This is an observability change; nothing
about what gets admitted changed.

**Both persisted formats version.** FileStore goes to snapshot version 2 and
PostgreSQL to schema version 2, each reading and upgrading its predecessor and
each refusing a version it does not recognize. PostgreSQL's primary key
becomes `(scope, actor_id, environment)`, because storing scope only in the
jsonb would let the second scope's row collide with the first's while the
authoritative row identity disagreed with the `Key` inside the value.

**The store port's `Observe` returns three values.** `(Baseline, bool, error)`
rather than `(Baseline, error)`. The port stays narrow — no new method, no new
concept — and every implementation answers the same question.

**One string is added to a map key on the `Analyze` path.** `Key` stays
comparable and allocation-free; the cost is one extra string comparison per
lookup.
