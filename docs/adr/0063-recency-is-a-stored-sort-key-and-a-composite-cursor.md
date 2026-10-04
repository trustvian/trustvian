# 0063 — Recency is a stored sort key and a composite cursor

**Status:** Accepted. Extends [ADR 0041](0041-bounded-hierarchy-collections-and-run-scoped-live-view.md) § 2.

## Context

ADR 0041 orders every collection by immutable identifier and left
"newest first over an unbounded history" to a later decision. The reason is
concrete: timestamps are stored as `RFC3339Nano` text, which does not sort —
Go trims trailing fractional zeros, so `…:00.5Z` sorts after `…:01Z`, and an
offset reorders it again. A cursor over that column silently skips and repeats
rows.

[Task 101](../tasks/v0.11/101-recency-ordered-run-discovery.md) needs that
decision: the Overview, the run selectors and the Runs destination all want
*the most recent runs* of a project, an agent or a candidate, and task 102 wants
the most recent scenario executions. Sorting one identifier-ordered page in the
browser and calling it "newest" would be a statement about one page presented
as one about the scope.

## Decision

### 1. A stored, fixed-width key

Schema v10 adds `created_order` to evaluation runs and `started_order` to
scenario executions: the creation (start) time as a 20-digit, zero-padded count
of UTC Unix nanoseconds. Byte order is time order on both backends, as
`observationSequenceKey` already makes byte order numeric order for sequences.

- **Derived, never read back as a time.** The key is computed from the time the
  row already stores — at every insert, and once by the migration's backfill —
  and `created_at` remains the time. A row whose key is empty or malformed is
  refused as corrupt rather than sorted somewhere.
- **No time outside 1970 to 2262-04-11**, the range an int64 of nanoseconds
  holds. Outside it `UnixNano` wraps to a plausible-looking key, so such a time
  is refused — at write, and by the migration, which then fails closed rather
  than backfill a key that sorts in the wrong place.
- **Columns and indexes only.** v9 and v10 hold the same tables, so the stamp
  alone routes the migration, exactly as it did for v5, v6 and v8.

### 2. Order and cursor

The order is `(key DESC, id DESC)`. Keys repeat — two runs can be created in
the same nanosecond — so the identifier is the tie-breaker and the cursor
carries both: `<20-digit key>.<id>`. The key's fixed width makes the format
unambiguous whatever the identifier contains. The predicate is
`key < k OR (key = k AND id < i)`, spelled out because SQLite and PostgreSQL
agree on that form.

The guarantee is ADR 0041's: a consistent **position**, not a snapshot. A run
created during a traversal is newer than the reader's position and does not
appear behind it; a read from the top shows it first. The UI says so and offers
a refresh.

### 3. An explicit scope, checked, never widened

`GET /v1/projects/{project_id}/evaluation-runs/recent` requires the project and
accepts `agent_id` and `candidate_id`. A narrowing that does not exist is
`404`; one that exists outside the scope above it — an agent of another project,
a candidate of another agent — is `400`. The narrowest identifier decides the
predicate. The route is a path of its own rather than a sort parameter on an
existing one: a route's order is part of its contract, and a parameter that
changed it would make one cursor mean two things.

### 4. Bounds

`limit` 1..64, `next_after` by the same one-row probe as every other route.
Three indexes serve the three questions: a candidate's runs `(candidate_id,
created_order, id)`, any runs `(created_order, id)` for the joined project and
agent scopes, and a project's executions `(project_id, started_order, id)`.

## Alternatives considered

**Parse the text in SQL** (`julianday` on SQLite, a cast on PostgreSQL). The two
backends parse to different precisions — milliseconds against microseconds — so
sub-millisecond ties would order differently on each, and neither can use an
index. Rejected.

**A non-paginated "most recent N".** Honest if it says so, but a Runs table and
a selector both need to read further than the first N. Rejected for the
paginated keyset.

**Denormalize project and agent onto each run** to index every scope directly.
Deferred, with its cost stated rather than hidden: the candidate scope is an
index range, but the project and agent scopes join through candidates and
agents, and a planner that walks `(created_order, id)` backwards filters
every newer run in the database until it has `limit` rows of the scope. The
*page* is bounded; the rows examined are bounded by the database's newer runs,
not the project's. That is acceptable for the local, single-team databases
this platform serves today, and two immutable columns — `project_id` and
`agent_id` on each run, with `(project_id, created_order, id)` and `(agent_id,
created_order, id)` indexes — are the change when a measured workload needs
it.

## Consequences

- Schema v10 on both backends, migrated forward from every earlier version,
  with a backfill whose result is asserted on each.
- Every write of a run or an execution stores its key.
- One new route; the identifier-ordered collections are unchanged.
