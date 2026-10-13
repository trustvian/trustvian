# unexpected-dependency

Ports [docs/use-cases.md § API behavioral anomaly](../../docs/use-cases.md#api-behavioral-anomaly):
a payment gateway normally calls `payment-service`. The baseline is
matured with 25 `Analyze`+`Observe` calls against that normal event
(mirroring [docs/sdk-guide.md § watching trust mature](../../docs/sdk-guide.md#watching-trust-mature)),
then one request instead reaches `admin-service` — the same route
(`POST /payment`), an unexpected dependency — from a connection with much
lower identity confidence (`0.96` -> `0.35`) and much higher latency
(`110ms` -> `640ms`).

Run it:

```bash
cd examples/unexpected-dependency && go run .
```

## Real output

```
warm-up  1: confidence=0.00 trust=0.96 decision=observe_only learned=true
warm-up 10: confidence=0.45 trust=0.72 decision=observe_only learned=true
warm-up 20: confidence=0.95 trust=0.91 decision=observe_only learned=true
warm-up 25: confidence=1.00 trust=0.96 decision=observe_only learned=true

Decision: observe_only
trust 0.35 (high): identity confidence 0.35, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
  - transition_deviation: 1.00 (transition from fingerprint 32484698f416a43f has never been observed leading to this one)
  - ngram_deviation: 1.00 (behavioral sequence via fingerprints 32484698f416a43f -> 32484698f416a43f has never been observed leading to this one, for this actor)
Policy: default action (no policy rules configured; observing by default)
```

`transition_deviation` and `ngram_deviation` are reported too: no
warm-up event was ever followed by this fingerprint. `anomaly.DefaultConfig()`
gives both signals a weight of `0`, so they explain the reading but add
nothing to `Anomaly.Score`.

Notice `confidence` climbs from `0.00` to `1.00` across the warm-up loop
as `payment-gateway -> POST /payment -> payment-service` matures (20
observations is `anomaly.DefaultConfig().MinObservations`), and `trust`
dips mid-ramp before settling — the same categorical-novelty-decay
behavior documented in
[docs/sdk-guide.md § watching trust mature](../../docs/sdk-guide.md#watching-trust-mature).
The final event targets `admin-service` instead, which is a completely
different `(Actor, Operation, Target)` fingerprint — none of the 25
warm-up observations transfer to it, so it scores as fully novel again
(`Confidence: 0.00`), and `Trust` collapses to the actor's much lower
`IdentityConfidence` (`0.35`). See
[the examples index](../README.md#a-note-on-decision) for why `Decision`
here is `observe_only` rather than the `BLOCK` shown in
[docs/use-cases.md](../../docs/use-cases.md#api-behavioral-anomaly).
