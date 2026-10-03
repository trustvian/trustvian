# trustvian-comment

Posts a [`trustvian-run`](../trustvian-run/README.md) result as the pull
request's behavioral gate comment, and edits that one comment in place on every
later push. It downloads the run job's artifact, renders it with
`trustvian-ci-render`, writes the rendering to the job summary, and posts it with
`trustvian-ci-comment`. Both commands are built from the commit
[`../trustvian-run/runtime.env`](../trustvian-run/runtime.env) pins, never from
the job's workspace.

It runs in a **separate job** from the run action. That job holds
`pull-requests: write` and nothing else, checks nothing out, and runs after the
run job unless the workflow was cancelled:

```yaml
comment:
  needs: run
  if: ${{ !cancelled() }}
  runs-on: ubuntu-latest
  permissions:
    pull-requests: write
  steps:
    - uses: trustvian/trustvian/.github/actions/trustvian-comment@REPLACE_WITH_A_REVIEWED_TRUSTVIAN_COMMIT_SHA
      with:
        exit-code: ${{ needs.run.outputs.exit-code }}
```

The complete two-job workflow is
[`examples/github-actions/behavioral-gate.yml`](../../../examples/github-actions/behavioral-gate.yml).
Use `pull_request`. The action refuses `pull_request_target` and `workflow_run`:
under either one, the run job in the same workflow would execute the pull
request's code holding a write-scoped token and the repository's secrets.

| Input | Default | Notes |
|---|---|---|
| `exit-code` | — (required) | The run job's `exit-code` output. Empty means the CLI did not run |
| `artifact-name` | `trustvian-run` | The run action's `artifact-name` |
| `marker-id` | `artifact-name` | Which gate comment this is: one per scenario or suite |
| `github-token` | `${{ github.token }}` | Must be the job's `GITHUB_TOKEN` — see below |

| Output | Meaning |
|---|---|
| `renderer-exit` | `0` evidence, `1` no verdict, `2` usage (nothing posted), `3` artifact rejected |
| `posted` | `true` when the comment was created or updated |
| `outcome` | `created`, `updated`, `superseded`, `not-permitted`, or empty |

**The gate is not this action's.** It never changes the check: the verdict is
the run job's exit code. A fork pull request gets a read-only token, so the
comment is not posted. The action warns, adds a note to the job summary, and
succeeds. A GitHub API failure fails this job, and only this job.

**Only `GITHUB_TOKEN`.** The poster owns a comment only if `github-actions[bot]`
wrote it. With a GitHub App token or a personal access token, it would never find
its own comment, and would post a new one on every run.

| File | Role |
|---|---|
| `action.yml` | The composite action: setup → download → render → post |
| `setup.sh` | Names the pull request and builds the two commands from the pin |
| `render.sh` | Renders the artifact and writes the job summary |
| `post.sh` | Posts the rendering; the only step whose environment holds the token |

Shared functions come from [`../trustvian-run/lib.sh`](../trustvian-run/lib.sh).
Guide: [docs/ci-github-action.md](../../../docs/ci-github-action.md).
Decision record:
[ADR 0058](../../../docs/adr/0058-the-comment-job-is-a-separate-action-that-posts-from-pinned-source.md).
Tests: `scripts/trustvian_comment_action_test.go`,
`scripts/trustvian_ci_workflows_test.go`, and the `comment` job of
`.github/workflows/trustvian-run-action.yml`.
