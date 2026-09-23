# 0036 — The WebUI is a same-origin static adapter over `/v1`

**Status:** Accepted

## Context

[Task 063](../tasks/v1.0/063-minimal-web-control-plane.md) adds the first
browser interface to the local control plane.

[ADR 0023](0023-interfaces-are-adapters.md) already decided the shape: CLI, TUI
and WebUI are peers, each an adapter, and none carries its own copy of
evaluation, diff, scorecard or gate logic. It also named the specific failure
mode — each interface is "a plausible place to *just compute the gate result
here*", and three implementations of a gate rule are three behaviours, with the
one a user believes being whichever interface they happened to open.

The CLI ([ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md)) and TUI
([ADR 0034](0034-tui-is-a-bounded-realtime-http-client.md)) both held that
line, and in the CLI's case the module boundary enforced it: importing
`internal/*` across modules is a compile error, so "recompute it here" was not
available.

A browser interface arrives with more pressure than either. Web convention
supplies a framework, a client-side data layer, and a habit of deriving
display values locally. And unlike the CLI, there is no module boundary to
lean on — a `platform/webui` package sits *inside* the platform module, where
every service is one import away.

Two questions had to be settled before any code: where the HTML comes from,
and what the browser is allowed to know.

## Decision

**The WebUI is a static browser application, embedded in the platform binary,
served same-origin from the Task 062 listener, that reaches the platform only
through `/v1` and SSE.**

### 1. The server-side package holds no control-plane authority

`platform/webui` serves embedded assets and sets headers. Its constructor takes
no arguments:

```go
func NewHandler() (http.Handler, error)
```

Not `NewHandler(plane *platform.ControlPlane)`. The signature is the boundary:
a package that is never handed platform authority cannot accumulate it, and the
mutation that would grant it does not compile against the architecture guard.

Its dependency graph contains only `embed`, `io/fs` and `net/http` — no
`trustvian-platform`, no root module, no `database/sql`, no driver.

### 2. Static assets rather than `html/template`

Server-rendered HTML was the obvious alternative, and it is rejected for one
reason: it requires giving the Go handler control-plane access.

The moment the handler holds a `ControlPlane`, §1's compile-time guarantee
becomes a code-review guarantee, and rendering code sits one import away from
every service that computes a verdict. ADR 0023's warning is exactly that each
interface is a plausible place to compute the gate result. A template handler
that already holds the plane is the most plausible of the three, because the
data it needs and the data it could derive arrive through the same object.

Contextual auto-escaping is a genuine advantage of `html/template`, and giving
it up is a real cost, not a free choice. It is paid for deliberately: a strict
CSP, a text-only rendering rule, and a source guard that fails on `innerHTML`.
That cost is bounded and testable. Re-establishing an adapter boundary after it
has been crossed is neither.

Secondary: the repository has no templates today, and `embed` keeps the release
artifact self-contained either way.

### 3. The browser is a third `/v1` client

One wire contract, three clients. The browser calls the same routes the CLI
calls, gets the same JSON, and is bound by the same rules — including the ones
that exist because the contract is versioned: tolerate unknown fields, treat
uint64 counters as decimal strings, never turn an operational error into a gate
result.

This is what makes the WebUI cheap to keep correct. A change that breaks it
breaks the CLI and TUI in the same release, rather than being discovered later
by the one client nobody automated.

### 4. No frontend framework and no build toolchain

No React, Vue, Svelte, Angular, HTMX, Alpine or jQuery; no npm, lockfile,
bundler or CDN reference. Semantic HTML, plain CSS, vanilla JavaScript,
`fetch`, `EventSource`, DOM APIs.

The UX is forms, tables and an event stream over bounded, fixed-shape state.
None of it needs a reconciling renderer or a component model. Against that, a
framework adds a toolchain to install, pin and audit, and a supply-chain
surface on a security product's control plane.

It would also make §6's CSP either dishonest or impossible: the common CDN and
inline-bundle patterns require `unsafe-inline`, and a policy with that in it
does not mean what it appears to mean.

Dependency delta: zero, in every module.

### 5. One listener, one origin, no CORS

The WebUI extends the Task 062 listener rather than adding a port:

```text
http://127.0.0.1:<ephemeral>/
    ├── /           WebUI
    ├── /assets/…   assets
    └── /v1/…       existing API + SSE
```

Because UI and API share an origin, no CORS header is needed — and none is
added. That is not an omission to fix later: a permissive
`Access-Control-Allow-Origin` is the single change that would make an
unauthenticated local control plane reachable from any page the developer
happens to visit. Task 070 owns remote access, and it owns authentication in
the same breath.

`/v1` is routed to the API handler before `/` reaches the WebUI, so an unknown
API path keeps its existing 404 rather than returning the HTML shell — which
would turn every client's typo into a 200 full of markup.

### 6. Untrusted text, strict policy

Every value from the API is untrusted, including ones the developer typed
themselves in another window. Text reaches the DOM through `textContent` and
elements through `createElement`; `innerHTML`, `outerHTML`,
`insertAdjacentHTML`, `document.write`, `eval` and `new Function` are absent
from shipped source and a guard enforces it.

```text
default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self';
img-src 'self'; font-src 'none'; object-src 'none'; base-uri 'none';
frame-ancestors 'none'; form-action 'self'
```

No inline `<script>` and no inline `<style>`, which is what makes the policy
achievable without `'unsafe-inline'`.

CSP is browser hardening, not authentication. It bounds what the page may do
and says nothing about who may open it. The runtime remains unauthenticated
and loopback-only.

### 7. Rendered fields are allowlisted, not enumerated

No generic object renderer on authoritative display paths.

This is a privacy boundary rather than a style preference. Tasks 050 and 059
deliberately exclude prompts, completions, tool arguments, `Event.Attributes`,
`PolicyReason`, contributors and the raw `DecisionRecord` from what the
platform exposes. The compatibility contract permits `/v1` to grow additive
fields at any time. A renderer that walks whatever object it receives would put
the next such field on screen without anyone deciding to — and the decision to
show something is exactly what the privacy boundary consists of.

### 8. Realtime notifies; the database decides

Task 063 inherits [ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md)
whole: subscribe before the authoritative read, bounded pending frames (64,
overflow abandons the stream and resynchronizes), bounded display rows (100,
oldest evicted), no replay, no `Last-Event-ID`, and one final authoritative
read after a terminal lifecycle event so closing counts never come from summed
frames.

The browser adds one bound the TUI did not need. `EventSource` surfaces
heartbeat comments to the network layer but not as events, so a server sending
only heartbeats holds a connection open indefinitely without ever synchronizing
it. A finite handshake deadline, cancelled by a valid `stream_ready` and not
extended by heartbeats, separates three questions that are easy to conflate:

```text
connection establishment   did the HTTP request succeed?
handshake establishment    did it become a Trustvian stream?
active liveness            has an established stream gone quiet?
```

The page also closes the `EventSource` itself on protocol failure. The
browser's implicit retry has no notion of protocol validity and would reconnect
forever to a server that never completes the handshake; it must not be the
authoritative reconnect state machine.

### 9. Navigation is by caller-known ID

The minimal WebUI has no list view, because the platform has no list
capability at any layer and designing one here would freeze four undesigned
semantics — scope, sort order, cursor, limit — under a route shape the
compatibility contract classifies as STABLE.

Consistent with ADR 0033 §4, IDs belong to the caller. Creating an entity opens
the returned entity for the current page session; that is a convenience, not a
catalog, and is not persisted. The UI says *Open by ID* rather than *Search*,
so it does not imply a surface that does not exist.

### 10. The browser stores no platform state

No `localStorage`, `sessionStorage`, `IndexedDB` or cookies. A reload
legitimately forgets which IDs were open, and the control-plane database stays
the only source of truth. A watched run ID in the URL fragment is navigation
state, never read back as truth.

## Alternatives considered

**`html/template` server-side rendering.** Free contextual auto-escaping, most
views need no JavaScript, and it is idiomatic in a Go repository. Rejected
because it requires handing the rendering package a `ControlPlane`, which
dissolves the one mechanism that makes the adapter boundary structural rather
than cultural — and which ADR 0023 identified as the exact place this
architecture fails. The escaping benefit is replaced by a strict CSP and a
source guard; the boundary has no replacement.

**A hybrid: templated shell, `fetch` for data.** Splits the difference and
keeps most of the escaping benefit. Rejected as the worst of both: two
rendering paths to audit, and the handler still needs enough access to render
the shell's navigation meaningfully. The seam would be re-argued with every new
view.

**A single-page application with a framework and bundler.** Conventional, and
would make a much larger WebUI pleasant. Rejected as unjustified for forms and
tables over bounded state, and actively harmful to the CSP and supply-chain
posture a security product's control plane should have. If a future WebUI
genuinely outgrows vanilla DOM code, that is a new decision with new evidence —
not a default to adopt now.

**A separate port for the UI.** Simpler routing, and no risk of `/v1` colliding
with a UI path. Rejected because it doubles the listener surface of an
unauthenticated service, needs a second discovery field, and forces CORS — the
one header that turns a loopback control plane into something any visited page
can reach.

**A list endpoint added for the browser.** The conventional way a UI opens.
Rejected in Task 063 because no list capability exists at store, service or
HTTP level; the `runs` table has no index or monotonic ordering column for a
stable cursor; and `/v1` route shapes are STABLE once published. Four
undesigned decisions would be frozen before tasks 064, 065 and 067 exist to
say what they should be.

## Consequences

The WebUI cannot show a developer anything they cannot name. Open-by-ID is the
honest interface to a platform without collection semantics, and it will feel
sparse next to tools that list everything. That friction is the visible cost of
not guessing, and it should be resolved by designing collection semantics once
— not by adding a route to make this screen feel finished.

Losing `html/template` means XSS safety is now a property of discipline plus
tooling rather than of the templating engine. The source guard, the CSP and the
fixture tests are therefore load-bearing, not decorative: if they are weakened,
the protection is gone and nothing else notices.

The browser is a client of a versioned contract, so it inherits the obligation
to tolerate additive fields. Combined with §7's allowlist, that means adding a
field to `/v1` will never make it appear in the UI by accident — someone has to
decide to render it, which is the intended cost.

There are now three interfaces to keep consistent. That was already true when
ADR 0023 was written; Task 063 is where it starts being paid. The mitigation is
that all three consume the same routes, so a contract change breaks them
together rather than silently leaving one behind.

Static assets live in the repository as source, not as build output, so they
are reviewed like code and diff like code. There is nothing to regenerate, and
no generated artifact that can drift from its input.
