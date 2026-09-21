# 0031 — The control plane owns ingest, and HTTP is an adapter

**Status:** Accepted

## Context

[Task 058](../tasks/v1.0/058-local-control-plane-api-and-ingest.md) adds the
first platform service layer and a local HTTP surface over it.

Two things get decided here that are hard to undo. Where evaluation logic
lives once more than one caller exists — HTTP now, a CLI, TUI and WebUI
later — and what happens when an ingest request is retried, given that
[ADR 0026](0026-evaluation-aggregation-is-bounded-evidence.md) deliberately
made a duplicate record count twice.

## Decision

Task 058 introduces an authoritative control-plane service layer. HTTP is a
versioned adapter over those services. Evaluation ingest consumes the public
`DecisionRecord` boundary and uses a durable per-run monotonic sequence to
make retries fail closed without retaining raw event history.

### 1. The services own the logic

Every rule about what an evaluation is — which lifecycle transitions are
legal, when evidence may be ingested, what a comparison requires — lives in
`ControlPlane`.

The alternative is logic in the handler, which works exactly until the second
caller arrives. A CLI that reimplemented "ingest only while running" would
drift from the HTTP version, and the drift would be invisible until the two
disagreed about a real evaluation. One implementation is the only arrangement
where a future CLI, TUI and WebUI cannot mean different things by the same
operation.

The service depends on capability interfaces rather than `*SQLiteStore`, so
task 064 can supply PostgreSQL without touching any of it.

### 2. Handlers do not compute diff, scorecard, or gates

No handler calls `CompareBehaviorSnapshots`, `NewEvaluationScorecard` or
`EvaluateEvaluationGate`. `CompareEvaluations` does, once.

This is the same reasoning as point 1 applied to the part most likely to be
duplicated: a comparison is four calls in a fixed order with preconditions
between them, which is precisely the shape someone reimplements "just for this
endpoint". The HTTP package lives in a subpackage and a structural test pins
the absence of those references.

### 3. DecisionRecord is the ingest payload

[Task 050](../tasks/v1.0/050-public-serializable-decision-record.md) created
the record so persistence, realtime, aggregation and a control API could share
one public engine boundary. This is that boundary being used as intended.

It also carries a privacy property worth keeping: the record structurally
excludes event attributes, tool arguments, prompts and completions. So the
ingest envelope gets no `metadata map[string]any` escape hatch — that field
would quietly undo the boundary, and it would be adopted immediately because
it is convenient.

### 4. Raw Event analysis is not hosted here

No route accepts an `Event` and runs `Engine.Analyze`.

The producer owning the engine produces the record. A control plane that
analyzed events would be a second behavioral execution path with its own
baseline to train, its own learning-scope decisions, and its own drift from
the producer's — and the platform would be back inside the engine it was
separated from in [ADR 0022](0022-core-platform-boundary.md).

Reconsidering this needs its own decision record, not a route.

### 5. The behavioral profile travels beside the record

`DecisionRecord` carries no learning scope.
[ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md) decided a
caller that selected the scope already knows it, and that scope is not
behavioral identity.

So the envelope carries `behavioral_profile` next to the record, and ingest
requires it to match the run's profile. Adding the field *to* the record
instead would push a platform concept into the core's public type and make
every producer carry an evaluation concern — exactly the direction ADR 0022
forbids.

### 6. Duplicate ingest cannot simply be ignored

ADR 0026 made a duplicate `DecisionRecord` count twice, because dedup inside
the reducer would need unbounded retention. That decision is correct and
stands — but it means an HTTP retry, the most ordinary thing a network client
does, would corrupt the evidence.

Retry semantics therefore belong here, at the transport-facing layer that
knows what a *request* is. The reducer keeps counting what it is given.

### 7. EventID cannot become the dedup key

The obvious fix — remember which EventIDs were seen — is an unbounded set that
grows with every event, in memory or in a table. That is event history with
extra steps, and event history is [task 067](../tasks/v1.0/README.md), which
owns the retention, privacy and volume questions this task must not answer by
accident.

An idempotency-key table has the same shape and the same problem.

### 8. A monotonic per-run sequence gives bounded retry semantics

The caller supplies an explicit sequence, starting at 1.

| Incoming | Digest | Outcome |
|---|---|---|
| expected | any | applied once |
| last accepted | same | replayed, no re-aggregation |
| last accepted | different | conflict |
| older | any | conflict |
| future | any | conflict — a gap |

Every case is decided from two values, and every ambiguous case fails closed.
A retry of the immediately preceding record is the only thing that succeeds
without applying, and only when the record is provably identical.

The digest is SHA-256 over the marshalled *decoded* record rather than the raw
bytes, so whitespace and key ordering do not make a genuine retry look like a
different record. It is storage metadata, not behavioral identity — not
`FingerprintID`, not `EventID`.

### 9. Ingest state is O(1) per run

`NextSequence` and `LastAcceptedDigest`. One fixed-shape row.

The bound is the point. It holds for a run that ingested ten records and one
that ingested ten million, which is what separates a retry mechanism from an
archive.

### 10. Cursor and evidence commit together

One transaction covers the aggregate, the snapshot header, every entry, the
advanced sequence and the digest.

Split, the two failure modes are both silent and both bad: evidence advanced
without the cursor means the next retry double-counts; the cursor advanced
without evidence means a record vanished while the protocol believes it
arrived. Neither surfaces as an error — they surface later as wrong numbers.

The same reasoning extends to reading: a cursor that disagrees with the
aggregate's record count is `ErrStoreCorrupt`, because there is no principled
way to choose which side is right.

### 11. The schema moves v1 → v2

The ingest cursor is durable state, so it is a schema change, and
[ADR 0030](0030-local-persistence-stores-authoritative-bounded-state.md)
already said adding a table is a versioned decision rather than an incidental
change.

A v1 database is migrated, never stamped. Migration and the version update
commit together, so a failure leaves a readable v1 rather than a half-stamped
hybrid — the same fail-closed discipline task 057 applied to initialization.

Legacy evidence needs one careful rule: a run with `RecordCount = N` and no
cursor starts at `N + 1`, because one ingest is one aggregate record. No
digest is fabricated for the legacy last record, so a retry of `N` cannot be
proven identical and conflicts. Inventing a digest there would manufacture
proof that does not exist.

### 12. No raw event history is created

One cursor row per run, and nothing else. No `events`, `decision_records`,
`ingest_records`, `payloads` or `record_history` table.

The aggregate exists precisely so the stream need not be kept, and task 067
owns the question of whether it ever should be.

### 13. Request bodies are bounded before decode

`DecisionRecord` is fixed-shape but not size-bounded — several fields carry
caller-supplied strings — and task 050 explicitly deferred a network limit to
this task.

The limit is applied before decoding, not after, because a limit that runs
after parsing has already spent the memory it was meant to protect.

### 14. No realtime appears here

No SSE, WebSocket, pub/sub, channel registry, event bus or polling loop. HTTP
requests are synchronous. Task 059 owns realtime and deserves to choose its
own delivery model rather than inherit one improvised inside a request
handler.

Nor is there CORS, authentication, or access control. A wildcard origin with
no caller is a permission granted to nobody in particular, and inventing
users, tokens or roles here would be a security model nobody reviewed — task
063 and task 070 own those. This task also binds no listener; whichever task
composes one must default to loopback.

### 15. API DTOs stay separate from domain values

No JSON tag is added to `Project`, `Agent`, `Candidate`, `EvaluationRun`, or
any evidence type.

[ADR 0025](0025-platform-domain-values-with-caller-owned-identity.md) made
those unexported-field values on purpose. A JSON tag would make the wire
format a property of the domain type, so renaming a field would silently break
clients, and an added field would silently appear on the wire. The DTOs own
the wire; conversion is mechanical and carries no logic.

Two version numbers exist for two problems: the `/v1` path versions the API
shape, and the envelope `"version"` versions the record wrapper. Neither goes
inside `DecisionRecord`.

### 16. No promotion logic ships here

A gate `PASS` still has no side effect, and no `Promote`, `Deployment`,
`EnvironmentTransition` or `PromotionRecord` exists. Task 066 owns promotion,
which needs authorization and an audit trail none of these types carry.

## Alternatives considered

**Logic in handlers, services later.** Rejected: the second caller is already
planned, and retrofitting a service layer after a CLI exists means migrating
two implementations that have already diverged.

**Server-assigned sequences.** Simpler for the client, but it removes the
property the sequence exists for: a client that does not know which number its
record got cannot retry safely, because it cannot say which record it is
retrying.

**Idempotency keys.** Familiar, and unbounded. It is point 7 with a different
name.

**Rejecting the whole ingest on behavioral saturation.** Rejected: the
aggregate can still legitimately accept the record, and refusing would discard
sound decision evidence because *behavioral* evidence filled up. The record is
applied, the snapshot stays incomplete, and the response says so — degraded
evidence reported honestly rather than a failure or a false completeness.

## Consequences

Producers must track a per-run sequence. That is a real burden, and it is the
price of retry safety given that duplicates count twice; the alternative was
unbounded server state.

A retry is only replayable one step back. Retrying two records ago conflicts
and needs an operator decision — deliberate, because the alternative is
remembering more history.

The service layer is the place every future interface goes through. When the
CLI and TUI arrive they should add nothing here but call sites; if either
needs new logic, that logic belongs in `ControlPlane`, not in the caller.

Comparison recomputes from stored evidence on every request. That is more work
per call than reading a stored verdict, and it is what keeps ADR 0030's single
source of truth intact.
