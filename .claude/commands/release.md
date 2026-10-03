---
description: Release Trustvian with the release-operator agent — /release minor|patch|rc|stable [--dry-run]. Worked examples in docs/releasing-with-claude-code.md
argument-hint: minor | patch | rc | stable [--dry-run]
---

<!-- release-forms: minor patch rc stable; flags: --dry-run -->

Requested: `/release $ARGUMENTS`

**Accepted forms, and nothing else:** `/release minor`, `/release patch`,
`/release rc`, `/release stable`, each optionally followed by `--dry-run`. If
the arguments are anything else (empty, `major`, a version, more words),
do not start anything: answer that `/release` accepts only those forms, and
that anything else (*"Release whatever main has"*, *"Release v1.0.0"*) can be
asked in plain words, which the `release-operator` agent handles.

For an accepted form, use the `release-operator` agent to run it by following
`docs/release-runbook.md`, with the examples in
`docs/releasing-with-claude-code.md`. Every `make release` and
`make release-prep` it runs passes **`MODE=agent`** explicitly.

| Form | Runbook | What the agent runs |
|---|---|---|
| `minor` | S1 | the release issue; `make release-prep MODE=agent BUMP=minor TITLE="…"`; notes; after a human merges, a dry run first if release tooling changed; `make release MODE=agent` |
| `patch` | S2 | the same with `BUMP=patch` |
| `rc` | S3 | `make release MODE=agent PRE=rc` for the version the merged CHANGELOG declares |
| `stable` | S3 step 6 | `make release MODE=agent` for the declared version after its candidates |
| any of them `--dry-run` | — | the same steps, with `make release MODE=agent DRY_RUN=1`, publishing nothing |

The agent never merges a pull request, never approves a deployment, never
creates or pushes a tag, and never runs `MODE=manual`. Publishing waits for an
Organization Admin to approve the `release` environment on GitHub; the agent
prints where.
