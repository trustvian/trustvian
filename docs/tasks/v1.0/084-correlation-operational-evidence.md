# 084 — Correlation and Operational Evidence on the Record Boundary

Status: specified; implemented
Milestone: `v1.0`
Depends on: nothing new
Blocks: [076](076-behavioral-evidence-explorer.md)'s timeline,
[085](082-agent-inspection-and-evaluation-depth.md#085--evidence-links-from-a-finding-to-the-observations-behind-it),
[087](082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence),
and the counting correction [083](083-behavioral-layer-classification.md)
deferred
Planned in: [082 § item 084](082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary)

## Objective

Carry **parent span identity, duration and error status** across the
`Event` → `DecisionRecord` → control-plane boundary, without changing
behavioral identity or any decision the engine makes.

Trustvian already computed two of the three and discarded them at the boundary.
`features.Extract` reads `duration_ms` and `error` off `Event.Attributes`
because both adapters bridge them there — and neither reached `DecisionRecord`,
so the platform, the comparison and every view were unaware an observation took
any time or failed. Parent identity was never read at all.

## What this task does not do

- **No per-record history.** Task 067 owns that. This makes the fields
  *available* and aggregates them per run; it stores no observation.
- **No trace tree, timeline or evidence-resolution UI.** 076 and 085.
- **No token, cost or pricing fields.** 087.
- **No correlation-aware counting.** 083's fold needs more than parent ids;
  see [§ What 083 still needs](#what-083-still-needs).
- **No span attribute map**, no span events, no links. The record gains four
  named scalar fields and nothing that could become an arbitrary payload —
  which is the boundary ADR 0030 and task 050 drew, and the specific risk 082
  named for this item.
- No percentiles, no histogram, no rate computed for a caller.

## The contract

### Where each field lives, and who populates it

| Field | Lives on | Populated by |
|---|---|---|
| `ParentSpanID`, `SpanLineage` | `event.Context` → `DecisionRecord` | the two telemetry adapters, from span parentage |
| `Execution.DurationNanos`, `Execution.DurationObserved` | `event.Event.Execution` → `DecisionRecord` | the two adapters, from span timing |
| `Execution.Status` | `event.Event.Execution` → `DecisionRecord` | the two adapters, from span status |
| duration and status aggregates | `platform.EvaluationAggregate` | `AddRecord`, per record |

A caller constructing an `Event` by hand populates none of them, and gets the
documented *unavailable* state throughout. Nothing is inferred anywhere.

### Correlation

```go
type SpanLineage string

const (
    LineageUnspecified SpanLineage = ""      // nothing established parentage
    LineageRoot        SpanLineage = "root"  // the producer said this is a trace root
    LineageChild       SpanLineage = "child" // ParentSpanID names its parent
)
```

`ParentSpanID` is a **trace-scoped reference**: it is meaningful only together
with `TraceID`, is not globally unique, and must never be used as a standalone
key. Nothing here indexes it, and 067 must not either.

**Three states, and both source formats can establish two of them.** An OTLP
span carries an all-zero parent span id for a root, and the OpenTelemetry SDK
returns an invalid parent `SpanContext` for one — so each adapter can say
*root* or *child*. Neither format can express "the producer does not know", so
neither adapter ever emits `LineageUnspecified`; that value exists for an
`Event` that did not come from a span at all. **This is a limitation of the
formats, stated rather than papered over**: a truncated or sampled trace whose
parent was dropped still reports `child` with a parent id that resolves to
nothing, because the child's own record is the only thing being described.

**No parent-existence check exists anywhere.** A child may arrive before its
parent, its parent may never arrive, and its parent may have been sampled away.
None of those is an error, and none is rejected — a validation that required a
parent to exist would drop exactly the evidence a lost parent makes valuable.

Parentage is read from the span's own parent reference and from nothing else.
**Never** from timing, adjacency, name similarity or ingestion order.

### Duration

Unsigned nanoseconds, with availability carried separately:

```go
type Execution struct {
    DurationNanos    uint64 // meaningful only when DurationObserved
    DurationObserved bool
    Status           SpanStatus
}
```

| Span timing | Result |
|---|---|
| end set, `end >= start` | observed; `DurationNanos = end - start`, **including exactly `0`** |
| end unset | unavailable |
| start unset | unavailable |
| `end < start` | unavailable — a negative duration is malformed, not zero |
| `end - start` exceeds `maxDurationNanos` | unavailable — see below |

**A genuine zero is preserved and is not the same as unavailable.** This is the
one place the evidence path deliberately diverges from the existing volatile
bridge, which writes `duration_ms` only when the duration is strictly positive.
Both readings are correct for their purpose and the divergence is asserted by
test rather than left to be discovered.

`maxDurationNanos` is 2^63 − 1 ns, about 292 years. A span longer than that is
a clock fault, not a slow call; it is reported unavailable rather than wrapped.
The bound also keeps every downstream sum inside `uint64` arithmetic that
already has an overflow contract.

Nanoseconds rather than the milliseconds the feature path uses: an integer
nanosecond count is exact for every span either adapter can produce, where
milliseconds as a float silently rounds sub-millisecond work — and a behavioral
security engine looking at tool calls sees a great deal of it.

### Error status

```go
type SpanStatus string

const (
    StatusUnavailable SpanStatus = ""      // nothing established a status
    StatusUnset       SpanStatus = "unset" // the producer explicitly left it unset
    StatusOK          SpanStatus = "ok"
    StatusError       SpanStatus = "error"
)
```

OpenTelemetry's status is a three-value enum whose default is `UNSET`, so a span
always yields one of `unset`, `ok` or `error`; `StatusUnavailable` is for an
`Event` that did not come from a span.

**`unset` and `` are not success.** OpenTelemetry's own convention is that
`UNSET` means the producer expressed no opinion, and most instrumentation never
sets `OK` at all. Counting either as a success would let a run of entirely
unstatused spans report a zero error rate, which is the specific claim this
vocabulary exists to prevent. Only `ok` is success, and only `error` is failure.

The existing `Attributes["error"] = true` bridge is unchanged: it is still
written only for an explicit `ERROR`, still read only by `features.Extract`, and
this task adds a parallel recorded path rather than moving it.

### The wire

`DecisionRecord` gains four fields, all `omitempty`:

```json
{
  "parent_span_id": "0102030405060708",
  "span_lineage":   "child",
  "duration_nanos": "1500000",
  "span_status":    "error"
}
```

`duration_nanos` is **canonical decimal text**, like every other `uint64` this
repository puts on a wire — a JSON number is a float64 to most parsers, and a
nanosecond count above 2^53 would round. The empty string is the unavailable
state, and `"0"` is a measured zero; `omitempty` therefore omits exactly the
unavailable case, which is what an older consumer already treats as absent.

**Older records stay valid.** Every field is optional and absent means
unavailable, so a record written before this task decodes unchanged and a
consumer ignoring the new fields is unaffected. The compatibility contract's
*additive fields* rule governs, and a round-trip test asserts it in both
directions.

### Aggregates

Two bounded, fixed-shape summaries on `EvaluationAggregate`, both `O(1)`:

```go
type DurationSummary struct {
    Count      uint64 // observations with an observed duration
    Unobserved uint64 // observations whose duration was unavailable
    Sum        uint64 // nanoseconds
    Min        uint64
    Max        uint64
}

type SpanStatusCounts struct {
    Unavailable, Unset, OK, Error uint64
}
```

**Denominators are explicit, and no rate is computed here.** `Error` over
`OK+Error` is a caller's question; this type publishes counts so that an
unstatused run cannot dilute anything. `Unavailable` and `Unset` are reported
separately for the same reason.

`Count + Unobserved == RecordCount` and `Unavailable + Unset + OK + Error ==
RecordCount` are invariants of every aggregate this code produces, asserted by
test. They are what makes "missing evidence" distinguishable from "measured
zero" without a second nullable field.

Overflow follows the existing contract exactly: any counter or the nanosecond
sum reaching `math.MaxUint64` returns `ErrAggregateOverflow` and the record is
refused, rather than wrapping.

**`Sum` is not run wall-clock latency and must never be presented as one.**
Spans nest and run concurrently, so summed durations double-count a parent and
its children and exceed the elapsed time of the run. It is the total observed
span time, and the field's documentation says so where a caller will read it.

### Where the summaries are exposed

On `platform.EvaluationAggregate`, through `Durations()` and `SpanStatuses()` —
the same Go control-plane surface every other aggregate reading uses, and the
authoritative one.

**No HTTP field is added, and that is a decision rather than an omission.** The
two obvious routes are both wrong for it today:

- `GET /v1/evaluation-runs/{id}/progress` reports from the *ingest cursor*, which
  loads its counts in one transaction precisely so the report cannot describe a
  state that never existed. Reading the aggregate as well would be a second read,
  and its own code comment says why that produces a cursor at N+1 beside evidence
  at N+2.
- The comparison surface is fixed-shape by
  [ADR 0028](../../adr/0028-scorecards-are-fixed-shape-comparative-evidence.md),
  and latency comparison is
  [087](082-agent-inspection-and-evaluation-depth.md#087--performance-and-cost-evidence)'s
  scope. Adding a field there now would settle 087's shape before 087 is
  specified.

So the evidence is carried, aggregated and persisted, and the consumer that
needs it over HTTP brings its own route. Nothing here is blocked by that: 076,
085 and 087 each add their own read surface.

### What is persisted

The two aggregates, as nine additional columns on the existing aggregate table,
in the same canonical-decimal-text form every other counter uses. **No
per-record row is added** — 067 owns that, and adding one here would be the
workaround this task explicitly refuses.

Schema version moves **5 → 6** on both backends, adding columns to one table.
Existing rows are migrated to say *unknown*, not *zero*:

```sql
UPDATE evaluation_aggregates
   SET duration_unobserved = record_count,
       span_status_unavailable = record_count,
       ... = '0'
```

That is the honest reading. Those records were ingested before the fields
existed, so no duration and no status was observed for any of them — and it
preserves the two invariants above, so a migrated run stays internally
consistent rather than becoming a run whose counts do not add up.

## Acceptance criteria

Against [082's list](082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary):

| # | Criterion | Status |
|---|---|---|
| 1 | A record from a span with a parent carries it; one from a root carries none, and the two are distinguishable | **Met** — `span_lineage` distinguishes them |
| 2 | Duration and status present when stated, explicitly absent when not — never defaulted to `0`/`false` | **Met** |
| 3 | The platform aggregates duration and error count per run, bounded and overflow-safe | **Met** |
| 4 | Nothing added enters `StableFeatures`, a fingerprint or a baseline key | **Met**, proven by test |
| 5 | A producer supplying neither produces the same behavioral result as today | **Met**, proven by test |
| 6 | Identical results on SQLite and PostgreSQL | **Met** on SQLite; PostgreSQL asserted by the shared conformance suite, which **skips without a database** — see [§ Validation limits](#validation-limits) |
| 7 | `features.Extract`'s reading of `duration_ms`/`error` is unchanged | **Met** — untouched, and a test pins the deliberate zero-duration divergence |

## What 083 still needs

Recording parent ids does **not** implement 083's counting correction, and 083
stays partially implemented. What this task unblocks is the *input*; what
remains is the policy:

1. **A bounded correlation structure.** Mapping observation → parent →
   behavioral identity is per-span state, and the 512-behavior cap does not
   bound span count. A bound has to be chosen and its saturation stated.
2. **An out-of-order rule.** A child span completes and exports before its
   parent, so the fold sees the child first and must handle a parent that never
   arrives.
3. **A decision about what a folded act reports** — one counted change with two
   contributing identities, or two with a stated relationship.
4. **Per-observation retention (067)** if the fold happens at comparison time
   rather than at ingest time, which is itself an open choice.

083's `TestCountingFoldIsNotImplementedYet` is unchanged and still fails the
moment the fold lands. Its `TestParentIsUnreachableAtCountingTime` is replaced
by `TestParentIsCarriedButCountingStillCannotFold`, which asserts the new state
of the world: the parent identity is now on the record, and the fold is still
not implemented.

## Validation limits

- **PostgreSQL was exercised**, against `postgres:17-alpine` with
  `TRUSTVIAN_TEST_POSTGRES_DSN` set, and the whole `platform` suite passes there.
  That mattered: the first run failed, because the PostgreSQL older-schema
  fixtures replay today's `CREATE TABLE` statements filtered by the version that
  introduced each *object* — exact while every version adds objects, and wrong the
  moment one adds a *column to an existing table*. A "v4" fixture was being built
  with schema 6's columns already present, so the migration under test failed on
  a column that existed. `stripLaterColumns` removes them, keyed off the same
  `aggregateOperationalColumns` list the DDL and the migration derive from.
- **PR CI does not run those tests.** They live in the nightly workflow, which is
  the only place a database is provisioned. So the evidence above is from a local
  run, not from this pull request's checks.
- The end-to-end suites that build the platform binary run under the module
  suites' defaults; no separate long run was performed.
- `TRUSTVIAN_TEST_POSTGRES_RESTART_CMD` was not set, so the nightly restart-drill
  case skipped.

## Documentation

`docs/OPENTELEMETRY.md` (what each adapter reads and the availability rules),
`docs/compatibility.md` (the additive record fields and schema 6),
`docs/ARCHITECTURE.md` (the boundary this does not cross), the task index, the
roadmap, `docs/tasks/v1.0/083-*.md`'s status note, and `CHANGELOG.md`.
