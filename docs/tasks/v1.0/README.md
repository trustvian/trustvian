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
| [063 — Minimal Web Control Plane](063-minimal-web-control-plane.md) | Specified |
| 064–072 | Approved and sequenced in [ROADMAP.md § v1.0](../../ROADMAP.md#v10--local-first-behavioral-security-platform); **no specification written yet** |

The numbers 064–072 are the approved plan, not placeholders — the sequence,
its ordering, and what each milestone covers are decided. What does not exist
is the specification for any of them.

Task 063 is **specified but not implemented**: the document describes what will
be built and why, and no WebUI exists yet.

**Each task gets its own written spec before implementation starts**, in the
shape the completed tasks in [`../../archive/tasks/`](../../archive/tasks/README.md)
use: objective, why, scope, non-goals, technical requirements, tests,
benchmarks, documentation, acceptance criteria. A roadmap row is not a
specification, and the row does not authorize writing code against it.

Tasks [015](../015-trustvian-mcp.md) and [016](../016-control.md) stay in the
parent directory. Neither belongs to this milestone: 015 is a future phase
that has not started, and 016 is a standing architectural constraint rather
than a unit of work.
