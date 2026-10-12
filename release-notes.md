# Trustvian v0.12.0 — Change Impact

`v0.12.0` answers the question a developer asks right after changing a model,
a prompt or a tool: *what did that do?* One report per scenario execution
puts it in one table — whether both sides ran the same scenario, inputs, model
and prompt; every behavior's k/N and calls per run; every target's load,
latency, status classes and 429s; tokens and, with a pricing table, cost; the
gate as the execution recorded it; and suggestions beside the evidence. The
CLI, the pull request comment's renderer and the WebUI's new Change Impact
destination show the same document. When nothing arrives at all, a Status
view says why. Upgrade if you run `trustvian eval run` or `trustvian dev`; the
engine is unchanged. **Back up the platform state first**: the database moves
from schema 10 to 15 and does not move back.

Every change is in
[CHANGELOG.md](https://github.com/trustvian/trustvian/blob/v0.12.0/CHANGELOG.md#v0120--change-impact).

## What's new

- **Status: why is Live empty?** `GET /v1/status`, `trustvian status`,
  `trustvian dev --check` and a WebUI Status view show which Collectors report,
  which producers and model calls they have seen, how each span's actor was
  bound, and named suggestions for what to check. The WebUI opens on Status
  when nothing is active and on Live otherwise (task 105).
- **Scenario, input and prompt provenance.** A scenario may declare `inputs:`,
  a `model` or `model_env` and a `prompt_ref` per side. Every repeated
  comparison says whether both sides ran the same scenario, inputs, model and
  prompt, and warns beside the gate when they differ (task 086).
- **Frequency evidence.** Every behavior shows k/N and calls per run per side;
  every target a permille call ratio; `lost` marks a behavior in every
  reference run missing from a candidate run. Optional limits
  `min_candidate_frequency`, `max_lost_behaviors` and `max_calls_per_run`
  (task 106).
- **Performance and cost evidence.** Latency buckets, span and HTTP status
  classes, 429s and token counts per behavior, per target and per run, and
  cost only from a versioned pricing table whose provenance every figure
  carries (`trustvian-local --pricing`; task 087).
- **Fidelity.** Every behavior says whether telemetry named it or it was
  inferred from transport, with counts, and `max_llm_calls_per_run` gates the
  model calls in one candidate run (task 081).
- **The scenario execution report.** Each completed execution records the
  gate it consumed. `trustvian eval report <id>` prints its report — result
  document version 2 — as JSON or, with `--format md`, as the Markdown the pull
  request comment's renderer produces, byte for byte. `trustvian eval run
  --json` now prints the same report (task 107).
- **Change Impact in the WebUI.** Compare becomes Change Impact: the
  context's scenario executions and one execution's report, every figure the
  report's own, every behavior one click from its observations. Overview gains
  a Latest comparison panel (task 107).
- **A measured Model Change Report** — `llama3.2` against `gemma3:4b`, ten
  runs a side — is published under `docs/results/2026-10-12-model-change-report/`,
  with what it shows and what it does not.
- **Security.** Builds use Go 1.27.2 and the Collector `golang.org/x/net
  v0.60.0`, fixing the 2026-10-08 HTTP/2, `net/http`, `mime/multipart`,
  `crypto/tls` and `html/template` advisories. **v0.11.0's published binaries
  and image predate the fix**; see the CHANGELOG's Security section for the
  advisories each carries.

## What this release does not include

- **No verdict, score or advice of its own.** Nothing adds an aggregate
  verdict, an impact score, a cost gate, a rate gate or a percentile.
  Suggestions are named rule outputs beside the evidence and change no exit
  code.
- **No content.** No prompt, completion, tool argument or body is read,
  retained or shown; a prompt is identified by a reference you declare.
- **The pull request comment is still the version 1 rendering.** The run and
  comment actions build the commit their `runtime.env` pins, which predates
  the report; a later release moves the pin.
- **No metadata-only detection evaluation (080)**; it moved to `v0.13.0`.
- **No review decisions or analytics (088, 092)**; deferred to `v0.13.0`.

## Known limits

- **No default `k`; calibrate first.** An unchanged *deterministic* workload
  passes at `k = 1, j = 0`. An unchanged *nondeterministic* one can fail
  there. Task 078 measured a model-driven agent failing 6 of 252 unchanged
  self-comparisons at T = 0.7 and 1 of 252 at T = 1.3. At T = 1.3, no `k`
  removed every crossing.
- **A scenario asserts no ordering.** Under `eval run`, `dev`'s Collector
  configuration leaves `transition_weight` at 0, and every repetition starts
  from a fresh learning scope. A reorder alone therefore cannot fail a scenario,
  and the block-decision and critical-risk checks are marked advisory.
- **No Windows for `dev` or `--suite`.** Both refuse to run on Windows; use WSL2.
  The Windows archive carries only `trustvian`.
- **A fork pull request gets no comment.** GitHub gives the comment job a
  read-only token. The job records a warning and a job-summary note and stays
  green; the check is still the run job's.
- **GitHub Enterprise Server is not supported by the poster.**
  `GITHUB_API_URL` must be a bare `https://` host; GHES's `/api/v3` path is
  refused. github.com and GHE.com work.
- **The comment action supports the job's own `GITHUB_TOKEN` only.** It finds
  its earlier comment by `github-actions[bot]` authorship, so another identity
  would post duplicates.
- **A git repository with a commit is required for `eval run`.** It derives
  the project and candidate from git and has no flag to name them. Outside a
  repository it refuses, and its message suggests `trustvian dev --candidate`,
  which `eval run` does not accept.
- **One agent, one project.** An agent belongs to the project that first
  registered it. A scenario for the same agent under a different project stops
  with exit `3` ("run is outside the scenario execution's scope").
- **macOS binaries are not notarized.** Download with `curl` as documented. A
  browser download is quarantined by Gatekeeper; clear it with
  `xattr -d com.apple.quarantine` on the three binaries.
- **Schema 10 → 15 is one-way.** The first start of `v0.12.0` migrates a
  `v0.11.0` platform database forward through schemas 11 to 15 in one step, on
  SQLite and PostgreSQL; an older build then refuses it as newer. **Back up
  first**: copy the stopped runtime's state directory — `.trustvian/`, or
  `~/.trustvian/dev/<hash>/` under `trustvian dev`
  ([where state lives](https://github.com/trustvian/trustvian/blob/v0.12.0/docs/local-development.md))
  — or `pg_dump` the PostgreSQL schema. Records and executions from before each
  step read *not recorded* for that step's evidence, never zero. Platform backup tooling is task 071, not yet implemented.
- **Upgrade the control plane before the CLI.** A control plane older than
  `v0.12.0` refuses the `provenance` and `cli_version` fields a `v0.12.0`
  `trustvian eval run` sends, with `400` before any workload starts.
- **`eval run --json` now prints result document version 2.** Against a
  control plane without the report route it prints version 1 and says so;
  consumers branch on `version`.
- **A model change always changes the model behavior.** The model's name is
  the GenAI operation name, so the reference model's behavior is removed and
  lost and the candidate's is added. `max_lost_behaviors: 0` and
  `min_candidate_frequency` fail any model change on that row alone.
- **One model call can count under two targets.** A GenAI model-call span and
  the HTTP request beneath it are two behaviors (`ollama` and
  `ollama.localhost` in the demo); do not add the two rows.
- **Operational and fidelity evidence arrive only through the Collector.** A
  run ingested in-process through the SDK path reads its operational sections
  as unavailable and its fidelity as unrecorded.
- **Legacy HTTP keys are not read.** A span with only `http.method` and
  `http.url` becomes `rpc/<METHOD>` with no target, and 105's
  `status.collapsed_http_operations` rarely fires for method-only span names.
  Task 108 proposes a staged change.
- **Actors inside one live run are indistinguishable** in the Live view until
  task 109; the retained observations name each actor.
- **Two Overview panels still count a page in the browser** — the Runs status
  breakdown and the Gate verdicts tally — and say so.
- **With a candidate chosen, run lists show only its runs.** Pick a
  promotion's or provenance's other side under Change Impact, or paste its ID.
- **A reference check holds for the execution's own repetition count, project
  and environment.** A scenario that differs on any of them is refused by the
  CLI's same validation.

## Verifying

```bash
V=v0.12.0
A=trustvian_${V}_linux_amd64.tar.gz
SHA=$(git ls-remote https://github.com/trustvian/trustvian "refs/tags/$V^{}" | cut -f1)

sha256sum -c checksums.txt --ignore-missing
gh attestation verify "$A" \
  --repo trustvian/trustvian \
  --signer-workflow trustvian/trustvian/.github/workflows/release.yml \
  --source-ref refs/heads/main --source-digest "$SHA"

IMAGE=ghcr.io/trustvian/trustvian-collector
DIGEST=$(crane digest "$IMAGE:$V")
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity https://github.com/trustvian/trustvian/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository trustvian/trustvian \
  --certificate-github-workflow-trigger workflow_dispatch \
  --certificate-github-workflow-sha "$SHA"
```

What each flag pins: [supply-chain.md](https://github.com/trustvian/trustvian/blob/v0.12.0/docs/supply-chain.md#verifying-a-published-image).
