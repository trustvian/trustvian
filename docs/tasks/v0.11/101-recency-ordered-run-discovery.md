# 101 — Recency-Ordered Run Discovery

Status: Planned
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience)
Depends on: [ADR 0041](../../adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)
Decision record: [ADR 0063](../../adr/0063-recency-is-a-stored-sort-key-and-a-composite-cursor.md)

## Problem

The question a dashboard most wants to answer — *what ran most recently in
this project?* — has no honest answer today. `/v1` publishes runs only per
candidate, ordered by identifier, and a "recent runs" view assembled in the
browser would be the crawl ADR 0041 refuses: every agent, every candidate,
every run page, then a sort.

The same gap applies to scenario executions, which have no collection at all.

## Why it is not a quick route

ADR 0041 orders every collection by immutable identifier on purpose: timestamps
are stored as `RFC3339Nano` text, which is not lexically ordered, so a
timestamp cursor silently skips and repeats rows. "Newest first" over an
unbounded history was explicitly left to a later decision. A recency collection
needs one of:

- a sortable, fixed-width creation key stored beside each run (a schema
  migration on both backends), or
- a bounded "most recent N" read that is not paginated at all, and says so.

Choosing between them is the decision this task exists to record.

## Decision

The first option: a fixed-width creation key stored beside each run, added
by schema v10 on both backends and backfilled from `created_at`. See ADR 0063.
Scenario executions gain the same kind of key for their start time in the
same migration (task 102 reads it).

## Scope

- Schema v10: `platform_evaluation_runs.created_order`, a 20-digit zero-padded
  UTC Unix-nanosecond key, and indexes for each scope.
- `GET /v1/projects/{project_id}/evaluation-runs/recent` with optional
  `agent_id` and `candidate_id` narrowing (a candidate requires its agent),
  ordered by `(created_order DESC, id DESC)`, keyset cursor
  `<created_order>.<run id>`, `limit` 1..64, `next_after` exactly when more
  exist.
- A narrowing that does not belong to the project — an agent of another
  project, a candidate of another agent — is refused, never silently widened.
- Overview's runs panel and every run selector read it, labelled as newest
  first within the stated scope.

## Tests

- Ordering newest first; equal creation times broken by identifier; the
  cursor boundary exclusive on both components; a run created mid-traversal
  does not appear behind the reader's position; invalid and mismatched
  scopes; a malformed cursor.
- Migration from v9 backfills every existing run; SQLite and PostgreSQL run
  the same conformance cases.

## Acceptance criteria

1. An ADR decides the ordering key and its migration.
2. The route pages consistently under concurrent creation on both backends.
3. Overview's runs panel can span the project, an agent or a candidate, and
   says which, newest first, with its pagination and read time.
