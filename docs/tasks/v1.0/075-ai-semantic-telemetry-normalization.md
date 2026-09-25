# 075 — AI Semantic Telemetry Normalization

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [051](051-behavioral-profile-learning-scope-isolation.md),
[073](073-otel-collector-evaluation-ingest.md)
Blocks: [072](README.md) — the OSS `v1.0` release gate;
[076](076-behavioral-evidence-explorer.md)

## Objective

Let Trustvian understand agent-oriented OpenTelemetry when a producer emits
it, **without introducing an agent-specific behavioral engine**.

```text
telemetry says                      Trustvian records today   and should record
────────────────────────────────    ──────────────────────    ─────────────────
POST /v1/export  →  export.localhost   http · POST · export.localhost   unchanged
span kind TOOL, tool.name=export_customer   http · POST · …    tool · export_customer · export-service
```

The second row is the whole task. Everything after normalization is unchanged:

```text
Event → Features → Fingerprint → Baseline → Anomaly → Trust → Policy → Decision
```

One engine, one pipeline, one set of behavioral dimensions. An AI agent is an
actor like any other, and this task adds no branch that asks whether it is one.

## Why

**Generic instrumentation flattens an agent's behavior into its transport.**
The reference demo drives Ollama, a CRM, a knowledge service, an exporter and
a mailer. Zero-code HTTP instrumentation reduces all five to `POST` and `GET`
against hostnames. Trustvian then learns a baseline over *transport shapes*,
which is correct but coarse: "this agent called a new host" is a weaker
statement than "this agent used a tool it has never used", and the second is
the one a developer acts on.

**The information is already on the wire, and already documented as future
work.** `docs/OPENTELEMETRY.md` carries a section titled *"Potential AI-agent
(GenAI) mappings — documented, not implemented"*, which states the position
precisely: `internal/otel.EventFromSpan` reads no `gen_ai.*` attribute, because
hard-coding an unstable external convention "would risk a breaking upstream
rename reaching into Trustvian's own `Event` semantics for no current consumer
need."

Task 073 and the reference demo are that consumer. The convention has also
moved: `gen_ai.*` has stabilized considerably, and OpenInference is in wide
use by the agent frameworks developers actually run.

**The core already has the vocabulary.** This is the fact that makes the task
small:

| Needed | Already exists |
|---|---|
| a tool operation category | `event.OperationCategoryTool` |
| an AI actor type | `event.ActorTypeAIAgent` |
| session correlation | `event.Context.SessionID` |
| delegation | `event.Context.DelegatedFrom` |
| trace correlation | `event.Context.TraceID`, `SpanID` |

`v0.7` added the agent-shaped fields and `v0.4` made `Operation.Category` an
open vocabulary. **No core change is required**, and this task must not make
one: a new core field needs a generic behavioral justification independent of
telemetry convenience.

## Current gap, verified against `main`

| Claim | Verified |
|---|---|
| `internal/otel.EventFromSpan` reads no `gen_ai.*` or OpenInference attribute | **True** — documented as deliberate in `docs/OPENTELEMETRY.md` |
| `event.OperationCategoryTool` exists and is usable today | **True** |
| `event.ActorTypeAIAgent` exists | **True** |
| `Context.SessionID`, `TraceID`, `SpanID`, `DelegatedFrom` exist and reach `DecisionRecord` | **True** |
| The core imports no OpenTelemetry package | **True** — ADR 0003 and the boundary script both enforce it |
| The Collector processor is the platform-facing OTel adapter | **True** — task 073 |

Nothing about this task requires a new concept. It requires a mapping.

## Scope

- A **semantic normalization layer** at the telemetry boundary — the existing
  adapter surface, not a new engine — that reads stabilized agent-oriented
  attributes and produces a better-populated `event.Event`.
- Version-tolerant support for the conventions producers actually emit:
  OpenTelemetry GenAI and OpenInference.
- An explicit **fidelity** notion, so a consumer can tell what the telemetry
  proved from what it did not.
- Unchanged fan-out: everything downstream of the Collector keeps working.

## Non-goals

**No framework dependency, ever.** No import of, or special case for,
LangChain, LangGraph, CrewAI, AutoGen, the OpenAI Agents SDK or LlamaIndex.
They are telemetry producers. Trustvian reads conventions, not frameworks, and
a framework name must not appear in any Trustvian type, field or branch.

**No new engine.** No `AgentEngine`, `LLMEngine`, `OpenInferenceEngine` or
AI-specific anomaly path. The same pipeline scores an AI agent and a payment
service.

**No core change.** `event`, `internal/features`, `internal/fingerprint` and
everything downstream are untouched. If this task believes it needs a core
field, that is a separate proposal with a generic justification.

**No content at the durable layer.** See
[Privacy](#privacy-where-the-boundary-actually-is), which states precisely
where that boundary binds and why it is not a claim about the transient
`Event.Attributes` map.

**No fabrication.** See [Fidelity](#fidelity-evidence-only).

**No prompt management, playground, judge, hallucination score, RAG relevance
metric, model benchmark or dataset semantics.** This task reads spans; it does
not evaluate answers.

## Normalization

### What is read

The task should support, version-tolerantly, the signals a producer may emit.
Illustrative rather than final — the implementation must check each against the
convention as it stands when the work begins, and must tolerate both the
presence and the absence of every one:

```text
span kind / operation      AGENT · LLM · TOOL · CHAIN · RETRIEVER · GUARDRAIL
agent identity             agent.name
tool identity              tool.name, gen_ai.tool.name
model identity             llm.model_name, gen_ai.request.model
provider                   gen_ai.system
operation                  gen_ai.operation.name
conversation               gen_ai.conversation.id
```

### What it becomes

```text
span kind TOOL + tool.name=export_customer
        ↓
Operation.Category = tool
Operation.Name     = export_customer
Target.Name        = the service the span already identifies
Actor.Type         = ai_agent        (when the producer establishes it)
Context.SessionID  = the conversation identifier, when present
```

A model call becomes an operation naming the model rather than an HTTP POST to
a hostname. A retriever call becomes an operation naming the retriever. The
fingerprint dimensions do not change; what changes is how well they are
filled.

### Fidelity: evidence only

**Trustvian must never fabricate a semantic name it was not given.** If the
telemetry proves only:

```text
POST → export.localhost
```

then the behavior is `http · POST · export.localhost`, and no layer may
upgrade it to `export_customer`. The demo's own value depends on this: an
operator seeing `export_customer` must be able to trust that the agent's
instrumentation actually said so.

The task should therefore specify a **fidelity indicator** — a bounded,
closed-vocabulary statement of how much semantic information a behavior was
derived from:

```text
transport     only protocol and target were available
semantic      an agent-oriented convention supplied the operation identity
```

Where it is carried, and whether it reaches `/v1`, is an implementation
decision this task must settle; what it must not be is free text or a
confidence float. Its purpose is to let a UI say *"this is what your telemetry
told us"* rather than implying a fidelity the producer never provided.

**Graceful degradation is a requirement, not a fallback.** A workload with
plain HTTP instrumentation must keep producing exactly the behavior it
produces today, with no regression and no empty fields. The mapping is
strictly additive: it fills dimensions that would otherwise be coarser.

## Privacy: where the boundary actually is

**Behavioral observability does not require content observability.** That is
the principle. Stating where it binds is the part a specification has to get
right, because the naive version of it is false today.

### What is true on `main`

`internal/otel`'s package comment says it plainly: *"Every span attribute —
mapped or not — is preserved in `Event.Attributes`, so nothing is silently
dropped."* The Collector processor does the same. So if a producer emits a
prompt as a span attribute, that string **is** in the in-process
`Event.Attributes` map today, before this task exists and regardless of it.

A specification that promised prompts never reach `Event` would be
contradicting the adapter it is written against.

### The layered contract

The guarantee Trustvian makes is a **durable and public evidence boundary**,
not a claim that a transient in-process map is empty:

```text
OTel span
    ↓
adapter
    Event.Attributes — transient adapter and core input
        MAY carry producer attributes, as it does today
        MUST NOT become behavioral identity merely by existing
        MUST NOT become durable or public evidence by default
    ↓
Features / Fingerprint
    only the approved behavioral dimensions, plus the explicitly consumed
    volatile keys — today exactly `duration_ms` and `error`
    ↓
DecisionRecord
    fixed-shape metadata. No attribute map, by construction
    ↓
Realtime · platform persistence · WebUI
    no arbitrary attributes, prompts, completions, arguments, results or bodies
```

Every layer below the adapter is already enforced. `StableFeatures` carries
six dimensions — actor type, operation category and name, target name and
category, environment — and no attribute reaches it. `DecisionRecord`'s own
doc comment states that *"`Event.Attributes`, tool arguments, prompts, and
completions have no field here and cannot leak into its JSON."*
`realtimeObservationDTO` carries the bounded projection and a test asserts a
distinctive attribute value never appears.

### What this task may and may not do

**May**: read specific semantic *identity* attributes out of that transient
map — a tool name, a model name, an agent name, a conversation identifier —
and use them to populate `Actor`, `Operation`, `Target` and `Context`. That is
what normalization *is*, and those values are behavioral dimensions.

**Must not**: cause any content-like attribute to become `StableFeatures`,
fingerprint identity, a `DecisionRecord` field, a `RealtimeObservation` field,
a persisted row or a WebUI payload. A tool *name* is what the agent did; a
tool *argument* is what it said, and routinely carries customer data.

The distinction is the whole design, and it is enforced at the boundary that
can enforce it.

### Adapter sanitization is a separate decision

A future task may decide the adapter should strip or allowlist attributes
before they reach `Event.Attributes` at all. That would be a **deliberate
compatibility change** — today's behavior is documented as "nothing is
silently dropped", and a consumer may rely on it — and it needs its own
review, its own migration note and its own compatibility row.

**It is not a hidden requirement of this task.** Task 075 changes what
Trustvian *reads* from the map, not what the adapter *puts* in it.

### Do not import an observability data model

OpenInference is designed to carry prompts, completions and retrieved
documents, because that is what a trace viewer needs. This task reads the
*identity* fields of that convention and ignores the content fields. Adopting
the model wholesale would make Trustvian a content store by accident — and it
would do so at the durable layer, which is the one that matters.

## Architecture

The mapping belongs at the **telemetry normalization boundary** and nowhere
else:

```text
OTLP span
    ↓
semantic normalization        ← this task
    ↓
event.Event
    ↓
Engine.Analyze                ← unchanged
```

Two existing adapter surfaces exist, and the task must choose between them or
serve both: `internal/otel.EventFromSpan` in the root module, and the
Collector processor in `trustvian-processor`. The choice is an implementation
decision with one binding constraint: **the core must remain unaware of
OpenTelemetry, OpenInference and GenAI conventions**, as ADR 0003 and the
existing boundary script require. `go list -deps` must continue to report zero
OTel packages in the core's graph.

Version tolerance is a design requirement, not a nicety. An attribute that is
renamed upstream must degrade to the previous fidelity rather than breaking
ingestion, and an unknown span kind must behave as if the convention were
absent.

## Interoperability

The Collector fan-out must keep working, unchanged:

```text
Application
     │ OTLP
     ▼
OTel Collector
     ├── Trustvian processor ──▶ behavioral analysis
     └── another backend     ──▶ traces, as before
```

A span Trustvian enriched with `trustvian.*` attributes remains an ordinary
span that any downstream consumer can display. That is interoperability, and
it is worth documenting as a scenario:

```text
Trustvian enriches a span with its decision and scores
        ↓
the same span reaches the team's existing trace backend
        ↓
that backend shows trustvian.* alongside everything else
```

**No downstream system is a dependency.** Trustvian requires none of them to
function, names none of them in an interface, and does not adopt any of their
data models. Being pleasant to sit beside is a property, not a coupling.

## Compatibility

Additive. `trustvian.*` output attributes keep their meaning; no existing
mapping rule changes; a producer emitting no agent-oriented convention sees
byte-identical behavior — including the adapter's current
preserve-every-attribute behavior, which this task does not alter.

If a fidelity indicator reaches `/v1` or the span attributes, it is a new
optional field under the existing additive-compatibility rule, and
`docs/compatibility.md` gains a row **when it exists**, not before.

## Tests

- Each supported convention maps to the expected `Operation`, `Target`,
  `Actor` and `Context`, table-driven, one case per attribute.
- **Absence degrades, never breaks**: a span with no agent-oriented attribute
  produces exactly the behavior it produces today, asserted against the
  current mapping's own fixtures.
- An unknown or future span kind behaves as if the convention were absent.
- A renamed or malformed attribute degrades to transport fidelity rather than
  erroring.
- **No fabrication**: a transport-only span never yields a tool name, asserted
  with a fixture that would be tempting to upgrade.
- **Privacy, at the layer that enforces it.** Spans carrying prompts,
  completions, `input.value`, `output.value`, tool arguments, tool results and
  retrieved documents are fed through, and every one of those distinctive
  values is asserted absent from:

  ```text
  StableFeatures · the fingerprint · DecisionRecord
  RealtimeObservation and its DTO · persisted rows · every /v1 and WebUI payload
  ```

  The test deliberately does **not** assert absence from `Event.Attributes`:
  the adapter preserves every span attribute today, that is documented
  behavior, and asserting otherwise would encode a change this task is not
  making.
- **Identity influences, content does not.** A span carrying both
  `tool.name=export_customer` and a prompt attribute yields behavior naming
  `export_customer` — proving the semantic attribute reached `Operation` —
  while the prompt value is absent from every layer listed above. One test,
  both halves, because the pair is the contract.
- Two spans differing **only** in a content attribute produce the **same**
  fingerprint, proving arbitrary attributes are not identity.
- Fingerprint stability: the same logical behavior at the same fidelity
  produces the same fingerprint across runs.
- **Boundary**: the core's dependency graph contains no OTel package; no
  framework name appears in any non-test source file; no `AgentEngine` or
  equivalent type exists.
- Fan-out: an enriched span still validates as an ordinary span and retains
  its `trustvian.*` attributes.

## Documentation

Written by the implementation PR:
`docs/OPENTELEMETRY.md` (replacing the *documented, not implemented* section
with what shipped), `docs/DOMAIN.md`, `docs/SECURITY.md` (the content boundary
restated for this path), `docs/ARCHITECTURE.md`, `docs/ROADMAP.md`, this
task's status, the task index and `CHANGELOG.md`.

**`docs/OPENTELEMETRY.md` must not claim this exists until it does.**

## ADR

An ADR is warranted, recording: why conventions are read and frameworks are
not; why the mapping lives at the adapter boundary and the core stays unaware;
why identity fields are read and content fields are deliberately ignored; how
version tolerance degrades rather than breaks; and — the one most likely to be
misread later — that the privacy guarantee is a durable and public evidence
boundary rather than a claim about the transient in-process attribute map, so
a future adapter-sanitization proposal is recognized as the compatibility
change it would be.

## Acceptance criteria

1. A producer emitting a supported agent-oriented convention yields behavior
   naming the tool, model or retriever rather than the transport.
2. A producer emitting none sees **no change whatsoever**.
3. No prompt, completion, reasoning, argument, result, retrieved document or
   arbitrary attribute becomes behavioral identity, a `DecisionRecord` field,
   a realtime field, a persisted row or a published payload. The transient
   `Event.Attributes` map keeps its documented preserve-everything behavior,
   which this task does not change and does not claim otherwise.
4. No semantic name appears that the telemetry did not supply.
5. The core imports no OpenTelemetry or convention package, proven by test.
6. No framework is named anywhere in the implementation.
7. No new engine, and no AI-specific branch in the pipeline.
8. Collector fan-out is unchanged and an enriched span remains consumable
   downstream.
9. Fidelity is reported rather than implied.

## Open questions left to implementation

1. **Which adapter surface carries the mapping** — `internal/otel`, the
   processor, or both. Both is assumed, sharing one mapping table.
2. **Where fidelity is carried**, and whether it reaches `/v1`. Assumed yes,
   as an optional additive field.
3. **The exact attribute set**, which must be checked against each convention's
   state when work begins rather than frozen here.
4. **Whether `Actor.Type` may be upgraded to `ai_agent` from telemetry alone**,
   or requires the producer to establish identity explicitly. The conservative
   reading — require it — is assumed.
5. **Whether a later task should sanitize `Event.Attributes` at the adapter.**
   Out of scope here, and noted so it is proposed deliberately rather than
   arrived at by drift.
