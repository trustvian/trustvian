# From first run to a release gate

A learning path through Trustvian, level by level. Each level continues with
the [QuickStart](quickstart.md)'s agent and its `trustvian-quickstart`
directory, and adds one question Trustvian can answer.

Every command shown here was run against the published `v0.12.1` archive, and
its output is pasted as printed, shortened only with `…`. Your ports, commit
SHAs, run identifiers and execution identifiers differ. Where a level needs a
real model, it points to a companion repository or to Trustvian's
[measured report](results/2026-10-12-model-change-report/README.md); no number
here is invented.

## What Trustvian answers that a trace viewer does not

A trace viewer shows what one run did. Trustvian compares what two versions of
an agent did, over repeated runs, and gates the difference.

| Question | Answered by | Doc |
|---|---|---|
| Which behaviors changed between two versions, across N runs, and how often? | Presence `k/N` per side, calls per run (min–max, mean) and `lost`, per behavior | [Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run) |
| Which targets take more load? | Calls per run and the call ratio per target, latency buckets, span and HTTP status classes, and 429s | [Web interface § Change Impact](webui.md#change-impact) |
| Did both sides run the same thing? | Sameness: scenario digest, input digest, declared model and prompt reference, with warnings beside the gate | [Platform CLI § Provenance](platform-cli.md#provenance-what-each-side-ran) |
| Should this change ship? | A deterministic gate with its evidence, and one report, identical in the CLI, the WebUI and the pull request comment | [Platform CLI § One execution's report](platform-cli.md#one-executions-report-eval-report) |
| Can I get all of that without sending content anywhere? | Yes: all of it comes from span metadata, on your machine, and no prompt, completion, tool argument or result is read | [OpenTelemetry § content](OPENTELEMETRY.md#what-is-not-read) |

What it does not give you: an aggregate score. Trust, anomaly, confidence,
risk and decision are separate readings, and nothing combines them into one
number. Suggestions are the outputs of named rules beside the evidence, never a
verdict.

## Level 1, beginner — See it

**The question.** What is my agent doing right now, and what did Trustvian
decide about each call?

**The feature.** The Status view, `trustvian status` and `trustvian dev
--check` (`v0.12.0`, [task 105](tasks/v0.12/105-pipeline-status-surface.md));
the Live Observatory (`v0.10.0`) and its inspector.

**Run it.** With the QuickStart's control plane running, read the stream Live
reads while the agent runs. `curl` prints each observation frame:

```bash
curl -sN --max-time 8 http://127.0.0.1:57923/v1/realtime | grep --line-buffered '"kind":"observation"' &
OTEL_SERVICE_NAME=quickstart-agent trustvian dev --api-url http://127.0.0.1:57923 -- .venv/bin/python agent.py
```

**What you see.** The first frame, as the server sent it, and the fourth:

```text
data: {"version":"1","kind":"observation","scope":{"project_id":"trustvian-quickstart","agent_id":"quickstart-agent","candidate_id":"git:066d627","run_id":"dev-git-066d627-20261015T234140.683Z","environment":"local","behavioral_profile":"git:066d627"},"observation":{"sequence":"1","record_count":"1","behavior_complete":true,"fingerprint_id":"98088330cff362c2","behavior":{"actor_type":"ai_agent","operation_category":"external","operation_name":"stub-model-v1","target_name":"stub","environment":"local"},"fidelity":"semantic","behavior_layer":"model","decision":"observe_only","risk_level":"low","trust_score":1,"anomaly_score":1,"anomaly_confidence":0,"new_behavior":true}}
…
data: {…,"observation":{"sequence":"4","record_count":"4","behavior_complete":true,"fingerprint_id":"de845ba7d3336de2","behavior":{"actor_type":"ai_agent","operation_category":"tool","operation_name":"knowledge_search","target_name":"kb.localhost","environment":"local"},"fidelity":"semantic","behavior_layer":"tool","decision":"observe_only","risk_level":"low","trust_score":0.9525,"anomaly_score":0.95,"anomaly_confidence":0.05,"new_behavior":false}}
```

Live draws its graph from these frames: the card for `quickstart-agent`, one
edge per `fingerprint_id`, a pulse per frame, `NEW` where `new_behavior` is
true, and each edge, card and timeline row styled by its `decision`, with the
decision's word beside it. Clicking an edge opens the inspector with the same
five readings, the three scores printed to two decimal places. Counts on the
strip are read from the control plane, never accumulated from frames.

**How to read it.**
- **A behavior is a fingerprint**: actor type, operation category, operation
  name, target and environment, the `behavior` object above. It is not the
  call's arguments, its result, or its timing. Two different agents doing the
  same thing produce the same fingerprint, which is why Live draws one run at a
  time ([why](webui.md#one-run-at-a-time-on-purpose)).
- **Five readings, never one.** `decision`, `risk_level`, `trust_score`,
  `anomaly_score` and `anomaly_confidence` are separate. A first-ever behavior
  reads anomaly `1` at confidence `0`: maximally novel, and no evidence yet to
  trust that reading, so it costs no trust. The second `knowledge_search` call
  in the same run already reads anomaly `0.95` at confidence `0.05`.
- **`NEW` means new to this run's evidence.** Running the agent again on the
  same commit reuses its baseline, so the next run's first `crm_lookup` reads
  confidence `0.05` and is still `new_behavior: true` in that run.
- **Content-free.** The frame holds names and numbers. The agent sends no
  prompt or argument, and Trustvian would not read one if it did
  ([the content attributes it refuses](OPENTELEMETRY.md#what-is-not-read)).
- **Following and pinning.** Live follows the newest active run until you
  click a card; then it is pinned to your choice, and **Follow active** goes
  back.

**What not to conclude.**
- `observe` is not "safe". The default policy observes everything; a decision
  is only as meaningful as the [policy](policy-guide.md) that produced it.
- `NEW` is not "dangerous", and a dashed edge is not an alert.
- Several actors inside one `trustvian dev` run draw as one agent: the stream
  frame carries the run's scope, not the observation's actor. Following one
  actor, and pausing Live without disconnecting, are planned
  ([task 109](ROADMAP.md#v0130--proposed)).

**Reference.** [Web interface § The Status view](webui.md#the-status-view),
[§ The Live Observatory](webui.md#the-live-observatory),
[§ The inspector](webui.md#the-inspector);
[Platform CLI § `trustvian dev`](platform-cli.md#trustvian-dev).

**You can now answer:** is my agent's telemetry arriving, which behaviors did
it just perform, and what did Trustvian decide about each one?

## Level 2 — Compare two runs

**The question.** What does the changed agent do that the old one did not?

**The feature.** `trustvian eval compare` (`v0.10.0`): the server's gate over
two runs' behaviors.

**Run it.** One run of each mode, named, then the comparison:

```bash
QUICKSTART_MODE=reference OTEL_SERVICE_NAME=quickstart-agent trustvian dev --api-url http://127.0.0.1:57923 --run-id l2-reference -- .venv/bin/python agent.py
QUICKSTART_MODE=candidate OTEL_SERVICE_NAME=quickstart-agent trustvian dev --api-url http://127.0.0.1:57923 --run-id l2-candidate -- .venv/bin/python agent.py
trustvian eval compare --reference-run l2-reference --candidate-run l2-candidate --max-added-behaviors 0 --max-block-decisions 0 --max-critical-risk-observations 0; echo "exit $?"
```

**What you see.**

```text
…
Run l2-reference complete.
…
Run l2-candidate complete.
Evaluation comparison
Reference: l2-reference
Candidate: l2-candidate

Behaviors: +1 / -0 / 5 shared
Counted changes: 1 (policy 1)

Gate checks:
  PASS Reference evidence: 6 (minimum 1)
  PASS Candidate evidence: 9 (minimum 1)
  FAIL Added behaviors: 1 (maximum 0)
  PASS Block decisions: 0 (maximum 0)
  PASS Critical risk observations: 0 (maximum 0)
  ---- Added behavior changes: not evaluated (no limit supplied)

Gate: FAIL
exit 1
```

**How to read it.**
- **Added and removed** are set differences of fingerprints: `+1` is
  `export_data`, and nothing was removed.
- **Evidence** counts observations, 6 and 9: one per span.
- **A single comparison sees presence, not frequency.** `knowledge_search`
  ran twice as often in the candidate, and it is one of the 5 shared behaviors.

**What not to conclude.**
- **One run is not enough for an agent driven by a model.** This agent does
  the same thing every time. A model-driven agent does not: task 078 measured
  an unchanged agent's behavior set moving between isolated runs, so a single
  comparison at zero failed half of the unchanged pairs
  ([the measurement](platform-cli.md#calibrating-n-k-and-j)). A FAIL from one
  pair of runs may be the sampler, not your change. Level 3 is the answer.
- Both runs here learned into one baseline, because they share a commit. That
  changes their readings, never which behaviors they performed.

**Reference.** [Platform CLI § CI: comparing a candidate against a reference](platform-cli.md#ci-comparing-a-candidate-against-a-reference),
[§ Exit codes](platform-cli.md#exit-codes).

**You can now answer:** which behaviors one version added or removed, against
another, in one run each, and why that is not yet evidence for a model-driven
agent.

## Level 3, intermediate — Repeat and gate

**The question.** Is a difference repeatable, and did both sides run the same
thing?

**The feature.** Scenarios and `trustvian eval run` (`v0.10.0`), the gate's six
checks, and exit codes; sameness, digests and warnings (`v0.12.0`,
[task 086](tasks/v0.12/086-scenario-and-input-versioning.md)); `trustvian eval
report` and Change Impact (`v0.12.0`,
[task 107](tasks/v0.12/107-change-impact-view-and-report.md)).

**Run it.** First calibrate: a scenario whose candidate is the reference. Then
change only the model the candidate declares:

```bash
sed -e 's/^name: quickstart/name: quickstart-self/' -e '/^candidate:/,/^gate:/s/QUICKSTART_MODE: candidate/QUICKSTART_MODE: reference/' scenario.yaml > self.yaml
sed -e 's/^name: quickstart/name: quickstart-model/' -e '/^candidate:/,/^gate:/s/QUICKSTART_MODE: candidate, QUICKSTART_MODEL: stub-model-v1/QUICKSTART_MODE: reference, QUICKSTART_MODEL: stub-model-v2/' scenario.yaml > model-change.yaml
diff scenario.yaml self.yaml; diff scenario.yaml model-change.yaml
git add self.yaml model-change.yaml && git commit -qm 'Add calibration and model-change scenarios'
trustvian eval run --scenario self.yaml 2>/dev/null; echo "exit $?"
trustvian eval run --scenario model-change.yaml 2>/dev/null; echo "exit $?"
```

Commit the scenarios first: an untracked file makes the candidate
`git:<sha>+dirty`. `2>/dev/null` hides the repetitions' own output.

**What you see.**

```text
5c5
< name: quickstart
---
> name: quickstart-self
14c14
<   env: {QUICKSTART_MODE: candidate, QUICKSTART_MODEL: stub-model-v1, OTEL_SERVICE_NAME: quickstart-agent}
---
>   env: {QUICKSTART_MODE: reference, QUICKSTART_MODEL: stub-model-v1, OTEL_SERVICE_NAME: quickstart-agent}
5c5
< name: quickstart
---
> name: quickstart-model
14c14
<   env: {QUICKSTART_MODE: candidate, QUICKSTART_MODEL: stub-model-v1, OTEL_SERVICE_NAME: quickstart-agent}
---
>   env: {QUICKSTART_MODE: reference, QUICKSTART_MODEL: stub-model-v2, OTEL_SERVICE_NAME: quickstart-agent}
quickstart-self   runs 3   (execution scn-quickstart-self-20261015T234221-9a96edb0)
…
Gate (k = 1, j = 0)
  PASS reference repetitions completed: 3 (== 3)
  PASS candidate repetitions completed: 3 (== 3)
  PASS repetitions failing minimum evidence: 0 (<= 0)
  PASS repeatedly added behaviors: 0 (<= 0)
  PASS worst candidate block decisions: 0 (<= 0)   advisory: fresh scope
  PASS worst candidate critical risk observations: 0 (<= 0)   advisory: fresh scope
…
Gate: PASS
exit 0
quickstart-model   runs 3   (execution scn-quickstart-model-20261015T234228-43cfa413)
Recorded
  reference: scenario sha256:faf622a54cd24a68efa7c2f6f307f6bfe017c81e66286e8860df64793cd31d85   inputs not_declared   model stub-model-v1   prompt_ref not_recorded
  candidate: scenario sha256:faf622a54cd24a68efa7c2f6f307f6bfe017c81e66286e8860df64793cd31d85   inputs not_declared   model stub-model-v2   prompt_ref not_recorded

Behavior                                  reference   candidate
  tool/crm_lookup → crm.localhost                3/3         3/3
  tool/send_reply → mail.localhost               3/3         3/3
  external/stub-model-v1 → stub                  3/3         0/3   - removed
  tool/knowledge_search → kb.localhost           3/3         3/3
  tool/quickstart-agent                          3/3         3/3
  external/stub-model-v2 → stub                  0/3         3/3   + added

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
  same_model       false
  same_prompt_ref  not_recorded
  reference: scenario sha256:faf622a54cd24a68efa7c2f6f307f6bfe017c81e66286e8860df64793cd31d85   inputs not_declared   model stub-model-v1   prompt_ref not_recorded
  candidate: scenario sha256:faf622a54cd24a68efa7c2f6f307f6bfe017c81e66286e8860df64793cd31d85   inputs not_declared   model stub-model-v2   prompt_ref not_recorded
  warning model_differs: The reference and candidate sides declared different models.

Gate: FAIL
exit 1
```

**How to read it.**
- **`runs`, `k` and `j`.** Each side runs `runs` times, each repetition in a
  learning scope of its own. A behavior is *added* when it is in at least `k`
  candidate runs and at most `j` reference runs; *removed* is the mirror.
  `k = 1, j = 0` is guidance for a workload you have not measured, not a
  default.
- **Calibrate before you trust a FAIL.** `self.yaml` compares the agent with
  itself, so every FAIL would be a false one. For this deterministic agent it
  passes; for a model-driven one, run it `M` times and choose `runs`, `k` and
  `j` where the false-FAIL count is zero
  ([the procedure and its cost](platform-cli.md#calibrating-n-k-and-j)).
- **The six checks** are both sides' repetitions, minimum evidence, repeatedly
  added behaviors, and the worst candidate run's block decisions and
  critical-risk observations. The last two are marked *advisory: fresh scope*:
  a repetition that learned nothing cannot answer a learned-policy question.
- **Exit codes:** `0` PASS, `1` gate FAIL, `2` usage, `3` operational. A broken
  workload is `3`, never a gate failure.
- **Sameness.** `same_model false` and the warning `model_differs` are a fact,
  not advice: a changed model is often the point of the comparison. No check
  reads sameness, and it never changes the verdict. A model change also always
  changes the model behavior's identity, so the `- removed` and `+ added` model
  rows are the change itself. The env *values* are not digested, which is why
  both sides share one scenario digest.
- **`not_recorded` is not `false`.** No prompt reference was declared on either
  side.

The report of every execution, sameness first, is `trustvian eval report <id>`
(`--format md` for Markdown, `--json` for the document); **Change Impact**
renders the same document.

**What not to conclude.**
- A PASS against yourself in `M` runs describes those `M` runs. It does not
  make the false-FAIL rate zero.
- A digest is computed by the CLI and stored by the control plane, which cannot
  recompute it: it is exactly as trustworthy as the client that sent it.
- `same_inputs true` with `inputs not_declared` on both sides means neither
  side declared inputs, not that they were compared.

**Reference.** [Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run),
[§ Provenance](platform-cli.md#provenance-what-each-side-ran),
[§ Calibrating `N`, `k` and `j`](platform-cli.md#calibrating-n-k-and-j);
[Web interface § Change Impact](webui.md#change-impact).

**You can now answer:** is a behavioral difference repeatable across `N` runs
at limits I calibrated, did both sides run the same scenario and model, and how
does the gate say so in its exit code?

## Level 4 — Load, latency, tokens and cost

**The question.** How often did behavior change, where did the load go, and
what did the tokens cost?

**The feature.** Frequency evidence and its optional limits (`v0.12.0`,
[task 106](tasks/v0.12/106-frequency-evidence.md)), persisted fidelity and
`max_llm_calls_per_run` (`v0.12.0`,
[task 081](tasks/v0.12/081-persisted-behavior-fidelity.md)), operational
evidence, tokens and cost (`v0.12.0`,
[task 087](tasks/v0.12/087-performance-and-cost-evidence.md)), and the
comparison rule table.

**Run it.** Add the four optional limits to a copy of the scenario, write an
illustrative pricing table, and restart the control plane with it:

```bash
sed 's/^name: quickstart/name: quickstart-limits/' scenario.yaml > limits.yaml
cat >> limits.yaml <<'EOF'
  min_candidate_frequency: 3
  max_lost_behaviors: 0
  max_calls_per_run:
    - {target: kb.localhost, max: 2}
  max_llm_calls_per_run: 1
EOF
cat > pricing.yaml <<'EOF'
version: 1
pricing_version: "quickstart-illustrative-1"
source: "illustrative prices for the QuickStart's stub model, not a provider's"
currency: USD
models:
  - model: stub-model-v1
    input_micros_per_million_tokens: 1000000
    output_micros_per_million_tokens: 4000000
EOF
git add limits.yaml pricing.yaml && git commit -qm 'Add frequency limits and an illustrative pricing table'
kill %1
trustvian-local --pricing pricing.yaml &
trustvian eval run --scenario limits.yaml 2>/dev/null; echo "exit $?"
trustvian eval report scn-quickstart-limits-20261015T234242-426328e9 --format md
```

**What you see.**

```text
Trustvian local runtime
API:   http://127.0.0.1:58141
Web:   http://127.0.0.1:58141/
Store: sqlite
State: .trustvian/platform.db
Pricing: quickstart-illustrative-1 (sha256:87c0fb70417997bd4745706ca9e40ffbe17c09a4ca9a51a44260605d8b32cac1)
Local clients in this directory can now omit --api-url.
Press Ctrl-C to stop.
quickstart-limits   runs 3   (execution scn-quickstart-limits-20261015T234242-426328e9)
…
Gate (k = 1, j = 0)
  PASS reference repetitions completed: 3 (== 3)
  PASS candidate repetitions completed: 3 (== 3)
  PASS repetitions failing minimum evidence: 0 (<= 0)
  FAIL repeatedly added behaviors: 1 (<= 0)
  PASS worst candidate block decisions: 0 (<= 0)   advisory: fresh scope
  PASS worst candidate critical risk observations: 0 (<= 0)   advisory: fresh scope
  PASS min candidate frequency: 3 (>= 3)
  PASS max lost behaviors: 0 (<= 0)
  FAIL max calls per run
      kb.localhost: 4 (<= 2)   fail
  PASS max llm calls per run: 1 (<= 1)
…
Gate: FAIL
exit 1
…
#### Tokens and cost

| | Reference | Candidate | Change |
|---|---|---|---|
| Input tokens | 3600 | 3600 | 0 |
| Output tokens | 900 | 900 | 0 |
| Unsplit tokens | 0 | 0 | 0 |
| Observations with usage | 3 | 3 | 0 |
| Observations without usage | 15 | 24 | 9 |

Cost in micro-`USD`, priced by `quickstart-illustrative-1` (digest `sha256:87c0fb70417997bd4745706ca9e40ffbe17c09a4ca9a51a44260605d8b32cac1`, source `illustrative prices for the QuickStart's stub model, not a provider's`):

| | Reference | Candidate | Change |
|---|---|---|---|
| Cost | 7200 | 7200 | 0 |
| Priced tokens | 4500 | 4500 | |
| Unpriced tokens | 0 | 0 | |
…
| calls per run | | | | **FAIL** | per target, below |
| calls per run · `kb.localhost` | 4 | at most | 2 | **FAIL** | |
| model calls per run | 1 | at most | 1 | pass | |
…
```

**How to read it.**
- **Frequency.** The frequency change Level 3 could not see now fails a named
  check: the worst candidate run made 4 calls to `kb.localhost`, over its limit
  of 2. The report's Behaviors and Targets sections show the same change as
  calls per run (`2–2` against `4–4`) and a `2.0×` call ratio, as in the
  [QuickStart's report](quickstart.md#7-read-the-report).
- **`min_candidate_frequency` and `max_lost_behaviors`** pass: every behavior
  in all three reference runs is in all three candidate runs, and none was
  lost.
- **`max_llm_calls_per_run` evaluated** because every candidate run had
  model-layer evidence, the GenAI `chat` span. Without it, the check is
  *deferred* and fails the verdict, naming the run that lacked it: an absence
  of model calls cannot be told from model calls that were invisible.
- **Fidelity.** Every row here is `semantic`: the GenAI convention named it. A
  behavior seen only as an HTTP request is `transport`, and a row whose
  observations disagree is `mixed`, at the lowest level any of them had. One
  transport observation makes the row transport.
- **Operational sections.** Latency buckets, span status, HTTP status classes
  and 429s per target and side. A side that never called a target reads *not
  available*, never zero.
- **Tokens** need `gen_ai.usage.*` (or OpenInference's `llm.token_count.*`)
  on the span. Here: 1200 and 300 per run.
- **Cost** exists only because the operator configured a pricing table, and it
  carries that table's version, digest and source. 3600 input tokens at
  1 000 000 micro-USD per million, plus 900 output tokens at 4 000 000, is
  7200 micro-USD a side. These prices are illustrative.
- **Suggestions** are rule-table outputs. The table has three rules:
  `compare.added_in_all_runs`, `compare.target_load_with_429` and
  `compare.lost_in_half`. A load increase with no 429 fires none of them, so the
  doubled `kb.localhost` load produced no suggestion: the evidence is in the
  table, and the rule did not apply.

**What not to conclude.**
- **Do not add a model's two target rows.** A real agent's model call is often
  one GenAI span (target: the provider) and one HTTP request beneath it
  (target: the host). They are two behaviors and count the same calls twice;
  `max_calls_per_run` needs a limit on each
  ([the measured report shows it](results/2026-10-12-model-change-report/README.md#what-the-report-shows)).
- **Latency is descriptive.** Through a model-driven agent the model dominates
  it, and nothing attributes a latency change to the change under test. Here the
  durations are the agent's fixed ones.
- **A cost figure is never a gate.** There is no cost check and no rate check.
- Operational evidence, fidelity and tokens arrive only through the Collector.
  A run ingested in-process through the SDK reads them as unavailable
  ([known limitations](ROADMAP.md#known-limitations-after-implementation-1)).
- A span carrying only the legacy `http.method`/`http.url` keys is not read as
  HTTP: it becomes `rpc/<METHOD>` with no target. Reading them is planned
  ([task 108](ROADMAP.md#v0130--proposed)).

**Reference.** [Platform CLI § Behavioral scenarios](platform-cli.md#behavioral-scenarios-trustvian-eval-run)
(the optional limits), [§ Fidelity on a comparison](platform-cli.md#fidelity-on-a-comparison),
[§ Latency, errors, tokens and cost per target](platform-cli.md#latency-errors-tokens-and-cost-per-target-eval-operational);
[Local development § pricing](local-development.md);
[the measured Model Change Report](results/2026-10-12-model-change-report/README.md),
two real models, ten runs a side.

**You can now answer:** how often each behavior happens per run on each side,
which target took more load, whether that breaks a limit I set, what latency,
status and tokens each side recorded, and what the tokens cost under a table I
chose.

## Level 5, advanced — Gate it in CI

**The question.** How do I put this gate on every pull request, and follow a
failure back to the spans behind it?

**The feature.** The `trustvian-run` and `trustvian-comment` GitHub Actions
(`v0.10.0`), posting the version 2 report since `v0.12.1`; `--reference`
reuse, `--suite`, environments, promotions and `trustvian evidence`
(`v0.10.0`); the WebUI's Evidence surface.

**Run it.** Follow the Level 2 finding to its evidence, reuse a recorded
reference, run a suite, and record a promotion decision:

```bash
trustvian evidence behaviors --reference-run l2-reference --candidate-run l2-candidate --check added_behaviors
trustvian evidence observations --reference-run l2-reference --candidate-run l2-candidate --behavior 7044b0468a915c5f
trustvian eval run --scenario scenario.yaml --reference last 2>/dev/null; echo "exit $?"
mkdir scenarios && git mv self.yaml limits.yaml scenarios/ && git commit -qm 'Move scenarios into a suite'
trustvian eval run --suite scenarios --scenario-timeout 2m 2>/dev/null; echo "exit $?"
trustvian env create --project-id trustvian-quickstart --ref staging --name Staging
trustvian env set --project-id trustvian-quickstart --ref local --revision 1 --rank 1
trustvian env set --project-id trustvian-quickstart --ref staging --revision 1 --rank 2
trustvian promotion create --id quickstart-promotion-1 --reference-run l2-reference --candidate-run l2-candidate --target-environment staging --max-added-behaviors 0 --max-block-decisions 0 --max-critical-risk-observations 0
```

**What you see.**

```text
STATUS  resolved
SIDE    candidate
RECORDED 1

BEHAVIORS (1)
  7044b0468a915c5f  added     tool · export_data → export.localhost

HISTORY complete (9 retained)
STATUS  resolved
SIDE    candidate
RECORDED 1
EXHAUSTIVE true

OBSERVATIONS (1)
  seq 7      2026-10-15T23:42:11.017003Z    observe_only low      tool · export_data → export.localhost

HISTORY complete (9 retained)
quickstart   runs 3   (execution scn-quickstart-20261015T234312-67788592)
Reference side reused from execution scn-quickstart-20261015T234310-2df6edc0 (last)
…
Gate: FAIL
exit 1
suite scenarios   2 scenarios

  FAIL    quickstart-limits                limits.yaml   execution scn-quickstart-limits-20261015T234318-28dbfbc5
  PASS    quickstart-self                  self.yaml   execution scn-quickstart-self-20261015T234321-874b4340

1 passed, 1 failed, 0 errors, 0 skipped   exit 1
exit 1
Environment staging
…
Environment local
Project: trustvian-quickstart
Name: local
Rank: 1
Status: active
Revision: 2
Environment staging
Project: trustvian-quickstart
Name: Staging
Rank: 2
Status: active
Revision: 2
Promotion quickstart-promotion-1
Project: trustvian-quickstart
Candidate: git:066d627
Reference run: l2-reference
Candidate run: l2-candidate
From: local rank 1 (revision 2)
To:   staging rank 2 (revision 2)
Outcome: rejected
Gate: FAIL
Decided at: 2026-10-16T02:43:34.192462+03:00
Trustvian recorded this decision. Nothing was deployed.
Gate checks:
  PASS Reference evidence: 6 (minimum 1)
  PASS Candidate evidence: 9 (minimum 1)
  FAIL Added behaviors: 1 (maximum 0)
  PASS Block decisions: 0 (maximum 0)
  PASS Critical risk observations: 0 (maximum 0)
  ---- Added behavior changes: not evaluated (no limit supplied)
```

**How to read it.**
- **Evidence.** The gate printed a number; `trustvian evidence` turns it into
  the behavior behind it and the retained observation that carried it.
  `RECORDED` is what the gate counted; the rows are what retained history still
  holds, and `HISTORY complete` says nothing is missing. In the WebUI, a
  Change Impact behavior row's `C1`…`C3` open the same observations in
  **Evidence**.
- **`--reference last`** reuses the reference side of the most recent completed
  execution of the same scenario name, project, agent and environment, and runs
  only the three candidate repetitions. This scenario's limits apply.
- **A suite** runs every scenario in the directory, each with its own limits
  and verdict, and exits with the most severe: here `1`, from
  `quickstart-limits`.
- **A promotion records a decision; it deploys nothing.** Its outcome is
  derived from the gate alone. A *rejected* promotion is a successful call and
  exits `0`. Promotion goes forward only: the environments need ranks first.
- **In CI**, copy
  [`examples/github-actions/behavioral-gate.yml`](../examples/github-actions/behavioral-gate.yml).
  Its `run` job runs your scenario with read-only permissions and its exit code
  is the check; its `comment` job runs no pull request code and posts the
  version 2 report, the same Markdown as `eval report --format md`. Pin both
  `uses:` lines to a reviewed commit, and **re-pin them to a release's commit**
  when you upgrade: the actions build the runtime their `runtime.env` names,
  and a pin older than `v0.12.1` posts the version 1 rendering
  ([ADR 0056 § Amendment](adr/0056-the-run-action-builds-a-pinned-source-commit.md#amendment-v0121-the-pin-keeps-up-with-releases)).

**What not to conclude.**
- A frequency limit whose evidence is absent is *deferred*, and a deferred
  check fails the verdict. It is not a pass.
- A FAIL from a model-driven agent at uncalibrated limits may be variance
  (Level 3).
- On a pull request from a fork, GitHub gives the comment job a read-only
  token: you get a warning and the job summary, not a comment.
- Two Overview panels still count in the browser over the page they read: the
  Runs panel's status breakdown and the Gate verdicts panel's outcome tally,
  each labelled so. Moving them to the server is planned
  ([task 110](ROADMAP.md#v0130--proposed)). Everything on Change Impact is the
  report's own figure.

**Reference.** [Running behavioral scenarios in GitHub Actions](ci-github-action.md);
[Platform CLI § `--reference`](platform-cli.md#reusing-a-recorded-reference---reference),
[§ `--suite`](platform-cli.md#a-suite-of-scenarios---suite),
[§ Following a finding to its evidence](platform-cli.md#following-a-finding-to-its-evidence),
[§ Recording a promotion](platform-cli.md#recording-a-promotion);
[Web interface § The Evidence surface](webui.md#the-evidence-surface).

**You can now answer:** which pull request changed my agent's behavior, by how
much, with which evidence, and which decision was recorded for promoting it.

## Level 6, operator — Run it for real

**The question.** How do I run the control plane for a team, keep its data and
upgrade it safely?

**The feature.** PostgreSQL as the control plane's store (`trustvian-local
--backend postgres`; the engine's PostgreSQL store since `v0.8.0`); backup,
restore and upgrade (`v0.9.0`); metrics, health and resource bounds; release
verification.

This level is reference material: it needs a PostgreSQL server and decisions
about your environment, so it has no QuickStart step.

**How to read it.**
- **The schema is forward-only.** An upgrade migrates the database forward,
  and a binary older than the schema refuses it. Back up before every upgrade;
  a backup is how you roll back across a schema step.
- **The connection string comes from the environment**, never a flag, because
  it normally carries a password (`TRUSTVIAN_PLATFORM_POSTGRES_DSN`).
- **Verify what you download**: checksums, signatures and provenance.

**What not to conclude.** `trustvian-local` binds loopback and has no
authentication yet; it is a local control plane, not a hosted service
([No authentication yet](platform-cli.md#no-authentication-yet)).

**Reference.** [Storage Guide](storage-guide.md), [Operations](operations.md)
(backup, restore, upgrade), [Observability](observability.md),
[Local development](local-development.md),
[Reference deployment](../deployments/docker-compose/README.md),
[Supply chain](supply-chain.md),
[Release Guide § Verifying a published release](release-guide.md#verifying-a-published-release).

**You can now answer:** where the control plane keeps its data, how to back it
up and upgrade it, and how to prove the binaries are the published ones.

## Level 7, expert — The engine

**The question.** What computes a decision, and how do I use it inside my own
service or pipeline?

**The feature.** The Go SDK (`v0.1.0`); policies from YAML (`v0.5.0`); alerts
(`v0.4.0`); sequence, Markov, n-gram and delegation signals (`v0.6.0`,
`v0.7.0`, opt-in); the OpenTelemetry Collector processor (`v0.2.0`).

This level is reference material: it needs Go, or your own Collector build.

**How to read it.**
- **The pipeline** is Event → Features → Fingerprint → Baseline → Anomaly →
  Trust → Policy → Decision. Each stage is a pure function except the baseline,
  and every decision carries its explanation.
- **Learning is gated.** Only actions that proceeded (`allow`, `observe_only`,
  `alert`) teach the baseline, so repeating a blocked action never normalizes
  it.
- **Some risk cannot be learned away**: a sensitive-target floor persists
  however familiar the behavior becomes.
- **Policy is data**, evaluated first-match-wins, and a misconfigured policy
  fails closed to `block`.

**What not to conclude.** The sequence signals ship off, and the Collector
processor's configuration has no `anomaly:` block, so they stay off on the
Collector path ([known gaps](ROADMAP.md#known-gaps-carried-to-v0130-not-patch-material)).

**Reference.** [SDK Guide](sdk-guide.md), [Policy Guide](policy-guide.md),
[Anomaly Configuration Guide](anomaly-config-guide.md),
[Sequence analysis](sequence-analysis.md), [OpenTelemetry](OPENTELEMETRY.md#what-is-not-read),
[the Collector processor](../processor/README.md),
[Architecture](ARCHITECTURE.md), [Security](SECURITY.md),
[Domain](DOMAIN.md).

**You can now answer:** how a single event becomes a decision, what Trustvian
learns and refuses to learn, and how to run that engine in your own code or
pipeline.

## Where each level runs

| Level | Runnable here | Needs |
|---|---|---|
| 1 See it | yes | the QuickStart |
| 2 Compare two runs | yes | the QuickStart |
| 3 Repeat and gate | yes | the QuickStart |
| 4 Load, latency, tokens and cost | yes, with an illustrative pricing table | the QuickStart; a real model for real latency and cost |
| 5 Gate it in CI | yes, except the GitHub Actions themselves | a GitHub repository for the Actions |
| 6 Run it for real | reference only | PostgreSQL |
| 7 The engine | reference only | Go, or a Collector build |
