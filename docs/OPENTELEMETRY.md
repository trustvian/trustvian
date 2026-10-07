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

### `trustvian dev` composes this processor for you

Since core task 077, `trustvian dev -- <command>` generates a Collector
configuration, starts `trustvian-collector` with it, and sets the workload's
exporter variables — so a locally-instrumented workload is evaluated without
anyone writing that configuration by hand. The generated document is
traces-only, enables **both** OTLP protocols, terminates in the `debug` exporter
at `basic` verbosity (so span attributes are never printed), and configures a
**file** engine store so that two runs of one candidate share a learned
baseline.

The variables `dev` sets in the workload's environment are the ordinary
specification ones, and all three matter:

```text
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT   the composed receiver, signal-specific
OTEL_SERVICE_NAME                    becomes the resource service.name
OTEL_RESOURCE_ATTRIBUTES             deployment.environment.name=<environment>
```

The endpoint variable is the **signal-specific** one rather than
`OTEL_EXPORTER_OTLP_ENDPOINT`, so a workload that also exports metrics or logs
somewhere keeps doing so. `deployment.environment.name` is not optional in
practice: without it the engine cannot fill the record's environment, and the
platform refuses a record whose environment differs from the run's — which
surfaces as a run with no usable evidence rather than as an error naming the
cause.

`dev` never *attaches* an SDK. It configures one that already exists, or it
stops — see
[ADR 0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md) for
why absence of detectable instrumentation must not select injection, and
[ADR 0042](adr/0042-dev-composes-the-collector-rather-than-owning-a-receiver.md)
for why the CLI supervises this processor instead of opening its own receiver.

## Agent-oriented conventions — implemented

Since [task 075](tasks/v1.0/075-ai-semantic-telemetry-normalization.md),
Trustvian reads agent-oriented telemetry when a producer emits it. Both
conventions are read by **one** table, `internal/semconv`, which both
`internal/otel` and the Collector processor call — so the two adapters cannot
drift into two conventions.

```text
telemetry says                               Trustvian records
──────────────────────────────────────────   ────────────────────────────────────
POST → export.localhost                      http · POST /v1/export → export.localhost
gen_ai.operation.name=execute_tool           tool · export_customer → export.localhost
  + gen_ai.tool.name=export_customer
```

### What is read

**OpenTelemetry GenAI**, verified against
`open-telemetry/semantic-conventions-genai` at commit `e57c543b4889`:

| `gen_ai.operation.name` | Category | `Operation.Name` | `Target.Name` |
|---|---|---|---|
| `execute_tool` | `tool` | `gen_ai.tool.name` | the transport target, if any |
| `invoke_agent`, `create_agent` | `tool` | `gen_ai.agent.name` | the transport target, if any |
| `invoke_workflow`, `plan` | `tool` | `gen_ai.agent.name`, else the operation | the transport target, if any |
| `chat`, `text_completion`, `generate_content`, `embeddings` | `external` | `gen_ai.request.model` | the provider |
| `retrieval` | `external` | `gen_ai.data_source.id`, else `retrieval` | `gen_ai.data_source.id` |

`gen_ai.conversation.id` becomes `Context.SessionID`. The provider is
`gen_ai.provider.name`, falling back to `gen_ai.system` — which appears nowhere in
the current convention and is read **only** because producers pinned to an older
one still emit it.

**OpenInference**, verified against `Arize-ai/openinference` at commit
`300bba9191bf`:

| `openinference.span.kind` | Category | `Operation.Name` | `Target.Name` |
|---|---|---|---|
| `TOOL` | `tool` | `tool.name` | the transport target, if any |
| `AGENT` | `tool` | `agent.name` | the transport target, if any |
| `LLM`, `EMBEDDING` | `external` | `llm.model_name` | `llm.provider`, else `llm.system` |
| `RETRIEVER` | `external` | `retriever` | the transport target, if any |
| `RERANKER` | `external` | `reranker.model_name` | the transport target, if any |

`session.id` becomes `Context.SessionID`. `CHAIN`, `GUARDRAIL`, `EVALUATOR` and
`PROMPT` are deliberately unmapped — `CHAIN` because its own spec calls it the glue
code between steps, so it names no operation the actor performed; the other three
because the spec defines no identity attribute for them.

**`tool.name` is read only on a `TOOL` span.** The same key appears under
`llm.tools.<index>.tool.name` as an *advertised tool definition*, so reading it
bare would record a model span that merely lists its available tools as having used
one.

### What is not read

Twenty-two content attributes, enumerated in `internal/semconv/content.go`:
`gen_ai.input.messages`, `gen_ai.output.messages`,
`gen_ai.tool.call.arguments`, `gen_ai.tool.call.result`,
`gen_ai.tool.definitions`, `gen_ai.system_instructions`, `input.value`,
`output.value`, both `mime_type` keys, `llm.input_messages`,
`llm.output_messages`, both `llm.prompt_template.*` keys,
`retrieval.documents`, both `reranker.*_documents` keys, `tool.parameters`,
`input.images`, `output.images`, `metadata` and `user.id`.

A tool *name* is what the agent did; a tool *argument* is what it said. See
[Privacy](SECURITY.md) for where that boundary binds and what it does **not**
claim.

### Read for the status surface only

The Collector processor's optional `status:` block
([task 105](tasks/v0.12/105-pipeline-status-surface.md)) reads three more things,
for the developer-facing pipeline status and nowhere else:

| Read from | Used for |
|---|---|
| resource `service.name` | naming a producer on the status surface — it is already the actor fallback |
| resource `telemetry.sdk.name`, `.language`, `.version` | showing which SDK a producer runs |
| the instrumentation scope's name and version | showing which instrumentation library emitted the spans |

None of them reaches an `Event`, a `DecisionRecord`, a fingerprint, a baseline,
the ingest envelope or anything persisted. They travel in the status report,
which the control plane holds in memory only.

The model calls and fidelity counts on the same surface read nothing new. A
model call is a span whose behavioral layer is `model`, its model is the
`Operation.Name` the table above produced and its provider is the `Target.Name`.
The per-target count of distinct operations at transport fidelity counts
`Operation.Name` values without reporting them, and only for spans whose category
is `http` and whose target is named (`service.peer.name` or `server.address`). A DB
span's operation is its span name, and the RPC fallback also holds the
OpenInference kinds this table leaves unmapped (`CHAIN`, `GUARDRAIL`,
`EVALUATOR`, `PROMPT`) and internal spans with no target. Counting those would
describe an instrumented agent as "visible only as HTTP". They still count as
transport in the fidelity totals.

### Behavioral layer

Beside fidelity, and answering a different question. Fidelity says *whether*
telemetry named the operation; the layer says *what kind* of operation it named.

```text
model        the producer named a model or embedding invocation
tool         the producer named a tool, agent or workflow invocation
retrieval    the producer named a retrieval or rerank against a data source
transport    no convention named the operation, so identity came from HTTP, DB or RPC
(empty)      not classified — this Event never passed through a telemetry adapter
```

Both questions are needed, because `external · gpt-4o` and
`external · handbook` are both *semantic* and only the layer distinguishes a
model call from a document-store query.

| Where | How |
|---|---|
| outbound span attribute | `trustvian.behavior.layer`, beside the other enrichment |
| ingest envelope | optional `behavior_layer`; absent means **not classified** |
| realtime observation | always present; the empty string is a real value |
| WebUI inspector | a sentence naming the layer, and a distinct sentence for each of the two non-layer states |
| `StableFeatures` / the fingerprint | **never** |

**The layer states no direction.** `transport` is the value for an inbound
`SERVER` or `CONSUMER` span as much as for an outbound `CLIENT` or `PRODUCER`
one, and for a span whose kind establishes no direction at all — what they share
is that no convention named the operation. Direction is `Operation.Direction`,
derived from the span kind, and is a separate field: nothing may present a
`transport` behavior as an outbound request on the strength of its layer.

**Absent is not transport, and that is the one place this differs from fidelity.**
A producer that said nothing classified nothing, and calling an unclassified Event
a transport operation would assert an identity source no telemetry established.
Fidelity can safely default to `transport` because its question is "did anything
prove a semantic name", and no really is transport.

**A layer is claimed exactly when fidelity is `semantic`.** One gate, so the two
indicators cannot disagree about one span: a GenAI span whose category matched but
whose identity attribute was missing keeps its transport mapping and is classified
`transport`.

**It is never behavioral identity.** There is deliberately no `model`
operation category: `OperationCategory` is a `StableFeatures` dimension, so a
sixth value would re-fingerprint every model call a producer had already been
emitting and discard those baselines, to improve a label. See
[ADR 0047](adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md).

Like fidelity, it is **not persisted**, so a comparison delta carries no layer.

### What one act counts as

Stated because it was previously true and written down nowhere.

A producer emitting agent-oriented telemetry observes one act at two layers: the
tool call, and the request the tool made. Each is its own observation, its own
`StableFeatures` and its own fingerprint:

```text
tool  · export_customer                    layer=tool        fingerprint A
http  · POST → export.localhost         layer=transport   fingerprint B
```

So **`max_added_behaviors` counts behavioral identities, and one act observed at
two layers contributes two.** A team allowing one added behavior is allowing one
*identity*, which may be half of one act.

**A comparison now also reports how many *changes* those identities amount to.**
A counted behavioral change is an added identity that is not the recorded child
of another added identity — where an identity is such a child only when every
retained occurrence of it is — so the tool and its transport child above are two
identities and one change. Both numbers are reported, named for what they count:

```text
added_count            2      behavioral identities
added_change_count     1      counted changes
correlation_state      complete
```

Identity is still deliberately *not* folded — folding would drop the destination
from behavioral identity, so a tool that started posting somewhere else would
stop changing the behavioral surface. That case keeps working precisely because
the fold is a counting rule: the tool is present in both runs, so it is not an
*added* parent, and the new transport identity stays a change of its own.

`max_added_behaviors` still counts identities and is unchanged. See
[ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)
for the rule, the unresolved cases and the compatibility reasoning, and see
[task 083](tasks/v1.0/083-behavioral-layer-classification.md) for the full
reasoning and
[084](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary)
for the prerequisite.

Missing correlation produces the same fallback rather than a guess. **Nothing is
inferred from timestamps, adjacency or similar names**, and no transport
observation is dropped to make a count smaller.

### Correlation and operational evidence

Task 084. Three facts read from span *fields* rather than attributes, and
carried on `DecisionRecord` as named scalars.

| Field | Read from | Availability |
|---|---|---|
| `parent_span_id`, `span_lineage` | the span's own parent reference | `root` or `child`; **never** inferred from timing, adjacency, names or arrival order |
| `duration_nanos` | start and end timestamps | canonical decimal nanoseconds; `""` is unavailable and `"0"` is a measured zero |
| `span_status` | the span's status code | `unset`, `ok` or `error` |

**Neither format can say "the producer does not know".** An OTLP span encodes a
root as an all-zero parent id and the SDK returns an invalid parent context for
one, so each adapter reports `root` or `child` and never an unspecified lineage.
That state exists for an `Event` built without a span. The limit belongs to the
formats and is recorded rather than papered over.

**A child whose parent never arrives is still a child.** Nothing checks that a
named parent was received. A parent span ends *after* the children it started,
so a child arriving first is the normal case, and a sampled-away parent is
ordinary too.

**A duration that was not measured is not a duration of zero.** A span with no
end timestamp, no start timestamp, an end before its start, or an interval past
2^63 ns reports unavailable. Only a valid interval reports a measurement, and a
valid interval of zero reports `"0"`.

**Neither `unset` nor an absent status is success.** OpenTelemetry's status
defaults to `UNSET` and most instrumentation never sets `OK`, so the platform
counts the four states separately instead of computing a rate a caller would
read as an error rate over everything.

**The volatile feature bridge is unchanged**, and diverges from this path in one
documented case. `Attributes["duration_ms"]` is still written only for a strictly
positive duration and `Attributes["error"]` only for an explicit `ERROR`; both
still feed `features.Extract` alone. So a zero-duration span reaches the evidence
path and not the feature path — correct for each, and asserted by test in both
adapters rather than left to be discovered.

None of this is behavioral identity: two observations differing only in how long
they took share a fingerprint.

### Usage and HTTP status code

Task 087. Read from attributes, by the Collector processor only, and carried on
the ingest envelope beside `fidelity` and `behavior_layer` — never on
`DecisionRecord`, and never in `StableFeatures`, a fingerprint or a baseline
key. The keys are listed once, in
[`internal/semconv/usage.go`](../internal/semconv/usage.go), and a test asserts
they share nothing with the content deny-list.

| Envelope field | Read from, in precedence order | Rule |
|---|---|---|
| `tokens_input` | `gen_ai.usage.input_tokens`, `gen_ai.usage.prompt_tokens` (legacy), `llm.token_count.prompt` | an integer from 0 to 2^32 |
| `tokens_output` | `gen_ai.usage.output_tokens`, `gen_ai.usage.completion_tokens` (legacy), `llm.token_count.completion` | an integer from 0 to 2^32 |
| `tokens_unsplit` | `llm.token_count.total`, **only** when both parts are absent | an integer from 0 to 2^32; a total is never split |
| `http_status_code` | `http.response.status_code`, `http.status_code` (legacy) | an integer from 100 to 599 |

**The first key present decides.** A present value that is not an integer in
range — a string, a fraction, a negative number, a float past 2^53 — makes that
field absent, and the next key is not read: the producer stated the value and
stated it wrongly. **Absent is never `0`**: the field is omitted from the
envelope, and `0` means a measured zero.

**Token counts come from usage attributes and from nothing else.** No text is
counted. **The status code is never inferred from span status**: an `UNSET` or
`ERROR` span says nothing about HTTP.

The in-process SDK path (`internal/otel`) builds no envelope, so a run ingested
that way reports tokens and status codes as unavailable. That is the honest
outcome, not a defect.

### What is neither read nor refused

Two gaps, recorded here because "not in the table" and "deliberately excluded"
are different statements and a reader cannot tell them apart from silence. Neither
is a content attribute; both are metadata, and each has an owner. Token usage was
the third until task 087 read it — see
[Usage and HTTP status code](#usage-and-http-status-code).

| Gap | State | Owner |
|---|---|---|
| **Per-observation history** | Not retained. The record now *carries* correlation and operational evidence (task 084), and the platform aggregates it per run; storing one row per observation is task 067's, so a trace tree still cannot be reconstructed from retained evidence | [067](ROADMAP.md#milestone-sequence) |
| **Latency and error *comparison*** | The evidence is carried and aggregated per run (task 084); comparing two runs on it is a separate decision | [087](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence) |

The bridging is described under
[Why latency/error are bridged, not mapped](#mapping-table) and is unchanged: it
feeds the engine's own signals. Since task 084 a parallel *recorded* path carries
duration and status to the evidence boundary as well — see
[Correlation and operational evidence](#correlation-and-operational-evidence)
for the two readings and where they deliberately differ.

**Four OpenInference span kinds stay unmapped**, as stated above — `CHAIN`,
`GUARDRAIL`, `EVALUATOR` and `PROMPT`. Two of them are worth revisiting on their
own merits rather than for completeness: `GUARDRAIL` is behavioral security
evidence a security product could reasonably read, and `PROMPT` would matter to a
prompt reference. Both are blocked on the same thing — the convention defines no
identity attribute for either — and no task claims them today. That absence is
deliberate, and recording it is what stops it being rediscovered as an oversight.

### Precedence

```text
trustvian.* override   always wins
OpenTelemetry GenAI    wins over OpenInference when a span carries both
OpenInference          wins over transport
transport              unchanged
```

### Degradation is a requirement, not a fallback

A producer emitting no agent-oriented convention sees **byte-identical**
behavior — asserted against real spans in
`internal/otel/degradation_test.go` and end to end in
`cmd/trustvian/semantic_e2e_test.go`.

An unknown operation name, an unknown span kind, a renamed attribute, or an
attribute of the wrong type all mean "the convention is absent". A category that
matched with its identity attribute missing does **not** fire: `tool · POST` would
be a semantic category wearing a transport name, and no layer may claim a name the
telemetry did not supply.

### Fidelity

```text
transport     only protocol and target were available
semantic      an agent-oriented convention supplied the operation identity
```

Reported, never implied. It describes the *mapping result* rather than the span, so
a span carrying conventions the table declined to read is `transport`.

| Where | How |
|---|---|
| outbound span attribute | `trustvian.fidelity`, beside the other enrichment |
| ingest envelope | optional `fidelity`; absent means transport |
| realtime observation | always present, so absence never needs interpreting |
| WebUI inspector | a sentence, not a badge |
| `StableFeatures` / the fingerprint | **never** — it would reset baselines on an instrumentation upgrade |

It rides **beside** the record on the ingest envelope rather than inside it, for
the same reason `behavioral_profile` does: it describes how the record was
*produced*, and `DecisionRecord` carries no attributes at all, so this is the
only place the value can travel. Both producers fill it — a client POSTing to
`/v1` directly, and the Collector processor, which reads it at the ingest call
site from the same `Result` that produced the span attribute. The two therefore
cannot disagree about one span.

There is deliberately **no inbound `trustvian.fidelity` override**: a producer able
to claim semantic fidelity would defeat the guarantee the indicator makes. An
unrecognized value on the ingest envelope is refused with `400` rather than
degraded, because this vocabulary is Trustvian's own closed set rather than an
external convention.

**Not yet persisted per behavior**, so the comparison response's behavior deltas
do not carry it — that needs a schema step and is deferred as its own task. See
[ADR 0045](adr/0045-conventions-are-read-frameworks-are-not.md)'s consequences.

### No framework is named

Trustvian reads conventions, not frameworks. No framework appears in any type,
field or branch, and `scripts/check-platform-boundary.sh` fails the build if one
appears in non-test source. `openinference` is the convention's own published
attribute name, not a framework.

The design reasoning is
[ADR 0045](adr/0045-conventions-are-read-frameworks-are-not.md).

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
