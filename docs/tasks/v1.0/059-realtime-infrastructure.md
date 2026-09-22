# 059 — Realtime Infrastructure

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [058](058-local-control-plane-api-and-ingest.md) ·
**Blocks:** 060 onward

## Objective

Add bounded, transport-independent realtime notification over committed
control-plane state, so a future CLI, TUI or WebUI can watch an evaluation
live without polling SQLite and without transport logic entering
`ControlPlane`.

```text
                  durable authority
                       │
                       ▼
                  ControlPlane
                       │
         successful state mutation
                       │
                       ▼
                 RealtimePublisher
                       │
                       ▼
              InMemoryRealtimeBus
              bounded / ephemeral
                 │            │
                 ▼            ▼
               SSE        future TUI · WebUI
```

## Authoritative-State Relationship

```text
durable state is authoritative
realtime is notification
```

That direction never reverses. Realtime is not an event database, a durable
log, a source of truth, a replay system, or a queue that grows until a client
catches up. It is a bounded ephemeral notification layer over state that has
already committed.

A subscriber that falls behind is **disconnected**, and recovers by
reconnecting and resynchronizing from authoritative HTTP state. The bus
reconstructs nothing.

Two consequences shape everything below.

**Publication happens strictly after durable success.** Publishing first and
committing second would let a subscriber observe state that never existed.

**Realtime failure is never a business failure.** If the commit succeeded and
the bus is closed, broken or full, the operation still reports its durable
success. The alternative turns a committed write into an apparent failure,
which a client then retries — and ambiguity about whether a write landed is
strictly worse than a missed notification.

## Realtime Event Model

Fixed-shape. No `map[string]any`, `interface{}` payload, raw JSON blob, raw
`Event`, or raw `DecisionRecord`.

```go
type RealtimeEvent struct {
    Kind        RealtimeEventKind
    Scope       RealtimeScope
    Evaluation  RealtimeEvaluation   // lifecycle kinds
    Observation RealtimeObservation  // observation kind
}
```

Unused fields stay zero rather than making the type variant — a fixed struct
is what keeps queue memory predictable and the wire schema closed.

### Kinds

```text
evaluation_created   evaluation_started   observation
evaluation_completed evaluation_failed    evaluation_cancelled
```

Only semantics the current platform can prove. No `policy_violation`: a
blocked decision is the policy engine doing what it was configured to do, and
naming it a violation would introduce a severity concept nothing models. No
`baseline_update`: the platform does not own learned core state. No
`gate_update`: a gate result is a caller-limit-dependent derived read today,
not durable state with its own identity, and streaming it would give a read a
side effect and blur where authority lives.

If a later task defines an authoritative gate operation, an additive kind can
be introduced then.

### Observation

A bounded factual projection of one applied record — not the record itself.
Streaming the whole `DecisionRecord` would couple realtime to that
compatibility surface, enlarge every queue, and carry a `Contributors` slice
no live consumer needs.

```text
Sequence            RecordCount          BehaviorComplete
FingerprintID       Behavior (StableFeatures)
Decision            RiskLevel            ApprovalStatus
TrustScore          AnomalyScore         AnomalyConfidence
NewBehavior
```

Deliberately absent: `Event.Attributes`, tool arguments, prompts,
completions, arbitrary metadata, the contributor slice, `PolicyReason`, and
the raw record. Task 050's privacy boundary holds on this path too, and a test
asserts a distinctive attribute value never reaches the wire.

**`NewBehavior`** is derived from the trusted pre-ingest snapshot: true when
this fingerprint was not already represented. At behavioral saturation an
unseen behavior still reports `NewBehavior=true` with
`BehaviorComplete=false` — a live factual observation, even though the
bounded snapshot cannot retain it. Nothing is evicted.

### Lifecycle

Run ID, candidate ID, status, created/started/finished times, and failure
reason where relevant. No `passed`, `safe` or `promotable`: completion still
means execution ended, which task 052 established and task 056 kept.

## Scope and Filtering

Every event carries its full immutable hierarchy, so a subscriber can filter
without reading the database:

```go
type RealtimeScope struct {
    ProjectID  ProjectID
    AgentID    AgentID
    CandidateID CandidateID
    RunID      EvaluationRunID
    Environment EnvironmentRef
    BehavioralProfile BehavioralProfileRef
}
```

The hierarchy is immutable once created — task 052 offers no `ChangeAgent` —
so resolving it at publish time is sound.

```go
type RealtimeFilter struct {
    ProjectID ProjectID
    AgentID   AgentID
    RunID     EvaluationRunID
}
```

Empty means unconstrained on that dimension; populated dimensions are ANDed.
No regex, glob, expression language, tags or predicates — those move matching
into an unbounded surface and put a small language on the wire contract.

**Filtering happens before enqueue.** Delivering everything and letting SSE
discard the rest would let a busy run overflow a subscriber watching a quiet
one, which breaks the isolation the bounds exist to provide.

## Publisher and Subscriber Contracts

Two capabilities, so neither side gains the other's authority:

```go
type RealtimePublisher interface {
    Publish(RealtimeEvent) RealtimePublishResult
}

type RealtimeSubscriber interface {
    Subscribe(context.Context, RealtimeFilter) (RealtimeSubscription, error)
}
```

`ControlPlane` receives a publisher. The HTTP adapter receives a subscriber and
**never** a publisher — a transport that could publish could fabricate state.

`Publish` returns a result rather than an error, because a caller must not be
able to treat delivery as something to handle. The result carries delivery and
disconnect counts for diagnostics and tests.

```go
type RealtimeSubscription interface {
    Events() <-chan RealtimeEvent
    Close() error
}
```

`Close` is idempotent; context cancellation unsubscribes; closing the bus
closes every subscription; `Subscribe` after `Close` fails; `Publish` after
`Close` never panics.

Sentinels: `ErrRealtimeClosed`, `ErrRealtimeCapacity`. No error per internal
condition.

## Bounds

```text
per-subscriber queue   64 events   (realtimeQueueCapacity)
subscriber count       64          (realtimeMaxSubscribers)
```

Fixed constants in this milestone rather than options, because an option that
accepts `MaxInt` would let a caller opt out of the bound while the code still
claims to have one. Tests reach the internal constructor to use small values.

A queue bound with unlimited subscribers is still unbounded memory, which is
why both exist. At the subscriber limit `Subscribe` returns
`ErrRealtimeCapacity`; an existing subscription is never evicted to make room
for a new one.

```text
memory  O(S × Q)     both finite
publish O(S)         S ≤ realtimeMaxSubscribers
history 0 events retained after delivery
```

## Slow-Consumer Policy

When a matching event cannot be enqueued immediately, the subscription is
**removed and its stream closed**. Publishing continues to everyone else.

Rejected alternatives: blocking the publisher (one stalled client would add
latency to every committed mutation); dropping the newest or oldest silently
(the client then believes it has a complete stream and is wrong); growing the
queue (unbounded memory).

A missed event means the stream is no longer complete for that client.
Disconnecting makes that observable and forces the resync that repairs it.

**Isolation.** Publishing to A never waits for B. A dead subscriber may
disconnect itself and nothing else.

**Ordering.** One healthy subscription receives matching events in publish
order. No goroutine per publish, so nothing can reorder.

## Reconnect and Resync

The bus retains no history, so every connection must resynchronize. The order
matters:

```text
1. subscribe
2. receive stream_ready { replay_available: false, resync_required: true }
3. fetch authoritative state over ordinary HTTP
4. process realtime events already queued since step 1
5. continue incrementally
```

Subscribing *first* is what closes the gap: fetching state and then
subscribing would miss any mutation landing between the two.

**`Last-Event-ID` is not a replay contract.** It is ignored, and every
connection still reports `resync_required`. Pretending an ID could restore
missing events would promise durability the bus does not have. No SSE `id:`
field is emitted in this task.

No realtime event carries a generated UUID or domain identity: with no replay
log there is nothing for an ID to address.

## SSE Adapter

```text
GET /v1/realtime?project_id=…&agent_id=…&run_id=…
```

SSE first because it is one GET, works through ordinary HTTP, and needs no
handshake or framing library. WebSockets are deferred: bidirectional framing
buys nothing for a one-way notification stream, and would add a dependency
ADR 0023 requires measured justification for. No broker, for the same reason.

Headers: `text/event-stream`, `no-cache`. No CORS — task 063 has the first
browser caller and can decide explicitly. `http.Flusher` is required; a writer
that cannot stream fails before a healthy stream is claimed.

Wire frames carry explicit DTOs in `httpapi`; domain realtime types gain no
JSON tags.

```text
event: stream_ready
data: {"version":"1","replay_available":false,"resync_required":true}

event: observation
data: {"version":"1","kind":"observation","scope":{…},"observation":{…}}
```

**Heartbeat** as an SSE comment frame (`: keepalive`) every 15 seconds, so a
dead connection is noticeable during a quiet evaluation. A comment rather than
a synthetic domain event, because a heartbeat has no domain meaning. The
interval is injectable so tests do not wait on a real clock.

One subscription per connection; disconnect releases it idempotently. The
server runs no reconnect loop — that belongs to the client.

**No subscriber configured** → `503` with a `realtime_unavailable` code, while
every task 058 route keeps working. Realtime is optional infrastructure.

## ControlPlane Integration

```go
func NewControlPlane(control, evaluations, ingest, options ...ControlPlaneOption)
func WithRealtimePublisher(p RealtimePublisher) ControlPlaneOption
```

Additive, so task 058 callers are unchanged and a plane with no publisher
behaves exactly as before. No post-construction setter: a bus that can be
swapped at runtime makes publication order unreasonable about.

Published, after durable success only:

```text
CreateEvaluationRun   StartEvaluationRun
IngestDecisionRecord  — applied only
CompleteEvaluationRun FailEvaluationRun  CancelEvaluationRun
```

Not published: every read (`Project`, `Agent`, `Candidate`,
`EvaluationProgress`, `EvaluationIngestState`, `CompareEvaluations`), any
failed or conflicted operation, and **a replayed ingest**. A network retry
that produced no second durable record must not produce a second live
observation.

Project, agent and candidate creation are not published either: the milestone
is live evaluation behavior, and expanding the taxonomy for symmetry would add
kinds no consumer needs.

Scope resolution reads the candidate and agent to build the hierarchy. If that
fails *after* the mutation committed, realtime is skipped — the operation still
reports success, because authority lives in the database.

## Security

Covered in `docs/SECURITY.md`: no raw event payload; bounded queues and
subscriber count; slow consumers disconnected; filters applied before enqueue;
no subscriber stalls another; no durable replay; reconnect requires resync;
delivery failure never falsifies a durable outcome; publication after commit;
no broker, CORS, authentication claim, listener, or event-history table; and
SSE disconnect releases its subscription.

Local realtime inherits task 058's local trust boundary exactly. It is **not**
authenticated, and nothing here claims otherwise.

## Concurrency

Safe for concurrent publish, subscribe, unsubscribe and close. No
send-on-closed-channel panic, no deadlock, no data race, and no goroutine
leak: the only goroutine per subscription is a context watcher that performs
no delivery, bounded by the subscriber limit and released on close.

Particular attention to publish versus subscriber close, publish versus bus
close, cancellation racing slow-consumer disconnect, double close, and removal
during iteration.

## Tests

**Bus.** Ordering; per-subscriber queue bound; slow consumer disconnected
while a fast one continues and publish never waits; subscriber-count limit
returning `ErrRealtimeCapacity` and the slot reclaimed after close;
filter-before-enqueue proven by flooding an unrelated run and showing the
filtered subscriber stays healthy and still receives its own event; context
cancellation removing the subscription; bus close closing every stream, making
later `Subscribe` fail and later `Publish` safe; `Close` idempotent. No sleeps
as a correctness mechanism.

**Publication timing.** A failed mutation publishes nothing; a committed one
becomes visible; **a closed bus does not stop the mutation from succeeding**.

**Ingest.** One applied record yields exactly one observation whose sequence,
record count and completeness match the commit. A replayed ingest yields
none. `NewBehavior` true, then false for a repeat, then true for a new
fingerprint. Saturation reports `NewBehavior=true`,
`BehaviorComplete=false`, `RecordCount=513` while durable semantics stay
task 058's.

**Scope filtering.** Two projects, agents and runs; subscriptions by project,
agent and run each receive exactly their own events.

**SSE.** Handshake ordering and headers; delivery after ready; filtering;
reconnect reporting `resync_required` again; `Last-Event-ID` claiming no
replay; every lifecycle kind; `503` with no subscriber configured while other
routes work; client disconnect releasing the subscription with no leak.

**Real Engine → HTTP ingest → SSE**: a real engine's `DecisionRecord` posted
through the task 058 endpoint arrives as an SSE observation whose fingerprint,
behavior, decision, sequence and record count match — then a replay produces
no second event, and completion arrives.

**Privacy**: a distinctive attribute value on the source `Event` is absent
from the SSE body.

**Architecture**: the HTTP package still references no store type, computes no
diff, scorecard or gate, and **cannot publish**.

## Mutation Tests

Each must fail a targeted test, and all are restored before commit:
publish-before-commit ordering; replay suppression; filter-before-enqueue;
queue-full disconnect; subscriber isolation; subscriber-count bound; context
cancellation removal; bus close semantics; ordering; no-history reconnect;
`stream_ready` requiring resync; no-publisher behavior; applied-ingest event
count; new-behavior detection; saturation projection; realtime failure not
changing a durable outcome; SSE privacy; HTTP unable to publish.

## Benchmarks

`Publish` with 1, 16 and 64 healthy subscribers, plus a filtered variant, in
`docs/PERFORMANCE.md`. No latency gate. The documented property is the shape:

```text
Publish  O(S), S ≤ 64
Subscribe bounded bookkeeping
memory   O(S × Q), both finite, no history term
```

## Non-Goals

No persistence change — `SchemaVersion` stays **2**, and no
`realtime_events`, `notifications`, `stream_offsets`, `message_log` or
equivalent table appears. No broker, WebSocket, polling loop, database
watcher, event history, retention policy, CLI, TUI, WebUI, listener, CORS,
authentication, or core runtime change. Task 058's synchronous atomic
persistence is observed, not redesigned.

## Acceptance Criteria

- [ ] Publication only after durable success; realtime failure never changes it.
- [ ] Replayed ingest publishes nothing.
- [ ] Per-subscriber queue and subscriber count both bounded by constants.
- [ ] Slow consumers disconnected, never blocked or silently dropped.
- [ ] Filtering before enqueue; one subscriber cannot stall another.
- [ ] No history retained; every connection requires resync.
- [ ] `Last-Event-ID` promises no replay.
- [ ] SSE is the only transport; publisher never reaches HTTP.
- [ ] Observation payload carries no raw event data.
- [ ] Schema stays v2; no realtime or history table.
- [ ] No core runtime change, CLI, UI, listener, CORS, or auth.
- [ ] `gofmt`, `go vet`, `go test`, `-race` pass; `GOWORK=off` holds;
      `check-modules` and `check-platform-boundary` pass.
