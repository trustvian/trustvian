# Changelog

All notable changes to Trustvian are documented in this file. Prior to
`v0.1`, the project was pre-release iteration on a single evolving
`develop` branch with no tagged snapshots — this file starts at the
point where a tag first exists for something external users can
actually depend on.

## Unreleased

### Added

- **The behavioral gate comment, end to end:
  `.github/actions/trustvian-comment`** (task 079, now implemented;
  [ADR 0058](docs/adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md),
  [guide](docs/ci-github-action.md)).

  A composite action for the second job of the behavioral gate workflow. It
  downloads the run job's artifact, renders it with `trustvian-ci-render`,
  writes the rendering to the job summary, and posts it with
  `trustvian-ci-comment` as the pull request's one gate comment, edited in
  place.

  - **Built from pinned source only.** Both commands are built from the commit
    `runtime.env` pins, with the run action's digest-checked toolchain,
    isolated fetch and `vcs.revision` check, now shared through its `lib.sh`.
    Nothing is read from the job's workspace, and a source scan asserts it.
  - **Inputs:** `exit-code` (required; empty means the CLI did not run),
    `artifact-name`, `marker-id` and `github-token`. **Outputs:**
    `renderer-exit`, `posted` and `outcome`.
  - **Every rendering is posted.** Evidence, no verdict and a rejected artifact
    all replace the previous comment. A missing artifact is the renderer's
    "artifact is missing" no verdict, not a failed step. Only the renderer's
    usage exit fails the job without posting.
  - **The token is in one step's environment.** Only the post step has
    `GITHUB_TOKEN`, and the poster is the only process that reads it. Only the
    job's own `GITHUB_TOKEN` is supported: comments are found by
    `github-actions[bot]` authorship.
  - **Forks degrade loudly and successfully.** A refused write is a warning
    and a job-summary note, and the check stays the run job's.
  - **The example is now the two-job workflow**,
    `examples/github-actions/behavioral-gate.yml` (renamed from
    `behavioral-gate-run.yml`):
    - the run job holds `contents: read`;
    - the comment job holds `pull-requests: write` alone, checks nothing out
      and runs under `!cancelled()`;
    - concurrency is per pull request, with cancel-in-progress.
  - **Structural tests over every workflow, example and Markdown YAML
    block:**
    - no `pull_request_target` or `workflow_run` trigger;
    - the comment job holds exactly `pull-requests: write`, with no checkout,
      local action or step of its own;
    - the run job holds no write scope;
    - no workflow-level permissions in shipped workflows;
    - every checkout sets `persist-credentials: false`.
  - **A real end-to-end comment** on this repository's own pull requests. It
    posts a PASS, reads it back, then replaces it with a no-verdict rendering
    for the same head commit. On a fork, it asserts the not-permitted path.

- **A poster for the behavioral gate comment: `cmd/trustvian-ci-comment`**
  (task 079, still partially implemented;
  [ADR 0058](docs/adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md)).

  Posts a `trustvian-ci-render` rendering as the pull request's one gate
  comment and edits it in place. It is standard library only, reads the token
  from `GITHUB_TOKEN` alone, and is the only Trustvian code that writes to
  GitHub. `.github/actions/trustvian-comment`, above, runs it.

  - **Ownership.** A comment is the poster's only if `github-actions[bot]`
    wrote it and its first line is `<!-- trustvian-behavioral-gate:<id> -->`.
    Human comments are never edited. The newest owned comment is updated, and
    duplicates are reported.
  - **Superseded runs write nothing.** If the pull request's head has moved
    on, a newer run owns the comment.
  - **Forks degrade loudly.** A refused write gives a warning and a
    job-summary note, and exits `0`. 429 and 5xx get one bounded retry, then
    the job fails visibly. A failed create is checked for having landed
    before it is retried, so it is never duplicated.
  - **No redirect is followed**, so the token reaches the validated API host
    only.
  - **Tested against a fake GitHub API**, including the renderer's real
    no-verdict bodies replacing a stored PASS.

- **An offline renderer for behavioral CI artifacts:
  `cmd/trustvian-ci-render`** (task 079, still partially implemented;
  [ADR 0057](docs/adr/0057-the-ci-renderer-is-a-standalone-offline-transcriber.md),
  [guide](docs/ci-github-action.md#rendering-the-artifact)).

  Turns a downloaded `trustvian-run` artifact into Markdown for a pull
  request comment or job summary. It works offline, needs no credential,
  executes nothing from the artifact, and is built from the Go standard library
  alone. It posts nothing itself; `.github/actions/trustvian-comment` runs it.

  - **The caller supplies the identity.** The head commit, repository, run id
    and attempt, and the run job's exit code are flags. `trustvian-run.json`
    must equal each one, and its size and SHA-256 must match `result.json` —
    consistency, not authenticity.
  - **The artifact is untrusted.**
    - Only the two fixed file names are opened, never a symbolic link, under
      bounded reads.
    - JSON is parsed strictly: duplicate keys, trailing data, invalid UTF-8
      and deep nesting are refused, and absent, `null`, `0` and `false` stay
      distinct.
    - Closed vocabularies are closed, and outcome shapes must be coherent.
    - Unknown fields are tolerated, as the compatibility contract requires, and
      never rendered.
  - **Transcription only.**
    - **A scenario verdict** shows every behavior, with both `k/N` counts and
      its stored classification, plus the `k` and `j` thresholds, all six
      checks with their advisory markers, reference reuse and both producer
      versions.
    - **A suite report** shows the recorded summary, its members (errors and
      skips included) and each member's own verdict. No suite verdict is
      composed.
    - Nothing is computed.
  - **An explicit no-verdict state.** It covers exit `2` and `3`, a missing
    artifact, a validation failure, and evidence that cannot fit. It carries
    the head commit, the run link and a fixed reason, and no number. A rejected
    artifact exits `3`, distinct from a run with no verdict (`1`).
  - **Inert, bounded Markdown.**
    - Every artifact string is one code span on one line, with control
      characters replaced, backticks fenced and pipes substituted.
    - Each value is capped at 256 bytes before the pipe substitution, and the
      body at 60,000 bytes, which bounds the output. Truncation is marked
      visibly.
  - **Tested against real producer output.** `testdata/generate.sh` drives the
    run action through the real CLI, control plane and Collector. Golden
    renderings pin the result.

- **A GitHub Action for the run side of the behavioral gate:
  `.github/actions/trustvian-run`** (task 079, partially implemented;
  [ADR 0056](docs/adr/0056-the-run-action-builds-a-pinned-source-commit.md),
  [guide](docs/ci-github-action.md)).

  Runs `trustvian eval run --json` for a scenario or a suite and uploads the
  result document as an artifact. Its final step exits with the CLI's own code.

  - **Exit codes pass through exactly.** `0`, `1`, `2` and `3` reach the job
    unchanged, and the `exit-code` output carries the code. `3` is never
    reported as `1`, and the action sets no `continue-on-error`.
  - **The result is preserved whatever the code.**
    - `result.json` is the CLI's stdout, byte for byte.
    - `trustvian-run.json` records:
      - the pull request's head commit, from the event, never the merge
        commit;
      - the exact exit code;
      - the result's status, size and SHA-256;
      - the runtime commit.
    - Output that is missing after a verdict, invalid, or larger than
      32 MiB + 64 KiB fails the action. It is never truncated or stored.
  - **Inputs** mirror the CLI: `scenario`, `suite`, `reference`,
    `scenario-timeout`, `fail-fast` and `api-url`, plus `working-directory`
    and `artifact-name`. Each value reaches the CLI as one argument, never
    through a shell string. There is no gate configuration in workflow YAML.
  - **A reproducible runtime.** No release ships `eval run` yet, so the action
    builds `trustvian`, `trustvian-local` and `trustvian-collector` from one
    reviewed commit pinned in `runtime.env`.
    - It uses a pinned, digest-verified Go 1.27.1, entirely under
      `$RUNNER_TEMP`.
    - Every binary must record that exact commit, unmodified, in its own Go
      build information. No version is injected.
  - **Its own control plane**, started on loopback and stopped by PID, or an
    existing one through `api-url`.
  - **Refuses `pull_request_target` and `workflow_run`.**
  - **A minimal job summary:** head commit, run link, exit code and artifact
    availability. No verdict is rendered.
  - **A run-only example workflow**, since extended to the two-job
    `examples/github-actions/behavioral-gate.yml`:
    - `pull_request` only;
    - `contents: read` per job;
    - commit-pinned actions and `persist-credentials: false`;
    - no secrets.
  - **Not yet:** caching the runtime across jobs. The comment and the
    renderer followed, above. Running a scenario in CI does not make a
    nondeterministic workload's verdict any more reliable; calibrate `N`, `k`
    and `j` for it first
    ([guide](docs/platform-cli.md#calibrating-n-k-and-j)).

- **Scenario suites: `trustvian eval run --suite DIR --scenario-timeout D`**
  (task 078,
  [ADR 0055](docs/adr/0055-a-scenario-suite-is-a-bounded-schedule-and-a-report-not-an-evaluation.md)).

  Runs every `.yaml`/`.yml` scenario directly inside a directory, in name
  order, one at a time, and reports them as one versioned document.

  - **Each scenario runs exactly as `--scenario` would.** Its own N, limits,
    recorded execution and control-plane verdict. Nothing is pooled or
    recomputed.
  - **Everything is validated first.** At most 64 scenarios, with distinct
    names, no symlinks and at most 4096 entries examined. A problem with the
    invocation or its files is exit `2`, and an unreadable environment is exit
    `3`; neither starts a workload or makes a request.
  - **Scheduling:**
    - A failure does not stop the rest; `--fail-fast` does.
    - Members not run are reported `skipped`, never as passes.
    - Ctrl-C or SIGTERM stops the running scenario and skips the rest as
      `cancelled`, also with `--fail-fast`. The suite is the only receiver of
      these signals, so the workload's process group gets one SIGTERM.
      Members run without the terminal.
  - **`--scenario-timeout` is a real deadline (1s–24h).**
    - The workload's process group gets SIGTERM, then SIGKILL after 5s, and
      leftover group members are killed.
    - The run and execution are failed.
    - The scenario is an operational error even if its workload exited `0`.
    - **A completion racing the deadline, or answered by a gateway's
      502/503/504, is settled by the control plane.**
      The runner fails the execution; if the server had already completed it,
      the member reports `completed_without_response`, a completed execution
      for which no verdict is reported. A member reported `scenario_timeout`
      or `cancelled` is never a completed execution.
  - **`--reference last` resolves per scenario.** An explicit execution id is
    `--scenario`-only.
  - **Output and exit code:**
    - The suite exits with its most severe scenario: `3`, then `2`, then `1`,
      then `0`.
    - Member result documents are embedded unchanged.
    - The document is capped at 32 MiB; overflow exits `3` with an explicitly
      incomplete document that claims no outcomes.

  No `/v1` or schema change. `trustvian dev` and single-scenario behavior are
  unchanged. `--suite` is refused on Windows, where `dev` is unsupported;
  `--scenario` is not. Member error messages are at most 1024 bytes of valid
  UTF-8.

- **Recorded scenario references: `trustvian eval run --reference
  <execution-id>|last`, and persisted scenario executions** (task 078,
  [ADR 0054](docs/adr/0054-scenario-executions-are-persisted-and-references-resolved-by-the-control-plane.md)).

  - **Every `eval run` is now a recorded scenario execution** (schema 9, both
    backends, metadata only). It begins before anything runs and completes with
    its verdict. A gate FAIL is a completed execution. An aborted one is
    recorded failed, and a running or failed execution is never a reference.
  - **`--reference` reuses a recorded execution's reference side.** All N of
    its reference runs are reused, from that one execution alone, and only N
    new candidate repetitions run.
  - **`last`** is the most recently completed execution of the same scenario,
    project, agent and environment, ordered by a completion sequence rather
    than any clock. If that execution is unusable, the command says so and
    never falls back to an older one.
  - **The current scenario's gate limits apply.**
  - **A reference that cannot be used stops the command with exit `3`, before
    any workload runs.** That covers a reference that is missing, unfinished,
    of another N, in another project or environment, or with incomplete
    evidence.
  - **New `/v1` routes:** `POST /v1/scenario-executions`,
    `GET /v1/scenario-executions/{id}` and `POST …/{id}/complete` and
    `…/{id}/fail`.
    - Completion evaluates through the same `CompareRepeatedEvaluations`
      implementation, and its comparison has compare-repeated's response shape.
    - The result document gains an optional `reference` block.

  **Schema 9** is a forward-only step on SQLite and PostgreSQL. It leaves
  existing data untouched and reconstructs no execution from it. A schema-8
  binary refuses a schema-9 database. Suites of scenarios are not built yet.

- **Behavioral scenarios: `trustvian eval run`, and repeated evaluation in the
  control plane** (task 078,
  [ADR 0053](docs/adr/0053-repeated-evaluation-counts-identities-across-isolated-repetitions.md)).

  One run of a model-driven agent is not evidence: the 2026-10-01 measurement
  found an unchanged agent failing a single-run gate at zero on half its pairs,
  because which optional tools it reaches varies between runs. A scenario file
  now runs each side `runs: N` times and gates over integer presence counts.

  - **The scenario file.** Every threshold and `runs` is required, with no
    defaults; `1 <= runs <= 64` and `0 <= j < k <= runs`. Counts and limits
    must be YAML integers; a fraction or a value past 64 bits is refused,
    including one that arrives through a merge or an alias. Unknown fields are
    refused and every error names its field.
  - **`trustvian eval run --scenario <file>`.**
    - Executes `2N` repetitions, one at a time, through `trustvian dev`, each
      under a fresh run id and its own `--behavioral-profile`.
    - The first repetition whose workload fails ends the scenario with exit `3`
      and no verdict. So does a repetition whose run could not be completed.
    - The workload's stdout goes to stderr, and a bare command name resolves
      on its side's own `PATH`.
    - It counts nothing itself: it submits the run ids and passes the server's
      verdict through as `0` or `1`.
  - **`POST /v1/evaluations/compare-repeated`.** The control plane reads each
    repetition's evidence, reports every behavior's reference and candidate
    presence counts, and classifies it added, removed or neither. It evaluates
    six checks; the two engine checks take the worst candidate repetition and
    carry `advisory: fresh_scope` when `runs > 1`.
  - **Refusals.** It refuses repetitions that shared a learning scope, ran in
    more than one environment, or disagree about fingerprint identity in either
    direction. It also refuses saturated evidence.
  - **The repeated limit's unit.** `max_repeated_added_behaviors` counts
    behavioral identities, the unit of `max_added_behaviors`. So at `runs: 1,
    k: 1, j: 0` the verdict is exactly `eval compare`'s, check for check.

  `eval compare`, its limits and its result are unchanged. No schema change.
  Comparing against a *recorded* reference execution (`--reference`) and suites
  of scenarios are not built yet.

- **An optional gate limit over counted behavioral changes:
  `max_added_behavior_changes`** ([issue 131](https://github.com/trustvian/trustvian/issues/131),
  [ADR 0052](docs/adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)).

  The comparison already reported `added_change_count`; nothing gated on it.
  `POST /v1/evaluations/compare` and `POST /v1/promotions` now accept
  `gate_limits.max_added_behavior_changes`, `trustvian eval compare` and
  `trustvian promotion create` accept `--max-added-behavior-changes`, and the
  browser's comparison and promotion forms carry an optional field for it.

  **Absent is not zero.** Omitted (or `null`), the check is not evaluated and
  the verdict is the other five checks alone — exactly what it was before.
  `"0"` is the strictest limit. The gate reports a sixth check,
  `added_behavior_changes`, with a `state` of `evaluated`, `not_evaluated` or
  `not_recorded`; only an evaluated check carries `actual`, `maximum`,
  `passed`, and the `correlation_state` and `counting_policy_version` its count
  rested on. When both behavior limits are supplied **both** are enforced.
  Under a `partial` or `unavailable` correlation the counted-change count is the
  identity count, so the check errs strict.

  **Schema 8** adds six nullable columns to `platform_promotions` on SQLite and
  PostgreSQL. Existing promotions migrate with the check `not_recorded`, no
  threshold and no outcome, and their stored verdicts and outcomes are
  untouched — no historical decision is re-evaluated or backfilled as a passed
  zero-valued check. The migration is forward-only; a schema-8 database is
  refused by an older binary.

  `max_added_behaviors` is unchanged and still counts identities. The evidence
  routes refuse `check=added_behavior_changes` with directions to the
  comparison's `added_changes` instead of resolving it, and the browser offers
  no evidence control on that row.

- **A comparison now reports how many behavioral *changes* its added
  behaviors amount to** (task 083, [ADR 0052](docs/adr/0052-a-counted-behavioral-change-is-an-added-identity-with-no-added-parent.md)).

  A developer adds one tool. The tool span and the transport child it calls
  are two behavioral identities, so a comparison reported `added 2` for one
  act. [ADR 0047](docs/adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
  settled that the identities stay two — the transport target is the
  security-relevant part, and folding identity would hide a tool that started
  posting somewhere else — which left the correction to counting.

  **A counted behavioral change is an added identity that is not the recorded
  child of another added identity** — and an identity is such a child only when
  every retained occurrence of it is. So the tool and its transport child are
  two identities and one change, and a known tool changing destination is
  still one change of its own: its parent is present in both runs, so it is
  not an *added* parent and nothing folds. That holds even when a new tool also
  reaches the same new destination: the occurrence beneath the known tool keeps
  the destination counted, and it also contributes to the new tool's change.

  `added_change_count`, `correlation_state`, `counting_policy_version` and
  `added_changes` join the comparison payload, the CLI output and the browser
  surface. Each counted change names its contributing identities, so every one
  stays resolvable to its observations through the existing evidence routes.

  **`max_added_behaviors` is unchanged and still counts identities.** No
  stored promotion, existing pipeline or historical run changed meaning, and
  no verdict moved. The optional limit over the new unit, specified in ADR
  0052, was deferred from this change because it needed promotion-table
  columns, a migration and both backends — separable work with its own risk.
  It is the entry above.

  The rule is structural — it asks only who is whose recorded parent, never
  what instrumentation layer an observation came from — so it is evaluable
  from durable evidence even though the layer is not persisted. Correlation is
  derived from the per-observation history schema 7 already stores, so there
  is **no schema change and no migration**, and a completed comparison is
  reproducible after a restart because the same rows produce the same edges.

  **Every unresolved case counts more, never less.** A missing parent, a
  parent in another trace, an ambiguous span reference, a cycle, a saturated
  or pre-schema-7 history: each falls back to the identity count and says so
  through `correlation_state`. A cycle or ambiguity anywhere in the added
  graph refuses the whole fold, so `partial` always means the identity count,
  contributors included. Folding only ever lowers a count, so an
  unresolved correlation can make a comparison stricter than it needed to be
  and cannot make one pass that should have failed.

### Changed

- **The run action's runtime pin** moves to `5521759` (#140), the first commit
  with both the renderer and the poster. #138–#140 changed no CLI,
  control-plane or Collector code.
- **Every `actions/checkout` in `ci.yml`, `nightly.yml` and `release.yml`** now
  sets `persist-credentials: false`. Nothing after a checkout needs the token.
- **`trustvian-ci-comment`'s superseded notice** names the newer head by 12
  characters rather than the full SHA.

- **Task 078 is closed. Acceptance criteria 9 and 11 are amended to what the
  evidence supports, and visibly so**
  ([amendment](docs/tasks/v1.0/078-behavioral-scenario-suites.md#amendment--criteria-9-and-11-2026-10-03)).
  Documentation and tests only, with no runtime change.

  - **Criterion 11** claimed that an unchanged *nondeterministic* workload
    passes under the documented limits. The 2026-10-01 measurement refutes it.
    At N = 5, an unchanged model-driven agent failed `k = 1, j = 0` in 6 of 252
    self-comparison splits at T = 0.7, and 1 of 252 at T = 1.3. At T = 1.3, no
    `k` removed every crossing. As amended:
    - An unchanged *deterministic* workload passes. CI asserts it, now also at
      the control plane at exactly `k = 1, j = 0`.
    - For a nondeterministic workload, no default `k` ships. A new section of
      the scenario guide,
      [Calibrating `N`, `k` and `j`](docs/platform-cli.md#calibrating-n-k-and-j),
      shows how to choose them from the workload's own self-comparison
      false-FAIL rate with existing commands. Each calibration costs `M × 2N`
      workload runs. The demo repository's `make stability` is the reference
      implementation.
  - **Criterion 9** asked that a reorder producing gated evidence fail a
    scenario. That cannot happen end to end under `trustvian eval run`:
    - `dev`'s generated Collector configuration sets no anomaly block, so
      `transition_weight` is 0.
    - Each repetition's scope is fresh, with anomaly confidence at its floor
      (0.1786 measured).

    As amended: scenarios assert no ordering, and nothing in the scenario path
    suppresses sequence evidence. Under fresh-scope repetitions with the default
    `transition_weight = 0`, a reorder cannot produce gated evidence end to end,
    and checks 5 and 6 are advisory there. New tests:
    - `TestScenarioRefusesOrderingKeys`: `order:`, `sequence:` and `steps:` are
      refused as unknown fields.
    - `TestAnalyzeReorderedSequenceIsGatedOnlyWithTransitionWeight`: with a
      learned baseline and `TransitionWeight > 0`, a reordered sequence is
      blocked or reaches critical risk. At weight 0 it is neither.
    - `TestAReorderFailsTheRepeatedGateOnlyThroughEngineEvidence`: those records
      fail check 5 through the real control plane, and pass at weight 0.
  - **ROADMAP.**
    - 078 and 079 are marked implemented in both tables and in the task index.
    - The `v0.10.0` section no longer says the release archive lacks `dev`'s
      helpers, which #114 ships on macOS and Linux from the next release on.
      ADR 0043 gains a dated note.
    - "Current State" now states what ships on `main`: `dev`, the WebUI,
      `eval run` with suites, the CI action, and every implemented task. It also
      names what is not implemented.

- **The browser surface is now an admin console, and no journey through it
  requires typing an identifier** (task 096). Task 074 removed the identifier
  form from the front door and ADR 0041 made the durable hierarchy
  discoverable, but every surface *behind* Live was still built out of inputs:
  Compare asked for two run identifiers through two menus, Evidence asked for a
  run and a narrowing, Promotions asked for a project before it listed
  anything. The identifiers had become discoverable without the surfaces ever
  becoming browsable.

  **A persistent sidebar, tables as the primary surface, and a contextual panel
  beside them.** Destinations are Live, Projects, Runs, Compare, Evidence,
  Promotions and Manage — one per capability `/v1` actually serves, and none
  for anything that does not exist yet. The sidebar always says which project
  the console is scoped to, and changes it by going to a searchable project
  table rather than by opening a menu.

  **Rows are the navigation.** Projects, runs, observations and behaviors are
  each a table of what the server returned, and clicking a row opens that
  record. A run's workspace carries its authoritative counts on a compact strip
  and three tabs — overview, observations, behaviors. Selecting an observation
  opens its recorded decision, scores, timing and correlation references
  alongside the table, without redrawing it; closing the panel returns focus to
  the row it came from.

  **Every identifier is a control that goes somewhere.** Session, trace and
  behavior references narrow the observation table to that view — a new bounded
  request with the narrowing applied in storage before the page bound, at most
  one at a time because the server accepts at most one.

  **A comparison's two sides are assigned from rows.** Every run in the table
  carries a `Reference` and a `Candidate` control; both chosen runs are then
  shown in full in labelled panels, and the submit stays disabled until two
  different runs are chosen. There is no identifier field for either side and
  no menu standing in for one. Shared-behavior evidence keeps its visible
  reference and candidate controls.

  **Nothing about the bounds changed.** One page per action, the continuation
  is still an explicit control, the startup budget is still one page of
  `GET /v1/projects`, and narrowing still happens in storage. The filter boxes
  narrow the rows already on screen and say so; a table always states whether
  it is showing the whole collection or one page of it. No figure on a summary
  strip is counted from the rows on screen — a strip reports what the run
  observed, not how much the page drew — and there is no decorative metric,
  invented chart or placeholder destination anywhere in the shell.

  Menus survive only where the choice is a setting rather than navigation: the
  promotion form's target environment and its run pickers, filled from a
  comparison already made. Every form, lifecycle control and result target that
  existed still exists, under Manage.

  Content-Security-Policy is unchanged and still carries no `unsafe-inline`,
  so there is still no web font, no CDN and no build step. See
  [ADR 0050](docs/adr/0050-the-browser-surface-is-a-record-first-admin-console.md).

- **The browser bundle is a layered design system** (task 096, second pass).
  The console reorganised the *information* architecture and left the bundle
  as it was: fifteen modules in one flat directory and a 1,400-line
  stylesheet. Three costs followed, each visible on screen — values were
  named wherever they were used and light and dark drifted apart; loading and
  empty rendered identically, so a reader could not tell "wait" from "there
  is nothing here"; and a `block`/`critical` observation was the same weight
  as an `allow`/`low` one, so finding the row that matters meant reading
  every line.

  **Five layers, dependencies only downward.** `core/` (DOM and formatting,
  no Trustvian at all) → `v1/` (the control-plane contract) and `ui/` (the
  design system), `live/` (the realtime observatory), `views/` (one module
  per destination), and `app.js` as the only composition root. `ui/` may not
  import `v1/`: the moment a component can read a route, presentation stops
  being a layer.

  **Only `styles/tokens.css` names a raw value.** Colour, size, space, radius
  and duration are each declared once with the dark-mode counterpart beside
  it; a hex literal in any other sheet now fails a test. The accent is blue
  by elimination — green, amber and red carry verdict and risk, violet
  carries "new", and an accent sharing any of those would make *selected*
  read as *severe*.

  **Four states, kept apart.** A fetching table draws a static skeleton in
  the shape of the rows that are coming; an empty one states the absence and
  names the next action; a refusal is shown as the refusal it is. The
  skeleton does not shimmer, because nothing on this page loops.

  **Severity as three carriers.** A gutter down the row's leading edge, a
  tint behind the row, and a mark beside the word — and the word, which is
  the server's, still carries the meaning alone.

  Also: a grouped sidebar with inline-SVG icons (built through
  `createElementNS`, never parsed, never a font) and counts where the console
  holds one; the scope chooser collapses once it has chosen, since the strip
  below already states the agent and candidate; and lifecycle transitions and
  refusals report through a short-lived status region instead of changing one
  word in a panel nobody is looking at.

  Four new guards hold it together, each verified to fail when its property
  is broken. Test helpers now name a module rather than a file path, and two
  harnesses that copied assets to a temporary directory reproduce the tree
  instead of flattening it. Nothing about the bounds, the privacy allowlists
  or the Content-Security-Policy changed. See
  [ADR 0051](docs/adr/0051-the-browser-bundle-is-a-layered-design-system.md).

### Fixed

- **Three state defects in the browser console, found in review.** All three
  were invisible to a source scan, because what was wrong was which of two
  responses arrived last and what a boolean meant after the reader had moved.

  **Run-scoped state survived a change of run.** Opening run B after viewing
  run A's Behaviors tab left the tab marked loaded, so it rendered A's rows
  and fetched nothing — and its paging cursor would have asked for the next
  page of A's collection under B. The same shape applied to the observation
  page, the narrowing, the selected row and the strip's counts. All of it is
  now cleared in one step when the run changes.

  **A stale response could overwrite the current view.** `openRunDetail`,
  the observation loader and the behavior loader committed whatever came
  back. A response for a run or a filter the reader had left would repopulate
  the surface, clear a newer error, close the current detail panel, or stop a
  newer loading indicator. Each read now takes a ticket before awaiting and
  presents it back before touching anything; the level browser checks its
  own generation before assigning, because the commit happens inside the
  awaited call and a guard in the caller would run after the clobber.

  **Project-scoped state survived a change of project.** Compare and
  Promotions decided whether to fetch by asking "have I loaded before?",
  which is still true after switching from project A to B — so B showed A's
  agents, A's assigned comparison sides and A's promotion history, under B's
  name in the sidebar. Cache validity is now project identity, and a change
  of project abandons what is in flight and clears what is held.

  This is **not** built on cancellation: an abort can lose the race with a
  response already queued, and a surface whose correctness depended on the
  abort winning would be right almost always. Surfaces are independent — the
  three reads of a run workspace, Compare's three lists and Promotions each
  hold their own ticket, so fetching one never abandons another's work. One
  further defect fell out of that: Compare's loading flags were the ones the
  Runs destination reads, so fetching on either drew a skeleton over the
  other.

  Ten behavioural regression tests drive the real modules under node with
  deliberately out-of-order promises, rather than asserting on source
  strings. Every one was verified to fail against the defect it covers.

### Added

- **The browser now follows a finding to the evidence behind it** (task 076). A
  gate FAIL named a count, task 085 made that count resolvable over `/v1` and the
  CLI, and a developer still had to leave the page to read the answer. An
  **Evidence** tab closes it.

  From a comparison, every gate check and every behavioral delta carries an
  evidence control. One step reaches the behavioral identities that contributed;
  one more reaches the retained observations that carried one of them; and from
  an observation, `Session`, `Trace` and `Behavior` controls open that run's
  retained history in the correlated view. **No identifier is typed anywhere in
  that path** — the finding reference is built from the two run identifiers the
  comparison itself returned, and each navigation control appears only when the
  observation actually recorded the identifier it needs.

  **Five views over one run's retained history**: session actions, trace context
  with its recorded parent/child structure, behavior sequence, decision timeline,
  and one behavioral identity's detail. Plus a provenance panel showing both
  sides' supplied `CandidateMetadata`, where **every field the producer did not
  supply reads `not stated`** — an unknown model is not the same fact as a model
  both sides shared.

  **Trace structure is drawn from the recorded parent span reference and nothing
  else** — never from timestamps, adjacency, name similarity or ingestion order.
  A parent this page does not have is not a root: `root`, `child`, `unresolved`,
  `ambiguous`, `self`, `cycle` and `unstated` are seven distinct states, the tree
  emits every row exactly once, and a cycle is stated rather than followed.
  Ordering is arrival order, which is not wall-clock order and is not reasoning:
  no label says an agent decided, chose, intended or planned anything, and
  per-observation durations are never summed into a latency.

  **Absence is shown as absence.** A measured zero renders as `0 ms (measured)`
  and an unmeasured duration as `not available`; `unset` is never success and an
  absent status is never "no errors"; `resolved`, `none_found`, `indeterminate`
  and `aggregate_only` each carry the sentence that keeps it apart from the other
  three. The status describes the **finding**, so a continuation page that comes
  back empty still reports `resolved`. `aggregate_only` is an applicability
  answer rather than a history one: the two minimum-count checks are resolved
  without reading any retained history, so the page shows their explanation and
  recorded count and makes no claim about availability, retention or sampling.

  **The three sub-surfaces cancel only themselves.** Finding, run history and
  provenance each hold their own request token and page position, so reading one
  neither discards a response the others are waiting for nor moves their page
  numbers.

  **The browser decides nothing.** Every status, side, recorded count and
  exhaustiveness flag is a value `/v1` returned; a test asserts each status
  literal appears in shipped source exactly once, as the key of the sentence
  explaining it. A cross-layer test runs the shipped row projection over a real
  route response and compares each rendered cell against the field it came from.

  **Two things the views cannot state, and say so.** Fidelity and behavioral
  layer are not retained per observation — they ride on the ingest envelope and
  the realtime frame — so a historical view renders the recorded descriptor
  verbatim and states that fidelity was not retained. Sequence-deviation evidence
  lives in the anomaly contributors, which retention excludes, so no view makes a
  statement about sequence deviation. Neither is inferred.

  **One new query capability, no schema change.**
  `GET /v1/evaluation-runs/{run_id}/observations` gains `session_id`, `trace_id`
  and `fingerprint_id`: optional, mutually exclusive equality narrowings applied
  in storage **before** the page bound, through task 067's existing digest
  indexes and against the original value beside each key. More than one is
  refused rather than answered. No table, no index, no migration, and no existing
  route, field or response shape changed. See
  [ADR 0049](docs/adr/0049-the-evidence-explorer-narrows-retained-history-and-answers-a-behavioral-question.md).

  **Nothing is stored in the browser**, one page is held at a time, and a
  response arriving after the run, view, side, finding or filter changed is
  discarded rather than drawn.

- **A failed gate check now leads to the evidence behind it** (task 085). The gate
  printed `added_behaviors actual 3 maximum 0 FAIL` and nothing in the platform
  could answer *which three*, or which observations carried them — `BehaviorDelta`
  holds no reference to an observation, and a gate result is deliberately closed.
  The investigation restarted from the run identifier every time.

  Two bounded `GET` routes and a `trustvian evidence` command family now resolve a
  finding to the behavioral identities that contributed to it, and each identity
  to the retained observations that carried it.

  **This does not reopen the gate's fixed shape.** Resolution is a query against
  authoritative state, not a payload inside a verdict: no gate result, scorecard,
  count or fingerprint changes, and nothing is recomputed — behavioral identities
  come from the same comparison function over the same persisted snapshots, and
  recorded counts come from the persisted aggregate.

  **A finding reference is built only from durable values** — two run identifiers
  and either a gate check name or a fingerprint — so it is stable by construction,
  needs no finding table, and travels in the query string, which makes the
  resolution URL itself the citable link.

  **An empty answer is not always the same answer.** A resolution reports
  `resolved`, `none_found`, `indeterminate` or `aggregate_only`, and the status
  describes the **finding** rather than the page: a page requested past the last
  match returns zero rows with `resolved`, because the evidence exists and the
  caller has read all of it. `none_found` is returned only when nothing matches at
  all and the history is complete; an empty result over partial or unavailable
  history is `indeterminate`, because absence there establishes nothing. Page
  exhaustion is signalled by the continuation cursor being absent.

  **A behavior present in both runs requires an explicit side.** Both runs hold
  their own observations of it and those two sets are what a developer is
  comparing, so the control plane refuses to pick one — an added behavior defaults
  to the candidate and a removed one to the reference, because each exists in one
  run only. `exhaustive` is true only when the history is complete, so a full set of
  matches drawn from a bounded history is never labelled as all of them. The
  recorded count travels beside the rows and is never reconciled with them — a
  check counts every record a run ingested, while retention is bounded.

  Three of the five gate checks resolve to evidence; `reference_evidence` and
  `candidate_evidence` report `aggregate_only`, because they fail when a run
  observed *too little* and an absence has no supporting records to invent.

  Filters are SQL predicates applied before the page limit, the behavioral filter
  uses task 067's bounded digest index and compares the original value as well as
  the key, and each page and its history metadata are read from one snapshot.
  **No schema change.**

- **A decision can now be read back after the run that produced it ended**
  (task 067). The platform retained two reductions per evaluation run — a
  fixed-shape aggregate and a behavior snapshot capped at 512 distinct behaviors
  — and neither can say *which* observation scored 0.91, when it happened
  relative to the one before it, or what trace it belonged to. Realtime carried
  that detail and dropped it when the connection closed.

  Each accepted record now retains one bounded observation row, readable through
  `GET /v1/evaluation-runs/{run_id}/observations`.

  **An observation is identified and ordered by `(run, ingest sequence)`, never
  by a span id or a timestamp.** A span id is unique only inside its trace, an
  event id is caller-supplied, and equal timestamps are ordinary — any of the
  three would make paging non-deterministic. The sequence is allocated by the
  transaction that admits the record, so it cannot collide. A parent span ends
  *after* the children it started, so out-of-order arrival is the normal case and
  is retained as it arrived; a child whose parent never arrives is still a child.

  **The row is written inside the transaction that already writes the aggregate,
  the snapshot and the ingest cursor.** A rejected record or a failed transaction
  retains nothing and moves nothing, and a retry — including a concurrent
  identical submission — produces no second observation.

  **A run says how much of itself its history describes**, in three states rather
  than two. *Complete* means every accepted record is retained. *Partial* means
  some are not: the run passed the 4096-observation bound, or it began ingesting
  before this schema existed and resumed after it. *Unavailable* means the run has
  records and none of their history was ever retained. **A database migrated from
  schema 6 gains an empty history and invents nothing** — reporting "complete,
  zero rows" for a run whose records predate retention would be a fabricated
  historical fact.

  Saturation is degraded evidence rather than a failed ingest: past the bound the
  aggregate, the snapshot and the cursor keep advancing, exactly as they do when
  the behavior collector saturates.

  **No content, and no new privacy surface.** The retained field set is an
  allowlist expressed as columns — there is no attribute map, no span-event list
  and no payload column, so no prompt, completion, tool argument, result,
  document, body or arbitrary attribute can be written through one. What changed
  is *duration*, which is why the existing tripwire sweep was extended to the new
  route and the new rows rather than trusted to the contract.

  **A page is read from one database snapshot**, so an ingest committing during a
  read yields the state before it or the state after it and never a mixture. The
  read is three statements, and against a pool a concurrent commit between any two
  of them returns a retained count of 1 beside two rows — a state the database
  never held, and one nothing in the response marks as composite. SQLite reads in
  a transaction; PostgreSQL reads in a read-only `REPEATABLE READ` transaction,
  and every write in that store keeps `READ COMMITTED`.

  **Correlation identifiers are retained whole, at any length the request body
  allows.** `trace_id` and `session_id` are validated nowhere on the ingest path
  and `fingerprint_id`'s 256-byte bound is skipped once a run saturates, so the
  platform already accepts values larger than a PostgreSQL B-tree key can hold.
  The three correlation indexes therefore key on a fixed-width digest stored
  beside each value, rather than on the value — indexing the value would have made
  retaining an already-accepted record fail and roll back its whole ingest, on
  PostgreSQL and not on SQLite. Storage does not get to narrow what the platform
  accepts.

  Schema **6 → 7** on both backends, forward-only: two tables and three
  run-scoped indexes, and no row.
  [ADR 0048](docs/adr/0048-retained-history-is-sequence-identified-bounded-and-honest-about-absence.md)
  records the reasoning.

- **Trustvian records what called what, how long it took, and whether it failed**
  (task 084). The engine already computed two of the three and threw them away at
  the boundary: `features.Extract` reads `duration_ms` and `error` off
  `Event.Attributes` because both adapters bridge them there, and neither reached
  `DecisionRecord`. Parent span identity was never read at all.

  A record now carries four named scalar fields — `parent_span_id`,
  `span_lineage`, `duration_nanos` and `span_status` — and the platform aggregates
  duration and status per evaluation run and persists them.

  **Availability is explicit everywhere, because the alternatives are wrong in
  the reassuring direction.** A span with no end timestamp did not take zero
  milliseconds, so `duration_nanos` distinguishes `""` (unavailable) from `"0"`
  (a measured zero). Neither an unset status nor an absent one is success —
  OpenTelemetry's status defaults to `UNSET` and most instrumentation never sets
  `OK`, so counting either as success would let an entirely unstatused run report
  a zero error rate. The aggregate publishes four status counts and no rate, so a
  caller has to choose and name its own denominator.

  **Parentage is read and never inferred** — not from timing, adjacency, span
  names or arrival order. A child whose parent was sampled away or has not arrived
  is still a child, and nothing checks that a named parent exists: a parent span
  ends *after* the children it started, so a child arriving first is the normal
  case. Neither OTLP nor the SDK can express "the producer does not know", which
  is documented rather than worked around.

  **Nothing added is behavioral identity.** Two observations differing only in how
  long they took share a fingerprint, and a test asserts the whole
  `StableFeatures` tuple, the fingerprint and the learning path are unchanged
  across every value. The volatile feature bridge is untouched, with one
  documented divergence: it still ignores a zero duration because a zero adds
  nothing to a feature, while the evidence path records it.

  Schema version moves to **6** on both backends, adding nine columns to one
  table. Existing rows are migrated to say *unknown* rather than *zero*: a run
  ingested before these fields existed observed no duration and no status for any
  of its records, and the backfill states exactly that.

  Per-observation history remains task 067's, and this adds none.

- **`trustvian dev` works from a downloaded release** — no checkout, no `make`.
  The macOS and Linux archives now ship the two helpers dev supervises beside
  the CLI:

  ```text
  trustvian            the CLI
  trustvian-local      the local control plane
  trustvian-collector  the OTLP receiver and the Trustvian processor
  ```

  Extract, run `trustvian dev`, and it finds them. dev's resolution order
  already looked alongside its own executable — that step was written for this
  and needed no change — so there is no new configuration and no new mechanism.

  This closes the item ADR 0043 left open. The Windows archive is unchanged:
  `dev` refuses to start there at all, so the helpers would be 60 MB with no
  capability behind them. A macOS or Linux archive grows from roughly 8.8 MB
  compressed to roughly 40 MB; `go install` still yields only `trustvian`,
  because the helpers live in repository-internal modules the root module must
  not import, and the not-found message now names that case instead of implying
  something is misconfigured.

  Supply-chain posture is unchanged and now stated explicitly in
  `docs/supply-chain.md`: all three binaries are covered by the release's
  SHA-256 manifest, exactly as the CLI alone was. The archives carry no SBOM or
  provenance attestation — they did not before either — and attesting them is
  recorded as a known gap rather than left as an assumed guarantee.

- **Trustvian understands agent-oriented telemetry** (task 075). Zero-code
  instrumentation flattens an agent into its transport: a model, a CRM, a
  knowledge service, an exporter and a mailer all become `POST` and `GET` against
  hostnames, and the baseline learns transport shapes rather than behavior. Where
  a producer emits OpenTelemetry GenAI or OpenInference, Trustvian now reads it:

  ```text
  before   http · POST /v1/export → export.localhost
  after    tool · export_customer → export.localhost
  ```

  Same engine, same pipeline, same six behavioral dimensions. No AI-specific
  branch anywhere, and **no core change**: `event.OperationCategoryTool`,
  `event.ActorTypeAIAgent` and `event.Context.SessionID` already existed.

  **One table, read by both adapters.** `internal/semconv` takes a span reduced to
  plain Go values and returns what a convention established. `internal/otel` and
  the Collector processor both call it — their *traversal* stays duplicated
  because `sdktrace.ReadOnlySpan` and `ptrace.Span` are unrelated types, but the
  table does not, because two copies of a convention table would be two
  conventions and the one a developer got would depend on which adapter their
  telemetry took. `event.NormalizeSpan` re-exports it so the processor's separate
  module can reach it.

  **It imports no OpenTelemetry package**, and that cost nothing to arrange: the
  GenAI attribute keys appeared in Go's `semconv` around v1.39.0, grew to 50 keys
  by v1.41.0, and were **gone by v1.42.0** — the version both adapters pin. The
  names had to be string literals wherever the table lived, so the confinement
  rule holds with no exception. `scripts/check-platform-boundary.sh` now proves the
  core's graph contains zero OpenTelemetry packages via `go list -deps`, paired
  with a check that `internal/otel` still imports one so the first cannot pass
  vacuously.

  **Conventions are read; frameworks are never named.** No framework appears in
  any type, field or branch, and the boundary script fails the build if one
  appears in non-test source. Both conventions were verified live rather than from
  memory, with the commits recorded in the source:
  `semantic-conventions-genai` at `e57c543b4889` and `Arize-ai/openinference` at
  `300bba9191bf`. `gen_ai.system` turned out to appear nowhere in the current
  convention, so it is read only as a legacy alias because producers lag the spec —
  and deliberately not confused with `gen_ai.system_instructions`, which is the
  system prompt.

  **Identity is read; content is refused.** A tool *name* is what the agent did; a
  tool *argument* is what it said. Twenty-two content attributes are enumerated in
  `internal/semconv/content.go` and read by nothing — the list exists so the
  refusal is checkable rather than asserted. The privacy guarantee is a **durable
  and public evidence boundary**, not a claim that the transient
  `Event.Attributes` map is empty: the adapters' documented
  preserve-every-attribute behavior is unchanged, and the tests deliberately
  assert content *is* there while proving it reaches no `StableFeatures`, no
  fingerprint, no `DecisionRecord`, no realtime field, no persisted row and no
  `/v1` payload. Each content attribute carries its own distinctive value so a
  failure names which one leaked, and the sweep is verified to catch a planted leak.

  **No fabrication.** A category that matched with its identity attribute missing
  does not fire — `tool · POST` would be a semantic category wearing a transport
  name. An unknown operation name, an unknown span kind, a renamed attribute or an
  attribute of the wrong type all mean "the convention is absent". OpenInference's
  `tool.name` is read only on a `TOOL` span, because the same key appears under
  `llm.tools.<index>` as an advertised tool *definition* — reading it bare would
  record a model span that merely lists its tools as having used one.

  **`Actor.Type` upgrades only on an explicit agent identity**, never on the mere
  presence of a GenAI operation: a backend service calling an LLM through an
  instrumented client emits `gen_ai.operation.name=chat` and is not an agent — and
  `ActorType` is a `StableFeatures` dimension, so a wrong upgrade would discard
  that actor's learned baseline.

  **Fidelity is reported, never implied.** A closed two-value vocabulary —
  `transport` or `semantic` — describing the mapping result rather than the span.
  Carried on the outbound span attribute `trustvian.fidelity`, on the ingest
  envelope beside the record, on the realtime observation (always present, so
  absence never needs interpreting), and in the WebUI inspector as a sentence
  rather than a badge. Never in `StableFeatures`: folding it in would reset every
  baseline the day a producer upgraded its instrumentation. There is no inbound
  override — a producer able to claim semantic fidelity would defeat the
  guarantee.

  **Graceful degradation is asserted, not hoped for.** A producer emitting no
  convention sees byte-identical behavior, checked against real SDK spans as whole
  values and end to end through a real Collector: the same fixture producer emits
  GenAI spans in one mode and plain HTTP spans in the other, and the second yields
  exactly one transport-named behavior with the actor left as `service`.

  Reasoning in
  [ADR 0045](docs/adr/0045-conventions-are-read-frameworks-are-not.md).
  One piece is deferred as task 081: fidelity is not persisted per behavior, so a
  comparison delta does not carry it — that needs a forward-only schema step in
  both backends, and `TestFidelityIsNotPersistedYet` fails the moment it lands.

- **`trustvian dev` — one command runs an agent under Trustvian** (task 077).
  Watching an agent behave used to mean a control plane, a Collector, a
  hand-written processor configuration, four `create` commands and the right OTLP
  environment. Now:

  ```bash
  make dev ARGS='-- python agent.py'
  ```

  `dev` starts `trustvian-local` and `trustvian-collector`, generates the
  Collector configuration, provisions the project / agent / candidate / run
  hierarchy, points the workload's exporter at the receiver, runs the command
  unchanged, and prints the URL to watch it. The application is not modified and
  gains no Trustvian dependency.

  **It supervises the Collector; it does not own a receiver.** An OTLP receiver
  is a protocol stack, `trustvian-collector` already carries it, and putting one
  in `cmd/trustvian` is the dependency leak `.claude/rules/architecture.md`
  forbids. The generated configuration is `text/template` output rather than a
  YAML library, so the root module gains no dependency, and every substituted
  scalar is *refused* rather than rewritten when it would need quoting. Both OTLP
  protocols are enabled, because a workload that builds its exporter in code
  chooses its own transport — an HTTP-only receiver would have observed nothing
  from this repository's own gRPC demo producer, silently.
  [ADR 0042](docs/adr/0042-dev-composes-the-collector-rather-than-owning-a-receiver.md).

  **Identity comes from the repository, and nothing is invented.** Project from
  the repository name, candidate from `git:<short sha>` plus `+dirty`,
  environment `local`, run id generated per invocation with millisecond
  precision. The Agent is the workload's own `OTEL_SERVICE_NAME` (or
  `service.name` in `OTEL_RESOURCE_ATTRIBUTES`) and is **required** when the
  workload declares neither: the processor derives the actor from the arriving
  `service.name`, so an invented Agent id would produce a run whose evidence
  cannot be attributed to it. The hierarchy is *ensured*, not created, so a
  second run of one commit meets the first run's baseline.
  [ADR 0043](docs/adr/0043-dev-provisions-the-local-hierarchy-from-the-repository.md).

  **The workload's repository is never written to** — not its files, not its
  dependency manifests, not its git state. `dev`'s state lives under
  `~/.trustvian/dev/<hash of the workload directory>/` and the path is printed on
  every start. Discovery gained a second location so `trustvian eval compare` in
  that directory still needs no `--api-url`. The end-to-end test hashes every
  file including `.git` before and after two real runs and compares the trees;
  `git status` runs with `--no-optional-locks` so even reading the state cannot
  write it.

  **The engine's baseline is file-backed**, one file per behavioral profile.
  With the in-memory default the learned baseline was discarded at every run's
  end, which made both engine-evidence gates unfireable — with an empty baseline
  every behavior is novel, anomaly confidence is zero, no `BLOCK` is ever
  decided, and `max_block_decisions` and `max_critical_risk_observations` could
  only ever read zero. Since the file store has no cross-process locking, a
  second concurrent run of the same candidate is refused with a message naming
  the run that holds it.

  **Instrumentation ownership is positive-evidence-only.**
  `--instrumentation existing | none | auto`, defaulting to `auto`, which either
  finds positive evidence that the workload initializes OpenTelemetry or **stops**
  — it never falls through to injection. A program can initialize the SDK after
  it starts, which is after the only moment `dev` could inspect it, so "detected
  nothing" is not "there is nothing"; attaching a second stack to one that exists
  would report every action twice, and duplicate spans are a behavioral lie the
  engine would faithfully report as an anomaly. `OTEL_SDK_DISABLED=true` is
  refused for every mode that would route, because it is the opposite of
  evidence. `python-zero-code` is named, reserved and refused pending an
  interpreter compatibility check. `none` means no *routing*, not no variables:
  it still declares `deployment.environment.name` and, when the workload declares
  none itself, `OTEL_SERVICE_NAME` — without those the platform refuses every
  record and the run collects nothing while every process reports success.
  [ADR 0044](docs/adr/0044-instrumentation-ownership-requires-positive-evidence.md).

  **It is transparent to scripts.** The exit status is the command's own,
  including `128 + signal`; `2` and `3` can only be reported before the command
  starts. `SIGINT` and `SIGTERM` are forwarded to the command's process group,
  and the run is **completed** rather than failed when the developer stopped it.
  If stdin is a terminal, `dev` hands the terminal over so an interactive
  workload can read input; `Ctrl-C` then reaches the whole foreground group and
  the run is still completed. Windows is refused with a reason rather than
  partially supported — no `SIGTERM` delivery, no `os.Interrupt` for another
  process, no `Setpgid`.

  **No new third-party dependency**, in any module. `docs/local-development.md`
  now leads with `trustvian dev` and documents running the parts separately;
  `docs/platform-cli.md` documents the command surface; `docs/compatibility.md`
  records the exit-status exception, the run-state mapping and the terminal
  Ctrl-C rows.

- **The web interface is now a live observability cockpit** (task 074). The
  primary surface stopped being a CRUD console organized around
  `Open by ID` and became a window into what an agent is doing.

  **Information architecture.** Five surfaces — **Live**, **Investigate**,
  **Compare**, **Promotions**, **Manage** — replacing eight tabs of which four
  were entity forms. Every control-plane operation still works, moved into
  Manage's five sub-sections: nothing was deleted, only demoted.

  **The Live Observatory** is the default. A header carrying connection state,
  the agent being watched and the run's authoritative counts; an active-scope
  rail populated from `RealtimeScope` alone; a behavior canvas; an inspector;
  and a bounded timeline. Opening `/` asks for nothing — the page subscribes,
  discovers active agents by itself, selects the most recently active one and
  animates its behavior flow.

  **Selection follows, or is pinned.** The newest active run is drawn until a
  developer clicks a card; from then on the choice is theirs and activity
  elsewhere raises its own card without taking the graph they are reading. A
  **Follow active** control returns to auto-selection, and the rail says which
  mode it is in.

  **Animation is evidence.** One pulse per received observation, along the edge
  that observation describes, ending when it arrives. No ambient loop, no idle
  motion, no simulated traffic, no replay after a reconnect — a quiet agent
  draws a still graph. A new behavior gets a persistent `NEW` badge on the
  edge, its target, the timeline row and the inspector, which opens it unless
  the developer is already reading something else. `NEW` means new: it is never
  labelled dangerous or unsafe.

  **The inspector shows the server's values and only those.** Decision, risk,
  trust, anomaly, confidence and new-behavior state, rendered as reported.
  Nothing is computed, combined, thresholded or ranked, and there is
  deliberately no aggregate health score — collapsing five independent readings
  into one red/amber/green verdict would be the browser inventing a judgement
  the platform never made. The privacy allowlist is unchanged: a richer panel
  is not permission to widen it, and a detail drawer is not an exemption.

  **The counters are authoritative.** The header's observation and
  distinct-behavior counts come from `GET /v1/evaluation-runs/{id}/progress`,
  read once per selection. A card's own number is labelled *seen live* and is a
  frame count, which is a different fact.

  **Forms explain themselves.** Every caller-owned identifier in Manage carries
  inline help with an example, and Compare and Promotions offer run selectors
  filled from the hierarchy browser so an opaque identifier no longer has to be
  copied out of a log. The gate limits say that they are policy rather than
  evidence.

  Unchanged: the startup budget of one `GET /v1/projects` page, zero automatic
  continuations, the run-scoped graph model, every visualization bound, and the
  technology — vanilla ES modules, SVG, same-origin `/v1` and SSE, with no
  framework, npm, CDN, external font or build step. The modules are split by
  responsibility (`graph.js`, `rail.js`, `timeline.js`, `inspector.js`,
  `discovery.js`) rather than grown into one `app.js`.

- **Zero-input live behavior WebUI** (task 074). Opening the WebUI used to show
  a form asking for four identifiers a developer did not have; *run locally,
  observe behavior live* meant reading a run ID out of a producer's logs. The
  page now lands on **Live**, subscribes to all local activity, and shows every
  run producing telemetry as a card — **with nothing typed**. Select one and
  its behavior flow draws itself: source agent, operation, target, and the
  decision, risk, trust and anomaly the server computed.

  **Discovery, never provisioning.** Nothing creates a durable entity because
  telemetry arrived. OTLP traffic does not create a Project, `service.name`
  does not become an Agent, and the browser creates no Candidate and starts no
  run. A producer still establishes the evaluation hierarchy — the Collector's
  `evaluation:` block names a run that must already exist and be running. What
  changed is only whether a *human* retypes those identifiers into a browser.

  Four bounded collection routes make the durable hierarchy discoverable after
  a reload with no traffic — `GET /v1/projects`,
  `GET /v1/projects/{project_id}/agents`,
  `GET /v1/agents/{agent_id}/candidates` and
  `GET /v1/candidates/{candidate_id}/evaluation-runs`. They reuse task 065's
  paging design unchanged: `id` byte-ascending, an exclusive `after` cursor,
  `limit` 1–64 defaulting to 64, `next_after` present exactly when another row
  follows, `404` for a missing parent and `200` with an empty array for an
  empty one. Each element is the entity's existing detail DTO, so a listing
  publishes nothing a by-id read does not. Cursors are identifiers rather than
  timestamps because this schema stores timestamps as `RFC3339Nano` text, which
  is not lexically ordered — a timestamp cursor would silently skip and repeat
  rows.

  The capability split is preserved: `ControlStore` gained `Projects`,
  `ProjectAgents` and `AgentCandidates`; `EvaluationStore` gained
  `CandidateEvaluationRuns`; `ControlPlane` composes both into one browsing
  surface. A list method does not move an entity between capabilities, and no
  fifth store interface was added.

  **Discovery is bounded as a workflow, not only per route.** Startup issues
  exactly one collection request — the first page of `GET /v1/projects` — and
  the same budget applies to every reconnect. No child level is fetched
  automatically, no continuation is followed automatically, and there is no
  timer, poll or prefetch anywhere in the page. A naïve hierarchy walk would be
  sixteen million rows across a quarter of a million requests while the resync
  buffer holds 64 frames, which under live traffic degenerates into a resync
  loop that gets worse the larger the database is.

  **The graph renders exactly one selected run**, because `fingerprint_id` is
  behavioral identity and carries no project, agent, candidate or run — two
  unrelated agents doing the same thing produce the same fingerprint, and
  merging them would let one run's `new_behavior`, decision and risk overwrite
  another's. An observation outside the selected run updates its card and is
  not drawn.

  Bounds are explicit and saturation is always stated, never silent: 16 active
  scope cards, 8 source nodes, 64 target nodes, 128 edges, the existing 100-row
  observation feed and 64-frame resync buffer. A saturated viewport,
  `behavior_complete: false` and a realtime queue overflow are reported as the
  three different facts they are. Animation is evidence — one pulse per
  received frame, no manufactured traffic, and a quiet agent draws a still
  graph. `prefers-reduced-motion` removes the movement and no information with
  it.

  `GET /v1/realtime` is unchanged: same route, same filters, same event kinds,
  same payload. The Live view subscribes with no filter, which the server
  already defined as unconstrained. The WebUI gained no authority —
  `webui.NewHandler()` still takes no arguments — no browser storage, no
  framework, no build step and no dependency. Every existing manual control
  remains reachable under **Manage**.

  `SchemaVersion` is 5 on both backends. The v4 → v5 migration adds three
  indexes — `platform_agents_by_project`, `platform_candidates_by_agent`,
  `platform_runs_by_candidate` — and **nothing else**: no table, no column, no
  backfill, no row written and no row changed. See
  [docs/tasks/v1.0/074-zero-input-live-behavior-webui.md](docs/tasks/v1.0/074-zero-input-live-behavior-webui.md)
  and [ADR 0041](docs/adr/0041-bounded-hierarchy-collections-and-run-scoped-live-view.md).

- **Promotion workflow** (task 066). The platform can now record that a
  candidate's evidence was gated and that the verdict accepted — or refused —
  advancement from one environment toward another. One sentence bounds the
  whole feature: **Trustvian records a promotion decision; Trustvian does not
  deploy anything.** There is no `Deployment`, `Release`, `CurrentEnvironment`
  or `DeployedCandidate`, no webhook and no pipeline trigger, and nothing in
  the platform acts on a recorded promotion. Trustvian has no observer that
  could confirm a deployment happened, so any such claim would be one it cannot
  substantiate.

  `POST /v1/promotions`, `GET /v1/promotions/{promotion_id}` and
  `GET /v1/projects/{project_id}/promotions` are the routes, and
  `trustvian promotion create|get|list` drives them over `/v1` — the noun,
  because it names a record rather than an action Trustvian performs. The
  request carries the two evaluation runs, the target environment and the three
  gate limits. It carries no source environment, which the server infers from
  the environment the two runs share, and no outcome: `accepted` and `rejected`
  are derived from the gate verdict alone, and a request attempting to set
  either is rejected. Both runs must belong to the same agent, while
  `CompareEvaluations` stays same-project.

  **Both verdicts are recorded.** A FAIL produces a stored `rejected`
  promotion, because the gate limits are caller-owned and a history of
  acceptances only would hide a caller retrying with progressively looser
  limits until one passed. A *structural* failure — unknown runs, two agents, a
  target that is not forward — is not a decision and is not recorded. Records
  are append-only: no update, no delete, at any layer.

  **The gate result is stored as historical evidence, field for field.** The
  promotion holds the exact result the decision consumed, every count, limit and
  per-check verdict flag included, restored without recomputation. A corrected
  gate may legitimately answer the same immutable evidence differently later,
  and history has to survive its own bug fixes — so a stored row that disagrees
  with today's arithmetic restores exactly as written, and only a row that
  cannot be read at all is corruption.

  **A promotion commits only against the environment state it was decided
  against.** The invariant spans three rows, so the write re-reads both
  environments inside its own transaction, compares both revisions, re-asks
  `CanPromote`, and inserts — anything moved, a rename included, yields a
  conflict and no row. PostgreSQL holds the two environment rows with
  `SELECT … FOR UPDATE` in `(project_id, ref)` byte order, so promotions in
  different projects and over disjoint pairs make independent progress; SQLite
  serializes writers database-wide instead, and nothing here claims otherwise.

  Stage skipping is allowed: `sandbox → production` is valid if the ranks are
  forward and the gate passed. There is no `approved_by`, no actor identity and
  no RBAC — the platform records what it decided on evidence, not who asked.
  `promotion_id` is the only identity: a duplicate is `409 already_exists`,
  never a silent overwrite and never an idempotent replay decided by comparing
  request bodies. `trustvian promotion create` exits `0` for a rejected
  decision; exit `1` keeps its single existing meaning of a gate FAIL from
  `eval compare`. The WebUI gains the promotion slice task 063 reserved, and
  performs no rank comparison, gate calculation or source inference in the
  browser.

  `SchemaVersion` is 4 on both backends. The v3 → v4 migration creates the
  table and its index and **writes no rows**: every completed evaluation in a v3
  database was gated by something, but nobody decided to advance any of them,
  and synthesizing promotions from old gate results would fabricate an audit
  trail of decisions that were never made. See
  [docs/tasks/v1.0/066-promotion-workflow.md](docs/tasks/v1.0/066-promotion-workflow.md)
  and [ADR 0040](docs/adr/0040-promotions-are-immutable-evidence-backed-platform-decisions.md).

- **Environment model** (task 065). A project now owns the environments its runs
  name. `POST /v1/environments` registers one, and `POST /v1/evaluation-runs`
  refuses an `environment` the project has not registered (`404`) or has
  archived (`409`) — so `"stagin"` is an error rather than a perfectly valid run
  whose comparisons describe a population of one. Existing databases keep
  working: the v2 → v3 migration backfills every distinct
  `(project, environment)` pair the stored runs reference, unranked and active,
  dropping, merging and renaming nothing.

  Identity is `(ProjectID, EnvironmentRef)` — the reference a run already
  records, with no second identifier to disagree with it — and uniqueness is per
  project, so two projects may each own a `staging`. There is no delete at any
  layer: archiving closes an environment to new work while keeping every
  historical reference resolvable, and is reversible.

  Ordering is an optional per-project integer rank and one primitive,
  `CanPromote(from, to)`, true iff both environments belong to one project, are
  active, are ranked, and the target's rank is strictly greater. Unranked is a
  real state rather than rank 0. **Ordering is not authorization** — the
  promotion workflow that decides whether a candidate *may* move is a later
  milestone, and nothing here moves one.

  A project may create at most 64 environments, enforced inside a transaction
  that serializes on the owning project row (`SELECT … FOR UPDATE` on
  PostgreSQL, a write-intent touch on SQLite) so concurrent creation cannot
  exceed it; different projects never block each other. The cap governs
  **creation, not existence**, which is what lets a migrated database hold more
  and still be read, configured and archived.
  `GET /v1/projects/{project_id}/environments` is the platform API's first
  collection route, bounded at 64 rows per page and paginating on the immutable
  `ref` rather than the mutable rank, so enumeration is exact under concurrent
  renaming and re-ranking. `trustvian env create|get|list|set|archive|activate`
  drives all of it over `/v1`, and `env list` follows every page.

  `CompareEvaluations` now requires both runs to belong to one project, checked
  before any evidence is loaded: project-scoped refs made the existing
  ref-equality check a hazard when two projects each own a `staging`.
  `SchemaVersion` is 3 on both backends. See
  [docs/tasks/v1.0/065-environment-model.md](docs/tasks/v1.0/065-environment-model.md)
  and [ADR 0039](docs/adr/0039-environments-are-project-owned-ranked-references.md).

- **PostgreSQL platform backend.** The control plane can now persist to a shared
  PostgreSQL database instead of a local SQLite file, so several processes can
  work against one set of authoritative state. **SQLite remains the default and
  `make local` is unchanged** — it needs no database, no container and no
  configuration. PostgreSQL is opt-in:

  ```bash
  TRUSTVIAN_PLATFORM_POSTGRES_DSN=postgres://… trustvian-local --backend postgres
  ```

  The DSN comes from the environment rather than a flag because a command line is
  visible through `ps`, and it never appears in a log, an error, the startup
  output, `runtime.json` or any `/v1` response. An unknown backend, or PostgreSQL
  without a DSN, fails before the listener binds; nothing ever falls back from a
  backend that was asked for explicitly.

  Backend selection exists only at the composition root. `/v1`, SSE, the CLI, the
  TUI and the WebUI cannot tell which database answered, and one logical schema
  version governs both physical schemas. Timestamps and uint64 counters are
  stored as text on both backends so a zone offset, nanosecond precision and the
  full unsigned range survive exactly — a native timestamp type would normalize
  and truncate values that `/v1` publishes.

  Multiple processes sharing one PostgreSQL database share authoritative state
  but **not** realtime notifications; each keeps its own in-process bus.
  Cross-node realtime remains a later milestone. No authentication, environment
  model, promotion workflow or event history is included. No new dependency: the
  PostgreSQL driver was already in the repository for the engine's store. See
  [docs/tasks/v1.0/064-postgresql-platform-backend.md](docs/tasks/v1.0/064-postgresql-platform-backend.md)
  and [ADR 0037](docs/adr/0037-postgresql-is-the-shared-platform-persistence-backend.md).

- **Minimal web control plane.** `make local` now prints a `Web:` URL alongside
  the API, serving a browser UI from the same loopback listener: open projects,
  agents, candidates and runs by ID, drive a run's lifecycle, watch one run live
  over SSE, and compare two runs. It is a static same-origin client of `/v1` —
  no server-side business logic, no framework, no build toolchain and no new
  dependency in any module. Server values render as text only under a strict
  deny-by-default CSP, gate verdicts come from the server, and nothing is stored
  in the browser. There is deliberately no list or search view: the control
  plane has no collection route, and adding one would freeze undesigned
  pagination semantics. See [docs/webui.md](docs/webui.md) and
  [ADR 0036](docs/adr/0036-webui-is-a-same-origin-adapter-over-v1.md).

- **Integrated local runtime.** `make local` starts the whole local platform in
  one command: file-backed SQLite, the control plane, the realtime bus and the
  `/v1` HTTP API on a loopback listener with an OS-assigned port. See
  [docs/local-development.md](docs/local-development.md) and
  [ADR 0035](docs/adr/0035-local-runtime-composes-platform-without-reversing-modules.md).

- **Automatic local endpoint discovery.** The runtime publishes
  `.trustvian/runtime.json`, and platform commands plus the TUI read it, so
  `--api-url` is no longer required for the local workflow. Explicit
  `--api-url` always wins, a discovered endpoint must be `http` on numeric
  loopback in exactly one JSON document under 4 KiB, and omitting both with no
  runtime running exits `3` rather than being reported as a usage error.
  Presence decides, not emptiness: `--api-url ""` is a usage error (`2`) and
  reads no discovery file, so an unset variable in CI can never redirect a
  command — a gate comparison least of all.

  The runtime is unauthenticated and binds loopback only — there is no flag to
  expose it. Authentication and remote access remain task 070, and no WebUI,
  PostgreSQL backend, environment model, promotion or event history is included
  here.

- **Interactive `trustvian tui`.** A run-scoped realtime terminal dashboard:
  `trustvian tui --run-id <id> [--api-url <url>]` watches one evaluation run
  live, combining authoritative HTTP reads with SSE notifications. It is
  read-only — three GET endpoints — and imports nothing from the platform
  module. See [docs/tui.md](docs/tui.md) and
  [ADR 0034](docs/adr/0034-tui-is-a-bounded-realtime-http-client.md).

  Automatic reconnect with bounded backoff, resubscribing before
  resynchronizing so nothing committed during the gap is lost. A bounded live
  observation window of 100 rows, cleared on every reconnect and labelled as
  the current stream rather than history. Terminal-control sanitization of
  every server-supplied string, so remote text cannot move the cursor, clear
  the screen or retitle the terminal.

  Exit codes: `0` normal exit, `2` usage, `3` operational. `1` is not used —
  it means a failed gate, and only for `eval compare`.

  Not included: integrated local startup (`--api-url` is required and has no
  default), event history or replay, promotion, a WebUI, and authentication.

- **Developer CLI control-plane commands.** `trustvian project`,
  `trustvian agent`, `trustvian candidate` and `trustvian eval` drive a
  local control plane over its `/v1` HTTP API, so an evaluation can be
  run from a shell script or a CI job. The CLI is an adapter: it imports
  nothing from the platform module, touches no database, and computes no
  behavioral diff, scorecard or gate verdict. See
  [docs/platform-cli.md](docs/platform-cli.md) and
  [ADR 0033](docs/adr/0033-developer-cli-is-a-thin-http-adapter.md).

  Not included, and deliberately: no integrated local server — `--api-url`
  is required and points at a control plane you are already running. No
  `watch`/realtime command, no authentication, no list or search
  commands.

- **Machine-readable CLI output.** Every control-plane command accepts
  `--json`, which writes the API's own successful response body to stdout
  and its error envelope to stderr. The CLI adds no wrapper and removes no
  field, so those fields inherit the `/v1` contract rather than defining a
  second namespace — additive server fields reach the caller unchanged.

- **CI-safe `eval compare` exit codes.** `0` gate PASS, `1` gate FAIL, `2`
  usage, `3` API or network failure. The comparison evidence is written to
  stdout on both `0` and `1`, because a gate FAIL is a result to publish
  rather than an error.

  Exit codes are **scoped by command family**, which resolves a conflict
  the roadmap had carried since the milestone was planned. `analyze`,
  `baseline` and `version` keep `0` success / `1` failure / `2` top-level
  usage exactly as released; the new families leave `1` unused, which is
  what frees it to mean gate failure on `eval compare` alone. An API
  error is never reported as a gate failure. See
  [docs/compatibility.md § CLI](docs/compatibility.md#cli).

- **OTel Collector evaluation ingest** (task 073). An optional `evaluation:`
  block makes the Collector processor post `Result.DecisionRecord()` to an
  existing evaluation run over `/v1`, so a workload instrumented with
  OpenTelemetry and nothing else can be evaluated rather than only enriched.
  The record is projected from the same `Result` that produces the
  `trustvian.*` attributes — `Analyze` still runs once per span — and the
  configured behavioral profile also selects the Engine's learning scope, so
  two candidates never train one baseline. Omitting the block changes nothing.
  See [ADR 0038](docs/adr/0038-collector-evaluation-ingest-is-an-http-adapter.md).

  **A lost response is reconciled, not assumed away.** A POST that is written
  in full, committed, and loses only its reply is indistinguishable from one
  that never arrived — so the sink does not guess. Failures that prove the
  record was not applied (a failed dial, a refused redirect, a 4xx) free the
  sequence; everything else holds it, bound to that exact record, and
  re-presents the same record at the same sequence, which the control plane's
  digest rule answers `replayed`. One synchronous attempt, no queue, no
  background worker, and no record ever sent under a sequence another record
  already claimed.

  **Learning follows confirmation, and survives a restart.**
  `Engine.Observe` runs once the control plane has accepted a record —
  never for one it declined, and never for one whose outcome is unknown.
  "Unknown" is not "committed", and with a durable `storage:` backend the
  difference outlives the process: learning applied on the chance a record
  landed is written to disk, and survives the restart that proves it never
  did. So the record in flight is itself durable (`pending_state_path`, one
  entry, written before the request leaves), and startup settles it against
  the run's own cursor: a record the run never received is discarded
  unlearned, and one it already holds is replayed at its own sequence and
  learned exactly once. A confirmed record resumes only when the run expects
  exactly the sequence after it; anything else means a second writer advanced
  the run, and startup refuses rather than stepping over evidence nothing
  local learned from. The only state a restart cannot settle — the process
  dying between confirming a record and releasing it — is reported at ERROR
  and not learned from twice, because a fingerprint that looks more familiar
  than the evidence supports is a silent weakening.

  **A store failure is part of that contract.** `Engine.Observe`'s error
  reaches the sink rather than a log line: with `evaluation:` configured, a
  store that could not persist means the run holds a record whose learning
  did not demonstrably happen — and a file-backed store updates its in-memory
  baseline before the flush that failed, so "failed" and "may have happened"
  are the same observation. The pending entry stays, the batch fails, and the
  Collector accepts no further record for that run until it is restarted.
  Without `evaluation:`, an `Observe` failure is still reported and still
  never fatal. The pending entry itself is durable in both halves — contents
  fsynced and the parent directory synced after the rename and after the
  removal — because a rename that reached only the page cache is one a host
  crash can undo.

### Added

- **Trustvian says which instrumentation layer a behavior came from** (task 083,
  partial). A model call, a named tool call, a retrieval and a plain transport
  operation were all equally anonymous in a rendered view, and two of them shared
  a category:
  `external` meant both "a model was consulted" and "a document store was queried".

  A new closed classification — `model`, `tool`, `retrieval`, `transport`, or not
  classified — now rides exactly where `trustvian.fidelity` rides: the outbound
  span attribute `trustvian.behavior.layer`, the ingest envelope's optional
  `behavior_layer`, and the realtime observation. The WebUI inspector states it in
  a sentence, with distinct wording for "not classified" and for a value the page
  does not recognize.

  **It is not behavioral identity, and that is the point.** There is deliberately
  no sixth `OperationCategory`: that field is a `StableFeatures` dimension, so a
  `model` value would have re-fingerprinted every model call a producer was already
  emitting and discarded those baselines — to improve a label. The classification
  costs no migration, and a test asserts the fingerprint, the whole
  `StableFeatures` tuple and the learning path are unchanged across every layer
  value.

  **A layer is claimed exactly when fidelity is `semantic`.** One gate, so two
  indicators derived from one table cannot disagree about one span. Absent means
  *not classified* rather than `transport`, which is the one place this differs from
  fidelity: silence classified nothing, while "nothing proved a semantic name"
  really is transport.

  Degradation is unchanged: a producer emitting no convention gets byte-identical
  behavioral results and is classified `transport`. Schema version stays 5.

### Changed

- **What `max-added-behaviors` counts is now stated** (task 083). It counts
  behavioral *identities*, and a producer emitting agent-oriented telemetry
  observes one act at two layers — the tool call and the request the tool made —
  so one act can consume two of the budget:

  ```text
  tool  · export_customer                 one identity
  http  · POST → export.localhost      another identity
  ```

  That was already true and was written down nowhere. It is now in the flag's own
  help, `docs/platform-cli.md` and `docs/OPENTELEMETRY.md`. At a limit of `0` it is
  invisible; it matters the first time a budget is nonzero.

  **The correction is deferred, and the task stays open.** Folding the two into one
  counted change requires knowing the request is the tool call's child, and no
  parent identity reaches the evidence boundary — verified by a test that fails
  when it changes, not assumed. Identity was deliberately *not* folded to work
  around it: folding drops the destination from behavioral identity, so a tool that
  started posting to another host would stop changing the behavioral surface, which
  trades a counting annoyance for a detection hole. Missing correlation falls back
  to today's count rather than to a guess; nothing is inferred from timestamps,
  adjacency or similar names, and no observation is dropped to make a count
  smaller.
  [ADR 0047](docs/adr/0047-behavioral-identity-is-per-observation-counting-is-a-policy.md)
  records where the fix belongs and what it must not do.

### Fixed

- **The fidelity indicator never left the Collector** (task 075). The mapping
  computed it, the outbound span carried it, and the control plane's ingest
  envelope accepted it — but the *processor's* envelope carried `version`,
  `sequence`, `behavioral_profile` and `record` and nothing else, so the value
  was dropped on the way out. `DecisionRecord` has no attributes, so nothing
  downstream could recover it either: the live view was told `transport` for
  every record that arrived this way, including the ones whose operation name
  came from a GenAI or OpenInference convention. Fidelity reached a live view
  only for a producer that POSTed to `/v1` and filled the field itself.

  `fidelity` now rides beside the record in the processor's ingest envelope,
  exactly as `behavioral_profile` already does and for the identical stated
  reason — it is metadata about how the record was *produced*, which the engine
  has no opinion about. The processor reads it at the ingest call site from the
  same `Result` that produced the span attribute, so the two reports of one
  mapping cannot disagree.

  No core change, no `DecisionRecord` change and no schema change: per-behavior
  persistence is still deferred (task 081), and a comparison delta still carries
  no fidelity. The value is kept in memory rather than in the sink's pending
  entry, because a recovered record is only ever re-presented to prove which
  record occupies a taken sequence — the control plane recognizes that by the
  record's own digest and replays without reading fidelity.

  Asserted end to end with nothing mocked between a real span and the SSE frame
  a browser reads: a GenAI tool span arrives as `semantic` and a plain HTTP span
  as `transport`, in the same run, on the same stream.

### Security

- **Per-actor fingerprint state is now bounded.** `Baseline.Fingerprints`
  had no upper limit, and `Fingerprint.ID` is derived from `Event` fields
  the caller supplies — so one actor emitting a distinct operation name
  per call could grow its baseline, and its PostgreSQL row, without
  limit. `internal/baseline` now caps a baseline at 512 fingerprint
  identities and refuses admission of new ones beyond it.

  The bound refuses rather than evicts. Nothing already learned is
  removed to make room, because an absent fingerprint scores as
  maximally novel with zero confidence and therefore contributes nothing
  to trust — evicting learned entries under pressure would let a flood of
  manufactured fingerprints suppress detection for an actor rather than
  merely cost memory. See
  [ADR 0019](docs/adr/0019-bounded-fingerprint-admission.md).

  A baseline already holding more than 512 identities, learned before
  this bound existed, keeps every one of them and keeps updating them; it
  admits nothing new and is never truncated. Upgrading from `v0.9`
  requires no migration and loses no learned state.

  Behavioral change, confined to actors past the cap: a new fingerprint
  observed by an actor already holding 512 is analyzed and decided
  normally but is not learned, so it continues to score as unknown.
  Known fingerprints keep learning regardless of how full the baseline
  is.

  No public API, configuration, CLI, Collector, or storage schema change.

### Added

- **Bounded realtime infrastructure.** `platform.InMemoryRealtimeBus`
  publishes notifications about committed control-plane state, and
  `GET /v1/realtime` streams them over Server-Sent Events with project, agent
  and run filtering. A future CLI, TUI or WebUI can watch an evaluation live
  without polling the database.

  **Durable state stays authoritative; realtime is notification.** Publication
  happens only after a mutation commits, so a subscriber cannot observe state
  that never existed — and a delivery failure never changes a committed
  operation's outcome, because reporting one as failed would make a client
  retry a write that already landed. Failed, conflicted and *replayed*
  operations publish nothing: a network retry that produced no second durable
  record produces no second live observation.

  **Bounded at every dimension.** Each subscriber owns a fixed-capacity queue,
  the subscriber count is capped, and one that falls behind is disconnected
  rather than blocking the publisher or silently losing events. Filtering
  happens before enqueue, so a busy run cannot overflow a subscriber watching
  a quiet one. Worst-case memory is subscribers × queue, with no history term.

  **No replay.** The bus retains nothing after delivery, `Last-Event-ID` is
  ignored, and no SSE `id:` is emitted. Every connection receives
  `stream_ready` with `resync_required` — sent *after* the subscription is
  registered, so nothing occurring during the resync is lost.

  Six event kinds, each matching a mutation that committed: evaluation
  created, started, completed, failed, cancelled, and one observation per
  applied record. No `policy_violation`, `baseline_update` or `gate_update` —
  those name semantics the platform cannot currently prove. An observation is
  a bounded projection carrying no attributes, tool arguments, prompts,
  completions, contributors or policy reason, so task 050's privacy boundary
  holds; a test drives a real engine with a distinctive attribute and asserts
  it never reaches the wire.

  Realtime is optional: a control plane built without a publisher behaves
  exactly as before, and a handler without a subscriber reports
  `realtime_unavailable` while every other route keeps working. No schema
  change (still version 2), no broker, no polling, no WebSocket, no CORS, no
  authentication, no listener, no event-history table, and no core runtime
  change. See [ADR
  0032](docs/adr/0032-realtime-is-bounded-ephemeral-not-authoritative.md).

- **Local control-plane API and sequenced ingest.** `platform.ControlPlane`
  is the authoritative service layer — it creates and reads the
  project/agent/candidate/run hierarchy, drives a run's lifecycle, ingests
  evidence, reports progress, and derives a comparison. `platform/httpapi`
  serves a local `/v1` HTTP surface over it and computes nothing: a
  source-scanning test fails if the adapter ever references the comparison,
  scorecard or gate constructors, or a store type.

  Ingest consumes the public `trustvian.DecisionRecord` and nothing else. No
  route analyzes a raw `Event` — the producer's engine already made the
  decision, and a second engine host would train a second baseline. The
  behavioral profile travels beside the record rather than inside it, because
  `DecisionRecord` carries no learning scope, and it must match the run's.

  **Retries are safe without retaining history.** Task 053 made a duplicate
  record count twice on purpose, so an ordinary HTTP retry would corrupt the
  evidence. Each accepted record carries an explicit monotonic per-run
  sequence; durable state is that sequence plus the digest of the last
  accepted record — two values, whatever the run ingested. The expected
  sequence applies, an identical retry of the previous one replays without
  re-aggregating, and a divergent retry, a stale number or a gap all fail
  closed. Sequences travel as canonical decimal strings so a browser client
  cannot round them.

  Evidence and cursor commit in one transaction, so neither can advance
  without the other. A restarted process resumes a running evaluation from
  stored evidence through a package-private snapshot-to-collector
  restoration — no public constructor was added, because one would let any
  caller forge collector state.

  Behavioral saturation degrades rather than fails: the 513th distinct
  behavior is still applied to the aggregate, the snapshot reports
  `behavior_complete = false`, and a later comparison refuses the incomplete
  evidence instead of manufacturing a scorecard from it.

  **SQLite schema version 2**, with a real forward migration from version 1
  that preserves every row. A migrated run with `N` records starts at
  sequence `N+1`; no digest is invented for a record this code never saw, so a
  retry of `N` conflicts rather than guessing.

  Request bodies are bounded at 256 KiB before decoding; internal errors are
  sanitized, with storage corruption reported as `500` rather than a
  client-fixable `400`. Gate limits are required inputs with zero distinct
  from omitted, because zero is a strict limit. Domain types still carry no
  JSON tags — the DTOs own the wire.

  No realtime, no listener (composing one is a later task and must default to
  loopback), no CORS, no authentication or access-control model, no promotion,
  no raw event history, no new binary, and no core runtime change. See [ADR
  0031](docs/adr/0031-control-plane-owns-ingest-and-http-is-an-adapter.md).

- **Local platform persistence: evaluation state survives a restart.**
  `platform.OpenSQLiteStore` provides a local SQLite adapter behind two narrow
  capabilities — `ControlStore` for projects, agents and candidates, and
  `EvaluationStore` for evaluation runs and their evidence. There is no
  generic `Database` interface; persistence is expressed in domain terms.

  It persists what cannot be rebuilt — the entities, the
  `EvaluationAggregate`, and the `BehaviorSnapshot` with its bounded entries.
  `BehaviorDiff`, `EvaluationScorecard` and `EvaluationGateResult` are
  deterministic functions of those and are recomputed on demand, so there is
  one source of truth rather than a stored copy that can disagree.

  Fail-closed throughout. Creates never upsert, so the same `CandidateID` with
  a different artifact digest cannot rewrite what a finished run was evaluated
  against. Identity stays caller-owned — the store generates no ID. Runs
  update by compare-and-swap and are rebuilt by replaying their domain
  transitions, so a corrupt chronology fails the same invariant a live value
  would. Aggregate and snapshot commit in one transaction; evidence never
  moves backwards; saturation is sticky, so an incomplete snapshot stays
  incomplete and is still refused by `CompareBehaviorSnapshots`.

  Restored evidence is validated before its private bound marker is set, and
  corrupt rows return an error rather than a repaired value. Counters are
  stored as canonical base-10 text, round-tripping the whole `0 … MaxUint64`
  domain — SQLite `INTEGER` is signed 64-bit, and narrowing would corrupt
  large values silently. Timestamps keep nanosecond precision and their
  numeric zone offset rather than being normalized to UTC.

  Unknown schema versions fail closed, and so do recognized tables with no
  version metadata: adopting those as fresh would silently take ownership of
  data this code has never seen. Schema creation and version stamping commit
  together.

  Raw event history is deliberately absent — no table grows per event — and
  core baseline state is not duplicated; the engine's own stores keep owning
  it. No API, transport, realtime, promotion workflow, or PostgreSQL platform
  backend, and no core runtime change. Adds a pure-Go SQLite driver
  (`modernc.org/sqlite`) to the platform module, so no CGO requirement is
  introduced. See [ADR
  0030](docs/adr/0030-local-persistence-stores-authoritative-bounded-state.md).

- **Deterministic hard gates: a scorecard plus explicit limits becomes a
  verdict.** `platform.EvaluateEvaluationGate` pairs an `EvaluationScorecard`
  with a caller-owned `EvaluationGatePolicy` and returns a fixed-shape
  `EvaluationGateResult` carrying five checks and a PASS/FAIL `GateVerdict`.

  The five checks, all evaluated on every call: reference evidence present,
  candidate evidence present, added behaviors within limit, candidate block
  decisions within limit, candidate critical-risk observations within limit.
  PASS requires all five, and there is no short-circuit — a FAIL reports
  everything measured.

  Every comparison is an integer. No mean, rate, delta, or presence ratio
  takes part in a verdict: floating-point sums are not guaranteed
  bit-identical under record reordering, and an average is precisely how
  strength in one dimension offsets a condition in another.

  Fail-closed throughout. An unbound policy or unbound scorecard is an error.
  A *valid* evaluation that observed nothing is not — it fails the two
  non-configurable sufficiency gates, which exist because a candidate that
  ran zero records satisfies every maximum. `0` is a strict limit rather than
  "unset", so a private marker separates the strictest policy from an absent
  one.

  Names stay factual: a block decision is the policy engine doing what it was
  configured to do, not a violation; a critical-risk observation is a risk
  classification, not an incident. Gates the evidence cannot support —
  critical policy violations, sensitive-resource access, approval compliance,
  per-rule or delegation compliance — remain absent rather than reported as
  zero, and `docs/ROADMAP.md` now separates implemented evidence-backed gates
  from those deferred until an explicit evidence contract exists.

  A verdict performs nothing. PASS means only that the configured gates
  passed; promotion is a later task. No change to `EvaluationScorecard`, no
  core runtime change, no persistence, and no transport. See [ADR
  0029](docs/adr/0029-hard-gates-use-explicit-integer-evidence.md).

- **Evaluation scorecards: one fixed-shape comparison of two evaluations.**
  `platform.NewEvaluationScorecard` composes the evidence tasks 053 and 054
  already produce:

  ```text
  reference EvaluationAggregate ───────┐
  BehaviorDiff(reference → candidate) ─┼──▶ EvaluationScorecard
  candidate EvaluationAggregate ───────┘
  ```

  It reports how the decision, risk, approval and policy-selection
  distributions moved, how the five numeric signals moved, and how much
  behavioral presence overlapped — counts, rates and signed deltas.

  **No third reducer.** Records were consumed once, by two reducers designed
  against the same stream; re-reading them would be a third ingestion path
  with a third chance to disagree. Both aggregates are required, because
  comparison is the point, and the diff cannot be derived from them nor they
  from it.

  **Evidence must describe one comparison.** Each aggregate is matched
  against its own side of the diff, all three must agree on the environment,
  and both observation counts must match — catching the case where one
  reducer saw records the other did not. There is no partial card.

  **Fixed-shape and O(1).** Construction costs 214 ns and zero allocations
  for a 1,024-delta comparison over 2,048 records — identical to an empty
  one, because only summary accessors are read. The diff's deltas are not
  copied; a caller wanting per-behavior rows reads the `BehaviorDiff` it
  already holds.

  **Still evidence, not judgement.** No overall score, weight, threshold, or
  verdict: a composite would let one dimension offset another, which the
  roadmap forbids, and would become the number read instead of the gate.
  Empty denominators are undefined rather than zero — two evaluations that
  observed nothing are unmeasured, not identical.

  **Unsupported semantics are absent, not zero.** Critical policy violations,
  blocked or unapproved sensitive actions, per-rule compliance and delegation
  stability have no field, because no current evidence can express them.
  Reporting `0` would be a false security claim. Task 056 deliberately did not
  infer or define those semantics: gates depending on policy severity,
  resource sensitivity, authorization semantics, or per-event correlation the
  aggregate does not retain remain deferred until explicit evidence contracts
  exist. See [ADR
  0028](docs/adr/0028-scorecards-are-fixed-shape-comparative-evidence.md).

- **Behavioral diff: which behavioral shapes changed between two
  evaluations.** `BehaviorCollector` reduces a `DecisionRecord` stream into a
  bounded set of behaviors; `BehaviorSnapshot` is the detached, deterministic
  result; `CompareBehaviorSnapshots` reports each behavior as `Added`,
  `Removed` or `Shared` with counts and normalized frequencies.

  ```text
  DecisionRecord ──▶ BehaviorCollector ──▶ BehaviorSnapshot ──▶ BehaviorDiff
  ```

  The comparison key is `FingerprintID` and nothing else — not actor, session,
  candidate, run, profile, commit, or digest. A diff keyed by any of those
  would report change every time a candidate was rebuilt.

  **`EvaluationAggregate` is unchanged.** [ADR
  0026](docs/adr/0026-evaluation-aggregation-is-bounded-evidence.md) made it
  O(1) and left this task to bring its own bounded contract; this is that
  contract, as a separate reducer. An evaluation that will never be compared
  pays nothing for behavioral bookkeeping.

  **Bounded, and loud when it cannot answer.** At most 512 distinct behaviors
  per collector and 1,024 deltas per diff; every retained string capped at 256
  bytes and rejected rather than truncated, because two behaviors must not
  merge because a name was shortened. A 513th distinct behavior marks the
  collector **permanently incomplete**, and an incomplete snapshot **cannot be
  compared** — silently comparing the first 512 would produce a confident,
  specific, wrong "new behavior" count.

  Fingerprint identity and behavior descriptor must agree **one-to-one, both
  ways**. One fingerprint with two shapes would merge two behaviors; one shape
  under two fingerprints would be reported as removed-and-added, claiming
  behavior changed when only its encoding did. Both fail closed, in the
  collector and again during comparison. The platform never recomputes the
  core's hash to check this, so the fingerprint algorithm stays free to
  change.

  Rates are derived from integer counts at comparison time, so equal counts
  give identical rates whatever order records arrived in. Environments must
  match; candidate, run and profile references may differ, since [task
  051](docs/tasks/v1.0/051-behavioral-profile-learning-scope-isolation.md)
  kept learning scope out of behavioral identity.

  **Evidence, not judgement.** No drift score, severity, threshold, pass, or
  promotability. `AddedCount` is the factual basis a later gate may build on.
  See [ADR
  0027](docs/adr/0027-behavioral-diff-compares-bounded-snapshots.md).

- **Evaluation result aggregation: the platform now consumes engine
  evidence.** `platform.EvaluationAggregate` folds
  `trustvian.DecisionRecord` values into a bounded, fixed-shape summary of
  what one evaluation observed:

  ```text
  Engine ──▶ DecisionRecord ──▶ EvaluationAggregate
  ```

  It counts observations, decisions by category, risk levels, approval
  evidence, and policy-selection shape; summarizes the five numeric signals as
  `{Count, Sum, Min, Max}` with an explicitly-absent mean when empty; and
  bounds the evidence in event time. `AddRecord` returns a new aggregate, so
  the receiver is never mutated and a rejected record leaves it identical.

  **This is the first real platform → core dependency, and it needed no core
  change** — which is the claim [task
  050](docs/tasks/v1.0/050-public-serializable-decision-record.md) built
  `DecisionRecord` to make good on. The platform imports the public API only;
  `policy.Decision` and `trust.RiskLevel` stay internal, so their small closed
  sets of string values are re-declared rather than the core's surface being
  widened.

  **Bounded by construction.** O(1) memory in the number of records: no slice,
  no map, no retained record, and no deduplication set. Retaining records
  would make it an accidental event archive, which is a separate capability
  with its own boundary. One consequence is stated rather than left implicit:
  duplicates count twice, because idempotency needs a retention window only an
  ingest boundary can define.

  **Evidence, not judgement.** No score, grade, pass, promotability,
  critical-violation count, or new-behavior count — each needs context the
  aggregate does not hold, and would become the field people read instead of
  the gate. See [ADR
  0026](docs/adr/0026-evaluation-aggregation-is-bounded-evidence.md).

  Malformed input fails closed: every consumed field is validated before any
  state changes, and nothing is clamped or coerced. Cross-environment records
  are refused. `MatchedDefault` and `PolicyRule` are validated as one pairing,
  so a hand-built record cannot claim a rule matched while naming none.
  `NewEvaluationAggregate` returns an error rather than binding evidence to an
  invalid or zero-value run, and a zero-value aggregate accepts no record at
  all — both zero values are writable from any package, since unexported
  fields prevent mutation rather than construction. Rejection paths echo only
  a bounded preview of untrusted strings, so a malformed record cannot turn an
  error into an amplification primitive. `AddRecord` costs 86 ns and zero
  allocations.

- **`platform/`: the control-plane domain, as a fourth Go module.** The first
  platform-layer runtime code — `Project`, `Agent`, `Candidate`,
  `EvaluationRun`, and opaque `EnvironmentRef` / `BehavioralProfileRef`
  references — establishing what an evaluation is, what it belongs to, and
  what may change once one has begun.

  ```text
  Project
    └─ Agent
        └─ Candidate
            └─ EvaluationRun ──▶ EnvironmentRef
                            └──▶ BehavioralProfileRef
  ```

  A separate module at `trustvian-platform`, deliberately **not** under
  `github.com/trustvian/trustvian`: Go's `internal/` rule turns on
  import-path ancestry rather than module membership, so a repository-prefixed
  path would be allowed to import the engine's internal packages, and this one
  is a compile error instead. The domain itself needed nothing from the core,
  and adding a dependency to demonstrate the relationship would have been the
  speculative coupling the boundary exists to prevent — so the module began
  with none. Aggregation introduced one, on its own merits, in the entry
  above.

  Identifiers are typed, opaque, and caller-owned: the domain generates none,
  reads no clock, and requires no UUID format. Candidate metadata is a fixed
  set of optional descriptive fields rather than a map — bounded by
  construction, with nothing to alias — and never becomes behavioral identity.
  A run's `Status` records that an execution finished, never that a candidate
  passed; gates and promotion are separate later concerns. See [ADR
  0025](docs/adr/0025-platform-domain-values-with-caller-owned-identity.md).

  **No engine change.** `scripts/check-platform-boundary.sh` enforces both
  directions of [ADR 0022](docs/adr/0022-core-platform-boundary.md)'s
  boundary in CI: no core `internal/*` import in the platform, no platform
  package in the core's build graph, and no platform identifier declared in
  core runtime code.

  Nothing here is usable yet: no persistence, transport, aggregation,
  behavioral diff, scorecard, gate, or promotion. Those are later tasks, and
  each would have been easier to add now than to remove later.

- **Learning scopes: independent behavioral history under one actor
  identity.** `trustvian.WithLearningScope("...")` partitions an Engine's
  learned state. Two engines over one store with different scopes never
  share a `Baseline`, even for the same actor in the same environment —
  each accumulates its own history and each starts cold. Two engines with
  the same scope share one profile; the scope is the identity, not the
  Engine instance.

  `baseline.Key` gains a `Scope` field, so learned-state identity is
  `{Scope, ActorID, Environment}`. Everything downstream follows from that
  one change: `InMemory` shards by the whole key, `FileStore` persists the
  `Key` the `Baseline` already carries, PostgreSQL gets one composite
  primary key, and `Freezer` becomes scope-aware without learning that
  scopes exist.

  **Scope is not behavioral identity.** The same event analyzed under two
  scopes produces the same `StableFeatures` and the same `Fingerprint.ID`;
  only the learned evidence it is compared against differs. Scope is absent
  from the fingerprint hash, from `StableFeatures`, and from
  `DecisionRecord`.

  **Scope cannot come from an event.** It is Engine configuration, fixed at
  construction, and is never derived from `SessionID`, `TraceID`, or
  `Attributes` — so an event producer cannot choose which learned profile it
  trains. This is a trust boundary, not tidiness; see
  [SECURITY.md](docs/SECURITY.md#learning-scope-selection).

  The default scope is `""`. `NewEngine()` without the option behaves
  exactly as before and finds exactly the state it found before.

  The 512-fingerprint bound ([ADR
  0019](docs/adr/0019-bounded-fingerprint-admission.md)) is unchanged and
  now applies per scoped baseline: filling one scope leaves every other with
  its own independent capacity. The constant has never bounded the *number*
  of baselines and does not now.

  See [ADR
  0024](docs/adr/0024-learning-scope-is-a-baseline-key-dimension.md).

- **`DecisionRecord`: a serializable public projection of one analysis.**
  `Result` is readable from outside the module, but four of its fields have
  types from `internal/`, so a consumer could inspect a result without being
  able to declare, store, or serialize one. `result.DecisionRecord()` returns
  a public type with explicit JSON field names carrying the evidence behind a
  decision: behavioral shape, fingerprint, anomaly score and contributors,
  trust and risk, the policy rule and reason, and correlation identifiers.

  It carries no raw event payload — `Event.Attributes`, tool arguments,
  prompts, and completions have no field and cannot reach its JSON — and no
  consumer-side identifiers. Both are asserted by test. The projection is
  pure: no I/O, no clock, no scoring, and its slices are copied rather than
  aliased, so a record shares no memory with the `Result` it came from.

  `StableFeatures` gained JSON tags so the record serializes consistently.
  The type is unreleased, so no published representation changed.

- **Every `Engine` option is now usable from outside the module.** Two
  were exported but uncallable by third-party code, because their
  parameter types live under `internal/` and no public path produced
  one:

  - `WithTrustConfig` gains `config.TrustConfig` and
    `config.CompileTrust`, following the pattern `CompilePolicy`,
    `CompileStorage`, and `CompileAnomaly` already established. Risk
    thresholds are pointers, so an unset one keeps its default rather
    than silently becoming zero — a zero threshold would classify every
    result as Critical.
  - `WithContextRisk` now takes `func(trustvian.StableFeatures) float64`.
    `StableFeatures` is a new public type carrying the six stable
    dimensions of an event and nothing per-occurrence. See
    [ADR 0021](docs/adr/0021-public-stable-features-boundary.md).

  `examples/configured-engine` configures all five options from a
  separate Go module, which is what proves the path works.

  **Source-breaking for in-module callers of `WithContextRisk`**, whose
  signature changed. Done deliberately before the `v1` freeze, when it
  costs nothing, rather than after it, when it would cost a major
  version. No in-repository caller used the option.

### Fixed

- **A pathological `WithContextRisk` callback could make a successful
  analysis unserializable.** `trust.Compute` clamps its inputs with
  `min`/`max`, which propagate `NaN`, so a callback returning `NaN` put one
  into `Trust.ContextRisk` and `Trust.Score` — and `encoding/json` refuses
  non-finite floats. `Analyze` returned success and the resulting record
  could not be marshalled.

  A non-finite input is now treated as invalid rather than as a position on
  the scale, and resolves to whichever end trusts least: context risk and the
  anomaly inputs to `1`, identity confidence to `0`. Both directions fail
  closed, and `-Inf` no longer reads as "no risk". Analysis still succeeds,
  because a detector that stops deciding when a caller's callback misbehaves
  is worse than one that assumes the worst. Ordinary out-of-range finite
  values keep their documented clamp behavior, and the trust formula is
  unchanged.

- **An unencodable `Event.Timestamp` could make a successful analysis
  unserializable.** `Event.Validate()` rejected a zero timestamp but accepted
  every other `time.Time` — and not every `time.Time` is one
  `time.Time.MarshalJSON` will encode. A year outside `[0,9999]` or a zone
  offset of 24 hours or more is constructible in Go and refused by RFC 3339,
  so such an event analyzed successfully and produced a `DecisionRecord`
  `json.Marshal` would not accept.

  `Validate()` now rejects both cases with a new `event.ErrInvalidTimestamp`,
  wrapped by `Analyze` through the existing `trustvian: invalid event:` path.
  The rule is exactly the standard library's, asserted by a test that derives
  its expectation from `MarshalJSON` rather than restating it, so the check
  can neither drift permissive nor start refusing timestamps Go accepts.

  A missing timestamp still returns `ErrMissingTimestamp`; the zero time is
  encodable, so only the earlier check distinguishes absent from unencodable.
  Rejection is at the input, where the caller still holds the event:
  `DecisionRecord()` does not sanitize, because a security record with a
  silently rewritten timestamp is worse than a refused event.

  Behavioral change for callers submitting such timestamps — previously
  accepted, now an error. No in-repository caller produces one, and no
  encodable timestamp's treatment changed.

- **`Engine.Observe` reported learning that did not happen.**
  `Baseline.Observe` refuses an unknown fingerprint once a baseline holds
  512 identities, but the store call still succeeded, so `Observe` returned
  `learned == true` regardless. Task 049 recorded this as an open conflict:
  a long run against a wide-surface actor could quietly stop learning while
  reporting that it had.

  The admission outcome now propagates from `Baseline.Observe` through
  `Store.Observe` to `Engine.Observe`. `learned` is false for an ineligible
  decision, false when an unknown fingerprint is refused at capacity, false
  when the store is frozen, and true when a known fingerprint keeps learning
  at capacity.

  **Admission policy is unchanged** — ADR 0019's refuse-never-evict stands,
  capacity refusal is still not an error, and analysis is unaffected. Only
  the reporting changed.

### Changed

- **Persisted state carries learning scopes, and both backends versioned.**
  The FileStore snapshot moves to `version: 2` and PostgreSQL to
  `SchemaVersion = 2`. Each reads its predecessor and upgrades in place:
  every pre-scope baseline lands in the default scope with its learned state
  unchanged, because a `Key` with no `Scope` field deserializes to exactly
  that. Nothing is invented, and no learned state moves between scopes.

  PostgreSQL's upgrade adds `scope text NOT NULL`, makes the primary key
  `(scope, actor_id, environment)`, and restamps each row's derived
  `schema_version` — all inside the existing migration transaction and
  advisory lock, so it is atomic and safe against a racing process. Scope is
  part of the primary key deliberately: storing it only in the jsonb would
  let a second scope's insert collide with the first's row.

  **Downgrade is not supported, deliberately.** Adding a JSON field is
  normally additive, but `Scope` changes what a record *identifies* — one
  version-2 snapshot can hold two baselines a version-1 reader sees as the
  same key, and it would keep whichever it loaded last. A file an old binary
  refuses is a recoverable operational problem; two learned profiles
  silently merged is corrupted state nothing detects. Recovery from a
  downgrade is restoring a pre-upgrade backup.

- **Source-breaking for in-module callers of the `store.Store` port.**
  `Observe` returns `(baseline.Baseline, bool, error)`. `Store` is not a
  public extension point — see below — so no external consumer is affected.

- Documented explicitly that **custom `Store` implementations are not a
  v1 extension point**. `store.Store` references internal types and
  stays internal; persistence is selected through
  `config.StorageConfig` from the shipped backends. Also documented that
  a compiled store is owned by the caller — an `Engine` never closes one
  it was handed, and there is no `Engine.Close`.

- `Engine`'s documentation claimed full configuration required code
  inside the module. That stopped being true when `CompilePolicy`,
  `CompileAnomaly`, and `CompileStorage` shipped; it is now accurate.
  A `config.StorageConfig` field also still described PostgreSQL as
  recognized but unimplemented, two years after it shipped.

- **A compatibility contract covering every observable surface**, not
  just the Go API: [docs/compatibility.md](docs/compatibility.md). The
  `v0.1.0` promise in this file covers `event.Event`, `Result`, and
  `Engine`; configuration schemas, CLI flags and exit codes, environment
  variables, Collector configuration, storage formats, metric names and
  labels, health endpoints, the webhook envelope, container interface,
  and release artifacts were all outside it. Each is now classified,
  with the version bump a breaking change costs.

  It also states how behavioral change is treated, which type signatures
  cannot express: enabling a signal by default or altering which
  decisions train the baseline is breaking, whereas correcting a signal
  against its documented formula is a fix. Deprecation is
  version-based — one subsequent minor, minimum — and the security
  exception is deliberately narrow. See
  [ADR 0020](docs/adr/0020-v1-compatibility-contract.md).

  Documentation only: no API, configuration, storage, or behavior
  change.

### Fixed

- Documentation stated a per-actor memory bound the implementation did
  not enforce: `docs/observability.md`'s growth table extrapolated from a
  120-fingerprint measurement as though fingerprint count were capped.
  `observability.md`, `SECURITY.md`, `storage-guide.md`, and
  `PERFORMANCE.md` now distinguish the structural cap from the one size
  figure actually measured, and separate per-actor growth (bounded) from
  actor-count growth (deliberately not).

## v0.9.0 — Operational Readiness

Makes Trustvian *operable*, where `v0.8` made it deployable. It adds no
behavioral capability: every quality gate now runs on a machine rather than
by discipline, releases produce verifiable artifacts, the runtime reports
its own health and shuts down cleanly, it emits bounded operational
metrics, and the learned behavioral state it protects can be backed up,
restored, and carried across an upgrade — each of those proven by a test
rather than a procedure.

Seven vertical slices (039–045), all additive. Existing deployments need no
configuration change: the health endpoints are opt-in, the metrics require
no wiring, and `InMemory` remains the default store.

One change is visible to Go consumers: the module path is now lowercase
(`github.com/trustvian/trustvian`) following the organization rename. See
[Changed](#changed) below for what that means for an existing import.

### Added

- **CI & quality gate automation**
  ([task 039](docs/archive/tasks/v0.9/039-ci-quality-gate-automation.md), first `v0.9`
  slice) — the checks this project has run by hand since `v0.1` now run
  automatically, across every module and boundary a release depends on.

  The previous workflow was the stock GitHub Go template: root module
  only, `go build` plus `go test`, and triggered solely on pull requests
  targeting `main`. Since work lands on `develop` and reaches `main` by
  release-time pull request, CI effectively first reported at the moment of
  release — and never covered `gofmt`, `go vet`, `-race`, the processor
  module, the examples module, PostgreSQL persistence, or the reference
  deployment at all.

  `.github/workflows/ci.yml` now runs on push and pull request for both
  `main` and `develop`: format, vet, build, test, and race across the root
  module; the processor and examples modules built and tested with
  `GOWORK=off`, so a Go workspace cannot mask a broken module boundary; a
  check that `examples/` imports no `internal/*`; PostgreSQL integration
  against a service container using the existing
  `TRUSTVIAN_TEST_POSTGRES_DSN` mechanism with no test modified; and
  validation of the reference deployment's Compose configuration.

  `.github/workflows/nightly.yml` carries the expensive tiers — the full
  PostgreSQL stress suite and the deployment's end-to-end smoke test — on a
  schedule and on demand. The split is by cost, not importance: correctness
  gates belong on pull requests, while contention benchmarks are more
  useful as a trend than as a tax on every push.

  Both workflows are read-only (`permissions: contents: read`), require no
  secrets, and publish nothing, so pull requests from forks run the full
  gate set with nothing to leak. Release and container publishing are
  deliberately later slices.

- **Release artifacts and module consistency**
  ([task 040](docs/archive/tasks/v0.9/040-release-artifacts-and-module-consistency.md),
  second `v0.9` slice) — releases through `v0.8.0` were produced entirely
  by hand, and no release has ever carried a binary. Trustvian now has a
  tag-triggered release pipeline and a documented module publication model.

  **`trustvian version`** reports the module version, VCS revision, commit
  time, and whether the build was made from a modified tree — read from
  Go's own build information, so there is no `-ldflags` contract to honor
  and no package-level version variable.

  **Release binaries.** `scripts/release-build.sh` cross-compiles the CLI
  for `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, and
  `windows/amd64`, archives each with `LICENSE` and `README.md`, and emits
  a verified SHA-256 `checksums.txt`. Built with `CGO_ENABLED=0` and
  `-trimpath`. `make release-dry-run` runs the whole matrix locally with no
  tag, credentials, or upload, and CI runs it on every push — so a broken
  target surfaces on the pull request rather than after a tag is public.

  **`.github/workflows/release.yml`** triggers on a pushed `v*` tag,
  rejects non-SemVer tags, verifies the checkout is the tagged commit,
  re-runs the full quality gates against that source, builds the matrix,
  and opens a **draft** GitHub Release with the artifacts attached for a
  maintainer to review and publish. It takes `contents: write` and nothing
  else; `ci.yml` and `nightly.yml` remain read-only.

  **Module publication model, written down and enforced.** The root module
  is the published one. The `processor` and `examples` modules declare
  non-resolvable paths (`trustvian-processor`, `trustvian-examples`) and
  have never been tagged — they are repository-internal by construction,
  and their `replace ... => ../` directives are correct for that, not a
  release blocker awaiting removal. `scripts/check-modules.sh` enforces the
  invariants this depends on — the published module declares no `replace`,
  every declared root version exists as a tag, and a module cannot be
  promoted to a resolvable path while still carrying a local replace — and
  runs in CI. See [`docs/release-guide.md`](docs/release-guide.md).

  `processor/go.mod`'s root requirement moved from `v0.5.0` to `v0.8.0`,
  the release that first contained the `config.StorageConfig` API it uses.
  This changes no resolution — the `replace` still supplies the code — but
  the declared floor is now truthful, and it is verified rather than
  assumed: with the replace removed, the processor builds against the
  published `v0.8.0` from the module proxy.

  The consistency check establishes that a declared version is real from
  either a local git tag or the module proxy, rather than requiring a local
  tag. Requiring one conflated *the version existing* with *the checkout
  having fetched tags*, which made the check fail on shallow CI checkouts
  while passing on developer machines. A checkout that can consult neither
  source now reports that explicitly instead of passing or blaming the
  version.

- **Runtime liveness and readiness endpoints, and bounded graceful
  shutdown** for the Collector runtime. Opt-in via a `health:` block on the
  processor's configuration; omitting it leaves existing Collector configs
  behaving exactly as before.

  ```yaml
  processors:
    trustvian:
      health:
        endpoint: 0.0.0.0:13133      # default
        readiness_timeout: 2s        # default
  ```

  `GET /livez` reports whether the runtime is functioning. It deliberately
  does **not** consult the database: restarting a process because its
  dependency is unreachable produces a restart storm that cannot fix
  anything. `GET /readyz` reports whether this instance can safely process
  work, and does consult the configured store — so PostgreSQL configured and
  unusable returns 503 rather than silently falling back to non-durable
  storage. A store with no external dependency (in-memory, file) is ready
  once started.

  Both return `200 {"status":"ok"}` or `503` with a status string and
  nothing else: no DSN, hostname, database error, or behavioral state. The
  reason a probe failed goes to the runtime's logs. Readiness probes are
  bounded by `readiness_timeout` and cost one driver-level ping — no
  migration, no query, no state load.

  Shutdown transitions readiness to not-ready first, then stops the health
  server, then closes the Store exactly once; repeated shutdown is safe.
  Signal handling and in-flight draining remain the Collector framework's,
  which already stops receivers before processors — this runtime adds no
  second shutdown owner.

  The official image documents port 13133 but adds **no Docker
  `HEALTHCHECK`**: it has no shell by design, and adding one would discard a
  deliberate security property. External HTTP probes are the intended
  mechanism.

- **Operational metrics for the Collector runtime**
  ([task 043](docs/archive/tasks/v0.9/043-self-observability-resource-safety.md)) — five
  OpenTelemetry instruments emitted through the `MeterProvider` the
  Collector already injects. Nothing to configure, no vendor client, and no
  second telemetry backend:

  | Metric | Type | Unit | Attributes |
  |---|---|---|---|
  | `trustvian.analyses` | Counter | `{analysis}` | `trustvian.outcome`: `analyzed`, `invalid_event`, `error` |
  | `trustvian.decisions` | Counter | `{decision}` | `trustvian.decision`: the six `policy.Decision` values, plus `other` |
  | `trustvian.analysis.duration` | Histogram | `s` | — |
  | `trustvian.observations` | Counter | `{observation}` | `trustvian.outcome`: `learned`, `not_eligible`, `error` |
  | `trustvian.observe.duration` | Histogram | `s` | — |

  These are the facts only Trustvian knows. Span counts are deliberately
  **not** among them: the Collector already counts spans accepted, refused,
  and dropped per component, and a parallel Trustvian counter would be a
  second, subtly different number for the same thing.

  **Cardinality is fixed at 15 time series**, regardless of how many actors,
  environments, or tenants a deployment sees — and it is enforced
  structurally rather than by review. Every attribute vocabulary is closed,
  and its measurement options are pre-built at construction, so a value with
  no entry has no option to record with; an unrecognized decision records as
  `other` instead of opening a series. Actor IDs, trace IDs, raw error text,
  DSNs, and behavioral payload cannot become labels, which a test asserts
  against recorded telemetry rather than against documentation.

  Instrumentation lives in the processor, never in the engine, so the core
  module's zero-OpenTelemetry dependency graph is unchanged and no SDK
  consumer gains a dependency. Recording is an in-process update — a failing
  metrics backend cannot slow, block, or alter a security decision — and the
  provider's lifecycle stays with the Collector, which created it.

  The span path stays allocation-free: `BenchmarkConsumeTraces` reports the
  same 33 allocs/op as before these metrics existed. With a real metrics SDK
  attached it costs ~350 ns more per span, which is the SDK's own
  aggregation across five measurements and is paid only when a metrics
  pipeline is configured. See
  [`docs/observability.md`](docs/observability.md).

  Both duration histograms carry explicit bucket boundaries (1 ms to 10 s).
  The SDK's defaults assume milliseconds; these instruments record seconds,
  so without the advice every measurement lands in the first bucket.

  Note that the bundled `cmd/trustvian-collector` — the minimal demo binary
  the reference deployment runs — supplies a **no-op `MeterProvider`**, so
  it records these metrics and exports none of them. That binary's
  hand-built telemetry factory exists to keep the demo's dependency graph
  small. Collecting them for real means building a Collector distribution
  with `ocb` and `otelconftelemetry`, which is how a production
  distribution is built anyway.

  Existing deployments need no configuration change.

- **Backup, restore, and upgrade procedures for learned behavioral state**
  ([task 044](docs/archive/tasks/v0.9/044-operations-backup-restore-upgrade.md)) —
  documented, tested, and built on PostgreSQL's own `pg_dump`/`pg_restore`
  rather than a Trustvian backup format.

  **[`docs/operations.md`](docs/operations.md)** is the one runbook: what
  Trustvian persists (and that policy and configuration never live in the
  database), taking a backup, restoring it safely, verifying learned state,
  upgrading, rolling back, a compatibility matrix, and a recovery drill. The
  `memory` store is stated plainly as having no recovery story.

  **`scripts/backup-postgres.sh`** takes an **online** backup — no downtime:
  `pg_dump` reads one snapshot and every Trustvian write is a single-row
  transaction, which a concurrent-writer test confirms. It produces a new
  directory with a custom-format archive (schema and data), a `MANIFEST`
  carrying no connection detail, and `SHA256SUMS`; artifacts are `0600`
  inside a `0700` directory; an existing path is never overwritten; and
  connection settings come only from the libpq environment, so no DSN ever
  appears on a command line.

  **`scripts/restore-postgres.sh`** restores only into an existing, empty
  database with no other sessions, requires matching checksums with no
  bypass, restores in a single transaction, and verifies the result's
  structure. Schema compatibility stays with Trustvian's own startup check —
  the script does not re-implement it. On failure the target is quarantined
  (`ALLOW_CONNECTIONS false`), because an empty database left behind by a
  failed restore would otherwise be adopted by Trustvian as a brand-new
  deployment and silently learn from zero. Neither script creates, drops,
  or renames a database, or moves traffic.

  **Recovery is proven by behavior, not row counts.** State learned through
  the real `Analyze`/`Observe` loop, backed up and restored, makes a new
  engine produce identical baselines and identical anomaly, trust, and
  decision results — which also differ from a cold start, so the comparison
  cannot pass vacuously. Upgrading is tested from the **real `v0.8.0`
  release**, built from its tag: the current release reads its database in
  place with identical results, the old release still reads it afterwards,
  a rollback restore reproduces pre-upgrade analysis, and both releases
  refuse a newer schema version.

  CI runs these on every pull request in a dedicated job with PostgreSQL 17
  client tools. `deployments/docker-compose/recovery-drill.sh` (also
  `make recovery-drill`, and nightly in CI) runs the whole chain on the real
  runtime — online backup, guarded restore, cutover, `/livez` and `/readyz`,
  learning continuing from the restored state, and readiness through a
  database outage.

  The reference deployment now enables the `health:` block (port 13133 on
  loopback) and gains `TRUSTVIAN_RUNTIME_POSTGRES_DB`, so cutting over to a
  restored database is one explicit variable. The drill found that setting
  it on a single command is not enough — Compose recreates the collector on
  the original database the next time a dependent service runs — so the
  documentation says to record it in `.env`.

- **[`docs/supply-chain.md`](docs/supply-chain.md)** — image identity,
  tagging, architectures, the scan policy, signing, and how to verify a
  published image. `docs/SECURITY.md` gains a supply-chain section kept
  explicitly distinct from Trustvian's runtime behavioral security.

- **[`docs/release-guide.md`](docs/release-guide.md)** — maintainer-facing:
  the module model, how to prepare and verify a release, what the
  automation does, and what promoting the processor to a published module
  would require.

- **[`CONTRIBUTING.md`](CONTRIBUTING.md)** — how to run the gates locally,
  why the processor and examples modules must be verified with `GOWORK=off`,
  how to run the PostgreSQL tests, and what the test tiers mean.

- **Official container image, with supply-chain verification** — an
  OpenTelemetry Collector running the Trustvian processor, published to
  `ghcr.io/trustvian/trustvian-collector` on a version tag. **No image has
  been published yet**; the pipeline runs on the next release.

  Built from a root `Dockerfile` onto
  `gcr.io/distroless/static-debian12:nonroot` for `linux/amd64` and
  `linux/arm64`. ~43 MB, runs as uid 65532, no shell, no package manager,
  no added capabilities, and containing nothing but the Collector binary and
  the base image's CA certificates — which are there because PostgreSQL TLS
  needs a trust store. A `.dockerignore` keeps `.git`, `dist/`, and local
  env files out of the build context.

  Releases carry an immutable `vX.Y.Z` image tag as the deployment
  contract; `X.Y` and `latest` are convenience tags, and a prerelease moves
  neither.

  The existing Docker Compose reference deployment still builds from
  source. Running the repository does not require a published image.

- **Security policy and issue templates**
  ([task 045](docs/archive/tasks/v0.9/045-v0.9-stabilization-release-gate.md), the
  `v0.9` release gate). [`.github/SECURITY.md`](.github/SECURITY.md) gives
  vulnerability reporters a private path — GitHub's private vulnerability
  reporting — with what to include and what to expect; before it, the
  repository had no stated reporting path, and GitHub surfaced the threat
  model as the security policy. Minimal bug-report and feature-request
  templates, with a link routing security reports away from public issues.

### Changed

- **The Go module path is now `github.com/trustvian/trustvian`** (lowercase),
  following the rename of the GitHub organization from `Trustvian` to
  `trustvian`. Every import path, repository URL, badge, and clone/install
  command moves with it.

  **This changes the import path for consumers.** Go module paths are
  case-sensitive, so the old and new paths are different modules to the
  toolchain:

  ```go
  import trustvian "github.com/trustvian/trustvian"   // from the next release
  ```

  Releases up to and including `v0.8.0` were published as
  `github.com/Trustvian/trustvian` and remain resolvable only under that
  path — their `go.mod` declares it. The lowercase path is resolvable from
  the first release tagged after the rename; until then,
  `go get github.com/trustvian/trustvian@latest` cannot resolve, and
  existing consumers should stay on the old path. Do not import both paths
  in one build: the toolchain would treat them as two modules and duplicate
  every type.

  The product name is unchanged. Only the organization identifier, and the
  technical paths derived from it, are lowercase.

- GitHub Actions workflows now use `actions/checkout@v7` and
  `actions/setup-go@v7`, replacing the `@v4`/`@v5` majors that run on the
  deprecated Node.js 20 runtime. Both new majors run on Node.js 24; the
  only breaking change across the intervening majors was that runtime move.
  Workflow permissions are unchanged.

- The nightly workflow now runs the PostgreSQL database-restart durability
  test, which previously ran in no workflow.

### Fixed

- **The release workflow could not run to completion.** It referenced
  `sigstore/cosign-installer@v4`, a version that has never existed — that
  project publishes exact tags and no floating major — so the container job
  of the first release candidate failed at job setup, before building
  anything. The installer is pinned to `v4.1.2`, and
  `scripts/check-action-refs.sh` now verifies in CI that every action
  reference in every workflow resolves, since valid YAML naming a
  nonexistent action version passes both linting and review. It resolves
  references with `git ls-remote` and needs no token, and it distinguishes
  "this reference does not exist" from "the lookup could not be performed"
  — an earlier API-based version reported a valid action as nonexistent
  when the lookup itself failed. The checker has its own tests.

- **Container image references are normalized to lowercase.** The release
  workflow built its image name from the GitHub organization's display name
  (`Trustvian`), and an OCI repository name must be lowercase, so every
  container step failed on the first one that ran. `scripts/image-name.sh`
  is now the one source for `ghcr.io/trustvian/trustvian-collector`, shared
  by the release workflow, CI, and the Makefile, so the scanned, pushed,
  signed, and attested repository cannot diverge. CI now builds the same
  reference a release does, which is what makes the expression testable
  before a tag exists.

- **Release candidates are marked as prereleases.** `gh release create` was
  called without `--prerelease` for any tag, so publishing a
  `v0.9.0-rc.N` draft would have made it GitHub's "Latest release". A tag
  with a prerelease suffix now creates a prerelease, using the same test
  that already keeps the `X.Y` and `latest` image tags from moving.

- Documentation corrected against the code during the `v0.9` release gate:
  the getting-started SDK example claimed an `allow` decision where the
  default engine returns `observe_only`; the release guide still described
  image signing as future work; the contributor guide's test-tier table
  predated the vulnerability, backup/restore/upgrade, and recovery-drill
  gates; 31 broken internal documentation links; stale "planned for `v0.9`"
  comments in the reference deployment. Operator documentation now states
  PostgreSQL support explicitly — 17 tested, 13+ expected — which
  previously lived only in a task file.

### Security

- **Supply-chain controls for released artifacts.** An SPDX SBOM and SLSA
  provenance attestation are attached to each published image using
  BuildKit's built-in mechanisms, and the image digest is signed with
  keyless Cosign via GitHub OIDC — so no signing key exists in the
  repository or in a secret. Signatures are made over digests rather than
  tags, since a tag can be moved.

- **Vulnerability gates.** `govulncheck` now runs in CI for both modules and
  fails on any *reachable* vulnerability; Trivy scans the container image
  and fails on `CRITICAL`/`HIGH` findings **with a fix available**. Unfixed
  advisories are reported but do not block, because blocking on an
  unfixable upstream issue would prevent shipping our own security fixes.
  Policy and every live exception: [`docs/supply-chain.md`](docs/supply-chain.md).

- **Affected dependencies updated to clear reachable vulnerability
  findings** surfaced by the new supply-chain gate. `golang.org/x/text` moves
  to `v0.41.0` in the root module, resolving `GO-2026-5970`, which was
  reachable from `postgres.NewStore` through pgx's connection setup. The
  latest `pgx/v5` requires the affected version, so raising the floor
  directly was the only available fix. `golang.org/x/crypto` moves to
  `v0.56.0` in the processor module, clearing two further advisories.
  `golang.org/x/sync` follows to `v0.22.0` as a transitive requirement.

  One finding remains as a documented exception: `GO-2026-5932` in
  `golang.org/x/crypto` has no upstream fix and is not reachable from
  Trustvian code. It is recorded with its scope and review condition in
  [`docs/supply-chain.md`](docs/supply-chain.md); no gate was weakened and
  no blanket ignore was added.

- **Vulnerability scanning resolves dependencies the way a consumer does.**
  Every scan runs with the Go workspace disabled, for all three modules. A
  workspace raises each module's dependency versions to satisfy the others,
  which can mask a vulnerability that a consumer of one module alone is
  genuinely exposed to — as happened here. The examples module is now
  scanned as well.

- **Publishing permissions are scoped to the publishing job.**
  `packages: write` and `id-token: write` exist only in the container job of
  the release workflow; `ci.yml` and `nightly.yml` remain `contents: read`.
  Registry authentication uses the workflow-scoped token, so there is no
  long-lived credential to rotate. No workflow uses `pull_request_target`.

- **Backup and restore safety.** Backups hold every actor's behavioral
  profile and are created non-world-readable; no credential reaches a
  backup, its manifest, script output, or a process listing; corrupt
  backups are refused; a restore never overwrites a database in use; and a
  failed restore is quarantined instead of being left for Trustvian to
  initialize as empty. `docs/SECURITY.md` records each property with the
  test that proves it, and the limits: a checksum detects corruption but
  does not authenticate, and any future change to the persisted `Baseline`
  shape must bump the schema version, or a binary-only downgrade would
  silently drop the new fields.

- **Floating image tags move only after signing.** The release workflow
  previously pushed `vX.Y.Z`, `X.Y`, and `latest` together and then signed:
  a signing failure would have left `latest` pointing at an unsigned image.
  It now pushes only the immutable tag, signs its digest, and then retags
  `X.Y` and `latest` to that digest without a rebuild. The release job also
  re-runs `govulncheck` against the tagged source, since the vulnerability
  database changes independently of the commit. Partial-release states and
  their recovery are documented in
  [`docs/release-guide.md`](docs/release-guide.md#the-release-is-not-atomic).

### Removed

- `.github/workflows/go.yml`, the stock template superseded by `ci.yml`.
  Keeping it would have run a strictly weaker duplicate of the same gates.

## v0.8.0 — Production Runtime & Storage

Makes Trustvian deployable as a real production system rather than a
library and a CLI against a local file: behavioral state can now be
selected through public configuration, persisted to PostgreSQL with
transaction-safe concurrent learning, and run from a documented reference
deployment. Five vertical slices, all additive — `InMemory` remains the
default and every `v0.5`–`v0.7` behavior is preserved.

### Added

- **Production Store Contract & Public Selection Boundary**
  ([task 034](docs/archive/tasks/v0.8/034-production-store-contract-and-public-boundary.md),
  first `v0.8` slice) — `config.StorageConfig` +
  `config.CompileStorage` (plus `LoadStorage`/`LoadStorageFile`) make
  persistence selectable through public API for the first time. Before
  this, `store.Store` lived in `internal/store` with methods referencing
  internal types, so an external consumer could neither name nor
  implement it and no exported function returned one — `WithStore` was
  in-module-only in practice, silently pinning every external deployment
  to the in-memory default and losing all learned baselines on restart.
  Supported backends at the time of this slice were `memory` and `file`;
  `postgres` was recognized but not yet implemented (see the next entry,
  which implemented it). Any compile error yields a nil Store — never a
  silent downgrade to non-durable storage, which would lose exactly the
  state an operator asked to keep.
  `trustvian analyze` / `baseline build` gain `--storage-config <path>`,
  which makes `baseline build` genuinely useful: a corpus learned by one
  invocation is now scored against by a separate one. Also adds
  `TestStoreContract` — the nine `Store` guarantees, executable against
  every implementation from one place, including a
  same-key-concurrency (no-lost-updates) test that a naive
  read-then-compute-then-write database backend would fail — and a new
  [`examples/persistent-baseline`](examples/persistent-baseline/)
  demonstrating restart survival through public API alone. No new
  dependency, no schema, no PostgreSQL code, no change to
  `store.Store`/`InMemory`/`FileStore` behavior, and `NewEngine`'s
  in-memory default is unchanged. See [ADR
  0018](docs/adr/0018-production-store-boundary-and-postgresql-direction.md).

- **PostgreSQL Store implementation**
  ([task 035](docs/archive/tasks/v0.8/035-postgresql-store-implementation.md), second
  `v0.8` slice) — `type: postgres` is now functional. The new
  `internal/store/postgres` package implements the existing
  `internal/store.Store` port against PostgreSQL and passes all nine of
  `TestStoreContract`'s guarantees **unmodified** — no contract test was
  weakened to accommodate it. This is the backend that makes a
  horizontally-scaled deployment coherent: several Trustvian instances
  share one baseline instead of each holding its own opinion of what
  "normal" means for the same actor.

  Configuration is a new `postgres:` block on the existing
  `StorageConfig` document (`dsn`, optional `max_connections` and
  `connect_timeout_seconds`), so selecting a production database is a
  YAML edit rather than a new mechanism — usable from the Go SDK, a YAML
  file, and `--storage-config` on both CLI subcommands.

  `Observe` is atomic and lost-update-free, **including the first
  observation for a key that does not exist yet**: it runs `INSERT ...
  ON CONFLICT DO NOTHING` before `SELECT ... FOR UPDATE`, because
  `FOR UPDATE` locks nothing when no row matches. Both protections are
  verified by mutation testing rather than asserted. State lives in one
  row per `baseline.Key`, authoritative in a single `jsonb` column using
  the identical encoding `FileStore` already writes, with derived
  inspection-only scalar columns (`fingerprint_count`,
  `observation_count`, `last_observed`, ...) so an operator can query
  learned state with plain SQL. Schema creation is automatic,
  idempotent, transactional, and safe under concurrent startup; a
  mismatched recorded version aborts with `ErrSchemaVersionMismatch`
  rather than being silently upgraded.

  Fail-closed throughout: an unreachable, unauthenticated, or
  schema-incompatible database returns an error and a nil Store from
  `CompileStorage`, with **no fallback** to the file or memory backend at
  any layer. The DSN is never logged and never wrapped into an error —
  `pgxpool.ParseConfig`'s error specifically is not wrapped, because pgx
  echoes an unparseable DSN verbatim while redacting parseable ones, and
  there is a regression test for exactly that. Every SQL value is a bound
  parameter.

  Also adds `Close()` on the PostgreSQL store via `io.Closer` — an
  optional, type-asserted capability following the existing
  `store.Freezer` precedent, so `store.Store` itself gained no method —
  and [`docs/storage-guide.md`](docs/storage-guide.md).

  One new direct dependency: `github.com/jackc/pgx/v5`, confined to the
  new package (only `config/compile.go` imports it), so the driver stays
  out of the build of everything that imports `internal/store`.
  `InMemory` remains the default, `FileStore` is unchanged and not
  deprecated, and `go test ./...` passes with no PostgreSQL installed —
  every test needing a server skips on an unset
  `TRUSTVIAN_TEST_POSTGRES_DSN`.

- **Store durability, concurrency & migration hardening**
  ([task 036](docs/archive/tasks/v0.8/036-store-durability-concurrency-and-migration-hardening.md),
  third `v0.8` slice) — proves the PostgreSQL backend holds under
  production failure and concurrency conditions, and fixes two
  schema-metadata safety gaps found while doing so. No new backend, no new
  configuration, no new dependency.

  **Fixed — ambiguous schema metadata is now rejected instead of guessed
  at**, via a new `postgres.ErrAmbiguousSchemaState` (distinct from
  `ErrSchemaVersionMismatch`, so an operator can tell "wrong version" from
  "unreadable metadata"):

  - A database holding baseline rows but **no recorded schema version** was
    silently treated as fresh and stamped with the current version. An
    operator who restored a partial backup, or ran `DELETE FROM
    trustvian_schema_version`, could therefore have an older binary adopt
    state written by a newer one. "No recorded version" now means "new
    database" only when there is also no data.
  - **Multiple version rows** were resolved by an unordered `LIMIT 1`; a
    database marked version 99 was observed being accepted because a
    leftover version 1 row came back instead. Ambiguity now fails startup.

  Both are fail-closed by design: recovery is an operator decision, since
  guessing is precisely what a schema version check exists to prevent.

  **Verified guarantees** (all newly tested; see
  [`docs/storage-guide.md`](docs/storage-guide.md) for the operator-facing
  summary): no lost updates under heavy same-key and first-write
  contention; distinct keys do not serialize globally; a failed
  transaction leaves the previous baseline byte-identical; committed state
  survives a real **PostgreSQL restart** as well as a client restart;
  connection loss yields an explicit error with stored state equal to the
  acknowledged writes; cancellation and deadline expiry are reportable
  through `errors.Is`; a transaction waiting on a row lock is cancellable;
  an exhausted connection pool fails on the caller's deadline rather than
  hanging; no connection leaks on any path; `Close` is idempotent;
  migration is atomic and safe under simultaneous startup; a corrupt
  stored baseline fails loudly and is **never silently reset**.

  **Storage choice does not affect behavior.** InMemory, FileStore, and
  PostgreSQL now have an explicit test asserting they produce identical
  learned state, identical `Anomaly`/`Trust` values, and identical
  `Decision`s — along with the learning-eligibility (anti-poisoning) gate
  and the `v0.7` agent-security semantics holding on all three.

  **Documented, now that it is verified**: `Observe` runs at READ
  COMMITTED and relies on explicit row locking rather than isolation
  level; PostgreSQL deadlock is structurally impossible because exactly
  one row is locked per transaction; Trustvian performs **no** transaction
  retries and needs none; pool-sizing guidance; and that automatic
  migration means the runtime role needs table-creation rights on first
  run (never superuser), with instructions for splitting migration from
  runtime identity.

  Integration tests gain a stress tier, selected with the standard
  `-short` flag rather than a new mechanism. `go test ./...` still requires
  no PostgreSQL.

- **Reference Docker Compose deployment**
  ([task 037](docs/archive/tasks/v0.8/037-reference-docker-compose-deployment.md),
  fourth `v0.8` slice) — a runnable local stack demonstrating Trustvian
  analyzing real OTLP telemetry against PostgreSQL-backed behavioral state
  that survives a restart:

  ```bash
  cd deployments/docker-compose
  docker compose up -d --build
  docker compose run --rm demo-producer
  ```

  The topology is an OpenTelemetry Collector running the Trustvian
  processor, writing baselines to PostgreSQL. **No standalone Trustvian
  server was introduced** — the deployment containerizes the existing
  Collector-processor runtime. Includes a deterministic `smoke-test.sh`
  that proves the whole path (startup, analysis, persistence across
  `down`/`up`, fail-closed on an unavailable database, and `down -v`
  teardown) and exits non-zero on failure. Images are built locally from
  source; nothing is published.

  **The Collector processor can now select a Store.** `processors.trustvian`
  gains a `storage:` block using the same `config.StorageConfig` schema the
  Go SDK and CLI already accept:

  ```yaml
  processors:
    trustvian:
      storage:
        version: v1
        type: postgres
        postgres:
          dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
  ```

  Before this, the processor called `NewEngine` with at most `WithPolicy`
  and never `WithStore`, so **every Collector deployment silently ran on
  the non-durable in-memory store and lost all learned baselines on
  restart** — the same reachability gap task 034 fixed for the CLI, in the
  one runtime that is actually long-lived. The block is decoded into
  `config.StorageConfig` and compiled by `config.CompileStorage`; the
  processor contains no database code, no DSN parsing, and no parallel
  storage configuration model. An unreachable database now fails Collector
  startup rather than falling back. `Shutdown` also releases the connection
  pool, which it previously did not.

  Omitting `storage:` preserves the previous in-memory behavior exactly.

  The deployment's PostgreSQL service doubles as the documented environment
  for the storage integration and stress suites (`make integration-postgres`),
  reusing the existing `TRUSTVIAN_TEST_POSTGRES_DSN` variable with no test
  changed.

### Changed

- `processor/go.mod` now uses a `replace` directive against the repository
  root instead of requiring `github.com/trustvian/trustvian v0.5.0`, which
  predates the storage configuration API the processor now needs. The
  release-time decision (bump to a released `v0.8.0` and drop the replace,
  or keep it) belongs to task 038.
- `cmd/trustvian-collector` registers the Collector's upstream
  `envprovider` alongside `fileprovider`, so `${env:VAR}` references in a
  Collector config resolve. This is what lets a deployment keep its
  database DSN out of its config file; no Trustvian-side interpolation
  mechanism was added.
- `newEngine` in `cmd/trustvian` now returns a cleanup function alongside
  the `Engine`, so the CLI releases the PostgreSQL connection pool on
  exit. Internal to the CLI; no public API change.

### Fixed

- A PostgreSQL integration test terminated *every* connection to the test
  database to simulate connection loss. Because `go test ./...` runs
  package binaries in parallel, it could kill connections belonging to
  other packages' tests mid-transaction, failing them with SQLSTATE 57P01.
  It now targets only its own store's connections, identified by
  `application_name`. Test-only; no product behavior involved.

### Removed

- `config.ErrStorageTypeNotImplemented`, added earlier in this same
  unreleased milestone for the state "a backend Trustvian recognizes but
  cannot construct." PostgreSQL was the only value ever in that state and is
  now implemented, leaving an exported sentinel no code path could return —
  something callers could write permanently dead `errors.Is` checks against.
  Removed during release stabilization rather than frozen by the release:
  it never appeared in a published version, so removing it now affects
  nobody, whereas removing it after `v0.8.0` would be a breaking change.
  Re-adding an error is backward-compatible if a future backend needs that
  distinction again.

### Security

Each item below is enforced by a test, not by convention:

- **Explicit storage selection fails closed.** A configured but unreachable,
  unauthenticated, or schema-incompatible PostgreSQL database is an error
  with a nil Store — there is no code path from "the database is
  unavailable" to a working in-memory or file store, at any layer (SDK, CLI,
  Collector processor, or external consumer). Silent persistence downgrade
  would mean every subsequent decision was made against state the operator
  believed was durable and shared.
- **Schema downgrade protection.** A database recorded at a schema version
  newer than the running binary aborts startup rather than being mutated.
  Ambiguous metadata — baseline data present with no recorded version, or
  multiple version rows — also fails closed instead of being guessed at.
- **Credential secrecy.** The DSN is never logged, never wrapped into an
  error, and never written to persisted state. `pgxpool.ParseConfig`'s error
  is deliberately not wrapped, because pgx echoes an *unparseable* DSN
  verbatim while redacting parseable ones.
- **SQL parameterization.** Every value reaches the database as a bound
  parameter. The only Go-assembled parts of any statement are two
  compile-time table-name constants.
- **Lost-update protection.** Concurrent `Observe` for the same key cannot
  lose an update, including the first observation for a key that does not
  exist yet.
- **Bounded state, no event warehouse.** Persistence stores current learned
  `Baseline` state only — one row per key, with every existing cardinality
  bound intact. No raw events, prompts, tool arguments, HTTP bodies, or SQL
  payloads are stored.
- **Anti-poisoning holds across backends.** The learning-eligibility gate
  lives above the Store, so durable shared persistence cannot be used to
  normalize blocked behavior.
- **Corruption is never silently reset.** An unreadable stored baseline
  produces an explicit error and the row is left intact, rather than being
  overwritten with a blank one.

## v0.7.0 — AI Agent Behavioral Security

Makes AI agents first-class behavioral actors in the existing engine —
no second pipeline, no agent-specific detector — then validates the
combined result and closes the public-configuration gap that validation
surfaced: five vertical slices, all additive, with `v0.5`/`v0.6`
behavior preserved by default.

### Added

- **AI Agent Event/Context Foundation**
  ([task 014](docs/archive/tasks/v0.7/014-ai-agent.md)) — `event.Context` gains
  three optional fields: `SessionID` (session/conversation grouping),
  `DelegatedFrom` (single-hop agent-to-agent delegation), and
  `ApprovalStatus` (a new exported enum type — `ApprovalUnspecified`
  (zero value) / `ApprovalNotRequired` / `ApprovalRequired` /
  `ApprovalApproved` / `ApprovalDenied` — recording a per-event
  human-approval fact, not a workflow state). None of the three affect
  `Fingerprint`/`baseline.Key` identity — proven, not just asserted,
  by a test that runs 1,000 distinct `SessionID` values through the
  real engine and confirms exactly one `Fingerprint` accumulates all
  1,000 observations. The task also proves `v0.6`'s existing bounded
  3-gram signal (`ngram_deviation`) already detects AI-agent
  tool-sequence novelty with zero new detector code. No new package,
  no new dependency, no agent-specific `Fingerprint`/`Baseline`/
  `Anomaly` type. Existing `v0.1`–`v0.6` callers are unaffected:
  `BenchmarkEngineAnalyze`'s allocation profile is unchanged. See [ADR
  0014](docs/adr/0014-ai-agents-as-first-class-behavioral-actors.md).
- **Approval-Aware Policy Semantics**
  ([task 030](docs/archive/tasks/v0.7/030-approval-aware-policy-semantics.md)) —
  `ApprovalStatus` gains its first real consumer, entirely inside
  `internal/policy`: `policy.Input`/`Condition` and
  `config.PolicyCondition` each gain an `ApprovalStatus` field (config
  key `approval_status`), letting a `Policy` rule require approval for
  an operation via the existing `Unless` mechanism
  (`Unless: &Condition{ApprovalStatus: Approved}`) — no new Rule-level
  primitive, no new anomaly signal. Fail-safe by construction, proven
  by test: an event self-declaring `ApprovalNotRequired` cannot
  override a `Policy` rule that requires approval, and missing
  evidence (`ApprovalUnspecified`) fails closed to `BLOCK` rather than
  silently passing. `Anomaly.Score`/`Trust.Score`/`Fingerprint.ID` are
  provably unaffected by `ApprovalStatus` — only `Decision` differs.
  The mechanism is domain-generic, not coupled to `ActorTypeAIAgent`.
  `ApprovalStatus` remains untrusted, self-reported evidence: this
  task adds no cryptographic verification of its provenance. No new
  package, no new pipeline stage, no new dependency; CLI and the OTel
  Collector processor require zero code changes. See [ADR
  0015](docs/adr/0015-approval-as-policy-evidence-not-behavioral-anomaly.md).
- **Delegation Behavioral Semantics**
  ([task 031](docs/archive/tasks/v0.7/031-delegation-behavioral-semantics.md)) —
  a new, opt-in `internal/anomaly` signal, `delegation_deviation`:
  has this actor ever received delegation from this immediate
  delegator before? `internal/features.VolatileFeatures` gains
  `DelegatedFrom` (read from `Context.DelegatedFrom`, never into
  `StableFeatures`); `internal/baseline.Baseline` gains a bounded
  (64-entry) `DelegatorCounts` map, scoped to the actor receiving
  delegation, not to any one operation; `internal/anomaly.Config`
  gains `DelegationWeight` (defaults to `0`, opt-in). Behavioral
  familiarity, not authorization or authenticated provenance: a
  familiar delegator is never thereby authorized, and an unfamiliar
  one is never thereby malicious — `DelegatedFrom` remains exactly as
  unauthenticated and self-reported as task 014 left it. Proven, not
  just documented: existing learning-eligibility (BLOCKed attempts
  never "normalize" a forged delegator through repetition), actor
  isolation, score-before-learn, cardinality bounding (200 distinct
  delegators stay capped at 64), and independence from both
  `Fingerprint` identity and task 030's approval-policy evidence. No
  new package, no new pipeline stage, no new `Store.Observe`
  parameter, no new dependency; `BenchmarkEngineAnalyzeDelegationAbsent`
  confirms zero added allocation when unused. See [ADR
  0016](docs/adr/0016-delegation-as-behavioral-evidence-not-provenance.md).
- **Agent Security Scenario Validation**
  ([task 032](docs/archive/tasks/v0.7/032-agent-security-scenario-validation.md)) —
  validated combined agent-security scenarios (unexpected privileged
  tool use, a sensitive read-then-external-post sequence, an approval
  violation, an unexpected delegator, external-destination drift, and
  one combined case exercising delegation, sequence, and approval
  evidence together) using existing primitives only — this is a
  validation and documentation task, not a new detection feature. No
  new anomaly signal, `Policy` primitive, or pipeline stage. Adds
  `scenario_test.go` (eleven new tests, including a combined poisoning
  regression and a no-correlated-evidence-explosion audit under every
  `v0.6`/`v0.7` signal weight at once) and a new, fully-public example,
  [`examples/ai-agent-security`](examples/ai-agent-security/),
  demonstrating task 030's approval mechanism end-to-end through
  `config.PolicyConfig`/`CompilePolicy` alone — the first example in
  that directory with a genuinely differentiated `ALLOW`/`BLOCK`
  decision. Also surfaces, and documents rather than patches, a real
  gap: `anomaly.Config` has no public-`config`-package equivalent of
  `policy.Policy`'s path, so delegation/sequence signals remain
  demonstrable only from inside this module today.
- **`v0.7` Stabilization & Release Gate**
  ([task 033](docs/archive/tasks/v0.7/033-v07-stabilization-release-gate.md)) —
  closes the gap task 032 found: `config.AnomalyConfig` (new public
  document) compiled by `config.CompileAnomaly` into the exact
  `anomaly.Config` value `trustvian.WithAnomalyConfig` already
  accepted, mirroring `config.CompilePolicy`'s exact shape as a third
  independent document alongside Policy and Alert configuration. Every
  existing `anomaly.Config` field is now publicly configurable
  (`DelegationWeight`, `NGramWeight`, `TransitionWeight`,
  `MarkovWeight`, `SensitiveTargetFloor`, and the rest) — none newly
  invented; `internal/baseline`'s state-size bounds stay internal,
  deliberately. `CompileAnomaly(AnomalyConfig{})` reproduces
  `anomaly.DefaultConfig()` byte-for-byte, proven by
  `TestCompileAnomalyZeroValueMatchesDefaultConfig` — existing callers
  who configure nothing see unchanged behavior. `trustvian analyze`/
  `baseline build` gain a matching `--anomaly-config <path>` flag.
  [`examples/ai-agent-security`](examples/ai-agent-security/) now
  demonstrates the full combined scenario — delegation, sequence, and
  approval evidence together — through public config alone, verified
  by a new `main_test.go` run from within the genuinely separate
  `examples` Go module (no `internal/*` import). Full `v0.7` regression
  (tasks 014/030/031/032's own tests, plus every `v0.6` sequence test)
  re-confirmed green. No new package, no new anomaly detector, no
  processor change, no new dependency. **`v0.7` is release-ready** —
  tagging `v0.7.0` remains a separate, explicit action. See [ADR
  0017](docs/adr/0017-public-anomaly-configuration-boundary.md).

## v0.6.0 — Behavioral Detection Depth

Adds sequence-aware behavioral detection on top of `v0.5`'s
configuration boundary: pairwise transition novelty and rarity,
bounded 3-gram (higher-order) detection, and a first-order Markov
severity curve over the same transition evidence — four vertical
slices plus a stabilization pass, all opt-in and byte-for-byte
compatible with `v0.5.0` by default.

### Added

- **Sequence Analysis Foundation**
  ([task 025](docs/archive/tasks/v0.6/025-sequence-analysis-foundation.md)) — a new,
  opt-in anomaly signal, `transition_deviation`: has the immediately
  preceding action ever led to this one before, for this actor?
  `internal/anomaly.Config` gains `TransitionWeight` (defaults to `0`,
  the same "ships opt-in" precedent `FrequencyWeight`/
  `TimePatternWeight` already set); `internal/baseline.Baseline` gains
  `LastFingerprintID`/`LastFingerprintTime`, and `FingerprintStats`
  gains a bounded (64-entry) `PredecessorCounts` map — no new
  `SequenceStore`, `SequenceKey`, or public API of any kind (see [ADR
  0010](docs/adr/0010-bounded-process-local-sequence-state.md)).
  Existing `v0.5` callers see byte-for-byte unchanged `Score`
  output and allocation profile (`BenchmarkEngineAnalyze`: 456 B/op, 17
  allocs/op, unchanged). See
  [docs/sequence-analysis.md](docs/sequence-analysis.md) for the full
  design.
- **Transition Rarity**
  ([task 026](docs/archive/tasks/v0.6/026-transition-rarity.md)) — a new, opt-in
  anomaly signal, `transition_rarity`, evolving task 025's binary
  seen/unseen `transition_deviation` into a graded common/uncommon/rare
  measure for transitions that *have* been seen:
  `rarity(A->B) = 1 - (PredecessorCounts_B[A] / OutgoingTransitionTotal_A)`
  — an empirical relative frequency, deliberately never called a
  "probability" (no Markov model, no smoothing — see [ADR
  0011](docs/adr/0011-transition-rarity-statistic-and-orientation.md)).
  `internal/baseline.FingerprintStats` gains one new scalar,
  `OutgoingTransitionTotal` (no new map, no new cardinality dimension);
  `internal/anomaly.Config` gains `MinTransitionObservations` (defaults
  to `20`, a minimum-support cold-start gate) and
  `TransitionRarityWeight` (defaults to `0`, the same "ships opt-in"
  precedent every other signal weight in this package already set).
  `transition_deviation` and `transition_rarity` remain mutually
  exclusive by construction — an unseen transition is never also
  reported as "rare." Existing `v0.5`/task-025 callers see byte-for-byte
  unchanged `Score` output; `Engine.Analyze`'s allocation profile stays
  unchanged (`456 B/op, 17 allocs/op`), though its latency grows by a
  small, reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 026](docs/PERFORMANCE.md#v06-task-026-transition-rarity).
- **Bounded n-gram Detection**
  ([task 027](docs/archive/tasks/v0.6/027-bounded-ngram-detection.md)) — two new,
  opt-in anomaly signals, `ngram_deviation`/`ngram_rarity`, extending
  order-awareness one step further back than tasks 025/026: given a
  3-gram `A -> B -> C`, has this exact (grandparent, predecessor) pair
  ever led to this destination before, and if so, how commonly? A
  fixed 3-gram only — no configurable `n`, no Markov model.
  `internal/baseline.Baseline` gains one new scalar,
  `PreviousFingerprintID`; `FingerprintStats` gains two new,
  *independently* bounded (64-entry each) maps, `TrigramCounts`
  (destination-keyed by a `(grandparent, predecessor)` pair via the
  new `baseline.TrigramKey` type) and `TrigramContinuationTotal`
  (predecessor-keyed by grandparent) — see [ADR
  0012](docs/adr/0012-bounded-trigram-behavioral-context.md) for why a
  scalar cannot answer a 3-gram's denominator, and for a genuine
  correctness subtlety this task's own review caught:
  `TrigramContinuationTotal`'s cardinality does not inherit
  `PredecessorCounts`'s existing bound "for free" and needed its own.
  `internal/anomaly.Config` gains `NGramWeight`, `MinNGramObservations`
  (defaults to `20`, a *separate* field from
  `MinTransitionObservations` — the two gate statistically different
  denominators), and `NGramRarityWeight` (both signal weights default
  to `0`, the same "ships opt-in" precedent every other signal weight
  in this package already set). Proven, end-to-end, to add genuine
  information beyond tasks 025/026: both pairwise hops of a sequence
  can be independently familiar while the complete 3-gram has never
  occurred, and `ngram_deviation` still detects it (see
  `TestScoreNGramDeviationDetectsNovelTrigramDespiteFamiliarPairwiseTransitions`).
  Existing `v0.5`/task-025/026 callers see byte-for-byte unchanged
  `Score` output; `Engine.Analyze`'s allocation profile stays unchanged
  (`456 B/op, 17 allocs/op`), though its latency grows by a small,
  reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 027](docs/PERFORMANCE.md#v06-task-027-bounded-n-gram-detection).
- **Markov Transition Scoring**
  ([task 028](docs/archive/tasks/v0.6/028-markov-transition-scoring.md)) — a new,
  opt-in anomaly signal, `markov_surprisal`, computing first-order
  Markov surprisal (`-log2(P(B|A))`, bounded into `[0,1)`) over the
  identical evidence task 026's `transition_rarity` already reads.
  Before writing any code, this task answered a mandatory question —
  what capability would Markov scoring add beyond `transition_rarity`?
  — and found the honest answer: **none, as evidence.** Both signals
  are proven (`TestMarkovSurprisalIsMonotonicReparameterizationOfRarity`)
  to be strictly monotonic functions of the identical
  `count/total` frequency; what differs is curve shape (surprisal
  preserves more resolution across the rare tail than the linear
  `1-frequency` mapping does). `markov_surprisal` is therefore built as
  an alternative, **mutually exclusive** scoring curve, not a second,
  independently-weighted signal: `anomaly.Score` forces
  `transition_rarity`'s own contribution to `Score` to zero whenever
  `Config.MarkovWeight` (defaults to `0`) is enabled, enforced in code
  and proven by
  `TestScoreMarkovAndTransitionRarityAreMutuallyExclusiveInScoring` — a
  caller cannot double-count this evidence by any combination of the
  two weights. No new `Baseline`/`FingerprintStats` state: reuses task
  025/026's `PredecessorCounts`/`OutgoingTransitionTotal` and
  `MinTransitionObservations` exactly. Never evaluates a `count == 0`
  transition (that remains `transition_deviation`'s domain), so
  `-log2(0)` is never computed — `Inf`/`NaN` are impossible by
  construction. See [ADR
  0013](docs/adr/0013-first-order-markov-surprisal-without-duplicate-evidence.md)
  for the full duplication analysis and design. Existing
  `v0.5`/task-025/026/027 callers see byte-for-byte unchanged `Score`
  output; `Engine.Analyze`'s allocation profile stays unchanged
  (`456 B/op, 17 allocs/op`), though its latency grows by a small,
  reported (not hidden) amount — see
  [docs/PERFORMANCE.md § v0.6 task 028](docs/PERFORMANCE.md#v06-task-028-markov-transition-scoring).

## v0.5.0 — Policy & Configuration

Adds a public, versioned configuration boundary for both `Policy` and
Alert Evaluation, and wires the former into the CLI and the OTel
Collector processor.

### Added

- **Public Policy configuration model + compiler**
  ([task 019](docs/archive/tasks/v0.5/019-policy-config-model.md)) — a new public
  package, `config` (a sibling to `event`/`alert`, not `internal/` —
  see [ADR 0008](docs/adr/0008-policy-config-boundary.md)):
  `PolicyConfig`/`PolicyRule`/`PolicyCondition` (primitive-typed
  structs mirroring `policy.Policy`/`Rule`/`Condition` exactly),
  `(PolicyConfig).Validate() error`, and
  `CompilePolicy(PolicyConfig) (policy.Policy, error)`. A caller
  outside this module can receive a `policy.Policy` from
  `CompilePolicy` and pass it straight into `trustvian.WithPolicy` via
  ordinary Go type inference, without ever importing `internal/policy`
  — a Go language property verified empirically, not assumed.
- **Config file loader & schema v1 parsing**
  ([task 020](docs/archive/tasks/v0.5/020-policy-config-loader.md)) —
  `Load([]byte) (PolicyConfig, error)` and
  `LoadFile(path string) (PolicyConfig, error)`, decoding a YAML
  document directly into task 019's existing public types via one new
  dependency, `go.yaml.in/yaml/v3`. Unknown fields and duplicate
  mapping keys are both rejected unconditionally — a config-time typo
  fails loudly rather than silently falling through to a different
  security behavior. `LoadFile` bounds its read to 1 MiB.
- **CLI configuration integration**
  ([task 021](docs/archive/tasks/v0.5/021-cli-config-integration.md)) —
  `trustvian analyze`/`trustvian baseline build` accept an optional
  `--config <path>`, consuming `config.LoadFile`/`config.CompilePolicy`
  directly. Omitting it preserves the CLI's pre-existing built-in
  default policy exactly; an invalid or missing explicit `--config`
  fails the whole command closed (non-zero exit, no analysis output),
  never a silent fallback.
- **OTel Collector policy configuration integration**
  ([task 022](docs/archive/tasks/v0.5/022-collector-config-integration.md)) — the
  standalone [`processor/`](processor/README.md) module accepts an
  optional `policy:` block in its own Collector configuration, decoded
  via `go-viper/mapstructure/v2` (pointed at `PolicyConfig`'s own
  `yaml` tags — Collector's confmap decoder only reads `mapstructure`
  tags) and compiled via the same `config.CompilePolicy`. An invalid
  explicit `policy:` block fails Collector startup outright.
  `processor/go.mod` depends on this exact `v0.5.0` release, verified
  with a clean `GOWORK=off go build ./... && GOWORK=off go test ./...
  -race` — no local workspace involved.
- **Declarative Alert configuration**
  ([task 023](docs/archive/tasks/v0.5/023-declarative-alert-configuration.md)) — a
  new, independent public model in the same `config` package:
  `AlertConfig`/`AlertRuleConfig`/`AlertConditionConfig`,
  `(AlertConfig).Validate() error`,
  `CompileAlerts(AlertConfig) ([]alert.Rule, error)`, and
  `LoadAlerts`/`LoadAlertsFile`. Deliberately a separate document and
  compilation path from `PolicyConfig`/`CompilePolicy` — see [ADR
  0009](docs/adr/0009-alert-config-is-a-separate-document.md) — not a
  combined schema. Not yet wired into the CLI or the Collector
  processor (neither has an alert-delivery flow for it to plug into
  yet); a Go SDK caller can use it directly today.

## v0.4.0 — Alert & Notification Foundation

Adds the Foundation stage of the Alert & Notification phase: a
minimal, explainable, externally deliverable `Alert`, without changing
`Decision` semantics or the existing detection pipeline. Delivery
reliability (retry, deduplication, cooldown) and provider-specific
sinks (Slack, Teams, PagerDuty) remain explicitly out of scope for this
release.

### Added

- **Alert & Notification Foundation**
  ([task 018](docs/archive/tasks/v0.4/018-alert-notification-foundation.md)) — a new
  public package, `alert` (a sibling to `event`, not `internal/` — see
  [ADR 0007](docs/adr/0007-alert-package-is-public.md)), turns a
  `Result`/`Decision` into a minimal, explainable, externally
  deliverable `Alert`: `Alert` (a view assembled from `Result`, no
  parallel model), `Severity`
  (`INFO`/`LOW`/`MEDIUM`/`HIGH`/`CRITICAL`, explicitly distinct from
  risk/anomaly score/trust score/decision), a `policy.Condition`-shaped
  Alert Evaluation matcher (`Condition`/`Rule`/`Evaluate` — flat
  AND-of-optional-fields, no combinators, structurally independent from
  `internal/policy`), the `Sink` interface, and `WebhookSink` — a
  generic HTTPS webhook signed with HMAC-SHA256 over a timestamp-bound
  payload, delivering a versioned contract
  (`Envelope{Version, Alert}`, `PayloadVersion = "1"`). No new pipeline
  stage: `Decision` semantics and the core detection engine are
  unchanged, verified via `go list -deps` showing zero HTTP/provider-SDK
  dependency reached `event` through `internal/policy` or `Engine`.
  Verified end-to-end via
  [`examples/alert-webhook`](examples/alert-webhook/README.md) — a
  genuinely external module building an `Alert` from a live
  `Engine.Analyze` call and delivering it to a signed webhook, no OTel
  involved. Day-of-week seasonality aside, this closes the last item
  `trustvian-project-spec.md` § 18 named as unimplemented for the
  Foundation stage; the Reliability and Additional-sinks-and-governance
  stages (retry, deduplication, cooldown, Slack/Teams/PagerDuty) remain
  explicitly deferred — see
  [§ v0.4.0](#v040--alert--notification-foundation).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.4.0` (Alert & Notification Foundation) as shipped, and
  the roadmap was extended with the planned milestone sequence toward
  `v1.0.0` (`v0.5` Policy & Configuration through `v0.9` Operational
  Readiness).

## v0.3.0 — Baseline & anomaly depth

Adds the one piece of time-based pattern awareness the roadmap
committed to for this milestone — hour-of-day seasonality — as a new,
deterministic, opt-in anomaly signal. No new pipeline stages; day-of-week
seasonality was explicitly scoped and deferred, not built.

### Added

- **Hour-of-day time-pattern anomaly signal**
  ([task 017](docs/archive/tasks/v0.3/017-baseline-time-patterns.md)) —
  `baseline.FingerprintStats` gained `HourActivity [24]float64` (a
  per-UTC-hour EWMA of traffic share) and a dedicated
  `TimePatternObservations` maturity counter, gating the signal
  independently of `Count` so a pre-existing persisted `FileStore`
  record — where `HourActivity` unmarshals to its zero array — is
  correctly treated as immature rather than falsely mature-with-empty-
  data. `anomaly.Score` gained a sixth signal, `time_pattern_deviation`,
  shipped **opt-in** like `frequency_deviation` before it
  (`Config.TimePatternWeight` defaults to `0`). An empirically
  discovered result: reusing the existing `emaAlpha = 0.2` for
  `HourActivity` was tried first and rejected — it made uniform,
  patternless hour-of-day traffic look sharply time-anomalous purely as
  a measurement-phase artifact — so a new, slower, separately-justified
  `hourActivityAlpha = 0.02` constant exists instead. Day-of-week
  seasonality was scoped and explicitly **not** built; it moved to
  [ROADMAP.md § Future research](docs/ROADMAP.md#future-research) as a
  separate vertical slice. See
  [DOMAIN.md § Baseline / § Anomaly](docs/DOMAIN.md).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.3.0` (baseline & anomaly depth) as shipped.

## v0.2.0 — OpenTelemetry maturation

Completes the OpenTelemetry integration story in the outbound
direction, and takes the first step toward a production Collector
deployment — without pulling OTel, or the Collector toolchain, into the
core.

### Added

- **Outbound `trustvian.*` result attributes**
  ([task 008](docs/archive/tasks/v0.2/008-otel.md)) —
  `internal/otel.AttributesFromResult` derives five outbound attributes
  (`trustvian.anomaly.score`, `trustvian.trust.score`,
  `trustvian.risk.level`, `trustvian.decision`,
  `trustvian.fingerprint.id`) from a `Result`, for a caller to attach to
  a span or export alongside one. `trustvian.behavior.id` — named in the
  original spec alongside the five above but never defined beyond its
  name — is deliberately not implemented: every plausible meaning
  collapses into `Fingerprint.ID`, and CLAUDE.md's OpenTelemetry section
  is explicit about not inventing telemetry attributes without
  documenting them. Verified via `go list -deps` that this introduces no
  import cycle and that `internal/otel` remains the sole package in this
  module depending on OpenTelemetry. See
  [OPENTELEMETRY.md § Trustvian output
  attributes](docs/OPENTELEMETRY.md#trustvian-output-attributes).
- **Standalone OTel Collector processor**
  ([task 009](docs/archive/tasks/v0.2/009-otel-collector.md)) —
  [`processor/`](processor/README.md), a separate Go module
  (`trustvian-processor`) implementing a real OpenTelemetry Collector
  traces processor: maps `ptrace.Span` into Trustvian events, runs them
  through the public `Engine` API, and writes the outbound
  `trustvian.*` attributes onto enriched spans before forwarding them.
  Verified end-to-end against a real OTel-SDK span sent over real
  OTLP/gRPC to a real running Collector binary built with this
  processor. Necessarily a parallel implementation of task 008's
  mapping/attribute code, not a reuse of it — `internal/otel` is
  unreachable from a genuinely separate module, and the Collector's
  `ptrace.Span` data model shares no relationship with the SDK's
  `sdktrace.ReadOnlySpan`. [ADR 0002](docs/adr/0002-public-api-boundary.md)'s
  public API boundary was considered and deliberately not revisited, so
  the processor runs Trustvian's zero-configuration default `Policy`
  today — every span it scores resolves to `trustvian.decision =
  "observe_only"` — documented as a known limitation. See
  [`processor/README.md` §
  Configuration](processor/README.md#configuration).

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.2` as shipped; `v0.3` (baseline & anomaly depth) became
  the new "next up."

## v0.1.0 — Behavioral core hardening & first public release

The core pipeline — `Event → Features → Fingerprint → Baseline →
Anomaly → Trust → Policy → Decision` — was already implemented, tested,
and benchmarked before this milestone. `v0.1` is the pass that took it
from "works and is tested" to "hardened, explainable, benchmarked,
demonstrated, and released as a stable OSS artifact." No new pipeline
stages were added; this release is depth, not breadth.

### Added

- **Target category** ([task 001](docs/archive/tasks/v0.1/001-feature-model.md)) —
  `event.Event.Target.Category` (`internal`, `external`, `database`) is
  a new, optional stable dimension. It flows through
  `internal/features.Extract` into `StableFeatures` and, from there,
  into the behavioral `Fingerprint`, so an actor that has only ever
  called internal services suddenly calling an external one is now a
  detectable category shift, not just a specific-hostname novelty.
  Zero value (`unspecified`) is accepted by `Event.Validate()` and
  never required.
- **Fingerprint versioning** ([task 002](docs/archive/tasks/v0.1/002-fingerprint.md))
  — `internal/fingerprint.Compute` now writes an explicit version
  marker into its hash before the stable fields. A future change to
  which dimensions feed the hash (or to the hash algorithm) bumps this
  one constant, producing a disjoint ID space instead of silently
  reinterpreting old `Fingerprint.ID`s under new semantics — the thing
  that stops being harmless once a persistent `Store` is in play (see
  `store.FileStore`, shipped in task 003). The fingerprint's design —
  what feeds the hash, in what order, and why FNV-1a — is now written
  up in [DOMAIN.md § Fingerprint](docs/DOMAIN.md#fingerprint).
- **`frequency_deviation` anomaly signal**
  ([task 004](docs/archive/tasks/v0.1/004-anomaly.md)) — `internal/baseline` now
  tracks an EWMA of inter-observation intervals
  (`FingerprintStats.IntervalMean`/`IntervalVariance`/
  `IntervalObservations`), and `internal/anomaly.Score` uses it to
  detect abnormal request *frequency*: an actor that normally calls an
  operation every 10 seconds and suddenly calls it every 100ms now
  triggers a dedicated signal, not just categorical novelty. Zero-cost
  (no allocation) on the common "frequency is normal" path, gated by
  `Config.FrequencyZThreshold`/`FrequencyWeight`, matching the existing
  latency/error signal shape exactly.

  The signal ships **opt-in**: `anomaly.DefaultConfig()` sets
  `FrequencyWeight` to `0`, so `frequency_deviation` is computed and
  reported in `Anomaly.Contributors` but contributes nothing to
  `Anomaly.Score` until an operator raises the weight. This mirrors
  `SensitiveTargetFloor`, which likewise ships empty and inert. The
  reason is calibration against real jitter: because the signal divides
  by the standard deviation of a fingerprint's *own* inter-event
  intervals, traffic with only a few milliseconds of natural jitter
  around a ten-second cadence produces `|z| > 3` — a clamped `1.0`
  signal — for entirely ordinary, on-cadence events. Measure your
  fleet's `IntervalMean`/`IntervalVariance` before enabling; see
  [DOMAIN.md § Anomaly](docs/DOMAIN.md#anomaly) and
  [examples/frequency-abuse](examples/frequency-abuse/).
- **Out-of-order observation guard in `internal/baseline`** — an
  observation whose timestamp does not strictly follow the
  fingerprint's `LastObserved` (clock skew, out-of-order delivery, a
  backdated event from an untrusted producer) is still counted, but its
  interval is no longer folded into the interval EWMA, and
  `LastObserved` now advances monotonically rather than regressing.
  `anomaly.Score` correspondingly refuses to fire `frequency_deviation`
  on a negative interval. Without this, a single backdated event could
  drive `IntervalMean` arbitrarily negative and make every subsequent
  on-time event look anomalous — a baseline-poisoning path that
  `Observe`'s decision gating structurally could not catch, since such
  an event is normally decided `observe_only`. See
  [SECURITY.md § baseline poisoning](docs/SECURITY.md#baseline-poisoning).
- **`Trust.Explain()`** ([task 005](docs/archive/tasks/v0.1/005-trust-risk.md)) — a
  new method rendering a `Trust` value as a short, human-readable
  sentence (identity confidence, anomaly at its effective confidence,
  context risk, and the resulting risk level). The multiplicative trust
  formula itself is unchanged; a new scenario-matrix test now sweeps
  identity confidence, anomaly score/confidence, and context risk
  across representative ranges and asserts the formula never produces a
  value outside `[0,1]` and is monotonic in every input.
- **Attribute matching in policy `Condition`**
  ([task 006](docs/archive/tasks/v0.1/006-policy.md)) — `policy.Condition` gained an
  `Attributes map[string]string` field, ANDed with every other
  `Condition` field, closing the original spec's own
  `tool.category: secrets` policy example. This is flat key/value
  equality only — no AND/OR/NOT combinators, no comparison operators,
  and no dynamic policy loading were added; `Condition{}`'s zero-value
  "matches everything" behavior is unchanged.
- **`Result.Explain()`** ([task 007](docs/archive/tasks/v0.1/007-decision.md)) — a
  new method rendering a complete, human-readable decision summary:
  final decision, trust/risk/anomaly scores, every contributing
  anomaly signal with its detail, and the matched policy rule (or
  default reason). Every field the project's own Decision checklist
  names was already present on `Result`; this makes assembling them
  into a readable explanation a reusable SDK method instead of
  CLI-only formatting logic.
- **`examples/`** ([task 010](docs/archive/tasks/v0.1/010-examples.md)) — a new,
  runnable `examples/` directory with six self-contained programs
  (`basic`, `credential-misuse`, `unexpected-dependency`,
  `external-destination`, `frequency-abuse`, `ai-agent`), each a
  genuine external consumer of this module (via `go mod replace`),
  each demonstrating the full `Event → ... → Decision` path with real,
  captured `go run` output. `make examples` runs and verifies all six.
- **Two closed performance-measurement gaps**
  ([task 011](docs/archive/tasks/v0.1/011-performance.md)) —
  `BenchmarkEventFromSpan` (`internal/otel`) and
  `BenchmarkInMemoryMemoryGrowth` (`internal/store`, at 100/1,000/10,000
  distinct keys) close the two gaps [PERFORMANCE.md](docs/PERFORMANCE.md)
  had explicitly named as unmeasured. Every pipeline stage named in the
  project roadmap is now benchmarked, with numbers reproduced fresh as
  part of this release (see [PERFORMANCE.md § Measured
  results](docs/PERFORMANCE.md#measured-results)).
- **Dedicated security test suite**
  ([task 012](docs/archive/tasks/v0.1/012-security-tests.md)) — new tests for
  malformed/extreme input (`NaN`/`±Inf` `IdentityConfidence`, very long
  strings, negative `duration_ms`), resource-exhaustion safety (a
  100,000-key `Attributes` map, a single actor producing 5,000 distinct
  fingerprints — neither panics), and an explicit end-to-end proof of
  cross-actor `Baseline` isolation
  (`TestAnalyzeCrossActorIsolation`). [SECURITY.md](docs/SECURITY.md)
  now documents every threat named in the project roadmap, each with a
  test reference or an explicit deferred-mitigation label.

### Changed

- [docs/ROADMAP.md](docs/ROADMAP.md)'s "Current status" section now
  reflects `v0.1` as shipped; `v0.2` (OpenTelemetry maturation) is the
  new "next up."

### Verified, not changed

`v0.1` is a hardening release: the pipeline shape, the trust formula,
and the policy evaluator's fail-closed guarantee are all unchanged from
before this milestone. What changed is depth — a new stable dimension,
a new anomaly signal, a versioned fingerprint hash, richer policy
matching, and, above all, verification: broader test coverage, a
documented performance baseline, and a documented security posture.

## Public API compatibility promise

Starting at `v0.1.0`, the following are committed stable — a breaking
change to any of them will be called out explicitly in a future
changelog entry and reflected in a major/minor version bump, not made
silently:

- **`event.Event`'s public field shapes** — `Actor`, `Operation`,
  `Target`, `Context`, `Attributes`, and the enum types backing them
  (`ActorType`, `OperationCategory`, `OperationDirection`,
  `TargetCategory`). New optional fields may be added in a
  backward-compatible way (zero value = "unset", never required by
  `Validate()`); existing fields will not be removed, renamed, or have
  their meaning changed.
- **`Result`'s public field shapes** — `Event`, `Features`,
  `Fingerprint`, `BaselineKey`, `Anomaly`, `Trust`, `Decision`,
  `Explanation`, and `Result.Explain()`'s general contract (a
  human-readable summary covering decision, scores, contributors, and
  policy reason — exact wording is not part of the promise, field
  presence is).
- **`Engine`'s public method signatures** — `NewEngine(opts ...Option)`,
  `Analyze(ctx context.Context, ev event.Event) (Result, error)`, and
  `Observe(ctx context.Context, result Result) (learned bool, err error)`.
- **The `Option` functions** — `WithStore`, `WithPolicy`,
  `WithAnomalyConfig`, `WithTrustConfig`, `WithContextRisk`, and the
  `Option func(*Engine)` type itself.

**Everything under `internal/` carries no compatibility promise.**
This includes, but is not limited to, `internal/features.Features`,
`internal/fingerprint.Fingerprint`, `internal/anomaly.Anomaly` and
`anomaly.Config`, `internal/trust.Trust` and `trust.Config`,
`internal/policy.Policy`/`Condition`/`Rule`, and
`internal/store.Store` and its implementations. External callers can
already *read* these types' exported fields through a `Result` value
without importing them (see [ADR
0002](docs/adr/0002-public-api-boundary.md)), but cannot construct or
name them directly from outside this module — Go's `internal/`
visibility rule enforces that boundary, and this project reserves the
right to change anything inside it, including field shapes, without
that counting as a breaking change to the public API described above.

**Where those two paragraphs meet.** `Result`'s fields are *typed* by
internal packages, so the two promises above need a precise boundary
between them. The promise on `Result` is a promise about `Result`
itself: that a field named `Trust` exists on it, that it is the trust
stage's output, and that it will not be removed, renamed, or
repurposed. It does not extend one level down into the type that field
holds. `trust.Trust` could gain, lose, or rename its own fields — so
`result.Trust.Score` is *not* covered by this promise even though
`result.Trust` is. The same applies to `result.Anomaly` (an
`anomaly.Anomaly`), `result.Features`, `result.Fingerprint`,
`result.BaselineKey`, `result.Decision`, and `result.Explanation`.

In practice these types are stable and no reshaping is planned; the
distinction is about what a future release is *permitted* to do without
calling it a break, not a warning that it will. If you depend on a
specific inner field — `result.Trust.Score` and
`result.Anomaly.Contributors` are the common ones — pin the minor
version and read the changelog before upgrading. Promoting any of these
types to a public package with a real promise attached is tracked in
[ROADMAP.md § future research](docs/ROADMAP.md#future-research), gated
on an external consumer actually needing it.
