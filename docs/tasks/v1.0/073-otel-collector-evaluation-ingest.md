# 073 — OTel Collector Evaluation Ingest

Status: implemented
Depends on: [050](050-public-serializable-decision-record.md),
[051](051-behavioral-profile-learning-scope-isolation.md),
[058](058-local-control-plane-api-and-ingest.md),
[062](062-integrated-local-developer-workflow.md)

## Objective

Give telemetry-only workloads a path into an evaluation.

Every piece exists. The Collector processor maps `ptrace.Span` → `event.Event`,
runs the real `Engine.Analyze`, and enriches the span with `trustvian.*`
attributes. The control plane accepts `trustvian.DecisionRecord` at
`POST /v1/evaluation-runs/{run_id}/records`. Nothing connects them:

```text
processor ──▶ Result ──▶ trustvian.* attributes ──▶ next consumer
                 │
                 └──▶ (nothing)

control plane ◀── DecisionRecord ◀── an application that embeds the engine
```

So an evaluation is reachable only by an application that imports Trustvian and
posts records itself. A service instrumented with OpenTelemetry and nothing
else — the case [ADR 0003](../../adr/0003-opentelemetry-adapter-single-module.md)
built the processor for — cannot be evaluated at all.

Task 073 adds the missing edge, and only that edge:

```text
processor ──▶ Result ──▶ trustvian.* attributes ──▶ next consumer
                 │
                 └──▶ Result.DecisionRecord() ──HTTP /v1──▶ control plane
```

The same `Result`. One `Analyze`. No second engine, and no new platform route.

## Architecture

```text
Application (any language, OTel SDK or zero-code instrumentation)
        │  OTLP
        ▼
  OTLP receiver
        │  ptrace.Traces
        ▼
┌──────────────────────────────────────────────────┐
│ trustvian processor                              │
│                                                  │
│   EventFromSpan ─▶ Engine.Analyze ─▶ Result      │
│                                       │          │
│                    ┌──────────────────┴────────┐ │
│                    ▼                           ▼ │
│        SetAttributesFromResult      DecisionRecord()
│                    │                           │ │
└────────────────────┼───────────────────────────┼─┘
                     │                           │ HTTP /v1
                     ▼                           ▼
              (your exporter)            Trustvian control plane
                                                 │
                                          SQLite / PostgreSQL
```

The left branch is existing behaviour and does not change. The right branch is
this task.

Both branches read one `Result`. The record is **projected** from it, never
reconstructed from the attributes written on the left — those are a lossy
subset (five values) of what a record carries, and rebuilding from them would
make the evaluation's evidence a function of the enrichment format.

## Module Boundary

This is the constraint everything else bends around.

```text
trustvian-processor ──▶ root public API ──▶ engine
trustvian-platform  ──▶ root public API ──▶ engine
trustvian-processor  ✗  trustvian-platform
```

The obvious implementation — import `trustvian-platform` and call
`ControlPlane.IngestDecisionRecord` directly — is forbidden for the reasons
[ADR 0033](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) already gave
for the CLI, and one more that is specific here. The processor is the component
compiled into an operator's Collector distribution; importing the platform
would put its SQLite driver, its schema and its domain types into every
Collector build that enriches spans and evaluates nothing.

So the sink speaks the versioned `/v1` HTTP API with client-side DTOs, exactly
as `cmd/trustvian/platform_client.go` does. This is the second consumer of that
contract, which is also the first evidence that it works for someone other than
the CLI.

`trustvian-platform` must not appear in `processor/go.mod` or `processor/go.sum`
— including as a test dependency. See [Tests](#tests) for how the end-to-end
test stays real without acquiring one.

## Configuration

```yaml
processors:
  trustvian:
    evaluation:
      api_url: http://127.0.0.1:54321
      run_id: run-reference
      behavioral_profile: support-reference
      required: true
```

A pointer field, so absence is distinguishable from a zero value — the same
shape `health:` uses, and for the same reason. Omitting the block is the
existing processor, unchanged: same `NewEngine` options, same enrichment, same
metrics, same startup sequence.

Decoded directly through `mapstructure` tags rather than deferred to a generic
map. `policy:` and `storage:` defer because a canonical `config.PolicyConfig` /
`config.StorageConfig` already exists to decode into; there is no canonical
Trustvian type for "which evaluation run is this Collector feeding", because it
is a property of *this runtime* rather than of the engine. There is nothing to
defer to, so the indirection would buy nothing.

Every field is required and validated at `CreateTraces`, so a bad block fails
Collector startup rather than the first span:

```text
api_url             absolute http/https, host, no path/query/fragment,
                    no embedded credentials
run_id              non-empty
behavioral_profile  non-empty
required            must be true
```

`api_url` validation reuses the rules `--api-url` already enforces, including
the one that matters most: a URL carrying credentials is rejected **without
echoing the value**, because a diagnostic that repeats the secret into
Collector logs has not protected anything.

### `required: true` is the only accepted value

`required: false` is rejected at startup rather than silently honoured.

An optional mode would have to answer what a Collector does when evidence
cannot be delivered, and both available answers are wrong. Dropping the record
produces a run that *looks* complete — the aggregate's counts are internally
consistent, the gate evaluates, and the verdict is computed from evidence with
holes nothing in the response describes. Recording the gap would need a
partial-evidence concept the platform deliberately does not have:
[ADR 0031 §10a](../../adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
made empty evidence a *result* precisely so a candidate that ran nothing fails
rather than looking perfect, and a silently truncated run would reintroduce
exactly that hazard one layer up.

The field exists rather than being omitted so the configuration states the
guarantee explicitly. A future task that gives the platform a way to report
incomplete evidence honestly can widen it; until then, refusing is the smaller
and truer answer.

## Learning Scope

`behavioral_profile` also selects the Engine's learning scope:

```go
opts = append(opts, trustvian.WithLearningScope(cfg.Evaluation.BehavioralProfile))
```

This is the mechanism [task 051](051-behavioral-profile-learning-scope-isolation.md)
and [ADR 0024](../../adr/0024-learning-scope-is-a-baseline-key-dimension.md)
exist for, and the platform already names the same idea `BehavioralProfileRef`.
Without it, a Collector with a durable store would train one baseline across
every candidate it evaluated — each candidate teaching the next, and the
evaluation measuring a baseline it had polluted. That is the precise failure
task 051 was built to prevent, reappearing at the one boundary that had no way
to select a scope.

It is safe against the ingest contract because scope and environment are
different dimensions of `baseline.Key`:

```go
key := baseline.Key{Scope: e.learningScope, ActorID: …, Environment: ev.Context.Environment}
```

`DecisionRecord.Environment` is taken from `BaselineKey.Environment`, so it
still equals what the event reported, and
`BehaviorCollector.Observe`'s environment check still matches the run's.
Selecting a scope changes which learned history the analysis is compared
against; it changes no field the platform validates.

Configuring evaluation is what selects the scope — there is no separate
`learning_scope:` knob. A Collector configured to feed one evaluation run is
dedicated to it, and two independent settings that must agree is a way for them
to disagree.

## Sequence Ownership

The producer owns the sequence
([ADR 0031 §8](../../adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md)),
so the sink owns a cursor.

```text
Start()      GET  /v1/evaluation-runs/{run_id}/ingest-state → next_sequence
per span     POST /v1/evaluation-runs/{run_id}/records      → next_sequence
```

Initialization happens in `Start`, not lazily on the first span. A Collector
whose control plane is unreachable then refuses to come up, instead of
enriching spans for an unknown period while recording nothing. It also makes
resume free: a restarted Collector continues a running evaluation from the
server's cursor rather than restarting the count and double-aggregating.

The cursor advances **only** to the `next_sequence` the server returned, for
both `applied` and `replayed`. Nothing is derived locally, so a client-side
increment can never disagree with durable state.

### Serialized, not atomic

One mutex covers allocate → POST → advance.

An atomic counter would be wrong, not merely coarse. The contract is gap-free
and strictly monotonic: two goroutines holding sequences 5 and 6 race, and if 6
arrives first the server refuses it as a gap — correctly, since it cannot know
5 is in flight. Reordering would have to be reimplemented in the client, which
is the retry-and-ordering machinery this contract exists to avoid.

Serializing makes it structurally impossible instead. The cost is real and is
measured by a benchmark rather than asserted: an evaluation-configured
Collector is bounded by one loopback round trip per analyzed span. That is
acceptable for what this configuration is — a Collector dedicated to one
evaluation run — and is the honest price of not inventing an ordering layer.
A Collector with no `evaluation:` block takes no lock and allocates nothing new
on the span path.

## Failure Semantics

A transport error is **not** proof that the server did not act. A POST can be
written in full, committed, and have only its response destroyed. Treating
that as "it did not happen" leaves the server holding sequence *N* while the
Collector believes *N* is free — and the next record it sends under *N* is
refused, correctly, as a different record claiming an occupied position.

So failures are classified, and only one class frees a sequence:

| Class | Examples | Sequence |
|---|---|---|
| Definitely not applied | failed dial, DNS failure, refused redirect, request over the body cap, a 4xx from the server | Free. The next record takes it. |
| Outcome unknown | reset connection, timeout, EOF mid-response, any 5xx, a 2xx whose body cannot be read | **Held**, bound to that exact record. |

Anything unproven is unknown. Calling a definitive failure unknown costs one
redundant POST the server answers `replayed`; calling an unknown outcome
definitive frees a sequence the server may already hold.

### Reconciliation, not retry

A held sequence is resolved by re-presenting **the same record** at **the
same sequence** — the case
[ADR 0031 §8](../../adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md)'s
digest replay rule exists for: identical digest replays, different content
conflicts. One attempt, synchronous, inside the same `Record` call, under the
caller's own context. Until it resolves, no other record may use that
sequence and no other record is accepted.

```text
idle  ──POST fails, outcome unknown──▶  pending(sequence, record)
pending ──same record, same sequence, server answers──▶  reconciled ──▶ idle
pending ──still unknown──▶  pending   (ErrUnresolved; the sink accepts nothing else)
```

An ingest failure the sink could not resolve returns
`consumererror.NewPermanent(err)`.

The wrapper is load-bearing. Without it the Collector retries the batch, and a
retried batch re-analyzes spans whose records already committed, sending them
again under **new** sequence numbers — the server accepts them, because from
its side they are new records, and the evaluation's counts silently inflate.
[ADR 0026](../../adr/0026-evaluation-aggregation-is-bounded-evidence.md) made a
duplicate record count twice on purpose, so the corruption is quiet and
permanent.

Loudly losing a batch is recoverable by starting a new run. Quietly inflating
evidence is not recoverable at all, because nothing downstream can tell which
records were doubled. The failure is surfaced, never absorbed.

### Evidence and learning move together

`Engine.Observe` runs when the record may be durable, and does not run when
the control plane declined it.

Skipping `Observe` on every ingest error — the obvious reading — leaves the
run holding a record whose behavior this Engine never learned, which is the
same response-loss failure one layer up. Observing unconditionally trades it
for the mirror image: learning from records the run refused. `Observe` runs
at most once per span either way; a reconciled record is one record and one
observation.

What this deliberately does **not** add:

- No blind retry, in-line or background. No queue, no goroutine, no timer,
  no disk spool, no backoff schedule. Resending a *batch*, or resending a
  record under a *new* sequence, is what inflates evidence; only
  same-sequence, same-record reconciliation is safe, and it is safe because
  the server specifies it.
- No partial success. A span whose record cannot be confirmed fails the batch.
- No fallback to "enrich but don't record". That is `required: false` by
  another name.

Spans that never produce a `Result` — a span that fails `Event.Validate`, or an
`Analyze` error — consume no sequence and are counted exactly as they are
today. They are not evaluation failures; there is no decision to record.

## HTTP Bounds

```text
request timeout    30s per request
request body       ≤ 256 KiB   (matches the server's own limit)
response body      ≤ 64 KiB
redirects          refused
retries            none, except one same-sequence reconciliation of a record
                   whose outcome is unknown (see Failure Semantics)
credentials        never sent, never logged
```

The response bound is tighter than the CLI's 4 MiB because these two responses
are small fixed shapes — a disposition and three counters — and a client
reading an unbounded body trusts the server's good behaviour for its own memory
safety.

Redirects are refused rather than followed: a mutation addressed to loopback
must not silently become a mutation against another host. No `Authorization`
header is sent; task 070 owns authentication, and a header invented here would
freeze a credential shape before there is anything to authenticate against.

## Observability

Two instruments on the existing meter, following the established
closed-vocabulary rule so no identifier can become a label:

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `trustvian.evaluation.records` | Counter | `{record}` | `trustvian.outcome`: `applied`, `replayed`, `error` |
| `trustvian.evaluation.duration` | Histogram | `s` | *(none)* |

Four additional time series — three outcomes on the counter, one histogram —
fixed regardless of run count, and present only when `evaluation:` is
configured. The existing cardinality bound is asserted exactly rather than as a
maximum, so its test is updated to the new number rather than loosened.

The run ID and behavioral profile are **not** attributes. A Collector serves
one run per process by construction, so they would be constant labels; and a
constant label is how a bounded metric acquires an unbounded one the first time
that assumption changes.

## Tests

Unit: config validation for every field, including `required: false` refused
and an omitted block preserving the existing construction path exactly;
malformed `api_url`; a credential-bearing `api_url` rejected with the value
absent from the diagnostic; redirect refused; response over bound; ingest-state
initialization and resume from a non-1 cursor; `applied` and `replayed` both
advancing to the server's number; an unresolved failure surfacing as
permanent; concurrent `ConsumeTraces` allocating a gap-free strictly
increasing sequence under `-race`.

Ambiguous delivery has its own set, because it is the one failure whose
wrong handling is silent: a record committed by the server with its response
destroyed is reconciled at the same sequence and reported `replayed`, with
the next record taking the following sequence and the run holding exactly
two records; a failure that stays unknown holds its sequence across calls and
never lets another record take it; a definitive refusal frees the sequence
and is not retried; reconciliation stays gap-free and duplicate-free under
concurrency with `-race`. The end-to-end version drives the same loss
through a proxy in front of a **real** control plane, so the digest rule
being relied on is the server's own rather than a stub's.

One test exists specifically to pin the point of the task: the posted record is
byte-identical to `result.DecisionRecord()` for the same `Result`, and carries
fields — `Contributors`, `PolicyReason`, `AnomalyConfidence`, `ContextRisk` —
that the `trustvian.*` attribute set does not contain, so a reconstruction
could not have produced it.

Architecture guard, mirroring `cmd/trustvian/cli_architecture_test.go`: no
`trustvian-platform` import in processor source, no platform identifier
declared, and `trustvian-platform` absent from `processor/go.mod` and
`processor/go.sum`.

### The end-to-end test

The real chain, with nothing behavioral mocked:

```text
real ptrace.Span
  → real trustvian processor
  → real Engine.Analyze
  → Result.DecisionRecord()
  → real loopback HTTP control plane
  → SQLite
  → evaluation progress
```

It reaches a real control plane **without importing the platform**, which a
test dependency would still do — `go.mod` does not distinguish one. Instead it
builds `../platform/cmd/trustvian-local` with `GOWORK=off`, runs it as a
subprocess, reads `runtime.json`, and drives `/v1` over HTTP: exec and JSON,
never a Go import. That is the same boundary
[ADR 0035 §3](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md)
keeps real locally, used as a test seam.

Readiness is observed, never slept on: the discovery file, a `/v1` read, and
authoritative `eval progress` counts.

## Mutation Tests

Record reconstructed from span attributes instead of the `Result`; `Analyze`
called a second time for the record; the platform imported directly; sequence
allocated atomically instead of under the lock; cursor incremented locally
instead of from the response; `replayed` treated as a failure; permanent
wrapper removed so the batch retries and double-counts; ingest failure
swallowed; `required: false` accepted; ingest-state skipped so the cursor
starts at 1 against a resumed run; redirects followed; response bound removed;
credentials echoed into a diagnostic; learning scope left unset so two
candidates share a baseline; `evaluation:` omitted yet the span path changed;
run ID added as a metric attribute.

## Documentation

`processor/README.md`, `docs/OPENTELEMETRY.md`, `docs/ARCHITECTURE.md`,
`docs/observability.md`, `docs/compatibility.md`, `CHANGELOG.md`, this
directory's `README.md`, and `docs/ROADMAP.md`'s milestone sequence. A new ADR
records the adapter contract and why the module edge stays absent.

## Acceptance Criteria

1. `processor/go.mod` and `go.sum` name no platform module; no processor source
   imports one.
2. A configured Collector posts exactly one `Result.DecisionRecord()` per
   successfully analyzed span, from the same `Result` already computed.
3. `Engine.Analyze` runs once per span, as today.
4. Sequence state initializes from `/v1/.../ingest-state` and advances only to
   the server's `next_sequence`, for both `applied` and `replayed`.
5. Concurrent `ConsumeTraces` produces a gap-free, strictly increasing
   sequence under `-race`.
6. An ingest failure the sink could not resolve surfaces as a permanent
   consumer error; no configuration silently continues.
6a. A failure that does not prove the record was unapplied holds its sequence
   bound to that exact record, is reconciled at the same sequence before any
   other record is accepted, and advances the cursor only to the server's
   `next_sequence`.
6b. `Engine.Observe` runs exactly once per analyzed span, and runs when the
   record may be durable — so a run's evidence and this Engine's learning
   never diverge in either direction.
7. Requests are bounded, time out, refuse redirects, and never carry or log
   credentials.
8. `behavioral_profile` selects the Engine's learning scope.
9. Omitting `evaluation:` leaves construction, enrichment, metrics and startup
   byte-for-byte identical.
10. The end-to-end test drives a real span through a real engine into a real
    control plane and reads the resulting progress.

## Non-Goals

No raw-event or `/v1/analyze` route — [ADR 0031 §4](../../adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
stands. No new platform endpoint of any kind, and no collection, list or
history route. No dynamic reconfiguration or policy reload: like `policy:`, the
evaluation block compiles once and is fixed for the processor's lifetime, so
switching runs means a new process. No authentication, TLS or credential store
(070). No blind retry, queue or spool — the only retry is the
same-sequence reconciliation above, which is the server's replay contract
rather than a retry policy. No promotion (066), no event history (067). No
core change: `Engine`, `event`, `Result` and `DecisionRecord` are untouched,
and no platform concept enters the core.
