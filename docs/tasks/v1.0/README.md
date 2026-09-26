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
| [066 — Promotion Workflow](066-promotion-workflow.md) | Specified and implemented |
| 067–072 | Approved and sequenced in [ROADMAP.md § v1.0](../../ROADMAP.md#v10--local-first-behavioral-security-platform); **no specification written yet** |
| [073 — OTel Collector Evaluation Ingest](073-otel-collector-evaluation-ingest.md) | Specified and implemented |
| [074 — Zero-Input Live Behavior WebUI](074-zero-input-live-behavior-webui.md) | Specified and implemented |
| [075 — AI Semantic Telemetry Normalization](075-ai-semantic-telemetry-normalization.md) | Specified; not implemented |
| [076 — Behavioral Trace & Session Evidence Explorer](076-behavioral-evidence-explorer.md) | Specified; not implemented |
| [077 — Unified OTLP Local Dev Runtime](077-unified-otlp-local-dev-runtime.md) | Specified; not implemented |
| [078 — Behavioral Scenario Suites](078-behavioral-scenario-suites.md) | Specified; not implemented |
| [079 — CI Integration: A GitHub Action](079-ci-integration-github-action.md) | Specified; not implemented |
| [080 — Metadata-Only Detection Evaluation](080-metadata-only-detection-evaluation.md) | Specified; not implemented |

The numbers 067–072 are the approved plan, not placeholders — the sequence,
its ordering, and what each milestone covers are decided. What does not exist
is the specification for any of them.

Task 065 is implemented. A project owns environments, identified by the
`EnvironmentRef` a run already records; a run may only name one its own
project owns and has not archived; `CanPromote` is the single ordering
primitive task 066 asks and it authorizes nothing; and schema 3 backfills
every environment a schema-2 database's runs referenced, whatever the creation
cap now allows.

Task 066 is **implemented**. It is the first layer allowed to decide that a
candidate may advance between environments, and the last one that could be
mistaken for deploying something — so it settles that first: a promotion is a
durable record of a *decision*, it models no deployment and no candidate
residence, and both verdicts of the gate are recorded while a malformed request
is not. It keeps the gate result the decision actually consumed — field for
field, every check's verdict flag included — rather than promising that today's
code would re-derive it, because a corrected gate may legitimately answer
differently from the same evidence and history has to survive its own bug
fixes. And it commits that decision only against the environment configuration
it was decided on: both environment states are revalidated inside the
transaction that writes the promotion, on both backends, with row-level writer
concurrency where PostgreSQL offers it and plain write-transaction
serialization where SQLite does not. Its schema step is **3 → 4**, and the
migration adds an empty history: synthesizing promotions from old evaluations
would fabricate decisions nobody made.
[ADR 0040](../../adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md)
records the reasoning.

Tasks 073–080 sit **after** that reserved block rather than inside it. None
was in the approved sequence. 073–078 each close a gap the sequence did not
anticipate, found by running the product end to end — an instrumented agent, a
browser, and a developer who has not read the source. 079 and 080 come from two
different questions: *what actually reaches a user*, and *what is the detection
claim measured against*.

**Numbering is identity, not execution order.** 067–072 keep their original
identity and scope; 073–080 close concrete gaps; and 072 remains the release
gate while now depending on several tasks numbered above it. That is correct
rather than untidy: a number records when a milestone entered the plan, and
renumbering one would break every specification, ADR, commit message and
document already citing it. `ROADMAP.md` carries the dependency order.

**Not every task here is a release gate.** 079 belongs to the
[developer preview](../../ROADMAP.md#v0100--developer-preview), and 080 is a
measurement rather than a feature; neither blocks `v1.0`. They live in this
directory because that is where active specifications live, the same way 068
sits in the sequence while staying conditional on measured volume.

Task 073 is **implemented**: the Collector produced a `Result` and the control
plane accepted a `DecisionRecord`, and nothing joined them, so a workload
observable only through OpenTelemetry could not be evaluated at all.

**Task 074 is implemented; 075–080 are specified and not implemented.**
Together they are the difference between a platform that works and one a
developer can pick up — and, in 079 and 080, between a platform that works and
one whose results reach a reviewer and whose central claim carries a number:

- **074 — Zero-Input Live Behavior WebUI** is **implemented**. Opening the
  WebUI used to show a form asking for an identifier the developer did not
  have, so *run locally, observe behavior live* meant reading one out of a
  producer's logs. It now discovers active work from the realtime stream it
  could always subscribe to unfiltered, renders a bounded run-scoped behavior
  graph, and rediscovers the durable hierarchy after a reload through four
  bounded collection routes — one request at startup, no automatic
  continuation, descent only when asked. The graph is one selected run because
  `fingerprint_id` is behavioral identity and not global observation identity;
  merging two runs that share one would let one run's decision overwrite
  another's. Its schema step was **4 → 5**, three indexes and nothing else, and
  it landed after 066 as the chain required.
  [ADR 0041](../../adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
  records the reasoning.
- **075 — AI Semantic Telemetry Normalization.** Zero-code instrumentation
  reduces an agent's tool call to an HTTP POST against a hostname, so the
  baseline learns transport shapes rather than behavior. Where a producer
  emits agent-oriented OpenTelemetry, Trustvian reads it — same pipeline, no
  AI-specific engine and no framework dependency. Its privacy guarantee is
  stated where it actually binds: prompts, completions and arguments never
  become behavioral identity, a `DecisionRecord` field, a realtime field, a
  persisted row or a published payload. The adapters' documented
  preserve-every-span-attribute behavior is unchanged, and the specification
  does not pretend otherwise.
- **076 — Behavioral Trace & Session Evidence Explorer.** A verdict without
  its evidence is not explainable, and `DecisionRecord` already carries trace,
  span and session correlation that the platform receives and does not retain.
  The explorer presents sessions, traces and behavioral sequence over whatever
  history **067** makes durable — 067 keeps ownership of the storage contract.
- **077 — Unified OTLP Local Dev Runtime.** Watching an agent today means a
  control plane, a Collector, a processor config, a manually created
  hierarchy and the right OTLP environment. One command should wrap an
  existing agent and compose the rest, with no source modification and no
  Trustvian dependency in the application. Instrumentation ownership is an
  explicit, positive-evidence-only mode: a child may initialize OpenTelemetry
  after it starts, so absence of detectable instrumentation never selects
  injection. **Independent of 075** — it transports whatever telemetry exists,
  and 075 decides how richly that telemetry is read.
- **078 — Behavioral Scenario Suites.** Diff, scorecard and gate all exist;
  what is missing is repeatability. A scenario runs the same workload again,
  compares the behavioral surface and applies deterministic limits — reusing
  the control plane for every verdict, and evaluating no answer quality. The
  runner scripts no ordering of its own and overrides none of the engine's
  learned sequence evidence: a reorder that drives the engine to a block
  decision or critical risk fails the gate, exactly as it should. Each scenario
  runs N times per side, because an LLM-driven agent may call a tool in one
  execution and not the next, and a gate that FAILs on unchanged code teaches a
  team to re-run CI. `N = 1` reproduces the single-pair semantics exactly.
- **079 — CI Integration: A GitHub Action.** A gate nobody reads is a gate
  nobody acts on. 078 makes the verdict scriptable; 079 makes it legible where
  the change is reviewed — a pull request comment rendered from 078's result
  document and nothing else, with the exit codes passed through untouched so an
  unreachable control plane is never reported as a policy violation. Behavior
  descriptors come from the workload's own telemetry, so every rendered string
  is treated as author-controlled and made inert. Its security posture is the
  specification's centre of gravity rather than a footnote: **running the
  workload and holding a token that can write to the pull request live in two
  different jobs** of the same `pull_request` event, because
  `actions/checkout` persists credentials by default and a compromised
  dependency in a same-repository pull request would otherwise reach a
  write-scoped token. `pull_request_target` is refused as a trigger, and a fork
  pull request loses only the comment.
- **080 — Metadata-Only Detection Evaluation.** *Behavioral observability does
  not require content observability* is this project's central claim, reasoned
  about carefully throughout `docs/` and measured nowhere. This measures it:
  precision, recall and false-positive rate for the **existing** signals
  against a public agent prompt-injection benchmark's paired benign and
  attacked runs. It evaluates Trustvian's detection, never a model's quality —
  an attack the agent resisted is excluded rather than counted as a catch,
  which is the rule that keeps the two apart. AgentDojo is the first candidate;
  its license is MIT and it emits no OpenTelemetry today, so whether its *tool
  calls* are observable without modifying it is the feasibility question that
  comes before any number. Not a feature and published as measured.

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

[Task 074](074-zero-input-live-behavior-webui.md) was the first milestone with a
concrete requirement for the rest of the hierarchy, and is the one that
resolved it: a browser that reloads when
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
