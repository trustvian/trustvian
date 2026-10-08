# 086 — Scenario and Input Versioning

Status: Implemented — see [What shipped](#what-shipped)
Milestone: [`v0.12.0`](../../ROADMAP.md#v0120--change-impact)
Depends on: [078](../v1.0/078-behavioral-scenario-suites.md) (implemented),
[ADR 0054](../../adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)
Blocks: [107](107-change-impact-view-and-report.md)'s sameness row
Planned in: [082 § 086](../v1.0/082-agent-inspection-and-evaluation-depth.md#086--scenario-and-input-versioning).
That section is the baseline. It is copied here and extended, and each change
is marked **Amends 082**.

## Developer problem

*From 082, unchanged:* A comparison says the candidate added a behavior. It does
not say whether both sides were given the same input. A developer who edited a
fixture between the reference run and the candidate run gets a behavioral
difference caused by their own test data. They read it as a regression and
spend an afternoon on it.

*Extended:* The same applies to the model and the prompt. "Did I change the
model, the prompt, the scenario, or the inputs?" should be answered on the
comparison itself, not reconstructed from git history.

## Current state, verified against `main` (`d1ad5f6`)

| Capability | State | Evidence |
|---|---|---|
| `CandidateMetadata` | `Label, SourceRef, ArtifactDigest, Model, ToolsetDigest, ConfigDigest`, all optional strings | `platform/domain.go:205-222` |
| Prompt reference | **Absent** | — |
| Scenario file | `version, name, runs, project, agent, environment, instrumentation, reference, candidate, gate`. Each side is `{command, env, candidate}` | `config/scenario.go:64-99` |
| `inputs:` in the scenario file | **Absent.** 078's illustrative `inputs:` was not implemented | `config/scenario.go:64-81` |
| Scenario or input digest | **Absent** | no digest on `ScenarioExecution` (`platform/scenario_execution.go:106-131`) |
| What an execution persists | identity, scope, N, reference execution, lifecycle, verdict, repetitions. **Not** the command, its environment or the gate limits | ADR 0054 § 1 |

## Scope

*From 082:* a scenario-definition digest and an input digest recorded on the
evidence; a prompt reference beside `Model`; a comparison that **states a
mismatch** rather than silently comparing across it.

### 1. `inputs:` in the scenario file

**Amends 082 (which assumed `inputs:` existed).** The scenario file gains an
optional `inputs:` list of repository-relative file paths. It is optional
because a scenario with no declared inputs is valid (082 criterion 6). Paths are
resolved relative to the scenario file, must exist, and must stay inside the
repository. At most 64 entries, each at most 64 MiB. The runner does not pass
inputs to the workload: `command` still decides what the workload reads.
`inputs:` declares which files make up the scenario's input set, so that they
can be digested.

### 2. Digests, computed once and named for what they cover

Both are SHA-256, lowercase hex, prefixed `sha256:`.

**`scenario_digest`** covers the scenario *definition*, canonicalized:

- the parsed `ScenarioConfig`, re-encoded as canonical JSON (sorted keys, no
  insignificant whitespace), so that a comment or a reformat changes nothing;
- **excluding `env` values.** Environment values commonly hold secrets, and a
  digest of a short secret can be brute-forced. Only the env *keys* are
  included. **Amends 082**, which did not discuss this;
- excluding `inputs:` file contents, which the input digest covers.

**`inputs_digest`** is SHA-256 over the sorted list of `(path, sha256(file
bytes))` pairs. Paths are normalized to forward slashes. Byte-exact: a changed
line ending is a changed input. An empty `inputs:` list, or no `inputs:` at all,
records `inputs_digest: null` with `inputs_declared: false`. That is not the
digest of an empty set (082 criterion 6: absence recorded explicitly).

This answers 082's open question 4 ("the file alone, or the file plus the inputs
it names"): **two digests, separately**, as 082 assumed.

**Who computes them.** The CLI reads the files, so the CLI computes both
digests and sends them when it creates the execution. This is provenance
reporting, like the CLI version the result document already carries. It is not
evaluation, so 078's "the runner computes nothing" holds: no count, diff,
verdict or classification moves to the runner. The control plane validates the
format and stores the values. It cannot recompute them, and the documentation
says so.

### 3. `prompt_ref` and `model` on the candidate

`CandidateMetadata` gains one field, `PromptRef`, an optional string of at most
255 bytes. Its doc comment states the refusal where the field is defined, as
082's risk section requires:

```go
// PromptRef identifies the prompt this candidate used: a digest
// ("sha256:…") or an identifier from the producer's own prompt store
// ("support-agent/system@v14"). It is never prompt text. Trustvian does not
// store, fetch or render prompt content, and no field named for prompt text
// will be added beside this one.
PromptRef string
```

A value longer than 255 bytes is refused, so the field cannot hold a prompt.
Refusing is not proof that the value is not text, and the documentation says so.

`Model` already exists, and this task reuses it. **Amends the brief:** "model
and prompt_ref on each execution" becomes "on each side's *candidate*". An
execution has two sides and each names a candidate, so the candidate is where a
model belongs. Copying it onto the execution would create a second place for it
to disagree.

### 4. Sameness on the comparison

The repeated comparison, and the single-run comparison where both runs belong to
scenario executions, gain a fixed-shape `sameness` section that the control
plane computes:

```json
"sameness": {
  "same_scenario": true,
  "same_inputs":   false,
  "same_model":    "not_stated",
  "same_prompt":   true,
  "reference": {"scenario_digest": "sha256:…", "inputs_digest": "sha256:…", "model": "llama3.2", "prompt_ref": "sys@v14"},
  "candidate": {"scenario_digest": "sha256:…", "inputs_digest": "sha256:…", "model": "",         "prompt_ref": "sys@v14"}
}
```

- Each `same_*` is `true`, `false` or `"not_stated"`. It is `not_stated` when
  either side lacks the value, and never `false` by default, because a missing
  value is not a different one (082 criterion 3).
- **Amends the brief:** `same_prompt` is added. The brief listed three booleans,
  and `prompt_ref` is otherwise recorded with nothing comparing it.
- The digests are always shown, so a reader can check the result.

**A mismatch produces a warning outside the gate.** The comparison gains a
`warnings[]` entry such as `{"code": "inputs_differ", …}`. It never refuses the
comparison and never changes the verdict (082 criterion 2). Expected
differences, such as a changed model in a model-change comparison, are
reported the same way. The warning states a fact, and the developer decides
whether it was intended. These warnings are evidence. They are not ADR 0064
suggestions and carry no advice.

### 5. Persistence

One forward-only schema step on both backends:
`platform_scenario_executions` gains `scenario_digest`, `inputs_digest` and
`inputs_declared`, and the candidate table gains `prompt_ref`. Executions
recorded before the step migrate to *not recorded*, which is distinct from *no
inputs declared*. ADR 0054's rule holds: the step invents nothing about
executions it did not see. The step number is assigned in landing order.

## Non-goals

*From 082, unchanged:* **no dataset platform**: no dataset entity, no dataset
API, no splits, no labelling, no curation, no golden outputs, no
expected-answer semantics. No prompt registry and no prompt storage. Versioning
belongs to git, not to Trustvian. 078's non-goals are unchanged.

Also out of scope: no fetching of a prompt by its reference, no validation that
a `prompt_ref` resolves anywhere, and no digest of the *command's* binary or the
agent source (`ArtifactDigest` and `SourceRef` already exist for that).

## Acceptance criteria

1. *(082 #1)* A scenario execution records the scenario digest and the inputs
   digest.
2. *(082 #2)* A comparison across executions whose digests differ reports it
   prominently, neither refusing nor hiding it.
3. *(082 #3)* A candidate may carry a prompt reference. One that does not is
   rendered *not stated*.
4. *(082 #4)* No field added here reaches `StableFeatures`, a fingerprint or a
   baseline key, proven by test.
5. *(082 #5)* No prompt, input or output text is stored or rendered.
6. *(082 #6)* A scenario with no declared inputs is valid and records the
   absence explicitly.
7. *(new)* Changing an `env` value does not change `scenario_digest`. Changing an
   `env` key does.
8. *(new)* Reformatting the scenario file or editing a comment does not change
   `scenario_digest`.

## Tests

- *(082)* Changing one byte of a fixture produces `same_inputs: false` and an
  `inputs_differ` warning.
- *(082)* A digest is stable across runs, operating systems (path separators)
  and backends.
- *(082)* The privacy tripwire is extended to every new field.
- Canonicalization: reordered keys, comments and whitespace produce the same
  digest. A changed limit produces a different one.
- Executions migrated from before the step read *not recorded*.

## Risks

*From 082:* a field named `PromptRef` invites a later `PromptText`. The refusal
is in the field's own doc comment.

*New:* a digest the control plane cannot recompute is only as trustworthy as
the client that sent it. That is the same trust level as every
`CandidateMetadata` field today, and it is stated in the documentation rather
than implied away.

## What shipped

All five layers, on branch `feat/086-scenario-input-versioning`. Schema step
**v12**. The engine, `DecisionRecord`, `StableFeatures`, fingerprints and
baseline keys are unchanged. The differences from the specification above are
each a decision taken while building, listed first.

| Specified | Shipped | Why |
|---|---|---|
| `PromptRef` on `CandidateMetadata`, `Model` reused from the candidate (§ 3) | A declared model and prompt reference **per execution side**, beside the digests; `CandidateMetadata` is unchanged ([ADR 0067](../../adr/0067-scenario-provenance-is-recorded-per-execution-side.md), proposed) | Candidates are create-only, and `trustvian dev` creates its candidate without metadata, keyed by the git revision. A changed `OLLAMA_MODEL` reruns under the same candidate, so a candidate field could never record it |
| `prompt_ref`: one string, at most 255 bytes | `prompt_ref` is `{name, digest}`. The name is 1 to 128 of `[A-Za-z0-9._:/@+-]`, starting alphanumeric, and the digest is `sha256:` and 64 lowercase hex. The model has the name's format | The brief: "name and digest only". With no whitespace allowed, a sentence of prompt text is refused outright, not just bounded. As the specification says, a refusal is still not proof that the value is not text |
| The model and prompt come from the candidate | Stated in the scenario file (`model`, `prompt_ref`), or named as a variable of the side's environment (`model_env`, `prompt_ref_env`, the latter holding `<name>@sha256:<hex>`), never both. Absent when neither is given | The brief: "supplied by the scenario file or an environment variable". `model_env: OLLAMA_MODEL` reads the same variable the demo agent reads its model from, after the side's `env` is applied |
| `inputs_digest`, `same_prompt`, `"not_stated"`, `true`/`false` as JSON booleans | `input_digest`, `same_prompt_ref`, `"not_recorded"`, and every answer a string: `"true"`, `"false"`, `"not_recorded"` | The brief's names. One type per field: a consumer never has to branch on whether a value is a boolean or a string |
| `inputs_declared` on the stored and returned execution | Requests send `inputs_declared`. Responses say `inputs`: `declared`, `not_declared` or `not_recorded` | Three states, not two: "no inputs declared" and "nothing recorded" are different facts, and a boolean cannot carry the third |
| `scenario_digest` excludes env values and input contents | It also excludes `model`, `model_env`, `prompt_ref` and `prompt_ref_env` | They are compared on their own. Changing only the model must read `same_scenario: true`, `same_model: false` (the brief's test) |
| Columns `scenario_digest`, `inputs_digest`, `inputs_declared`, and `prompt_ref` on the candidate table | Ten nullable TEXT columns on `platform_scenario_executions`, five per side: `{side}_scenario_digest`, `_input_digest`, `_model`, `_prompt_ref_name`, `_prompt_ref_digest`. No backfill | NULL is "not recorded", which is all that is known about a v11 execution. "Inputs declared" is a non-NULL input digest beside a scenario digest. A stored empty string is refused as corrupt, never read as a digest |
| — | A reused reference side **carries the referenced execution's reference-side provenance, copied** when the execution begins. A reference-side declaration with `--reference` is a `400` | Those are the runs it reuses. A copy keeps naming the execution that ran them through any chain of reuse |
| Sameness on the repeated comparison and on the single-run comparison | On every repeated comparison: an execution's completion, and `POST /v1/evaluations/compare-repeated`. **Not** on `POST /v1/evaluations/compare` | The brief scopes it to compare-repeated. On the plain route each run is looked up through the executions that recorded it, and a side states a value only when all its runs agree. A single-run comparison would read it the same way, and is left for 107 to decide |
| `warnings[]` entries such as `{"code": "inputs_differ", …}` | `{code, text}` with four codes, in fixed order: `scenario_differs`, `inputs_differ`, `model_differs`, `prompt_ref_differs`. Each text is a fixed sentence stating the fact | No advice, as § 4 says. Codes are what a consumer branches on |
| — | `trustvian eval compare-repeated`: any runs, body unchanged in both modes, exit on the verdict | The brief: the command did not exist, and the sameness block must be reachable from the CLI exactly as returned |
| "Paths … must stay inside the repository" | The repository is the nearest directory at or above the scenario file holding `.git`, or the scenario's own directory when there is none. Checked after resolving symbolic links | A boundary the CLI can find without running git |

### How each digest is computed

- **`scenario_digest`** — `"sha256:"` + hex SHA-256 of the validated
  `ScenarioConfig` encoded as compact JSON, in this fixed field order:
  - `version`, `name`, `runs`, `project`, `agent`, `environment`,
    `instrumentation`;
  - `inputs`: clean paths, sorted;
  - `reference` and `candidate`, each `{command, env_keys, candidate}`.
    `command` keeps its order, because it is an argument vector, and
    `env_keys` are sorted;
  - `gate`: the five required limits; `min_candidate_frequency` and
    `max_lost_behaviors`, each `null` when omitted; and `max_calls_per_run`,
    sorted by target, `null` when omitted.

  It excludes environment values, input contents, `model`, `model_env`,
  `prompt_ref` and `prompt_ref_env`.
  `TestScenarioDigestMatchesTheDocumentedEncoding` rebuilds it by hand.
- **`input_digest`** — `"sha256:"` + hex SHA-256 of the compact JSON list
  `[{"path": …, "sha256": …}, …]`, sorted by path.
  - Each path is `/`-separated, `path.Clean`ed and relative to the scenario
    file. Backslashes, absolute paths and duplicates are refused.
  - Each `sha256` is the hex digest of the file's exact bytes.
  - Each input must be a regular file of at most 64 MiB inside the repository,
    and a scenario may list at most 64.
  - With no inputs, nothing is declared and there is no digest.

  `TestInputDigestMatchesTheDocumentedEncoding` rebuilds it by hand.

The CLI computes both. The control plane checks their format only. It cannot
recompute them, and `docs/platform-cli.md` says so.

### Measured with a real model

The demo agent (`trustvian-python-agent-demo`, `harness/run_agent.py`, 3
tickets, `opentelemetry-instrument`), on Ollama, through `bin/trustvian` and
`bin/trustvian-local` built from this branch.

- **Scenario:** `runs: 2`, the same command on both sides, and `model_env:
  OLLAMA_MODEL` on both sides. No inputs: the demo's tickets are in
  `agent/main.py`, and it reads no input file.
- **First run:** `OLLAMA_MODEL=llama3.2`, self-contained.
- **Second run:** the **unchanged scenario file**, with `OLLAMA_MODEL=gemma3:4b`
  and `--reference last`.

Every value below is quoted from the `--json` result documents.

**Execution `scn-provenance-086-measurement-20261008T181540-963d2466`**
(unchanged, PASS, exit 0):

```json
"sameness": {"same_scenario": "true", "same_inputs": "true", "same_model": "true",
  "same_prompt_ref": "not_recorded",
  "reference": {"scenario_digest": "sha256:f9a230d6268437334fd36667dee1b55224a9497472ca393f057361321fad0e8d",
                "inputs": "not_declared", "model": "llama3.2"},
  "candidate": {"scenario_digest": "sha256:f9a230d6268437334fd36667dee1b55224a9497472ca393f057361321fad0e8d",
                "inputs": "not_declared", "model": "llama3.2"}},
"warnings": []
```

**Execution `scn-provenance-086-measurement-20261008T181814-936f1445`**
(`OLLAMA_MODEL` changed, reference reused from the first, FAIL, exit 1):

```json
"sameness": {"same_scenario": "true", "same_inputs": "true", "same_model": "false",
  "same_prompt_ref": "not_recorded",
  "reference": {"scenario_digest": "sha256:f9a230d6268437334fd36667dee1b55224a9497472ca393f057361321fad0e8d",
                "inputs": "not_declared", "model": "llama3.2"},
  "candidate": {"scenario_digest": "sha256:f9a230d6268437334fd36667dee1b55224a9497472ca393f057361321fad0e8d",
                "inputs": "not_declared", "model": "gemma3:4b"}},
"warnings": [{"code": "model_differs", "text": "The reference and candidate sides declared different models."}]
```

- **The FAIL is the gate's, not sameness's.** Under `gemma3:4b` both
  candidate runs added `tool/account_history` and `http/GET →
  history.localhost` (2/2 against 0/2), and dropped `tool/billing_lookup` and
  `http/GET → billing.localhost` (0/2 against 2/2).
- **The same answer from the new command.** `trustvian eval compare-repeated`
  over the two executions' candidate runs, given in reversed order on the
  reference side, returned the same `sameness` and `warnings` and FAIL (exit 1).
  There each side's values came from looking up the executions that recorded
  its runs.
- **Wall time:** 2 min 22 s for the first run (4 repetitions) and 2 min 27 s
  for the second (2 repetitions).
- **The added lookup** on compare-repeated measured about 0.75 ms and 2,254
  allocations at the 64-repetitions-a-side bound
  ([PERFORMANCE.md](../../PERFORMANCE.md)). Nothing else changed on any path.

### Proven by test

- `scenario_digest` is stable under reordered keys, flow versus block style,
  comments, quoting, map and list order, and `./` on a path. It changes on
  every one of 19 definition values. It does not change on an env value or a
  provenance declaration. Both digests are rebuilt by hand from their
  documented encoding.
- `input_digest` is stable under reordering and respelling, and changes on
  one byte, a line ending included. No inputs is recorded as absence.
  - Refused: a missing file, a directory, a path outside the repository
    (including through a symbolic link) and a file over 64 MiB.
  - Accepted: a file of exactly 64 MiB.
- Prompt text is refused as a model or prompt name at every layer: the
  scenario loader, the CLI's environment read, the control plane's validation,
  the HTTP request, and restore.
- `same_*` is `not_recorded`, never `false`, for an execution with nothing
  recorded, and for a migrated v11 execution, which reads nothing recorded.
- Two executions differing only in model report `same_model: "false"` and
  `same_scenario: "true"`: through the service, over HTTP on completion, and
  on compare-repeated in either run order. A side mixing runs recorded
  differently reads `not_recorded`.
- Sameness changes nothing else. Identical evidence under the same and a
  different model has equal gates, behaviors and suggestions.
- The compare-repeated body for unrecorded runs is byte-identical to `main` at
  `a1ee3fd` once `sameness` and `warnings` are removed.
- The 079 renderer renders the fail, pass and suite documents carrying recorded
  provenance, sameness and warnings byte for byte as before.
- The migration round-trips on both backends: v11 data survives, executions
  read not recorded, and new columns are written and survive a restart. The
  fresh and migrated tables have the same columns. Restore refuses six damaged
  shapes as corrupt. `RunProvenance` conforms on SQLite, file and memory, and
  PostgreSQL.
- `StableFeatures` and `baseline.Key` keep exactly their fields. The same
  workload under different provenance has the same fingerprints, behaviors and
  learning scopes.

### Current state rows found stale on `main` (`a1ee3fd`)

- `config/scenario.go:64-99` is now `ScenarioConfig` 65-82, `ScenarioSide`
  85-91 and `ScenarioGate` 95-114 (106 added the optional limits).
- "`inputs:` absent, `config/scenario.go:64-81`": still absent, at 65-82.
- `ScenarioExecution`, `platform/scenario_execution.go:106-131`: 106-130.
- `CandidateMetadata`, `platform/domain.go:205-222`: holds.
