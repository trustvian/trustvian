# 086 — Scenario and Input Versioning

Status: Specified; not implemented
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
