# Local development

One command starts the whole local platform; everything else finds it on its
own.

## Quick start

```bash
make local
```

Then, in another terminal **in the same directory**:

```bash
./bin/trustvian project create --id proj-1 --name Checkout
./bin/trustvian agent create --id agent-1 --project-id proj-1 --name "Checkout agent"
./bin/trustvian candidate create --id cand-1 --agent-id agent-1
./bin/trustvian eval create --id run-1 --candidate-id cand-1 \
    --environment local --behavioral-profile checkout
./bin/trustvian eval start --id run-1
./bin/trustvian tui --run-id run-1
```

No `--api-url` anywhere. `make local` publishes the endpoint it bound, and
every client in that directory reads it.

Stop the runtime with `Ctrl-C`.

## What `make local` starts

```text
Application / Agent
      │  Engine.Analyze
      ▼
DecisionRecord
      │  HTTP /v1
      ▼
┌──────────────────────────────────────┐
│ local runtime                        │
│   SQLite ← ControlPlane              │
│              ├─ Realtime Bus ── SSE ─┼──▶ TUI · WebUI
│              └─ HTTP API ────────────┼──▶ CLI · WebUI
└──────────────────────────────────────┘
```

The engine stays outside it. Your application analyzes events, serializes a
`DecisionRecord`, and posts it — the server never runs a second engine.

## Watching an agent behave

The shortest useful loop, with nothing typed into the browser:

```bash
make local                  # terminal A — prints the URL
<run your instrumented agent>   # terminal B
```

Then open the `Web:` URL. The page lands on **Live**, subscribes to all local
activity, and shows each active run as a card as telemetry arrives. Select one
and its behavior flow draws itself — source agent, operation, target, and the
decision, risk, trust and anomaly the server computed for each observation. A
behavior the reference never showed is marked `NEW`.

```text
                 POST
support-agent ───────────▶ ollama.localhost
             ├─ GET  ────▶ crm.localhost
             ├─ GET  ────▶ knowledge.localhost
             ├─ POST ────▶ mail.localhost
             └─ POST ────▶ export.localhost   NEW
```

You do not need a Project, Agent, Candidate or EvaluationRun identifier to see
this. **A producer still has to create them** — the Collector's `evaluation:`
block names a run that must already exist and be running, and nothing here
creates a durable entity because telemetry arrived. What changed is that a
person no longer retypes those identifiers into a browser to see the result.

If nothing is running, the same page browses what exists: one bounded page of
projects at startup, then Projects → Agents → Candidates → Runs a page at a
time, when you ask. See [the WebUI guide](webui.md).

The reference end-to-end workflow is the companion repository
`trustvian/trustvian-python-agent-demo`: a Python agent driven by Ollama,
instrumented with runtime OpenTelemetry, feeding the Collector and the
evaluation ingest path. This repository deliberately keeps no second Python
demo — two demos of the same thing drift.

## State

```text
.trustvian/platform.db     evaluation state, durable across restarts
.trustvian/runtime.json    the endpoint the running server bound
```

Project-local, so two checkouts are two independent environments and
`rm -rf .trustvian` ends one completely. `.trustvian/` is gitignored.

The database survives restarts; the realtime stream does not, by design —
reconnecting resynchronizes from durable state rather than replaying history.

## The endpoint is not fixed

The runtime binds `127.0.0.1:0` and lets the OS choose a free port, so nothing
collides with whatever else you are running. That port changes every restart,
which is why discovery exists: clients read the current endpoint instead of
remembering one.

If you want to see it:

```bash
cat .trustvian/runtime.json
# {"version":"1","api_url":"http://127.0.0.1:54321"}
```

The browser UI is the same endpoint — `make local` prints it as `Web:` — so the
one field locates both. See [Web control plane](webui.md).

## A shared database, when you need one

`make local` uses SQLite and needs nothing else. That is the default and it is
not changing.

A deployment that wants several processes against one set of state can select
PostgreSQL instead:

```bash
TRUSTVIAN_PLATFORM_POSTGRES_DSN='postgres://user:password@host:5432/trustvian' \
  ./bin/trustvian-local --backend postgres
```

The DSN is read from the environment rather than a flag because a command line
is visible to every process through `ps`. It is never printed, never logged and
never written into `runtime.json`.

Selecting an unknown backend, or `postgres` without a DSN, fails before the
listener binds — nothing falls back to SQLite from a backend you asked for. The
CLI, the TUI and the WebUI behave identically either way; they cannot tell which
database answered.

Two caveats worth knowing. Two processes sharing one PostgreSQL database share
authoritative state but **not** realtime notifications: each keeps its own
in-process bus, so a dashboard connected to one does not see events published by
the other. And this is still unauthenticated and loopback-only — a shared
database does not make the listener safe to expose.

## Reaching a different control plane

An explicit endpoint always wins over discovery:

```bash
./bin/trustvian eval compare --api-url https://control.example \
    --reference-run baseline --candidate-run candidate \
    --max-added-behaviors 0 --max-block-decisions 0 \
    --max-critical-risk-observations 0
```

That matters for CI: a checked-out working directory can never redirect a
command that named its own endpoint.

`--api-url ""` counts as naming one — badly. It exits **2** without reading any
discovery file, because an empty value almost always means an unset variable:

```bash
# $CONTROL_PLANE_URL is unset → exit 2, not a gate result from the local runtime
./bin/trustvian eval compare --api-url "$CONTROL_PLANE_URL" …
```

## When there is no runtime

```text
$ ./bin/trustvian eval get --id run-1
trustvian: no local Trustvian runtime found; start one with `make local` or pass --api-url
$ echo $?
3
```

Exit **3**, not 2 — the command was valid and the environment was not. Exit `1`
still means a failed gate, and only for `eval compare`.

## Security

The local runtime is **unauthenticated**. It binds loopback only, refuses any
other address, and there is no flag to change that — loopback is the entire
security boundary.

Discovery is not authentication: `runtime.json` carries no token and no secret,
and a discovered URL is accepted only if it is `http` on a numeric loopback
address. It must be exactly one JSON document under 4 KiB. A repository that
ships its own `.trustvian/runtime.json` cannot redirect your commands anywhere
else — and cannot make `make local` connect anywhere else either: the startup
liveness probe validates the address before it dials it.

**Do not expose this runtime, or put a reverse proxy in front of it.** Task 070
owns authentication and remote access.

## Options

```text
trustvian-local [--state-dir <dir>] [--listen <loopback-address>]
```

`--state-dir` defaults to `.trustvian`; `--listen` defaults to `127.0.0.1:0`
and accepts only numeric loopback (`127.0.0.1:8080`, `[::1]:0`).

`trustvian-local` is a repository-internal executable, not a released
artifact — `trustvian` is the shipped binary.

## Related

- [Platform CLI](platform-cli.md) — commands, flags, exit codes
- [Web control plane](webui.md) — the browser interface on the same listener
- [Terminal dashboard](tui.md) — watching a run live
- [ADR 0035](adr/0035-local-runtime-composes-platform-without-reversing-modules.md)
  — why the runtime lives in the platform module
