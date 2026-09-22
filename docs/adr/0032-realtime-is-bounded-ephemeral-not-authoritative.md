# 0032 — Realtime is bounded, ephemeral, and not authoritative

**Status:** Accepted

## Context

[Task 059](../tasks/v1.0/059-realtime-infrastructure.md) adds realtime so a
future CLI, TUI or WebUI can watch an evaluation as it runs.

Realtime is where systems quietly acquire a second source of truth. A
notification stream grows a replay buffer, then an offset, then a retention
policy, and at some point a client trusts the stream instead of the database —
at which point the stream's bugs become data bugs. This decision fixes the
relationship before any of that can accrete.

## Decision

Realtime is an in-memory, bounded, transport-independent notification
capability over committed control-plane state. Slow subscribers are
disconnected rather than allowed to grow queues or stall publishers. No replay
history is retained; every new connection explicitly requires resynchronization
from authoritative control-plane state before consuming subsequent incremental
updates.

### 1. Realtime is not storage

The bus holds what has not yet been delivered and nothing else. Zero events are
retained after delivery.

The moment it retains more, it is a database with none of the properties a
database has: no schema version, no migration, no corruption detection, no
consistency validation. [ADR 0030](0030-local-persistence-stores-authoritative-bounded-state.md)
put real care into those for the evidence that matters; a parallel store
without them would be the weaker copy that consumers end up trusting.

### 2. Polling SQLite is not realtime

A ticker reading `EvaluationProgress` and diffing results is polling storage —
the roadmap rejects it explicitly. It scales with subscribers times poll rate,
reports changes late by up to one interval, and cannot distinguish two updates
inside one tick.

Publication originates from control-plane mutations instead. The only timer in
this task is the SSE heartbeat, which carries no domain meaning.

### 3. Publication happens after durable success

Publishing first would let a subscriber observe state that never existed: a
record "applied" whose transaction then rolled back.

So every publication sits after the commit it describes, and a failed or
conflicted operation publishes nothing.

### 4. Delivery failure cannot fail a committed operation

If the commit succeeded and the bus is closed, broken or full, the operation
reports its durable success.

The alternative is worse than a missed notification. Returning an error after a
successful commit makes the client retry a write that already landed, and for
ingest that means a sequence conflict on a record the caller believes failed.
Ambiguity about whether a write happened is a data-integrity problem;
a missing notification is recoverable by resync.

Realtime is advisory, and the code says so by returning a result rather than an
error.

### 5. Each subscriber owns a bounded queue

One shared queue would couple every consumer to the slowest. A per-subscriber
queue is what makes "one client is slow" a fact about that client.

### 6. Queue saturation disconnects the subscriber

The alternatives were each considered and rejected:

- **Block the publisher.** One stalled HTTP client would add latency to every
  committed mutation, including ingest.
- **Grow the queue.** Unbounded process memory, controlled by whoever reads
  slowest.
- **Drop silently.** Covered next.

Disconnecting is the only option that keeps the publisher bounded *and* tells
the client the truth.

### 7. Silent dropping is rejected

A client that missed an event but keeps receiving later ones holds an
incomplete picture and does not know it. Every subsequent decision it makes is
based on a gap it cannot see.

Disconnection makes the gap observable at the only moment the client can still
act on it, and the resync protocol repairs it.

### 8. One subscriber cannot block another

Publishing to A never waits for B. A dead subscriber may disconnect itself and
affect nothing else — which is the property that makes bounds meaningful rather
than merely present.

### 9. Subscriber count is bounded too

A bounded queue with unlimited subscribers is still unbounded memory: total
cost is subscribers × queue. Both halves are capped, so worst-case memory is a
constant rather than a function of who connected.

At the limit `Subscribe` fails. An existing subscription is never evicted for a
new one — a caller should not be able to disconnect somebody else's stream by
connecting.

### 10. No replay history is retained

A replay buffer needs a retention bound, an addressing scheme, and a promise
about how far back it reaches. Each is a contract, and together they are a
durable log — which is [task 067](../tasks/v1.0/README.md)'s subject, with
retention and privacy questions this task must not answer by accident.

`Last-Event-ID` is therefore ignored rather than honored, and no SSE `id:`
field is emitted. Accepting the header while being unable to replay would
promise durability that does not exist.

### 11. Reconnect requires resync

Every connection reports `resync_required: true` and
`replay_available: false`, and the handshake arrives *after* the subscription
is registered.

That order is the point. Fetching state and then subscribing leaves a window
where a mutation lands between the two and is lost by both paths. Subscribing
first means anything that happens during the fetch is already queued.

### 12. SSE is the first transport

One `GET`, ordinary HTTP, no handshake, no framing library, and browsers
already reconnect on their own. For a one-way notification stream that is the
whole requirement.

### 13. WebSockets are deferred

Bidirectional framing buys nothing here — nothing flows upstream — and it would
add a dependency [ADR 0023](0023-interfaces-are-adapters.md) says needs
measured justification. There is no measurement to offer, so there is no case.

### 14. No message broker

Kafka, NATS, Redis, RabbitMQ and Pulsar all solve durability, partitioning and
cross-process fan-out. This task needs in-process notification with a bounded
queue, and the roadmap says a broker requires a measurement behind it.

An operator running one local binary should not need to run a broker to see
what their agent is doing.

### 15. Filtering belongs in the realtime abstraction

Not in SSE. If the bus delivered everything and the transport discarded the
rest, a busy run would fill the queue of a subscriber watching a quiet one —
the isolation in points 5 through 8 would be nominal.

Filtering therefore happens before enqueue, and the filter is part of the
capability so every future transport inherits it.

Every event carries its full immutable hierarchy — project, agent, candidate,
run — so a subscriber can match without reading the database. That hierarchy is
immutable by [ADR 0025](0025-platform-domain-values-with-caller-owned-identity.md):
there is no `ChangeAgent`, so resolving it once at publish time is sound.

The filter is three optional identifiers ANDed together. No regex, glob or
expression language: that would put a small query language on the wire
contract and move matching into an unbounded surface.

### 16. Payloads are fixed-shape and carry no raw event data

No `map[string]any`, no raw `Event`, no raw `DecisionRecord`.

Streaming the record would couple realtime to that compatibility surface,
enlarge every queue by a `Contributors` slice no live consumer reads, and — the
real problem — reopen the boundary task 050 drew. The record already excludes
attributes, tool arguments, prompts and completions; the realtime projection
narrows further, to the fields a live view actually shows.

A fixed struct also keeps queue memory predictable, which points 5 and 9 depend
on.

### 17. Only provable semantics are emitted

Six kinds: created, started, observation, completed, failed, cancelled. Each
corresponds to a control-plane mutation that committed.

### 18. No invented semantics

**`policy_violation`** — a blocked decision is the policy engine doing what it
was configured to do. Calling it a violation introduces severity that
[ADR 0029](0029-hard-gates-use-explicit-integer-evidence.md) established
nothing models, and it would be read as a security finding.

**`baseline_update`** — the platform does not own learned core state; that
stays in the engine's own stores, and inventing a stream for it would imply
otherwise.

**`gate_update`** — a gate result today is a derived read under
caller-supplied limits, not durable state with its own identity. Streaming it
would give a read a side effect, and would look durable while depending on
whichever limits one caller happened to pass. If a later task defines an
authoritative gate operation, an additive kind can be introduced then.

### 19. The SQLite schema stays at version 2

Nothing here is durable, so nothing needs a column. A schema change would
imply state worth migrating, and there is none.

### 20. No event-history table appears

`realtime_events`, `notifications`, `stream_offsets`, `subscriber_offsets`,
`message_log` — each is point 1 or point 10 wearing a table name.

## Alternatives considered

**A small replay ring, "just a few seconds".** Rejected: it needs a retention
promise, and every client that notices it works stops resyncing. The bound
would then be load-bearing for correctness while sized by guesswork.

**Sequence numbers on realtime events for gap detection.** Tempting, and
rejected here because a client that detects a gap can only resync — which
disconnection already forces, sooner and without a client-side protocol.

**Publishing project, agent and candidate creation for symmetry.** Rejected:
no consumer in this milestone needs them, and a taxonomy grown for tidiness is
a contract grown for nothing.

**Making realtime mandatory in `NewControlPlane`.** Rejected: every task 058
caller would have to construct a bus to do nothing with it, and the
optionality is what keeps realtime infrastructure rather than a dependency.

## Consequences

A subscriber can be disconnected by its own slowness, and clients must
implement subscribe → handshake → resync → stream. That is more client work
than a replay buffer would be, and it is the work that keeps the database the
only thing anyone trusts.

Scope resolution reads the candidate and agent on each published mutation.
That is two extra reads per ingest, accepted so subscribers need none — and
sound because the hierarchy is immutable.

Worst-case realtime memory is subscribers × queue, a constant. Adding a
transport later changes nothing about the bus; adding durable replay is a
different capability with its own decision record, not a setting here.
