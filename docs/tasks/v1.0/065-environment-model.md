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
5. Two cross-entity rules: a new `EvaluationRun` must name an environment
   that exists, is active, and belongs to the run's own project; and a
   comparison must be between two runs of the same project, now that a ref
   alone no longer says which environment it means.
6. The `/v1` surface for the above, including the first collection route in
   the platform API — bounded twice, once at creation and once per response.
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
demonstrated need for it — see [Listing](#listing-one-collection-two-bounds).

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
| `CreatedAt` / `UpdatedAt` | No consumer reads them, and no ordering depends on them — the list traverses by `ref`. A timestamp nothing reads is a field every backend must still agree about. |

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

### The cross-entity rules this task adds

**Rule 1. `ControlPlane.CreateEvaluationRun` must verify that the run's
environment exists, is active, and belongs to the run's own project.**

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

**Rule 2. `ControlPlane.CompareEvaluations` must verify that both runs belong
to the same project.**

`CompareBehaviorSnapshots` already refuses a comparison whose two
`EnvironmentRef` values differ — environment is a fingerprint dimension, so
every behavior would differ ([ADR 0027](../../adr/0027-behavioral-diff-compares-bounded-snapshots.md)).
That equality check has always been standing in for "these two runs describe
the same environment", and until now it was the closest thing the platform
had to a proof.

Making refs explicitly project-scoped turns that proxy into a hazard. Two
projects may each own a `staging`, and two runs — one from each — would pass
an equality check that was meant to establish they were comparable, producing
a scorecard and a gate result over two unrelated populations. Nobody
downstream could tell: the comparison, the diff and the gate all report the
environment as `staging`, and they would be right and misleading at once.

So the ownership check moves to the layer that can see the graph. Both runs
resolve `run → Candidate → Agent → Project`, and a mismatch is
`ErrComparisonScope` before any evidence is loaded — fail fast, and cheaper
than the four evidence reads it precedes. Together with the existing ref
equality, the precondition becomes what it always meant: *the same
`Environment` entity*, which is `(ProjectID, EnvironmentRef)`.

Two things this deliberately does not do:

- **It does not touch the pure functions.** `CompareBehaviorSnapshots` and
  `NewEvaluationScorecard` keep taking evidence values, keep their existing
  environment check, and gain no store, no project argument and no ownership
  concept. Cross-entity validation stays in `ControlPlane`, exactly where
  rule 1 puts it.
- **It does not require the same Agent.** Comparing two candidates of one
  agent is the intended shape, but comparing two agents inside one project is
  merely unusual rather than ambiguous — the runs are in the same project's
  same environment, and the scorecard means what it says. The ambiguity
  project-scoped refs create sits precisely at the project boundary, so that
  is precisely what this closes. Requiring the same agent would also change
  behaviour for any existing caller with no requirement behind it. If task
  066 needs agent identity — a promotion is about one candidate of one agent
  — it adds that narrower precondition alongside the workflow that needs it,
  and says why.

The cost is two reads per comparison, the same two `resolveScope` already
performs to publish a realtime event.

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
- **A list method.** See [Listing](#listing-one-collection-two-bounds).

`Store`'s composite is unchanged in shape; both backends implement the four
new methods, and the existing compile-time assertions — `var _ Store =
(*SQLiteStore)(nil)` in `store.go` and its counterpart in `postgres.go` —
fail the build if either does not.

### Semantics, identical on both backends

| Operation | Semantics |
|---|---|
| `CreateEnvironment` | Create means create, under the transaction described below. An existing `(project, ref)` is `ErrStoreAlreadyExists` and nothing is overwritten. A missing project is `ErrStoreNotFound`. A project already at the creation cap is `ErrEnvironmentLimit`. |
| `Environment` | By `(project, ref)`. Missing is `ErrStoreNotFound`. A stored row that cannot reconstruct a valid domain value is `ErrStoreCorrupt` — nothing is clamped or repaired on the way out. |
| `UpdateEnvironment` | Compare-and-swap on `(project, ref, revision)`: the stored revision must equal `previous.Revision()`, and `next.Revision()` must be `previous.Revision() + 1`. A stale revision is `ErrStoreConflict`. `ref` and `project_id` differing between `previous` and `next` is `ErrInvalidID` — the store refuses to move an identity. |
| `ProjectEnvironments` | A bounded page of one project's environments, active and archived, in `ref` byte order, starting after an optional `ref`. A project with none returns an empty slice and no error. A project that does not exist returns `ErrStoreNotFound`. See [Listing](#listing-one-collection-two-bounds). |

```go
// ProjectEnvironments returns at most limit environments whose ref sorts
// after `after`, in ref byte order. after == "" starts at the beginning.
ProjectEnvironments(
    ctx context.Context, projectID ProjectID, after EnvironmentRef, limit int,
) ([]Environment, error)
```

### Creating one is a cross-row invariant

The cap is not a property of the row being written, so the primary key cannot
enforce it. Two transactions can each count 63 environments for one project,
each insert a *different* ref, and each commit against an intact
`(project_id, ref)` primary key — leaving 65. SQLite's single connection
happens to serialize that today; PostgreSQL's pool does not, and a guarantee
that holds only because of a connection limit is not a guarantee.

So creation is transactional, and it serializes on the row that owns the
invariant:

```text
BEGIN
  lock the owning project row               -- SELECT … FOR UPDATE (PostgreSQL)
  project missing            → ErrStoreNotFound
  (project, ref) exists      → ErrStoreAlreadyExists
  count(project) >= cap      → ErrEnvironmentLimit
  INSERT
COMMIT
```

**The invariant, stated once:** for one `ProjectID`, every `CreateEnvironment`
serializes around that project's cap decision.

That is a statement about semantics, not about scheduling. On PostgreSQL the
lock is per project row, so creates in different projects proceed
concurrently; on SQLite a write transaction serializes the database, so they
queue. The difference is latency under load, never outcome — which is the
line task 064 already draws between logical equivalence and physical
mechanism.

Four decisions inside that sequence:

- **The project row is the serialization point.** It already exists, it is
  already the parent every environment references, and locking it makes the
  lock scope exactly the invariant's scope. A dedicated counter table would
  add a row that can disagree with the rows it counts, and a table-level lock
  would serialize unrelated projects.
- **The identity check precedes the cap check**, deliberately. A caller
  re-sending a ref that already exists is adding nothing, so the cap is
  irrelevant to it — and answering `ErrEnvironmentLimit` there would be both
  misleading and nondeterministic, since which of a same-ref pair arrived
  first would decide which error the loser saw. With this ordering, a
  duplicate is a duplicate whether the project is empty or over cap.
- **Reads take no lock.** `Environment` and `ProjectEnvironments` are single
  statements outside any transaction; the cap constrains writes, and a
  reader has nothing to serialize against.
- **SQLite takes a write transaction, not a coincidence.** `BEGIN IMMEDIATE`,
  so the write intent is acquired when the transaction starts rather than
  upgraded after the count has already been read. Relying on
  `SetMaxOpenConns(1)` instead would put the guarantee in a pool setting
  rather than in the code that depends on it, and a deferred transaction
  would read the count as a reader and only then discover it cannot write.
  The mechanisms differ — a write transaction on one connection, a row lock
  on a pooled backend — and the observable semantics are identical, which is
  what the shared conformance suite asserts and the PostgreSQL contention
  test proves separately.

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

Index: the primary key on `(project_id, ref)` is exactly the access path both
reads use — a point lookup, and the list's
`WHERE project_id = ? AND ref > ? ORDER BY ref LIMIT ?`, which is a range
scan along the key in its own order. No second index: nothing sorts by rank
in SQL, and a migrated project's extra rows are still traversed along this
one.

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
- The same ref used by two different projects backfills as two environments,
  because identity is `(ProjectID, EnvironmentRef)`.
- Nothing is dropped, merged, renamed, archived, ranked, or synthesized. A
  backfilled environment is exactly `name = ref`, `rank = NULL`,
  `status = active`, `revision = 1`.
- Downgrade is refused, as it already is: a v3 database opened by a v2 binary
  reports `ErrStoreSchemaVersion`.

### The cap does not apply to what migration finds

Schema 2 has no environment registry and no cap, so a valid v2 database may
hold a project whose runs reference 65 — or 500 — distinct environment
strings. The backfill preserves every one of them.

**The cap governs creation, not existence.** It is a bound on how a project
grows from here, not a claim about what a project already contains:

| Migrated count | New `CreateEnvironment` | Existing rows |
|---|---|---|
| < 64 | succeeds until the project reaches 64 | readable, configurable, archivable |
| ≥ 64 | `ErrEnvironmentLimit` | unchanged — readable, configurable, archivable |

Every historical `EnvironmentRef` stays resolvable, every historical run
stays loadable, and every environment above the cap remains a full
participant in everything except being joined by a new sibling. Nothing about
a completed run changes.

The alternatives were considered and rejected. Dropping refs past the 64th
would orphan the evidence that names them. Failing the migration closed would
brick a previously valid database with no remediation that does not require
deleting historical evidence — which this design has no operation for, on
purpose. Archiving the excess would silently rewrite what an operator
configured. Each of those trades a real, current guarantee for a cap whose
only job is to stop unbounded growth.

What this does cost is the claim that a project's environment list always
fits in one response. It does not, so the collection route is bounded
independently of the entity count — see
[Listing](#listing-one-collection-two-bounds).

## Control-Plane Service

```go
func (c *ControlPlane) CreateEnvironment(ctx, env Environment) error
func (c *ControlPlane) Environment(ctx, projectID, ref) (Environment, error)
func (c *ControlPlane) ProjectEnvironments(ctx, projectID, after EnvironmentRef, limit int) ([]Environment, error)
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
[the cross-entity rules](#the-cross-entity-rules-this-task-adds),
performed before `CreateEvaluationRun` reaches the store, and therefore
before any realtime publication.

No realtime events are published for environment mutations. The realtime bus
is scoped to evaluation lifecycle and ingest
([ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md));
an environment rename is not something a live dashboard is watching, and
adding a new event kind for it would widen a bounded contract for no
subscriber.

## HTTP API

Six routes. Path, method and shape follow the existing conventions exactly:
DTOs own the JSON, domain values carry no tags, `POST` creates and acts,
identifiers are path parameters, and the response envelope carries
`"version": "1"`.

```text
POST   /v1/environments                                              201
GET    /v1/projects/{project_id}/environments?limit=&after=          200
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
// GET /v1/projects/proj-1/environments?limit=64&after=production
{
  "version": "1",
  "project_id": "proj-1",
  "environments": [ … ],       // ref-ascending, at most `limit` entries
  "next_after": "staging"      // omitted on the last page
}
```

An object rather than a bare array, so the envelope has somewhere to live and
a later additive field has somewhere to go — the same reason every other
response here is an object, and what makes `next_after` an additive field
rather than a shape change.

`limit` is optional, defaults to 64, and is capped at 64; a larger value is a
`400` rather than a silent clamp, because a client that asked for 500 and
received 64 without being told would conclude it had seen everything.
`after` is a `ref` from a previous page's `next_after`, validated by the same
`validateID` rules as any other ref. It is scoped by the `project_id` already
in the path, so it never implies a ref is unique anywhere but inside one
project.

`next_after` is present exactly when the page filled its limit and another
row follows, and absent on the last page. A caller pages until it is absent.
Ordering is by `ref`, which is immutable, so a traversal returns every row
that exists throughout it exactly once — see
[Listing](#listing-one-collection-two-bounds) for why the traversal key is
not `rank`.

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
| `limit` above 64, non-numeric, or negative; malformed `after` | 400 | `invalid_request` |
| project or environment not found | 404 | `not_found` |
| comparison across two projects | 400 | `invalid_request` |
| `(project, ref)` already exists | 409 | `already_exists` |
| stale `revision` | 409 | `conflict` |
| environment archived, on run creation | 409 | `conflict` |
| creation cap reached for that project | 409 | `conflict` |
| body over 256 KiB | 413 | `payload_too_large` |

`classify` gains `ErrEnvironmentUnavailable`, `ErrEnvironmentLimit` and
`ErrComparisonScope` alongside the existing sentinels — the first two as
`409 conflict`, the third as `400 invalid_request`, because a cross-project
comparison is a request that can never succeed rather than a state that
might change. No new code string is introduced where an
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
store call, and is percent-decoded by `net/http`'s router. The list response
is bounded by the page limit rather than by how many environments a project
happens to hold, which is what keeps a migrated project with hundreds of
historical refs from producing one enormous body.

## Listing: one collection, two bounds

Task 063 deferred collection semantics and named this milestone as a possible
place they would first be needed. They are.

**Concrete requirement:** an operator must be able to see which environments
a project has, because an environment is now the thing a run must name
correctly, and task 066 needs the same set — each row carrying its rank — to
reason about what "next" means. Neither is answerable by ID lookup: the
caller does not know the refs, and knowing them is the question.

### Two bounds, for two different risks

| Bound | Value | Protects against |
|---|---|---|
| State growth | 64 environments per project, at creation | A project accumulating environments without limit from here on |
| Response size | at most 64 rows per page | A single response that is unbounded *today*, because migration preserves whatever a v2 database already had |

The first version of this specification had only the state bound and claimed
the response was "bounded by construction". That was true of anything created
under v3 and false of anything migrated into it: schema 2 had no registry and
no cap, so a migrated project may legitimately hold more environments than
the cap allows anyone to create. Relying on the entity bound alone would have
left exactly one path — the one that matters, an upgraded database — able to
produce an unbounded response.

Both bounds now exist, and neither depends on the other being correct.

### The four decisions 063 said would otherwise be frozen by accident

| Decision | Value |
|---|---|
| Scope | exactly one project, named in the path |
| Sort order | `ref` ascending, byte order (`COLLATE "C"` on both backends) |
| Pagination | keyset, `after=<ref>` |
| Limit | `limit` ≤ 64, default 64 |

### Why the traversal order is `ref` and not rank

This is the part worth reviewing, because the obvious answer is wrong.

Ordering the *route* by rank and paginating on it would need a cursor
encoding the whole sort tuple — and rank is **mutable**. Re-ranking is one of
exactly two operations this entity exists for, so a cursor over rank would
let a row move from a page the caller has not read into one it already read,
or the reverse: a row silently skipped, or returned twice, in the middle of a
traversal whose whole job is to enumerate a set completely.

`ref` has the property a cursor needs and rank does not: it is **immutable**
(see [What may change](#what-may-change-and-what-may-not)) and unique within
the project the path already names. So traversal is exact — every row present
for the whole traversal is returned exactly once, whatever anyone is renaming,
re-ranking or archiving at the time — and the cursor never assumes a ref is
globally unique, because it is scoped by the project in the path.

A row created during a traversal appears if and only if its ref sorts after
the caller's current position, which is the ordinary and honest property of
keyset pagination. There is no deletion, so nothing can vanish mid-traversal.

**Promotion order is not traversal order.** Every row carries its `rank`, and
`CanPromote` — the question that actually has a semantic answer — stays in
the platform where no adapter can re-derive it. Rendering a list in rank
order is presentation over data the server supplied; deciding whether one
environment precedes another for promotion is not, and no interface does it.

### Response shape

A project with no environments returns `200` with an empty array — the
project exists and has nothing, which is not the same as a project that does
not exist (`404`). A page that filled its limit carries the cursor to
continue from; the last page does not, which is how a caller knows it is
done.

### Still not a general list capability

No list route is added for projects, agents, candidates or runs. Those still
need the scope, ordering and cursor design 063 deferred, and this task
neither performs nor pre-empts it. What it establishes is a pattern worth
reusing: bound the state where the domain allows it, bound the response
anyway, and paginate on a key that cannot move.

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
gate-FAIL meaning scoped. `--json` prints the response unchanged.

`env list` follows the route's pages until there is no continuation, so a
migrated project holding more environments than one page still lists
completely, and it never asks for a larger page than the contract allows. It
may present rows in rank order for a human to read, using the rank each row
carries; what it must not do is answer whether one environment precedes
another for promotion. That question has one implementation, `CanPromote`,
and it lives in the platform. The CLI imports no platform package and decides
nothing.

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
| environments created per project | 64 | store create, inside the project-locked transaction |
| request body | 256 KiB | existing `MaxBytesReader` |
| list response | ≤ 64 entries per page | the route's `limit`, independently of how many rows exist |
| list `limit` parameter | 1–64, default 64 | rejected above 64 rather than clamped |

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
| Two concurrent creates of the same `(project, ref)` | They serialize on the project row. One wins; the other finds the row already there and gets `ErrStoreAlreadyExists`. Deterministic whatever the project's count is, because the identity check precedes the cap check — a duplicate is never reported as a limit. |
| Two concurrent creates of **different** refs into a project at 63 | They serialize on the project row. The first counts 63, inserts, and commits at 64; the second counts 64 and returns `ErrEnvironmentLimit`. Exactly one succeeds, and the project ends at 64 — never 65. |
| Concurrent creates into **different** projects | No contention: each locks its own project row. |
| Two concurrent configures | Both read revision *n*; one commits *n+1*, the other's CAS matches no row and returns `ErrStoreConflict` → `409`. The loser re-reads and retries, knowing what changed. |
| Configure racing an archive | Same mechanism; revision covers every mutation, not only same-field ones. |
| A run created while its environment is being archived | Either order is correct: the check reads the environment before the run is written, so the run is refused, or it is created against a then-active environment and keeps running. A run in flight is never invalidated by an archive. |
| Archive racing an ingest | Unaffected. Ingest never resolves an environment. |
| `ProjectEnvironments` during a mutation | A single-statement read, taking no lock. Rows may be renamed, re-ranked or archived between pages; none of that moves a row, because the traversal key is the immutable `ref`. A create during a traversal appears if and only if its ref sorts after the caller's position. |
| PostgreSQL serialization failure or deadlock | Mapped to `ErrStoreConflict` by the existing `mapPostgresError`; no new mapping. |
| Migration racing a second opener | The existing recovery applies: re-verify the durable schema, and treat a complete, valid v3 as somebody else's completed migration. |

**No row locking is required for revision-checked updates.** Configure,
archive and activate are a predicated `UPDATE` whose predicate *is* the
revision, so the database serializes them with no lock held across a round
trip — the same reasoning task 064 applied where it did not need
`SELECT … FOR UPDATE`.

**Creation is the exception, and deliberately so.** The per-project cap is a
cross-row invariant that no predicate on the inserted row can express, so
`CreateEnvironment` locks the owning project row for the duration of its
transaction — see
[Creating one is a cross-row invariant](#creating-one-is-a-cross-row-invariant).
That is one lock, scoped to one project, held across a count and an insert,
taken only on creation. Reads never take it, updates never take it, and
creates in other projects never wait on it.

## The Task 066 Boundary

Stated once, in one place, because it is the line this task exists to draw.

**065 establishes, and 066 may rely on without redesigning:**

| | |
|---|---|
| Identity | `Environment{project, ref}`, keyed by the reference runs already carry |
| Ownership | one project, verified where both values are loadable |
| Ordering | optional per-project rank, and `CanPromote(from, to)` as the whole relation |
| Availability | active/archived, with archived closed to new work and to promotion |
| Persistence | both backends at `SchemaVersion` 3, with a project's whole environment set enumerable through a bounded, stable traversal |
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
- `CompareEvaluations` refuses two completed runs whose projects differ with
  `ErrComparisonScope`, **before** loading any evidence — proven with a store
  that fails the test if evidence is read.
- The case that motivates it: two projects each own a `staging`, each has a
  completed run there, and a comparison of the two is refused rather than
  producing a scorecard over unrelated populations.
- `CompareEvaluations` still succeeds for two runs of the same project,
  including two runs of **different agents** in that project — the
  precondition added here is project scope, and nothing narrower.
- `CompareBehaviorSnapshots` and `NewEvaluationScorecard` are unchanged: same
  signatures, no store, no project argument. Their existing environment-ref
  equality check still fires on mismatched refs within one project.

### Persistence, per backend and shared

The conformance suite task 064 established runs these against **both**
backends; the differential suite compares resulting logical state.

- Create, read back, and compare field by field, including unranked.
- Duplicate `(project, ref)` is `ErrStoreAlreadyExists`, and the stored value
  is untouched.
- The same ref in two projects coexists and reads back independently.
- A create naming a missing project is `ErrStoreNotFound`.
- The 65th environment in a project is `ErrEnvironmentLimit`; the 64th
  succeeds. The cap is per project: a second project is unaffected.
- A duplicate ref in a project **at or over** the cap is
  `ErrStoreAlreadyExists`, not `ErrEnvironmentLimit` — the identity check
  precedes the cap check, and a caller adding nothing is not hitting a limit.
- `UpdateEnvironment` with a stale revision is `ErrStoreConflict` and changes
  nothing.
- `UpdateEnvironment` whose `next` changes `ref` or `project_id` is refused.
- `ProjectEnvironments` ordering is `ref` byte order — with a fixture whose
  refs order differently under a locale-aware collation than under byte
  order, which is what proves `COLLATE "C"` is doing its job on PostgreSQL.
- `ProjectEnvironments` paging: a project with 130 environments is
  enumerated completely in pages of 64 with no duplicate and no omission;
  each page after the first starts strictly after the previous page's last
  ref; the final page is short and carries no continuation.
- `ProjectEnvironments` with a `limit` above 64 or below 1 is refused at the
  store's edge rather than clamped.
- Renaming, re-ranking and archiving rows **between** pages of a traversal
  changes none of the above — the property that made `ref` the cursor.
- `ProjectEnvironments` on an empty project returns an empty slice and no
  error; on a missing project returns `ErrStoreNotFound`.
- A corrupt row — unknown status, rank out of range — is `ErrStoreCorrupt`,
  not a clamped value.
- Concurrency: N goroutines configuring one environment produce exactly one
  winner per revision and no lost update, under `-race`.

### The cap under contention

The conformance suite asserts the **logical** outcome on both backends; a
PostgreSQL-specific test proves the **locking** that produces it, because
SQLite reaches the same answer through a different mechanism and would hide a
missing lock.

Shared conformance, both backends:

```text
seed 63 environments in one project
start two CreateEnvironment calls with distinct refs, released together
assert: successes == 1
        ErrEnvironmentLimit == 1
        final row count == 64
```

- The same shape with the **same** ref in both calls: one success, one
  `ErrStoreAlreadyExists`, never `ErrEnvironmentLimit`, final count 64.
- The same shape against two different projects at 63 each: both succeed,
  because the transactions lock different rows.
- Repeated enough times to be meaningful rather than once — the loop count
  belongs to the implementation, and the test fails on the first run that
  ends at 65.

PostgreSQL-specific, against a real server:

- The two creates are driven on separate pooled connections, so the test
  exercises the pool the conformance suite cannot.
- The second transaction is observed to **wait** rather than to count
  concurrently: with the first transaction held open by a test hook, the
  second blocks until it commits. This is what distinguishes "serialized by a
  lock" from "serialized by luck", and it is the assertion that fails if
  `FOR UPDATE` is ever dropped.
- Contention across N goroutines into one project seeded near the cap ends at
  exactly the cap, with the remainder reporting `ErrEnvironmentLimit`, under
  `-race`.
- Creates into distinct projects run concurrently without blocking each
  other.

### Migration

- A v2 database with runs across two projects migrates to v3 and backfills
  exactly the distinct `(project, environment)` pairs those runs reference,
  all unranked and active, with `name == ref`.
- Every pre-existing row survives the migration byte-identical.
- A v2 database with no runs migrates to an empty environments table.
- After migration, creating a run against a backfilled environment succeeds
  without any further operator action.
- The same environment string used by two different projects backfills as two
  independent environments, because identity is `(ProjectID, EnvironmentRef)`.
- A v3 database opened again is verified, not re-migrated.
- A v3 database opened by a binary supporting v2 is `ErrStoreSchemaVersion`.
- The v1 → v2 → v3 path works from a task 057 database.
- Both backends, same assertions, in the shared suite.

#### Migration against the cap

Three fixtures, because the interesting behaviour is at and past the
boundary:

```text
63 distinct refs → migrate → 63 rows
                 → one create succeeds as the 64th
                 → the next create is ErrEnvironmentLimit

64 distinct refs → migrate → 64 rows
                 → every create is ErrEnvironmentLimit

65 distinct refs → migrate → 65 rows, none dropped, merged, renamed,
                             archived or ranked
                 → every historical run still loads, and each still resolves
                   its own environment
                 → the list route enumerates all 65 through its pages
                 → every row is readable, configurable, archivable and
                   re-activatable
                 → every create is ErrEnvironmentLimit
```

The 65-ref case is the one that would have failed the first version of this
specification, and it asserts the whole resolution: history is preserved, the
cap governs creation only, and the response is bounded by the page rather
than by the row count.

### HTTP

- Each route: success shape, status code, and envelope version.
- `rank` omitted in a response for an unranked environment; present and
  numeric otherwise.
- Validation failures map to `400` with `invalid_request`; not-found to
  `404`; duplicate to `409 already_exists`; stale revision to
  `409 conflict`; creation cap to `409 conflict`; cross-project comparison to
  `400 invalid_request`.
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
- The list route defaults to 64 when `limit` is absent, rejects `limit=65`,
  `limit=0` and `limit=abc` with `400`, and rejects a malformed `after` with
  `400`.
- A project holding 130 environments is enumerated over three pages by
  following `next_after`, with every ref seen exactly once; `next_after` is
  present on the first two pages and absent on the last.
- `after` naming a ref that does not exist is not an error: it is a position,
  and the page starts at the first ref sorting after it.
- The page is never larger than the limit, whatever the project holds — the
  regression a migrated project would otherwise expose.

### CLI

- Each subcommand issues exactly one request to the expected path and method,
  against a stub server.
- Exit codes: `0` success, `2` usage (missing `--project-id`, missing
  `--revision`, `--rank` with `--clear-rank`), `3` operational (transport
  failure, `409`).
- `--json` emits the response body unchanged.
- `env list` follows `next_after` until it is absent, so a migrated project
  with more environments than one page is fully listed, and it sends no
  `limit` above the documented maximum.
- `env list` may order rows for **display** by `(rank, ref)` with unranked
  last, using the rank the server supplied. What it must not do is decide
  precedence: no adapter implements `CanPromote`, and a test asserts the CLI
  contains no rank comparison that answers a promotion question.

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
- **Ordering lives in one place.** `CanPromote` is the only implementation of
  promotion precedence; no adapter re-derives it from ranks. Sorting rows for
  display is not that, and the distinction is asserted rather than assumed:
  the test names `CanPromote` as the only function answering the question.

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
    preserves every existing row, whatever the resulting count.
14. The only collection route added is a project's environments, traversed by
    the immutable `ref` with `after`/`limit`, at most 64 rows per page,
    complete for any project whatever its row count, and correct under
    concurrent renaming, re-ranking and archiving.
15. New creation cannot take a project past 64 environments, including under
    concurrent writers on PostgreSQL: creation serializes on the owning
    project row, a same-ref race reports `ErrStoreAlreadyExists` rather than
    the cap, and — on PostgreSQL, where the lock is per row — creates in
    different projects do not block each other. Both backends produce the
    same outcomes; only their contention differs.
16. Migration preserves every distinct historical `(project, environment)`
    pair — nothing dropped, merged, renamed, archived, ranked or synthesized
    — even when a project exceeds the creation cap; such a project keeps
    every row readable, configurable, archivable and enumerable, and refuses
    only new creation.
17. `CompareEvaluations` refuses two runs from different projects with
    `ErrComparisonScope`, before loading evidence, and
    `CompareBehaviorSnapshots` and `NewEvaluationScorecard` keep their
    signatures and gain no store access.
18. `trustvian env` exists with the specified subcommands and exit-code
    scheme; it pages the list route to completion; and it imports no platform
    package and implements no promotion ordering.
19. No TUI or WebUI change.
20. No core change: no engine, `baseline.Key`, fingerprint or public API
    modification, and no core package that knows a platform Environment
    exists.
21. No URL, credential, secret, or deployment field is stored, and no
    outbound request is made on an environment's behalf.
22. `gofmt`, `go vet`, `go test ./...` and `go test -race ./...` pass in every
    module with `GOWORK=off`, and the platform-boundary and module scripts
    pass.
23. ADR 0039 is added by the implementation PR, recording the seven decisions
    listed above.
