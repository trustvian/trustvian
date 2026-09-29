# 083 — Behavioral Layer Identity and Display Classification

Status: **specified; partially implemented.** The classification and rendering
half is implemented. **The counting correction is deferred** and depends on
[084](082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary)
— see [§ What is deferred](#what-is-deferred-and-why).
Milestone: `v1.0`
Depends on: [075](075-ai-semantic-telemetry-normalization.md) (implemented)
Blocks: [078](078-behavioral-scenario-suites.md)'s threshold re-run — its k-of-N
counts are counts of behaviors, so the counting semantics must be settled first
Planned in: [082 § item 083](082-agent-inspection-and-evaluation-depth.md#083--behavioral-layer-identity-and-display-classification)

## Objective

Make behavioral counting across tool and transport instrumentation **explicit
and defensible**, and render model calls, tool calls and outbound requests
clearly — without changing what a behavioral identity is.

Two halves, and only one of them can be built today. That is the task's
central finding, and it is verified rather than asserted.

## Three words this task keeps apart

The plan for this work used "behavior" for three different things, which is how
the counting question became confusing. They are now separate:

| Term | What it is | Owner |
|---|---|---|
| **Observation** | One observed action: one span in, one `Event`, one `Result`, one `DecisionRecord` out | the engine, per call |
| **Behavioral identity** | The `StableFeatures` tuple and the `Fingerprint` derived from it — *what kind of action this was* | the engine (`internal/fingerprint`) |
| **Counted change** | What a comparison reports as added or removed, and what `max_added_behaviors` is measured against | the control plane (`platform`: `BehaviorDiff`, `EvaluationGatePolicy`) |

The defect is in the third, and **only** the third. One act observed at two
instrumentation layers is:

- **two observations** — correct, and both are real evidence;
- **two behavioral identities** — correct, and deliberately kept so;
- **two counted changes** — *misleading*, because a developer who allowed one
  added behavior allowed half of one act.

Every earlier statement of this problem read as though identity were wrong.
It is not. Counting is.

## The motivating case, reproduced

The reference workload gained the ability to export a customer record. A real
producer emits two spans for it: the tool call, and the HTTP request the tool
made as its child.

Reproduced end to end through the real processor and the real engine
(`processor/layer_test.go`):

```text
one act produced 2 records
  [0] fingerprint=e7528b283e002e05  category=tool  name="export_customer"  target=""
  [1] fingerprint=6d9aa49abdab01a3  category=http  name="POST"   target="export.localhost"
```

Two facts in that output, both load-bearing:

1. **Two distinct fingerprints**, so a comparison reports `added 2`.
2. **The tool behavior carries no target.** `internal/semconv`'s
   `opExecuteTool` branch sets a category and a name and deliberately leaves
   `TargetName` unset, and the wrapper's span carries no `server.address` for
   the transport fallback to find. A renderer that appends a separator
   unconditionally prints `export_customer →` with nothing after it.

## Why identity is not changed

**Decision, resolving [082's open decision 2](082-agent-inspection-and-evaluation-depth.md#open-decisions).**
Two spans observing one act remain two behavioral identities. Recorded in
[ADR 0047](../../adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md).

The alternative — folding the two layers into one fingerprint — is refused for a
reason that is not about migration cost:

```text
before the fold   tool · export_customer            +  http · POST → export.localhost
after the fold    tool · export_customer  (the transport target is gone)
```

The transport target *is* the security-relevant part. An agent whose
`export_customer` tool starts posting to `attacker.example` instead of
`export.localhost` changes no tool-layer behavior at all. Folding identity would
make that change invisible, which trades a counting annoyance for a detection
hole. It would also reset every affected actor's learned baseline.

So identity stays, and the counting policy is where the fix belongs — which is
also where it can be stated, versioned and configured.

## Why there is no `model` operation category

**Decision, resolving [082's open decision 3](082-agent-inspection-and-evaluation-depth.md#open-decisions).**
No new `event.OperationCategory` value. `OperationCategory` is a
`StableFeatures` dimension, so adding `model` would re-fingerprint every model
call a producer had already been emitting and discard those baselines — to
improve a label. The display need is met by a non-identity classification
instead, which costs nothing and carries strictly more information than a
category could.

## What is implemented

### A behavioral layer classification, carried the way fidelity is

`semconv.Layer` is a closed vocabulary saying **which instrumentation layer
supplied the operation identity**:

```text
model        the producer named a model or embedding invocation
tool         the producer named a tool, agent or workflow invocation
retrieval    the producer named a retrieval or rerank against a data source
transport    no convention named the operation; identity came from HTTP, DB or RPC
             (empty)  not classified — this Event never passed through an adapter
```

It is derived in `internal/semconv`, the one table both adapters call, and it
rides exactly where `trustvian.fidelity` rides: the outbound span attribute
`trustvian.behavior.layer`, the ingest envelope beside `fidelity`, and the
realtime observation. **It is never persisted** — per-behavior persistence is
[081](082-agent-inspection-and-evaluation-depth.md)'s, for the same reason
fidelity's is, and a comparison delta therefore carries no layer.

**`retrieval` is a fourth value rather than three**, because `external`
currently means both "a model was consulted" and "a document store was
queried". Collapsing retrieval into `model` would assert a model call that did
not happen; collapsing it into `transport` would deny a convention that did
match. Four values is the smallest honest vocabulary.

**One gate, not two.** A layer is claimed exactly when fidelity is `semantic`
— that is, when the convention supplied the operation *name*. A GenAI span
whose category matched but whose identity attribute was missing keeps today's
transport mapping and is classified `transport`, because transport is what its
behavior was actually derived from. Reusing fidelity's gate is what stops the
two indicators from ever disagreeing about one span.

### Honest rendering

- A model call, a tool call and an outbound request are distinguishable in the
  live view and the terminal dashboard without reading attribute names.
- A behavior with no target renders **without a dangling separator**.
- An unclassified behavior renders as unclassified, never as `transport` and
  never as blank.

### The counting rule, stated

`max_added_behaviors` counts **behavioral identities**, and one act observed at
two instrumentation layers contributes two. That was already true and was
nowhere written down; it is now stated in `docs/OPENTELEMETRY.md`, in
`docs/cli-guide.md`'s flag reference, and in the CLI's own flag help.

Stating it is not the same as fixing it. It is the honest interim: a team
setting a budget can now read what the budget counts.

## What is deferred, and why

**The counting correction is not implemented, and this task is not complete.**

A fold needs to know that the HTTP observation is a child of the tool
observation. Three verified facts make that impossible in this slice:

| Fact | Verified by |
|---|---|
| `ParentSpanID` is read **nowhere** in this repository | `grep` over all non-test source; no call site exists |
| `event.Context` and `DecisionRecord` carry no parent identity, so the counting layer cannot correlate | `processor/layer_test.go`'s `TestParentIsUnreachableAtCountingTime` serializes a record and asserts no parent field appears |
| The processor is stateless per span, and a child span **completes and exports before its parent** | `processor.go`'s `ConsumeTraces` loop; OpenTelemetry span lifetime |

So the fold's prerequisite is
[084](082-agent-inspection-and-evaluation-depth.md#084--correlation-and-operational-evidence-on-the-record-boundary)
— parent span identity on the evidence boundary — and 084 alone is **not
sufficient**. Counting happens in `platform`'s `BehaviorDiff`, over
per-fingerprint snapshots that hold no parent relation, so a correlation-aware
counting policy additionally needs:

1. **a bounded correlation structure.** Mapping observation → parent →
   behavioral identity is per-span state, and the existing 512-behavior cap does
   not bound span count. A bound has to be chosen, and saturation stated.
2. **an out-of-order rule.** The child arrives first. A counting policy that
   folds on seeing the parent must handle a parent that never arrives.
3. **a decision about what the fold reports.** Whether a folded act is reported
   as one counted change with two contributing identities, or as two changes with
   a stated relationship, is a product decision and not a mechanical one.

None of those is guessed at here. `TestCountingFoldIsNotImplementedYet` records
the gap in the suite and **fails the moment it closes**, the way
`TestFidelityIsNotPersistedYet` does for 081.

## Non-goals

- **No identity change**, no new `OperationCategory`, no fingerprint change, no
  baseline migration. A test asserts identity did not move.
- **No suppression of transport observations.** A dropped span is evidence
  destroyed. Nothing here deletes an observation to make a count smaller.
- **No parentage inferred** from timestamps, adjacency, span names or similar
  names. Missing correlation produces the documented fallback — two counted
  changes, as today — never a guess.
- **No display metadata in identity.** The layer never enters `StableFeatures`,
  a fingerprint or a baseline key.
- **No demo-specific anything.** No tool name, destination or workload is named
  in non-test source.
- **No persistence**, no comparison-delta layer, no 084, no 085.
- **No recomputation in an adapter.** The CLI, the WebUI and the TUI render what
  the control plane sent and compute no count or verdict.

## Compatibility

Additive throughout, and the existing guarantees are unweakened.

| Surface | Change | Rule it follows |
|---|---|---|
| `event` package | New exported `Layer` type, four constants, `AttrLayer`; `Normalization` gains `Layer` | Additive public API |
| Outbound span attributes | New `trustvian.behavior.layer` | Additive enrichment, as `trustvian.fidelity` was |
| Ingest envelope | New optional `behavior_layer` | *Additive envelope fields*, `docs/compatibility.md` |
| Realtime observation | New `behavior_layer` | *New fields — consumers must tolerate unknown fields* |
| `StableFeatures`, `Fingerprint`, `Baseline` | **None** | Asserted by test |
| Persisted schema | **None.** Schema version stays 5 | No migration, so no migration risk |

**Degradation is unchanged.** A producer emitting no agent-oriented convention
gets byte-identical behavioral results, `transport` as its layer, and the same
fingerprints as before. The existing degradation suites pass unmodified.

## Tests

| Case | Test | Where |
|---|---|---|
| The motivating tool-plus-HTTP act | `TestOneActProducesTwoObservationsAndTwoIdentities` | `processor` |
| The deferral, failing when it closes | `TestCountingFoldIsNotImplementedYet` | `processor` |
| Parent unreachable at counting time | `TestParentIsUnreachableAtCountingTime` | `processor` |
| Absent / unlinked correlation | `TestMissingCorrelationFallsBackToTwoCountedChanges` | `processor` |
| The same tool switching destination | `TestToolSwitchingDestinationChangesTheTransportIdentity` | `processor` |
| One tool, several destinations | `TestOneToolManyDestinationsStayDistinct` | `processor` |
| An unrelated HTTP request | `TestUnrelatedTransportObservationSurvives` | `processor` |
| Nested tool calls | `TestNestedToolCallsEachKeepTheirIdentity` | `processor` |
| Concurrent tool calls | `TestConcurrentToolCallsAreNotConflated` | `processor` |
| A failed tool call | `TestFailedToolCallStillClassifies` | `processor` |
| Incomplete instrumentation | `TestToolSpanWithoutIdentityAttributeIsTransport` | `processor` |
| Transport-only compatibility | `TestTransportOnlyProducerIsByteIdentical` + the existing degradation suites | `processor`, core |
| The wire carries the layer | `TestIngestEnvelopeCarriesTheBehaviorLayer`, `TestLayerMatchesTheOutboundSpanAttribute` | `processor` |
| Vocabulary is closed | `TestLayerVocabularyIsClosed`, `TestLayerAttributeNameIsStable` | `internal/semconv` |
| One gate, so the two indicators cannot disagree | `TestLayerIsClaimedExactlyWhenFidelityIsSemantic` | `internal/semconv` |
| Model separated from retrieval | `TestLayerSeparatesModelFromRetrieval` | `internal/semconv` |
| **Layer is never identity** | `TestLayerNeverReachesBehavioralIdentity`, `TestLayerIsNotOnTheDecisionRecord`, `TestLayerAttributeDoesNotDisturbLearning` | core |
| Layer is never identity, platform side | `TestBehaviorLayerDoesNotEnterBehavioralIdentity` | `platform/httpapi` |
| Reaches the realtime observation, all five states | `TestBehaviorLayerReachesTheRealtimeObservation`, `TestBehaviorLayerIsAlwaysPresentOnTheWire` | `platform/httpapi` |
| An unknown value is refused | `TestUnknownBehaviorLayerIsRefused` | `platform/httpapi` |
| Rendered honestly, not defaulted | `TestInspectorStatesTheBehaviorLayer`, `TestLiveViewCarriesTheLayerWithoutDefaulting` | `platform/webui` |
| Target-less rendering, no dangling separator | `TestBehaviorLabelHasNoDanglingSeparator`, `TestInspectorRendersMissingTargetAsWords`, `TestDescribeBehaviorIsPresentationOnly/named_tool_with_no_target` | `platform/webui`, `cmd/trustvian` |

**What these do not assert.** They do not assume that adding instrumentation
must leave a count unchanged. It legitimately need not: a tool span carries
evidence a transport span does not, and observing it is new information. What
the suite pins is the *documented* semantics, and the one case where the current
semantics are misleading is pinned as a known gap rather than as correct.

## Acceptance criteria

Against [082's list for this item](082-agent-inspection-and-evaluation-depth.md#083--behavioral-layer-identity-and-display-classification):

| # | Criterion | Status |
|---|---|---|
| 1 | One documented rule decides whether the two layers are one behavior or two, applied in one place | **Met.** The rule is "two, and the counting fold is deferred", stated in `docs/OPENTELEMETRY.md` and derived in `internal/semconv` only |
| 2 | A single change produces a diff whose added count matches the number of changes a developer would name | **Not met — deferred.** Requires 084; pinned by `TestCountingFoldIsNotImplementedYet` |
| 3 | Model, tool and outbound request each distinguishable in a rendered view | **Met** |
| 4 | A behavior with no target renders without a dangling separator | **Met** |
| 5 | No display label reaches `StableFeatures`, a fingerprint or a baseline key | **Met**, proven by test |
| 6 | A producer emitting no convention sees byte-identical behavior | **Met**, existing suites unmodified |
| 7 | If identity changes, a migration is specified and drilled; if not, a test asserts it did not | **Met** — identity does not change, and a test asserts it |

Five of seven. **Criterion 2 is the task's point**, so the task stays open.

## Documentation

`docs/OPENTELEMETRY.md` (the layer table, the counting rule, the deferral),
`docs/compatibility.md` (the additive surfaces),
[ADR 0047](../../adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md),
`docs/cli-guide.md` (what `--max-added-behaviors` counts), the task index, the
roadmap, and `CHANGELOG.md`.

## Open questions left to the counting work

1. **What bounds the correlation structure**, and what does saturation report.
2. **What a folded act reports** — one counted change with two contributing
   identities, or two with a stated relationship.
3. **What happens when the parent never arrives**, beyond the current fallback.
4. **Whether the fold is configurable**, and if so whether a gate limit means
   acts or identities. A limit whose unit depends on a flag is a limit nobody
   can read.
