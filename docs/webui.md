# Web interface

A live window into what an agent is doing. One command starts it; the URL is
printed.

Trustvian's browser surface is an **observability cockpit first and a control
plane second**. Opening it shows the agents that are working right now, what
they are touching, and what Trustvian decided about each call. The
control-plane forms still exist — creating entities, driving a run's lifecycle,
opening something by an identifier — but they are a secondary surface called
**Manage**, and watching an agent never requires them.

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

**Nothing needs to be typed.** The page opens on **Live**, subscribes to all
local activity, and shows every run that is producing telemetry as it arrives.
If your agent is running, it is already on the screen; if nothing is running,
the hierarchy browser below the graph shows what exists.

The API and the WebUI are the **same endpoint**. The UI is served from the same
loopback listener that answers `/v1`, which is why `runtime.json` still carries
one URL and needs no second field.

## What it can do

| Section | Purpose |
|---|---|
| **Live** *(default)* | Watch. Active agents appear by themselves, one run's behavior flow animates as calls arrive, and an inspector shows what Trustvian decided |
| **Investigate** | Explore. Browse the durable hierarchy a bounded page at a time, open a run's authoritative detail, or narrow the stream to one run |
| **Compare** | Measure. Compare a reference run against a candidate and read the server's gate, diff and scorecard |
| **Promotions** | Decide. Record a promotion decision and page a project's history |
| **Manage** | Administer. Create projects, agents, candidates and runs; drive a run's lifecycle; open anything by identifier |

What it deliberately cannot do: ingest decision records (that is the job of the
application under evaluation, through the CLI or the API), browse event history
(task 067), or manage environments (task 065).

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
then. Task 067 owns retained history; this view owns what is happening now.

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
reads one page of its runs. Where more exists, **More** says so and costs one
request. There is no timer, no polling and no background prefetch anywhere in
the page.

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

Everything that creates or changes control-plane state, behind one tab with
five sub-sections: **Open by ID**, **Projects**, **Agents**, **Candidates** and
**Evaluations**. Nothing was removed when it moved here — creating entities,
the full run lifecycle, and opening anything by identifier all work exactly as
before.

It is no longer how you *find* things: **Live** discovers active work by
itself, and **Investigate** walks Projects → Agents → Candidates → Runs with
nothing typed.

**The forms explain themselves now.** A field labelled "Candidate ID" beside an
empty box told a developer nothing about what belonged there. Each caller-owned
identifier carries inline help and an example:

```text
Candidate ID
A stable identifier for the version being evaluated.
Example: git:43af19c
```

There is still no search. Every collection is parent-scoped, ordered by an
immutable identifier in byte order, and bounded per page; filtering by name is
a capability with its own design and nothing in this journey needs it.

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
why discovery is bounded as a whole workflow rather than only per route.

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
is [task 067](tasks/v1.0/).

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

## Comparing two runs

Browse to a candidate under **Investigate** and its runs fill the reference and
candidate selectors here, so you pick a run you can see rather than copying an
identifier out of a log. The text inputs remain, because a run you already have
an identifier for should not require browsing to it first — the identifier is
still the value the server receives.

All three gate limits are required, and `0` is valid. They are **policy, not
evidence**: they are yours to choose, the same comparison yields PASS or FAIL
depending only on them, and each carries inline help saying what it bounds.

The verdict shown is the server's `gate.verdict`. The browser does no gate
arithmetic — it renders the five checks the control plane returned, with the
actual and the bound for each.

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
- **Nothing is stored in the browser.** No `localStorage`, `sessionStorage`,
  `IndexedDB` or cookies.

CSP is browser hardening. It is not authentication, and it does not make the
runtime safe to expose.

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
- [ADR 0036](adr/0036-webui-is-a-same-origin-adapter-over-v1.md) — why the UI is
  a static same-origin adapter rather than a server-rendered or framework app
- [Task 063](tasks/v1.0/063-minimal-web-control-plane.md) — the specification
