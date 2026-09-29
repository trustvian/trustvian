# 082 — Agent Inspection and Evaluation Depth

Status: specified. **Documentation and planning only; implements nothing.**
Milestone: `v1.0` for the items marked so below; the rest are later or
`PROPOSED`
Depends on: [075](075-ai-semantic-telemetry-normalization.md) (implemented),
[067](README.md) — event-history capability boundary, for the items that need
durable history
Blocks: nothing. It reserves and scopes 083–090, and authorizes no code

## Objective

Decompose one product sentence into dependency-ordered milestones:

> **Inspect what your agent did, understand what changed between versions, and
> make release decisions using evidence.**

That sentence is not a new direction. It is the
[Track B journey](../../ROADMAP.md#v10-exit-criteria) read from a developer's
side, and every clause of it already has an owner in this repository — except
where it does not, which is what this task exists to find and to number.

The developer workflow it serves, in six steps:

```text
1  run an agent against a versioned set of scenarios
2  inspect model calls, tool calls, outbound requests, errors and timing
3  compare a reference version with a candidate
4  evaluate behavior, task quality, performance and cost
5  investigate a regression through linked evidence
6  apply explicit release criteria and record the review decision
```

Steps 1, 3 and 6 are substantially owned already — by
[078](078-behavioral-scenario-suites.md),
[054](054-behavioral-diff.md), [055](055-evaluation-scorecards.md) and
[056](056-deterministic-hard-gates.md), and
[066](066-promotion-workflow.md). Step 2 is owned by
[076](076-behavioral-evidence-explorer.md) over
[067](README.md)'s storage. Step 5 is owned by **nobody**, and it is the step a
developer reaches for first when a gate fails. Step 4's *behavior* half is
owned and its *performance, cost and quality* halves are not.

This task is the planning layer, in the shape
[049](049-platform-architecture-alignment.md) used: it writes down what is
implemented, what is planned, what is missing, and what a decision would cost —
and it writes no code.

## Why a planning task rather than eight specifications

Because the repository's own rule says a row is not a specification:

> **Each task gets its own written spec before implementation starts.** A
> roadmap row is not a specification, and the row does not authorize writing
> code against it.

Tasks 067–072 have carried approved roadmap rows and no specifications for
several milestones, deliberately. The eight items below are numbered, scoped
and sequenced here so the dependency graph is decidable; each still gets its
own specification before anything is built, and three of them cannot be
specified honestly until a decision recorded in
[§ Open decisions](#open-decisions) is made.

## Where this came from

Two sources, and the distinction matters for how much weight each carries.

**Measurement.** Two independent measurements against a real local model found
two gaps this repository had not recorded. One is fixed:
`trustvian.fidelity` never reached the control plane through the Collector
path, so the live view read `transport` for every record — found in the
companion demo's tool-fidelity measurement and again in
[078's design notes](078-design-notes.md), fixed since. The other is **not**
fixed and is item [083](#083--behavioral-layer-identity-and-display-classification):
one behavioral change produced *two* added behaviors, because an instrumented
tool call and the HTTP request underneath it are two behavioral identities.

**Comparative reading.** The existing agent trace-and-evaluation backends — the
category of tool that stores spans, renders waterfalls and scores model output —
are the closest well-developed systems to the workflow above, and reading how they
frame the problem is what makes these gaps legible as a set rather than as six
unrelated annoyances. What Trustvian takes from that reading is **conceptual**:
that a developer investigating a regression wants to move from a finding to the
spans behind it in one step; that a comparison is more useful when the two sides
declare which prompt, model, toolset and configuration they were; and that
evaluator output is more honest when it carries a score, a label, an explanation
and its own provenance.

What Trustvian does **not** take from that reading is code, a dependency, or a
product boundary. Those systems are commonly source-available rather than OSI open
source, and this repository is Apache-2.0, so code reuse is settled by licence
before the engineering question arises — see
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md),
which records why a trace backend is an interoperability target and never a
runtime requirement, and which is **Proposed** rather than Accepted.

That category of tool answers *what happened inside this trace, and how good was
the output*. Trustvian answers *which observed actions constitute this actor's
behavior, how does that differ from reference, and what decision follows*. The
second question is the product. Feature parity with the first is not a goal here,
and **no item below is justified by another product having one** — each is
justified by a gap in Trustvian's own developer workflow, and two of them by a
measurement.

## Current state, verified against `main`

Every row was read from source at `d54d3fe`, not from a previous document's
description of it.

| Capability | State | Evidence |
|---|---|---|
| Agent-oriented conventions read (GenAI, OpenInference) | **Implemented** | `internal/semconv`, task 075 |
| Tool calls named as tool calls; graceful degradation to transport | **Implemented** | `internal/semconv/genai.go`, `openinference.go`, `degradation_test.go` |
| Fidelity reported on span, ingest envelope, realtime observation, WebUI | **Implemented** | task 075; the Collector-path envelope gap was fixed separately |
| Fidelity persisted per behavior, so a comparison delta carries it | **Planned, unspecified** | task 081; `TestFidelityIsNotPersistedYet` |
| Content refused at every durable and published surface | **Implemented** | 22 attributes in `internal/semconv/content.go`, read by nothing |
| Model calls distinguishable from tool calls | **Implemented, coarsely** | a model call is `external`, named by `gen_ai.request.model` — but `retrieval` is `external` too |
| A dedicated `model` operation category | **Absent** | `event.OperationCategory` is `http`, `db`, `rpc`, `tool`, `external` (`event/event.go`) |
| Tool-layer and transport-layer spans counted as one behavior | **Absent, and measured wrong** | one change produced two added behaviors; no rule exists |
| Trace and span identity on the evidence boundary | **Implemented** | `DecisionRecord.TraceID`, `.SpanID`, `.SessionID`, `.DelegatedFrom` |
| **Parent** span identity on the evidence boundary | **Absent** | `event.Context` has `TraceID`/`SpanID` and no parent; nor does `DecisionRecord` |
| Span duration and error status on the evidence boundary | **Absent** | both adapters bridge them into `Event.Attributes["duration_ms"]`/`["error"]` for `features.Extract`; neither reaches `DecisionRecord` |
| Token usage read from telemetry | **Absent** | `gen_ai.usage.*` and `llm.token_count.*` are neither read nor on the refused list |
| Per-record retention in the platform | **Absent, by design** | a run persists an aggregate and a per-fingerprint snapshot (ADR 0030); task 067 owns changing that |
| Latency, error-rate, token or cost aggregates | **Absent** | `EvaluationAggregate` summarizes identity confidence, anomaly, trust and context risk only |
| Behavioral diff between two runs | **Implemented** | task 054; `BehaviorDelta` carries presence, counts and rates |
| A link from a delta or a gate check to the observations behind it | **Absent** | `BehaviorDelta` carries no observation reference; `EvaluationGateResult` is fixed-shape with five named integer checks (ADR 0028, ADR 0029) |
| Candidate version provenance | **Partly implemented** | `CandidateMetadata` has `Label`, `SourceRef`, `ArtifactDigest`, `Model`, `ToolsetDigest`, `ConfigDigest` — and no prompt or scenario reference |
| Repeatable scenarios, N runs per side, k-of-N gate | **Specified, not implemented** | task 078; awaits its own measurement re-run |
| A run-scoped behavior route | **Specified, not implemented** | 078's first slice; not present on `main` |
| Promotion recorded with the evidence it rested on | **Implemented** | task 066 |
| Human annotation or review decision on a finding | **Absent** | a promotion records a decision about a *candidate*, not about a finding |
| Any quality, correctness or answer evaluation | **Absent, and currently out of product** | ROADMAP § What Trustvian is not becoming; 078 § Non-goals |
| Trace search, trace tree, waterfall, timeline | **Absent** | 076 scopes session/trace/sequence views and explicitly excludes a waterfall "for its own sake" |

Two conclusions follow, and they set the order of everything below.

**The telemetry foundation is in better shape than the evidence boundary.**
Trustvian reads the conventions well. What it then carries across
`DecisionRecord` into the platform is narrower than what it read — no parent
span, no duration, no status, no tokens — so several capabilities a developer
would call "observability" are blocked on the *record* boundary rather than on
the adapter.

**Nothing links a finding to its evidence.** This is the single highest-value
gap in the list. A gate FAIL today names a count; a delta names a fingerprint.
Neither says *which observations*, and a developer whose gate just failed
wants exactly that.

## The eight items

Placement vocabulary, used in every item below:

| Placement | Meaning |
|---|---|
| **Core product** | ships in the OSS engine, platform, CLI or WebUI |
| **Optional integration** | an operator may enable it; nothing requires it, and its absence changes no result |
| **Demo** | the reference workload demonstrates a *completed* product capability |

**The demo demonstrates; it never owns.** No item below moves evaluation,
aggregation, diffing, gating or orchestration into `examples/` or into the
companion demo repository. A capability that only works because the demo does
part of the work is not delivered.

### 083 — Behavioral layer identity and display classification

**Placement:** core product. **Depends on:** 075 (implemented).
**Gate:** `v1.0` — it changes what a diff counts, and criterion 17 says a diff
is "produced and explained".

**Developer problem.** One behavioral change is reported as two. The demo agent
gained the ability to export a customer record; the comparison reported:

```text
BEHAVIORAL DIFF: added 2
    + POST  → export.localhost
    + export_customer →
```

Both rows describe the same act. The tool call is one span, the HTTP request it
made is another, and each produces its own `StableFeatures` and therefore its
own fingerprint. At `max_added_behaviors: 0` this is invisible; the moment a
team allows one added behavior, they are allowing half of one and have no way to
know. A second, smaller symptom sits beside it: a `tool` span carries no
transport target, so the same view renders `export_customer →` with a dangling
arrow.

**Current state, verified.** `Operation.Category` is one of `http`, `db`, `rpc`,
`tool`, `external` (`event/event.go`), and it is a `StableFeatures` dimension —
so it *is* behavioral identity. `internal/semconv` maps a GenAI tool span to
`tool` and the transport span beneath it stays `http`; nothing relates the two.
A model call maps to `external` named by `gen_ai.request.model`, and so does a
`retrieval` — so "a model was consulted" and "a document store was queried" are
the same category. No rule, flag or documentation states what should happen when
both layers of one act are observed.

**Scope.**

- An **explicit rule** for the case where a tool-layer span and a
  transport-layer span describe one act, with the rule stated in
  `docs/OPENTELEMETRY.md` and applied in one place.
- A **non-identity classification** that lets a view say "model call", "tool
  call" or "outbound request" without `OperationCategory` changing, and lets a
  view render a target-less behavior honestly.
- Whatever the rule chooses, **stated rather than inferred** — the same
  discipline task 075 applied to fidelity.

**Non-goals.** No new `OperationCategory` value *unless*
[§ Open decisions](#open-decisions) decides to accept the baseline reset that
implies. No suppression of transport spans from the trace record — a dropped
span is evidence destroyed; this is about what counts as *one behavior*, not
about what is observed. No inference of parent/child from timing. No content.

**Dependencies and architectural impact.** This is the one item in the list
that can touch behavioral identity, which makes it the one with a real
migration cost. Adding a `model` category, or folding two layers into one
fingerprint, changes `StableFeatures` for spans that previously produced a
different one — and every affected actor's learned baseline becomes
unreachable. ADR 0024 and
[Compatibility § persisted state](../../compatibility.md#persisted-state) both
apply. The alternative design — identity unchanged, a separate non-identity
label — costs nothing in migration and delivers the display half without the
counting half. **Which one is chosen is not decided here.**

**Acceptance criteria.**

1. One documented rule decides whether a tool-layer span and its
   transport-layer span are one behavior or two, and the rule is applied in
   exactly one place.
2. A single behavioral change produces a diff whose added-behavior count
   matches the number of changes a developer would name, demonstrated against
   the reference workload's export-customer change.
3. A model call, a tool call and an outbound HTTP request are each
   distinguishable in a rendered view without reading attribute names.
4. A behavior with no target renders without a dangling separator.
5. No display label reaches `StableFeatures`, a fingerprint, or a baseline key,
   proven by test.
6. A producer emitting no agent-oriented convention sees byte-identical
   behavior — task 075's degradation guarantee is not weakened.
7. If identity changes, a migration and its rollback are specified and drilled
   before the change lands; if identity does not change, a test asserts it did
   not.

**Validation.** The existing degradation suites (`internal/otel`,
`cmd/trustvian/semantic_e2e_test.go`) must pass unchanged. A differential test
runs the same workload with and without tool-level instrumentation and asserts
the added-behavior count is the same number of *changes*. The measured
double-count is reproduced as a regression fixture before the rule is written,
so the rule is verified against the thing that motivated it.

**Privacy and risks.** No new content surface; a label is derived from
attributes already read. The material risk is the baseline reset above, and the
second risk is subtler: a rule that folds two layers into one *loses* the
transport target from behavioral identity, so an agent that moved
`export_customer` from `export.localhost` to `attacker.example` would no longer
change its behavioral surface. That is a detection regression, and it is the
strongest argument for keeping identity as it is and fixing only the counting
and the labels.

**Relationship to existing tasks.** Extends 075's mapping table. Changes what
054's diff counts and therefore what 056's `max_added_behaviors` means, and what
078's k-of-N presence counts describe. Must land before 078's thresholds are
measured a second time, or the re-run measures the double count.

### 084 — Correlation and operational evidence on the record boundary

**Placement:** core product. **Depends on:** nothing new.
**Gate:** `v1.0` — criteria 14 and 17 depend on what it carries.

**Developer problem.** A developer looking at a failed gate wants to know which
call was slow, which one errored, and what called what. Trustvian computes the
first two and throws them away at the boundary, and never had the third.

Concretely: a candidate's `export_customer` call started failing and taking
four seconds. Trustvian's `features.Extract` saw both facts — `duration_ms` and
`error` are bridged onto `Event.Attributes` by both adapters precisely so it
could — but `DecisionRecord` carries neither, so the platform, the comparison,
the scorecard and every view are unaware that anything took any time at all.

**Current state, verified.** `internal/otel/otel.go` and `processor/mapping.go`
both write `Attributes["duration_ms"]` and `Attributes["error"]` from span
fields, for `features.Extract`. `DecisionRecord` carries `TraceID`, `SpanID`,
`SessionID`, `DelegatedFrom`, `ApprovalStatus` — and no duration, no status, no
parent span. `event.Context` has no parent field either. `EvaluationAggregate`
summarizes identity confidence, anomaly score and confidence, trust and context
risk, and nothing operational.

**Scope.**

- **Parent span identity** on `event.Context` and on `DecisionRecord`, so a
  trace tree can be reconstructed from retained evidence rather than inferred.
- **Duration and error status** promoted from volatile feature inputs to
  recorded evidence on `DecisionRecord`.
- Corresponding **bounded aggregates** on the platform side, in the existing
  `MetricSummary` shape for duration and a counter for errors.
- An explicit **unavailable-versus-zero** rule: a span with no end timestamp did
  not take zero milliseconds, and the record must say "not stated" rather than
  `0`.

**Non-goals.** No per-record persistence — task 067 owns that, and this item
only makes the fields *available* to whatever 067 retains. No token or cost
fields; those are 087's, and bundling them here would make an additive change
wait on a pricing decision. No span events, links, or arbitrary attributes. No
latency percentiles: min/max/sum/count is what the existing `MetricSummary`
offers and what ADR 0029's reasoning against floats in a verdict tolerates.

**Dependencies and architectural impact.** Additive to two public types
(`event.Context`, `DecisionRecord`), which is permitted —
[ADR 0020](../../adr/0020-v1-compatibility-contract.md) and the compatibility
contract govern the shape. It is generic, and that matters: duration and error
status are ordinary properties of an observed action, not platform concepts, so
this does not put platform awareness in the core. It is the hard prerequisite
for 076's timeline, 085's evidence links carrying timing, and 087's latency
comparison.

**Acceptance criteria.**

1. A `DecisionRecord` produced from a span with a parent carries the parent span
   identity; one produced from a root span carries none, and the two are
   distinguishable.
2. Duration and error status are present on the record when the span stated
   them, and explicitly absent when it did not — never defaulted to `0` or
   `false`.
3. The platform aggregates duration and error count per run, bounded and
   overflow-safe like every existing counter.
4. Nothing added here enters `StableFeatures`, a fingerprint or a baseline key,
   proven by test.
5. A producer that supplies neither duration nor status produces the same
   behavioral result as today.
6. Identical results on SQLite and PostgreSQL for whatever is persisted.
7. `features.Extract`'s existing reading of `duration_ms`/`error` is unchanged —
   this item adds a recorded path, it does not move the feature path.

**Validation.** Round-trip tests over the public JSON boundary for present,
absent and zero durations. A test asserts the volatile attribute bridge and the
new recorded fields agree for the same span. Existing engine and aggregate
suites pass unchanged, and a compatibility test asserts an older consumer
ignoring the new fields still decodes a record.

**Privacy and risks.** Duration, error status and parent span identity are
metadata and carry no content; the boundary in
[SECURITY.md](../../SECURITY.md) is unaffected. The material risk is scope
creep — a record that has learned to carry operational fields is a record
somebody will want to carry a span attribute map next, which is the boundary
ADR 0030 and task 050 both drew. The specification must say so explicitly.

### 085 — Evidence links: from a finding to the observations behind it

**Placement:** core product. **Depends on:** 067 (durable history) and 084 —
nothing else. **Consumed by** 076 (browser navigation), 079 (pull-request
comment) and 088 (review decisions), none of which it depends on.
**Gate:** `v1.0` — criterion 17's "and explained".

**085 completes without 076, and the dependency runs one way.** The capability is
authoritative at the control plane and is validated over `/v1` and the CLI; a
browser is one consumer among three, and navigating it is 076's acceptance
criterion rather than this item's. Stating that explicitly matters because an
earlier draft of this task had 076 depending on 085 for navigation *and* 085
depending on 076 for presentation — a cycle, which would have left neither
completable.

**Developer problem.** The gate failed. A developer sees:

```text
GATE   FAIL
  added_behaviors        actual 3   maximum 0   FAIL
```

and has no path from that `3` to the three behaviors, or from any of them to
the observations that produced it, or from an observation to the trace it came
from. The investigation restarts from the run identifier every time. This is the
step the workflow calls *investigate a regression through linked evidence*, and
it is the reason a developer reaches for a second tool — which the Track B
gate rules out in the sentence beneath its journey, and which 076's own
criterion 8 states directly.

**Current state, verified.** `BehaviorDelta` carries `FingerprintID`,
`Behavior`, `Presence`, reference and candidate counts and rates — and no
reference to any observation. `EvaluationGateResult` is deliberately
fixed-shape: five named integer checks, a verdict, comparison identity, and
explicitly "no slice, map, pointer, interface, retained scorecard or policy, or
string reason" (ADR 0028, ADR 0029). Nothing in the platform can answer "which
observations made this count 3".

**Scope.**

- A **resolution capability**: given a comparison and a finding — a delta, or a
  named gate check — return the behavioral identities that contributed to it,
  and from a behavioral identity the retained observations for it, bounded and
  paginated in the repository's one pagination shape.
- **Stable finding identity**, so a link can be cited in a pull-request comment
  or a review decision and still resolve later.
- A **bounded `/v1` read route** for it, and CLI access to that route, so the
  capability is exercisable and shippable on its own.

**Non-goals.** **No change to the fixed shape of a gate result or a scorecard.**
This is the central design constraint: ADR 0028 and ADR 0029 chose closed,
fixed-shape evidence on purpose, and a `[]Link` field would reopen exactly what
they closed. Resolution is a *query against authoritative state*, not a payload
carried inside a verdict. No re-derivation: the resolver reads what was
recorded and never recomputes a count, a rate or a verdict — a corrected gate
may legitimately answer differently from the same evidence, which is why 066
snapshots rather than re-derives. No storage of its own. No content.

**No user interface.** This item ships a capability and its `/v1` route, not a
view. 076 owns browser navigation over it, 079 owns rendering a link in a
pull-request comment, and 088 owns recording a decision against a resolved
finding. None of those is in this item's scope or its acceptance criteria.

**Dependencies and architectural impact.** Blocked on 067: there is nothing to
link *to* until per-record or per-behavior history is durable, and this item
must not route around 067 with a store of its own — the same rule 076 accepted.
It should be specified *with* 067 so 067's retention contract is chosen knowing
what resolution needs, which is the ordering mistake 076 documents having
avoided. Architecturally it is a read capability on the control plane, consumed
by three adapters, and it puts no evaluation logic in any of them.

**Acceptance criteria.**

1. Given a comparison and a named gate check that FAILed, the control plane
   returns the behavioral identities that contributed to it; given a behavioral
   identity, it returns the retained observations for it. Verified over `/v1` and
   through the CLI, with no browser involved.
2. Every number a resolution returns equals the number the recorded evidence
   holds; nothing is recomputed, proven by test.
3. A finding identity resolves to the same finding on a later read, or reports
   explicitly that the evidence is gone.
4. Every resolution is bounded, paginated in the established shape, and states
   saturation rather than truncating silently.
5. No prompt, completion, argument, result, document, body or arbitrary
   attribute is reachable through any resolution route.
6. A run whose history has aged out resolves to an explicit empty state, not an
   error and not a fabricated summary.
7. Identical results on both persistence backends.
8. **Resolution logic exists only in the control plane.** The CLI reaches it over
   `/v1` and holds none, proven by the architecture test that already forbids
   evaluation logic in an adapter. Later consumers inherit the same rule as their
   own criterion; this item does not wait for them.
9. **This item is complete with no browser change.** A test exercises the whole
   path — gate FAIL to contributing behaviors to observations — without the
   WebUI, so nothing here depends on 076.

**Validation.** At the `/v1` and CLI level, deliberately — **no UI test is
required for this item to be complete.** A regression fixture reproduces the
demo's export-customer change end to end and asserts the FAIL resolves to exactly
the behaviors and observations that caused it. A privacy tripwire injects
distinctive content values upstream and asserts they appear in no resolution
payload. Pagination is tested for duplicates and omissions under concurrent
writes. An aged-out run is tested explicitly. The architecture test covering
adapter boundaries is extended to the new route.

**Privacy and risks.** This is the item with the largest privacy surface in the
list, because its whole purpose is to make evidence reachable, and every
reachable field is one somebody will ask to widen. The mitigation is the same
one 076 uses: an allowlist of fields the retention contract names, and a
tripwire test rather than a review promise. The second material risk is a
correctness trap — a resolver that recomputes instead of reading would let the
displayed explanation of a historical FAIL drift from the FAIL itself, which is
precisely the failure 066 was designed to prevent.

**Relationship to existing tasks.** Needs 067 and 084, and nothing else.
**Consumed by** 076, whose views navigate it; by 079's pull-request comment, which
renders and computes nothing; and by 088's review decisions, which are recorded
against a resolved finding. Every one of those edges points *into* this item, so
it can be built, tested and shipped before any of them exists.

### 086 — Scenario and input versioning

**Placement:** core product. **Depends on:** 078.
**Gate:** not a `v1.0` gate item; it belongs with 078's thread.

**Developer problem.** A comparison says the candidate added a behavior. It does
not say whether the two sides were fed the same input. A developer who edited a
fixture between the reference run and the candidate run gets a behavioral
difference caused by their own test data, reads it as a regression, and spends an
afternoon on it.

**Current state, verified.** `CandidateMetadata` records `Label`, `SourceRef`,
`ArtifactDigest`, `Model`, `ToolsetDigest` and `ConfigDigest` — fixed fields,
descriptive only, with its own doc comment noting "a field can be added in a
later task". There is **no** prompt reference and **no** scenario or input
reference. 078 defines a scenario file naming `inputs:` and explicitly refuses a
dataset platform: "no dataset entity, no versioning, no splits, no labelling, no
curation and no dataset API".

**Scope.**

- A **scenario definition digest** and an **input digest** recorded on the
  evidence a scenario execution produces, so a comparison can state whether both
  sides ran the same scenario against the same inputs.
- A **prompt reference** field beside `Model` on `CandidateMetadata`, for
  producers that supply one — a reference or digest, never prompt text.
- A comparison that **states a mismatch** rather than silently comparing across
  it.

**Non-goals.** **No dataset platform**, and this item does not soften 078's
non-goal: no dataset entity, no dataset API, no splits, no labelling, no
curation, no golden outputs, no expected-answer semantics. No prompt registry
and no prompt storage — a digest and a reference are recorded; prompt text is
content and stays refused. Versioning is **git's**, not Trustvian's: a scenario
file is a repository artifact, and this item records *which* version was used
rather than becoming a place to keep versions.

**Dependencies and architectural impact.** Additive fixed fields on
`CandidateMetadata`, following the precedent its own comment sets, plus fields on
078's result document. Nothing read for a decision — like every existing
`CandidateMetadata` field, it is descriptive and never reaches a fingerprint or
a baseline key. Requires 078, because there is no scenario execution to stamp
until 078 exists.

**Acceptance criteria.**

1. A scenario execution records the digest of the scenario definition and of its
   declared inputs.
2. A comparison across two executions whose scenario or input digests differ
   reports that fact prominently; it does not refuse the comparison and does not
   hide the difference.
3. A candidate may carry a prompt reference; a candidate that does not is
   rendered as "not stated", never as empty or as a default.
4. No field added here reaches `StableFeatures`, a fingerprint or a baseline key,
   proven by test.
5. No prompt, input or output text is stored or rendered — digests and
   references only.
6. A scenario run with no declared inputs is valid and records the absence
   explicitly.

**Validation.** A test changes one byte of a fixture and asserts the comparison
reports an input mismatch. A test asserts a digest is stable across runs and
across backends. The existing privacy tripwire is extended to the new fields.

**Privacy and risks.** A digest of an input is not the input, and that is the
whole design: it answers "same or different" without retaining anything. The
material risk is a slippery one — a field named `PromptRef` invites a later field
named `PromptText`, and the specification must state the refusal where the field
is defined rather than in a separate document.

**Relationship to existing tasks.** Extends 078 without changing any of its
non-goals: 078 still runs scenarios and still refuses to be a dataset platform.
Extends 052's `CandidateMetadata`. Its digests are what 085's finding identities
are cited alongside, and what 088's review decisions record.

### 087 — Performance and cost evidence

**Placement:** core product for latency and errors; **cost is an optional
integration** because it requires operator-supplied pricing.
**Depends on:** 084. **Gate:** not a `v1.0` gate item.

**Developer problem.** The candidate passed every behavioral gate and made the
agent twice as slow and three times as expensive. Trustvian says nothing, so the
team finds out from a bill or from a user.

**Current state, verified.** No latency, error-rate, token or cost evidence
exists anywhere in the platform. `EvaluationAggregate` summarizes identity
confidence, anomaly score, anomaly confidence, trust score and context risk.
Token attributes (`gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`,
`llm.token_count.*`) are **neither read nor listed among the 22 refused content
attributes** — they are simply unconsidered, which is itself a documentation
gap this item closes. 078 lists "no cost or token accounting" among *its*
non-goals, which scopes 078 and does not decide the product question.

**Scope.**

- **Latency and error comparison** between reference and candidate, from 084's
  recorded duration and status, in the existing bounded summary shape.
- **Token counts** read from the GenAI and OpenInference usage attributes where a
  producer emits them, added to `internal/semconv`'s table and to
  `docs/OPENTELEMETRY.md`.
- **Cost estimation** as an explicitly versioned, operator-supplied pricing
  table: a cost figure is reported only together with the pricing version and
  its provenance, and never computed from a rate Trustvian guessed.
- **Unavailable is not zero**, everywhere: a run whose producer emitted no token
  counts reports "not available", and a comparison involving it says so instead
  of reporting a 100% reduction.

**Non-goals.** No model benchmarking and no provider comparison — the subject
measured is *this candidate against its reference*, never one model against
another, which is the line ROADMAP § What Trustvian is not becoming draws. No
bundled pricing data: shipping a price list would make this repository's release
cadence a pricing feed, and a stale bundled price is worse than an absent one.
No latency percentiles or distribution statistics; no statistical significance
testing, for the reason 078 gives. No cost gate in the first slice — gating on
cost is a policy decision that should follow evidence, not arrive with it.

**Dependencies and architectural impact.** Hard-blocked on 084: without recorded
duration and status there is nothing to compare. Pricing is configuration and
belongs in the existing schema-versioned YAML surface, parsed in one place, with
no second configuration mechanism. Cost is derived evidence and must be
reproducible: the same records plus the same pricing version must produce the
same number, which means the pricing version is part of the evidence rather than
an ambient input.

**Acceptance criteria.**

1. A comparison reports candidate and reference latency summaries and error
   counts when the telemetry supplied them.
2. A run whose producer supplied no duration, status or token counts reports each
   as unavailable, and no comparison involving it reports a delta.
3. Token counts are read only from the documented usage attributes, and the
   mapping table in `docs/OPENTELEMETRY.md` lists them.
4. A cost figure is never rendered without its pricing version and provenance.
5. Cost is reproducible: identical records and identical pricing version produce
   an identical figure, proven by test.
6. Absent pricing produces no cost figure and no error — cost is optional, and
   its absence changes no behavioral result.
7. No token count, duration or cost enters `StableFeatures`, a fingerprint or a
   baseline key.
8. No prompt, completion or message content is read to obtain a token count; only
   the producer's own usage attributes are.

**Validation.** Golden tests over a fixed record set and a fixed pricing version.
A test asserts a missing-token run renders "not available" and not `0`. A test
asserts that removing the pricing configuration removes the cost figure and
nothing else. The degradation suite is extended: a producer emitting no usage
attributes must see byte-identical behavioral results.

**Privacy and risks.** Token counts are metadata; deriving them by counting
tokens in prompt text would not be, and is refused explicitly. The material risk
is credibility: a cost number is the kind of figure people quote, and a wrong one
damages trust in every other number on the page. Hence pricing provenance as an
acceptance criterion rather than a nicety. A second risk is that latency measured
through an agent driven by a local model is dominated by the model, so a latency
comparison must not be presented as attributable to the change under test.

### 088 — Review decisions and annotations on findings

**Placement:** core product. **Depends on:** 085, 066.
**Gate:** not a `v1.0` gate item; 066 already satisfies criterion 18.

**Developer problem.** The gate failed for a reason the team decided is fine —
the candidate legitimately gained a tool. Today the options are to loosen the
gate limit, which silently loosens it for every future run, or to ignore the
FAIL, which teaches the team to ignore FAILs. There is no way to say "this
specific finding, in this specific comparison, is expected" and have that
recorded.

**Current state, verified.** Task 066 records an immutable promotion decision
about a *candidate*, snapshotting the gate result it consumed field for field.
Nothing records a decision, note or annotation about an individual *finding*. The
gate's limits are the only mechanism available, and they are global to the
policy.

**Scope.**

- An **annotation**: a durable, append-only note attached to a resolved finding
  identity, carrying who, when, and a bounded text rationale.
- An **acknowledgement**: a decision that a named finding in a named comparison
  is expected, recorded as a separate fact **beside** the computed verdict.
- **Review scope and audit history**: what a reviewer was shown, what they
  decided, and the ability to read the sequence back.

**Non-goals.** **An acknowledgement never rewrites the verdict.** This is the
item's central rule: the gate's computed result is evidence and stays exactly as
computed, and an acknowledgement is a second, separately-authored record that
says a human accepted it. A view may show "FAIL, acknowledged"; nothing may show
"PASS" because somebody accepted a FAIL. Task 066's reasoning applies directly —
it keeps the gate result the decision consumed rather than promising today's code
would re-derive it. No mutable annotations and no deletion; append-only, like a
promotion. No approval workflow, no assignment, no notification, no roles — 070
owns authentication and access control, and this item must not invent a
half-version of it. No global suppression rule that outlives one comparison.

**Dependencies and architectural impact.** Needs 085, because an annotation
attached to a finding that cannot be resolved later is a note about nothing.
Follows 066's shape — immutable, evidence-backed, decided against the state it
was decided on. Identity attribution is the open problem: with no
authentication before 070, "who" can only be an operator-supplied string, and the
specification must say that plainly rather than implying an authenticated
identity.

**Acceptance criteria.**

1. A finding can carry an append-only annotation, and reading the annotations
   back returns them in order with their recorded authorship and time.
2. An acknowledgement is stored as a distinct record; the gate result it refers
   to is byte-identical before and after, proven by test.
3. No surface reports a verdict that differs from the computed one because of an
   acknowledgement.
4. An acknowledgement names one finding in one comparison and does not apply to
   any other comparison.
5. Annotations and acknowledgements survive a restart and behave identically on
   both backends.
6. An annotation's text is bounded and rendered as text, never as markup.
7. Authorship is recorded as what it is — operator-supplied until 070 — and no
   surface implies it was authenticated.

**Validation.** A test acknowledges a FAIL and asserts the gate result and the
promotion path see an unchanged FAIL. A test asserts an acknowledgement on one
comparison has no effect on a second comparison with the same behavioral
difference. Migration and rollback are drilled as 066's were.

**Privacy and risks.** Annotation text is the first free-text field a human
writes into Trustvian's durable state, which makes it the first place a human can
paste a prompt. It is bounded, rendered inert, and the specification must state
that Trustvian does not inspect it — but it cannot claim the field is
content-free, and it must not. The material risk is governance drift: an
acknowledgement mechanism with no access control is a mechanism anybody can use
to make a red gate look handled, which is the argument for sequencing this after
070 rather than before it, or for scoping the first slice to a single-developer
local deployment and saying so.

### 089 — PROPOSED: optional quality evaluation and prompt experimentation

**Placement:** undecided. **Depends on:** a decision, then 085 and 086.
**Gate:** none. **Status: PROPOSED — this contradicts a currently accepted
product boundary and is not approved.**

**What it would be.** An extensible evaluator interface — deterministic checks
first, model-based judges as a strictly optional second kind — producing a score,
a label, an explanation and its own configuration provenance, kept in a separate
namespace from behavioral findings, with explicit composition if a gate is ever
allowed to read one. A prompt playground would come later still, and only after
the prerequisites below are defined.

**Why it is recorded here rather than planned.** Because the repository already
decided against it, in writing, and a planning task must not quietly reverse an
accepted boundary. ROADMAP § What Trustvian is not becoming states that "a prompt
playground, prompt versioning, output-quality scoring, and model benchmarking are
outside the product, not merely unscheduled", and lists "prompt management and
playgrounds, LLM-as-a-judge, hallucination and groundedness scoring, RAG
relevance metrics" among what stays outside. Task 078 repeats the refusal for its
own scope. Those are decisions, not omissions, and the reasoning given for them
is specific: answering "is this model any good" would blur the question the
detection engine is built for.

**What would have to be decided first.** Five things, none of which this task
decides:

1. **Whether the product boundary moves at all.** If it does not, this item is
   closed rather than deferred, and that is a legitimate outcome.
2. **Whether a quality score may ever reach a gate.** If it may, gate
   composition becomes explicit and ADR 0029's integer-evidence reasoning has to
   be revisited for a score that is inherently a float. If it may not, quality
   is reporting only — which is a much smaller and much safer item.
3. **Content availability.** A model-based judge needs the prompt, the completion
   or the tool result. Trustvian refuses all three at every durable and published
   surface. So this item cannot be specified before the content-capability
   requirements in [§ Privacy prerequisites](#privacy-prerequisites) exist, and
   it must never be described as available when the content it needs is not.
4. **Judge fallibility.** A model judge's output is evidence from a fallible
   instrument, and it would have to be recorded as such — with its model,
   version, configuration and prompt digest — rather than as a measurement.
5. **Replay side effects.** A playground that replays an agent or tool execution
   would re-issue whatever that execution did. Replaying `send_email` sends
   email. No replay capability may be specified before the mocked or sandboxed
   execution boundary is, and "the developer will be careful" is not a boundary.

**Non-goals even if it is approved.** No model ranking, no provider comparison,
no benchmark leaderboard, no hallucination or groundedness metric presented as
ground truth, no replay against live external effects, and no merging of quality
scores into behavioral findings. The separation is the point: a behavioral
finding is deterministic and reproducible, and a judge's score is neither.

**Relationship to existing tasks.** Contradicts ROADMAP § What Trustvian is not
becoming as written today. Would extend 086's versioning to evaluator
configuration. Is *not* a prerequisite for anything: every other item in this
task is independent of it, deliberately, so that a decision to close 089 costs
nothing already built.

### 090 — Optional: trace-backend interoperability

**Placement:** **optional integration**, demonstrated by the reference
deployment. **Depends on:** nothing. **Gate:** none.

**Developer problem.** A developer who wants a full trace viewer beside
Trustvian's behavioral evidence has to work out the Collector wiring themselves,
and a developer who already runs a trace backend has no documented answer to
"does adding Trustvian mean re-instrumenting or moving my traces".
The roadmap's answer is no, and nothing demonstrates it.

**Current state, verified.** The Collector processor already sits in a pipeline
and passes spans through, enriched; fan-out to a second exporter is an ordinary
Collector configuration and the roadmap states the fan-out "is deliberate and
stays". `trustvian dev` generates a Collector configuration from a template. No
documented recipe, compose profile or worked example exists for exporting the
same spans to a trace backend.

**Scope.** A documented recipe and a reference-deployment profile that fan the
same OTLP stream out to Trustvian and to a trace backend. The worked example is
chosen at specification time for being self-hostable in one process and for
reading the same OpenInference attributes Trustvian reads, so neither side is
adapted to the other; **no backend is privileged**, and choosing a different one
costs a paragraph. Documentation of what each side answers, so a reader does not
expect Trustvian to render a waterfall or a trace backend to produce a gate.

**Export policy is part of the scope, not a footnote.** A fan-out forwards spans,
and the spans a producer emits may carry prompts, completions, tool arguments and
tool results — the attributes Trustvian itself refuses to read or store. So the
integration specifies, as requirements:

- **Disabled by default.** No profile, template or generated Collector
  configuration enables a second exporter unless an operator turns it on.
- **Enabling it names the destination explicitly**, and names the export mode.
  There is no inferred destination and no default-on mode.
- **Two modes, and the difference is stated where it is configured.** A
  `metadata-only` mode requires a filtering step *before* export, so
  content-bearing attributes never reach the destination. A `full-span` mode
  forwards what the producer emitted, content included, and is an explicit
  operator opt-in whose privacy consequence is documented at the point of
  configuration rather than in a separate document.
- **Neither mode changes Trustvian's own persistence.** What Trustvian reads,
  fingerprints, records and stores is unaffected by either — that guarantee is
  about Trustvian's pipeline and durable state, and it is not a guarantee about
  what a separate exporter sends somewhere else.

**Nothing above exists today.** The Collector supports fan-out; **no Trustvian
filtering, redaction or metadata-only export processor exists**, and this item
must not be described as though one does. If `metadata-only` is offered, the
filter is part of this item's implementation and its tests; if it is not offered,
the item ships `full-span` only and says so plainly.

**Non-goals.** **No runtime dependency**, nothing bundled, no vendored code, no
client library, no required configuration, and no behavioral capability that
degrades when the trace backend is absent. No imported source: the mature backends
in this category are commonly source-available rather than OSI open source, and
this repository is Apache-2.0. No claim of feature parity in either direction.
**No claim that forwarding previously-received spans is inherently content-free**
— it is not; the span is whatever the producer emitted. See
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md).

**Dependencies and architectural impact.** None on the engine or the platform. It
follows the adapter shape ADR 0003 established and the fan-out the Collector
already supports. Its entire cost is documentation plus a deployment profile,
which is why it sits at the end of the list rather than the beginning: it is
cheap, and nothing depends on it.

**Acceptance criteria.**

1. A documented configuration exports one OTLP stream to both Trustvian and a
   trace backend, and the worked example runs.
2. Removing the trace backend changes no Trustvian result and produces no error.
3. No Trustvian module gains a dependency, and no non-Apache-2.0 code enters the
   repository, proven by the existing module and dependency checks.
4. The documentation states which question each side answers and claims parity
   with neither.
5. **Off by default.** A default install, a default reference-deployment profile
   and a `trustvian dev`-generated configuration each produce **no** second
   exporter, proven by a test asserting the generated configuration declares none.
6. **Enabling it requires an explicit destination and an explicit mode.** A
   configuration naming a destination without a mode, or a mode without a
   destination, is refused with an error naming the missing field — not
   defaulted, since the permissive default would be the content-bearing one.
7. **Trustvian's own persistence is unchanged in every mode**, proven by a test
   that runs the same workload with the exporter off, in `metadata-only` and in
   `full-span`, and asserts byte-identical Trustvian evidence in all three.
8. **If `metadata-only` ships, filtering happens before export and is proven by
   sentinel test.** Distinctive sentinel values are planted in every content
   attribute `internal/semconv/content.go` enumerates, and a test asserts none
   reaches the destination — asserted at the destination, not at the filter's
   own output, and verified to catch a deliberately disabled filter.
9. **If `full-span` ships, it is opt-in and its consequence is documented where
   it is configured.** The documentation states that the destination receives
   prompts, completions, tool arguments and tool results when the producer emits
   them, and a test asserts the configuration surface carries that statement.
10. **No filtering or redaction is claimed that does not exist.** Every privacy
    statement this item makes names either a shipped filter with a passing
    sentinel test, or a future requirement marked as one.

**Validation.** The reference deployment's existing smoke test is extended to the
new profile. A test asserts the absence of the second exporter is inert, and a
second asserts the default configuration has none. The module and boundary scripts
already in CI cover the dependency criterion. The sentinel suite for
`metadata-only`, if that mode ships, follows task 075's pattern: a planted leak
must fail the test, or the test proves nothing.

**Privacy and risks.** Stated precisely, because an earlier draft of this item got
it wrong in the dangerous direction — it claimed "no content leaves Trustvian's
boundary as a result of the integration" while its own risk paragraph said raw
spans including prompts and tool arguments reach the second backend. Both cannot
be true. The accurate statement separates two different guarantees:

| Guarantee | Holds | Scope |
|---|---|---|
| Trustvian does not read, fingerprint, record, publish or persist content | **Yes, today** | Trustvian's pipeline and durable state (task 075, ADR 0030) |
| A second exporter sends no content to another backend | **Only in a `metadata-only` mode that filters before export** | The exporter, which is not Trustvian's pipeline |

"Trustvian exports no span it did not receive" is a true statement about
provenance and **not** a privacy guarantee: the span is whatever the producer
emitted, so forwarding it forwards its content. A fan-out is therefore not
privacy-neutral for the operator, which is precisely why it is off by default, why
enabling it names its mode, and why the content-bearing mode is an explicit opt-in
rather than the path of least resistance.

Two further risks. **A `metadata-only` mode is a promise that has to be kept at
the destination**, not at the filter — an attribute the filter's allowlist missed
is a leak, so the test asserts at the receiving end. And **a filter is a security
control that can silently stop working**; the sentinel suite must be verified to
fail when the filter is disabled, or it is decoration.

## Delivery order

Dependency-derived, not preference-derived. The `v1.0` gate keeps every
criterion it has; nothing here is added to it except where an item changes what
an existing criterion means.

```text
  already implemented
    075  semantic telemetry ──────────────────────────────┐
                                                          │
  phase A — semantic telemetry foundation                  │
    083  behavioral layer identity and display ◀───────────┤
    084  correlation and operational evidence ◀────────────┤
    081  persist behavior fidelity  (already reserved) ◀────┘
            │
            │  083 must precede 078's threshold re-run,
            │  or the re-run measures the double count
            ▼
  phase B — inspection and evidence-linked comparison
    067  event-history capability boundary   [pre-existing, unspecified]
            │   specify 085's needs alongside it, not after it
            └──▶ 085  evidence resolution: findings ─▶ observations
                      │     control plane + /v1 + CLI; ships without a browser
                      └──▶ 076  evidence explorer  (extended: tree, timeline,
                                     provenance, navigation over 085)

  phase C — repeatable experiments
    078  behavioral scenario suites   [pre-existing, specified]
            └──▶ 086  scenario and input versioning

  phase D — performance, cost and review
    084 ──▶ 087  performance and cost evidence
    085 ──▶ 088  review decisions and annotations   (also needs 066 ✓,
                                                     and 070 for authorship)

  phase E — proposed, undecided
    089  optional quality evaluation and prompt experimentation
            requires a product-boundary decision first; closing it is a
            legitimate outcome and costs nothing above

  anytime, independent
    090  trace-backend interoperability   (documentation + deployment profile)
```

**085 precedes 076, and the edge runs one way.** An earlier draft of this task
had 076 depending on 085 for navigation and 085 depending on 076 for
presentation, which is a cycle: neither could be completed first. The resolution
capability is authoritative at the control plane, so it owns its own `/v1` route,
its CLI access and its validation, and it is shippable before any view consumes
it. Browser navigation — *reaching a finding's observations in one step, in a
browser, without typing an identifier* — is 076's acceptance criterion, and the
pull-request rendering is 079's. The invariant survives the split unchanged: the
control plane owns resolution, and no adapter recomputes a finding, a count or a
verdict.

Three deviations from the order the brief suggested, each with its reason:

**083 comes before everything, including the inspection work.** The brief put
trace inspection immediately after the telemetry foundation. But 083 changes what
a *behavior* is, and therefore what a diff counts, what a k-of-N presence count
means, and what an evidence link resolves to. Building inspection and linking
first means building them against a counting rule that is known to be wrong and
then revising both. It also has a deadline the other items do not: 078's threshold
measurement is waiting to be re-run at tool-name fidelity, and re-running it
against the double count would produce a number nobody should write down.

**084 is separated from 083 rather than bundled with it.** They both touch the
telemetry-to-record path, so bundling looks natural. They have opposite risk
profiles: 084 is purely additive and can land at any time, while 083 may require
a baseline migration. Task 081 was separated from 075 for exactly this reason, and
the same separation applies — an additive change should not wait on a migration
decision.

**085 is *specified* alongside 067 and *delivered* before 076.** Two separate
orderings, and conflating them is what produced the cycle this task corrected.
Specification: 076 documents the mistake to avoid — a presentation task that
discovers its storage layer retained too little has to amend itself — and
evidence resolution has sharper retention requirements than presentation does,
since it needs a stable, resolvable identity for a finding and for an observation,
so 067 should choose its contract knowing them. Delivery: 085 is authoritative,
owns its `/v1` route and CLI access, and ships validated without a browser; 076
then consumes it. The edge is one-way.

And one thing the diagram deliberately does not say: **it does not extend the
`v1.0` gate.** Of the eight items, two are gate items (083, 084) because they
change what existing criteria 14, 17 and 12 mean; 085 is a gate item only through
criterion 17's "and explained", which 076 already carries. The rest are
post-preview work, a proposal, or documentation.

## Privacy prerequisites

**Content-free observation stays the default, and no item above changes that.**
The principle is unchanged: *behavioral observability does not require content
observability*. Items 083 through 088 are all specified to work from metadata,
and each carries its own tripwire requirement.

Two items would need content that Trustvian refuses today: a model-based judge
(089) and any replay of a recorded execution (089's playground). Neither may be
specified until the following exist as their own reviewed capability — **not as a
paragraph inside a feature task**:

| Requirement | What has to be decided |
|---|---|
| **Collection-time filtering** | Which content attributes may be captured at all, per deployment, refused by default and enabled explicitly — never "capture everything and filter on read" |
| **Redaction** | What is removed before anything is written, how a redaction failure behaves (it must fail closed), and what is provably unrecoverable afterwards |
| **Retention** | How long content lives, separately from and never longer than metadata retention, and what enforces the bound |
| **Access control** | Who may read captured content — which requires task 070, because there is no authentication to scope it to today |
| **Deletion** | How an operator deletes captured content for a run, an actor or a subject, with proof it is gone from every backend and every backup path |
| **Disclosure** | How a developer discovers that content capture is on, in the product rather than in a document |

Until those exist, content-enabled capabilities are not "later" — they are
unspecifiable, and the honest description is that the required content is
unavailable. **No item above may be described as delivering a content-based
capability partially.** A quality evaluation that silently degrades to scoring
nothing, or a replay that silently replays without inputs, is worse than its
absence.

One consequence worth stating: item 090's **full-span** mode fans the operator's
spans out to a second backend, content included. That is not a Trustvian content
capability — Trustvian neither reads nor stores those attributes — but it is a
real disclosure obligation, which is why the mode is off by default, why enabling
it names the destination and the mode explicitly, and why only a
**metadata-only** mode that filters before export may claim to send no content.
No such filter exists today; it is one of 090's requirements, not a capability to
describe as present.

## Storage, scale and completeness

Three cross-cutting assessments the items above depend on and do not own.

**Volume and indexing belong to 067, and this task sharpens its inputs.**
Retaining per-observation history is a different storage profile from the bounded
per-run aggregate the platform holds today: one run of a chatty agent produces
thousands of observations where it currently produces one aggregate and a capped
behavior set. 085 needs those rows indexed by run, by behavioral identity and by
trace identity; 076 needs them ordered by time within a session. Both access
patterns should be stated to 067 *before* it chooses a contract, and both point
at the same conditional already in the roadmap: task 068's analytical backend
arrives if measured volume justifies one, and this task does not make it a
prerequisite. Pagination follows the repository's single established shape —
immutable-key keyset cursor, bounded page, exclusive `after`, continuation only
when another row follows — and every new route in every item above inherits it
rather than inventing a second.

**Migrations stay forward-only and drilled.** 084, 086 and 088 each add
persisted state, and 083 may change behavioral identity. The schema chain is at
**5**; each item owns one forward step, in the order it lands, and states it in
its own specification rather than assuming a number. 081's deferral is the
precedent for why: a migration bug damages a user's database, which is why it was
separated from 075 rather than folded in.

**Missing telemetry must not read as a passing gate.** This is the most
security-relevant of the three. Every gate above counts observations, and fewer
observations means fewer added behaviors — so a sampled trace pipeline, a dropped
batch, a crashed repetition or an agent that failed to start all push a gate
toward PASS. The mechanism for this already exists and must be extended rather
than re-invented: 056's gate carries `referenceEvidence` and `candidateEvidence`
as **minimum**-count checks, and 078 states that minimum-evidence gates cannot be
configured away. Three requirements follow for the items above:

1. Any new comparison or summary states the evidence it rests on, and a
   comparison with insufficient evidence FAILs rather than passing quietly.
2. Sampling is disclosed. If a deployment samples, the ratio is recorded on the
   run or the run declares it unknown — a gate computed over sampled evidence
   must not be presented as a gate computed over all of it.
3. An incomplete or aborted run is a distinct state from a clean run with a small
   behavioral surface, and no comparison may treat the two alike. 078 already
   aborts a scenario on a crashed repetition; the same explicitness applies to a
   truncated trace stream.

## Build versus integrate

The brief asked whether to consume existing instrumentation or maintain bespoke
integrations. The answer this repository already reached is *consume*, and the
evidence now supports it more strongly than when it was decided.

**Consume conventions; never name a framework.** ADR 0045 decided this and
`scripts/check-platform-boundary.sh` enforces it: Trustvian reads OpenTelemetry
GenAI and OpenInference, names no agent framework in non-test source, and pays a
bounded maintenance cost — one package, one table. The measured alternative is
unbounded: a per-framework integration list is never finished. Nothing in items
083 through 088 requires a framework integration, and 087's token counts come from
the same two conventions rather than from a provider SDK.

**Where the conventions are thin, say so rather than inventing.**
`docs/OPENTELEMETRY.md` already documents the four OpenInference span kinds
Trustvian does not map and why. Two of them are worth revisiting on their own
merits rather than for completeness: `GUARDRAIL` is behavioral security evidence a
security product should arguably read, and `PROMPT` would matter to 086's prompt
reference. Both are blocked on the same thing — the specification defines no
identity attribute for either — and neither is claimed by an item above. That is
a deliberate absence, recorded here so it is not rediscovered as an oversight.

**Integrate with trace backends; depend on none.** Item 090, and
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)
for why. The licensing asymmetry is decisive on its own — Apache-2.0 here,
source-available there — and the architectural reason stands independently: a
behavioral gate that required a trace backend to be running would make a security
decision depend on an observability deployment.

**Three relationships, kept distinct.** The brief asked for this distinction and
it is worth writing down, because conflating them is how a comparative reading
turns into a copied product:

| Relationship | What it permits | What it costs |
|---|---|---|
| **Conceptual inspiration** | Reading another system to recognize a gap — evidence links, version provenance, evaluator provenance | Nothing, and it is what this task did |
| **Interoperability** | Exchanging OTLP with another system, in either direction | A documented recipe; item 090 |
| **Code reuse** | Importing or vendoring another implementation | Not permitted here. Source-available licensing is incompatible with this repository's Apache-2.0 distribution, and no item above needs it |

No code was read for reuse in producing this task, and no item above depends on
any.

## Open decisions

Five, and three of them block a specification rather than an implementation.

1. **Does the product boundary move for quality evaluation?** ROADMAP § What
   Trustvian is not becoming currently says no, with reasoning. Item 089 cannot be
   specified until this is answered, and **closing it is a legitimate answer** —
   nothing else in this task depends on it. *Blocks 089.*
2. **Does behavioral identity change for the layer rule, or only the label?**
   Folding a tool span and its transport span into one fingerprint fixes the
   measured double count and costs a baseline migration — and loses the transport
   target from behavioral identity, which is a detection regression an attacker
   could use. Keeping identity and adding a non-identity label costs nothing and
   fixes only the display half. *Blocks 083's specification.*
3. **Is there a `model` operation category?** Same trade-off, smaller: `external`
   currently means both "a model was consulted" and "a document store was
   queried". A new category is behavioral identity and resets baselines; a
   non-identity classification does not. *Blocks 083's specification.*
4. **May a quality score ever reach a gate?** If yes, gate composition becomes
   explicit and ADR 0029's integer-evidence reasoning must be revisited for a
   float. If no, 089 shrinks to reporting only. *Blocks 089's scope.*
5. **Does 088 wait for 070?** An acknowledgement mechanism with no access control
   lets anyone mark a red gate as handled. Either 088 follows 070, or its first
   slice is scoped to a single-developer local deployment and says so. *Affects
   088's sequencing, not its design.*

Decisions 2 and 3 are the ones that warrant an ADR when they are made; this task
deliberately does not pre-empt them, because
[ADR 0019](../../adr/0019-bounded-fingerprint-admission.md)-style identity
decisions are exactly the kind this repository records rather than arrives at.

## Tests

This task implements nothing, so it has no tests of its own. What it commits to
is that each item's specification carries, before implementation:

- a regression fixture reproducing the measured problem, where a measurement
  motivated the item (083, 085);
- a privacy tripwire over every new route and rendering (085, 086, 087, 088);
- a degradation assertion that a producer emitting less telemetry sees
  byte-identical behavioral results (083, 084, 087);
- a backend-equivalence assertion for anything persisted (084, 086, 088);
- an assertion that no new field reaches `StableFeatures`, a fingerprint or a
  baseline key (083, 084, 086, 087);
- an assertion that no adapter computes a verdict, count or score the control
  plane owns (085, 088), which 076 and 079 inherit as consumers.

## Documentation

Written by this task: `docs/ROADMAP.md`, this file,
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md),
the ADR index, the task index, and the amendments to
[076](076-behavioral-evidence-explorer.md),
[078](078-behavioral-scenario-suites.md) and `docs/OPENTELEMETRY.md` that record
the relationships and the documented gaps.

Written by each item's own implementation, not here: `DOMAIN.md`,
`ARCHITECTURE.md`, `SECURITY.md`, `webui.md`, `compatibility.md` and
`CHANGELOG.md`. Documenting a capability before it exists is what this
repository's status vocabulary exists to prevent.

## Acceptance criteria

This task is complete when:

1. Every claim in [§ Current state](#current-state-verified-against-main) is
   verifiable by reading the named source, and none restates a previous
   document's description of it.
2. Items 083–090 are numbered, scoped, sequenced and reachable from
   `docs/ROADMAP.md`'s dependency order and task table.
3. Each item states its developer problem, current state, scope, non-goals,
   dependencies, acceptance criteria, validation strategy, privacy implications,
   material risks, relationship to existing tasks, and placement.
4. No item is presented as accepted direction when it contradicts an accepted
   boundary; 089 is marked `PROPOSED` and the contradiction is named.
5. No existing task's status, scope or non-goals are silently changed. 078's
   non-goals in particular are unchanged, and the amendment says so.
6. Existing task history and completion status are preserved exactly.
7. No implementation, dependency or application-code change is introduced.
8. The `v1.0` exit criteria are unchanged in number and force.

## Open questions left to the items themselves

1. **Whether 067's retention proves sufficient for 085**, which is the one
   question that could narrow evidence linking materially — and the reason 085
   should be specified alongside 067 rather than after it.
2. **Whether `GUARDRAIL` and `PROMPT` span kinds get an identity rule**, which
   the conventions do not currently supply.
3. **Whether latency comparison is meaningful for a model-driven agent**, where
   the model dominates the measurement. 087 must either establish that it is or
   present latency as descriptive rather than attributable.
4. **What a scenario-definition digest covers** — the file alone, or the file
   plus the inputs it names. 086 assumes both, separately.
