# alert-webhook

Demonstrates the v0.4 Alert & Notification Foundation
([task 018](../../docs/archive/tasks/v0.4/018-alert-notification-foundation.md))
end to end, entirely through the direct Go SDK — no OTel adapter and no
Collector processor involved anywhere in this program.

A cold-start (never-before-seen) event runs through the normal `Engine`
pipeline, producing an `OBSERVE_ONLY` `Decision` (the only `Decision` the
default, rule-less `Policy` ever produces — see
[the examples index](../README.md#a-note-on-decision)).
One `alert.Rule` matches on anomaly score, not `Decision` — the point
this example exists to make concrete: `Decision` and `Alert` are
different questions
([spec § 18.1](../../docs/archive/project-spec.md#181-why-alert-is-not-decision)).
A routine `OBSERVE_ONLY` decision on a highly novel action can still be
worth an operator's attention, and Alert Evaluation says so
independently of what Decision already decided.

The matching `Alert` is delivered to a signed HTTPS webhook. The
"external system" receiving it is a local `httptest` server this
program starts itself — no network access or external setup required,
matching every other example in this directory — but the delivery
itself is real: a real HTTP POST, a real HMAC-SHA256 signature the
receiver independently recomputes and verifies before accepting the
payload.

Run it:

```bash
cd examples/alert-webhook && go run .
```

## Real output

```
Decision: observe_only
trust 0.90 (low): identity confidence 0.90, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
Policy: default action (no policy rules configured; observing by default)

Alert matched: severity=high decision=observe_only reasons=[fingerprint never observed for this actor policy default: no policy rules configured; observing by default]
Receiver: signature verified, payload version=1 alert.id=alt_d38fb13c20d0017bdcf4d6698b5271b5
Webhook delivered and signature verified by the receiver.
```

Note `severity=high` alongside `decision=observe_only` — proving the
independence the whole feature exists for. `alert.ID` is randomly
generated per run (via `crypto/rand`), so it differs on every
invocation; everything else above reproduces exactly.

## What this proves

- `alert.Evaluate` reads a `Result` and produces an `Alert` without any
  pipeline stage, `Engine.Analyze`, or `Engine.Observe` awareness of
  alerting — the same "downstream consumer, not a new stage" boundary
  [ARCHITECTURE.md § Relationship to a future Alert & Notification
  layer](../../docs/ARCHITECTURE.md#relationship-to-a-future-alert--notification-layer)
  documents.
- `alert.NewWebhookSink`/`Send` work with zero OpenTelemetry
  involvement — no `internal/otel` import, no Collector, nothing.
- The whole program has no `internal/` import anywhere (verify with
  `grep internal/ main.go`) — `alert.Condition{Decision: ...}` and
  similar fields accept plain string literals
  (`alert.Condition{MinAnomalyScore: 0.8}` here doesn't even need one,
  but a `Decision`-based rule would use `"block"`, not
  `policy.DecisionBlock` — the same untyped-constant pattern
  [ADR 0002](../../docs/adr/0002-public-api-boundary.md) already
  documents for reading `result.Decision == "block"` from outside this
  module), proving `alert`'s types are genuinely usable from a
  separate Go module, exactly as
  [ADR 0007](../../docs/adr/0007-alert-package-is-public.md) decided
  they must be.
