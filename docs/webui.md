# Web interface

A live window into what an agent is doing. One command starts it; the URL is
printed.

Trustvian's browser surface is an **observability cockpit first and a control
plane second**. Opening it shows the agents that are working right now, what
they are touching, and what Trustvian decided about each call. The
control-plane forms still exist — creating entities, driving a run's lifecycle,
opening something by an identifier — but they are a secondary surface called
**Manage**, and watching an agent never requires them.

It is laid out as an **admin console**: a persistent sidebar of destinations, a
workspace whose primary surface is a table of real records, and a contextual
panel that opens beside the table when a row is selected. Records are how you
move around it. Every project, run, observation and behavior on screen is a row
you can click, and every identifier — a run, a trace, a session, a behavioral
fingerprint — renders as a control that goes somewhere.

**You never have to type an identifier to reach anything.** Discovering a run,
inspecting an observation, following its trace or session, choosing two runs to
compare and opening the evidence behind a gate check are all reachable by
clicking what is on screen. The identifier fields that remain are for the case
where you already have one, from a log or a CI job.

## Quick start

```bash
make local
```

```text
Trustvian local runtime
API:   http://127.0.0.1:54321
Web:   http://127.0.0.1:54321/
State: .trustvian/platform.db
```

Open the `Web:` URL. No browser is launched for you, and there is no `--open`
flag — a security tool that opens windows by itself is a surprise.

**Nothing needs to be typed.** The page subscribes to all local activity and
reads the pipeline status once. If your agent is running, it opens on **Live**
and the agent is already on the screen. If nothing is arriving, it opens on
**Status**, which says why and what to check (see
[the Status view](#the-status-view)). The control plane decides which: the
browser reads the answer and holds no rule of its own.

The API and the WebUI are the **same endpoint**. The UI is served from the same
loopback listener that answers `/v1`, which is why `runtime.json` still carries
one URL and needs no second field.

## What it can do

| Destination | Purpose |
|---|---|
| **Live** *(landing view when something is active)* | Watch. Active agents appear by themselves, one run's behavior flow animates as calls arrive, and an inspector shows what Trustvian decided |
| **Status** *(landing view when nothing is)* | Diagnose. Which Collectors are reporting, which producers they have seen and how, which model calls the telemetry named, how many spans arrived only as HTTP, how each span's actor was bound, and named suggestions for what to check |
| **Overview** | Orient. Live activity, the newest runs of a project, agent or candidate by status, one run's authoritative evidence, recent gate verdicts and the project's environments — each saying what it covers and when it was read |
| **Projects** | Choose. A searchable table of what exists; picking a row scopes the whole console and the sidebar says which project that is |
| **Runs** | Explore. A visible list of the project's agents and their candidates, then the run table itself. Clicking a run opens its workspace |
| **Traces** | Investigate. A run's traces as a searchable list, one trace's evaluated actions as a waterfall, and one action's details beside it |
| **Compare** | Measure. Assign two runs from a table as reference and candidate, then read the server's gate, diff and scorecard |
| **Evidence** | Explain. Follow a gate check or a behavioral delta to the observations behind it, and read one run's retained session, trace, sequence and timeline |
| **Promotions** | Decide. Record a promotion decision and page the scoped project's history |
| **Manage** | Administer. Create projects, agents, candidates and runs; drive a run's lifecycle; open anything by identifier |

Every destination is backed by a capability `/v1` serves. There is no entry for
work that does not exist yet: an empty destination teaches a reader that the
product is thinner than it is.

What it deliberately cannot do: ingest decision records (that is the job of the
application under evaluation, through the CLI or the API), or manage
environments (task 065).

## The Status view

**Status** answers *why is Live empty?* — or, when it isn't, *what is the
pipeline actually receiving?* It renders `GET /v1/status`
([task 105](tasks/v0.12/105-pipeline-status-surface.md)) and nothing else, the
same document `trustvian status` and `trustvian dev --check` print.

| Section | What it shows |
|---|---|
| Suggestions | The outputs of a named rule table over the evidence on the page, each with its rule, rule version and the evidence it read ([ADR 0064](adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md)) |
| Collector | Reporting or stale, last report, start time, OTLP receivers, the evaluation run it feeds, and what became of every span it was handed |
| Producers | Each `service.name` it has seen, its instrumentation scopes, its `telemetry.sdk.*` values, its span count and its last span |
| Model calls | Each model and provider the telemetry named, with a call count — display metadata, never identity |
| Fidelity | How many evaluated spans a convention named, how many were transport only, and per named HTTP target how many distinct operations reached it at transport fidelity |
| Actors and learning | Which link of the actor chain bound each span — or neither, in which case it was never evaluated — and what the engine's `Observe` reported, by the decision it followed |
| Engine | *Unavailable*, with the reason: the engine exposes no statistics accessor, so baseline count, maturity and fingerprint admission are not shown |

**It is the landing view only when nothing is active.** The page makes one
status read at startup; the document's `landing` field — `live` when a
Collector is reporting and has seen a producer within 30 seconds, or when the
control plane committed an ingest record within 30 seconds (`last_ingest_at`),
`status` otherwise — decides where it opens. A page whose reader has already moved is
left where it is. When spans start arriving while Status is open, a line at the
top says so and offers **Open Live**; the page does not move by itself.

**It stays current without polling.** The control plane publishes a
`status_changed` event on the realtime stream when a Collector's report changes
what a reader would see; an idle Collector reporting every ten seconds publishes
nothing. Status re-reads when it is on screen and otherwise re-reads on the next
visit. **Refresh** reads again; a reconnect of the stream reads again. A
Collector that stops reporting becomes *stale* on the next read — the page
prints when it was read.

**It computes nothing and claims nothing.** Every figure is a field of the
document. A section a Collector did not report says *not reported by this
Collector*, never `0`. Status is held in memory by the control plane, so a
restart forgets it, and the page says how long its view covers. There is no
health score, no traffic light and no history. Operation names at transport
fidelity are counted and never shown, because a span name can carry a URL path
or a query.

Every bound is named at the foot of the page: 16 Collectors; per Collector 64
producers, 64 models and 64 transport targets; 16 scopes per producer; 32
distinct operations counted per HTTP target; 64 suggestions; reporting within 30 s;
forgotten after 300 s.

## The Overview

**Overview** answers *where should I look first?* from records `/v1` already
serves. Choose a project, agent and candidate at the top — or arrive with them
already chosen from any other destination — and five panels summarise them:

| Panel | Reads | Says it covers |
|---|---|---|
| Live now | the Live connection's scope cards, for the project | this connection since it last synchronized |
| Runs | the newest runs of the deepest level chosen — project, agent or candidate | every run newest first, or the newest page when more exist; **Load next page** reads on |
| Newest run / Chosen run | that run's progress and first page of behaviors | one run; the newest by creation time unless you chose one |
| Gate verdicts | the first page of the project's promotion decisions | identifier order, not newest first |
| Environments | the project's whole environment collection | every environment |

Every panel prints when it was **read** as a clock time, has its own loading,
empty and failed state, and fails alone: an unreachable control plane shows in
the panels that needed it and leaves the rest standing. Nothing is re-read on a
timer. **Refresh** reads every panel again; changing the context re-reads what
the change affects.

Everything links to the record behind it. A run's chip chooses it for the
evidence panel and **Open** goes to its workspace; **Reference** and
**Candidate** assign it as a side in Compare, which is then ready with both
runs chosen; a verdict's **Open** opens its decision; a live card's **Watch**
selects it in Live.

**Charts are a second rendering of printed numbers.** The status bar counts the
rows on the runs page and says so. The behavior bars are each behavior's
authoritative observation count, ordered within the first page. Counters stay
decimal strings: a bar's length is drawn from the leading digits, never by
converting the counter to a number, and the exact figure is printed beside it.
A figure the server did not return reads *not available*, never `0`.

**Runs are newest first across a scope** (task 101,
[ADR 0063](adr/0063-recency-is-a-stored-sort-key-and-a-composite-cursor.md)).
A project alone is enough: the panel reads
`GET /v1/projects/{id}/evaluation-runs/recent`, narrowed to the agent and the
candidate when they are chosen, and its heading names the scope. The order is
the server's — by stored creation time, ties by identifier — so the first row
is the newest in the whole scope, not the newest of an identifier-ordered page.
A run created after the read is not on screen until **Refresh**; the read time
says how old the list is. Pinning a run for the evidence panel is local to the
Overview and does not narrow the page to that run's candidate.

What the Overview does not show, and says so on the page: a decision or risk
distribution across a run (only a bounded page of retained history exists),
trends, and any combined health score.

## The Live Observatory

```text
open /
  → already connected
  → active agents appear by themselves
  → the newest is selected and its behavior flow animates
  → click a behavior to inspect what Trustvian decided
```

The layout is four regions:

```text
┌───────────────────────────────────────────────────────────────┐
│ Trustvian  ● LIVE   support-agent · local   127 obs · 5 behav. │
├──────────────┬───────────────────────────────┬────────────────┤
│ Active now   │ Behavior flow                 │ Inspector      │
│ ● support-   │                               │                │
│   agent      │   support-agent ──POST──▶ …   │ POST           │
│ ○ invoice-   │                    ──GET───▶ … │ export.local…  │
│   agent      │                               │ NEW BEHAVIOR   │
├──────────────┴───────────────────────────────┴────────────────┤
│ Live stream · current connection                              │
└───────────────────────────────────────────────────────────────┘
```

On a narrow screen these stack in reading order rather than compressing into
three unreadable columns.

**The header is operational.** A connection chip — `LIVE`, `SYNCING`,
`RECONNECTING`, `DISCONNECTED` — the agent being watched, and the run's
observation and distinct-behavior counts. Those counts are read from
`GET /v1/evaluation-runs/{id}/progress`, never accumulated from the stream: a
count derived from frames would drift the moment one was dropped, and the
stream's own bound makes dropping possible by design. A card's own number is
labelled *seen live* and is a frame count, which is a different fact.

The page subscribes to `GET /v1/realtime` with no filter. An empty filter is
unconstrained on every dimension — that is what the server already means by it
— so one subscription receives all local activity, and every frame carries the
project, agent, candidate, run, environment and behavioral profile it belongs
to. A card appears for each, with no database read per event and no identifier
typed by anyone.

**Live activity is a current viewport, not event history.** The stream keeps
nothing and replays nothing, so a reconnect starts again from what is happening
then. Task 067 owns retained history and **Evidence** is where you read it;
this view owns what is happening now.

**Hierarchy collections are durable discovery.** They answer *what exists*,
which is a different question from *what is active*, and they are what a reload
with no live traffic falls back on. The page never confuses the two: a card is
activity, a hierarchy row is durable state.

### Selection: following, or pinned

The most recently active run is selected and drawn. When you click a different
card, that choice is **pinned** — activity elsewhere raises its own card and
never takes the graph you are reading. The rail says which mode it is in, in
words, and offers **Follow active** to go back.

This state is ephemeral. A reload starts following again, which is correct:
nothing about which run somebody was reading is platform state.

### One run at a time, on purpose

The graph draws exactly one selected run. A fingerprint identifies a behavioral
shape and carries no project, agent, candidate or run — so two unrelated agents
doing the same thing produce the same fingerprint. Drawing them together would
let one run's decision, risk and new-behavior state overwrite another's.

Activity in a run you are not watching updates its card and is not drawn.
Selecting a different card draws that run and discards the previous topology.
Once you select something explicitly, a newly active run raises its own card
but never takes the graph you are reading.

### What the graph shows

```text
                 POST
support-agent ───────────▶ ollama.localhost
             ├─ GET  ────▶ crm.localhost
             ├─ GET  ────▶ knowledge.localhost
             ├─ POST ────▶ mail.localhost
             └─ POST ────▶ export.localhost   NEW
```

The source is the agent from the event's scope; the edge is the operation
category and name; the target is the target name and category. All six come
from `StableFeatures` as the observation carried them.

**No semantic names are invented.** If telemetry proves only
`POST → export.localhost`, that is exactly what is drawn — never
`export_customer`. Richer semantic fidelity is task 075's, and guessing one
here would assert something no evidence supports.

Each received observation produces **one** pulse along its edge, and the edge
brightens for the length of that traversal. Nothing animates without a frame
behind it: no ambient loop, no idle motion, no simulated packets, and no replay
after a reconnect. A quiet agent draws a still graph, which is the correct
picture.

A behavior the run had not shown before gets a persistent `NEW` badge on both
the edge and its target, a `NEW` column in the timeline, and — if you are not
already reading something else — the inspector opens it. `NEW` means new: it is
never labelled dangerous, malicious or unsafe, because those would be
judgements the server did not make.

### The inspector

Clicking an edge, a timeline row or a target opens the evidence for that
behavior:

```text
POST
export.localhost

NEW  This behavior was not already represented in the run's evidence.

Decision   alert        Risk   high
Trust      0.61  ▓▓▓▓▓▓░░░░
Anomaly    0.82  ▓▓▓▓▓▓▓▓░░
Confidence 0.74  ▓▓▓▓▓▓▓░░░
```

Every value is the server's, rendered. Nothing is computed, combined,
thresholded or ranked here, and there is deliberately **no aggregate health
score** — five independent readings collapsed into one red/amber/green verdict
would be the browser inventing a judgement the platform never made. The bars
are a second rendering of the same numbers, which stay printed beside them.

Identifiers appear at the bottom of the panel as technical detail. They are not
the visual hierarchy: what you are reading is what the agent did.

### Bounds, and what saturation means

| Bound | Value |
|---|---|
| active scope cards | 16 |
| graph source nodes | 8 |
| graph target nodes | 64 |
| graph edges | 128 |
| timeline rows | 100 |
| frames buffered during resync | 64 |
| automatic collection requests at startup or reconnect | 1 |

Eviction is least-recently-observed, and saturation is always stated: the view
names which bound it hit and how many items it is not drawing. It never
describes a truncated graph as complete.

Three different facts are kept apart, because confusing them would misreport
the platform:

- **a saturated viewport** — the browser chose not to draw everything;
- **`behavior_complete: false`** — the *run's own evidence* saturated at 512
  distinct behaviors, which is the platform's statement, not the browser's;
- **a realtime queue overflow** — notification continuity was lost, so the
  stream is abandoned and resynchronized rather than silently dropping a frame.

### Browsing what exists

Opening the page reads **one** page of `GET /v1/projects` and nothing else. No
child level is fetched and no continuation is followed until you ask, and the
same budget applies to every reconnect.

That bound is deliberate. Each route caps a page at 64, but a route that is
bounded does not make a *workflow* bounded: 64 projects × 64 agents × 64
candidates × 64 runs is sixteen million rows across a quarter of a million
requests, during which the resync buffer holds 64 frames — it would overflow,
the client would resynchronize, and the crawl would start again.

So descent is yours: selecting a project reads one page of its agents,
selecting an agent reads one page of its candidates, selecting a candidate
reads one page of its runs. Where more exists, **Load next page** says so and
costs one request. There is no timer, no polling and no background prefetch
anywhere in the page.

**Runs** reads the shared selection context (task 104): its agent list,
candidate list and run table are the context's pages, so a choice made under
Overview, Manage or any selector is already chosen here, and the reverse. The
run table is the recency collection (task 101) at the deepest level chosen —
a project's runs newest first before an agent is picked, an agent's once one
is, a candidate's once that is — so it never needs the crawl above. Each list
is one bounded page per press of **Load next page**; a page already held for
the scope is not read again, and run reads wait until a choice has finished
preselecting below itself, so choosing a project with one agent reads that
agent's runs once and nothing wider. A failed page shows its error with
**Try again** and is never retried by itself.

The filter boxes on **Projects** and **Runs** narrow the rows already on
screen. They never ask the server for a page it was not going to fetch, and the
line beside them always says whether you are looking at the whole collection or
one page of it.

### A run's workspace

Clicking a run row opens it. The sidebar stays on **Runs**, because that is
where you came from and where the breadcrumb returns you.

A compact strip carries the run's authoritative figures — status, candidate,
environment, and the record, observation and distinct-behavior counts read from
`GET /v1/evaluation-runs/{id}/progress`. Nothing on the strip is counted from
the rows on screen: retained history is bounded and those counts are not, so a
strip that counted itself would report how much the page drew rather than how
much the run observed.

Three tabs sit under it:

- **Overview** — the run record and its progress, as the control plane holds
  them.
- **Observations** — one bounded page of retained history. A decision shows as
  a rule down the row's leading edge *and* as a word in its own column; the
  rule makes fifty rows scannable, and the word is what carries the meaning.
- **Behaviors** — the distinct behavioral identities the run produced, with the
  authoritative observation count each carries.

Selecting an observation opens it in the panel beside the table: the recorded
decision and policy rule, the five scores, timing and span status, the
behavior, and the correlation references. **Session**, **trace** and
**behavior** are controls — pressing one narrows the table to that view, which
is a new bounded request with the narrowing applied in storage *before* the
page bound. At most one narrowing applies at a time, because the server accepts
at most one and a page that offered two would be claiming a capability the
protocol does not have.

Selecting a row does not redraw the table, so your place in it survives; a chip
above the table shows the active narrowing and removes it. Closing the panel
returns focus to the row it came from.

### Accessibility

Motion is decoration, never information. Under `prefers-reduced-motion` no
pulse element is created at all, and every fact it carried — which edge fired,
the decision, the risk level, the `NEW` badge, the connection state — remains
as text or a badge.

Nothing is conveyed by colour alone: every state carries a word or a marker.
Graph edges and target nodes are focusable and activate on Enter or Space with
accessible names describing the operation, target, decision, risk and
new-behavior state. The rail is a listbox whose cards report `aria-selected`.
Timeline rows are focusable and readable without the graph, and selecting one
moves focus to the inspector.

The timeline deliberately carries **no** `aria-live`: a busy agent produces
many observations per second and announcing each would make a screen reader
unusable. Connection state — the thing actually worth announcing — stays in the
header's `role="status"` region.

## Manage — the administrative surface

Everything that creates or changes control-plane state, behind one destination
with five sub-sections: **Projects**, **Agents**, **Candidates**,
**Evaluations** and **Open by ID**. Nothing was removed when it moved here —
creating entities, the full run lifecycle, and opening anything by identifier
all work exactly as before.

It is not how you *find* things: **Live** discovers active work by itself, and
**Projects** and **Runs** walk the hierarchy with nothing typed. Forms are the
right shape here because these are inputs, not navigation. Where the console
already knows a value — the scoped project, the chosen agent or candidate — the
field is filled for you and stays editable.

**The forms explain themselves now.** A field labelled "Candidate ID" beside an
empty box told a developer nothing about what belonged there. Each caller-owned
identifier carries inline help and an example:

```text
Candidate ID
A stable identifier for the version being evaluated.
Example: git:43af19c
```

Where a form needs an existing record rather than a new name, it offers a
searchable selector instead of a text box — see
[Choosing a record without typing it](#choosing-a-record-without-typing-it).
The search is over the pages a selector has loaded; there is still no
server-side search. Every collection is parent-scoped, ordered by an immutable
identifier in byte order, and bounded per page.

You open things by the ID you already know, which is the same ID the CLI
uses:

```text
Open by ID    project · agent · candidate · evaluation run
```

Creating something opens it for the current page session. That is a convenience
only — it is not a catalog, it is not history, and **a reload forgets it**.
Nothing about which IDs you opened is stored in the browser; the control-plane
database is the only source of truth.

[Task 074](tasks/v1.0/074-zero-input-live-behavior-webui.md) is what made this
secondary. See [ADR 0041](adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
for why the collection capability was added after two deliberate deferrals, and
why discovery is bounded as a whole workflow rather than only per route, and
[ADR 0050](adr/0050-the-browser-surface-is-a-record-first-admin-console.md) for
why what remains is a console of tables rather than a set of forms.

## Choosing a record without typing it

Evidence → Run history's session, trace and behavior narrowings are chosen
from the run's own lists — `GET /v1/evaluation-runs/{id}/sessions` (task 103),
`/traces` (task 100) and `/behaviors`. Each lists only identifiers carried by
**retained** observations, at most 4096 per run, and the selector's footer
says it was found in the run's retained history; anything else can still be
pasted.

Every field that asks for an existing project, agent, candidate, run or
environment is a **searchable selector** (task 098): type to narrow, arrow keys
to move, Enter to choose, Escape to close. Each option leads with the name a
person recognises — a project's or agent's name, a candidate's label, a run's
status, environment and creation time — and shows the identifier beside it.
The chosen record's identifier stays visible with a **Copy ID** control, because
telling two records with the same name apart is what an identifier is for.

**One context, carried everywhere.** The console holds one project, agent,
candidate and run. Choosing one in any destination — a row under Runs, a
selector under Manage, a project under Promotions — is the preselection in all
the others. A selection belongs to its parent: choosing a different project
clears the agent, candidate, run and environments; choosing a different agent
clears the candidate and run. Lists read for the previous parent are dropped,
reads still in flight for it are abandoned, and a late answer for it is
discarded — so a selector never shows one agent's candidates under another's
name.

**Preselected only when unambiguous.** When a whole collection — no
continuation — holds exactly one record, it is chosen and the selector says
*Chosen for you: the only one*. The only entry on a page that has more after it
is never treated as the only one.

**What a selector lists.** One bounded page of the existing collection route,
read when the selector is first opened, with **Load next page** where more
exist, up to 512 options. Filtering is over what is loaded, and the footer says
whether that is the whole collection or one page of it. Nothing is prefetched.

**Pasting is still there.** Each selector's field moved into an **Advanced:
paste an ID** disclosure, and it is still what the form sends — so an
identifier from a log, a CI job or the CLI works exactly as before. A pasted
project, agent, candidate or run in a selector's own paste path is resolved
upward through its record (a run's candidate, that candidate's agent, that
agent's project), so the context is always a chain that exists.

Fields that create something keep their text box: a new project, agent,
candidate, run or promotion identifier, a name, a behavioral profile, candidate
metadata and a failure reason are inputs, not choices.

| Where | Chosen from |
|---|---|
| Live → Watch one run | the newest runs in scope |
| Evidence → Run history | the newest runs in scope |
| Evidence → Provenance | Compare's two sides; the newest runs in scope |
| Promotions → History | projects; each row opens its decision |
| Promotions → Record one | Compare's two sides; the newest runs in scope; the project's environments |
| Manage → Agents / Candidates / Evaluations | projects / agents / candidates and environments |
| Manage → Lifecycle | the newest runs in scope |

**Run selectors are newest first across the deepest level chosen** (task 101):
a project alone lists the project's runs, an agent narrows to its runs, a
candidate to its own. The selector's footer names the scope. Choosing a run
from a wider list brings its candidate and agent with it — resolved from the
run's record — so the context never holds a run under the wrong candidate.

## Recorded scenario executions

Evidence → **Scenarios** lists the project's recorded scenario executions —
every `trustvian eval run` — newest first by start time (task 102), filtered by
the agent chosen in the context and an environment, with a search over what is
loaded. Each row shows the scenario name, status, verdict, repetition count,
environment, agent, start time and the execution it reused.

Opening one shows its scope, times and repetitions, each run a link to its
workspace. **Check eligibility** asks the control plane whether
`trustvian eval run --reference <id>` would accept it — the same validation the
CLI runs, unchanged — and shows its answer: usable, or not with the server's
own reason, and the conditions the answer holds under (the execution's `runs`,
project and environment). Only a usable execution offers **Copy CLI command**,
`trustvian eval run --scenario <scenario.yaml> --reference=<id>`, with the
identifier shell-quoted.

Nothing runs from the browser. A scenario executes a developer's command, and
starting one from a page would make the local control plane an executor; that
stays in the CLI.

## Watching one run

Both subscriptions — the global Live one and the single-run watch — follow the
same sequence, in this order:

```text
subscribe to the stream
→ wait for a valid stream_ready
→ keep buffering events that arrive
→ read the authoritative snapshot
→ apply it
→ replay the buffered events
→ live
```

The order matters. Reading state first and subscribing afterwards would lose
anything committed in between.

The two differ only in what "the authoritative snapshot" is. Watching one run
reads that run and its progress. The global Live view has no single run, so its
snapshot is **one bounded page of `GET /v1/projects` and nothing else** — a
snapshot of what exists at the root, not of what is active. Activity is
realtime's answer, and the page says which is which.

Narrowing to one run is still worth doing when a run is finishing and you care
about its final authoritative state; the Live stream above already shows every
run as it happens.

### Live rows are a viewport, not history

The observation table shows at most the **100 most recent observations of the
current connection**, and it is **cleared whenever the stream restarts**.

That is not a shortcoming to work around. The realtime bus keeps no history and
there is nothing to replay, so joining two connections into one apparent
sequence would present a continuity that does not exist. Retained event history
is [task 067](tasks/v1.0/067-event-history-capability-boundary.md)'s, and
[the Evidence surface](#the-evidence-surface) is where it is read.

### Counts always come from the database

Every number shown as a count — records, distinct behaviours, next sequence —
is read from the control plane, never accumulated from stream events. When a run
finishes, one final authoritative read happens so the closing counts are the
database's.

### When the stream has trouble

| What happened | What the page does |
|---|---|
| Never synchronized | Shows **Failed** and stops retrying. Use **Reconnect now** |
| Was live, then disconnected | Reconnects with bounded backoff: 250ms, 500ms, 1s, 2s, 4s, capped at 5s |
| Heartbeats but no `stream_ready` | Treated as a failure after a finite handshake window, not as a healthy stream |
| More than 64 events buffered during a read | Abandons the stream and resynchronizes — never silently drops one |

Heartbeat traffic keeps an *established* stream alive but does not make a
connection ready. A connection that never completes the realtime handshake
fails or reconnects; it does not sit there looking like it is working.

## Investigating a trace

**Traces** lays one run's traces out the way Grafana Tempo and Jaeger do — a
searchable list on the left, a waterfall in the middle, an action's details on
the right — over what Trustvian actually retains
([ADR 0062](adr/0062-trace-investigation-is-a-waterfall-over-retained-observations.md)).

```text
Overview ─ Investigate traces ─┐
Run ─ Investigate traces ──────┼─▶ Traces: run ▸ trace list ▸ waterfall ▸ details
Observation ─ Trace timeline ──┘
```

**What a row is.** Each row is an *evaluated action*: an observation Trustvian
made a decision about. Spans it did not evaluate were never retained, nor were
attributes, events, links, service names or content — so this is the evaluated
part of a trace, and the page says so above the list and above the waterfall.

**The list** is `GET /v1/evaluation-runs/{run_id}/traces`: every trace
identifier in the run's retained history, in the order the run first produced
each, with how many actions carried it and how many recorded an `error` status.
The filter is over the loaded list; **Load next page** reads more.

**The waterfall** nests rows by the recorded parent span reference and nothing
else, exactly like the Evidence tree, so a parent the page does not hold reads
*unresolved* rather than becoming a root. A bar starts at the producer's
timestamp — span start, for OTLP — relative to the earliest on the page, and is
as long as the measured duration. A row with no measured duration is a dashed
marker, never a zero-width bar; a row without a readable timestamp has no bar.
Producers' clocks can disagree and nothing corrects for it. A trace longer than
64 actions is paged.

**Details.** Clicking a row, or pressing Enter on it, opens the action beside
the waterfall: the decision, timing, recorded structure, behavior, scores and
identifiers, with session and behavior as links into Evidence. Selecting
redraws nothing, so the chosen trace, the filter and both scroll positions
stay. Arrow keys, Page Up/Down, Home and End move the selection and the panel
follows; Escape closes it and returns focus to the row. On a narrow screen the
panel is a drawer over the waterfall and focus moves into it.

## Comparing two runs

**Compare** shows the runs of a candidate as a table with two controls on every
row: **Reference** and **Candidate**. You assign a side by pressing one. Both
chosen runs are then shown in full in labelled panels, so what you are about to
compare is on screen rather than implied by two opaque strings, and the
**Compare runs** control stays disabled until both sides are chosen and they
are different runs.

There is no identifier field for either side and no menu standing in for one.
The identifier is still the value the server receives — the contract is
unchanged — but nobody has to find one, copy one or recognise one in a list.

Compare keeps its own agent and candidate lists rather than sharing the **Runs**
destination's. Sharing them would mean that choosing a comparison moved your
place in the run table, and that changing that table silently changed what a
pending comparison meant.

All three gate limits are required, and `0` is valid. They are **policy, not
evidence**: they are yours to choose, the same comparison yields PASS or FAIL
depending only on them, and each carries inline help saying what it bounds.

A fourth field, **Max added behavior changes**, is optional on both the
comparison and the promotion form (issue 131). Left empty it is omitted from the
request — never sent as `0` — and the gate row reads *not evaluated*. A
promotion recorded before the limit existed reads *not recorded*. Either way the
row shows no pass or fail, because the check had neither, and it offers no
evidence control: the server does not resolve that check through the finding
route, since its evidence is the comparison's counted changes.

The verdict shown is the server's `gate.verdict`. The browser does no gate
arithmetic — it renders the checks the control plane returned, with the actual
and the bound for each, and the counted-change check's state as the server
named it.

```text
Gate: PASS   ✓        Added behaviors: 2 / max 3
Gate: FAIL   ✗        Critical observations: 0 / max 0
```

A gate result is a deterministic outcome under **the limits you supplied**. It
is not a judgement that a candidate is safe, unsafe, secure or ready for
production, and the UI does not use that language.

Two things that look similar and are not:

- **Gate FAIL** — the comparison succeeded and the verdict is `fail`.
- **An error** — the request failed, or the control plane refused it (for
  example, a run with no evidence). This is shown as the error it is, never as
  a FAIL.

A comparison of two runs with no ingested evidence returns a real FAIL, because
the gate requires a minimum of one observation on each side and fails closed
when evidence is missing. That is the gate working, not a bug.

## The Evidence surface

A gate FAIL names a count. **Evidence** is where that count becomes the
observations behind it.

```text
Compare               gate FAIL · added_behaviors 2 / max 0
  → Evidence          the two behavioral identities that contributed
  → Evidence          the retained observations that carried one of them
  → Run history       that observation's session · trace · behavior
```

**Nothing in that path is typed.** The evidence control on a gate check and on
a behavioral delta builds the finding reference from the two run identifiers the
comparison itself returned, and the `Session`, `Trace` and `Behavior` controls
on an observation row carry the identifier that observation recorded.

### What it shows, and what it refuses to

This surface renders retained evidence. It resolves nothing: **task 085** owns
resolution at the control plane, **task 067** owns retention, and every status,
side, count, verdict and exhaustiveness flag on screen is a value `/v1`
returned. The browser chooses a URL and renders a response.

What it deliberately cannot reach: no prompt, completion, reasoning trace, tool
argument, tool result, retrieved document, HTTP body, SQL text or arbitrary
attribute. Not filtered out here — **no column holds one**, so no response
carries one. The one retained field it declines to render is `policy_reason`,
the single free-text producer-supplied value on the row; `policy_rule`, the
identifier naming which rule decided, is shown in its place.

### Finding

The finding view answers *which evidence supports this*, and it keeps four
answers apart because three of them look identical in a payload:

| Status | What it means |
|---|---|
| `resolved` | The finding has supporting evidence in the retained history |
| `none_found` | Nothing matches, **and** this side's retained history is complete — so that is a fact about the run |
| `indeterminate` | Nothing matches and the history is partial or unavailable, so the absence establishes nothing |
| `aggregate_only` | The check counts an absence — it fails when a run observed *too little* — so there is no observation to link and none is invented |

**`aggregate_only` is an applicability answer, not a history answer.** The two
minimum-count checks have no per-observation evidence to attribute, so the
control plane reports that and returns **without reading any retained history at
all**. Their history and exhaustiveness fields are therefore the zero value, and
the page shows neither: no retained-history block, no sampling caveat, no empty
table and no page control. What it does show is the explanation above and the
recorded count, which is the gate's own actual read from the run's aggregate.
Rendering those defaults would tell you a run whose retained history is complete
had none of it retained.

**The status describes the finding, not the page.** Paging past the last match
shows zero rows and still reports `resolved`, because the evidence exists and
you have read all of it. "End of results" is worded as a statement about the
page for exactly that reason.

**Reference and candidate stay visibly distinct**, with a chip and a word on
every resolution. A **shared** behavioral delta is offered one control per side
and no default: both runs hold their own observations of it, those two sets are
what you are comparing, and picking one silently would look identical to the
answer you wanted. Added and removed behaviors send no side at all — presence
decides it, and the control plane is what decides.

The **recorded count** is what the evidence holds: it counts records the run
ingested, while retained history is bounded at 4096 per run and may be partial.
A larger recorded count beside a shorter page is bounded retention describing
itself, and the page says so rather than reconciling two different
measurements.

### Run history

Five views over one run, each a bounded page:

| View | Reads |
|---|---|
| **Session actions** | one bounded interaction's actions, in the order the platform accepted them |
| **Trace context** | one invocation's actions and the parent/child structure recorded for them |
| **Behavior sequence** | the behavioral identities the run exhibited, in ingest order |
| **Decision timeline** | the decisions in order, with each observation's duration and span status |
| **Behavior detail** | one behavioral identity's retained observations inside this run |

Each narrowing — session, trace or behavior — is a **storage predicate applied
before the page bound**, so a page of 64 holds 64 matches rather than 64 rows of
which some matched. At most one may be set; the control plane refuses a
combination rather than answering a question nobody specified. See
[ADR 0049](adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md).

### Trace structure is recorded, never inferred

The tree is drawn from the recorded parent span reference and from nothing
else — never from timestamps, adjacency, name similarity or ingestion order.
**A parent this page does not have is not a root**, and the view distinguishes
seven states:

```text
root         the producer said this span starts the trace
child        the parent reference names a span on this page
unresolved   the parent is not here — never retained, sampled away, or on
             another page. Which of those is not knowable from here
ambiguous    more than one retained observation carries that span id
self         the span names itself as its parent
cycle        the recorded parent chain returns to this span
unstated     no parent recorded, and the lineage does not claim a root
```

Rows are in the order the platform **accepted** them, which is not wall-clock
order: a parent span ends after the children it started, so a child is routinely
accepted first. Durations are per observation and are **never summed**, because
a sum of span durations is not wall-clock latency.

**Ordering is not reasoning.** No label says an agent decided, chose, intended
or planned anything, and no label carries a causal connective between two spans.
A trace shows what was observed and in what structure; why a model chose
anything is not something the telemetry can support.

### Absence is shown as absence

| Fact | Shown as |
|---|---|
| a measured duration of zero | `0 ms (measured)` |
| no duration measured | `not available` — never `0`, and never "fast" |
| span status `unset` | `unset — the producer stated no status`, never success |
| span status absent | `not available`, never "no errors" |
| `new_behavior` | *new to this run* — which is **not** novelty against a learned baseline |
| retained history | `complete`, `partial` or `unavailable`, each with a sentence saying what it means |

### Two things this surface cannot tell you

Both because task 067 does not retain them, and both stated on screen rather
than guessed at:

- **Fidelity and behavioral layer.** They travel beside a record at ingest and
  on the realtime stream and have no retained column, so a historical view does
  not know whether an operation's identity came from agent-oriented telemetry or
  from its transport. It renders the recorded descriptor exactly as it stands and
  says fidelity was not retained. **Live** still states fidelity, because
  realtime carries it.
- **Sequence deviation.** The engine's sequence signals live in the anomaly
  contributors, which 067 excluded as its one variable-length field. No view
  states whether an order departed from what was learned.

### Provenance

Both sides' supplied `CandidateMetadata`, side by side: label, source ref,
artifact digest, model, toolset digest and config digest. **Every field the
producer did not supply reads `not stated`** — never blank, never a default, and
never omitted. An unknown model is materially different from a model both sides
shared, and a panel that could not tell them apart would be worse than one that
said nothing.

### Bounds

One page at a time, `after` exclusive, `limit` 64, continuation offered exactly
when the route publishes a cursor. **A continuation replaces what is on
screen**, so the memory a history costs is a page however far you read — the
same shape the promotion history uses. There is no previous-page control,
because reverse traversal is not something `/v1` offers.

Changing the run, view, side, finding or identifier resets the cursor and
abandons whatever is in flight **for that surface**: a response that lands after
the question changed is discarded rather than drawn.

**Finding, Run history and Provenance cancel only themselves.** They answer three
unrelated questions, so each keeps its own request token and its own page
position. Loading provenance while a run-history page is still arriving leaves
that page to land, and leaves its page number where it was — a shared token
discarded the response and left the panel on a loading notice that nothing would
replace, because selecting a subtab starts no read.

Nothing about which finding you were reading survives a reload, and nothing is
stored in the browser.

## Recording a promotion

The Promotion panel records a **decision** — that a candidate's evidence was
gated, and that the verdict accepted or refused advancement toward a target
environment. It is not a deploy button. Trustvian has no deployer and no
observer that could confirm a deployment happened, so the UI never says a
candidate was deployed, released, rolled out, or is now running or live
anywhere.

The form asks for the two run IDs, the target environment and the three gate
limits, and nothing else. The source environment is not a field: the server
infers it from the environment the two runs share. Neither is the outcome —
that comes from the gate verdict alone, and a browser cannot propose one.

The browser performs no promotion logic of its own. It compares no environment
ranks, computes no gate result, and infers no source: every one of those is the
control plane's answer, rendered as received.

**Target environments** come from the project's whole environment collection,
not its first page. Task 065's cap of 64 per project governs creation rather
than existence — a database migrated from schema 2 holds one environment per
distinct reference its runs recorded — so **Load environments** follows
`next_after` to the end and offers everything it finds. Which of those are
valid targets is still the server's answer: nothing is filtered out here, and a
promotion toward an invalid one is refused with its own message. If a server
will not terminate the collection, the traversal stops and says so rather than
offering a short list that would read as "that environment does not exist".

**Project history** is read one bounded page at a time. **Load next page**
requests exactly one more page using the cursor the previous one published, and
replaces what is on screen — so the memory a history costs is a page, however
far you read. **Start over** returns to the first page. There is no
previous-page control: reverse traversal is not something `/v1` offers, and
keeping every visited page in order to walk backwards is the unbounded
accumulation this shape avoids.

Your position in the history lives in the tab and nowhere else. A reload starts
again at the first page, for the same reason nothing else here survives one.

Each history row carries the two evaluation runs the decision was made on, each
with an **Open** control. Opening one re-reads `GET /v1/evaluation-runs/{id}`
through the same path the Evaluation panel uses, so what you see is the run's
own current state — the promotion record does not become a second source of
truth for it.

`accepted` and `rejected` mean exactly what the gate said under the limits that
were supplied. `accepted` does not mean deployed, and it does not mean safe;
`rejected` does not mean unsafe. The same caveat as the gate verdict applies,
for the same reason.

### Large counters

Counters and gate limits are exact decimal integers up to
`18446744073709551615`. The UI keeps them as text end to end and never converts
them to JavaScript numbers, which cannot represent values that large without
rounding.

## Security

**The local runtime is unauthenticated.** It binds numeric loopback only, has
no flag to change that, and loopback is the entire security boundary. Do not
expose it, and do not put a reverse proxy in front of it. Authentication, TLS
and remote access are [task 070](tasks/v1.0/).

What the browser side adds:

- **Every value from the API is treated as untrusted text.** Names, failure
  reasons, behaviour descriptors and error messages reach the page through
  `textContent` only. No server string can become executable markup, and the
  test suite fails the build if `innerHTML` or its relatives appear in shipped
  source.
- **A strict Content Security Policy** — `default-src 'none'` with `'self'`
  for scripts, styles and fetches, and nothing external. There is no inline
  script and no inline style, which is what makes that policy achievable
  without weakening it.
- **No external dependency.** No CDN script, stylesheet or font; no npm
  package, lockfile or bundler. The page works offline and adds nothing to the
  supply chain.
- **No CORS header.** The UI is same-origin with the API, so none is needed —
  and adding one is the single change that would let any page you visit reach
  your control plane.
- **Only fields the UI names are displayed.** Rendering works from an explicit
  allowlist, so an additive field the API gains later cannot appear on screen
  without someone deciding to show it. Prompts, completions, tool arguments,
  event attributes, policy reasons and raw decision records are absent from the
  realtime contract and are not displayed.
- **No platform state is stored in the browser.** No identifier, record, page
  or cursor goes into `localStorage`, `sessionStorage`, `IndexedDB` or a
  cookie. The one exception is the colour-scheme choice: one `localStorage`
  key, `trustvian.theme`, holding `light` or `dark` and nothing else
  ([ADR 0061](adr/0061-the-theme-preference-is-the-one-value-the-browser-stores.md)).

CSP is browser hardening. It is not authentication, and it does not make the
runtime safe to expose.

## How the bundle is put together

Five layers, and dependencies only point downward.

| Layer | May import | Holds |
|---|---|---|
| `core/` | nothing | DOM construction, value formatting |
| `v1/` | `core` | the control-plane contract: routes, field allowlists, renderers |
| `ui/` | `core` | the design system: tables, strips, pick lists, panels, icons, states |
| `live/` | `live` | the realtime observatory: graph, rail, timeline, inspector |
| `views/` | `core`, `v1`, `ui` | one module per destination |
| `app.js` | anything | the composition root, and the only one |

`ui/` may not import `v1/`. That boundary is the one that matters: the moment
a component can read a route, presentation stops being a layer and the design
system starts carrying domain knowledge.

Styles are five sheets in cascade order — `tokens`, `base`, `layout`,
`components`, `views`. **Only `tokens.css` names a raw value.** Every colour,
size, space, radius and duration is declared once with its dark-mode
counterpart beside it and read elsewhere as `var(--…)`; a hex literal in any
other sheet fails a test. That is what keeps light and dark from drifting
apart, and it is why the accent is blue: green, amber and red carry verdict
and risk, violet carries "new", and an accent sharing any of those hues would
make *selected* read as *severe*.

### Themes

Light, Dark and System sit at the foot of the sidebar as one radio group, so
arrow keys move the choice and a screen reader announces it. A first visit
follows the operating system. An explicit choice is remembered; choosing
System forgets it, and the page then follows the operating system again —
including when it changes while the page is open. The note under the switcher
says which scheme System currently means, and says so when the browser is not
keeping the choice.

A remembered Dark never flashes light: `core/theme-boot.js` is a classic
script in `<head>`, ahead of the stylesheets, that applies the stored choice
before the first paint. It is external, so the CSP is unchanged.

The dark palette is declared twice in `tokens.css` — for an explicit choice
(`data-theme="dark"`) and for a dark system with no choice — and a test keeps
the two bodies identical. Each scheme declares `color-scheme`, so native
controls and scrollbars follow it. Body text meets WCAG AA (4.5:1) on every
surface it sits on, in both.

There is no web font. `font-src 'none'` is an honest policy only because
nothing asks for one, so the type system's distinction is prose versus
record: labels and help in the UI sans, and every value that *is* a record —
an identifier, a timestamp, a score, a count — in mono with tabular figures,
so a digit never moves between rows.

## Which response is still the one that matters

The reader can move while a read is in flight, so every asynchronous surface
decides on arrival whether its response is still wanted. A request takes a
ticket before awaiting and presents it back afterwards; a ticket is stale
once a newer request has started on that surface, or once the subject has
changed underneath it.

This is deliberately **not** built on cancellation. An abort is a request to
stop that the network may decline, and it can lose the race with a response
already queued — a surface whose correctness depended on the abort winning
would be right almost always, which is the worst place for a correctness
property to be. A ticket comparison has no race to lose.

What it prevents, each of which was a real defect:

- A response for run A landing after the reader opened run B, filling B's
  table with A's rows.
- An older request finishing last and overwriting the newer answer.
- An older request's completion clearing the loading flag a newer one had
  just raised, so the skeleton vanished while the newer read was still out.
- A response for the previous project repopulating the destination the
  reader has already switched away from.

Two rules follow from it. **Everything scoped to a subject is cleared when
the subject changes** — opening a run clears both tab pages, their cursors,
the narrowing, the selection and the strip's counts in one step, and changing
project clears Compare's lists, both assigned comparison sides and the
promotion cursor. And **a cache is valid for an identity, not for a
boolean**: Compare and Promotions ask whether what they hold describes the
project they are scoped to, because "have I loaded before?" is still true
after a project change.

Surfaces are independent. The three reads of a run workspace, the three
lists in Compare, and Promotions each hold their own ticket, so fetching one
never abandons another's outstanding work.

## What a surface looks like when it has no records

Four states, and a surface is always in exactly one:

- **Loading** — a skeleton in the shape of the rows that are coming, so the
  page does not jump when they arrive. It is **static**: this console has a
  standing rule that nothing loops, because a page with something
  perpetually moving on it teaches a reader to ignore movement, and movement
  is the one signal this product has. `aria-busy` says the same thing to a
  screen reader.
- **Empty** — the absence stated as a fact, and the next action named. An
  empty collection is not a failure and nothing here apologises for one.
- **Failed** — the server's refusal, shown as the refusal it is. An
  unreachable control plane is never rendered as a gate result.
- **Populated** — the records.

Loading and empty used to render identically, as one line of italic text, so
a table that was still fetching and a table with nothing in it looked the
same. They are now different answers, and a guard keeps them that way.

### Severity is three carriers, and the word is the one that counts

A decision shows as a gutter down the row's leading edge, a tint behind the
row, and a mark beside the word in its own column.

```text
▏1  09:11:01  POST /chat        ✓ allow    low
▏5  09:15:05  export_customer   ⊘ block    ⚠ critical
▏6  09:16:06  send_email        ⚠ alert    ⚠ high
```

Remove the gutter, the tint and the mark and the table is still correct —
that is the test for whether any of them was allowed to be added. The word is
always the one the server returned, spelled its way.

## No build step

The UI is HTML, CSS and vanilla JavaScript, compiled into the binary with
`embed`. There is nothing to install, build or regenerate, and no frontend
toolchain in the repository. Editing the files under
`platform/webui/assets/` and rebuilding is the whole workflow.

## Compatibility

| Surface | Class |
|---|---|
| `/v1` request and response semantics | Versioned machine contract |
| Realtime protocol | Task 059's contract |
| Gate meaning | Server-owned |
| `/` and static asset paths | Local-runtime interface, not the versioned API |
| Visual layout, DOM structure, CSS classes, element IDs | **Observational** |

The page's markup is not an interface. Do not scrape it, and do not build
automation against a class name or element ID — automation reads `/v1`, which
is the machine contract every client shares.

## Related

- [Local development](local-development.md) — the runtime the UI is served from
- [Platform CLI](platform-cli.md) — the same operations from a shell or CI
- [Terminal dashboard](tui.md) — the same live view in a terminal
- [ADR 0036](adr/0036-webui-is-a-same-origin-adapter-over-v1.md) — why the UI
  is a static same-origin client with no control-plane authority
- [ADR 0041](adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
  — the collection capability, the id cursor, and why the graph is one run
- [ADR 0049](adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md)
  — why the Evidence surface narrows retained history with three mutually
  exclusive predicates, and why it answers a behavioral question rather than a
  trace question
- [ADR 0036](adr/0036-webui-is-a-same-origin-adapter-over-v1.md) — why the UI is
  a static same-origin adapter rather than a server-rendered or framework app
- [Task 063](tasks/v1.0/063-minimal-web-control-plane.md) — the specification
