# QuickStart

Try Trustvian in about ten minutes, with nothing but the release archive and
Python 3. You run a small agent we provide, watch it in the browser, gate a
change to it over repeated runs and read the report the gate produced.

No clone, no Go, no model, no Docker and no account. The agent calls no model
and no tool: it emits the OpenTelemetry spans an instrumented support agent
would, and nothing else.

To run **your own** agent instead, see [Getting Started](getting-started.md).
To go further with this one, see the [learning path](learning-path.md).

**You need:**
- macOS or Linux, on amd64 or arm64. On Windows use WSL2: `trustvian dev` and
  `eval run` refuse to run on Windows.
- Python 3.10 or newer, with its `venv` module.
- `git`, with a user name and email configured, because the agent's directory
  becomes a repository with one commit.
- `curl`, and a browser.

Everything below runs in **one terminal**. Every command and its output on
this page are from the published `v0.12.1` archive on macOS, arm64.
`make quickstart-check` runs the same commands, parsed from this page, in CI.

## 1. Install from the archive

```text
trustvian            the CLI
trustvian-local      the local control plane
trustvian-collector  the OTLP receiver and the Trustvian processor
```

The archive holds these three binaries. Keep them side by side: `trustvian`
finds the other two next to itself.

<!-- quickstart-check: run -->
```bash
V=v0.12.1
A=trustvian_${V}_darwin_arm64          # or darwin_amd64, linux_amd64, linux_arm64
curl -sfLO https://github.com/trustvian/trustvian/releases/download/$V/$A.tar.gz
curl -sfLO https://github.com/trustvian/trustvian/releases/download/$V/checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing   # or: sha256sum -c checksums.txt --ignore-missing
tar -xzf $A.tar.gz
export PATH="$PWD/$A:$PATH"
```

<!-- quickstart-check: expect -->
```text
trustvian_v0.12.1_darwin_arm64.tar.gz: OK
```

The checksum line must say `OK`. Then ask each binary its version:

<!-- quickstart-check: run -->
```bash
trustvian version
trustvian-local --version
trustvian-collector --version
```

```text
trustvian v0.12.1
  revision:  5728e8afdd194bc8a05c87f8797b5368a75dc991
  committed: 2026-10-14T20:14:05Z
  go:        go1.27.2
  platform:  darwin/arm64
trustvian-local v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991
trustvian-collector v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991
```

All three name the same release and commit. To verify the archive's signature
and provenance as well, see
[Release Guide § Verifying a published release](release-guide.md#verifying-a-published-release).

*If this does not happen:* a checksum that is not `OK` means the download is
not the published one; delete it and download again. `command not found` means
the `export PATH` line did not run in this terminal.

## 2. Get the agent

The agent is four files in
[`examples/quickstart/`](../examples/quickstart/). Download them into a new
directory, install the agent's pinned dependencies into a virtual environment,
and make the directory a git repository with one commit:

<!-- quickstart-check: run -->
```bash
mkdir trustvian-quickstart && cd trustvian-quickstart
R=https://raw.githubusercontent.com/trustvian/trustvian/main/examples/quickstart
for f in .gitignore agent.py requirements.txt scenario.yaml; do curl -sfLO "$R/$f"; done
python3 -m venv .venv
.venv/bin/pip install -q --disable-pip-version-check -r requirements.txt
git init -q && git add . && git commit -qm 'Trustvian QuickStart agent'
git status --short && ls -A
```

<!-- quickstart-check: expect -->
```text
.git
.gitignore
.venv
agent.py
requirements.txt
scenario.yaml
```

`git status --short` prints nothing: the worktree is clean. Trustvian names the
project after the directory (`trustvian-quickstart`) and the candidate after
the commit, and an untracked file would make the candidate read
`git:<sha>+dirty`. That is why the agent's `.gitignore` already ignores
`.trustvian/`, `.venv/` and `__pycache__/`.

The files are read from `main`: the example is newer than `v0.12.1`, so it is
not at that tag. `requirements.txt` pins every package, with hashes. The
[agent's README](../examples/quickstart/README.md) says exactly what it emits.

*If this does not happen:* `curl` failing on a file means the URL is wrong or
GitHub is unreachable. `ensurepip is not available` means your Python has no
`venv` module (on Debian and Ubuntu, install `python3-venv`).
`Author identity unknown` means git has no user configured: set
`git config --global user.name` and `user.email`, then run the `git` line again.

## 3. Start the control plane

<!-- quickstart-check: background -->
```bash
trustvian-local &
```

<!-- quickstart-check: expect -->
```text
Trustvian local runtime
API:   http://127.0.0.1:57746
Web:   http://127.0.0.1:57746/
Store: sqlite
State: .trustvian/platform.db
Local clients in this directory can now omit --api-url.
Press Ctrl-C to stop.
```

It runs in the background of this terminal, keeps its state in `.trustvian/`
and binds loopback only. Your port differs: **use your own `API:` URL** wherever
this page shows `http://127.0.0.1:57746`.

Open the `Web:` URL in a browser. Nothing is arriving yet, so the page opens on
**Status**, and its suggestion says why Live is empty. `trustvian status`
prints the same document:

<!-- quickstart-check: run -->
```bash
trustvian status
```

```text
{"version":"1",…,"landing":"status","collectors":[],…,"suggestions":[{"rule":"status.no_collector","rule_version":1,"evidence":[{"name":"collectors_reporting","value":"0"},{"name":"fresh_window_seconds","value":"30"}],"text":"No Collector has reported in 30 s. Is 'trustvian dev' running, and is trustvian-collector on its path?"}],"suggestions_truncated":false}
```

`landing` is `status` because nothing is active. The suggestion is the output
of a named rule, `status.no_collector`, with the evidence it read. It is advice
beside the evidence, not a verdict.

*If this does not happen:* if the page does not load, check that the URL is the
`Web:` line this run printed. If `trustvian-local` stopped at once, read its
message: another control plane may already hold `.trustvian/`.

## 4. Check the pipeline

`trustvian dev --check` starts a Collector against this control plane, with no
workload, and prints the status document once the Collector has reported:

<!-- quickstart-check: run -->
```bash
trustvian dev --check --api-url http://127.0.0.1:57746
```

```text
trustvian dev --check: control plane http://127.0.0.1:57746, OTLP receivers http://127.0.0.1:57749 and 127.0.0.1:57750; waiting for the Collector's first status report
{"version":"1",…,"landing":"status","collectors":[{"collector_id":"dev-check",…,"state":"reporting",…,"spans":{"received":"0","evaluated":"0","invalid":"0","analyze_errors":"0"},…}],…,"suggestions":[],"suggestions_truncated":false}
```

A `dev-check` Collector that is `reporting`, with an **empty `suggestions`
list**, is the healthy answer: the helpers start and the Collector reaches the
control plane. It exits `0`, and the Collector stops with it.

*If this does not happen:* exit `3` means a helper did not start or the
Collector did not report within 15 seconds; the document printed beside it says
what the control plane saw. See
[Platform CLI § Checking the pipeline](platform-cli.md#checking-the-pipeline---check).

## 5. Run the agent under Trustvian

<!-- quickstart-check: run -->
```bash
OTEL_SERVICE_NAME=quickstart-agent trustvian dev --api-url http://127.0.0.1:57746 -- .venv/bin/python agent.py
```

<!-- quickstart-check: expect -->
```text
Trustvian dev

  Project     trustvian-quickstart
  Agent       quickstart-agent   (declared by the workload)
  Candidate   git:b569542
  Environment local
  Run         dev-git-b569542-20261015T233557.870Z

  State    ~/.trustvian/dev/5803497345dd
…
  API      http://127.0.0.1:57746
…
  Attached to a control plane this command did not start.
  Owner    existing   (auto: $OTEL_SERVICE_NAME is set)
…
Running python (+1 arguments)

agent: reference run complete, 6 spans delivered

Run dev-git-b569542-20261015T233557.870Z complete.
```

`dev` runs the command unchanged, with an OTLP receiver around it, and exits
with the command's status. It does not add an SDK: the agent already sends
OpenTelemetry, and `dev` only sets the standard `OTEL_EXPORTER_OTLP_*`
variables it names on `Set` (shortened here). `OTEL_SERVICE_NAME` is the agent
Trustvian reports on. The agent's default mode is `reference`.

**In the browser.** When spans arrive while Status is open, a line at the top
says so and offers **Open Live**; the page does not move by itself. In **Live**:

- **The card.** `quickstart-agent` appears under *Active now* with no
  identifier typed, and is selected. The strip above names its environment and
  run, and the run's observation and behavior counts, read from the control
  plane.
- **The graph.** The agent on the left, one edge per behavior, each target on
  the right: `tool crm_lookup → crm.localhost`, `tool knowledge_search →
  kb.localhost`, `tool send_reply → mail.localhost`, the model call
  `external stub-model-v1 → stub`, and the `quickstart-agent` invocation, which
  has no target. Each arriving observation sends one pulse along its edge.
- **NEW.** Each behavior is new to this run's evidence the first time it
  arrives, so its edge is dashed and its target carries `NEW`. New means new,
  not dangerous.
- **Decision colors.** Every edge, card and timeline row is styled by its
  decision, and every state also carries its word. Here each one reads
  `observe` with risk `low`: the default policy observes. A `block` would be
  styled differently, and still say `block`.
- **The inspector.** Click an edge to read the decision, risk, trust, anomaly
  and confidence the server computed for it. They are five separate readings:
  there is no combined score.

After the agent exits, **Status** shows its spans. `trustvian status` again:

<!-- quickstart-check: run -->
```bash
trustvian status
```

```text
{"version":"1",…,"landing":"live","collectors":[{"collector_id":"dev",…,"evaluation_run_id":"dev-git-b569542-20261015T233557.870Z","spans":{"received":"6","evaluated":"6","invalid":"0","analyze_errors":"0"},"producers":[{"service_name":"quickstart-agent","spans":"6",…,"sdk":{"name":"opentelemetry","language":"python","version":"1.45.1"}}],…,"models":{"reported":true,"calls":[{"provider":"stub","model":"stub-model-v1","calls":"1"}],"truncated":false},"fidelity":{"reported":true,"semantic":"6","transport":"0"},…
```

The run was shorter than the Collector's ten-second reporting interval, and its
six spans are still counted: the Collector reports once more when it stops.
All six were named by the GenAI convention (`semantic`), and the one model call
is listed by its declared name. Nothing in the document is content: names and
counts only.

*If this does not happen:* if Live stays empty, read Status's suggestions.
`status.no_producer` means the Collector saw no span; check that the command
ran under `trustvian dev`. `status.spans_without_service_name` means spans
arrived without `OTEL_SERVICE_NAME`. If `dev` stops before running the agent
with an ownership message, `OTEL_SERVICE_NAME` was not set on its line; see
[instrumentation ownership](platform-cli.md#instrumentation-ownership).

## 6. Gate a change

A scenario runs a reference and a candidate `runs` times each and gates the
difference. The candidate mode differs from the reference in exactly two ways:
it adds one tool, `export_data`, and calls `knowledge_search` twice as often.
This is the downloaded `scenario.yaml`:

<!-- quickstart-check: file scenario.yaml -->
```yaml
# The QuickStart's scenario: the agent's reference mode against its candidate
# mode, three runs each. Both sides declare the same model, read from the
# variable the agent itself reads.
version: v1
name: quickstart
runs: 3
instrumentation: existing
reference:
  command: [.venv/bin/python, agent.py]
  env: {QUICKSTART_MODE: reference, QUICKSTART_MODEL: stub-model-v1, OTEL_SERVICE_NAME: quickstart-agent}
  model_env: QUICKSTART_MODEL
candidate:
  command: [.venv/bin/python, agent.py]
  env: {QUICKSTART_MODE: candidate, QUICKSTART_MODEL: stub-model-v1, OTEL_SERVICE_NAME: quickstart-agent}
  model_env: QUICKSTART_MODEL
gate:
  added_candidate_presence_minimum: 1        # k
  added_reference_presence_maximum: 0        # j
  max_repeated_added_behaviors: 0
  max_block_decisions_per_run: 0
  max_critical_risk_observations_per_run: 0
```

`model_env` makes each side declare the model it ran with, read from its own
environment. It is a declaration, never an observation and never content.

<!-- quickstart-check: run -->
```bash
trustvian eval run --scenario scenario.yaml
echo "exit $?"
```

<!-- quickstart-check: expect -->
```text
…
quickstart   runs 3   (execution scn-quickstart-20261015T233559-1527db69)
Recorded
  reference: scenario sha256:81f1ef763a869d6edcddd8f87707cc87092a8c575e9a352df0fa9beb7761b2f8   inputs not_declared   model stub-model-v1   prompt_ref not_recorded
  candidate: scenario sha256:81f1ef763a869d6edcddd8f87707cc87092a8c575e9a352df0fa9beb7761b2f8   inputs not_declared   model stub-model-v1   prompt_ref not_recorded

Behavior                                  reference   candidate
  tool/crm_lookup → crm.localhost                3/3         3/3
  tool/send_reply → mail.localhost               3/3         3/3
  tool/export_data → export.localhost            0/3         3/3   + added
  external/stub-model-v1 → stub                  3/3         3/3
  tool/knowledge_search → kb.localhost           3/3         3/3
  tool/quickstart-agent                          3/3         3/3

Gate (k = 1, j = 0)
  PASS reference repetitions completed: 3 (== 3)
  PASS candidate repetitions completed: 3 (== 3)
  PASS repetitions failing minimum evidence: 0 (<= 0)
  FAIL repeatedly added behaviors: 1 (<= 0)
  PASS worst candidate block decisions: 0 (<= 0)   advisory: fresh scope
  PASS worst candidate critical risk observations: 0 (<= 0)   advisory: fresh scope

Sameness
  same_scenario    true
  same_inputs      true
  same_model       true
  same_prompt_ref  not_recorded
…

Gate: FAIL
exit 1
```

Before the result, `eval run` prints each of its six repetitions: one `dev` run
per repetition, each in a learning scope of its own (shortened here to `…`).

- **The verdict.** `export_data` is in every candidate run and in no reference
  run, so it is *added*, and the gate fails with exit `1`: `0` is PASS, `1` a
  gate FAIL, `2` a usage error and `3` an operational one.
- **The frequency change** passes this gate: `knowledge_search` is present in
  3/3 runs on both sides. Presence alone does not see it. The report, next,
  does.
- **Sameness.** Both sides ran the same scenario and the same declared model.
  No prompt reference was declared, so that answer is `not_recorded`, which is
  not `false`.

*If this does not happen:* exit `3` means a repetition's workload failed or the
control plane could not be reached; the repetition output above the result
names which. A `+dirty` candidate means an untracked or changed file; commit or
ignore it. See
[Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run).

## 7. Read the report

Every completed execution keeps its report. Use your execution id, from the
first line of the result:

<!-- quickstart-check: run -->
```bash
trustvian eval report scn-quickstart-20261015T233559-1527db69 --format md
```

<!-- quickstart-check: expect -->
```text
### Trustvian behavioral gate — FAIL

**Scenario** `quickstart` · **runs** 3 · **execution** `scn-quickstart-20261015T233559-1527db69`

#### Sameness

| | Reference | Candidate | Same |
|---|---|---|---|
| Scenario | `sha256:81f1ef763a869d6edcddd8f87707cc87092a8c575e9a352df0fa9beb7761b2f8` | `sha256:81f1ef763a869d6edcddd8f87707cc87092a8c575e9a352df0fa9beb7761b2f8` | same |
| Inputs | none declared | none declared | same |
| Model | `stub-model-v1` | `stub-model-v1` | same |
| Prompt reference | not recorded | not recorded | not recorded |

No sameness warning.

#### Behaviors

Added: present in at least 1 of 3 candidate runs and at most 0 of 3 reference runs. Removed: present in at least 1 of 3 reference runs and at most 0 of 3 candidate runs.

| Behavior | Fingerprint | Reference | Candidate | Calls per run, reference | Calls per run, candidate | Classification | Lost | Fidelity, reference | Fidelity, candidate |
|---|---|---|---|---|---|---|---|---|---|
| `ai_agent` · `tool` · `crm_lookup` · `crm.localhost` · `local` | `260bb3295267d29b` | 3/3 | 3/3 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 3 · transport 0 · unrecorded 0 | semantic · semantic 3 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `send_reply` · `mail.localhost` · `local` | `557882e3a904d480` | 3/3 | 3/3 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 3 · transport 0 · unrecorded 0 | semantic · semantic 3 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `export_data` · `export.localhost` · `local` | `7044b0468a915c5f` | 0/3 | 3/3 | 0–0, mean 0 | 1–1, mean 1 | **+ added** | no | — | semantic · semantic 3 · transport 0 · unrecorded 0 |
| `ai_agent` · `external` · `stub-model-v1` · `stub` · `local` | `98088330cff362c2` | 3/3 | 3/3 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 3 · transport 0 · unrecorded 0 | semantic · semantic 3 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `knowledge_search` · `kb.localhost` · `local` | `de845ba7d3336de2` | 3/3 | 3/3 | 2–2, mean 2 | 4–4, mean 4 | unclassified | no | semantic · semantic 6 · transport 0 · unrecorded 0 | semantic · semantic 12 · transport 0 · unrecorded 0 |
| `ai_agent` · `tool` · `quickstart-agent` · `local` | `dea545e8d0203c64` | 3/3 | 3/3 | 1–1, mean 1 | 1–1, mean 1 | unclassified | no | semantic · semantic 3 · transport 0 · unrecorded 0 | semantic · semantic 3 · transport 0 · unrecorded 0 |

#### Targets

| Target | Calls per run, reference | Calls per run, candidate | Call ratio |
|---|---|---|---|
| *(no target)* | 1–1, mean 1 | 1–1, mean 1 | 1.0× |
| `crm.localhost` | 1–1, mean 1 | 1–1, mean 1 | 1.0× |
| `export.localhost` | 0–0, mean 0 | 1–1, mean 1 | not available |
| `kb.localhost` | 2–2, mean 2 | 4–4, mean 4 | 2.0× |
| `mail.localhost` | 1–1, mean 1 | 1–1, mean 1 | 1.0× |
| `stub` | 1–1, mean 1 | 1–1, mean 1 | 1.0× |

Latency and status per target, each side summed over its completed repetitions. Latency buckets with no observation are not listed.

| Target | Side | Latency | Span status | HTTP status | 429 |
|---|---|---|---|---|---|
…
| `export.localhost` | reference | not available | not available | not available | not available |
| `export.localhost` | candidate | ≤5 ms 3 (observed 3, unobserved 0, 3 runs) | ok 0 · error 0 · unset 3 · unavailable 0 (3 runs) | 1xx 0 · 2xx 3 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `kb.localhost` | reference | ≤5 ms 6 (observed 6, unobserved 0, 3 runs) | ok 0 · error 0 · unset 6 · unavailable 0 (3 runs) | 1xx 0 · 2xx 6 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `kb.localhost` | candidate | ≤5 ms 12 (observed 12, unobserved 0, 3 runs) | ok 0 · error 0 · unset 12 · unavailable 0 (3 runs) | 1xx 0 · 2xx 12 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 0 | 0 |
| `kb.localhost` | change | | ok 0 · error 0 | 2xx 6 · 4xx 0 · 5xx 0 | 0 |
…
| `stub` | reference | ≤500 ms 3 (observed 3, unobserved 0, 3 runs) | ok 0 · error 0 · unset 3 · unavailable 0 (3 runs) | 1xx 0 · 2xx 0 · 3xx 0 · 4xx 0 · 5xx 0 · unavailable 3 | 0 |
…

#### Tokens and cost

| | Reference | Candidate | Change |
|---|---|---|---|
| Input tokens | 3600 | 3600 | 0 |
| Output tokens | 900 | 900 | 0 |
| Unsplit tokens | 0 | 0 | 0 |
| Observations with usage | 3 | 3 | 0 |
| Observations without usage | 15 | 24 | 9 |

Cost: not available — no pricing table is configured (`pricing_not_configured`).

#### Gate

**Verdict: FAIL**, as the execution recorded it when it completed.

| Gate check | Actual | Rule | Bound | Outcome | Note |
|---|---|---|---|---|---|
| reference repetitions completed | 3 | equals | 3 | pass |  |
| candidate repetitions completed | 3 | equals | 3 | pass |  |
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

- `compare.added_in_all_runs`: `tool/export_data → export.localhost was added in 3/3 candidate runs. If this was expected, add it to the scenario; otherwise investigate before promoting.`
  Evidence: `fingerprint_id` `7044b0468a915c5f` · `behavior` `tool/export_data → export.localhost` · `candidate_runs_present` `3` · `reference_runs_present` `0` · `runs` `3`

Produced by `trustvian` `v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991` and control plane `v0.12.1 5728e8afdd194bc8a05c87f8797b5368a75dc991`.
```

This Markdown is exactly what a pull request comment shows, after its commit,
run link and exit code lines. Read it section by section:

- **The added behavior.** `export_data → export.localhost` is `0/3` against
  `3/3` and classified **+ added**; it is the one gate check that failed, and
  the one suggestion. Its fingerprint, `7044b0468a915c5f`, is the behavior's
  identity: actor type, category, operation, target and environment, and
  nothing about the call's content.
- **The frequency change.** `knowledge_search` is `2–2, mean 2` calls per run
  against `4–4, mean 4`, and its target `kb.localhost` carries a `2.0×` call
  ratio. It stays *unclassified*: present on both sides, so neither added nor
  removed. No suggestion names it either, because the rule table's load rule
  needs 429s, and there were none.
- **Targets.** `export.localhost` reads *not available* on the reference side,
  which never called it. That is not zero, and its call ratio is not available
  either. Every figure is an integer or a display string the control plane
  formatted.
- **Tokens.** 1200 input and 300 output tokens per run, from the model-call
  span's `gen_ai.usage.*` attributes, 3600 and 900 over three runs on each
  side. 15 and 24 observations carried no usage: every tool call. Cost is *not
  available*, because no pricing table is configured; Trustvian ships no
  prices.
- **The gate**, as the execution recorded it when it completed, is never
  re-evaluated. The four optional frequency checks read *not configured*,
  because this scenario sets none.

The same document as JSON carries `comparison.counts`, the totals the
Overview shows:

<!-- quickstart-check: run -->
```bash
trustvian eval report scn-quickstart-20261015T233559-1527db69 --json | python3 -c 'import json, sys; print(json.load(sys.stdin)["comparison"]["counts"])'
```

<!-- quickstart-check: expect -->
```text
{'rows': '6', 'added': '1', 'removed': '0', 'neither': '5', 'not_recorded': '0', 'lost': '0'}
```

**In the browser.** Choose **Change Impact**: the execution is first in the
list of recorded scenario executions, and choosing it renders the same report
as tables, with the same figures. Each behavior row lists the repetitions that
observed it, `R1`…`R3` and `C1`…`C3`; choosing one opens that repetition's
observations in **Evidence**. **Overview**'s **Latest comparison** panel shows
the execution's recorded verdict, its four sameness answers, the first
suggestion and the added, removed and lost counts from `comparison.counts`;
**Open in Change Impact** opens the whole report.

*If this does not happen:* `eval report` exits `3` with nothing on stdout when
the execution id is wrong (`404`), or the execution is still running or failed
(`409`). Use the id `eval run` printed.

## 8. Stop, and where next

Stop the control plane when you are done. Its state stays in `.trustvian/`, so
starting it again in this directory shows everything you recorded:

<!-- quickstart-check: run -->
```bash
kill %1
```

- **Your own agent:** [Getting Started](getting-started.md), with an agent
  that already sends OpenTelemetry.
- **A real model and framework:** the companion repositories run model-driven
  agents under Trustvian:
  [`trustvian-langgraph-quickstart`](https://github.com/trustvian/trustvian-langgraph-quickstart),
  [`trustvian-crewai-quickstart`](https://github.com/trustvian/trustvian-crewai-quickstart),
  [`trustvian-vllm-quickstart`](https://github.com/trustvian/trustvian-vllm-quickstart)
  and
  [`trustvian-python-agent-demo`](https://github.com/trustvian/trustvian-python-agent-demo).
- **Your pull requests:** [Running behavioral scenarios in GitHub Actions](ci-github-action.md).
- **Everything above, level by level:** the [learning path](learning-path.md).

## What this agent cannot show you

It is deterministic: each mode does the same thing on every run, so `3/3`
against `0/3` is the sampler-free case, and an unchanged candidate always
passes. **A model-driven agent is not deterministic.** Before you trust a FAIL
from your own agent, measure how often it fails against itself:
[Calibrating `N`, `k` and `j`](platform-cli.md#calibrating-n-k-and-j).

It calls no model, so the latencies are its fixed durations and its tokens are
fixed counts. They show that the sections exist and how to read them, not what
a model costs or how fast it answers. For that, see
[the measured Model Change Report](results/2026-10-12-model-change-report/README.md),
two real models through a real agent.
