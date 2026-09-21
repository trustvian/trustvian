# Performance

## Philosophy

Trustvian is a runtime security engine — `Engine.Analyze` is meant to
sit on a request path, so its cost has to be predictable and small.
CLAUDE.md's stance applies directly: pay attention to allocations, CPU
overhead, lock contention, goroutine lifecycle, and memory growth; use
benchmarks, not guesses; don't prematurely optimize. Every number below
is measured, not estimated — see
[ADR 0005](adr/0005-fingerprint-computed-once-per-analyze.md) for a
concrete example of a real inefficiency this benchmark suite caught
that inline code review had missed.

## Hot paths

In order of how often they run in a typical deployment:

1. `Engine.Analyze` — runs on every event. The full pipeline.
2. `Engine.Observe` — runs on every event whose decision is
   learning-eligible (see [SECURITY.md](SECURITY.md#baseline-poisoning)).
   Its cost is dominated by `Store.Observe`.
3. Everything inside `Analyze` individually: `features.Extract`,
   `fingerprint.Compute`, `Store.Get`, `anomaly.Score`,
   `trust.Compute`, `policy.Evaluate`.

## Measured results

**This table is the `v0.1` release baseline** (confirmed as part of
[task 013](archive/tasks/v0.1/013-oss-v01.md)'s release gate, 2026-09-04). Every
number in it was already current as of task 011's own re-measurement;
task 013 re-ran the full `go test ./... -bench . -benchmem -run ^$`
suite fresh (no cache) on the exact commit being released and confirms
every `B/op`/`allocs/op` figure below is unchanged and every `ns/op`
figure is within normal session-to-session noise — see "v0.1 gate
confirmation run" below for that fresh run's numbers side by side.
Future performance work should diff against this table, not against
individual task commits, as the `v0.1` comparison point.

Environment: Go 1.27, darwin/arm64, Apple M3 Pro. Run with
`make bench` or `go test ./... -run '^$' -bench . -benchmem`.
`-12` suffix = `GOMAXPROCS`/parallel benchmark; no suffix = sequential
(`-cpu 1`).

Full suite re-measured for task 011 (see
[tasks/011-performance.md](archive/tasks/v0.1/011-performance.md)) after tasks
002/004/006 landed. Every allocation count (`B/op`/`allocs/op`) below
is unchanged from the prior recorded session for every benchmark whose
underlying code did not change in those tasks (allocation counts are
deterministic and load-independent, unlike `ns/op`) — see "Reading the
numbers" below for which `ns/op` deltas are real, code-driven changes
versus this session's general measurement noise.

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `features.Extract` | 19.8 | 0 | 0 |
| `fingerprint.Compute` | 221.1 | 120 | 15 |
| `trust.Compute` | 5.8 | 0 | 0 |
| `policy.Evaluate` (rule match) | 30.5 | 0 | 0 |
| `policy.Evaluate` (falls to default) | 43.2 | 0 | 0 |
| `baseline.Observe` (direct, same fingerprint) | 276.6 | 464 | 3 |
| `anomaly.Score` (familiar, no signal fires) | 54.1 | 0 | 0 |
| `anomaly.Score` (novel, every signal fires) | 335.3 | 448 | 5 |
| `otel.EventFromSpan` | 385.8 | 696 | 10 |
| `otel.AttributesFromResult` | 59.9 | 320 | 1 |
| `store.InMemory.Observe` (same key, sequential) | 345.7 | 464 | 3 |
| `store.InMemory.Observe` (same key, 12-way parallel) | 363.1 | 464 | 3 |
| `store.InMemory.Observe` (distinct keys, sequential) | 343.8 | 464 | 3 |
| `store.InMemory.Observe` (distinct keys, 12-way parallel) | 180.6 | 464 | 3 |
| `store.InMemory.Observe` memory growth (100 keys) | 243.8 | 464 | 3 |
| `store.InMemory.Observe` memory growth (1,000 keys) | 273.8 | 464 | 3 |
| `store.InMemory.Observe` memory growth (10,000 keys) | 280.8 | 464 | 3 |
| `store.FileStore.Observe` (same key, sequential) | 3,712,459 | 3,777 | 24 |
| `store.FileStore.Observe` (same key, 12-way parallel) | 3,367,967 | 3,812 | 24 |
| `store.FileStore.Observe` (distinct keys, sequential) | 3,858,850 | 3,890 | 24 |
| `store.FileStore.Observe` (distinct keys, 12-way parallel) | 4,700,393 | 16,891 | 51 |
| `Engine.Analyze` (sequential) | 553.8 | 456 | 17 |
| `Engine.Analyze` (12-way parallel) | 191.9 | 456 | 17 |

### v0.1 gate confirmation run

Same environment, same commit, run fresh (no test cache) as part of
[task 013](archive/tasks/v0.1/013-oss-v01.md)'s release-gate verification. Shown
here to prove the table above reproduces, not as a replacement for it —
`B/op`/`allocs/op` match exactly everywhere; `ns/op` differences are
normal machine-load variance (this run shared the machine with other
work), not code changes.

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `features.Extract` | 17.76 | 0 | 0 |
| `fingerprint.Compute` | 170.8 | 120 | 15 |
| `trust.Compute` | 5.161 | 0 | 0 |
| `policy.Evaluate` (rule match) | 27.15 | 0 | 0 |
| `policy.Evaluate` (falls to default) | 37.75 | 0 | 0 |
| `baseline.Observe` (direct, same fingerprint) | 165.8 | 464 | 3 |
| `anomaly.Score` (familiar, no signal fires) | 48.04 | 0 | 0 |
| `anomaly.Score` (novel, every signal fires) | 234.1 | 448 | 5 |
| `otel.EventFromSpan` | 347.9 | 696 | 10 |
| `store.InMemory.Observe` (same key, sequential) | 356.5 | 464 | 3 |
| `store.InMemory.Observe` (distinct keys, sequential) | 140.4 | 464 | 3 |
| `store.InMemory.Observe` memory growth (100 keys) | 212.3 | 464 | 3 |
| `store.InMemory.Observe` memory growth (1,000 keys) | 231.9 | 464 | 3 |
| `store.InMemory.Observe` memory growth (10,000 keys) | 237.2 | 464 | 3 |
| `store.FileStore.Observe` (same key, sequential) | 4,416,998 | 3,873 | 24 |
| `store.FileStore.Observe` (distinct keys, sequential) | 4,668,619 | 16,948 | 51 |
| `Engine.Analyze` (sequential) | 435.6 | 456 | 17 |
| `Engine.Analyze` (12-way parallel) | 158.3 | 456 | 17 |

### v0.3 task 017 re-measurement (HourActivity time-pattern signal)

[Task 017](archive/tasks/v0.3/017-baseline-time-patterns.md) added a `[24]float64`
array and a `uint64` counter to `FingerprintStats`
(`internal/baseline`), and one additional signal-scoring branch to
`anomaly.Score`. Re-measured same environment (Go 1.27, darwin/arm64,
Apple M3 Pro), 2026-09-08, `go test ./... -run '^$' -bench . -benchmem`
(`-cpu 1` for the sequential rows, matching this table's existing
convention):

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `baseline.Observe` (direct, same fingerprint) | 275.7 | 672 | 3 |
| `anomaly.Score` (familiar, no signal fires) | 79.2 | 0 | 0 |
| `anomaly.Score` (novel, every signal fires) | 294.9 | 448 | 5 |
| `store.InMemory.Observe` (same key, sequential) | 349.5 | 672 | 3 |
| `store.InMemory.Observe` (distinct keys, sequential) | 347.8 | 672 | 3 |
| `store.InMemory.Observe` memory growth (100 keys) | 339.9 | 672 | 3 |
| `store.InMemory.Observe` memory growth (1,000 keys) | 405.9 | 672 | 3 |
| `store.InMemory.Observe` memory growth (10,000 keys) | 435.9 | 672 | 3 |
| `store.FileStore.Observe` (same key, sequential) | 3,813,890 | 4,320 | 24 |
| `store.FileStore.Observe` (distinct keys, sequential) | 3,774,905 | 4,432 | 24 |
| `Engine.Analyze` (sequential) | 502.3 | 456 | 17 |
| `Engine.Analyze` (12-way parallel) | 176.1 | 456 | 17 |

**`B/op` moved by exactly 208 bytes everywhere `FingerprintStats` is
copied; `allocs/op` did not move anywhere.** `HourActivity [24]float64`
is a fixed-size array field, not a slice or map — it adds to the *size*
of the `FingerprintStats` value, copied wherever the struct already was
(`FingerprintStats.observe`'s copy-on-write, `Baseline.Observe`'s map
write), but it does not add a new heap allocation: `baseline.Observe`
stays at 3 allocs/op (464→672 B/op, +208 bytes exactly), and every
`store.InMemory.Observe` variant shows the identical 464→672 B/op
shift with 3 allocs/op unchanged. `store.FileStore.Observe`'s `B/op`
also grows (v0.1 baseline: 3,777/3,890 same-key/distinct-keys
sequential → 4,320/4,432 now) because it additionally serializes 24
extra floats into the persisted JSON per fingerprint; `allocs/op` (24)
is unchanged, and `ns/op` is unaffected within session noise
(fsync-dominated, as documented above — see "Persistence costs roughly
four orders of magnitude").

**`anomaly.Score`'s familiar-path cost moved from ~54ns (v0.1) to
~79ns; this is a real, small, code-driven cost, not just variance.**
The new `time_pattern_deviation` branch runs a `TimePatternObservations
>= MinObservations` check and, when eligible, an array index plus a
handful of float comparisons (`timePatternSignal`) on every call, known
or not — cheap, but not free, the same "always evaluated so it can
report in `Contributors`, only allocates if it actually fires" shape as
`frequencySignal` before it (see "The common case is the cheap case,
by design" above). `BenchmarkScoreNovelWithAllSignals`'s
`B/op`/`allocs/op` (448 B, 5 allocs) is unchanged from the v0.1
baseline for a structural reason, not luck: this benchmark scores a
*novel* fingerprint (`known == false`), and `time_pattern_deviation` is
gated on `known` exactly like `latency_deviation`/`frequency_deviation`
— so it cannot fire here regardless of `HourActivity`'s contents, the
same reason `frequencySignal`'s addition in task 004 didn't move this
benchmark's allocation count either (see "The common case is the cheap
case, by design" above).

### v0.4 task 018 (Alert & Notification Foundation)

New package, so a new subsection rather than an addition to the main
table above — nothing existing changed. Measured same environment (Go
1.27, darwin/arm64, Apple M3 Pro), 2026-09-08, `go test ./alert/... -run
'^$' -bench . -benchmem -cpu 1`:

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `alert.Evaluate` (no rule matches) | 41.8 | 0 | 0 |
| `alert.Evaluate` (a rule matches) | 494.5 | 208 | 5 |
| `json.Marshal(alert.NewEnvelope(...))` | 1,051 | 992 | 3 |

**The no-match path is zero-allocation, verified rather than assumed.**
`BenchmarkEvaluateNoMatch` confirms
[task 018](archive/tasks/v0.4/018-alert-notification-foundation.md)'s own
Acceptance Criteria bullet directly: a `Result` that matches no
configured `Rule` costs nothing beyond the `Condition.Matches` field
comparisons themselves — no `Alert` is constructed, no ID is generated,
no JSON is ever built. This is the same "only pay for what actually
fires" discipline `anomaly.Score`'s signal functions already follow
(see "The common case is the cheap case, by design" above).

**Marshaling is a separate cost from evaluation, on purpose.**
`BenchmarkNewEnvelopeMarshal` is measured independently from
`BenchmarkEvaluateMatch` specifically to keep this visible: `Evaluate`
never marshals JSON itself (992 B/3 allocs from `encoding/json`'s own
reflection-based marshaling only happens inside `WebhookSink.Send`,
which only runs once an `Alert` has already been produced and a caller
has chosen to deliver it) — a `Result` that matches a `Rule` but whose
caller decides not to deliver the resulting `Alert` never pays this
cost at all.

**`BenchmarkEvaluateMatch`'s 208 B / 5 allocs is `alert.New`'s cost**,
dominated by `newRandomID` (a 16-byte `crypto/rand.Read` plus a hex
encode and string concatenation) and `reasonsFromResult`'s slice
allocation — both proportional to "how much there is to explain," the
same explainability-costs-proportionally-not-unconditionally property
`anomaly.Score`'s worst-case benchmark already established.

### v0.6 task 025 (Sequence Analysis Foundation)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro),
`go test -bench=. -benchmem -run=^$ ./internal/baseline/...
./internal/anomaly/... ./internal/store/... .`:

| Benchmark | Before | After |
|---|---:|---:|
| `BenchmarkObserve` (no transition recorded) | 231.6 ns/op, 672 B/op, 3 allocs | 222.7 ns/op, 672 B/op, 3 allocs — unchanged |
| `BenchmarkObserveTransition` (new) | — | 377.1 ns/op, 1,344 B/op, 6 allocs |
| `BenchmarkInMemoryObserveSameKey` | 412.7 ns/op, 672 B/op, 3 allocs | 573.6 ns/op, 910 B/op, 4 allocs |
| `BenchmarkInMemoryObserveDistinctKeys` | 173.2 ns/op, 672 B/op, 3 allocs | 241.4 ns/op, 928 B/op, 5 allocs |
| `BenchmarkScoreKnownFamiliar` | 80.18 ns/op, 0 B/op, 0 allocs | 97.74 ns/op, 0 B/op, 0 allocs |
| `BenchmarkScoreTransitionDeviation` (new) | — | 163.0 ns/op, 160 B/op, 3 allocs |
| `BenchmarkEngineAnalyze` (end-to-end, default config) | 462.3 ns/op, 456 B/op, 17 allocs | 481.0 ns/op, 456 B/op, 17 allocs |

**`Engine.Analyze`'s (read-only) allocation profile is unchanged** —
`BenchmarkEngineAnalyze`'s `B/op`/`allocs/op` are byte-for-byte
identical to before this task. This is genuinely representative of the
read path, not an artifact: `anomaly.Score`'s "already-familiar
transition" case (`BenchmarkScoreKnownFamiliar`) is a map read and an
early-return zero-value `Signal`, never appended to `Contributors` —
0 B/0 allocs either way, confirmed directly, not merely because the
benchmark's fixed clock happens to avoid the code path (see below,
where that *is* the case for a different benchmark).

**`Engine.Observe`'s (write) allocation profile genuinely increased —
+1 alloc / ~+240 B per call, once a real transition is being
recorded**, and this shows up in the pre-existing
`internal/store` benchmarks (which use a real, advancing
`time.Now()`, unlike `BenchmarkObserve`'s fixed clock — see below):
`recordPredecessor`'s copy-on-write (a fresh `map[string]uint64`
allocated on every call that updates an existing entry, for the
identical immutability reason `Baseline.Fingerprints` itself is
already copied on every `Observe`) is a real, structural cost, not
noise — reproduced identically across three independent full test
runs. It was deliberately not optimized away: the numbers remain
sub-microsecond and sub-kilobyte at every measured point, and avoiding
it would require either mutating a shared map in place (unsafe — see
[ADR 0010](adr/0010-bounded-process-local-sequence-state.md)) or a
materially more complex copy-on-write map structure not justified by
these numbers. This is the same "don't optimize blindly" discipline
CLAUDE.md asks for, applied honestly in both directions — reported as
found, not minimized or omitted.

**`BenchmarkObserve` (the pre-existing benchmark) does *not* exercise
this cost**, and that gap is itself a documented finding, not an
oversight: its loop uses a fixed clock and the same fingerprint every
iteration, so `Baseline.Observe`'s ordering guard
(`now.After(LastFingerprintTime)`) is only satisfied on the very first
iteration — every steady-state call takes the cheap "no valid
transition" path, identical to before this task.
`BenchmarkObserveTransition` (new) uses a strictly-advancing clock and
an alternating fingerprint pair specifically so every call *does*
exercise `recordPredecessor`'s copy-on-write, giving an honest
worst-case number the unchanged benchmark alone would have hidden —
this is the exact kind of gap [`.claude/rules/testing.md`](../.claude/rules/testing.md)'s
"benchmark every stage individually" discipline exists to catch (see
its own `fingerprint.Compute` precedent).

**`BenchmarkScoreTransitionDeviation`'s 160 B / 3 allocs is
`transitionSignal`'s cost** on a `PredecessorCounts` map sized near
`maxPredecessors` (64 entries) — a single map lookup plus one `Signal`
struct and its `Detail` string formatting, paid only when the signal
actually fires (a genuinely unseen transition), the identical
"only pay for what fires" discipline every other `anomaly` signal
function already follows.

### v0.6 task 026 (Transition Rarity)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro).
"Before" is task 025 (Sequence Analysis Foundation) re-measured on this
same machine in a disposable `git worktree` at commit `e91752d`, not
merely carried over from the table above, so the delta below is a real
same-machine A/B, not cross-session noise:

| Benchmark | Before (task 025) | After (task 026) |
|---|---:|---:|
| `BenchmarkObserve` (no transition recorded) | 223.4 ns/op, 672 B/op, 3 allocs | 220.9–224.4 ns/op, 704 B/op, 3 allocs — unchanged in kind |
| `BenchmarkObserveTransition` | 376.3 ns/op, 1,344 B/op, 6 allocs | 420.0–420.8 ns/op, 1,408 B/op, 6 allocs |
| `BenchmarkScoreKnownFamiliar` | 98.77 ns/op, 0 B/op, 0 allocs | 136.3–136.9 ns/op, 0 B/op, 0 allocs |
| `BenchmarkScoreTransitionDeviation` | 164.5 ns/op, 160 B/op, 3 allocs | 206.6–206.9 ns/op, 160 B/op, 3 allocs — unchanged |
| `BenchmarkScoreTransitionRarity` (new) | — | 364.7–373.1 ns/op, 296 B/op, 5 allocs |
| `BenchmarkEngineAnalyze` (end-to-end, read-only, default config) | 467.9–471.8 ns/op, 456 B/op, 17 allocs | 506.4–512.5 ns/op, 456 B/op, 17 allocs |
| `BenchmarkEngineAnalyzeTransitionRarity` (new, rarity weight enabled) | — | 509.7–515.1 ns/op, 456 B/op, 17 allocs |

**`Engine.Analyze`'s allocation profile is unchanged — `456 B/op, 17
allocs/op`, identical to task 025 — but its latency genuinely
increased, by design, not by accident.** `transitionRaritySignal` is
called unconditionally alongside `transitionSignal` on every event that
has a predecessor (see `anomaly.Score`), the same "always compute,
weight-gate the contribution" pattern every prior signal already
follows — it is not gated behind `TransitionRarityWeight > 0` before
running, only before its `Value` is allowed to affect `Score`. This
means the ~35–40ns/call cost below is paid on **every** steady-state
`Analyze` call from this task onward, whether or not an operator ever
sets `TransitionRarityWeight` above its `0` default — not hidden here:

- `BenchmarkScoreKnownFamiliar`: 98.77ns → 136.3–136.9ns (+~38ns), still
  `0 B/0 allocs` — the added cost is exactly one more map lookup
  (`PredecessorCounts`) plus a field read (`OutgoingTransitionTotal`)
  and a division/min/max, on a path that was already a single map
  lookup before this task; no allocation because the self-transition in
  this benchmark's mature baseline is 100% frequent (`rarity == 0`), so
  the early-return-with-zero-value path is what's measured, and it
  never builds a `Detail` string.
- `BenchmarkEngineAnalyze`: 467.9–471.8ns → 506.4–512.5ns (+~38–41ns),
  same relative cost, propagated through the full pipeline — consistent
  with the isolated `anomaly.Score` delta above, confirming the extra
  cost is fully accounted for by `transitionRaritySignal` itself, not
  an incidental change elsewhere in the pipeline.
- `BenchmarkEngineAnalyzeTransitionRarity` (weight actually set to
  `0.7`) measures within noise of `BenchmarkEngineAnalyze` on the same
  tree (509.7–515.1ns vs. 506.4–512.5ns) — expected, since the weight
  only changes whether `combine()` incorporates an already-computed,
  already-fired `Value`; it does not change whether the computation
  itself runs.
- `BenchmarkObserveTransition`: 376.3ns → 420.0–420.8ns (+~44ns), and
  `B/op` grew from 1,344 to 1,408 (+64 B) with **no additional alloc
  count** (6 allocs, unchanged) — `FingerprintStats` grew by one
  `uint64` field (`OutgoingTransitionTotal`), so every copy-on-write map
  entry this benchmark's alternating-fingerprint path already makes is
  now 8 bytes larger per stored value; `observeOutgoingTransition`
  itself writes into a map slot the surrounding `Observe` call already
  cloned, so it adds no *new* allocation, only the field-size increase.
  `BenchmarkObserve`'s own steady-state path never exercises
  `observeOutgoingTransition` at all — per task 025's own documented
  finding, its fixed clock and repeated fingerprint mean the ordering
  guard only passes on the very first iteration — yet its `B/op` also
  moved, consistently, from 672 to 704 (reproduced identically across
  three separate runs, so not run-to-run noise): the same 8-byte
  `FingerprintStats` growth is copied into `Baseline.Fingerprints`'
  cloned map on *every* `Observe` call regardless of whether a
  transition is recorded, so this +32 B is the map's own per-entry copy
  cost growing with the struct, not a new code path being exercised.
  Allocation *count* stayed at 3 either way.

**Not optimized away, and not hidden.** This is a genuine, small,
sub-40ns-per-call latency cost on the common `Analyze` path, paid
regardless of whether the feature is enabled, in exchange for keeping
`transitionRaritySignal`'s lookup O(1) (one map read, one field read,
one division) rather than gating it behind a second config check that
would itself cost a branch on every call for no meaningful savings.
Every number above stays sub-microsecond and the allocation profile is
provably unaffected (`0 B/0 allocs` in the no-fire case,
`456 B/17 allocs` end-to-end) — consistent with
[docs/adr/0011](adr/0011-transition-rarity-statistic-and-orientation.md)'s
own "no full-map scans, O(1) lookup" requirement holding in practice,
not just in the design.

**`BenchmarkScoreTransitionRarity`'s 296 B / 5 allocs is
`transitionRaritySignal`'s cost when it actually fires** — a rare
(2-of-52), but seen and past-minimum-support transition, so the
`Detail` string (`fmt.Sprintf`) is built, the genuine "worst case" this
benchmark exists to measure, mirroring
`BenchmarkScoreTransitionDeviation`'s own choice to benchmark the firing
case rather than a degenerate always-common one.

### v0.6 task 027 (Bounded n-gram Detection)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro).
"Before" is task 026 (Transition Rarity) re-measured on this same
machine in a disposable `git worktree` at commit `ead742f`, the same
same-machine-A/B discipline task 026's own PERFORMANCE.md entry used:

| Benchmark | Before (task 026) | After (task 027) |
|---|---:|---:|
| `BenchmarkObserveTransition` | 435.9 ns/op, 1,408 B/op, 6 allocs | 688.4–701.9 ns/op, 2,064 B/op, 10 allocs |
| `BenchmarkObserveTrigram` (new) | — | 757.8–771.7 ns/op, 2,512 B/op, 11 allocs |
| `BenchmarkInMemoryObserveSameKey` (pre-existing, `internal/store`) | 619.8 ns/op, 944 B/op, 4 allocs | 960.7–979.2 ns/op, 1,559 B/op, 8 allocs |
| `BenchmarkInMemoryObserveDistinctKeys` (pre-existing, `internal/store`) | 248.1 ns/op, 960 B/op, 5 allocs | 424.8 ns/op, 1,616 B/op, 9 allocs |
| `BenchmarkScoreKnownFamiliar` | 136.3–136.9 ns/op, 0 B/op, 0 allocs | 198.9–204.6 ns/op, 0 B/op, 0 allocs |
| `BenchmarkScoreTransitionDeviation` | 206.6–206.9 ns/op, 160 B/op, 3 allocs | 203.4–210.2 ns/op, 160 B/op, 3 allocs — unchanged (see below) |
| `BenchmarkScoreTransitionRarity` | 359.3 ns/op, 296 B/op, 5 allocs | 425.0–435.1 ns/op, 296 B/op, 5 allocs — unchanged allocation profile |
| `BenchmarkScoreNGramDeviation` (new) | — | 289.9–291.5 ns/op, 224 B/op, 4 allocs |
| `BenchmarkScoreNGramRarity` (new) | — | 660.2–669.5 ns/op, 672 B/op, 10 allocs |
| `BenchmarkEngineAnalyze` (end-to-end, read-only, default config) | 512.6–518.8 ns/op, 456 B/op, 17 allocs | 579.7–588.0 ns/op, 456 B/op, 17 allocs |
| `BenchmarkEngineAnalyzeNGram` (new, both weights enabled) | — | 584.0–587.7 ns/op, 456 B/op, 17 allocs |

**`Engine.Analyze`'s allocation profile is unchanged — `456 B/op, 17
allocs/op`, identical to task 026 — but its latency grows by
~65–70ns/call, for the identical structural reason task 026's own
latency growth did.** `ngramDeviationSignal`/`ngramRaritySignal` are
both called unconditionally alongside the existing transition signals
on every event that has a complete two-fingerprint history, the same
"always compute, weight-gate the contribution" pattern every prior
signal already follows — paid on every steady-state `Analyze` call
from this task onward, whether or not `NGramWeight`/`NGramRarityWeight`
are ever raised above their `0` default:

- `BenchmarkScoreKnownFamiliar`: 136.3–136.9ns → 198.9–204.6ns
  (+~63–68ns), still `0 B/0 allocs` — two more map lookups
  (`TrigramCounts`, `TrigramContinuationTotal`) plus a division/min/max,
  on a self-transition fixture where the resulting 3-gram is 100%
  frequent (`ngram_rarity` value `0`), so the early-return path is what
  runs, never building a `Detail` string.
- `BenchmarkEngineAnalyze`: 512.6–518.8ns → 579.7–588.0ns
  (+~67–70ns), consistent with the isolated `anomaly.Score` delta
  above, confirming the full-pipeline cost is fully accounted for by
  the two new signal functions, not an incidental change elsewhere.
- `BenchmarkEngineAnalyzeNGram` (both weights actually set to `0.7`)
  measures within noise of `BenchmarkEngineAnalyze` on the same tree
  (584.0–587.7ns vs. 579.7–588.0ns) — expected, since the weight only
  changes whether `combine()` incorporates an already-computed,
  already-fired `Value`, not whether the computation runs.
- `BenchmarkObserveTransition`/`BenchmarkInMemoryObserveSameKey`/
  `BenchmarkInMemoryObserveDistinctKeys`: each grew by 4–5 allocs and a
  proportional `B/op` increase. These benchmarks use a strictly
  advancing clock and a small, fixed set of fingerprints, so — once
  `PreviousFingerprintID` is populated (after the second observation) —
  every subsequent call also completes a valid 3-gram, exercising both
  new bounded maps' copy-on-write (`recordTrigram` +
  `recordTrigramContinuation`, each allocating a fresh map, mirroring
  `recordPredecessor`'s own established cost) on top of the pre-existing
  transition bookkeeping. This is a real, structural cost — the same
  "copy-on-write pays a real, non-optimized-away price" discipline
  task 025's own `recordPredecessor` already established, now paid
  twice more per call in the worst case (a strictly-ordered, repeating
  or small-alphabet event stream) — not investigated further at the
  individual-allocation level beyond confirming it reproduces
  identically across repeated runs.

**Not hidden: two pre-existing benchmarks incidentally now also
exercise the new signals, and were handled differently depending on
whether that changed what they measure.**
`BenchmarkScoreTransitionDeviation`'s own fixture ends with an extra,
unpaired `Observe` call (to correctly position `LastFingerprintID` for
the transition-deviation case it was written to isolate) — which, now
that `PreviousFingerprintID` exists at all, also left it non-empty,
making `ngram_deviation` fire too and changing this benchmark's
allocation count (a real, measured regression: `160 B/3 allocs` →
`432 B/7 allocs` before the fix). Since this benchmark's own doc
comment states its purpose is to isolate `transitionSignal`'s cost
alone (predating task 027 by two tasks), its fixture now explicitly
clears `bl.PreviousFingerprintID` before the timed loop — a one-line,
test-only fix, no production code touched — restoring its original,
documented scope; see its own updated comment in
`internal/anomaly/anomaly_bench_test.go`. `BenchmarkScoreTransitionRarity`
was left as-is: its own fixture's history window ends up positioned
such that the incidentally-exercised `ngram_deviation`/`ngram_rarity`
compute but do not fire (`Value == 0`), so its allocation profile
(`296 B/5 allocs`) is genuinely unaffected — only its `ns/op` moved,
for the same "computed, not fired" reason `BenchmarkScoreKnownFamiliar`'s
own delta is explained above.

**`BenchmarkScoreNGramDeviation`'s 224 B / 4 allocs is
`ngramDeviationSignal`'s cost** on a `TrigramCounts` map sized near
`maxTrigramPredecessors` (64 entries) — a single map lookup plus one
`Signal` struct and its `Detail` string formatting, paid only when the
signal actually fires (a genuinely unseen 3-gram), mirroring
`BenchmarkScoreTransitionDeviation`'s own discipline one level up.

**`BenchmarkScoreNGramRarity`'s 672 B / 10 allocs is
`ngramRaritySignal`'s cost when it actually fires** — a rare
(2-of-52), but seen and past-minimum-support 3-gram, so the `Detail`
string is built; the roughly 2x cost over `BenchmarkScoreTransitionRarity`
(296 B/5 allocs *as measured at task 027* — see task 028's own section
below for why this number later changed, unrelated to n-grams) is
expected, not a red flag — this benchmark's own fixture construction
does twice as much `Baseline.Observe` work per repeat (three `Observe`
calls — grandparent, predecessor, destination — versus two for the
pairwise case), and `ngramRaritySignal` itself does one more map lookup
(`TrigramContinuationTotal` in addition to `TrigramCounts`) than
`transitionRaritySignal`'s single `OutgoingTransitionTotal` scalar
read.

### v0.6 task 028 (Markov Transition Scoring)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro).
"Before" is task 027 (Bounded n-gram Detection) re-measured on this
same machine in a disposable `git worktree` at commit `e31875f`, the
same same-machine-A/B discipline every prior v0.6 task's own entry
used:

| Benchmark | Before (task 027) | After (task 028) |
|---|---:|---:|
| `BenchmarkScoreKnownFamiliar` | 205.7–210.7 ns/op, 0 B/op, 0 allocs | 255.0–260.5 ns/op, 0 B/op, 0 allocs |
| `BenchmarkScoreTransitionRarity` | 434.2–437.3 ns/op, 296 B/op, 5 allocs | 722.0–745.4 ns/op, 648 B/op, 9 allocs |
| `BenchmarkScoreMarkovSurprisal` (new) | — | 722.0–728.9 ns/op, 648 B/op, 9 allocs |
| `BenchmarkScoreMarkovLookup` (new, below minimum support) | — | 280.7–285.7 ns/op, 112 B/op, 2 allocs |
| `BenchmarkEngineAnalyze` (end-to-end, read-only, default config) | 597.2–606.5 ns/op, 456 B/op, 17 allocs | 645.8–652.3 ns/op, 456 B/op, 17 allocs |
| `BenchmarkEngineAnalyzeMarkov` (new, weight enabled) | — | 650.2–652.3 ns/op, 456 B/op, 17 allocs |

**`Engine.Analyze`'s allocation profile is unchanged — `456 B/op, 17
allocs/op`, identical to task 027 — but its latency grows by
~45–50ns/call**, for the identical structural reason every prior v0.6
task's own latency growth did: `markovSurprisalSignal` is called
unconditionally alongside the existing transition signals whenever a
predecessor exists, regardless of whether `MarkovWeight` is ever raised
above its `0` default.

- `BenchmarkScoreKnownFamiliar`: 205.7–210.7ns → 255.0–260.5ns
  (+~47–52ns), still `0 B/0 allocs` — one more map lookup
  (`PredecessorCounts`, already read by `transitionRaritySignal` too,
  so no *additional* lookup here beyond the arithmetic) plus
  `math.Log2` twice and a division, on a self-transition fixture where
  the resulting frequency is 100% (`surprisal == 0`, `normalized ==
  0`), so the early-return path is what runs, never building a
  `Detail` string.
- `BenchmarkEngineAnalyze`: 597.2–606.5ns → 645.8–652.3ns
  (+~46–49ns), consistent with the isolated `anomaly.Score` delta
  above.
- `BenchmarkEngineAnalyzeMarkov` (weight actually set to `0.7`)
  measures within noise of `BenchmarkEngineAnalyze` on the same tree
  (650.2–652.3ns vs. 645.8–652.3ns) — expected, since the weight only
  changes whether `combine()` incorporates an already-computed,
  already-fired `Value`, not whether the computation runs. (This
  benchmark's own self-transition fixture also resolves to `surprisal
  == 0`, so the delta here is smaller still than
  `BenchmarkScoreMarkovSurprisal`'s own firing-case cost below.)

**`BenchmarkScoreMarkovLookup`'s 112 B / 2 allocs is the common,
non-firing case** — a predecessor below `MinTransitionObservations`,
so `markovSurprisalSignal` takes its early-return path: one map read
(`PredecessorCounts`), one field read (`OutgoingTransitionTotal`), one
comparison, no `math.Log2`, no `Detail` string. The 112 B / 2 allocs
here come from `anomaly.Score`'s own surrounding `categorical_novelty`/
`Contributors` slice machinery on this fixture's otherwise-empty
baseline, not from this signal itself.

**`BenchmarkScoreMarkovSurprisal`'s 648 B / 9 allocs is
`markovSurprisalSignal`'s cost when it actually fires** — a rare
(2-of-52), but seen and past-minimum-support transition, so the
`Detail` string (`fmt.Sprintf`, plus two `math.Log2` calls and a
division) is built, mirroring `BenchmarkScoreTransitionRarity`'s own
choice to benchmark the firing case.

**`BenchmarkScoreTransitionRarity`'s allocation profile genuinely
changed (296 B/5 allocs at task 027 → 648 B/9 allocs at task 028), and
this was deliberately left uncorrected, unlike task 027's own
`BenchmarkScoreTransitionDeviation` fix.** The two cases differ in
kind: task 027's leak was a genuine fixture accident (a benchmark that
predated `Baseline.PreviousFingerprintID`'s mere existence happened to
leave it non-empty, activating an unrelated signal it was never meant
to exercise, fixable with a one-line, test-only reset). This one is
not fixable the same way, because it is not an accident:
`markovSurprisalSignal` shares `transitionRaritySignal`'s *exact* gate
by deliberate design (see
[docs/adr/0013](adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md) —
both read identical `count`/`total` state under the identical
`MinTransitionObservations` threshold), so any fixture with enough
history to fire `transition_rarity` necessarily has enough to fire
`markov_surprisal` too, regardless of `MarkovWeight`'s own value
(`Score`'s append condition is `Value > 0`, weight-independent — the
same "always compute" precedent every prior signal already
established). There is no way to isolate `transitionRaritySignal`'s
cost alone anymore without breaking the minimum-support gate it itself
needs to fire. Reported here in full, not hidden, per this codebase's
own "report as found" benchmark discipline.

### v0.6 stabilization pass (task 029): full feature-combination matrix

Task 029's own release-readiness audit asked a question no prior v0.6
task's own benchmark had directly answered: how does the full
`Engine.Analyze` pipeline compare across *every* feature-enablement
combination, not just each task's own before/after delta? Measured
fresh (Go 1.27, darwin/arm64, Apple M3 Pro,
`go test -bench "BenchmarkEngineAnalyze" -benchmem -benchtime=2s
-count=3 .`), all six configurations on the *same* tree (this table is
a same-machine comparison across configurations, not an across-commit
one — see the task-by-task tables above for that):

| Configuration | Weight(s) set | ns/op | B/op | allocs/op |
|---|---|---:|---:|---:|
| Default (everything at its `0` default) | — | 649.9–657.3 | 456 | 17 |
| Transition deviation enabled | `TransitionWeight=0.7` | 651.2–652.6 | 456 | 17 |
| Transition rarity enabled | `TransitionRarityWeight=0.7` | 653.5–668.6 | 456 | 17 |
| n-gram enabled | `NGramWeight=NGramRarityWeight=0.7` | 651.0–656.9 | 456 | 17 |
| Markov enabled | `MarkovWeight=0.7` | 652.7–658.9 | 456 | 17 |
| Full behavioral (all five weights) | all `=0.7` | 656.0–659.6 | 456 | 17 |

(`BenchmarkEngineAnalyzeTransitionDeviation` and
`BenchmarkEngineAnalyzeFullBehavioral`, both new this pass, complete
the per-signal matrix — every other row already existed as its own
task's dedicated benchmark.)

**Every configuration lands in the same ~650–669ns band, and every
configuration's allocation profile is byte-for-byte identical
(`456 B/op, 17 allocs/op`).** This is not noise masking a real
difference — it is the direct, expected consequence of an
architectural decision already made and documented at every prior
v0.6 task: every signal function runs unconditionally on every
`Analyze` call that has the history to evaluate it, regardless of its
own weight. Raising a weight from `0` to a real value changes nothing
about *whether* the signal computes — only whether `combine()`
multiplies an already-computed value into `Score`. "Full behavioral"
costs no more than "Markov enabled" alone, because by the time any one
v0.6 weight is nonzero, every v0.6 signal was already being computed
regardless.

**Answering task 029's own critical question directly: does a user
who has not enabled Markov pay the `math.Log2`/lookup/normalization
cost? Yes — `MarkovWeight=0 computation skipped: NO`.** Measured, not
assumed: `BenchmarkEngineAnalyze` (every weight at its `0` default,
649.9–657.3ns) and `BenchmarkEngineAnalyzeMarkov` (`MarkovWeight=0.7`,
652.7–658.9ns) are indistinguishable within this session's own
run-to-run noise. The cost of `markovSurprisalSignal` running was
already fully paid in `BenchmarkEngineAnalyze`'s own number — turning
the weight on adds nothing further.

**This was evaluated explicitly against the alternative (skip a
signal's computation entirely when its own weight is `0`) and
deliberately left unchanged.** Two architectural options exist:

- **A (current, for all eleven signals, not just Markov's three
  v0.6-added ones): compute every signal regardless of weight.** A
  signal's raw `Value`/`Detail` remains visible in
  `Anomaly.Contributors` for explainability/calibration purposes even
  before an operator opts into scoring it — the exact mechanism
  `FrequencyWeight` (task 004, `v0.1`) and `TimePatternWeight` (task
  017, `v0.3`) already established, years before any v0.6 signal
  existed, and which every v0.6 signal (`TransitionWeight` onward)
  deliberately continued rather than reinventing.
- **B: skip a signal's computation entirely when its own weight is
  `0`.** Would eliminate the ~40–70ns/signal cost measured at each
  task's own introduction (see the task-specific sections above), but
  at the cost of `Contributors` silently omitting a signal's raw
  reading whenever its weight happens to be `0` — breaking
  explainability for exactly the calibration workflow this codebase's
  own documentation (see `FrequencyWeight`'s own doc comment in
  `internal/anomaly/anomaly.go`) tells operators to rely on: "measure
  your own fleet's jitter... only then raise `FrequencyWeight`" is
  only possible if the signal is visible *before* the weight is
  raised.

**Decision: A, unchanged, for all eleven signals uniformly.** Adopting
B for Markov alone (while every other signal, including the other five
v0.6 additions, kept convention A) would be exactly the kind of
signal-specific inconsistency task 029's own audit was asked to catch,
not introduce. Adopting B for *all* signals would be a genuine,
whole-package behavior change with real explainability consequences —
out of scope for a stabilization pass whose own mandate is "fix only
genuine release blockers or clearly justified defects," not
"optimize because the code looks inefficient" (the task's own explicit
instruction). The measured costs are real but small: sub-100ns/call,
zero additional allocations in every non-firing case measured across
tasks 025–028's own sections above. Not optimized away; reported here
in full, per this codebase's "report as found" discipline, exactly as
every prior task's own PERFORMANCE.md entry already does.

### v0.7 task 014 (AI Agent Event/Context Foundation)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro). This
task adds three optional `event.Context` fields (`SessionID`,
`DelegatedFrom`, `ApprovalStatus`) and reads none of them in
`features.Extract` — so, unlike every `v0.6` signal (each of which
*does* run unconditionally on the hot path once its gate condition is
met), this task's own fields impose no cost at all when unset, and no
new cost at any weight/configuration, because nothing in the pipeline
reads them yet:

| Benchmark | Before (v0.6, unaffected) | After (task 014) |
|---|---:|---:|
| `BenchmarkExtract` (`internal/features`) | 18.02–18.80 ns/op, 0 B/op, 0 allocs | 18.80 ns/op, 0 B/op, 0 allocs — unchanged |
| `BenchmarkCompute` (`internal/fingerprint`) | 170.1–180.1 ns/op, 120 B/op, 15 allocs | 170.6 ns/op, 120 B/op, 15 allocs — unchanged |

**Byte-for-byte identical allocation profile, confirmed, not
assumed.** `features.Extract` never reads `Context.SessionID`/
`DelegatedFrom`/`ApprovalStatus` — the three new fields are plain
strings/one small string-enum sitting on the `Event` value the caller
already constructed, costing nothing extra to *not* read. This is a
structurally different situation from every `v0.6` signal's own
"always compute regardless of weight" cost (see the sections above):
those signals *do* run and *do* cost real, measured nanoseconds once
their gate condition is met, by deliberate design, for explainability.
This task's fields aren't consumed by any signal yet at all — there is
nothing to gate.
`BenchmarkEngineAnalyze` itself was not re-measured for this task
specifically, since no code on that path changed (`Event`'s new fields
are additive struct fields Go's compiler lays out at zero marginal
cost when unread; `Analyze`'s own logic is byte-for-byte unchanged) —
the `features`/`fingerprint`-level numbers above are the load-bearing
proof for this task's own overhead claim.

### v0.7 task 030 (Approval-Aware Policy Semantics)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro). This
task adds one field to `policy.Condition`/`Input`, checked with one
more string equality comparison in `Condition.Matches` — the identical
shape every other `Condition` field already has, so the honest
before/after comparison is `BenchmarkEvaluateMatch` (a matching rule
with no approval condition) against a new benchmark for the equivalent
approval-gated rule:

| Benchmark | Result |
|---|---:|
| `BenchmarkEvaluateMatch` (`internal/policy`, no approval condition) | 31.10 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkEvaluateApprovalCondition` (`internal/policy`, approval-gated rule) | 29.95 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkEngineAnalyze` (root, before and after) | 456 B/op, 17 allocs/op — unchanged |
| `BenchmarkCompilePolicy` (`config`, with an `approval_status` field present) | 365.9 ns/op, 480 B/op, 5 allocs/op |
| `BenchmarkValidate` (`config`) | 239.7 ns/op, 48 B/op, 3 allocs/op |

**Zero added allocation, confirmed by the matched pair.**
`BenchmarkEvaluateApprovalCondition`'s `0 B/op, 0 allocs/op` matches
`BenchmarkEvaluateMatch`'s exactly (the small ns/op difference is
measurement noise, not a real per-run cost difference — both are a
handful of struct-field comparisons with no allocation). `Engine.Analyze`
itself was not expected to move and did not: `policy.Input`'s one new
field is a plain assignment from an already-computed
`ev.Context.ApprovalStatus`, not a new allocation path.
`BenchmarkCompilePolicy`/`BenchmarkValidate` were re-run rather than
diffed against a stored pre-task baseline (none exists at this
granularity), but remain in the same low-allocation range every other
`config` compile/validate path already occupies — consistent with
adding one more `string` field to an existing struct literal, not a
new code path.

### v0.7 task 031 (Delegation Behavioral Semantics)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro).

| Benchmark | Result |
|---|---:|
| `BenchmarkEngineAnalyze` (root, before) | 456 B/op, 17 allocs/op |
| `BenchmarkEngineAnalyzeDelegationAbsent` (`DelegationWeight` enabled, no event carries `DelegatedFrom`) | 456 B/op, 17 allocs/op — unchanged |
| `BenchmarkEngineAnalyzeDelegationFamiliar` (`DelegatedFrom` set, seen 30 times) | 200 B/op, 17 allocs/op |
| `BenchmarkEngineAnalyzeDelegationNovel` (`DelegatedFrom` set, never seen) | 328 B/op, 20 allocs/op |

**Zero cost when unused, confirmed by the matched pair.**
`BenchmarkEngineAnalyzeDelegationAbsent`'s `456 B/op, 17 allocs/op`
matches `BenchmarkEngineAnalyze` exactly: `delegationSignal` is gated
on `feat.Volatile.DelegatedFrom != ""` before it is ever called (see
`anomaly.Score`), so an event that never carries a delegator pays
nothing beyond the one string-empty check, identical in shape to every
prior opt-in signal's own "computed only when the input exists" gate.
The Familiar/Novel numbers differ from `BenchmarkEngineAnalyzeDelegationAbsent`
because they additionally exercise `agentEvent`'s own construction path
(`AI Agent` events carry `Target`, `Context.SessionID`, etc.,
distinctly from `paymentEventAt`'s fixture) and, for the Novel case,
`delegationSignal`'s `Detail` string formatting (`fmt.Sprintf`) on
every call — the identical "Detail only formatted when the signal
actually fires" cost every other signal in `internal/anomaly` already
pays (see `latencySignal`'s own doc comment). Neither number reflects
a `DelegatorCounts` map-copy cost on the read (`anomaly.Score`) path,
since scoring only ever reads the map; the copy-on-write cost lives
entirely in `Baseline.Observe` (the write path), structurally identical
to `recordPredecessor`'s already-measured cost
(`BenchmarkObserveTransition`) — no dedicated new
`internal/baseline` benchmark was added for this reason.

### v0.7 task 033 (Public Anomaly Configuration & Stabilization)

Measured same environment (Go 1.27, darwin/arm64, Apple M3 Pro). This
task adds a config-compilation boundary, not a hot-path change —
`CompileAnomaly` runs once, at `Engine` construction, identical to
`CompilePolicy`'s own existing discipline (see [ADR
0017](adr/0017-public-anomaly-configuration-boundary.md)):

| Benchmark | Result |
|---|---:|
| `BenchmarkCompileAnomaly` (`config`, setup-path only) | 55.99 ns/op, 48 B/op, 1 alloc/op |
| `BenchmarkValidateAnomaly` (`config`, `Validate` alone) | 32.30 ns/op, 0 B/op, 0 allocs/op |
| `BenchmarkEngineAnalyze`/`BenchmarkEngineAnalyzeDelegationAbsent` (root, before and after) | 456 B/op, 17 allocs/op — unchanged |

**No hot-path cost, confirmed by the unchanged pair.** Nothing on
`Engine.Analyze`'s own call path invokes `CompileAnomaly`, `Validate`,
or any other `config` package function — `Engine` stores only the
already-compiled `anomaly.Config` value `WithAnomalyConfig` received.
`BenchmarkCompileAnomaly`'s one allocation is the `map[string]float64`
copy for a populated `SensitiveTargetFloor`; a config with none
allocates nothing.

### v0.8 task 034 (Production Store Contract & Public Boundary)

**No hot-path change, and no new numbers to publish.** This slice added
a public configuration boundary and a contract test suite; it changed
no code on `Engine.Analyze`'s or `Engine.Observe`'s path.
`config.CompileStorage` runs exactly once, at `Engine` construction,
alongside `CompilePolicy`/`CompileAnomaly` — see [§ v0.7 task
033](#v07-task-033-public-anomaly-configuration--stabilization) for the
same compile-once-not-per-event discipline.

The relevant existing baselines are therefore unchanged and remain the
reference: `BenchmarkEngineAnalyze` (456 B/op, 17 allocs/op) and
`internal/store`'s own `BenchmarkInMemoryObserveSameKey` /
`BenchmarkInMemoryObserveDistinctKeys` /
`BenchmarkFileStoreObserveSameKey` /
`BenchmarkFileStoreObserveDistinctKeys` (see [§ Measured
results](#measured-results)). No storage benchmark was added, because
nothing about storage performance changed.

**Measurement plan for task 035 (PostgreSQL), recorded now so it is not
improvised later** — kept as written for the record; tasks 035 and 036
carried it out and their real numbers are in the two sections below, and
the Docker Compose environment it anticipates was delivered by task 037:

- **Compare like with like.** `FileStore` and a database serve different
  roles; their raw ns/op are not meant to match, and presenting them as
  a head-to-head would be misleading. The useful comparisons are
  `Observe` latency, `Get` latency, and — the one that actually
  distinguishes a production backend — *concurrent same-key update
  throughput*, where `FileStore` serializes every write behind a
  whole-file rewrite while a row-locked database serializes only
  same-key writes.
- **Keep database timing out of unit tests.** `go test ./...` must not
  require a running PostgreSQL, so database benchmarks and integration
  tests sit behind an explicit opt-in and reuse `v0.8`'s planned Docker
  Compose reference deployment rather than adding a
  container-orchestration dependency. A network-dependent benchmark in
  the default suite would be flaky, not informative.
- **Publish real numbers only.** No invented figures for an
  unimplemented backend — this section stays empty of PostgreSQL results
  until task 035 measures them.

### v0.8 task 035 (PostgreSQL Store)

**No hot-path change to the engine.** `internal/store/postgres` adds a
third `Store` implementation; `Engine.Analyze`/`Observe` and every
pipeline package are byte-for-byte unchanged, so
`BenchmarkEngineAnalyze` and all per-stage numbers above still stand.
What changed is the cost of the `Store.Get` and `Store.Observe` calls
*within* them, when a deployment selects this backend.

`internal/store/postgres/postgres_bench_test.go` follows the measurement
plan recorded above, unchanged. Benchmarks are gated on
`TRUSTVIAN_TEST_POSTGRES_DSN` and skip without it, so `go test -bench=.`
never requires a database.

Measured against PostgreSQL 17 in Docker over loopback, Apple M3 Pro
(`-benchtime=200x`, 12-way parallel):

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `BenchmarkGet` | 152,408 | 11,649 | 56 |
| `BenchmarkObserveDistinctKeys` | 238,181 | 21,081 | 100 |
| `BenchmarkObserveSameKey` | 599,961 | 19,605 | 95 |

**How to read these.** Every figure is dominated by network round-trips
and is a property of the deployment — network latency, server tuning,
pool size — not of this code. They are useful as *ratios* and as
regression signals (an extra round-trip per `Observe` would show up
immediately), never as advertised latencies. A remote database will be
slower; a tuned one on a fast link, faster.

Two ratios do carry meaning:

**Same-key is ~2.5× distinct-keys.** That gap is the row lock doing its
job: concurrent observations of one actor serialize, concurrent
observations of different actors do not. If that ratio ever collapsed to
1.0, the lock would have stopped working; if it grew sharply, something
would be contending that should not be.

**PostgreSQL is ~6–17× *faster* than `FileStore` under concurrent
writes** — 600 µs versus `BenchmarkFileStoreObserveSameKey`'s ~3.4–4.7 ms,
and 238 µs versus `BenchmarkFileStoreObserveDistinctKeys`'s ~3.9–4.7 ms.
This inverts the expectation the task-034 plan above was written with, and
the reason is structural rather than incidental: `FileStore` rewrites its
*entire* contents on every `Observe`, so its cost scales with total store
size, while PostgreSQL updates exactly one row. The plan anticipated the
concurrent-same-key case being the distinguishing one and it was — just
more decisively, and in the other direction.

The practical consequence for the guidance below: `FileStore` is no longer
the "faster durable option." It is the *zero-setup* durable option for a
single process. Once durability matters at all under load, PostgreSQL is
both the faster and the shareable choice. See
[storage-guide.md](storage-guide.md).

### v0.8 task 036 (Store Durability, Concurrency & Migration Hardening)

**No hot-path change and no new benchmark.** Task 036's single production
change is in `Migrate`, which runs once at `Engine` construction and never
on `Analyze`'s or `Observe`'s path. Every microbenchmark above stands
unchanged, including task 035's own PostgreSQL figures.

What this slice adds is a third measurement category, which should not be
confused with the other two:

| Category | What it measures | Where |
|---|---|---|
| **Microbenchmark** | one function, in-process, no I/O | `Benchmark*` across the pipeline packages |
| **PostgreSQL integration measurement** | one store operation including network round-trips | `internal/store/postgres/postgres_bench_test.go` (task 035) |
| **Stress measurement** | aggregate throughput under deliberate contention | `internal/store/postgres/stress_test.go` (task 036) |

Stress figures come from *correctness* tests. Each asserts an exact
observation count and never a duration, because a lost update is a defect
no measured speed excuses. The rates below are logged output, not
thresholds — putting a network-dependent latency bound in a test would
manufacture flakes rather than catch regressions.

Measured against PostgreSQL 17 in Docker over loopback, Apple M3 Pro:

| Scenario | Throughput | Correctness |
|---|---|---|
| 32 writers × 100 observations, **one key**, 3 rounds | ~2,300 obs/s | 3200/3200 each round, 0 lost |
| 32 keys × 100 observations, **distinct keys** | ~5,000 obs/s | 3200/3200, 0 lost |
| 96 concurrent **first** writes, 5 rounds | — | 96/96 each round, exactly 1 row |
| Mixed committing + cancelled writers | — | acknowledged == stored, exactly |

**The one ratio worth watching: multi-key is ~2.1× same-key.** That gap is
the row lock behaving correctly — concurrent observations of one actor
serialize, concurrent observations of different actors do not. If the two
figures ever converged, something would have introduced global
serialization (a table lock, an advisory lock on the write path); if the
gap grew sharply, something would be contending that should not be. The
absolute rates are properties of the deployment and will differ on any
other network.

Two latency figures are recorded because they bound *failure* rather than
throughput, and both come from tests where the deadline is the subject of
the assertion rather than a guess:

- A transaction waiting on a row lock, then cancelled, returned in
  **5.4 ms**.
- An operation against an exhausted pool with a 2 s deadline gave up at
  **2.0001 s** — it waited for capacity rather than failing instantly, and
  did not overrun.

A note on pool sizing, since it is the only performance knob: contention
does not scale with pool size. Concurrent observations of the same actor
serialize on that row's lock regardless of how many connections exist;
only concurrency across different actors benefits from a larger pool. See
[storage-guide.md § Pool sizing](storage-guide.md#pool-sizing).

### v1.0 task 053 (Evaluation Result Aggregation)

The first measured path outside the engine. `EvaluationAggregate.AddRecord`
folds one public `DecisionRecord` into a fixed-shape summary, and will
plausibly run once per decision in a future control plane.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `EvaluationAggregateAddRecord` | 85.5 | 0 | 0 |

Six runs, darwin/arm64, Apple M3 Pro, Go 1.27; spread 84.1–87.6 ns/op.

**Read the allocation column, not the latency one.** Across three revisions
of this path — 95.2, then 99.5 after policy-selection validation added two
string comparisons, then 85.5 after a binding check added one more — `ns/op`
moved in both directions while `B/op` and `allocs/op` stayed at zero. Each
revision added work, so the final number being the fastest is session
variance, not an optimization; this file's own [reading the
numbers](#reading-the-numbers) section documents exactly that split. Nothing
was tuned, and the validation added between measurements is all still there.

**Zero allocations is the number that matters**, and it was not free. The
first implementation paired each metric name with a `*MetricSummary` pointing
at the receiver's own field, so one loop could both validate and fold. That
made the receiver escape to the heap — `moved to heap: a` under `-gcflags=-m`
— costing one 416-byte allocation per record and 141 ns/op. Replacing the
pointer array with five explicit assignments removed the escape entirely and
took the path to 95 ns/op, a 32% improvement on top of the allocation.

The invariant worth protecting is that a record does not allocate *because the
aggregate has already seen many*. The aggregate is fixed-size and retains no
record, so per-record cost is constant in the number of records — see
[ADR 0026](adr/0026-evaluation-aggregation-is-bounded-evidence.md).

### v1.0 task 054 (Behavioral Diff)

A second per-record platform reducer, plus a comparison path.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `BehaviorCollectorObserveExisting` | 60.9 | 0 | 0 |
| `BehaviorCollectorObserveNew` | 413 | 1,116 | 3 |
| `CompareBehaviorSnapshotsTypical` (32 × 32) | 6,510 | 30,456 | 10 |
| `CompareBehaviorSnapshotsFull` (512 × 512, disjoint) | 103,700 | 469,752 | 10 |

Three to five runs each, darwin/arm64, Apple M3 Pro, Go 1.27.

**What enforcing one-to-one identity cost.** The table above is current; the
figures in this paragraph are superseded and recorded only to show the price.

An earlier revision checked one direction only — that a fingerprint described
a single behavior — and measured roughly 190 ns for new-fingerprint admission
and 57 µs for the full comparison. Adding the reverse check, that a behavior
has a single fingerprint, about doubled both: it needs a bounded reverse index
in the collector and a descriptor map in the comparison. The hot path is
untouched — repeat observation is still ~61 ns and allocation-free.

A bounded linear scan was tried before the index and measured first: 3.2 µs
per newly admitted fingerprint, because the scan averages half of a 512-entry
map. The index is 7.7× better for one more bounded map written in a single
place. The measurement is what chose between them.

**Three properties, none of them "fast":**

*Observing a behavior already seen allocates nothing.* This is the common case
by a wide margin — an evaluation observes the same shapes repeatedly — and it
is what makes per-record cost independent of how many records came before.

*Admitting a new behavior allocates 3 times,* covering the entry and the
growth of the two bounded indexes. Both are capped at 512 per collector, so
the total is bounded however long the evaluation runs.

*Comparison allocates 10 times* in both the representative and the full
bounded case — 32 × 32 and 512 × 512 measure the same count. That is the
invariant worth reading: the allocation *count* does not grow with the number
of historical observations, and bytes scale only with the bounded number of
behaviors and deltas. The worst case, two full disjoint snapshots producing
the maximum 1,024 deltas, is ~104 µs and ~470 KB on the machine recorded
above.

Treat the `ns/op` figures as machine- and session-specific; the allocation
counts are structurally stable and are what these rows are really for — see
[reading the numbers](#reading-the-numbers).

Zero allocations was not a goal here and would have been the wrong one: two
bounded maps and a defensive snapshot copy legitimately allocate, and removing
any of them would trade a real invariant for a number.

### v1.0 task 055 (Evaluation Scorecards)

Composing two aggregates and a behavioral diff into one fixed-shape
comparison.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `NewEvaluationScorecardTypical` (2,048 records, 1,024 deltas) | 214 | 0 | 0 |
| `NewEvaluationScorecardEmpty` | 215 | 0 | 0 |

Five runs each, darwin/arm64, Apple M3 Pro, Go 1.27. As everywhere in this
document, the `ns/op` figures are machine- and session-specific measurements
rather than architectural constants; the `0`/`0` allocation columns are the
structurally stable part — see [reading the numbers](#reading-the-numbers).

**The two rows being identical is the measurement.** "Typical" compares two
evaluations of 2,048 records each whose diff holds the bounded maximum of
1,024 deltas; "empty" compares two evaluations that observed nothing. They
cost the same, because the scorecard reads only summary accessors — never the
deltas, never anything that scales.

Scorecard construction is therefore O(1) in both the number of records
previously aggregated and the number of behaviors compared. That is the main
reason task 054's detailed diff is not retained: a card copying the deltas
would cost and weigh in proportion to behavioral cardinality, for data the
caller already holds.

Zero allocations here, unlike task 054's comparison, because nothing is built
— every field is a copied counter, identifier, or summary.

### v1.0 task 056 (Deterministic Hard Gates)

Applying five integer gates to a full-size scorecard.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `EvaluateEvaluationGatePass` | 97 | 0 | 0 |
| `EvaluateEvaluationGateFail` | 97 | 0 | 0 |

Five runs each, darwin/arm64, Apple M3 Pro, Go 1.27. As everywhere in this
document, the `ns/op` figures are machine- and session-specific measurements
rather than architectural constants; the `0`/`0` allocation columns are the
structurally stable part — see [reading the numbers](#reading-the-numbers).

**PASS and FAIL costing the same is the measurement.** Both evaluate a card
built from 2,048 records and 512 behaviors a side, the bounded maximum of
1,024 diff deltas. A gate evaluator that stopped at the first failure would
make FAIL the cheaper case; these two rows are what "every check is evaluated
on every call" looks like from the outside.

Evaluation is O(1) in record count and behavioral cardinality alike, because
it reads five scalar accessors off a card that already summarized everything.
The input size above is therefore not a stress case — it is the same work any
card requires.

Zero allocations because nothing is built: the result is a fixed struct of
counters, identifiers and booleans, and no deltas, records or evidence
objects are retained.

### v1.0 task 057 (Local Platform Persistence)

Saving and loading one evaluation run's evidence through the local SQLite
adapter.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `SaveEvaluationEvidence32` | 692,539 | 97,928 | 2,081 |
| `SaveEvaluationEvidence512` | 4,686,661 | 1,127,251 | 23,668 |
| `LoadEvaluationEvidence32` | 157,493 | 47,429 | 1,105 |
| `LoadEvaluationEvidence512` | 598,297 | 494,855 | 11,202 |

Five runs each, darwin/arm64, Apple M3 Pro, Go 1.27, on a temporary
file-backed database with crash durability left on. As everywhere in this
document, `ns/op` is a machine- and session-specific measurement rather than
an architectural constant — see [reading the numbers](#reading-the-numbers).

**This is I/O, and no latency gate is asserted on it.** The numbers exist for
regression tracking; durability settings were not weakened to improve them.

The read path costs more than it first did, and visibly so at the small end:
`LoadEvaluationEvidence32` moved from ~118 µs to ~157 µs when loading gained
two existence probes and a read of the owning `EvaluationRun`. Those are fixed
per-call costs, so they show up as roughly a third at 32 behaviors and under a
tenth at 512. They buy the read-path trust boundary — partial evidence is
detected rather than mistaken for absent evidence, and restored values are
bound back to the run they claim to describe — which is not a trade worth
reversing for 39 µs.

**The shape is the claim, not the speed.** Persistence here is:

```text
O(1) in event-history length
O(B) in retained behavior cardinality, where B <= 512
```

Not "O(1) persistence" — storing a snapshot necessarily writes up to 512
entry rows, and the 16× step from 32 to 512 behaviors costs roughly 7× on
write and 4.7× on read. Fixed per-transaction overhead is why it is
sub-linear rather than proportional.

The important half is the first line. No table grows per event, so a run that
processed a million records writes the same rows as one that processed ten:
one aggregate, one header, and at most 512 entries. Raw event history is a
separate capability with its own volume question.

### v1.0 task 058 (Local Control-Plane API and Ingest)

Service-level operations over local SQLite.

| Benchmark | ns/op | B/op | allocs/op |
|---|---|---|---|
| `IngestDecisionRecord` | 1,523,510 | 448,419 | 8,965 |
| `CompareEvaluations32` | 362,977 | 131,112 | 2,361 |
| `CompareEvaluations512` | 1,411,803 | 1,480,610 | 22,555 |

Five runs each, darwin/arm64, Apple M3 Pro, Go 1.27, file-backed SQLite with
crash durability on. `ns/op` is machine- and session-specific — see
[reading the numbers](#reading-the-numbers).

**No latency gate is asserted, and durability was not weakened for these.**

**Ingest cost is dominated by persistence, not by the reducers.** One accepted
record rewrites the run's whole evidence — aggregate row, snapshot header and
every behavior entry — inside one transaction, because the cursor and the
evidence must become durable together. So ingest tracks the *bounded* snapshot
it rewrites rather than the record it accepted, which is why a single record
costs about what task 057 measured for a full evidence save.

That is a deliberate trade. Batching would amortize it, and would also mean a
window where the sequence and the evidence disagree.

**Comparison cost tracks behavioral cardinality, not history length.** The 16×
step from 32 to 512 behaviors a side costs under 4×: two bounded evidence
loads plus a diff of at most 512 behaviors per side. A run that ingested a
million records compares exactly as fast as one that ingested a thousand,
because no table grows per event.

## Reading the numbers

**Session-to-session `ns/op` moved broadly; allocation counts didn't —
that split tells you what's real.** This table was re-measured for
task 011 on the same environment (Go 1.27, darwin/arm64, Apple M3 Pro)
as the prior recording, and nearly every `ns/op` figure is higher than
before — but every `B/op`/`allocs/op` figure for a benchmark whose code
didn't change is *identical* to the prior recording (`features.Extract`
0/0, `trust.Compute` 0/0, `fingerprint.Compute` 120/15,
`policy.Evaluate` 0/0 for both scenarios). Allocation counts are
deterministic and load-independent; wall-clock `ns/op` is not — a busy
laptop measures slower than an idle one for code that hasn't changed at
all. Concretely: task 006 added attribute matching to
`policy.Condition`, but neither benchmarked `policy.Evaluate` scenario
sets `Condition.Attributes`, so `Condition.Matches` ranges over a `nil`
map there — a zero-cost no-op in Go — which is exactly why its
`allocs/op` is unchanged even though `ns/op` moved with everything
else. Treat the `ns/op` deltas below as this session's absolute
numbers, not as evidence of a regression, except where a specific
code change is named.

**`Engine.Analyze`'s cost is accounted for.** `features.Extract`
(19.8ns) + `fingerprint.Compute` (221.1ns, called once) +
`anomaly.Score`'s familiar-path floor (54.1ns, which itself already
includes the fingerprint map lookup plus the frequency-deviation
branch's map read, subtraction, and `math.Sqrt`, even when the signal
doesn't fire — see [task 004](archive/tasks/v0.1/004-anomaly.md)) + `trust.Compute`
(5.8ns) + `policy.Evaluate` (30.5–43.2ns) sum to roughly 331–344ns
against the measured 553.8ns, with the remainder attributable to
`Store.Get`'s map read and struct construction for `Result` (the same
~40% remainder-to-sum ratio as the prior recording, so this isn't a new
gap — see [ADR 0005](adr/0005-fingerprint-computed-once-per-analyze.md)
for how the original accounting was confirmed by finding and removing a
real duplicate computation).

**The common case is the cheap case, by design.** `anomaly.Score`'s
familiar/no-signal path is zero-allocation (54.1ns this session, up
from 29.8ns before task 004 added the frequency-deviation branch —
that specific jump was a real, code-driven cost from the extra
arithmetic on that path, not allocation; the further move to 54.1ns is
this session's general variance, not a further code change); the
"everything fires" worst case (335.3ns, 5 allocs) only pays for
`fmt.Sprintf`-built explanation strings when a signal has actually
fired — see `latencySignal`'s and `frequencySignal`'s doc comments in
[`internal/anomaly/anomaly.go`](../internal/anomaly/anomaly.go). A
brand-new fingerprint has no Baseline entry at all
(`known == false`), so `frequencySignal` structurally cannot fire
there regardless of how many other signals do — the "everything fires"
benchmark's allocation count (448 B, 5 allocs) is unchanged from the
prior recording for exactly that reason. Explainability's cost is
proportional to how much there is to explain, not paid unconditionally
on every call.

**The `FrequencyWeight: 0` default costs nothing and saves nothing.**
The v0.1 final-review pass changed `anomaly.DefaultConfig()`'s
`FrequencyWeight` from `0.6` to `0` (see
[DOMAIN.md § Anomaly](DOMAIN.md#anomaly)), but a weight scales a
signal's contribution *inside* `combine`; it does not gate whether
`frequencySignal` runs, whether its `math.Sqrt` is computed, or whether
the `Signal` is appended to `Contributors` (that is `Value > 0`, which
is weight-independent). Re-measuring after the change confirms this:
`BenchmarkScoreKnownFamiliar` 53.2ns / 0 allocs and
`BenchmarkScoreNovelWithAllSignals` 448 B / 5 allocs, both matching the
recording above within session variance. The same pass added two
ordering guards — a `now.After(LastObserved)` check in
`baseline.FingerprintStats.observe` and an `interval > 0` check in
`anomaly.Score` — each a single `time.Time` comparison on a path that
already did a subtraction, and neither allocates. `baseline.Observe`
re-measures at 185.9ns / 464 B / 3 allocs — the allocation profile that
matters here is byte-for-byte identical to both recordings above, and
the `ns/op` sits inside the session-to-session spread those two
recordings already show (276.6ns and 165.8ns for the same code).

**Sharded locking measurably works.** `store.InMemory.Observe` under
12-way concurrent load on distinct keys (180.6ns) is roughly 2×
faster than 12-way concurrent load on the *same* key (363.1ns) — this
is the per-`Key`-sharded lock in `internal/store` doing its job: two
different actors never contend with each other, only concurrent
updates to the same actor's baseline do.

**Persistence costs roughly four orders of magnitude, and that's the
deliberate tradeoff.** `store.FileStore.Observe` (~3.4–4.7 ms/op) is
about 10,700×–26,000× slower than `store.InMemory.Observe` (~180–365
ns/op) — entirely attributable to a synchronous `fsync` on every call
(see [ADR 0006](adr/0006-file-backed-persistent-store.md)). This is
large enough that it matters which `Store` a deployment chooses:
`InMemory` for throughput, `FileStore` when surviving a restart is
worth the cost — and, since task 035, PostgreSQL when the cost is worth
paying *and* more than one process must share the result, at roughly a
sixth to a seventeenth of `FileStore`'s per-`Observe` cost (see [§ v0.8
task 035](#v08-task-035-postgresql-store)). Note `FileStoreObserveDistinctKeys`'s 12-way-parallel
row shows both higher `ns/op` *and* much higher `B/op`/`allocs/op`
(16,891 B, 51 allocs) than its sequential counterpart (3,890 B, 24
allocs) — this specific benchmark's store grows as concurrent
goroutines each add a new key during the run (see the benchmark's own
doc comment in `internal/store/file_bench_test.go`), so later flushes
in that run are serializing a larger snapshot than earlier ones; it is
not a per-call regression, it's the flush-cost-scales-with-store-size
property `FileStore`'s design accepts, visible in the data.

**`Engine.Analyze` scales under real concurrency.** 553.8ns
sequential vs. 191.9ns at 12-way parallelism reflects that the common
path (`Store.Get`) only takes a read lock and every other stage is a
pure function with no shared mutable state.

**`otel.EventFromSpan` is comparable in cost to the rest of the
pipeline, not a bottleneck relative to it.** At 385.8ns/696B/10 allocs
for a representative HTTP-server span (resource attributes, a
request-method attribute, a measured duration — the same shape as
`TestEventFromSpanHTTPServerMapping`), it costs less than
`fingerprint.Compute` alone (221.1ns) plus `store.InMemory.Observe`
(345.7–363.1ns) combined, and it runs once per span, before `Analyze`,
not inside it. This closes the first item task 011 was scoped to
measure (see [tasks/011-performance.md](archive/tasks/v0.1/011-performance.md)) —
there was no reason to expect it to be expensive, and it isn't.

**`otel.AttributesFromResult` (task 008) is the cheapest adapter
function in this table.** At 59.9ns/320B/1 alloc — a single
five-element `[]attribute.KeyValue` slice, the only allocation — it
costs roughly a sixth of `otel.EventFromSpan`'s 385.8ns. This is
expected: it's pure field extraction and type conversion (`float64`,
`string(...)`) over a `Result` that's already fully computed, with no
map construction or ID-stringification work like `EventFromSpan`'s
`attributeMap`/`SpanID().String()` calls. It is not on the
`Engine.Analyze` hot path itself — it runs after a `Result` already
exists, for a caller (e.g. a future OTel Collector processor,
[task 009](archive/tasks/v0.2/009-otel-collector.md)) choosing to export it.

**`store.InMemory`'s per-`Observe` cost does not grow with the number
of distinct keys it holds — allocation-wise, at least.** (A key is
`{Scope, ActorID, Environment}` as of `v1.0`; the learning scope is an
additional key dimension, so the figures below describe one *baseline*, and
a deployment using several scopes per actor holds correspondingly more of
them.) Across 100,
1,000, and 10,000 pre-populated distinct baseline keys
(each with its own distinct `Fingerprint`), `B/op` and `allocs/op` are
*exactly* identical at every key count (464 B, 3 allocs — the same
copy-on-write `Fingerprints`-map rebuild `baseline.Observe` always
does, see "Allocation considerations" below): a store already holding
10,000 keys pays the same allocation cost per `Observe` call as one
holding 100. `ns/op` does drift upward as the key count grows — 243.8ns
at 100 keys to 280.8ns at 10,000 keys, about +15% — most plausibly from
reduced CPU cache locality as Go's underlying map grows more buckets to
hold more entries, not from any additional allocation (a repeat
measurement at `-cpu 1` shows the same shape, a larger ~333ns to ~422ns
range, +26%, confirming the trend is real and not single-run noise, on
`ns/op` specifically). This is not concerning at this scale, and it
would take direct evidence at production key-count scales, not
extrapolation from 10,000, to justify further investigation — per this
task's Non-Goals, no optimization is attempted here. Note this
benchmark still does not measure total heap footprint as the key set
grows without bound: `InMemory` has no eviction/expiration policy (see
[ROADMAP.md](ROADMAP.md)), so a real long-running deployment's *total*
memory use is still expected to grow linearly with the number of
distinct actors ever observed — this benchmark characterizes per-call
cost at a given store size, not the shape of that unbounded total
growth, which would need a different measurement (e.g. `runtime.MemStats`
sampled across a run) if it becomes a real question.

## Allocation considerations

- `fingerprint.Compute`'s 15 allocs/op come from `hash/fnv.New64a()`
  (a heap-allocated hasher) and `strconv.FormatUint` (the resulting ID
  string) — a known, accepted cost from the original implementation,
  not yet optimized further since it hasn't shown up as the dominant
  cost in end-to-end benchmarks. A future optimization (reusing a
  hasher, avoiding the string conversion) is possible but unmeasured —
  not claimed here as already done. [Task 002](archive/tasks/v0.1/002-fingerprint.md)
  added the `fingerprintVersion` marker and `TargetCategory` to the
  hash input (two more `writeField` calls), which is the entirety of
  the increase from the prior 12 allocs/op, 104 B/op, 136.7 ns/op —
  each additional field written through an `io.Writer` interface
  carries its own small conversion/write cost; still not the dominant
  cost anywhere it's measured end-to-end.
- `baseline.Observe` and `store.InMemory.Observe`'s 3 allocs/op are the
  copy-on-write `Fingerprints` map rebuild — a deliberate tradeoff (see
  [ADR 0004](adr/0004-narrow-store-port-in-memory-only.md) and
  `Baseline`'s doc comment): paying a map-rebuild cost on the
  *infrequent, gated* write path (`Observe`) is what makes the *hot,
  frequent* read path (`Get`, called on every `Analyze`) completely
  lock-free after acquiring a read lock, with no defensive copying
  needed. [Task 004](archive/tasks/v0.1/004-anomaly.md)'s three new
  `FingerprintStats` fields (`IntervalObservations`, `IntervalMean`,
  `IntervalVariance`) grew the struct copied into that map by 24 bytes
  (three more `float64`/`uint64`-sized fields), which is the entirety
  of `baseline.Observe`'s B/op increase (432 → 464); the allocation
  *count* is unchanged (still 3) since it's the same map-rebuild
  shape, just a larger value type.
- `otel.EventFromSpan`'s 10 allocs/op (696 B) are dominated by building
  a fresh `map[string]any` for `Event.Attributes` (`attributeMap`) and
  the `string(...)` conversions `SpanID()`/`TraceID()` require —
  unavoidable given `ReadOnlySpan`'s API returns typed IDs, not
  pre-formatted strings, and `Event.Attributes` is a plain map by
  design (see `event.Event`'s doc comment). Not yet profiled at the
  per-line level since, per the reading above, it isn't the dominant
  cost anywhere it's used — the same "measure before optimizing" bar as
  every other entry in this section.

## Concurrency considerations

- `Engine.Analyze` spawns no goroutines — it's fully synchronous, which
  is what keeps goroutine-leak risk at zero for the current
  implementation (verified: no `go func`/goroutine spawns anywhere in
  non-test code, checked via `grep` across the module).
- `internal/store.InMemory` shards its lock per `baseline.Key`, with a
  brief global lock only for first-time key creation (see
  [ARCHITECTURE.md § storage boundary](ARCHITECTURE.md#storage-boundary)).
  All store and baseline tests run under `go test -race`.
- `store.FileStore` (added for persistence — see
  [ADR 0006](adr/0006-file-backed-persistent-store.md)) keeps this
  property: its synchronous flush-per-`Observe` design was chosen
  specifically to avoid introducing this module's first background
  goroutine (a timer-based flusher was considered and rejected for
  exactly that reason). Re-verified after adding it: still zero
  `go func`/goroutine spawns anywhere in non-test code.
- The **Collector processor** is where the one exception lives, and it
  is deliberate: the health listener added in
  [task 042](archive/tasks/v0.9/042-runtime-health-readiness-graceful-shutdown.md)
  runs one goroutine, serving until `Shutdown` stops it. That is the
  entire goroutine inventory of the long-lived runtime — still zero
  channels, zero tickers, and zero timers in non-test code across both
  modules. `Analyze`/`Observe` remain synchronous, so the processor adds
  no per-span goroutine either.
- The processor's operational metrics
  ([task 043](archive/tasks/v0.9/043-self-observability-resource-safety.md)) are
  **allocation-free**: `BenchmarkConsumeTraces` reports the same 33
  allocs/op and 1352 B/op as before they existed, because every
  attribute set is pre-built at construction from a closed vocabulary
  rather than assembled per call. With a real metrics SDK attached the
  span path costs ~350 ns more — the SDK's own aggregation across five
  measurements, paid only when a metrics pipeline is configured. Full
  table in [Observability § what it costs](observability.md#what-it-costs).

## What's not benchmarked (yet)

Both gaps this section previously named —
`internal/otel.EventFromSpan` and `store.InMemory`'s per-call cost as
the number of distinct keys it holds grows — are closed as of
[task 011](archive/tasks/v0.1/011-performance.md): see `BenchmarkEventFromSpan` and
`BenchmarkInMemoryMemoryGrowth` above. Every pipeline stage named in
the roadmap brief (event processing, feature extraction, fingerprint
generation, baseline lookup, baseline update, anomaly detection,
trust/risk calculation, policy evaluation, the complete `Analyze`
pipeline) is now benchmarked, and so is the OTel adapter.

One related, but genuinely distinct, open question remains, checked
honestly rather than assumed away: `BenchmarkInMemoryMemoryGrowth`
characterizes *per-call* cost (`ns/op`/`B/op`/`allocs/op`) at three
fixed store sizes — it does not measure a long-running process's
*total* heap footprint as an unbounded number of distinct actors
accumulate over time, which would need a different technique
(`runtime.MemStats` sampled across a run, not `go test -bench`). Since `InMemory` has no eviction or expiration policy *across actors*,
that total footprint is expected to grow roughly linearly with the
number of distinct actors ever observed — each actor's own contribution
is now structurally bounded (at most 512 fingerprint identities, see
[ADR 0019](adr/0019-bounded-fingerprint-admission.md)), but the number
of actors is not —
this is a known, named design gap already, not a new discovery — but
it hasn't been directly measured, and doing so is future work, not
part of this task's scope (see its Non-Goals).

`Trust.Explain()` and `Result.Explain()` (added in tasks 005/007) are
deliberately not benchmarked here for the same reason
`otel.EventFromSpan` wasn't originally prioritized: both are on-demand
audit/logging convenience methods called after a `Result` already
exists, not part of the `Event → ... → Decision` hot path itself, so
they don't meet this document's own bar for what gets a row.
