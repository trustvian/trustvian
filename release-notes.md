# Trustvian v0.10.0 — Developer preview

<!-- One paragraph: what this release is for, and who should upgrade. -->

Every change is in
[CHANGELOG.md](https://github.com/trustvian/trustvian/blob/v0.10.0/CHANGELOG.md#v0100--developer-preview).

## What's new

<!-- The user-facing changes, in the order a user meets them. -->

## What this release does not include

<!-- What a reader might expect here and will not find. -->

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

## Verifying

```bash
V=v0.10.0
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

What each flag pins: [supply-chain.md](https://github.com/trustvian/trustvian/blob/v0.10.0/docs/supply-chain.md#verifying-a-published-image).
