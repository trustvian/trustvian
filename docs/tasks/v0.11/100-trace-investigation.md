# 100 — Trace Investigation over Retained Evidence

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [067](../v1.0/067-event-history-capability-boundary.md),
[076](../v1.0/076-behavioral-evidence-explorer.md),
[084](../v1.0/084-correlation-operational-evidence.md),
[098](098-selection-based-context-workflows.md)
Decision record: [ADR 0062](../../adr/0062-trace-investigation-is-a-waterfall-over-retained-observations.md)

## What Trustvian retains

Task 067 retains, per run, up to 4096 **observations** — one per accepted
decision record — in ingest order. For trace investigation each carries:

| Field | Meaning |
|---|---|
| `trace_id`, `span_id`, `parent_span_id` | recorded correlation, as the producer sent it |
| `span_lineage` | whether the producer marked the span a root |
| `timestamp` | the producer's clock; for OTLP ingest, **span start** |
| `duration_nanos` | measured duration, absent when unmeasured |
| `span_status` | `ok`, `error`, `unset`, or absent |
| decision, risk, scores, behavior | what Trustvian decided about the action |

It does **not** retain: spans the engine never evaluated, span events, links,
attributes, resource attributes, service names, or any content. A run's
retained trace is therefore **the evaluated actions of a trace**, not the
distributed trace. There is no trace collection either: a trace is reachable
only by an identifier read off an observation.

## Scope

- One bounded, run-scoped collection:
  `GET /v1/evaluation-runs/{run_id}/traces?after=&limit=` — the distinct trace
  identifiers in the run's retained history, ordered by the sequence of each
  trace's first retained observation, with retained-observation and
  error-status counts and the history state.
- A **Traces** destination: a searchable trace list, a hierarchical
  waterfall for the selected trace, and a details panel on the right.
- Entry points from an observation's detail panel and from Overview.

## Technical requirements

1. The route follows ADR 0041: keyset over an immutable key (the first
   retained sequence, which retention never changes), exclusive `after`,
   `limit` ≤ 64, `next_after` exactly when more exist, 404 for an unknown run.
   One SQL statement inside the existing read transaction, on both backends.
2. Hierarchy comes only from `parent_span_id`, through the existing
   `buildTraceTree` and its seven states. Bars are placed by `timestamp` and
   sized by `duration_nanos`; a row without a measured duration is a marker,
   never a zero-width bar. Clock skew between producers is stated, not
   corrected.
3. The waterfall reads one page (64) of the trace at a time and says when a
   trace continues on another page.
4. Selecting a row opens details without redrawing the list, the waterfall or
   their scroll positions. Escape closes; focus returns to the row. Arrow keys
   move the selection. On a narrow screen the panel becomes a full-width drawer.
5. The details panel shows only allowlisted retained fields; `policy_reason`
   stays unrendered, as on every other surface.

## Tests

- Store: ordering, paging, exclusivity of `after`, a trace spanning pages of
  observations, empty trace ids excluded, error counting, unknown run, both
  backends through the conformance suite.
- HTTP: shape, limit bounds, malformed cursor, 404.
- Browser: waterfall geometry from timestamps and durations, unmeasured rows,
  ordering independence from ingest order, selection/Escape/focus.

## Acceptance criteria

1. A run's traces are listed, searchable over the loaded page, and paged.
2. A selected trace renders as a tree with timing bars, durations and status.
3. Clicking or pressing Enter on a span opens its details on the right while
   the selected trace, the filter and both scroll positions are preserved.
4. Keyboard selection, a visible focus ring and Escape-to-close work; on a
   narrow screen the panel is usable.
5. The view states what is not retained and never calls the retained actions a
   complete distributed trace.
