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
deployment and no candidate residence, both verdicts of the gate are recorded
while a malformed request is not, and the evidence it keeps is exactly what
cannot be recomputed from the immutable runs it names.

Task 073 sits **after** that reserved block rather than inside it. It was not
in the approved sequence: it closes a gap the sequence did not anticipate — the
Collector produces a `Result` and the control plane accepts a `DecisionRecord`,
and nothing joined them, so a workload observable only through OpenTelemetry
could not be evaluated at all. Taking 065 for it would have renamed a milestone
whose scope is already decided.

Task 063's open question — collection semantics — was recorded in its
specification rather than resolved: the WebUI navigates by caller-known ID, and
a list route was deferred to the milestone that first has concrete filtering
requirements. [Task 065](065-environment-model.md) is that milestone, and it
resolved it narrowly: one collection, for one entity, scoped to a project,
traversed by the immutable `ref`, and bounded twice — the entity capped at
creation and the response capped per page, because a migrated database may
already hold more than the cap allows anyone to create. No other list route
was added.

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
