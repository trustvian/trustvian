# 108 — Legacy HTTP semantic conventions

Status: Proposed
Milestone: `v0.13.0` (proposed)
Depends on: [075](../v1.0/075-ai-semantic-telemetry-normalization.md) (implemented),
[105](../v0.12/105-pipeline-status-surface.md) (implemented)
Found by: task 105's rule 4 measurement (#158, [What shipped](../v0.12/105-pipeline-status-surface.md#what-shipped))

**No code until a maintainer chooses a rollout below.** The change this task
describes alters behavioral identity, and so every affected actor's
fingerprints and baselines. That is a decision, not an implementation detail.

## Developer problem

A Python agent instrumented with `opentelemetry-instrumentation-requests` or
`-httpx`, exporting to a Collector that is not `trustvian dev`, and without
`OTEL_SEMCONV_STABILITY_OPT_IN=http`, sends HTTP client spans that carry only
the pre-stable keys. #158 measured them (instrumentation 0.66b1, SDK 1.45.1):

```text
OTEL_SEMCONV_STABILITY_OPT_IN=''      span name POST   http.method, http.url — no server.address
OTEL_SEMCONV_STABILITY_OPT_IN=http    span name POST   http.request.method, server.address, url.full
```

Trustvian reads only the stable keys, so each of those spans becomes:

| Field | With the opt-in | Without it |
|---|---|---|
| `OperationCategory` | `http` | `rpc` — the fallback |
| `OperationName` | `POST` | `POST` |
| `TargetName` | the host | empty |

Every POST to every host collapses into one behavior, `(rpc, POST, "")`, and a
reader sees RPC calls to nowhere. `trustvian dev` sets the opt-in, so its users
are not affected; anyone running their own Collector is.

## Current state, verified against `main` (`20bc50d`)

| Fact | Evidence |
|---|---|
| Category is `http` only for `http.request.method` | `processor/mapping.go:321-329` (`inferCategory`); mirrored in `internal/otel/otel.go:342-350` |
| Target is `service.peer.name`, then `db.namespace`, then `server.address` | `processor/mapping.go:331-339` (`targetName`); mirrored in `internal/otel/otel.go:355-363` |
| No legacy key is read anywhere | no Go source under `internal/` or `processor/` reads `http.method`, `http.url`, `net.peer.name` or `peer.service` |
| `OperationCategory` and `TargetName` are identity | `internal/features/features.go:29-36` (`StableFeatures`) |
| The baseline key does not include them | `baseline.Key{Scope, ActorID, Environment}` (`internal/baseline/baseline.go:242-246`) |

`peer.service` is itself the legacy name of `service.peer.name`. A producer on
an older SDK that sets `peer.service` gets no target today either.

## What would change, quantified

For one actor, in one environment and learning scope, whose legacy HTTP client spans use `M`
distinct methods across `H` distinct hosts:

| | Before | After |
|---|---|---|
| Fingerprints for those spans | `M`: `(rpc, METHOD, "")` | between `M` and `M × H`: `(http, METHOD, host)`, one per method and host pair actually used |
| Fingerprint IDs kept | — | **none**: every one is new, since two of its six `StableFeatures` dimensions changed |
| Baseline key | `(Scope, ActorID, Environment)` | unchanged; the same baseline now holds new fingerprints |
| Maturity of those behaviors | whatever they had | cold start (`Confidence = 0`), maturing again through the gated loop |
| Admission against the 512 bound | `M` entries | up to `M × H` new entries, while the `M` old ones remain until they age out |
| A comparison across the change | — | `M` behaviors removed and up to `M × H` added. An `added_behaviors` gate fails, and every 078 recorded reference made before the change must be re-recorded |
| Status rule 4 (105) | these spans are not counted (category `rpc`) | they are counted per host; they still carry method-only names, so the rule still rarely fires |

Worked example: an agent calling one model gateway and one tool host with
`POST` and `GET` (`M = 2`, `H = 2`) goes from 2 behaviors to up to 4, all new.

Spans that already carry the stable keys are unchanged, because the stable
keys keep precedence (below). So is every span a convention table matched
(075): the convention's category and target outrank the transport fallback.

The implementation must measure these numbers with a fixture of real legacy
spans before choosing a default, and record them here.

## Proposed keys and precedence

The stable key always wins. A legacy key is read only when every stable key
for the same field is absent.

**Category `http`** (otherwise the existing DB, then RPC, fallback):

1. `http.request.method` (stable, read today)
2. `http.method` (legacy)

**Target name**:

1. `service.peer.name` (stable, read today)
2. `peer.service` (legacy)
3. `db.namespace` (stable, read today)
4. `server.address` (stable, read today)
5. `net.peer.name` (legacy, client spans)
6. the host of `http.url` (legacy, client spans): `net/url` parse, `Hostname()`
   only, lower-cased, at most 255 bytes, and empty when the URL does not parse
   or has no host

**Status code** (task 087, not identity): `http.response.status_code`, then
`http.status_code`. 087 reads both; neither is identity, so it changes no fingerprint.

Legacy **server** spans (`http.host`, `net.host.name`, `http.target`) and legacy
**DB** keys (`db.system`, `db.name`) are out of scope. They are named here so a
reader does not assume they were forgotten. Each needs its own measurement.

### Content

`http.url` is the first content-adjacent attribute this would read. Its path
and query can carry identifiers or secrets. Only the host is kept, and no other
part is retained, logged or reported. A test asserts that a URL with userinfo,
path and query yields the host alone. The privacy tripwire
(`TestContentAttributesAreNeverRead`) gains `http.url`'s path and query as
content that never reaches an Event.

## Rollout options

| Option | What changes | Identity | Cost |
|---|---|---|---|
| **A. Diagnose only** | The processor counts spans that carry `http.method` and no `http.request.method`, reports the count in its status `fidelity` section, and a new status rule says: set `OTEL_SEMCONV_STABILITY_OPT_IN=http` | none | the user changes the producer's environment, and their own baseline moves once, on their schedule |
| **B. Opt in** | A processor setting (for example `legacy_http_semconv: true`), off by default, and the same option in the in-process `internal/otel` path. Enabling it is documented as a baseline reset for affected actors | moves only for whoever enables it | one setting, two code paths to keep in parity |
| **C. On by default** | The precedence above, always | moves for every affected actor on upgrade | a release note that says, in so many words, that affected baselines restart and recorded references must be re-recorded |

**Proposed: A in `v0.13.0`, with B alongside if a user asks for it.** C only
after the measurement above shows how many actors it touches, and never as a
silent change. CLAUDE.md forbids weakening a security policy silently, and a
baseline reset on upgrade weakens every learned decision until it matures
again. The setting in B is a config knob with a current caller: a user who
cannot change the producer.

## Tests (when implemented)

- A table of real legacy spans (`requests`, `httpx`, with and without the
  opt-in) through `EventFromSpan` and `internal/otel`, asserting identical
  `StableFeatures` from both paths.
- The stable key wins whenever both are present.
- Fingerprint IDs for spans carrying stable keys are byte-identical before and
  after.
- The `http.url` host extraction: userinfo, port, path, query, IPv6 and
  malformed URLs.
- Option A: the status count and rule, and that neither changes a fingerprint.

## Acceptance criteria

1. A maintainer has chosen a rollout, recorded here.
2. The measured before-and-after fingerprint counts are recorded here.
3. No fingerprint of a span carrying the stable keys changes.
4. No part of `http.url` other than its host reaches an Event.
