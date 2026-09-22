# 0034 — The TUI is a bounded realtime HTTP client

**Status:** Accepted

## Context

[Task 061](../tasks/v1.0/061-terminal-dashboard.md) adds `trustvian tui`, a
live terminal dashboard for one evaluation run.

Three things make this the riskiest interface so far. It is the first client
that holds a long-lived connection, so it is the first that can accumulate
state. It is the first that renders server-supplied text to a terminal, which
is an execution surface. And it needs a UI framework, which would be the
repository's fourth third-party dependency in a codebase whose rule is that
each one is confined to exactly one package.

## Decision

The terminal dashboard ships inside the existing `trustvian` binary as the
additive `trustvian tui` command. It consumes the platform only through `/v1`
HTTP and SSE, treats durable HTTP state as authoritative and realtime as
ephemeral notification, keeps all in-memory display state bounded, and
reconnects by resubscribing before resynchronizing.

### 1. It ships in the existing binary, not a fifth module

A separate module would need its own release artifact, its own version, and its
own copy of the HTTP client — and users would have to install a second thing to
watch the first one work.

The counter-argument is real: the binary now carries a UI framework for a
command most invocations never reach. That is a size cost, accepted because the
alternative is a distribution problem, and because
[the packaging boundary](../ARCHITECTURE.md) already treats `trustvian` as the
single user-facing artifact.

### 2. It still cannot import `trustvian-platform`

Everything [ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md) says about
the CLI applies unchanged. The module edge points one way; a dashboard is not a
reason to close the loop, and the fact that it needs *more* of the platform's
data than the CLI does makes the temptation stronger rather than the rule
weaker.

### 3. HTTP and SSE remain the adapter boundary

The TUI reuses task 060's client for authoritative reads and speaks task 059's
SSE contract for notification. It adds no third transport.

This also keeps the two servers honest: the dashboard is a real consumer of the
`/v1` wire contract, so a change that breaks clients breaks a test here.

### 4. It is read-only

Three endpoints: the realtime stream, the run, the run's progress. No POST of
any kind — no lifecycle transition, no ingest, no compare.

A dashboard that could mutate is a dashboard where a keystroke can change
durable state, and the failure mode is someone pressing a key while looking at
the wrong run. Mutations stay in the CLI, where they are typed deliberately and
appear in shell history. An architecture test enforces the endpoint list.

### 5. It starts no server

No listener, no port, no daemon. The TUI requires an already-running control
plane and says so when one is not there.

### 6. Task 062 owns integrated local startup

Running the engine, the control plane and an interface together from one
command is a real requirement and a separate one — process lifecycle, storage
location, port selection, shutdown ordering. None of it has to be decided for a
dashboard to be useful against a control plane the developer already started.

That is also why `--api-url` has no default here: freezing an address before
the thing that binds it exists is much harder to undo than to add later.

### 7. One run at a time

A run is what a developer is actually watching. A multi-run view needs list and
scoping semantics [task 058](../tasks/v1.0/058-local-control-plane-api-and-ingest.md)
deliberately did not define, and inventing them in a terminal client would
decide them for every later interface.

Run-scoped subscription is also what keeps this client inside its own
per-subscriber queue on the bus.

### 8. No list, search or admin surface

The API has no collection routes, and faking them client-side would invent
ordering, paging and scoping. Project administration, policy editing,
promotion and historical analytics are the WebUI's, per the roadmap's split
between the three interfaces.

### 9. SSE notification is not history

[ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md) retains
zero events after delivery. A client that accumulated them would rebuild the
replay buffer that decision refused, one screen at a time, and would then be a
second source of truth with none of the database's properties.

The displayed rows are a viewport over the current stream, labelled as such.
[Task 067](../tasks/v1.0/) owns event history.

### 10. Reconnect means subscribe first, then resync

```text
subscribe → stream_ready → drain → fetch state → apply → replay buffered
```

Fetching state and then subscribing leaves a window in which a committed
mutation lands between the two and is seen by neither. The window is small,
which is exactly what makes it the kind of bug that survives review and shows
up once a month in someone's dashboard.

Draining continues *during* the authoritative reads. The bus queue is 64
events; a client that paused reading while its own HTTP was in flight would
fill it and be disconnected for being slow — turning a slow resync into a
reconnect loop that makes the slow resync worse.

### 11. Live rows mean "since this resync", not "this run"

On reconnect the observation window is cleared. Two disconnected ephemeral
streams rendered as one list would assert a continuity that does not exist —
the client cannot know what happened while it was away, and the authoritative
summary, not the retained rows, is what bridges the gap.

### 12. No polling

No ticker over `/progress`, no database watcher, no filesystem watcher.
Realtime drives the dashboard; authoritative reads happen at first
`stream_ready`, at each reconnect's `stream_ready`, on explicit user resync, and
once when a terminal lifecycle event arrives.

That last one is event-driven, not a loop: a completed run's final counts are
worth one read, and no periodic refresh follows it. The SSE heartbeat is
transport liveness and is not a polling signal. A test asserts a quiet healthy
stream issues exactly one `/run` and one `/progress`.

### 13. Bounded display state

An observation window of 100 rows and a pending buffer of 64 frames, both
named constants.

The two bounds are different in kind, and conflating them would be a bug in
either direction. Transport may never silently drop: exceeding the pending
capacity cancels the stream and resyncs, because a client that discarded a
notification cannot know what it missed. Display may intentionally truncate:
the window is a viewport, not evidence, so the oldest row is evicted without
ceremony.

### 14. The UI framework is confined to the command layer

Bubble Tea v1.3.10 (MIT) is imported only by `cmd/trustvian`.

`.claude/rules/go.md` allows a fourth dependency on the condition that the
single package permitted to import it is named and the confinement is verified
with `go list -deps`. That is the whole bar, and it is met: the engine's
package graphs — `event`, `internal/features` through `internal/policy`,
`internal/store`, and the root `trustvian` package — contain no part of it, and
a test fails if that changes.

The binary containing a UI framework is expected. The behavioral engine
depending on one would not be, and those are different claims.

### 15. Terminal control sequences from remote text are never rendered

Identifiers, operation and target names, failure reasons and environment refs
all originate outside this process and all reach the screen. A terminal is an
execution surface: ESC sequences move the cursor, clear the screen, retitle the
window, emit clickable OSC hyperlinks and change colors.

So every such string passes through one sanitizer that replaces ESC, BEL, C0,
DEL and C1 before rendering. The framework may emit control sequences; data may
not. A regression drives `"\x1b[2J\x1b]0;owned\x07"` through server fields and
asserts nothing server-originated survives into the view.

This is the one place where the TUI is meaningfully more dangerous than the
CLI, because the CLI's output is mostly the server's JSON going to a pipe while
the TUI's is composed into a live screen.

### 16. No authentication contract is invented

No `--token`, no credential file, no header. [Task 070](../tasks/v1.0/) owns
authentication, and a long-lived connection is exactly the wrong place to
improvise a credential shape — it would need refresh semantics too, and that is
a design, not a flag.

Credentials embedded in `--api-url` are refused, as in task 060.

## Alternatives considered

**Poll `/progress` on a ticker instead of consuming SSE.** Simpler, and
[ADR 0032](0032-realtime-is-bounded-ephemeral-not-authoritative.md) already
rejected it for the bus: polling storage is not realtime, and the refresh
interval becomes a latency floor nobody can tune away. Task 059 exists so this
client does not have to.

**Fetch authoritative state first, then subscribe.** The obvious order, and
wrong: it drops anything committed in between. The correct order costs a
bounded buffer and a few lines of state machine.

**Retain observations across reconnects for a fuller list.** Rejected — it
would present a sequence with an unknown hole as continuous, which is worse
than a short honest list.

**A second module or binary for the TUI.** Cleaner dependency story, worse
product: users install one thing today.

**Writing the terminal loop by hand to avoid the dependency.** Considered
seriously, because the dependency is the most contentious part of this task.
Rejected: raw-mode handling, resize signals, input decoding and Windows console
support are a lot of platform-specific code to own, and getting them subtly
wrong produces a broken terminal rather than a failed test. The confinement
rule exists precisely so a dependency like this can be taken without spreading.

## Consequences

The shipped binary grows by a UI framework and its transitive dependencies —
13 modules in the build graph. That is the real cost of this decision, and it
is paid by every user of `analyze`, who will never run `tui`.

The TUI shows a bounded window and an authoritative summary, which means a
developer who looks away cannot scroll back to what they missed. That is the
deliberate consequence of having no history; task 067 is where that changes.

Adding a second UI dependency later would need its own argument. The
confinement test makes that a decision rather than an accident.
