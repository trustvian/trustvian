# 078 — design notes

**Temporary.** Deleted by slice 5, once its decisions are in the ADRs, the
scenario guide and `docs/compatibility.md`. Nothing here is the contract; this
is the reasoning that produced one.

Read: `docs/tasks/v1.0/078-behavioral-scenario-suites.md` in full (1422 lines,
as revised by #112), ADRs 0022–0024, 0026–0029, 0031, 0033, 0038, 0041–0045,
and in the companion checkout `docs/results/2026-09-27-stability.md` and
`docs/upstream/078-measurement.md`.

## 0. The re-run measurement does not exist yet

The brief points at `docs/results/<date>-stability-tool-fidelity.md` and says to
let its numbers set the `k`/`j` guidance. **That file has not been written.**
The companion checkout holds exactly one result document:

```
docs/results/2026-09-27-stability.md    the first sweep, at HTTP fidelity
docs/upstream/078-measurement.md        addressed to 078 § Measurement, same sweep
```

The tool-fidelity sweep is Phase C2 of that repository's current work. Phase C1
— the design, `docs/design/2026-09-28-tool-fidelity-measurement.md` — was
written and stopped for approval, which has not come. So C2 never ran.

**This does not block the implementation, and the revision is why.** #112
changed 078 precisely so it could be built without that number: `k` and `j` are
**required with no default**, and the documented guidance is `k: 1, j: 0`, which
is task 054's set semantics and asserts no threshold. Nothing ships unmeasured
because nothing ships defaulted. The spec says so itself:

> The documented guidance is therefore: `k: 1 j: 0` unless presence variance
> has been measured for that workload.

So the guidance I document is `k = 1, j = 0`, citing the first sweep — the
numbers that exist. That is what those numbers justify: zero false FAILs in
4/4 configurations, every behavior 10/10, and an explicit finding that the
workload was *structurally incapable* of presence variance, so the rate is not
evidence that workloads do not vary.

Acceptance criterion 17 stays **partially satisfied**, in the exact words the
spec already uses, and criterion 19's "no defaults" is fully satisfied and
tested. When C2 produces a tool-fidelity sweep, the guidance is revisited — and
because it is guidance rather than a default, revisiting it is a documentation
change and not a contract break. That property is the whole point of the
revision, and it is worth saying out loud in the ADR.

**What I am not doing:** writing a `k` I cannot cite, and quietly reusing the
first sweep's numbers as if they were the re-run's. Both were available and both
would be wrong.

### The conditional the brief supplies resolves this

A later brief stated the rule directly:

> If they say set semantics still suffice, `k`/`j` stay required-and-explicit
> with `k: 1, j: 0` as the documented guidance; if they recommend a `k`,
> document it as measured guidance for that workload, never as a default.

Only one branch of that conditional can fire on the evidence that exists, and it
is the first. The 2026-09-27 sweep found zero false FAILs in 4/4
configurations with every behavior 10/10, which recommends no `k` and leaves set
semantics sufficient. So the guidance is `k: 1, j: 0`, required and explicit,
with no default — and that is also, independently, what the revised spec already
documents.

The tool-fidelity sweep could only change this by *finding variance* and
recommending a `k`. If it later does, the second branch fires, and what changes
is a documented guidance line rather than a contract — because `k` and `j` are
required fields with no default, which is precisely the property #112 introduced
to make the implementation safe to build before the re-run. Nothing about this
decision has to be revisited as a breaking change.

**The one thing the missing sweep still costs** is acceptance criterion 17,
which stays partially satisfied: the guidance asserts set semantics rather than
a measured threshold. That is recorded in the spec, in the ADR, and here — not
papered over.

---

### Update 2026-10-01: the re-run exists

The section above is kept as written; it was true when written. The tool-fidelity
re-run has now been performed: Phase C2 of the companion repository, against
Trustvian `07cb4e3`, ten isolated repetitions per side at T = 0.7 and 1.3. It is
recorded in task 078 §
[The re-run at tool-name fidelity](078-behavioral-scenario-suites.md#the-re-run-at-tool-name-fidelity-2026-10-01),
with the full results at the companion commit `a19d7f4`.

It changes three things here:

- **Acceptance criterion 17 is no longer "partially satisfied" for want of a
  sweep.** The sweep exists and found variance: unchanged pairs FAIL 48/90 and
  59/90 at a single-run zero.
- **The `k`/`j` decision is unchanged, but its basis is.** Both stay required,
  with no default. The reason is no longer that nothing was measured: it is that
  at T = 1.3 no `k` removed every unchanged crossing. The second branch of the
  brief's conditional — "if they recommend a `k`" — did not fire, because the
  numbers do not recommend one.
- **Slice 4 gains a decision it did not have: the unit of
  `max_repeated_added_behaviors`** (078 open question 8). Per-identity presence
  moves a tool and its HTTP child together, so a nonzero budget double-counts.
  Pairwise `added_change_count` is not a stable unit to aggregate instead.

---

## 1. Reference association

### The flag in the brief names the wrong kind of thing

The brief says `--reference-run`. With repetition a reference is **N runs**, and
the spec is explicit:

> A "most recent completed run" convenience default becomes "the most recent
> completed *scenario execution* of the same scenario on the same agent, and all
> N of its reference repetitions" — never a mix drawn from two executions.

A flag taking one `EvaluationRunID` cannot supply N reference repetitions. It
would appear to work at `runs: 1` and fail the first time somebody wrote
`runs: 3` — the worst available shape, because the failure arrives long after
the decision. So the flag names a scenario execution:

```text
--reference <scenario-execution-id>   an explicit prior execution
--reference last                      the documented convenience default
```

`last` resolves to **the most recent completed scenario execution with the same
scenario name, on the same agent**, and all N of its reference repetitions.
Deterministic: ordered by execution id byte-ascending among completed
executions matching (scenario name, agent), most recent taken. Not by
timestamp — ADR 0041 § 2 rules that out, and for the same reason here (the
schema stores `RFC3339Nano` text, which is not lexically ordered).

### Two modes, and the default is self-contained

| Mode | Shape | Reference |
|---|---|---|
| **self-contained** (default) | the scenario declares both sides; the runner executes 2N repetitions | the N reference repetitions of this execution |
| **against history** (`--reference`) | the scenario's reference side is not executed; N candidate repetitions run | the N reference repetitions of the named execution |

Self-contained is the default because it is the only mode that needs no prior
state, it is what every test in slice 6 exercises, and it is the shape the
companion demo already uses. `--reference` is what makes the persisted execution
worth persisting, and it is the CI path: a pipeline records a reference
execution on merge and compares each pull request against it without re-running
the reference side.

### Failing loudly, three ways

```text
--reference names an execution that does not exist          → 3, naming the id
--reference last, and no completed execution matches        → 3, naming scenario + agent
the reference execution's repetition count != this runs: N  → 3, naming both counts
```

Never truncated to fit, never padded, never an implicit pass. The third is the
one the spec calls out specifically, and it is not a usage error (`2`) because
nothing about the *invocation* is malformed — the operator asked a coherent
question about state that cannot answer it, which is operational.

A reference execution that is itself incomplete (saturated evidence, or a
repetition that never completed) is refused the same way. `completedRun`'s
existing reasoning applies one level up: a comparison against evidence that did
not finish is not a comparison.

---

## 2. Where the runner lives

```text
trustvian eval run --scenario scenarios/support-login.yaml
                   [--reference <id>|last]
                   [--api-url <url>]
                   [--json]
```

In the `eval` family in the **root CLI** (`cmd/trustvian`), per open question 2,
so it inherits the exit-code contract `docs/compatibility.md` already publishes
for `eval compare` rather than defining a second one.

### It computes nothing, and that is enforced three ways

ADR 0033 makes the developer CLI a thin HTTP adapter. The runner:

1. **imports no platform package** — already enforced by
   `scripts/check-platform-boundary.sh`, which is why the aggregation must be
   reachable over HTTP and not by a function call;
2. **contains no diff, scorecard, gate, presence count, verdict composition or
   ordering comparator** — a structural source scan, extending the one that
   already exists (`cli_architecture_test.go`), with the two new prohibitions
   the spec names: no presence counting, no repeated verdict;
3. **accepts the server's verdict unchanged** — a control plane returning FAIL
   yields FAIL and one returning PASS yields PASS, asserted in both directions.

The runner's whole job: parse the scenario, validate it, execute 2N
repetitions, submit 2N run identifiers plus the limits, receive one result,
render it, exit.

### It invokes `dev` in-process

`runDev(s streams, args []string) int` (`cmd/trustvian/dev.go:121`) is in the
same binary. One repetition is one `runDev` call with
`--behavioral-profile <ref>` and a fresh `--run-id`, sequentially — open
question 5's assumption, for its stated reason: concurrent repetitions of a
workload talking to a shared external service measure contention rather than the
workload.

In-process rather than `exec`: no PATH lookup, no second binary to find, no
argv quoting layer, and the child's exit code arrives as an `int` rather than
through a `*exec.ExitError`. The workload itself is still a subprocess — `dev`
already owns that, and this task adds no new process management.

A repetition that exits non-zero **aborts the scenario immediately** with exit
`3`. Remaining repetitions do not run. See § 5.

### The new HTTP route

Because the CLI may not import the platform, the aggregation needs a route:

```text
POST /v1/evaluations/compare-repeated
```

Body: N reference run ids, N candidate run ids, and the five repeated limits.
Response: the result document of § 3. `POST /v1/evaluations/compare` is
untouched, as the spec's Compatibility section requires.

Per ADR 0031 § 2 the handler calls exactly one control-plane method and computes
nothing; the existing structural test that pins the absence of
`CompareBehaviorSnapshots`/`NewEvaluationScorecard`/`EvaluateEvaluationGate`
references in the HTTP package extends to the two new symbols.

---

## 3. The platform aggregation

### Types

```go
// N per-repetition comparisons reduced to per-behavior [0,N] integer counts.
type RepeatedEvaluationEvidence struct{ ... }

type RepeatedBehaviorPresence struct {
    FingerprintID          string
    Behavior               trustvian.StableFeatures
    ReferenceRunsPresent   uint64   // [0, N]
    CandidateRunsPresent   uint64   // [0, N]
    Classification         RepeatedBehaviorClassification
}

type RepeatedBehaviorClassification string // "added" | "removed" | "neither"

type RepeatedEvaluationGateLimits struct {
    AddedCandidatePresenceMinimum      uint64 // k, required, 1 <= k <= N
    AddedReferencePresenceMaximum      uint64 // j, required, 0 <= j < k
    MaxRepeatedAddedBehaviors          uint64
    MaxBlockDecisionsPerRun            uint64
    MaxCriticalRiskObservationsPerRun  uint64
}

type RepeatedEvaluationGateResult struct{ ... } // six checks, stable order
```

`ControlPlane.CompareRepeatedEvaluations(ctx, referenceRunIDs, candidateRunIDs,
limits)` composes the existing chain N times, unchanged, and reduces the N
diffs.

### Why a new aggregation and not a wider diff or richer scorecard

Settled by the spec and by two ADRs; restating here only because the ADR must
carry it. Widening `CompareBehaviorSnapshots` to N snapshots per side breaks
task 055's one-to-one correspondence check (`reference.RecordCount ==
diff.ReferenceObservationCount` — there is no single `RecordCount` for N runs)
and changes a published signature. Putting per-behavior rows on the scorecard
undoes exactly the property ADR 0028 chose it for: fixed shape, construction
cost independent of behavior count.

So: **N unchanged chains in, one new value out.** It consumes the N `BehaviorDiff`
values, never the `DecisionRecord` stream — a second reducer over records would
be the fourth ingestion path tasks 055 and 056 each declined to add.

### The reduction, and permutation invariance

A behavior is present in a repetition when that repetition's diff carries a
non-zero count for it on that side. Counting is over the N diffs:

```text
reference_runs_present  = |{ i : referenceCount_i(fp) > 0 }|
candidate_runs_present  = |{ i : candidateCount_i(fp) > 0 }|
```

Reference repetition *i* is paired with candidate repetition *i* **solely** so
the existing pair-shaped chain can be reused, and the pairing carries no
meaning. Asserted by test, not by comment: shuffle either side's repetitions
independently and every presence count is identical. That test is what makes the
claim true rather than stated.

```text
repeatedly added    candidate_runs_present >= k  and  reference_runs_present <= j
repeatedly removed  reference_runs_present >= k  and  candidate_runs_present <= j
neither             otherwise
```

`j < k` validated at parse time, not assumed — with `j >= k` a behavior
appearing equally often on both sides satisfies both halves.

**Removed gates nothing.** Classified and reported, contributing to no check —
asserted by showing that adding a repeatedly-removed behavior changes no
verdict. ADR 0029 § 7 requires a new named gate to arrive with its own review,
not slipped in beside an existing one.

### The six checks

| # | Check | Rule | Advisory at N > 1 |
|---|---|---|---|
| 1 | reference repetitions completed | `== N` | no |
| 2 | candidate repetitions completed | `== N` | no |
| 3 | repetitions failing minimum evidence | `== 0` | no |
| 4 | repeatedly added behaviors | `<= max_repeated_added_behaviors` | no |
| 5 | worst candidate block count | `<= max_block_decisions_per_run` | **yes** |
| 6 | worst candidate critical-risk count | `<= max_critical_risk_observations_per_run` | **yes** |

All six evaluated on every call, no short-circuit, every one populated in every
result including when 1–3 fail — task 056's rule, inherited. Checks 1–3 are not
configurable away. Checks 5 and 6 take the **maximum** across repetitions, never
a sum (which would change a limit's meaning with N) and never a mean (which
would let one clean repetition offset a blocking one — the compensating path
ADR 0029 § 9 exists to prevent). A test pins max-not-sum-not-mean: one
repetition with two blocks and four with none FAILs at
`max_block_decisions_per_run: 1`.

`PASS` iff all six pass. A failed gate is a normal result with a `nil` error.

### The advisory marker

At `N > 1` checks 5 and 6 carry `advisory: fresh scope`. They are still
evaluated, still carry their real values, and still fail the verdict when they
fail — the marker changes no verdict, asserted by a pair of tests (a genuinely
blocking repetition still FAILs, with the marker present).

The measured justification, from the first sweep and cited in the ADR: anomaly
confidence sits at 0.1786 in every isolated repetition and never moves, against
0.1786 → 1.0000 by repetition 8 when shared. Since
`effectiveAnomaly = Anomaly.Score * Anomaly.Confidence`, trust is barely
penalized, nothing blocks, and checks 5 and 6 read `0` in all ninety comparisons
of every isolated configuration. **They did not fail spuriously; they could not
fire at all.** An unmarked passing check reads as evidence it is not.

Not marked at `N = 1`, asserted — at `N = 1` there is one profile and the
question is as answerable as `eval compare` makes it.

### Saturation

512 distinct identities across the whole execution, following task 054's figure
because the evidence is the same kind. The 512th is admitted, the 513th refused,
the evidence permanently incomplete, and **the gate refuses incomplete
evidence** — the same refusal `CompareBehaviorSnapshots` makes for
`ErrIncompleteSnapshot`, reported as incomplete and distinct from a gate
failure. A confident count from truncated evidence is the failure mode task 054
named as the worst thing its type could get wrong.

### N = 1 reproduces task 056 exactly

At `N = 1, k = 1, j = 0`, "present in ≥1 of 1 candidate runs and ≤0 of 1
reference runs" is literally task 054's `reference == 0 && candidate > 0`. The
six checks reduce to task 056's five and the verdict is identical to
`EvaluateEvaluationGate` on the single pair. Asserted **against the real gate**,
not against a restated expectation — this is the test that fails if anyone later
"simplifies" the presence predicate, and it is what makes the new gates an
addition rather than a redefinition.

The guidance at every N is the same thing: `k = 1, j = 0` is set semantics at
`N = 5` exactly as at `N = 1`, asserted separately, because that is the property
that makes the guidance safe for a workload whose variance nobody has measured.

### Result document

Every field 078 § "Result document" requires, JSON field names published in
`docs/compatibility.md` in slice 5:

```text
scenario      name, runs (N), k, j
behaviors     every observed behavior — identity, descriptor, both counts,
              classification; not only the classified ones
gate          all six checks in the stable order, each with actual value,
              limit or rule, and pass/fail; 5 and 6 carry the advisory marker
              at N > 1
verdict       pass | fail, closed vocabulary
producers     CLI version and control-plane version
identity      the scenario execution id, and the 2N evaluation run ids
```

Content-free throughout: identities, descriptors, counts, thresholds, verdicts.
Descriptors are **producer-supplied strings** and the documentation says so, for
task 079's benefit.

---

## 4. Schema

### Persisted, because the reference default requires it

Settled by the brief rather than left open: "the scenario execution and its
repetitions **are persisted** (the reference default needs them)". The design
below is unchanged — it reached the same conclusion for the same reason.

`--reference last` has to find a prior execution, so the execution and its
repetitions are durable state. Two tables, forward-only, version-gated, in both
backends:

```text
scenario_executions    id, scenario_name, agent_id, runs, k, j,
                       status, created_at, completed_at
scenario_repetitions   execution_id, side, repetition_index,
                       evaluation_run_id, behavioral_profile
                       PK (execution_id, side, repetition_index)
```

`SchemaVersion` goes **5 → 6** (`platform/sqlite.go:38`). The existing
version-gated `migrate` gains a `schemaVersionV5` case creating both tables;
`schemaTablesV6` lists them so `requireTables` verifies an already-migrated
database. PostgreSQL takes the identical step, and the existing
cross-backend conformance tests cover both.

`side` is a closed vocabulary (`reference` | `candidate`), constrained in the
schema rather than by convention.

`repetition_index` lives **here and only here**. It is correlation metadata and
obeys the `SessionID`/`EvaluationRunID` rule: it never enters `StableFeatures`,
the fingerprint hash, or any `DecisionRecord` field — asserted by the structural
scan the task already uses.

`behavioral_profile` is recorded so slice 6 can assert from the evidence that
each repetition ran under a distinct profile, which is acceptance criterion 15.

### The drill

Backup → restore → upgrade, the same drill tasks 065, 066 and 074 each ran:

1. a v5 database opens at v6 after migration, with every pre-existing row intact;
2. a v6 database opened by v6 verifies rather than re-migrates;
3. a v6 database opened by a v5 binary refuses, rather than silently operating
   against a schema it does not understand;
4. a backup taken at v5 restores and migrates to v6;
5. both backends, asserted by the existing conformance harness.

---

## 5. Bounds

| Bound | Value | Enforced |
|---|---|---|
| `runs` | required, `1 <= N <= 64`, no default | parse time, usage error `2` naming the field |
| `k` | required, `1 <= k <= N`, no default | parse time, `2` naming the field |
| `j` | required, `0 <= j < k`, no default | parse time, `2` naming the field |
| distinct identities | 512 across the execution | collector; gate refuses incomplete |
| repetitions in memory | ≤ 64 diffs | follows from `runs` |

Every gate limit is required and none has a silent default — task 056's rule:
zero is a meaningful strict value for a maximum, so zero cannot also mean
"unset", and the failure direction of an inferred default is toward looking
permissive.

### One crashing repetition aborts the scenario

Exit `3`, no gate verdict, remaining repetitions not executed. Asserted three
ways: the exit code, the absence of a verdict, and the number of repetitions
actually executed.

A crash is `3` and never `1`. A broken workload is not a behavioral regression;
N repetitions give N chances to get that confusion wrong, which is why the spec
restates it and why the test asserts it.

### Minimum-evidence gates cannot be configured away

Checks 1–3 take no limit from the scenario file. There is no field that relaxes
them and no value that disables them. A scenario that ran nothing satisfies
every maximum, which is the fail-open shape ADR 0029 § 2 added them to close.
One empty repetition out of N fails check 3 rather than being averaged away by
N − 1 healthy ones — asserted.

### Exit codes

```text
0   gate PASS
1   gate FAIL
2   usage — malformed scenario, missing or out-of-range field, before anything runs
3   operational — API, network, a crashing workload, a missing or mismatched reference
```

---

## 6. Scenario file

Schema-versioned YAML through the `config` package's loader conventions — the
existing surface, not a second configuration mechanism. Per `.claude/rules/go.md`
`go.yaml.in/yaml/v3` is importable **only** from `config`, so the scenario type
and its loader live there and the runner consumes a parsed value.

```yaml
version: "1"
name: support-login
runs: 5

reference:
  command: [python, agent.py]
  env: { AGENT_MODE: reference }

candidate:
  command: [python, agent.py]
  env: { AGENT_MODE: candidate }

gate:
  added_candidate_presence_minimum: 1      # k — guidance, see § 0
  added_reference_presence_maximum: 0      # j
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
```

Every field required, no silent defaults, all limits named in the file.
Unknown fields are refused — a misspelled limit that parsed to its zero value
would be strict by accident, which is the right direction only by luck.

A scenario file is executable configuration: it names a command, and it is read
from the developer's own repository under their own privileges, exactly as a
Makefile is. The guide says so plainly rather than implying a sandbox this task
does not build.

---

## 7. Slices

| # | Content | Gate |
|---|---|---|
| 1 | `GET /v1/evaluation-runs/{run_id}/behaviors` — bounded, paged as task 065; **no fidelity field** (task 081), and the doc says why rather than guessing | full |
| 2 | `trustvian dev --behavioral-profile <ref>`, additive, defaults to the candidate; baseline file lock follows the profile | full |
| 3 | scenario file + loader in `config`; the runner: 2N repetitions, each its own run and profile, correlated into one execution | full |
| 4 | `RepeatedEvaluationEvidence`, the six checks, classification, advisory marking, the route, schema v6 | full |
| 5 | result document, exit codes, scenario guide, `docs/platform-cli.md`, compatibility rows, ROADMAP, task status + index, CHANGELOG, ADRs; delete these notes | full |
| 6 | end-to-end with the real binaries | full |

Gate after every slice: `make check`, `make check-platform-boundary`,
`make check-modules`, and platform and processor tests with `GOWORK=off`.

### Slice 1's fidelity field

The route returns identity, descriptor and observation count, and **not**
fidelity. 075 shipped the indicator but did not persist it per behavior, which
needs a forward-only schema step in both backends — that is task 081. The route
documentation states this rather than leaving a consumer to guess a tool name
from its shape, which is the guess the indicator exists to remove.

**A related gap, found while measuring in the companion repository and worth
recording here because it affects what 081 has to do:** fidelity does not reach
the control plane through the Collector ingest path at all today.
`platform/httpapi/dto.go:299` accepts `fidelity` on the ingest envelope, but
`processor/internal/evaluation/client.go:255`'s envelope has no such field, and
`trustvian.DecisionRecord` carries no `Attributes`, so the value computed at
`processor/mapping.go:124` is dropped before the wire. Every record ingested
through the Collector reads `transport`, including semantic ones. That is task
081's problem, not this one's, and slice 1 is correct to omit the field — but
081 will find it has two gaps to close, not one.

### Slice 2's baseline lock

`dev` derives the profile from the candidate at `cmd/trustvian/dev_identity.go:153`
(`identity.Profile = identity.Candidate`). The flag overrides that one
assignment and nothing else. `dev` already keeps one baseline file per profile
and refuses two concurrent writers of one, so N sequential repetitions produce N
files and no contention — the lock follows the profile because the file already
does.

### Slice 6's end-to-end tests

```text
unchanged fixture workload, runs: 3, k: 1, j: 0             PASS
candidate gains one tool-level behavior in 3/3 runs, k: 1   FAIL, 0/3 → 3/3
runs: 1, k: 1, j: 0                                         identical to eval compare
```

The third is asserted against the real `EvaluateEvaluationGate`, not a restated
expectation.

The workload is `processor/cmd/agent-producer`, extended if needed — it already
emits GenAI `execute_tool` spans with `gen_ai.tool.name` (task 075, slice 4),
which is exactly what "one tool-level behavior" requires, and it is deterministic
and model-free so CI needs no model.

**The spec asks for more than slice 6 can honestly deliver, and says so itself:**

> An unchanged workload compared with itself at N runs PASSES ... Driven by a
> workload whose behavior set genuinely varies between executions, not a
> deterministic stub — a stub would pass whatever the thresholds were and prove
> nothing.

A deterministic fixture is what CI can run. The "genuinely varies" half needs
the model-driven sweep that does not exist (§ 0). So slice 6 delivers the
deterministic tests and the documentation records which property is proven by
test and which waits for the re-run — rather than letting a passing stub imply
a claim it cannot support. The `k` and `k+1` threshold pair *is* provable
deterministically, by injecting a behavior into exactly k of N candidate runs,
and that test is in slice 6.

---

## 8. What I need decided

1. **`--reference <scenario-execution-id>` rather than the brief's
   `--reference-run`** (§ 1). A flag naming one evaluation run cannot supply N
   reference repetitions, and would break the first time somebody wrote
   `runs: 3`.
2. **The `k`/`j` guidance stays `k = 1, j = 0`, citing the first sweep** (§ 0),
   because the re-run does not exist. The alternative is to run C2 in the
   companion repository first — that is a real option and it is yours to take;
   it costs roughly two hours of model time and needs the C1 design approved.
3. **Slice 6 proves the threshold pair deterministically and documents the
   "genuinely varies" half as pending** (§ 7), rather than letting a fixture
   stand in for a property it cannot exhibit.
