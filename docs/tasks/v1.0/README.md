# v1.0 Tasks

Tasks for the `v1.0` milestone.

| Task | Status |
|---|---|
| [049 — Platform Architecture Alignment](049-platform-architecture-alignment.md) | Specified. Documentation only; implements nothing |
| [050 — Public Serializable Decision Record](050-public-serializable-decision-record.md) | Specified and implemented |
| [051 — Behavioral Profile: Learning-Scope Isolation](051-behavioral-profile-learning-scope-isolation.md) | Specified and implemented |
| [052 — Evaluation Domain](052-evaluation-domain.md) | Specified and implemented |
| [053 — Evaluation Result Aggregation](053-evaluation-result-aggregation.md) | Specified and implemented |
| [054 — Behavioral Diff](054-behavioral-diff.md) | Specified and implemented |
| [055 — Evaluation Scorecards](055-evaluation-scorecards.md) | Specified and implemented |
| [056 — Deterministic Hard Gates](056-deterministic-hard-gates.md) | Specified and implemented |
| [057 — Local Platform Persistence](057-local-platform-persistence.md) | Specified and implemented |
| [058 — Local Control-Plane API and Ingest](058-local-control-plane-api-and-ingest.md) | Specified and implemented |
| [059 — Realtime Infrastructure](059-realtime-infrastructure.md) | Specified and implemented |
| [060 — Developer CLI](060-developer-cli.md) | Specified and implemented |
| [061 — Terminal Dashboard (TUI)](061-terminal-dashboard.md) | Specified and implemented |
| [062 — Integrated Local Developer Workflow](062-integrated-local-developer-workflow.md) | Specified and implemented |
| [063 — Minimal Web Control Plane](063-minimal-web-control-plane.md) | Specified and implemented |
| [064 — PostgreSQL Platform Backend](064-postgresql-platform-backend.md) | Specified and implemented |
| [065 — Environment Model](065-environment-model.md) | Specified and implemented |
| [066 — Promotion Workflow](066-promotion-workflow.md) | Specified. Not implemented |
| 067–072 | Approved and sequenced in [ROADMAP.md § v1.0](../../ROADMAP.md#v10--local-first-behavioral-security-platform); **no specification written yet** |
| [073 — OTel Collector Evaluation Ingest](073-otel-collector-evaluation-ingest.md) | Specified and implemented |
| [074 — Zero-Input Live Behavior WebUI](074-zero-input-live-behavior-webui.md) | Specified; not implemented |
| [075 — AI Semantic Telemetry Normalization](075-ai-semantic-telemetry-normalization.md) | Specified; not implemented |
| [076 — Behavioral Trace & Session Evidence Explorer](076-behavioral-evidence-explorer.md) | Specified; not implemented |
| [077 — Unified OTLP Local Dev Runtime](077-unified-otlp-local-dev-runtime.md) | Specified; not implemented |
| [078 — Behavioral Scenario Suites](078-behavioral-scenario-suites.md) | Specified; not implemented |

The numbers 067–072 are the approved plan, not placeholders — the sequence,
its ordering, and what each milestone covers are decided. What does not exist
is the specification for any of them.

Task 065 is implemented. A project owns environments, identified by the
`EnvironmentRef` a run already records; a run may only name one its own
project owns and has not archived; `CanPromote` is the single ordering
primitive task 066 asks and it authorizes nothing; and schema 3 backfills
every environment a schema-2 database's runs referenced, whatever the creation
cap now allows.

Task 066 is **specified and not implemented**. It is the first layer allowed
to decide that a candidate may advance between environments, and the last one
that could be mistaken for deploying something — so the specification settles
that first: a promotion is a durable record of a *decision*, it models no
deployment and no candidate residence, and both verdicts of the gate are
recorded while a malformed request is not. It keeps the gate result the
decision actually consumed — field for field, every check's verdict flag
included — rather than promising that today's code would re-derive it, because
a corrected gate may legitimately answer differently from the same evidence and
history has to survive its own bug fixes. And it commits that decision only
against the environment configuration it was decided on: both environment
states are revalidated inside the transaction that writes the promotion, on
both backends, with row-level writer concurrency where PostgreSQL offers it and
plain write-transaction serialization where SQLite does not.

Tasks 073–078 sit **after** that reserved block rather than inside it. None
was in the approved sequence; each closes a gap the sequence did not
anticipate, found by running the product end to end — an instrumented agent, a
browser, and a developer who has not read the source.

**Numbering is identity, not execution order.** 067–072 keep their original
identity and scope; 073–078 close concrete gaps; and 072 remains the release
gate while now depending on several tasks numbered above it. That is correct
rather than untidy: a number records when a milestone entered the plan, and
renumbering one would break every specification, ADR, commit message and
document already citing it. `ROADMAP.md` carries the dependency order.

Task 073 is **implemented**: the Collector produced a `Result` and the control
plane accepted a `DecisionRecord`, and nothing joined them, so a workload
observable only through OpenTelemetry could not be evaluated at all.

**Tasks 074–078 are specified and not implemented.** Together they are the
difference between a platform that works and one a developer can pick up:

- **074 — Zero-Input Live Behavior WebUI.** Opening the WebUI shows a form
  asking for an identifier the developer does not have, so *run locally,
  observe behavior live* requires reading one out of a producer's logs. The
  milestone discovers active work from the realtime stream it can already
  subscribe to unfiltered, renders a bounded run-scoped behavior graph, and
  rediscovers the durable hierarchy after a reload through bounded routes —
  one request at startup, no automatic continuation, descent only when asked.
  Its schema step is **4 → 5**, which places it after 066 in the chain.
- **075 — AI Semantic Telemetry Normalization.** Zero-code instrumentation
  reduces an agent's tool call to an HTTP POST against a hostname, so the
  baseline learns transport shapes rather than behavior. Where a producer
  emits agent-oriented OpenTelemetry, Trustvian reads it — same pipeline, no
  AI-specific engine, no framework dependency, and no prompt, completion or
  argument ever retained.
- **076 — Behavioral Trace & Session Evidence Explorer.** A verdict without
  its evidence is not explainable, and `DecisionRecord` already carries trace,
  span and session correlation that the platform receives and does not retain.
  The explorer presents sessions, traces and behavioral sequence over whatever
  history **067** makes durable — 067 keeps ownership of the storage contract.
- **077 — Unified OTLP Local Dev Runtime.** Watching an agent today means a
  control plane, a Collector, a processor config, a manually created
  hierarchy and the right OTLP environment. One command should wrap an
  existing agent and compose the rest, with no source modification and no
  Trustvian dependency in the application.
- **078 — Behavioral Scenario Suites.** Diff, scorecard and gate all exist;
  what is missing is repeatability. A scenario runs the same workload again,
  compares the behavioral surface and applies deterministic limits — reusing
  the control plane for every verdict, and evaluating no answer quality.

Taking a number inside 049–072 for any of them would have renamed a milestone
whose scope is already decided.

Task 063's open question — collection semantics — was recorded in its
specification rather than resolved: the WebUI navigates by caller-known ID, and
a list route was deferred to the milestone that first has concrete filtering
requirements. [Task 065](065-environment-model.md) is the milestone that
resolved it, and it resolved it narrowly: one collection, for one entity,
scoped to a project, traversed by the immutable `ref`, and bounded twice — the
entity capped at creation and the response capped per page, because a migrated
database may already hold more than the cap allows anyone to create. No other
list route was added, because no other entity had a consumer that needed one.

[Task 074](074-zero-input-live-behavior-webui.md) is the first milestone with a
concrete requirement for the rest of the hierarchy: a browser that reloads when
nothing is happening has to find the Projects, Agents, Candidates and
EvaluationRuns that already exist, and the alternatives — browser storage,
direct database access, pretending realtime replays — are each refused for
their own reason. It reuses task 065's semantics unchanged rather than
inventing a second pagination shape, and it keeps the capability split: the
Project, Agent and Candidate collections belong to `ControlStore`, the
EvaluationRun collection to `EvaluationStore`, composed by the control plane.
None of this means task 063 should have built it: the deferral was correct, and
what was missing then was precisely the consumer that now exists.

Task 064 is implemented. SQLite remains the zero-configuration local default;
PostgreSQL is opt-in and must be selected explicitly. One logical
`SchemaVersion` governs both physical schemas, and a shared conformance suite
plus a SQLite/PostgreSQL differential suite is what keeps them from drifting.

**Each task gets its own written spec before implementation starts**, in the
shape the completed tasks in [`../../archive/tasks/`](../../archive/tasks/README.md)
use: objective, why, scope, non-goals, technical requirements, tests,
benchmarks, documentation, acceptance criteria. A roadmap row is not a
specification, and the row does not authorize writing code against it.

Tasks [015](../015-trustvian-mcp.md) and [016](../016-control.md) stay in the
parent directory. Neither belongs to this milestone: 015 is a future phase
that has not started, and 016 is a standing architectural constraint rather
than a unit of work.
