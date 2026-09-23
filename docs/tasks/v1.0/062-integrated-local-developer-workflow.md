# 062 — Integrated Local Developer Workflow

Status: implemented
Depends on: [058](058-local-control-plane-api-and-ingest.md),
[059](059-realtime-infrastructure.md), [060](060-developer-cli.md),
[061](061-terminal-dashboard.md)

## Objective

Make the local platform startable as one coherent environment.

Every piece already exists — engine, decision record, SQLite store, control
plane, `/v1` API, realtime bus, CLI, TUI — and nothing composes them. There is
no listener, so `--api-url` has nothing to point at unless a developer writes
their own `main`.

Task 062 adds the composition root and the discovery that lets the shipped
clients find it:

```text
Terminal A          make local
Terminal B          ./bin/trustvian project create --id proj-1 --name Checkout
                    ./bin/trustvian eval start --id run-1
                    ./bin/trustvian tui --run-id run-1
```

No `--api-url` in the local path. An explicit one keeps working exactly as
before.

## Architecture

```text
Application / Agent
      │  Engine.Analyze
      ▼
DecisionRecord
      │  HTTP /v1
      ▼
┌──────────────────────────────────────┐
│ trustvian-local                      │
│   SQLite ← ControlPlane              │
│              ├─ Realtime Bus ── SSE ─┼──▶ TUI
│              └─ HTTP API ────────────┼──▶ CLI
└──────────────────────────────────────┘
```

The engine stays **outside** the runtime. The producer analyzes, serializes a
`DecisionRecord`, and posts it; the server never runs a second engine.

## Module Boundary

This is the constraint everything else bends around.

```text
trustvian-platform ──▶ root public API ──▶ engine
```

The root module must not begin depending on `trustvian-platform`, so the
composition cannot live in `cmd/trustvian`. It lives in the platform module
instead, as `platform/cmd/trustvian-local`, which may import the platform
freely because it is already inside it.

The CLI and TUI stay HTTP/SSE clients. They learn *where* the server is from a
file, not from a Go import — that is the whole reason discovery exists rather
than a function call. See
[ADR 0035](../../adr/0035-local-runtime-composes-platform-without-reversing-modules.md).

## Local Runtime

`platform/localruntime` owns lifecycle and nothing else — no behavioral logic,
no second service implementation:

```go
store, _   := platform.OpenSQLiteStore(ctx, dbPath)
bus        := platform.NewInMemoryRealtimeBus()
plane, _   := platform.NewControlPlane(store, store, store,
                  platform.WithRealtimePublisher(bus))
handler, _ := httpapi.NewHandler(plane, httpapi.WithRealtimeSubscriber(bus))
```

Exactly one of each. `platform/cmd/trustvian-local` adds flags and signal
handling on top; the runtime package owns contexts and resources, the
executable owns the process.

## Loopback Listener

Default `127.0.0.1:0` — loopback, OS-assigned port.

`--listen` accepts only numeric loopback (`127.0.0.1:<port>`, `[::1]:<port>`,
port 0 valid). `0.0.0.0`, `::`, private ranges, public addresses and hostnames
are refused. There is no `--allow-remote`, `--insecure` or `--public`: this
runtime is unauthenticated, and loopback *is* the safety boundary. Task 070
owns a remotely exposed authenticated platform.

Hostnames are not accepted even when they would resolve to loopback — resolution
is not proof, and `localhost` is whatever the resolver says it is.

The port is ephemeral on purpose. Freezing one because examples need a number
would make it a compatibility surface nobody chose.

## State Directory

```text
.trustvian/                 0700 where POSIX permissions apply
.trustvian/platform.db      control-plane state
.trustvian/runtime.json     endpoint discovery, 0600
```

Project-local, so the workflow is visible, removable, and isolated per working
directory. Not `/tmp`, not the OS temp dir, not `$HOME` implicitly.

The permission bits are defence in depth, not a claim: platforms without POSIX
semantics do not implement them identically.

## SQLite Ownership

`.trustvian/platform.db` holds project, agent, candidate, evaluation run,
aggregate evidence, behavior snapshot and ingest cursor.

It is **not** the engine's baseline store. Baseline learning does not route
here, no core tables join the platform schema, and no platform tables join the
core store. The two persistence concerns stay separate, and a test asserts the
platform database contains no baseline table.

## Realtime Ownership

The runtime owns one `InMemoryRealtimeBus`, passed to the control plane as a
publisher and to the HTTP handler as a subscriber. Nothing else constructs one.
Realtime is ephemeral by [ADR 0032](../../adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md)
and does not survive restart — deliberately.

## HTTP Server Bounds

```text
ReadHeaderTimeout  5s
ReadTimeout        30s
IdleTimeout        60s
MaxHeaderBytes     64 KiB
WriteTimeout       0   ← deliberate
```

`WriteTimeout` is a *total* response deadline. Task 059's SSE streams are
long-lived by design and already carry a finite per-write deadline through
`ResponseController.SetWriteDeadline`, so a server-wide write timeout would
eventually kill a healthy dashboard for being healthy. Leaving it zero is the
correct configuration, not an oversight.

## Startup

Fail-closed, in order:

```text
validate config → create state dir → open SQLite → bus → control plane
→ handler → bind listener → serve → publish discovery → ready
```

Discovery is published **after** the listener is bound, never before: a client
that finds the file must find an endpoint that exists. If publishing fails,
startup fails and everything opened so far is closed.

## Runtime Discovery

```json
{"version": "1", "api_url": "http://127.0.0.1:54321"}
```

Two fields. No maps, no metadata, no token, no secret, no identifiers, no
database path.

Written atomically — temp file in the same directory, complete bounded JSON,
mode set, closed, renamed — so no reader can observe a partial file.

It means *a local runtime announced this endpoint*. It is not platform data,
not security evidence, not authentication, not service discovery for
production, and it is never persisted into SQLite.

An existing file is not proof of a live server. Before replacing one, the
runtime probes the advertised endpoint with a bounded TCP dial and refuses to
start if something is already listening in the same state directory. The
address is validated first, by the same rule the clients apply: the probe must
never be the way a checked-out repository gets the local runtime to open an
outbound connection to an address of its choosing. A
malformed, stale or unreachable file is replaced. This is single-node local
development; [task 069](069-*) owns multi-node concerns, and no distributed
locking is introduced here.

On clean shutdown the file is removed **only if it still describes this
runtime's URL**. A stale process must never delete a newer runtime's endpoint.

## CLI/TUI Resolution

```text
--api-url present → Task 060 validation → use it
--api-url absent  → read ./.trustvian/runtime.json → strict validation → use it
```

Presence on the command line decides, not emptiness. `--api-url ""` is present
and invalid: usage (`2`), with no discovery file read. An empty value in a
script is an unset variable, and falling back there would run the command —
including a gate decision — against a control plane the caller never chose.

Explicit input always wins; a working directory can never redirect a command
that named its endpoint. No environment variable, no parent-directory search,
no `$HOME`, no network scan — exactly `./.trustvian/runtime.json`.

A **discovered** URL is held to a stricter rule than an explicit one: `http`
only, numeric loopback host only, no credentials, no path beyond `/`, no query,
no fragment. A checked-out repository must not be able to point a developer's
mutation commands at `https://attacker.example`.

The file is read under a 4 KiB bound — two fields need far less, and the input
is not necessarily trustworthy. The bound is measured on the bytes actually
read, with no second stat-based check in front of it: the two can only disagree
in the window where the file grows between them, and a redundant outer layer
would hide the real one from every test.

It must also be **exactly one** JSON document. Decoding a single value from a
stream stops at the end of the first one, which accepts a file whose second
document names a different endpoint.

Unknown fields inside version 1 are tolerated; an unknown `version` fails
closed.

## Exit Codes

Unchanged everywhere, with one classification added:

```text
--api-url omitted, no runtime found  → 3   operational
--api-url omitted, runtime malformed → 3   operational
--api-url given but invalid          → 2   usage
```

The distinction is the point: after task 062, `trustvian eval get --id run-1`
is a *valid* invocation. If no runtime is running, the command was right and
the environment was not — that is operational, not a usage error. Exit `1`
remains gate FAIL for `eval compare` alone.

## Shutdown

```text
close realtime bus → server.Shutdown(5s) → server.Close if it times out
→ SQLite close → remove owned discovery file
```

The bus closes first so active SSE handlers exit rather than holding shutdown
open. Signals (`os.Interrupt`, `SIGTERM`) are the executable's; the runtime
package takes a context.

No daemonization — no background mode, PID file, systemd unit, launchd job or
Windows service. The runtime runs in the foreground and the shell owns
backgrounding.

## Engine Producer Boundary

No raw event route is added. `POST /v1/events`, `/v1/analyze` and `/v1/engine`
do not exist, because task 058 chose `DecisionRecord` as the platform boundary
and a server-side engine would be a second one.

An end-to-end test drives the real chain — `event.Event` → real
`trustvian.Engine` with `WithLearningScope` matching the run's behavioral
profile → `Result.DecisionRecord()` → real HTTP ingest → SQLite → realtime →
progress — over a real loopback listener with an ephemeral port. Nothing in
that path is mocked.

## Tests

Full end-to-end through the real API; restart durability against the same state
directory; discovery appearing only after bind and removed on shutdown; CLI and
TUI resolving through discovery, including a write command; explicit URL
overriding discovery; a malicious non-loopback discovery file reaching zero
external requests; size bound at and over the limit; unknown version rejected
and additive fields tolerated; stale shutdown not deleting a newer file;
non-loopback `--listen` refused; resource cleanup for listener, bus and store;
and an architecture guard that the root module still cannot import the
platform.

No sleep is a sequencing primitive — listener readiness, discovery publication,
`stream_ready` and shutdown completion are all observed directly.

## Mutation Tests

Root importing the platform; default listener on `0.0.0.0`; discovered
non-loopback accepted; an explicitly empty `--api-url` falling back to
discovery; a stream decoder accepting a trailing second document; the read
bound removed; unknown fields rejected; the liveness probe dialling before
validation; discovery published before bind; size bound removed;
discovery overriding an explicit URL; missing runtime classified as usage 2;
version 2 silently accepted; discovery left behind after shutdown; stale
shutdown deleting a newer file; publisher or subscriber unwired; in-memory
SQLite instead of file-backed; restart persistence removed; a global
`WriteTimeout` that kills SSE; bus not closed before shutdown; a raw
`/v1/events` route; platform SQLite used as the engine baseline store.

## Documentation

`docs/local-development.md` (new), plus `docs/platform-cli.md`, `docs/tui.md`,
`docs/ARCHITECTURE.md`, `docs/SECURITY.md`, `docs/compatibility.md`,
`docs/ROADMAP.md`, `CHANGELOG.md` and both indexes.

## Acceptance Criteria

1. Root module does not import or require `trustvian-platform`.
2. Composition lives in the platform module; HTTP remains the client boundary.
3. Listener is loopback-only with an ephemeral default port.
4. Platform SQLite is file-backed and separate from core baseline storage.
5. Discovery is published after bind, bounded, versioned, loopback-only, and
   removed only by its owner.
6. Explicit `--api-url` always wins; missing runtime is exit 3.
7. Real engine → decision record → HTTP ingest → SQLite → realtime works, and
   survives restart.
8. Graceful shutdown closes bus, server, store and discovery file.
9. No new dependency in either module.

## Non-Goals

No WebUI (063), no PostgreSQL platform backend (064), no environment model
(065), no promotion (066), no event history (067), no multi-node (069), no
authentication or TLS (070). No CORS, no raw event endpoint, no daemonization,
no core or published-module change.
