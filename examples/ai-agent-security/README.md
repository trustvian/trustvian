# ai-agent-security

Ports [docs/archive/tasks/v0.7/032-agent-security-scenario-validation.md](../../docs/archive/tasks/v0.7/032-agent-security-scenario-validation.md)'s
combined scenario — delegation, sequence, and approval evidence,
together — through the real, declarative configuration path an OSS
user would actually use: `config.PolicyConfig` (approval, since `v0.5`)
and `config.AnomalyConfig` (delegation/n-gram, since
[task 033](../../docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md)),
each compiled with `config.CompilePolicy`/`config.CompileAnomaly` into
the exact types `trustvian.WithPolicy`/`WithAnomalyConfig` accept. This
file never imports `internal/policy` or `internal/anomaly` — see
[the examples index](../README.md#a-note-on-decision) for the general
public/internal boundary, and
[ADR 0017](../../docs/adr/0017-public-anomaly-configuration-boundary.md)
for why `AnomalyConfig` specifically needed to exist before this
example could be written this way.

An AI agent's normal path (`search`, delegated by a known
`orchestrator-agent`, always approved) matures into familiar behavior;
`search → secret.read` and `secret.read → external.post` are
separately familiarized as pairwise transitions, so neither looks
unusual alone. Then a combined attack is evaluated: delegated by
`unknown-agent` (never seen before), completing the exact three-step
sequence that was never trained as one continuous path, with no
approval evidence — surfacing `delegation_deviation` and
`ngram_deviation` together, and blocked by the approval policy
regardless of either signal's own magnitude.

Run it:

```bash
cd examples/ai-agent-security && go run .
```

## Real output

```
Baseline trained: search/secret.read/external.post are each individually
familiar for this agent, delegated by orchestrator-agent, always approved.

Attack: delegated by unknown-agent, completes search -> secret.read ->
external.post as one sequence for the first time, approval missing.

Decision: block
trust 0.03 (critical): identity confidence 0.95, anomaly 0.97 at full confidence, context risk 0.00
Anomaly score: 0.97 (confidence 1.00)
Detected:
  - transition_rarity: 0.50 (transition observed 20/40 (50.00%) of this fingerprint's outgoing transitions)
  - markov_surprisal: 0.19 (transition observed 20/40 (50.00%) of this fingerprint's outgoing transitions, carrying 1.00 bits of surprise under the learned first-order model)
  - ngram_deviation: 1.00 (behavioral sequence via fingerprints 851a5a2993ffb7ef -> 65be207313221b27 has never been observed leading to this one, for this actor)
  - delegation_deviation: 1.00 (delegator "unknown-agent" has never been observed for this actor)
Policy: rule "external-post-requires-approval" (external.post requires approval; approval evidence was not Approved)

Decision: block (rule "external-post-requires-approval": external.post requires approval; approval evidence was not Approved)

Alert: alt_7dc321b115939f92becbc74d87e0d5bb severity=high reasons=[transition observed 20/40 (50.00%) of this fingerprint's outgoing transitions transition observed 20/40 (50.00%) of this fingerprint's outgoing transitions, carrying 1.00 bits of surprise under the learned first-order model behavioral sequence via fingerprints 851a5a2993ffb7ef -> 65be207313221b27 has never been observed leading to this one, for this actor delegator "unknown-agent" has never been observed for this actor policy rule "external-post-requires-approval": external.post requires approval; approval evidence was not Approved]
```

(`Alert.ID` is randomly generated per run — see `alert.New`.
`transition_rarity`/`markov_surprisal` are reported for
explainability, per this codebase's "always compute, weight-gate the
contribution" convention, but contribute nothing to `Anomaly.Score`
here — this example never sets `TransitionRarityWeight`/`MarkovWeight`,
so both default to `0`.)

## What this shows

- **`config.AnomalyConfig` closes the gap task 032 found.**
  `DelegationWeight`/`NGramWeight` are set through public config, not
  an internal Go struct literal. With
  [configured-engine](../configured-engine/), it is one of two examples
  here that configure anomaly scoring at all.
  `config.CompileAnomaly(config.AnomalyConfig{})` (every field
  omitted) reproduces `anomaly.DefaultConfig()` byte-for-byte, proven
  by `TestCompileAnomalyZeroValueMatchesDefaultConfig` in
  `config/anomaly_compile_test.go` — existing callers who configure
  nothing see unchanged behavior.
- **Three independent evidence types compose without colliding.**
  `Anomaly.Score = 0.97` is exactly the documented noisy-OR of
  `ngram_deviation` (`1.0 * 0.9`) and `delegation_deviation`
  (`1.0 * 0.7`) — `1 - (1-0.9)(1-0.7) = 0.97` — and `Decision` comes
  from the approval rule alone, independent of that score.
- **Familiar is not authorized; unfamiliar is not malicious.** Neither
  `delegation_deviation` nor `ngram_deviation` blocks anything by
  themselves — they are evidence Policy may act on. The actual `BLOCK`
  here traces to one specific, named rule
  (`external-post-requires-approval`), not to the anomaly score.
