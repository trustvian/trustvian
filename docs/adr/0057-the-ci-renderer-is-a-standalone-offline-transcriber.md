# 0057 — The CI renderer is a standalone, offline transcriber of the run artifact

**Status:** Accepted

## Context

[Task 079](../tasks/v1.0/079-ci-integration-github-action.md) puts the
behavioral gate's result on the pull request. Its first slice
([ADR 0056](0056-the-run-action-builds-a-pinned-source-commit.md)) ships the
run side: a job that runs the pull request's code and preserves
`trustvian eval run --json`'s result document as an artifact, next to
`trustvian-run.json`, the run's metadata. The comment itself has to come from a
**different** job, one that holds a write-scoped token and therefore must never
run pull request code.

Between the two sits a renderer: something that turns the artifact into the
Markdown a comment or a job summary carries. It is the one component that will
run inside the write-enabled job, and it reads a file produced by a job that
executed untrusted code. So it is a trust boundary as much as a formatter, and
it had to be settled before anything posts:

1. Where it lives, and what it may depend on.
2. What it trusts in the artifact, and how strictly it reads it.
3. What it may compute.
4. What it renders when it cannot render a verdict.
5. How untrusted strings stay inert.

## Decision

### 1. A standard-library-only command, `cmd/trustvian-ci-render`

The renderer is a `main` package in the root module, separate from
`cmd/trustvian`, and it imports **only the Go standard library** — asserted by
a test that parses its imports and runs `go list -deps`. Its sources import no
`os/exec`, `net`, `plugin`, `reflect` or `unsafe`, also asserted.

- **Small and separately buildable.** The write-enabled job will build exactly
  this package from a reviewed, commit-pinned source. A standard-library-only
  package is the smallest thing that can be built and audited there.
- **Not part of the CLI.** Task 079 forbids GitHub-specific behavior in
  `trustvian`. A `--github` mode, or a Markdown renderer for comments, inside
  the CLI would put one CI provider into the product.
- **Not under `internal/`.** `internal/` is the engine's hexagonal core. This
  is an adapter, one layer further out than the CLI.

It is offline and needs no credential: it reads a directory, writes Markdown
to stdout, and exits. Posting, marker ownership and comment updates are the
next slice's.

### 2. The artifact is untrusted; the caller's context is not

What identifies the run comes from the **caller**: the head commit from the
event, the repository, the run id and attempt, and the run job's `exit-code`
output. The artifact must agree with every one of them.

The artifact is read defensively:

- **Only two fixed names.** `trustvian-run.json` and `result.json` are opened,
  each only if it is a regular file — a symbolic link is a path the artifact
  chose — with the file re-checked after opening. `result.file` is compared
  with `result.json`, never used as a path.
- **Bounded reads.** 64 KiB for the metadata, and 32 MiB + 64 KiB for the
  result — exactly what the run action preserves.
- **Digest and size checked.** The SHA-256 and byte count in
  `trustvian-run.json` must match the `result.json` that was read. A match
  proves the two files agree, **not that either is authentic**: a workload
  that can write one can write both. The independently supplied context is
  what ties the artifact to this run.
- **Strict JSON.** A small tree parser over `encoding/json`'s tokenizer
  refuses invalid UTF-8, duplicate keys at any depth (unknown fields
  included), trailing data and excessive nesting. Fields are then checked one
  by one against that tree, so an absent field, a `null`, a `0` and a `false`
  stay distinct.
- **Closed vocabularies are closed.** Verdicts, classifications, check names,
  rules, advisories, member outcomes, skip reasons, reference modes,
  metadata modes and statuses must be defined values. The six checks must be
  exactly the six, in their stable order. Counts and limits must be canonical
  decimal strings. `version` must be `"1"` in each document.
- **Shapes must be coherent.** Examples:
  - a PASS needs exit `0` and a FAIL exit `1`;
  - a single scenario with a document cannot have exit `2` or `3`;
  - a suite's `exit_code` must equal the CLI's;
  - a suite member's fields must belong to its outcome;
  - an embedded result must be that member's own scenario, execution and
    verdict.

**Unknown fields are tolerated and never rendered.** `docs/compatibility.md`
lets both `trustvian-run.json` and the result document gain fields within
version `"1"`, and requires consumers to tolerate them. Refusing them would
turn every additive producer change into a broken comment. Instead, the
renderer reads only the fields it lists, validates each whatever else is
present, and prints nothing it did not validate. Duplicate keys are still
refused inside unknown fields: an unknown field may be ignored, but a document
that says two things is not one document.

### 3. It transcribes; it computes nothing

Every count, limit, threshold, classification, check outcome, verdict and
suite summary is printed as stored.

- **No decimal-string count or limit becomes a number.** Presence counts,
  check actuals and bounds, and the five gate limits are the decimal text the
  control plane wrote. They are matched against a canonical-decimal pattern
  and printed as that same text.
- **JSON integers are range-checked, not computed with.** `runs`, exit codes,
  the suite summary and `scenario_count` are JSON numbers. Each is read through
  `json.Number`, checked to be a canonical integer within its range, and
  re-printed in canonical form.
- A test scans every source file but `main.go` for `strconv` and other
  text-to-number conversions. `main.go` parses only the caller's
  `--exit-code`.

The only comparisons are **equality checks between two stored copies of one
fact**: `scenario.runs` against `comparison.runs`, the two producer versions,
a member's scenario against its embedded result, a verdict against the exit
code that encodes it. None of them derives a value. A document whose stored
classification contradicts its own counts is rendered as stored, and a test
proves it: the control plane owns every one of those decisions, and a renderer
that "corrected" one would be the second implementation task 078 exists to
prevent.

**A suite gets no verdict of its own.** It is rendered as its recorded summary,
its members' recorded outcomes, and each eligible member's own result. A
complete suite is a report whatever its exit code. A member that errored or was
skipped is shown as such, and its error code and message are inert text.

### 4. Three states, and "no verdict" carries nothing

A rendering is a **verdict** (one scenario), a **report** (a suite), or
**no verdict**.

No verdict is rendered when:

- the run job reported no exit code;
- the CLI exited `2`, `3`, or outside `0`–`3` with no document;
- the artifact is missing;
- the result was not preserved (`oversized`, `invalid`);
- a suite document is the CLI's incomplete overflow form;
- validation failed;
- the evidence cannot fit the size limit.

The no-verdict state is built from the caller's context alone: the head
commit, a fixed reason and the run link. It carries no count, no check, no
zero placeholder and nothing parsed from a document that was not accepted
whole, and the renderer keeps no state between renderings.

Validation failures stay observable. The command exits:

- `3` when the artifact was rejected;
- `1` for a run that produced no verdict;
- `0` for evidence;
- `2` for a malformed context, in which case nothing is rendered.

### 5. Every artifact string is one inert code span

Every artifact-supplied string is rendered as one code span on one line, and
only through one function:

- Control, format and line-separator characters become U+FFFD, so no newline
  and no bidirectional override survives.
- The fence is longer than any backtick run in the value.
- `|` becomes `｜` (U+FF5C). GitHub splits a table row on a pipe even inside a
  code span, and an escaped `\|` can interact with a preceding backslash in
  the value, so no ASCII pipe ever comes from the artifact.
- Each value is capped at 256 bytes, cut on a UTF-8 boundary, and marked
  *(truncated)* outside the span. The cap applies **before** the pipe
  substitution, and `｜` is three bytes, so a value can render larger than
  256 bytes, as can its fence and padding. The body cap below is what bounds
  the output.

The whole body is capped at 60,000 bytes, below GitHub's 65,536-character
comment limit, and the same bound serves a job summary:

1. **Full.** Everything is rendered.
2. **Shortened.** Unclassified behaviors are dropped, with a visible note, but
   every added and removed behavior, every check, the thresholds, the
   identity and the producers are kept.
3. **No verdict.** If even that does not fit, nothing that looks complete is
   rendered.

Labels for closed vocabularies come from the renderer's own tables, never from
the document.

## Alternatives considered

- **A `--markdown` mode in `trustvian`.** Rejected. It would put a CI
  provider's format into the CLI, and the write-enabled job would have to
  build the whole CLI and its dependencies.
- **Decoding straight into Go structs.** Rejected. `encoding/json` keeps the
  last of two duplicate keys, and cannot tell absent from zero or false
  without a pointer on every field. Either lets a document mean two things.
- **Refusing unknown fields.** Rejected. It contradicts the compatibility
  contract both documents are published under. A producer adding a field would
  break every comment.
- **Escaping Markdown character by character instead of code spans.**
  Rejected. It is safe only if every extension GitHub layers on Markdown —
  mentions, issue references, autolinks — honors every escape in every
  position, and one missed character becomes active Markdown. A code span
  turns all of them off at once. GitHub's own renderer, given the hostile
  fixture, produced no link, image, mention, issue reference or extra row.
- **Rendering a partial result when the size limit is hit.** Rejected. A
  comment that silently omits a failing check reads as complete; task 079
  names that the worst failure mode.

## Consequences

- The future comment job builds `cmd/trustvian-ci-render` from a reviewed,
  commit-pinned source **outside the pull request's checkout**, runs it on the
  downloaded artifact, and posts its stdout. It never runs a local action, a
  script or a program from the pull request. That job, its permission, marker
  ownership, stale-comment replacement and fork degradation remain task 079's
  next slice.
- The Markdown is OBSERVATIONAL: no wording, layout or ordering is promised.
  The flags and exit codes are OPERATIONALLY STABLE.
- **Two things guard against drift between the producers and the renderer.**
  - **Checked-in fixtures pin the output.** The renderer's tests run on
    artifacts the real producers wrote: `testdata/generate.sh` drives the
    action's `run.sh` against the real CLI, control plane, Collector and the
    model-free `agent-producer`. Golden renderings pin the result.
  - **The end-to-end workflow renders live output.**
    `.github/workflows/trustvian-run-action.yml` builds the renderer from the
    same commit and renders every artifact the run job produced through the
    pinned runtime. It passes the run job's asserted exit codes as the context
    and requires no rejection. That workflow also runs when
    `cmd/trustvian-ci-render` changes.
  - A producer change the renderer does not follow therefore fails a real end
    to end, not only a fixture that might be stale.
- **What it cannot prove.** The artifact is as authentic as the job that wrote
  it, and that job ran the pull request's code. A workload that rewrites
  `result.json` and the digest beside it can present any member outcomes,
  summary or check rows it likes. The renderer refuses what is incoherent
  without computing:
  - shapes;
  - closed values;
  - stored copies of one fact;
  - a single scenario's verdict against the caller's exit code.

  It does **not** recount a suite's members against its summary or its exit
  code, or re-derive a verdict from check rows: those are computations task
  079 forbids, and the second implementation of the gate it exists to
  prevent. The authoritative signal is the run job's exit code — the check
  itself, supplied by the caller and printed first. A suite rendering is the
  CLI's report as preserved, not a verification of it.
- **Bounded parsing.** A JSON document may hold at most two million values,
  because 32 MiB of tiny values would otherwise cost gigabytes as a tree. The
  renderer opens a file it has examined with `Lstat`, then confirms it is the
  same regular file. A file swapped for a FIFO in between could still block
  the open, which needs a writer racing the renderer on a downloaded, static
  artifact.
- A new check, classification or verdict value within version `"1"` makes the
  renderer report no verdict until it is taught the value. That is the
  intended failure direction: a reviewer sees "no verdict", never a rendering
  that silently dropped what it did not recognize.
