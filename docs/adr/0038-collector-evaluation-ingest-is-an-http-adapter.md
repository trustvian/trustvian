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

### 9. Learning follows confirmation, never precedes it

`Engine.Observe` runs when the control plane has accepted the record, and
not before.

The original order — `Analyze` → `Record` → `Observe`, with `Observe` skipped
on an ingest error — produced a divergence in the response-loss case: the run
held the record, and the Engine had never folded that behavior into a
baseline. The first fix inverted it for unknown outcomes, observing whenever
the record *may* be durable. That is not restart-safe, and the reason is
§10: "may" is a statement about this process's knowledge, while a durable
baseline is a statement about disk. A record that never reached the control
plane, observed on the theory that it might have, survives the restart that
proves it did not.

So the rule is the strict one:

| Ingest outcome | Learning |
|---|---|
| applied or replayed | applied, exactly once |
| declined (4xx) | never — the run does not hold the record |
| unknown | not yet — it waits for an answer, in this process or the next |

`Observe` runs at most once per record on every path. A reconciled record is
one record and one observation; the retry is a second attempt at delivering
the same record, never a second record.

The sink owns that ordering rather than the caller, because the caller
getting it right on every path is exactly what the first fix got wrong. The
processor hands `Record` the serialized `Result` beside the record and a
`LearnFunc`; the sink calls it once, after confirmation, before releasing the
sequence. The engine's types stay out of the sink — what it holds is opaque
bytes — and the sink's HTTP contract stays out of the engine.

**A learning failure is part of that ordering, not a log line.** The
processor's own `Observe` failures are non-fatal by design: a store blip must
not stop a Collector forwarding traces, and without `evaluation:` there is
nothing for the failure to diverge *from*. With `evaluation:` the same
failure means the run holds a record whose learning did not demonstrably
happen, which is a fact that has to survive the process. So `LearnFunc`
returns `Engine.Observe`'s error to the sink, and the sink treats it as what
it is: the second half of a two-system commit that did not complete.

And it is genuinely ambiguous, in the same way a lost HTTP response is.
`FileStore.Observe` folds the event into its in-memory baseline and *then*
flushes, so a flush error leaves a baseline that has already changed and a
file that has not. A database error can arrive on either side of a commit.
"Observe returned an error" therefore does not mean "nothing was persisted",
and the design never claims it does.

### 10. An unsettled record is durable state, not process memory

A record's two halves live in two places that cannot share a transaction:
its evidence is in the control plane, its learning is in the Engine's store.
Nothing can make both happen atomically, so a process that dies between them
leaves a question — *did the record land, and was it learned from?* — that
its successor cannot answer from the server cursor alone. The cursor says
what the run expects next. It does not say whether the record at the previous
sequence was this Collector's, nor whether anything was learned from it.

So the pending record is written to disk before its request is sent:
`pending_state_path`, one entry, holding the sequence, the record, its
learning, and how far the delivery had progressed.

```text
posting    written before the POST; proves nothing has been learned yet
confirmed  written after the control plane accepts it, before learning
(absent)   both halves are done
```

Startup reads it and the run's cursor together:

| Entry | Cursor | What it means | What happens |
|---|---|---|---|
| `posting` at *N* | *N* | the request never arrived | discard it; nothing was learned, nothing is posted, *N* goes to the next record |
| `posting` at *N* | *N+1* | something occupies *N* | re-present the record there: an identical one replays, anything else conflicts. On a replay, apply the learning — exactly once, because `posting` proves the dead process had not |
| `posting` at *N* | anything else | another writer advanced the run | refuse to start |
| `confirmed` at *N* | *N+1* | the run holds the record; the learning may or may not have been applied | do not apply it again; report it at ERROR, then resume |
| `confirmed` at *N* | anything else | another writer advanced the run, or the run does not hold a record it accepted | refuse to start |

**`confirmed` resumes at exactly *N+1*, never merely "past *N*".** A process
that died holding *N* cannot have produced *N+1* — it never released *N* — so
a run expecting *N+2* or beyond contains records this Collector did not
analyze and did not learn from. Resuming there would step over evidence whose
local learning is nobody's to account for, silently, which is the same
divergence in a different disguise. A cursor at or below *N* is the opposite
contradiction: the record was accepted, yet the run does not hold it. Both
refuse, and both leave the entry in place — it is the only record that the
record existed. Single-writer is not an assumption the sequence contract can
drop, so a run that shows a second writer is not resumed at all.

**A `confirmed` entry survives a failed or ambiguous learning.** That is what
makes the state reachable in the first place, rather than only by a crash:
`Engine.Observe` returning an error leaves the entry exactly where it is,
`Record` returns `ErrLearningIndeterminate`, and the sink accepts no further
record until a restart settles it. Deleting the entry and reporting success —
what the processor's swallowed `Observe` error used to produce — is the one
outcome this whole mechanism exists to prevent: the run holding a record with
nothing, anywhere, saying its learning was never established.

Three further consequences worth stating plainly.

**The discarded case posts nothing.** A record whose request never arrived
belonged to a batch that was already abandoned; delivering it alone after a
restart would add evidence for a span the pipeline dropped. Both halves agree
at nothing, which is the invariant, and the run is one record short — the
documented, recoverable outcome (§5), not the silent one.

**The `confirmed` window is the one thing a restart cannot settle**, and the
ambiguity is resolved deliberately toward a *possibly missing* observation
rather than a *possibly doubled* one. An observation that is
missing makes a fingerprint look *less* familiar; one applied twice makes it
look *more* familiar than the run's evidence supports, which is a silent
weakening of the signal every later decision is made from. So it is not
applied again, and startup logs it at ERROR naming the sequence. The window
is between two local writes, not around the network call.

**The learning is a serialized `Result`**, because it is the only thing that
can complete the local half later and it cannot be recomputed: analyzing
again would produce a different `Result` (the baseline has moved), and
rebuilding one from the record is the reconstruction §2 rejects. The live
path encodes and decodes it too, rather than observing the in-memory value —
so what a restart would apply is exactly what this process applies, and a
payload that could not carry the learning fails on the first span instead of
after a crash.

**Durability is the file and its directory entry.** `fsync` on a file commits
that file's contents; the name that points at it lives in the parent
directory and is committed separately. A journal that synced only the temp
file would survive a process dying — the common case — and not a host losing
power, because the rename that put it in place could still be undone. So a
write is temp file → fsync → close → rename → **fsync the parent directory**,
and a release is remove → **fsync the parent directory**: clearing an entry
is a durability operation too, since an unlink a crash undoes brings back a
note for a record that is already settled.

Both halves report failure rather than assuming it. A rename whose directory
sync failed *did happen*, and the error says so — it is reported as
durability that could not be proven, never as a write that did not occur.
Every one of those outcomes is safe in both readings: an unproven `posting`
write means nothing was sent, so the entry is at worst stale; an unproven
`confirmed` write means the record is re-presented and learned once if the
entry reverts, or reported indeterminate if it does not; an unproven release
means at worst one conservative indeterminate report for a record that was in
fact settled.

**The entry is bounded at both ends, by one limit.** The reader refuses a
file over 4 MiB, so the writer refuses an entry that would produce one —
before the temp file, and therefore before the record can be posted. Without
that, a process could write a pending state its own restart then refuses to
read: the record delivered, the note unreadable, and nothing left to say
which. The ingest request limit does not imply this one, because the entry
carries a serialized `Result` and the `Result` carries the `Event`'s
attributes, while a `DecisionRecord` carries none of them — one large span
attribute makes a small record and a large entry. A record whose entry does
not fit is refused locally, definitively, before any delivery: nothing is
sent, nothing is learned, and its sequence goes to the next record.

On Windows there is no portable way to flush a directory handle, so
`syncDirectory` is a documented no-op there and the guarantee is stated
honestly rather than claimed: the journal survives a process dying on every
supported platform, and a host losing power on POSIX ones.

`pending_state_path` is required whenever `evaluation:` is configured, and
not conditional on which store is configured. Branching on the backend would
put a store-specific path into the one mechanism that must behave identically
for all of them, and an operator who starts on `memory` and later configures
`postgres` would silently lose the guarantee at the moment it starts to
matter.

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
the control plane declined or never received. §9 keeps learning tied to what
the run actually holds, in both directions.

**Observing whenever the record *may* be durable.** This is what the first
version of §9 did, and §10 is why it was wrong: with a durable store the
learning outlives the process, while "may" does not. A record that never
reached the control plane leaves the baseline holding behavior the run's
evidence has no trace of, permanently and silently.
`TestRestartAfterUnreachedRecordLeavesNoLearning` is that case, and it fails
against that implementation.

**Extending `/v1` ingest-state with the last accepted record's digest**, so
startup could recognize its own record without re-presenting it. Not needed:
re-presenting *is* the recognition, because the replay rule already compares
digests server-side and answers `replayed` or conflicts. An additive field
would have moved that comparison to the client without removing a single
failure mode — and the client would still need the durable entry to have
something to compare. No `/v1` field was added.

**Keeping the pending record in memory only.** It is what the sink did
before, and it is sufficient for everything except the case this ADR section
exists for: the process not being there any more.

**Reconstruct the record from span attributes.** Rejected by point 2.

## Consequences

An evaluation-configured Collector is bounded by one control-plane round trip
per analyzed span, serialized. That is a real throughput ceiling, and it is
the honest price of not inventing an ordering layer. A lost response costs
one additional round trip, once, on the span that lost it.

A control plane that is unreachable for both attempts leaves the sink
holding one record and refusing further ones until it is reconciled. That is
a hard stop, deliberately: the alternative is reusing a sequence the server
may already have committed. The state it holds is one record, never a queue.

An evaluation-configured Collector now needs a writable path as well as a
reachable control plane, and pays two small local writes and a delete per
analyzed span beside the round trip. On a `file:` store, whose `Observe`
already rewrites its snapshot per span, that is proportionate; on `postgres:`
it is local I/O beside a database round trip. A Collector with no
`evaluation:` block writes nothing and is unchanged.

The pending file belongs on the same durable medium as the baseline — a
volume, not a container's writable layer — and to one run. It names its run,
and a sink configured for a different one refuses to start rather than
discarding an unsettled record.

A Collector serves exactly one evaluation run for its lifetime. Switching
runs means a new process, the same way switching policy does — the
configuration compiles once and is fixed, which is a non-goal this module
already held.

An operator who wants evaluation and durable baselines must configure
`storage:` as well; the two are independent, and the learning scope makes
them safe to combine.

A second consumer of `/v1` now exists. The ingest contract's stability is no
longer a claim about one client.
