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
| **PROPOSED** | Written down for a decision, and **not approved**. A proposal that contradicts or extends an accepted boundary says so, and closing it is a legitimate outcome |
| **FUTURE** | A direction, not scoped, designed, committed, or dated |

**PROPOSED is not a weaker PLANNED.** PLANNED means the decision is made and the
work is not done. PROPOSED means the decision is not made, and the entry exists so
it can be made deliberately rather than drifted into. Exactly one item carries it
today — item 089, optional quality evaluation, inside
[task 082](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md) — and it
carries it because it contradicts
[What Trustvian is not becoming](#what-trustvian-is-not-becoming) as written.

`v0.10.0` — the [developer preview](#v0100--developer-preview) — is NEXT, and
`v1.0` is the release gate beyond it. `v0.11.0` — the
[WebUI experience](#v0110--webui-experience) — is PLANNED after the preview. Track B is **partly implemented**: the
evaluation foundation, local persistence, the local control-plane API and
realtime, the developer CLI, the TUI, the WebUI, the PostgreSQL backend, the
environment model and promotion (tasks 051–066, 073 and 074) exist; 067 and
075–079 are implemented, 080 is specified, and
068–072 are still PLANNED. [Task 082](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md)
is a planning task, in 049's shape: it reserves and scopes 083–090 — inspection
and evaluation depth — and implements nothing.
On `main`, a developer can run an instrumented agent under `trustvian dev`, watch
it in the WebUI, gate a candidate over repeated scenarios with `trustvian eval
run`, and post the verdict on a pull request with the CI action — see
[Current State](#current-state). None of it is in a release yet. Everything under
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

**The platform is implemented on `main` and not yet released.** It lives in
`platform/`, a separate Go module at `trustvian-platform`. What ships on `main`
today, by task:

- **Evaluation foundation.** The public `DecisionRecord` (050),
  learning-scope isolation (051), the evaluation domain (052), result
  aggregation (053), behavioral diff (054), scorecards (055) and deterministic
  hard gates (056).
- **Control plane and storage.** SQLite persistence (057), the authoritative
  control plane with its local `/v1` API and sequenced ingest (058), realtime
  notification over SSE (059), and the PostgreSQL backend (064).
- **Developer surfaces.**
  - The CLI evaluation workflow (060) and the terminal dashboard (061).
  - The integrated local runtime (062) and the WebUI served from it: the
    minimal web control plane (063), zero-input live behavior (074), and the
    record-first admin console (096).
- **Environments and history.** The environment model (065), promotion as a
  recorded decision on gate evidence (066), and bounded per-observation event
  history (067).
- **Agent telemetry and evidence.** Collector evaluation ingest (073), AI
  semantic telemetry normalization (075), and the behavioral evidence explorer
  (076). Under them sit layer classification, correlation evidence and
  evidence resolution (083–085).
- **`trustvian dev`** (077) runs an existing instrumented agent with one
  command. It composes the local control plane and a Collector around it. On
  macOS and Linux, the release archive carries both helpers from the next
  release on (#114).
- **`trustvian eval run`** (078) runs a scenario N times per side under isolated
  profiles and gates the difference over k-of-N evidence. It persists each
  execution, reuses a recorded reference with `--reference`, and runs a
  directory of scenarios as a bounded suite with `--suite`. No default `k`
  ships; the scenario guide documents
  [how to calibrate one](platform-cli.md#calibrating-n-k-and-j).
- **The CI action** (079) runs in pull requests. `.github/actions/trustvian-run`
  runs a scenario or suite, and the exit code decides the job. A separate job,
  `.github/actions/trustvian-comment`, renders the result offline and posts it as
  one pull request comment, and it runs no pull request code
  ([guide](ci-github-action.md)).

A gate verdict has no side effect of its own. Promotion is a separate, recorded
decision that consumes one. Realtime is notification over state the database
already holds. A repeated scenario's verdict is the control plane's;
`trustvian eval run` counts nothing. The API binds no listener of its own:
task 062's runtime composes one, on loopback by default.

**No platform capability is in a release yet.** The next release, planned as
`v0.10.0`, is the first that will carry one.

**Not implemented:**
- Multi-node and load validation, platform security hardening, platform backup
  and restore, and the rest of 068–072. They are planned, with no
  specification.
- Metadata-only detection evaluation (080), which is specified.
- Inspection depth 086–090, which is scoped by
  [task 082](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md). 089 is
  PROPOSED, not approved.
- Multi-tenancy, access control, an MCP server surface, a machine-learning
  detection path, and prompt- or content-level analysis.

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

## v0.10.0 — Developer Preview

**NEXT.** The first release a developer outside this project can pick up and
use for the thing the product is for.

It exists because of a sequencing problem, not a scope disagreement. The whole
Track B journey currently reaches users at exactly one moment — the `v1.0` tag —
and that moment is gated on event history (067), multi-node and load validation
(069), platform security hardening (070), platform backup and restore (071),
the behavioral evidence explorer (076), sandbox sharing and promotion — of which
067 and 076 have since landed, and the rest have not. A
developer who wants to run their own agent locally and catch a behavioral change
in CI needs none of those, and today waits for all of them. Shipping nothing
until everything is ready means the feedback that would improve the rest arrives
after it is built.

A minor release is the right shape. Pre-`v1.0`, `MINOR` is where
backward-compatible capability lands
([Branching Strategy § Semantic Versioning](governance/branching.md#semantic-versioning)),
the current stable line is `v0.9.x`, and `v0.10.0` is the next minor. The exact
number stays a maintainer's decision at release time —
[the commit convention](COMMIT_CONVENTION.md) is explicit that the prefix
convention derives no version — and nothing here creates a `v1` compatibility
promise:
[the compatibility contract](compatibility.md) describes the surface from
`v1.0.0` onward, and
[pre-`v1.0` discipline](governance/branching.md#pre-v10-discipline) governs
until then.

### Contents

| Task | Status | Milestone |
|---|---|---|
| 075 | **Implemented** | [AI semantic telemetry normalization](tasks/v1.0/075-ai-semantic-telemetry-normalization.md) |
| 077 | **Implemented** | [Unified OTLP local dev runtime](tasks/v1.0/077-unified-otlp-local-dev-runtime.md) |
| 078 | **Implemented**: `eval run`, repeated evaluation, persisted executions with `--reference <execution>\|last` (schema 9) and suites (`--suite`, per-scenario deadlines). Criteria 9 and 11 were amended to what the evidence supports, and both are met ([amendment](tasks/v1.0/078-behavioral-scenario-suites.md#amendment--criteria-9-and-11-2026-10-03)) | [Behavioral scenario suites](tasks/v1.0/078-behavioral-scenario-suites.md) |
| 079 | **Implemented**: `.github/actions/trustvian-run` runs a scenario or suite, passes the exit code through and preserves the result as an artifact ([ADR 0056](adr/0056-the-run-action-builds-a-pinned-source-commit.md)); `cmd/trustvian-ci-render` renders that artifact as inert Markdown or an explicit no-verdict state, offline ([ADR 0057](adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md)); and `.github/actions/trustvian-comment` posts it as one pull request comment from a separate job that holds `pull-requests: write` and runs no pull request code ([ADR 0058](adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md)) | [CI integration — a GitHub Action over the 078 command](tasks/v1.0/079-ci-integration-github-action.md) |

**The build order inside this preview is 075 → 078 → 079**, not the four in
parallel: 078's thresholds can only be measured at 075's tool-name fidelity — see
[the execution order](#execution-order-rather-than-numeric-order).

**075 has since landed**, so that edge is satisfied and the preview's critical path
is back to its original length. 078's measurement re-run at that fidelity has
since been recorded (2026-10-01): an unchanged agent's behavior set varies between
isolated runs, and the evidence justifies no default `k` or `j`. **078 is now
implemented.** No default `k` ships. The scenario guide documents how to
calibrate `N`, `k` and `j` against a workload's own self-comparisons
([`docs/platform-cli.md`](platform-cli.md#calibrating-n-k-and-j)). 079 has
landed too, so every task in the contents table is implemented. What remains is
cutting the release.

074 is already implemented and is a prerequisite rather than contents: without
it, opening the browser asks for an identifier the developer does not have.

With 077 implemented, the first line of the exit criterion below — *run an
existing instrumented agent locally, with one Trustvian command* — is satisfied
by `trustvian dev -- <command>`. `dev` supervises two helper executables,
`trustvian-local` and `trustvian-collector`. Since #114, the macOS and Linux
release archives carry both beside `trustvian`, so `dev` works from a downloaded
release with no checkout. That closes the consequence
[ADR 0043](adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md)
left open. Three limits remain:
- The first release with the helpers is the next one. The `v0.9.0` archive
  predates #114.
- The Windows archive is unchanged, because `dev` refuses to start on Windows.
- `go install` still yields only `trustvian`.

### Exit criterion

The [Track B journey](#v10-exit-criteria), truncated at the step a scenario
suite reaches, and usable in CI:

```text
run an existing instrumented agent locally, with one Trustvian command
    ↓
open the WebUI without entering an internal identifier
    ↓
watch the actor's current behavior live
    ↓
see agent, model, tool and service flow at the fidelity the telemetry carries
    ↓
evaluate a candidate
    ↓
inspect the behavioral diff
    ↓
receive a scorecard and a hard-gate decision
    ↓
run the same behavioral scenario again
```

**Usable in CI** is part of the criterion, not a nice-to-have on top of it: a
scenario runs in a pull request, the gate's exit code decides the job, and the
result is legible on the pull request itself rather than in a log a reviewer has
to open. Task 079 added that last part, in a separate comment job.

### What it does not require

Explicitly **not** gated on 067 (event history), 068–071, 076 (the behavioral
evidence explorer), sandbox sharing, or promotion. 067 and 076 have since been
implemented; that does not add them to this milestone's requirements, and this
section is unchanged by it.

Those remain `v1.0` requirements and lose none of their force. **The `v1.0` gate
keeps every criterion it has**, this milestone removes nothing from it, and a
capability shipping in a preview does not count as gate-verified — see
[Track A's release principle](#release-principle), which applies here too: the
gate is verified against the release candidate rather than trusted from a prior
release.

The two journey steps this preview drops are the ones that genuinely need
what it omits: *inspect why a behavior was familiar, new or anomalous* needs
durable history (067, then 076 — both now implemented), and *share, then promote
on the evidence*
needs a deployment more than one person uses. Neither is needed to answer
"did my agent's behavior change, and does that change pass my limits", which
is the question a developer preview has to answer.

## v0.11.0 — WebUI Experience

**PLANNED.** Tasks 097–104 are implemented and not in a release: 097–100 on
`main`, 101–104 pending review and merge. The
release after the developer preview, and a WebUI-focused one: the engine is
unchanged, and the platform gains bounded collection routes (tasks 100–103)
and one schema step for recency ordering (task 101). Everything else is
presentation over data `/v1` serves.

The preview proved a developer can watch an agent and gate a candidate without
typing an identifier at the front door. What it left is the *investigation*
after that — the moment a gate fails or a behavior is new and the developer has
to work out where to look. Today that means visiting four destinations to
assemble one picture, pasting identifiers into the forms that remain, reading a
trace as a flat table, and accepting whichever color scheme the operating
system chose.

### User journey

```text
open the WebUI in the theme you chose last time
    ↓
choose a project, agent and candidate once — by name, from searchable lists
    ↓
read the Overview: live activity, run status, the newest run's evidence,
recent gate verdicts — each saying what it covers and when it was read
    ↓
open the run, or compare two runs with both sides already chosen
    ↓
open a trace: a searchable list, a waterfall of the evaluated actions,
and the details of one span beside it
    ↓
follow the evidence — without typing an identifier at any step
```

### Contents

| Task | Status | Milestone |
|---|---|---|
| 097 | **Implemented** | [Theme preference: Light, Dark and System](tasks/v0.11/097-theme-preference.md) |
| 098 | **Implemented** | [Selection-based context workflows](tasks/v0.11/098-selection-based-context-workflows.md) |
| 099 | **Implemented** | [Overview dashboard](tasks/v0.11/099-overview-dashboard.md) |
| 100 | **Implemented** | [Trace investigation over retained evidence](tasks/v0.11/100-trace-investigation.md) |
| 101 | **Implemented** | [Recency-ordered run discovery](tasks/v0.11/101-recency-ordered-run-discovery.md) |
| 102 | **Implemented** | [Scenario execution discovery and reference selection](tasks/v0.11/102-scenario-execution-discovery.md) |
| 103 | **Implemented** | [Session selection over retained observations](tasks/v0.11/103-session-selection.md) |
| 104 | **Implemented** | [The Runs destination reads the shared context](tasks/v0.11/104-runs-destination-from-shared-context.md) |

**Build order 097 → 098 → 099 → 100, then 101 → 103 → 102 → 104.** Every
later surface is drawn with 097's tokens and reads 098's context. 101's schema
v10 key is what 102's execution ordering also uses, and 104 moves the Runs
destination onto the context once 101 gives it a scope-wide run list.

### Dependencies and missing capabilities

- **Available:** every hierarchy collection (ADR 0041), environments,
  promotions, run progress and behaviors, retained observations narrowed by
  trace (067, 076), and the realtime scope cards (074).
- **Added by this milestone:** `GET /v1/evaluation-runs/{run_id}/traces` — the
  distinct trace identifiers in a run's retained history. Without it a trace is
  reachable only by an identifier read off one observation (task 100).
- **Planned to close the gap:** a recency-ordered run collection at project,
  agent or candidate scope with schema v10's sort key (101), a scenario
  execution collection and a reference check over the existing validation
  (102), and a run's session list (103).

### Exit criterion

1. **Themes.** Light, Dark and System are selectable from every destination by
   pointer and keyboard; a first visit follows the operating system, an explicit
   choice survives a reload, and a dark choice does not flash light. Text meets
   WCAG AA in both themes.
2. **Dashboard.** With a context chosen, Overview shows live activity, run
   status, the newest run's evidence and behaviors, recent gate verdicts and
   environments, each with its scope, read time, drill-down, and distinct
   loading, empty and error states. Absent data reads *not available*, never 0.
3. **Investigation.** A run's traces are listed and searchable; a trace renders
   as a hierarchy with timing bars, durations and status; a span's details open
   on the right while the trace, filter and scroll positions are kept; Escape
   closes and focus returns.
4. **Selection.** Every identifier field that has a collection behind it is a
   searchable selector showing names with copyable identifiers; dependent
   selectors clear when their parent changes; context carries between
   destinations and is preselected only when unambiguous; a paste path remains.
5. **Accessibility.** Every new control is reachable and operable by keyboard
   with a visible focus ring; nothing is conveyed by color alone;
   `prefers-reduced-motion` is honored.
6. **Responsive.** Every new surface works at desktop and at a phone-width
   viewport in both themes, with the details panel becoming a drawer.

### Exclusions

- **No new retention.** Spans the engine did not evaluate, span events,
  attributes, service names and all content stay unretained. The trace view is
  the evaluated actions of a trace, says so, and never calls itself a complete
  distributed trace.
- **No polling, prefetch or auto-refresh.** Freshness is a read time and a
  Refresh control; Live remains the landing view and its startup budget is
  unchanged.
- **No aggregate health score**, no trend charts, and no distribution drawn
  from a bounded page as if it were the run.
- **No authentication or remote access** (task 070), and no change to gate,
  promotion or realtime semantics.

### Known limitations after implementation

- Every collection's `next_after` probe and its page are two reads, so the
  cursor and a history state can describe adjacent instants.
- The project- and agent-scope recency reads join through candidates and
  agents; their page is bounded but the rows examined grow with the database's
  newer runs (ADR 0063 names the denormalization that removes this).
- Run lists are newest first at the deepest level chosen, so with a candidate
  chosen a promotion's or provenance's other side is picked from Compare or
  pasted rather than listed.
- A scenario execution's reference eligibility is checked for its own
  repetition count, project and environment; a scenario that differs on any
  of them is refused by the CLI's same validation, and the page says so.
- Live's authoritative header counts are still read once per selection, as
  before this milestone.

### Retained-data limitations

The investigation view inherits 067's bounds: at most 4096 observations per
run, the first ones admitted; one page of 64 per read; trace structure only as
recorded; timestamps from the producer's clock, so bars from different
producers can be skewed; and no fidelity, layer or sequence-deviation evidence
per observation (076). Each is stated on screen where it applies.

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

The promotion workflow (066) is **implemented** — see
[the task](tasks/v1.0/066-promotion-workflow.md) and
[ADR 0040](adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md).
It settled what a promotion is before anything was built: a durable,
append-only record of a platform *decision*, never a deployment and never a
claim about where a candidate now runs. Both gate verdicts are recorded, the
gate result the decision consumed is snapshotted field for field rather than
re-derived on read, and the write commits only against the environment state it
was decided against. Schema version is now **4** on both backends.

**One gap-closing milestone remains open: 080 is specified. 074, 075, 076,
077, 078 and 079 are implemented.**
075–078 were each found by running the product end to end — an instrumented
agent, a browser, and a developer who has not read the source — rather than by
planning, which is why they sit outside the reserved 049–072 block alongside
073. 079 and 080 come from two different questions: *what actually reaches a
user*, and *what is the detection claim measured against*. Neither is a `v1.0`
exit criterion.

- **[074 — zero-input live behavior WebUI](tasks/v1.0/074-zero-input-live-behavior-webui.md)
  is implemented.** Opening the browser used to show a form asking for an
  identifier the developer did not have. The WebUI now opens on Live,
  subscribes to all local activity unfiltered, shows every active run as a
  card with nothing typed, and draws one selected run's behavior flow. Four
  bounded collection routes make the durable hierarchy discoverable after a
  reload with no traffic — the capability tasks 063 and 058 deferred until a
  consumer existed for it. The browser surface is an observability cockpit —
  Live, Investigate, Compare, Promotions, Manage — rather than the ID-driven
  console it replaced. Startup costs exactly one collection request and
  follows no continuation automatically, because a bounded route is not a
  bounded workflow. It shared the schema chain with 066 — 066 owned 3 → 4 and
  074 owned 4 → 5 — so it landed after it, and schema version is now **5**.
  See [ADR 0041](adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md).
- **[075 — AI semantic telemetry normalization](tasks/v1.0/075-ai-semantic-telemetry-normalization.md).**
  Generic instrumentation flattens an agent's behavior into its transport, so a
  tool call learns as an HTTP POST. Where a producer emits agent-oriented
  OpenTelemetry, Trustvian should read it — through the same pipeline, with no
  AI-specific engine and no framework dependency, and with content kept out of
  behavioral identity and every durable and published surface.
- **[076 — behavioral evidence explorer](tasks/v1.0/076-behavioral-evidence-explorer.md)
  is implemented.** A verdict without its evidence is not explainable. An
  **Evidence** surface leads from a gate check or a behavioral delta to the
  behaviors that contributed and the observations that carried them, and from
  one observation into its session, its trace's recorded parent/child structure,
  the behavior sequence, the decision timeline and one behavior's detail — with
  no identifier typed. It stores nothing and resolves nothing; 067 owns
  retention and 085 owns resolution. Its one added capability is three optional,
  mutually exclusive narrowings on the existing observation route
  ([ADR 0049](adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md)),
  with no schema change. Fidelity and sequence deviation are **not** shown,
  because 067 retains neither, and the views say so.
- **[077 — unified OTLP local dev runtime](tasks/v1.0/077-unified-otlp-local-dev-runtime.md)
  is implemented.** `trustvian dev -- <command>` composes the runtime around an
  existing agent, with no source modification and no Trustvian dependency in the
  application, and exits with the workload's own status. Instrumentation
  ownership is explicit and positive-evidence-only: absence of detectable
  instrumentation never selects injection, because a child that instruments
  itself a moment later would then be observed twice. Three decisions are
  recorded — [0042](adr/0042-dev-composes-the-collector-rather-than-owning-a-receiver.md)
  (supervise `trustvian-collector`, do not own a receiver),
  [0043](adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md)
  (derive identity from the repository, keep state outside it) and
  [0044](adr/0044-instrumentation-ownership-requires-positive-evidence.md)
  (refuse rather than guess). Independent of 075, and implemented ahead of it.
- **[078 — behavioral scenario suites](tasks/v1.0/078-behavioral-scenario-suites.md).**
  Run the same scenario again, diff the behavior, gate the difference — reusing
  the diff, scorecard and gate the platform already owns, and evaluating no
  answer quality. The runner scripts no action ordering of its own, and does
  not suppress the engine's learned sequence signals: where they are opted in
  against a learned profile, a reorder the engine blocks fails, and the verdict
  stays the control plane's. Each
  scenario runs N times per side, because an LLM-driven agent may call a tool
  in one execution and not the next, and a gate that FAILs on unchanged code
  teaches a team to re-run CI. `N = 1` reproduces today's semantics exactly.
- **[079 — CI integration: a GitHub Action](tasks/v1.0/079-ci-integration-github-action.md)
  is implemented.** A gate nobody reads is a gate nobody acts on. 078 makes the verdict
  scriptable; 079 makes it legible where the change is reviewed — a pull request
  comment rendered from 078's result document and nothing else, with the exit
  codes passed through untouched so an unreachable control plane is never
  reported as a policy violation. It computes nothing and carries metadata only,
  and it treats every string it renders as author-controlled, because behavior
  descriptors come from the workload's own telemetry. Its security posture is
  the specification's centre of gravity: **running the workload and holding a
  token that can write to the pull request are placed in two different jobs** of
  the same `pull_request` event, since `actions/checkout` persists credentials
  by default and a compromised dependency in a same-repository pull request
  would otherwise reach a write-scoped token. `pull_request_target` is refused
  as a trigger outright, and a fork pull request loses only the comment. Part of
  the
  [developer preview](#v0100--developer-preview), not a release gate.
- **[080 — metadata-only detection evaluation](tasks/v1.0/080-metadata-only-detection-evaluation.md).**
  *Behavioral observability does not require content observability* is this
  document's central claim, reasoned about throughout and measured nowhere. 080
  measures it: precision, recall and false-positive rate for the **existing**
  signals against a public agent prompt-injection benchmark's paired benign and
  attacked runs, reproducible and published under `examples/` or `docs/`. It
  evaluates Trustvian's detection rather than any model's quality — an attack
  the agent resisted is excluded rather than counted as a catch, which is the
  rule that keeps the two apart — and it retains no prompt or completion
  content. See
  [What Trustvian is not becoming](#what-trustvian-is-not-becoming), whose
  no-model-benchmarking line this does not cross. Not a feature and not a gate
  item.
- **[096 — record-first admin console](tasks/v1.0/096-record-first-admin-console.md).**
  074 removed the identifier form from the front door and
  [ADR 0041](adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
  made the durable hierarchy discoverable, but every surface *behind* Live was
  still assembled from inputs: two menus to choose a comparison's runs, a run
  identifier and a narrowing to read evidence, a project typed in before any
  promotion history appeared. Replacing a text box with a menu had made the
  identifiers discoverable without making the records browsable. 096 makes the
  browser surface a console: a persistent sidebar of destinations, tables of
  real records as the primary navigation, identifiers rendered as the controls
  that follow them, a contextual detail panel beside the observation table, and
  a comparison whose two sides are assigned from rows and shown in full. No
  capability is added or removed, no route changes, and every bound 0041
  established is unchanged — one page per action, an explicit continuation, and
  narrowing in storage before the page bound. A second pass made the bundle
  itself a layered design system — five layers whose dependencies point only
  downward, one file that names every raw value, and four visibly different
  states for a surface that has no records — after the first pass left a flat
  directory in which light and dark drifted apart, a loading table looked
  like an empty one, and a blocked observation weighed the same as an allowed
  one. See
  [ADR 0050](adr/0050-the-browser-surface-is-a-record-first-admin-console.md)
  and [ADR 0051](adr/0051-the-browser-bundle-is-a-layered-design-system.md).

Still planned: everything from 068 onward. The platform
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
**Partly implemented:** tasks 049–067 are done; 068–072 remain PLANNED.
Outside that reserved sequence, 073, 074, 075 and 077 are done and the rest are
specified — see the gap-closing table below.

**Numbering is identity, not order.** A task's number records when it was
added to the plan, never when it is built. Tasks 073–078 were discovered by
using the product end to end, after 049–072 was already reserved, so several
of them carry numbers *higher* than the release gate they block; 079 and 080
were added later still, and neither blocks the gate at all. That is
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
| 066 | Promotion workflow |

**Closing unplanned gaps.** None of these was in the reserved 049–072
sequence. 073–078 each close a concrete gap found by running the product end to
end — an instrumented agent, a browser, and a developer who has not read the
source. 079 and 080 close two gaps of a different kind. 082 is a planning task
and 083–090 are what it reserves — see
[inspection and evaluation depth](#agent-inspection-and-evaluation-depth) below.
The **Gate** column says which rows the release actually depends on.

| Task | Milestone | Status | Gate |
|---|---|---|---|
| 073 | OTel Collector evaluation ingest — the Collector produces a `Result`, the control plane accepts a `DecisionRecord`, and nothing joined them | **Implemented** | `v1.0` |
| 074 | [Zero-input live behavior WebUI](tasks/v1.0/074-zero-input-live-behavior-webui.md) — automatic active-scope discovery, a bounded live behavior graph, and a browseable hierarchy without entering an identifier | Implemented | `v1.0` |
| 075 | [AI semantic telemetry normalization](tasks/v1.0/075-ai-semantic-telemetry-normalization.md) — read agent-oriented OpenTelemetry where a producer emits it, so a tool call is a tool call rather than an HTTP POST | **Implemented** | `v1.0` |
| 076 | [Behavioral trace and session evidence explorer](tasks/v1.0/076-behavioral-evidence-explorer.md) — see *why* behavior was familiar, new or anomalous, from metadata alone | **Implemented** — an Evidence surface over 085's resolution and 067's history; three narrowings on the existing observation route, no schema change; fidelity and sequence deviation narrowed because neither is retained | `v1.0` |
| 077 | [Unified OTLP local dev runtime](tasks/v1.0/077-unified-otlp-local-dev-runtime.md) — one command wraps an existing agent, composes the runtime, and needs no change to the application | **Implemented** | `v1.0` |
| 078 | [Behavioral scenario suites](tasks/v1.0/078-behavioral-scenario-suites.md) — run the same scenario N times per side, diff the behavior, gate the difference over k-of-N evidence | **Implemented** — repeated evaluation, recorded references and suites; criteria 9 and 11 amended to what the evidence supports, and met | `v1.0` |
| 079 | [CI integration: a GitHub Action](tasks/v1.0/079-ci-integration-github-action.md) — the 078 verdict rendered on the pull request, exit codes passed through, no `pull_request_target` with an untrusted checkout | **Implemented** — the run action, the offline renderer and the comment action in a separate job | preview only |
| 080 | [Metadata-only detection evaluation](tasks/v1.0/080-metadata-only-detection-evaluation.md) — precision, recall and false-positive rate for the existing signals against a public agent prompt-injection benchmark | Specified | neither |
| 081 | Persist behavior fidelity, so a comparison delta reports whether a behavior was named by telemetry or inferred from transport — a forward-only schema step in both backends, deferred from 075 | Not specified | neither |
| 082 | [Agent inspection and evaluation depth](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md) — the planning task for the six-step developer workflow: what is implemented, what is missing, and what a decision would cost. Documentation only | Specified | neither |
| 083 | [Behavioral layer identity and display classification](tasks/v1.0/083-behavioral-layer-classification.md) — an explicit rule for when a tool span and the HTTP request beneath it are one behavior, and a non-identity label so a model call, a tool call and an outbound request are distinguishable without changing what a fingerprint is | **Implemented.** Classification and rendering shipped first; the counting correction folds on 084's parent identity ([ADR 0047](adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md), [ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)). The optional gate limit over the new unit, `max_added_behavior_changes`, is built too (issue 131): omitted, it is not evaluated; schema 8 persists its promotion evidence without rewriting historical decisions | `v1.0` |
| 084 | [Correlation and operational evidence on the record boundary](tasks/v1.0/084-correlation-operational-evidence.md) — parent span identity, duration and error status promoted from volatile feature inputs to recorded evidence, additively | **Implemented** — carried, aggregated per run and persisted at schema 6; per-observation history is 067's and is now implemented | `v1.0` |
| 085 | [Evidence resolution](tasks/v1.0/085-evidence-resolution.md) — from a gate check or a behavioral delta to the behaviors and observations behind it, as a resolution query rather than a payload inside a fixed-shape verdict. Authoritative at the control plane, exercised over `/v1` and the CLI, and **delivered before 076 consumes it** | **Implemented** — two `GET` routes and a CLI family, no schema change; three checks resolve and the two evidence checks are aggregate-only by construction | `v1.0` (via 17) |
| 086 | Scenario and input versioning — a scenario-definition digest and an input digest on the evidence, and a prompt *reference* beside `Model`, so a comparison can state whether both sides ran the same thing | Not specified | neither |
| 087 | Performance and cost evidence — latency and error comparison from 084, token counts from the conventions, and cost only with an explicit pricing version and provenance | Not specified | neither |
| 088 | Review decisions and annotations — an append-only note on a resolved finding, and an acknowledgement recorded **beside** the computed verdict rather than replacing it | Not specified | neither |
| 089 | **PROPOSED** — optional quality evaluation and prompt experimentation. Contradicts [What Trustvian is not becoming](#what-trustvian-is-not-becoming) as written; needs a product-boundary decision, and closing it is a legitimate outcome | Not specified | none |
| 090 | Trace-backend interoperability — a documented OTLP fan-out to a trace backend beside Trustvian, with no runtime dependency in either direction. **Off by default; enabling it names the destination and whether content-bearing attributes may be transmitted.** Optional integration ([ADR 0046](adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)) | Not specified | none |
| 091 | [Platform analytics and developer ecosystem](tasks/v1.0/091-platform-analytics-and-developer-ecosystem.md) — the planning task for what becomes valuable once the observation, retention, investigation, comparison and decision foundations are in place, with finding review scoped by 088. Documentation only | Specified | none |
| 092 | Behavioral analytics — a bounded analytical read model over authoritative evidence, answering questions that span more than one run | Not specified | none |
| 093 | Behavioral alert rules — platform-level conditions over that evidence, reusing existing delivery and security principles. The notification domain boundary is decided by 093, once a second producer exists. Notification, never enforcement | Not specified | none |
| 094 | Public control-plane API contract and typed clients — an OpenAPI description of `/v1`, machine-validated, with generated or contract-tested clients | Not specified | none |
| 095 | Saved investigations — a bounded durable metadata and reference surface holding investigation context that references authoritative evidence rather than copying it. Depends on 085 only | Not specified | none |
| 096 | [Record-first admin console](tasks/v1.0/096-record-first-admin-console.md) — the browser surface reorganized around tables of records and clickable identifiers, so no journey through it requires typing one. Presentation and information architecture only; no route, capability or bound changes | Specified | none |

**Production history and scale:**

| Task | Milestone |
|---|---|
| 067 | Event-history capability boundary — **implemented** ([task 067](tasks/v1.0/067-event-history-capability-boundary.md)) |
| 068 | ClickHouse reference adapter, if justified by measured volume |
| 069 | Multi-node and load validation |
| 070 | Platform security hardening |
| 071 | Platform backup, restore, and upgrade |
| 072 | OSS platform `v1.0` release gate |

**081 is a deferral, not a discovery.** Task 075 shipped fidelity on the
outbound span attribute, the ingest envelope, the realtime observation and the
WebUI — everywhere it needs no storage. Carrying it on a comparison delta needs it
persisted per behavior, which is a forward-only schema step in both SQLite and
PostgreSQL plus the backup/restore/upgrade path. It was separated deliberately: a
migration bug damages a user's database, and the rule for a behavior whose
observations disagree about fidelity is a decision that should be made rather than
arrived at. `TestFidelityIsNotPersistedYet` records the gap in the suite and fails
the moment it closes. See
[ADR 0045](adr/0045-conventions-are-read-frameworks-are-not.md)'s consequences.

### Agent inspection and evaluation depth

**082 is a planning task, 083–090 are what it reserves. 083 is partially
implemented; the rest are not started.** The planning is in
[task 082](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md), which carries
each item's developer problem, verified current state, scope, non-goals,
dependencies, acceptance criteria, validation strategy, privacy implications,
risks and placement. This section is the roadmap-level reading of it.

It decomposes one sentence:

> **Inspect what your agent did, understand what changed between versions, and
> make release decisions using evidence.**

That is not a new direction. It is the [Track B journey](#v10-exit-criteria) read
from the developer's side, and every clause already has an owner — except two.
The workflow, with who owns each step:

```text
1  run an agent against a versioned set of scenarios      078, + 086
2  inspect model calls, tool calls, requests, errors      076 (shipped) over 067,
                                                          083, 084
3  compare a reference version with a candidate           054 055 056  (shipped)
4  evaluate behavior, task quality, performance, cost     behavior shipped;
                                                          performance and cost 087;
                                                          quality PROPOSED (089)
5  investigate a regression through linked evidence       085, 076 (both shipped)
6  apply release criteria and record the decision         066 (shipped), + 088
```

**Step 5 is the gap that matters most.** A gate FAIL today names a count and a
delta names a fingerprint; neither says *which observations*, and that is the
first thing a developer whose gate just failed wants. It is also the reason a
developer opens a second tool — which the [Track B gate](#v10-exit-criteria)
rules out in the sentence directly beneath its journey.

**Step 2 is blocked lower down than it looks.** Trustvian reads the conventions
well (075). What crosses `DecisionRecord` into the platform is narrower than what
it read: no parent span, no duration, no error status, no token counts. So a
timeline with errors and timing is blocked on the *record* boundary (084), not on
the adapter.

**And one item is a correction rather than an addition.** 083 exists because a
measurement found one behavioral change reported as two — an instrumented tool
call and the HTTP request beneath it are two behavioral identities, and no rule
says what should happen. At `max_added_behaviors: 0` that is invisible; the first
team to allow one added behavior would be allowing half of one. It has to land
before 078's threshold measurement is re-run, or the re-run measures the double
count.

Three sequencing consequences, each argued in 082:

- **083 precedes the inspection work**, because it changes what a behavior *is*,
  and therefore what a diff counts and what an evidence link resolves to.
- **084 is separated from 083** rather than bundled with it: 084 is purely
  additive and 083 may require a baseline migration. This is the separation 081
  already demonstrates.
- **085 is specified alongside 067 and delivered before 076.** 076 records the
  mistake the first half avoids: a presentation task whose storage layer retained
  too little has to amend itself. The second half is a correction — the
  capability is authoritative at the control plane and ships over `/v1` and the
  CLI without a browser, and 076 consumes it, so the edge runs one way.

**This section adds nothing to the `v1.0` gate.** 083 and 084 are gate items
because they change what criteria 12, 14 and 17 already mean; 085 rides on
criterion 17's *"and explained"*, which 076 already carries. The rest is
post-preview work, one proposal and one documentation item. No criterion is added,
removed or weakened.

**089 is PROPOSED and that is the whole of its status.** Optional quality
evaluation contradicts
[What Trustvian is not becoming](#what-trustvian-is-not-becoming) as written, and
a planning task does not get to reverse an accepted boundary quietly. See
[the proposal that is not approved](#the-proposal-that-is-not-approved).

**090 is an optional integration, not a dependency.** A documented OTLP fan-out
puts a trace backend beside Trustvian for the waterfall Trustvian deliberately
does not render. It is **off by default**, and enabling it names the destination
and the export mode: a `metadata-only` mode must filter before export and prove it
with sentinel content asserted absent at the destination, while a `full-span` mode
forwards what the producer emitted — prompts and tool arguments included — and is
an explicit opt-in. *Trustvian exports no span it did not receive* is provenance,
not a privacy guarantee, and no filter of this kind exists today.
[ADR 0046](adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)
records why that is interoperability rather than a dependency or a source of
code — and it is **Proposed**, not Accepted.

### Platform analytics and developer ecosystem

**091 is a planning task, 092–095 are what it reserves, and none of them is a
`v1.0` gate.** It is positioned after the current investigation chain, not
inside it: observe, retain, explain, compare and gate are implemented, while
finding review (088) is scoped rather than shipped and the explorer (076) has
since shipped. The planning is in
[task 091](tasks/v1.0/091-platform-analytics-and-developer-ecosystem.md), which
follows 082's shape: each item carries its developer problem, verified current
state, scope, non-goals, dependencies, architectural constraints and privacy
implications, and authorizes no code.

It answers a question the investigation chain above leaves open. Once 085 can
explain one finding completely, nothing can answer a question that spans **more
than one run**:

```text
Which agents observed new behavior most often?            run-native
Which targets appeared for the first time this week?      run-native
Which agents have incomplete behavioral evidence?         run-native

Which environments are producing gate failures?           needs a durable
                                                          comparison context
```

**Not every one of those is the same kind of question**, which is 092's central
constraint. A normal comparison is not persisted — `CompareEvaluations` derives
the diff, scorecard and gate on demand from stored run evidence, with
caller-supplied limits — so a historical gate-failure count has no durable
decision context to be counted from. 092 aggregates stored facts and **never
manufactures historical comparison facts by pairing arbitrary runs or
re-evaluating today's gate over yesterday's**. Promotion (066) is durable
because it records the gate evidence its decision consumed; 078 may add another
such context.

| Task | What it reserves |
|---|---|
| 092 | **Behavioral analytics** — a bounded analytical read model over authoritative evidence, split by how durable that evidence is. **Run-native**: agents observed, evaluation runs, new and familiar behaviors, block decisions, critical-risk observations, incomplete evidence, newly observed targets. **Durable workflow**: promotion accepted and rejected, because 066 records the gate evidence its decision consumed. **Comparison and scenario** — added and removed behaviors, gate failures — **only where a durable comparison or scenario context exists**, and otherwise unavailable rather than reconstructed. Dimensions: project, agent, environment, profile, decision, risk, layer, time window |
| 093 | **Behavioral alert rules** — platform-level conditions over that evidence, reusing the existing delivery and security *principles* and generic webhook mechanics. **Whether the core `alert.Sink` boundary is reused is 093's decision, not 091's**: `Alert` describes one behavioral decision, and a gate or promotion notification has no actor, target or fingerprint to put in it. A notification is *about evidence* and never changes a verdict, a decision or a promotion |
| 094 | **Public control-plane API contract and typed clients** — an OpenAPI description of `/v1`, machine-validated against the implementation, and generated or contract-tested clients. Python matters most, as *tooling*, never as instrumentation |
| 095 | **Saved investigations** — a bounded durable metadata and reference surface: shareable investigation context that references authoritative evidence rather than copying it, and states when referenced evidence has aged out. Depends on 085 only; **076 does not depend on it**, and after it ships the Evidence surface may consume it |

**This is behavioral security analytics, not LLM product analytics.** A prompt
registry, a playground, a dataset platform, LLM-as-a-judge, hallucination,
groundedness and RAG scoring, model and provider leaderboards and generic
product analytics are **not** new roadmap items — they answer *is this output
good*, which
[§ What Trustvian is not becoming](#what-trustvian-is-not-becoming) already
excludes. 089 stays `PROPOSED`.

**No infrastructure is adopted by imitation.** Reserving these numbers creates
no dependency on an analytical store, an object store, a queue or an
orchestrator. 068 keeps the conditional analytical-store decision and 069 keeps
load and multi-node validation; if PostgreSQL satisfies measured requirements,
nothing is added.

**MCP stays [task 015](tasks/015-trustvian-mcp.md)** and trace interoperability
stays 090. 091 duplicates neither — it records only that 015 becomes materially
more useful once 085 exposes stable finding resolution, with read-only as the
strong default for a first slice.

### Execution order, rather than numeric order

What is built next is a dependency question, and the numbers do not answer it.
Conceptually:

```text
066  promotion workflow
       ↓
074  zero-input live WebUI ──▶ 077  unified local dev runtime  [implemented]
                                          │
075  AI semantic telemetry ───────────────┤
       │                                  ↓
       │                         083  behavioral layer identity  [part shipped]
       │                                  ↓
       │                         078  behavioral scenario suites
       │                                  ↓
       │                         079  CI integration (GitHub Action)
       │                                  ↓
       │                         086  scenario and input versioning
       │
       └──────────▶ 080  detection evaluation
                             │        (measurement; gates nothing)

084  correlation and operational evidence   [additive, no migration, anytime]
       │      parent span · duration · error status
     ═══════════ v0.10.0 developer preview ships here ═══════════
       │
       └──▶ 087  performance and cost evidence

067  event history   [implemented, schema 7]
       │   085's retention needs were specified alongside it, not after it
       └──▶ 085  evidence resolution   [implemented, no schema step]
                   │   control plane + /v1 + CLI; shipped without a browser
                   ├──▶ 076  behavioral evidence explorer   [implemented]
                   │          navigates 085; needed 084 for the timeline
                   └──▶ 088  review decisions and annotations
                              [needs 066; wants 070 for authorship]

069  multi-node and load validation
070  platform security hardening
071  backup, restore and upgrade
       ↓
072  v1.0 release gate

068  ClickHouse — only if measured volume justifies it

═══════════ v1.0 ships here ═══════════

091  platform analytics and developer ecosystem   [planning only]
       ├──▶ 092  behavioral analytics        [optional enrichment from 087]
       ├──▶ 093  behavioral alert rules      [independent of 092]
       ├──▶ 094  public API contract and typed clients   [independent]
       └──▶ 095  saved investigations        [needs 085 only]
                   └──▶ future extension of the Evidence/076 surface
```

**075 now precedes 078, and that edge was added by a measurement.** It used to
run beside this thread, on the reasoning that repetition transports whatever
telemetry exists while 075 decides how richly it is read. That is still true for
*running* a scenario and false for **deciding 078's thresholds** — see
[078 § Sequencing](tasks/v1.0/078-behavioral-scenario-suites.md#sequencing-078-follows-075).
Two pieces of 078 were not blocked by it and landed first: a run-scoped behavior
route that 079 and 080 both need, and an additive `--behavioral-profile` flag on
`trustvian dev`. 078 is now implemented.

Twelve things this diagram says, and one it does not:

- **074 and 075 were independent of each other** and both are implemented.
  076 needs both, plus whatever history 067 makes durable.
- **075 did not block 077**, and both are implemented. The local dev runtime
  transports whatever telemetry the workload emits; 075 decides how richly it is
  read. `trustvian dev` was useful at HTTP, DB and RPC fidelity before 075 landed
  and reports tool-level behavior now that it has. They were parallel
  capabilities, which is how they were built.
- **075 does block 078**, which is a change and the one edge here that was added
  by evidence rather than by design. 078's central mechanism is a k-of-N
  threshold over how many runs showed a behavior, and its own specification
  required that threshold be measured before being written down. The measurement
  was performed against a real local model — forty runs, two temperatures, two
  learning configurations — and returned a **zero** false-FAIL rate, because at
  HTTP fidelity a behavior is a method and a destination and the workload's
  behavioral surface was saturated. At 075's tool-name fidelity the surface is the
  size of the toolset instead, which is where the phenomenon can appear at all.

  **That edge is now satisfied: 075 is implemented.** What remained before 078
  could be built was not 075 but 078's own re-run — a workload whose toolset is
  wider than one run visits, measured at the tool-name fidelity 075 delivers.
  That re-run was recorded on 2026-10-01
  ([re-run conditions](tasks/v1.0/078-behavioral-scenario-suites.md#the-section-stays-with-two-re-run-conditions)),
  and 078 is implemented.
- **077, 078 and 079 are a second thread**, needing 074's discovery but not the
  explorer. 078 needs 077's repeatable invocation *and* 075's fidelity, and 079
  needs 078's machine-readable result and exit codes — it renders them and
  computes nothing.
- **The `v0.10.0` line is a release boundary, not a dependency edge.** It marks
  where the [developer preview](#v0100--developer-preview) ships. Nothing above
  it depends on anything below it, which is the property that makes the preview
  possible at all; nothing below it is dropped or weakened by shipping early.
- **080 sits beside the thread rather than in it.** It needs 075 for tool-level
  fidelity, and reuses 078 if the harness would otherwise reimplement repeated
  isolated execution — but nothing waits for it and no release gates on it. It
  measures the signals that already exist, so it could in principle run today
  at transport fidelity, and would answer a weaker question.
- **068 remains conditional**, exactly as its row says: an analytical backend
  arrives if measured volume justifies one, and not otherwise. It is not a
  release-gate prerequisite, and this restructuring does not make it one.
- **083 is implemented, and 078's re-run is unblocked.** Its classification half
  shipped first; its counting half now folds on top of 084's parent identity,
  under the rule
  [ADR 0052](adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)
  states. 078's k-of-N thresholds count behaviors, and there are now two units
  to choose between — added identities and counted changes — both reported and
  both named, which is what the re-run was waiting for.
- **083 joins the 078 thread ahead of 078**, and that edge is the second one here
  added by a measurement rather than by design. One behavioral change was reported
  as two added behaviors, because a tool span and the HTTP request beneath it are
  two behavioral identities and no rule says which wins. 078's k-of-N presence
  counts are counts of *behaviors*, so re-running its threshold measurement before
  the counting rule is fixed would measure the double count and then write the
  number down.
- **084 is implemented, and it unblocked an input rather than a feature.** Parent
  span identity, duration and error status are now on `event.Context`,
  `DecisionRecord` and the run aggregate. 076 can draw a timeline with timing and
  errors, 085 can link a finding to a slow call, and 087 has latency to compare —
  none of which it delivers. It also gave 083's counting fold the parent ids it
  lacked. 083 has since closed that: the correlation structure is retention's
  own bounded history rather than a new one, the out-of-order rule is that
  edges resolve over the whole history rather than as records stream, and a
  folded act reports as one change naming its contributing identities.
- **085 is *specified* alongside 067 and *delivered* before 076**, which are two
  different orderings. Evidence resolution needs a stable, resolvable identity for
  a finding and for an observation, so 067 should choose its retention contract
  knowing that — instead of 076-style amendment afterwards, which 076 itself
  records as the thing to avoid. And the capability is authoritative at the
  control plane: it owns its `/v1` route and CLI access and ships validated with no
  browser change, after which 076 navigates it and 079 renders a link to it. **The
  edge is one-way** — an earlier draft had 076 and 085 depending on each other,
  which is a cycle, and the invariant survives the split unchanged: the control
  plane owns resolution and no adapter recomputes a finding or a verdict.
- **089 is absent from the diagram deliberately.** A PROPOSED item has no position
  in a dependency order until the decision that would create it is made, and
  nothing above waits for it.
- **090 is absent for the opposite reason**: it depends on nothing, blocks
  nothing, and is a documented configuration plus a deployment profile.

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
22. **At least five developers outside this project have completed the journey
    above on their own agents**, and the friction they hit is recorded — as
    linked issues, or as a written report naming where each of them got stuck.

Criteria 20 and 21 are the architectural invariants this whole direction rests
on, which is why they are release gates rather than guidelines.

Criterion 22 is the only one no amount of internal work can satisfy, and it is
here because every other criterion is technical. The journey above opens with
*"by a developer who has not read the source"* — and until developers who have
not read the source have actually walked it, that phrase describes an intention
rather than a measurement. This document's own principle is **verified, not
assumed**; a usability claim verified only by the people who built the thing is
assumed.

Five is a deliberately small number. It is not a market test and not an adoption
target — it is the smallest sample that reliably surfaces the friction one
developer's particular setup would hide. *Their own agents* is the load-bearing
half: the repository's demo workload is the one instrumented agent this project
is guaranteed to get right.

And the friction being **recorded** is half the criterion. Five developers who
succeed and say nothing leave the project exactly where it started; five who get
stuck in five places named in five issues are what makes the next milestone
obvious. A criterion satisfied by silence would be satisfied by not asking.

Criteria 9 through 15 are the gap-closing milestones 074–078, which is why
tasks numbered above the release gate nonetheless block it. Task 068 is
**not** among them: it stays conditional on measured volume and is not a
prerequisite for `v1.0`.

Criteria 9 through 15 and criterion 22 are also where the
[developer preview](#v0100--developer-preview) earns its place: 9 through 13 and
15 ship there, so criterion 22's developers have something to use well before
the tag that depends on their having used it.

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

**Tasks 074–080 do not move that line, and it is worth saying why.** Adding AI
semantic spans, a session view, trace correlation, behavior visualization,
scenario suites and a CI comment makes Trustvian *better at reading
observability evidence*. It does not make Trustvian an observability product,
because the question it asks of that evidence is unchanged:

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

**Task 080 is the one that looks like it crosses the line, and does not.** It
runs a public agent prompt-injection benchmark, which is a thing model
evaluations also do, so the distinction is stated rather than assumed:

```text
model benchmarking asks    which model or defense resists injection best?

task 080 asks              do Trustvian's existing signals notice the
                           resulting tool misuse, from metadata alone, and
                           at what false-positive cost?
```

The measured subject is **Trustvian**, not the agent. No model is ranked, no
model is compared with another, and a model that resists the injection entirely
is not recorded as a Trustvian success — that run is *excluded*, because
counting it would make Trustvian's score a function of the model's robustness,
which is the confusion this paragraph exists to prevent. No prompt or completion
content is retained, in the pipeline or in the published result. And nothing
ships: 080 adds no signal, no scoring change and no product surface. It measures
what already exists, which is the opposite of a new evaluation capability.

It is there for a reason worth stating plainly: *behavioral observability does
not require content observability* is the claim this whole direction rests on,
and it is currently argued rather than measured. A principle nobody has tried to
falsify is a preference.

Still outside the product, and not made less so by any of this: prompt
management and playgrounds, LLM-as-a-judge, hallucination and groundedness
scoring, RAG relevance metrics, model benchmarking, prompt or completion
warehousing, generic dataset platforms, and provider marketplaces.

#### The proposal that is not approved

One roadmap item asks to move the line above, and it is recorded rather than
acted on. Item 089 — optional quality evaluation, and a prompt playground after
it — is **PROPOSED**: written down so the decision can be made deliberately
instead of drifted into. It is not approved, nothing depends on it, and **closing
it is a legitimate outcome that costs nothing already built**. See
[task 082](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md).

Naming it here is the point. The paragraphs above are a decision with reasoning,
and a planning task does not get to reverse one quietly by adding a row to a
table. Five things would have to be decided first, and three of them are
prerequisites rather than preferences:

- **Whether the boundary moves at all.** If it does not, the item closes.
- **Whether a quality score may ever reach a gate.** If it may,
  [ADR 0029](adr/0029-hard-gates-use-explicit-integer-evidence.md)'s
  integer-evidence reasoning has to be revisited for a number that is inherently
  a float. If it may not, the item shrinks to reporting only, which is a far
  smaller and safer thing.
- **Content availability.** A model judge needs the prompt, the completion or the
  tool result, and Trustvian refuses all three at every durable and published
  surface. The
  [privacy prerequisites](tasks/v1.0/082-agent-inspection-and-evaluation-depth.md#privacy-prerequisites)
  — collection-time filtering, redaction, retention, access control, deletion and
  in-product disclosure — are their own reviewed capability, not a paragraph
  inside a feature. Until they exist, a content-based capability is not "later",
  it is unspecifiable, and it must never be presented as partially available.
- **Judge fallibility.** A model judge's output is evidence from a fallible
  instrument and would have to be recorded as one — with its model, version,
  configuration and prompt digest — never as a measurement.
- **Replay side effects.** Replaying a recorded execution re-issues what it did.
  Replaying `send_email` sends email. No replay may be specified before its mocked
  or sandboxed boundary is, and "the developer will be careful" is not a boundary.

What is **not** in that proposal, and is ordinary planned work: reading what the
conventions already carry. Token counts are metadata a producer emits, latency and
error status are properties of an observed action, and a cost figure computed from
an operator's own declared pricing is arithmetic over both. Item 087 does those and
crosses none of the lines above — it compares a candidate against its reference
and ranks no model against another, which is the distinction task 080's own
paragraph already draws.

No message broker is planned. No analytical store is a dependency of the
engine. Neither becomes one without a measurement behind it.

### The WebUI product model

The information architecture the interface builds toward, at roadmap level.
Labels will be refined during implementation; the ordering is the point.

```text
Live          active actors · animated behavior topology · new behavior
              trust · anomaly · risk · decision                        (074)

Evidence      sessions · traces · behavioral sequence            (076, shipped)
              correlation and explanation                              (076)
              trace tree · timeline · errors · duration            (076 over 084)

Evaluations   runs · reference and candidate · diff · scorecard · gate
              every finding links to the observations behind it  (076 over 085,
                                                                    shipped)
              which prompt, model, toolset, config and inputs          (086)
              latency · errors · tokens · cost, where telemetry says   (087)

Review        annotations on findings · acknowledgements
              recorded beside the verdict, never replacing it          (088)

Analytics     behavior · risk · gate and change trends
              over authoritative evidence, server-computed        (092, post-v1)

Promotions    environment advancement decisions                        (066)

Manage        projects · agents · candidates · environments
              advanced and manual operations
```

```text
Manage is not the landing page. Live behavioral understanding is.
```

Today the shipped WebUI opens on Manage, in effect — a form asking for an
identifier. That inversion is the gap task 074 closes, and everything above it in
the list is what 074–078 and 083–088 add.

**Analytics is a view like every other row**, and the rule below binds it hardest:
a browser that crawled raw observations to compute a platform-level aggregate
would be a second engine that is also wrong about a page boundary. Every number
it shows comes from an authoritative server response. Saved investigations (095)
support Evidence, Evaluations and Review rather than becoming a navigation row of
their own.

**Two properties of this list are load-bearing, not incidental.** Every row is a
*view* — criterion 19 holds throughout, and no interface carries its own copy of
evaluation, scoring, policy, diff, gate, promotion or link-resolution logic; a
browser that recomputed a number would be a second engine. And no row renders
content: the fields each one shows are named by the retention contract behind it,
enforced by a tripwire test rather than by review. Review is a new row and the
first place a human writes free text into durable state, which its own item
addresses directly.

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

**Policy-engine interoperability is the first integration candidate after the
[developer preview](#v0100--developer-preview).** It means one thing
specifically: exporting Trustvian's behavioral evidence as an *input* to
external policy and enforcement points — Microsoft's Agent Governance Toolkit,
agentgateway, and Open Policy Agent are the three concrete shapes to evaluate —
rather than Trustvian acquiring an enforcement point of its own.

It fits the existing rules rather than bending them. A gate result and a
behavioral diff are already fixed-shape, metadata-only values, so an exporter
publishing them reaches into nothing and adds no content surface. And the
direction is the one this document has taken throughout: *evidence, not
verdicts* — Trustvian quantifies and explains, and something else decides what
to enforce. An integration that asked Trustvian to make the enforcement decision
would be the opposite of this candidate, not a larger version of it.

It stays **FUTURE and unscoped**: no task file, no design, no committed release,
no chosen target among the three. "First candidate" orders a queue; it does not
approve work. The opening sentence of
[Beyond v1.0](#beyond-v10) governs this paragraph exactly as it governs the
rest of the section.

**Trace-backend interoperability is not in this section**, and the distinction is
worth one sentence: it is scoped, cheap and numbered (item 090), because the
Collector fan-out it needs already exists and is already deliberate. A developer
who wants a waterfall runs a trace backend beside Trustvian, and
[ADR 0046](adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md)
records why that is interoperability rather than a dependency or a source of code.
That ADR is **Proposed**, not Accepted, and it is explicit that forwarding a span
forwards whatever content the producer put in it — so the integration is off by
default and its export mode is named when it is enabled.

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
