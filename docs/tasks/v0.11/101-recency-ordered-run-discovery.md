# 101 — Recency-Ordered Run Discovery

Status: Planned — needs a decision before it needs code
Milestone: [`v0.11.0`](../../ROADMAP.md#v0110--webui-experience) records it;
it is not part of the milestone's build
Depends on: [ADR 0041](../../adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md)

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

## Scope when picked up

An ADR choosing the ordering key, then `GET /v1/projects/{id}/evaluation-runs`
bounded and paginated like every other collection, then the Overview panel
that reads it.

## Acceptance criteria

1. An ADR decides the ordering key and its migration.
2. The route pages consistently under concurrent creation on both backends.
3. Overview's "recent runs" spans the project rather than one candidate.
