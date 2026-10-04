# 103 — Session Selection over Retained Observations

Status: Implemented
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [100](100-trace-investigation.md) (the same correlation-summary
shape)

## Problem

Evidence → Run history's session narrowing is the last identifier field with
no list behind it: the session ID has to be read off an observation row.

## Scope

- `GET /v1/evaluation-runs/{run_id}/sessions` — the distinct session
  identifiers in the run's retained history, with retained-observation and
  error-status counts and first/last sequence, ordered and paged by first
  retained sequence. The same query as task 100's traces over the session
  column; nothing new is retained.
- The session field becomes a searchable selector over that list, with the
  existing paste path as the fallback.

## Tests

- Store conformance on both backends; HTTP contract; the selector lists
  sessions for the chosen run and discards a list for a previous run.

## Acceptance criteria

1. A run's sessions are chosen from a list, never typed, with the retention
   limit stated: only sessions carried by retained observations are listed.
2. "Advanced: paste an ID" remains.

## What shipped

- One query for both correlation summaries (`runCorrelationPage`, over a closed
  set of two columns), `SessionSummaryStore` in the full contract, and the
  route with the shared paging helper.
- The evidence identifier selector lists sessions, traces or behaviors for the
  chosen run and view, and drops a list read for a previous run or view.
