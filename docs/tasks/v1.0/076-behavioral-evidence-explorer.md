# 076 — Behavioral Trace & Session Evidence Explorer

Status: specified; **implemented** — see [§ Implementation](#implementation)
for what shipped, and [§ Narrowed criteria](#narrowed-criteria) for the two
presentations that narrowed because 067 does not retain what they would have
needed
Milestone: `v1.0`
Depends on: [067](README.md) — event-history capability boundary,
[074](074-zero-input-live-behavior-webui.md),
[075](075-ai-semantic-telemetry-normalization.md),
and — added by [082](082-agent-inspection-and-evaluation-depth.md) —
084 (parent span, duration, error status) for the timeline, and
085 (evidence resolution) for what its views navigate.
**085 does not depend on this task**; the edge runs one way, and 085 ships
complete without a browser change
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

## Current gap, as it stood when this was written

The table below described `main` at specification time. **Three of its five
rows are now stale, and they are the three this task was waiting on** — 067,
084 and 085 all shipped before implementation began. It is kept as written,
with the current state beside it, because the gap is what motivates the task
and rewriting it would erase the reason it exists.

| Claim | At specification time | Now |
|---|---|---|
| `DecisionRecord` carries `TraceID`, `SpanID`, `SessionID`, `DelegatedFrom` | **True** | Unchanged, plus 084's `ParentSpanID`, `SpanLineage`, duration and span status |
| The platform persists no per-record correlation | **True** | **Stale** — 067 retains one row per observation, bounded at 4096 per run |
| Realtime carries correlation only while a connection is open | **True** — bounded and ephemeral, ADR 0032 | Unchanged, and still the only place fidelity and behavioral layer travel |
| No session, trace or sequence view exists in any interface | **True** | **Stale** — this task is what closed it |
| Task 067 is approved and unspecified | **True** | **Stale** — specified and implemented |

So the explorer could not be built on what was stored then, and this task did
not pretend otherwise. It was built on what 067 made durable and what 085 made
resolvable, and it added **no storage and no resolution of its own**.

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

> **Corrected by implementation.** Two things in the two sketches above are not
> what shipped, and both for the same reason: **067 does not retain them.**
>
> - **Fidelity is not stated per observation.** Fidelity and behavioral layer
>   travel beside a record at ingest and on the realtime stream, and 067's
>   retained column set includes neither — so a historical view genuinely does
>   not know whether `crm_lookup` came from agent-oriented telemetry or from its
>   transport. The views render the recorded descriptor exactly as it stands and
>   say that fidelity was not retained. The **Live** view still states fidelity,
>   because realtime carries it.
> - **`unusual sequence` is not shown at all.** The engine's sequence signals
>   live in the anomaly contributors, which 067 deliberately excluded as its one
>   variable-length field. Nothing durable says whether an observation departed
>   from a learned sequence, so no view says anything about it.
>
> `familiar` / `NEW` and the scores **are** shown, because `NewBehavior` and all
> five scores are retained. See [§ Narrowed criteria](#narrowed-criteria).

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

## Amendment — task 082

[Task 082](082-agent-inspection-and-evaluation-depth.md) planned inspection and
evaluation depth against this specification and changed three things about it.
Nothing above is withdrawn; the sections below are additive, and where one
touches a non-goal it says so explicitly rather than quietly widening it.

### A trace tree and a timeline are in scope, narrowly

The **Non-goals** section above refuses "no span-tree waterfall **for its own
sake**, no latency flamegraph, no arbitrary attribute search, no cross-service
dependency map". That refusal stands, and the qualifier was load-bearing: a tree
*in service of explaining a behavioral verdict* is a different thing from a
waterfall as a product surface.

So the scope gains two views, and only two:

- **Trace tree** — the parent/child structure of one invocation's observed
  actions, so a developer can see that `export_customer` issued the HTTP request
  beneath it rather than inferring it from ordering.
- **Timeline** — the same actions in time, carrying each one's duration and
  error status.

Both are blocked on **084**, which is the point of recording this here. `event.Context`
carries `TraceID` and `SpanID` and no parent; both adapters bridge span duration
and status into `Event.Attributes` for `features.Extract` and neither reaches
`DecisionRecord`. A tree drawn without parent identity would be a tree inferred
from timing, and a timeline without duration would be a list.

What stays refused, unchanged: a flamegraph, arbitrary attribute search, a
cross-service dependency map, and any view whose purpose is trace exploration
rather than explaining a recorded decision.

### Trace order is not reasoning, and the view must not imply it is

A new guardrail, and it belongs here because this is the task that renders
ordering. A trace tree shows what was observed and in what structure. It does
**not** show why the model chose anything, and a view that presents span order as
the agent's reasoning, plan or intent is asserting something the telemetry cannot
support — the same fabrication [075](075-ai-semantic-telemetry-normalization.md)
refuses for operation names.

Concretely: no label may read "the agent decided to", "then chose", "because", or
any causal connective between two spans. Sequence *deviation* may be stated
because the engine recorded it; sequence *intent* may not, because nothing did.
`CHAIN` spans are unmapped for a related reason — the convention itself calls
them glue code between steps — and this view must not reconstruct a plan from
what it does have.

### Version provenance is shown, and absence is shown as absence

A comparison is only interpretable if a developer can see what the two sides
*were*. `CandidateMetadata` already carries `Label`, `SourceRef`,
`ArtifactDigest`, `Model`, `ToolsetDigest` and `ConfigDigest`; [086](082-agent-inspection-and-evaluation-depth.md#086--scenario-and-input-versioning)
adds a prompt reference and scenario/input digests.

This task renders whichever of them the producer supplied, and renders the rest
as **not stated** — never as blank, never as a default, and never omitted so that
a reader assumes the two sides matched. An unknown model is materially different
from a model both sides shared, and a view that cannot tell them apart is worse
than one that says nothing.

### What this task still does not own

Unchanged and worth restating, because the amendment adds navigation:

- **085 owns resolution**, this task owns presentation. 085 is the authoritative
  capability and is complete, tested and shippable over `/v1` and the CLI before
  this task exists; this task *consumes* it. The views navigate resolved evidence
  and compute nothing — not which observations produced a finding, not a count,
  not a rate, not a verdict.
- **067 owns retention.** If 067 retains no parent span identity, the tree
  narrows and this specification is amended again — it does not add a store.
- **No content**, and the amendment adds no field. The tripwire test above
  extends to the tree, the timeline and the provenance panel.

### Amended acceptance criteria

Additional to the nine above, not replacing them:

9. A trace tree is drawn from recorded parent identity, never inferred from
   timestamps or ordering; with no parent identity retained, the view says so.
10. A timeline states duration and error status where the telemetry supplied them
    and "not available" where it did not — never `0` and never "no errors".
11. No rendering asserts model reasoning, intent or causality between spans.
12. Version provenance renders every unsupplied field as explicitly not stated.
13. **From a gate FAIL, a developer reaches the contributing behavioral
    identities in one step, and from one of those the retained observations,
    without typing an identifier.** Moved here from 085, which owns the
    capability: reaching it *in a browser, in one step* is a property of this
    view, not of the resolution route.
14. The browser reaches 085's resolution through the control plane and holds no
    resolution logic of its own, proven by the existing WebUI boundary test.
15. Resolved evidence renders identically to what `/v1` returned for the same
    finding; a test compares the rendering's inputs against the route's response
    rather than trusting the view.

## Implementation

Shipped as one **Evidence** tab in the existing WebUI, over one new query
capability and no new storage.

### What was added, and where

| Layer | Change |
|---|---|
| `platform` | `ObservationScope` — three optional, mutually exclusive narrowings — and `ControlPlane.FindEvaluationRunObservations`. `ObservationFilter` gains `SessionID` and `TraceID`, each matched through 067's digest index **and** against the original value |
| `platform/httpapi` | `GET /v1/evaluation-runs/{run_id}/observations` gains `session_id`, `trace_id` and `fingerprint_id`; the response echoes the scope it applied. No new route |
| `platform/webui` | `trace.js` (recorded-structure and honest-fact rendering), `evidence.js` (the surface and its controller), plus an Evidence panel, allowlists and styles |
| Storage | **Nothing.** 067's two tables and four indexes are unchanged, and there is no migration |

[ADR 0049](../../adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md)
records why the narrowing is three mutually exclusive equality predicates on an
existing route, why a combination is refused rather than answered, and why the
explorer answers a behavioral question rather than a trace question.

### The views

Three sub-sections, six presentations:

```text
Finding      a gate check or a behavioral delta → the behaviors that
             contributed → the retained observations that carried them
Run history  session actions · trace context (with recorded structure) ·
             behavior sequence · decision timeline · behavior detail
Provenance   both sides' supplied CandidateMetadata, side by side
```

**The gate-to-evidence path types nothing.** A comparison under **Compare**
renders an evidence control on every gate check and every behavioral delta;
the finding reference is built from the comparison's own echo of the two run
identifiers. From a resolved observation, `Session`, `Trace` and `Behavior`
controls open the run-history views with the identifier the observation
recorded — and each is offered only when that observation actually carries one.

**A shared behavior is offered one control per side and no default.** Presence
decides the side for `added` and `removed`, so no side is sent and the control
plane derives it; `shared` gets two controls, because defaulting to the
candidate would answer a question nobody asked with an answer indistinguishable
from the one they wanted.

### Honest rendering

- **A parent this page does not have is not a root.** Structure is drawn from
  the recorded parent span reference and from nothing else — never from
  timestamps, adjacency, name similarity or ingestion order. Seven parent
  states are distinguished: `root`, `child`, `unresolved` (never retained,
  sampled away, or outside the rows read), `ambiguous` (more than one retained
  observation carries that span id), `self`, `cycle` and `unstated`. The tree
  emits every input row exactly once, indents to a bound, and states clamping.
- **Unavailable is not zero, and unset is not success.** A measured `0`
  renders as `0 ms (measured)`; an unmeasured duration renders as
  `not available`. Only `ok` is success and only `error` is failure; `unset`
  and unavailable say what they are.
- **"New to this run" is not novelty against a learned baseline.** The label
  says which it is. Nothing is called novel, anomalous, suspicious or unsafe.
- **No rendering asserts reasoning, intent or causality.** Asserted by a test
  that collects every sentence the surface can render and fails on any
  intent word or causal connective.
- **Durations are per observation and are never summed**, because a sum of span
  durations is not wall-clock latency.

### Bounds and client state

One page at a time, `after` exclusive, `limit` defaulting to 64, continuation
offered exactly when the route publishes a cursor. A continuation **replaces**
what is on screen, so a history costs a page however far it is read; the
controller holds no row array at all, asserted by test. Changing the run, view,
side, finding or identifier bumps a generation token and resets the cursor — a
response issued under an old token is discarded rather than drawn, and every
awaited read is followed immediately by that check.

## Narrowed criteria

Two of the fifteen criteria are narrowed, both because 067 does not retain what
they would need. Neither is worked around, and neither is fabricated.

| Criterion | Status |
|---|---|
| **2** — the view states its fidelity | **Narrowed.** Fidelity and behavioral layer are not retained per observation; they ride on the ingest envelope and the realtime frame only. Historical views state that fidelity was not retained and render the recorded descriptor verbatim. Nothing infers semantic fidelity from the shape of a descriptor, which would be the fabrication 075 refuses for operation names. The Live view is unchanged and still states fidelity. |
| **Scope § behavior sequence** — *"where the sequence departed from what it had learned"* | **Not implemented, deliberately.** Sequence signals live in the anomaly contributors, which 067 excluded as its one variable-length field. No view states a sequence deviation, and the behavior-sequence view says so. Open question 3 is answered: the required evidence was not retained. |

Both are stated on screen rather than left as an omission, and both are pinned
by tests that fail if a future change starts deriving either.

One further presentational decision, recorded because it is a deliberate
subtraction rather than a limitation: **`policy_reason` is retained and is not
rendered.** It is the single free-text, producer-supplied value on the retained
row; task 059 already keeps it off the realtime projection for that reason, and
the WebUI's field-allowlist guard forbids it. `policy_rule` — the identifier
naming which rule decided — is rendered in its place.

Everything else in [§ Acceptance criteria](#acceptance-criteria) and
[§ Amended acceptance criteria](#amended-acceptance-criteria) is implemented,
including criteria 9–15 from the task 082 amendment.

## Validation

At the store, control-plane, `/v1` and browser levels.

- **Both backends.** The new correlation predicates run in the shared
  conformance suite; the PostgreSQL half is skipped without a database and was
  run against a real one for this change.
- **Pure browser logic under `node`**, the arrangement
  [083](083-behavioral-layer-classification.md) established: the trace tree
  against every degenerate parent reference, the duration and status mappings
  including a nanosecond count above 2^53, every rendered sentence against
  intent and causality words, and the controller against pagination reset,
  out-of-order responses and unbounded state. Structural assertions hold when
  `node` is absent.
- **Rendering against the route.** A cross-layer test builds real evidence
  through the real ingest path, reads `/v1`, runs the **shipped** row
  projection over the response under `node`, and compares each cell against the
  field it came from — the criterion-15 check, asserting the rendering's inputs
  rather than trusting the view.
- **Privacy tripwire** extended to all three narrowed reads, each swept
  individually rather than assumed to inherit the unnarrowed page's result.
