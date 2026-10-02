### Trustvian behavioral suite — CLI exit code 3

**Commit** `0123456789abcdef0123456789abcdef01234567` · [workflow run](https://github.com/trustvian/trustvian/actions/runs/1000000001/attempts/1)

A suite has no verdict of its own: each scenario below carries its own, and the CLI exit code is the most severe of them.

**Suite** `cmd/trustvian-ci-render/testdata/scenarios/suite-error` · **scenarios** 2 · **scenario timeout** `5m0s` · **fail-fast** no

**Recorded summary:** passed 1 · failed 0 · errors 1 · skipped 0

| File | Scenario | Runs | Outcome | Exit | Detail |
|---|---|---|---|---|---|
| `a-pass.yaml` | `err-pass` | 2 | PASS | 0 | execution `scn-err-pass-20261002T184517-32cba94a` |
| `b-operational.yaml` | `err-operational` | 2 | error | 3 | `repetition_failed`: `a repetition failed: candidate repetition 1 of 2 (run scn-err-operational-20261002T184518-c23c8e22-candidate-1) exited 5; the scenario stopped and no verdict was produced` |

#### `a-pass.yaml` — PASS

**Scenario** `err-pass` · **runs** 2 · **execution** `scn-err-pass-20261002T184517-32cba94a`

Added: present in at least 2 of 2 candidate runs and at most 0 of 2 reference runs. Removed: present in at least 2 of 2 reference runs and at most 0 of 2 candidate runs.

| Behavior | Fingerprint | Reference | Candidate | Classification |
|---|---|---|---|---|
| `ai_agent` · `tool` · `crm_lookup` · `tools.localhost` · `local` | `3e2da711ec218a24` | 2/2 | 2/2 | unclassified |
| `ai_agent` · `tool` · `knowledge_search` · `tools.localhost` · `local` | `8dcf62b0a624c416` | 2/2 | 2/2 | unclassified |

| Gate check | Actual | Rule | Bound | Outcome | Advisory |
|---|---|---|---|---|---|
| reference repetitions completed | 2 | equals | 2 | pass |  |
| candidate repetitions completed | 2 | equals | 2 | pass |  |
| repetitions failing minimum evidence | 0 | at most | 0 | pass |  |
| repeatedly added behaviors | 0 | at most | 0 | pass |  |
| worst candidate block decisions per run | 0 | at most | 0 | pass | fresh scope |
| worst candidate critical-risk observations per run | 0 | at most | 0 | pass | fresh scope |

*Fresh scope:* each repetition learns in a scope of its own, so the engine has learned nothing there and these checks cannot answer the learned-policy question. The marker changes no verdict.

The scenario's limits: at most 0 repeatedly added behaviors; at most 0 block decisions and 0 critical-risk observations in any one candidate run.

Produced by `trustvian` `v0.9.1-0.20261002183633-3e86f9cead25 3e86f9cead2586e7ee90e14185a8f34074b54240` and control plane `(devel) 3e86f9cead2586e7ee90e14185a8f34074b54240`.

Produced by `trustvian` `v0.9.1-0.20261002183633-3e86f9cead25 3e86f9cead2586e7ee90e14185a8f34074b54240`.
