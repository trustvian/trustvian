### Trustvian behavioral gate — FAIL

**Scenario** `support` · **runs** 2 · **execution** `golden-deferred`

#### Sameness

| | Reference | Candidate | Same |
|---|---|---|---|
| Scenario | not recorded | not recorded | not recorded |
| Inputs | not recorded | not recorded | not recorded |
| Model | not recorded | not recorded | not recorded |
| Prompt reference | not recorded | not recorded | not recorded |

No sameness warning.

#### Behaviors

Added: present in at least 2 of 2 candidate runs and at most 0 of 2 reference runs. Removed: present in at least 2 of 2 reference runs and at most 0 of 2 candidate runs.

| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |
|---|---|---|---|---|---|---|---|---|---|
| `ai_agent` · `http` · `POST` · `api.model.example` · `external` · `staging` | `fp-http-POST@api.model.example` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | unrecorded · semantic 0 · transport 0 · unrecorded 2 | unrecorded · semantic 0 · transport 0 · unrecorded 2 |

#### Targets

| Target | Calls per run, reference | Calls per run, candidate | Call ratio |
|---|---|---|---|
| `api.model.example` (`external`) | 1–1, mean 1 | 1–1, mean 1 | 1.0× |

Latency and status per target, each side summed over its completed repetitions. Latency buckets with no observation are not listed.

| Target | Side | Latency | Span status | HTTP status | 429 |
|---|---|---|---|---|---|
| `api.model.example` (`external`) | reference | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `api.model.example` (`external`) | candidate | ≤1 s 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `api.model.example` (`external`) | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |

#### Tokens and cost

Tokens: not available on either side (`both_unavailable`); no change is shown.

| | Reference | Candidate | Change |
|---|---|---|---|
| Input tokens | not available | not available |  |
| Output tokens | not available | not available |  |
| Unsplit tokens | not available | not available |  |
| Observations with usage | not available | not available |  |
| Observations without usage | not available | not available |  |

Cost: not available — no pricing table is configured (`pricing_not_configured`).

#### Gate

**Verdict: FAIL**, as the execution recorded it when it completed.

| Gate check | Actual | Rule | Bound | Outcome | Note |
|---|---|---|---|---|---|
| reference repetitions completed | 2 | equals | 2 | pass |  |
| candidate repetitions completed | 2 | equals | 2 | pass |  |
| repetitions failing minimum evidence | 0 | at most | 0 | pass |  |
| repeatedly added behaviors | 0 | at most | 0 | pass |  |
| worst candidate block decisions per run | 0 | at most | 0 | pass | fresh scope |
| worst candidate critical-risk observations per run | 0 | at most | 0 | pass | fresh scope |
| minimum candidate presence of reference behaviors | | | | not configured | |
| lost behaviors | | | | not configured | |
| calls per run | | | | not configured | |
| model calls per run | | | | **deferred** | missing evidence: `run "golden-deferred-candidate-1" has no model-layer observation; model calls cannot be counted (2 of 2 candidate runs have none)` |

*Fresh scope:* each repetition learns in a scope of its own, so the engine has learned nothing there and these checks cannot answer the learned-policy question. The marker changes no verdict.

The recorded limits: at most 0 repeatedly added behaviors; at most 0 block decisions and 0 critical-risk observations in any one candidate run. At most 2 model calls per run.

#### Suggestions

No suggestion.

Produced by `trustvian` `v0.13.0 3e86f9cead2586e7ee90e14185a8f34074b54240` and control plane `v0.13.0`.
