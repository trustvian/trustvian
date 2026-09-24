# 0038 — Collector evaluation ingest is an HTTP adapter

**Status:** Accepted

## Context

[Task 073](../tasks/v1.0/073-otel-collector-evaluation-ingest.md) connects two
things that already existed and had never been joined.

The Collector processor maps a span to an `event.Event`, runs
`Engine.Analyze`, and enriches the span with five `trustvian.*` attributes.
The control plane accepts a `trustvian.DecisionRecord` at
`POST /v1/evaluation-runs/{run_id}/records`. Between them, nothing.

The consequence was not obvious until someone tried it: an evaluation was
reachable only by an application that imported Trustvian and posted records
itself. A service instrumented with OpenTelemetry and nothing else — the case
[ADR 0003](0003-opentelemetry-adapter-single-module.md) built the processor
for — could be scored on every span and evaluated on none.

## Decision

The processor gains an optional sink that posts the `DecisionRecord`
projected from the `Result` it already computed, over the versioned `/v1`
API, using client-side DTOs and no Go dependency on `trustvian-platform`.

### 1. HTTP, not a Go import

`trustvian-platform` depends on the root module's public API. The processor
does too. Importing the platform from the processor would not create a formal
cycle, but it would put the platform's SQLite driver, its schema and its
domain types into every Collector distribution built from this component —
including the overwhelming majority that enrich spans and evaluate nothing.

This is [ADR 0033](0033-developer-cli-is-a-thin-http-adapter.md)'s reasoning
applied a second time, and the second consumer is also the first evidence
that the `/v1` ingest contract works for someone other than the CLI.

The guard covers test files. `go.mod` does not distinguish a test-only
dependency, so an import in a `_test.go` reverses the edge just as
thoroughly. The end-to-end test therefore builds and runs the local runtime
as a subprocess and drives it over HTTP.

### 2. The record is projected, never reconstructed

One `Analyze` per span, and `Result.DecisionRecord()` called on that same
`Result`.

Rebuilding the record from the enriched attributes would have avoided
threading the `Result` through — and would have made an evaluation's evidence
a function of the enrichment format. The attributes are five values;
`Contributors`, `PolicyReason`, `AnomalyConfidence`, `ContextRisk`,
`IdentityConfidence` and the behavioral descriptor are not among them. A
scorecard built from that subset would be quietly poorer than one built from
the record, and nothing would say so.

### 3. The behavioral profile selects the learning scope

The platform's `BehavioralProfileRef` is its name for what the engine calls a
learning scope ([ADR 0024](0024-learning-scope-is-a-baseline-key-dimension.md)),
so configuring one selects both.

Without this, a Collector with a durable store evaluating two candidates
would train one baseline across both, each teaching the other — the precise
failure [task 051](../tasks/v1.0/051-behavioral-profile-learning-scope-isolation.md)
exists to prevent, reappearing at the one boundary that had no way to select
a scope.

Scope and environment are separate dimensions of `baseline.Key`, so
`DecisionRecord.Environment` still reports what the event carried and the
control plane's environment check is unaffected.

There is deliberately no second `learning_scope:` knob. Two independent
settings that must agree is a way for them to disagree.

### 4. Required is the only mode

`required: false` is rejected at startup rather than honoured.

An optional mode has to answer what happens when evidence cannot be
delivered, and both available answers are wrong. Dropping the record produces
a run whose counts are internally consistent, whose gate evaluates, and whose
evidence has holes nothing in the response describes. Reporting the gap needs
a partial-evidence concept the platform does not have — and
[ADR 0031 §10a](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
made empty evidence a *result* precisely so a candidate that ran nothing
fails rather than looking perfect. A silently truncated run reintroduces that
hazard one layer up.

The field exists rather than being omitted so a configuration states the
guarantee explicitly instead of relying on a default nobody read.

### 5. Failure is permanent, and that is a correctness property

An ingest failure returns `consumererror.NewPermanent`.

Without the wrapper the Collector retries the batch. A retried batch
re-analyzes spans whose records already committed and resends them under
*new* sequence numbers, which the server accepts because from its side they
are new records.
[ADR 0026](0026-evaluation-aggregation-is-bounded-evidence.md) made a
duplicate count twice on purpose, so the corruption is silent and nothing
downstream can tell which records were doubled.

Loudly losing a batch is recoverable by rerunning it. Quietly inflating
evidence is not recoverable at all.

### 6. Sequence allocation is serialized, not atomic

One mutex covers allocate, post and advance.

An atomic counter is not merely coarser, it is wrong. The contract is
gap-free and strictly monotonic, so two goroutines holding 5 and 6 race, and
if 6 arrives first the server refuses it as a gap — correctly, since it
cannot know 5 is in flight. Client-side reordering is the machinery the
explicit-sequence contract exists to avoid.

The cost is one loopback round trip per analyzed span, serialized, in a
Collector dedicated to one evaluation run. A Collector with no `evaluation:`
block takes no lock.

### 7. The cursor is the server's

`Start` reads `/v1/evaluation-runs/{run_id}/ingest-state`, and the cursor
thereafter advances only to the `next_sequence` a response returned.

Initializing in `Start` rather than lazily means a Collector that cannot
reach its control plane refuses to come up, instead of enriching spans for an
unknown period while recording nothing. It also makes restart-resume free.

Nothing is derived locally, so a client-side increment cannot disagree with
durable state, and a server that claims success without advancing fails
closed rather than freezing the cursor.

## Alternatives considered

**Import the platform.** Rejected for the reasons in point 1. It is the
change that would look like a simplification in review.

**A new `/v1/analyze` route so the control plane scores spans.** Rejected by
[ADR 0031 §4](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
before this task existed: a second behavioral execution path with its own
baseline and its own drift.

**Same-sequence retry against the digest replay contract.** Genuinely
coherent — a retry of the identical record is exactly what the digest replay
rule is for, and a network failure leaves the client unable to tell whether
the record landed. Deferred rather than rejected: it is additive, it needs a
bounded backoff policy this task has no measurement to choose, and permanent
failure is correct in the meantime. Building it speculatively would add a
retry layer nobody had yet needed.

**Reconstruct the record from span attributes.** Rejected by point 2.

## Consequences

An evaluation-configured Collector is bounded by one control-plane round trip
per analyzed span, serialized. That is a real throughput ceiling, and it is
the honest price of not inventing an ordering layer.

A Collector serves exactly one evaluation run for its lifetime. Switching
runs means a new process, the same way switching policy does — the
configuration compiles once and is fixed, which is a non-goal this module
already held.

An operator who wants evaluation and durable baselines must configure
`storage:` as well; the two are independent, and the learning scope makes
them safe to combine.

A third consumer of `/v1` now exists. The ingest contract's stability is no
longer a claim about one client.
