# 0067 — Scenario provenance is recorded per execution side

**Status:** Proposed (task [086](../tasks/v0.12/086-scenario-and-input-versioning.md)).
Amends task 086's specification § 3, which put the model and the prompt
reference on the candidate. A maintainer accepts or revises this in review.

## Context

Task 086 records what each side of a comparison ran, so the comparison can say
whether both sides ran the same scenario, inputs, model and prompt. The
specification put `prompt_ref` on `CandidateMetadata` beside the existing
`Model`, on the reasoning that each side names a candidate.

On `main` that placement cannot record the change it exists for:

- Candidates are immutable and create-only. There is no update route.
- `trustvian dev`, which every scenario repetition runs through, creates its
  candidate idempotently and without metadata. Its default identifier is the
  git revision.

A developer who changes `OLLAMA_MODEL` and runs the scenario again does so
under the same candidate, created earlier, with no model recorded. Recording
the model there would need either mutable candidates or a new candidate per
model, decided by the runner. Each is a larger change than this task, and the
second would put an evaluation decision in the runner.

## Decision

1. **Provenance is recorded on the scenario execution, per side.** Each side
   holds a scenario digest, an input digest (or "no inputs declared"), a
   declared model and a declared prompt reference (name and digest). Every
   field is optional: absent means not recorded.
2. **The CLI computes and declares; the control plane validates and stores.**
   The CLI reads the files, so it computes both digests. It resolves each
   side's declarations from the scenario file (`model`, `prompt_ref`) or from
   a named variable of the side's environment (`model_env`, `prompt_ref_env`).
   The control plane checks formats only. It cannot recompute a digest, and
   says so.
3. **A reused reference side carries the referenced execution's provenance,
   copied when the execution begins.** Those are the runs it reuses, and a copy
   keeps naming the execution that ran them through any chain of
   `--reference` reuse. A reference-side declaration on such an execution is
   refused.
4. **A comparison of arbitrary runs looks each run up** through the executions
   that recorded it. A side states provenance only when all its runs agree.
5. **Nothing is added to `CandidateMetadata`.** Its existing `Model` is left
   as it is.

## Alternatives considered

- **`PromptRef` on the candidate, as specified.** It cannot record a per-run
  change while candidates are immutable and `dev` writes no metadata.
- **A candidate per model, created by the runner.** The runner would then
  decide what counts as a different candidate, which is an evaluation
  question the control plane owns.
- **Provenance on the execution, not per side.** A reused reference side ran
  under another execution's scenario file and model, which is exactly the
  difference sameness exists to show.

## Consequences

- `platform_scenario_executions` gains ten nullable columns at schema 12,
  five per side. Executions from before the step read not recorded.
- A run compared outside an execution has provenance only through the
  execution that recorded it. A run no execution recorded reads
  not_recorded.
- `CandidateMetadata.Model` and the per-side model are two places a model can
  appear. They answer different questions: what the candidate was registered
  as, and what a side declared it ran with. Only the second is compared.
