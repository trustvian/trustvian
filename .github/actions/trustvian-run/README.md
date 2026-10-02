# trustvian-run

Runs a Trustvian behavioral scenario or suite (`trustvian eval run --json`),
uploads its result document as an artifact, and exits with the CLI's own code.

**Run-side foundation only.** It renders no verdict and does not comment on the
pull request; that is the next slice of
[task 079](../../../docs/tasks/v1.0/079-ci-integration-github-action.md), and it
belongs in a separate job that never checks out or runs pull request code.

```yaml
on:
  pull_request:
    branches: [main]

jobs:
  run:
    runs-on: ubuntu-latest
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: trustvian/trustvian/.github/actions/trustvian-run@REPLACE_WITH_A_REVIEWED_TRUSTVIAN_COMMIT_SHA
        with:
          scenario: scenarios/support-login.yaml
```

Use `pull_request`. The action refuses `pull_request_target` and
`workflow_run`, which would run the workload with a write-scoped token and the
repository's secrets.

| File | Role |
|---|---|
| `action.yml` | The composite action: setup → run → upload → finish |
| `runtime.env` | The reviewed Trustvian commit and Go toolchain it builds |
| `setup.sh` | Builds and verifies the runtime under `$RUNNER_TEMP` |
| `run.sh` | Starts a control plane, runs the CLI, preserves the result |
| `finish.sh` | Writes the job summary and exits with the CLI's code |
| `lib.sh` | Shared, separately tested functions |

Guide: [docs/ci-github-action.md](../../../docs/ci-github-action.md).
Decision record:
[ADR 0056](../../../docs/adr/0056-the-run-action-builds-a-pinned-source-commit.md).
Tests: `scripts/trustvian_run_action_test.go`, and the end-to-end workflow
`.github/workflows/trustvian-run-action.yml`.
