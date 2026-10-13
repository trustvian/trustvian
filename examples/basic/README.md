# basic

The "hello world" of the Trustvian Go SDK. Construct an `Engine` with no
options, analyze one well-formed HTTP event, print the `Result` via
`Result.Explain()`, then `Observe` it so it would count toward a future
baseline.

This corresponds to Example 1 in [docs/archive/tasks/v0.1/010-examples.md](../../docs/archive/tasks/v0.1/010-examples.md).
It is the full program around the calls shown in
[docs/sdk-guide.md § Constructing an Engine](../../docs/sdk-guide.md#constructing-an-engine)
and [§ Analyze](../../docs/sdk-guide.md#analyze).

Run it:

```bash
cd examples/basic && go run .
```

## Real output

```
Decision: observe_only
trust 1.00 (low): identity confidence 1.00, anomaly 1.00 at 0% confidence, context risk 0.00
Anomaly score: 1.00 (confidence 0.00)
Detected:
  - categorical_novelty: 1.00 (fingerprint never observed for this actor)
Policy: default action (no policy rules configured; observing by default)
```

Anomaly score is `1.00` because this actor/operation/target combination
has never been observed before (cold start) — but `Confidence` is `0.00`,
so `Trust` doesn't collapse: the engine correctly treats "this is
unfamiliar" and "how much should I trust that reading" as two separate
questions. See [Architecture § cold start](../../docs/ARCHITECTURE.md#cold-start-two-numbers-not-one).

`Decision` is `observe_only` because `NewEngine()`'s default `Policy`
has no rules and always falls through to its `observe_only` default.
Getting `ALLOW`/`BLOCK` differentiation like
[docs/use-cases.md](../../docs/use-cases.md) requires a custom `Policy`,
which an external module compiles with `config.CompilePolicy` — see
[the examples index](../README.md#a-note-on-decision) and
[ai-agent-security](../ai-agent-security/).
