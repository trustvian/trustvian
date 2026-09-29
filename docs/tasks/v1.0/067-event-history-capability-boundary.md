# 067 — Event-History Capability Boundary

Status: specified; implemented
Milestone: `v1.0`
Depends on: [084](084-correlation-operational-evidence.md) (implemented) — the
correlation and operational fields this task retains
Blocks:
[085](082-agent-inspection-and-evaluation-depth.md#085--evidence-links-from-a-finding-to-the-observations-behind-it),
then [076](076-behavioral-evidence-explorer.md) and
[088](082-agent-inspection-and-evaluation-depth.md#088--review-decisions-and-annotations)
Approved in: [ROADMAP.md § v1.0](../../ROADMAP.md#v10--local-first-behavioral-security-platform)

## Objective

Make **per-observation history durable**, bounded, and honest about its own
limits — so a decision can be read back after the run that produced it ended.

Until now the platform retained two things per evaluation run: a fixed-shape
aggregate and a capped behavior snapshot. Both are reductions. Neither can
answer *which* observation scored 0.91, *when* it happened relative to the one
before it, or *what trace it belonged to* — because no row describes one
observation. Realtime carried that detail and dropped it when the connection
closed ([ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md)).

This task adds the row, and nothing that reads it.

## Three words this task keeps apart

Inherited from [083](083-behavioral-layer-classification.md), unchanged:

| Term | What it is | Owner |
|---|---|---|
| **Observation** | One observed action: one span in, one `Event`, one `Result`, one `DecisionRecord` out | the engine, per call |
| **Behavioral identity** | The `StableFeatures` tuple and the `Fingerprint` derived from it | the engine |
| **Counted change** | What a comparison reports as added or removed | the control plane |

**This task retains observations.** It changes no behavioral identity, no
counted change, and no decision the engine makes.

## What this task does not do

- **No views.** No session view, no trace tree, no timeline, no waterfall. 076.
- **No resolution.** No path from a gate check or a behavioral delta to the
  observations behind it. That is 085, which this task exists to unblock and
  deliberately does not pre-empt.
- **No counting fold.** 083's deferred half needs a correlation structure and
  an out-of-order rule; retaining a parent id supplies neither.
- **No cost, token or pricing field.** 087.
- **No recomputation.** Every retained value is written as the record carried
  it. Nothing here derives a score, a verdict or a rate.
- **No new behavioral signal.** The engine does not read history back. A
  retained observation influences no future decision, no baseline and no
  fingerprint — history is evidence, not input.
- **No content, of any kind.** See [§ Privacy](#privacy-the-boundary-is-the-schema).

## The retention contract

### The unit and its identity

One retained row is one **observation**: the projection of one accepted
`DecisionRecord`.

Its identity is **`(RunID, Sequence)`**, where `Sequence` is the ingest
sequence the retry contract already assigns.

That choice is the centre of this design, and it is made for four reasons:

| Property | Why `(RunID, Sequence)` has it |
|---|---|
| **Unique** | The ingest cursor admits each sequence exactly once, in the same transaction that writes the row |
| **Deterministic order** | Sequences are dense and monotonic per run, so ordering never depends on a timestamp |
| **Already atomic** | The cursor advance and the row become durable together, because they are the same write |
| **Free** | No new identifier, no UUID, no clock, nothing to collide |

**A span id is not the identity, and must not become one.** `SpanID` is unique
only inside its trace, `ParentSpanID` is a trace-scoped reference
([084](084-correlation-operational-evidence.md#correlation)), and `EventID` is
caller-supplied and unconstrained. Any of the three would make identity depend
on a producer behaving well. The sequence depends on this platform's own
admission decision, which is the only thing here that cannot be wrong.

### Ordering, and why it is not by time

Retained observations are ordered **by sequence, ascending** — the order the
platform accepted them, which for a single ingesting client is the order they
were produced.

`Timestamp` is retained and is **not** the sort key. Three reasons, each of
which has already occurred in this repository's fixtures:

1. **Equal timestamps are ordinary.** A millisecond-resolution clock and a fast
   agent produce ties, and a tie under a timestamp sort makes pagination
   non-deterministic — the defect this contract exists to prevent.
2. **Producer clocks are not authority.** A record's timestamp is caller-supplied.
3. **Children do not arrive after parents.** A parent span ends *after* the
   children it started, so a child is routinely accepted first. Sorting by time
   would reorder a trace against its own arrival; sorting by sequence states
   what happened and lets 076 draw parentage from the retained parent id rather
   than from adjacency — which
   [084](084-correlation-operational-evidence.md#correlation) already forbids.

**Out-of-order arrival is not an error and is never rejected.** A child whose
parent never arrives, or was sampled away, is retained with its parent id
resolving to nothing. No parent-existence check exists at any layer.

### What is retained

Exactly the fields [076 named as its requirement](076-behavioral-evidence-explorer.md#the-boundary-with-task-067),
plus [084](084-correlation-operational-evidence.md)'s four. Nothing else.

| Group | Fields |
|---|---|
| Identity | `Sequence`, `EventID`, `Timestamp` |
| Actor | `ActorID`, `ActorType`, `IdentityConfidence` |
| Behavior | `FingerprintID`, `Behavior` (the `StableFeatures` tuple), `NewBehavior` |
| Scores | `AnomalyScore`, `AnomalyConfidence`, `TrustScore`, `ContextRisk`, `RiskLevel` |
| Outcome | `Decision`, `PolicyRule`, `PolicyReason`, `MatchedDefault` |
| Correlation | `TraceID`, `SpanID`, `SessionID`, `DelegatedFrom`, `ApprovalStatus` |
| Operational (084) | `ParentSpanID`, `SpanLineage`, `DurationNanos` + `DurationObserved`, `SpanStatus` |

`NewBehavior` is the one value not present on the record: it is the
already-computed *was this fingerprint new* state the ingest path derives from
the trusted snapshot before folding, and publishes to realtime today. It is
recorded rather than re-derived, because after the fold it can no longer be
answered.

**`Contributors` is deliberately excluded.** It is the one variable-length
field on `DecisionRecord`, it grows with the number of signals that fired, and
retaining it would make a row's size a function of producer behaviour. A
fixed-shape row is what keeps the storage profile predictable. If 085 finds it
needs contributor detail, that is an amendment with a measured cost, not a
default.

### Bounds

**`MaxRetainedObservations = 4096` per run.** Reached, the run stops retaining
and reports `Partial`; ingest continues unaffected.

This is the same rule the behavior collector applies at 512 distinct behaviors,
for the same reason and with the same failure mode
([`platform/behavior.go`](../../platform/behavior.go)): saturation is *degraded
evidence, not a failed ingest*. Refusing the record would discard sound
decision evidence because historical evidence filled up, which inverts their
importance.

4096 is chosen against the volume [082 states](082-agent-inspection-and-evaluation-depth.md#storage-scale-and-completeness)
— *"one run of a chatty agent produces thousands of observations"* — and bounds
one run's history at roughly a megabyte of fixed-shape rows. It is a constant
rather than a configuration knob, exactly as 512 and `MaxListPage` are: a
per-deployment retention setting is a decision 068 and 071 should make with
measured volume in hand, not one this task should guess at.

**Reads are bounded independently of storage.** Every read is a page of at most
`MaxListPage` (64), keyset-paginated on the immutable `Sequence`, `after`
exclusive, with continuation reported only when another row actually follows.
No route returns a run's whole history, and no code path assembles one in
memory.

### Retention lifetime

History lives exactly as long as the run that produced it, and is deleted with
it. **There is no age-based sweep in this task**, because the platform has no
scheduler to run one and adding one here would be a second capability wearing
this task's number. What this task guarantees instead is that an absent history
is *reported as absent* rather than as an empty one — which is the property
[085 asks for](082-agent-inspection-and-evaluation-depth.md#085--evidence-links-from-a-finding-to-the-observations-behind-it)
(criterion 6) and the only part a sweep would need from it. A sweep added later
sets the same flag and needs no schema change.

### Honesty: three states, not two

A run's history is described by `ObservationHistory`, and it has **three**
states because two would force a lie:

| State | Means | Arises when |
|---|---|---|
| **Unavailable** | This run has records, and none of their history was ever retained | The run was ingested before schema 7 existed |
| **Complete** | Every accepted record is retained | The ordinary case |
| **Partial** | Some records are retained and some are not | The run exceeded `MaxRetainedObservations`, **or** it began ingesting before schema 7 and resumed after it |

**A migrated run reports `Unavailable`, never an empty `Complete`.** A schema-6
database's runs have aggregates and behavior snapshots and no observations, and
reporting "complete, zero rows" for one would be a fabricated historical fact —
the same error [066 refused](README.md) when it declined to synthesize
promotions from old evaluations. The migration adds an empty history and states
that it is empty.

The distinction is drawn without a backfill and without a new column on runs: a
history row is created by the first ingest that runs under schema 7, so

- no history row **and** `RecordCount > 0` → **Unavailable**;
- no history row **and** `RecordCount == 0` → **Complete**, trivially empty —
  a run that has ingested nothing has no history to be missing;
- a history row → its own `Complete` / `Partial` flag.

**A run that began before schema 7 and resumed after it reports `Partial`, not
`Complete`.** Its history row is created by the first ingest under schema 7, and
that ingest knows it is not sequence 1 — so the row is born incomplete rather
than claiming a history that starts in the middle. Saturation and a
pre-retention prefix are the same state deliberately: no consumer can act on the
difference, and a state nobody can act on is one somebody eventually reads as
"complete enough".

## Atomicity

The observation is written **inside `CommitEvaluationIngest`**, in the
transaction that already writes the aggregate, the behavior snapshot and the
cursor.

Not beside it, not after it, not in a second store call. The existing contract
is explicit that evidence and cursor become durable together *"or neither
does"*, because the split failure modes are silent: evidence without the cursor
double-counts on retry, and a cursor without evidence loses a record the
protocol believes arrived. A third artefact written outside that transaction
would reintroduce exactly that class of bug — an observation with no aggregate
to belong to, or an aggregate whose history is short by one and says it is
complete.

Consequences, each asserted by test:

- A **rejected record** — bad sequence, wrong profile, terminal run, invalid
  aggregate, failed validation — retains no observation, advances no cursor and
  changes no aggregate.
- A **failed transaction** leaves all three unchanged.
- A **retry** of the last accepted record replays: it produces no second
  observation, because it reaches no commit.
- A **concurrent identical submission** that loses the race reports
  `AlreadyCommitted` and writes nothing, for the same reason.

## Privacy: the boundary is the schema

The retained field set above is an **allowlist expressed as columns**, not a
filter applied to a wider object. There is no attribute map, no span-event
list, no link list, no payload column, no JSON blob, and no column whose
content is producer-chosen beyond the identifiers and descriptors
`DecisionRecord` already publishes.

Nothing that this task retains is new privacy surface: every field is one the
platform already accepts at ingest, aggregates, or publishes to realtime. What
changes is *duration*, which is precisely why the boundary is restated here
rather than assumed: a field that was ephemeral and is now durable deserves the
question asked again, and the answer is that no prompt, completion, reasoning
trace, tool argument, tool result, retrieved document, HTTP body, SQL text,
credential or arbitrary attribute has a column, cannot be written through one,
and is asserted absent by a tripwire test that plants distinctive content
upstream and fails if it reaches storage or any response.

[ADR 0030](../../adr/0030-local-persistence-stores-authoritative-bounded-state.md)
named this task as the one allowed to hold raw history. It holds bounded
*evidence* history, and the record boundary task 050 drew is unchanged.

## Schema

**Forward-only, 6 → 7**, on both backends, adding an empty history.

Two tables, mirroring the aggregate/snapshot pair that already exists:

```text
platform_observations           one row per retained observation
                                PRIMARY KEY (run_id, sequence)

platform_observation_history    one row per run that retained anything
                                PRIMARY KEY (run_id)
                                retained_count, complete
```

Indexes, one per access pattern [082 stated](082-agent-inspection-and-evaluation-depth.md#storage-scale-and-completeness),
and none that is not:

| Index | Serves |
|---|---|
| `(run_id, sequence)` — the primary key | run-scoped paging, the only route this task ships |
| `(run_id, fingerprint_key, sequence)` | 085 resolving a behavioral identity to its observations |
| `(run_id, trace_key, sequence)` | 085 and 076 resolving a trace |
| `(run_id, session_key, sequence)` | 076 ordering a session |

Every index is **run-scoped first**, because every access pattern in 076 and
085 is scoped to a run and a global index on a trace-scoped identifier would
invite exactly the cross-run key 084 forbids.

`ParentSpanID` is **not indexed**, deliberately: 084 states that nothing
indexes it and *"067 must not either"*, because it is meaningful only beside
`TraceID` and an index would suggest otherwise.

### The index key is a digest, and the value is stored whole

The three correlation indexes key on a `_key` column holding hex SHA-256 of the
value beside it, never on the value.

**PostgreSQL refuses a B-tree entry larger than about 2704 bytes, and refuses it
at `INSERT`.** None of the three indexed values has a length bound the ingest
path enforces:

| Value | Bound at ingest |
|---|---|
| `TraceID` | **none** — validated nowhere; only the 256 KiB request body limits it |
| `SessionID` | **none** — the same |
| `FingerprintID` | 256 bytes, **except** once a run saturates: `Observe` returns `ErrBehaviorCapacity` before validating, and the ingest path deliberately swallows that error |

So the platform already accepts records carrying values far larger than a raw
index key can hold. Indexing the value would make retaining such a record fail,
roll back the whole ingest, and do it **on PostgreSQL only** — the two backends
disagreeing about which records are ingestable, which is the drift the
conformance suite exists to prevent.

The two alternatives were both refused as changing the wrong thing: truncating
the identifier stores a value the producer did not send, and rejecting the
record narrows the accepted-record contract to suit an index. **The contract is
not the index's to change**, so the key is bounded instead and the value is kept
whole.

**A lookup through one of these indexes must filter on the key *and* on the
original value.** A digest narrows; it does not identify, and filtering on the
key alone would let a collision return another observation's row as a match. The
digest width is not the safety property — the value comparison beside it is.
That rule belongs to 085, which owns the first such lookup, and is asserted by
test here so it cannot be inferred wrongly later.

Each key is verified on read: a stored key that disagrees with its value was not
written by this code, and the row is refused rather than repaired — repairing it
would leave the index itself still wrong.

An absent value keys as empty rather than as the digest of the empty string.
Most observations carry no session, so giving absence a 64-byte key would put one
enormous equal-key run in every index for rows that can never be looked up by it.

## Reads are one snapshot

A page is three statements — the history row, the record count, and the
observation rows. **They must describe one instant**, so each backend's
`RunObservations` opens a transaction that provides a consistent snapshot and
the shared page function runs inside it.

Read against a pool instead, a concurrent ingest committing between any two of
them produces a page describing a state that never existed: a retained count of
1 beside two rows, or completeness metadata from before a saturation the rows
already show. Nothing in the page's values reveals that it is a composite, which
is what makes this worth stating rather than leaving to be noticed.

| Backend | How |
|---|---|
| SQLite | a read transaction, rolled back. `SetMaxOpenConns(1)` does **not** supply this: it serializes the statements without joining them, and the connection is released between each one |
| PostgreSQL | a **read-only `REPEATABLE READ`** transaction. `READ COMMITTED` takes a fresh snapshot per statement, which is invisible in a read that asks one question and wrong in a read that combines three |

The raised isolation level is confined to this read. Every write in the
PostgreSQL store keeps `READ COMMITTED`, because their invariants are held by row
locks and predicates and a global bump would make serialization failures routine
for callers that have no retry — a guard test pins that exactly one transaction
raises isolation and that it is read-only.

## Acceptance criteria

1. An accepted record retains exactly one observation, atomically with the
   aggregate, the snapshot and the cursor; a rejected record or a failed
   transaction retains none and leaves all of them unchanged.
2. A retry, and a concurrent identical submission, produce no second
   observation.
3. Retained history survives a restart and reads back field-for-field.
4. Observations are identified and ordered by `(RunID, Sequence)`; ordering is
   deterministic under equal timestamps, unaffected by out-of-order arrival, and
   never depends on span-id uniqueness.
5. Root, child, missing-parent and out-of-order observations are all retained
   and all report their recorded lineage; none is rejected.
6. Unavailable duration and a measured zero read back as different facts, and
   every `SpanStatus` state round-trips.
7. A run that saturates reports `Partial` and keeps ingesting; a run migrated
   from schema 6 reports `Unavailable` rather than an empty `Complete`; a
   migrated run that resumes ingesting reports `Partial` rather than `Complete`.
8. Every read is bounded and keyset-paginated in the established shape, with no
   duplicate and no omission across page boundaries, and **each page is read
   from one database snapshot** — an ingest committing mid-read yields the state
   before it or the state after it, never a mixture.
9. Runs are isolated: one run's history never appears in another's page.
10. Identical behaviour on SQLite and PostgreSQL, including the migration.
11. No prompt, completion, argument, result, document, body or arbitrary
    attribute is reachable through storage or any response, proven by a
    tripwire test.
12. A record carrying correlation identifiers larger than a B-tree key is
    retained on both backends and returned byte for byte. Retention never
    narrows which records the platform accepts.
13. Behavioral fingerprints, policy decisions, baseline learning and the
    existing observation counting are unchanged.

## Validation

At the store, control-plane and `/v1` levels. A conformance suite runs the same
assertions against both backends; the PostgreSQL half is skipped without a
database and run against a real one in CI and in `make integration-postgres`.
Migration is tested from a populated schema-6 fixture, asserting that no
historical observation is invented. Pagination is tested for duplicates and
omissions across boundaries and at equal timestamps.

## Compatibility

Additive. No existing route, field, realtime contract or store method changes
shape. A schema-6 database migrates forward; a schema-7 database is not
readable by an older binary, which is the existing forward-only rule.

## What this enables, and what it does not

It unblocks **085**, which is the next item and owns resolution. 076 and 088
follow 085. None of them is delivered here, and this task ships no view.
