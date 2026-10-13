# Use Cases

Four scenarios mirroring the ones in the project spec's vision
document, each reproduced for real against this repository — every
JSON fixture below was actually run through `trustvian analyze`, and
every terminal block is genuine output, not written by hand. Reproduce
any of them yourself:

```bash
make build
echo '<the json below>' > /tmp/e.json
./bin/trustvian analyze /tmp/e.json
```

Note: `analyze` never writes a baseline, and without
`--storage-config` it has none to read — every event below is scored as
the actor's first-ever observation (see
[CLI Guide § trustvian analyze](cli-guide.md#trustvian-analyze)). What
these scenarios demonstrate is the pipeline's *cold-start* behavior:
how identity confidence, operation sensitivity, and novelty combine
into a decision with no history to lean on. For how the same actor's
`Decision` changes once a baseline exists, see
[Go SDK Guide § watching trust mature](sdk-guide.md#watching-trust-mature).

All four use the CLI's built-in starter policy — block on
high/critical risk, alert on medium, allow otherwise (see
[Policy Guide](policy-guide.md#example-the-clis-starter-policy)).

## API behavioral anomaly

A payment gateway normally calls `payment-service`. One request
instead reaches `admin-service` — the same route, an unexpected
dependency — from a connection Trustvian has much lower identity
confidence in.

**Normal:**

```json
[{
  "id": "evt-6", "timestamp": "2026-01-01T08:00:00Z",
  "actor": { "id": "payment-gateway", "type": "service", "identity_confidence": 0.96 },
  "operation": { "category": "http", "name": "POST /payment" },
  "target": { "name": "payment-service" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 110 }
}]
```

```
Trustvian Behavioral Analysis

Service: payment-gateway
Anomaly: 1.00
Trust:   0.96
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

**Same route, unexpected dependency, lower identity confidence:**

```json
[{
  "id": "evt-7", "timestamp": "2026-01-01T08:00:05Z",
  "actor": { "id": "payment-gateway", "type": "service", "identity_confidence": 0.35 },
  "operation": { "category": "http", "name": "POST /payment" },
  "target": { "name": "admin-service" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 640 }
}]
```

```
Trustvian Behavioral Analysis

Service: payment-gateway
Anomaly: 1.00
Trust:   0.35
Risk:    HIGH

Detected:
  ! fingerprint never observed for this actor

Decision: BLOCK
Reason:   trust score indicates high or critical risk
```

## AI-agent security

A support agent's normal tool is a CRM lookup. A credentials-store
access from the same agent, at lower identity confidence, is a very
different kind of action even though it's structurally the same shape
of event (`Operation.Category = "tool"`).

**Benign:**

```json
[{
  "id": "evt-1", "timestamp": "2026-01-01T09:00:00Z",
  "actor": { "id": "support-agent", "type": "ai_agent", "identity_confidence": 0.9 },
  "operation": { "category": "tool", "name": "search_customer" },
  "target": { "name": "crm-api" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 80 }
}]
```

```
Trustvian Behavioral Analysis

Service: support-agent
Anomaly: 1.00
Trust:   0.90
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

**Suspicious:**

```json
[{
  "id": "evt-2", "timestamp": "2026-01-01T09:05:00Z",
  "actor": { "id": "support-agent", "type": "ai_agent", "identity_confidence": 0.3 },
  "operation": { "category": "tool", "name": "get_credentials" },
  "target": { "name": "credentials-store" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 45 }
}]
```

```
Trustvian Behavioral Analysis

Service: support-agent
Anomaly: 1.00
Trust:   0.30
Risk:    HIGH

Detected:
  ! fingerprint never observed for this actor

Decision: BLOCK
Reason:   trust score indicates high or critical risk
```

This is the mechanism the spec describes as "Trustvian evaluates
runtime behavior instead of relying only on identity" — `Operation`
and `Target` drive the outcome here just as much as `Actor`.

## Service-to-service security

An order service normally calls a known internal RPC dependency.
A sudden external call to a secrets manager, at lower identity
confidence, is flagged.

**Normal:**

```json
[{
  "id": "evt-3", "timestamp": "2026-01-01T10:00:00Z",
  "actor": { "id": "order-service", "type": "service", "identity_confidence": 0.97 },
  "operation": { "category": "rpc", "name": "InventoryService.Reserve" },
  "target": { "name": "inventory-service" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 15 }
}]
```

```
Trustvian Behavioral Analysis

Service: order-service
Anomaly: 1.00
Trust:   0.97
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

**Sudden secrets access:**

```json
[{
  "id": "evt-4", "timestamp": "2026-01-01T10:01:00Z",
  "actor": { "id": "order-service", "type": "service", "identity_confidence": 0.4 },
  "operation": { "category": "external", "name": "GET /secret" },
  "target": { "name": "secrets-manager" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 20 }
}]
```

```
Trustvian Behavioral Analysis

Service: order-service
Anomaly: 1.00
Trust:   0.40
Risk:    HIGH

Detected:
  ! fingerprint never observed for this actor

Decision: BLOCK
Reason:   trust score indicates high or critical risk
```

For a stronger version of this scenario — where a sensitive
destination stays flagged **even once fully familiar** — see
`anomaly.Config.SensitiveTargetFloor` in
[Architecture § design choices](ARCHITECTURE.md#design-choices-worth-knowing-before-you-extend-this)
and the end-to-end proof in
[`engine_test.go`'s `TestAnalyzeSensitiveTargetFloorEndToEnd`](../engine_test.go).
The CLI reaches it through `--anomaly-config` and the
`sensitive_target_floor` key (see the
[Anomaly Configuration Guide](anomaly-config-guide.md#loading-from-a-yaml-file)).
With that guide's `anomaly.yaml`, the same event reports the floor as a
contributor:

```bash
trustvian analyze --anomaly-config anomaly.yaml /tmp/e.json
```

```
Trustvian Behavioral Analysis

Service: order-service
Anomaly: 1.00
Trust:   0.40
Risk:    HIGH

Detected:
  ! fingerprint never observed for this actor
  ! target "secrets-manager" carries a fixed risk floor regardless of baseline familiarity

Decision: BLOCK
Reason:   trust score indicates high or critical risk
```

At cold start the floor does not move `Trust`: anomaly confidence is
zero. It matters once the fingerprint is familiar, which is what the
end-to-end test proves.

## Valid identity, abnormal behavior

The same well-established, high-confidence service account performs an
operation it's never performed before — a bulk export, five seconds
long. This is the case that most directly demonstrates the
[cold-start design](ARCHITECTURE.md#cold-start-two-numbers-not-one):
full novelty (`Anomaly: 1.00`) from a trusted identity does **not**
default to `BLOCK`.

```json
[{
  "id": "evt-5", "timestamp": "2026-01-01T11:00:00Z",
  "actor": { "id": "order-service", "type": "service_account", "identity_confidence": 0.98 },
  "operation": { "category": "db", "name": "EXPORT accounts_bulk" },
  "target": { "name": "payment-db" },
  "context": { "environment": "production" },
  "attributes": { "duration_ms": 5000 }
}]
```

```
Trustvian Behavioral Analysis

Service: order-service
Anomaly: 1.00
Trust:   0.98
Risk:    LOW

Detected:
  ! fingerprint never observed for this actor

Decision: ALLOW
Reason:   risk within tolerance
```

This is the spec's own point, verified: "A valid service account can
still perform unusual actions such as bulk export... Trustvian detects
behavioral deviation even when authentication succeeds" — but detecting
deviation isn't the same as *distrusting on sight*. A single novel
action from a trusted identity is `ALLOW`ed and observed; it's a
*pattern* of such deviations, or one combined with low identity
confidence or a sensitive destination (see the three scenarios above),
that should actually change the decision.

## Bulk baseline seeding

`trustvian baseline build` replays a corpus and reports what it
learned from — this is the mechanism underlying the mature-baseline
behavior shown in the [Go SDK Guide](sdk-guide.md#watching-trust-mature):

```bash
trustvian baseline build corpus.json
```

```
Trustvian Baseline Build

Events processed: 4
Learned:          3
Skipped:          1 (flagged by policy; not learned from)
```

(Corpus: three consistent `svc-payment` DB calls, one unrelated
low-confidence external-call event that gets correctly excluded from
learning. See [`cmd/trustvian/testdata/corpus.json`](../cmd/trustvian/testdata/corpus.json).)
