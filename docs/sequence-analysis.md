# Sequence Analysis

How Trustvian reasons about the *order* of an actor's behavior, not
just one event in isolation — the `v0.6` foundation
([task 025](archive/tasks/v0.6/025-sequence-analysis-foundation.md),
[ADR 0010](adr/0010-bounded-process-local-sequence-state.md)).

## The problem

Every anomaly signal through `v0.5` scores one event against its own
fingerprint's history:

```
event -> Features -> Fingerprint -> Baseline lookup -> Anomaly
```

None of them see what happened *immediately before* it. A canonical
example:

```
authenticate -> read_customer -> update_customer     (this actor's normal path)
authenticate -> read_customer -> export_customer -> delete_customer   (observed)
```

`export_customer` and `delete_customer` may each be individually
unremarkable — a `read -> export` transition might even be common
elsewhere in the fleet — but `read -> delete`, specifically, for
*this* actor, may never have happened. Order carries information the
existing signals structurally cannot see.

## What this task adds: `transition_deviation`

One new anomaly signal, integrated exactly like every other one
(`internal/anomaly.Signal`, combined via the existing noisy-OR
`combine()`, opt-in via a zero-default `Config.TransitionWeight`):

```
Anomaly.Score  (unchanged formula, one more Signal folded in)
```

`transition_deviation` answers exactly one question: **has this exact
predecessor fingerprint ever led to this destination fingerprint
before, for this actor?** Not "how often" (that is
[`transition_rarity`](#transition-rarity-v06-task-026)), not "how
surprising" (that is [`markov_surprisal`](#first-order-markov-surprisal-v06-task-028))
— just seen vs. never seen.
That is a deliberate scope boundary, not an oversight — see
[task 025's Non-Goals](archive/tasks/v0.6/025-sequence-analysis-foundation.md#non-goals).

## Where the state lives

There is no new `SequenceStore`. Two small additions to the existing
`internal/baseline.Baseline`/`FingerprintStats` types carry everything
this detector needs — see
[ADR 0010](adr/0010-bounded-process-local-sequence-state.md) for the
full reasoning behind reusing the existing `Baseline`/`Store`
infrastructure instead of building a parallel one:

```go
type Baseline struct {
    // ... existing fields ...
    LastFingerprintID   string    // this actor's most recent Fingerprint.ID
    LastFingerprintTime time.Time // its (ordering-guarded) timestamp
}

type FingerprintStats struct {
    // ... existing fields ...
    PredecessorCounts map[string]uint64 // bounded at 64 distinct entries
}
```

## Scope: `baseline.Key{Scope, ActorID, Environment}`

A transition is scoped to exactly the same key every other learned
signal in this codebase already uses — no new identity concept, no
mixing of unrelated actors' history. `svc-a`'s `read -> delete` and
`svc-b`'s `read -> delete` are tracked completely independently,
inherited for free from `internal/store`'s existing per-`Key` sharding.
`Scope` is the Engine's learning scope (`WithLearningScope`, default
`""`): two scopes keep separate sequence history for the same actor.

## Sequence length: one step

The first detector operates on `E(n-1) -> E(n)` only — the smallest
possible unit of order, and a deliberately narrow foundation (see
[task 025's Non-Goals](archive/tasks/v0.6/025-sequence-analysis-foundation.md#non-goals)).
The [3-gram detector](#bounded-3-gram-detection-v06-task-027) below
extends this by exactly one more fingerprint (`PreviousFingerprintID`)
without reversing it — the write path (`Baseline.Observe`) and read path
(`anomaly.Score`) shapes stay the same; only how much history is kept
grows.

## Event ordering

`Baseline.Observe` only records a transition, and only advances
`LastFingerprintID`/`Time`, when the incoming event's timestamp
**strictly follows** `LastFingerprintTime` — the identical guard
`FingerprintStats.observe` already applies to interval statistics, for
the identical reason: an out-of-order or backdated event (clock skew,
replay, a deliberately backdated event from an untrusted source) must
not be allowed to (a) be scored as following a predecessor it didn't
actually follow in real time, or (b) silently overwrite what the
*next* legitimate event's transition should be measured against. An
out-of-order event still updates its own `FingerprintStats` as usual
(`Count`, etc.) — it simply contributes no transition information in
either direction.

## Cold start

Two distinct "not enough history" cases, handled differently, matching
this codebase's existing cold-start philosophy (see
[ARCHITECTURE.md § Cold start](ARCHITECTURE.md#cold-start-two-numbers-not-one)):

- **No predecessor at all** (`LastFingerprintID == ""` — the actor's
  first-ever observation): `transition_deviation` does not fire at
  all. There is no transition to evaluate, so reporting one would be
  fabricating evidence, not observing it.
- **A predecessor exists, but this exact transition has never been
  seen**: `transition_deviation` fires at maximal value (`1`) — the
  same "genuinely novel scores as novel" philosophy
  `categorical_novelty` already applies to a never-before-seen
  fingerprint. This is **not** automatically treated as a critical
  attack: `TransitionWeight` defaults to `0`, so the signal is
  computed and reported (visible in `Anomaly.Contributors` for
  explainability) but contributes nothing to `Score` until an operator
  opts in — the identical mechanism `FrequencyWeight`/
  `TimePatternWeight` already use, and for the identical reason: a
  brand-new signal needs real traffic to calibrate against before its
  raw "novel" reading is a trustworthy basis for a decision.

## Resource bounds

`PredecessorCounts` is capped at 64 distinct predecessor entries per
destination fingerprint (`maxPredecessors` in `internal/baseline`).
Once at the bound, a genuinely new predecessor is not added; existing
entries keep accumulating normally — a deliberately simple "first 64
distinct predecessors win" policy, not LRU. See
[ADR 0010](adr/0010-bounded-process-local-sequence-state.md#why-bounded)
for the full reasoning and
[SECURITY.md § Sequence state](SECURITY.md#sequence-state) for the
threat model this closes.

## Activation

A Go SDK option, in-module:

```go
cfg := anomaly.DefaultConfig()
cfg.TransitionWeight = 0.7 // opt-in: defaults to 0

engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(cfg))
```

or, since [task 033](archive/tasks/v0.7/033-v07-stabilization-release-gate.md),
through public API/config — no `internal/anomaly` import required —
and the CLI's own `trustvian analyze/baseline build --anomaly-config
<path>` flag:

```go
cfg := config.AnomalyConfig{Version: config.AnomalySchemaVersionV1, TransitionWeight: 0.7}
ac, err := config.CompileAnomaly(cfg)
engine := trustvian.NewEngine(trustvian.WithAnomalyConfig(ac))
```

See [Anomaly Configuration Guide](anomaly-config-guide.md) for the
full field reference. The Collector processor has no anomaly
configuration block: its config accepts `policy`, `storage`, `health`,
`evaluation`, and `status`, so it scores with `anomaly.DefaultConfig()`,
where every sequence weight is `0`. That holds for every `v0.6`/`v0.7`
signal equally; it is not specific to this one.

## Transition rarity (`v0.6` task 026)

`transition_deviation` above answers seen vs. never seen. It cannot
distinguish a transition that happens 40% of the time from one that has
happened twice out of two thousand — both are simply "seen."
`transition_rarity` ([task 026](archive/tasks/v0.6/026-transition-rarity.md),
[ADR 0011](adr/0011-transition-rarity-statistic-and-orientation.md))
closes that gap with a graded measure, still without a Markov model:

```
frequency(A -> B) = PredecessorCounts_B[A] / OutgoingTransitionTotal_A
rarity(A -> B)     = 1 - frequency(A -> B)
```

- **The orientation question, answered up front.** `PredecessorCounts`
  (task 025) lives on the *destination* and naturally supports
  `P(predecessor | destination)` — not the question a transition
  detector actually needs, `P(destination | predecessor)`. Reading it
  as if it answered the second would be silently wrong. See [ADR 0011 §
  The orientation problem](adr/0011-transition-rarity-statistic-and-orientation.md#the-orientation-problem)
  for the full proof. The fix is one new scalar,
  `FingerprintStats.OutgoingTransitionTotal`, living on the
  *predecessor's* own stats — giving an O(1) computation of the correct
  statistic, no scan of `Baseline.Fingerprints` required.
- **Naming.** Called *frequency*/*rarity*, never *probability* — an
  unsmoothed empirical ratio, not a Markov model. The
  [Markov signal](#first-order-markov-surprisal-v06-task-028) below
  reads the same ratio on a different curve.
- **Minimum support.** `transition_rarity` only fires once
  `OutgoingTransitionTotal_A >= Config.MinTransitionObservations`
  (default `20`) — below that, a low frequency is evidence of too
  little data, not evidence of rarity, the identical cold-start
  philosophy `frequency_deviation` already applies to
  `IntervalObservations == 0`.
- **Mutually exclusive with `transition_deviation`, by construction.**
  `transition_deviation` fires exactly when `PredecessorCounts_B[A] ==
  0`; `transition_rarity` is only evaluated when that count is `> 0`. A
  transition is either never seen, or seen-and-possibly-rare — never
  both.
- **Activation** is the identical `WithAnomalyConfig` pattern:

  ```go
  cfg := anomaly.DefaultConfig()
  cfg.TransitionRarityWeight = 0.7 // opt-in: defaults to 0
  ```

## Bounded 3-gram detection (`v0.6` task 027)

`transition_deviation`/`transition_rarity` above compare one event
against its single immediate predecessor. That structurally cannot see
a real gap: `authenticate -> read_customer` may be normal, and
`read_customer -> export_customer` may *also* be normal, elsewhere —
yet the complete sequence `authenticate -> read_customer ->
export_customer`, as one continuous path, may never have happened for
this actor. `ngram_deviation`/`ngram_rarity`
([task 027](archive/tasks/v0.6/027-bounded-ngram-detection.md),
[ADR 0012](adr/0012-bounded-trigram-behavioral-context.md)) close that
gap with a fixed, bounded 3-gram — not a configurable `n`:

```
frequency(A, B -> C) = TrigramCounts_C[{A,B}] / TrigramContinuationTotal_B[A]
```

- **Minimal added history.** Exactly one more fingerprint of history
  than a transition needs: `Baseline` gains
  `PreviousFingerprintID` (the fingerprint two steps back), alongside
  the existing `LastFingerprintID`. No ring buffer, no raw `Event`
  retention.
- **The orientation question, answered again, one level up.** A 3-gram's
  denominator is "how many continuations has this specific *pair*
  `(A, B)` had," which a scalar (like task 026's own
  `OutgoingTransitionTotal`) cannot answer — a single fingerprint `B`
  participates in many distinct pairs. The fix:
  `TrigramContinuationTotal`, a *bounded map* keyed by grandparent `A`,
  living on `B`'s own stats. See
  [ADR 0012 § The orientation problem, generalized one level up](adr/0012-bounded-trigram-behavioral-context.md#the-orientation-problem-generalized-one-level-up).
- **Two independently-bounded maps, not one bound inherited from
  another.** `TrigramCounts` (on the destination) and
  `TrigramContinuationTotal` (on the immediate predecessor) each carry
  their own explicit `maxTrigramPredecessors` (64) cap — a genuine
  subtlety this task's own review caught: `PredecessorCounts`'s
  existing cap does *not* automatically bound either new map. See
  ADR 0012 for the full argument.
- **Cold start has three states, not two.** No predecessor at all
  (first-ever observation); a predecessor but no grandparent
  (second-ever observation — enough for a 2-gram, not a 3-gram); both
  present (third-ever observation onward — a complete 3-gram exists).
  `ngram_deviation`/`ngram_rarity` only evaluate in the third case.
- **Mutually exclusive with each other, and can co-fire with the
  pairwise signals.** `ngram_deviation`/`ngram_rarity` split on
  seen/unseen exactly like `transition_deviation`/`transition_rarity`
  do. When the final hop `B -> C` is itself unseen,
  `transition_deviation` and `ngram_deviation` fire together (a 3-gram
  cannot be familiar if its last transition never happened) —
  `combine()`'s noisy-OR is not redesigned for this; every contribution
  is already clamped before multiplying, so the combined score stays
  bounded regardless. See ADR 0012's own section on this.
- **Activation** is the identical `WithAnomalyConfig` pattern (in-module):

  ```go
  cfg := anomaly.DefaultConfig()
  cfg.NGramWeight = 0.7       // opt-in: defaults to 0
  cfg.NGramRarityWeight = 0.7 // opt-in: defaults to 0
  ```

  or, through public API/config (see [Anomaly Configuration
  Guide](anomaly-config-guide.md)):

  ```go
  cfg := config.AnomalyConfig{Version: config.AnomalySchemaVersionV1, NGramWeight: 0.7, NGramRarityWeight: 0.7}
  ac, err := config.CompileAnomaly(cfg)
  ```

## First-order Markov surprisal (`v0.6` task 028)

`markov_surprisal` ([task 028](archive/tasks/v0.6/028-markov-transition-scoring.md),
[ADR 0013](adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md))
reads the same evidence as `transition_rarity` — no new state — and
scores it as surprisal:

```
P(B|A)      = PredecessorCounts_B[A] / OutgoingTransitionTotal_A
S           = -log2(P(B|A))
normalized  = S / (S + k),   k = log2(max(MinTransitionObservations, 2))
```

- **Not new evidence.** Surprisal and `1 - frequency` are monotonic
  reparameterizations of one statistic. They never disagree on which
  of two transitions is rarer; only the curve differs. So when
  `MarkovWeight > 0`, `transition_rarity` is still reported in
  `Anomaly.Contributors`, but its weight is forced to `0` — the two
  cannot double-count, whatever weights are set.
- **No zero probability, no smoothing.** It shares
  `transition_rarity`'s gate: an unseen transition (`count == 0`) is
  `transition_deviation`'s case, so `P(B|A)` is always `> 0`.
- **Same minimum support.** It reuses `MinTransitionObservations`; no
  separate threshold exists.
- **First-order only.** The 3-gram signals above carry the
  two-step context.
- **Activation:** `MarkovWeight` (`markov_weight` in YAML), default
  `0`. See the [Anomaly Configuration Guide](anomaly-config-guide.md).
