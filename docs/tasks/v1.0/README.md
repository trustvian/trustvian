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
| [067 — Event-History Capability Boundary](067-event-history-capability-boundary.md) | Specified and implemented |
| 068–072 | Approved and sequenced in [ROADMAP.md § v1.0](../../ROADMAP.md#v10--local-first-behavioral-security-platform); **no specification written yet** |
| [073 — OTel Collector Evaluation Ingest](073-otel-collector-evaluation-ingest.md) | Specified and implemented |
| [074 — Zero-Input Live Behavior WebUI](074-zero-input-live-behavior-webui.md) | Specified and implemented |
| [075 — AI Semantic Telemetry Normalization](075-ai-semantic-telemetry-normalization.md) | Specified and implemented |
| [076 — Behavioral Trace & Session Evidence Explorer](076-behavioral-evidence-explorer.md) | Specified and implemented — two presentations narrowed, both because 067 retains neither fidelity nor sequence-deviation evidence |
| [077 — Unified OTLP Local Dev Runtime](077-unified-otlp-local-dev-runtime.md) | Specified and implemented |
| [078 — Behavioral Scenario Suites](078-behavioral-scenario-suites.md) | Specified; not implemented — sequenced after 075, and its k-of-N thresholds await a measurement that can fail |
| [079 — CI Integration: A GitHub Action](079-ci-integration-github-action.md) | Specified; not implemented |
| [080 — Metadata-Only Detection Evaluation](080-metadata-only-detection-evaluation.md) | Specified; not implemented |
| 081 | Approved in [ROADMAP.md](../../ROADMAP.md#milestone-sequence); **no specification written yet** — deferred from 075 |
| [082 — Agent Inspection and Evaluation Depth](082-agent-inspection-and-evaluation-depth.md) | Specified. Documentation and planning only; implements nothing |
| [083 — Behavioral Layer Identity and Display Classification](083-behavioral-layer-classification.md) | Specified and implemented — classification and rendering shipped first, the counting correction folds on 084's parent identity ([ADR 0052](../../adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)). The optional gate limit over counted changes, `max_added_behavior_changes`, is built (issue 131, schema 8) |
| [084 — Correlation and Operational Evidence on the Record Boundary](084-correlation-operational-evidence.md) | Specified and implemented |
| [085 — Evidence Resolution](085-evidence-resolution.md) | Specified and implemented |
| 086–090 | Reserved and scoped by [082](082-agent-inspection-and-evaluation-depth.md); **no specification written yet.** 089 is **PROPOSED** rather than approved |
| [091 — Platform Analytics and Developer Ecosystem](091-platform-analytics-and-developer-ecosystem.md) | Specified. Documentation and planning only; implements nothing |
| 092–095 | Reserved and scoped by [091](091-platform-analytics-and-developer-ecosystem.md); **no specification written yet.** Post-`v1.0` platform depth — none is a release gate |
| [096 — Record-First Admin Console](096-record-first-admin-console.md) | Specified and implemented — presentation and information architecture only; no route, capability or bound changes |

The numbers 068–072 are the approved plan, not placeholders — the sequence,
its ordering, and what each milestone covers are decided. What does not exist
is the specification for any of them. **067 was the first of that block to be
specified**, and its specification was written alongside the retention needs
[085 states](082-agent-inspection-and-evaluation-depth.md#085--evidence-links-from-a-finding-to-the-observations-behind-it)
rather than after them, which is the ordering the roadmap asked for and the
mistake [076](076-behavioral-evidence-explorer.md) records having avoided.

Task 067 is **implemented**. It is the first layer allowed to retain anything
per observation, so it settles what that means before anything reads it: an
observation is identified and ordered by `(run, ingest sequence)` — never by a
span id, an event id or a timestamp, because none of those is unique, dense and
allocated by this platform's own admission decision. The row is written inside
the transaction that already writes the aggregate, the snapshot and the cursor,
so a rejected record or a failed commit retains nothing and a retry produces no
second row. Retention is bounded at 4096 observations per run, and passing the
bound degrades the evidence rather than failing the ingest — the rule the
behavior collector already applies at 512 distinct behaviors.

Its honesty property is the one worth stating twice: a run's history has
**three** states, because two would force a lie. A schema-6 database's runs have
records and no observations, and reporting "complete, zero rows" for one would
fabricate a historical fact — so the migration adds an empty history and those
runs report *unavailable*. Its schema step is **6 → 7**, two tables and three
run-scoped indexes and no row.
[ADR 0048](../../adr/0048-retained-history-is-sequence-identified-bounded-and-honest-about-absence.md)
records the reasoning.

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

**Numbering is identity, not execution order.** 068–072 keep their original
identity and scope; 073–080 close concrete gaps; and 072 remains the release
gate while now depending on several tasks numbered above it. That is correct
rather than untidy: a number records when a milestone entered the plan, and
renumbering one would break every specification, ADR, commit message and
document already citing it. `ROADMAP.md` carries the dependency order.

**091 is a planning task in 082's shape, and reserves nothing for `v1.0`.** It
names the capabilities that become valuable once a developer can already
observe, retain, explain, compare and gate, with finding review scoped by 088 —
aggregate questions across
runs (092), rules that notify when a behavioral condition holds (093), a
described `/v1` contract with typed clients (094), and saved investigation
context (095). Reserving those numbers authorizes no implementation, and none of
them is a `v1.0` blocker. MCP stays [015](../015-trustvian-mcp.md) and trace
interoperability stays 090; 091 creates no duplicate of either.

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
- **075 — AI Semantic Telemetry Normalization** is **implemented**. Zero-code
  instrumentation flattens an agent into its transport: five distinct services
  become `POST` and `GET` against hostnames. Trustvian now reads OpenTelemetry
  GenAI and OpenInference where a producer emits them, so a behavior reads
  `tool · export_customer → export.localhost` instead of `http · POST /v1/export`.
  One table in `internal/semconv`, imported by both adapters, importing no
  OpenTelemetry package itself — which cost nothing to arrange, because the GenAI
  keys appeared in Go's `semconv` around v1.39.0 and were **gone by v1.42.0**, the
  version both adapters pin. Identity attributes are read and all twenty-two
  content attributes are refused; the privacy claim is a durable-evidence boundary
  rather than a claim about the transient attribute map, asserted at every
  enforcing layer and verified to catch a planted leak.
  [ADR 0045](../../adr/0045-conventions-are-read-frameworks-are-not.md) records
  why conventions are read and frameworks never named. One piece is deferred as
  its own task: fidelity is not persisted per behavior, so comparison deltas do
  not carry it.

  The rest of what the specification asked for holds as written: the same
  pipeline, no AI-specific engine, no framework dependency, and a privacy
  guarantee stated where it actually binds — prompts, completions and arguments never
  become behavioral identity, a `DecisionRecord` field, a realtime field, a
  persisted row or a published payload. The adapters' documented
  preserve-every-span-attribute behavior is unchanged, and the specification
  does not pretend otherwise.
- **076 — Behavioral Trace & Session Evidence Explorer** is **implemented**. A
  verdict without its evidence is not explainable, and `DecisionRecord` already
  carried trace, span and session correlation the platform received and did not
  retain. An **Evidence** tab now leads from a gate check or a behavioral delta
  to the behaviors that contributed and the observations that carried them, and
  from one observation into its session, its trace's recorded parent/child
  structure, the run's behavior sequence, its decision timeline and one
  behavior's detail — with no identifier typed anywhere in that path. It stores
  nothing and resolves nothing: **067** owns retention, **085** owns resolution,
  and this surface reads both. The one capability it added is three optional,
  mutually exclusive equality narrowings on the existing observation route,
  recorded in [ADR 0049](../../adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md);
  there is no schema change. **Two presentations narrowed** because 067 retains
  neither fidelity nor sequence-deviation evidence — both say so on screen
  rather than inferring what they would have shown.
- **077 — Unified OTLP Local Dev Runtime** is **implemented**. Watching an agent
  used to mean a control plane, a Collector, a processor config, a manually
  created hierarchy and the right OTLP environment. `trustvian dev -- <command>`
  now composes all of it: it supervises `trustvian-local` and
  `trustvian-collector` rather than opening its own receiver
  ([ADR 0042](../../adr/0042-dev-composes-the-collector-rather-than-owning-a-receiver.md)),
  derives the hierarchy from the git repository and keeps its state outside it
  ([ADR 0043](../../adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md)),
  and its exit status is the workload's own. The application is not modified and
  gains no Trustvian dependency. Instrumentation ownership is an explicit,
  positive-evidence-only mode: a child may initialize OpenTelemetry after it
  starts, so absence of detectable instrumentation never selects injection
  ([ADR 0044](../../adr/0044-instrumentation-ownership-requires-positive-evidence.md)),
  which is why `python-zero-code` is named, reserved and refused rather than
  half-attached. **Independent of 075** — it transports whatever telemetry
  exists, and 075 decides how richly that telemetry is read. (078 is *not*
  independent of 075; see below.)
- **078 — Behavioral Scenario Suites.** Diff, scorecard and gate all exist;
  what is missing is repeatability. A scenario runs the same workload again,
  compares the behavioral surface and applies deterministic limits — reusing
  the control plane for every verdict, and evaluating no answer quality. The
  runner scripts no ordering of its own and overrides none of the engine's
  learned sequence evidence: a reorder that drives the engine to a block
  decision or critical risk fails the gate, exactly as it should. Each scenario
  runs N times per side, and `N = 1` reproduces the single-pair semantics exactly.

  **Its own measurement rule fired, and the specification changed.** The task
  required its k-of-N thresholds to be measured before being written down, and
  said the measurement was allowed to refute it. Forty model-driven runs of an
  unchanged agent — two temperatures, two learning configurations — produced a
  **zero** false-FAIL rate. The reason is the useful part: at HTTP fidelity a
  behavior is a method and a destination, that workload's behavioral surface was
  saturated, and behavioral identity is not sequence-dependent — so the property
  the workload was chosen for, varying action order, is exactly the one the diff
  ignores. The task is **not** deleted and **not** implemented on an expectation:
  it now ships no default `k`, recommends set semantics (`k = 1, j = 0`) until a
  workload's variance is measured, is sequenced after **075** because tool-name
  fidelity is where the phenomenon can appear, and carries two re-run conditions.
  Two pieces of it are exempt and can land first — a run-scoped behavior route
  that 079 and 080 both need, and an additive `--behavioral-profile` flag on
  `trustvian dev`.
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

**Task 082 is a planning task, in 049's shape**, and 083–090 are what it reserves.
It decomposes one sentence — *inspect what your agent did, understand what changed
between versions, and make release decisions using evidence* — into
dependency-ordered items, and it verified the current state against source rather
than against this file's previous description of it. Two findings shaped
everything it planned:

- **Nothing links a finding to its evidence.** A gate FAIL names a count and a
  behavioral delta names a fingerprint; neither says *which observations*. That is
  the first thing a developer whose gate just failed wants, and it is why they open
  a second tool — which the Track B gate rules out in the sentence directly
  beneath its journey. Item 085 owns the capability at the control plane; item
  076 navigates it in a browser.
- **The record boundary is narrower than the adapter.** Trustvian reads the
  agent-oriented conventions well (075), then carries less across
  `DecisionRecord` than it read: no parent span identity, no duration, no error
  status, no token counts — even though both adapters already compute duration and
  error for `features.Extract`. So a timeline with timing and errors is blocked on
  the *record*, not on the telemetry. Item 084.

And one item is a correction rather than an addition: **083** exists because a
measurement found a single behavioral change reported as two added behaviors — an
instrumented tool call and the HTTP request beneath it are two behavioral
identities, and no rule says which counts. It has to land before 078's threshold
measurement is re-run.

Two of the eight touch the `v1.0` gate (083 and 084, because they change what
criteria 12, 14 and 17 already mean) and **no criterion is added, removed or
weakened**. One — **089**, optional quality evaluation — is **PROPOSED**: it
contradicts
[What Trustvian is not becoming](../../ROADMAP.md#what-trustvian-is-not-becoming)
as written, nothing depends on it, and closing it is a legitimate outcome. One —
**090** — is an optional integration recorded in
[ADR 0046](../../adr/0046-trace-backends-are-interoperability-targets-not-dependencies.md),
which is **Proposed** rather than Accepted.

**Task 083 is partially implemented, and says so.** It resolved the two design
questions 082 left open — behavioral identity stays per observation, and there is
no `model` operation category — and shipped the half that follows from them: a
non-identity `Layer` classification carried where fidelity is carried, and honest
rendering of model calls, tool calls, retrievals and transport operations — the
last of which states no direction, because a transport classification covers
inbound and outbound spans alike.

**Task 084 is implemented.** A record now carries parent span identity, span
lineage, duration in nanoseconds and span status, and the platform aggregates
duration and status per run and persists them (schema **6**). Availability is
explicit throughout: an unmeasured duration is not a duration of zero, and
neither an unset status nor an absent one is success. Nothing it adds touches
behavioral identity, and the volatile feature bridge is unchanged.

**The counting correction is deferred, and the task stays open.** Folding a tool
observation and the request beneath it into one counted change needs to know they
are parent and child, and no parent identity reaches the evidence boundary —
verified, not assumed, by a test that fails when it changes. Identity was
deliberately *not* folded to work around it: folding drops the destination from
behavioral identity, so a tool that started posting to another host would stop
changing the behavioral surface. Five of seven acceptance criteria are met, the
sixth is the task's point, and
[ADR 0047](../../adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
records where the fix belongs and what it must not do.

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
