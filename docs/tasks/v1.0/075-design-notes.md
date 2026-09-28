# 075 — Design notes

**Temporary.** Folded into the ADR and deleted before this PR merges, exactly as
task 077's design notes were. It exists so the decisions below can be reviewed
*before* code, not reconstructed from a diff afterwards.

Written against `main` at `5362f51`. Conventions verified live rather than from
memory; every version and commit is recorded in §2.

---

## What the reading changed

Four things I expected to be true and checked, three of which came out
differently:

1. **The Go `semconv` package cannot supply the GenAI keys.** I assumed
   `go.opentelemetry.io/otel/semconv/v1.43.0` — which both adapters already
   import — would carry `gen_ai.*`, making key constants free. It does not, and
   the history is worse than absence:

   ```text
   semconv/v1.39.0   genaiconv/ present   41 GenAI attribute keys
   semconv/v1.40.0   genaiconv/ present   46
   semconv/v1.41.0   genaiconv/ present   50
   semconv/v1.42.0   genaiconv/ GONE       0
   semconv/v1.43.0   genaiconv/ GONE       0      ← the version both adapters pin
   ```

   Measured with `grep -c 'GenAI.*Key = attribute' attribute_group.go` across the
   module cache. The keys grew for three releases and then vanished. Whatever the
   upstream reason, **the version this repository pins exports none of them**, so
   the attribute names have to be string literals in Trustvian's own source no
   matter which package holds the table. That removes the tension I expected
   between "share one table" and "import no OTel package" — see §1.

2. **`gen_ai.system` no longer exists upstream.** The brief asks to support
   `gen_ai.provider.name` "(formerly `gen_ai.system` — support both)", and task
   075's own *What is read* table still lists `gen_ai.system` as the provider
   signal. Checked against the current registry: `gen_ai.system` appears **nowhere**
   — not in `model/gen-ai/registry.yaml`, not in a deprecated registry (there is no
   `docs/registry/deprecated/` directory in that repository at all). The current
   provider attribute is `gen_ai.provider.name`.

   Supporting both is still right, but for a different reason than the brief
   gives: **producers lag the spec.** An SDK pinned to a 2025 convention still
   emits `gen_ai.system`, and version tolerance is a stated requirement of this
   task. So `gen_ai.system` is read as a *legacy alias*, documented as such, and
   not as a currently-specified attribute. Task 075's table should be corrected
   in the same PR.

   Separately: `gen_ai.system_instructions` is a **different** attribute — Opt-In
   content, the system prompt. Conflating the two would read a prompt as a
   provider name. It is on the content deny-list in §2.

3. **Every GenAI content attribute carries an explicit upstream sensitivity
   warning.** Not an inference — the registry says so for all six:
   `gen_ai.input.messages`, `gen_ai.output.messages`, `gen_ai.tool.call.arguments`,
   `gen_ai.tool.call.result`, `gen_ai.tool.definitions`,
   `gen_ai.system_instructions`. That makes the deny-list in §2 quotable rather
   than asserted.

4. **`tool.name` in OpenInference is ambiguous without the span kind.** It means
   the invoked tool on a `TOOL` span, but the same key appears under
   `llm.tools.<index>.tool.name` as an *advertised tool definition* on an `LLM`
   span. Reading a bare `tool.name` would therefore let a model span that merely
   *lists* available tools be recorded as *having used* one — a fabrication, and
   exactly what acceptance criterion 4 forbids. So OpenInference tool identity is
   read **only when the span kind is `TOOL`**. This is the single subtlest rule in
   the table.

The fourth thing I checked and which held: **the core's dependency graph has zero
OTel packages today.** `GOWORK=off go list -deps` over `event`,
`internal/features` … `internal/policy`, `internal/store` and the root package
reports `0` matches for `opentelemetry`; `internal/otel` alone reports `31`. That
is the number slice 2 must leave unchanged.

---

## 1. Where the mapping lives

**A new pure package in the root module, `internal/semconv`, imported by both
adapters — with the processor reaching it through a thin re-export.**

### The shape

```go
// internal/semconv — no OpenTelemetry import, by construction.
package semconv

// Span is the adapter-independent input: plain Go values only.
type Span struct {
    Kind          Kind              // client/server/producer/consumer/internal
    Name          string
    Attributes    map[string]any
    Resource      map[string]any
}

// Normalized is what the table could establish. Every field is optional;
// the zero value means "the telemetry did not say".
type Normalized struct {
    OperationCategory string   // "" when unestablished
    OperationName     string
    TargetName        string
    ActorType         string
    SessionID         string
    Fidelity          Fidelity // transport | semantic
}

func Normalize(s Span) Normalized
```

Plain `string` rather than `event.OperationCategory` at the boundary is
deliberate: it keeps this package's signature free of any opinion about which
core types exist, and the two callers convert. `Normalize` is pure — no clock, no
I/O, no map mutation — so it is table-driven testable with no fixtures at all.

### Why the confinement rule holds

`.claude/rules/go.md` confines `go.opentelemetry.io/otel{,/sdk,/trace}` to
`internal/otel`. Because §0.1 established that the GenAI keys are **not** in the
pinned `semconv` package, this package needs no OTel import to name them — the
attribute names are string literals, which is what they would have had to be
anyway. `Kind` is Trustvian's own five-value enum, not `trace.SpanKind`.

So the rule is satisfied without an exception, and **the confinement table needs
no fourth row**. That is the outcome to prefer: the brief said "do not widen
internal/otel's OTel import to a second package", and this does not widen
anything.

### Internal, not public — and how the processor reaches it

ADR 0002's bar for a public package is *a real external consumer*, and it warns
that promoting speculatively is a one-way door. `trustvian-processor` is a
separate Go module, so it cannot import `internal/semconv` directly. Three ways
out, and I recommend the third:

| | |
|---|---|
| Make it a public `semconv` package | Fails ADR 0002's bar today. The processor is *repository-internal* (`check-modules.sh` asserts its module path is not resolvable and not published), so it is not the "external consumer" the ADR means. And `semconv` as a public name collides conceptually with OTel's own. |
| Duplicate the table in the processor | What the brief forbids, and rightly: `processor/mapping.go` already documents its duplication of `EventFromSpan` as forced by incompatible span types. A *table* is not forced by anything — the input is a plain map on both sides. |
| **Re-export from the existing public `event` package** | Recommended. |

The third works because the processor **already imports
`github.com/trustvian/trustvian/event`** — it is one of the two packages ADR 0002
makes public, and `processor/mapping.go` constructs `event.Event` values from it
today. So:

```text
internal/semconv          the table, pure, unexported to the world
    ↑                ↑
internal/otel      event.NormalizeSpan(...)   ← thin re-export, ~10 lines
                       ↑
              processor/mapping.go
```

`event.NormalizeSpan` is a pure delegation with its own doc comment; it adds one
public function and two public types (`event.SpanKind`, `event.Normalized`) to a
package that already exists for exactly this reason — *"the one type every caller
must construct just to call the engine"*. The processor is a real consumer, in
this repository, today. That satisfies ADR 0002's bar as written rather than by
stretching it.

**Open for your call:** if you would rather not grow `event`'s public surface at
all, the alternative is a fourth top-level public package (`normalize`). I prefer
re-export because `event` is already the module's telemetry-facing vocabulary and
a second public package is the ceremony CLAUDE.md warns against — but it is your
API surface, and this is the one decision here that is not reversible cheaply.

---

## 2. The exact attribute set, verified

### OpenTelemetry GenAI

Repository `open-telemetry/semantic-conventions-genai`, **HEAD `e57c543b4889`,
committed 2026-09-24**. Read from `model/gen-ai/registry.yaml` and
`docs/gen-ai/gen-ai-spans.md`.

Every attribute below is stability **`development`** — there is no stable GenAI
attribute. That is the version-tolerance argument in one word, and it is why
§0.1's disappearing-keys history matters.

| Attribute | Level | Read as |
|---|---|---|
| `gen_ai.operation.name` | Required | the operation discriminator |
| `gen_ai.tool.name` | Development | `Operation.Name` for `execute_tool` |
| `gen_ai.tool.call.id` | Development | **not read** — per-call, would destroy fingerprint stability |
| `gen_ai.tool.type` | Development | not read (see §3 note) |
| `gen_ai.agent.name` | Development | `Operation.Name` for agent operations; actor evidence |
| `gen_ai.agent.id` | Development | actor evidence only (§5) |
| `gen_ai.request.model` | Cond. required | `Operation.Name` for inference operations |
| `gen_ai.provider.name` | Required | `Target.Name` for inference operations |
| `gen_ai.system` | **absent from the spec** | legacy alias for `provider.name` (§0.2) |
| `gen_ai.conversation.id` | Cond. required | `Context.SessionID` |
| `gen_ai.data_source.id` | Development | `Target.Name` for `retrieval` |

`gen_ai.operation.name`'s full enum, as it stands: `chat`, `create_agent`,
`create_memory`, `create_memory_store`, `delete_memory`, `delete_memory_store`,
`embeddings`, `execute_tool`, `fetch_response`, `generate_content`,
`invoke_agent`, `invoke_workflow`, `plan`, `retrieval`, `search_memory`,
`text_completion`, `update_memory`, `upsert_memory`.

Eighteen values. The table in §3 maps six of them and treats the rest as
**unknown**, which by §3's rule means the convention is absent for that span. That
is the version-tolerance property: a nineteenth value added upstream degrades to
transport rather than breaking.

### OpenInference

Repository `Arize-ai/openinference`, **HEAD `300bba9191bf`, committed
2026-09-25**. Read from `spec/semantic_conventions.md`.

| Attribute | Read as |
|---|---|
| `openinference.span.kind` | **required by that spec** on every OpenInference span; the discriminator |
| `tool.name` | `Operation.Name`, **only when span kind is `TOOL`** (§0.4) |
| `llm.model_name` | `Operation.Name` for `LLM` / `EMBEDDING` |
| `llm.provider` | `Target.Name` for `LLM` / `EMBEDDING` |
| `llm.system` | fallback for `llm.provider` — both are defined, unlike GenAI |
| `session.id` | `Context.SessionID` |
| `agent.name` | `Operation.Name` for `AGENT`; actor evidence. **Defined** — the brief asked |
| `reranker.model_name` | `Operation.Name` for `RERANKER` |

Span kinds, verbatim: `LLM`, `EMBEDDING`, `CHAIN`, `RETRIEVER`, `RERANKER`,
`TOOL`, `AGENT`, `GUARDRAIL`, `EVALUATOR`, `PROMPT`. Ten; §3 maps six.

### Content attributes — never identity, never durable

Read by nothing in the table. The GenAI six each carry an upstream sensitivity
warning in the registry itself, which is worth quoting in the ADR rather than
paraphrasing:

```text
GenAI            gen_ai.input.messages          Opt-In · "Likely to contain
                 gen_ai.output.messages                   sensitive information
                 gen_ai.tool.call.arguments               including user/PII data"
                 gen_ai.tool.call.result
                 gen_ai.tool.definitions
                 gen_ai.system_instructions

OpenInference    input.value · output.value
                 input.mime_type · output.mime_type
                 llm.input_messages · llm.output_messages
                 llm.prompt_template.template · llm.prompt_template.variables
                 retrieval.documents · reranker.input_documents
                 reranker.output_documents
                 tool.parameters
                 input.images · output.images
                 metadata
                 user.id
```

`user.id` is on the list deliberately even though it is an identifier rather than
a payload: it is a *human's* identifier, it is not a behavioral dimension, and
`Actor.ID` is the service, not the end user. `metadata` is a free-form JSON blob
and therefore content by construction. The two `mime_type` keys are on the list
because they describe content and nothing else.

Slice 3 feeds a span carrying **every** key above through the whole pipeline and
asserts each distinctive value is absent from the enforcing layers.

---

## 3. The mapping table

Read in this order; the first row that matches wins. "Unestablished" means the
field is left exactly as today's mapping would leave it.

### Precedence, outermost first

```text
1. trustvian.* explicit overrides        always win               → §7
2. OpenTelemetry GenAI                   gen_ai.operation.name present
3. OpenInference                         openinference.span.kind present
4. today's transport mapping             unchanged                → fidelity=transport
```

**GenAI before OpenInference** when a span carries both. Reasons, in order of
weight: GenAI is the vendor-neutral OpenTelemetry convention and the one this
project's own docs already discuss; `gen_ai.operation.name` is *Required* in its
spec where `openinference.span.kind` is required in a vendor's; and a span
carrying both is almost always an OpenInference producer that also emits GenAI,
so preferring GenAI picks the more portable reading. This is a coin-flip decision
made explicit rather than left to map iteration order — it will be a named test.

### GenAI rows

| `gen_ai.operation.name` | Category | Operation.Name | Target.Name | Fidelity |
|---|---|---|---|---|
| `execute_tool` | `tool` | `gen_ai.tool.name` | §4 | semantic |
| `invoke_agent`, `create_agent` | `tool` | `gen_ai.agent.name` | §4 | semantic |
| `invoke_workflow`, `plan` | `tool` | `gen_ai.agent.name`, else the operation name | §4 | semantic |
| `chat`, `text_completion`, `generate_content` | `external` | `gen_ai.request.model` | provider | semantic |
| `embeddings` | `external` | `gen_ai.request.model` | provider | semantic |
| `retrieval` | `external` | `gen_ai.data_source.id`, else `retrieval` | `gen_ai.data_source.id` | semantic |
| anything else, or the attribute absent | — | — | — | *convention absent* |

`Context.SessionID` ← `gen_ai.conversation.id` on every GenAI row.
Provider ← `gen_ai.provider.name`, else `gen_ai.system`.

**Why `tool` and not a new category for agents and workflows.** `OperationCategory`
is an open vocabulary with five members, and `tool` is the one that means "the
agent did a thing rather than spoke to a model". Adding `agent` or `workflow`
members would be a **core change**, which this task must not make. `external` for
inference is the honest pick: a model call leaves the process to a third party,
which is what `external` already means; using `tool` for it would make "a tool
was used" and "a model was consulted" indistinguishable, and the whole point is
to tell them apart.

**Empty `Operation.Name` is not a semantic match.** If `gen_ai.operation.name` is
`execute_tool` but `gen_ai.tool.name` is missing or not a string, the row does not
fire: the result is `transport` fidelity and today's mapping. A semantic category
with a transport-derived name (the span name) would be the fabrication criterion
4 forbids — it would read `tool · POST` in a UI. This is the malformed-attribute
rule the brief asks for, and it is one branch, not a special case per row.

### OpenInference rows

| `openinference.span.kind` | Category | Operation.Name | Target.Name | Fidelity |
|---|---|---|---|---|
| `TOOL` | `tool` | `tool.name` | §4 | semantic |
| `AGENT` | `tool` | `agent.name` | §4 | semantic |
| `LLM` | `external` | `llm.model_name` | `llm.provider`, else `llm.system` | semantic |
| `EMBEDDING` | `external` | `llm.model_name` | provider | semantic |
| `RETRIEVER` | `external` | `retriever` | unestablished | semantic |
| `RERANKER` | `external` | `reranker.model_name` | unestablished | semantic |
| `CHAIN`, `GUARDRAIL`, `EVALUATOR`, `PROMPT` | — | — | — | *convention absent* |
| anything else | — | — | — | *convention absent* |

`Context.SessionID` ← `session.id`.

**`CHAIN` is deliberately not mapped.** Its own spec calls it "a starting point or
a link between different LLM application steps … the glue code". It names no
operation the agent performed, so mapping it would invent a behavior out of
control flow. `GUARDRAIL`, `EVALUATOR` and `PROMPT` are unmapped for the
narrower reason that none has an identity attribute defined for it in the spec —
if one gains one, a row is added then.

**Unknown kind does nothing**, per the brief: no category, no name, no fidelity
upgrade. Not an error, not a log line, not a metric — the convention is simply
absent for that span, which is the same code path as a span with no
`openinference.span.kind` at all.

---

## 4. `Target.Name` for tool and model spans

This is the decision most likely to be wrong in a way that only shows up as a
churning baseline, so it is reasoned from fingerprint stability rather than from
what reads nicely.

`StableFeatures` is six dimensions, of which this task can affect four:
`OperationCategory`, `OperationName`, `TargetName`, `TargetCategory`. A
fingerprint must be **the same across runs for the same logical behavior**.

### Model and retrieval spans: the provider

`gen_ai.provider.name` (`openai`, `anthropic`, `aws.bedrock`) and `llm.provider`
are stable per deployment and low-cardinality. `Target.Name = provider` reads as
`external · gpt-4o → openai`, which is exactly the right shape: *what* was
consulted, and *whose*. For retrieval, `gen_ai.data_source.id` is the same kind of
value. No decision needed here.

### Tool and agent spans: prefer the existing transport target, else empty

A `TOOL` span usually names no `server.address`. Four candidates:

| Candidate | Rejected because |
|---|---|
| the tool name itself | `Target.Name == Operation.Name` is a tautology that carries no information, and it makes `tool · export_customer → export_customer` — noise in every UI and every diff row |
| the agent's own name | The target would then be the *actor*, which inverts the dimension's meaning. Every one of an agent's tool calls would share one target, collapsing the dimension to a constant |
| the service name from the resource | Same objection, one step removed: that is `Actor.ID` |
| **whatever the transport mapping already found, else empty** | **Chosen** |

So: if the span *also* carries `server.address` or a peer service name — which a
tool span wrapping an HTTP call very often does — that value is kept, giving
`tool · export_customer → export.localhost`, the single richest row this task can
produce and the exact example in task 075's own objective. If it carries nothing,
`Target.Name` stays **empty**.

**Empty is safe, and this is the part worth checking.** `Target.Name` is already
optional: today's mapping leaves it empty whenever a span has no peer, DB
namespace or server address, and `event.Validate` does not require it. An empty
target is stable across runs by definition, so the fingerprint is stable. What it
costs is one dimension of discrimination for pure tool spans — two different tools
are still distinguished by `Operation.Name`, which is the dimension that got
*better*. That is an acceptable trade and it is honest: the telemetry did not say
what the target was, so Trustvian does not either.

---

## 5. `Actor.Type` upgrade

**Keep the conservative reading the task assumes, and define "establishes" as one
of exactly three attributes.**

`Actor.Type` becomes `ai_agent` only when the span carries one of:

```text
gen_ai.agent.id        an agent identity, from the OTel convention
gen_ai.agent.name
agent.name            an agent identity, from OpenInference
```

Not on the mere presence of `gen_ai.operation.name` or
`openinference.span.kind`. The reason is that those say *what kind of operation
this was*, not *what kind of actor performed it* — a plain backend service calling
an LLM through an instrumented client library emits `gen_ai.operation.name=chat`
and is not an AI agent. Upgrading it would relabel an ordinary service.

And the cost of getting it wrong is not cosmetic: `ActorType` **is** a
`StableFeatures` dimension, so flipping it changes every fingerprint for that
actor and discards its learned baseline. A rule that fires on a client library's
presence would silently reset baselines the first time a service adopted one.

`trustvian.actor.type` continues to override, as §7 requires. A producer that
knows it is an agent and emits no agent-name attribute can still say so
explicitly — which is the escape hatch that makes the conservative default
affordable.

---

## 6. Fidelity

```text
type Fidelity string
const (
    FidelityTransport Fidelity = "transport"   // protocol and target only
    FidelitySemantic  Fidelity = "semantic"    // a convention supplied the identity
)
```

Two values, closed. Not a float, not free text, not a per-dimension matrix — task
075 forbids the first two and the third is a shape nobody asked for.

**It describes the mapping, not the span.** A span carrying GenAI attributes that
the table declines to read (unknown operation, missing tool name) is `transport`,
because transport is what Trustvian actually derived the behavior from. Fidelity
answers *"where did this name come from"*, and the answer must be about the
result.

### Where it is carried

| Surface | Carried | Why |
|---|---|---|
| Outbound span attribute `trustvian.fidelity` | **yes** | Sits beside the existing `trustvian.*` enrichment, so a trace backend shows it. Interoperability, per the task's fan-out scenario |
| `event.Event` | **no field** | A core change, which this task must not make. It travels in `Event.Attributes` under `trustvian.fidelity`, which is where the adapter already puts derived values |
| `StableFeatures` / fingerprint | **never** | Fidelity is metadata about the derivation. Folding it in would make the same logical behavior fingerprint differently before and after a producer upgraded its instrumentation — a baseline reset disguised as a new behavior |
| `DecisionRecord` | **yes, as a new optional field** | It *is* metadata, of exactly the fixed-shape kind that type carries, and a consumer cannot otherwise tell a tool name from a guess. Platform-side, so not a core change |
| `/v1` compare deltas | **yes, optional** | Slice 4. See the note below about the behaviors route |
| WebUI | **yes, shown** | "Reported rather than implied" is acceptance criterion 9 |

**Slice 4 targets the compare deltas, not a behaviors route.** Task 078's first
slice — `GET /v1/evaluation-runs/{run_id}/behaviors` — is specified only in PR
#112, which is **open, not merged**. So the route does not exist and slice 4 uses
the compare response's `behavior_diff.deltas`, which is the brief's stated
fallback. If #112 merges and the route lands first, the field moves there too;
the shape is identical either way.

### Compatibility

Additive everywhere. `docs/compatibility.md` gains rows **when the fields exist**,
not in advance — the task says so explicitly. Classification:

```text
trustvian.fidelity span attribute      OPERATIONALLY STABLE   (like the other trustvian.* enrichment)
DecisionRecord.Fidelity                STABLE, optional       (omitempty; absent means transport)
/v1 delta fidelity field               STABLE, optional       (additive response field)
```

`omitempty` with "absent means transport" is what keeps an old producer, an old
Collector and a new control plane mutually compatible without a version
negotiation.

---

## 7. Precedence over `trustvian.*` overrides

**Explicit overrides keep winning, unconditionally.** The order is §3's:
`trustvian.*` first, then the conventions, then transport.

Today's overrides are `trustvian.actor.id`, `trustvian.actor.type`,
`trustvian.identity.confidence`, `trustvian.operation.category`. Each stays
absolute. Two consequences worth stating because they are where a reader would
expect a special case:

- `trustvian.operation.category = http` on a span with
  `gen_ai.operation.name = execute_tool` yields category `http`. The override is
  an operator statement about their own telemetry and outranks a convention
  reading.
- **Fidelity still reflects what the table read.** An override that sets the
  category does not make the behavior `semantic`, and does not make it
  `transport` either if a convention legitimately supplied `Operation.Name`.
  Fidelity describes the *name*'s provenance, so it is computed from the table's
  result, not from whether an override fired.

There is deliberately **no** `trustvian.fidelity` inbound override. A producer
being able to *claim* semantic fidelity would defeat the one thing the indicator
exists to guarantee.

---

## 8. Slice plan

| # | Files | Gates |
|---|---|---|
| 1 | `internal/semconv/{semconv,table,fidelity}.go` + `_test.go`; `event/normalize.go` (re-export) | `make check`, table-driven tests, byte-for-byte degradation against `internal/otel`'s existing fixtures |
| 2 | `internal/otel/otel.go`, `processor/mapping.go`, `processor/attributes.go` (outbound `trustvian.fidelity`), `scripts/check-platform-boundary.sh` (+ framework-name scan) | all four gates; `go list -deps` still `0` for the core; processor + platform with `GOWORK=off` |
| 3 | `internal/otel/privacy_test.go`, `processor/privacy_endtoend_test.go`, `platform/…` layer assertions | all four gates |
| 4 | `platform/httpapi/dto.go` + handler, `platform/webui/…`, `decision_record.go`-side field | all four gates |
| 5 | `internal/otel/testdata/`, a Go fixture producer under `processor/cmd/` or `testdata/`, the docs list, one ADR, delete these notes | all four gates + a real `trustvian dev` run |

### Nothing here needs a core change or a new dependency

Checked explicitly, because the brief asks:

- **No core change.** `event.OperationCategoryTool`, `event.ActorTypeAIAgent` and
  `event.Context.SessionID` already exist. The one thing I would otherwise have
  wanted — an `agent` or `workflow` operation category — is deliberately mapped
  onto `tool` instead (§3), and `Fidelity` deliberately does **not** become an
  `Event` field (§6). `event/normalize.go` adds a public function to an existing
  public package; it changes no existing type.
- **No new dependency**, in any module. `internal/semconv` is standard library
  only. The fixture producer in slice 5 uses the OTel SDK, which `internal/otel`
  already depends on — no new module requirement.

### The one thing I want your decision on

§1's last paragraph: re-exporting through `event` adds one public function and two
public types to the module's public API, versus a fourth public top-level package.
I recommend the re-export. It is the only choice in this design that is expensive
to reverse.
