# 0049 — The evidence explorer narrows retained history, and answers a behavioral question rather than a trace question

**Status:** Accepted

## Context

[Task 067](../tasks/v1.0/067-event-history-capability-boundary.md) made
per-observation history durable and shipped exactly one read: a run's whole
history, one bounded page at a time.
[Task 085](../tasks/v1.0/085-evidence-resolution.md) added two more, each
answering "which evidence supports this finding".
[Task 076](../tasks/v1.0/076-behavioral-evidence-explorer.md) is the browser
surface over both, and it needs three views none of those reads can serve:

```text
which actions belonged to this session?
which actions belonged to this invocation, and how were they structured?
which observations in this run carried this behavioral identity?
```

Each is one equality predicate over a column 067 already retains and already
indexes. None is expressible as a filter over a page that has already been
truncated — task 085 documented why: filtering after `LIMIT` returns three rows
where sixty-four match, and makes the continuation cursor describe the
unfiltered stream rather than the answer.

So the explorer either gets a narrowing in storage, or it gets a client that
fetches whole histories and sorts them itself. The second is the thing every
bound in this repository exists to prevent.

Task 076's own specification anticipated this and asked for a record:
*"warranted if this task adds a query capability of its own. It should record
why the explorer answers a behavioral question rather than a trace question,
and why content is excluded by design rather than by omission."*

## Decision

### The narrowing is three optional, mutually exclusive predicates on the route that already exists

`GET /v1/evaluation-runs/{run_id}/observations` gains `session_id`, `trace_id`
and `fingerprint_id`. No new route, no new table, no migration, and no change
to any existing response.

**At most one may be set**, and more than one is refused with a diagnostic
rather than answered. Not because the SQL could not combine them — it can, and
`ObservationFilter` already does for task 085's gate checks — but because each
combination is a query shape nothing has measured and a view nobody specified.
A session inside a trace is a question this task does not ask, and answering it
speculatively would publish a contract on the strength of a guess.

**There is no decision or risk-level narrowing here.** Those belong to 085's
gate checks and are reached through the resolution routes. Duplicating them
would give a browser a second way to ask a question the control plane already
answers authoritatively, and two paths to one answer are two paths that can
disagree.

### Every predicate is run-scoped, and none is a standalone key

The predicate runs inside one run's rows, because `run_id` leads the primary
key and each of 067's three correlation indexes is run-scoped first. A session
or trace identifier repeated across two runs therefore resolves separately in
each, which is exactly the property
[ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)'s
task requires when it forbids treating a trace-scoped reference as a global
one.

**`ParentSpanID` gets no predicate at all.** Task 084 states that nothing
indexes it and that 067 must not either, because it is meaningful only beside a
`TraceID`. A parent is therefore reached by reading its trace, not by querying
for it — which is also what keeps a tree drawn from one bounded page rather
than by following references outward until something terminates.

### Each lookup compares the digest *and* the original value

067's three correlation indexes key on a hex SHA-256 of the value beside them,
because PostgreSQL refuses a B-tree entry over roughly 2704 bytes and none of
the three indexed values has a length bound the ingest path enforces. A digest
narrows; it does not identify. So each predicate filters on the key **and** on
the column beside it, and a planted collision is excluded by the value
comparison rather than by the digest width.

### Correlation values are not validated on the way in

`session_id` and `trace_id` are passed to the query as bound parameters without
going through `validateID`. Task 067 records that neither has any length bound
at ingest and that retention must never narrow which records the platform
accepts; refusing an over-long or unusual value on the read side would make a
row the platform legitimately retained permanently unreachable, which is the
same defect seen from the other end. `fingerprint_id` keeps `validateID`,
because task 085's resolution route already applies it to the same value and
two routes disagreeing about what a fingerprint may be is worse than either
rule alone.

### The explorer answers a behavioral question, and its shape says so

A trace viewer answers *what exactly happened inside this invocation*. This
surface answers *which observed actions constitute this actor's behavior, how
do they differ from the reference, and what decision followed*. The difference
is not a matter of emphasis; it is what makes the capability bounded:

- the second question is answerable from metadata, and the first is not;
- the second needs one run's retained rows, and the first needs a span store;
- the second has five views with fixed columns, and the first has an arbitrary
  attribute search.

Concretely, the surface gains a trace tree and a timeline **in service of
explaining a recorded verdict**, and nothing else a trace tool would have. No
flamegraph, no latency waterfall, no arbitrary attribute search, no
cross-service dependency map, and no span store of its own. Per-observation
durations are shown and never summed, because a sum of span durations is not
wall-clock latency and presenting it as one would be a measurement this
platform never made.

**Ordering is arrival order, and arrival order is not reasoning.** A parent
span ends after the children it started, so retained history is deliberately
sequenced by ingest rather than by timestamp. No rendering may assert that a
model decided, intended, chose or planned anything from that ordering, and no
label carries a causal connective between two spans. Sequence *deviation* could
be stated if it were recorded; sequence *intent* may not be, because nothing
records it.

### Content is excluded by the schema, not by the view

There is no filter in the browser removing prompts, completions, reasoning
traces, tool arguments, tool results, retrieved documents, HTTP bodies or
arbitrary attributes from what it displays. There is no column that could hold
one, so no response carries one, so no view can reach one. That is a stronger
guarantee than a filter applied on the way past, and it is why the boundary is
restated in the schema rather than in the renderer.

The one retained field the browser deliberately does not render is
`policy_reason`: it is the single free-text, producer-supplied value on the
retained row, task 059 already keeps it off the realtime projection for that
reason, and the WebUI's field-allowlist guard forbids it. `policy_rule` — an
identifier naming which rule decided — is rendered in its place, which is the
explanatory half without the free text.

## Consequences

**The browser holds no resolution logic.** It chooses a URL and renders a
response. Every status, side, count, verdict and exhaustiveness flag on screen
is a value `/v1` returned, asserted by a test that runs the shipped row
projection over a real route response and compares cell against field.

**Two facts task 067 did not retain narrow two views, and both are stated
rather than inferred.** Fidelity and behavioral layer travel beside a record at
ingest and on the realtime stream and have no retained column, so a historical
view cannot state which one produced an operation's identity — it says so, and
renders the descriptor exactly as recorded. Sequence-deviation evidence lives
in the anomaly contributors, which 067 deliberately excluded as its one
variable-length field, so no historical view makes a statement about sequence
deviation at all. Inferring either from the shape of what *is* retained would
be the fabrication [task 075](../tasks/v1.0/075-ai-semantic-telemetry-normalization.md)
refuses for operation names.

**Adding a fourth narrowing later is cheap, and adding a combination is not.**
A fourth equality predicate over an indexed column costs a field and a
diagnostic. A combination costs a measurement, because nothing here has one.

**No schema change, and no migration.** 067's tables and its four indexes are
unchanged, and a deployment upgrading into this gains the narrowings with no
data step.

## Related

- [ADR 0036](0036-webui-is-a-same-origin-adapter-over-v1.md) — why the browser
  is a static same-origin client with no control-plane authority
- [ADR 0041](0041-bounded-hierarchy-collections-and-run-scoped-live-view.md) —
  why discovery is bounded as a whole workflow, not only per route
- [ADR 0047](0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
  — behavioral identity per observation, and correlation as a trace-scoped
  reference
- [ADR 0048](0048-retained-history-is-sequence-identified-bounded-and-honest-about-absence.md)
  — the retention contract this narrows
- [Task 076](../tasks/v1.0/076-behavioral-evidence-explorer.md) — the
  specification
