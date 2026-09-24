# OpenTelemetry Adapter

`internal/otel` is the *only* package in this module that imports
`go.opentelemetry.io/otel*`. The core engine (`event` through
`internal/policy`, and `Engine` itself) has zero OpenTelemetry
dependency — see
[Architecture § package boundaries](ARCHITECTURE.md#package-boundaries).

It's currently `internal/`, so — like `Policy` and the `Config` types —
it's usable by code inside this repository but not yet importable from
a separate Go module. Note this means the shipped [OTel Collector
processor](#the-otel-collector-processor) does *not* use it — see that
section for why it necessarily carries its own parallel mapping code
instead. See [Go SDK Guide § the public/internal boundary
today](sdk-guide.md#configuring-from-outside-the-module).

## What it does

```go
func EventFromSpan(span sdktrace.ReadOnlySpan) event.Event
```

A pure, deterministic mapping from one finished OpenTelemetry span to
one `event.Event`. It uses standard semantic conventions where they
exist, and four documented `trustvian.*` attributes as an escape hatch
for what no convention covers yet (there is currently no standard
convention for, e.g., "this span represents an AI-agent tool call").

## Mapping table

| `Event` field | Derived from |
|---|---|
| `ID`, `Context.SpanID` | `span.SpanContext().SpanID()` |
| `Context.TraceID` | `span.SpanContext().TraceID()` |
| `Timestamp` | `span.StartTime()` |
| `Operation.Name` | `span.Name()` |
| `Operation.Category` | `http.request.method` → `http`; `db.system.name` → `db`; `rpc.system.name` → `rpc`; else falls back to `rpc` (see below) |
| `Operation.Direction` | `span.SpanKind()`: `Server`/`Consumer` → `inbound`, `Client`/`Producer` → `outbound`, else unspecified |
| `Target.Name` | `service.peer.name`, then `db.namespace`, then `server.address` — most specific first |
| `Actor.ID` | resource `service.name` |
| `Actor.Type` | defaults to `service` |
| `Actor.IdentityConfidence` | defaults to `1.0` |
| `Context.Environment` | resource `deployment.environment.name` |
| `Attributes` | every span attribute, unmapped or not — nothing is dropped |
| `Attributes["duration_ms"]` | `span.EndTime().Sub(span.StartTime())`, only if positive |
| `Attributes["error"]` | `true` if `span.Status().Code == codes.Error` |

**Why the RPC fallback.** A span matching none of HTTP/DB/RPC's
semantic conventions falls back to `Operation.Category = "rpc"` — the
most generic "some kind of call happened" bucket among Trustvian's
five categories (`http`, `db`, `rpc`, `tool`, `external`). See
[`internal/otel/otel.go`](../internal/otel/otel.go)'s `inferCategory`.

**Why latency/error are bridged, not mapped.** Span duration and
status aren't OTel *attributes* — they're separate fields on the span
itself. `features.Extract` only ever reads `Event.Attributes`, so the
adapter explicitly writes them into the two keys it understands
(`duration_ms`, `error`) rather than leaving them to be inferred.
Without this bridging, an OTel-derived event would silently carry no
latency/error signal at all.

## Trustvian-specific override attributes

| Attribute | Overrides |
|---|---|
| `trustvian.actor.id` | `Actor.ID` |
| `trustvian.actor.type` | `Actor.Type` (e.g. `"ai_agent"`) |
| `trustvian.identity.confidence` | `Actor.IdentityConfidence` |
| `trustvian.operation.category` | `Operation.Category` (e.g. `"tool"`) |

Example: an AI-agent tool-call span with no applicable standard
convention.

```go
tracer.Start(ctx, "search_customer",
	trace.WithSpanKind(trace.SpanKindInternal),
	trace.WithAttributes(
		attribute.String("trustvian.actor.type", "ai_agent"),
		attribute.String("trustvian.actor.id", "support-agent"),
		attribute.Float64("trustvian.identity.confidence", 0.9),
		attribute.String("trustvian.operation.category", "tool"),
	),
)
```

maps to an `Event` with `Actor.Type = ActorTypeAIAgent`,
`Actor.ID = "support-agent"`, `Actor.IdentityConfidence = 0.9`,
`Operation.Category = OperationCategoryTool` — exactly the shape the
[Use Cases § AI-agent security](use-cases.md#ai-agent-security) example
constructs directly as JSON, just sourced from a live span instead.

## Trustvian output attributes

```go
func AttributesFromResult(result trustvian.Result) []attribute.KeyValue
```

A pure function deriving the outbound `trustvian.*` attributes from a
`Result`, for a caller to attach to a span or export alongside one. It
does not write to a live span itself — attaching the returned
attributes to a real span is that caller's concern, not this adapter's
(the shipped [OTel Collector processor](#the-otel-collector-processor),
[task 009](archive/tasks/v0.2/009-otel-collector.md), is one such caller, though it
writes its own parallel attribute set rather than calling this function
directly — see that section for why). This is the one function in `internal/otel`
that depends on the root `trustvian` package rather than only `event`;
verified via `go list -deps` to introduce no import cycle and to leave
`internal/otel` the sole package in this module that imports
`go.opentelemetry.io/otel*`.

| Attribute | Derived from |
|---|---|
| `trustvian.anomaly.score` | `Anomaly.Score` |
| `trustvian.trust.score` | `Trust.Score` |
| `trustvian.risk.level` | `Trust.Risk` |
| `trustvian.decision` | `Result.Decision` |
| `trustvian.fingerprint.id` | `Fingerprint.ID` |

Do not confuse these *output* attributes with the four *input* override
attributes above (`trustvian.actor.id`, etc.) — the two serve opposite
directions of the same adapter boundary.

**`trustvian.behavior.id` is deliberately not implemented.** The
original project spec named it alongside the five above, but never
defined what it means beyond "carried over from the spec's original
naming." [Task 008](archive/tasks/v0.2/008-otel.md) resolved this by tracing every
plausible reading back to `Fingerprint.ID`: `internal/fingerprint`'s
`Fingerprint` already *is* the identity of one behavioral shape for an
actor (see [DOMAIN.md § Fingerprint](DOMAIN.md#fingerprint)), so a
second "behavior ID" attribute would either duplicate it exactly or
require inventing a new domain concept — an actor-level profile
spanning multiple `Fingerprint`s — that nothing in this codebase tracks
today. CLAUDE.md's OpenTelemetry section is explicit: don't invent
telemetry attributes without documenting them; documenting an attribute
whose meaning is still undefined is the same mistake with extra steps.
The name is reserved, not implemented, and stays that way until a real,
distinct behavior-level identity concept is scoped.

Values are not independently re-validated for `NaN`/`Inf` at this
boundary: `internal/trust.Compute` already clamps its inputs and output
to `[0,1]`, and `internal/anomaly`'s noisy-OR combination is bounded to
`[0,1]` by construction — both proven by
`TestComputeScenarioMatrixBoundsAndMonotonicity` and
`TestComputeClampsOutOfRangeInputs`. `AttributesFromResult` reads values
that are already guaranteed finite and bounded; re-checking them here
would duplicate an already-tested invariant, not close a real gap.

## The OTel Collector processor

[`processor/`](../processor/) (module `trustvian-processor`) is a
minimal, working OpenTelemetry Collector processor that scores every
span passing through a Collector pipeline and enriches it with the
`trustvian.*` attributes above — see [its own
README](../processor/README.md) for the full picture. This is
intentionally **not** part of this module:

- It depends on the `go.opentelemetry.io/collector/*` component APIs, a
  materially heavier dependency tree than the lightweight OTel API/SDK
  packages `internal/otel` uses.
- It imports this module's public API (`github.com/trustvian/trustvian`,
  for `Engine`/`Result`) via a `replace` directive during development,
  the same relationship any other embedder has — not a privileged
  internal dependency. `go list -deps ./...` in *this* module's root
  confirms `go.mod`/`go.sum` here are completely unaffected by
  `processor/`'s existence.

**It cannot reuse `internal/otel.EventFromSpan`/`AttributesFromResult`**,
for two independent reasons: those functions are under `internal/` and
therefore unreachable from a genuinely separate module (see [ADR
0002](adr/0002-public-api-boundary.md)); and, even ignoring that,
`EventFromSpan` takes `sdktrace.ReadOnlySpan` — a type only the OTel
**SDK**'s in-process span-export path produces — while a Collector
processor receives `ptrace.Span`, the OTLP/pipeline data model, which
shares no relationship with `sdktrace.ReadOnlySpan` at all. `processor/`
therefore carries its own parallel mapping and attribute-writing code,
reusing the same semantic-convention key constants
(`go.opentelemetry.io/otel/semconv`) so the convention *names* stay in
sync even though the traversal code necessarily differs.

**[ADR 0002](adr/0002-public-api-boundary.md)'s public API boundary was
considered and deliberately NOT revisited** as part of *originally*
building this processor (task 009) — `Policy` stayed unconfigurable
from Collector config, since building this processor was not, by
itself, the "real external consumer" trigger ADR 0002 named for
promoting it to a public package. [Task 019](archive/tasks/v0.5/019-policy-config-model.md)'s
`config` package later became that public surface for a different
consumer (the Go SDK and, per [task 021](archive/tasks/v0.5/021-cli-config-integration.md),
the CLI), without ADR 0002 needing to be revisited at all — and [task
022](archive/tasks/v0.5/022-collector-config-integration.md) is this processor's
own update to consume that same surface: an explicit `policy:` block
in Collector configuration now compiles into a real `Policy` via
`config.PolicyConfig`/`config.CompilePolicy`, identically to the Go
SDK and CLI. Omitting `policy:` still preserves the original
zero-configuration default (`trustvian.decision = "observe_only"` for
every span). See
[`processor/README.md` § Configuration](../processor/README.md#configuration)
for the full reasoning, including why `Config.Policy` is decoded as a
generic map rather than embedding `config.PolicyConfig` directly, and
task 022's own "Release / Module Compatibility" section for why this
is implemented and tested but not yet resolvable through
`processor/go.mod`'s committed dependency.

See [ADR 0003](adr/0003-opentelemetry-adapter-single-module.md) for why
this lives in a separate module at all, and
[`tasks/009-otel-collector.md`](archive/tasks/v0.2/009-otel-collector.md) for the
task this closes.

Since core task 073 the processor can also post its results to a Trustvian
control plane. An optional `evaluation:` block makes it project
`Result.DecisionRecord()` from the same `Result` that produced the attributes
above and send it to `POST /v1/evaluation-runs/{run_id}/records`, so a
workload instrumented with OpenTelemetry and nothing else can be evaluated
rather than only enriched. It reaches the control plane over HTTP and imports
no platform package — see
[ADR 0038](adr/0038-collector-evaluation-ingest-is-an-http-adapter.md) and
[`processor/README.md` § Configuration](../processor/README.md#configuration).

The output attributes and the posted record are two projections of one
`Result`, not two computations: `Analyze` still runs exactly once per span.

## Potential AI-agent (GenAI) mappings — documented, not implemented

`v0.7` ([task 014](archive/tasks/v0.7/014-ai-agent.md)) added `event.Context.SessionID`/
`DelegatedFrom`/`ApprovalStatus`. OpenTelemetry's own GenAI semantic
conventions (`gen_ai.*` span/event attributes — covering conversation
IDs, agent names, and tool-call spans) are, as of this writing, still
marked experimental/evolving upstream. `internal/otel.EventFromSpan`
does **not** read any `gen_ai.*` attribute today, and this task did not
add that mapping — hard-coding an unstable external convention into
this module's adapter would risk a breaking upstream rename reaching
into Trustvian's own `Event` semantics for no current consumer need.

Documented here as *potential* future adapter work, not a commitment or
an implemented behavior:

| OTel GenAI attribute (illustrative, unstable) | Potential Trustvian mapping |
|---|---|
| `gen_ai.conversation.id` | `Context.SessionID` |
| A span representing one delegated call between agents | `Context.DelegatedFrom` = the calling agent's identity |
| `gen_ai.tool.name` | `Operation.Name` (already representable via the existing `Operation`/`Target` mapping this document describes above — no new mapping rule needed) |

If and when a future task adopts a stabilized GenAI convention, the
change belongs entirely in `internal/otel` (or a dedicated adapter),
exactly like every other mapping this document describes — never in
`event`, `internal/features`, or any other core package, preserving
the "OTel is an adapter, never a dependency of the core" boundary
[`docs/ARCHITECTURE.md`](ARCHITECTURE.md) already establishes.

## Best-effort, not validated

`EventFromSpan` never fabricates data it doesn't have. A span with no
resource and no override attribute produces an `Event` with an empty
`Actor.ID` — which `Event.Validate()` (and therefore `Engine.Analyze`)
correctly rejects, rather than the adapter inventing a placeholder
identity. Always check the error from `Analyze`, or call
`ev.Validate()` yourself first if you're doing something other than
immediately analyzing the result.

## Testing note for contributors

`sdktrace.ReadOnlySpan` has a deliberately unexported method — only the
real SDK can produce one, so `internal/otel`'s tests build actual spans
through a `TracerProvider` with a capturing `SpanExporter`, not a
hand-rolled fake. See
[`internal/otel/otel_test.go`](../internal/otel/otel_test.go). One
non-obvious gotcha discovered while writing those tests: a
`TracerProvider` constructed with no `WithResource` still attaches its
own default `Resource` (including a fallback `service.name`) — pass
`resource.Empty()` explicitly to test the "no resource at all" case.
