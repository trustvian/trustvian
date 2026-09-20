# 018 — Alert & Notification Foundation

**Milestone:** v0.4 · **Depends on:** v0.1 shipped (needs the stable
`Result` shape Alert Evaluation reads — see
[CHANGELOG.md § v0.4.0](../../../../CHANGELOG.md#v040--alert--notification-foundation)'s
existing dependency note) · **Blocks:** the Reliability stage and the
Additional-sinks-and-governance stage of the Alert & Notification
phase (both explicitly deferred — see Non-Goals)

## Objective

Turn a Trustvian `Result`/`Decision` into a minimal, explainable,
externally deliverable `Alert` — without changing `Decision` semantics
or the existing detection pipeline. This is the "Foundation" stage
named in [CHANGELOG.md § v0.4.0](../../../../CHANGELOG.md#v040--alert--notification-foundation) and specified in full
in [`docs/archive/project-spec.md` §
18](../../../archive/project-spec.md#18-alert--notification-system);
this task file is the first concrete, implementable slice of that
architecture, not a restatement of it.

## Motivation

Today, `Engine.Analyze` produces a `Result` and `Engine.Observe`
conditionally learns from it — nothing downstream of `Decision` exists.
An operator running Trustvian today can only discover a `BLOCK` or
`ALERT` decision by inspecting `Result` themselves (via the CLI's
`analyze` output, a log line, or their own OTel attribute inspection);
there is no way for Trustvian to *tell* an external system that
something notification-worthy happened. `Decision` answers "what
should Trustvian do about this behavior"; nothing today answers
"should this be communicated externally, and to what." Per spec §18.1,
these are genuinely different questions — conflating them would force
every `Decision` to carry delivery concerns it has no business knowing
about, or force every notification integration to reimplement policy
evaluation. This task adds the minimal layer that answers the second
question, strictly downstream of the first.

## Scope

```
Event → Features → Fingerprint → Baseline → Anomaly → Trust → Policy → Decision
                                                                            ↓
                                                                  Alert Evaluation
                                                                            ↓
                                                                          Alert
                                                                            ↓
                                                                       AlertSink
                                                                            ↓
                                                              Generic HTTP Webhook
```

No new pipeline stage. Nothing above `Decision` changes. Alert
Evaluation is a pure function of an existing `Result` value, exactly
like `Result.Explain()` is today — it reads, it does not write back.

- **Alert domain model.** A view assembled from an existing `Result`,
  not a parallel model — see Domain Model below for the concept list
  and the explicit "what NOT to duplicate" rule.
- **Severity.** A new, genuinely new concept `Alert` introduces —
  `INFO`/`LOW`/`MEDIUM`/`HIGH`/`CRITICAL` — distinct from anomaly
  score, trust score, risk level, and decision (see Domain Model).
- **Alert Evaluation.** A minimal, deterministic matcher over an
  existing `Result`, following `internal/policy.Condition`'s exact
  discipline: flat AND-of-optional-fields, first-match-wins rules, no
  combinators (see Domain Model).
- **`AlertSink` boundary.** The conceptual interface every notification
  provider implements — `Send(ctx context.Context, alert Alert) error`
  — with exactly one implementation in this stage: a generic HTTP
  webhook sink (see Architecture for why this interface's *location*
  is a genuine open question, unlike `policy.Policy`/`anomaly.Config`).
- **Versioned webhook payload contract.** A stable, versioned JSON
  shape for the one delivery mechanism this stage ships (see Domain
  Model).
- **Webhook security.** HMAC signing, a timestamp header, a bounded
  request timeout, bounded payload size, and documented secret-handling
  expectations (see Security).

**OSS / Enterprise boundary for this stage** (per spec § 18.16 — cross-
referenced, not re-argued): everything listed above is OSS-core scope.
Centralized alert management, advanced/combinator rules, multi-tenant
notification config, routing/escalation, alert history, delivery
observability, RBAC, audit, and centralized Slack/Teams/PagerDuty
integrations are Trustvian Control/Enterprise territory — not because
they're hard, but because they're governance/centralization concerns
this OSS core has never carried for any other feature (see
[ADR 0002](../../../adr/0002-public-api-boundary.md) and
[ROADMAP.md § Control / Enterprise phase](../../../ROADMAP.md#organizational-scale)
for the same line drawn elsewhere in this project).

## Non-Goals

- **No boolean combinators or expression language for alert rules.**
  Flat AND-of-optional-fields only, matching `policy.Condition`'s own
  first-version discipline (spec § 18.4). AND/OR/NOT, regex, and
  scripting are explicitly future evolution, not a v1 requirement.
- **No `Policy` reuse or coupling.** Alert Evaluation does not import
  `internal/policy` and does not feed back into `Policy.Evaluate` or
  `Decision`. It reads the same `Result` a `Decision` was already
  computed from; it is a second, independent consumer of that value,
  not a second stage of decision-making. Alert Evaluation may define
  its *own* condition/rule types shaped like `policy.Condition`'s, by
  precedent, not by import.
- **No provider SDKs.** No Slack SDK, Teams SDK, PagerDuty SDK, Kafka,
  SMTP, Redis, or database-backed delivery queue. The generic webhook
  is the only transport this stage ships (spec § 18.7–18.8); every
  other provider is a future `AlertSink` implementation, not a core
  dependency.
- **No delivery reliability subsystem.** No retry, no exponential
  backoff, no delivery-state tracking, no idempotency, no
  deduplication, no cooldown, no suppression, no escalation, no
  dead-letter/failure handling, no delivery observability. This is the
  explicitly separate **Reliability** stage (spec § 18.11–18.12,
  [CHANGELOG.md § v0.4.0](../../../../CHANGELOG.md#v040--alert--notification-foundation)) — it depends on this
  Foundation stage shipping first and is not scoped here. A webhook
  `Send` call in this stage either succeeds or returns an error; nothing
  retries it.
- **No incident-management domain.** An `Alert` is one
  notification-worthy event, not a grouped investigation (spec §
  18.13). No alert history, no incident grouping.
- **No multi-tenancy, RBAC, or Control integration.** Local,
  file/config-based rule configuration only, matching this stage's OSS
  scope above.
- **No new task-numbered work for Reliability or Additional
  sinks/governance.** Per this roadmap's "small vertical slices"
  principle, those stages get their own task file, with their own
  number, once this one ships and its real shape is known — not
  reserved speculatively here.

## Architecture

**Pipeline diagram** — added downstream of `Decision` only, per
Scope's diagram above. `internal/anomaly`, `internal/trust`,
`internal/policy`, and `Engine.Analyze`/`Engine.Observe` are not
modified by this task in any way; a diff to this task's implementation
touching those packages is out of scope by construction, not an
implementation detail left to judgment.

**Boundary guarantees this task must preserve** (mirrors spec §
18.14–18.15 exactly):

- The core detection engine (`event`, `internal/features` through
  `internal/policy`, root `Engine`) does not gain an HTTP dependency.
- The core detection engine does not gain a Slack/Teams/PagerDuty/
  Kafka/Redis dependency, directly or transitively.
- Alert delivery never influences `Decision` generation — Alert
  Evaluation is read-only over `Result`, the same guarantee
  `Result.Explain()` already has.
- OTel is not required for Alert delivery — a caller using the direct
  Go SDK (no OTel adapter, no Collector processor involved at all)
  must be able to produce and deliver an `Alert` end to end, exactly as
  `internal/otel` remains optional for the core pipeline today (see
  [ARCHITECTURE.md § Relationship to a future Alert & Notification
  layer](../../../ARCHITECTURE.md#relationship-to-a-future-alert--notification-layer)).

**Open architecture question this task must resolve, not defer:**
where `Alert`/`AlertSink` live is a genuinely different question than
where `policy.Policy`/`anomaly.Config` live, and should not be
defaulted to `internal/` by unreflective precedent. [ADR
0002](../../../adr/0002-public-api-boundary.md) keeps `Policy`/`Config`
internal specifically because *no external consumer needs to construct
one today* — reading `Result`'s fields from outside the module already
works without importing anything. `AlertSink` breaks that precedent on
its own terms: its entire purpose (spec § 18.6 — "a new provider can be
added by implementing one method against a stable `Alert` shape,
without the Notification Dispatcher... changing") is for code the
Trustvian module does not control to implement
`Send(ctx context.Context, alert Alert) error`. A Go method signature
naming a type requires importing the package that defines it; Go's
`internal/` rule makes that impossible from outside this module. If
`Alert` stays `internal/`, no third-party sink can ever exist — the
one thing this stage exists to enable. This task's implementation must
therefore either (a) define `Alert` and `AlertSink` in a new public
package (a sibling to `event`, following that package's own precedent
for "the one type every caller must touch to use this feature at all"
— see [ARCHITECTURE.md § The internal/ boundary — and why event isn't
inside it](../../../../.claude/rules/architecture.md#the-internal-boundary--and-why-event-isnt-inside-it)),
or (b) place them in the root package alongside `Result`. Decide
between these two at implementation time against the real shape of the
code, and record the decision in a new ADR — this is exactly the kind
of non-obvious "why did we do this" decision
[CLAUDE.md](../../../../CLAUDE.md) asks for one. What must **not** happen is
defaulting `Alert`/`AlertSink` into `internal/alert` by habit and
discovering the third-party-sink use case is structurally impossible
only after the fact.

**Illustrative future package direction** (spec § 18.17, not a
commitment): `internal/notification/webhook` for the webhook sink
implementation itself is a reasonable home regardless of where `Alert`
lands, since the webhook sink's own types (HTTP client config, HMAC
signing) have no third-party-construction requirement — the same
"internal is default; public is deliberate" reasoning as everywhere
else in this codebase applies there without the tension named above.

## Domain Model

**Alert domain concept** (spec § 18.2 — concepts, not a frozen struct;
this task's implementation fixes the concrete Go shape against the
real `Result` type at that time, not speculatively here per
[.claude/rules/architecture.md](../../../../.claude/rules/architecture.md)'s
"add the interface when a second one exists, not before" discipline
applied to data shapes too):

- an alert identifier (distinct from `Fingerprint.ID` and from
  `Event.ID` — an `Alert` is a new thing, not a renamed existing one)
- a timestamp
- severity (new concept — see below)
- the `Decision` that produced it
- `Trust.Risk`
- `Trust.Score`
- `Anomaly.Score`
- `Event.Actor`
- `Event.Target`
- the fingerprint identifier (`Fingerprint.ID`)
- reasons/explanation — reuses `Anomaly.Contributors`/
  `Result.Explain()`'s existing material, not a new explanation engine
- open metadata for extension (a `map[string]string` or `map[string]any`,
  matching `event.Event.Attributes`'s existing precedent)

**Explicitly not duplicated:** a second copy of trust score, anomaly
score, risk, decision, actor, target, or explanation logic. `Alert` is
assembled *from* a `Result`, the same relationship
`policy.Input`/`Condition` already have to `features.StableFeatures`
and `trust.Trust` — a read-shaped view, not a parallel model that can
drift from the value it was computed from.

**Severity** (spec § 18.3): `INFO` / `LOW` / `MEDIUM` / `HIGH` /
`CRITICAL`, a new string-backed enum type following the exact pattern
`policy.Decision` and `trust.RiskLevel` already establish (typed
string constants, a `Valid()` method). Severity is **derived by Alert
Evaluation from operator-configured rules**, not computed by any
existing pipeline stage and not silently inferred by a fixed mapping
table — an unconfigured deployment produces no severity mapping at all
(mirrors `anomaly.Config.SensitiveTargetFloor`'s "empty means inert,
not a hidden default" precedent). Explicitly, by definition:

```text
severity != risk
severity != anomaly score
severity != trust score
severity != decision
```

A `BLOCK` at `RiskCritical` against a known-sensitive target might
always be `CRITICAL` severity by an operator's own rule; a `CHALLENGE`
at `RiskMedium` on a first-time integration might reasonably be `INFO`.
No built-in mapping ships in this stage — every severity assignment
comes from an explicit, operator-authored rule (see below), so there is
no hidden magic mapping to audit.

**Alert Evaluation matcher** — a new type shaped exactly like
`policy.Condition` (flat, AND-of-optional-fields, zero value matches
everything), matching on:

```text
Decision
Trust.Risk           (as a minimum-severity-style floor, mirroring
                       Condition.MinRiskLevel's AtLeast semantics)
Event.Actor.Type
Event.Target.Category
Anomaly.Score         (as a minimum threshold)
Trust.Score           (as a maximum threshold)
```

A matching rule produces an `Alert` carrying the rule's configured
`Severity`; a non-matching rule produces nothing. First-match-wins over
an ordered rule list, exactly like `policy.Policy.Evaluate` — but this
is a structurally independent type, not a reuse of `policy.Policy`
itself (see Non-Goals: no `Policy` coupling). No default/fail-closed
requirement analogous to `policy.Policy`'s applies here: an
unconfigured Alert rule set means simply "no alerts," which is a safe,
inert default (unlike an unconfigured `Policy`, where a fail-open
default would be a security regression) — this is a deliberate,
documented asymmetry with `policy.Policy.Evaluate`, not an oversight.

**`AlertSink` boundary** (spec § 18.6, conceptual only — this task
does not implement it yet, see Non-Goals and Architecture's open
question above):

```go
type AlertSink interface {
    Send(ctx context.Context, alert Alert) error
}
```

**Generic HTTP webhook** (spec § 18.7) is the one `AlertSink`
implementation this stage ships. It performs exactly one action: build
the versioned payload, sign it, POST it, apply a timeout, and return
the resulting error (or nil) — no retry loop, no queue, no state (see
Non-Goals).

**Versioned webhook payload contract** (spec § 18.10 — illustrative,
reconciled against the real `Alert` shape at implementation time, not
binding here):

```json
{
  "version": "1",
  "alert": {
    "id": "alt_...",
    "timestamp": "2026-09-08T12:00:00Z",
    "severity": "critical",
    "decision": "block",
    "risk": "high",
    "trust_score": 0.31,
    "anomaly_score": 0.94,
    "actor": { "id": "payment-service", "type": "service" },
    "target": { "name": "/customers/export", "category": "external" },
    "fingerprint_id": "fp_...",
    "reasons": ["Previously unseen operation", "Abnormal request frequency"]
  }
}
```

A top-level `version` field (not embedded in a URL or header alone)
lets a receiver branch on payload shape without an out-of-band
contract. Bumping it is required for any future breaking change to the
`alert` object's shape — the same "no silent reinterpretation" discipline
[`internal/fingerprint`'s versioned hash](../../../DOMAIN.md#fingerprint)
already established for this codebase.

## Security

Per spec § 18.9, at minimum:

- **HTTPS only.** The webhook sink must refuse to configure or send to
  a plain-`http://` destination — a hard requirement, not a warning,
  matching this codebase's "fail closed" discipline
  ([.claude/rules/security.md](../../../../.claude/rules/security.md)).
- **HMAC request signing**, using a per-destination secret the operator
  configures. The signing key is a secret: never logged, never echoed
  back in an error message or debug output.
- **A delivery timestamp header**, enabling the receiver to enforce a
  replay-window (the specific tolerance window is an implementation
  decision informed by real delivery latency, not fixed here).
- **A distinct delivery identifier**, separate from the alert's own
  `id`, so a receiver can deduplicate retried deliveries even before
  the Reliability stage's own retry logic exists — this stage sends
  once, but the header contract should not need to change when
  retries are added later.
- **A bounded request timeout** on every webhook call — this is a
  security property as much as a performance one: an unbounded call to
  an operator-configured, potentially attacker-influenced destination
  is a resource-exhaustion vector the core engine must not inherit.
- **A bounded payload size.** The JSON payload's size must be bounded
  (derived from `Alert`'s own bounded shape — no unbounded field should
  exist on `Alert` per Domain Model above), so a malicious or
  misbehaving `Result` cannot produce an arbitrarily large outbound
  payload.
- **Destination validation** — the configured webhook URL is validated
  (well-formed, HTTPS, not resolving to a link-local/loopback address
  by default) before first use, consistent with treating an
  operator-supplied destination as a trust boundary, not a blindly
  trusted value.

Illustrative headers (spec § 18.9, not a naming commitment):

```http
X-Trustvian-Signature: sha256=...
X-Trustvian-Timestamp: ...
X-Trustvian-Alert-ID: ...
X-Trustvian-Delivery-ID: ...
```

**Explicitly out of scope for this stage:** a secret-management
platform (KMS/Vault integration) — secrets are configured the same way
every other `Engine` configuration value is today (in-process, via the
Go API), with documentation of the operator's own responsibility to
store them securely. This stage documents configuration expectations;
it does not build secret storage.

**Cross-reference, not re-argued:** this closes
[SECURITY.md § Future: Alert/notification delivery
integrity](../../../SECURITY.md#alertnotification-delivery-integrity)'s
named threat once implemented — that entry gets a real test reference
at implementation time, per its own "Future work" note.

## Performance

Alert Evaluation runs downstream of every `Result`, potentially on
every `Engine.Analyze` call, so it inherits this codebase's existing
hot-path discipline (`.claude/rules/testing.md`'s benchmark
conventions) rather than being treated as "just an integration, doesn't
need to be fast":

- Alert Evaluation itself (the matcher) must be allocation-conscious on
  the common "nothing matches" path, following
  `policy.Condition.Matches`'s existing zero-allocation shape.
- No reflection anywhere in the matcher or payload construction.
- **No JSON marshaling unless a delivery actually occurs.** A `Result`
  that matches no alert rule must not pay any serialization cost.
- **No network call when no `Alert` is generated.** The webhook sink is
  only invoked once Alert Evaluation has already produced a match.
- Bounded memory usage — no unbounded buffering of alerts or payloads
  (consistent with Non-Goals' exclusion of a delivery queue).

No numeric performance target is set here without measurement, per
this codebase's own "no arbitrary formulas"/measure-first discipline
([.claude/rules/testing.md](../../../../.claude/rules/testing.md)) — real
benchmarks (`BenchmarkAlertEvaluate` no-match / match paths, at
minimum) are written and measured as part of implementing this task,
the same way task 017's `hourActivityAlpha` was chosen from measured
data, not asserted up front.

## Tests

At minimum, once implemented:

- `Decision` does not automatically imply `Alert` — a `Result` with a
  non-`ALLOW` `Decision` and no matching alert rule produces no
  `Alert`.
- A matching alert rule produces an `Alert` with the rule's configured
  `Severity`.
- A non-matching rule (every field checked independently, mirroring
  `TestConditionMatches`-style coverage in
  `internal/policy/policy_test.go`) produces no `Alert`.
- Severity is exactly what the matched rule configured — never
  inferred from risk/decision/anomaly/trust by any implicit mapping.
- Webhook payload construction is deterministic: the same `Alert`
  value serializes to byte-identical JSON across repeated calls.
- The webhook payload's `version` field is present and correct.
- HMAC signature verification: a receiver computing the same HMAC over
  the same payload and secret gets the header's value; a tampered
  payload fails verification.
- Webhook timeout behavior: a non-responding destination is aborted at
  the configured timeout, not hung indefinitely.
- Malformed destination/config handling: an invalid URL, a non-HTTPS
  URL, or a missing secret fails configuration/construction rather
  than failing silently at send time.
- Alert Evaluation does not mutate `Result` — the same
  prove-immutability discipline
  [.claude/rules/testing.md](../../../../.claude/rules/testing.md) already
  requires for `Baseline.Observe` applies here: construct a `Result`,
  run Alert Evaluation, assert the `Result` value is unchanged.
- Concurrency/race safety: concurrent Alert Evaluation calls against
  distinct `Result` values (and the webhook sink's concurrent `Send`
  calls) are race-clean under `go test -race`.
- No sensitive internal state leakage: the webhook payload contains
  only the documented `Alert` fields — no raw `Baseline`, no internal
  `Config` values, no signing secret, ever serialized into the
  outbound payload or logged.

## Documentation

To be updated as part of implementing this task, once the real shape
exists (do not pre-write these updates speculatively in this planning
pass):

- [ARCHITECTURE.md § Relationship to a future Alert & Notification
  layer](../../../ARCHITECTURE.md#relationship-to-a-future-alert--notification-layer):
  update from "nothing implements this today" to the real shape,
  including the `Alert`/`AlertSink` package-location decision from
  Architecture above (recorded in its own new ADR).
- [DOMAIN.md](../../../DOMAIN.md): a new `## Alert` section describing the
  domain concept, severity, and the evaluation matcher, matching this
  file's own Domain Model section once implemented.
- [SECURITY.md § Future: Alert/notification delivery
  integrity](../../../SECURITY.md#alertnotification-delivery-integrity):
  promote from "not implemented" to a real entry in the Test Index
  table, with concrete test references.
- [PERFORMANCE.md](../../../PERFORMANCE.md): new benchmark numbers for Alert
  Evaluation's match/no-match paths and webhook payload construction.
- [ROADMAP.md](../../../ROADMAP.md): mark this task done under `v0.4`, and
  update the Alert & Notification phase's remaining-stages section
  (Reliability, Additional sinks/governance) to reflect Foundation
  being complete and available as their dependency.
- [README.md](../../../../README.md): mention the shipped webhook sink,
  matching how `processor/` and the OTel attributes are already
  mentioned there.

## Acceptance Criteria

- `Alert`, severity, and the Alert Evaluation matcher exist, are
  tested per the Tests section above, and pass `go test ./... -race
  -count=1`.
- The `Alert`/`AlertSink` package-location question (Architecture,
  above) has been explicitly decided and recorded in a new ADR — not
  left implicit or defaulted to `internal/` without discussion.
- A generic HTTP webhook `AlertSink` implementation exists, sends the
  versioned payload contract, and enforces HTTPS, HMAC signing, a
  bounded timeout, and a bounded payload size.
- A `Result` that matches no configured alert rule produces zero
  allocations attributable to alert machinery beyond the matcher's own
  (zero-allocation) evaluation, and makes no network call — verified by
  benchmark, not assumed.
- Core detection engine packages (`event`, `internal/features` through
  `internal/policy`, root `Engine`) show no new import of an HTTP
  client, Slack/Teams/PagerDuty SDK, Kafka, Redis, or database driver —
  verified via `go list -deps`, the same verification technique used
  for task 008/009's OTel-isolation claims.
- Direct Go SDK usage (no OTel adapter, no Collector processor) can
  produce and deliver a real `Alert` through the webhook sink,
  end-to-end, without any OTel-related import — verified by an
  integration test or example.
- No retry, backoff, deduplication, cooldown, suppression, escalation,
  incident grouping, alert history, or provider-native (Slack/Teams/
  PagerDuty) sink exists anywhere in the diff — confirmed by review
  against this file's Non-Goals before merge.
- `gofmt -l .`, `go vet ./...`, `go test ./...`, and `go test -race
  -count=1 ./...` all pass.
