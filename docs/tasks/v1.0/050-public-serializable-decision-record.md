# 050 — Public Serializable Decision Record

**Milestone:** v1.0 — Local-First Behavioral Security Platform ·
**Depends on:** [049](049-platform-architecture-alignment.md) ·
**Blocks:** 052 onward · **First implementation slice of Track B, and one
of the two core changes 049 expects.**

## Objective

Add one stable, explicit, serializable public projection of a single
`Engine.Analyze` outcome: `DecisionRecord`. It is the boundary the future
platform reads, persists, streams, and aggregates — without importing
`internal/*` and without reproducing behavioral logic.

## Why

`Result` is readable from outside the module today, but four of its fields
(`Features`, `Fingerprint`, `Anomaly`, `Trust`) have static types from
`internal/*`. Reading `result.Trust.Score` works; *declaring a variable*,
persisting a row, or defining an API payload does not.

That is fine for immediate inspection and wrong as a long-term boundary.
Persistence, realtime streaming, evaluation aggregation, CLI JSON output, and
the control API all need a type they can name, store, and version. Without
one, the platform's realistic options are to reflect over `Result` — brittle
and dependent on internal field names — or to re-declare the shapes itself,
which is the second behavioral implementation
[ADR 0022](../../adr/0022-core-platform-boundary.md) exists to prevent.

`DecisionRecord` is the third option: one projection, owned by the core,
copied from evidence the engine already computed.

## Scope

- `DecisionRecord` and `ContributorRecord` in the root package, beside
  `Result`.
- `Result.DecisionRecord()` — a pure projection.
- JSON tags on both, with explicit field names.
- Tests: projection correctness through the real engine, novel behavior,
  matched-rule and default-policy explanation, agent correlation fields,
  attribute non-leakage, aliasing independence, JSON round trip, determinism.
- An external-consumer proof in `examples/`, extending the existing pattern.
- Compatibility classification for the new surface.

## Non-Goals

No platform module, Project, Agent, Candidate, EvaluationRun,
BehavioralProfile, Scorecard, hard gates, promotion, local database, HTTP API,
realtime bus, TUI, or WebUI.

No new detection signal, policy semantic, or trust formula. No change to
`baseline.Key`, `Store`, the file-store format, the PostgreSQL schema,
fingerprint identity, `Observe` semantics, baseline admission, or learning
eligibility — **task 051 owns learning-scope isolation and this task must not
solve it incidentally.**

No raw event history. No `Event.Attributes` on the record.

## Technical Requirements

### Naming

The method is `Result.DecisionRecord()`, not `Result.Record()`. `Record` reads
as a verb on a `Result` receiver — "record this result" — and this is a
frozen-at-`v1.0` public API where an ambiguous reading is a cost paid forever.
The mild stutter buys an unambiguous noun that matches the type a platform
author is looking for.

### Contents

| Field | Source | Why |
|---|---|---|
| `EventID`, `Timestamp` | `Event.ID`, `Event.Timestamp` | Associate a decision with an observed event |
| `ActorID`, `ActorType`, `IdentityConfidence` | `Event.Actor` | Attribute the decision, and carry the identity evidence trust consumed |
| `Environment` | `BaselineKey.Environment` | The learning scope the decision was made in; taken from the baseline key rather than the event so it matches what the engine actually used |
| `Behavior` | `Fingerprint.Stable` | The behavioral shape, so a future diff explains `shell.execute` appeared without reversing a hash |
| `FingerprintID` | `Fingerprint.ID` | Stable identity for grouping |
| `AnomalyScore`, `AnomalyConfidence` | `Anomaly` | The two numbers cold start deliberately keeps separate |
| `Contributors` | `Anomaly.Contributors` | Why the score exists |
| `TrustScore`, `RiskLevel`, `ContextRisk` | `Trust` | The outcome and the non-learned penalty that shaped it |
| `Decision`, `PolicyRule`, `PolicyReason`, `MatchedDefault` | `Decision`, `Explanation` | What was decided and by which rule |
| `TraceID`, `SpanID`, `SessionID`, `DelegatedFrom`, `ApprovalStatus` | `Event.Context` | Correlation and evidence — see below |

`Behavior` reuses `StableFeatures`, the public type task 048 introduced. No
second representation of the same six dimensions.

`ActorType` and `ApprovalStatus` keep their `event` package types: both are
public, string-backed, and carry meaning a bare `string` would discard.
`RiskLevel` and `Decision` become `string`, because their types live in
`internal/`.

### Correlation fields — included, deliberately

`TraceID` and `SpanID` connect a decision back to the telemetry that produced
it, which is the difference between an investigable alert and an unexplained
one. `SessionID`, `DelegatedFrom`, and `ApprovalStatus` are already
first-class behavioral evidence — approval compliance and delegation stability
are named scorecard inputs in the roadmap, and a record that omitted them
would force the platform to re-read raw events to aggregate them.

All five are caller-supplied identifiers already exposed on `event.Context`.
None is new surface area; each is `omitempty`.

Including them changes nothing about identity: they are not fingerprint
dimensions, and a test asserts that two events differing only in these fields
produce the same `FingerprintID`.

### Privacy boundary

`Event.Attributes` is **not** projected, and neither is the `Event` value
itself. The record carries security evidence, not producer payload: tool
arguments, prompts, completions, and arbitrary maps stay out by construction —
there is no field for them — and a test asserts a distinctive attribute value
is absent from the marshalled JSON.

This keeps two things small at once: the privacy surface, and the public
compatibility surface.

It makes the record **fixed-shape, not size-bounded**, and the difference is
worth stating. Caller-supplied strings — `EventID`, `ActorID`, operation and
target names, and the correlation identifiers — are not length-limited by
`Event.Validate()` or by this task, and contributor details can embed
caller-controlled behavioral names. What the record excludes is the part that
grows with whatever a producer chose to send. Request and field size limits
are an ingest concern and belong to task 058, not here.

### Serialization

Standard-library JSON, explicit snake_case names, no map fields, no dependence
on internal package names.

**No schema version field.** The compatibility contract already governs this
layer: field addition is additive and allowed in a minor, removal or
redefinition is major. A version number would duplicate that with machinery
nothing currently reads. When the record crosses a network boundary — task
058's local control-plane API and ingest — that transport owns its own
envelope version, exactly as `alert.Envelope` does for webhooks. Adding one
here would be speculative.

### Purity and ownership

`DecisionRecord()` performs no I/O, reads no clock, generates no ID, computes
no score, touches no store, and mutates neither `Result` nor any baseline. It
copies. `Contributors` is deep-copied so neither side aliases the other.

The record is **detached, not immutable**: its fields are exported and it
holds a slice, so a holder can modify one. The guarantee is ownership —
mutating the source `Result` cannot reach a record already produced, and
mutating a record cannot reach the `Result`. Hiding fields to manufacture
immutability would make the type worse at the one job it has, which is being
a plain serializable value.

### Serializable for every successful analysis

`encoding/json` refuses non-finite floats, so "serializable" is only true if
the engine cannot produce one. `WithContextRisk` takes an arbitrary caller
callback, and nothing between it and `Trust` validated what came back:
`trust.clamp01` is built on `min`/`max`, which propagate `NaN`, so a `NaN`
reached `Trust.ContextRisk` and `Trust.Score` and made the record
unmarshallable — while `Analyze` returned success.

`trust.Compute` now treats a non-finite input as invalid rather than as a
position on the scale, and resolves it to whichever end trusts least:
`contextRisk` and the anomaly inputs to `1`, `identityConfidence` to `0`.
Both directions fail closed. `Analyze` still succeeds, because a detector
that refuses to decide when a callback misbehaves is worse than one that
assumes the worst — and mapping an unusable risk reading to `0` would be the
one outcome that turns a caller's bug into a silently permissive decision.

Ordinary out-of-range finite values keep their documented clamp behavior.

Non-finite floats are one of two ways a successful analysis could produce an
unmarshallable record. The other is the timestamp, which the record carries
verbatim. Not every `time.Time` is one `time.Time.MarshalJSON` will encode: a
year outside `[0,9999]` or a zone offset of 24 hours or more is constructible
in Go and refused by RFC 3339, and `Event.Validate()` checked only for the
zero value.

`Event.Validate()` now rejects both cases with `event.ErrInvalidTimestamp`,
which `Analyze` wraps through its existing `trustvian: invalid event:` path.
The zero value keeps `ErrMissingTimestamp` — the zero time is perfectly
encodable, so only the earlier check separates "not set" from "not
encodable". The rule is exactly the standard library's, not a stricter one:
the test derives its expectation from `MarshalJSON` case by case, so a
divergence in either direction fails.

The two guards do not behave alike, and the difference is deliberate rather
than an inconsistency. A non-finite trust input is resolved fail-closed
inside `trust.Compute`, before the `Result` is returned: `Analyze` succeeds
and hands back conservative finite numbers. An unencodable timestamp is
rejected by `Event.Validate()`: `Analyze` fails with an error wrapping
`event.ErrInvalidTimestamp`, and no `Result` exists at all.

```text
non-finite trust input          invalid timestamp
        ↓                               ↓
  trust.Compute                  Event.Validate
        ↓                               ↓
 fail-closed finite         ErrInvalidTimestamp
   normalization                        ↓
        ↓                        Analyze fails
 successful Analyze
```

What separates them is whether a safe substitute exists. Risk has a
direction, so an unusable reading still has an honest answer — assume the
worst — and refusing to decide would be the more dangerous response to a
caller's arithmetic bug. A timestamp has no direction: every replacement
value is a claim about when something happened, and a security record
asserting a time nobody observed is worse than no record. So the timestamp is
refused at the input, where the caller still holds the event and can correct
it.

**`DecisionRecord()` itself sanitizes nothing** either way — it copies, and
that is the whole of its contract. A projection that clamped a score or
rewrote a timestamp would produce a security record whose evidence had been
quietly altered, and would move the failure away from the only place a caller
can still fix it.

### Placement

Root package, beside `Result`. The record is a projection *of* `Result`, and
a new public package for two DTOs would add an import path without adding a
boundary — the opposite of what
[ADR 0002](../../adr/0002-public-api-boundary.md) asks for.

## Tests

- Projection correctness for every field, driven through a real `Engine`
  rather than a hand-built `Result`.
- Novel behavior retains contributors and confidence.
- A matched policy rule projects `PolicyRule` with `MatchedDefault` false; the
  default path projects `MatchedDefault` true.
- An agent-shaped event projects the correlation fields, and those fields do
  not alter `FingerprintID`.
- A distinctive `Attributes` value never appears in the marshalled JSON.
- Mutating `result.Anomaly.Contributors` after projection does not change the
  record, and vice versa.
- JSON marshal/unmarshal round trip is semantically equal.
- The same `Result` projects to an identical record twice.
- An external module compiles and uses the whole path with no `internal/*`
  import.
- A `WithContextRisk` callback returning `NaN`, `+Inf`, or `-Inf` still
  produces a successful analysis whose record marshals, with context risk
  resolved to `1` rather than to `0`.
- Finite out-of-range context risk keeps the documented clamp behavior.
- `Event.Validate()` accepts a timestamp if and only if the standard library
  can marshal it, checked against `MarshalJSON` rather than against a
  restatement of the rule; the zero value still returns `ErrMissingTimestamp`;
  `Analyze` wraps the sentinel; and a record built at each boundary-valid
  timestamp marshals and round-trips.
- `trust.Compute` returns finite, in-range fields for any input, in both
  directions, and deterministically.

## Benchmarks

None. The projection is a fixed number of field copies plus one slice copy
bounded by the contributor count, and it is not on the `Analyze` hot path — it
runs when a caller asks for a record. A benchmark would measure struct
assignment. If the platform later projects at ingest volume, that is where the
measurement belongs.

## Documentation

`docs/compatibility.md` (new matrix rows), `docs/ARCHITECTURE.md` (the record
as the platform's read boundary), `docs/sdk-guide.md` (how a consumer obtains
one), this file, `README.md` in this directory, and `CHANGELOG.md`.

## Acceptance Criteria

- [ ] `DecisionRecord`, `ContributorRecord`, and `Result.DecisionRecord()`
      exist in the root package.
- [ ] No `internal/*` type appears in the public record.
- [ ] `Event.Attributes` and raw payload cannot reach the JSON, proven by
      test.
- [ ] Contributors preserve order, value, weight, and detail, and are
      independently owned.
- [ ] Policy explanation is structured, not a formatted string.
- [ ] An external module produces and marshals a record without importing
      `internal/*`.
- [ ] Projection is pure and deterministic.
- [ ] Every successful `Analyze` produces a record `json.Marshal` accepts,
      including under a pathological `WithContextRisk` callback and for every
      timestamp `Event.Validate()` admits.
- [ ] Non-finite trust input resolves away from trust, never toward it.
- [ ] No platform concept (`ProjectID`, `CandidateID`, `EvaluationRunID`, …)
      appears on the record.
- [ ] Nothing owned by task 051 changed.
- [ ] `gofmt`, `go vet`, `go test ./...`, `go test -race ./...` pass, and both
      nested modules pass with `GOWORK=off`.
