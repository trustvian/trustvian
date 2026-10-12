### Trustvian behavioral gate — FAIL

**Scenario** `support` · **runs** 2 · **execution** `golden-inputs-after`

**Reference side** reused from execution `golden-inputs-before` (`--reference last`)

#### Sameness

| | Reference | Candidate | Same |
|---|---|---|---|
| Scenario | `sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` | `sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` | same |
| Inputs | declared `sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb` | declared `sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc` | **differs** |
| Model | `gemma3:4b` | `gemma3:4b` | same |
| Prompt reference | `support-prompt` `sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee` | `support-prompt` `sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee` | same |

> **Warning** `inputs_differ`: `The reference and candidate sides ran with different inputs.`

A warning is beside the gate: no check reads it.

#### Behaviors

Added: present in at least 2 of 2 candidate runs and at most 0 of 2 reference runs. Removed: present in at least 2 of 2 reference runs and at most 0 of 2 candidate runs.

| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |
|---|---|---|---|---|---|---|---|---|---|
| `ai_agent` · `external` · `gemma3:4b` · `ollama` · `external` · `staging` | `fp-external-gemma3:4b@ollama` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 2 · transport 0 · unrecorded 0 | semantic · semantic 2 · transport 0 · unrecorded 0 |
| `ai_agent` · `http` · `POST` · `ollama.localhost` · `external` · `staging` | `fp-http-POST@ollama.localhost` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | transport · semantic 0 · transport 2 · unrecorded 0 | transport · semantic 0 · transport 2 · unrecorded 0 |
| `ai_agent` · `tool` · `lookup_customer` · `staging` | `fp-tool-lookup_customer@` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 2 · transport 0 · unrecorded 0 | semantic · semantic 2 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `lookup_invoice` · `staging` | `fp-tool-lookup_invoice@` | 0/2 | 2/2 | 0–0, mean 0 | 1–1, mean 1 | **+ added** | no | — | semantic · semantic 2 · transport 0 · unrecorded 0 |

#### Targets

| Target | Calls per run, reference | Calls per run, candidate | Call ratio |
|---|---|---|---|
| *(no target)* | 1–1, mean 1 | 2–2, mean 2 | 2.0× |
| `ollama` (`external`) | 1–1, mean 1 | 1–1, mean 1 | 1.0× |
| `ollama.localhost` (`external`) | 1–1, mean 1 | 1–1, mean 1 | 1.0× |

Latency and status per target, each side summed over its completed repetitions. Latency buckets with no observation are not listed.

| Target | Side | Latency | Span status | HTTP status | 429 |
|---|---|---|---|---|---|
| *(no target)* | reference | ≤5 ms 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 2 | 0 |
| *(no target)* | candidate | ≤5 ms 4 (observed 4, unobserved 0, 2 runs) | ok 4 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 4 | 0 |
| *(no target)* | change | | ok 2 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `ollama` (`external`) | reference | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 2 | 0 |
| `ollama` (`external`) | candidate | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 2 | 0 |
| `ollama` (`external`) | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `ollama.localhost` (`external`) | reference | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` (`external`) | candidate | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` (`external`) | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |

#### Tokens and cost

| | Reference | Candidate | Change |
|---|---|---|---|
| Input tokens | 2200 | 2200 | 0 |
| Output tokens | 500 | 500 | 0 |
| Unsplit tokens | 0 | 0 | 0 |
| Observations with usage | 2 | 2 | 0 |
| Observations without usage | 4 | 6 | 2 |

Cost: not available — no pricing table is configured (`pricing_not_configured`).

#### Gate

**Verdict: FAIL**, as the execution recorded it when it completed.

| Gate check | Actual | Rule | Bound | Outcome | Note |
|---|---|---|---|---|---|
| reference repetitions completed | 2 | equals | 2 | pass |  |
| candidate repetitions completed | 2 | equals | 2 | pass |  |
| repetitions failing minimum evidence | 0 | at most | 0 | pass |  |
| repeatedly added behaviors | 1 | at most | 0 | **FAIL** |  |
| worst candidate block decisions per run | 0 | at most | 0 | pass | fresh scope |
| worst candidate critical-risk observations per run | 0 | at most | 0 | pass | fresh scope |
| minimum candidate presence of reference behaviors | | | | not configured | |
| lost behaviors | | | | not configured | |
| calls per run | | | | not configured | |
| model calls per run | | | | not configured | |

*Fresh scope:* each repetition learns in a scope of its own, so the engine has learned nothing there and these checks cannot answer the learned-policy question. The marker changes no verdict.

The recorded limits: at most 0 repeatedly added behaviors; at most 0 block decisions and 0 critical-risk observations in any one candidate run.

#### Suggestions

Rule outputs beside the evidence; none changes the verdict.

- `compare.added_in_all_runs`: `tool/lookup_invoice was added in 2/2 candidate runs. If this was expected, add it to the scenario; otherwise investigate before promoting.`
  Evidence: `fingerprint_id` `fp-tool-lookup_invoice@` · `behavior` `tool/lookup_invoice` · `candidate_runs_present` `2` · `reference_runs_present` `0` · `runs` `2`

Produced by `trustvian` `v0.13.0 3e86f9cead2586e7ee90e14185a8f34074b54240` and control plane `v0.13.0`.
