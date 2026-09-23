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
│              ├─ Realtime Bus ── SSE ─┼──▶ TUI
│              └─ HTTP API ────────────┼──▶ CLI
└──────────────────────────────────────┘
```

The engine stays outside it. Your application analyzes events, serializes a
`DecisionRecord`, and posts it — the server never runs a second engine.

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
address. A repository that ships its own `.trustvian/runtime.json` cannot
redirect your commands anywhere else.

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
- [Terminal dashboard](tui.md) — watching a run live
- [ADR 0035](adr/0035-local-runtime-composes-platform-without-reversing-modules.md)
  — why the runtime lives in the platform module
