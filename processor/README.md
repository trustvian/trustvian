# trustvian-processor

An OpenTelemetry Collector processor that scores every span passing
through a Collector pipeline with [Trustvian](https://github.com/trustvian/trustvian)
and enriches it with the outbound `trustvian.*` attributes, before
forwarding it unchanged in shape to the next consumer in the pipeline.

This is a **separate Go module**, deliberately — see
[ADR 0003](../docs/adr/0003-opentelemetry-adapter-single-module.md) in
the core repository. Building a real Collector component requires the
`go.opentelemetry.io/collector/*` component APIs, a materially heavier
dependency tree than the core engine's own lightweight OTel API/SDK
usage; keeping it in its own module means that tree never touches
`github.com/trustvian/trustvian`'s own `go.mod`, even indirectly.

## How it fits together

```
Application (OTel SDK)
        │  OTLP
        ▼
  OTLP receiver
        │  ptrace.Traces
        ▼
trustvian processor  ← this module
        │  ptrace.Traces, now carrying trustvian.* attributes
        ▼
   (your exporter)
```

Internally, for each span:

```
ptrace.Span + resource attributes
        │  mapping.go: EventFromSpan
        ▼
     event.Event
        │  Engine.Analyze (github.com/trustvian/trustvian, public API)
        ▼
       Result
        │  attributes.go: SetAttributesFromResult
        ▼
ptrace.Span, enriched in place
```

## Why this can't just import `internal/otel`

The core module's `internal/otel.EventFromSpan` and
`AttributesFromResult` ([task 008](../docs/archive/tasks/v0.2/008-otel.md)) cannot be
reused here, for two independent reasons:

1. **They're under `internal/`.** Go's `internal/` visibility rule
   blocks any package outside `github.com/trustvian/trustvian` itself
   from importing them — this module is a genuinely separate module,
   so it's on the wrong side of that boundary, the same as any other
   embedder (see
   [ADR 0002](../docs/adr/0002-public-api-boundary.md)).
2. **Even without that restriction, the span type is wrong.**
   `internal/otel.EventFromSpan` takes `sdktrace.ReadOnlySpan` — a type
   only the OpenTelemetry **SDK**'s own in-process span-export path
   produces (it has a deliberately unexported method; see the core
   module's own testing notes in `docs/OPENTELEMETRY.md`). A Collector
   processor never sees that type. It receives `ptrace.Span` — the
   OTLP/pipeline data model (`go.opentelemetry.io/collector/pdata/ptrace`)
   — which shares no relationship with `sdktrace.ReadOnlySpan` at all.

So `mapping.go` and `attributes.go` in this module are **parallel
implementations**, not shortcuts around ones that could have been
shared instead. They reuse the exact same semantic-convention key
constants (`go.opentelemetry.io/otel/semconv`, a plain-constants
package with no SDK dependency) as the core module's adapter, so the
convention *names* can never drift between the two — only the
traversal code, which the two different span APIs force to differ, is
duplicated.

## Configuration

`Config` has four fields: `policy`, `storage`, `health`, and `evaluation`.
`policy` and `storage` each declare real Trustvian configuration in exactly
the same schema the Go SDK and the CLI already consume — see the core
repository's [Policy Guide](../docs/policy-guide.md) and [Storage
Guide](../docs/storage-guide.md) for the field references. `health` and
`evaluation` decode directly through their own `mapstructure` tags instead:
neither has a canonical Trustvian type to defer to, because each is a
property of this runtime rather than of the engine.

`storage` (added by core task 037) is what lets a Collector persist. Before
it, this processor called `NewEngine` with at most `WithPolicy` and never
`WithStore`, so every Collector deployment ran on Trustvian's non-durable
in-memory default and discarded every learned baseline on restart. The
block is decoded into `config.StorageConfig` and compiled by
`config.CompileStorage`; this module contains no database code, no DSN
parsing, and no storage configuration model of its own. An unreachable or
misconfigured database fails Collector startup rather than silently falling
back — and `Shutdown` releases the connection pool.

Omitting `storage` keeps the in-memory default, so a config written before
the field existed behaves identically.

`health` (added by core task 042) enables the runtime's operational
endpoints:

```yaml
processors:
  trustvian:
    health:
      endpoint: 0.0.0.0:13133      # default
      readiness_timeout: 2s        # default
```

`GET /livez` reports whether the runtime is functioning and never consults
the store; `GET /readyz` reports whether it can safely process work and does.
When PostgreSQL is configured and unusable, readiness is 503 — never a
silent fall back to non-durable storage. Both bodies carry a status string
and nothing else. Omitting the block binds no listener.

**Readiness reflects the configured `storage:` store only, never the
evaluation control plane.** When `evaluation:` is also configured, a control
plane that is down is not what `/readyz` answers: adding it would put
third-party network I/O inside a health probe, and — the reason that matters
more than convenience — a Collector serves exactly one evaluation run, so
every replica feeding it points at the same control plane. A false readiness
there would fail every replica at once and trigger a restart storm that
cannot help, since the control plane is not in any of those processes. That
is the identical reasoning liveness already gives for never probing the
store, applied one layer further out. Watch
`trustvian.evaluation.records{trustvian.outcome="error"}` and the ERROR-level
`evaluation ingest failed` log line (which carries `run_id`) for this
instead — see [Observability](../docs/observability.md).

`evaluation` (added by core task 073) posts every decision to a Trustvian
control plane, so a workload observable only through OpenTelemetry can be
evaluated:

```yaml
processors:
  trustvian:
    evaluation:
      api_url: http://127.0.0.1:54321
      run_id: run-reference
      behavioral_profile: support-reference
      required: true
```

The run must already exist and be **running** — nothing here creates one,
because run lifecycle belongs to the control plane. This is checked, not just
documented: `Start` reads the run's own status before seeding the ingest
cursor, and refuses to come up — naming the actual status — against a run
that is still pending or has already finished, rather than coming up clean
and then failing every span from the first one onward once the control plane
itself refuses records for a run that is not running. `behavioral_profile`
must match the run's, and it also selects the Engine's learning scope, so two
candidates evaluated against the same store never train each other's
baseline.

The record posted is `Result.DecisionRecord()` projected from the same
`Result` that produced the `trustvian.*` attributes. `Analyze` runs once, and
nothing is reconstructed from those attributes — they are five values, and a
record carries contributors, policy reason, confidence and context risk that
none of them contains.

`required: true` is the only accepted value. `required: false` fails startup,
because a run that silently lost evidence would still report as complete and
its gate would still evaluate.

Bounds: 30s per request, a 256 KiB request body matching the server's own cap,
a 64 KiB response bound, refused redirects, and no blind retries. A URL
carrying credentials is rejected without repeating it into Collector logs.

**A lost response is not a lost record.** A POST can be written in full,
committed by the control plane, and have only its reply destroyed — a reset
connection, a timeout, an EOF partway through. Treating that as "it did not
happen" would hand the next record a sequence the server already holds, and
the run would then reject every record that followed. So failures are
classified:

| Class | Examples | What happens |
|---|---|---|
| Definitely not applied | failed dial, DNS failure, refused redirect, oversized body, a 4xx from the control plane | The sequence is free; the next record takes it. |
| Outcome unknown | reset connection, timeout, EOF mid-response, any 5xx, an unreadable 2xx | The sequence stays bound to that exact record and is reconciled. |

Reconciliation is the server's own contract, not a retry policy: the same
record is re-presented at the same sequence, the control plane recognizes the
identical digest and answers `replayed`, and the cursor advances to the
`next_sequence` it returns. One attempt, made inside the same call, under the
same context — no queue, no goroutine, no backoff. The metric shows it as
`trustvian.evaluation.records{trustvian.outcome="replayed"}`.

Until a held sequence is reconciled, the sink accepts no other record. That
is deliberate: the alternative is reusing a sequence the control plane may
already have committed.

An ingest failure the sink could **not** resolve — a declined record, or one
still unconfirmed after reconciliation — is a **permanent** consumer error:
the pipeline does not retry the batch, and — worth stating plainly, since it
is the first thing an operator will notice — the **whole batch is abandoned,
not forwarded**, including every span already enriched ahead of the failure.
An evaluation-configured Collector whose control plane goes down therefore
stops exporting traces entirely, not only evaluation records. A batch retry
would re-analyze spans whose records already committed and resend them under
new sequence numbers, and duplicate records count twice by design.

**Learning follows the evidence.** `Engine.Observe` runs when the record may
be durable — including a record still awaiting reconciliation — and does not
run when the control plane declined it. That is what keeps the run's evidence
and this Collector's baselines describing the same history; it runs at most
once per span either way, so a reconciled record is one record and one
observation.

**What this leaves behind:** if a batch fails partway through, the spans
analyzed before the failure already posted durable records and advanced the
run's cursor past them, but nothing in the run describes that the batch was
cut short — its counts stay internally consistent, just short of what would
otherwise have arrived. Recovering means starting a **new run**, not
rerunning the same batch into this one: a rerun would re-post the
already-committed prefix under new sequence numbers, which the control plane
accepts as new records and silently doubles them — the exact corruption the
permanent-error wrapper exists to prevent.

**Omitting `evaluation:` entirely** preserves this processor's behavior
exactly — no sink, no learning scope, no lock on the span path, and no
evaluation metrics.

`WithAnomalyConfig`, `WithTrustConfig`, and `WithContextRisk` remain
unconfigurable from here for the same reason they always were: those
`Option`s take types from the core module's `internal/` packages that this
module — a genuinely separate one — structurally cannot construct. `Store`
is the exception precisely because core tasks 034–035 built a public
compilation boundary for it; `AnomalyConfig` has one too
(`config.CompileAnomaly`) and could be wired the same way when a
deployment needs it.

```yaml
processors:
  trustvian:
    policy:
      version: v1
      default_decision: observe_only
      default_reason: no policy rules configured; observing by default
      rules:
        - name: block-critical-risk
          when:
            min_risk_level: critical
          decision: block
          reason: critical risk is blocked by configured policy

    # Persist learned baselines, so they survive a Collector restart and
    # are shared by every replica pointed at the same database.
    storage:
      version: v1
      type: postgres
      postgres:
        # Supplied through the Collector's own ${env:...} provider, so the
        # DSN — which carries a password — stays out of this file.
        dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
```

For a complete, runnable version of the above, see the core repository's
[reference Docker Compose deployment](../deployments/docker-compose/).

**Omitting `policy:` entirely** preserves this processor's original
behavior exactly: every span still resolves to
`trustvian.decision = "observe_only"`, `NewEngine()`'s
zero-configuration default. The `trustvian.*` score/risk/fingerprint
attributes are always genuinely computed and meaningful regardless.

**An invalid or incomplete explicit `policy:` block** (an unrecognized
schema version, a missing `default_decision`/`default_reason`, an
invalid decision or condition value, or a duplicate rule name) fails
the whole Collector's startup — `CreateTraces` returns an error before
any pipeline runs, never a fallback to the default policy at the first
span.

**An invalid or unreachable `storage:` block** fails startup the same way,
and for a sharper reason: a Collector that fell back to in-memory storage
when its configured database was unavailable would keep enriching spans
with trust decisions derived from state that evaporates when the process
exits, with nothing telling the operator their database was never used.

Why `Config.Policy` is typed as a generic map (`map[string]any`)
rather than `config.PolicyConfig` directly: see the core repository's
[task 022](../docs/archive/tasks/v0.5/022-collector-config-integration.md) for the
full reasoning — in short, Collector's own confmap decoder only reads
`mapstructure` struct tags, matched case-sensitively, and
`config.PolicyConfig` only carries the `yaml:"..."` tags its own file
loader (task 020) added; `decodePolicy` (`config.go`) bridges that gap
by decoding with `go-viper/mapstructure/v2` pointed directly at those
existing `yaml` tags, producing a real `config.PolicyConfig` with zero
duplicate policy model anywhere in this module.

`processor/go.mod` carries a `replace` directive pointing at the
repository root, so this module builds against the core module's source
rather than a published tag. That changed in core task 037: the `storage`
field needs `config.StorageConfig`, which does not exist in any released
version (the newest tag is `v0.7.0`, and the `require` line still reads
`v0.5.0` as a floor). Writing `require ... v0.8.0` before that tag exists
would make this file assert something untrue.

`GOWORK=off go build ./... && GOWORK=off go test -race ./...` still
succeeds, and still proves what it did before — that this module needs no
Go workspace — but it now resolves the core module through the replace
rather than the module proxy. Core task 038 owns the release-time decision:
bump to `v0.8.0` and drop the replace once tagged, or keep it. See task
022's own "Release / Module Compatibility" section for this dependency's
earlier history.

## `trustvian.behavior.id`

Deliberately not implemented — see the identical rationale in the core
module's `docs/OPENTELEMETRY.md` § Trustvian output attributes. Nothing
about running inside a Collector processor gives this attribute a
meaning it didn't already have (or rather, didn't already lack).

## Running it

This module includes a minimal, hand-assembled Collector binary
(`cmd/trustvian-collector`) that registers this processor alongside the
standard OTLP receiver and the debug exporter — no `ocb` (OpenTelemetry
Collector Builder) install required, just `go run`:

```bash
cd processor
go run ./cmd/trustvian-collector --config=config.yaml
```

Then point any OTel-SDK-instrumented application at `localhost:4317`
(OTLP/gRPC) or `localhost:4318` (OTLP/HTTP). Enriched spans are logged
to stdout by the debug exporter, e.g.:

```
Attributes:
     -> http.request.method: Str(POST)
     -> server.peer.name: Str(checkout-frontend)
     -> trustvian.anomaly.score: Double(1)
     -> trustvian.trust.score: Double(1)
     -> trustvian.risk.level: Str(low)
     -> trustvian.decision: Str(observe_only)
     -> trustvian.fingerprint.id: Str(b106dcd5d46d7ef7)
```

(captured from a real run of this exact `config.yaml` against a real
`go.opentelemetry.io/otel/sdk` span, sent over real OTLP/gRPC — not
hand-written, matching the core repository's own documentation
standard.)

The binary's own internal telemetry (its logger, tracer, meter
providers) uses a small hand-built `telemetry.Factory`
(`cmd/trustvian-collector/telemetry.go`) rather than the Collector's
full-featured default (`otelconftelemetry`), which transitively pulls
in cloud-provider resource detectors and a Kubernetes API client —
several hundred extra dependency-graph entries a "minimal working
version" (this task's own words) has no use for.

## Observability

The processor emits seven OpenTelemetry metrics through the
`MeterProvider` the Collector injects — no configuration, no vendor
client, and nothing to turn on:

| Metric | Type | Unit | Attributes |
|---|---|---|---|
| `trustvian.analyses` | Counter | `{analysis}` | `trustvian.outcome`: `analyzed`, `invalid_event`, `error` |
| `trustvian.decisions` | Counter | `{decision}` | `trustvian.decision`: the six `policy.Decision` values, plus `other` |
| `trustvian.analysis.duration` | Histogram | `s` | *(none)* |
| `trustvian.observations` | Counter | `{observation}` | `trustvian.outcome`: `learned`, `not_eligible`, `error` |
| `trustvian.observe.duration` | Histogram | `s` | *(none)* |
| `trustvian.evaluation.records` | Counter | `{record}` | `trustvian.outcome`: `applied`, `replayed`, `error` |
| `trustvian.evaluation.duration` | Histogram | `s` | *(none)* |

Nineteen time series in total, fixed no matter how many actors or
environments the deployment sees — and the last four exist only in a
Collector configured to feed an evaluation run. Every attribute has a closed
vocabulary whose measurement options are pre-built at construction, so
an actor ID, trace ID, or raw error string cannot become a label even by
mistake. Instrumentation is allocation-free on the span path.

Both duration histograms carry explicit bucket boundaries spanning 1 ms
to 10 s, because the SDK's defaults assume milliseconds while these
instruments record seconds.

**The bundled `cmd/trustvian-collector` supplies a no-op
`MeterProvider`**, so it records these metrics and exports none of them —
its minimal telemetry factory exists to keep the demo's dependency graph
small, and `service.telemetry.metrics` is rejected there for the same
reason. Build a distribution with `ocb` and `otelconftelemetry` to
collect them for real.

The in-process counters (`processed`, `invalid`, `analyzeErrors`, per
`Decision`) remain, unexported, for tests and for the debug log line at
shutdown.

Full metric reference, the cardinality argument, what is deliberately
*not* emitted, and the measured cost:
[`docs/observability.md`](../docs/observability.md).

## Testing

`go test ./... -race` covers: the `ptrace.Span` → `Event` mapping
(mirroring the core module's own semantic-convention test cases),
`Result` → attribute writing, the factory's component lifecycle, a
full `ConsumeTraces` unit test (span in, enriched span out, forwarded),
a malformed-span-doesn't-fail-the-batch test, a concurrency test (many
goroutines calling `ConsumeTraces` on one processor instance), and —
since task 022 — the `policy:` configuration path: decoding a real
`policy:` block through Collector's own `confmap` decoder, an invalid
policy failing `CreateTraces` outright, a configured Policy actually
changing a real span's `trustvian.decision`, an omitted `policy:`
preserving the pre-task default, and first-match-wins rule ordering
surviving decode + compile. Since task 043 it also covers the metrics:
each instrument through an in-memory SDK reader, recorded attribute sets
asserted to contain no forbidden value, the cardinality bound asserted
exactly, a forced store error counted as a bounded category, and the
whole span path driven concurrently under `-race`.

## Non-goals (this version)

No distributed/multi-instance Trustvian server. No Kubernetes/Helm
packaging. No new policy language, matchable condition, or dynamic
policy reload — and no dynamic evaluation reconfiguration either. Both
the configured Policy and the configured evaluation run compile once, at
processor creation, and are fixed for the processor's lifetime, so
feeding a different run means a new process. No dashboards,
Grafana packaging, or vendor metrics client — the Collector's exporters
already reach every backend. No Alert configuration (`alerts:`/`sinks:`/`webhook:` in Collector
config) — a separate, future task. These match the scope boundaries in
the core repository's
[`docs/archive/tasks/v0.2/009-otel-collector.md`](../docs/archive/tasks/v0.2/009-otel-collector.md)
and [`docs/archive/tasks/v0.5/022-collector-config-integration.md`](../docs/archive/tasks/v0.5/022-collector-config-integration.md).
