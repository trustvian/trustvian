# The measured Model Change Report — 2026-10-12

The `v0.12.0` milestone's measured report (task 107, "The measured report"):
the companion demo's support agent with its reference side on `llama3.2` and
its candidate side on `gemma3:4b`, ten repetitions a side, gated and reported by
this repository's own code.

This is evidence, not a verdict. It shows what changed between two models on
one agent, one scenario and one machine. It says nothing about which model is
better.

## Files

| File | What it is |
|---|---|
| [`scenario.yaml`](scenario.yaml) | The scenario that ran. `$DEMO` stands for the demo checkout's absolute path, which the run used; the `scenario_digest` in the report was computed over the file with that path in place |
| [`pricing.yaml`](pricing.yaml) | The pricing table the control plane ran with. **Illustrative, not any provider's prices** |
| [`report.json`](report.json) | `trustvian eval report <id> --json`: result document version 2, byte for byte what `trustvian eval run --json` printed for the same execution |
| [`report.md`](report.md) | `trustvian eval report <id> --format md`: byte for byte what `trustvian-ci-render --document report.json` prints, and what the pull request comment shows after its commit and run lines. `TestPublishedReportsAreTheRenderersOutput` (`cmd/trustvian-ci-render`) fails if the two files drift apart |

## How it ran

| | |
|---|---|
| Execution | `scn-support-model-change-20261012T175737-fe980505` |
| Trustvian | `073e6ff2c4ee7c3dbeffae56127d766ac385fb43` (branch `docs/close-v0.12.0`, the report counts fix on top of `main` at `9e1bd20`). The `+dirty` in the version strings is an untracked directory outside the build |
| Demo | `trustvian-python-agent-demo` at `28eb5f51d4a5505d5a7458e617827c9dcef045b4` (`main`, including #13's GenAI model-call span) |
| Models | reference `llama3.2` (`a80c4f17acd5`), candidate `gemma3:4b` (`a2af6cc3eb7f`), chosen by `model_env: OLLAMA_MODEL`; the agent samples at temperature 0 |
| Ollama | 0.34.4 |
| Hardware | Apple M3 Pro, 18 GiB, macOS 26.6.2 |
| Control plane | `trustvian-local`, SQLite, `--pricing pricing.yaml` |
| Runs | 10 a side, sequential, each in its own learning scope |
| Tickets | four of the demo's five, `t-1001` to `t-1004` — see below |
| Wall time | 26 min 32 s (17:57:37Z to 18:24:09Z), from `eval run` starting to its exit |
| Exit | `eval run` exited `1`: the gate's FAIL |

### Two attempts with all five tickets did not finish

The scenario first ran with all five tickets. Twice, the reference side's first
repetition stopped on ticket `t-1005` ("Wants a copy of their data"): `llama3.2`
did not answer the planner's request within the agent's 180 s timeout, the agent
exited `1`, and `eval run` exited `3` with no verdict (executions
`scn-support-model-change-20261012T174423-7b7baa15` and `…T175310-279f6495`,
both recorded as failed). Tickets `t-1001` to `t-1004` had completed in both.
At temperature 0 the request is the same every time, so the failure reproduced.
The measured run therefore works the first four tickets. Whether `llama3.2`
would ever answer `t-1005`, or `gemma3:4b` would, is not measured here.

## What the report shows

Every section as it rendered; [`report.md`](report.md) is the whole of it.

**Sameness.** Scenario digest and inputs the same on both sides (no inputs
declared). Model `llama3.2` against `gemma3:4b`: **differs**, with the warning
`model_differs`. Prompt reference not recorded on either side, because the
scenario declares none.

**Behaviors.** 17 rows (`comparison.counts`: `rows` 17, `added` 1, `removed` 3,
`neither` 13, `not_recorded` 0, `lost` 3).

- Every row is either present in all ten runs of a side or in none: at
  temperature 0 each side did the same thing ten times, and every calls-per-run
  range is a single value (`25–25`, `28–28`, …).
- **Added:** `external/gemma3:4b → ollama`, 0/10 → 10/10, 28 calls a run.
- **Removed and lost:** `external/llama3.2 → ollama` (10/10 → 0/10, 25 calls a
  run), `tool/billing_lookup` (3 a run) and its request `http/GET →
  billing.localhost` (3 a run).
- **Unclassified, with changed frequency:** the candidate sends twice the email
  (`tool/send_email` 4 → 8 a run, `mail.localhost` 4 → 8), looks customers up
  twice as often (`crm_lookup` 4 → 6, `crm.localhost` 3 → 6), escalates half as
  often (`escalate_ticket` 4 → 2), and reads account history twice
  (`account_history` 1 → 2).
- **Fidelity:** every tool and model behavior is semantic and every HTTP
  behavior transport, on both sides, with no mixed row.

**Targets.** Ten targets including the target-less tool calls. `ollama` and
`ollama.localhost` each carry the same model calls, 25 a run on the reference
and 28 on the candidate (`1.1×`): one model call is one GenAI span on `ollama`
and one HTTP request on `ollama.localhost`, so **the two rows count the same
calls twice and must not be added** (a known limitation).

- Latency at `ollama`: reference 183 calls ≤2.5 s and 67 ≤10 s; candidate 10
  ≤2.5 s and 270 ≤10 s. On this machine `gemma3:4b` answered more slowly.
  Latency is descriptive: nothing here attributes it to anything but the model.
- Status: `ollama.localhost` answered `2xx` to every request on both sides, no
  429. The GenAI spans on `ollama` state no HTTP status. The reference side had
  10 `4xx` answers from `crm.localhost` (one a run) and 20 errored target-less
  tool spans; the candidate side had none.
- `billing.localhost` reads *not available* on the candidate side, which never
  called it.

**Tokens and cost.** Input 134,000 → 191,610, output 29,130 → 38,070; 250 →
280 observations reported usage, and 660 → 760 did not (every non-model
behavior). Cost **under illustrative prices** (`illustrative-2026-10`, source
"illustrative prices, not a provider's"): 12,526 → 34,389 micro-USD. These are
not real prices, and the figure says only how the illustrative table prices the
measured tokens.

**Gate: FAIL, as recorded.** Three checks failed:

- repeatedly added behaviors 1 (at most 0) — the candidate's model behavior;
- minimum candidate presence of reference behaviors 0 (at least 8) — three
  reference behaviors appear in no candidate run;
- lost behaviors 3 (at most 0).

Passing: both sides completed 10 of 10 repetitions, no repetition lacked
evidence, no block decision or critical-risk observation (fresh-scope
advisory), calls per run to `ollama` and to `ollama.localhost` 28 (at most 60),
and model calls per run 28 (at most 60, evaluated — every candidate run had
model-layer evidence).

**Suggestions.** Four rule outputs: the candidate's model behavior added in
10/10 runs, and `billing.localhost`, the `llama3.2` model behavior and
`billing_lookup` lost from every candidate run.

**Not available or not recorded:** the prompt reference on both sides; lost
*transitions* (`lost_transitions: not_recorded`, as for every comparison); the
candidate's figures for `billing.localhost`.

## What to conclude, and what not to

- **A model change always changes the model behavior's identity.** The model's
  name is the GenAI operation name, so the reference model's behavior is
  removed and lost and the candidate's is added in every run. With
  `max_repeated_added_behaviors: 0`, `max_lost_behaviors: 0` and
  `min_candidate_frequency` set, any model change fails those checks on that
  row alone, whatever else happened. A team gating model changes has to account
  for it — today by its limits; the report states the fact, it does not adjust
  for it.
- **Beyond the model row, `gemma3:4b` worked these four tickets differently:**
  it never used `billing_lookup`, sent twice the email, looked customers up
  more and escalated less. Whether that is better support is outside what
  Trustvian measures; it records what the agent did.
- **Repeatability here is the sampler's, not the agent's.** At temperature 0
  ten runs agree exactly, so N = 10 adds no information over N = 1 for this
  agent. The k-of-N evidence earns its keep when the sampler varies.
- **Do not read the latency or cost as properties of the models.** One machine,
  one Ollama configuration, sequential runs, illustrative prices.
- **Do not add the `ollama` and `ollama.localhost` rows**, and do not read 28
  model calls a run as 56.
