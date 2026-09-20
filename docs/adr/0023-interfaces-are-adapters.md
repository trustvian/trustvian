# 0023 — Interfaces, transports, and stores are adapters over control-plane services

**Status:** Accepted

## Context

[ADR 0022](0022-core-platform-boundary.md) settled the boundary *between*
the behavioral engine and the platform. This one settles the shape
*inside* the platform, before any of it is built.

Three pressures push the same way if nothing resists them.

The platform has three first-class interfaces — a CLI for automation and
CI, a TUI for the developer inner loop, a WebUI for management and
history. Each is a plausible place to "just compute the gate result
here". Three implementations of a gate rule are three behaviors, and the
one a user believes is whichever interface they happened to open.

Live behavior needs a transport. Picking one early — server-sent events,
WebSockets, long polling — and threading it through the code makes the
choice permanent, because replacing it then means touching everything
that produces an event rather than one adapter.

Persistence spans SQLite locally, PostgreSQL in a sandbox, and
potentially ClickHouse for production event history. A single
`Database` interface would make those interchangeable on paper and
leak at the first thing one backend expresses that another does not.

## Decision

**The control plane owns all authoritative logic. Everything else is an
adapter.**

### Interfaces are adapters

Evaluation, scoring, policy, behavioral diff, gate evaluation, and
promotion live in control-plane services. CLI, TUI, and WebUI call those
services and render results. No interface carries its own copy of any of
them, and a rule that exists in one interface only is a defect.

They are peers, not a hierarchy, and they are shaped by different jobs:

- **CLI** — scripting, CI, machine-readable output, one-shot commands.
- **TUI** — the inner loop: run, observe, change, run again, compare. It
  deliberately does not own project administration, policy editing,
  environment configuration, promotion workflows, or deep analytics.
- **WebUI** — management, history, investigation, and the workflows the
  TUI declines.

The TUI is built before the full WebUI. Local developer adoption is the
primary product goal, and it is won or lost in the inner loop.

### Transports are adapters

The realtime bus is defined by its internal abstraction, not by its wire
format. Server-sent events are the first transport to evaluate, because
the flow is one-way and SSE is the smaller answer; WebSockets wait for a
bidirectional requirement to actually appear.

Four properties are part of the design rather than discovered later:
bounded queues, an explicit slow-consumer policy, subscriber isolation
so one client cannot stall another, and defined disconnect and
reconnect behavior. A bus without a slow-consumer answer is an unbounded
queue with better branding — the same reasoning that bounds every
learned map in the engine.

### Stores are capabilities, not a database

No generic `Database` interface. Capability boundaries — `ControlStore`,
`EvaluationStore`, `BehaviorStore`, `EventStore`, `RealtimeBus`, or
narrower — are named once their call patterns are known, which is why
the evaluation domain is sequenced before local persistence. This is the
same discipline that kept `internal/store.Store` at two methods:
[ADR 0004](0004-narrow-store-port-in-memory-only.md) rejected a generic
repository interface precisely because a narrow port expresses the real
access pattern and a wide one invites CRUD-shaped thinking.

The WebUI never queries a database directly. It consumes the control
plane API, like every other interface.

## Alternatives considered

- **Build the WebUI first and treat the TUI as optional.** Rejected on
  product grounds: the primary adoption path is a developer running an
  agent locally and watching what it does. A browser tab is a worse
  place for that loop than the terminal the agent is already running in.
- **Let each interface own its own logic for speed.** Rejected. It is
  faster exactly once, and the divergence is invisible until two
  interfaces disagree about whether a candidate passed.
- **Commit to SSE in the architecture.** Rejected. SSE is likely right
  and is still a transport decision; encoding it structurally would make
  revisiting it a rewrite rather than an adapter swap.
- **One `Database` interface across all three deployment profiles.**
  Rejected for the reason ADR 0004 already gave, now applied to a
  larger surface.
- **Adopt a message broker for the realtime bus.** Rejected as premature.
  Kafka or NATS may eventually be justified by measurement; neither is
  justified by anticipation, and both would become infrastructure a
  local developer has to run.

## Consequences

- A new interface is additive: it renders existing services and needs no
  new logic. Conversely, a feature landing in only one interface is a
  design error, not a scope decision.
- Gate and scorecard logic is testable without any interface at all,
  which is what keeps determinism and explainability checkable.
- Transport and storage choices stay reversible. Replacing SSE, or
  adding ClickHouse behind `EventStore`, touches one adapter.
- Capability interfaces cannot be designed up front by definition — they
  wait for the call patterns from tasks 052–056, which is why local
  persistence is sequenced after the evaluation domain rather than
  before it.
- The local profile must remain runnable with no external
  infrastructure. Any proposal that requires a broker or a server to
  develop against fails the primary product goal regardless of its other
  merits.
