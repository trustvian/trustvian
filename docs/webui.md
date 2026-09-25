# Web control plane

A browser interface to the local control plane. One command starts it; the URL
is printed.

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

The API and the WebUI are the **same endpoint**. The UI is served from the same
loopback listener that answers `/v1`, which is why `runtime.json` still carries
one URL and needs no second field.

## What it can do

| Section | Actions |
|---|---|
| **Open** | Open a project, agent, candidate or evaluation run by ID |
| **Project** | Create; view `id`, `name` |
| **Agent** | Create; view `id`, `project_id`, `name` |
| **Candidate** | Create with the fixed metadata fields; view them |
| **Evaluation** | Create; view identity, status, timestamps, failure reason; start, complete, fail, cancel; read authoritative progress |
| **Live** | Watch one run over SSE |
| **Compare** | Compare two runs and read the gate, diff and scorecard |
| **Promotion** | Record a promotion decision; open one by ID; page a project's history |

What it deliberately cannot do: ingest decision records (that is the job of the
application under evaluation, through the CLI or the API), browse event history
(task 067), or manage environments (task 065).

## Navigating by ID

There is no search, and no way to browse the hierarchy. That is a deliberate
omission, not an unfinished screen.

The control plane has no collection route for the hierarchy —
`GET /v1/projects` and its siblings do not exist — because pagination, sort
order, cursor semantics and scoping have not been designed for them, and `/v1`
route shapes are a stable contract once published. Adding a list endpoint so a
browser could open with one would freeze four undesigned decisions at once.

Two project-scoped collections do exist, because two entities had a consumer
that needed one: a project's environments (task 065) and its promotion history
(task 066). Both traverse by an immutable key in byte order with the server
bounding every page, and neither opens the hierarchy — you still need the
project ID to ask.

So you open things by the ID you already know, which is the same ID the CLI
uses:

```text
Open by ID    project · agent · candidate · evaluation run
```

Creating something opens it for the current page session. That is a convenience
only — it is not a catalog, it is not history, and **a reload forgets it**.
Nothing about which IDs you opened is stored in the browser; the control-plane
database is the only source of truth.

> **Planned, not shipped.**
> [Task 074](tasks/v1.0/074-zero-input-live-behavior-webui.md) specifies a
> default Live view that discovers active agents from the realtime stream and
> browses the hierarchy through bounded collection routes, so opening the page
> while an agent is running needs no identifier at all. It is specified and not
> implemented; everything described on this page is what ships today.

## The live view

Watching a run does this, in this order:

```text
subscribe to the stream
→ wait for a valid stream_ready
→ keep buffering events that arrive
→ read the run and its progress from the database
→ apply that authoritative snapshot
→ replay the buffered events
→ live
```

The order matters. Reading state first and subscribing afterwards would lose
anything committed in between.

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

Supply two run IDs and all three gate limits. Limits are required, and `0` is
valid.

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
- [ADR 0036](adr/0036-webui-is-a-same-origin-adapter-over-v1.md) — why the UI is
  a static same-origin adapter rather than a server-rendered or framework app
- [Task 063](tasks/v1.0/063-minimal-web-control-plane.md) — the specification
