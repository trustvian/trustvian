# 058 — Local Control-Plane API and Ingest

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [052](052-evaluation-domain.md)–[057](057-local-platform-persistence.md) ·
**Blocks:** 059 onward

## Objective

Add the first authoritative platform service layer and a local HTTP adapter
over it.

```text
producer / future CLI · TUI · WebUI
              │
              ▼
        HTTP / JSON  ← adapter only
              │
              ▼
         ControlPlane ← authoritative services
              │
   ┌──────────┼───────────┐
   ▼          ▼           ▼
 domain   evaluation   store capabilities
              │
              ▼
           SQLite
```

Flows this task must make possible through one shared service layer: create
the project/agent/candidate/run hierarchy; drive a run's lifecycle; ingest
public `DecisionRecord` evidence into a running evaluation; inspect progress;
and compare two completed evaluations into a diff, scorecard and gate result.

HTTP contains no evaluation, diff, scorecard or gate logic. A future CLI, TUI
or WebUI calls the same service methods and gets the same semantics.

## Architecture

**The service layer is authoritative; HTTP is an adapter.** ADR 0023 already
says transports are adapters. This task is where that stops being an
aspiration: every rule about what an evaluation *is* lives in `ControlPlane`,
and the handler's whole job is JSON in, JSON out, plus status mapping.

The handler lives in a subpackage so it cannot reach package-private platform
state at all. The service lives in `platform` because restoring a collector
from a stored snapshot requires unexported fields — see below.

### The core boundary does not move

Ingest consumes `trustvian.DecisionRecord` and nothing else. No internal
`Result`, policy, trust or baseline type; no `internal/*` import; no core
change. [Task 050](050-public-serializable-decision-record.md) created the
record so persistence, realtime, aggregation and this API could share one
public engine boundary — this is that boundary being used as intended.

**No raw `Event` endpoint.** The control plane is not a second engine host.
A producer owning an `Engine` calls `Result.DecisionRecord()` and sends the
detached record. An HTTP route that ran `Engine.Analyze` would make the
platform a second behavioral execution path, with its own baseline to train
and its own drift from the producer's. Reconsidering that needs its own ADR.

## Service Layer

```go
func NewControlPlane(
    control ControlStore,
    evaluations EvaluationStore,
    ingest EvaluationIngestStore,
) (*ControlPlane, error)
```

Capability interfaces, never `*SQLiteStore`, `*sql.DB`, or a generic
`Database`. One backend may implement all three — SQLite does — but the
service must not know that, so [task 064](README.md) can supply PostgreSQL
without touching business logic.

Operations:

```text
CreateProject / Project
CreateAgent / Agent
CreateCandidate / Candidate
CreateEvaluationRun / EvaluationRun
StartEvaluationRun / CompleteEvaluationRun / FailEvaluationRun / CancelEvaluationRun
EvaluationProgress
IngestDecisionRecord
CompareEvaluations
```

No list, search, filter or pagination. Callers here know their IDs, and
freezing list semantics before a caller needs them would decide cursor, order
and scoping by accident. Narrow list capabilities can arrive when a real call
pattern demands them.

**Identity stays caller-owned.** The control plane generates no ID and adds no
UUID dependency; every create runs through task 052's constructors.

**Clocks are injected, and only choose lifecycle timestamps.** Domain
constructors never read a clock. The HTTP adapter supplies `Now` (defaulting
to `time.Now`) so tests are deterministic. It must never influence behavior,
fingerprints, scores, gate thresholds or identity.

## HTTP Adapter

Standard-library `net/http`; no router dependency. The handler owns no
database.

```text
POST /v1/projects                              GET /v1/projects/{id}
POST /v1/agents                                GET /v1/agents/{id}
POST /v1/candidates                            GET /v1/candidates/{id}
POST /v1/evaluation-runs                       GET /v1/evaluation-runs/{id}

POST /v1/evaluation-runs/{id}/start
POST /v1/evaluation-runs/{id}/complete
POST /v1/evaluation-runs/{id}/fail
POST /v1/evaluation-runs/{id}/cancel

GET  /v1/evaluation-runs/{id}/progress
GET  /v1/evaluation-runs/{id}/ingest-state
POST /v1/evaluation-runs/{id}/records

POST /v1/evaluations/compare
```

No collection `GET`. `POST /v1/projects` shares the path; listing stays
unsupported rather than guessed at.

**Explicit DTOs.** No JSON tag is added to `Project`, `Agent`, `Candidate`,
`EvaluationRun`, `EvaluationAggregate`, `BehaviorSnapshot`,
`EvaluationScorecard` or `EvaluationGateResult`. Task 052 kept domain values
separate from wire contracts; DTO conversion is mechanical and carries no
business logic. A structural test pins the absence of those tags.

## DecisionRecord Ingest

### Envelope

```json
{
  "version": "1",
  "sequence": "42",
  "behavioral_profile": "profile-a",
  "record": { "...DecisionRecord JSON..." }
}
```

The envelope is decoded strictly — unknown fields rejected, `version` other
than `"1"` rejected. The record is held as `json.RawMessage` and decoded
*without* `DisallowUnknownFields`, because `DecisionRecord` is an additive
stable contract: an older server must not reject a newer record merely because
the core added a field. Versioning belongs to the envelope, and task 050
deliberately put it there rather than inside the record.

**No `metadata map[string]any`.** `DecisionRecord` structurally excludes event
attributes, tool arguments, prompts and completions; an escape hatch here
would undo that privacy boundary.

### Behavioral profile binding

`DecisionRecord` carries no learning scope — ADR 0024 decided a caller that
chose the scope already knows it. So the envelope carries
`behavioral_profile` beside the record, and ingest requires

```text
envelope.behavioral_profile == run.BehavioralProfile()
```

Mismatch is rejected with no evidence or cursor advance. No profile, run or
candidate field is added to `DecisionRecord`.

### Lifecycle gate

Ingest is permitted only while the run is `Running`. `Pending`, `Completed`,
`Failed` and `Cancelled` are conflicts.

**Checked twice, and the transactional check is authoritative.** The service
prechecks before computing evidence; the durable commit transaction rechecks
inside the same transaction that writes. Without the second, a completion
committing between preflight and commit would let evidence land after the run
became terminal — leaving a completed evaluation whose evidence kept growing.

Both orderings are legitimate; only one interleaving is forbidden:

```text
ingest commits, then completion   → the record is part of the final evidence
completion commits, then ingest   → refused, evidence unchanged
completion commits, ingest after  → must never happen
```

Lifecycle transitions reuse the domain's immutable transitions and task 057's
compare-and-swap update; there is no second state machine.

`Completed` still means *execution ended*, not passed, safe, promotable or
approved. Comparison and gating stay separate.

## Ingest Sequencing

[Task 053](053-evaluation-result-aggregation.md) established that a duplicate
`DecisionRecord` **counts twice**, because reducer-level dedup would need
unbounded retention. So an HTTP retry cannot simply resubmit, and this task
owns the retry contract.

Forbidden: a `map[EventID]`, an unbounded EventID set, an idempotency-key
table, or a raw record table. Each grows with event count, and event history
is [task 067](README.md).

**A monotonic per-run sequence instead.** The first accepted record is
sequence `1`, then `2, 3, …`, supplied explicitly by the caller. Durable state
is one fixed-shape row per run:

```text
NextSequence
LastAcceptedDigest
```

That is O(1) per run, and it gives bounded retry semantics:

| Incoming sequence | Digest | Outcome |
|---|---|---|
| `== NextSequence` | any | applied once |
| `== NextSequence - 1` | same as last accepted | **replayed**, no re-aggregation |
| `== NextSequence - 1` | different | conflict |
| `< NextSequence - 1` | any | conflict |
| `> NextSequence` | any | conflict — a gap |

A conflict is an error, never a third success disposition.

**Concurrent identical submissions all succeed.** A client retrying a slow
request sends exactly the same sequence and record, and several may pass
preflight before any commits. The commit transaction distinguishes the two
reasons the cursor can have moved: advanced past this sequence *with this
digest* means another request committed the same logical record, which is a
replay; anything else is a conflict. One request applies, the rest replay,
none conflicts, and the aggregate advances once.

The store therefore reports a disposition — `committed` or `already
committed` — rather than only an error, because only the transaction can tell
those apart without a race. The counts come back from that same transaction,
so a replay response never combines a cursor from one moment with evidence
from another.

### Wire representation

`sequence` is `uint64` and travels as a canonical decimal **string**, so a
JavaScript client cannot silently lose precision later. `"01"`, `"+1"`,
`"-1"`, `"1.0"`, whitespace, empty and overflow are rejected — the same
canonical rule task 057 applied to stored counters.

Accepting a sequence that would push `NextSequence` past `MaxUint64` fails
closed before commit. Operationally unreachable, trivially testable, cheap to
get right.

### Record digest

SHA-256 over `json.Marshal` of the **decoded** `DecisionRecord`, not the raw
HTTP bytes. Whitespace and key ordering therefore do not make a different
logical record, which is what makes a genuine retry replay rather than
conflict. The digest is storage metadata, not behavioral identity — it is
emphatically not `FingerprintID` or `EventID`.

**An empty digest is legitimate in exactly one place.** A run migrated from a
task 057 database has evidence, no cursor row, and no digest to derive — the
record predates this code. A *persisted* cursor row is different: the commit
path always records a digest, so an empty one there cannot have been produced
legitimately and is `ErrStoreCorrupt`. Persisted digests are exactly 64
lowercase hex characters, refused rather than normalized, because anything
else was written by something that is not this code.

## SQLite Schema v2

Task 057 shipped v1; this task needs a real forward migration.
`SchemaVersion` becomes **2**, adding one table:

```text
platform_evaluation_ingest_state
  run_id         PRIMARY KEY, FK → platform_evaluation_runs(id)
  next_sequence  canonical uint64 text
  last_digest    canonical SHA-256 hex, empty when none
```

No record payload, no EventID list, no receipt history.

| On open | Behavior |
|---|---|
| no tables, no metadata | create v2 directly |
| valid, complete v1 | migrate transactionally to v2, preserving every row |
| valid, complete v2 | continue |
| partial v1 or v2 | fail closed |
| unknown or newer version | fail closed |

A v1 database is **never** stamped v2 without migrating. Migration and version
update commit together, so a failure leaves a readable v1 rather than a
half-stamped hybrid.

**Concurrent openers converge.** A racing initializer can make any statement
fail — a locked database, or the table another opener just created — not only
the commit. So a failed attempt rolls back and inspects the durable schema
once: a complete, valid v2 means somebody else finished the job; anything else
keeps the original error. No loop, no driver error-text parsing, and the
inspection runs after rollback because the store holds a single connection.

### Legacy evidence and the initial sequence

A v1 database may hold evidence with no ingest cursor. For a run with no
ingest-state row:

```text
no evidence                     → NextSequence = 1
aggregate with RecordCount = N  → NextSequence = N + 1
RecordCount == MaxUint64        → fail closed, no valid next sequence
```

because one ingest represents one aggregate record. There is no known digest
for the legacy last record, and none is fabricated — so a retry of sequence
`N` against legacy evidence cannot be proven identical and is a conflict. The
first task 058 ingest makes the cursor explicit.

### Cursor and evidence must agree

Once a cursor exists, `NextSequence == aggregate.RecordCount + 1` for
API-managed evidence. Disagreement is `ErrStoreCorrupt` — there is no
principled way to pick which side is right.

## Atomicity

Reads spanning several queries run inside one transaction too. Loading
evidence issues four — aggregate, snapshot header, entries, owning run — and
outside a transaction each is its own implicit one, so a concurrent commit
between them yields a snapshot from after the write beside an aggregate from
before it. The consistency checks then report that as corruption, which is
right for an inconsistency that should never have been observable.

One SQLite transaction commits the aggregate row, the snapshot header, every
behavior entry, the advanced `NextSequence` and the digest. Either all old
state survives or all new state does. Never:

```text
evidence advanced but sequence not      → the next retry double-counts
sequence advanced but evidence missing  → a record silently vanished
```

## Ingest Algorithm

1. load the run; require `Running`;
2. require envelope profile == run profile;
3. load evidence, or initialize empty evidence if genuinely absent;
4. restore an ephemeral `BehaviorCollector` from the trusted snapshot;
5. apply the record to the aggregate;
6. apply it to the collector;
7. build the next snapshot;
8. commit evidence + cursor atomically;
9. return `applied` or `replayed`.

No raw record is retained at any step.

### Restoring a collector

There is no public `BehaviorSnapshot → BehaviorCollector` constructor and this
task does not add one: exporting it would let any caller forge collector
state. A package-private `behaviorCollectorFromSnapshot` rebuilds bound flag,
identity, observation count, entries, the reverse `byBehavior` index and
completeness from an **already trusted bound** snapshot, deep-copying maps so
nothing aliases the snapshot's backing storage.

This is what lets a restarted process continue a running evaluation from task
057 evidence.

### Saturation is degraded evidence, not failure

At 512 distinct behaviors the collector saturates. Ingesting the 513th unseen
behavior means the aggregate accepts the record while the collector returns
`ErrBehaviorCapacity` and becomes incomplete. That is **not** a failed ingest:

```text
persist  advanced aggregate + incomplete snapshot + advanced sequence
respond  disposition = applied, behavior_complete = false
```

Later records keep advancing aggregate evidence while the snapshot stays
incomplete and bounded at 512. Nothing is evicted, and behavioral evidence is
never reported complete when it is not.

**Only `ErrBehaviorCapacity` gets this handling.** An environment mismatch,
fingerprint conflict, invalid behavior identity, or overflow rejects the whole
ingest with no evidence or cursor advance — as does aggregate validation
failure.

## Concurrency

Two requests racing one run must produce a deterministic outcome: same
sequence and digest — one applies, the other replays; same sequence, different
record — one applies, the other conflicts; two different future sequences —
the gap conflicts. No lost update, no duplicate count. SQLite's serialization
is an implementation detail; the semantics are what tests pin.

## Comparison

```go
CompareEvaluations(ctx, referenceRunID, candidateRunID, EvaluationGateLimits)
    (EvaluationComparison, error)
```

Loads both runs, requires both `Completed`, loads both evidence pairs, then
`CompareBehaviorSnapshots` → `NewEvaluationScorecard` →
`NewEvaluationGatePolicy` → `EvaluateEvaluationGate`, returning one
read-shaped value. Handlers never repeat those steps, and no handler calls
those functions directly.

**Completed runs only.** A running evaluation has mutable evidence; a failed
or cancelled one is not a completed evaluation. Anything else is a conflict.

**Incomplete evidence refuses comparison** through task 054's existing
semantics. No scorecard or gate result is manufactured. The API says the
behavioral comparison is unavailable because evidence saturated — not
"unsafe", not "failed policy". Those are judgements the evidence cannot
support.

The result is not persisted. It is recomputed from stored evidence, which is
ADR 0030's one-source-of-truth decision.

### Gate limits

All three task 056 limits are required inputs, and **zero is a valid strict
limit**, so omitted and zero must not collapse. The wire DTO makes each
required; a request omitting one is `400`, never silently zero.

## Evaluation Progress

Factual only: run identity, status, record count, behavior observation count,
distinct behavior count, behavior completeness, next ingest sequence.

Every count comes from one transactional read, so a report never shows a
cursor at N+1 beside an aggregate already at N+2. For API-managed evidence
`NextIngestSequence == RecordCount + 1` holds in every observation. The run's
status is read separately on purpose: it is independent of evidence, and a
lifecycle transition racing the read is a genuinely concurrent fact rather
than a torn one. No
pass/fail, promotable, or safety field — a running evaluation has progress,
not a verdict.

## Request Bounds

```go
const maxAPIRequestBody = 256 << 10 // 256 KiB
```

`DecisionRecord` is fixed-shape but not size-bounded — several fields carry
caller-supplied strings — and task 050 explicitly deferred a network limit
here. The body is capped **before** decoding; oversized requests are `413`.
Boundary and boundary+1 are both tested. 256 KiB is a transport bound, not a
claim about typical records.

## Wire Versioning

Two versions solving two problems:

```text
path /v1/...        the control-plane HTTP API shape
envelope "version"  the DecisionRecord wrapper
```

No version field is added to `DecisionRecord` itself.

## Error Semantics

```json
{ "version": "1", "error": { "code": "conflict", "message": "..." } }
```

Codes: `invalid_request`, `not_found`, `already_exists`, `conflict`,
`payload_too_large`, `unsupported_media_type`, `unsupported_version`,
`incomplete_evidence`, `internal`.

| Status | Meaning |
|---|---|
| 200 | successful read, action, ingest or comparison |
| 201 | resource created |
| 400 | malformed or invalid request |
| 404 | missing resource |
| 409 | already exists, lifecycle conflict, sequence conflict |
| 413 | request too large |
| 415 | unsupported content type |
| 500 | corrupt storage or unexpected internal failure |

`ErrStoreCorrupt` is **never** a 400. Persistence corruption is a server
failure the client cannot fix. No SQL text, database path, stack trace or
driver error reaches a response body.

JSON routes require `application/json`, allowing parameters such as charset;
anything else is `415`.

### Ingest response

```json
{
  "version": "1",
  "disposition": "applied",
  "next_sequence": "43",
  "record_count": "42",
  "behavior_complete": true
}
```

`disposition` is `applied` or `replayed`. A conflict is an error, not a third
success. No automatic retry loop exists anywhere — the sequence protocol *is*
the retry mechanism.

### Determinism

For the same stored state and limits, a comparison response is semantically
deterministic: no generated request ID, random value, server timestamp, or
map-iteration ordering as content. Diagnostic headers may vary; the domain
response may not.

## Security

Covered in `docs/SECURITY.md`: body bounded before decode; raw event payloads
impossible by schema; profile bound to the run; ingest gated on `Running`;
monotonic sequencing preventing duplicate aggregation; same-sequence /
different-digest, gap and stale sequences failing closed; atomic cursor and
evidence commit; O(1) cursor rather than history; cursor/evidence corruption
failing closed; sanitized internal errors; no SQL or path leakage; no realtime
or listener; no CORS wildcard; no authentication claim; and HTTP computing no
business logic.

**No authentication or access control.** Local v1 mode does not require it and
inventing users, tokens, sessions or roles here would be a security model
nobody reviewed — [task 070](README.md) owns that. This task binds no network
listener; whichever later task composes one must default to loopback.

## Resource Bounds

```text
request body        ≤ 256 KiB
ingest state        1 row per run, no per-event row
BehaviorSnapshot    ≤ 512 entries
BehaviorDiff        ≤ 1024 deltas
comparison response bounded by those structures
```

No unbounded queue, dedup map, or retention window.

## Tests

**Service.** Create/get hierarchy. Lifecycle: pending→running→completed,
pending→cancelled, running→failed, invalid transitions rejected. Ingest
refused in every non-running status. Profile mismatch rejected with no
advance. First ingest at sequence 1. Ordered 1,2,3. Gap rejected. Last-sequence
retry with the same record replays without re-aggregating. Same sequence with
a different record conflicts. Older sequence conflicts. Two concurrent
same-sequence requests never both aggregate.

**Restart continuation** (mandatory): ingest 1..N, close the store, reopen,
`EvaluationIngestState` reports N+1, ingest N+1 succeeds, the aggregate
advances, and the snapshot keeps prior behaviors plus the new one — proving
the private restoration path with no exported constructor.

**Saturation** (mandatory): 512 distinct behaviors applied normally; the 513th
returns applied with `behavior_complete=false`; aggregate count 513, snapshot
observation count 512, `Complete()==false`, sequence advanced; a further
record keeps advancing the aggregate while the snapshot stays bounded; and a
later comparison refuses the incomplete evidence.

**Atomicity**: a failure injected between evidence and cursor writes leaves
old evidence *and* old sequence — never one advanced without the other.

**Migration**: fresh opens at v2; a **real structural v1 fixture** — built
from task 057's schema statements, not by calling v2 creation and rewriting
the version number — migrates to v2 with every row preserved and ingest
usable; legacy evidence with `RecordCount = N` yields initial sequence `N+1`
with no fabricated digest and a conflicting retry of `N`; partial and unknown
versions fail closed; a failed migration rolls back to readable v1.

**HTTP**: route and method correctness; malformed JSON; unsupported envelope
version; unknown envelope field; additive unknown field *inside* the record
tolerated; missing required field; missing gate limit distinguished from zero;
non-canonical sequence; wrong media type; body at the limit and one byte over;
not found; duplicate create; lifecycle conflict; ingest applied, replayed and
conflicting; compare success; compare before completion; incomplete evidence
refused; corruption sanitized to 500 with no SQL or path leakage.

**Real Engine → HTTP → comparison** (mandatory): two real `Engine`s produce
records through `Result.DecisionRecord()`, each posted as an envelope to
`/v1/evaluation-runs/{id}/records` — no hand-built records. Reference runs
`shell.read`, `db.query`; candidate runs `shell.read`, `shell.execute`. After
completing both, `POST /v1/evaluations/compare` shows `shell.read` shared,
`db.query` removed, `shell.execute` added, correct scorecard identity, and a
gate result reflecting exactly the supplied limits. No hard-coded fingerprint
hashes.

**Structural**: domain types still carry no JSON tags; the HTTP package does
not reference `CompareBehaviorSnapshots`, `NewEvaluationScorecard`,
`EvaluateEvaluationGate` or `SQLiteStore`.

## Mutation Tests

Each must fail a targeted test, and all are restored before commit: the
running-status requirement; profile binding; the expected-sequence check;
same-sequence/different-digest conflict; replay not double-counting; gap
rejection; cursor/evidence atomicity; cursor/evidence consistency validation;
collector restoration after restart; saturation handling; the request-size
limit; missing-versus-zero gate limits; handler/service separation; and
v1→v2 migration preservation.

## Benchmarks

`IngestDecisionRecord` and `CompareEvaluations` through service methods over
local SQLite. No latency gate — ingest cost includes SQLite persistence, and
comparison includes loading two bounded evidence states and diffing up to 512
behaviors a side. Durability is not weakened for numbers.

## Documentation

New: this file, ADR 0031, the ADR index, `docs/tasks/v1.0/README.md`.
Updated: `ARCHITECTURE.md`, `DOMAIN.md`, `SECURITY.md`, `ROADMAP.md`,
`PERFORMANCE.md`, `compatibility.md` (the `/v1` route shapes, envelope
version, field names and error/status semantics as intended contract — with
message *wording* explicitly observational, not contract), and `CHANGELOG.md`.

## Acceptance Criteria

- [ ] HTTP calls only control-plane services; no duplicate evaluation logic.
- [ ] `DecisionRecord` is the evidence boundary; no raw `Event` route.
- [ ] Profile travels beside the record and is matched against the run.
- [ ] Ingest only while `Running`.
- [ ] Monotonic sequence; replay, gap and stale all fail closed.
- [ ] Ingest state is O(1) per run; no EventID set, no record table.
- [ ] Cursor and evidence commit atomically and are validated together.
- [ ] Ingest resumes after restart without an exported restore constructor.
- [ ] Saturation advances aggregate and sequence, snapshot stays incomplete.
- [ ] Comparison requires completed runs and refuses incomplete evidence.
- [ ] Gate limits caller-supplied; zero distinct from omitted.
- [ ] Body bounded before decode; internal errors sanitized.
- [ ] Domain values carry no JSON tags; DTOs own the wire.
- [ ] Schema version 2 with a real, row-preserving v1→v2 migration.
- [ ] No realtime, CORS, auth, promotion, raw history, binary, or core change.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass; `GOWORK=off` holds;
      `check-modules` and `check-platform-boundary` pass.
