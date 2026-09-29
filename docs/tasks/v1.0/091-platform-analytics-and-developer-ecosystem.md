# 091 — Platform Analytics and Developer Ecosystem

Status: specified. **Documentation and planning only; implements nothing.**
Milestone: **post-`v1.0` platform depth.** No item here is a `v1.0` release
gate, and the `v1.0` exit criteria are unchanged in number and force
Depends on: the investigation chain being in place —
[067](067-event-history-capability-boundary.md),
[084](084-correlation-operational-evidence.md) and
[085](085-evidence-resolution.md) (all implemented), with
[076](076-behavioral-evidence-explorer.md) specified
Blocks: nothing. It reserves and scopes 092–095, and authorizes no code
Planned in the shape of: [082](082-agent-inspection-and-evaluation-depth.md),
which is the model this task follows

## Objective

Name the capabilities that become valuable once the current observation,
retention, investigation, comparison and decision foundations are in place —
with finding review already scoped by 088 — and sequence them without building
them.

**Implemented today:** observe (074), retain (067), explain (085), compare
(054), gate (056). **Scoped, not implemented:** review (088), and the explorer
(076) that presents all of it. This task names nothing as shipped that is not.

082 decomposed one sentence: *inspect what your agent did, understand what
changed, make release decisions using evidence.* That sentence is now largely
answered. A developer can watch a run live, retain its observations, compare two
runs, gate the difference, and follow a failed check to the observations behind
it.

What that developer still cannot do is ask a question about **more than one
run**:

```text
Which agents observed new behavior most often?            run-native
Which targets appeared for the first time this week?      run-native
Where are block decisions increasing?                     run-native
Which agents have incomplete behavioral evidence?         run-native

Which environments are producing gate failures?           needs a durable
How often is this behavior added by a candidate?          comparison context
```

The first four are questions about evidence Trustvian already holds durably, and
cannot be asked. **The last two are not the same kind of question** — they are
about comparison decisions, and a normal comparison is not persisted. 092 treats
that difference as its central constraint rather than a detail; see
[§ Three evidence classes](#three-evidence-classes-because-they-are-not-equally-durable).

That gap — plus three adjacent ones this task names — is what 092–095 reserve.

## Why now

Not because a capability list was copied from somewhere. Because three things
became true at once, and each is checkable in this repository:

1. **There is authoritative evidence worth aggregating.** Before 067 there was
   no per-observation history; before 085 there was no path from a finding to
   it. Aggregating a reduction of a reduction would have produced numbers
   nothing could explain.
2. **The `/v1` surface crossed the line into a developer platform.** It is 34
   routes today (§ Current state), across seven entity families, with no
   machine-readable description and no typed client.
3. **The investigation a developer performs is now lossy at the end.** 085 makes
   a finding resolvable and the resolution URL citable; nothing preserves the
   context a reviewer would need to arrive at the same place.

## Product boundary

Unchanged, and this task strengthens rather than moves it.

> Trustvian evaluates **behavior**. It learns how an actor behaves, detects
> behavioral change and anomaly, links that change to evidence, and turns the
> evidence into deterministic security and release decisions.

Every item below is a question asked of *that* evidence. None of them asks
whether a model's output was any good, which is the boundary
[§ What Trustvian is not becoming](../../ROADMAP.md#what-trustvian-is-not-becoming)
draws and this task does not touch.

The single sharpest way to state it for analytics specifically:

```text
generic product analytics asks     how are people using this application?
LLM evaluation analytics asks      how good are this model's answers?

Trustvian analytics asks           which actors changed behavior, where,
                                   how often, and what did the evidence
                                   decide about it?
```

## Current state, verified against `main`

Read from source at `71a5f24`, not from an earlier document's description of it.

| Claim | Verified |
|---|---|
| No cross-run aggregate query exists | **True.** Every `ControlPlane` read is scoped to one project, agent, candidate, run, or one comparison. There is no "count by" capability at any layer |
| The `/v1` surface is 34 routes | **True**, across projects, agents, candidates, environments, evaluation runs, promotions, evidence resolution and realtime |
| No machine-readable API description exists | **True.** No OpenAPI or equivalent document is present in the repository |
| No typed client exists in any language | **True.** The CLI is the only first-party consumer, and it is a hand-written HTTP adapter (ADR 0033) |
| Core alerting exists and is generic | **True.** `alert` holds `Alert`, a condition evaluator and a webhook sink. `Sink` is exported so code this module does not control can implement it (ADR 0007) — but it is **typed on `Alert`**, which describes one behavioral decision and carries `Actor`, `Target`, `FingerprintID` and `AnomalyScore` |
| No alert rule evaluates platform evidence | **True.** `alert` is strictly downstream of one `Result`/`Decision`. Nothing evaluates a condition over an evaluation run, a gate result, a promotion or retained history |
| No saved-view or investigation concept exists | **True**, at any layer — no route, no store, no browser state |
| The WebUI has no analytics surface | **True.** Its assets are the live graph, the inspector, the rail and discovery |
| Retained history is bounded and states its own completeness | **True.** 4096 observations per run, with `complete` / `partial` / `unavailable` carried through `/v1` |
| A finding resolves to its evidence | **True**, since 085: two bounded `GET` routes and a `trustvian evidence` command family |

## What mature agent-observability platforms teach us

The mature systems in this category — agent trace-and-evaluation backends that
store spans, render waterfalls and score model output, described as a category
here for the reason
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)
describes it as one — converge on a small set of platform capabilities that are
*not* about model quality:

```text
aggregate views over stored evidence
rules that notify when a condition holds
a described, versioned API with generated clients
saved, shareable investigation context
```

Those four are worth learning from because they are **surface-independent**:
they are what any system accumulating evidence eventually needs, whatever the
evidence is about. The lesson transfers; the subject matter does not.

**Every item below is justified by a Trustvian user problem, not by parity.**
Where this task could not name the Trustvian problem, it did not reserve a
number — which is why there is no fifth item.

## What Trustvian deliberately does not copy

These are **not** new roadmap items, and naming them is the point:

```text
prompt registry                 prompt version management
prompt playground               generic dataset platform
LLM-as-a-judge                  hallucination scoring
groundedness scoring            RAG relevance scoring
model leaderboard               provider comparison
prompt / completion warehouse   generic product analytics
```

They answer *"is this model or application output good?"*. Trustvian's core
product answers *"what behavior occurred, how did it differ, was it anomalous,
and what evidence-backed decision followed?"* Those are different questions with
different evidence, and answering both would blur the one the detection engine
is built for.

**089 stays `PROPOSED`.** Optional quality evaluation remains the one item that
contradicts an accepted boundary, and this task does not promote it, soften the
contradiction, or route around it by introducing quality metrics under another
name.

Infrastructure is not copied either. **No roadmap dependency on Redis, S3,
Kafka, ClickHouse, a worker queue or Kubernetes is created here**, because
another system in this category uses them. The rule remains *measure first*:
[068](README.md) owns the conditional analytical-store decision and
[069](README.md) owns load and multi-node validation. **If PostgreSQL satisfies
measured requirements, no analytical store is added** — a page existing is not a
measurement.

## The four items

### 092 — Behavioral Analytics

**Placement:** core product, post-`v1.0`. **Depends on:** the authoritative
evidence that exists today; optionally enriched by
[087](082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence).
**Gate:** none.

**Developer and operator problem.** Trustvian can explain one evaluation
completely and cannot answer a question that spans several. A team running
nightly suites across a dozen agents has every fact needed to see that one agent
observed behavior marked new in six of the last ten runs, or that block
decisions against one environment tripled this week — and no way to see either.
The investigation always starts from a run identifier somebody remembered.

Both of those examples are deliberately **run-native**: each is a count over
evidence every run already retains. The questions that sound similar but are not
— *how many gate failures did this agent have* — are the ones
[§ Three evidence classes](#three-evidence-classes-because-they-are-not-equally-durable)
separates out.

**Scope.** A bounded analytical read model over already-authoritative platform
evidence — and the first thing its specification must settle is **which
evidence is authoritative for a historical claim at all.**

### Three evidence classes, because they are not equally durable

A count is only a historical fact if the thing it counts was durably recorded.
Trustvian's evidence divides into three classes on exactly that line, and 092
must keep them apart.

**A. Run- and observation-native analytics.** Aggregated from authoritative
retained run and observation evidence, which is durable by construction:

```text
agents observed                  evaluation runs
new and familiar behaviors       block decisions
critical-risk observations       incomplete evidence
newly observed targets           behavior, risk and decision counts
```

**B. Durable workflow analytics.** Aggregated from authoritative durable
workflow records. Promotion qualifies because [066](066-promotion-workflow.md)
records the decision **and the gate evidence it consumed**, field for field, at
decision time:

```text
promotion accepted               promotion rejected
```

**C. Comparison and scenario analytics.** Metrics such as **added behaviors,
removed behaviors, gate failures and repeatedly added behaviors** are historical
analytics **only when the underlying comparison or scenario decision is itself
durably identifiable and its decision-time context was retained.**

### Why C is not available today

**A normal `EvaluationComparison` is not persisted.** `CompareEvaluations`
derives the `BehaviorDiff`, the `EvaluationScorecard` and the
`EvaluationGateResult` on demand from stored run evidence, and **its gate limits
are supplied by the caller on every request.** No table holds a comparison, a
scorecard or a gate result (§ Current state).

So a claim like *"this agent had 12 gate failures last week"* cannot be
reconstructed honestly. Nothing recorded which two sides were compared, which
limits were applied, or which verdict was reached — and a limit is a caller's
acceptance policy, not a property of the runs. Re-running today's gate over
yesterday's runs with today's limits produces a number that was never anyone's
verdict.

```text
092 must never manufacture historical comparison facts by selecting arbitrary
run pairs, or by re-evaluating today's gate over historical runs.
```

**If no durable comparison or scenario context exists, the metric is
unavailable** — not recomputed and relabelled as historical truth. Two roadmap
items could supply that context: 066 already does for promotions, and
[078](078-behavioral-scenario-suites.md) may, if persisted scenario executions
and their results become durable records. Until one of them covers a metric,
class C stays unavailable and 092's specification says so.

### Enrichment

After 087 exists, optionally latency, errors, tokens and estimated cost —
**dependent on 087**, never re-derived here, and absent until it ships.

**Dimensions**, where authoritative data supports them: project, agent,
candidate, environment, behavioral profile, decision, risk level, behavior
layer, new-versus-familiar, and a time window. **No dimension is derived from
content**, which is not a restriction this item accepts so much as one the
schema already enforces — there is no content column to group by.

**Non-goals.** No prompt analytics, completion analytics, answer-quality
analytics, model or provider leaderboard, RAG metrics, or user-product
analytics. This is **behavioral security analytics**, not LLM product analytics.

**Architectural constraints.**

- **Analytics may compute bounded aggregate counts and groupings over
  authoritative stored evidence.** It must **not** recompute domain facts whose
  historical meaning depends on a decision-time context — gate verdicts, diff
  classifications, promotion outcomes, or any other historical decision. The
  line is:

  ```text
  aggregate existing facts        allowed
  reconstruct historical decisions forbidden
  ```

- **The WebUI must not compute platform-level aggregate truth by crawling raw
  observations.** Every number on an analytics surface comes from an
  authoritative server response — criterion 19's rule that no interface carries
  its own copy of engine logic applies with more force here, because an
  aggregate assembled in a browser would be a second engine that is also wrong
  about a page boundary.
- Storage stays SQLite local, PostgreSQL shared. An analytical store arrives
  only if 068's conditional is satisfied by measured query volume.
- Retention honesty propagates. An aggregate computed over partial or
  unavailable history must say so; 067 and 085 both refuse to let an absence
  read as a fact, and an analytics surface is where that guarantee is easiest to
  lose.

**Privacy.** No new surface: every metric is a count or a grouping over fields
already retained under 067's allowlist.

**Why it belongs in core.** Detecting, scoring and deciding on one deployment's
behavior is OSS by the existing boundary, and an aggregate over one deployment's
own evidence is the same question asked at a higher level.

### 093 — Behavioral Alert Rules

**Placement:** core product, post-`v1.0`. **Depends on:** existing authoritative
evidence and the existing `alert` package; **not** on 092. **Gate:** none.

**Developer and operator problem.** Trustvian notices things a human has to be
watching a screen to learn. A gate fails at 03:00, a candidate introduces a
behavior against a sensitive target, evidence becomes incomplete mid-run — and
nothing tells anyone until someone opens the WebUI.

**Scope.** Platform-level alert rules over platform evidence, reusing the
existing delivery and security *principles*. Candidate conditions, as
illustration only:

```text
new behavior observed at critical risk
first behavior against a sensitive target
block decision observed
evaluation gate failed
behavior evidence became incomplete
candidate introduced more than N behaviors
unexpected behavior in a production environment
promotion rejected
repeated high-anomaly observation with mature evidence
```

**The rule language is not designed here.** 093 gets its own specification, and
choosing a condition vocabulary before the analytics questions are real is how a
query language ends up on a wire contract.

**Delivery model, and the boundary 091 deliberately does not decide.**

The generic webhook stays the preferred generic delivery mechanism, and **core
packages must not import a service-specific SDK merely to send a notification**.
Both survive whatever 093 chooses.

What 091 must **not** settle is that platform alert rules reuse `alert.Sink`
directly. The existing boundary is typed on the core alert:

```go
type Sink interface {
    Send(ctx context.Context, a Alert) error
}
```

and `alert.Alert` describes **one behavioral decision** — `Severity`,
`Decision`, `Risk`, `TrustScore`, `AnomalyScore`, `Actor`, `Target`,
`FingerprintID`, `Reasons`. A platform condition such as *evaluation gate
failed*, *promotion rejected*, *behavior evidence became incomplete* or
*candidate introduced N behaviors* has no actor, no target, no fingerprint and
no anomaly score. **Forcing one into that type would require fabricating those
values**, which is exactly the kind of invented evidence this repository refuses
everywhere else.

So the correction is a deferral, not a design:

> 093 should reuse existing notification delivery and security principles, and
> generic webhook mechanics, **where structurally valid**. The semantic
> notification domain boundary is decided by 093 itself, once a second real
> notification producer exists.

That is the repository's *no speculative abstraction* rule applied literally —
an interface arrives with its second implementation, not before it. A future
design might conceivably look like this, and **091 does not freeze it**:

```text
core behavioral Alert  ──┐
                         ├──▶  shared delivery transport
platform Notification  ──┘
```

**093 must explicitly evaluate three questions**, and none of them is answered
here:

- whether `Alert` and `Sink` remain core-specific;
- whether a shared lower-level delivery abstraction is justified by the second
  consumer, or whether two producers with their own sinks is simpler;
- how a platform notification is represented **without fabricating behavioral
  fields**.

**The semantic distinction that governs this item.** An alert is a
**notification about evidence**. It must not change a gate verdict, a policy
decision or a promotion state, and it never becomes an enforcement point. The
roadmap principle is *evidence, not verdicts*; an alert that could change an
outcome would make the notification path a second policy engine, and a flaky one.

**Non-goals.** No alert that mutates state. No condition over content. No
per-event alerting that duplicates the core `alert` package — this evaluates
*platform* evidence, which the core package by design knows nothing about.

**Privacy.** A notification carries what the retention contract already
publishes. A webhook body is an export surface and inherits the same allowlist
discipline, which its specification must assert by test rather than by review.

### 094 — Public Control-Plane API Contract and Typed Clients

**Placement:** developer ecosystem, post-`v1.0`. **Depends on:** nothing —
explicitly **not** on 092, 093 or 095. **Gate:** none.

**Developer problem.** `/v1` is 34 routes with a stable shape, documented in
prose and in `docs/compatibility.md`, and consumable only by writing HTTP calls
by hand. A team building tooling around Trustvian re-derives the same request
shapes, and nothing mechanically checks that the documentation and the handler
agree.

**Scope.** Evaluate and specify:

- an OpenAPI description of `/v1`;
- machine validation that the description and the implementation agree;
- generated or contract-tested clients, with Go, Python and TypeScript as the
  candidates.

**Three decisions belong to that specification and are deliberately open here:**
generated versus hand-written thin clients; which surfaces are stable enough to
describe; and whether the realtime SSE stream belongs inside the description or
beside it. **No commitment to three SDKs is made by reserving this number.**

**Why Python matters most.** Agent tooling is overwhelmingly Python, and a
control-plane client would let a developer drive evaluations, read evidence and
resolve findings from the environment they already work in.

**The distinction this item must not blur:**

```text
application under observation      must not need Trustvian
developer / control-plane tooling  may call Trustvian /v1
```

**OpenTelemetry is how workloads integrate. The control-plane API is how tools
integrate.** A Python client is tooling, not instrumentation, and this item must
not become a reason to add a Trustvian dependency to an observed workload.

**Non-goals.** No agent SDK a developer embeds in their application. No
replacement for OpenTelemetry ingestion. No moving engine functionality behind
HTTP for SDK symmetry — the engine is a library and stays one.

**Why it belongs in core.** A described API is part of *Integrate*, which the
OSS boundary already lists.

### 095 — Saved Investigations and Shareable Evidence Views

**Placement:** core product, post-`v1.0`. **Depends on:**
[085](085-evidence-resolution.md). **Gate:** none.

**The dependency runs one way, and the direction matters** because 076 is a
`v1.0` item and 095 is not:

```text
085 ──▶ 076
085 ──▶ 095
          └──▶ future extension of the Evidence / 076 surface
```

**076 does not depend on 095**, and 095 is not a `v1.0` prerequisite. After 095
ships, the existing Evidence surface 076 builds is the natural browser consumer
of a saved investigation — an extension of something already shipped, never a
precondition for shipping it.

**Developer problem.** 085 made a finding resolvable and its URL citable. What a
developer builds during an investigation — this comparison, this failed check,
this behavior, these three observations, these filters — still evaporates. A
reviewer asked to look gets a run identifier and reconstructs the path by hand,
which is the same restart 085 removed one level down.

**Scope direction.** A bounded saved investigation that references authoritative
evidence rather than copying it. It may reference a project, a comparison, a
finding identity, the runs involved, a selected behavioral identity, a selected
observation, active filters and a view mode.

**The governing principle:**

```text
a saved investigation stores navigation and context,
not duplicated findings or observations
```

**Aged-out evidence must be stated, never substituted.** If retention no longer
holds what the investigation referenced, opening it says so. It must not
silently resolve to a different finding, and it must not present a reconstructed
approximation as the original — which is 067's and 085's honesty requirement
applied one layer up.

**Shareability.** Stable identifiers such as `/investigations/{id}` are worth
evaluating. **DOM routes and CSS selectors do not become compatibility
contracts**; the machine truth stays `/v1`.

**Non-goals.** No duplicated event store. No browser-local authority. No saved
prompt or completion content. No editable copy of a gate result. No
browser-generated finding.

**Privacy, and the surface this honestly introduces.**

A saved investigation stores references, not evidence. But it is **persisted
state**, and calling it "no retention surface" would be wrong:

> **095 introduces a bounded durable metadata and reference surface.** It does
> not duplicate observations, findings, gate results, behavioral evidence or
> content.

What it may persist is a project reference, a comparison reference, a finding
identity, run references, a behavior reference, an observation reference, active
filters, a view mode, and its own identity and timestamps. That is new durable
state with its own lifecycle, and the specification owes it the same answers
every other persisted thing in this repository has had to give.

**095's specification must settle, and 091 deliberately does not:**

- retention and lifetime;
- a maximum number of saved investigations;
- a maximum size for stored references and filter state;
- deletion semantics;
- behaviour when referenced evidence has aged out;
- schema and migration behaviour;
- ownership and access semantics, once authentication exists.

The honesty rule survives all of them: **if referenced evidence has aged out,
the saved investigation says so.** It does not reconstruct an approximation and
present it as the original — which is 067's and 085's rule applied one layer up,
and the reason a saved investigation stores a reference rather than a copy in
the first place.

## Existing tasks that already own adjacent work

This task creates **no** duplicate of any of these, and each remains the owner.

| Task | Owns | Not re-opened here |
|---|---|---|
| [083](083-behavioral-layer-classification.md) | behavioral-layer identity and display classification | model/tool/transport classification; the tool-plus-HTTP counting fold |
| [084](084-correlation-operational-evidence.md) | correlation and operational evidence | parent span, duration, error and status |
| [085](085-evidence-resolution.md) | finding → behavioral identity → retained observation | generic "evidence links" |
| 086 | scenario, input and prompt-*reference* provenance | a prompt registry |
| 087 | latency, error, token and optional cost evidence | a second cost or usage capability |
| 088 | annotations and acknowledgements on findings | a second annotation queue; 093 notifies, it does not record review |
| 089 | **PROPOSED** optional quality evaluation | still `PROPOSED`, still contradicting an accepted boundary |
| 090 | trace-backend interoperability | a second trace-backend integration |
| [015](../015-trustvian-mcp.md) | Trustvian as an MCP server | **no new MCP number is allocated** |

### 015 — MCP, revisited rather than duplicated

Task 015 remains the owner of Trustvian's MCP surface, and this task
respecifies nothing in it.

The insight worth recording: **015 becomes materially more useful once 085
exposes stable finding and evidence resolution.** A read-only MCP surface would
then naturally expose authoritative queries — list evaluations, compare runs,
list changed behaviors, resolve a finding, get retained evidence, get trace or
session evidence, list gate results — none of which existed when 015 was
written.

Two directions for whoever picks it up, recorded here and decided there:

- **Read-only is the strong default for a first slice.**
- **Write capabilities** — promotion, acknowledgement, configuration mutation —
  require the authentication and authorization design that
  [§ Organizational scale](../../ROADMAP.md#organizational-scale) still holds,
  and must not arrive ahead of it.

## Two-level investigation model

This is the product direction that keeps every item above from drifting, and it
is worth stating once, plainly. Trustvian provides **two complementary views,
and the second serves the first.**

**Behavioral view** — what does this actor *do*?

```text
Agent
 ├─ tool → search
 ├─ tool → send_email
 └─ tool → export_customer   NEW
```

Fingerprint-centred. Answers: what is new, what changed, what is anomalous, what
decision followed.

**Execution evidence view** — what happened in *this execution*?

```text
trace
 ├─ model
 ├─ tool search
 │   └─ HTTP request
 └─ tool export
     └─ HTTP request
```

Trace- and span-centred. Answers: what called what, which observation was slow,
which errored, which span produced the behavior.

**The second supports the first. It does not replace it.** That sentence is the
whole defence against Trustvian becoming another trace viewer: the execution
view exists to explain a behavioral finding, and a feature that only makes sense
without a behavioral question attached is a trace tool's feature.
076, 084 and 085 own this foundation; 091–095 build on it and must not invert it.

## Delivery order

No cycles, and no artificial edges. **094 does not need 092. 093 does not need
095. 090 needs none of them.**

```text
existing / current
084 ───────────────┐
067 ─▶ 085 ─▶ 076  │
                   │
078 ─▶ 086         │
084 ─▶ 087         │
085 ─▶ 088         │
                   ▼
post-v1 platform depth
091  planning only
   │
   ├─▶ 092  behavioral analytics
   │         └─ optional enrichment from 087
   │
   ├─▶ 093  behavioral alert rules
   │         └─ consumes existing authoritative evidence
   │
   ├─▶ 094  public API contract and typed clients   [independent]
   │
   └─▶ 095  saved investigations
             depends on 085 only — 076 does not depend on 095;
             after 095 ships, the Evidence/076 surface may consume it

015  MCP — richer after 085; keeps its existing identity
090  trace interoperability — independent and optional
```

**None of 091–095 is a `v1.0` gate.** The `v1.0` journey, its exit criteria and
its execution order are unchanged by this task.

## WebUI evolution

The existing product model gains one row and loses none:

```text
Live          what agents are doing now
Evidence      why a behavior or finding exists
Evaluations   what changed, reference → candidate
Review        what a human recorded about a finding
Analytics     behavior, risk, gate and change trends over
              authoritative evidence                            (092)
Promotions    evidence-backed advancement decisions
Manage        advanced and manual control-plane operations
```

**Analytics is a view like every other row** — it renders server-computed
aggregates and carries no reduction of its own.

**Saved investigations are not a navigation row.** 095 supports Evidence,
Evaluations and Review rather than becoming a seventh destination, unless future
UX evidence justifies one.

## API and integration principles

Recorded here because 094 makes it easy to blur:

```text
agent / workload integration    OpenTelemetry
developer tooling               CLI · HTTP /v1 · future typed clients · future MCP
CI                              machine-readable evaluation result · GitHub Action
```

**A Python or JavaScript Trustvian SDK must never become a requirement for
observing an application.** The application under observation integrates through
OpenTelemetry and gains no Trustvian dependency — that is
[077](077-unified-otlp-local-dev-runtime.md)'s property and this task must not
erode it.

## Alerts connect analytics to action

The desired relationship, and the one it must not become:

```text
authoritative behavioral evidence        browser watches a chart
          ↓                                        ↓
bounded analytics / query                JavaScript decides a threshold crossed
          ↓                                        ↓
explicit alert rule                      notification
          ↓
notification adapter
```

**Browser logic never becomes the alert engine.** A threshold evaluated in a
page is evaluated only while somebody has the page open, which is the opposite
of what an alert is for.

## Privacy constraints

Every item inherits the retention allowlist and adds no content surface:

| Item | Surface | Content risk |
|---|---|---|
| 092 | counts and groupings over retained fields | none — there is no content column to group by |
| 093 | notification bodies | an export surface; inherits the allowlist, asserted by test |
| 094 | a description of existing routes | none — describing a route adds no field |
| 095 | **a bounded durable metadata and reference surface** | no content, and no duplicated evidence — but it *is* persisted state, with retention, bounds, deletion and migration left to 095's own specification |

**090's direction is unchanged and this task does not amend it.** Its shape is
already what the interoperability question needs — Trustvian works with no trace
backend present, and adding one changes no Trustvian result:

```text
                           ┌── Trustvian          behavioral evidence, gates
Application → OpenTelemetry┤
                           └── optional trace     waterfalls, span detail
                               backend            (absent by default)
```

The integration is **Collector fan-out**: no SDK is imported, no source is
copied, no runtime dependency is created in either direction, and no parity is
claimed. The worked example stays chosen at specification time from
self-hostable, OpenTelemetry-compatible backends in this category, on
self-hostability, agent semantic support, licence compatibility and operational
simplicity — **no backend is privileged**, which is 082's language and
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)'s
decision, and this task found no contradiction that would justify reopening
either.

**090's privacy requirements are unchanged and are not weakened by anything
here.** A fan-out is off by default; a metadata-only mode requires filtering
before export and ships only with a sentinel test that is verified to fail when
the filter is disabled; a full-span mode is an explicit operator opt-in.
**Trustvian's own metadata-only persistence guarantee does not extend to a
second exporter** — the span is whatever the producer emitted.

## OSS and organizational-scale boundary

Unchanged. All four planned capabilities are OSS-compatible under the existing
rule, because each is *Detect, Score, Decide, Alert, Integrate, Evaluate,
Observe* applied to one deployment's own evidence:

```text
OSS        single-deployment behavioral analytics
           behavioral alert rules
           public API specification
           local or single-team saved investigations

beyond     multi-tenant analytics · organization-wide RBAC
           fleet-wide policy · compliance workflow
           managed retention · cross-tenant reporting
```

The dividing question is the existing one — *does this only make sense across
many deployments or tenants* — and this task does not redefine it.

## Acceptance criteria for this planning task

Complete when:

1. Every proposed capability maps to a concrete missing Trustvian workflow,
   stated as a developer or operator problem rather than as a feature.
2. Nothing duplicates work owned by 083–090 or 015.
3. 089 remains `PROPOSED`, and its contradiction is neither softened nor routed
   around.
4. The `v1.0` exit criteria are unchanged in number and force, and no item here
   is a release gate.
5. No prompt, completion, dataset or output-quality product is added.
6. No new infrastructure dependency is assumed; 068 and 069 keep their
   conditionals.
7. 092–095 each state scope and explicit non-goals.
7a. **092 separates stored facts from historical decisions.** Run-native,
   durable-workflow and comparison/scenario analytics are distinguished, and no
   metric is presented as historical when its decision context was never
   recorded.
7b. **091 does not decide 093's notification domain boundary.** Reuse is stated
   as principles and mechanics; whether `alert.Sink` is reused is left to 093,
   with a second producer in hand.
7c. **095 is described as a durable metadata and reference surface**, not as
   introducing none, with its retention questions deferred to its own
   specification.
8. The dependency order has no cycles and no artificial edges.
9. The WebUI product model gains behavioral analytics without becoming generic
   LLM analytics.
10. MCP remains task 015; trace interoperability remains task 090.
11. Comparable platforms are treated as category-level inspiration and, for 090,
    as interoperability candidates — never as a runtime dependency, and named as
    a category rather than as products, per ADR 0046.
12. No code, schema, API, dependency or migration change is introduced.

## Open questions left to the items themselves

1. **Whether an analytical read model can be served from PostgreSQL at measured
   volume**, which is 068's conditional and 092's first real question. A page
   existing is not a measurement.
1a. **Whether a durable comparison or scenario context is worth creating at
   all**, which decides whether 092's class-C metrics ever become available. 066
   supplies one for promotions; 078 might supply another through persisted
   scenario executions. Neither is a reason to persist every comparison, and
   092 must not assume one arrives.
2. **What a platform alert rule is expressed in** — 093 must choose a condition
   vocabulary without putting a query language on a wire contract.
2a. **How a platform notification is represented without fabricating behavioral
   fields**, and whether a shared delivery abstraction is justified once a
   second producer exists. 093 decides; 091 deliberately does not.
3. **Whether the realtime stream belongs inside an OpenAPI description**, which
   094 must decide rather than assume.
4. **Whether a saved investigation survives a schema change to what it
   references**, which 095 must answer alongside its aged-out-evidence rule.
