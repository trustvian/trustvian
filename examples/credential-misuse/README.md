# credential-misuse

Ports [docs/use-cases.md § Valid identity, abnormal behavior](../../docs/use-cases.md#valid-identity-abnormal-behavior):
a well-established, high-confidence service account (`order-service`,
`identity_confidence: 0.98`) performs an operation it has never performed
before — a five-second bulk export from `payment-db`.

Unlike the other three ported examples, this use-cases.md section shows a
single event, not a normal/anomalous pair — so there is no
baseline-maturity warm-up loop here, only one `Analyze` call, faithfully
matching the source scenario's cold-start framing.

This is the use case that most directly demonstrates
[the cold-start design](../../docs/ARCHITECTURE.md#cold-start-two-numbers-not-one):
full novelty (`Anomaly: 1.00`) from a trusted identity does not, by
itself, translate into a block.

Run it:

```bash
cd examples/credential-misuse && go run .
```

## Real output

```
Decision: observe_only
trust 0.98 (low): identity confidence 0.98, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
Policy: default action (no policy rules configured; observing by default)
```

`Trust` stays high (`0.98`, matching the actor's `IdentityConfidence`)
because `Anomaly.Confidence` is `0.00` — the fingerprint is brand new, so
its full novelty contributes nothing to `Trust` yet. This is the same
mechanism [docs/use-cases.md](../../docs/use-cases.md#valid-identity-abnormal-behavior)
demonstrates via the CLI's starter policy (which would `ALLOW` this
event): a single novel action from a trusted identity is not treated as
an attack. See [the examples index](../README.md#a-note-on-decision) for
why `Decision` here is `observe_only` rather than `ALLOW`.
