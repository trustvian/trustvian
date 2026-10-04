# 0062 — Trace investigation is a waterfall over retained observations

**Status:** Accepted

## Context

[Task 100](../tasks/v0.11/100-trace-investigation.md) asks for trace
investigation in the manner of Grafana Tempo and Jaeger: a searchable trace
list, a hierarchical timeline with duration and status, and a details panel.
Those tools sit on a trace store that holds every span, its attributes, events
and links. Trustvian does not, and the question this records is what an honest
version looks like over what it does hold.

What task 067 retains per run, up to 4096 rows, is one **observation** per
accepted decision record. For trace investigation each carries the recorded
`trace_id`, `span_id`, `parent_span_id`, `span_lineage`, the producer's
`timestamp` (span start, for OTLP ingest), a measured `duration_nanos` or none,
and `span_status`. [ADR 0049](0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md)
already lets a reader narrow a run's history to one trace. What was missing was
the list a trace is chosen from: a trace was reachable only by its identifier,
read off one observation.

## Decision

### 1. One run-scoped collection, and no new retention

`GET /v1/evaluation-runs/{run_id}/traces` lists the distinct trace identifiers
in the run's retained history with, for each, the retained-observation count,
the first and last ingest sequence, and the count of observations whose
recorded span status is `error`. It is one bounded `GROUP BY` over at most 4096
rows, inside the same read snapshot as the history state, which it returns.

- **Ordered by first retained sequence**, which is also the keyset cursor.
  Retention only appends, so a trace's first retained sequence never changes —
  the immutability ADR 0041 requires of a cursor. A trace identifier is not
  used as the key: it is unbounded and indexed by digest.
- **Grouped by the digest key and the value**, so a collision splits into two
  entries rather than merging two traces.
- **Run-scoped**, like every correlation read (task 084).
- **A capability of its own** (`TraceSummaryStore`), in the full `Store`
  contract and the conformance suite, rather than a widening of
  `ObservationStore`.

Nothing new is retained. Spans the engine never evaluated, attributes, events,
links, resource and service names, and content stay out of the platform.

### 2. The waterfall's structure is the tree's; its placement is labelled

Rows, depth and parent state come from the existing `buildTraceTree` — the
recorded parent reference and nothing else. The waterfall adds only placement:
a bar starts at the recorded timestamp, relative to the earliest on the page,
and is as long as the measured duration. An unmeasured duration is a marker,
never a zero-width bar. A row without a readable timestamp is listed without a
bar. Timestamps are never used to infer parentage or order, and producer clock
skew is stated rather than corrected.

### 3. The page says what it is

Each row is an **evaluated action**. The view says, in the shell and above the
waterfall, that this is not a complete distributed trace and what is not
retained. A trace longer than one page of 64 is paged, and a parent on another
page reads as unresolved — the tree's existing honesty about bounded pages.

### 4. Selection redraws nothing

Selecting an action marks it and opens a details panel built from the row
already on screen; it issues no request and redraws neither the list nor the
waterfall, so the selected trace, the filter and both scroll positions survive.
Arrow keys move the selection with the panel following, Enter opens it,
Escape closes it and returns focus. Beside a wide waterfall focus stays on the
row; on a narrow screen the panel is a drawer and focus moves into it.

## Alternatives considered

**Assemble the trace list in the browser** from pages of observations. Reads
up to 64 pages to list one run's traces, or shows the traces of one page as if
they were the run's. Rejected — the crawl ADR 0041 refuses, or a list that
misreports itself.

**Retain spans (or span attributes) to draw a complete trace.** A retention
change with privacy, storage and schema consequences that this milestone has
no mandate for, and that a dedicated trace backend already does better.
Rejected; a deployment that wants complete traces keeps its Tempo or Jaeger.

**Order by trace identifier.** The identifier is unbounded and indexed only by
digest, so a cursor over it would be an unbounded value in a URL and an
ordering the index cannot serve. Rejected for first sequence, which also lists
traces in the order the run produced them.

## Consequences

- One additive `/v1` route, documented in [compatibility](../compatibility.md).
- The browser can list a run's traces and draw one as a waterfall; Overview,
  a run's workspace and an observation's details link into it, and Evidence →
  Run history offers the same list as a selector for its trace narrowing.
- A row without a measured duration, or without a readable timestamp, is
  visibly different from a short one.
