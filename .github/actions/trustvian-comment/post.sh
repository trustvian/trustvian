#!/usr/bin/env bash
#
# trustvian-comment, step 4: post the rendering as the pull request's gate
# comment.
#
# trustvian-ci-comment does the work: it finds the one comment it owns for the
# marker, edits it in place or creates it, writes nothing when a newer head
# owns the comment, and degrades to a warning and a summary note when the
# token cannot write — a fork pull request. GITHUB_TOKEN is in this step's
# environment alone, and the poster is the only process that reads it.
#
#   poster 0 → succeed: created, updated, superseded, or not permitted
#   poster 1 → fail: the GitHub API failed, and the poster said how
#   poster 2 → fail: this job's context is malformed
#
# The gate is unaffected either way: it is the run job's exit code.
#
# Outputs: posted (true or false) and outcome, once the poster has exited 0.
# On a failure neither is written, and the action's posted output is false.

set -euo pipefail

here="$(CDPATH='' cd -P -- "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd -P)"
# shellcheck source=../trustvian-run/lib.sh
. "$here/../trustvian-run/lib.sh"
TRUSTVIAN_ACTION=trustvian-comment

refuse_privileged_event
: "${RUNNER_TEMP:?RUNNER_TEMP is not set; this action runs inside a GitHub Actions job}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set; this action runs inside a GitHub Actions job}"
: "${TRUSTVIAN_COMMENT_BIN_DIR:?the setup step did not build the poster}"
: "${TRUSTVIAN_COMMENT_BODY_FILE:?the render step produced no rendering}"
CDPATH='' cd -P -- "$RUNNER_TEMP" >/dev/null || fail "RUNNER_TEMP is not a directory"

poster="$TRUSTVIAN_COMMENT_BIN_DIR/trustvian-ci-comment"
[ -x "$poster" ] || fail "the setup step did not build trustvian-ci-comment"

# The poster's stdout is captured so its outcome can be reported as an output,
# then replayed to the log unchanged: its annotations are workflow commands
# and must reach the runner. Its stderr streams directly.
out="$(mktemp "$RUNNER_TEMP/trustvian-comment-out.XXXXXX")"
code=0
"$poster" \
    "--repository=${GITHUB_REPOSITORY:-}" \
    "--pull-request=${TRUSTVIAN_COMMENT_PULL_REQUEST:-}" \
    "--head-sha=${TRUSTVIAN_COMMENT_HEAD_SHA:-}" \
    "--marker-id=${TRUSTVIAN_COMMENT_MARKER_ID:-}" \
    "--body-file=$TRUSTVIAN_COMMENT_BODY_FILE" \
    >"$out" || code=$?
cat "$out"

case "$code" in
    0) ;;
    1) fail "the gate comment was not posted: the GitHub API failed. The gate's result is the run job's exit code and is unchanged" ;;
    2) fail "trustvian-ci-comment refused this job's context (exit 2); its message above says which value. Nothing was posted" ;;
    *) fail "trustvian-ci-comment exited $code, which it never does" ;;
esac

# The poster's outcome, read from the lines the pinned poster writes for each.
outcome=""
if grep -Eq '^created comment [0-9]+ for ' "$out"; then
    outcome=created
elif grep -Eq '^updated comment [0-9]+ for ' "$out"; then
    outcome=updated
elif grep -q '^::notice title=trustvian-comment::superseded by ' "$out"; then
    outcome=superseded
elif grep -q '^::warning title=trustvian-comment::the gate comment was not posted: the token cannot write' "$out"; then
    outcome=not-permitted
fi
case "$outcome" in
    created | updated) set_output posted true ;;
    *) set_output posted false ;;
esac
[ -n "$outcome" ] || warn "trustvian-ci-comment succeeded without reporting what it did"
set_output outcome "$outcome"
rm -f -- "$out"
