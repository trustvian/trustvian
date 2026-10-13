# external-destination

Ports [docs/use-cases.md § Service-to-service security](../../docs/use-cases.md#service-to-service-security):
an order service normally calls a known internal RPC dependency
(`InventoryService.Reserve` on `inventory-service`). The baseline is
matured with 25 `Analyze`+`Observe` calls against that normal event
(mirroring [docs/sdk-guide.md § watching trust mature](../../docs/sdk-guide.md#watching-trust-mature)),
then a sudden external call to a secrets manager (`GET /secret` on
`secrets-manager`), at much lower identity confidence (`0.97` -> `0.4`),
is analyzed.

Run it:

```bash
cd examples/external-destination && go run .
```

## Real output

```
warm-up  1: confidence=0.00 trust=0.97 decision=observe_only learned=true
warm-up 10: confidence=0.45 trust=0.73 decision=observe_only learned=true
warm-up 20: confidence=0.95 trust=0.92 decision=observe_only learned=true
warm-up 25: confidence=1.00 trust=0.97 decision=observe_only learned=true

Decision: observe_only
trust 0.40 (high): identity confidence 0.40, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
  - transition_deviation: 1.00 (transition from fingerprint c0400b6d1425679c has never been observed leading to this one)
  - ngram_deviation: 1.00 (behavioral sequence via fingerprints c0400b6d1425679c -> c0400b6d1425679c has never been observed leading to this one, for this actor)
Policy: default action (no policy rules configured; observing by default)
```

`transition_deviation` and `ngram_deviation` are reported too: no
warm-up event was ever followed by this fingerprint. `anomaly.DefaultConfig()`
gives both signals a weight of `0`, so they explain the reading but add
nothing to `Anomaly.Score`.

The RPC-to-inventory-service fingerprint matures to full confidence over
the warm-up loop, same as the other maturity-loop examples. The secrets
access is a different operation category (`external` vs. `rpc`) against
a different target entirely, so it's scored as a brand-new fingerprint —
`Trust` collapses to the actor's degraded `IdentityConfidence` (`0.40`).
A stronger version of this scenario sets a sensitive-target floor
(`config.AnomalyConfig.SensitiveTargetFloor`, compiled with
`config.CompileAnomaly`), so a sensitive destination stays flagged even
once fully familiar — see
[docs/anomaly-config-guide.md](../../docs/anomaly-config-guide.md). This
example keeps the default, empty floor. See
[the examples index](../README.md#a-note-on-decision) for why `Decision`
here is `observe_only` rather than the `BLOCK` shown in
[docs/use-cases.md](../../docs/use-cases.md#service-to-service-security).
