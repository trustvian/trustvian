# Roadmap

Where Trustvian is going, and what has to be true before the open-source
product is called `v1.0`.

`v1.0` means the OSS **platform**: the behavioral engine, held to its existing
production-readiness and compatibility commitments, plus the local-first
control plane that makes it usable as a product. The engine's contract is not
weakened to get there — the platform requirements are additive.

This document is deliberately forward-looking. What already shipped is in
[CHANGELOG.md](../CHANGELOG.md); how the system works is in
[ARCHITECTURE.md](ARCHITECTURE.md) and [DOMAIN.md](DOMAIN.md); why past
decisions were made is in [`adr/`](adr/); the specifications behind completed
work are in [`archive/tasks/`](archive/tasks/README.md).

Status vocabulary:

| Status | Meaning |
|---|---|
| **SHIPPED** | Released and in use |
| **CURRENT** | The stable line today |
| **NEXT** | The milestone being worked toward |
| **PLANNED** | Approved and decomposed into tasks, not implemented |
| **FUTURE** | A direction, not scoped, designed, committed, or dated |

`v1.0` is NEXT. Track B is **partly implemented**: its evaluation foundation,
local persistence, the local control-plane API and realtime (tasks 051–059)
exist, while the developer CLI, the UI, promotion and the remaining numbered
work are still PLANNED.
What exists is not usable end to end on its own. Everything under
[Beyond v1.0](#beyond-v10) is FUTURE.

## Product Direction

Trustvian is a **Behavioral Security & Trust Engine**: it turns runtime
behavior into an explainable trust score and a security decision.

```text
Event → Features → Fingerprint → Baseline → Anomaly → Trust → Policy → Decision
```

The question it answers is not one the surrounding stack already answers:

| Layer | Question |
|---|---|
| Identity | Who is this actor? |
| Observability | What did this actor do? |
| **Trustvian** | **Should this behavior be trusted?** |

Three consequences shape everything below. Identity is not behavior — a valid
credential keeps working while the actor behind it starts behaving unusually.
Observability is not trust — a trace records what happened, not whether it
should have. And static authorization is not behavioral confidence — a
permission granted once does not notice drift.

### The product this roadmap builds toward

**A local-first, self-hosted behavioral security and promotion platform for AI
agents and agentic applications.**

> Develop locally. Evaluate in sandbox. Promote with evidence. Monitor in
> production.

Two principles govern what that means in practice.

> **OpenTelemetry tells Trustvian what happened. Trustvian turns that telemetry
> into behavioral evidence, trust, policy, and promotion decisions.**

Trustvian is a *consumer* of observability, not a competitor to it. It does not
ask a team to re-instrument, adopt a new SDK or move their traces. It reads
what their instrumentation already emits and answers a question no trace
backend answers.

> **Run your agent locally. Trustvian shows how it behaves before you ship
> it.**

The progression the product builds, end to end:

```text
telemetry
    ↓  live behavioral visibility
    ↓  semantic normalization
    ↓  behavioral evidence
    ↓  baseline · anomaly · trust
    ↓  decision
    ↓  reference versus candidate
    ↓  gate
    ↓  promotion
```

And a privacy principle that constrains every step of it:

> **Behavioral observability does not require content observability.**

Trustvian answers *what did this actor do, and is that normal* from metadata:
actor, operation, target, model and tool identity, sequence, timing, status
and correlation. It does not need — and by default does not retain, fingerprint
or display — prompt text, completions, reasoning, tool arguments, tool results,
retrieved documents, HTTP bodies, SQL text or arbitrary span attributes. A
capability that wants any of those is a separate, separately reviewed decision,
and no `v1.0` milestone depends on one.

The lifecycle it serves:

```text
Local Development
        ↓
Behavioral Observation
        ↓
Evaluation
        ↓
Shared Sandbox
        ↓
Behavioral Scorecard + Policy Gates
        ↓
Promotion
        ↓
Production
        ↓
Continuous Behavioral Monitoring
```

For a developer: *see how your agent behaves, and know what changed before you
ship it.*

For an organization: *promote agent versions between environments using
behavioral evidence, explicit policy gates, and explainable results.*

The engine described above answers questions about individual events and
learned behavioral state. The platform answers questions about a *candidate* —
is this version of this agent safe to promote — by consuming the engine as an
ordinary dependency. **Neither absorbs the other**, and the direction of
dependency never reverses: see
[ADR 0022](adr/0022-core-platform-boundary.md).

Direction, not feature list: deepen behavioral evidence, keep every decision
explainable and deterministic, and remain something a developer can run on a
laptop without an account.

## Current State

**Current stable line: `v0.9.x`.** SHIPPED.

The pre-`v1.0` core provides:

- the behavioral pipeline above, with explainable contributors on every score
- learned per-actor baselines with copy-on-write semantics and gated learning
- anomaly signals including novelty, frequency, time-pattern, sequence
  (transition deviation and rarity, bounded 3-gram, first-order surprisal),
  and delegation deviation
- deterministic, fail-closed policy evaluation with recorded approval evidence
- alert evaluation and signed generic-webhook delivery
- a Go SDK, a CLI (`analyze`, `baseline`, `version`), and public `event`,
  `alert`, and `config` packages
- an inbound OpenTelemetry adapter and a standalone Collector processor
- in-memory, file, and PostgreSQL stores, with a versioned schema and
  transactional migration
- a reference Docker Compose deployment, health and readiness endpoints, and
  operational metrics
- documented backup, restore, and upgrade procedures with an automated
  recovery drill
- signed container images with SBOM and provenance attestations, published by
  an automated release pipeline

**The platform foundation has begun, and is not usable end to end.** Task 052
added the `platform/` module — a separate Go module at `trustvian-platform`.
It now holds the evaluation foundation, and nothing built on top of it:

- implemented: the evaluation domain — `Project`, `Agent`, `Candidate`,
  `EvaluationRun`, `EnvironmentRef`, `BehavioralProfileRef`, with
  construction-time validation and an explicit run lifecycle (task 052);
  evaluation result aggregation over the core's public `DecisionRecord`
  (task 053); behavioral diff over bounded behavioral snapshots (task 054);
  fixed-shape comparative scorecards (task 055); deterministic
  evidence-backed hard gates over those scorecards (task 056); local
  SQLite persistence for control and evaluation state (task 057); an
  authoritative control-plane service with a local `/v1` HTTP adapter and
  sequenced `DecisionRecord` ingest (task 058); and bounded, ephemeral
  realtime notification over committed state, streamed over SSE (task 059);
- also implemented: the developer CLI evaluation workflow (task 060), the
  terminal dashboard (task 061), the integrated local runtime that binds a
  loopback listener and advertises it (task 062), and the minimal web control
  plane served from that same listener (task 063);
- also implemented: the PostgreSQL platform backend (task 064), selected
  explicitly at the composition root with SQLite remaining the default;
- not implemented: the full environment model, promotion, and the
  event-history capability.

So the platform can now describe an evaluation, aggregate bounded result
evidence, compare bounded behavioral snapshots, build a comparative scorecard
from the two, apply deterministic evidence-backed hard gates to it, keep all
of that across a restart, and accept evidence over a versioned local HTTP API
that a restarted process resumes rather than restarts. Subscribers can watch an evaluation live without polling the database.
A developer drives all of it from one command, a CLI, a live terminal
dashboard and a browser, and a deployment can share one PostgreSQL database
instead. Nothing yet promotes —
a gate returns a verdict and has no side effect, and realtime is notification
over state the database already holds, never a source of truth. Raw event history is
deliberately still absent. The API itself still binds no listener; task 062's
runtime composes one, and it defaults to loopback. Everything else about the
platform in this document remains approved direction rather than shipped
behavior.

Also not implemented: multi-tenancy, access control, an MCP server surface, a
machine-learning detection path, and prompt- or content-level analysis.

## Released Milestones

One line each. Details are in [CHANGELOG.md](../CHANGELOG.md); the task
specifications behind each are in [`archive/tasks/`](archive/tasks/README.md).

| Release | What it established |
|---|---|
| `v0.1.0` | Behavioral core hardened, benchmarked, and published — pipeline, SDK, CLI, first persistent store |
| `v0.2.0` | OpenTelemetry maturation: outbound `trustvian.*` attributes and a real Collector processor |
| `v0.3.0` | Baseline and anomaly depth, including time-pattern deviation |
| `v0.4.0` | Alert and notification foundation: alert evaluation, severity, signed webhook sink |
| `v0.5.0` | Policy and configuration: declarative policy and alert configuration through a public `config` package |
| `v0.6.0` | Behavioral detection depth: order-aware sequence analysis, bounded 3-gram context, first-order surprisal |
| `v0.7.0` | AI-agent behavioral security: session, delegation, and approval context, and approval-aware policy |
| `v0.8.0` | Production runtime and storage: PostgreSQL persistence, reference deployment, durability hardening |
| `v0.9.0` | Operational readiness: CI quality gates, release artifacts, supply-chain signing, health endpoints, self-observability, backup and restore |

## v1.0 — Local-First Behavioral Security Platform

**NEXT.** `v1.0.0` is the first release where Trustvian is usable as a
*product* rather than as a library: a developer runs one command, exercises an
agent locally, and sees what it did, what changed, and whether it passes the
gates their team set.

It has two tracks, and both must land:

| Track | What it means |
|---|---|
| **A — Engine production readiness** | The existing core is stable enough to depend on. Unchanged from the previous plan; no requirement is dropped |
| **B — Platform** | A local-first control plane that evaluates candidates and records promotion decisions, consuming the engine through its public API |

Track A is a gate over work that already exists. Track B is new construction,
decomposed into small milestones rather than one "build Control" task.

**Neither track changes what the engine decides.** No scoring formula and no
fingerprint composition changes to make the platform possible.

The invariant is about *behavioral identity*, not about the engine being
frozen. Platform concepts — Project, Agent, Candidate, EvaluationRun,
Scorecard, Promotion — must never enter behavioral identity or appear in the
core as types, fields, or options. Learning-scope isolation, by contrast, may
require a **generic, additive** change to the core or its persistence. Task
051 owns that design — see the
[milestone sequence](#milestone-sequence) — and whatever it chooses must stay
generic rather than becoming platform-aware. If such a change
alters the persisted `Baseline` shape, the existing storage-versioning and
migration requirements apply unchanged — see
[Compatibility Contract § persisted state](compatibility.md#persisted-state).

A platform requirement that appears to need a *platform-aware* engine change
is a signal to redesign the boundary, not the engine.

## Track A — Engine production readiness

### Release principle

`v1.0` hardens `v0.1`–`v0.9`. Anything discovered missing at gate time becomes
a task under the milestone it actually belongs to, not a `v1.0` exception. The
gate is verified by reading source, tests, and benchmarks — never assumed.

### Reliability and correctness

- Every package exercised by unit, integration, and end-to-end tests, with
  `go test -race ./...` clean across all four modules.
- Every documented formula or algorithm reproduced exactly by at least one
  test, extended to cover every signal added through `v0.6` and `v0.7`.
- Cross-stage behavior — `Analyze` and `Observe` together — covered by tests
  that run the real gated learning loop rather than hand-seeded baselines.
- The PostgreSQL store's concurrency and durability guarantees verified under
  contention, not only in the `-short` tier.

### Security hardening

The threat model and its test index are in [SECURITY.md](SECURITY.md);
vulnerability reporting is [.github/SECURITY.md](../.github/SECURITY.md). The
gate is that every threat there still has a passing test, extended to cover:

- baseline poisoning against the sequence and delegation signals
- fail-closed behavior at the configuration boundary for malformed input
- bounded per-fingerprint state for every signal that learns, so behavioral
  state cannot become a resource-exhaustion path
- credential handling for store connections and webhook secrets
- dependency and container vulnerability scanning gating the release

### API and configuration stability

`v1.0` creates compatibility expectations that `0.x` did not. Before the tag:

- a deliberate review of every exported symbol in the root package, `event`,
  `alert`, and `config`, since after `v1.0` removing one requires a major bump
- the configuration schema reviewed the same way, with a documented rule for
  adding fields compatibly
- CLI flags and output treated as an interface, with a stated compatibility
  scope
- the Collector processor's configuration surface reviewed alongside it
- the storage schema's migration and rollback path documented for every
  supported version transition, extending the
  [compatibility matrix](operations.md#compatibility-matrix)
- a written breaking-change policy stating what a major, minor, and patch
  release may change after `v1.0` — **published** as
  [Compatibility Contract](compatibility.md); the remaining gate work is
  confirming each surface it classifies still matches the code at
  release time

### Performance and resource safety

Measured numbers belong in [PERFORMANCE.md](PERFORMANCE.md). The gate is:

- every hot path benchmarked, including the sequence signals and the
  configuration load path
- allocation behavior on the common path understood and documented, so a
  regression is caught rather than discovered later
- memory bounded and documented for every structure that learns
- sustained-load behavior measured against the reference deployment, so
  contention and growth are known rather than assumed

### Operational readiness

Deployment, health, readiness, persistence, backup, restore, upgrade, and the
recovery drill are shipped and documented in [operations.md](operations.md).
The gate is that each is verified against the release candidate rather than
trusted from a prior release, and that an upgrade from the previous stable
line is proven to preserve learned state.

### Observability and diagnostics

Operational metrics exist and are documented in
[observability.md](observability.md). The gate is that an operator can answer,
from the running system alone, whether Trustvian is healthy, whether it is
learning, and why a specific decision was made — without attaching a debugger
or reading the source.

### Documentation and adoption

Install, configure, integrate, deploy, operate, troubleshoot, and upgrade each
have a verified document, and every command and example in them runs against
the released version. The public API has a documented stability scope, and a
new adopter reaches a first analysis without reading the architecture first.

### Supply chain and release readiness

Release automation, signing, SBOM, and provenance are shipped and described in
[supply-chain.md](supply-chain.md) and the
[release guide](release-guide.md). The gate is that a downloaded artifact and
a published image can be verified end to end by a third party following only
the public documentation.

## Track B — Platform

**Partly implemented.** The foundations exist, and the local developer
workflow on top of them does too; the shared and production layers do not.

Implemented: generic learning-scope isolation in the core (051), the
evaluation domain (052), evaluation result aggregation (053), behavioral
diff (054), evaluation scorecards (055), deterministic hard gates (056),
local SQLite persistence (057), the local control-plane API and ingest (058),
and bounded realtime infrastructure (059).

Also implemented: the developer CLI (060), which drives all of that from a
shell or a CI job over the `/v1` API, and the terminal dashboard (061), which
watches one run live over SSE.

Also implemented: the integrated local runtime (062) — `make local` starts
SQLite, the control plane, the realtime bus and a loopback HTTP listener, and
local clients discover the endpoint without being told.

Also implemented: the PostgreSQL platform backend (064). SQLite stays the
zero-configuration local default and PostgreSQL is opt-in, so the same control
plane runs on either without any layer above persistence knowing which.

Also implemented: the environment model (065). A project now owns
environments, a run may only name one its project owns and has not archived,
and `CanPromote` gives a promotion workflow one deterministic ordering
question to ask — while authorizing nothing.

The promotion workflow (066) is **specified and not implemented** — see
[the task](tasks/v1.0/066-promotion-workflow.md). It settles what a promotion
is before anything is built: a durable record of a platform *decision*, never a
deployment and never a claim about where a candidate now runs.

**Five gap-closing milestones are specified and not implemented: 074–078.**
Each was found by running the product end to end — an instrumented agent, a
browser, and a developer who has not read the source — rather than by planning,
which is why they sit outside the reserved 049–072 block alongside 073.

- **[074 — zero-input live behavior WebUI](tasks/v1.0/074-zero-input-live-behavior-webui.md).**
  Opening the browser today shows a form asking for an identifier the developer
  does not have. The milestone makes the WebUI discover active work by itself,
  render it as a bounded run-scoped behavior graph, and browse the hierarchy
  with nothing typed. It shares the schema chain with 066 — 066 owns 3 → 4 and
  074 owns 4 → 5 — so its implementation follows 066's.
- **[075 — AI semantic telemetry normalization](tasks/v1.0/075-ai-semantic-telemetry-normalization.md).**
  Generic instrumentation flattens an agent's behavior into its transport, so a
  tool call learns as an HTTP POST. Where a producer emits agent-oriented
  OpenTelemetry, Trustvian should read it — through the same pipeline, with no
  AI-specific engine, no framework dependency and no content.
- **[076 — behavioral evidence explorer](tasks/v1.0/076-behavioral-evidence-explorer.md).**
  A verdict without its evidence is not explainable. Sessions, traces and
  behavioral sequence, from metadata alone, over whatever history 067 makes
  durable.
- **[077 — unified OTLP local dev runtime](tasks/v1.0/077-unified-otlp-local-dev-runtime.md).**
  One command wraps an existing agent and composes the runtime around it, with
  no source modification and no Trustvian dependency in the application.
- **[078 — behavioral scenario suites](tasks/v1.0/078-behavioral-scenario-suites.md).**
  Run the same scenario again, diff the behavior, gate the difference — reusing
  the diff, scorecard and gate the platform already owns, and evaluating no
  answer quality.

Still planned: everything from 066 onward. The platform
can describe an evaluation, aggregate bounded result evidence, compare bounded
behavioral snapshots, produce fixed-shape comparative scorecards, apply
deterministic evidence-backed hard gates to them, persist local control and
evaluation state across a restart, accept public `DecisionRecord` evidence
through a versioned local HTTP API that resumes a running evaluation after a
restart, publish bounded live updates over SSE with project, agent and run
filtering, and drive all of it from one command, a CLI and a live terminal
dashboard, and inspect it in a browser. That makes it usable end to end
**locally**. It cannot yet promote, retain raw event history, or replay realtime history.

The scorecard itself carries no verdict and no threshold; acceptance lives in
[task 056](tasks/v1.0/056-deterministic-hard-gates.md), which pairs a card
with caller-owned limits and returns PASS or FAIL. A PASS means only that the
configured gates passed — it is not a promotion, and nothing acts on it.

Several metrics named below are **not derivable from current evidence** — see
[task 055](tasks/v1.0/055-evaluation-scorecards.md). Task 056 did not invent
those contracts: policy severity and resource sensitivity remain unsupported
until explicit evidence exists, and the gates depending on them are deferred
rather than approximated.

The concepts below are named so that every task shares one vocabulary.

### The domain the platform owns

| Concept | What it is | What it is not |
|---|---|---|
| **Project** | A workspace holding agents, policies, and environments | Not a tenant; multi-tenancy is out of scope for `v1.0` |
| **Agent** | A stable logical agent identity | Not tied to a commit, model version, or deployment |
| **Candidate** | One version or configuration of an Agent under evaluation | Its metadata — git SHA, artifact digest, model or tool-set hash — **must never become fingerprint dimensions** |
| **Evaluation Run** | A bounded execution assessing one Candidate, grouping sessions, events, results, detections, policy outcomes and a scorecard | Correlation and evaluation metadata, **not behavioral identity** |
| **Behavioral Profile** | The platform's reference to a generic core learning scope | The isolation mechanism is solved (task 051); allocation, reuse, ownership and lifecycle remain platform policy — see below |
| **Scorecard** | An evaluation-level aggregation of evidence | **Distinct from `Trust.Score`**, which stays event-level and is not redefined or overloaded |
| **Environment** | A deployment stage a project owns, identified by the reference a run records, with an optional promotion rank | Not a deployment target: it holds no URL, credential or secret, and nothing connects to one |
| **Promotion** | A platform workflow moving a candidate between environments | The engine promotes nothing; it supplies evidence. An environment's rank orders stages; it authorizes no movement |

### Behavioral profile

Two candidates evaluated against the same actor identity would train the same
baseline, so one candidate's behavior would teach another's — and an
evaluation would measure a baseline it had itself polluted. Learning isolation
is therefore a prerequisite for evaluation, and **task 051 solved it
generically**: a learning scope is a `baseline.Key` dimension, selected by
`trustvian.WithLearningScope`, absent from behavioral identity, and knowing
nothing about evaluations. See
[ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md).

The constraints that shaped it, all still binding:

- `SessionID` must **not** become baseline identity. It is correlation, and
  making it identity would create a new baseline per session, learning nothing.
- `EvaluationRunID` must **not** become fingerprint identity.
- Candidate metadata must **not** become fingerprint identity — a git SHA is
  not a behavioral dimension, and treating it as one would make every deploy
  look like a brand-new actor.
- The solution must be **generic**, not evaluation-aware. The engine must not
  learn what a Candidate or an Evaluation Run is.

The mechanism is decided. `baseline.Key` was made composite in `v0.4`
against exactly this kind of future need, and task 051 extended it:
`{Scope, ActorID, Environment}`. The platform's `BehavioralProfileRef` maps
onto that opaque core scope from outside; the core never learns why a caller
chose one.

What remains platform work: how profiles are **allocated and reused** —
whether two runs of one candidate share a profile, when a profile is retired,
who owns that lifecycle. Task 054 deliberately does not require two snapshots
to share a profile ref, precisely because the allocation policy is still
open.

### Hard gates, not averages

Promotion must never rest on an aggregate score alone. A high average must not
override a critical violation. This is the same fail-closed discipline
`policy.Evaluate` already applies at event level, raised to the evaluation
level — and [task 056](tasks/v1.0/056-deterministic-hard-gates.md) implements
it with integer counts, because a float is not guaranteed bit-identical under
record reordering and an average is precisely the mechanism by which one
dimension offsets another.

**Implemented in task 056.** Five checks, all evaluated on every call, with
PASS requiring all five:

```text
reference_record_count         >= 1
candidate_record_count         >= 1
new_behavior_count             <= configured maximum
candidate_block_decisions      <= configured maximum
candidate_critical_risk_count  <= configured maximum
```

The first two are evidence-sufficiency gates and are not configurable. A
candidate that ran zero records satisfies every maximum, so without them the
strictest policy would pass an evaluation that never happened.

The other three are caller-owned limits over factual integers. Their names
stay factual on purpose: a **block decision** is the policy engine doing what
it was configured to do, not a violation; a **critical-risk observation** is a
risk classification, not an incident; an **added behavior** is a count, and
the platform makes no claim that new behavior is bad.

**Deferred until explicit evidence exists.** An earlier version of this
section listed the first three of these as immediately implementable:

```text
critical_policy_violations
blocked_sensitive_actions
unapproved_sensitive_actions
per-rule compliance
resource sensitivity gates
approval-compliance gates
delegation-compliance gates
```

[Task 055](tasks/v1.0/055-evaluation-scorecards.md) established that none of
them is derivable from evidence this chain retains. `PolicyRule` carries no
severity and is not retained; `ContextRisk` has no repository-defined
sensitivity threshold; `ApprovalStatus` is producer-supplied evidence rather
than proof of authorization; and the aggregate keeps no per-event rule,
resource, or delegation correlation.

They could each be approximated from a count that *is* available — but

```text
critical risk count   != critical policy violation count
block decision count  != blocked sensitive action count
denied approval count != unapproved sensitive action count
```

and a gate whose name promises a guarantee its evidence cannot support is
worse than no gate, because it reads as a check that ran and found nothing.

**The intent is not withdrawn.** These gates remain planned. Each needs an
explicit evidence contract first — policy severity metadata, an approved
sensitivity mapping, an authorization signal distinct from reported approval
status — and each will then add new named gates rather than reinterpreting
the counts above. See
[ADR 0029](adr/0029-hard-gates-use-explicit-integer-evidence.md).

### Deployment stages

**Local developer** — maximum developer impact for minimum infrastructure.
One command to start, single node, local control plane, TUI and WebUI,
evaluation runs, behavioral diff, scorecards and gates. No cloud account, no
mandatory PostgreSQL or ClickHouse, and **loopback binding by default** — a
developer tool that listens on every interface by default is a developer tool
that ships an exposure.

**Shared sandbox** — teams evaluating candidates before production: multiple
developers, projects, agents, candidates, evaluation runs, environments, and
the evidence a promotion was based on.

**Production** — continuous monitoring at volume, where an event and
analytical store earns its place only when measured volume justifies it.

Two constraints hold across all three. Realtime delivery never depends on the
database, so live views keep working regardless of the persistence choice. And
no analytical store ever becomes a dependency of the behavioral engine: the
engine deliberately retains no raw event history, and the platform is where
history lives.

Persistence per stage is in
[Storage is a set of capabilities](#storage-is-a-set-of-capabilities-not-a-database).

### Three interfaces, one control plane

The platform has three first-class interfaces, and they are peers rather than
a hierarchy:

| Interface | For |
|---|---|
| **CLI** | Scripting, automation, CI/CD, machine-readable output, one-shot operations |
| **TUI** | Realtime behavioral feedback while developing — the inner loop |
| **WebUI** | Control-plane management, history, diffing, policies, environments, promotion, investigation |

**None of them owns authoritative logic.** Evaluation, scoring, policy,
behavioral diff, gate evaluation, and promotion live in control-plane services;
every interface is an adapter over the same services. Three implementations of
a gate rule is three behaviors, and the one a user sees becomes whichever
interface they happened to open. See
[ADR 0023](adr/0023-interfaces-are-adapters.md).

The TUI is deliberately not a small WebUI. It optimizes one loop —
`run → observe → change code → run again → compare` — and does not own project
administration, policy editing, environment configuration, promotion
workflows, or deep historical analytics. Those are the WebUI's.

The CLI gains an exit-code contract for CI use: `0` gate passed, `1` gate
failed, `2` and above configuration, runtime, or execution error. This
conflicted with the exit codes the CLI already shipped;
[task 060](tasks/v1.0/060-developer-cli.md) resolved it by scoping rather than
renumbering — see [CLI exit codes](#cli-exit-codes-resolved-in-task-060).

### Realtime is infrastructure, not a feature of the database

Live behavior must never be produced by polling historical storage. The
internal abstraction is transport-independent; server-sent events are the
first transport to evaluate because the flow is one-way, and WebSockets wait
for a bidirectional requirement that has not appeared.

```text
Agent / Application
        │
        ▼
     Ingest API
        │
        ▼
      Engine
        │
        ├──────────▶ Evaluation Aggregator
        ├──────────▶ Realtime Bus ──▶ TUI, WebUI
        └──────────▶ Async Persistence
```

Message kinds to expect: `observation`, `decision`, `new_behavior`,
`policy_violation`, `baseline_update`, `evaluation_update`, `gate_update`,
`evaluation_complete`.

Requirements the design must answer rather than discover: bounded queues, a
defined slow-consumer policy, subscriber isolation so one client cannot stall
another, filtering and scoping by project/agent/run, and explicit
disconnect/reconnect behavior. A realtime bus without a slow-consumer answer
is an unbounded queue with better branding.

### Storage is a set of capabilities, not a database

Do not model persistence as one generic interface:

```go
type Database interface {  // not this
	Query(...)
}
```

That shape hides every difference that matters — transactionality, retention,
query pattern, volume — behind a name, and the first backend to need something
it does not express forces a leak. The same reasoning kept
`internal/store.Store` at two methods.

Capability boundaries are named once their call patterns are known, which is
why the evaluation domain lands before local persistence. Expected shapes:
`ControlStore`, `EvaluationStore`, `BehaviorStore`, `EventStore`,
`RealtimeBus` — or narrower, once real usage says so.

Reference direction, `*` meaning subject to measured requirements:

| | Local | Sandbox | Production |
|---|---|---|---|
| Control state | SQLite | PostgreSQL | PostgreSQL |
| Evaluation state | SQLite | PostgreSQL | PostgreSQL |
| Behavior state | existing store | PostgreSQL | PostgreSQL |
| Event history | SQLite\* | PostgreSQL\* | ClickHouse\* |
| Realtime | in-memory | TBD | TBD |

No message broker — Kafka, NATS, or otherwise — is planned. Adding one is a
decision that needs a measurement behind it.

### Milestone sequence

Small, independently shippable tasks continuing this repository's numbering.
**Partly implemented:** tasks 049–065 are done and 066 is specified; 067–072
remain PLANNED. Outside that reserved sequence, 073 is done and 074–078 are
specified — see the gap-closing table below.

**Numbering is identity, not order.** A task's number records when it was
added to the plan, never when it is built. Tasks 073–078 were discovered by
using the product end to end, after 049–072 was already reserved, so several
of them carry numbers *higher* than the release gate they block. That is
correct and deliberate: renumbering a milestone would break every
specification, ADR, commit message and document that already cites it. Read
[the execution order](#execution-order-rather-than-numeric-order) for what
actually happens first.

**Architecture and core boundary** — the only tasks that touch the engine:

| Task | Milestone |
|---|---|
| 049 | Platform architecture alignment *(this planning task)* |
| 050 | Public serializable decision record — the engine result boundary a control plane consumes |
| 051 | Behavioral profile: learning-scope isolation in the core |

**Evaluation foundation** — pure domain and logic, before any persistence, so
that storage capabilities are designed against known call patterns:

| Task | Milestone |
|---|---|
| 052 | Evaluation domain: Project, Agent, Candidate, EvaluationRun, environment references |
| 053 | Evaluation result aggregation |
| 054 | Behavioral diff |
| 055 | Scorecards |
| 056 | Deterministic hard gates |

**Local developer platform** — the primary adoption path:

| Task | Milestone |
|---|---|
| 057 | Local platform persistence |
| 058 | Local control-plane API and ingest |
| 059 | Realtime infrastructure |
| 060 | Developer CLI |
| 061 | Terminal dashboard (TUI) |
| 062 | Integrated local developer workflow |
| 063 | Minimal web control plane |

The TUI lands **before** the full WebUI, deliberately. Local developer
adoption is the primary product goal, and the inner loop is where it is won or
lost.

**Shared sandbox and promotion:**

| Task | Milestone |
|---|---|
| 064 | PostgreSQL platform backend |
| 065 | Environment model |
| 066 | Promotion workflow — [specified](tasks/v1.0/066-promotion-workflow.md), not implemented |

**Closing unplanned gaps.** None of these was in the reserved 049–072
sequence. Each closes a concrete gap found by running the product end to end —
an instrumented agent, a browser, and a developer who has not read the source.

| Task | Milestone | Status |
|---|---|---|
| 073 | OTel Collector evaluation ingest — the Collector produces a `Result`, the control plane accepts a `DecisionRecord`, and nothing joined them | **Implemented** |
| 074 | [Zero-input live behavior WebUI](tasks/v1.0/074-zero-input-live-behavior-webui.md) — automatic active-scope discovery, a bounded live behavior graph, and a browseable hierarchy without entering an identifier | Specified |
| 075 | [AI semantic telemetry normalization](tasks/v1.0/075-ai-semantic-telemetry-normalization.md) — read agent-oriented OpenTelemetry where a producer emits it, so a tool call is a tool call rather than an HTTP POST | Specified |
| 076 | [Behavioral trace and session evidence explorer](tasks/v1.0/076-behavioral-evidence-explorer.md) — see *why* behavior was familiar, new or anomalous, from metadata alone | Specified |
| 077 | [Unified OTLP local dev runtime](tasks/v1.0/077-unified-otlp-local-dev-runtime.md) — one command wraps an existing agent, composes the runtime, and needs no change to the application | Specified |
| 078 | [Behavioral scenario suites](tasks/v1.0/078-behavioral-scenario-suites.md) — run the same scenario again, diff the behavior, gate the difference | Specified |

**Production history and scale:**

| Task | Milestone |
|---|---|
| 067 | Event-history capability boundary |
| 068 | ClickHouse reference adapter, if justified by measured volume |
| 069 | Multi-node and load validation |
| 070 | Platform security hardening |
| 071 | Platform backup, restore, and upgrade |
| 072 | OSS platform `v1.0` release gate |

### Execution order, rather than numeric order

What is built next is a dependency question, and the numbers do not answer it.
Conceptually:

```text
066  promotion workflow
       ↓
074  zero-input live WebUI  ──┐
075  AI semantic telemetry  ──┤
                              ↓
                        067  event history
                              ↓
                        076  behavioral evidence explorer

077  unified local dev runtime
       ↓
078  behavioral scenario suites

069  multi-node and load validation
070  platform security hardening
071  backup, restore and upgrade
       ↓
072  v1.0 release gate

068  ClickHouse — only if measured volume justifies it
```

Three things this diagram says, and one it does not:

- **074 and 075 are independent of each other** and can proceed in parallel.
  076 needs both, plus whatever history 067 makes durable.
- **077 and 078 are a second, largely independent thread.** 078 needs 077's
  repeatable invocation; neither needs the explorer.
- **068 remains conditional**, exactly as its row says: an analytical backend
  arrives if measured volume justifies one, and not otherwise. It is not a
  release-gate prerequisite, and this restructuring does not make it one.

What it does not say is that a lower number comes first. 066 is a prerequisite
for nothing in the gap-closing set beyond a shared migration chain, and 074
blocks 072 while carrying a higher number. **Do not renumber anything to make
the diagram read left to right.**

Tasks 050 and 051 are the only core changes currently expected, and both are
additive. Everything from 052 onward lives in the platform layer. Any further
core change requires explicit architectural review and must be generic —
justified by the behavioral engine on its own terms, never by platform
convenience. What stays prohibited outright is platform awareness in the
core.

### CLI exit codes, resolved in task 060

`1` meant "the run failed" and `2` meant "the invocation was wrong", and
[the compatibility contract](compatibility.md#cli) treats both as an
automation-facing interface. The evaluation direction wanted `1` to mean *the
behavioral gate failed* — a different meaning for the same code.

Resolved by **scoping, not renumbering**. The new control-plane command
families use `0` success, `2` usage and `3` operational, leaving `1` unclaimed;
`trustvian eval compare` then takes `1` for a gate FAIL, and only there.
`analyze`, `baseline` and `version` are untouched, so no released automation
changes meaning. See
[ADR 0033](adr/0033-developer-cli-is-a-thin-http-adapter.md) and
[the compatibility contract](compatibility.md#cli).

### Open conflicts to resolve before implementation

One remains.

**Fingerprint admission and long evaluations.** A baseline admits at most 512
fingerprint identities ([ADR 0019](adr/0019-bounded-fingerprint-admission.md)).
A long evaluation of a wide-surface agent could reach that bound, and nothing
currently surfaces it — an evaluation would silently stop learning new
behavior while still reporting. Task 051 owns this, because learning-scope
isolation changes what counts toward the bound.

## v1.0 exit criteria

`v1.0.0` is tagged when all of the following hold, each demonstrable rather
than asserted.

**Track A — engine:**

1. The full gate set passes against the candidate commit, in CI, on the tagged
   source.
2. No known correctness, security, or data-durability defect is open against
   shipped behavior.
3. The public API and configuration review is complete and its outcome
   documented.
4. An upgrade from the previous stable line preserves learned state, proven by
   test.
5. Every document named above is accurate against the candidate, with its
   examples executed.
6. A published artifact and image verify from the public documentation alone.
7. The breaking-change policy is written and published.

**Track B — platform.** The gate is one journey, end to end, by a developer
who has not read the source:

```text
run an existing instrumented agent locally, with one Trustvian command
    ↓
open the WebUI without entering an internal identifier
    ↓
watch the actor's current behavior live
    ↓
see agent, model, tool and service flow at the fidelity the telemetry carries
    ↓
inspect why a behavior is familiar, new or anomalous
    ↓
evaluate a candidate
    ↓
inspect the behavioral diff
    ↓
receive a scorecard and a hard-gate decision
    ↓
run the same behavioral scenario again
    ↓
share the evaluation in a sandbox
    ↓
promote on the evidence
```

Not one step of that requires the developer to modify their application, adopt
a Trustvian SDK, or open a second tool to understand Trustvian's own evidence.

Concretely:

8. The platform starts locally with one command, no account, and loopback
   binding by default.
9. **One command runs an existing instrumented agent locally** — no source
   modification, no Trustvian dependency in the application, no manual
   creation of platform objects
   ([task 077](tasks/v1.0/077-unified-otlp-local-dev-runtime.md)).
10. With telemetry flowing, opening the WebUI shows the active agent and its
    behavior live **without a Project, Agent, Candidate or EvaluationRun
    identifier being entered in the browser**
    ([task 074](tasks/v1.0/074-zero-input-live-behavior-webui.md)).
11. That live view is a **bounded behavior visualization** driven by real
    observations, which saturates explicitly rather than silently
    ([task 074](tasks/v1.0/074-zero-input-live-behavior-webui.md)).
12. Where a producer emits agent-oriented OpenTelemetry, behavior is shown at
    that **semantic fidelity** — a tool call named as a tool call
    ([task 075](tasks/v1.0/075-ai-semantic-telemetry-normalization.md)).
13. Where it does not, Trustvian **degrades gracefully** to generic HTTP, DB
    and RPC behavior with no regression and no fabricated semantics
    ([task 075](tasks/v1.0/075-ai-semantic-telemetry-normalization.md)).
14. A developer can inspect **why** a behavior was familiar, new or anomalous,
    from metadata alone, without a prompt, completion, argument or body being
    retained or displayed
    ([task 076](tasks/v1.0/076-behavioral-evidence-explorer.md)).
15. A behavioral scenario runs **repeatably**, producing a diff, a scorecard
    and a deterministic gate result usable in CI
    ([task 078](tasks/v1.0/078-behavioral-scenario-suites.md)).
16. An evaluation run against a candidate produces a scorecard and a
    deterministic gate result, and every score is explainable from the
    evidence beneath it.
17. A behavioral diff between two evaluation runs is produced and explained.
18. A promotion decision is recorded together with the evidence it rested on.
19. CLI, TUI, and WebUI each reach the same result through control-plane
    services, with no interface carrying its own copy of evaluation, scoring,
    policy, diff, gate, or promotion logic.
20. The platform imports no `internal/*` package from the core, proven by
    test rather than by review.
21. The engine contains no platform-aware branch, type, or configuration.

Criteria 20 and 21 are the architectural invariants this whole direction rests
on, which is why they are release gates rather than guidelines.

Criteria 9 through 15 are the gap-closing milestones 074–078, which is why
tasks numbered above the release gate nonetheless block it. Task 068 is
**not** among them: it stays conditional on measured volume and is not a
prerequisite for `v1.0`.

## v1.0 non-goals

`v1.0` is **not** gated on an MCP surface, on multi-tenancy, on
authentication or access control, on a managed offering, or on anything under
[Beyond v1.0](#beyond-v10). It is not gated on Kafka, Redis, Kubernetes, or
ClickHouse without a milestone of their own. It does not require new detection
signals; adding one is a minor release, before or after `v1.0`.

It is explicitly **not** gated on the platform being ready for
organizational scale. `v1.0` targets a developer on a laptop and a team
sharing a sandbox. Fleet-wide operation is later work.

### What Trustvian is not becoming

Trustvian evaluates **behavior** — what an agent did, against what it normally
does, against policy. It is not becoming a general LLM evaluation suite. A
prompt playground, prompt versioning, output-quality scoring, and model
benchmarking are outside the product, not merely unscheduled: they answer "is
this model any good", which is a different question from "did this agent do
something it should not have", and answering both would blur the one the
detection engine is built for.

**Tasks 074–078 do not move that line, and it is worth saying why.** Adding AI
semantic spans, a session view, trace correlation, behavior visualization and
scenario suites makes Trustvian *better at reading observability evidence*. It
does not make Trustvian an observability product, because the question it asks
of that evidence is unchanged:

```text
an observability tool asks     what happened inside this trace?

Trustvian asks                 which observed actions constitute this actor's
                               behavior, how do they differ from its learned or
                               reference behavior, and what decision follows?
```

The first needs prompts, completions, arguments, results and every attribute a
span carries. The second needs none of them, which is why *behavioral
observability does not require content observability* is a principle and not a
limitation. Trustvian consumes observability evidence to answer a behavioral
trust question, and sits beside a trace backend rather than replacing one —
the Collector fan-out that makes that possible is deliberate and stays.

Still outside the product, and not made less so by any of this: prompt
management and playgrounds, LLM-as-a-judge, hallucination and groundedness
scoring, RAG relevance metrics, model benchmarking, prompt or completion
warehousing, generic dataset platforms, and provider marketplaces.

No message broker is planned. No analytical store is a dependency of the
engine. Neither becomes one without a measurement behind it.

### The WebUI product model

The information architecture the interface builds toward, at roadmap level.
Labels will be refined during implementation; the ordering is the point.

```text
Live          active actors · animated behavior topology · new behavior
              trust · anomaly · risk · decision                        (074)

Evidence      sessions · traces · behavioral sequence
              correlation and explanation                              (076)

Evaluations   runs · reference and candidate · diff · scorecard · gate

Promotions    environment advancement decisions                        (066)

Manage        projects · agents · candidates · environments
              advanced and manual operations
```

```text
Manage is not the landing page. Live behavioral understanding is.
```

Today the shipped WebUI opens on Manage, in effect — a form asking for an
identifier. That inversion is the gap task 074 closes, and everything above it
in the list is what 074–078 add.

## Beyond v1.0

**FUTURE. Nothing in this section is scoped, designed, committed, or dated.**
No task file exists for any of it. These are credible directions consistent
with the product, not planned work, and no version numbers beyond `v1.0` are
assigned to them.

### Behavioral intelligence

Deeper behavioral evidence, still deterministic and explainable: richer
sequence context beyond the fixed 3-gram, cross-session reasoning, baselines
that age gracefully, and numeric baselines for volume and rate. Any of these
must stay reproducible and explainable, and none may make machine learning a
dependency of the core detection path.

### AI agent security

Foundations shipped in `v0.7`: agents are behavioral actors analyzed by the
same pipeline, with session, delegation, and approval context. Future
direction extends that rather than replacing it — multi-hop delegation, tool
and MCP call behavior as first-class observed events, and behavioral
resource-abuse detection.

What this does not imply: Trustvian does not authenticate delegation claims,
verify approval provenance, or inspect prompt or completion content. Those
stay outside the deterministic core by design.

### Runtime Identity & Provenance

Workload and agent provenance as behavioral evidence — where an actor runs and
what it was built from — treated the way identity confidence already is: an
input Trustvian consumes, never one it computes.

### Integrations and adapters

Additional inbound and outbound surfaces, each following the adapter shape
[ADR 0003](adr/0003-opentelemetry-adapter-single-module.md) established:
framework middleware, additional alert sinks, policy-engine interoperability.
The core stays unaware of every adapter.

### Trustvian MCP

Trustvian exposing *itself* as an MCP server, so an agent platform could query
behavioral trust directly. Specified in
[015 — Trustvian MCP](tasks/015-trustvian-mcp.md), which remains
unimplemented.

This is distinct from observing the MCP tool calls an agent makes, which is
ordinary detection work. Neither depends on the other, and **neither is
required for `v1.0`**.

### Organizational scale

The platform in `v1.0` targets a developer and a team. What stays beyond it is
everything that only makes sense across *many* deployments or teams:
multi-tenancy, authentication and access control, fleet-wide policy
administration, audit and compliance workflows, and managed operation.

This is where the earlier "Trustvian Control" placeholder now points. The
local-first control plane moved into `v1.0`; organizational scale did not.
[Task 016](tasks/016-control.md) records the constraint that survives both:
the platform consumes the core as an ordinary dependency and never forks,
reimplements, or reaches inside it.

### Managed and cloud direction

A managed offering is a possible future direction, not committed scope. No
availability, timeline, or terms are stated here.

### Future research

Research only, with no concrete design or consumer: machine-learning and
graph-based anomaly detection, kept explicitly optional and never a dependency
of the core; and prompt- or content-level security, which structurally
requires a model the deterministic core is designed not to need. If either is
ever built, it is a separate optional adapter producing ordinary
`event.Event` values, not a change to the core.

## The OSS / Enterprise product boundary

This governs every item above. **Trustvian OSS is a complete, standalone,
production-usable behavioral security product.** Nothing an operator needs to
detect, score, decide, alert on, integrate, or run Trustvian in production is
withheld for a paid product.

```text
Detect      Event, Features, Fingerprint, Baseline, Anomaly
Score       Trust, Risk
Decide      Policy, Decision, Explainability
Alert       Alert evaluation, notification, webhook delivery
Integrate   Go SDK, CLI, OTel adapter, Collector processor
Run         Persistence, deployment, health, self-observability, hardening
Evaluate    Evaluation runs, behavioral diff, scorecards, hard gates   (v1.0, planned)
Promote     Environments, promotion decisions, recorded evidence       (v1.0, planned)
Observe     Local dashboard, realtime activity                         (v1.0, planned)
```

Applying that rule to the platform: **the local-first control plane is OSS.**
Evaluating a candidate, producing a scorecard, diffing behavior, gating a
promotion, and watching activity on one deployment are all the same question
the engine already answers, asked at a higher level. A developer on a laptop
and a team sharing a sandbox are not organizational scale.

A future commercial layer's job is what genuinely is: centralized management
across many deployments, multi-tenancy, access control, fleet-wide policy,
audit and compliance workflows, and managed operation.

The dividing question is never "is this valuable enough to withhold from OSS".
It is "does this only make sense at organizational scale, across many
deployments or tenants", or "is this an operations and compliance concern
orthogonal to detection itself". Detecting, scoring, deciding, and alerting on
one deployment's behavior is OSS, however sophisticated the detection becomes.

## Roadmap principles

- **Deterministic before statistical, statistical before ML.** Explainability
  is a product requirement, not a preference, and ML never becomes a
  dependency of the core detection path.
- **Evidence, not verdicts.** Trustvian quantifies and explains deviation;
  policy decides what to do about it.
- **Small vertical slices.** A milestone is a sequence of independently scoped
  tasks, each with its own tests and acceptance criteria.
- **Verified, not assumed.** Statements about what exists are checked against
  source, tests, and benchmarks.
- **No speculative abstraction.** An interface arrives with its second
  implementation, not before it.
- **Trustvian consumes identity, it does not compute it.** Behavior can reduce
  trust; it never retroactively re-decides who an actor is.

## Related

- [CHANGELOG.md](../CHANGELOG.md) — what shipped, per release
- [Architecture](ARCHITECTURE.md) — system shape and boundaries
- [Domain Model](DOMAIN.md) — the concepts this roadmap refers to
- [Security Model](SECURITY.md) — threats considered and tested
- [Performance](PERFORMANCE.md) — measured results
- [Task specifications](tasks/README.md) — active work; completed work is
  under [`archive/tasks/`](archive/tasks/README.md)
