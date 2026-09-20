# 0026 — Evaluation aggregation is bounded evidence, not event history

**Status:** Accepted

## Context

[Task 053](../tasks/v1.0/053-evaluation-result-aggregation.md) is the first
platform code that consumes behavioral evidence. It turns a stream of
`trustvian.DecisionRecord` into a summary of what an evaluation observed.

The obvious implementation retains the records. It is obvious because it is
useful: a later task wanting per-fingerprint counts, or the full decision
timeline, or a diff against another run, would find everything it needed
already in memory. Several later tasks want exactly that — 054 compares
behavior across runs, 055 aggregates into a scorecard, 057 persists, 058
serves, 067 owns history.

So the decision is not "should aggregation be bounded" in the abstract. It is
whether the *first* consumer of core evidence establishes a shape that grows
with the stream, because every task after it will inherit whichever shape this
one picks.

The core already answered the same question for itself. `Baseline` holds
bounded statistics rather than an event log; every map inside it is capped
([ADR 0019](0019-bounded-fingerprint-admission.md) and the sequence-state
ADRs); and `DecisionRecord` deliberately excludes `Event.Attributes` so it
cannot become an archive of producer payload. A platform aggregate that grew
without limit would reintroduce, one layer up, precisely what the engine
refuses to be.

## Decision

`EvaluationAggregate` is a **fixed-shape, O(1)-memory value**. Its size does
not change as the number of aggregated records grows.

It holds four identifiers captured once from the `EvaluationRun`, a counter,
two timestamps, four fixed counter structs, and five four-field numeric
summaries. It holds:

- no `[]DecisionRecord` or any other record slice;
- no `Result` and no `Event`;
- no map keyed by actor, fingerprint, session, policy rule, contributor, or
  any other caller-controlled value;
- no `EventID` set for deduplication.

Every one of those is a cardinality-indexed structure whose size is chosen by
whoever produces the events, which is the definition of the bound this
project keeps refusing to leave open.

### Consequences the later tasks inherit

**Task 054 (behavioral diff)** cannot ask this aggregate what behaviors
occurred, because it does not remember. It needs its own input contract with
its own bound — a capped set of fingerprints, a sampled window, or a
comparison computed at ingest. That is more work than reading a retained
slice, and it is the work that keeps a diff over a long evaluation from
costing memory proportional to the evaluation.

**Task 055 (scorecards)** consumes this aggregate as evidence. It does not
extend it. A scorecard is an interpretation with its own thresholds; putting
one here would mean a value that reports facts also rendering a judgement,
and the judgement would be the thing people read.

**Task 057 (persistence)** stores a fixed number of columns per aggregate. It
does not inherit a growing blob, and it does not need a retention policy for
something this type accumulated by accident.

**Task 067 (event history)** remains the only place raw history may live, and
remains an explicit capability with its own boundary — not a side effect of
having aggregated something.

### Deduplication is not solved here

One `AddRecord` call is one observation; the same record twice counts twice.
Detecting a duplicate requires remembering every identifier seen, which is the
unbounded structure this ADR exists to exclude — and it would still be the
wrong layer, because idempotency needs a retention window and a durable
identity that only an ingest or persistence boundary can define.

Stating this is part of the decision. An aggregator that silently counted
duplicates twice *without* saying so would look like a bug the first time
someone retried a delivery.

### Only `DecisionRecord` crosses the boundary

The aggregator consumes `trustvian.DecisionRecord` and nothing else from the
core — not `Result`, whose stage fields have `internal/` types a platform
cannot name, and not any internal package.

That is what [task 050](../tasks/v1.0/050-public-serializable-decision-record.md)
built the record for, and this is the first occasion to prove it: the platform
began consuming engine evidence without a single core change. Where a public
type exists (`event.ApprovalStatus`) it is imported; where the core keeps a
type internal (`policy.Decision`, `trust.RiskLevel`) the platform re-declares
the small closed set of string values rather than asking the core to widen its
public surface.

## Alternatives considered

**Retain records, bound the count.** A ring buffer of the last N records would
be bounded and would give later tasks something to work with. Rejected: N is a
number nobody can justify without knowing what reads it, a truncated history
is worse than none for a diff (it silently answers about a window while
looking like it answered about the run), and it would still be raw evidence
retention — the thing task 067 owns — arriving through a side door.

**Retain a bounded per-fingerprint map, as `Baseline` does.** Tempting,
because the core already accepts this shape with a 512-entry cap. Rejected
here for a different reason than boundedness: it is task 054's design
question, and answering it now would fix the diff's input contract before
anyone has written a diff. `Baseline`'s cap exists because a learned model
genuinely needs per-fingerprint state; an evaluation summary does not, yet.

**Compute richer statistics — variance, percentiles, histograms.** Rejected
for now. Variance is O(1) via Welford and would be defensible; percentiles are
not, without either sample retention or an approximation whose error budget
someone must own. Nothing consumes either today, and
[CLAUDE.md](../../CLAUDE.md) says not to add a knob without a caller.

**Let the aggregate also decide pass/fail.** Rejected, and worth naming
because it is the most natural thing to add next. A gate needs thresholds, and
thresholds are configuration; an aggregate that carried them would become the
place decisions are made while looking like the place facts are counted. Task
056 owns gates, and it will read this.

## Consequences

The aggregate is cheap to hold, cheap to persist, and safe to keep for as long
as anyone wants, because its size is known at compile time.

It is also lossy, permanently and by design. Any question needing per-record
detail — which fingerprints appeared, what the decision timeline looked like,
whether a specific event was counted — cannot be answered from an aggregate
and must be answered by whatever retains records under its own explicit bound.

The floating-point sums it does keep make it deterministic for a given ordered
stream but not bit-identical under arbitrary reordering, since floating-point
addition is not associative. Counts, minima, maxima, and the time range are
order-independent. Gates must therefore rest on categorical and count evidence
for critical conditions rather than on an average — which is the same
discipline `policy.Evaluate` already applies by never letting a score override
a fail-closed default.
