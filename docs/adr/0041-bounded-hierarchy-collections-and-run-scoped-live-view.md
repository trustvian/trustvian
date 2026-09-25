# 0041 — Bounded hierarchy collections and a run-scoped live view

**Status:** Accepted

## Context

The WebUI opened on a form with four text inputs — project, agent, candidate,
evaluation run — and `docs/webui.md` stated the position plainly: "There is no
list and no search." A developer whose agent was already producing telemetry
had no way to ask *what is happening right now*. They had to find an identifier
a producer had chosen, read it out of a log, and type it into a browser.

Everything needed to answer that question was already on the wire.
`GET /v1/realtime` accepts `project_id`, `agent_id` and `run_id` filters, and
`RealtimeFilter.matches` treats an empty value as unconstrained on every
dimension — so an unfiltered subscription already received all local activity,
and every frame already carried a complete `RealtimeScope`. What was missing
was a consumer that used it, and an honest answer for the other half of the
problem: a browser that reloads when *nothing* is happening receives no frames
at all and must still find what exists.

[Task 074](../tasks/v1.0/074-zero-input-live-behavior-webui.md) is that
consumer. The `v1.0` Track B gate begins "run locally → observe behavior live",
and until now the second step required reading an identifier out of a
producer's logs — which is not a journey "by a developer who has not read the
source".

## Decision

### 1. A bounded collection capability exists now, after two deliberate deferrals

[Task 063](../tasks/v1.0/063-minimal-web-control-plane.md) shipped no
collection route on purpose: "pagination, sort order, cursor semantics and
scoping have not been designed, and `/v1` route shapes are a stable contract
once published." [Task 065](../tasks/v1.0/065-environment-model.md) designed
exactly those four things, for one entity, because it had a concrete consumer —
a caller cannot open an environment by ref when knowing the refs is the
question. [Task 066](0040-promotions-are-immutable-evidence-backed-platform-decisions.md)
reused that design unchanged for promotions.

What was still missing was a consumer for the rest of the hierarchy. It now
exists, and the alternatives were each refused on their own grounds:

| Alternative | Why it was refused |
|---|---|
| `localStorage` | Browser state is not authority, and [ADR 0036](0036-webui-is-a-same-origin-adapter-over-v1.md) forbids storing platform state in the browser |
| Give the WebUI database access | It is a static same-origin client; handing it a database removes the entire boundary |
| Pretend realtime replays | `stream_ready` publishes `replay_available: false`. [ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md) is explicit |
| Poll the database on a timer | Not a discovery design. It scales by accident rather than by contract |

Four routes, following the entity graph the domain already makes immutable:

```text
GET /v1/projects?limit=&after=
GET /v1/projects/{project_id}/agents?limit=&after=
GET /v1/agents/{agent_id}/candidates?limit=&after=
GET /v1/candidates/{candidate_id}/evaluation-runs?limit=&after=
```

`GET /v1/projects` is the one unscoped collection, and it is bounded like every
other. It is the root of a hierarchy; without it there is no entry point that
does not require prior knowledge. `GET /v1/agents`, `GET /v1/candidates` and
`GET /v1/evaluation-runs` remain absent, because each would be an unscoped
listing of an entity whose natural scope is its parent.

### 2. Cursors are immutable identifiers in byte order, never timestamps

Every collection orders by `id`, byte-ascending, with an exclusive `after`
cursor.

A timestamp cursor is the obvious alternative and it is unsound here. This
schema stores timestamps as `RFC3339Nano` **text**, which is not lexically
ordered: Go trims trailing fractional zeros, and a non-UTC offset breaks the
ordering again. A cursor over that column would silently skip and repeat rows —
the worst failure mode available, because the traversal still terminates and
still looks correct.

`id` is immutable, unique, already validated and byte-ordered on both backends.
Immutability is what makes keyset pagination safe: nothing already returned can
move between pages, so a row created mid-traversal appears if and only if it
sorts after the caller's position. That is a consistent *position*, not a
consistent snapshot, and it is the honest guarantee.

Ordering by a mutable column would be worse still: an agent rename or an
environment re-rank would move a row between pages while somebody was reading
them. Task 065 chose `ref` over `rank` for this reason and this follows it.

Newest-first ordering over an unbounded history remains
[task 067](../tasks/v1.0/README.md)'s subject. For the Live view it costs
nothing: activity discovery comes from realtime, where events arrive in the
order they happened.

### 3. The store capability split is preserved, not collapsed for browsing

```go
// ControlStore
Projects(ctx, after ProjectID, limit int) ([]Project, error)
ProjectAgents(ctx, projectID ProjectID, after AgentID, limit int) ([]Agent, error)
AgentCandidates(ctx, agentID AgentID, after CandidateID, limit int) ([]Candidate, error)

// EvaluationStore
CandidateEvaluationRuns(ctx, candidateID CandidateID, after EvaluationRunID, limit int) ([]EvaluationRun, error)
```

Browsing a hierarchy is convenient to implement as one flat interface, and that
convenience is what this refuses. [Task 057](../tasks/v1.0/057-local-platform-persistence.md)
split the two capabilities because "the two have different lifetimes, write
patterns and consumers … a later backend may reasonably implement one and not
the other". **A list method is a read, and a read does not move an entity
between capabilities.** Putting `CandidateEvaluationRuns` on `ControlStore`
would oblige a backend implementing only the control capability to serve
evaluation runs it does not store.

`ControlPlane` already holds both, so it composes them into one browsing
surface and the HTTP adapter never learns that the last one came from a
different capability. The adapter should not care; the service boundary must.

**No new interface.** No `HierarchyStore`, `DiscoveryStore`, `ListStore`,
`TraceStore`, `WebUIStore` or `Database`. A fifth interface with one
implementation pair and one consumer is the abstraction CLAUDE.md says not to
build ahead of need, and once it exists everything else gets hung off it.

### 4. The behavior graph renders exactly one selected run

This is the decision most likely to be revisited, so the reasoning is recorded
rather than the conclusion alone.

`fingerprint_id` is **not globally unique**, by design. A fingerprint
identifies the behavioral shape derived from `StableFeatures` and deliberately
carries no `ProjectID`, `AgentID`, `CandidateID`, `EvaluationRunID` or
`ActorID` — [task 051](../tasks/v1.0/051-behavioral-profile-learning-scope-isolation.md)
was explicit that run and candidate metadata must never become fingerprint
dimensions. So two unrelated agents doing the same thing produce the same
fingerprint:

```text
project-a / agent-a / run-1    POST → ollama.localhost    fingerprint abc
project-b / agent-b / run-2    POST → ollama.localhost    fingerprint abc
```

Those are one behavior and **two observations**. They may legitimately disagree
about `new_behavior`, `decision`, `risk_level`, `trust_score`, `anomaly_score`
and `anomaly_confidence`, because every one of those is contextual to the run
that produced it. Merging them into one edge would let run A's
`new_behavior: true` overwrite run B's state and report one agent's decision as
another's — the same mistake at the UI layer that task 065 closed at the
comparison layer.

So the model is:

```text
unfiltered stream
        ↓
bounded active scope cards          ← every active run, from RealtimeScope
        ↓
one selected card
        ↓
one run-scoped graph                ← only that run's observations
```

Within a run-scoped graph `fingerprint_id` *is* a sufficient edge key, because
the run is already fixed. An observation whose scope is not the selected run
updates its card and is not drawn. Selecting a different card discards the
previous topology rather than keeping it alongside.

Scope identity is a structured key over all six dimensions — project, agent,
candidate, run, environment, behavioral profile — serialized as JSON with a
fixed field order, never a delimiter join. `a|b` and `a|b` are the same string
whether the parts were `("a","b")` or `("a|b","")`, and identifiers here may
contain almost any printable character.

If a future task wants a combined multi-run graph, every entity in it must be
keyed by a structured tuple — `(run_id, fingerprint_id)` for an edge,
`(project_id, agent_id)` for a source node — and never by concatenated strings.

### 5. Discovery is bounded as a workflow, not only per route

Each route caps a page at 64. **That alone does not bound discovery**, and a
naïve hierarchy walk is how a bounded API becomes an unbounded client:

```text
64 projects × 64 agents × 64 candidates × 64 runs  =  16,777,216 rows
```

across a quarter of a million requests, during which the resync buffer holds
**64** frames. Under live traffic that buffer overflows, the client abandons
and resynchronizes, and the crawl starts again — a resync loop that gets worse
the larger the database is, which is precisely backwards.

So the workflow is bounded explicitly:

```text
automatic project pages at startup      1
automatic child traversal               0
automatic continuation following        0
```

**Startup performs exactly one collection request**: the first page of
`GET /v1/projects`. Not one per project, not one per level, not one per
continuation token. The same budget applies to **every reconnect**, so a
flapping connection cannot amplify into a crawl.

That is enough for the journey this task exists for, because realtime answers
*what is active now* with no API read at all: a frame creates or updates a
scope card from its own `RealtimeScope`. The collection routes answer the
different question *what exists*, which only matters on a reload with no
traffic.

Descent is lazy and user-triggered: one click, one bounded page. A continuation
renders as an explicit affordance and is never followed automatically. There is
no timer, no refresh loop and no interval anywhere in the bundle.

### 6. Discovery is not provisioning

Stated here because it is the line this work is most likely to be pushed
across later.

This capability **discovers and presents** platform state that already exists.
It does not create any. OTLP traffic does not create a Project, `service.name`
does not become an Agent, the browser does not create a Candidate, and no SSE
frame causes a durable write. The local workflow still requires a producer or
runtime to establish the evaluation hierarchy before telemetry can be
attributed to it — the Collector's `evaluation:` block names an existing
`run_id`, and [task 073](../tasks/v1.0/073-otel-collector-evaluation-ingest.md)
refuses to start ingest unless that run is running.

What changed is only whether a *human* has to retype those identifiers into a
browser to see the result.

Zero-configuration auto-provisioning — a runtime that creates a project, agent,
candidate and run on first telemetry — is a separate milestone with its own
trust, naming and lifecycle design. It is named here so it cannot be smuggled
in as "part of making the UI zero-input".

### 7. Realtime remains ephemeral and non-authoritative

`GET /v1/realtime` is unchanged: same route, same three filters, same event
kinds, same payload shape. The Live view subscribes with none of the filters
set, which the server already defines as unconstrained.

Nothing about the view treats the stream as authority. Cards and graph are
cleared on every connect, because both are statements about the current stream
and a graph that survived a reconnect would assert a continuity `stream_ready`
explicitly denies. No count displayed as authoritative comes from a frame — a
card's count is labelled *seen live*, and an authoritative one comes from
`/v1`. No historical animation is replayed, and no past flow is reconstructed
from aggregates. Resync buffer overflow still abandons and resynchronizes
rather than dropping a frame.

Three different kinds of limit are kept distinguishable, because conflating
them would misreport the platform:

| Kind | Meaning when exceeded |
|---|---|
| **visualization bound** | the browser chose not to draw everything — say which bound and how much |
| **behavior evidence bound** | the *run's evidence* saturated at 512 distinct behaviors; `behavior_complete: false` already reports it |
| **realtime queue bound** | a notification would be lost — abandon and resync, never drop |

### 8. `SchemaVersion` moves 4 → 5, and the migration adds only indexes

Three indexes, because three of the four scans are not free from a primary key:

```sql
CREATE INDEX platform_agents_by_project   ON platform_agents          (project_id, id);
CREATE INDEX platform_candidates_by_agent ON platform_candidates      (agent_id, id);
CREATE INDEX platform_runs_by_candidate   ON platform_evaluation_runs (candidate_id, id);
```

`platform_projects` needs none: `WHERE id > ? ORDER BY id LIMIT ?` is a range
scan along its primary key.

The migration's entire content is those three statements and a version stamp.
No table, no column, no backfill, no data rewrite and no synthesized entity — a
schema change is not a source of platform state. This makes v5 the first
migration in this schema where v4 and v5 hold the *same tables*, so the two are
distinguishable only by the stamped version, which both backends' dispatch now
accounts for.

The version was pinned in the specification rather than left to merge order:
task 066 is a merged specification that pins 3 → 4, the roadmap sequences 066
before 074, and two merged specifications must not both claim "the next number"
and resolve it by whichever implementation lands first.

## Alternatives considered

**One `GET /v1/hierarchy` returning the tree.** Refused. It is unbounded by
construction, it freezes a shape nobody designed, and it answers a question
("everything") that no consumer asks.

**`limit+1` lookahead for continuation.** Refused, and this is a repeat of a
correction task 066 already made: fetching `limit+1` requires the store to
accept 65, so a public contract saying "at most 64 per page" would mean 65,
with an off-by-one that exists only because one transport wanted it. A full
page is resolved by a second bounded `limit=1` probe — one indexed single-row
lookup, once per full page.

**A generic pagination abstraction in the browser.** Refused. Four small
explicit helpers are clearer than a framework over four call sites, and a
generic pager makes an unbounded traversal one convenience method away.

**A visualization library for the graph.** Refused. The topology is bounded at
a few dozen nodes by construction, far below where a dependency earns itself,
and ADR 0036's no-build-step property is worth more here than any layout
algorithm. A future task wanting force-directed layout over hundreds of nodes
may revisit this with a measured requirement rather than a preference.

**Adding `Direction` to the graph.** Refused, and it was in the original brief.
`event.Operation` carries an `OperationDirection`, but `StableFeatures` — the
behavioral identity that reaches `RealtimeObservation.Behavior` — does not.
Adding a field to `StableFeatures` changes behavioral identity, which would be
a core change made for a visualization. The graph renders the six dimensions it
actually has.

## Consequences

The WebUI's default view is Live, and the ID-driven forms move to a secondary
Manage surface. Nothing is removed: create, open, drive a lifecycle, compare
and promote all remain reachable, because they are the right tools for
debugging. They stop being the front door.

`SchemaVersion` is **5** on both backends. A v5 database opened by a v4 binary
is refused, as every version step here is.

`docs/compatibility.md` gains four route rows and one paging contract row. The
contract is purely additive: no existing route changes shape, no field changes
meaning, and `GET /v1/realtime` is not modified at all.

The CLI does not gain list commands in this task. The routes are a platform
capability rather than a WebUI endpoint, so a future CLI or TUI may use them
equally — but adding a command is its own decision with its own output-format
questions, and nothing here needs one.

The companion repository `trustvian/trustvian-python-agent-demo` can stop
instructing its user to type `run-reference` and `run-candidate` into the
browser. That is a follow-up change in that repository, after this one, because
until this lands its README would document something that does not work.
