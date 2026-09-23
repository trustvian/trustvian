# 063 — Minimal Web Control Plane

Status: specified
Depends on: [057](057-local-platform-persistence.md),
[058](058-local-control-plane-api-and-ingest.md),
[059](059-realtime-infrastructure.md), [061](061-terminal-dashboard.md),
[062](062-integrated-local-developer-workflow.md)

## Objective

Make the local control plane legible from a browser.

Task 062 made the platform startable and self-advertising. Its only clients
are a CLI and a terminal dashboard, both of which assume a developer who
already knows what to type. Task 063 adds the first graphical client:

```text
Terminal    make local
Browser     open the Web URL trustvian-local prints
```

From there a developer opens an evaluation run and can see what the run is,
what the candidate did, what changed against a reference, and whether it
passes the gates — without reconstructing any of it from CLI output.

## Why

Three reasons, in the order they matter.

**The evidence is already shaped for a screen and has nowhere to go.** Tasks
053–056 produce a fixed-shape aggregate, a bounded behavioral diff, a
comparative scorecard and a five-check gate result. `eval compare --json`
emits all of it as one JSON document. A behavioral diff with per-fingerprint
deltas and a scorecard with six decision classes across two runs is a table,
and reading it as raw JSON in a terminal is the current experience.

**The roadmap commits to it.** `v1.0` is defined as the release where
Trustvian is usable as a product rather than a library, and
[ADR 0023](../../adr/0023-interfaces-are-adapters.md) names the WebUI as one of
three first-class interfaces from the start. Task 063 is where that stops being
a plan.

**It is the last interface that can still be shaped by the existing boundary.**
ADR 0023 anticipated that each of the three interfaces would be a plausible
place to "just compute the gate result here", and said no. The CLI (060) and
TUI (061) both held that line. A WebUI is the one with the most pressure on it,
because browsers conventionally arrive with a framework, a data layer and a
habit of computing things client-side. Building it as a third `/v1` client,
while the other two are fresh precedent, is materially easier than retrofitting
the boundary later.

### Why *minimal*

[ADR 0023](../../adr/0023-interfaces-are-adapters.md) scopes the eventual
WebUI to "management, history, investigation, and the workflows the TUI
declines". Most of that is owned by later milestones: history by task 067,
environments by 065, promotion by 066, multi-tenancy and auth by 070.

What is left once those are removed is exactly what this task builds: navigate
to an evaluation, drive its lifecycle, watch it live, and compare two completed
runs. That is the v1.0 local developer journey and nothing more.

## Scope

1. A static browser application, embedded in the platform binary, served from
   the Task 062 listener at `/`.
2. Open-by-ID navigation for project, agent, candidate and evaluation run.
3. Create project, agent, candidate and evaluation run.
4. Evaluation run detail: identity, status, timestamps, failure reason, and
   authoritative progress counts.
5. Lifecycle actions — start, complete, fail, cancel — issued to the server,
   which decides whether each is legal.
6. A live view of one run over the existing SSE endpoint.
7. A compare view rendering behavioral diff, scorecard and the five-check gate
   result from `POST /v1/evaluations/compare`.
8. An architecture guard proving the server-side WebUI package holds no
   control-plane authority.

## Non-goals

No collection or list route, and no direct store access (see
[Open question](#open-question-collection-semantics)). No ingest form in the
browser — ingest is producer/automation behaviour that belongs to the
application under evaluation, and a large JSON textarea is not a control-plane
workflow. No browser persistence of platform state. No frontend framework or
build toolchain. No WebSockets, no polling, no replay. No CORS.

Explicitly reserved for later milestones, and not to be previewed here:
PostgreSQL platform backend (064), environment model (065), promotion (066) —
**including a disabled or mock "Promote" control** — event history (067),
ClickHouse (068), multi-node (069), authentication, TLS, RBAC or
multi-tenancy (070), backup/restore/upgrade (071), release gate (072).

No core or published-module change. No change to `/v1` request/response
semantics, the realtime protocol, the persistence schema, CLI exit codes, or
`DecisionRecord` compatibility.

## Architecture

```text
Browser
  │
  ├── fetch       → HTTP  /v1/…
  └── EventSource → SSE   /v1/realtime?run_id=…
                      │
                      ▼
                 ControlPlane
                      │
                      ▼
                   Stores
```

The WebUI is an adapter, and the boundary is enforced by two separate
mechanisms rather than by discipline.

**Server side** — a new `platform/webui` package that serves embedded static
assets and sets security headers. It imports only `embed`, `io/fs` and
`net/http`. Its constructor takes no arguments:

```go
func NewHandler() (http.Handler, error)
```

not

```go
func NewHandler(plane *platform.ControlPlane) http.Handler
```

A package that cannot be handed platform authority cannot accumulate it. This
is the same technique ADR 0033 used for the CLI — there, the module boundary
made `internal/*` a compile error; here, the constructor signature makes
control-plane access unavailable.

**Browser side** — the application calls `/v1`, the same contract the CLI and
TUI use. One wire contract, three clients.

Forbidden edges, each covered by a test:

```text
webui → trustvian-platform          (the control plane, stores, gate, diff)
webui → github.com/trustvian/trustvian
webui → database/sql, any driver
browser → anything but /v1
```

### Why embedded static assets rather than `html/template`

Server-rendered HTML was the obvious alternative and is rejected for one
reason: it requires giving the Go handler control-plane access.

The moment `platform/webui` holds a `ControlPlane`, the compile-time guarantee
above becomes a code-review guarantee, and rendering code sits one import away
from every service that computes a verdict. ADR 0023's warning is precisely
that each interface is a plausible place to compute the gate result; a template
handler that already holds the plane is the most plausible of the three.

Contextual auto-escaping is a real advantage of `html/template` and its loss
must be paid for deliberately — with a strict CSP, a text-only rendering rule,
and source guards that fail the build on `innerHTML`. That cost is bounded and
testable. Re-establishing the adapter boundary after it has been crossed is
not.

Secondary evidence: the repository has no templates anywhere today, and a
release artifact must stay self-contained, which `embed` satisfies either way.

This decision is recorded in
[ADR 0036](../../adr/0036-webui-is-a-same-origin-adapter-over-v1.md).

### Why no frontend framework

React, Vue, Svelte, HTMX, Alpine and jQuery are all excluded, along with npm,
lockfiles, bundlers and CDN references.

The UX required is forms, tables and an event stream over bounded,
fixed-shape state. No part of it needs a reconciling renderer or a component
model. Against that, a framework adds a toolchain to install and pin, a
dependency graph to audit, and a supply-chain surface on a security product's
control plane — and would make the strict CSP below either dishonest or
impossible, since most CDN and inline-bundle patterns require `unsafe-inline`.

Expected dependency delta: **zero**, in every module.

## HTTP/API interaction model

Every route the browser uses exists today. Task 063 adds no API capability.

| Action | Route |
|---|---|
| Create / open project | `POST /v1/projects` · `GET /v1/projects/{project_id}` |
| Create / open agent | `POST /v1/agents` · `GET /v1/agents/{agent_id}` |
| Create / open candidate | `POST /v1/candidates` · `GET /v1/candidates/{candidate_id}` |
| Create / open run | `POST /v1/evaluation-runs` · `GET /v1/evaluation-runs/{run_id}` |
| Lifecycle | `POST /v1/evaluation-runs/{run_id}/{start,complete,fail,cancel}` |
| Progress | `GET /v1/evaluation-runs/{run_id}/progress` |
| Compare | `POST /v1/evaluations/compare` |
| Live | `GET /v1/realtime?run_id=…` |

Deliberately unused: `POST /v1/evaluation-runs/{run_id}/records` and
`GET …/ingest-state`, which are producer surface.

### Client bounds

Mirroring limits the server and CLI already establish:

| Bound | Value | Source of the number |
|---|---|---|
| Request body | 256 KiB | `httpapi.maxAPIRequestBody` |
| Response body | 4 MiB | the CLI's `maxPlatformResponseBody` |
| One-shot timeout | 30 s | the CLI's `platformRequestTimeout` |

The response bound is enforced through the body's stream reader as chunks
arrive, and the reader is cancelled when it is passed. `await response.text()`
on an unbounded body makes the client's memory safety a property of the server
behaving well, which is not worth assuming even on loopback.

Mutations are never retried automatically. A retried `POST …/start` is a second
lifecycle attempt, and the CLI already refuses the same thing for the same
reason.

### uint64 counters stay strings

Task 058 encodes uint64 counters as decimal strings because JSON numbers cannot
represent them. The browser keeps them strings: no `Number(record_count)`, no
`parseInt(next_sequence)` on an authoritative value. Gate limits are validated
as canonical decimal text, so `0` is valid and `18446744073709551615` neither
rounds nor loses a digit.

This is the one place where a browser client is *more* exposed than the CLI —
JavaScript has no integer type that holds these values — so it is called out
rather than left to care.

### The server owns lifecycle legality

The UI shows lifecycle actions and lets the server accept or reject them. It
does not carry a second state machine; `if (status === "running")` gating is a
duplicated rule, and ADR 0023 calls a rule that exists in one interface only a
defect.

Controls may be disabled for presentation reasons only: a request already in
flight, or a required field empty.

### The server owns the gate verdict

The displayed verdict is `response.gate.verdict`. The browser performs no
arithmetic on limits — no `added_count <= max_added_behaviors` anywhere — and
may render the five returned checks individually as presentation of returned
data.

## Realtime interaction model

Task 059's SSE endpoint, consumed with browser `EventSource`. Realtime
notifies; it is never the source of truth.

```text
subscribe
→ valid stream_ready
→ keep buffering arriving events
→ GET run
→ GET progress
→ apply authoritative snapshot
→ replay buffered events
→ live
```

Subscribe **before** the authoritative read. The reverse order loses anything
committed between the fetch and the subscription —
[SECURITY.md](../../SECURITY.md) records this as a property of the TUI, and it
is a property of the transport pattern, not of terminals.

`stream_ready` must carry `version == "1"`, `replay_available == false` and
`resync_required == true`. A known lifecycle or observation event before it
invalidates the connection. Unknown event kinds are ignored so a future
additive kind does not break an older page. Known events are validated for wire
integrity only — `version`, `kind` matching the SSE event name, `scope.run_id`
matching the watched run, and the correct payload half present — not
re-validated as domain objects.

### Bounds

| Bound | Value | On overflow |
|---|---|---|
| Pending frames during resync | 64 | abandon stream and resynchronize |
| Live observation rows | 100 | evict oldest |

The distinction is load-bearing and is inherited verbatim from task 061:
dropping a pending frame would be data loss a client could not describe, while
evicting a display row is a viewport. Live rows are labelled *Live observations
— current stream* and cleared on every reconnect, so two disconnected streams
are never joined into an apparent sequence.

### Handshake deadline

`EventSource` exposes heartbeat comments to the network layer but not as
events, so a server sending only heartbeats can hold a connection open forever
without ever synchronizing it. A finite handshake deadline (30 s) starts with
each generation and is cancelled by a valid `stream_ready`; heartbeats do not
extend it.

Three different questions, not to be conflated — the distinction task 061
had to discover:

```text
connection establishment   did the HTTP request succeed?
handshake establishment    did it become a Trustvian stream?
active liveness            has an established stream gone quiet?
```

### Generation ownership

One watched run and one `EventSource` at a time. Changing run, reconnecting or
leaving the live view closes the old `EventSource`, abandons in-flight fetch
results, clears the handshake timer and drops pending frames. A response from
generation *N* must never mutate generation *N+1*; an incrementing token
enforces it.

The page closes the `EventSource` itself on error or protocol failure before
deciding what to do. The browser's implicit retry must not become the
authoritative reconnect state machine — it has no notion of protocol validity
and would happily reconnect forever to a server failing the handshake.

### Reconnect

`250ms · 500ms · 1s · 2s · 4s · 5s cap`, reset after a successful handshake and
resync. One timer at a time; no unbounded counter. No jitter — one developer on
loopback; task 069 owns load behaviour.

### Terminal events

On `evaluation_completed`, `evaluation_failed` or `evaluation_cancelled`: one
final authoritative `GET run` + `GET progress`, so closing counts come from the
database rather than from summed SSE frames. No polling afterwards.

A terminal event arriving while the initial resync is still in flight must
still schedule that final read when replayed from the pending buffer. Task 061
found this bug once; the spec records it so it is not refound.

## UI capability boundaries

The browser may **format**. It may not **decide**.

Specifically it must not determine whether a behaviour is unsafe, whether a
candidate is promotable, whether a `BLOCK` decision means the evaluation
failed, whether critical risk implies gate failure, or whether completion
implies pass. Those are either server-computed or not modelled at all.

### Rendered fields are allowlisted

No generic object renderer. `Object.entries(serverValue).forEach(…)` is absent
from authoritative display paths and every rendered field is named explicitly.

This is a privacy boundary rather than a style preference. Task 050 and task
059 deliberately exclude prompts, completions, tool arguments,
`Event.Attributes`, `PolicyReason`, contributors and the raw `DecisionRecord`
from the realtime projection. When `/v1` later grows an additive field — which
the compatibility contract explicitly permits — a generic renderer would put it
on screen without anyone deciding to.

The live view renders task 059's projection (sequence, new behaviour, decision,
risk level, trust score, anomaly score, anomaly confidence, behaviour
descriptor) plus the authoritative snapshot (status, record count, distinct
behaviour count, behaviour completeness, next sequence).

### Gate wording

Factual only:

```text
Gate: PASS          Added behaviors: 2 / max 3
Gate: FAIL          Critical observations: 0 / max 0
```

Never *safe*, *unsafe*, *production-ready*, *secure*, *good agent* or *bad
agent*. A gate result is a deterministic outcome under caller-supplied limits,
not a universal safety judgement — the distinction task 056 built the gate
around. PASS and FAIL are distinguished by text and shape, never by colour
alone.

### Accessibility and interaction

Semantic HTML with `<label>` for every control; keyboard usable; visible focus
states; explicit loading, empty and connection states; errors announced
accessibly. No animation framework, and no charting library — simple factual
bars and tables, implemented locally, are sufficient for bounded counts.

## Security model

The threat a browser adds that a terminal did not: a server-supplied string can
become executable DOM, and a page can reach origins the developer never chose.
[SECURITY.md](../../SECURITY.md) gains a section mirroring the one task 061
added for the TUI.

**Server-originated text cannot create executable DOM.** Every value from the
API is untrusted: identifiers, names, failure reasons, candidate metadata,
behaviour descriptors, decision and risk strings, API error messages, and every
additive string field not yet added. Text reaches the DOM through
`textContent`; elements through `createElement`; `setAttribute` is used only
for attributes the page controls. `innerHTML`, `outerHTML`,
`insertAdjacentHTML`, `document.write`, `eval` and `new Function` do not appear
in shipped source, enforced by a source guard that strips comments and string
literals before matching, so the rule cannot be weakened into uselessness by a
false positive.

**Content Security Policy:**

```text
default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self';
img-src 'self'; font-src 'none'; object-src 'none'; base-uri 'none';
frame-ancestors 'none'; form-action 'self'
```

No `'unsafe-inline'`, no `'unsafe-eval'`, no `*`, no `https:`, no `data:` for
scripts, no external origin. No inline `<script>` and no inline `<style>` —
which is what makes the policy achievable without weakening it. Plus
`X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, and
`Cache-Control: no-store` on the HTML shell so a restarted binary never runs a
stale UI against a new database.

**No secret reaches the browser.** The runtime holds none: `runtime.json`
carries no token by [ADR 0035](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md),
and no credential, connection string or filesystem path is rendered or embedded
in JavaScript. The home view describes where state lives in prose without
reading a path from an API that does not expose one.

**CSP is not authentication.** It bounds what the page may do; it says nothing
about who may open it. The runtime stays unauthenticated, and task 070 owns
that.

## Local binding/exposure rules

Unchanged from task 062, and not relaxed because a browser is involved:

```text
default 127.0.0.1:0        numeric loopback only
no localhost hostname      resolution is not proof
no --allow-remote          no --insecure, no --public
no TLS contract            no authentication
```

A local browser reaches numeric loopback without help, so nothing about this
task creates pressure to widen the listener.

Server timeouts are preserved exactly:

```text
ReadHeaderTimeout 5s · ReadTimeout 30s · IdleTimeout 60s
MaxHeaderBytes 64 KiB · WriteTimeout 0
```

`WriteTimeout` stays zero. It is a *total* response deadline and would
eventually kill a healthy SSE stream; task 059 already owns per-write
deadlines.

### Same-origin composition

One listener, one server, one `ControlPlane`, one store, one bus:

```text
http://127.0.0.1:<ephemeral>/
    ├── /           WebUI shell
    ├── /assets/…   WebUI static assets
    └── /v1/…       existing API + SSE
```

Composed in `platform/localruntime` with an outer `http.ServeMux` routing
`/v1` and `/v1/` to `httpapi.Handler` and `/` to `webui.Handler`. Both `/v1`
patterns are registered because Go's `ServeMux` treats exact and subtree
patterns separately and only the second matches descendants.

Because the UI and API share an origin, **no CORS header is added**. Adding one
would be the only change needed to make the control plane reachable from any
web page the developer visits.

Mandatory invariants:

1. `/v1` never falls through to the HTML shell.
2. Unknown `/v1/…` paths keep their current API behaviour — verified by probe
   to be Go `ServeMux`'s plain-text `404 page not found`, **not** the API error
   envelope. Task 063 must preserve that, not "improve" it into HTML and not
   change it into an envelope, which would be an API change this task has no
   mandate for.
3. Unknown UI paths 404 as UI paths, never as a successful API response.
4. Existing API behaviour does not change.

### Static serving rules

Serve `/` and a known set of asset paths with explicit content types. Return
404 for anything else. No directory listing, no arbitrary filesystem read, no
path traversal, no disk web root, no user-supplied template name. Assets are
compiled in with `embed`, so there is no path for a request to name a file.

## State ownership

| State | Owner | Lifetime |
|---|---|---|
| Projects, agents, candidates, runs, evidence | SQLite via `ControlPlane` | durable |
| Realtime events | in-memory bus | ephemeral, never authoritative |
| Which IDs are open, live rows, form contents | browser page | until reload |

Browser state is **presentation state only**. No `localStorage`,
`sessionStorage`, `IndexedDB` or cookies hold platform state. A reload
legitimately forgets which IDs were open; the database stays authoritative.

The watched run ID may appear in the URL fragment for navigation. That is
navigation state, not platform state, and nothing is read back from it as
truth.

Realtime never becomes the database: every count displayed after a terminal
event comes from `GET progress`, not from summing observed frames.

## Error behavior

| Condition | Behaviour |
|---|---|
| Control plane unreachable | Connection error surfaced; no partial state shown as current; explicit retry offered. Not a gate result |
| Malformed API response | Treated as an operational failure; no partial render of a half-parsed document |
| Response exceeds 4 MiB | Reader cancelled, operational error, nothing rendered |
| Run not found | The API's 404 rendered as "not found" for that ID; other views unaffected |
| Evaluation incomplete | Progress shown as-is; absence shown as absent, never as zero |
| Gate not available | Compare requires two runs with evidence; the API's refusal is rendered verbatim rather than presented as a FAIL |
| SSE disconnect | Stream closed by the page, live rows cleared, bounded backoff reconnect, explicit reconnect action available |
| Handshake timeout, never live | Failed/disconnected state; automatic startup retry stops |
| Handshake timeout, previously live | Bounded backoff reconnect |
| Pending overflow during resync | Stream abandoned and resynchronized — never a silent drop |

Errors are rendered from the API's envelope as `error.code`, `error.message`
and HTTP status. No stack traces, no raw internal errors.

**An operational failure is never a gate FAIL.** Gate FAIL is only a successful
compare response whose `gate.verdict` is `fail`. The browser has no exit codes,
but the semantic separation is the same one task 060 encoded as exit 3 versus
exit 1, and confusing the two would mean a network error reading as a rejected
candidate.

The distinction between *absent* and *zero* is preserved throughout: task 053
built the aggregate around it, and a mean that is unknown renders as unknown
rather than as `0`.

## Testing requirements

**Static handler.** `GET /` → 200 HTML; `HEAD /`; each known asset with its
correct content type; unknown asset → 404; traversal attempts (`../`,
percent-encoded, absolute, backslash) → 404 with no file outside the embedded
set; no directory listing. Security headers asserted present; CSP asserted to
contain none of `unsafe-inline`, `unsafe-eval`, `*` or an external host.

**Architecture boundary.** The `webui` package's import graph contains no
`trustvian-platform`, no root module, no `database/sql` and no driver —
checked with `go list -deps`, the same technique
`scripts/check-platform-boundary.sh` already uses. Source must not name
`ControlPlane`, `SQLiteStore`, `EvaluateEvaluationGate`, `EvaluationScorecard`,
`BehaviorDiff` or `BehaviorCollector`. `NewHandler` must take no
control-plane parameter — asserted structurally, so the mutation of adding one
fails to compile the guard.

**Routing coexistence**, against the composed runtime: `/` → WebUI;
`/assets/…` → assets; `/v1/…` → API; `/v1/realtime` → SSE. Critically, an
unknown `/v1/…` path returns the API's plain-text 404 and **not** `index.html`.

**Existing API regression.** The whole `httpapi` suite runs unchanged. Status
codes, JSON bodies, error envelopes, SSE behaviour and request limits may not
move because a UI landed.

**Web source safety.** Shipped UI source, with comments and string literals
stripped, contains none of `.innerHTML`, `.outerHTML`, `insertAdjacentHTML`,
`document.write`, `eval(`, `new Function`. External references (`http://`,
`https://`, protocol-relative `//`) are rejected in HTML, JS and CSS where they
would load a dependency. Documentation prose is not scanned.

**Escaping/XSS.** `<img src=x onerror=alert(1)>`,
`</script><script>alert(1)</script>` and `javascript:alert(1)` travel through
the real API as project names, failure reasons and behaviour descriptors, and
the rendering path treats them as text.

**Explicit-field privacy.** A fixture carrying
`prompt: "SECRET_PROMPT_DO_NOT_RENDER"`,
`attributes.tool_argument: "SECRET_TOOL_ARGUMENT"` and
`future_field: "SECRET_ADDITIVE_FIELD"` is checked against the renderer's
allowlists; none of those values may be reachable.

**Realtime contract.** Source and fixtures pin: subscribe before resync,
pending capacity 64, display capacity 100, `stream_ready` required, unknown
kind tolerated, wrong-run event rejected, terminal event triggers one final
resync, no polling interval, no replay cursor, no `Last-Event-ID`.

**Race.** `go test -race` across all four modules, per the repository baseline.
Realtime generation handling is the concurrency-sensitive part on the Go side;
the browser's generation logic is single-threaded by the event loop and is
covered by determinism of the state transitions instead.

**End-to-end**, against a real ephemeral loopback runtime with the control
plane not mocked:

```text
start local runtime
→ create project, agent, candidate, run
→ ingest evidence through the real /v1 path
→ open the WebUI at /
→ retrieve the evaluation
→ inspect behavioral diff
→ inspect scorecard
→ inspect hard-gate result
```

Plus: only one listener exists; restart preserves SQLite state; the discovery
schema is unchanged.

This is an HTTP-level test. No browser automation framework, no headless
Chrome, no Node — the assertions are about what the server serves and what the
API returns, and the rendering rules are covered by the source and allowlist
guards above.

### Testing the browser code without a JS runtime

State transitions that matter — handshake, resync ordering, pending overflow,
generation replacement — are written as small deterministic functions separated
from DOM callbacks, so they can be pinned by source structure and fixtures
rather than by executing them. Where a rule cannot be proven that way, the
spec prefers a Go-side guard over adding Node to the toolchain.

This is the weakest part of the test strategy and is named as such. It is
accepted for a minimal, bounded UI on loopback; a larger WebUI should revisit
it rather than inherit it.

### Mutation tests

WebUI handed a `ControlPlane`; `/v1` falling through to `index.html`;
`innerHTML` introduced; CSP weakened with `unsafe-inline`; an external CDN
script added; the response bound removed; the pending SSE bound removed; live
rows grown without limit; state fetched before the `EventSource` opens; the
gate verdict recomputed in JavaScript; periodic `/progress` polling added;
unknown additive fields rendered generically. Each must fail a named test.

## Benchmarks and resource requirements

No Go benchmark is warranted: the server-side addition is static file serving
from memory, and the engine's hot paths are untouched.

The resource requirements that matter are client-side bounds, and they are
pinned as constants rather than measured:

| Bound | Value |
|---|---|
| Request body | 256 KiB |
| Response body | 4 MiB |
| One-shot timeout | 30 s |
| Pending SSE frames | 64 |
| Live display rows | 100 |
| Handshake deadline | 30 s |
| Reconnect backoff cap | 5 s |

Binary size grows by the embedded asset bytes. The release dry run and the
existing CGO-free cross-build targets (`linux/amd64`, `linux/arm64`,
`darwin/amd64`, `darwin/arm64`, `windows/amd64`) must continue to pass with
`CGO_ENABLED=0`, and no frontend build step may be introduced into any of them.

## Documentation requirements

New: `docs/webui.md` — starting the runtime, opening the printed Web URL, what
the UI can do, explicit-ID navigation and why there is no list, live-view
semantics and why live rows are not history, compare semantics, the same-origin
model, the loopback security warning, and the absence of authentication.

Updated: `docs/ARCHITECTURE.md` (the WebUI as third adapter),
`docs/SECURITY.md` (a threat section mirroring the TUI's),
`docs/compatibility.md` (classification below), `docs/ROADMAP.md`,
`docs/local-development.md`, `docs/tasks/v1.0/README.md`,
`docs/adr/README.md`, and `CHANGELOG.md` per existing convention.

### Compatibility classification

| Surface | Class |
|---|---|
| `/v1` request/response semantics | versioned machine contract, unchanged |
| Realtime protocol | task 059's contract, unchanged |
| Gate meaning | server-owned, unchanged |
| `/` and static asset paths | local-runtime interface, not the versioned API |
| Visual layout, DOM structure, CSS classes, element IDs | observational |

DOM structure and CSS class names are **not** a compatibility promise.
Automation reads `/v1`; nothing should scrape the page. This mirrors how task
061 classified the TUI's rendered dashboard.

## Open question: collection semantics

A browser conventionally opens with a list, and this task deliberately ships
without one. The decision and its evidence:

No list capability exists at any layer. `ControlStore` and `EvaluationStore`
expose only by-ID reads; there is no `ListEvaluationRuns`. The `runs` table has
`created_at TEXT` with no index and no monotonic ordering column, so a stable
cursor would need `(created_at, id)` and a new index. `httpapi`'s `routes()`
states the intent directly:

> No collection GET. Task 059+ has not specified sort order, cursors, limits or
> scoping, and a list route added here would freeze all four by accident.

And [compatibility.md](../../compatibility.md) classifies platform `/v1` route
shapes as **STABLE**: changing one after `v1.0` requires a major bump or a new
path version.

Adding a list route here would therefore freeze scope, sort order, cursor
semantics and limit — four undesigned decisions — under a stable contract,
before task 064's PostgreSQL backend exists to validate the cursor against a
second store, and before tasks 065 and 067 exist to say what filtering is
actually needed.

So task 063 navigates by caller-known ID, consistent with
[ADR 0033 §4](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) ("IDs
belong to the caller"). Creating an entity opens the returned entity for the
current page session; that is a convenience, not a catalog, and is not
persisted or called history. The UI says *Open by ID*, never *Search* or
*Browse all*, so it does not imply a surface that does not exist.

**This is the most likely review objection to this specification, and it is
deliberately left open rather than resolved by guessing.** Collection semantics
should be designed once, against concrete filtering requirements, in or
alongside the milestone that first needs them.

## Acceptance criteria

1. The WebUI is served from the task 062 listener, same-origin with `/v1`,
   with no second listener and no second `ControlPlane`.
2. `webui.NewHandler` receives no control plane, and the package's dependency
   graph contains no platform, root-module, `database/sql` or driver package.
3. No collection/list route and no direct store access is added.
4. The browser reaches the platform only through `/v1` and SSE.
5. Unknown `/v1/…` paths keep their existing 404 behaviour and never return
   the HTML shell.
6. Strict CSP with no inline script, no inline style, no external origin; plus
   `nosniff`, `no-referrer`, and `no-store` on the shell.
7. No server-supplied string can create executable DOM, proven by fixture.
8. Gate verdicts come from `gate.verdict`; the browser computes none.
9. uint64 counters remain exact decimal strings end to end.
10. Realtime preserves subscribe-before-resync, bounded pending (64) and
    display (100) sets, a finite handshake deadline, and generation-scoped
    cancellation.
11. No new dependency in any module; no frontend build step; CGO-free
    cross-builds unchanged.
12. Existing API, CLI, TUI, runtime, persistence-schema and discovery
    behaviour is unchanged.

## Explicit dependencies

| Depends on | For |
|---|---|
| [057](057-local-platform-persistence.md) | durable control and evaluation state |
| [058](058-local-control-plane-api-and-ingest.md) | every `/v1` route the browser calls |
| [059](059-realtime-infrastructure.md) | the SSE endpoint and its projection |
| [061](061-terminal-dashboard.md) | the realtime client pattern reused here |
| [062](062-integrated-local-developer-workflow.md) | the listener, composition root and runtime output |
| [ADR 0022](../../adr/0022-core-platform-boundary.md) | core/platform boundary |
| [ADR 0023](../../adr/0023-interfaces-are-adapters.md) | interfaces are adapters |
| [ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md) | realtime is not authoritative |
| [ADR 0033](../../adr/0033-developer-cli-is-a-thin-http-adapter.md) | client-adapter precedent |
| [ADR 0034](../../adr/0034-tui-is-a-bounded-realtime-http-client.md) | bounded realtime client precedent |
| [ADR 0035](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md) | composition root and discovery |

Blocks nothing. Tasks 064–072 do not depend on this one.

## Files and modules expected to change

New:

```text
platform/webui/handler.go
platform/webui/handler_test.go
platform/webui/architecture_test.go
platform/webui/assets/index.html
platform/webui/assets/app.js          (may be split into api/realtime/render)
platform/webui/assets/styles.css
docs/adr/0036-webui-is-a-same-origin-adapter-over-v1.md
docs/webui.md
```

Modified:

```text
platform/localruntime/runtime.go          outer mux composition
platform/localruntime/runtime_test.go     routing coexistence
platform/localruntime/endtoend_test.go    WebUI in the real E2E path
platform/cmd/trustvian-local/main.go      the "Web:" output line
platform/cmd/trustvian-local/main_test.go
docs/ARCHITECTURE.md  docs/SECURITY.md  docs/compatibility.md
docs/ROADMAP.md  docs/local-development.md
docs/tasks/v1.0/README.md  docs/adr/README.md  CHANGELOG.md
```

Must **not** change: the core engine, baseline, fingerprint or policy logic;
the platform persistence schema; `/v1` semantics; the discovery schema; CLI
exit codes; any `go.mod` or `go.sum`.

Expected dependency delta: **none**.
