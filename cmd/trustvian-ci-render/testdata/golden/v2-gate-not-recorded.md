### Trustvian behavioral gate — gate not recorded

**Scenario** `support` · **runs** 2 · **execution** `golden-pre-v14`

#### Sameness

| | Reference | Candidate | Same |
|---|---|---|---|
| Scenario | not recorded | not recorded | not recorded |
| Inputs | not recorded | not recorded | not recorded |
| Model | not recorded | not recorded | not recorded |
| Prompt reference | not recorded | not recorded | not recorded |

No sameness warning.

#### Behaviors

Classification is not recorded: it needs the limits the execution did not keep.

| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |
|---|---|---|---|---|---|---|---|---|---|
| `ai_agent` · `http` · `POST` · `ollama.localhost` · `external` · `staging` | `fp-http-POST@ollama.localhost` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | not recorded | no | transport · semantic 0 · transport 2 · unrecorded 0 | transport · semantic 0 · transport 2 · unrecorded 0 |
| `ai_agent` · `tool` · `export_customer` · `staging` | `fp-tool-export_customer@` | 0/2 | 1/2 | 0–0, mean 0 | 0–1, mean 0.5 | not recorded | no | — | semantic · semantic 1 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `lookup_customer` · `staging` | `fp-tool-lookup_customer@` | 2/2 | 2/2 | 1–1, mean 1 | 1–1, mean 1 | not recorded | no | semantic · semantic 2 · transport 0 · unrecorded 0 | semantic · semantic 2 · transport 0 · unrecorded 0 |

#### Targets

| Target | Calls per run, reference | Calls per run, candidate | Call ratio |
|---|---|---|---|
| *(no target)* | 1–1, mean 1 | 1–2, mean 1.5 | 1.5× |
| `ollama.localhost` (`external`) | 1–1, mean 1 | 1–1, mean 1 | 1.0× |

Latency and status per target, each side summed over its completed repetitions. Latency buckets with no observation are not listed.

| Target | Side | Latency | Span status | HTTP status | 429 |
|---|---|---|---|---|---|
| *(no target)* | reference | ≤5 ms 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 2 | 0 |
| *(no target)* | candidate | ≤5 ms 3 (observed 3, unobserved 0, 2 runs) | ok 3 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 3 | 0 |
| *(no target)* | change | | ok 1 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |
| `ollama.localhost` (`external`) | reference | ≤500 ms 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` (`external`) | candidate | ≤500 ms 2 (observed 2, unobserved 0, 2 runs) | ok 2 · error 0 · unset 0 · unavailable 0 (2 runs) | 1xx 0 · 2xx 2 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `ollama.localhost` (`external`) | change | | ok 0 · error 0 | 2xx 0 · 4xx 0 · 5xx 0 | 0 |

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

**Gate: not recorded.** This execution completed before its gate result was kept (schema 14). No check, limit or verdict is shown, and none is re-derived from today's code.

#### Suggestions

No suggestion.

Produced by `trustvian` (version not recorded) and control plane `v0.13.0`.
