# 074 — Zero-Input Live Behavior WebUI

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [059](059-realtime-infrastructure.md),
[063](063-minimal-web-control-plane.md),
[065](065-environment-model.md),
[066](066-promotion-workflow.md) — implementation ordering only, for the
schema-version chain,
[073](073-otel-collector-evaluation-ingest.md)
Blocks: [072](README.md) — the OSS `v1.0` release gate

## Objective

Turn the WebUI from an ID-driven control-plane form into a zero-input live
developer surface.

When telemetry is already flowing into Trustvian, opening `/` must show the
active agents, the evaluation activity and the behavioral flow **without the
developer creating or typing a Project, Agent, Candidate or EvaluationRun
identifier into the browser**.

```text
Open Trustvian and watch what your agent is doing now.
```

and, locally:

```text
Run your agent locally. Trustvian shows how it behaves before you ship it.
```

### What zero-input does and does not mean

Zero-input is a statement about **discovery**, never about **provisioning**.

```text
producer / runtime establishes the evaluation context
        ↓
telemetry flows
        ↓
WebUI discovers and renders it automatically
```

The browser stays an observer and a control-plane client. It does not
fabricate a Project, Agent, Candidate, EvaluationRun, Decision, fingerprint,
scorecard or gate result, and it never creates a durable entity because an SSE
frame arrived. See [Discovery is not
provisioning](#discovery-is-not-provisioning).

## Why

The gap is visible the first time somebody who did not write the code runs a
local agent against Trustvian and opens the browser.

**The default screen asks for an ID nobody has.** The shipped WebUI opens on an
"Open by ID" panel with four text inputs — project, agent, candidate,
evaluation run — and `docs/webui.md` states the position plainly: "There is no
list and no search." A developer whose agent is already producing telemetry has
no way to ask *what is happening right now*; they must go and find an
identifier that a producer chose, and type it in.

**The data to answer that question is already on the wire.** `GET /v1/realtime`
accepts `project_id`, `agent_id` and `run_id` filters, and
`RealtimeFilter.matches` treats an empty value as unconstrained on every
dimension — an unfiltered subscription already receives all local activity.
Every frame carries a complete `RealtimeScope`: project, agent, candidate, run,
environment and behavioral profile. The browser can therefore name the active
scopes from events alone, with no database read per event and no identifier
typed by a human.

**Task 063's deferral has now met its requirement.** Task 063 shipped no
collection route on purpose, because "pagination, sort order, cursor semantics
and scoping have not been designed, and `/v1` route shapes are a stable
contract once published." Task 065 then designed exactly those four things for
one entity. What was missing was a concrete consumer for the rest of the
hierarchy. This task is that consumer: a reload with no live traffic must still
find what exists, and the only honest answer is a bounded authoritative
listing.

**The release gate names this journey.** The `v1.0` Track B gate begins "run
locally → observe behavior live". Today the second step requires reading an
identifier out of a producer's logs. That is not a journey "by a developer who
has not read the source".

## Current gap, verified against `main`

Each row below was checked in source rather than assumed. Two of the
assumptions this task was briefed with turned out to be wrong; both are
corrected here and the specification is written against reality.

| Claim | Verified | Where |
|---|---|---|
| No Project/Agent/Candidate/Run collection route exists | **True** | `handler.go` registers `GET /v1/projects/{project_id}` and siblings, and no bare-collection GET |
| Task 065 added only the project-scoped environment collection | **True** | `GET /v1/projects/{project_id}/environments` is the one list route |
| `GET /v1/realtime` supports `project_id`, `agent_id`, `run_id` | **True** | `RealtimeFilter` carries exactly those three |
| An empty `RealtimeFilter` is unconstrained | **True** | `matches` skips any empty dimension; `validate` documents "Empty stays valid and means unconstrained" |
| A browser can therefore subscribe to all local activity with no run ID | **True** | Follows from the two rows above; no code change is needed to do it |
| Realtime is bounded and ephemeral, never durable history | **True** | `stream_ready` carries `replay_available: false`, `resync_required: true`; ADR 0032 |
| `RealtimeObservation` carries scope, behavior, fingerprint, decision, risk, trust, anomaly and new-behavior state | **True** | `platform/realtime.go`, and `realtimeObservationDTO` publishes all of it |
| Prompts, completions, tool arguments, arbitrary attributes and raw `DecisionRecord`s are excluded | **True** | `realtimeObservationDTO`'s doc comment says so and a test asserts a distinctive attribute value never appears |
| The WebUI is a same-origin static adapter over `/v1` | **True** | `webui.NewHandler()` takes no arguments — ADR 0036's structural guarantee |
| The WebUI has no control-plane authority | **True** | Same signature — it cannot hold one |

### Corrections to the briefed assumptions

**There is no `Direction` dimension on a behavior.** The task brief's
conceptual edge mapping lists `Direction` alongside `Operation.Category` and
`Operation.Name`. `event.Operation` does carry an `OperationDirection`, but
`trustvian.StableFeatures` — the behavioral identity that reaches
`RealtimeObservation.Behavior` and `behaviorDescriptorDTO` — does not. Its
fields are `ActorType`, `OperationCategory`, `OperationName`, `TargetName`,
`TargetCategory` and `Environment`.

Direction is therefore **not available** to the graph, and this task does not
add it. Adding a field to `StableFeatures` changes behavioral identity, which
is a core change this task is forbidden to make and would be wrong to make for
a visualization. The graph renders the six dimensions it actually has.

**There is no Python/Ollama demo inside *this* repository, and the project's
reference demo lives elsewhere.** `make demo` here runs `trustvian analyze`
against a bundled JSON fixture; it starts no agent and produces no live
telemetry, and no `.py` file exists anywhere in this tree.

The project-level reference local-agent workflow is the companion repository
**`trustvian/trustvian-python-agent-demo`**: a Python agent driven by Ollama
`gemma3:4b`, instrumented with runtime OpenTelemetry, feeding the Trustvian
Collector and the evaluation ingest path, exercising CRM, knowledge, mail and
export targets across a reference run and a candidate run.

That demo is this task's integration target, not a thing to duplicate. Its
README currently instructs the user to open the WebUI and type `run-reference`
and `run-candidate` into the Open-by-ID form — which is exactly the friction
this milestone removes. See [The companion demo](#the-companion-demo).

## User journey

The journey this task must make true, with nothing copied into a browser
field:

```text
Terminal A                          Terminal B
$ make local                        $ <run the companion demo agent>
  Web: http://127.0.0.1:<port>/       telemetry flows through the
                                      Collector into /v1

Browser  →  http://127.0.0.1:<port>/

  ● Trustvian connected
  ● Telemetry flowing

  support-agent appears automatically, as a card
        ↓  the card is selected, and its graph draws
        ↓  each call pulses along its edge as the frame arrives
        ↓  a fingerprint the reference never showed gains NEW
        ↓  run completes
        ↓  the behavioral summary stays reachable from authoritative state
```

The pulses arrive in whatever order the agent actually worked in. The demo is
model-driven, so that order is the model's choice and differs between runs —
the graph animates what arrived, and this task validates the observed behavior
*set*, never a sequence. See [The companion demo](#the-companion-demo).

At no point does the developer type `support-demo`, `support-agent`,
`run-reference` or `run-candidate` into the page.

## Scope

- A new **Live** view, which is the default landing view.
- Automatic active-scope discovery from an unfiltered realtime subscription.
- A bounded, animated behavior-flow graph driven by real observations.
- The minimum bounded, authoritative collection surface needed to rediscover
  the control-plane hierarchy after a reload with no live traffic — a genuine
  platform capability, not a DOM convenience.
- An information architecture that keeps every existing manual control
  reachable but secondary.

## Non-goals

**No provisioning from the browser, and none from telemetry.** This task does
not make arbitrary OTLP create a Project, `service.name` create an Agent, or
the WebUI create a Candidate or start a run. See [Discovery is not
provisioning](#discovery-is-not-provisioning).

**No retained event history.** [Task 067](README.md) owns the event-history
capability boundary. The live graph is a *current viewport*, not a trace
explorer, and nothing here retains raw observations or reconstructs a past
animation from aggregates.

**No change to behavioral identity.** `StableFeatures` gains no field, and no
layout coordinate, node position or WebUI concept enters the core or the
platform domain.

**No semantic invention.** If telemetry proves only `POST → export.localhost`,
the UI renders `POST → export.localhost`. It never labels that
`export_customer` unless normalized telemetry actually carried that operation
name.

**No new privacy surface.** No prompt, completion, request body, tool argument,
arbitrary attribute or raw `DecisionRecord` is added to any contract this task
touches.

**No frontend framework, build step or dependency.** See [Web
technology](#web-technology).

**No realtime protocol change.** `GET /v1/realtime` keeps its route, filters,
event kinds and payload shape. Everything this task adds is additive
elsewhere.

**No unbounded route.** There is no `GET /v1/everything`, and no route returns
an unbounded page.

## Discovery is not provisioning

Stated once, in one place, because it is the line this task is most likely to
be pushed across during implementation.

**This task owns:** automatic discovery and presentation of platform state and
realtime activity that *already exists*.

**This task does not own, and must not quietly acquire:**

| Not this task | Why it is a different question |
|---|---|
| OTLP traffic creating a Project | A durable entity created by an unauthenticated producer is a provisioning and trust decision |
| `service.name` becoming an Agent | Identity mapping needs a rule nobody has written, and getting it wrong pollutes a durable hierarchy |
| The browser creating a Candidate | The browser has no authority to name what a candidate *is* |
| The browser starting an evaluation run | Lifecycle is the control plane's, driven by whoever owns the run |

**Honest statement of the current requirement:** the local developer workflow
still requires a producer or runtime to create the evaluation hierarchy before
telemetry can be attributed to it — the Collector's `evaluation:` block names
an existing `run_id`, and task 073 refuses to start ingest unless that run is
running. This task does not change that. It changes only whether a *human* has
to retype those identifiers into a browser to see the result.

If zero-configuration auto-provisioning is wanted — a runtime that creates a
project, agent, candidate and run on first telemetry — that is a **follow-up
milestone** with its own trust, naming and lifecycle design. It is named here
so it cannot be smuggled in as "part of making the UI zero-input".

## Architecture

Unchanged from [ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md):

```text
browser ──SSE──▶  GET /v1/realtime            (unfiltered, for the Live view)
        ──GET──▶  bounded collection routes    (authoritative discovery)
        ──GET──▶  existing by-ID routes        (detail)

platform/webui.NewHandler()   no arguments, no control plane, static assets only
```

The WebUI gains **no** new capability, import or authority. It gains new
*server routes to call*, which is a platform capability that the CLI and any
future client may use equally.

### Web technology

The existing stack is kept, and this task finds no evidence it is
insufficient:

```text
embedded static HTML · vanilla JavaScript modules · CSS · SVG
same-origin /v1 · SSE · no npm · no framework · no CDN · no external font
no browser storage
```

The graph is **SVG with CSS transitions**, drawn by hand. The topology this
task renders is bounded at a few dozen nodes by construction, which is far
below where a visualization library earns its dependency — and ADR 0036's
no-build-step property is worth more here than any layout algorithm would be.

A future task that wants a force-directed layout over hundreds of nodes may
revisit this, and must do so with a measured requirement rather than a
preference. Introducing a frontend build system for animation alone is
explicitly refused.

## Discovery and listing contracts

### The problem this solves

```text
an evaluation happened
        ↓
the browser opens or reloads afterward
        ↓
no realtime event arrives, because nothing is happening now
```

The page must still show what exists. Four answers are refused outright:

| Refused | Why |
|---|---|
| `localStorage` | Browser state is not authority, and ADR 0036 forbids storing platform state in the browser |
| Direct database access from the WebUI | It is a static same-origin client; giving it a database is the whole boundary gone |
| Pretending realtime replays | `stream_ready` publishes `replay_available: false`. ADR 0032 is explicit |
| Polling the database every second | Not a discovery design, and it scales by accident rather than by contract |

The answer is a **bounded authoritative collection surface**, designed the way
task 065 designed the first one.

### The shape

Four routes, each scoped by the parent the domain already makes immutable:

```text
GET /v1/projects?limit=&after=
GET /v1/projects/{project_id}/agents?limit=&after=
GET /v1/agents/{agent_id}/candidates?limit=&after=
GET /v1/candidates/{candidate_id}/evaluation-runs?limit=&after=
```

These follow the existing entity graph exactly — `Agent.ProjectID`,
`Candidate.AgentID`, `EvaluationRun.CandidateID` are each immutable
caller-owned references the store already holds — so no denormalization, no
new column and no new join is required to serve them.

`GET /v1/projects` is the one unscoped collection, and it is bounded by the
same page limit as every other. It is the root of a hierarchy; without it the
hierarchy has no entry point that does not require prior knowledge.

### Semantics, identical to task 065's

One pagination shape in this API, not two. Every property below is the
environment collection's, restated because a contract that is only implied is
a contract that drifts:

| | |
|---|---|
| Scope | the parent named in the path; `projects` is global |
| Ordering | `id`, **byte-ascending**, `COLLATE "C"` on PostgreSQL |
| Cursor | `after`, exclusive, an identifier validated by the same `validateID` rules |
| Limit | `1..64` at the store edge and at the route; the HTTP default is 64, and a larger value is `400 invalid_request`, never a silent clamp. The store accepts nothing above 64 — task 066 corrected exactly this, and a transport's lookahead must not widen a public contract |
| Continuation | `next_after` present exactly when the page filled its limit and another row follows |
| How that is decided | a short page is the end; a full page is resolved by a **second bounded call** with `limit=1` after the page's last id — never by fetching `limit+1` |
| Concurrent creation | a row created during a traversal appears if and only if its id sorts after the caller's position. Nothing already returned moves, because `id` is immutable |
| Missing parent | `404 not_found` |
| Empty | `200` with an empty array, never a `404` |
| Backends | identical on SQLite and PostgreSQL, proven by the shared conformance suite |

**Why `id` and not `created_at`.** Task 066 established that this schema stores
timestamps as `RFC3339Nano` text, which is **not lexically ordered** — Go trims
trailing fractional zeros, and a non-UTC offset breaks it again — so a
timestamp cursor would silently skip and repeat rows. `id` is immutable,
unique, already validated and byte-ordered on both backends. Newest-first
ordering over an unbounded history remains [task 067](README.md)'s subject.

For the Live view this costs nothing: activity discovery comes from realtime,
where events arrive in the order they happened.

### Response shape

```jsonc
// GET /v1/projects/proj-1/agents?limit=64
{
  "version": "1",
  "project_id": "proj-1",          // the scope, echoed; absent on /v1/projects
  "agents": [ … ],                 // id-ascending, at most `limit` entries
  "next_after": "agent-9"          // omitted on the last page
}
```

Each element is the **existing** detail DTO for that entity, unchanged — a
collection publishes no field a by-ID read does not already publish.

### Persistence impact

No table and no column is added. Three indexes are, because three of the four
scans are not free from a primary key:

```sql
CREATE INDEX platform_agents_by_project     ON platform_agents     (project_id, id);
CREATE INDEX platform_candidates_by_agent   ON platform_candidates (agent_id, id);
CREATE INDEX platform_runs_by_candidate     ON platform_evaluation_runs (candidate_id, id);
```

`platform_projects` needs none: `WHERE id > ? ORDER BY id LIMIT ?` is a range
scan along its primary key.

Adding an index is an additive schema change that existing databases must
receive, so a migration is required.

**`SchemaVersion` moves 4 → 5**, and this is pinned rather than left to merge
order. [Task 066](066-promotion-workflow.md) is a merged specification that
pins 3 → 4 for the promotion table, the roadmap sequences 066 before 074, and
two merged specifications must not both claim "the next number" and resolve it
by whichever implementation lands first.

The migration's content:

```text
CREATE INDEX platform_agents_by_project
CREATE INDEX platform_candidates_by_agent
CREATE INDEX platform_runs_by_candidate
stamp schema version 5
```

No table, no column, no backfill, no data rewrite, and no synthetic entity.

**Implementation ordering is therefore a real constraint**, and the only one
this task places on 066: task 074's implementation must not land before task
066's, because they share one migration chain. If there is ever a compelling
reason to build 074 first, that is a coordinated amendment to task 066's
merged specification — renumbering both — not a decision for whichever pull
request is ready first.

Nothing else about 074 depends on 066. The promotion model, its routes and its
store methods are irrelevant here; only the counter is shared.

### Capability placement: two stores, not one

The four collections do **not** all belong to one capability, and this task
must not collapse the split to make browsing convenient.

```go
// ControlStore — the durable control hierarchy it already owns.
Projects(ctx context.Context, after ProjectID, limit int) ([]Project, error)

ProjectAgents(
    ctx context.Context, projectID ProjectID, after AgentID, limit int,
) ([]Agent, error)

AgentCandidates(
    ctx context.Context, agentID AgentID, after CandidateID, limit int,
) ([]Candidate, error)
```

```go
// EvaluationStore — runs are evaluation state, not control state.
CandidateEvaluationRuns(
    ctx context.Context, candidateID CandidateID, after EvaluationRunID, limit int,
) ([]EvaluationRun, error)
```

`ControlStore` already owns `Project`, `Agent`, `Candidate`, `Environment` and
(from task 066) `Promotion`. `EvaluationStore` already owns `EvaluationRun`
and its evidence. Task 057 split them because "the two have different
lifetimes, write patterns and consumers … a later backend may reasonably
implement one and not the other", and a list method does not change which
capability an entity belongs to. Putting `CandidateEvaluationRuns` on
`ControlStore` would mean a backend implementing only the control capability
would have to serve evaluation runs it does not store.

**No new interface.** No `HierarchyStore`, `DiscoveryStore`, `ListStore`,
`Database` or generic query capability. The existing model is sufficient, and
a fifth interface with one implementation pair and one consumer is the
abstraction CLAUDE.md says not to build ahead of need.

**No update and no delete** on any of the four. They are reads.

Each mirrors `ProjectEnvironments`'s signature and error contract exactly:
`limit` outside `1..MaxListPage` is `ErrInvalidID`, a missing parent is
`ErrStoreNotFound`, an existing parent with no children is an empty slice.
Compile-time assertions for both backends, as task 064 established.

**The control plane composes both.** `ControlPlane` already holds a
`ControlStore` and an `EvaluationStore`, so it exposes one coherent browsing
surface — `Projects`, `ProjectAgents`, `AgentCandidates`,
`CandidateEvaluationRuns` — and the HTTP adapter never learns that the last
one came from a different capability. The adapter should not care; the service
boundary must.

### Compatibility

Purely additive. Five new stable rows for `docs/compatibility.md` — four route
shapes and one paging contract — written by the implementation PR, **not by
this specification**, because a contract must not be published before it
exists.

Existing `/v1` behaviour is untouched: no route changes shape, no field changes
meaning, and `GET /v1/realtime` is not modified at all.

## Realtime flow

### The Live view's subscription

```text
GET /v1/realtime          with no project_id, agent_id or run_id
```

Unconstrained, which `RealtimeFilter` already defines and already implements.
Nothing about the endpoint changes.

### Resync, unchanged in shape

The existing subscribe-first protocol holds, and ADR 0032's authority model
holds with it:

```text
subscribe
    ↓
stream_ready              replay_available: false, resync_required: true
    ↓
fetch authoritative bounded state      ← the new collection routes
    ↓
apply the snapshot
    ↓
replay the frames buffered during the fetch
    ↓
live
```

**The one variation this view needs**, stated explicitly so it cannot be
mistaken for a weakening: the existing single-run view resyncs by reading *one*
run's authoritative state. The Live view has no single run, so its snapshot is
the **first bounded page of `GET /v1/projects` and nothing else** — a snapshot
of *what exists at the root*, not of *what is active*. Activity is realtime's
answer, and the page must say which is which.

#### A bounded route is not a bounded workflow

Each route caps a page at 64. That alone does not bound discovery, and a naïve
hierarchy walk is how a bounded API becomes an unbounded client:

```text
64 projects × 64 agents × 64 candidates × 64 runs  =  16,777,216 rows
```

fetched across a quarter of a million requests, during which the resync buffer
holds **64** frames. Under live traffic that buffer overflows, the client
abandons and resynchronizes, and the crawl starts again — a resync loop that
gets worse the larger the database is, which is precisely backwards.

So the workflow is bounded explicitly, not incidentally.

#### The startup budget

```text
automatic project pages at startup      1
automatic child traversal               0
automatic continuation following        0
```

One request. Not one per project, not one per level, and not one per
continuation token. The same budget applies to **every** reconnect, so a
flapping connection cannot amplify into a crawl.

Realtime remains the answer to *what is active now*: a frame creates or
updates a scope card from its own `RealtimeScope`, with no API read. A page
can therefore be fully useful for the journey this task exists for — watch the
agent that is running — having issued exactly one collection request.

#### Lazy, user-triggered descent

Children are fetched only when a person asks for them, one bounded page at a
time:

```text
project selected or expanded   → one page of that project's agents
agent selected or expanded     → one page of that agent's candidates
candidate selected or expanded → one page of that candidate's runs
```

A continuation is never followed automatically. `next_after` renders as an
explicit affordance — *More* — and costs one request when pressed. Selecting a
scope may read that entity's existing by-ID route for authoritative detail;
that is one bounded read, not a walk.

**This is not polling.** There is no timer, no refresh loop and no interval.
Every collection request is caused by a person or by the single startup
snapshot.

#### Saturation at every level

If the root page carries `next_after`, the view says **more projects exist**.
The same at every level. The page never implies that one page is the whole
platform, and a bounded root snapshot is never described as a complete
hierarchy.

Nothing else changes: `PENDING_MAX` still bounds frames buffered during the
fetch, overflow still abandons the stream and resynchronizes rather than
dropping a frame, and no count the page displays as authoritative comes from a
realtime event.

Refused, as before: polling on a timer, treating SSE as state authority,
browser-computed durable counts, an unbounded event array, or any form of
simulated replay.

## Behavior-graph semantics

The graph is defined over the dimensions that already exist. It invents
nothing.

```text
source node   the observed workload identity — the Agent of the event's scope

edge          Operation.Category  +  Operation.Name      (from StableFeatures)

target node   Target.Name  +  Target.Category            (from StableFeatures)

edge state    Decision · RiskLevel · TrustScore · AnomalyScore ·
              AnomalyConfidence · NewBehavior
```

There is deliberately **no `Direction`**: `StableFeatures` does not carry one,
and adding it would change behavioral identity. See [Corrections to the briefed
assumptions](#corrections-to-the-briefed-assumptions).

A rendered example, from five observations in one run:

```text
                    POST
             ┌───────────────▶ ollama.localhost
             │
             │      GET
support-agent├───────────────▶ crm.localhost
             │
             │      GET
             ├───────────────▶ knowledge.localhost
             │
             │      POST
             ├───────────────▶ export.localhost
             │                    NEW
             │      POST
             └───────────────▶ mail.localhost
```

### The graph is scoped to one selected run

`fingerprint_id` is **not globally unique**, and a Live view over an unfiltered
stream must not treat it as if it were.

A fingerprint identifies the behavioral shape derived from `StableFeatures`.
It deliberately carries no `ProjectID`, `AgentID`, `CandidateID`,
`EvaluationRunID` or `ActorID` — that is the whole point of behavioral
identity, and task 051 was explicit that run and candidate metadata must never
become fingerprint dimensions. So two unrelated agents doing the same thing
produce the same fingerprint:

```text
project-a / agent-a / run-1    POST → ollama.localhost    fingerprint abc
project-b / agent-b / run-2    POST → ollama.localhost    fingerprint abc
```

Those are one behavior and **two observations**, and they may legitimately
disagree about `new_behavior`, `decision`, `risk_level`, `trust_score`,
`anomaly_score` and `anomaly_confidence`, because every one of those is
contextual to the run that produced it. Merging them into one edge would let
run A's `new_behavior: true` overwrite run B's state and would report one
agent's decision as another's.

**The model, decided here rather than left to implementation: the graph renders
exactly one selected run.**

```text
unfiltered stream
        ↓
bounded active scope cards          ← every active run, from RealtimeScope
        ↓
one selected card
        ↓
one run-scoped graph                ← only that run's observations
```

Within a run-scoped graph, `fingerprint_id` **is** a sufficient edge key,
because the run is already fixed. An observation whose scope is not the
selected run updates its card and is not drawn.

Three reasons for this over a combined multi-run graph:

- it is readable — one agent's topology, not several overlaid;
- it bounds the topology naturally, by the run's own behavior count rather
  than by an arbitrary visual ceiling;
- it never conflates evidence from two evaluations, which is the same mistake
  at the UI layer that task 065 closed at the comparison layer.

A developer still sees every active agent and run simultaneously, as cards.
Selecting one changes which graph is drawn; it changes nothing about which
scopes are discovered.

**If a future task wants a combined graph**, every entity in it must be keyed
by a conceptual tuple — `(run_id, fingerprint_id)` for an edge,
`(project_id, agent_id)` for a source node — held as structured keys and never
as concatenated strings that could collide across a delimiter. That is stated
so the constraint survives if the model is revisited; this task does not build
it.

### Scope cards

Each card is created and updated from `RealtimeScope` alone, with no `/v1`
read per event, and retains the full scope so two cards can never be confused:

```text
ProjectID · AgentID · CandidateID · RunID · EnvironmentRef · BehavioralProfileRef
```

Two runs of one candidate, two candidates of one agent, and the same
environment ref under two projects are each distinct cards. A card shows last
activity and a count of behaviors *seen live*, labelled as such — it is not a
durable count, and an authoritative one comes from `/v1` when the scope is
selected.

### Animation is evidence, not decoration

Every visual event corresponds one-to-one with a received observation.

| Trigger | Rendering |
|---|---|
| observation received | a pulse travels the edge from source to target; the target node briefly emphasises |
| `new_behavior: true` | the edge and target gain a persistent `NEW` badge and a stronger temporary emphasis |
| decision / risk change | the event row updates; the edge's state indicator updates |
| stream disconnected | the graph enters a visibly labelled *disconnected / resyncing* state |
| `stream_ready` | a visibly labelled *live* state |

**No intermediate frames are manufactured** to make the graph look busy, and no
animation fires without a frame behind it. A quiet agent produces a still
graph, which is the correct picture.

## Bounds

Three different kinds of limit, which this task must not conflate:

| Kind | What it protects | What exceeding it means |
|---|---|---|
| **visualization bound** | the browser's rendering cost | the picture is incomplete — say so |
| **behavior evidence bound** | the platform's evidence, already bounded at 512 distinct behaviors per run | the *run's evidence* is incomplete — `behavior_complete: false` already reports it |
| **realtime queue bound** | undelivered frames per subscriber | a notification would be lost — abandon and resync, never drop |

Only the first is new. The second and third exist and are reused unchanged.

### Explicit visualization limits

| Bound | Value | Rationale |
|---|---|---|
| active scope cards | **16** | Enough for every agent a developer runs locally at once, small enough to stay readable |
| graph source nodes | **8** | One per agent being rendered; beyond that the view is a list, not a topology |
| graph target nodes | **64** | Matches the platform's page bound, so one screenful of topology is one page of anything else |
| graph edges | **128** | Two per target on average; the ceiling a hand-laid-out SVG stays legible at |
| visible observation rows | **100** | The existing `DISPLAY_MAX`, reused rather than re-chosen |
| frames buffered during resync | **64** | The existing `PENDING_MAX`, which matches the server's per-subscriber queue |
| automatic collection requests at startup or reconnect | **1** | One page of `GET /v1/projects`. No child traversal and no continuation is automatic — see [The startup budget](#the-startup-budget) |

The last two are already implemented and already documented as different kinds
of limit; this task adopts them rather than introducing parallel numbers.

### Saturation is stated, never silent

When a visualization bound is reached:

- the view shows an explicit saturated state — text and an icon, naming which
  bound and how many items are not drawn;
- eviction is **least-recently-observed**, and the page must not then describe
  the remaining graph as the complete run;
- any authoritative count already on screen — a run's `record_count`, its
  distinct-behavior count — stays as read from `/v1`, unaffected by what the
  graph chose to draw;
- `behavior_complete: false` is surfaced as its own, separate statement,
  because a saturated *viewport* and saturated *evidence* are different facts
  and confusing them would misreport the platform.

The page never accumulates an unbounded `Map`, array or DOM subtree.

## Security and privacy

Every current property is preserved, and this task adds no new class of
content.

| Property | Held by |
|---|---|
| strict CSP, `default-src 'none'` | unchanged handler constant |
| no `innerHTML` | existing rule; the SVG graph builds nodes with `createElementNS` and sets text via `textContent` |
| untrusted API strings rendered as text | existing rule, extended to every new field the graph renders |
| no CORS, no external asset, no external font | unchanged |
| no browser storage | unchanged, and the collection routes are what make storage unnecessary |
| loopback-only local runtime | unchanged |
| no prompt, completion, request body, tool argument, arbitrary attribute or raw `DecisionRecord` | the graph consumes only `realtimeObservationDTO` and the existing detail DTOs, none of which carry any of these |
| no raw SQL anywhere near the browser | unchanged |

Two additions worth stating because an animated surface invites them:

**A tooltip is not an exemption.** Hover, focus and detail panels render the
same allowlisted fields as the graph. Room on screen is not a reason to add
evidence the contract excludes.

**The new collection routes publish nothing new.** Each element is the existing
detail DTO, so a list cannot become a privacy regression by accident.

## Accessibility

- **Never colour alone.** Every state — new, blocked, high risk, disconnected,
  saturated — carries text or an icon as well.
- **`prefers-reduced-motion` is honoured.** Motion is replaced by instantaneous
  state transitions; every fact the animation conveyed remains visible as text
  or a badge. No information exists only in movement.
- **No flashing or strobing**, at any rate.
- The graph is keyboard-reachable and screen-reader-legible: nodes and edges
  are focusable with accessible names, and the live feed keeps the existing
  polite live-region behaviour.
- Connection state stays in the existing `role="status"` region.

## Failure and reconnect semantics

| Situation | Behaviour |
|---|---|
| stream drops | the existing bounded backoff sequence; the graph shows *disconnected*, and stops claiming to be live |
| reconnect | full resync — snapshot from the collection routes, then buffered frames, then live |
| resync buffer overflows | abandon and resynchronize, never drop a frame silently |
| handshake never completes | the existing handshake timeout applies unchanged |
| a collection route fails | the view renders the error as text and stays in a stated degraded state; it does not fall back to browser-held state, because there is none |
| an unknown future event kind arrives | ignored structurally — `EventSource` only delivers kinds a listener registered for, which is the existing forward-compatibility mechanism |
| no activity at all | the graph is empty and says so, and the hierarchy from the collection routes is still browseable |

After a reconnect, only what authoritative platform state can legitimately
reconstruct is shown as authoritative. **No historical animation is replayed**,
and no past flow is reconstructed from aggregates.

## Information architecture

```text
Live          ← default
Evaluations
Compare
Manage        ← Project · Agent · Candidate · Evaluation · Environment · Open by ID
```

Exact labels are observational and may be refined during implementation. The
requirement is not the labels:

> A developer must not need the Manage surface in order to watch an
> already-running local agent.

Nothing is removed. Every current capability — create project, create agent,
create candidate, drive a run's lifecycle, open by ID, compare — stays
reachable, because they remain the right tools for debugging and for advanced
use. They stop being the front door.

## The companion demo

The acceptance journey is proved by **`trustvian/trustvian-python-agent-demo`**,
and this task deliberately creates no second Python agent inside this
repository. Two demos of the same thing drift, and the existing one is already
the reference workflow.

The work therefore splits across two repositories, in order:

```text
trustvian/trustvian                      this task's implementation PR
    Live view · behavior graph · collection routes · schema 4 → 5

trustvian/trustvian-python-agent-demo    a separate follow-up PR, afterwards
    README and workflow stop instructing the user to type run IDs
```

The implementation PR here stays scoped to this repository. The companion
update can only land after the WebUI and the routes exist, because until then
its README would document something that does not work.

What changes there:

```text
before   open the WebUI → Open by ID → type run-reference / run-candidate
after    open the WebUI → the reference and candidate runs appear by themselves
                        → watch live behavior with nothing typed
```

### Behavior is validated, ordering is not

The agent is model-driven, so **the model chooses what it does and in what
order**. A verified run of the existing demo produced
`CRM → knowledge → mail → export`, which is as legitimate as any other
sequence. An acceptance criterion that pinned an order would be testing the
model, not Trustvian, and would fail for a correct reason.

What is validated is the **observed behavior set** and its semantic fidelity:

```text
reference run visibly includes
    POST → ollama.localhost
    GET  → crm.localhost
    GET  → knowledge.localhost
    POST → mail.localhost

candidate run additionally includes
    POST → export.localhost
```

and:

```text
export.localhost is marked NEW, because it is present in the candidate's
behavior and absent from the reference evidence
```

The graph animates the **actual arrival order**, whatever it was. Nothing
requires export before mail, mail after export, or any other model choice.

## Tests

Written by the implementation PR.

### Zero-input discovery

- Opening `/` with telemetry flowing shows the active agent and run **with no
  identifier entered**, asserted by driving the page with no input events.
- An unfiltered realtime subscription receives activity from **multiple
  distinct scopes** — two projects, two agents — and each appears as its own
  card.
- Active scope cards are created and updated from event scope alone, with no
  per-event `/v1` read.
- Reload with **no live traffic** rediscovers the hierarchy from the collection
  routes; a test asserts no browser storage API is called anywhere in the
  bundle.

### Graph correctness and scope isolation

- A repeated fingerprint **within the selected run** updates exactly one edge
  and leaves every other edge untouched.
- **Two runs emitting the same `fingerprint_id` do not share an edge.** The
  regression for the whole scope-identity section: feed identical fingerprints
  from `run-1` and `run-2` and assert two distinct graph identities.
- The same fingerprint from two **projects** does not cross-update.
- The same fingerprint from two **candidates** of one agent does not
  cross-update.
- `new_behavior: true` from run A never changes run B's rendered state, and the
  same for `decision`, `risk_level`, `trust_score`, `anomaly_score` and
  `anomaly_confidence`.
- With the run-scoped model: an observation whose scope is not the selected run
  updates that run's **card** and is **not drawn** in the graph.
- Switching the selected card renders only the newly selected run's graph, with
  no residue from the previous one.
- `new_behavior: true` renders a distinct, labelled state — not colour alone.
- The rendered operation and target strings are exactly the descriptor's; a
  test feeds `POST` / `export.localhost` and asserts no semantic name is
  invented.
- No animation fires without a corresponding received frame.

### Bounds and saturation

- Each visualization bound is enforced at its documented value.
- Exceeding a bound produces an **explicit saturated state** naming the bound,
  and the page does not describe the graph as complete.
- An authoritative count read from `/v1` is unchanged by graph eviction.
- `behavior_complete: false` renders as its own statement, distinct from
  viewport saturation.
- Sustained observation traffic leaves DOM node count, edge count and row count
  at their ceilings rather than growing.

### Bounded discovery

- **Startup issues exactly one collection request** — one page of
  `GET /v1/projects` — asserted by counting requests against a stub.
- Startup performs **no** recursive descent: no agents, candidates or runs
  request is issued automatically.
- Startup follows **no** continuation, even when the root page carries
  `next_after`.
- A root page with `next_after` renders an explicit continuation affordance and
  never implies the page is the whole hierarchy.
- Expanding one project fetches exactly one page of that project's agents, and
  nothing else.
- A project with 130 agents is fully traversable through explicit pagination
  and is **not** eagerly loaded; the request count grows only with user
  actions.
- A hierarchy that is larger than one page in every dimension causes no
  automatic request explosion.
- Realtime frames create and update active scope cards **while no child list
  has been loaded at all**.
- **Reconnect uses the same one-request bootstrap**, asserted by forcing
  repeated disconnects and counting requests — the resync-loop regression.
- Browser memory and DOM node count do not grow with the size of the durable
  hierarchy, only with what a person opened.
- No timer-driven refresh exists anywhere in the bundle.

### Realtime and resync

- A slow client preserves the existing resync semantics: overflow abandons and
  resynchronizes.
- Disconnect renders a labelled disconnected state; reconnect performs a full
  snapshot-then-replay.
- Unknown future SSE event kinds are tolerated.
- No count displayed as authoritative originates from an SSE frame.

### Collections, on both backends

- Ordering is `id` byte-ascending, with a fixture whose identifiers sort
  differently under a locale-aware collation — proving `COLLATE "C"`.
- `limit` 64 accepted; 65, 0 and −1 refused with `400 invalid_request`.
- A page never exceeds `limit`; `next_after` appears exactly when another row
  follows.
- **Concurrent creation produces no duplicate and no omission**: a row inserted
  mid-traversal appears if and only if its id sorts after the cursor.
- A project with 130 agents enumerates completely.
- Missing parent → `404`; empty collection → `200` with an empty array.
- SQLite and PostgreSQL return identical logical results, through the shared
  conformance suite.
- The store refuses `limit` 65 as well as 0 and −1 — the bound is `1..64` at
  the store edge, not only at the route.

### Capability boundary

- `ControlStore`'s compile-time surface gains `Projects`, `ProjectAgents` and
  `AgentCandidates` — and **no run listing**.
- `EvaluationStore`'s surface gains `CandidateEvaluationRuns` — and **no
  project, agent or candidate listing**.
- No `HierarchyStore`, `DiscoveryStore`, `ListStore`, `Database` or generic
  query interface exists; asserted by scanning the package's exported
  interface declarations.
- `ControlPlane` composes both capabilities and exposes one browsing surface; a
  test drives all four through the service with a `ControlStore` and an
  `EvaluationStore` that are distinct objects, proving neither is asked for the
  other's entities.
- Both backends satisfy both interfaces, by compile-time assertion.

### Migration, v4 → v5, on both backends

- A v4 database migrates to v5 and stamps **5**.
- **Exactly three indexes** are added, named as specified.
- **No table, no column, no data change**: every existing row is byte-identical
  afterwards, asserted field by field across the control and evaluation
  entities.
- No synthetic project, agent, candidate, run or promotion is created.
- A v5 database reopens and verifies rather than re-migrating.
- A binary that understands only v4 refuses a v5 database, by the existing
  fail-closed version rule.
- The full chain v1 → v2 → v3 → v4 → v5 succeeds, and everything held at each
  step survives.

### Companion demo integration

- The acceptance journey references `trustvian/trustvian-python-agent-demo`;
  no duplicate Python agent exists in this repository, asserted by a scan for
  `.py` files outside any vendored path.
- Against that demo, the reference run's observed behavior set includes
  `ollama.localhost`, `crm.localhost`, `knowledge.localhost` and
  `mail.localhost`, and the candidate additionally includes
  `export.localhost`.
- `export.localhost` renders as **NEW** in the candidate, because it is absent
  from the reference evidence.
- **No test asserts an action ordering.** The assertions are over the observed
  set; a test that pinned `export` before `mail` would be testing the model.
- The graph's animation order matches the arrival order of the frames actually
  received.

### Security and boundary

- Arbitrary API text cannot become HTML — the existing escaping assertion,
  extended to every new rendered field including SVG text.
- A distinctive prompt, completion, tool argument and attribute value is
  injected upstream and asserted **never** to appear in any WebUI-reachable
  payload.
- `prefers-reduced-motion` disables movement while preserving every piece of
  information as text or badge.
- The WebUI performs **no** gate, diff, scorecard or policy arithmetic — the
  existing scan, extended to the new modules.
- `platform/webui.NewHandler` still takes no arguments; no import gives the
  WebUI control-plane authority.
- No `internal/*` core import enters the platform or the WebUI.
- **No frontend dependency, package manifest or build step exists** — asserted
  by scanning for `package.json`, a lockfile, a bundler config and any
  non-`self` script origin.
- The CSP constant is unchanged.

## Benchmarks

None required. The bounded local topology makes rendering cost a non-question,
and the platform-side additions are indexed primary-key range scans identical
in shape to one task 065 already ships. If a collection route ever needs a
benchmark, that is evidence the bound is wrong, not that the benchmark was
missing.

## Documentation

Written by the implementation PR, not before:

| File | Change |
|---|---|
| `docs/webui.md` | Replace "Navigating by ID" with the Live view as the default; keep ID navigation documented as the Manage surface |
| `docs/local-development.md` | The journey: `make local`, run an agent, open the browser, see behavior |
| `docs/compatibility.md` | Four new route rows and one paging contract row — **only once they exist** |
| `docs/ARCHITECTURE.md` | The collection capability and **where each half lives**: `ControlStore` owns the Project, Agent and Candidate collections; `EvaluationStore` owns the EvaluationRun collection; `ControlPlane` composes them into one browsing surface |
| `docs/compatibility.md` | also `SchemaVersion` 5 |
| `docs/ROADMAP.md` | 074 implemented |
| `docs/tasks/v1.0/074-…md` | Status → specified and implemented |
| `docs/tasks/v1.0/README.md` | The 074 row |
| `CHANGELOG.md` | An Unreleased entry |

**Documentation honesty is a requirement of this task, not a courtesy.**
`docs/webui.md` describes ID-based navigation because that is what ships today.
It must keep doing so until the implementation lands; this specification adds
only a clearly-labelled forward reference.

### ADR

An ADR is warranted for the collection capability — the repository deferred it
twice, and the reasoning for finally adding it is exactly the "why did we do
this?" a future developer will ask. It should record:

1. **Why a collection capability exists now**, after two deliberate deferrals:
   a browser that reloads with no live traffic has no other honest way to find
   what exists, and the alternatives — browser storage, direct database access,
   pretended replay — are each refused for their own reason.
2. **Why `id` byte order and not time**: timestamps are stored as
   `RFC3339Nano` text, which is not lexically ordered, so a timestamp cursor
   would skip and repeat rows. Time-ordered history is task 067's.
3. **Why the capability split is preserved**: Project, Agent and Candidate
   collections on `ControlStore`; the EvaluationRun collection on
   `EvaluationStore`; composed by `ControlPlane`. A list method does not change
   which capability owns an entity.
4. **Why the graph is scoped to one run**: `fingerprint_id` is not globally
   unique by design, and merging observations across runs would let one run's
   decision and new-behavior state overwrite another's.
5. **Why discovery is bounded as a workflow, not only per route**: a bounded
   page with an automatic recursive crawl is an unbounded client, and under
   live traffic it degenerates into a resync loop. One startup request, zero
   automatic continuations, lazy user-triggered descent.

The number is whatever is free when the implementation lands; this PR creates
none, matching the convention tasks 065 and 066 followed.

## Acceptance criteria

1. With Trustvian running and an evaluation producing OTel-derived records,
   opening `/` shows the active Agent and Run **without the user typing an
   identifier**.
2. Each realtime observation appears in a bounded feed and updates a bounded,
   **run-scoped** behavior-flow visualization.
3. A newly observed fingerprint is visibly identified as new, by text or icon
   and not colour alone.
4. The UI shows factual operation and target information **only at the
   semantic fidelity the observation carries** — no inferred tool or function
   names.
5. The graph communicates activity without relying on colour alone and honours
   `prefers-reduced-motion`.
6. Refreshing the browser rediscovers the durable hierarchy through bounded
   authoritative routes; **browser storage is unnecessary and unused**.
7. Realtime remains ephemeral; no event history is implied, retained or
   replayed.
8. Existing manual management surfaces remain reachable and are not required
   for ordinary observation.
9. All authority for evaluation, diff, scorecard, gate and lifecycle remains
   server-side.
10. The same WebUI works against SQLite and PostgreSQL, with no page able to
    tell which answered.
11. `GET /v1/realtime` compatibility is preserved; every change is additive.
12. No prompt, completion, tool argument or raw arbitrary payload is added to
    any WebUI-reachable contract.
13. The journey is demonstrated with
    **`trustvian/trustvian-python-agent-demo`**, whose README stops
    instructing the user to type `run-reference` and `run-candidate`. That
    change is a follow-up PR in the companion repository, after this one.
14. [Task 072](README.md) cannot declare the local developer platform ready
    until this journey is demonstrable.

### Every question this task owns, answered

| Question | Answer |
|---|---|
| What does zero-input mean? | **Discovery, not provisioning.** The browser creates nothing durable, ever |
| Which store lists Projects, Agents and Candidates? | **`ControlStore`** — `Projects`, `ProjectAgents`, `AgentCandidates` |
| Which store lists EvaluationRuns? | **`EvaluationStore`** — `CandidateEvaluationRuns`. A list method does not move an entity between capabilities |
| Is a new store interface added? | **No.** No `HierarchyStore`, `DiscoveryStore`, `ListStore`, `Database` or generic query capability |
| Who composes the two? | `ControlPlane`, which already holds both. The HTTP adapter never learns they differ |
| Is `fingerprint_id` globally unique across runs? | **No.** It is behavioral identity and carries no project, agent, candidate, run or actor |
| What is graph identity, then? | The graph is **scoped to one selected run**, so `fingerprint_id` keys an edge inside it. A future combined graph would need `(run_id, fingerprint_id)` as a structured tuple |
| Can one run's state affect another's edge? | **No.** Observations outside the selected run update their card and are not drawn |
| Does the initial Live load walk the hierarchy? | **No** |
| How many collection requests does startup make? | **One** — a single page of `GET /v1/projects`. The same on every reconnect |
| How many continuations does it follow automatically? | **Zero**, at every level |
| How are descendants loaded? | **Lazily, one bounded page per user action.** Never on a timer |
| What if a page has more? | An explicit affordance, and an explicit statement that more exist. A page is never implied to be the whole hierarchy |
| Does task 074 create another Python/Ollama demo? | **No.** It integrates with the existing companion repository |
| Which demo proves the journey? | **`trustvian/trustvian-python-agent-demo`**, updated in its own follow-up PR |
| Is the model's action ordering fixed? | **No.** The observed behavior *set* is validated; the order is the model's and the graph animates whatever actually arrived |
| What makes `export.localhost` NEW? | It is present in the candidate's behavior and absent from the reference evidence |
| What schema migration does task 074 own? | **4 → 5**, after task 066's implementation, which owns 3 → 4 |
| What is in that migration? | Three indexes, a version stamp, and nothing else — no table, column, backfill or data rewrite |
| Does task 074 retain event history? | **No.** Task 067 keeps it |
| Does the WebUI gain authority? | **No.** `webui.NewHandler()` still takes no arguments |

## Open questions left to implementation

Deliberately left open because they are implementation judgement, not product
intent. Each has a stated default so nothing is blocked:

1. **Graph layout.** A fixed radial or left-to-right layout is assumed. Any
   layout is acceptable that stays legible at the stated bounds and needs no
   dependency.
2. **Scope-card ordering.** Most-recently-active first is assumed; the
   alternative is stable identifier order, which flickers less but buries the
   thing the developer is watching.
3. **Which scope is selected by default** when several are active. Most
   recently active is assumed, and selection must never change under the
   developer while they are reading — a newly active scope raises its card, not
   the graph.
4. **Whether `GET /v1/projects` should accept a name filter.** Assumed no —
   filtering is a capability with its own design, and nothing in this journey
   needs it.

Three questions that *were* open here are now decided in the body and are no
longer implementation choices: the graph's scope model (one selected run), the
startup request budget (one page, zero continuations), and the schema version
(4 → 5, after task 066).
