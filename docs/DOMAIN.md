# Domain Model

The domain concepts behind each pipeline stage, and how they relate.
For package/API details see [ARCHITECTURE.md](ARCHITECTURE.md) and
[Go SDK Guide](sdk-guide.md); for how these concepts prevent specific
attacks, see [SECURITY.md](SECURITY.md).

```
Event → Features → Fingerprint → Baseline → Anomaly → Trust → Policy → Decision
```

## Event

The atomic unit: one observed action. Package `event`
(`github.com/trustvian/trustvian/event`) — see
[Go SDK Guide § the Event type](sdk-guide.md#the-event-type) for the
full field reference.

- **Actor** — who/what performed the action: `ID`, `Type` (`service`,
  `user`, `service_account`, `ai_agent`, `device`, `unknown`), and
  `IdentityConfidence` — an *input* Trustvian trusts (sourced from
  upstream authentication), never something Trustvian computes.
- **Operation** — what was done: `Category` (`http`, `db`, `rpc`,
  `tool`, `external`), `Name` (e.g. `"POST /payment"`,
  `"search_customer"`), and optional `Direction` (`inbound`/`outbound`,
  from `SpanKind` when sourced via OTel).
- **Target** — the destination, when there is a distinct one (a
  service, database, or host). Optional. `Category` (`internal`,
  `external`, `database`) optionally classifies what *kind* of
  destination it is; like `Direction`, the zero value means
  unclassified and is never checked by `Validate()`.
- **Context** — deployment `Environment` plus, when available, OTel
  `TraceID`/`SpanID` for correlation, and (`v0.7`) `SessionID`/
  `DelegatedFrom`/`ApprovalStatus` for AI-agent session, delegation, and
  approval context — see [AI Agent behavioral
  context](#ai-agent-behavioral-context) below for which of these
  affect `Fingerprint` identity (none of them, deliberately) and which
  don't. Task 084 adds `ParentSpanID` and `SpanLineage` (`root`, `child`,
  or nothing established): a trace-scoped reference, never a standalone
  key, and never identity.
- **Execution** — operational evidence about one occurrence (task 084):
  the observed duration (bounded by `event.MaxDurationNanos`) and the
  producer's span status (`unset`, `ok`, `error`, or the empty value when
  nothing established one — neither empty nor `unset` is success). It
  lives outside `Operation` on purpose: a slower call is the same
  behavior, so nothing here reaches `StableFeatures` or a fingerprint.
- **Metadata (`Attributes`)** — an open `map[string]any`. Two keys
  carry defined meaning (`duration_ms`, `error` — see
  [Go SDK Guide](sdk-guide.md#the-event-type)); everything else passes
  through unmodified, available to a custom `ContextRisk` function.

`Event` is treated as an immutable value: nothing in the domain
mutates one after construction.

## Feature

`internal/features.Extract(Event) Features` splits an event's
dimensions into two kinds, because they play different roles
downstream:

- **Stable features** — `ActorType`, `OperationCategory`,
  `OperationName`, `TargetName`, `TargetCategory`, `Environment`.
  These identify *what kind of behavior* this is and feed the
  `Fingerprint`. `TargetCategory` (added in
  [task 001](archive/tasks/v0.1/001-feature-model.md)) mirrors `Event.Target.Category`
  and is optional — it flows through `Extract` and, as of
  [task 002](archive/tasks/v0.1/002-fingerprint.md), is part of
  `fingerprint.Compute`'s hash.
- **Volatile features** — `Timestamp`, `Latency`, `Error`. These are
  per-event, noisy, and feed `Anomaly` directly; they never become
  part of a `Fingerprint`.

`Extract` is a pure function, deterministic and (in the common case)
zero-allocation — see [PERFORMANCE.md](PERFORMANCE.md).

## Fingerprint

`internal/fingerprint.Compute(StableFeatures) Fingerprint` derives a
deterministic identity — an `ID` (an FNV-1a hash over the stable
dimensions) plus the `Stable` snapshot itself, retained for
explainability. Two events with identical stable dimensions always
produce the same `Fingerprint.ID`, regardless of how their volatile
data (latency, timestamp, errors) differs.

This is deliberately a *per-event-shape* identity — one ID per
distinct `(ActorType, OperationCategory, OperationName, TargetName,
TargetCategory, Environment)` combination — not an aggregated,
all-behavior profile for an actor. The aggregation ("the set of
fingerprints this actor is known to use") emerges naturally from
`Baseline`'s map, keyed by `Fingerprint.ID`, rather than being tracked
separately.

**What feeds the hash, in order.** `Compute` writes, in this fixed
order: the version marker (see below), then `ActorType`,
`OperationCategory`, `OperationName`, `TargetName`, `TargetCategory`,
`Environment` — the same six fields `internal/features.Extract`
classifies as *stable* (see [§ Feature](#feature)). No volatile field
(`Timestamp`, `Latency`, `Error`) and no event-instance identifier
(`Event.ID`, `TraceID`, `SpanID`) ever enters the hash — that's what
makes the ID a behavioral-shape key rather than a per-request one.

**Why FNV-1a.** `hash/fnv`'s 64-bit variant is fast (no cryptographic
primitives), non-cryptographic, and gives a large enough ID space
(2^64) that accidental collisions across genuinely distinct behavioral
shapes are not a practical concern. This is a content-addressed
identity key for a map lookup, not a security boundary — nothing
downstream trusts `Fingerprint.ID` to resist a deliberate,
computationally-motivated collision attack, so a slower cryptographic
hash would only add cost without buying a real property this design
needs.

**Field-boundary collision protection.** Each field is written through
`writeField`, which appends a NUL (`\x00`) byte after the field's
bytes. Without this, two structurally different inputs could hash
identically by having bytes shift across an (unmarked) field boundary
— e.g. `OperationName="ab", TargetName="c"` and `OperationName="a",
TargetName="bc"` would otherwise concatenate to the same byte stream.
`TestComputeAvoidsFieldBoundaryCollision` in
`internal/fingerprint/fingerprint_test.go` pins this property.

**Versioning.** `Compute` writes a `fingerprintVersion` constant
(currently `"1"`) as the *first* field, ahead of every stable
dimension, using the same `writeField` NUL-separator convention. This
exists so that a future change to which dimensions feed the hash, or
to the hash algorithm itself, produces a *disjoint* ID space from
today's rather than silently reinterpreting existing IDs under new
semantics — an in-memory-only `Store` would survive that silently
(restart clears everything), but a persistent `Store` would not: a
stored baseline computed under one fingerprint composition could be
misread after an upgrade changes what its ID means. Bump
`fingerprintVersion` whenever the stable field set or hash algorithm
changes — version `"1"` is the version under which `TargetCategory`
first became part of the hash (see
[task 002](archive/tasks/v0.1/002-fingerprint.md)). There is deliberately no
separate `Version` field on `Fingerprint`: no current consumer needs to
read the version independent of the ID it's baked into, so exposing
one would be exactly the kind of interface `.claude/rules/architecture.md`
says to add only when needed, not speculatively.

## Baseline

`internal/baseline.Baseline` is the statistical history for one
`Key{Scope, ActorID, Environment}`: a map from `Fingerprint.ID` to
`FingerprintStats`.

`Scope` is the learning scope — an opaque namespace that lets one actor in
one environment hold several independent histories, selected by
`trustvian.WithLearningScope`. The empty string is the default scope, which
is where every baseline lives unless a caller says otherwise.

It is worth being precise about what it is *not*. A learning scope decides
**which history an observation belongs to**; it never describes **what the
actor did**. It is absent from `StableFeatures`, from the fingerprint hash,
and from `Event` — the same event analyzed under two scopes yields the same
`Fingerprint.ID` and differs only in the learned evidence it is compared
against. See
[ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md).

- **Learning** — `Baseline.Observe(fp, volatile, now) (Baseline, bool)`
  is a pure, copy-on-write update: it never mutates the receiver, it
  returns a new `Baseline`. The bool reports whether this fingerprint's
  statistics were actually updated — false exactly when admission control
  refused an unknown fingerprint at capacity — and it travels out through
  `Store.Observe` to `Engine.Observe`'s `learned` return. This is what makes a value read via
  `Store.Get` a permanently valid snapshot, safe to use without
  holding any lock.
- **Updating** — per-`Fingerprint` statistics use an EWMA
  (exponentially-weighted moving average) for latency mean/variance,
  inter-observation interval mean/variance, and error rate — not a
  plain cumulative (unweighted) average. This reconciles two needs at
  once: O(1) memory with no raw samples retained (in the spirit of
  Welford's online algorithm), and *decay*, so legitimate behavioral
  drift is absorbed over time rather than requiring a manual reset.
  The interval EWMA (`IntervalObservations`/`IntervalMean`/
  `IntervalVariance`) is computed from the *previous* `LastObserved`
  before it's overwritten, so it captures how much time elapsed since
  the fingerprint's last occurrence — the raw material
  `internal/anomaly`'s `frequency_deviation` signal (task
  [004](archive/tasks/v0.1/004-anomaly.md)) scores against. It has no interval to
  record on a fingerprint's first observation (`IntervalObservations`
  stays 0), mirroring `LatencyObservations`' cold-start behavior.
- **Ordering** — an observation whose timestamp does not strictly
  follow `LastObserved` (clock skew, out-of-order delivery, a
  deliberately backdated event) is still counted, but its interval is
  *not* folded into the EWMA: a negative interval is an absence of
  timing information, not a measurement. `LastObserved` correspondingly
  advances monotonically and never regresses, so the next in-order
  event still measures from the freshest observation and `IsStale` is
  never fooled into reporting a fingerprint as staler than the newest
  evidence held for it. Without this guard a single backdated event
  drags `IntervalMean` below zero and makes every subsequent on-time
  event look anomalous — see [SECURITY.md § baseline poisoning](SECURITY.md).
- **Cold start** — `FingerprintStats.Count` is the maturity counter: how
  many times this specific fingerprint has been observed.
  `internal/baseline` only counts; it does not itself decide what
  count is "mature" — that threshold
  (`anomaly.Config.MinObservations`) belongs to the consumer that
  actually needs to make that judgment call.
- **Confidence** — derived downstream (in `internal/anomaly`) as
  `Count / MinObservations`, capped at 1. `Baseline` doesn't compute a
  confidence value itself; it exposes the raw material.
- **Expiration** — not implemented as active pruning today; the EWMA
  decay means stale patterns lose statistical weight over time rather
  than being explicitly expired. See [ROADMAP.md](ROADMAP.md).
- **Time-of-day pattern** — `HourActivity [24]float64` is a second,
  independent EWMA per `FingerprintStats`: one bucket per UTC
  hour-of-day, each estimating the fraction of this fingerprint's
  traffic that historically falls in that hour. It uses its own
  smoothing constant, `hourActivityAlpha = 0.02`, deliberately much
  slower than the `emaAlpha = 0.2` used everywhere else in this
  package. Every observation updates all 24 buckets (the matching hour
  toward 1, the other 23 toward 0), so a bucket that is hit only once
  every 24 observations decays heavily between hits under a fast alpha
  — `TestFingerprintStatsHourActivityUniformTraffic` measured swings of
  two orders of magnitude around the true 1/24 uniform share under
  `emaAlpha`, a pure measurement-phase artifact rather than a real
  behavioral signal. `hourActivityAlpha = 0.02` bounds that swing to
  about ±0.01 in exchange for slower adaptation to genuine hour-of-day
  drift (~100-observation effective memory vs. ~9), which is the right
  tradeoff since a real hour-of-day pattern is a weeks-scale
  phenomenon. `TimePatternObservations` tracks maturity for this signal
  specifically (not reusing `Count`), so a `FingerprintStats` loaded
  from a `store.FileStore` file written before this field existed —
  where `HourActivity` unmarshals to its zero array — is correctly
  treated as immature rather than as a suspiciously empty, fully mature
  distribution. See `internal/anomaly`'s `time_pattern_deviation` signal
  below, and [task 017](archive/tasks/v0.3/017-baseline-time-patterns.md).

### What persists, and the contract it persists under

A `Baseline` is **current learned state**, not an event history — it is
bounded by construction: at most 512 `FingerprintStats` entries
(`maxFingerprints`, enforced by refusing admission of a new fingerprint
at capacity, never by evicting a learned one — see
[ADR 0019](adr/0019-bounded-fingerprint-admission.md)), with every
per-entry map capped too: `maxPredecessors`, `maxTrigramPredecessors`,
`maxDelegators`, all 64. Nothing persists raw
events, prompt text, tool arguments, secret values, or request bodies;
there is no retention policy because there is no growing history to
retain. Trustvian is not a SIEM or event lake.

`internal/store.Store` is the port that state flows through, with two
operations — `Get` (read the current snapshot) and `Observe` (apply one
observation). Since `v0.8` task 034 the guarantees every implementation
owes are written down and executable (`TestStoreContract`,
`internal/store/contract_test.go`): a missing key reads as a
zero-value-but-keyed `Baseline` plus `false` (never nil, never an error
— `internal/anomaly` scores a never-seen actor against exactly that
value); `Observe` is *incremental*, applying one observation rather than
writing back a caller-computed `Baseline`; a value already returned by
`Get` never mutates under a later `Observe`; keys are isolated; and
concurrent `Observe` calls for the same key lose no updates.

That last guarantee is why the port's shape matters. Because `Observe`
receives the observation rather than a finished `Baseline`, the whole
read-modify-write cycle happens inside one implementation call and can
be made atomic there — a per-key mutex in `store.InMemory` and
`store.FileStore`, and a transaction holding the row with `SELECT … FOR
UPDATE` in `internal/store/postgres`. See [ADR
0018](adr/0018-production-store-boundary-and-postgresql-direction.md).

Which implementation an `Engine` uses is selected via public
configuration (`config.StorageConfig` → `config.CompileStorage` →
`trustvian.WithStore`), and failure there is always closed: an
unbuildable store yields no store, never a silent downgrade to
non-durable memory.

## Anomaly

`internal/anomaly.Score(Features, Fingerprint, Baseline, Config)
Anomaly` combines up to twelve independent signals via a **noisy-OR**
combination — `score = 1 - Π(1 - value_i · weight_i)` — chosen
specifically because a single severe signal should dominate the
result, not be diluted by averaging against several unrelated benign
signals:

| Signal | Fires when |
|---|---|
| `categorical_novelty` | The fingerprint is unfamiliar or below `MinObservations` maturity |
| `latency_deviation` | Current latency's z-score against the baseline's EWMA mean/stddev exceeds a threshold |
| `frequency_deviation` | The current inter-observation interval's z-score against the baseline's EWMA mean/stddev interval exceeds `Config.FrequencyZThreshold` — the "this actor normally calls this operation every 10s; it just called it every 100ms" signal (a classic abuse/exfiltration pattern). Requires the fingerprint to be known, at least one recorded interval (`FingerprintStats.IntervalObservations > 0`), and a strictly positive current interval; a fingerprint's very first observation has no prior `LastObserved` to measure from, so it never fires on cold start, mirroring `latency_deviation`'s `LatencyObservations > 0` gate. **Contributes 0 to `Score` by default** — see below |
| `error_deviation` | An error occurred against a fingerprint whose baseline error rate is low |
| `sensitive_target` | The destination is in `Config.SensitiveTargetFloor` — a fixed penalty that persists *regardless of familiarity* |
| `time_pattern_deviation` | The event's UTC hour-of-day has historically accounted for a much smaller share of this fingerprint's traffic than a uniform 1/24 baseline would predict. Requires the fingerprint to be known and `FingerprintStats.TimePatternObservations >= Config.MinObservations`. **Contributes 0 to `Score` by default** — see below |
| `transition_deviation` (`v0.6` task 025) | The immediately preceding fingerprint has never before led to this one, for this actor (`PredecessorCounts_B[A] == 0`). **Contributes 0 to `Score` by default** — see [Sequence-aware detection](#sequence-aware-detection) below |
| `transition_rarity` (`v0.6` task 026) | The immediately preceding fingerprint *has* led to this one before, but rarely: `1 - (PredecessorCounts_B[A] / OutgoingTransitionTotal_A)`, gated on `OutgoingTransitionTotal_A >= Config.MinTransitionObservations`. Mutually exclusive with `transition_deviation` for the same transition. **Contributes 0 to `Score` by default** — see [Sequence-aware detection](#sequence-aware-detection) below |
| `ngram_deviation` (`v0.6` task 027) | The 3-gram (the two fingerprints immediately preceding this one) has never before led to this one, for this actor (`TrigramCounts_C[{A,B}] == 0`). Requires both a predecessor and a grandparent fingerprint to exist. **Contributes 0 to `Score` by default** — see [Sequence-aware detection](#sequence-aware-detection) below |
| `ngram_rarity` (`v0.6` task 027) | That same 3-gram *has* been observed before, but rarely: `1 - (TrigramCounts_C[{A,B}] / TrigramContinuationTotal_B[A])`, gated on `TrigramContinuationTotal_B[A] >= Config.MinNGramObservations`. Mutually exclusive with `ngram_deviation` for the same 3-gram. **Contributes 0 to `Score` by default** — see [Sequence-aware detection](#sequence-aware-detection) below |
| `markov_surprisal` (`v0.6` task 028) | The identical seen-transition evidence `transition_rarity` reads, through a different, unbounded-then-normalized curve: `normalized(-log2(P(B\|A)))`. A monotonic reparameterization of `transition_rarity`, not new evidence — when this signal's weight is enabled, `transition_rarity`'s own contribution to `Score` is forced to zero for that call (still reported in `Contributors`, never double-counted). **Contributes 0 to `Score` by default** — see [Sequence-aware detection](#sequence-aware-detection) below |
| `delegation_deviation` (`v0.7` task 031) | `Context.DelegatedFrom` (via `Volatile.DelegatedFrom`) names a delegator this *actor* has never received delegation from before (`Baseline.DelegatorCounts[delegator] == 0`) — actor-scoped, not per-Fingerprint. Evaluated only when the event carries a delegator at all. Binary seen/unseen, mirroring `transition_deviation`'s exact shape — deliberately not a rarity estimate. **Contributes 0 to `Score` by default** — see [AI Agent behavioral context](#ai-agent-behavioral-context) below |

**Nine of the twelve ship inert.** `DefaultConfig()` leaves
`SensitiveTargetFloor` empty (so `sensitive_target` never fires until an
operator names their sensitive destinations), and sets
`FrequencyWeight`, `TimePatternWeight`, `TransitionWeight`,
`TransitionRarityWeight`, `NGramWeight`, `NGramRarityWeight`,
`MarkovWeight`, and `DelegationWeight` all to `0` (so
`frequency_deviation`, `time_pattern_deviation`, `transition_deviation`,
`transition_rarity`, `ngram_deviation`, `ngram_rarity`,
`markov_surprisal`, and `delegation_deviation` are all
detected and reported in `Contributors`,
but multiply to nothing inside the noisy-OR). In every case the
mechanism is complete and tested; only the deployment-specific value that
makes it count is left to the operator, because no default is correct
everywhere. Since `v0.7` task 033, every one of these weights (plus
`SensitiveTargetFloor` and the maturity/threshold fields) is
configurable through public API — see
[Anomaly Configuration Guide](anomaly-config-guide.md) and [ADR
0017](adr/0017-public-anomaly-configuration-boundary.md).

`time_pattern_deviation` ships opt-in for a second, distinct reason
beyond calibration: `HourActivity`'s per-bucket EWMA (see Baseline above)
only reaches its documented convergence bound after enough elapsed wall
time for its slow `hourActivityAlpha` to smooth out a real distribution —
`MinObservations` alone does not guarantee a fingerprint has actually
been observed across a representative spread of hours, only that it has
been observed *some* number of times. A fingerprint that happens to
cross `MinObservations` within a single day's traffic has a
`HourActivity` distribution concentrated by pure sampling luck, not by a
real time-of-day pattern; scoring against it before the operator has
confirmed enough elapsed-time coverage for their own traffic would
misattribute normal activity in an unseen hour as anomalous. This is a
known limitation, not a bug — see [task 017's Non-Goals](archive/tasks/v0.3/017-baseline-time-patterns.md).

For `frequency_deviation` specifically, the reason is calibration
against real jitter. The signal divides by the standard deviation of a
fingerprint's own inter-event intervals, so on traffic with only
milliseconds of natural jitter around a ten-second cadence, an event a
few milliseconds off the mean already exceeds `FrequencyZThreshold` and
clamps the signal to `1.0`. At the `0.6` weight originally shipped, that
alone carried a fully-familiar actor to `RiskHigh` on routine traffic
(`TestAnalyzeOrdinaryCadenceJitterDoesNotElevateRisk` pins this). Measure
your own fleet's `IntervalMean` and the stddev implied by
`IntervalVariance` before raising the weight.

**Two branches, not one.** `frequencySignal` (and `latencySignal`
identically) switches on whether the baseline's standard deviation is
usable. Above the threshold — 1ms for intervals, 1µs for latency — it
takes the z-score path: `value = min(|z| / ZThreshold, 1)`. At or below
it, dividing by a near-zero stddev would produce an unbounded or `NaN`
z-score, so it degrades to an exact-match test instead: any interval
different from the mean at all scores `1.0`, anything identical scores
`0`. That branch is correct for genuinely fixed-cadence traffic — a cron
job firing at exactly `00:00:00` every hour, a fixed-interval poller —
where "different from the mean" really is the whole signal. It is also
why perfectly synthetic test fixtures never exercise the z-score path:
a hand-built baseline with an exactly constant interval always lands in
this branch (see `baselineWithStableInterval` vs
`baselineWithJitteredInterval` in `internal/anomaly`'s tests).

**Score and Confidence are reported separately, on purpose.** A
brand-new fingerprint scores near-maximum novelty (`Score` near 1) but
with `Confidence` at 0 (`Count / MinObservations`). `internal/anomaly`
does not suppress `Score` for low confidence — see
[ARCHITECTURE.md § cold start](ARCHITECTURE.md#cold-start-two-numbers-not-one)
for why, and how the two are recombined in `Trust`.

Every contributing signal (`Name`, `Value`, `Weight`, `Detail`) is
retained on `Anomaly.Contributors` — this is the explanation for "which
behavioral signals contributed" and "why the score changed."

## Trust and Risk

`internal/trust.Compute(Anomaly, IdentityConfidence, ContextRisk,
Config) Trust`:

```
effectiveAnomaly = Anomaly.Score * Anomaly.Confidence
TrustScore        = IdentityConfidence * (1 - effectiveAnomaly) * (1 - ContextRisk)
```

Multiplicative, not averaged: trust is capped by its weakest factor,
the same way a chain is only as strong as its weakest link. A single
severe, *confidently measured* anomaly, or a high `ContextRisk`, can
drive `TrustScore` toward zero even when other inputs are high.

`Risk` (`RiskLevel`: `low`/`medium`/`high`/`critical`) is a
configurable-threshold bucket derived from `1 - TrustScore` — the
residual distrust. `IdentityConfidence` and `ContextRisk` are inputs
`trust.Compute` never computes itself: identity comes from upstream
authentication, context risk from caller-supplied, deterministic,
config-driven classification (not learned) — see
[SECURITY.md](SECURITY.md) for why context risk is deliberately not
purely learned.

`Trust` retains every input (`IdentityConfidence`, `AnomalyScore`,
`AnomalyConfidence`, `ContextRisk`) alongside `Score` and `Risk` — the
relationship between anomaly, risk, and trust is never collapsed into
one opaque number. `Trust.Explain() string` renders those retained
fields as one human-readable sentence (e.g. `"trust 0.35 (high):
identity confidence 0.97, anomaly 0.91 at full confidence, context
risk 0.10"`) — pure formatting over existing fields, no new
computation.

`TestComputeScenarioMatrixBoundsAndMonotonicity`
(`internal/trust/trust_test.go`, task
[005](archive/tasks/v0.1/005-trust-risk.md)) sweeps `IdentityConfidence`,
`Anomaly.Score`, `Anomaly.Confidence`, and `ContextRisk` each across
`{0, 0.25, 0.5, 0.75, 1}` — the full cross product — and asserts two
guarantees that were previously only implied by the formula, not
explicitly tested across the whole input space: `TrustScore` never
leaves `[0,1]`, and it is monotonic in each risk-bearing input
(increasing `Anomaly.Score` or `ContextRisk` never *increases*
`TrustScore`; increasing `IdentityConfidence` never *decreases* it).

## Policy and Decision

`internal/policy.Policy.Evaluate(Input) Result` turns `Trust` plus an
event's stable features into a final `Decision`. See
[Policy Guide](policy-guide.md) for the full reference; in domain
terms:

- **Policy is data**, not code: an ordered `[]Rule` plus a mandatory
  default, not per-rule Go closures.
- **Input** carries what a `Condition` can match against:
  `features.StableFeatures`, `trust.Trust`, `Attributes
  map[string]any` (since task 006) — the raw `Event.Attributes` passed
  through unchanged by `Engine.Analyze` — and the event's
  `ApprovalStatus` (since task 030). `Attributes` is what lets
  `Condition.Attributes` match a specific key/value pair (e.g.
  `tool.category: secrets`) without inventing a general policy
  language; see [Policy Guide § matching
  Event.Attributes](policy-guide.md#example-matching-eventattributes).
- **Decision** is one of `allow`, `observe_only`, `alert`,
  `challenge`, `require_approval`, `block` — written in upper case
  (`ALLOW`, `BLOCK`, …) in prose throughout these docs.
- **Evaluation is first-match-wins**, deterministic, and fails closed:
  an unconfigured or misconfigured policy resolves to `BLOCK`, never a
  silent `ALLOW`.
- **Explanation** — every `Result` carries `RuleName` (which rule
  fired, if any), `Reason` (a human-readable explanation), and
  `MatchedDefault` (whether no rule matched and the default applied).
  This is "which policy was evaluated" and "why the final decision was
  produced," verified by a test that checks every `Decision` across
  several policies has a non-empty reason.

`Engine.Result` (root package) is the complete record tying all of
this together: the original `Event`, `Features`, `Fingerprint`,
`Anomaly` (with its contributors), `Trust` (with every input), and the
final `Decision` + `Explanation`. Nothing is discarded on the way to
the final answer — that completeness is what makes every decision
explainable end to end, not just at the policy stage. `Result.Explain()`
renders that whole record as one human-readable summary, so answering
"why did Trustvian allow/block/challenge this" never requires
hand-assembling the story from five separate fields.

**Configuring a `Policy` from outside this module** — package
`config` (public, alongside `event`/`alert` — see [ADR
0008](adr/0008-policy-config-boundary.md)) defines `PolicyConfig`/
`PolicyRule`/`PolicyCondition`, primitive-typed structs mirroring
`Policy`/`Rule`/`Condition` exactly, plus
`CompilePolicy(PolicyConfig) (policy.Policy, error)`. `internal/policy`
itself is not made public and gains no new compatibility obligation
from this: `CompilePolicy`'s returned `policy.Policy` is usable by a
caller outside this module via type inference (received from
`CompilePolicy`, passed straight into `trustvian.WithPolicy`) without
that caller ever importing `internal/policy` — a real Go property,
verified empirically, not assumed; see ADR 0008 for the experiment.
[Task 019](archive/tasks/v0.5/019-policy-config-model.md) built the
model and compiler. The Collector processor now uses the same path: its
optional `policy:` block is decoded into `config.PolicyConfig` and
compiled with `config.CompilePolicy` (`processor/processor.go`), and
without the block it runs the default `Policy`.

## Alert

`alert.Alert` (package `alert`, a public package alongside `event` —
see [ADR 0007](adr/0007-alert-package-is-public.md) for why it isn't
under `internal/`) is the notification-worthy summary of one behavioral
decision, produced by `alert.Evaluate(Result, []Rule) (Alert, bool)` —
strictly downstream of `Decision`, never a pipeline stage, never
written back into a `Result`. See
[ARCHITECTURE.md § Relationship to a future Alert & Notification
layer](ARCHITECTURE.md#relationship-to-a-future-alert--notification-layer)
and [`docs/archive/project-spec.md` §
18](archive/project-spec.md#18-alert--notification-system) for the
full architecture; in domain terms:

- **`Alert` is a view, not a parallel model.** Every field is read from
  an existing `Result` field — `Decision`, `Trust.Risk`, `Trust.Score`,
  `Anomaly.Score`, `Event.Actor`, `Event.Target`, `Fingerprint.ID` — plus
  `Reasons`, built from `Anomaly.Contributors` and `Explanation`, the
  same material `Result.Explain()` already renders. Nothing here is a
  second, competing copy of trust score, anomaly score, risk, decision,
  actor, target, or explanation logic.
- **Severity is a genuinely new concept**, distinct from every other
  output value: `severity != risk`, `severity != anomaly score`,
  `severity != trust score`, `severity != decision`. It is one of
  `info`/`low`/`medium`/`high`/`critical`, and is always exactly what
  the matched `Rule` configured — never inferred from
  risk/decision/anomaly/trust by an implicit mapping. A `BLOCK` at
  `RiskCritical` might always be `CRITICAL` by an operator's own rule; a
  `CHALLENGE` at `RiskMedium` on a first-time integration might
  reasonably be `INFO`. No built-in mapping ships.
- **Alert Evaluation is a flat, first-match-wins matcher**
  (`alert.Condition`/`alert.Rule`), shaped exactly like
  `internal/policy.Condition`: every field optional, zero value means
  "don't care," no AND/OR/NOT combinators. It is a structurally
  independent, read-only consumer of `Result` — it does not import or
  couple back into `internal/policy`, and a `Decision` never
  automatically implies an `Alert`. Unlike `policy.Policy.Evaluate`,
  there is no mandatory default and no fail-closed requirement: an
  empty or non-matching rule set simply means "no alert," a safe,
  inert outcome — a deliberate, documented asymmetry with
  `policy.Policy.Evaluate`, not an oversight.
- **`Sink`** (conceptually `AlertSink` in the spec) is the one-method
  boundary every notification provider implements —
  `Send(ctx context.Context, a Alert) error`. `WebhookSink` is this
  stage's one implementation: a generic HTTPS webhook, signing every
  delivery with HMAC-SHA256 over a timestamp-bound payload (see
  [SECURITY.md § Alert/notification delivery
  integrity](SECURITY.md#alertnotification-delivery-integrity)) and
  enforcing a bounded timeout and payload size. No retry, backoff,
  deduplication, or delivery-state tracking exists yet — that is the
  separately-scoped Reliability stage
  ([CHANGELOG.md § v0.4.0](../CHANGELOG.md#v040--alert--notification-foundation)), not this one.
- **The webhook payload is a versioned contract**
  (`alert.Envelope{Version, Alert}`, currently `alert.PayloadVersion =
  "1"`) from its first release, not an internal struct serialized as a
  convenience — the same "no silent reinterpretation" discipline
  `internal/fingerprint`'s versioned hash already established.

**Configuring Alerts from outside this module** — the same `config`
package `PolicyConfig` lives in also defines `AlertConfig`/
`AlertRuleConfig`/`AlertConditionConfig`, primitive-typed structs
mirroring `[]alert.Rule`/`alert.Rule`/`alert.Condition` exactly, plus
`CompileAlerts(AlertConfig) ([]alert.Rule, error)` — the `alert.Rule`
analogue of `CompilePolicy`. This is a deliberately **separate**
document, type, and compilation path from `PolicyConfig`/
`CompilePolicy`, not a second field bolted onto the existing schema:
Policy configuration answers "what decision should Trustvian make,"
Alert configuration answers "which Results/Decisions should produce an
Alert," and `v0.5` preserves that separation at the configuration layer
exactly as `alert.Evaluate`'s own structural independence from
`internal/policy` already preserves it at runtime — see [ADR
0009](adr/0009-alert-config-is-a-separate-document.md) for the full
reasoning and the alternative (a combined `policy:`/`alerts:` schema
v2) it weighs against. [Task
023](archive/tasks/v0.5/023-declarative-alert-configuration.md) builds this model
and compiler; wiring it into the CLI or the Collector processor is
explicitly out of that task's scope (there is no alert-delivery flow
in either today for a compiled `[]alert.Rule` to plug into) and remains
separate, later work.

## Sequence-aware detection

Every signal above scores one event against its own fingerprint's
history — none of them see what happened *immediately before* it.
`v0.6` ([task 025](archive/tasks/v0.6/025-sequence-analysis-foundation.md), [ADR
0010](adr/0010-bounded-process-local-sequence-state.md)) adds exactly
one new signal, `transition_deviation`, integrated identically to
every existing one: another `anomaly.Signal`, folded into the same
noisy-OR `Anomaly.Score`, opt-in via a zero-default
`anomaly.Config.TransitionWeight` — no new `Result` field, no second
scoring path, no new pipeline stage.

- **What it answers.** Has this exact predecessor `Fingerprint.ID` ever
  led to this destination `Fingerprint.ID` before, for this actor? Not
  "how often" and not "how probable" — seen vs. never seen, the
  smallest useful unit of order. See
  [Sequence Analysis](sequence-analysis.md) for the full design.
- **Where the state lives.** No new `SequenceStore`. Two small,
  bounded additions to the existing `internal/baseline.Baseline`
  (`LastFingerprintID`, `LastFingerprintTime`) and `FingerprintStats`
  (`PredecessorCounts`, capped at 64 distinct entries) — the same
  `baseline.Key{ActorID, Environment}` scope, the same
  `internal/store` concurrency/persistence boundary, the same
  copy-on-write immutability discipline every other `Baseline` field
  already has. See ADR 0010 for why a parallel abstraction was
  considered and rejected.
- **Ordering.** A transition is only recorded, and only advances the
  actor's "last fingerprint" pointer, when an event's timestamp
  strictly follows the previous one — the identical guard
  `FingerprintStats`'s own interval statistics already use, for the
  identical out-of-order/backdating-resistance reason.
- **Cold start.** No predecessor at all (first-ever observation): the
  signal doesn't fire — there is no transition to evaluate. A
  predecessor exists but this transition has never been seen: the
  signal fires at maximal value, exactly like `categorical_novelty`
  does for a brand-new fingerprint — and is equally *not* automatically
  treated as dangerous, since `TransitionWeight` defaults to `0`.
- **Sequence length.** One step (`E(n-1) -> E(n)`) — a deliberately
  narrow foundation. n-gram/Markov generalization is explicitly future,
  unscoped work (see [CHANGELOG.md § v0.6](../CHANGELOG.md#v060--behavioral-detection-depth)).
- **Transition rarity (`v0.6` task 026).** A second signal,
  `transition_rarity`, evolves `transition_deviation`'s binary
  seen/unseen into a graded common/uncommon/rare measure for
  transitions that *have* been seen: `rarity(A->B) = 1 -
  (PredecessorCounts_B[A] / OutgoingTransitionTotal_A)`, an empirical
  relative frequency, never called a "probability" — see [ADR
  0011](adr/0011-transition-rarity-statistic-and-orientation.md) for
  why the destination-oriented `PredecessorCounts` alone cannot answer
  this and why the new `FingerprintStats.OutgoingTransitionTotal`
  scalar (living on the predecessor's own stats) is what makes an O(1)
  answer possible. Gated on a minimum-support threshold
  (`Config.MinTransitionObservations`, default `20`) below which the
  signal does not fire at all, and mutually exclusive with
  `transition_deviation` by construction (never both fire for the same
  transition). Opt-in via `Config.TransitionRarityWeight` (defaults to
  `0`), the same precedent as every other signal weight in this
  package.
- **Bounded 3-gram detection (`v0.6` task 027).** Two more signals,
  `ngram_deviation`/`ngram_rarity`, extend order-awareness one step
  further back: given a 3-gram `A -> B -> C`, has this exact
  (grandparent, predecessor) pair ever led to this destination before,
  and if so, how commonly? This is genuinely new information the
  one-step signals above cannot express — `A -> B` and `B -> C` can
  each be individually familiar while the complete sequence
  `A -> B -> C` has never occurred (proven end-to-end by
  `TestScoreNGramDeviationDetectsNovelTrigramDespiteFamiliarPairwiseTransitions`
  in `internal/anomaly/anomaly_test.go`). `Baseline` gains exactly one
  more scalar, `PreviousFingerprintID` (the fingerprint two steps
  back); `FingerprintStats` gains two new, *independently* bounded maps
  — `TrigramCounts` (on the destination, keyed by a `(grandparent,
  predecessor)` pair) and `TrigramContinuationTotal` (on the immediate
  predecessor, keyed by grandparent) — see
  [ADR 0012](adr/0012-bounded-trigram-behavioral-context.md) for why a
  scalar (as `OutgoingTransitionTotal` is for the 2-gram case) cannot
  answer a 3-gram's denominator, and for why `TrigramContinuationTotal`
  needed its own explicit bound rather than inheriting one from
  `PredecessorCounts`. A fixed 3-gram only — no configurable `n`, no
  Markov model. Both signal weights (`Config.NGramWeight`,
  `Config.NGramRarityWeight`) default to `0`, the same precedent as
  every prior signal weight in this package.
- **First-order Markov surprisal (`v0.6` task 028).** One more signal,
  `markov_surprisal`, offers an alternative severity curve over the
  *identical* evidence `transition_rarity` already reads — not new
  evidence. `transition_rarity = 1 - P(B|A)` and
  `markov_surprisal`'s underlying `surprisal = -log2(P(B|A))` are both
  strictly monotonic functions of the same `count/total` frequency;
  proven directly (not merely asserted) by
  `TestMarkovSurprisalIsMonotonicReparameterizationOfRarity` in
  `internal/anomaly/anomaly_test.go`. What differs is curve shape: the
  linear `1-frequency` mapping saturates near `1` quickly across the
  rare tail, while `-log2(frequency)` (before being bounded into
  `[0,1)`) keeps growing across the whole tail, preserving more
  resolution between "quite rare" and "extraordinarily rare." Because
  the two signals are reparameterizations of one statistic, not
  independent evidence, `anomaly.Score` never lets both contribute to
  `Score` at once: enabling `Config.MarkovWeight` forces
  `transition_rarity`'s own contribution to zero for that call
  (`transition_rarity` remains visible in `Anomaly.Contributors` for
  explainability; only its scoring effect is suppressed) — enforced in
  code, proven by
  `TestScoreMarkovAndTransitionRarityAreMutuallyExclusiveInScoring`, not
  left to operator discipline. No new `Baseline`/`FingerprintStats`
  state: Markov reuses `PredecessorCounts`/`OutgoingTransitionTotal`
  (task 025/026) and `Config.MinTransitionObservations`'s own gate
  exactly — no separate Markov-specific state or threshold. See [ADR
  0013](adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md)
  for the full mathematical definition, normalization, and the
  mandatory duplication analysis this task's own brief required before
  any code was written.

## AI Agent behavioral context

`v0.7` ([task 014](archive/tasks/v0.7/014-ai-agent.md), [ADR
0014](adr/0014-ai-agents-as-first-class-behavioral-actors.md)) adds
three optional `event.Context` fields for AI-agent session,
delegation, and approval context. No new package, no new pipeline
stage, no agent-specific `Fingerprint`/`Baseline`/`Anomaly` type — an
AI agent is `Actor{Type: ActorTypeAIAgent}`, already representable
since `v0.1`, flowing through the identical, unmodified pipeline.

**What affects `Fingerprint` identity, and what deliberately does
not** — the central design question this task answers explicitly:

| Field | Affects `Fingerprint`? |
|---|---|
| `Actor.Type`, `Operation.Category`, `Operation.Name`, `Target.Name`, `Target.Category`, `Context.Environment` | **Yes** (pre-existing, unchanged) |
| `Context.TraceID`, `Context.SpanID` | No (pre-existing) |
| `Context.SessionID` | **No** (new, `v0.7`) |
| `Context.DelegatedFrom` | **No** (new, `v0.7`) |
| `Context.ApprovalStatus` | **No** (new, `v0.7`) |
| `Context.ParentSpanID`, `Context.SpanLineage`, `Event.Execution` | **No** (task 084) — properties of one occurrence |

**Telemetry conventions fill those dimensions better without changing which ones they are.**
[Task 075](tasks/v1.0/075-ai-semantic-telemetry-normalization.md) reads
agent-oriented telemetry conventions at the adapter boundary, so
`Operation.Category`/`Name` and `Target.Name` can carry `tool · export_customer →
export.localhost` where they previously carried `http · POST /v1/export`. The
table above is unchanged: the same six dimensions are identity, and
`Context.SessionID` — now populated from `gen_ai.conversation.id` or `session.id`
rather than only by hand — is still deliberately not one of them.

Two additions worth naming because they are the kind of thing a reader would
expect to be identity and which deliberately is not:

| Value | Affects `Fingerprint`? |
|---|---|
| The **fidelity** a behavior was derived at (`transport` / `semantic`) | **No** (task 075) — it would reset every baseline the day a producer upgraded its instrumentation |
| The **behavior layer** (`model` / `tool` / `retrieval` / `transport`) | **No** (task 083) — same reason |
| Any content attribute a convention defines — prompts, completions, tool arguments, retrieved documents | **No**, and none is read at all — see [ADR 0045](adr/0045-conventions-are-read-frameworks-are-not.md) |

- **`SessionID`** groups events belonging to one bounded
  interaction/session (e.g. one agent conversation). It must never
  affect `Fingerprint`/`baseline.Key` identity: a session identifier is
  typically unique per conversation, so folding it into behavioral
  identity would give every session a fresh, never-reused Fingerprint
  or Baseline, and the system would never accumulate enough
  observations to learn anything — proven, not just asserted, by
  `TestAnalyzeAgentSessionIDDoesNotExplodeBaseline`: 1,000 events with
  1,000 distinct `SessionID` values and otherwise-identical behavior
  accumulate into exactly one `Fingerprint` entry with `Count == 1000`.
- **`DelegatedFrom`** carries the immediate parent `Actor.ID` for a
  single agent-to-agent delegation hop (Agent A delegates to Agent B:
  B's own `Event` carries `Actor.ID = B`, `DelegatedFrom = A`'s ID). A
  single hop only — no delegation graph, no `DelegationID`, no depth
  counter. Never enters `Fingerprint`/`baseline.Key` identity — proven
  by `TestAnalyzeAgentDelegationContextScoredIdentically` (task 014)
  and `TestAnalyzeDelegationFingerprintStability`/
  `TestScoreDelegationDeviationDoesNotAffectFingerprintOrStable`
  (task 031).

  **`v0.7` task 031 gave it its first real consumer:**
  `features.VolatileFeatures.DelegatedFrom` (read from
  `Context.DelegatedFrom` in `Extract`, never into `StableFeatures`)
  feeds `baseline.Baseline.DelegatorCounts` — a bounded map (64
  distinct delegators, `maxDelegators`), scoped to the *actor*
  receiving the delegation, not to any one operation it performs
  (`Baseline`-level state, like `LastFingerprintID`, not
  `FingerprintStats`-level state, like `PredecessorCounts`) — and a
  new opt-in anomaly signal, `delegation_deviation`
  (`Config.DelegationWeight`, defaults to `0`): binary seen/unseen,
  mirroring `transition_deviation`'s exact shape, deliberately not a
  rarity/probability estimate. See [docs/policy-guide.md](policy-guide.md)
  for `ApprovalStatus`'s own analogous consumer and [ADR
  0016](adr/0016-delegation-as-behavioral-evidence-not-provenance.md)
  for the full design.

  **Orientation, stated precisely:** the learned relationship is "for
  the current actor (the delegatee), how familiar is this delegator?"
  — never "for this delegator, which actors does it typically delegate
  to." `Actor.ID` is the actor's own behavioral identity, as always;
  `DelegatedFrom` is contextual provenance about one specific event,
  never itself a second identity dimension.

  **Cold start:** a brand-new actor's first delegation observation
  still evaluates `delegation_deviation` (maximal novelty, since no
  delegator has ever been seen) — cold start is handled the same way
  every other signal in this module handles it, through the overall
  `Anomaly.Confidence` this actor's own `Fingerprint` maturity drives,
  not by a dedicated minimum-support gate on this signal (which,
  unlike `transition_rarity`, does not have one — see ADR 0016 for why
  a binary seen/unseen signal doesn't need it).

  **Familiar is not authorized; unfamiliar is not malicious.**
  `delegation_deviation` is behavioral evidence only — "is this
  unusual for this actor?" — never an authorization or provenance
  judgment. `DelegatedFrom` remains exactly as unauthenticated,
  self-reported input after task 031 as it was after task 014; nothing
  in this task verifies who actually delegated an event. See
  [docs/SECURITY.md § AI Agent behavioral security](SECURITY.md) for
  the full trust-boundary writeup, and
  [ApprovalStatus's own entry](#ai-agent-behavioral-context) above for
  the parallel case task 030 already established — the two evidence
  types are proven independent by
  `TestAnalyzeDelegationApprovalIndependence`.
- **`ApprovalStatus`** (`event.ApprovalStatus`: `ApprovalUnspecified`
  (zero value) / `NotRequired` / `Required` (a requirement flagged
  with no decision recorded yet — functionally *pending*) / `Approved`
  / `Denied`) records a per-event fact, not a workflow state —
  Trustvian does not manage an approval process. Never read by
  `features.Extract` — it does not affect `Fingerprint`/`baseline.Key`
  identity or `Anomaly`/`Trust` scoring (proven by
  `TestAnalyzeApprovalPolicyBehavioralScoreIndependence`), only
  `Decision`.

  **`v0.7` task 030 gave it its first real consumer:** `policy.Input`/
  `Condition` (and `config.PolicyCondition`) each gained an
  `ApprovalStatus` field, an equality match identical in kind to every
  other `Condition` field. This is the same "typed but optional, no
  default assumed" precedent `OperationDirection` established — the
  field waited for a concrete consumer before one was built. See
  [docs/policy-guide.md § an operation that requires
  approval](policy-guide.md#example-an-operation-that-requires-approval)
  for the worked example and [ADR
  0015](adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md)
  for why this belongs to `policy`, never `anomaly`.

  **Approval *state* vs. approval *evidence trust* — two different
  questions.** *State* is what `ApprovalStatus`'s five values encode
  (was approval required, what was the outcome) — task 030 makes
  `Policy` able to read and act on this state. *Evidence trust* is a
  separate question this task deliberately does **not** answer:
  whether a given `ApprovalStatus` value can be believed. `Approved`
  is evidence the event producer supplied, not proof Trustvian
  verified — an AI agent's own event self-declaring `Approved` is
  evaluated exactly as-is, with no cryptographic check, no OAuth/IAM
  call, and no coupling to `Actor.IdentityConfidence` (a deliberately
  independent signal — see ADR 0015). **Policy owns the requirement,
  not the event**: an event self-declaring `ApprovalNotRequired`
  cannot exempt itself from a `Policy` rule that requires `Approved` —
  proven by
  `TestEvaluateApprovalPolicyAuthorityEventCannotOverridePolicy`. See
  [docs/SECURITY.md § AI Agent behavioral security](SECURITY.md)'s
  "Approval self-assertion" entry for the full trust-boundary
  writeup.
- **Agent identity is `Actor.ID`, never model/provider metadata.** A
  model name (`"gpt-5"`) is not behavioral identity — two agents built
  on the same model are different actors; the same agent migrating
  model versions is still the same actor. No field for model/provider
  metadata is added; `Event.Attributes` already covers optional,
  non-behavioral metadata a producer wants to carry (like
  `duration_ms`), without risk of it being mistaken for identity.
- **Tool calls and destinations reuse `Operation`/`Target` as-is.**
  `Operation{Category: OperationCategoryTool, Name: "shell.execute"}`,
  `Target{Name: "...", Category: TargetCategoryExternal}` — no `Tool`
  or `AgentDestination` type exists. Tool identity must be a stable,
  low-cardinality string (`"filesystem.read"`, `"secret.read"`) —
  **never** raw prompt text, tool argument values, or free-form user
  input, which would both explode Fingerprint cardinality and retain
  sensitive data in behavioral state indefinitely. See
  [docs/SECURITY.md § AI Agent behavioral security](SECURITY.md).

  Since task 075 that stable string can come from the telemetry itself —
  `gen_ai.tool.name` or OpenInference's `tool.name` — rather than only from a
  producer that set `Operation.Name` by hand. The prohibition is unchanged and is
  now enforced by the mapping: the adapter reads identity attributes and reads no
  content attribute, so a tool *argument* has no path into `Operation.Name`.
- **No new detector — the existing signals already work, proven not
  assumed.** `TestAnalyzeAgentToolNoveltyDetectedByExistingEngine`
  shows the pre-existing `categorical_novelty`/`transition_deviation`
  signals fire on an unexpected tool call. The task's own mandatory
  critical test,
  `TestAnalyzeAgentToolSequenceNoveltyDetectedByExistingEngine`,
  mirrors [task 027](archive/tasks/v0.6/027-bounded-ngram-detection.md)'s own proof:
  `search -> secret.read` and `secret.read -> external.post` are each
  trained as familiar pairwise transitions via different contexts, the
  complete `search -> secret.read -> external.post` sequence is never
  trained as one continuous path, and the pre-existing `ngram_deviation`
  signal (task 027, built with zero knowledge of AI agents) still
  detects the higher-order novelty.

## The control-plane domain (`platform/`)

The concepts above are the *behavioral* domain: what an actor did and how much
it is trusted. The `platform/` module holds a second, separate vocabulary —
what is being evaluated, and by whom:

```text
Project
  ├─ Agent
  │   └─ Candidate
  │       └─ EvaluationRun ──▶ EnvironmentRef
  │                       └──▶ BehavioralProfileRef
  ├─ Environment ◀──────────────────┘
  ├─ Promotion ──▶ two EvaluationRuns, two Environments, one gate result
  └─ ScenarioExecution ──▶ N reference + N candidate EvaluationRuns,
                           one recorded gate result
```

- **Project** — a local control-plane workspace owning agents. Not a tenant,
  organization, or access-control boundary.
- **Agent** — the stable logical identity of an agentic application across
  every version of it. Independent of commit, digest, model, and deployment:
  those describe a Candidate.
- **Candidate** — one version or configuration of an Agent. Its metadata
  (label, source ref, artifact digest, model, tool-set and config digests) is
  **descriptive only**.
- **EvaluationRun** — one bounded execution of one Candidate against one
  environment reference and one behavioral profile reference. It carries
  identity and lifecycle, and no results.
- **Environment** — one deployment stage a project owns, identified by the
  `EnvironmentRef` a run records. It carries a name, an optional promotion
  rank, and an active/archived status, and nothing else: no URL, credential,
  secret or deployment target. Identity is `(ProjectID, EnvironmentRef)`, so
  two projects may each own a `staging` and they are two environments.
- **EnvironmentRef** — the reference a run records and the environment's
  identity, not a separate identifier. Still an open set: `local`, `staging`,
  `production` are plausible values, not a closed enum.
- **BehavioralProfileRef** — an opaque reference. The platform does not
  allocate one; the client that creates the run does. `trustvian dev` uses
  the candidate identifier unless told otherwise, and `trustvian eval run`
  gives every repetition its own (`<execution>-<side>-p<i>`). An environment
  is not that choice.
- **Promotion** — one immutable record that a candidate's evidence was gated
  and the verdict accepted or refused advancement from one environment toward
  another. It is a record of a **decision**, not of a deployment: see below.
- **ScenarioExecution** — one `trustvian eval run` invocation: N repetitions
  per side under one scope (project, agent, environment), compared once. See
  [Scenario executions](#scenario-executions) below.

**These two domains never merge, and the separation is the point.** No
platform identifier is a behavioral dimension: a candidate is not an actor, a
run is not a fingerprint, and a git SHA never reaches a `StableFeatures` or a
baseline key — treating one as behavior would make every deployment look like
a brand-new actor and destroy the learning the product exists to accumulate.
The relationship runs one way through a reference:

```text
platform BehavioralProfileRef
        ↓  the client that runs the workload chooses it; the Collector's
        ↓  evaluation.behavioral_profile selects the Engine's scope
core     trustvian.WithLearningScope(string)
```

A run's `Status` is execution state, not a verdict. `completed` means the
execution finished; whether the candidate passed is a gate's separate answer.

An environment's **rank** is an ordering, not a permission. `CanPromote(from,
to)` reports whether one environment is forward of another inside one project
— both active, both ranked, strictly greater — and answers nothing about
whether a candidate may actually move there. That is a promotion workflow's
question, and the engine promotes nothing either way. Environments are
archived rather than deleted, so a completed run's reference always resolves.
See [ADR 0039](adr/0039-environments-are-project-owned-ranked-references.md).

### Evaluation evidence

`EvaluationAggregate` (task 053) is the bounded summary of what one evaluation
observed, folded one `trustvian.DecisionRecord` at a time:

```text
Engine ──▶ DecisionRecord ──▶ EvaluationAggregate
```

It counts observations, decisions by category, risk levels, approval evidence,
and policy-selection shape; it summarizes the five numeric signals as
`{Count, Sum, Min, Max}` with an explicitly-absent mean when empty; it
summarizes observed durations as integer nanoseconds and counts span statuses
(task 084), with an observation that reported no duration or no status
counted as such rather than as zero; and it bounds the evidence in event time.

Three properties define it:

- **Bounded.** Fixed-shape and O(1) in the number of records. No slice, no
  map, no retained record — retaining them would make it an accidental event
  archive, which is a separate capability with its own boundary.
- **Factual.** It counts what happened. It computes no score, grade, pass,
  promotability, critical-violation count, or new-behavior count: each of
  those is a judgement needing context — thresholds, a comparison, policy
  severity — that the aggregate does not hold.
- **Fail-closed.** Every consumed field is validated before any state changes,
  and a rejected record leaves the aggregate identical. Malformed evidence is
  refused, never repaired.

See [ADR 0026](adr/0026-evaluation-aggregation-is-bounded-evidence.md).

### Retained observation history

`Observation` and `ObservationHistory` (task 067) are the third durable
artefact a run produces, beside the aggregate and the behavior snapshot. Both
of those are reductions; this is the row that says *which* observation scored
0.91, when it happened relative to the one before it, and what trace it
belonged to.

```text
DecisionRecord ──▶ Observation      identified by (RunID, Sequence)
                                    ordered by Sequence, never by timestamp
```

- **Identity is `(RunID, Sequence)`**, the ingest sequence the retry contract
  already assigns. A span id is unique only inside its trace, a parent span id
  is a trace-scoped reference, and an event id is caller-supplied — none of the
  three may be an identity, because each would make it a property of the
  producer behaving well.
- **Ordering is by sequence, not by time.** Equal timestamps are ordinary,
  producer clocks are not authority, and a parent span ends *after* the
  children it started — so a child is routinely accepted first. Sorting by time
  would reorder a trace against its own arrival.
- **Fixed shape.** An allowlist expressed as columns. `Contributors` is
  deliberately absent as the record's one variable-length field, and there is no
  attribute map, span-event list or payload column — so no prompt, completion,
  argument, result, document or arbitrary attribute can be written through one.
- **Bounded at 4096 per run.** Past it the run keeps ingesting and reports its
  history as partial, the same rule the behavior collector applies at 512
  distinct behaviors: saturation is degraded evidence, not a failed ingest.

`ObservationHistory` has **three** states because two would force a lie:

| State | Means |
|---|---|
| `complete` | every accepted record is retained |
| `partial` | some are and some are not — the run saturated, or began before retention existed |
| `unavailable` | this run has records and none of their history was ever retained |

A run migrated from a pre-retention schema reports `unavailable`, never an
empty `complete`: reporting "complete, zero rows" would be a fabricated
historical fact. See
[ADR 0048](adr/0048-retained-history-is-sequence-identified-bounded-and-honest-about-absence.md).

`ObservationScope` (task 076) is how that history is read in a correlated view:
at most one of session, trace or behavioral identity, applied as a storage
predicate before the page bound and scoped inside one run. More than one is
refused rather than answered — see
[ADR 0049](adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md).

**Two facts about an observation are not retained on its row, and both matter
to a reader.** Fidelity (task 075) and behavioral layer (task 083) travel beside
a record at ingest and on the realtime frame and have no column here, so a
retained observation cannot say whether its operation's identity came from
agent-oriented telemetry or from its transport. Since schema 13 (task 081,
[ADR 0068](adr/0068-fidelity-and-layer-are-persisted-as-per-behavior-counts.md))
the answer is kept per behavior instead, as counts on the behavior snapshot —
see [Behavioral comparison](#behavioral-comparison). Sequence signals live in
the anomaly contributors, which are excluded, so nothing durable says whether an
order departed from what was learned. Consumers state both absences rather than
inferring either.

### Behavioral comparison

`BehaviorCollector`, `BehaviorSnapshot` and `BehaviorDiff` (task 054) answer a
different question from the aggregate: not *what happened*, but *which
behaviors happened*, and how that changed between two evaluations.

```text
DecisionRecord ──▶ BehaviorCollector ──▶ BehaviorSnapshot ──▶ BehaviorDiff
```

The comparison key is `FingerprintID` — the core's stable behavioral-shape
identity, and nothing else. Not the actor, session, trace, candidate, run,
profile, commit, or artifact digest. A diff keyed by any of those would report
change every time a candidate was rebuilt.

Each behavior is classified `Added`, `Removed` or `Shared`, with counts and
rates normalized within each snapshot so a long reference is comparable to a
short candidate. Rates are derived from integer counts at comparison time, so
equal counts give identical rates whatever order records arrived in.

Two refusals define the type as much as its output. A collector that observes
more than 512 distinct behaviors is **permanently incomplete**, and an
incomplete snapshot **cannot be compared** — the alternative is a confident,
specific, wrong answer to "what is new". And one fingerprint claiming two
different shapes fails closed rather than merging them.

Like the aggregate, it reports facts and no verdict: no score, severity,
threshold, or promotability. See
[ADR 0027](adr/0027-behavioral-diff-compares-bounded-snapshots.md).

Each snapshot entry (`BehaviorEntry`) also carries two fixed-shape sets of
counts about the observations it counted. Neither is identity:

- **Operational evidence** (task 087) — an 11-bucket duration histogram with
  fixed upper-inclusive boundaries (1, 5, 10, 50, 100, 250, 500, 1 000,
  2 500 and 10 000 ms, then everything above), span-status counts, HTTP
  status classes with 429 counted on its own, and token counts. Every group
  partitions the entry's observations, so an observation that stated nothing
  is counted as unobserved, never as a measured zero. No percentile, mean or
  rate.
- **Fidelity and layer** (task 081) — counts per fidelity (`semantic`,
  `transport`, `unrecorded`) and per layer. A behavior's reported fidelity is
  the lowest level seen. An observation recorded before schema 13, or whose
  envelope did not state the pair, is `unrecorded`; a contradictory pair is
  refused rather than counted.

### Evaluation scorecards

`EvaluationScorecard` (task 055) composes the two reducers' outputs into one
fixed-shape comparison:

```text
reference EvaluationAggregate ───────┐
BehaviorDiff(reference → candidate) ─┼──▶ EvaluationScorecard
candidate EvaluationAggregate ───────┘
```

It reports how the decision, risk, approval and policy-selection
distributions moved, how the five numeric signals moved, and how much
behavioral presence overlapped — as counts, rates and signed deltas.

Three properties define it:

- **Comparative.** Both aggregates are required: "reference block rate →
  candidate block rate" cannot be expressed from one side, and the diff
  cannot be derived from the aggregates nor they from it.
- **Fixed-shape.** No slice, map, or retained input. Construction costs the
  same for a 1,024-delta comparison as for an empty one, which is why the
  diff's deltas are not copied — a caller wanting per-behavior rows reads the
  `BehaviorDiff` it already holds.
- **Evidence, still.** No overall score, no weight, no threshold, no verdict.
  Concepts the evidence cannot express — critical policy violations, blocked
  sensitive actions, per-rule compliance — are **absent rather than reported
  as zero**, because a zero derived from evidence with no notion of severity
  is a false security claim.

Empty denominators are undefined rather than zero throughout. Two evaluations
that observed nothing are unmeasured, not identical.

See [ADR 0028](adr/0028-scorecards-are-fixed-shape-comparative-evidence.md).

### Deterministic hard gates

`EvaluateEvaluationGate` (task 056) is the first platform layer allowed to
judge a comparison. It pairs a scorecard with explicit caller-owned limits:

```text
EvaluationScorecard ──┐
                      ├──▶ EvaluateEvaluationGate ──▶ EvaluationGateResult
EvaluationGatePolicy ─┘                                   PASS | FAIL
```

Five checks, all evaluated on every call, in a stable order: reference
evidence present, candidate evidence present, added behaviors within limit,
candidate block decisions within limit, candidate critical-risk observations
within limit. PASS requires all five.

A sixth check is optional (ADR 0052, issue 131): counted behavioral changes
within `MaxAddedBehaviorChanges`. It has a state — `evaluated`,
`not_evaluated`, or `not_recorded` on a promotion stored before it existed — and
takes part in the verdict only when evaluated. Its limit is the one limit with
an absent state: an `OptionalGateLimit` whose zero value is absent, because
making it mandatory would have defaulted every existing caller to a maximum of
zero.

The separation is the point. A scorecard says what happened; a policy says
what is acceptable. The same card yields PASS or FAIL depending only on the
limits, and the limits belong to the caller because no fact in the evidence
can settle whether three added behaviors are routine or disqualifying.

Four properties define it:

- **Integer-only.** No mean, rate, delta, or presence ratio takes part in a
  verdict. Floating-point sums are not guaranteed bit-identical under record
  reordering, and an average is how one dimension offsets another.
- **Fail-closed.** An unbound policy or unbound card is an error. A *valid*
  evaluation that observed nothing is not an error — it fails the two
  sufficiency gates, which exist because a candidate that ran zero records
  satisfies every maximum.
- **Strict zero.** `MaxBlockDecisions = 0` accepts no block decision, so zero
  never means "unset"; a private marker separates the strictest policy from
  an absent one.
- **A verdict, not an action.** PASS means only that the configured gates
  passed. Recording a promotion on that verdict is task 066's (below), and the
  result itself performs nothing.

Names stay factual: a block decision is not a policy violation, a
critical-risk observation is not an incident, and an added behavior is not a
defect. Gates the evidence cannot support — critical policy violations,
sensitive-resource access, approval compliance — remain **absent rather than
reported as zero**.

See [ADR 0029](adr/0029-hard-gates-use-explicit-integer-evidence.md).

### Promotion decisions

`Promotion` (task 066) is the first platform layer allowed to conclude that a
candidate may advance between environments, and the last one that could be
mistaken for deploying something. One sentence governs it:

> Trustvian records a promotion decision. Trustvian does not deploy anything.

```text
reference run ─┐
               ├─▶ gate result ──▶ Promotion ──▶ accepted | rejected
candidate run ─┘        │
                        └─ source environment inferred from the two runs
```

It is **not** a deployment, a release, a rollout, an approval, an
authorization, or a rollback; it triggers nothing and nothing in the platform
acts on it. There is no `Deployment`, no `Release`, and no
`CurrentEnvironment`: modeling where a candidate "currently lives" would mean
setting a field at the moment of the *decision* and serving it as fact
thereafter, kept accurate only by a deployment system Trustvian is not
integrated with and cannot observe. A consumer wanting a current-state view
computes one from promotion history and owns that assumption.

Five properties define it:

- **Append-only.** `CreatePromotion`, read, and list. No update, no delete, no
  status field, at any layer. A decision made in error is superseded by a later
  decision, and both stay visible.
- **Both verdicts recorded.** FAIL produces a stored `rejected` promotion.
  Because gate limits are caller-owned, a history of acceptances only would
  hide a caller retrying with progressively looser limits until one passed. A
  *structural* failure — unknown runs, two different agents, a target that is
  not forward — is not a decision and is not recorded.
- **The outcome is derived, never supplied.** `outcomeFor` is the only place
  `accepted ↔ PASS` is written. No request field can set it, and a stored
  `accepted` beside a FAIL verdict is corruption on read rather than something
  to repair.
- **The gate result is snapshotted, field for field.** The stored promotion
  holds the exact `EvaluationGateResult` the decision consumed — every count,
  every limit, every per-check `Passed` flag — restored without recomputation.
  A corrected gate may legitimately answer the same immutable evidence
  differently later; history has to survive its own bug fixes, so a row that
  disagrees with today's arithmetic is history rather than damage.
- **It commits only against the state it was decided against.** The invariant
  spans three rows, so the write re-reads both environments inside its own
  transaction, compares both revisions, re-asks `CanPromote`, and inserts —
  anything moved, including a rename, yields `ErrStoreConflict` and no row.

Stage skipping is allowed: `sandbox → production` is valid if the ranks are
forward and the gate passed. `CanPromote` remains the only ordering primitive,
and it still authorizes nothing. There is no `approved_by` and no actor
identity — an audit field nobody authenticates is one that can say anything.

See [ADR 0040](adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md).

### Repeated evaluation

One run of a model-driven agent is not evidence: an unchanged agent's behavior
set moves between isolated runs. `CompareRepeatedEvaluations` (task 078,
[ADR 0053](adr/0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md))
asks the question over N repetitions per side (1 ≤ N ≤ 64) and answers it in
integers:

```text
reference_runs_present   how many of the N reference runs showed the behavior
candidate_runs_present   how many of the N candidate runs showed it
```

- **Presence, never pairing.** Reference repetition *i* is never compared with
  candidate repetition *i*, so the counts do not depend on order.
- **Isolation is checked, not trusted.** Each repetition must run under its own
  behavioral profile, or repetition *i* is scored against what 1…*i*−1 taught
  the engine. Shared profiles are refused (`ErrRepeatedIsolation`).
- **Classification uses caller-owned k and j** (1 ≤ k ≤ N, 0 ≤ j < k):
  `added` is candidate ≥ k and reference ≤ j, `removed` the mirror image, and
  every other behavior is `neither`. `removed` is reported and gates nothing.
- **Six named checks, all evaluated on every call:** both sides completed N
  repetitions, no repetition failing minimum evidence, repeatedly added
  behaviors within limit, and the worst candidate repetition's block decisions
  and critical-risk observations within limit. At N > 1 the last two carry the
  advisory `fresh_scope`: against per-repetition scopes the engine has learned
  nothing. At N = 1, k = 1, j = 0 the verdict equals the single-run gate's.
- **Four optional frequency checks** (tasks 106 and 081):
  `min_candidate_frequency`, `max_lost_behaviors`, `max_calls_per_run` (per
  named target, at most 16 targets, [ADR 0066](adr/0066-a-per-target-call-limit-is-one-named-check-over-a-bounded-list.md))
  and `max_llm_calls_per_run`. Omitted means `not_evaluated` and leaves the
  verdict alone. Supplied but without the evidence it needs means `deferred`,
  which names what was missing and **fails** the verdict: PASS would answer a
  question nobody had evidence for. A named target neither side called is
  `not_observed` and fails too, so a misspelled target cannot pass silently.

Frequency is evidence beside presence. Per behavior and per target, each side
reports calls per run as a minimum, a maximum and a floored mean in
thousandths, over its completed repetitions; a run where the behavior did not
appear is a run of zero calls. A target's call ratio is an integer permille,
and with a reference total of zero there is no ratio. A behavior present in all
N reference runs and missing from at least one candidate run is `lost`, a flag
beside the classification rather than a fourth value of it. Lost transitions
are reported as `not_recorded`: nothing records a sequence per execution.

### Scenario executions

`ScenarioExecution` (task 078,
[ADR 0054](adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md))
persists one `trustvian eval run`: its scenario name, scope, N, the ordered
repetitions of each side with their runs and profiles, the execution whose
reference side it reused (if any), its lifecycle and its verdict.

```text
running ──complete──▶ completed   the comparison was evaluated: PASS or FAIL
   └─────fail──────▶ failed       no verdict
```

- **Completed is not passed.** A FAIL is a completed execution and a usable
  reference. Running and failed executions never are.
- **The control plane resolves every reference.** `last` is the completed
  execution of the same scenario name, project, agent and environment with the
  highest completion sequence — an integer assigned per project when the
  completion commits, not a clock. If its N differs or its evidence is
  unusable, the error names it; an older execution is never substituted. When a
  reference is reused, the control plane supplies its runs and refuses any the
  client sends.
- **Metadata only.** No scenario command, no environment variable value, no
  limit a later execution could inherit, and no content.

Four recorded facts sit beside that. None is identity, and none reaches a
fingerprint or a baseline key:

| Fact | Since | What it is |
|---|---|---|
| Provenance, per side (task 086, [ADR 0067](adr/0067-scenario-provenance-is-recorded-per-execution-side.md)) | schema 12 | `scenario_digest` and `input_digest` (`sha256:` digests the CLI computes, because it reads the files — the control plane validates their format and cannot recompute them), and the declared model and prompt reference (a name and a digest, never prompt text). Environment variable values are excluded from the digest |
| The gate record ([ADR 0069](adr/0069-a-completed-execution-records-the-gate-it-consumed.md)) | schema 14 | The limits, every check with its state, actual value, bound and outcome, and the verdict, as one canonical text that must re-encode to itself and agree with itself. A report reads it and never re-evaluates the gate |
| CLI version and reference mode (`execution` or `last`) | schema 15 | What the runner said about itself when it began |

Each fact recorded before its schema step reads `not_recorded`, never an empty
or zero value.

**Sameness** compares the two sides' provenance: `same_scenario`,
`same_inputs`, `same_model` and `same_prompt_ref`, each `true`, `false` or
`not_recorded`. A value missing on either side is never `false`. A `false`
adds a warning (`scenario_differs`, `inputs_differ`, `model_differs`,
`prompt_ref_differs`) that states the fact and gives no advice. No check
reads sameness and it never changes a verdict.

**The report** (`GET /v1/scenario-executions/{id}/report`, `trustvian eval
report`) is result document version `"2"`, assembled by the control plane from
the persisted runs, the execution's provenance and its gate record. It adds no
evidence: row totals, per-repetition presence and display strings are
computed server-side beside the integers they format, so no client counts
or computes anything.

### Operational, frequency and cost evidence

A comparison carries three operational sections — latency, errors and tokens
(task 087) — each a reference side and a candidate side summed from the
persisted behavior entries. A section is comparable only when both sides
carry its evidence. Otherwise it says which side did not
(`reference_unavailable`, `candidate_unavailable`, `both_unavailable`) and
carries no delta at all: a delta against "nothing was measured" would read as
a change nobody observed. Gates read none of these sections.

Cost is optional. Given an operator-supplied pricing table
(`trustvian-local --pricing <file>`: integer micro-units per million tokens,
with a `pricing_version`, a `source` and a currency), a side's cost is the sum
over priced models of `floor((input × input price + output × output price) /
1 000 000)`, in integer micro-units. Tokens of a model the table does not price,
and every total reported without an input/output split, are `unpriced` and
never given a guessed price. Every figure travels with the table's version,
source and digest. A figure past `uint64` is refused, never wrapped. Without a
table there is no cost section, and there is no cost gate.

### Suggestions are rule-table outputs

A suggestion ([ADR 0064](adr/0064-suggestions-are-rule-table-outputs-beside-the-evidence.md))
is the output of one named, versioned rule over the evidence of the document it
sits in, rendered from a fixed template whose only substitutions are the
evidence values it names. Rules compare integers and closed categories, combine
nothing into a score, and never fire on evidence that was not reported. There is
no severity and no ranking; table order is the only order.

| Table | Rules |
|---|---|
| Pipeline status | `status.no_collector`, `status.no_producer`, `status.spans_without_service_name`, `status.collapsed_http_operations`, `status.admission_near_bound` |
| Repeated comparison | `compare.added_in_all_runs`, `compare.target_load_with_429`, `compare.lost_in_half` |

Each document carries at most 64, with a `suggestions_truncated` flag.
Suggestions sit beside the evidence and are read by nothing — no check, no
verdict, no exit code, no stored record — and tests prove that removing either
table changes nothing else. Sameness warnings are not suggestions.

### Pipeline status

`PipelineStatus` (task 105,
[ADR 0065](adr/0065-collectors-report-pipeline-status-the-control-plane-holds-it-in-memory.md))
explains an empty Live view. A Collector configured with a `status:` block
reports what reached it on a fixed interval (10 s by default): counts, ages,
learning outcomes, and the identity metadata a producer declares
(`service.name`, instrumentation scope, `telemetry.sdk.*`) — nothing a span
says about its subject.

- **Held in memory, never persisted.** The latest report per Collector, at most
  16 Collectors; a 17th is refused rather than one evicted. A Collector that
  stops reporting is `stale` after 30 s and forgotten after 5 minutes. A
  restart loses everything, and nothing — no gate, no promotion — reads it.
- **The control plane's clock.** Reports carry ages, not timestamps, so a
  skewed Collector cannot make a producer look fresh or stale.
- **Landing.** `live` when a reporting Collector saw a producer, or an ingest
  record committed, within 30 s; `status` otherwise. Decided once, here.
- **Engine facts are `unavailable`.** The engine exposes no accessor for its
  baseline count, maturity or fingerprint admission, and the document says so
  rather than approximating them.

### Local persistence

Task 057 makes the values above survive a restart, behind narrow
capabilities rather than a generic database. Two carry the core of it:

```text
ControlStore     Project, Agent, Candidate, Environment, Promotion
EvaluationStore  EvaluationRun, and one run's evidence
```

Later capabilities sit beside them, each for one need: ingest state, retained
observations and the views over them (traces, sessions, recent runs), and
scenario executions. The schema is at version 15 on both backends, SQLite and
PostgreSQL.

What is persisted is what cannot be rebuilt: the entities, the
`EvaluationAggregate`, the `BehaviorSnapshot` with its bounded entries, and
the bounded observation history. `BehaviorDiff`, `EvaluationScorecard` and a
run comparison's `EvaluationGateResult` are deterministic functions of those,
so they are recomputed on demand — storing them would create a second thing
that can be true, and the first disagreement would have no principled
resolution. The two exceptions are decisions, not derived reads: a promotion
and a completed scenario execution each keep the gate result they consumed, so
history survives a later fix to the gate.

Four rules shape the adapter:

- **Create means create.** A duplicate identity is refused, never upserted.
  The same `CandidateID` with a different artifact digest must not rewrite
  what a finished run was evaluated against.
- **Identity stays caller-owned.** The store generates no `ProjectID`,
  `AgentID`, `CandidateID`, `EvaluationRunID` or profile reference.
- **Runs update by compare-and-swap**, and are rebuilt by replaying their
  domain transitions rather than by writing private fields — so a corrupt
  chronology fails the same invariant a live value would.
- **Evidence is one transaction.** The aggregate and snapshot commit together
  or not at all, evidence never moves backwards, and saturation is sticky: an
  incomplete snapshot stays incomplete across a restart.

No raw event is stored. The one table that grows per accepted record is the
retained observation history, a fixed-column allowlist bounded at 4096 rows
per run. Core baseline state stays in the engine's own stores.

See [ADR 0030](adr/0030-local-persistence-stores-authoritative-bounded-state.md).

### The control plane

Task 058 adds `ControlPlane`, the authoritative service over those
capabilities. It creates and reads the hierarchy, drives a run's lifecycle,
ingests evidence, reports progress, and derives a comparison. Transports call
it; they decide nothing.

**Ingest is sequenced.** A `DecisionRecord` arrives with an explicit
per-run sequence and the behavioral profile it was produced under — the
profile travels beside the record because `DecisionRecord` carries no learning
scope, and ADR 0024 kept it that way.

```text
expected sequence            → applied
last sequence, same record   → replayed, no re-aggregation
last sequence, different     → conflict
older, or a gap              → conflict
```

Durable state is two values per run: the next sequence and the digest of the
last accepted record. Task 053 made duplicates count twice on purpose, so
something had to own retry semantics; this does, in O(1), without consulting
any retained record.

**Saturation degrades rather than fails.** The 513th distinct behavior is
still applied to the aggregate; the snapshot stops being the whole truth and
reports `behavior_complete = false`. Later records keep advancing aggregate
evidence, and a comparison over incomplete evidence is refused rather than
manufactured.

**Comparison needs completed runs.** A running evaluation has mutable
evidence; a failed or cancelled one is not a completed evaluation.
`CompareEvaluations` loads both evidence pairs and derives the diff, scorecard
and gate result — once, in one place — and stores none of it.

See [ADR 0031](adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md).

### Realtime notification

Task 059 lets a consumer watch an evaluation live. It is **notification over
committed state**, never a second source of truth: the bus retains nothing
after delivery, and a subscriber that falls behind resynchronizes from the
control plane rather than replaying.

Seven kinds. Six correspond to a mutation that committed; the seventh says the
in-memory pipeline status document changed:

```text
evaluation_created   evaluation_started   observation
evaluation_completed evaluation_failed    evaluation_cancelled
status_changed
```

`status_changed` (task 105) carries only the identifier of the Collector whose
report moved — a reader re-reads `GET /v1/status`, which is the authority.

Deliberately absent: `policy_violation` (a blocked decision is the policy
engine working, and "violation" implies severity nothing models),
`baseline_update` (the platform does not own learned core state), and
`gate_update` (a gate result is a derived read under caller-supplied limits,
not durable state).

An **observation** is a bounded projection of one applied record — sequence,
record count, completeness, fingerprint, behavioral shape, fidelity, behavior
layer, decision, risk, approval, the three numeric signals (trust score,
anomaly score, anomaly confidence), and whether the behavior was new. Not the
record itself: no attributes, arguments, prompts, completions, contributors or
policy reason, so task 050's privacy boundary holds here too.

`NewBehavior` comes from the trusted pre-ingest snapshot. At saturation an
unseen behavior still reports `NewBehavior=true` with
`BehaviorComplete=false` — a live fact, even though the bounded snapshot
cannot retain it.

Every evaluation event carries its full immutable hierarchy, so a subscriber
filters by project, agent or run without reading the database. A
`status_changed` event has an empty scope and reaches only unfiltered
subscriptions.

See [ADR 0032](adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md).

See [ADR 0025](adr/0025-platform-domain-values-with-caller-owned-identity.md)
and [task 052](tasks/v1.0/052-evaluation-domain.md).

This document describes the domain model as it ships in `v0.12.0`. Planned
extensions to it are scoped in [ROADMAP.md](ROADMAP.md) and
[`tasks/`](tasks/) — each updates this document when it ships, not before.
