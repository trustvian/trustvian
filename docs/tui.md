# Terminal dashboard

```bash
trustvian tui --run-id run-42
```

Watches **one** evaluation run as it happens. This is the inner-loop
interface: you have an agent running, records are arriving, and you want to see
what it is doing without tailing a log or re-running a query.

For scripting and CI, use [the CLI](platform-cli.md). The TUI is for watching.

## You need a control plane running

Start one with `make local` ([local development](local-development.md)) and the
dashboard finds it: the runtime publishes its endpoint to
`.trustvian/runtime.json`, and the TUI reads it from the current directory.

To watch a run on a different control plane, name it:

```bash
trustvian tui --api-url http://127.0.0.1:8080 --run-id run-42
```

Explicit `--api-url` always wins over discovery, and a discovered endpoint must
be `http` on a numeric loopback address — so a repository cannot redirect the
dashboard elsewhere.

`--run-id` is still required; nothing can guess which run you meant. With no
`--api-url` and no local runtime, the TUI exits **3**: the command was valid
and the environment was not.

An *empty* `--api-url` is different from an absent one: `--api-url ""` exits
**2** and reads no discovery file, because an empty value is an unset variable
rather than a request to use whatever runtime is in the directory.

The TUI starts no server of its own and binds no port.

## What it shows

```text
Trustvian — Evaluation run-42
Connection: LIVE

Candidate: cand-2
Environment: local
Behavioral profile: checkout-agent
Status: running

Authoritative snapshot
Records: 18
Distinct behaviors: 11
Behavior evidence: complete
Next sequence: 19

Live observations — current stream
SEQ      NEW  DECISION  RISK      TRUST  ANOMALY  BEHAVIOR
18       yes  ALLOW     medium    0.81   0.22     tool/shell.execute → build-host
17       no   ALLOW     low       0.88   0.11     http/POST /pay → payment-db
```

Layout is observational and may change. The machine interface is the `/v1` API,
not this screen.

## Snapshot and live rows are different things

The two blocks are not the same kind of state, and the difference matters:

- **Authoritative snapshot** — what the control plane said at the last resync.
  These numbers move only when a resync happens. They are correct.
- **Live observations** — what has arrived on the stream since that snapshot.

A field that cannot be safely updated from a single event keeps its snapshot
value until the next resync. Distinct behavior counts, gate verdicts and
scorecards are never derived from the observation stream, because a stream with
any gap would make them quietly wrong.

`NEW` means this run had not produced that behavioral shape before. It does not
mean unsafe, bad, or a regression — and an event-level `BLOCK` or `critical` is
the policy engine working, not an evaluation verdict.

## The row list is a window, not history

At most 100 rows are kept. When it fills, the oldest is dropped.

This is a viewport, deliberately. It is labelled *current stream* because that
is what it is:

- it holds only what arrived since the last resync;
- it is **cleared on every reconnect**;
- nothing is written to disk, and nothing can be scrolled back to.

Realtime retains no history server-side either — see
[ADR 0032](adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md). If you
look away, what you missed is gone from this screen; the authoritative summary
is what bridges the gap. Durable event history is task 067.

## Reconnect and resync

Connection states:

```text
CONNECTING → RESYNCING → LIVE
                  ↑          ↓
             RECONNECTING ←──┘
```

The state is always on screen, so a stale dashboard never looks current.

Every connection — the first and every reconnect — does this in order:

```text
subscribe → stream_ready → drain → fetch run + progress → apply → replay buffered
```

Subscribing first is what makes it lossless: fetching state and then
subscribing leaves a window where a change lands between the two and is missed
by both.

If the stream drops after the dashboard has been live, it reconnects on a
bounded backoff (250ms, 500ms, 1s, 2s, 4s, capped at 5s) and resyncs. Press `r`
to skip the wait.

A stream closing does **not** mean the evaluation ended — the server disconnects
clients that fall behind. Only lifecycle events change the run's status.

A stream that goes *silent* without closing is treated the same way. The server
heartbeats a quiet healthy connection, so 60 seconds with no bytes at all means
the connection is gone even though nothing reported an error — the dashboard
reconnects rather than continuing to show LIVE against something it can no
longer refresh. Heartbeats count as activity, so a genuinely quiet evaluation
stays connected indefinitely.

A connection that talks but never finishes starting up is treated the same way.
If a server accepts the request and sends heartbeats but never delivers a valid
handshake, the dashboard gives up rather than waiting at `CONNECTING`
indefinitely — heartbeats prove the connection is alive, not that the protocol
works. The same applies to an error response whose body never arrives: the
diagnostic is worth a few seconds, not forever.

None of this puts a limit on a working stream. A healthy dashboard can stay
connected for as long as you leave it open.

When a run completes, fails or is cancelled, the dashboard performs one final
authoritative read so the closing numbers are correct. That happens once, and
nothing periodic follows it — including when the run finishes while a resync is
already in progress.

If something fails *before* the dashboard is ever live — a bad URL, a run that
does not exist, realtime not configured — it exits rather than retrying
forever against something that will not work.

## Keys

```text
q, ctrl-c   quit
r           reconnect and resync now
?           toggle help
```

That is the whole set. The TUI is **read-only**: it issues three GETs and
nothing else, and there are no keys that start, complete, fail or cancel a run.
Those are CLI commands, typed deliberately, where they appear in shell history.

## Exit codes

```text
0  you quit
2  usage / invalid invocation
3  startup, HTTP, SSE, protocol or terminal failure
```

`1` is never returned. It means a failed gate, and only for
`trustvian eval compare` — a disconnected dashboard must not look like a policy
result. See [the compatibility contract](compatibility.md#cli).

## No authentication yet

The TUI sends no credentials and claims no transport guarantee. Credentials
embedded in `--api-url` are rejected. Task 070 owns authentication; run the
control plane on loopback and treat it as the local trust boundary it is.

## Related

- [Platform CLI](platform-cli.md) — scripting, CI, one-shot operations
- [ADR 0034](adr/0034-tui-is-a-bounded-realtime-http-client.md) — why the
  dashboard is shaped this way
- [Task 061](tasks/v1.0/061-terminal-dashboard.md) — the specification
