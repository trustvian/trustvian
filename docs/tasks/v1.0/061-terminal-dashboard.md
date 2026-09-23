# 061 — Terminal Dashboard (TUI)

Status: implemented
Depends on: [059](059-realtime-infrastructure.md), [060](060-developer-cli.md)

## Objective

A live terminal dashboard for watching **one** evaluation run as it happens:

```text
trustvian tui --api-url http://127.0.0.1:PORT --run-id run-42
```

This is the inner-loop interface. The CLI ([task 060](060-developer-cli.md))
serves scripting and CI; the TUI serves a developer watching their agent
behave. It is not a prettier CLI, a terminal WebUI, an admin console, a gate
engine, or a daemon.

## Architecture

```text
                 HTTP authoritative reads
trustvian tui ───────────────────────────▶ /v1/evaluation-runs/{run}
      │                                    /v1/evaluation-runs/{run}/progress
      │
      └──── SSE /v1/realtime?run_id=… ───▶ bounded ephemeral notification
```

```text
durable HTTP state   →  authoritative
realtime SSE         →  notification that something changed
terminal             →  presentation
```

The TUI is never authoritative. It ships inside the existing `trustvian`
binary, imports nothing from `trustvian-platform`, and reuses task 060's HTTP
boundary.

## Command Surface

```text
trustvian tui --api-url <url> --run-id <id>
```

Both required. No `--project-id`, `--agent-id`, `--candidate-id`, `--db`,
`--port`, `--listen`, `--token`, `--api-key`, `--history`, `--replay`.

`--api-url` gets no default: [task 062](062-*) owns integrated local startup,
and a default address frozen before the thing that binds it exists is far
harder to change than to add.

One run at a time. A run is the unit a developer is actually watching, and a
multi-run view needs list semantics the API does not have.

## Realtime Contract

Subscribes only to `GET /v1/realtime?run_id=<escaped>` — never unfiltered,
never a project- or agent-wide stream. Task 059 filters before enqueue, so a
run-scoped subscription is also what keeps this client inside its own
per-subscriber queue.

No `Last-Event-ID` header is ever sent, and no `id:` field is retained. Task
059 keeps zero history; accepting a replay cursor would promise a resume that
cannot happen.

## Subscribe-before-resync

Every connection and reconnection, in this order:

```text
1. subscribe to SSE for the run
2. receive stream_ready
3. verify replay_available == false
4. verify resync_required == true
5. begin draining realtime frames
6. GET /v1/evaluation-runs/{run}
7. GET /v1/evaluation-runs/{run}/progress
8. apply the authoritative snapshot
9. apply frames buffered since step 5
10. continue live
```

Fetching state and *then* subscribing leaves a window where a committed
mutation lands between the two and is lost by both paths. The order is the
whole point of the resync protocol, and a test drives it directly: the
`/progress` response is held open while the server emits an observation, and
that observation must still appear after the snapshot is applied.

**Draining does not stop during resync.** The server's per-subscriber queue is
64 events; a client that stopped reading while its authoritative reads were in
flight would fill it and be disconnected — turning a slow resync into a
reconnect loop.

**A terminal lifecycle event buffered during a resync still gets its final
authoritative read.** Replaying a pending frame returns a command exactly when
that frame is a completion, failure or cancellation, and that command *is* the
final read. Discarding it left the status updated from the event and the counts
frozen at the stale snapshot, with the one-resync guard stuck closed. One
terminal event produces exactly one final resync whether it arrives while LIVE
or while a resync is still in flight.

## Authoritative State

Exactly two reads per resync: the run and its progress. `ingest-state` is not
fetched — progress already carries the next ingest sequence. Project, agent and
candidate are not fetched; the run response already carries candidate,
environment and profile identity.

## Dashboard Model

Two classes of state, deliberately separate:

```text
authoritative snapshot   what the control plane said at the last resync
live notifications       what has been observed since that snapshot
```

A field that cannot be safely updated from a single event keeps its snapshot
value until the next resync. `distinct_behavior_count`, any gate verdict and
any scorecard are **never** derived from the observation stream.

## Observation View

Only task 059's projection: sequence, record count, behavior completeness,
fingerprint, behavioral shape, decision, risk, approval, three scores, and
`new_behavior`. No `Event.Attributes`, tool arguments, prompts, completions,
contributors or `PolicyReason` — those are absent from the realtime wire on
purpose, and the TUI has no endpoint that would return them.

A new behavior is labelled `NEW`. Not unsafe, bad, a violation or a regression:
an event-level decision is not an evaluation verdict, and `BLOCK` or `critical`
are rendered as the server's values rather than re-interpreted.

## Connection States

```text
connecting → resyncing → live
                 ↑          ↓
            reconnecting ←──┘
                 ↓
               fatal
```

Always visible. A stale screen must never look current, so `LIVE` and
`RECONNECTING` are distinguishable at a glance.

## Reconnect

A dropped stream after the dashboard has been live is a reconnect, not an exit:

```text
250ms → 500ms → 1s → 2s → 4s → 5s (cap)
```

Reset after a successful `stream_ready` + resync. At most one pending timer, no
goroutine per retry, no unbounded counter. `r` cancels the wait and reconnects
immediately.

**Before** the first successful `stream_ready` + resync, an unrecoverable
failure exits `3` — a bad URL, a 404 run, realtime unavailable, a malformed
handshake. Sitting forever on a quiet subscription to a typoed run ID is worse
than failing.

**Reconnect clears the live observation rows.** Visually joining two
disconnected streams would imply one complete sequence. After a resync the list
means *observed since this resync*; the authoritative summary is what bridges a
disconnect.

**EOF is not the end of the evaluation.** Task 059 disconnects a slow consumer,
so a closed stream means the stream is incomplete — reconnect and resync. Only
lifecycle events determine run status.

**Four different time bounds, asking four different questions.** Conflating any
two of them produced a real bug:

```text
ResponseHeaderTimeout    30s   waiting for response headers
sseErrorBodyReadTimeout   5s   consuming a non-200 diagnostic body
sseHandshakeTimeout      30s   waiting for a valid stream_ready
sseReadIdleTimeout       60s   silence after bytes stop arriving
```

There is deliberately **no total client timeout** — that would kill a healthy
long-lived dashboard on a schedule.

A non-200 response needs its own bound because by then the header timeout has
already been satisfied and `io.LimitReader` caps bytes, not time: a server that
sent 404 headers and then never finished its body blocked startup forever. The
read is now bounded in both, and the request is cancelled and the body closed
either way.

The handshake bound exists because heartbeat comments are real activity and
*should* refresh the idle watchdog — which means a server sending nothing but
heartbeats kept liveness satisfied indefinitely while never completing the
protocol, leaving the dashboard at `CONNECTING` with no frame to act on and no
reason to give up. The handshake deadline asks "did the server establish the
protocol?", and heartbeat traffic does not answer it. It is released the moment
a *valid* `stream_ready` is accepted — an event merely named `stream_ready`, or
any other byte, leaves it armed — so it can never disconnect an established
stream.

**Silence is bounded too.** An active stream that stops delivering bytes
without closing produces no error and no EOF, which is exactly what a half-open
connection looks like — and a dashboard that keeps showing LIVE against one is
asserting state it can no longer refresh. `sseReadIdleTimeout` (60s, four times
task 059's 15s heartbeat) ends such a stream; before first synchronization that
is fatal, after it a reconnect, identical to an EOF.

Any byte refreshes the bound, heartbeat comments included. They never become
domain events, so a client counting only events would disconnect a healthy but
quiet evaluation. `http.Transport.IdleConnTimeout` is **not** this mechanism —
it governs unused pooled connections and never touches an active body; it is
named `sseIdleConnPoolTimeout` here so the two cannot be confused.

## Bounded Memory

```text
tuiObservationCapacity  = 100    displayed rows; oldest evicted
tuiPendingEventCapacity = 64     frames buffered during resync; overflow reconnects
maxSSELineBytes         = 64 KiB
maxSSEFrameBytes        = 64 KiB
maxSSEErrorBodyBytes    = 8 KiB  non-200 diagnostic body
sseErrorBodyReadTimeout = 5s     consuming that diagnostic
sseHandshakeTimeout     = 30s    waiting for a valid stream_ready
sseReadIdleTimeout      = 60s    silence on an active stream
total stream lifetime    unbounded, on purpose
connections              1 SSE + bounded authoritative reads
reconnect timers         ≤ 1
per stream               1 body, 1 reader goroutine, 1 idle watchdog,
                         1 handshake timer (released at handshake),
                         1 bounded frame channel
durable/replay history   0
```

**Abandoning a generation cancels its work, not just its result.** Generation
numbers stop a stale answer being applied; they do not stop the request. Every
network operation runs under a generation-scoped context, so a manual `r`, a
stream failure, a pending overflow or a quit cancels the open and the
authoritative reads that belonged to the generation being replaced. Without
that, repeated `r` against a blocked endpoint accumulated sockets and
goroutines whose answers were already destined to be discarded.

The two bounds differ in kind, and the difference is deliberate:

- **Transport may never silently drop.** Exceeding the pending capacity cancels
  the stream and resyncs, because a stream with dropped notifications is
  incomplete and the client cannot know what it missed.
- **Display may intentionally truncate.** The observation window is a viewport,
  not evidence. It is labelled *Live observations — current stream*, never
  history: [task 067](067-*) owns event history.

## SSE Parsing

A narrow parser for the contract task 059 actually emits: `event:`, `data:`
(joined per SSE rules when repeated), comment heartbeats, blank-line frame
terminator. Not a browser `EventSource` clone. `id:` is ignored and never
echoed.

Reads are bounded at both the line and frame level; exceeding either makes the
stream invalid rather than allocating for it.

**Unknown event names are ignored and the stream continues** — the
compatibility contract requires clients to tolerate additive kinds, and an
unknown one is held to no shape at all. A *known* event is validated for wire
structural integrity, and failing that invalidates the stream:

```text
payload version == "1"
envelope.kind   == the SSE event name
scope.run_id    == the watched run
observation     ⇒ observation payload present, evaluation absent
lifecycle       ⇒ evaluation payload present, observation absent
```

Structure only — no lifecycle legality, no gate semantics, no field-by-field
domain checks. Each of those failures was previously absorbed in silence: an
observation with no payload did nothing, a mismatched kind was applied under
the wrong name, and an event scoped to another run would have been rendered as
this run's. That last one matters most: the subscription is filtered
server-side, so a mismatch means the filter did not hold, and attributing
another agent's behavior to this dashboard is worse than showing nothing.

`stream_ready` must be the first domain frame, with `version: "1"`,
`replay_available: false` and `resync_required: true`. Anything else — an
observation first, a true replay flag, a false resync flag, an unknown version,
malformed JSON — invalidates the connection before any event is consumed as if
state were synchronized.

## Terminal Safety

Every string that originated outside this process — identifiers, operation and
target names, failure reasons, environment and profile refs, and any future
server string — passes through one sanitizer before it reaches the screen. ESC,
BEL, C0, DEL and C1 are replaced.

Server text must not move the cursor, clear the screen, retitle the terminal,
emit OSC hyperlinks, or change colors. The framework emits control sequences;
remote data does not. A regression feeds `"\x1b[2J\x1b]0;owned\x07"` through
server fields and asserts no server-originated escape survives into the view.

## Keybindings

```text
q, Ctrl-C   quit
r           reconnect and resync now
?           toggle compact help
```

No mouse, no command palette, and no lifecycle keys. The TUI is read-only; the
CLI owns mutations. It issues only `GET /v1/realtime`,
`GET /v1/evaluation-runs/{id}` and `GET /v1/evaluation-runs/{id}/progress`, and
an architecture test enforces that.

## Exit Codes

```text
0  normal user exit
2  usage / invalid invocation
3  startup, HTTP, SSE, protocol or terminal operational failure
```

`1` has no meaning here. Only `eval compare` owns it, and a disconnected stream
must never look like a gate result.

## Dependency Confinement

One UI dependency: **Bubble Tea v1.3.10** (MIT), imported only by
`cmd/trustvian`. No Lip Gloss, Bubbles, Cobra or Resty added directly;
rendering is plain text.

The engine's package graphs stay framework-free, verified with `go list -deps`
and by a test. The release binary containing the framework is expected — a
shipped user interface is not the behavioral engine depending on a UI toolkit.

## Tests

Subscribe-before-resync with a held `/progress`; reconnect with row clearing
and no `Last-Event-ID`; no-polling under a quiet stream; pending overflow;
observation-window eviction; unknown event tolerated; malformed known event
reconnects; the full `stream_ready` validation table; SSE line and frame bounds
at and over the limit; terminal injection through three server fields; keyboard
and resize model tests with no network; lifecycle status and exactly one final
resync per terminal event; a privacy test asserting an unknown field carrying
`SECRET_PROMPT_DO_NOT_RENDER` never reaches the view.

## Mutation Tests

Twenty, covering: fetch-before-subscribe; draining stopped during resync;
pending overflow dropped silently; pending bound removed; display slice
unbounded; rows kept across reconnect; `Last-Event-ID` sent; unknown kind
fatal; malformed known event ignored; observation accepted before ready;
`replay_available` true accepted; `resync_required` false accepted; SSE bounds
removed; periodic polling added; reader not cancelled on quit; raw ESC
rendered; a POST issued; `trustvian-platform` imported; task 060's 30s total
timeout used for SSE; local channel unbounded.

## Performance

No FPS target. Informational benchmarks for model update, a 100-row render, and
sanitization. The meaningful contract is the resource table under *Bounded
Memory*.

## Documentation

`docs/tui.md`, plus `docs/compatibility.md`, `docs/ARCHITECTURE.md`,
`docs/SECURITY.md`, `docs/ROADMAP.md`, `CHANGELOG.md` and both indexes.

## Acceptance Criteria

1. Subscribe precedes authoritative fetch, and draining continues during it.
2. Pending and display bounds hold; overflow reconnects rather than drops.
3. Reconnect resyncs and clears live rows; no `Last-Event-ID`.
4. `stream_ready` validated before any event is trusted.
5. Unknown kinds tolerated; malformed known events reconnect.
6. No polling; terminal-lifecycle resyncs are event-driven and happen once.
7. Server text cannot emit terminal control sequences.
8. Read-only: three GET endpoints, nothing else.
9. Framework confined to `cmd/trustvian`; all five release targets cross-build
   with `CGO_ENABLED=0`.
10. No platform import, no SQLite, no listener, no auth.

## Non-Goals

No comparison or gate UI (060 owns CI comparison), no integrated local startup
(062), no environment model (065), no promotion (066), no event history (067),
no authentication (070), no WebUI, no WebSocket, no broker, no mouse, no
mutation keys, no core or platform runtime change.
