# ai-agent

Ports [docs/use-cases.md § AI-agent security](../../docs/use-cases.md#ai-agent-security):
a support agent's normal tool is a CRM lookup (`search_customer` on
`crm-api`). The baseline is matured with 25 `Analyze`+`Observe` calls
against that benign event (mirroring
[docs/sdk-guide.md § watching trust mature](../../docs/sdk-guide.md#watching-trust-mature)),
then a credentials-store access from the same agent (`get_credentials`
on `credentials-store`), at lower identity confidence (`0.9` -> `0.3`),
is analyzed — a structurally identical event shape
(`Operation.Category = "tool"`) but a very different kind of action.

Run it:

```bash
cd examples/ai-agent && go run .
```

## Real output

```
warm-up  1: confidence=0.00 trust=0.90 decision=observe_only learned=true
warm-up 10: confidence=0.45 trust=0.68 decision=observe_only learned=true
warm-up 20: confidence=0.95 trust=0.86 decision=observe_only learned=true
warm-up 25: confidence=1.00 trust=0.90 decision=observe_only learned=true

Decision: observe_only
trust 0.30 (high): identity confidence 0.30, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
  - transition_deviation: 1.00 (transition from fingerprint ed44f1240328faef has never been observed leading to this one)
  - ngram_deviation: 1.00 (behavioral sequence via fingerprints ed44f1240328faef -> ed44f1240328faef has never been observed leading to this one, for this actor)
Policy: default action (no policy rules configured; observing by default)
```

`transition_deviation` and `ngram_deviation` are reported too: no
warm-up event was ever followed by this fingerprint. `anomaly.DefaultConfig()`
gives both signals a weight of `0`, so they explain the reading but add
nothing to `Anomaly.Score`.

This is the mechanism the project spec describes as "Trustvian evaluates
runtime behavior instead of relying only on identity" — `Operation` and
`Target` drive the outcome here just as much as `Actor`, even though both
events are the same `Operation.Category` ("tool") from the same actor.
See [the examples index](../README.md#a-note-on-decision) for why
`Decision` here is `observe_only` rather than the `BLOCK` shown in
[docs/use-cases.md](../../docs/use-cases.md#ai-agent-security).
