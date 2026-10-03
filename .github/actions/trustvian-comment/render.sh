#!/usr/bin/env bash
#
# trustvian-comment, step 3: render the artifact, and write the rendering to
# the job summary.
#
# trustvian-ci-render checks the artifact against this run's identity — the
# event's head commit, the repository, the run id and attempt, and the run
# job's exit code — and transcribes it, or renders an explicit no verdict. This
# step interprets nothing in the artifact; it only acts on the renderer's exit:
#
#   0  evidence          → summary, then posted
#   1  no verdict        → summary, then posted, replacing any stale verdict
#   3  artifact rejected → summary, then posted, as no verdict
#   2  usage             → this job's own context is malformed: nothing is
#                          rendered or posted, and the step fails
#
# Outputs: renderer-exit, and body-file on 0, 1 and 3.

set -euo pipefail

here="$(CDPATH='' cd -P -- "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd -P)"
# shellcheck source=../trustvian-run/lib.sh
. "$here/../trustvian-run/lib.sh"
TRUSTVIAN_ACTION=trustvian-comment

refuse_privileged_event
: "${RUNNER_TEMP:?RUNNER_TEMP is not set; this action runs inside a GitHub Actions job}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set; this action runs inside a GitHub Actions job}"
: "${TRUSTVIAN_COMMENT_BIN_DIR:?the setup step did not build the renderer}"
: "${TRUSTVIAN_COMMENT_ARTIFACT_DIR:?the setup step did not provide an artifact directory}"
CDPATH='' cd -P -- "$RUNNER_TEMP" >/dev/null || fail "RUNNER_TEMP is not a directory"

renderer="$TRUSTVIAN_COMMENT_BIN_DIR/trustvian-ci-render"
[ -x "$renderer" ] || fail "the setup step did not build trustvian-ci-render"
[ -d "$TRUSTVIAN_COMMENT_ARTIFACT_DIR" ] || fail "the artifact directory is missing"

body_file="$(mktemp "$RUNNER_TEMP/trustvian-comment-body.XXXXXX")"

# Each value is one argument attached to its flag, so an empty exit code is
# passed as the empty value it is ("the CLI did not run"), and nothing is
# parsed by a shell. The renderer validates every one of them.
code=0
"$renderer" \
    "--artifact-dir=$TRUSTVIAN_COMMENT_ARTIFACT_DIR" \
    "--head-sha=${TRUSTVIAN_COMMENT_HEAD_SHA:-}" \
    "--repository=${GITHUB_REPOSITORY:-}" \
    "--run-id=${GITHUB_RUN_ID:-}" \
    "--run-attempt=${GITHUB_RUN_ATTEMPT:-}" \
    "--exit-code=${INPUT_EXIT_CODE:-}" \
    "--server-url=${GITHUB_SERVER_URL:-https://github.com}" \
    >"$body_file" || code=$?

set_output renderer-exit "$code"
case "$code" in
    0 | 1 | 3) ;;
    2) fail "trustvian-ci-render refused this job's context (exit 2), so nothing was rendered or posted; its message above says which value" ;;
    *) fail "trustvian-ci-render exited $code, which it never does; nothing was posted" ;;
esac

# The summary carries the rendering in every case that has one, so a fork —
# where the comment cannot be posted — still shows it. A summary that cannot
# be written is reported and does not stop the comment.
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    if ! { cat "$body_file" >>"$GITHUB_STEP_SUMMARY"; } 2>/dev/null; then
        warn "the job summary could not be written; the comment is posted regardless"
    fi
fi

set_output body-file "$body_file"
