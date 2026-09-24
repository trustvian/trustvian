# 065 — Environment Model

Status: specified, not implemented
Depends on: [052](052-evaluation-domain.md),
[057](057-local-platform-persistence.md),
[058](058-local-control-plane-api-and-ingest.md),
[060](060-developer-cli.md),
[064](064-postgresql-platform-backend.md)
Blocks: [066](README.md) — promotion workflow

## Objective

Turn the environment from a string a run happens to carry into an entity a
project owns, ordered well enough that promotion can ask one question and get
a deterministic answer.

```text
Project
  ├─ Agent ──▶ Candidate ──▶ EvaluationRun ──▶ EnvironmentRef
  └─ Environment ◀───────────────────────────────┘
       ref · name · rank? · status
```

The arrow that already exists does not change. `EvaluationRun` keeps
recording an `EnvironmentRef`, byte for byte, and this task gives that
reference something to resolve against inside the same project.

What task 066 must be able to ask, and what this task must answer:

```text
can environment A promote toward environment B?
```

What task 066 must still own entirely: whether it *may*, when, on what
evidence, and what happens as a result.

## Why

Three concrete gaps, each visible in code today.

**A typo is currently an environment.** `platform.EnvironmentRef` is
validated for shape and nothing else, so `POST /v1/evaluation-runs` with
`"environment": "stagin"` creates a perfectly valid run against an
environment that does not exist. Every later comparison bounded by that
reference then silently describes a population of one. Nothing in the system
is positioned to notice, because nothing knows what environments a project
has.

**Promotion has nothing to order.** Task 066 moves a candidate between
environments on evidence. `sandbox → staging → production` is a sequence
somebody has to have written down; deriving it from names would make
`prod-eu` and `production` two unrelated strings and `dev2` a mystery. Task
066 must not invent that model, or every consumer after it inherits a second
one.

**The roadmap already assigns environments to projects.** `Project` is
documented as "a workspace holding agents, policies, and environments", and
the project owns exactly one of those three today. An environment that is
global platform state would make `staging` mean one thing across every
project on a shared PostgreSQL backend — the multi-tenancy this milestone
explicitly does not have, arriving by accident.

## Scope

1. An `Environment` domain value: project-owned, keyed by the existing
   `EnvironmentRef`, with a human name, an optional promotion rank, and an
   active/archived status.
2. A single ordering primitive — is B forward of A within one project — with
   ties, gaps, unranked environments and archived environments all defined.
3. Persistence on **both** backends at one logical `SchemaVersion`, including
   a migration that backfills the environments existing runs already
   reference.
4. Control-plane operations: create, read, list within a project, configure,
   archive, activate — with compare-and-swap semantics on every mutation.
5. One cross-entity rule: a new `EvaluationRun` must name an environment that
   exists, is active, and belongs to the run's own project.
6. The `/v1` surface for the above, including the first collection route in
   the platform API — bounded by construction rather than paginated.
7. A `trustvian env` CLI family over that surface.

## Non-Goals

No promotion of any kind — no `Promotion` entity, no promotion request,
decision, record, approval, rollback, history, route, or CLI verb. Task 066
owns every one of them, and this task is finished when 066 can be written
without inventing an environment model.

**No deployment.** No deployment target, endpoint URL, cluster reference,
namespace, region, provider, credential, secret, token, kubeconfig, CI
trigger, webhook, or remote command. Trustvian is not a deployment
orchestrator, and this task records no address it could connect to — see
[Security and Resource Bounds](#security-and-resource-bounds).

No policy attached to an environment. The roadmap's "workspace holding
agents, policies, and environments" names three things a project owns; this
task delivers one of them. Environment-scoped policy has no consumer yet —
`policy.Policy` is compiled by the engine's caller, not by the control plane
— and inventing the association here would freeze it before anything reads
it.

No behavioral-profile lifecycle. Which profile a run uses, whether two runs
share one, and when one retires remain open platform questions
([ROADMAP § Behavioral profile](../../ROADMAP.md)). An environment may later
*influence* that choice; it is not that choice, and the two identities stay
separate.

No core change. `Fingerprint`, `StableFeatures`, `baseline.Key`,
`Engine.Analyze` and `Engine.Observe` are untouched, and the engine learns
nothing about a platform Environment — see
[Two environments, one string](#two-environments-one-string).

No list route for projects, agents, candidates or runs. This task introduces
exactly one collection, for one entity, because that entity has a
demonstrated need for it — see [Listing](#listing-exactly-one-collection).

No authentication, authorization or RBAC. Who may configure an environment is
[task 070](README.md)'s question, unchanged by this one.

No WebUI environment management — see
[CLI, TUI and WebUI](#cli-tui-and-webui).

## Domain Model

### Environment

```go
type Environment struct {
    ref       EnvironmentRef
    projectID ProjectID
    name      string
    rank      uint16
    ranked    bool
    status    EnvironmentStatus
    revision  uint64
}

func NewEnvironment(
    ref EnvironmentRef, projectID ProjectID, name string,
) (Environment, error)

func (e Environment) Ref() EnvironmentRef        { … }
func (e Environment) ProjectID() ProjectID       { … }
func (e Environment) Name() string               { … }
func (e Environment) Rank() (uint16, bool)       { … }
func (e Environment) Status() EnvironmentStatus  { … }
func (e Environment) Revision() uint64           { … }

func (e Environment) Rename(name string) (Environment, error)
func (e Environment) WithRank(rank uint16) (Environment, error)
func (e Environment) WithoutRank() Environment
func (e Environment) Archive() (Environment, error)
func (e Environment) Activate() (Environment, error)
```

Unexported fields with accessors, and transitions returning a new value
rather than mutating the receiver — the shape every entity in this package
already has, and for the same reason
([ADR 0025](../../adr/0025-platform-domain-values-with-caller-owned-identity.md)):
with exported fields every invariant the constructor establishes could be
undone by one assignment from outside the package.

`NewEnvironment` creates an **unranked, active** environment. Rank is applied
deliberately, by a separate call, because an ordering nobody chose is worse
than no ordering — see [Ordering](#ordering-and-the-promotion-relationship).

`revision` starts at 1 and increments on every successful mutation. It exists
for one reason: a mutation has to be able to fail rather than silently
overwrite a concurrent one, and the transport needs something smaller than
the whole entity to express "the value I read". See
[Concurrency](#concurrency-and-failure-semantics).

### EnvironmentStatus

```go
type EnvironmentStatus string

const (
    EnvironmentActive   EnvironmentStatus = "active"
    EnvironmentArchived EnvironmentStatus = "archived"
)
```

Two states, no state machine. `Archive` and `Activate` are total and mutually
inverse; there is no terminal state and no transition table, because there is
no transition an invariant forbids. `RunStatus` has one because a completed
run is historical evidence that must never move again; an environment is
configuration, and archiving one destroys nothing.

### What is deliberately not on it

| Field | Why not |
|---|---|
| `Description` | No consumer. A name is what a human reads in a list of at most 64 entries. |
| `URL` / `Endpoint` / `DeploymentTarget` | Nothing in Trustvian would connect to it, so it would be an unvalidated address stored for a system that does not exist. See [Security](#security-and-resource-bounds). |
| `Credentials` / `Secret` / `TokenRef` | Same, with a much worse failure mode. Secret handling arrives with the thing that needs a secret, never before. |
| `Labels` / `Annotations` / `Metadata map[string]any` | `CandidateMetadata`'s precedent is explicit: fixed fields bound the size by construction, keep the shape reviewable, and can be extended later. An open map cannot be taken back. |
| `PolicyRef` | See [Non-Goals](#non-goals). |
| `DefaultBehavioralProfile` | Collapses two identities that answer different questions. |
| `CreatedAt` / `UpdatedAt` | No consumer reads them, no ordering depends on them, and the list is ordered by rank. A timestamp nothing reads is a field every backend must still agree about. |

Each of these becomes reasonable the moment something reads it. None of them
is read by anything this task or task 066 specifies.

## Identity and Ownership

### `EnvironmentRef` stays the identifier

**Decision: no `EnvironmentID` is introduced. The `Environment` entity is
keyed by the `EnvironmentRef` that already exists**, and `EvaluationRun` is
not changed at all.

This is the smallest coherent change, and the alternative is worse than it
looks. Four places already treat the reference as the environment's identity:

1. `EvaluationRun.Environment()` persists it, and every run in every existing
   database carries one.
2. `EvaluationAggregate.AddRecord` compares `DecisionRecord.Environment` —
   the *core's* environment string — against `string(a.environment)` byte for
   byte, and refuses the record on mismatch (`ErrEnvironmentMismatch`). The
   same check exists for behavioral evidence
   (`ErrBehaviorEnvironmentMismatch`).
3. `BehaviorSnapshot`, `BehaviorDiff`, `EvaluationScorecard` and
   `EvaluationGateResult` each carry an `EnvironmentRef` as the bound of what
   they describe;
   [ADR 0027](../../adr/0027-behavioral-diff-compares-bounded-snapshots.md)
   makes matching refs a precondition of a comparison.
4. `/v1` publishes `environment` as a string field on run, progress,
   scorecard and realtime payloads, and
   [compatibility.md](../../compatibility.md) classifies `/v1` field names as
   STABLE.

A separate `EnvironmentID` would mean point 2 needs a lookup — per ingested
record, on the hot path — to translate an ID into the string an event
carries, or a second stored copy of the ref to compare against. Both add a
way for the two to disagree, for no gain: the ref is already unique where it
needs to be, already caller-owned
([ADR 0025](../../adr/0025-platform-domain-values-with-caller-owned-identity.md)),
and already the value every existing row holds. The `*ID` naming of other
entities is not a reason; it is a naming convention, and this value has a
different job.

`EnvironmentRef` remains **not an enum**. `local`, `sandbox`, `staging` and
`production` stay plausible values rather than a closed set, exactly as task
052 specified. This task adds a registry, not a vocabulary.

### Uniqueness is per project, not global

`(ProjectID, EnvironmentRef)` is the primary key.

Global uniqueness would mean the first project to create `staging` takes the
name away from every other project on a shared backend — the multi-tenancy
failure mode this milestone does not otherwise have. Per-project uniqueness
is also what the hierarchy already implies: a run resolves to a project
through `Candidate → Agent → Project`, so a run's ref is never ambiguous in
context.

The consequence is a route shape: reading one environment needs the project,
so `GET /v1/projects/{project_id}/environments/{environment_ref}` carries
both. That is a deliberate departure from the flat `/v1/agents/{agent_id}`
shape, and it is a departure because agent IDs *are* globally unique and
environment refs are not.

### Ownership is recorded by the constructor, verified by the store

`NewEnvironment` records `projectID` and does not check that the project
exists — the same division `NewAgent` already documents:

> The owning project is recorded, not verified: this constructor cannot see
> other entities, and checking existence needs both loaded at once.

Existence is enforced where both values are loadable: a foreign key to
`platform_projects` plus `requireExists` on SQLite, a foreign key plus
`sqlStateForeignKeyViolation → ErrStoreNotFound` on PostgreSQL. A create
naming a project that does not exist is `ErrStoreNotFound`, which is what
`store.go` already documents for a missing parent.

### The one cross-entity rule this task adds

**`ControlPlane.CreateEvaluationRun` must verify that the run's environment
exists, is active, and belongs to the run's own project.**

Authoritative location: `ControlPlane`, not the domain constructor and not
the store.

- Not `NewEvaluationRun`: it would need to load three entities, and a pure
  domain constructor that performs I/O is the boundary violation
  [ADR 0031](../../adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
  exists to prevent.
- Not a database constraint: a run stores `candidate_id` and `environment`,
  not `project_id`, so the composite key cannot be expressed as a foreign key
  without denormalizing the hierarchy onto runs — two sources of truth for
  which project a run belongs to, free to disagree.
- The control plane already performs exactly this class of check:
  `IngestDecisionRecord` verifies the run's status and that the request's
  behavioral profile matches the run's, before touching the store.

The resolution path is the one `resolveScope` already walks:

```text
run.CandidateID → Candidate.AgentID → Agent.ProjectID → Environment(project, ref)
```

Failure modes, all before anything is written:

| Condition | Error |
|---|---|
| environment not found in that project | `ErrStoreNotFound` |
| environment found but archived | `ErrEnvironmentUnavailable` (new sentinel) |
| candidate or agent missing | `ErrStoreNotFound`, as today |

`ErrEnvironmentUnavailable` is a new exported sentinel because a client
should be able to tell "no such environment" from "that environment is
closed"; both map to a 4xx, and the codes differ.

This is a **create-time** check. Nothing revalidates a run afterwards: a run
that was created against an environment which is later archived keeps
running, keeps ingesting, and completes normally. Environment configuration
governs what may start, never what is already underway.

### Existing databases keep working

Making the check strict without a migration would break every database that
already has runs: the environments they reference would not exist. So the
v2 → v3 migration **backfills** them — one row per distinct
`(project, environment)` pair reachable from existing runs, with
`name = ref`, `status = active`, and **no rank**.

Backfilled environments are deliberately unranked. Assigning ranks would
invent a promotion order no operator chose, and an invented order is exactly
what task 066 would then act on. An unranked environment hosts runs normally
and participates in no promotion until somebody ranks it.

## Two environments, one string

The core has an environment too: `baseline.Key.Environment`, a behavioral
dimension, set from `event.Context.Environment`. It is not this entity, and
this task must not make the engine aware of one.

The relationship is a **string equality that already exists**, entirely on
the platform side of the boundary — in the two places evidence is folded into
a run, and nowhere else:

```text
core     DecisionRecord.Environment   (string, from baseline.Key.Environment)
                    │
                    │  compared, never translated
                    ▼
platform EvaluationRun.Environment()  (EnvironmentRef)
                    │
                    │  resolved, only at run creation
                    ▼
platform Environment{project, ref}
```

Three properties this task preserves:

- **The comparison stays a comparison.** `EvaluationAggregate.AddRecord` and
  `BehaviorCollector.Observe` keep comparing strings. Neither gains a store,
  a lookup, or an `Environment` value; both remain pure functions over
  evidence, which is what lets them run per record.
- **Resolution happens once, at the edge.** `CreateEvaluationRun` is the only
  place a ref becomes an entity. Ingest never resolves.
- **The engine never sees any of it.** No core package imports the platform;
  no platform type is passed to `Engine.Analyze` or `Engine.Observe`; the
  engine continues to receive an opaque string chosen by its caller.

A consequence worth stating: an environment's ref must equal the environment
string the workload actually emits, or its records will be refused by the
existing mismatch check. That coupling is not new — it is what
`ErrEnvironmentMismatch` has always meant — and this task makes it
diagnosable for the first time, because the registry now says which values a
project expects.

## Ordering and the Promotion Relationship

### The model: an optional integer rank, per project

```go
func (e Environment) Rank() (uint16, bool)
```

Ordering is a **rank comparison within one project**. Nothing else.

Alternatives considered, and why not:

**A directed graph of allowed transitions.** This is a workflow DAG. It needs
edge creation and deletion routes, cycle detection, reachability queries, and
a decision about whether a path of length two is a promotion or two
promotions. Every one of those is a question task 066 would have to answer
anyway, and none of them is answerable now. The requirement is "is B forward
of A", not "what paths exist".

**Predecessor/successor pointers.** A linked list gives immediate-successor
semantics cheaply, and then breaks on the three cases that actually occur:
two environments at the same stage (needs multiple successors, which is a
graph), archiving one in the middle (the list must be spliced, mutating two
unrelated rows), and inserting one (same). A rank needs no neighbour to know
its own position.

**An ordered enum.** Closed set; already rejected by task 052 and by the
roadmap.

**Ordering derived from names.** `prod` and `production` would be unrelated;
`dev2` unorderable. Not considered further.

A rank is a small integer an operator chooses, stored on the row that owns
it, mutable without touching any other row, and totally ordered by
construction. Operators are advised — not required — to leave gaps (10, 20,
30) so a stage can be inserted later without renumbering.

### The primitive

```go
// CanPromote reports whether a candidate evaluated in from may be considered
// for promotion toward to. It is an ordering question and nothing else: a
// true answer is a precondition of promotion, never an authorization of one.
func CanPromote(from, to Environment) bool
```

True iff **all** of:

1. `from.ProjectID() == to.ProjectID()`
2. both are `EnvironmentActive`
3. both are ranked
4. `to.Rank() > from.Rank()` — strictly

A free function over two values rather than a method on a service: it reads
nothing, needs no store, and is deterministic and testable in isolation. The
control plane exposes a loading wrapper for transports:

```go
func (c *ControlPlane) PromotionOrder(
    ctx context.Context, projectID ProjectID, from, to EnvironmentRef,
) (bool, error)
```

which loads both and applies `CanPromote`, returning `ErrStoreNotFound` if
either is absent. Task 066 may use either; both give the same answer.

### Every case the relation has to define

| Case | Result | Why |
|---|---|---|
| Same environment (`A → A`) | false | Equal ranks are not strictly greater. Promoting into the environment you are already in is not a move. |
| Two environments at the same rank | false | Deliberate. Same rank means *peers* — `staging-eu` and `staging-us` — and a peer is not a next stage. If one should follow the other, they are not peers and their ranks should differ. |
| Backward (`rank 30 → rank 10`) | false | Rollback is a task 066 concept with its own evidence rules, not a promotion run backwards. |
| Skipping a stage (`rank 10 → rank 30`) | **true** | Forward is forward. Whether a *jump* is permitted is a policy about promotion, not a fact about ordering — see below. |
| Either environment unranked | false | An environment with no rank has no position, so no direction exists. Not an error: hosting runs and participating in promotion are separate capabilities. |
| Either environment archived | false | Archived means closed to new work. |
| Different projects | false | The ordering is per project, and a cross-project promotion is not a thing this platform has. |

### Ordering is not authorization

This distinction is the reason the table above allows a skip.

```text
065  CanPromote(staging, production) == true
        ↑ the environments are ordered that way

066  may this candidate, with this evidence, promote there, now?
        ↑ gate results, required evidence, approval, who asked
```

A gate PASS is not a promotion, and an ordering relation is not permission.
Task 056 already made the first half of that point explicitly; this is the
second half. If task 066 decides that promotions may not skip a stage, it
implements that rule with the ranks this task provides — the next rank above
`from` within the project is a query over an already-bounded list — and it
does so as *its* policy, without a second ordering model.

## Lifecycle and Historical Semantics

### What may change, and what may not

| Field | Mutable | Consequence of a change |
|---|---|---|
| `ref` | **No** | It is the identity historical runs hold. Renaming it would silently re-point evidence. Changing an environment's identity means creating another one. |
| `projectID` | **No** | Ownership is not transferable; a moved environment would orphan the runs that reference it in the old project. |
| `name` | Yes | Display only. Nothing reads it to decide anything. |
| `rank` | Yes | Changes future promotion eligibility. Changes no past evidence — see below. |
| `status` | Yes | Governs what may start, never what is already underway. |

### Delete: there is no delete

No `DeleteEnvironment` at any layer — not in the store, not in the control
plane, not on `/v1`, not in the CLI. `ControlStore` already states the
principle ("No delete method either: nothing in this milestone deletes, and a
delete API implies a retention model that does not exist"), and environments
make the consequence concrete: a completed run's `EnvironmentRef` would
resolve to nothing, and every scorecard and diff bounded by that ref would
describe an environment the platform could no longer name.

Archive is the model. An archived environment:

- accepts **no new runs** (`CreateEvaluationRun` refuses with
  `ErrEnvironmentUnavailable`),
- is **not** a promotion source or target (`CanPromote` is false either way),
- remains readable by ref, and appears in the project's list marked archived,
- keeps every historical reference resolvable, forever.

`Activate` reverses it. Archiving destroys nothing, so un-archiving restores
nothing that was lost — it is a configuration change in both directions, not
a recovery.

Retention — whether a platform may ever forget an environment, and what
happens to the evidence that names it — is a genuine question with no current
consumer, and it belongs with whatever first needs to delete anything at all.

### Historical runs: identity only, no snapshot

**Decision: `EvaluationRun` continues to store only an `EnvironmentRef`. No
environment snapshot is added, to runs or to evidence.**

The test is whether a completed run can change meaning when an environment is
edited. Field by field:

- `ref` is immutable, so the value the run holds always denotes the same
  environment.
- `name` is read by humans, by nothing else. A rename changes what a list
  displays and no evidence at all.
- `rank` is *not* historical evidence. It is the current ordering, and
  ordering is consulted at promotion time by the workflow that promotes. A
  run does not become a different run because production moved from rank 30
  to rank 40.
- `status` is a statement about the present ("may work start here"), not
  about the past.

So a snapshot would copy fields nothing reads and freeze one field that must
be read live. Adding it would also create the first place in the platform
where two copies of the same configuration exist, with no mechanism to notice
when they disagree.

The case that *does* need historical fidelity is a promotion: "this candidate
moved from staging to production on this evidence, when the order was this".
That is a record of an event, and it belongs on the promotion record task 066
owns — alongside the gate result and the evidence it acted on — not on the
environment and not on the run. This task deliberately leaves that space
empty rather than filling it with a snapshot that would be the wrong shape.

## Persistence

### Capability placement: extend `ControlStore`

Environment is the fourth project-owned control-plane entity, with the same
lifetime and access pattern as the other three: created once, read often,
never bulk-written, never rewritten by an evaluation. Splitting it into an
`EnvironmentStore` would create an interface with one implementation pair and
no second consumer — the speculative abstraction
[ADR 0023](../../adr/0023-interfaces-are-adapters.md) rules out.

`ControlStore` gains four methods:

```go
CreateEnvironment(ctx context.Context, env Environment) error
Environment(ctx context.Context, projectID ProjectID, ref EnvironmentRef) (Environment, error)
UpdateEnvironment(ctx context.Context, previous, next Environment) error
ProjectEnvironments(ctx context.Context, projectID ProjectID) ([]Environment, error)
```

This changes the interface's existing character in two documented ways, both
deliberate:

- **An update method.** `ControlStore`'s create-only rule exists so that a
  re-sent `CandidateID` cannot rewrite what a finished run was evaluated
  against. An environment is configuration, not evidence: renaming and
  re-ranking are the operations the entity exists for. The evidence-safety
  rule is preserved by making `ref` immutable rather than by refusing all
  updates.
- **A list method.** See [Listing](#listing-exactly-one-collection).

`Store`'s composite is unchanged in shape; both backends implement the four
new methods, and the existing compile-time assertions — `var _ Store =
(*SQLiteStore)(nil)` in `store.go` and its counterpart in `postgres.go` —
fail the build if either does not.

### Semantics, identical on both backends

| Operation | Semantics |
|---|---|
| `CreateEnvironment` | Create means create. An existing `(project, ref)` is `ErrStoreAlreadyExists` and nothing is overwritten. A missing project is `ErrStoreNotFound`. Exceeding the per-project cap is `ErrEnvironmentLimit`. |
| `Environment` | By `(project, ref)`. Missing is `ErrStoreNotFound`. A stored row that cannot reconstruct a valid domain value is `ErrStoreCorrupt` — nothing is clamped or repaired on the way out. |
| `UpdateEnvironment` | Compare-and-swap on `(project, ref, revision)`: the stored revision must equal `previous.Revision()`, and `next.Revision()` must be `previous.Revision() + 1`. A stale revision is `ErrStoreConflict`. `ref` and `project_id` differing between `previous` and `next` is `ErrInvalidID` — the store refuses to move an identity. |
| `ProjectEnvironments` | Every environment of one project, active and archived, ordered by rank ascending with unranked last, then by `ref` ascending in byte order. A project with none returns an empty slice and no error. A project that does not exist returns `ErrStoreNotFound`. |

### Schema: version 3, both dialects

One logical `SchemaVersion` governs both physical schemas — task 064's rule,
unchanged. `SchemaVersion` moves from 2 to 3.

SQLite:

```sql
CREATE TABLE platform_environments (
    project_id TEXT    NOT NULL REFERENCES platform_projects(id),
    ref        TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    rank       INTEGER,            -- NULL means unranked
    status     TEXT    NOT NULL,
    revision   INTEGER NOT NULL,
    PRIMARY KEY (project_id, ref)
);
```

PostgreSQL: the same, with `TEXT COLLATE "C"` on `project_id` and `ref`.

Three representation notes, in the style task 064 established:

- **`rank` is `INTEGER`, not `TEXT`.** The counters in this schema are text
  because they can exceed `MaxInt64` and must round-trip exactly. A rank is a
  `uint16` chosen by a human; it fits every integer type both engines have,
  and it is the one column in this schema that SQL actually orders.
- **`COLLATE "C"` on the key columns**, for the same reason
  `platform_behavior_entries.fingerprint_id` carries it: `ORDER BY … ref` is part of
  the list's contract, and two backends ordering refs differently is exactly
  the drift the differential suite exists to catch.
- **`revision` is `INTEGER`** and is bounded by the number of times a human
  edits one environment. Overflow is not a realistic condition, and the CAS
  compares rather than sums.

Index: the primary key covers lookup by `(project_id, ref)` and the list's
`WHERE project_id = ?`. No second index — the rows per project are capped at
64, so the sort is over a bounded set and a rank index would earn nothing.

**`runs.environment` gains no foreign key.** It cannot express the composite
without denormalizing `project_id` onto runs, and a foreign key would make a
historical run unloadable if its environment ever became unreachable. The
service-layer check at creation is the enforcement point, by design.

### Migration v2 → v3, on both backends

One transaction, matching `migrateV1ToV2`'s shape and its failure discipline:

1. Create `platform_environments`.
2. Backfill: for every distinct `(project_id, environment)` reachable by
   joining `runs → candidates → agents`, insert
   `(project_id, environment, name = environment, rank = NULL,
   status = 'active', revision = 1)`.
3. Stamp version 3.

Properties this must have, and that tests must prove:

- Deterministic and idempotent in effect: re-running against a v3 schema is
  refused by the version check, and a racing opener that already completed it
  is recognized by the existing "verify the durable schema" recovery rather
  than by parsing a driver's error text.
- Existing rows are otherwise untouched: every project, agent, candidate,
  run, aggregate, snapshot, entry and ingest cursor survives byte-identical.
- A run whose environment string is not a valid `EnvironmentRef` under
  current validation cannot exist (it was validated on the way in), so the
  backfill never has to decide what to do with an unusable value. A row that
  nonetheless fails to reconstruct is `ErrStoreCorrupt` at read time, as
  today.
- Downgrade is refused, as it already is: a v3 database opened by a v2 binary
  reports `ErrStoreSchemaVersion`.

## Control-Plane Service

```go
func (c *ControlPlane) CreateEnvironment(ctx, env Environment) error
func (c *ControlPlane) Environment(ctx, projectID, ref) (Environment, error)
func (c *ControlPlane) ProjectEnvironments(ctx, projectID) ([]Environment, error)
func (c *ControlPlane) ConfigureEnvironment(ctx, projectID, ref, ConfigureEnvironmentRequest) (Environment, error)
func (c *ControlPlane) ArchiveEnvironment(ctx, projectID, ref, revision uint64) (Environment, error)
func (c *ControlPlane) ActivateEnvironment(ctx, projectID, ref, revision uint64) (Environment, error)
func (c *ControlPlane) PromotionOrder(ctx, projectID, from, to EnvironmentRef) (bool, error)
```

`ConfigureEnvironment` takes a small explicit request rather than a patch
map:

```go
// ConfigureEnvironmentRequest is one configuration change, named after
// IngestRequest's precedent: a service request is a value, not a bag of
// positional arguments.
type ConfigureEnvironmentRequest struct {
    Revision  uint64  // the revision the caller read
    Name      *string // nil leaves it unchanged
    Rank      *uint16 // nil leaves it unchanged
    ClearRank bool    // removes the rank; mutually exclusive with Rank
}
```

Pointers rather than a map, and an explicit `ClearRank` rather than a
sentinel value, because "unset" and "set to zero" are different intentions
and rank 0 is a legitimate rank.

Each mutation: load, apply the domain transition, `UpdateEnvironment` with
the compare-and-swap, return the new value. The load-apply-store sequence is
not a transaction — it does not need to be, because the CAS is the
serialization point, and a losing writer gets `ErrStoreConflict` rather than
a lost update.

`CreateEvaluationRun` gains the check described in
[The one cross-entity rule](#the-one-cross-entity-rule-this-task-adds),
performed before `CreateEvaluationRun` reaches the store, and therefore
before any realtime publication.

No realtime events are published for environment mutations. The realtime bus
is scoped to evaluation lifecycle and ingest
([ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md));
an environment rename is not something a live dashboard is watching, and
adding a new event kind for it would widen a bounded contract for no
subscriber.

## HTTP API

Five routes. Path, method and shape follow the existing conventions exactly:
DTOs own the JSON, domain values carry no tags, `POST` creates and acts,
identifiers are path parameters, and the response envelope carries
`"version": "1"`.

```text
POST   /v1/environments                                              201
GET    /v1/projects/{project_id}/environments                        200
GET    /v1/projects/{project_id}/environments/{environment_ref}      200
POST   /v1/projects/{project_id}/environments/{environment_ref}/configure  200
POST   /v1/projects/{project_id}/environments/{environment_ref}/archive    200
POST   /v1/projects/{project_id}/environments/{environment_ref}/activate   200
```

`POST /v1/environments` is flat with `project_id` in the body, matching
`POST /v1/agents`. The reads and mutations are project-scoped because the ref
alone is not unique.

### Request and response shapes

```jsonc
// POST /v1/environments
{ "project_id": "proj-1", "ref": "staging", "name": "Staging", "rank": 20 }
```

`rank` is optional; omitted means unranked. `null` is equivalent to omitted.

```jsonc
// environment response
{
  "version": "1",
  "project_id": "proj-1",
  "ref": "staging",
  "name": "Staging",
  "rank": 20,          // omitted entirely when unranked
  "status": "active",
  "revision": 3
}
```

```jsonc
// list response
{ "version": "1", "project_id": "proj-1", "environments": [ … ] }
```

An object rather than a bare array, so the envelope has somewhere to live and
a later additive field has somewhere to go — the same reason every other
response here is an object.

```jsonc
// configure
{ "revision": 3, "name": "Staging (EU)", "rank": 25 }
// or
{ "revision": 3, "clear_rank": true }

// archive / activate
{ "revision": 4 }
```

### Status and error mapping

| Condition | Status | Code |
|---|---|---|
| created | 201 | — |
| read, list, configure, archive, activate | 200 | — |
| malformed body, invalid id/name/rank, `rank` with `clear_rank` | 400 | `invalid_request` |
| project or environment not found | 404 | `not_found` |
| `(project, ref)` already exists | 409 | `already_exists` |
| stale `revision` | 409 | `conflict` |
| environment archived, on run creation | 409 | `conflict` |
| per-project cap reached | 409 | `conflict` |
| body over 256 KiB | 413 | `payload_too_large` |

`classify` gains `ErrEnvironmentUnavailable` and `ErrEnvironmentLimit`
alongside the existing sentinels. No new code string is introduced where an
existing one is accurate: the codes are contract, and a client that already
handles `conflict` should not need a release to handle a new spelling of it.

`revision` is **required** on every mutation. A request without it is a 400,
not an unconditional write. This is the lost-update answer: there is no
endpoint that says "make it so regardless of what it currently is", because
two operators reordering ranks in a browser tab each would otherwise silently
overwrite each other, and the loser would never know.

### Bounds

The existing 256 KiB `MaxBytesReader` and `Content-Type` checks apply
unchanged; no route here approaches them. `environment_ref` in a path is
validated with the same `validateID` rules as everywhere else, before any
store call, and is percent-decoded by `net/http`'s router.

## Listing: exactly one collection

Task 063 deferred collection semantics and named this milestone as a possible
place they would first be needed. They are.

**Concrete requirement:** an operator must be able to see which environments
a project has and in what order, because environments are now the thing a run
must name correctly, and because task 066 needs the ordered set to decide
what "next" means. Neither question is answerable by ID lookup: the caller
does not know the IDs — knowing them is the question.

The four things 063 said a list route would otherwise freeze by accident, all
decided here:

| Decision | Value | Why |
|---|---|---|
| Scope | exactly one project | The only scope that exists; refs are unique per project, so a global list would return ambiguous rows. |
| Sort order | rank ascending, unranked last, then `ref` byte-ascending | Rank is the ordering the entity exists for. `ref` breaks ties deterministically; `COLLATE "C"` makes both backends agree. |
| Pagination | **none** | See below. |
| Limit | 64 environments per project, enforced at create | See below. |

**The entity is bounded instead of the response.** A project with more than
64 environments is not a project this product serves; a promotion order with
more than 64 stages is not an order anyone reasons about. Capping the entity
at creation makes the list inherently bounded, which is stronger than a page
size: there is no cursor to design, no stable-ordering-under-mutation problem
to solve, no `next` token to keep compatible, and no way for the response to
grow. `CreateEnvironment` returns `ErrEnvironmentLimit` at the cap, and the
message says so.

A project with no environments returns `200` with an empty array — the
project exists and has nothing, which is not the same as a project that does
not exist (`404`).

This decision is explicitly **not** a general list capability. No list route
is added for projects, agents, candidates or runs; those still need the
scope, ordering and cursor design 063 deferred, and this task neither
performs nor pre-empts it. What it establishes is one precedent worth
reusing: prefer bounding the collection to paginating it, where the domain
allows.

## CLI, TUI and WebUI

**CLI: in scope.** Run creation now requires an environment to exist, so the
local flow needs a way to create one, and the CLI is where
[ADR 0033](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) puts that.
A new family, shaped like the existing ones:

```text
trustvian env create   --project-id <id> --ref <ref> --name <name> [--rank <n>]
trustvian env get      --project-id <id> --ref <ref>
trustvian env list     --project-id <id>
trustvian env set      --project-id <id> --ref <ref> --revision <n> [--name <name>] [--rank <n> | --clear-rank]
trustvian env archive  --project-id <id> --ref <ref> --revision <n>
trustvian env activate --project-id <id> --ref <ref> --revision <n>
```

Exit codes follow task 060's control-plane family — `0` success, `2` usage,
`3` operational — leaving `1` unclaimed, which is what keeps `eval compare`'s
gate-FAIL meaning scoped. `--json` prints the response unchanged. The CLI
imports no platform package, computes no ordering, and decides nothing; `env
list` renders what `/v1` returned, in the order `/v1` returned it.

**TUI: out of scope, deliberately.** Task 061 scoped the TUI to the live
development loop, and
[ADR 0034](../../adr/0034-tui-is-a-bounded-realtime-http-client.md) states
that it does not own administration. Nothing in this task changes that: an
environment is configured once and read often, which is the opposite of what
a live dashboard is for. The roadmap gives no reason to reverse the decision,
so this task does not.

**WebUI: out of scope for this milestone.** The vertical slice is complete
without it — domain → store → service → `/v1` → CLI is a working path from a
human to durable state, and the browser is a second adapter over the same
routes rather than a missing layer. Including it would double the slice for a
surface that is not on the critical path to task 066, and the list route
specified here is precisely what a later WebUI task needs in order to do it
properly. The roadmap's intent that the WebUI eventually owns environment
configuration is unchanged;
[ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md)'s
boundary — the page learns everything from `/v1` — is what makes deferring it
cheap.

No interface holds ordering or promotion logic. `CanPromote` lives in the
platform module, and every adapter that wants the answer asks for it.

## Security and Resource Bounds

Every caller-controlled field reuses the validation this package already
applies, unchanged: `validateID` for `ref` (non-empty, ≤ 256 bytes, valid
UTF-8, no C0/C1 control characters or DEL, no leading or trailing
whitespace), `validateName` for `name` (Unicode and spaces allowed,
whitespace-only rejected, same length and control-character rules).

| Bound | Value | Enforced |
|---|---|---|
| `ref`, `name` length | 256 bytes | domain constructor |
| `rank` | `uint16`, 0–9999 | domain constructor; values above 9999 rejected so the space stays human-sized and sortable |
| environments per project | 64 | store create |
| request body | 256 KiB | existing `MaxBytesReader` |
| list response | ≤ 64 entries by construction | the cap above |

**No address, no credential, no outbound request.** This task stores nothing
that could be dialled: no URL, host, endpoint, cluster, namespace, registry
or webhook. The platform makes **no outbound connection on behalf of an
environment**, in this task or as a consequence of it, so there is no SSRF
surface to reason about — not a mitigated one, an absent one. If a later task
genuinely needs an actionable target, it arrives with the consumer that acts
on it, and that task owns the scheme allow-list, the credential handling, the
redaction rule, and the question of whether the platform should be making
that request at all.

Nothing in an environment is a secret, so nothing here is excluded from logs.
`ref` and `name` are already bounded, control-character-free text, which is
what makes them safe to log in the first place — the same property
`validateText` exists to guarantee.

Authentication and authorization are unchanged and still absent:
[task 070](README.md) owns them. This task adds routes that mutate
configuration, which raises the value of that task without changing its
scope — worth stating plainly rather than leaving implied.

## Concurrency and Failure Semantics

| Situation | Behaviour |
|---|---|
| Two concurrent creates of the same `(project, ref)` | One wins; the other gets `ErrStoreAlreadyExists` from a primary-key violation. Nothing is overwritten. |
| Two concurrent configures | Both read revision *n*; one commits *n+1*, the other's CAS matches no row and returns `ErrStoreConflict` → `409`. The loser re-reads and retries, knowing what changed. |
| Configure racing an archive | Same mechanism; revision covers every mutation, not only same-field ones. |
| A run created while its environment is being archived | Either order is correct: the check reads the environment before the run is written, so the run is refused, or it is created against a then-active environment and keeps running. A run in flight is never invalidated by an archive. |
| Archive racing an ingest | Unaffected. Ingest never resolves an environment. |
| `ProjectEnvironments` during a mutation | Returns a consistent single-statement read. No cross-row invariant spans the list, so no transaction or locking is needed. |
| PostgreSQL serialization failure or deadlock | Mapped to `ErrStoreConflict` by the existing `mapPostgresError`; no new mapping. |
| Migration racing a second opener | The existing recovery applies: re-verify the durable schema, and treat a complete, valid v3 as somebody else's completed migration. |

No row locking is introduced. Task 064 added `SELECT … FOR UPDATE` where a
read-then-write had to be atomic across a connection pool; environment
mutations are a predicated `UPDATE` whose predicate *is* the revision, so the
database does the serialization with no lock held across a round trip.

## The Task 066 Boundary

Stated once, in one place, because it is the line this task exists to draw.

**065 establishes, and 066 may rely on without redesigning:**

| | |
|---|---|
| Identity | `Environment{project, ref}`, keyed by the reference runs already carry |
| Ownership | one project, verified where both values are loadable |
| Ordering | optional per-project rank, and `CanPromote(from, to)` as the whole relation |
| Availability | active/archived, with archived closed to new work and to promotion |
| Persistence | both backends at `SchemaVersion` 3, with the set of a project's environments readable in order |
| Configuration | name and rank, changed under compare-and-swap |

**065 must not establish, and 066 owns entirely:**

`Promotion` as an entity; a promotion identifier, status or lifecycle; a
promotion request, decision or record; the evidence a promotion requires and
how a gate result feeds it; approval, who may approve, and whether approval
is required; rollback; what "the candidate is now in production" means and
where that is recorded; whether a promotion may skip a ranked stage; any
automatic action following a PASS; and any route, CLI verb or UI for the
above.

Two of those deserve emphasis because they look like ordering and are not:

- **Skipping.** `CanPromote` says `rank 10 → rank 30` is forward. Whether the
  workflow *permits* the jump is a promotion policy, and 066 implements it
  from the ranks this task stores rather than from a second ordering model.
- **Candidate movement.** Nothing in this task records where a candidate
  currently "is". An environment does not hold candidates, and a run naming
  an environment is evidence about an execution, not a statement of residence.
  If 066 needs that concept it creates it, with its own history.

A test in this task asserts the absence half: the platform module exports no
`Promotion*` identifier, and no environment operation touches a candidate or
a run.

## Tests

### Domain

- `NewEnvironment` accepts a valid ref, project and name; rejects an empty,
  over-length, control-character-bearing, or whitespace-padded ref; rejects a
  whitespace-only name; rejects an empty project id.
- A new environment is active, unranked, and at revision 1.
- `WithRank` accepts 0 and 9999 and rejects 10000; `WithoutRank` clears;
  `Rank()` reports `(value, true)` and `(0, false)` correspondingly.
- Every transition returns a new value and leaves the receiver unchanged —
  the immutability proof `testing.md` requires for value types.
- Every transition increments the revision by exactly one; `WithoutRank` on
  an already-unranked environment still produces a distinct value (no silent
  no-op that would make a CAS succeed without a change).
- `Archive` then `Activate` returns to active with two revisions consumed.
- There is no exported way to change `ref` or `projectID` — asserted by the
  package's existing API-surface test style.

### Ordering

- `CanPromote` truth table: every row of
  [Every case the relation has to define](#every-case-the-relation-has-to-define),
  each as a named subtest.
- Ordering is antisymmetric and irreflexive: for all pairs, not both
  `CanPromote(a, b)` and `CanPromote(b, a)`; never `CanPromote(a, a)`.
- Ordering is transitive over ranks.
- Archiving either side flips a previously-true answer to false, and
  activating restores it.
- Rank ties are symmetric and both false.

### Ownership and cross-entity rules

- `CreateEvaluationRun` succeeds when the environment exists, is active, and
  belongs to the project reached through candidate → agent.
- It fails with `ErrStoreNotFound` when the ref exists **in another project**
  — the cross-project misuse case, with two projects each owning a `staging`.
- It fails with `ErrEnvironmentUnavailable` when the environment is archived,
  and the run is not written.
- It fails when the environment does not exist at all.
- The failure happens before any write and before any realtime publication:
  no run row, no event.
- A run created against an active environment keeps running, ingesting and
  completing after that environment is archived.
- `NewEvaluationRun` still performs no I/O: the domain constructor is
  unchanged, proven by constructing one with no store in scope.

### Persistence, per backend and shared

The conformance suite task 064 established runs these against **both**
backends; the differential suite compares resulting logical state.

- Create, read back, and compare field by field, including unranked.
- Duplicate `(project, ref)` is `ErrStoreAlreadyExists`, and the stored value
  is untouched.
- The same ref in two projects coexists and reads back independently.
- A create naming a missing project is `ErrStoreNotFound`.
- The 65th environment in a project is `ErrEnvironmentLimit`; the 64th
  succeeds.
- `UpdateEnvironment` with a stale revision is `ErrStoreConflict` and changes
  nothing.
- `UpdateEnvironment` whose `next` changes `ref` or `project_id` is refused.
- `ProjectEnvironments` ordering: ranked ascending, unranked last, `ref` byte
  order within a tie — with a fixture whose refs order differently under a
  locale-aware collation than under byte order, which is what proves
  `COLLATE "C"` is doing its job.
- `ProjectEnvironments` on an empty project returns an empty slice and no
  error; on a missing project returns `ErrStoreNotFound`.
- A corrupt row — unknown status, rank out of range — is `ErrStoreCorrupt`,
  not a clamped value.
- Concurrency: N goroutines configuring one environment produce exactly one
  winner per revision and no lost update, under `-race`.

### Migration

- A v2 database with runs across two projects migrates to v3 and backfills
  exactly the distinct `(project, environment)` pairs those runs reference,
  all unranked and active, with `name == ref`.
- Every pre-existing row survives the migration byte-identical.
- A v2 database with no runs migrates to an empty environments table.
- After migration, creating a run against a backfilled environment succeeds
  without any further operator action.
- A v3 database opened again is verified, not re-migrated.
- A v3 database opened by a binary supporting v2 is `ErrStoreSchemaVersion`.
- The v1 → v2 → v3 path works from a task 057 database.
- Both backends, same assertions, in the shared suite.

### HTTP

- Each route: success shape, status code, and envelope version.
- `rank` omitted in a response for an unranked environment; present and
  numeric otherwise.
- Validation failures map to `400` with `invalid_request`; not-found to
  `404`; duplicate to `409 already_exists`; stale revision to
  `409 conflict`; cap to `409 conflict`.
- A mutation without `revision` is `400`, not an unconditional write.
- `configure` with both `rank` and `clear_rank` is `400`.
- A `ref` path parameter with a control character or 300 bytes is rejected
  before any store call — provable with a control plane over a store that
  fails the test if it is called.
- Unknown fields in a request body are tolerated (the additive-compatibility
  rule), unknown routes still 404.
- A body over the limit is `413`.
- The list route returns `200` with an empty array for a project with no
  environments.

### CLI

- Each subcommand issues exactly one request to the expected path and method,
  against a stub server.
- Exit codes: `0` success, `2` usage (missing `--project-id`, missing
  `--revision`, `--rank` with `--clear-rank`), `3` operational (transport
  failure, `409`).
- `--json` emits the response body unchanged.
- `env list` renders in the order received and performs no client-side
  sorting — asserted with a server returning a deliberately unsorted-looking
  but contract-ordered list.

### Boundary and absence tests

These prove what the task must *not* do, and each fails loudly if a later
change crosses the line:

- **No core awareness.** The root module's dependency graph is unchanged; no
  core package imports the platform; `Engine.Analyze` and `Engine.Observe`
  signatures and behaviour are untouched; no `baseline.Key` change.
- **No promotion.** The platform module exports no `Promotion*` identifier,
  no promotion route exists, and no environment operation has a side effect
  on any candidate or run.
- **No outbound request.** No environment operation opens a network
  connection — asserted by a test whose control plane is wired with an HTTP
  transport that fails the test if dialled.
- **No secret or deployment execution.** The package declares no credential,
  token, URL or command field; `go vet`-adjacent grep-style assertions in the
  existing architecture-test style cover the identifier names.
- **No platform logic in adapters.** The CLI imports no platform package
  (existing architecture test, extended to the new files); `webui` still
  receives no control plane; neither computes ordering.
- **Ordering lives in one place.** `CanPromote` is the only implementation;
  no adapter re-derives it from ranks.

### Execution

Everything runs with `GOWORK=off` in the platform module, as CI does, and
with `-race` for the concurrency and migration-race tests. The PostgreSQL
tests stay gated on `TRUSTVIAN_TEST_POSTGRES_DSN` exactly as task 064 left
them, and the SQLite path is the one that runs on every push.

## Benchmarks

**None, deliberately** — following task 052's precedent.

Environment configuration is control-plane state on the human path: created
once per project per stage, read at run creation, edited rarely. It is not on
the event hot path, the ingest path, or any per-record path. A microbenchmark
here would measure a SQLite round trip and prove nothing about the product.

The one resource question that does matter — how large the list can get — is
answered by a **bound**, not a measurement: 64 rows per project, enforced at
creation and asserted by the cap test. A limit proven by a test is worth more
than a latency number that drifts with hardware.

If task 066 later puts ordering on a path that runs per evidence item rather
than per promotion, that task measures it. Nothing in this design suggests it
will: `CanPromote` is two comparisons over two loaded values.

## Documentation

Delivered by the **implementation** PR, not this one:

- `docs/DOMAIN.md` — the control-plane domain diagram gains `Environment`
  under `Project`, and the `EnvironmentRef` bullet changes from "the
  environment model itself is a later task" to what it actually is.
- `docs/ARCHITECTURE.md` — the platform section notes environments as
  project-owned configuration, and that the engine remains unaware.
- `docs/compatibility.md` — new `/v1` routes and field names under the
  existing STABLE rows; `platform.SchemaVersion` 3.
- `docs/SECURITY.md` — the create-time environment check as a fail-closed
  control, and the explicit statement that no environment target is dialled.
- `CHANGELOG.md` — one entry, in the implementation PR.
- `docs/ROADMAP.md` — 065 moves from planned to implemented.
- This file — status becomes "specified and implemented".

### ADR required by the implementation PR

**ADR 0039 — "Environments are project-owned ranked references"**, added by
the implementation PR and not by this specification. The rationale is written
here so it cannot be lost; the ADR records it as an accepted decision once
the code exists, which is the convention every ADR in this repository
follows.

It must record, with the alternatives each rejected:

1. **Identity** — the entity is keyed by the existing `EnvironmentRef`; no
   `EnvironmentID` is introduced. Rejected: a new identifier, which would put
   a translation on the per-record ingest comparison.
2. **Not an enum** — restating task 052's open set as a durable rule now that
   a registry exists that could tempt someone to close it.
3. **Ordering** — an optional per-project integer rank, with strict-greater
   as the whole relation. Rejected: a transition graph, predecessor pointers,
   and name-derived order.
4. **Ordering is not authorization** — and where the line sits against task
   066.
5. **Archive, never delete** — and why a hard delete of a referenced
   environment is the one operation that could orphan evidence.
6. **No snapshot on runs** — identity is immutable, so history is stable
   without copying configuration; promotion-time ordering belongs on the
   promotion record.
7. **Platform environment vs behavioral environment** — one string, compared
   on the platform side, never a type the engine sees.

## Acceptance Criteria

1. `platform.Environment` exists as a project-owned value keyed by
   `EnvironmentRef`; no `EnvironmentID` type is introduced; `EvaluationRun`
   is unchanged.
2. `EnvironmentRef` remains an open set: no enum, no closed constant list, no
   validation against a fixed vocabulary.
3. `(ProjectID, EnvironmentRef)` is unique; the same ref in two projects is
   two environments.
4. `NewEnvironment` performs no I/O and verifies no other entity;
   project existence is enforced by the store's foreign key.
5. `CreateEvaluationRun` refuses a run whose environment is missing, archived,
   or owned by another project, before writing anything, and that check lives
   in `ControlPlane`.
6. A run already created is unaffected by later archiving of its environment.
7. `CanPromote` implements exactly the truth table specified, is the only
   ordering implementation in the repository, and reads no store.
8. No promotion entity, route, record, or side effect exists.
9. Environments are archivable and reactivatable; no delete exists at any
   layer.
10. `ref` and `projectID` are immutable; `name`, `rank` and `status` are
    mutable through revision-checked operations.
11. Every mutation is compare-and-swap on `revision`; a stale revision is a
    `409` and changes nothing; a mutation without a revision is a `400`.
12. `ControlStore` gains exactly the four specified methods, implemented
    identically on SQLite and PostgreSQL, and proven by the shared
    conformance and differential suites.
13. `SchemaVersion` is 3 on both backends, with a migration that backfills
    the environments existing runs reference — unranked and active — and
    preserves every existing row.
14. The only collection route added is a project's environments, ordered as
    specified, bounded at 64 per project by creation, with no pagination and
    no cursor.
15. `trustvian env` exists with the specified subcommands and exit-code
    scheme; the CLI imports no platform package and computes no ordering.
16. No TUI or WebUI change.
17. No core change: no engine, `baseline.Key`, fingerprint or public API
    modification, and no core package that knows a platform Environment
    exists.
18. No URL, credential, secret, or deployment field is stored, and no
    outbound request is made on an environment's behalf.
19. `gofmt`, `go vet`, `go test ./...` and `go test -race ./...` pass in every
    module with `GOWORK=off`, and the platform-boundary and module scripts
    pass.
20. ADR 0039 is added by the implementation PR, recording the seven decisions
    listed above.
