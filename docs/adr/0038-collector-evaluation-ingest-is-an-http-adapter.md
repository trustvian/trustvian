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

An ingest failure that the sink could not resolve returns
`consumererror.NewPermanent`.

Without the wrapper the Collector retries the batch. A retried batch
re-analyzes spans whose records already committed and resends them under
*new* sequence numbers, which the server accepts because from its side they
are new records.
[ADR 0026](0026-evaluation-aggregation-is-bounded-evidence.md) made a
duplicate count twice on purpose, so the corruption is silent and nothing
downstream can tell which records were doubled.

That is the argument against **blind** retry, and it is unchanged. It is not
an argument against the one retry the server's own contract defines — see
§8, which is where a lost response is resolved before it can become this.

Losing a batch is loud, but it is not recoverable by rerunning it into the
same run: a batch that fails partway leaves its earlier spans' records
already committed and the run's cursor already advanced past them, and
nothing in the run describes where the batch was cut short. Rerunning the
same batch would re-post that committed prefix under *new* sequence numbers,
which the server accepts as new records — producing exactly the double count
this wrapper exists to prevent, the same failure a retry would have caused.
What recovers the evaluation is a **new run**, not a repeat of this one.
Quietly inflating evidence is not recoverable at all, and unlike a lost
batch, there would be nothing to even show it happened.

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

### 8. A transport failure does not mean the record was not applied

This is the correction to an assumption the first implementation made
silently:

```go
response, err := c.http.Do(request)
if err != nil {
    // the operation did not happen   ← not true
}
```

A POST can be written in full, committed, and have only its reply destroyed —
a reset connection, a timeout, an EOF partway through the response. The
server holds the record at sequence *N*; the client still believes *N* is
free. The next record takes *N*, and the control plane refuses it for the
right reason: sequence *N* is already occupied by different content. The run
is then stuck in a way no operator action inside it can undo.

So failures are classified, and the two classes are handled differently:

| Class | Examples | Sequence |
|---|---|---|
| Definitely not applied | failed dial, DNS failure, refused redirect, request over the body cap, a 4xx from the server | Free. The next record takes it. |
| Outcome unknown | reset connection, timeout, EOF mid-response, any 5xx, a 2xx whose body cannot be read | **Held**, bound to that exact record. |

The asymmetry is deliberate. Calling a definitive failure unknown costs one
redundant POST the server answers `replayed`. Calling an unknown outcome
definitive frees a sequence the server may already hold. Anything unproven is
therefore unknown, and only two transport shapes are treated as proof —
failed dial and DNS failure, both of which happen before a byte is sent.

A held sequence is reconciled by re-presenting **the same record** at **the
same sequence**, which is exactly the case
[ADR 0031 §8](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)'s
digest replay rule was written for: same sequence and identical digest
replays, same sequence and different content conflicts. One attempt, made
synchronously inside the same `Record` call, under the caller's own context.
Not a background worker, not a queue, not a backoff schedule — a state
machine with three states and one held record:

```text
idle  ──POST fails, outcome unknown──▶  pending(sequence, record)
pending ──same record, same sequence, server answers──▶ reconciled ──▶ idle
pending ──still unknown──▶ pending   (ErrUnresolved; no other record may proceed)
```

The pending slot is bounded at one by construction: the mutex in §6 means
there is never a second record in flight to become unknown. A sink that
cannot reconcile refuses to accept another record rather than reusing the
sequence, which is the fail-closed reading of "the producer owns the
sequence".

### 9. Evidence and learning move together

`Engine.Observe` runs when the record may be durable, and does not run when
the control plane declined it.

The original order — `Analyze` → `Record` → `Observe`, with `Observe` skipped
on any ingest error — produced a divergence in the response-loss case: the
run held the record, and the Engine had never folded that behavior into a
baseline. The evaluation's evidence then described behavior the Collector
would not recognize next time, and nothing said so.

§8 resolves the ordinary case before it arises: the reconciliation finishes
inside `Record`, so the call returns success and `Observe` runs as usual.
What remains is a record still pending when the call returns. It may be
durable, so it is observed — and if the reconciliation later succeeds, the
two agree. The batch still fails; it just does not fail asymmetrically.

The opposite direction is handled by the same rule read backwards: a record
the control plane **declined** is not in the run, so learning from it would
teach this Engine from behavior no scorecard can account for. That path
returns before `Observe`.

`Observe` runs at most once per span on every path. A reconciled record is
one record and one observation; the retry is a second attempt at delivering
the same record, never a second record.

## Alternatives considered

**Import the platform.** Rejected for the reasons in point 1. It is the
change that would look like a simplification in review.

**A new `/v1/analyze` route so the control plane scores spans.** Rejected by
[ADR 0031 §4](0031-control-plane-owns-ingest-and-http-is-an-adapter.md)
before this task existed: a second behavioral execution path with its own
baseline and its own drift.

**Same-sequence retry against the digest replay contract.** Adopted — see
§8. It was deferred in the first version of this ADR on the grounds that it
needed a bounded backoff policy and that permanent failure was correct in
the meantime. Both were wrong in the same way: permanent failure is correct
only for a record that is *not* there, and the deferral rested on treating
an unknown outcome as a definite one. No backoff policy is needed either —
the reconciliation is one synchronous attempt at the same sequence, not a
schedule.

**A blind retry layer, in-line or background.** Still rejected, and §5 is
why. Resending a *batch*, or resending a record under a *new* sequence, is
what inflates evidence. The distinction that makes §8 safe is that the
retry is not generic: it is the same record at the same sequence, which the
server is specified to recognize.

**Observing before posting** (`Analyze` → `Observe` → `Record`), to make
"evidence without learning" structurally impossible. Rejected: it only
trades the divergence for its mirror image, teaching the Engine from records
the control plane declined. §9 keeps learning tied to what the run may
actually hold, in both directions.

**Reconstruct the record from span attributes.** Rejected by point 2.

## Consequences

An evaluation-configured Collector is bounded by one control-plane round trip
per analyzed span, serialized. That is a real throughput ceiling, and it is
the honest price of not inventing an ordering layer. A lost response costs
one additional round trip, once, on the span that lost it.

A control plane that is unreachable for both attempts leaves the sink
holding one record and refusing further ones until it is reconciled. That is
a hard stop, deliberately: the alternative is reusing a sequence the server
may already have committed. The state it holds is one record, never a queue,
and it survives nothing — a restarted Collector re-reads the cursor from the
server, and any record whose fate was unknown is simply the sequence that
server reports next.

A Collector serves exactly one evaluation run for its lifetime. Switching
runs means a new process, the same way switching policy does — the
configuration compiles once and is fixed, which is a non-goal this module
already held.

An operator who wants evaluation and durable baselines must configure
`storage:` as well; the two are independent, and the learning scope makes
them safe to combine.

A second consumer of `/v1` now exists. The ingest contract's stability is no
longer a claim about one client.
