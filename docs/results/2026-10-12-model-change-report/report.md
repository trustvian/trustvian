### Trustvian behavioral gate — FAIL

**Scenario** `support-model-change` · **runs** 10 · **execution** `scn-support-model-change-20261012T175737-fe980505`

#### Sameness

| | Reference | Candidate | Same |
|---|---|---|---|
| Scenario | `sha256:7f9b467312acb6bb8466d72a3e777abc7b595c91c88e1726fe0f7e1b474855c6` | `sha256:7f9b467312acb6bb8466d72a3e777abc7b595c91c88e1726fe0f7e1b474855c6` | same |
| Inputs | none declared | none declared | same |
| Model | `llama3.2` | `gemma3:4b` | **differs** |
| Prompt reference | not recorded | not recorded | not recorded |

> **Warning** `model_differs`: `The reference and candidate sides declared different models.`

A warning is beside the gate: no check reads it.

#### Behaviors

Added: present in at least 8 of 10 candidate runs and at most 2 of 10 reference runs. Removed: present in at least 8 of 10 reference runs and at most 2 of 10 candidate runs.

| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |
|---|---|---|---|---|---|---|---|---|---|
| `service` · `http` · `GET` · `crm.localhost` · `local` | `33f8e264c47c88e3` | 10/10 | 10/10 | 3–3, mean 3 | 6–6, mean 6 | unclassified | no | transport · semantic 0 · transport 30 · unrecorded 0 | transport · semantic 0 · transport 60 · unrecorded 0 |
| `service` · `tool` · `send_email` · `local` | `3424b30b5c2ed59` | 10/10 | 10/10 | 4–4, mean 4 | 8–8, mean 8 | unclassified | no | semantic · semantic 40 · transport 0 · unrecorded 0 | semantic · semantic 80 · transport 0 · unrecorded 0 |
| `service` · `http` · `GET` · `billing.localhost` · `local` | `347cd21e906ff5d4` | 10/10 | 0/10 | 3–3, mean 3 | 0–0, mean 0 | **− removed** | **lost** | transport · semantic 0 · transport 30 · unrecorded 0 | — |
| `service` · `http` · `POST` · `ollama.localhost` · `local` | `494987743bf8c56f` | 10/10 | 10/10 | 25–25, mean 25 | 28–28, mean 28 | unclassified | no | transport · semantic 0 · transport 250 · unrecorded 0 | transport · semantic 0 · transport 280 · unrecorded 0 |
| `service` · `http` · `GET` · `knowledge.localhost` · `local` | `530c3d28f96270cb` | 10/10 | 10/10 | 4–4, mean 4 | 5–5, mean 5 | unclassified | no | transport · semantic 0 · transport 40 · unrecorded 0 | transport · semantic 0 · transport 50 · unrecorded 0 |
| `service` · `tool` · `account_history` · `local` | `6432d49319bd5344` | 10/10 | 10/10 | 1–1, mean 1 | 2–2, mean 2 | unclassified | no | semantic · semantic 10 · transport 0 · unrecorded 0 | semantic · semantic 20 · transport 0 · unrecorded 0 |
| `service` · `http` · `POST` · `escalation.localhost` · `local` | `6c5f7b4e583eedd8` | 10/10 | 10/10 | 4–4, mean 4 | 2–2, mean 2 | unclassified | no | transport · semantic 0 · transport 40 · unrecorded 0 | transport · semantic 0 · transport 20 · unrecorded 0 |
| `service` · `http` · `POST` · `diagnostics.localhost` · `local` | `737851e9f02ce153` | 10/10 | 10/10 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | transport · semantic 0 · transport 10 · unrecorded 0 | transport · semantic 0 · transport 10 · unrecorded 0 |
| `service` · `tool` · `knowledge_search` · `local` | `a4d95d24dfabc321` | 10/10 | 10/10 | 4–4, mean 4 | 5–5, mean 5 | unclassified | no | semantic · semantic 40 · transport 0 · unrecorded 0 | semantic · semantic 50 · transport 0 · unrecorded 0 |
| `service` · `tool` · `attach_diagnostics` · `local` | `a86d74f50c4e504c` | 10/10 | 10/10 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 10 · transport 0 · unrecorded 0 | semantic · semantic 10 · transport 0 · unrecorded 0 |
| `service` · `external` · `llama3.2` · `ollama` · `local` | `ab138b2d804ae517` | 10/10 | 0/10 | 25–25, mean 25 | 0–0, mean 0 | **− removed** | **lost** | semantic · semantic 250 · transport 0 · unrecorded 0 | — |
| `service` · `tool` · `escalate_ticket` · `local` | `badf30b1f7d4e27b` | 10/10 | 10/10 | 4–4, mean 4 | 2–2, mean 2 | unclassified | no | semantic · semantic 40 · transport 0 · unrecorded 0 | semantic · semantic 20 · transport 0 · unrecorded 0 |
| `service` · `http` · `GET` · `history.localhost` · `local` | `cc6707291c53f4b7` | 10/10 | 10/10 | 1–1, mean 1 | 2–2, mean 2 | unclassified | no | transport · semantic 0 · transport 10 · unrecorded 0 | transport · semantic 0 · transport 20 · unrecorded 0 |
| `service` · `external` · `gemma3:4b` · `ollama` · `local` | `e9f86b3acb1ab0db` | 0/10 | 10/10 | 0–0, mean 0 | 28–28, mean 28 | **+ added** | no | — | semantic · semantic 280 · transport 0 · unrecorded 0 |
| `service` · `http` · `POST` · `mail.localhost` · `local` | `ecbf10fe57500d2a` | 10/10 | 10/10 | 4–4, mean 4 | 8–8, mean 8 | unclassified | no | transport · semantic 0 · transport 40 · unrecorded 0 | transport · semantic 0 · transport 80 · unrecorded 0 |
| `service` · `tool` · `billing_lookup` · `local` | `edd3dedf7030dc28` | 10/10 | 0/10 | 3–3, mean 3 | 0–0, mean 0 | **− removed** | **lost** | semantic · semantic 30 · transport 0 · unrecorded 0 | — |
| `service` · `tool` · `crm_lookup` · `local` | `f83ccafb9ba9faa3` | 10/10 | 10/10 | 4–4, mean 4 | 6–6, mean 6 | unclassified | no | semantic · semantic 40 · transport 0 · unrecorded 0 | semantic · semantic 60 · transport 0 · unrecorded 0 |

#### Targets

| Target | Calls per run, reference | Calls per run, candidate | Call ratio |
|---|---|---|---|
| *(no target)* | 21–21, mean 21 | 24–24, mean 24 | 1.1× |
| `billing.localhost` | 3–3, mean 3 | 0–0, mean 0 | 0.0× |
| `crm.localhost` | 3–3, mean 3 | 6–6, mean 6 | 2.0× |
| `diagnostics.localhost` | 1–1, mean 1 | 1–1, mean 1 | 1.0× |
| `escalation.localhost` | 4–4, mean 4 | 2–2, mean 2 | 0.5× |
| `history.localhost` | 1–1, mean 1 | 2–2, mean 2 | 2.0× |
| `knowledge.localhost` | 4–4, mean 4 | 5–5, mean 5 | 1.2× |
| `mail.localhost` | 4–4, mean 4 | 8–8, mean 8 | 2.0× |
| `ollama` | 25–25, mean 25 | 28–28, mean 28 | 1.1× |
| `ollama.localhost` | 25–25, mean 25 | 28–28, mean 28 | 1.1× |

Latency and status per target, each side summed over its completed repetitions. Latency buckets with no observation are not listed.

| Target | Side | Latency | Span status | HTTP status | 429 |
|---|---|---|---|---|---|
| *(no target)* | reference | ≤1 ms 10 · ≤5 ms 136 · ≤10 ms 63 · ≤50 ms 1 (observed 210, unobserved 0, 10 runs) | ok 0 · error 20 · unset 190 · unavailable 0 (10 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 210 | 0 |
| *(no target)* | candidate | ≤5 ms 186 · ≤10 ms 51 · ≤50 ms 3 (observed 240, unobserved 0, 10 runs) | ok 0 · error 0 · unset 240 · unavailable 0 (10 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 240 | 0 |
| *(no target)* | change | | ok 0 · error -20 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `billing.localhost` | reference | ≤1 ms 2 · ≤5 ms 28 (observed 30, unobserved 0, 10 runs) | ok 0 · error 0 · unset 30 · unavailable 0 (10 runs) | 1xx 0 · 2xx 30 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `billing.localhost` | candidate | not available | not available | not available | not available |
| `crm.localhost` | reference | ≤1 ms 2 · ≤5 ms 27 · ≤10 ms 1 (observed 30, unobserved 0, 10 runs) | ok 0 · error 10 · unset 20 · unavailable 0 (10 runs) | 1xx 0 · 2xx 20 · 3xx 0 · 4xx 10 · 5xx 0 · unavailable 0 | 0 |
| `crm.localhost` | candidate | ≤1 ms 4 · ≤5 ms 52 · ≤10 ms 4 (observed 60, unobserved 0, 10 runs) | ok 0 · error 0 · unset 60 · unavailable 0 (10 runs) | 1xx 0 · 2xx 60 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `crm.localhost` | change | | ok 0 · error -10 | 2xx 40 · 4xx -10 · 5xx 0 | 0 |
| `diagnostics.localhost` | reference | ≤5 ms 8 · ≤10 ms 2 (observed 10, unobserved 0, 10 runs) | ok 0 · error 0 · unset 10 · unavailable 0 (10 runs) | 1xx 0 · 2xx 10 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `diagnostics.localhost` | candidate | ≤5 ms 9 · ≤10 ms 1 (observed 10, unobserved 0, 10 runs) | ok 0 · error 0 · unset 10 · unavailable 0 (10 runs) | 1xx 0 · 2xx 10 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `diagnostics.localhost` | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `escalation.localhost` | reference | ≤1 ms 1 · ≤5 ms 38 · ≤10 ms 1 (observed 40, unobserved 0, 10 runs) | ok 0 · error 0 · unset 40 · unavailable 0 (10 runs) | 1xx 0 · 2xx 40 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `escalation.localhost` | candidate | ≤5 ms 19 · ≤10 ms 1 (observed 20, unobserved 0, 10 runs) | ok 0 · error 0 · unset 20 · unavailable 0 (10 runs) | 1xx 0 · 2xx 20 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `escalation.localhost` | change | | ok 0 · error 0 | 2xx -20 · 4xx 0 · 5xx 0 | 0 |
| `history.localhost` | reference | ≤5 ms 7 · ≤10 ms 3 (observed 10, unobserved 0, 10 runs) | ok 0 · error 0 · unset 10 · unavailable 0 (10 runs) | 1xx 0 · 2xx 10 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `history.localhost` | candidate | ≤5 ms 19 · ≤10 ms 1 (observed 20, unobserved 0, 10 runs) | ok 0 · error 0 · unset 20 · unavailable 0 (10 runs) | 1xx 0 · 2xx 20 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `history.localhost` | change | | ok 0 · error 0 | 2xx 10 · 4xx 0 · 5xx 0 | 0 |
| `knowledge.localhost` | reference | ≤1 ms 1 · ≤5 ms 35 · ≤10 ms 4 (observed 40, unobserved 0, 10 runs) | ok 0 · error 0 · unset 40 · unavailable 0 (10 runs) | 1xx 0 · 2xx 40 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `knowledge.localhost` | candidate | ≤5 ms 49 · ≤10 ms 1 (observed 50, unobserved 0, 10 runs) | ok 0 · error 0 · unset 50 · unavailable 0 (10 runs) | 1xx 0 · 2xx 50 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `knowledge.localhost` | change | | ok 0 · error 0 | 2xx 10 · 4xx 0 · 5xx 0 | 0 |
| `mail.localhost` | reference | ≤5 ms 40 (observed 40, unobserved 0, 10 runs) | ok 0 · error 0 · unset 40 · unavailable 0 (10 runs) | 1xx 0 · 2xx 40 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `mail.localhost` | candidate | ≤1 ms 5 · ≤5 ms 73 · ≤10 ms 2 (observed 80, unobserved 0, 10 runs) | ok 0 · error 0 · unset 80 · unavailable 0 (10 runs) | 1xx 0 · 2xx 80 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `mail.localhost` | change | | ok 0 · error 0 | 2xx 40 · 4xx 0 · 5xx 0 | 0 |
| `ollama` | reference | ≤2.5 s 183 · ≤10 s 67 (observed 250, unobserved 0, 10 runs) | ok 0 · error 0 · unset 250 · unavailable 0 (10 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 250 | 0 |
| `ollama` | candidate | ≤2.5 s 10 · ≤10 s 270 (observed 280, unobserved 0, 10 runs) | ok 0 · error 0 · unset 280 · unavailable 0 (10 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 280 | 0 |
| `ollama` | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `ollama.localhost` | reference | ≤2.5 s 183 · ≤10 s 67 (observed 250, unobserved 0, 10 runs) | ok 0 · error 0 · unset 250 · unavailable 0 (10 runs) | 1xx 0 · 2xx 250 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` | candidate | ≤2.5 s 10 · ≤10 s 270 (observed 280, unobserved 0, 10 runs) | ok 0 · error 0 · unset 280 · unavailable 0 (10 runs) | 1xx 0 · 2xx 280 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` | change | | ok 0 · error 0 | 2xx 30 · 4xx 0 · 5xx 0 | 0 |

#### Tokens and cost

| | Reference | Candidate | Change |
|---|---|---|---|
| Input tokens | 134000 | 191610 | 57610 |
| Output tokens | 29130 | 38070 | 8940 |
| Unsplit tokens | 0 | 0 | 0 |
| Observations with usage | 250 | 280 | 30 |
| Observations without usage | 660 | 760 | 100 |

Cost in micro-`USD`, priced by `illustrative-2026-10` (digest `sha256:906664a4dac05a672a9a5d3ff426083d66a4f91ba4f303921054a3a145e2ff08`, source `illustrative prices, not a provider's`):

| | Reference | Candidate | Change |
|---|---|---|---|
| Cost | 12526 | 34389 | 21863 |
| Priced tokens | 163130 | 229680 | |
| Unpriced tokens | 0 | 0 | |

#### Gate

**Verdict: FAIL**, as the execution recorded it when it completed.

| Gate check | Actual | Rule | Bound | Outcome | Note |
|---|---|---|---|---|---|
| reference repetitions completed | 10 | equals | 10 | pass |  |
| candidate repetitions completed | 10 | equals | 10 | pass |  |
| repetitions failing minimum evidence | 0 | at most | 0 | pass |  |
| repeatedly added behaviors | 1 | at most | 0 | **FAIL** |  |
| worst candidate block decisions per run | 0 | at most | 0 | pass | fresh scope |
| worst candidate critical-risk observations per run | 0 | at most | 0 | pass | fresh scope |
| minimum candidate presence of reference behaviors | 0 | at least | 8 | **FAIL** | |
| lost behaviors | 3 | at most | 0 | **FAIL** | |
| calls per run | | | | pass | per target, below |
| calls per run · `ollama` | 28 | at most | 60 | pass | |
| calls per run · `ollama.localhost` | 28 | at most | 60 | pass | |
| model calls per run | 28 | at most | 60 | pass | |

*Fresh scope:* each repetition learns in a scope of its own, so the engine has learned nothing there and these checks cannot answer the learned-policy question. The marker changes no verdict.

The recorded limits: at most 0 repeatedly added behaviors; at most 0 block decisions and 0 critical-risk observations in any one candidate run. Every behavior in all 10 reference runs is in at least 8 candidate runs. At most 0 lost behaviors. At most 60 calls per run to `ollama`. At most 60 calls per run to `ollama.localhost`. At most 60 model calls per run.

#### Suggestions

Rule outputs beside the evidence; none changes the verdict.

- `compare.added_in_all_runs`: `external/gemma3:4b → ollama was added in 10/10 candidate runs. If this was expected, add it to the scenario; otherwise investigate before promoting.`
  Evidence: `fingerprint_id` `e9f86b3acb1ab0db` · `behavior` `external/gemma3:4b → ollama` · `candidate_runs_present` `10` · `reference_runs_present` `0` · `runs` `10`
- `compare.lost_in_half`: `http/GET → billing.localhost was in every reference run and is missing from 10 of 10 candidate runs. Task completion may have regressed.`
  Evidence: `fingerprint_id` `347cd21e906ff5d4` · `behavior` `http/GET → billing.localhost` · `reference_runs_present` `10` · `candidate_runs_present` `0` · `runs` `10`
- `compare.lost_in_half`: `external/llama3.2 → ollama was in every reference run and is missing from 10 of 10 candidate runs. Task completion may have regressed.`
  Evidence: `fingerprint_id` `ab138b2d804ae517` · `behavior` `external/llama3.2 → ollama` · `reference_runs_present` `10` · `candidate_runs_present` `0` · `runs` `10`
- `compare.lost_in_half`: `tool/billing_lookup was in every reference run and is missing from 10 of 10 candidate runs. Task completion may have regressed.`
  Evidence: `fingerprint_id` `edd3dedf7030dc28` · `behavior` `tool/billing_lookup` · `reference_runs_present` `10` · `candidate_runs_present` `0` · `runs` `10`

Produced by `trustvian` `v0.11.1-0.20261012174354-073e6ff2c4ee+dirty 073e6ff2c4ee7c3dbeffae56127d766ac385fb43` and control plane `(devel) 073e6ff2c4ee7c3dbeffae56127d766ac385fb43+dirty`.
