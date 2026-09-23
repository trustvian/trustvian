# 0035 — The local runtime composes the platform without reversing modules

**Status:** Accepted

## Context

[Task 062](../tasks/v1.0/062-integrated-local-developer-workflow.md) makes the
local platform startable. Everything it needs already exists — SQLite store,
control plane, `/v1` handler, realtime bus, CLI, TUI — and nothing binds a
listener, so `--api-url` has nothing to point at.

The obvious place to add `main` is `cmd/trustvian`, where the CLI already
lives. That would make the root module import `trustvian-platform`, which
[ADR 0022](0022-core-platform-boundary.md) and
[ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md) both forbid — and the
convenience of "the binary can start its own server" is exactly the argument
that would retire a boundary two earlier decisions were built to protect.

## Decision

The integrated local runtime is composed inside the repository-internal
platform module rather than inside the published root CLI module. The root
`trustvian` CLI and TUI remain HTTP/SSE clients and never import
`trustvian-platform`. A loopback-only local server owns SQLite, the realtime
bus, `ControlPlane` and the HTTP handler. The running endpoint is advertised
through a bounded local discovery file so root-module clients can find it
without introducing a Go dependency edge.

### 1. The root → platform dependency stays prohibited

Nothing about needing a server changes the direction. `trustvian-platform`
depends on the root's public API; if the root imported it back, the shipped CLI
would carry the platform's implementation types, its SQLite driver and its
schema — and every user of `analyze` would pay for a server they never start.

Task 062 is not authorization to reverse ADR 0022 or ADR 0033. It is the task
that tests whether those boundaries survive the first real pressure on them.

### 2. The composition executable lives in the platform module

`platform/cmd/trustvian-local` may import the platform freely: it is already
inside it. That is the whole reason it lives there rather than beside the CLI.

It is a composition root and nothing else — no behavioral logic, no second
service implementation, no CLI commands. `project create` already exists in
`trustvian`, and duplicating it here would be two implementations of the same
operation drifting in parallel.

### 3. HTTP stays a real boundary even locally

The runtime could have handed the CLI an in-process `ControlPlane` and skipped
serialization entirely. It does not.

Keeping the real listener means the local workflow exercises the same wire
contract a remote deployment will, so a change that breaks clients breaks the
local developer loop immediately rather than at the first deployment. It also
keeps the CLI honest: it cannot quietly grow an in-process fast path that only
works on one machine.

### 4. The runtime duplicates no business logic

It wires exactly one store, one bus, one control plane, one handler, one
server. Every decision — ingest, diff, scorecard, gate, realtime publication —
stays where it already is.

A composition root that starts making decisions is how a second service is
born.

### 5. Platform SQLite is not the engine's baseline store

`.trustvian/platform.db` holds evaluation state: projects, agents, candidates,
runs, bounded evidence, ingest cursors. The engine's learned baselines are a
different concern with a different lifecycle, a different owner, and different
correctness properties.

Merging them would be easy and quietly wrong: baseline learning is per learning
scope and long-lived, while evaluation evidence is per run and bounded. A test
asserts the platform schema contains no baseline table.

### 6. The listener defaults to loopback

The runtime is unauthenticated. Loopback is not a convenience default here — it
is the entire security boundary, and the only reason it is safe to ship an
unauthenticated control plane at all.

### 7. Non-loopback listening is not supported

`--listen` refuses `0.0.0.0`, `::`, private ranges, public addresses and
hostnames, and there is no `--allow-remote`, `--insecure` or `--public` escape
hatch.

A flag that exposes an unauthenticated control plane to a network is a flag
someone will set on a laptop on a café network. [Task 070](../tasks/v1.0/) owns
a remotely exposed platform, and it owns authentication in the same breath
because the two cannot be separated.

Hostnames are refused even when they would resolve to loopback: resolution is
not proof, and what `localhost` means is the resolver's opinion.

### 8. The port is ephemeral

`127.0.0.1:0` lets the OS choose. Picking 8080 or 3000 because examples need a
number would turn an arbitrary choice into a compatibility surface — and the
first developer whose port is already taken would have to work around a default
that existed only for documentation.

Discovery exists precisely so the port does not have to be memorable.

### 9. Discovery is an operational file, not a registry

`.trustvian/runtime.json` says one thing: a local runtime announced this
endpoint. Two fields, bounded, versioned, atomically published.

It is not a service registry, not cluster membership, not durable evidence. It
is never written into SQLite, because the moment it lives in the database it
becomes state someone reasons about after the process is gone.

### 10. Discovery is not authentication

Finding the file proves nothing about who is allowed to use the endpoint. It
carries no token and no secret, and it must never grow one — a credential in a
world-readable project directory is worse than no credential, because it looks
like security.

What protects the endpoint is that it is on loopback.

### 11. Task 070 still owns authentication and TLS

No token, API key, login, certificate generation or TLS claim appears here. A
local HTTP server on loopback is the honest description, and documenting it
that way is better than a self-signed certificate that implies more.

### 12. Runtime state is project-local

`.trustvian/` in the working directory, not `/tmp`, not the OS temp directory,
not `$HOME` implicitly.

Project-local state is visible (`ls` finds it), removable (`rm -rf` ends it),
and isolated: two checkouts are two environments without anyone configuring
that. Hidden global state is how a developer ends up debugging a run that
belongs to a different project.

### 13. No raw event HTTP route is added

[Task 058](../tasks/v1.0/058-local-control-plane-api-and-ingest.md) chose
`DecisionRecord` as the platform's ingest boundary. A `POST /v1/events` that
analyzed server-side would be a second engine — a second place where scoring
happens, with its own configuration and its own drift.

The producer path stays: application → `Engine.Analyze` →
`Result.DecisionRecord()` → `POST /v1/evaluation-runs/{run}/records`.

### 14. Task 058's ingest boundary remains authoritative

Everything about sequence, replay, digests and bounded evidence is unchanged.
The runtime starts the server; it does not reinterpret what the server accepts.

### 15. No WebUI

[Task 063](../tasks/v1.0/) owns it. No HTML, templates, static assets or
browser routes, and no CORS — task 063 can define browser-origin requirements
against an interface that exists, rather than this task guessing.

### 16. Release packaging stays separate

Task 062 makes the repository's local workflow runnable. It does not change
which module is published or what the release artifact is;
[task 072](../tasks/v1.0/) remains the release gate.

`trustvian-local` is a repository-internal executable, and the documentation
says so rather than implying a second shipped binary.

### 17. Shutdown ownership and ordering

```text
close realtime bus → server.Shutdown(5s) → server.Close on timeout
→ SQLite close → remove owned discovery file
```

The bus closes first so in-flight SSE handlers return instead of holding
graceful shutdown open for their full duration — a dashboard connected at
`Ctrl-C` should not delay exit by the length of its own stream.

The discovery file is removed **only if it still describes this runtime's URL**.
A process shutting down late must not delete the endpoint a newer runtime has
already published; that would leave a working server undiscoverable.

Signals belong to the executable. The reusable runtime package takes a context
and owns resources, so nothing in `platform/` installs a signal handler.

## Alternatives considered

**Put `main` in `cmd/trustvian` and import the platform.** One binary, no
discovery file, no second command. Rejected because it reverses the module
direction that ADR 0022 and ADR 0033 exist to hold, and pulls the SQLite driver
into every shipped CLI.

**Hand the CLI an in-process control plane when running locally.** Faster and
simpler, and it would mean the local workflow no longer tests the wire contract
the product depends on. The bugs that reach production are the ones only the
network path has.

**A fixed default port.** Memorable, documentable, and a compatibility surface
the moment anyone scripts against it — plus a conflict for the first developer
already using it.

**An environment variable instead of a discovery file.** It would have to be
exported into every shell, and would silently follow a developer into
directories where it is wrong. The file is scoped to the directory that owns
the runtime.

**A lock file or distributed locking for concurrent runtimes.** Rejected as
scope: a bounded TCP probe answers "is something already listening here?" well
enough for single-node development, and [task 069](../tasks/v1.0/) owns the
real question.

## Consequences

There are now two executables in the repository, and only one of them ships.
The documentation has to be explicit about that or someone will look for
`trustvian-local` in a release archive.

Clients depend on a file format. `.trustvian/runtime.json` version 1 is
consumed by the shipped CLI and TUI, so its field meanings are operational
surface even though the file itself is ephemeral — additive fields are allowed,
existing meanings are not.

`--api-url` is no longer required, which is a relaxation rather than a break:
every existing script that passes it keeps working unchanged, and explicit
input always wins over discovery.

A developer can now run the whole platform locally with one command, and the
engine still sits outside it — which is the shape the rest of the milestone
assumes.
