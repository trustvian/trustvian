# 076 — Behavioral Trace & Session Evidence Explorer

Status: specified; not implemented
Milestone: `v1.0`
Depends on: [067](README.md) — event-history capability boundary,
[074](074-zero-input-live-behavior-webui.md),
[075](075-ai-semantic-telemetry-normalization.md)
Blocks: [072](README.md) — the OSS `v1.0` release gate

## Objective

Let a developer see **why** Trustvian reached a behavioral conclusion, without
Trustvian becoming a trace warehouse.

Two different questions, and the difference is this task's entire shape:

```text
a trace viewer answers          what exactly happened inside this invocation?

this explorer answers           which observed actions constitute this actor's
                                behavior, how do they differ from its learned
                                or reference behavior, and what decision
                                followed?
```

The second is answerable from metadata. The first is not, and Trustvian
deliberately does not retain what it would need.

## Why

**A verdict without its evidence is not explainable.** Trustvian's whole claim
is that every decision is explainable from the evidence beneath it — it is a
release criterion. Today a developer can see that a run produced a gate FAIL
and that a fingerprint was new. What they cannot do is follow one session
through and see *which* actions were familiar, which were new, where the
sequence drifted, and what the trust score was at each point.

**Correlation already crosses the boundary and is then dropped.**
`DecisionRecord` carries `TraceID`, `SpanID`, `SessionID` and `DelegatedFrom`,
and the ingest path accepts all four. The platform then folds each record into
a bounded aggregate and a per-fingerprint snapshot — deliberately, under ADR
0030 — so the correlation is received and not retained. That is the right
default and it is exactly the gap: **task 067 owns making some of it durable,
and this task owns making it understandable.**

**Without it, a developer needs a second UI.** The `v1.0` goal is that a
developer can use Trustvian without opening another trace tool merely to
understand Trustvian's own evidence. That is a bounded, achievable goal
precisely because the question is narrower than a trace viewer's.

## Current gap, verified against `main`

| Claim | Verified |
|---|---|
| `DecisionRecord` carries `TraceID`, `SpanID`, `SessionID`, `DelegatedFrom` | **True** |
| The platform persists no per-record correlation | **True** — a run persists an aggregate and a behavior snapshot; no per-record row exists |
| Realtime carries correlation only while a connection is open | **True** — bounded and ephemeral, ADR 0032 |
| No session, trace or sequence view exists in any interface | **True** |
| Task 067 is approved and unspecified | **True** |

So the explorer cannot be built on what is stored today, and this task does
not pretend otherwise. It depends on 067.

## The boundary with task 067

Stated plainly, because these two are easy to merge by accident:

| | Task 067 | Task 076 |
|---|---|---|
| Owns | the **capability boundary** for retained event history: what is stored, for how long, under what bounds, on which backend | **presentation, correlation and behavior-oriented querying** over whatever 067 makes durable |
| Decides | the storage contract | how a human reads it |

**This task must not silently redesign 067's storage.** It may — and should —
state the correlation fields it needs, as *requirements 067 must satisfy
before 076 can be implemented*:

```text
per-record or per-behavior retention of:
    trace id · span id · session id · delegation hop
    fingerprint id · operation · target · sequence position
    decision · risk level · trust · anomaly score · anomaly confidence
    new-behavior state · timestamp
```

Every one of those is metadata Trustvian already computes or receives. If 067
chooses to retain less, this task's views narrow accordingly and the
specification is amended — it does not route around 067 with its own store.

## Scope

Bounded, read-only views over authoritative evidence:

- **Session** — one bounded interaction, its actions in order.
- **Trace** — one invocation's actions, correlated by trace identity.
- **Behavior sequence** — the ordered behaviors an actor exhibited, and where
  the sequence departed from what it had learned.
- **Decision timeline** — decisions in order, with the scores behind them.
- **Behavior detail** — one fingerprint: what it is, when first seen, how
  often, and what it scored.

## Non-goals

**No raw content rendering, as a requirement or an option.** No prompt, no
completion, no reasoning, no tool arguments, no tool results, no retrieved
documents, no HTTP bodies, no SQL text, no arbitrary span attributes. Not
because the screen lacks room — because Trustvian does not retain them, and
this task must not create a reason to start.

**No storage of its own.** No table, no cache, no browser-held history. It
reads what 067 made durable.

**No second engine.** Anomaly, trust, risk and decision are read as recorded.
The explorer computes no score, recomputes no verdict and re-derives no
sequence judgement.

**Not a general trace explorer.** No span-tree waterfall for its own sake, no
latency flamegraph, no arbitrary attribute search, no cross-service dependency
map. Those are a trace tool's job and Trustvian sits happily beside one.

**No retention policy.** 067's.

## What a view looks like

At semantic fidelity, when the producer supplied agent-oriented telemetry
(task 075):

```text
SESSION   support-agent · local · run-candidate

├── model  gemma3:4b          familiar
├── crm_lookup                familiar
├── knowledge_search          familiar
├── export_customer           NEW      anomaly 0.91  risk high  trust 0.42
└── send_email                unusual sequence
```

At transport fidelity, when it did not:

```text
SESSION   support-agent · local · run-candidate

├── POST → ollama.localhost      familiar
├── GET  → crm.localhost         familiar
├── GET  → knowledge.localhost   familiar
├── POST → export.localhost      NEW      anomaly 0.91  risk high  trust 0.42
└── POST → mail.localhost        unusual sequence
```

**The same view, at whatever fidelity the telemetry provided.** The explorer
states which it is rather than implying the richer one — task 075's fidelity
indicator is what makes that honest.

## Correlation model

The explorer joins what Trustvian legitimately holds:

```text
trace id ─┐
span id  ─┤
session  ─┼─▶ one action ─▶ fingerprint ─▶ behavior (operation · target)
          │                      │
          │                      ├─ new behavior?
          │                      ├─ anomaly score · confidence
          │                      ├─ trust score · risk level
          │                      └─ decision · policy outcome
          │
delegated─┘   one hop, as v0.7 defined it
```

Sequence judgements — *unusual sequence* above — are read from what the engine
recorded, never recomputed in a view. If the engine did not record a sequence
signal for an action, the view says nothing about its sequence.

## Bounds

Every view is bounded, and the bounds are explicit:

- a session view renders at most a bounded number of actions, with explicit
  saturation when there were more;
- pagination follows the repository's one shape — immutable-key keyset
  cursor, bounded page, exclusive `after`, continuation only when another row
  follows — as tasks 065 and 074 established;
- no view accumulates unbounded client state;
- authoritative counts come from authoritative reads, never from what a view
  happened to render.

**Saturation is stated, never silent.** A truncated session must not read as a
complete one — the same rule task 074 applies to its graph, for the same
reason.

## Security and privacy

The content boundary is the point of this task, not a constraint on it.

| Property | Held by |
|---|---|
| no prompt, completion, reasoning, argument, result, document, body or arbitrary attribute | none is retained by 067 or requested here |
| rendered values are text, never markup | the existing WebUI rule |
| no browser storage of evidence | the existing rule |
| strict CSP, no external asset, no CORS | unchanged |
| no new privacy surface from a detail panel | an expanded row shows the same allowlisted fields |

**A detail view is not an exemption.** The temptation in an explorer is that
one more click could show one more thing; every field it shows must be one
067 durably retains and this task's contract names.

## Compatibility

Additive. Whatever routes this needs follow the established collection
semantics. No existing route, field or realtime contract changes.

## Tests

- Each view renders from authoritative reads only; a test asserts no score,
  verdict or sequence judgement is computed client-side.
- Fidelity is stated: the same evidence at transport and semantic fidelity
  renders the appropriate labels, and the lower one is never presented as the
  higher.
- **Privacy tripwire**: distinctive prompt, completion, argument, result,
  document and attribute values are injected upstream and asserted absent from
  every explorer-reachable payload and rendering.
- Correlation: actions of one session group correctly; two sessions never
  interleave; a delegation hop renders as one hop and no graph is inferred.
- Bounds: every view saturates explicitly, pagination has no duplicate or
  omission under concurrent writes, and client memory does not grow with
  history size.
- Absence: a run with no retained history renders an explicit empty state, not
  an error and not a fabricated summary.
- Backends: identical results on SQLite and PostgreSQL.
- Boundary: the WebUI gains no control-plane authority; no `TraceEngine` or
  equivalent exists; no `internal/*` core import enters the platform.

## Documentation

Written by the implementation PR: `docs/webui.md`, `docs/DOMAIN.md`,
`docs/SECURITY.md`, `docs/ARCHITECTURE.md`, `docs/ROADMAP.md`, this task's
status, the task index and `CHANGELOG.md`.

## ADR

Warranted if this task adds a query capability of its own. It should record
why the explorer answers a behavioral question rather than a trace question,
and why content is excluded by design rather than by omission.

## Acceptance criteria

1. A developer can follow one session and see each action, whether it was
   familiar or new, and the scores and decision recorded for it.
2. The view states its fidelity and never implies semantics the telemetry did
   not carry.
3. No prompt, completion, reasoning, argument, result, document or arbitrary
   attribute is reachable through any explorer surface.
4. Every view is bounded, and truncation is explicit.
5. Every number shown comes from authoritative state; nothing is recomputed in
   a browser.
6. Task 067 remains the owner of retained history; this task stores nothing.
7. Identical behaviour on both persistence backends.
8. A developer can answer *why did Trustvian call this new* without opening
   another tool.

## Open questions left to implementation

1. **Which views ship first.** Session is assumed primary; trace and decision
   timeline follow.
2. **Whether trace correlation needs its own route** or is a filter on a
   session route. A filter is assumed.
3. **How sequence drift is surfaced**, which depends on what the engine
   records — to be settled against the signal set at implementation time.
4. **Whether 067's retention proves sufficient**, which is the one question
   that could narrow this task's scope and must be re-checked once 067 is
   specified.
