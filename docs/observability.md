# Observability

How to see what Trustvian itself is doing: the metrics it emits, what
each one means, what it deliberately does *not* emit, and the resource
bounds a long-lived deployment runs within.

This document is about **the health of Trustvian**. It is not about
observing actors more deeply — that is what the engine's decisions and
the `trustvian.*` span attributes are for, and they are covered in
[OpenTelemetry Adapter](OPENTELEMETRY.md).

## Two streams that never meet

```text
actors' behavior  →  Trustvian engine  →  decisions            (the product)

Trustvian runtime →  operational metrics  →  OTel pipeline     (this document)
```

Operational metrics describe *Trustvian*; behavioral telemetry describes
*its subjects*. Routing the first into the second would make the engine
analyze its own analysis, producing anomaly signals about nothing.
Nothing connects them, and nothing in the default configuration can: the
metrics package only records, and has no path back into an `Engine`.

## Where the metrics come from

Instrumentation lives in the **Collector processor**
([`processor/`](../processor/)), not in the engine.

The core module — `event` through `internal/policy` and the root
`Engine` — has zero OpenTelemetry in its dependency graph, and that is
enforced, not aspirational (`go list -deps` reports no OTel package in
any core package; see [Architecture](ARCHITECTURE.md)). Instrumenting
`Engine.Analyze` would push an OTel dependency onto every SDK consumer
to serve a concern only the long-lived service has.

The processor already receives a `MeterProvider` through the Collector's
`TelemetrySettings`, so instrumentation needs no global state and no
configuration:

- **Nothing to turn on.** If your Collector exports metrics, Trustvian's
  appear alongside the Collector's own.
- **Nothing to turn off.** With no metrics pipeline configured, the
  provider is a no-op and recording costs nothing measurable
  ([below](#what-it-costs)).
- **Provider lifecycle belongs to the Collector.** Trustvian never
  shuts down a provider it did not create.

Using the Go SDK or the CLI directly, rather than the Collector? Then
these metrics do not exist — they are a property of the long-lived
runtime, not of the library.

### The bundled demo collector records them into a no-op

`processor/cmd/trustvian-collector` — the minimal binary the reference
Docker Compose deployment runs — supplies a **no-op `MeterProvider`**. Its
hand-built telemetry factory exists to keep the demo's dependency graph
small: the Collector's own `otelconftelemetry` factory transitively pulls
in AWS/Azure/GCP resource detectors and a Kubernetes client, which a
minimal demo has no use for.

The consequence is worth stating plainly: **Trustvian records these
metrics there, and nothing exports them.** `service.telemetry.metrics` is
rejected by that binary for the same reason, since no telemetry factory
claims the key.

To actually collect them, build a Collector distribution the normal way —
[`ocb`](https://opentelemetry.io/docs/collector/custom-collector/) with
`otelconftelemetry` — and include this processor. Then
`service.telemetry.metrics` works as upstream documents it, and the
metrics appear alongside the Collector's own:

```yaml
service:
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 127.0.0.1
                port: 8888
```

Verified against a real Collector built that way, with PostgreSQL
storage, after 60 spans:

```text
trustvian_analyses{trustvian_outcome="analyzed"} 60
trustvian_decisions{trustvian_decision="allow"} 60
trustvian_observations{trustvian_outcome="learned"} 60
trustvian_analysis_duration_count 60
trustvian_observe_duration_count 60
```

## Metric reference

Seven instruments. Names follow OpenTelemetry conventions: namespaced,
lowercase, no `total`/`count` suffix on counters (exporters add those per
their own conventions), UCUM units, durations in seconds.

Scope name: `trustvian-processor`.

| Metric | Type | Unit | Attributes | Series |
|---|---|---|---|---|
| `trustvian.analyses` | Counter | `{analysis}` | `trustvian.outcome` | 3 |
| `trustvian.decisions` | Counter | `{decision}` | `trustvian.decision` | 7 |
| `trustvian.analysis.duration` | Histogram | `s` | *(none)* | 1 |
| `trustvian.observations` | Counter | `{observation}` | `trustvian.outcome` | 3 |
| `trustvian.observe.duration` | Histogram | `s` | *(none)* | 1 |
| `trustvian.evaluation.records` | Counter | `{record}` | `trustvian.outcome` | 3 |
| `trustvian.evaluation.duration` | Histogram | `s` | *(none)* | 1 |

**Total: 19 time series**, fixed — regardless of how many actors,
events, environments, or tenants the deployment sees. Fifteen come from
analysis, decisions and observations and exist on every Collector; the
remaining four are evaluation ingest and exist only when `evaluation:` is
configured. That property is the point, and it is [enforced by
construction](#cardinality-is-a-hard-bound).

### `trustvian.analyses`

Spans Trustvian attempted to analyze, by outcome. `trustvian.outcome` is
one of:

| Value | Meaning |
|---|---|
| `analyzed` | The span mapped to a valid `Event` and the engine produced a `Result`. |
| `invalid_event` | The span did not map to a `Validate`-passing `Event` — usually a missing `service.name` resource attribute. Not an error: most pipelines carry spans Trustvian has no opinion about. |
| `error` | `Engine.Analyze` returned an error, typically because the store was unreachable. |

A span counted here is **not** dropped: a span that fails to map or fails
to analyze is forwarded un-enriched, so one malformed span never costs
the rest of its batch.

A rising `invalid_event` rate right after a deployment change usually
means spans lost an attribute, not that Trustvian broke.

### `trustvian.decisions`

Policy decisions produced, by `trustvian.decision`: `allow`,
`observe_only`, `alert`, `challenge`, `require_approval`, `block` — the
six `policy.Decision` values — plus `other`.

`other` should always be zero. A non-zero value means a decision type
exists in the engine that this instrumentation does not know about, and
is a signal to update the vocabulary rather than evidence of a security
event. See [cardinality](#cardinality-is-a-hard-bound) for why it exists.

### Duration histograms and their buckets

Both histograms carry explicit bucket boundaries as instrument advice,
spanning 1 ms to 10 s:

```text
0.001  0.0025  0.005  0.0075  0.01  0.025  0.05  0.075
0.1    0.25    0.5    0.75    1     2.5    5     7.5    10
```

They are stated rather than left to the SDK because the SDK's defaults —
`0, 5, 10, 25, … 10000` — assume **milliseconds**. These instruments
record **seconds**, so against the defaults every realistic measurement
lands in the first bucket: a histogram that counts correctly and
describes nothing. That is not hypothetical — it is what a live Collector
scrape showed before the boundaries were added.

### `trustvian.analysis.duration`

Duration of Trustvian's own analysis of one event, recorded only for the
`analyzed` outcome.

This measures **the engine call and nothing around it** — not span
mapping, not the Collector's receive path. Starting the clock earlier
would fold in work the Collector already measures and make the number
mean something other than its name says. Compare it against
`otelcol_processor_*` to separate "Trustvian is slow" from "the pipeline
is slow."

Nothing is recorded for `invalid_event` (no analysis ran) or, in the
histogram, anything that would let a failed call skew the distribution of
successful ones.

### `trustvian.observations`

Learning attempts, by `trustvian.outcome`:

| Value | Meaning |
|---|---|
| `learned` | The result was learning-eligible and was folded into the baseline. |
| `not_eligible` | The decision held or stopped the action, so it was correctly excluded from learning. |
| `error` | The store failed to persist the observation. |

`not_eligible` is normal and expected traffic, not a failure: it is the
baseline-poisoning defense working. `CHALLENGE`, `REQUIRE_APPROVAL`, and
`BLOCK` are all ineligible by design — see
[Security Model](SECURITY.md) and
[`.claude/rules/security.md`](../.claude/rules/security.md).

A sustained `error` rate here means the engine is still deciding
correctly but is no longer *learning* — baselines are going stale
silently. It is the single most useful alert in this list.

### `trustvian.observe.duration`

Duration of folding one observation into the baseline, including
storage. Recorded for **every** outcome, including errors: a failed
`Observe` has still paid the storage round trip, and that latency is
exactly what an operator investigating a slow database wants to see.

With the in-memory or file store this is sub-microsecond; with
PostgreSQL it is your database round trip, and it is the metric that
tells you so.

### `trustvian.evaluation.records`

Decision records offered to a control plane, by `trustvian.outcome`. Exists
only in a Collector configured with `evaluation:`.

| Value | Meaning |
|---|---|
| `applied` | The record was folded into the evaluation run's evidence. |
| `replayed` | This exact record was already applied — a restarted Collector resent it, or the sink reconciled a POST whose response was lost, and the control plane recognized it as the same record. Nothing changed, and no second record exists. |
| `error` | The post failed: the control plane declined the record, or the sink could not confirm it either way. Nothing was learned from it. |

**A climbing `replayed` count is not an error, but it is a signal.** A
Collector that is not restarting should replay rarely: a steady rate means
responses are being lost between it and the control plane, and each one costs
an extra serialized round trip on the span that lost it.

**A climbing `error` count does not mean one record was skipped.** An
unresolved ingest failure is a permanent consumer error
([`processor/README.md`](../processor/README.md)), so it aborts the whole
`ConsumeTraces` batch: every span in that batch — including ones already
enriched ahead of the failure — is abandoned rather than forwarded to the
next consumer. An evaluation-configured Collector whose control plane is
down is therefore not losing evaluation coverage alone; it has stopped
exporting traces entirely. This is the single most useful alert in this
list for an evaluation-configured Collector, the same way a sustained
`trustvian.observations{trustvian.outcome="error"}` rate is for learning.

### `trustvian.evaluation.duration`

Duration of delivering one decision record: the request, and — once the
control plane confirms it — the durable pending-state writes and the
`Engine.Observe` they gate. Recorded for **every** outcome, including errors
— the same reasoning as `trustvian.observe.duration`: a failed post has still
paid the network round trip, and that latency is exactly what an operator
investigating a slow or wedged control plane wants to see. The observation's
own share is isolated in `trustvian.observe.duration`.

### Two startup lines worth alerting on

With `evaluation:` configured, a Collector that restarts while a record was
in flight reports what became of it:

- `a record left pending by a previous process never reached the run and was
  discarded` (WARN) — the run is one record short, which is the documented
  outcome of a batch that failed. Frequent ones mean the control plane is
  unreachable often enough to be losing evidence.
- `a record the run holds may not have been learned from` (ERROR) — the one
  case a restart cannot settle. The run holds the record; whether this
  Collector's baseline learned from it is unknowable, so it was not learned
  again. At most one observation is missing, in the direction that fails
  safe. Repeated occurrences mean the process is dying inside the window
  between confirming a record and releasing it, which is worth investigating
  on its own.

## Cardinality is a hard bound

Every attribute has a closed vocabulary enumerated as constants in code.
Nothing is derived from input.

The enforcement is structural, not a convention: the instrumentation
pre-builds one measurement option per vocabulary entry at construction
time, so a value with no entry has no option to record with. An
unbounded label cannot reach an instrument even by mistake — an
unrecognized decision records as `other` rather than creating a series.

These are **forbidden** as attributes, and a test asserts their absence
against recorded telemetry rather than against this paragraph:

> actor IDs · session IDs · trace IDs · span IDs · target names ·
> operation names · fingerprints · environments · tenant identifiers ·
> raw error strings · DSNs · database hostnames · prompts · tool
> arguments · request bodies

Each is either unbounded or is data about a *subject* rather than about
Trustvian. Error *categories* are recorded; error text never is — a raw
`err.Error()` as a label is the classic way a metrics backend acquires an
actor identifier, a hostname, or a credential by accident.

The behavioral detail already travels on the span, where it belongs and
where sampling applies.

## What is deliberately absent

| Not emitted | Why |
|---|---|
| A Trustvian span counter | The Collector already counts spans accepted, refused, and dropped per component. A second, subtly different number for the same thing would just force operators to learn which to trust. |
| Process memory / CPU / uptime | The Collector already reports these for the whole binary. |
| A `ready` gauge | [`/readyz`](#health-endpoints) is the authoritative health surface. A gauge would be a second answer to the same question that can disagree with the first across a scrape gap. |
| Per-actor or per-fingerprint metrics | Unbounded by construction. This is the whole cardinality argument above. |
| A vendor client (Prometheus, StatsD, Datadog) | The Collector's exporters already reach every backend, vendor-neutrally. Adding a client would put a second telemetry path in a security component. |

## Telemetry cannot affect a decision

Instrumentation is a side effect, never a dependency:

- Recording is an in-process atomic update. **No synchronous remote call
  is added to the span path.**
- Export is the SDK's and the Collector's concern, and does not block
  recording.
- A broken metrics backend therefore cannot slow, block, or alter a
  security decision — it can only make Trustvian harder to watch.
- Even instrument *construction* failure degrades rather than failing
  startup: the processor logs the error and runs uninstrumented. A
  telemetry problem must never stop Trustvian from deciding.

A security decision must never depend on an observability backend, and
it does not.

## What it costs

Measured on an Apple M3 Pro, `BenchmarkConsumeTraces` — one realistic
HTTP server span through the full processor path — at `-benchtime
20000x -count 8`, reporting medians:

| Configuration | ns/op | B/op | allocs/op |
|---|---|---|---|
| Before this instrumentation existed | 1210 | 1352 | 33 |
| With instrumentation, no metrics pipeline | 1226 | 1352 | 33 |
| With instrumentation and a real metrics SDK | 1562 | 1353 | 33 |

Two things worth reading off that table:

- **Instrumentation allocates nothing.** Allocations per span are
  identical to before it existed, which is what the pre-built attribute
  sets buy. An earlier implementation using `metric.WithAttributes` at
  each call site cost 3 extra allocations per span even when the meter
  was a no-op, because the attribute set was built regardless.
- **The ~350 ns is the SDK's own aggregation**, across five recorded
  measurements per span (~70 ns each) — six when `evaluation:` is
  configured, since `RecordEvaluationIngest` adds one more — and is paid
  only when a metrics pipeline is actually configured. It is inherent to
  synchronous OTel instruments, not something Trustvian is doing
  inefficiently.

Reproduce with:

```bash
cd processor
GOWORK=off go test -run '^$' -bench ConsumeTraces -benchtime 20000x -count 8 .
```

## Health endpoints

Metrics answer "what is it doing"; the health endpoints answer "should
traffic reach it." They are separate on purpose.

| Endpoint | Answers | Consults the store |
|---|---|---|
| `/livez` | Is the process alive and not wedged? | **Never** |
| `/readyz` | Can the configured store do useful work right now? | Yes, with a bounded timeout |

Liveness deliberately never probes the store: a database outage must not
get the process killed and restarted, because restarting fixes nothing
and drops the in-flight pipeline. Readiness reports it, so traffic
drains instead.

**Readiness does not cover the evaluation control plane.** With
`evaluation:` configured, `storeProbe` — the only input `/readyz` has — still
reflects the configured `storage:` store alone; a control plane that is
unreachable is invisible to it, and `/readyz` keeps returning 200 while every
trace batch fails. That is deliberate, for the same reason liveness never
probes the store: adding the control plane would put third-party network I/O
inside a health probe, and a Collector serves exactly one evaluation run, so
every replica feeding it shares one control plane. A false readiness there
would take every replica down at once — a restart storm that cannot help,
since the control plane being down is not a condition restarting any of them
fixes. A down control plane surfaces instead as the ERROR-level `evaluation
ingest failed` log (carrying `run_id`) and as
`trustvian.evaluation.records{trustvian.outcome="error"}` climbing — see
[below](#trustvianevaluationrecords).

Configuration, payloads, and shutdown ordering are in
[`processor/README.md`](../processor/README.md); the fail-closed
persistence contract behind readiness is in
[Storage Guide](storage-guide.md).

## Resource bounds

A runtime security engine that leaks is a security problem, so the
long-lived runtime's resource ownership is audited rather than assumed.
The most useful result is how little there is: non-test Trustvian code
contains **one** goroutine, **zero** channels, **zero** tickers, and
**zero** timers.

| Resource | Bound | Shutdown owner |
|---|---|---|
| Health-server goroutine | One, with a bounded header-read timeout | `Shutdown` → `http.Server.Shutdown` |
| Channels, queues, tickers, timers | None exist | — |
| PostgreSQL pool | `MaxConns`; connection lifetime 1h, idle 30m | `Shutdown` → `Close`, exactly once |
| In-memory store | O(distinct actors), each actor's baseline capped | Process lifetime |
| Meter and instruments | Fixed, 19 series (15 always, 4 evaluation-only) | **The Collector** — never Trustvian |
| Engine | One, fully synchronous per call | Process lifetime |
| Evaluation sink HTTP client | Holds no goroutine, timer, or dedicated transport of its own — it uses the shared `http.DefaultTransport`, whose idle connections are already bounded and reaped | Nothing to shut down |

The evaluation sink's client is worth being explicit about, since it is the
one resource this task added: it never sets its own `http.Transport`, so it
rides `net/http`'s process-wide default — the same one already bounding and
reaping idle connections for everything else in the process. That absence is
*why* `Shutdown` correctly never touches it, not an oversight this inventory
missed until now: there is no per-client pool, goroutine, or timer for a
teardown step to release.

No Trustvian-owned queue exists, bounded or otherwise, and the pipeline
adds no per-event goroutine — `Analyze` and `Observe` are synchronous,
which is what keeps goroutine-leak risk at zero.

No global concurrency limiter is added either: the Collector owns
pipeline concurrency, and a second limiter would mean two control layers
fighting over one throughput number.

### PostgreSQL pool

Trustvian overrides only `MaxConns`, and only when configured.
Everything else is pgx's default — `MinConns` 0, `MaxConnLifetime` 1h,
`MaxConnIdleTime` 30m, `HealthCheckPeriod` 1m. All finite: the pool
cannot grow without bound, and connections are recycled rather than held
forever.

**No Trustvian configuration is added for these.** pgx already accepts
`pool_max_conns`, `pool_max_conn_lifetime`, and the rest as DSN
parameters, so an operator who needs them already has them. Duplicating
pgx's tuning surface would create two places to set one value — see
[Storage Guide](storage-guide.md).

### In-memory store growth

The in-memory store holds one entry per distinct `{ActorID,
Environment}`. Growth has two dimensions, and they are bounded
differently.

**Within one actor: bounded.** A baseline learns at most 512 distinct
fingerprint identities (`internal/baseline`'s `maxFingerprints`), and
every map inside a fingerprint's statistics is independently capped at
64. Once a baseline reaches 512 identities, a new one is refused
admission — nothing is evicted, and everything already learned keeps
updating. See
[ADR 0019](adr/0019-bounded-fingerprint-admission.md) for why refusal
rather than eviction.

That makes a single baseline's worst case structurally bounded. The one
size measurement this repository has is
`TestLargeBoundedBaselineRoundTrips`, which serializes a baseline
holding 120 fingerprints with the inner caps filled to roughly 13 KB —
about 110 bytes of serialized state per fingerprint. Extrapolating
gives a baseline at the 512 cap on the order of 50–60 KB serialized.
Treat that as an order of magnitude for planning, not a measurement:
it is arithmetic from a smaller case, resident heap is not serialized
size, and a directly measured figure at the cap is still outstanding
work.

**Across actors: not bounded.** There is **no TTL, no eviction, and no
maximum** on the number of distinct `{ActorID, Environment}` entries.
That is a deliberate property. A behavioral baseline exists to
accumulate what normal looks like for an actor; evicting one silently
resets that actor to "never seen", so the next legitimate action looks
novel and may be challenged or blocked. Eviction across actors would be
a change to behavioral semantics wearing the costume of a memory
optimization.

So total footprint scales with the number of distinct actors ever
observed, with each actor's contribution capped. Long-lived deployments
with many actors should use PostgreSQL, which is what it is for. This is
an operational characteristic to plan around, not a defect to work
around.

## See also

| Topic | Document |
|---|---|
| Span attributes Trustvian reads and writes | [OpenTelemetry Adapter](OPENTELEMETRY.md) |
| Processor configuration and health endpoints | [`processor/README.md`](../processor/README.md) |
| Store backends, fail-closed persistence | [Storage Guide](storage-guide.md) |
| Engine hot paths and benchmark methodology | [Performance](PERFORMANCE.md) |
| Why learning is gated | [Security Model](SECURITY.md) |
| A runnable stack that exercises all of this | [Reference Deployment](../deployments/docker-compose/README.md) |
