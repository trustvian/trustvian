# 0045 — Conventions are read, frameworks are not

**Status:** Accepted

## Context

Generic instrumentation flattens an agent's behavior into its transport. The
reference demo drives a model, a CRM, a knowledge service, an exporter and a
mailer; zero-code HTTP instrumentation reduces all five to `POST` and `GET`
against hostnames. Trustvian then learns a baseline over transport shapes, which
is correct but coarse — *"this agent called a new host"* is a weaker statement
than *"this agent used a tool it has never used"*, and the second is the one a
developer acts on.

The information is already on the wire.
[Task 075](../tasks/v1.0/075-ai-semantic-telemetry-normalization.md) is the work
that reads it, and `docs/OPENTELEMETRY.md` had carried a section titled
*"Potential AI-agent (GenAI) mappings — documented, not implemented"* stating the
position precisely: hard-coding an unstable external convention "would risk a
breaking upstream rename reaching into Trustvian's own `Event` semantics for no
current consumer need."

Task 073 and the reference demo became that consumer. This record is about the
decisions that let the convention be read without the risk that sentence named.

## Decision

### 1. Conventions are read; frameworks are never named

Trustvian reads **OpenTelemetry GenAI** and **OpenInference**. It names no agent
framework — not in a type, a field, a branch, or a special case — and
`scripts/check-platform-boundary.sh` now fails the build if one appears in
non-test source.

A convention is a published attribute contract: read it once and every producer
that emits it works. A framework is a product: name one and there is a per-vendor
special case to maintain forever, and the list is never finished. The check is
scoped to non-test source deliberately — a test may legitimately record which
producer a fixture imitates, which is documentation rather than coupling.

`openinference` is not on the forbidden list. It is the convention's own published
attribute name (`openinference.span.kind`), and reading it is the task.

### 2. The table lives in one OTel-free package, and the core stays unaware

`internal/semconv` holds the mapping. It takes a span reduced to plain Go values —
a kind, a name, an attribute map, a resource map — and returns the behavioral
dimensions a convention established, plus a fidelity value. It is pure: no clock,
no I/O, and it does not modify the map it is given.

**It imports no OpenTelemetry package, and that cost nothing to arrange.** The
GenAI attribute keys are not in the `semconv` package this repository pins, and
the history is the argument for every degradation rule below:

```text
semconv/v1.39.0   genaiconv/ present   41 GenAI attribute keys
semconv/v1.40.0   genaiconv/ present   46
semconv/v1.41.0   genaiconv/ present   50
semconv/v1.42.0   genaiconv/ GONE       0
semconv/v1.43.0   genaiconv/ GONE       0      ← the version both adapters pin
```

The keys grew for three releases and vanished. So the attribute names had to be
string literals wherever the table lived, which means the confinement rule in
`.claude/rules/go.md` is satisfied without an exception and the table needs no
fourth row in it.

Two adapters call the table: `internal/otel` for the in-process SDK path, and the
Collector processor for the OTLP pipeline path. Their *traversal* stays
duplicated — `sdktrace.ReadOnlySpan` and `ptrace.Span` are unrelated types and the
processor is a separate Go module — but the table is not, because both reduce their
span to the same plain map first. Two copies of a convention table would be two
conventions, and the one a developer got would be whichever adapter their telemetry
happened to take.

`event.NormalizeSpan` re-exports it so the processor can reach it.
[ADR 0002](0002-public-api-boundary.md)'s bar for public surface is a real
consumer, and the processor already imports `event` to construct `Event` values —
so the bar is met as written rather than stretched.

### 3. Identity fields are read; content fields are deliberately ignored

A tool *name* is what the agent did. A tool *argument* is what it said, and
routinely carries customer data. The first is a behavioral dimension; the second is
content.

So the table reads `gen_ai.tool.name`, `gen_ai.agent.name`, `gen_ai.request.model`,
`gen_ai.provider.name`, `gen_ai.conversation.id`, `gen_ai.data_source.id`,
`openinference.span.kind`, `tool.name`, `llm.model_name`, `llm.provider`,
`session.id`, `agent.name` and `reranker.model_name` — and reads none of the
twenty-two content attributes the two conventions define.

Those twenty-two are enumerated in `internal/semconv/content.go`, which changes no
behavior and exists so the refusal is **checkable**: one test feeds each key as a
span's only attribute and requires it to establish nothing, another feeds all of
them beside a tool name and requires only the tool name to survive, and a third
asserts the identity and content lists are disjoint. A deny-list nobody tests is a
comment.

Every GenAI entry carries an explicit sensitivity warning in the convention's own
registry — *"Likely to contain sensitive information including user/PII data"* — so
the classification is upstream's assessment rather than Trustvian's guess.

Three entries are worth justifying because they are not obviously payload:
`user.id` is an identifier, but a *human's*, and `Actor.ID` is the service;
`metadata` is a free-form JSON blob and therefore content by construction; the two
`mime_type` keys describe content and nothing else.

### 4. The privacy guarantee is a durable and public evidence boundary

**This is the decision most likely to be misread later**, so it is stated
exactly.

Trustvian does not promise that prompts never reach `event.Event`. It could not:
`internal/otel`'s own package comment says *"every span attribute — mapped or not
— is preserved in `Event.Attributes`, so nothing is silently dropped"*, and the
Collector processor does the same. That was true before task 075 and is unchanged
by it.

What Trustvian promises is that content never becomes **durable or public
evidence**:

```text
Event.Attributes        MAY carry producer attributes, as it does today
StableFeatures          six approved dimensions; no attribute reaches it
the fingerprint         does not depend on any attribute
DecisionRecord          fixed shape, no attribute map, by construction
realtime · persistence · /v1 · WebUI    bounded projections only
```

Each of those layers is asserted, with all twenty-two content attributes carrying
individually distinctive values so a failure names which one leaked. The tests
deliberately assert content **is** present in `Event.Attributes`, because asserting
absence there would encode a change this task is not making.

**A future proposal to sanitize `Event.Attributes` at the adapter is therefore a
compatibility change**, not a tightening of an existing promise. Today's
preserve-everything behavior is documented and a consumer may rely on it. That
proposal needs its own review, its own migration note and its own compatibility
row — and this paragraph exists so it is recognized as such rather than arrived at
by drift.

### 5. Version tolerance degrades, never breaks

Every rule here answers "what happens when upstream changes", and the answer is
always the same shape: fall back to the fidelity the telemetry actually proved.

| Situation | Result |
|---|---|
| an operation name outside the enum | convention absent — today's mapping |
| an unknown `openinference.span.kind` | convention absent; no error, no log line |
| a renamed attribute | convention absent |
| an attribute of the wrong type | convention absent |
| a category matched but the identity attribute missing | **the row does not fire** |

The last one is the important one. Emitting a category without a name would render
as `tool · POST` — a semantic category wearing a transport name — which is exactly
the fabrication the task forbids. Identity is dropped; correlation and actor
evidence are kept.

`gen_ai.system` is read **only as a legacy alias** for `gen_ai.provider.name`. It
appears nowhere in the current convention — not in the registry, and not in a
deprecated registry, because that repository has none — so it is read because
producers lag the specification, not because the specification lists both. It must
not be confused with `gen_ai.system_instructions`, which is a different attribute
carrying the system prompt; reading that one as a provider would put a prompt in
`Target.Name`, a durable behavioral dimension.

### 6. `Actor.Type` upgrades only on an explicit agent identity

`ai_agent` requires `gen_ai.agent.id`, `gen_ai.agent.name` or `agent.name`. The
presence of `gen_ai.operation.name` is **not** evidence: a plain backend service
calling an LLM through an instrumented client library emits
`gen_ai.operation.name=chat` and is not an AI agent.

The cost of getting this wrong is not cosmetic. `ActorType` is one of the six
`StableFeatures` dimensions, so flipping it changes every fingerprint for that
actor and discards its learned baseline. A rule that fired on a client library's
presence would silently reset baselines the first time a service adopted one.

`trustvian.actor.type` still overrides, which is what makes the conservative
default affordable: a producer that knows it is an agent and emits no agent-name
attribute can say so explicitly.

### 7. Explicit `trustvian.*` overrides outrank conventions

```text
trustvian.* override   always wins
convention             wins over transport
transport              unchanged
```

An operator's statement about their own telemetry outranks a convention reading,
and a convention outranks a guess from a hostname. Overriding one dimension does
not discard another: an explicit `trustvian.operation.category` beside a
convention-supplied name keeps the name.

### 8. Fidelity is reported, and is not behavioral identity

A closed two-value vocabulary — `transport` or `semantic` — describing the
*mapping result* rather than the span. A span carrying conventions the table
declined to read is `transport`, because transport is what the behavior was
derived from.

It is carried as the outbound span attribute `trustvian.fidelity`, on the ingest
envelope beside the record, on the realtime observation, and in the WebUI, which
states it in a sentence rather than a badge. There is deliberately **no inbound
override**: a producer able to *claim* semantic fidelity would defeat the one
guarantee the indicator makes.

It rides beside the record rather than inside `DecisionRecord` for the reason
[ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md) gave about the
learning scope: it is metadata about how the *adapter* derived the record, the
engine has no opinion about it, and putting it in the record would imply the
engine produced it and push an adapter concept into every producer.

**It is never a `StableFeatures` dimension.** Folding it in would make the same
logical behavior fingerprint differently before and after a producer upgraded its
instrumentation — reporting a baseline reset as a wave of novel behavior.

## Alternatives considered

**Read frameworks directly.** Rejected: an unbounded per-vendor maintenance
surface, and the conventions already carry what is needed.

**Put the table in the processor and let `internal/otel` stay transport-only.**
Rejected: the two adapters would then disagree, and which answer a developer got
would depend on whether their telemetry took the SDK or the pipeline path.

**Duplicate the table in both adapters**, as the traversal already is. Rejected:
the traversal duplication is *forced* by incompatible span types; a table over a
plain map is not forced by anything, and two copies of a convention are two
conventions.

**Make the table a public top-level package.** Rejected against ADR 0002's bar,
and because `semconv` as a public name collides conceptually with OpenTelemetry's
own. Re-exporting through `event` uses the door that already exists for this.

**Add an `agent` or `workflow` operation category.** Rejected: that is a core
change, which task 075 must not make. Agent, workflow and plan operations map onto
the existing `tool` category — the one that means "the actor did a thing rather
than spoke to a model" — and inference maps onto `external`, because a model call
leaves the process to a third party.

**Upgrade `Actor.Type` on any GenAI attribute.** Rejected as §6 explains.

**Make fidelity a float or free text.** Rejected by the task explicitly, and a
per-dimension matrix is a third shape nobody asked for.

## Consequences

- A producer emitting a supported convention gets behaviors named by tool, model,
  agent or data source. A producer emitting none sees byte-identical behavior,
  asserted against real spans and end to end through a real Collector.
- The core's dependency graph still contains zero OpenTelemetry packages, now
  checked mechanically by `go list -deps` in the boundary script rather than by
  convention — paired with a check that `internal/otel` *does* still import
  OpenTelemetry, so the first cannot pass vacuously because somebody moved the
  adapter.
- `internal/semconv` will need editing whenever either convention moves. That is
  the cost of reading an unstable contract, and it is bounded: one package, one
  table, two commits of provenance recorded in its source.
- **The indicator reaches Collector-path operators.** As first shipped it did not:
  the mapping computed fidelity and the outbound span carried it, the platform's
  ingest envelope accepted it, and the *processor's* envelope had no such field —
  so the value stopped at the Collector and the live view read `transport` for
  every record, including the ones a convention had named. `DecisionRecord` has no
  attributes, so nothing downstream could re-derive it. It now rides beside the
  record in the processor's envelope too, exactly as `behavioral_profile` does and
  for the identical stated reason, read at the ingest call site from the same
  `Result` that produced the span attribute. Fidelity is therefore reported to
  everyone who is told a behavior's name, not only to a producer that POSTs to
  `/v1` itself. The gap was found by measurement rather than by reading — twice,
  independently, against a real agent — which is the argument for the end-to-end
  assertion that now covers it: every layer was individually correct.
- **Open: fidelity is not persisted per behavior**, so the comparison response's
  behavior deltas do not carry it. A delta is built from persisted behavioral
  evidence, and storing fidelity per behavior needs a forward-only schema step in
  both SQLite and PostgreSQL plus the backup/restore/upgrade path. It is deferred
  as its own task rather than folded into this one: a migration bug damages a
  user's database, and the summarisation rule for a behavior whose observations
  disagree about fidelity is a decision that should be made rather than arrived at.
  `TestFidelityIsNotPersistedYet` records the gap in the suite and fails the moment
  it closes. *Closed by task 081
  ([ADR 0068](0068-fidelity-and-layer-are-persisted-as-per-behavior-counts.md)):
  that test is replaced by `TestComparisonsReportPersistedFidelity`.*
- Task 075's own *What is read* table listed `gen_ai.system` as the provider
  signal. That was accurate when written and is now stale; the task has been
  corrected in the same change as this record.
