# Examples

Nine small, runnable `package main` programs demonstrating the full
`Event -> Features -> Fingerprint -> Baseline -> Anomaly -> Trust ->
Policy -> Decision` pipeline — and, since v0.4, the downstream Alert &
Notification Foundation — against the real
`github.com/trustvian/trustvian` Go SDK. Each is a genuinely external
consumer of the module — `examples/go.mod` is a separate Go module with
a `replace github.com/trustvian/trustvian => ../` directive back to this
repository root, so these programs only ever see what an outside caller
actually sees (no `internal/` imports anywhere under this directory).

Five of the first six scenarios port event sequences already verified in
[docs/use-cases.md](../docs/use-cases.md) and
[docs/sdk-guide.md § watching trust mature](../docs/sdk-guide.md#watching-trust-mature)
almost verbatim; `frequency-abuse` and `alert-webhook` are new. Every
README below has real, `go run`-captured output, not hand-written output.

| Example | What it demonstrates | README |
|---|---|---|
| [basic](basic/) | Construct an `Engine`, analyze one well-formed event, print it via `Result.Explain()` — the "hello world" of the SDK | [basic/README.md](basic/README.md) |
| [credential-misuse](credential-misuse/) | A trusted, high-confidence service account performs a novel bulk-export action; full novelty from a trusted identity does not by itself mean block | [credential-misuse/README.md](credential-misuse/README.md) |
| [unexpected-dependency](unexpected-dependency/) | A payment gateway's baseline matures against its normal dependency, then the same route reaches an unexpected internal service at lower identity confidence | [unexpected-dependency/README.md](unexpected-dependency/README.md) |
| [external-destination](external-destination/) | An order service's baseline matures against a normal internal RPC call, then it suddenly reaches an external secrets manager | [external-destination/README.md](external-destination/README.md) |
| [frequency-abuse](frequency-abuse/) | A fully mature, familiar fingerprint bursts far outside its learned request cadence — `frequency_deviation` is detected with zero categorical novelty, and reported even though it ships opt-in (`FrequencyWeight` defaults to `0`) | [frequency-abuse/README.md](frequency-abuse/README.md) |
| [ai-agent](ai-agent/) | An AI agent's baseline matures against a benign CRM-lookup tool call, then the same agent reaches for a credentials store | [ai-agent/README.md](ai-agent/README.md) |
| [alert-webhook](alert-webhook/) | A cold-start, highly novel event resolves to `OBSERVE_ONLY`, but an `alert.Rule` matching on anomaly score alone still produces a `HIGH`-severity `Alert`, signed and delivered over HTTPS to a webhook — proving `Decision != Alert` end to end via the direct Go SDK, no OTel involved | [alert-webhook/README.md](alert-webhook/README.md) |
| [ai-agent-security](ai-agent-security/) | An AI agent's `shell.execute` matures into fully familiar behavior; a `config.PolicyConfig`-compiled approval requirement still blocks it when `ApprovalStatus` isn't `approved` — the one example here with a genuinely differentiated `Decision`, since `Policy` (unlike `anomaly.Config`) has a public configuration path | [ai-agent-security/README.md](ai-agent-security/README.md) |
| [configured-engine](configured-engine/) | Every supported `Engine` option configured from outside the module: policy, storage, anomaly and trust through the `config` facade, plus a context-risk callback over `trustvian.StableFeatures` — and the caller-owned cleanup that goes with a compiled store | — |
| [persistent-baseline](persistent-baseline/) | A learned baseline survives a process restart: a durable store selected through `config.StorageConfig`/`CompileStorage`, an `Engine` discarded entirely, and a fresh one reading the same state back off disk — the only example that persists anything, and impossible to write externally before `v0.8` | [persistent-baseline/README.md](persistent-baseline/README.md) |

Not a Go program: [`github-actions/behavioral-gate-run.yml`](github-actions/behavioral-gate-run.yml)
is a copyable, read-only workflow that runs a behavioral scenario on every pull
request — see [Running behavioral scenarios in GitHub Actions](../docs/ci-github-action.md).

## Running them

```bash
cd examples/basic && go run .
# ...or all nine at once, from the repository root:
make examples
```

`make examples` (see the root [`Makefile`](../Makefile)) runs every
example in turn and fails the build if any exits non-zero — this is the
mechanism that keeps these examples from silently going stale as the
`Engine`'s public API evolves.

No example requires network access, external services, or any setup
beyond `go run`.

## A note on `Decision`

Eight of the nine examples here use a `Policy`-less `Engine`, so their
printed `Decision` is always `observe_only` — `NewEngine()`'s default
`Policy` has no rules and always falls through to its `observe_only`
default (see
[docs/sdk-guide.md § Constructing an Engine](../docs/sdk-guide.md#constructing-an-engine)).
This is deliberate, not a limitation: those eight each demonstrate one
specific `Anomaly`/`Trust` reading (or, for
[persistent-baseline](persistent-baseline/), one storage behavior), and
a custom `Policy` would be one more moving part than the scenario needs.

`policy.Policy` itself is a type that lives under `internal/` — per
[ADR 0002](../docs/adr/0002-public-api-boundary.md), a true external
module (which is exactly what `examples/` is set up to be) cannot name
that type directly. But since `v0.5` ([ADR
0008](../docs/adr/0008-policy-config-boundary.md)), a caller doesn't
need to: `config.CompilePolicy` compiles a public `config.PolicyConfig`
value into a `policy.Policy` a caller receives and passes straight into
`trustvian.WithPolicy` via ordinary Go type inference, without ever
importing `internal/policy`. [ai-agent-security](ai-agent-security/) is
this directory's own example of that path — the one example here with
a genuinely differentiated `ALLOW`/`BLOCK` `Decision`, not
`observe_only`. See
[docs/policy-guide.md § Configuring a Policy from outside this module](../docs/policy-guide.md#configuring-a-policy-from-outside-this-module)
for the full mechanism, and [`cmd/trustvian`](../cmd/trustvian) for how
this repository's own CLI uses the same path.
