---
description: Run a Trustvian release with the release-operator agent (patch | minor | rc | stable)
argument-hint: patch | minor | rc | stable
---

Use the `release-operator` agent to run this release by following
`docs/release-runbook.md`. Requested: **$ARGUMENTS**

- `patch` / `minor`: runbook S2 / S1 — open the release issue,
  `make release-prep BUMP=$ARGUMENTS TITLE="…"` (propose a title from the
  CHANGELOG), write the notes, wait for a human to merge, dry run if release
  tooling changed, then `make release`.
- `rc`: runbook S3 — the version the merged CHANGELOG declares, as its next
  candidate: `make release PRE=rc`.
- `stable`: runbook S3 step 6 — `make release` for the declared version after
  its candidates.
- Anything else, or nothing: ask which of the four is meant.

The agent never merges a pull request and never approves a deployment.
Publishing waits for an Organization Admin to approve the `release`
environment on GitHub; the agent prints where.
