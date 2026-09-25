# 074 — Zero-Input Live Behavior WebUI

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [059](059-realtime-infrastructure.md),
[063](063-minimal-web-control-plane.md),
[065](065-environment-model.md),
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

**There is no Python/Ollama reference demo in the repository.** `make demo`
runs `trustvian analyze` against a bundled JSON fixture; it starts no agent and
produces no live telemetry. No `.py` file exists anywhere in the tree. The
acceptance journey below is therefore written against what exists — `make
local` plus the task 073 Collector processor — and the reference demo is
listed as a **deliverable of this task's implementation**, not as an existing
thing to point at. See [Acceptance criteria](#acceptance-criteria) item 13.

## User journey

The journey this task must make true, with nothing copied into a browser
field:

```text
Terminal A                          Terminal B
$ make local                        $ <run the local agent>
  Web: http://127.0.0.1:<port>/       telemetry flows through the
                                      Collector into /v1

Browser  →  http://127.0.0.1:<port>/

  ● Trustvian connected
  ● Telemetry flowing

  support-agent appears automatically
        ↓  model call pulses          Agent ──POST──▶ ollama.localhost
        ↓  CRM call pulses            Agent ──GET───▶ crm.localhost
        ↓  knowledge call pulses      Agent ──GET───▶ knowledge.localhost
        ↓  export appears as NEW      Agent ──POST──▶ export.localhost  NEW
        ↓  mail call pulses           Agent ──POST──▶ mail.localhost
        ↓  run completes
        ↓  the behavioral summary stays reachable from authoritative state
```

At no point does the developer type `support-demo`, `support-agent` or
`run-candidate` into the page.

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
| Limit | `1..64`, defaulting to 64; a larger value is `400 invalid_request`, never a silent clamp |
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
receive, so **`SchemaVersion` advances by one step**, with a migration that
creates the three indexes and stamps the new version. This specification
deliberately does not pin the integer: task 066 also moves the counter, and
whichever lands second takes the next number. What is pinned is the migration's
*content* — three indexes, no table, no column, no backfill, no data change.

`ControlStore` gains four list methods, each mirroring
`ProjectEnvironments`'s signature and error contract, with compile-time
assertions on both backends. No update, no delete, no generic query capability.

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
the **first bounded page** of the project hierarchy — and it is a snapshot of
*what exists*, not of *what is active*. Activity is realtime's answer, and the
page must say which is which.

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

### Edge identity

An edge is identified by the observation's **`fingerprint_id`**, which the
platform already computes and already publishes. The browser does not derive
identity from the descriptor fields, because two behaviors that render
identically may be distinct fingerprints and the server is the only thing
entitled to say so.

A repeat observation of a known fingerprint updates that edge. A first
observation of a fingerprint creates one.

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

### Graph correctness

- One observation updates **exactly one** edge, identified by `fingerprint_id`,
  and leaves every other edge untouched.
- `new_behavior: true` renders a distinct, labelled state — not colour alone.
- A repeated fingerprint updates rather than duplicating an edge.
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
- Migration adds three indexes, no table, no column and **no data change**;
  an existing database's contents are byte-identical afterwards.

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
| `docs/ARCHITECTURE.md` | The collection capability and its place in `ControlStore` |
| `docs/ROADMAP.md` | 074 implemented |
| `docs/tasks/v1.0/074-…md` | Status → specified and implemented |
| `docs/tasks/v1.0/README.md` | The 074 row |
| `CHANGELOG.md` | An Unreleased entry |

**Documentation honesty is a requirement of this task, not a courtesy.**
`docs/webui.md` describes ID-based navigation because that is what ships today.
It must keep doing so until the implementation lands; this specification adds
only a clearly-labelled forward reference.

### ADR

An ADR is likely warranted for the collection capability — the repository has
deferred it twice, and the reasoning for finally adding it, plus why `id` and
not time, is exactly the "why did we do this?" a future developer will ask. The
number is whatever is free when the implementation lands; this PR creates none,
matching the convention tasks 065 and 066 followed.

## Acceptance criteria

1. With Trustvian running and an evaluation producing OTel-derived records,
   opening `/` shows the active Agent and Run **without the user typing an
   identifier**.
2. Each realtime observation appears in a bounded feed and updates a bounded
   behavior-flow visualization.
3. A newly observed fingerprint is visibly identified as new, by text or icon
   and not colour alone.
4. The UI shows factual operation and target information **only at the
   semantic fidelity the observation carries** — no inferred tool or function
   names.
5. The graph communicates activity without relying on colour alone and honours
   `prefers-reduced-motion`.
6. Refreshing the browser rediscovers the durable Project → Agent → Candidate →
   Run hierarchy through bounded authoritative routes; **browser storage is
   unnecessary and unused**.
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
13. A **reference local-agent demo exists** and demonstrates
    `model → CRM → knowledge → export (NEW) → mail` as live observed behavior
    without `run-candidate` being typed into the browser. No such demo exists
    on `main` today — `make demo` runs `analyze` against a fixture — so
    creating one is part of this task's implementation, not a precondition it
    can assume.
14. [Task 072](README.md) cannot declare the local developer platform ready
    until this journey is demonstrable.

## Open questions left to implementation

Deliberately left open because they are implementation judgement, not product
intent. Each has a stated default so nothing is blocked:

1. **Graph layout.** A fixed radial or left-to-right layout is assumed. Any
   layout is acceptable that stays legible at the stated bounds and needs no
   dependency.
2. **Scope-card ordering.** Most-recently-active first is assumed; the
   alternative is stable identifier order, which flickers less but buries the
   thing the developer is watching.
3. **Whether the Live snapshot reads more than the first project page.**
   Assumed no: one bounded page, with explicit "more exist" affordance.
4. **The exact `SchemaVersion` integer**, which depends on whether task 066
   lands first. The migration content is pinned; the number is not.
5. **Whether `GET /v1/projects` should accept a name filter.** Assumed no —
   filtering is a capability with its own design, and nothing in this journey
   needs it.
