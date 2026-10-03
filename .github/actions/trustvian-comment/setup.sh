#!/usr/bin/env bash
#
# trustvian-comment, step 1: name the pull request, and build the renderer
# and the poster.
#
# The head commit and the pull request number come from the event, before
# anything is fetched. trustvian-ci-render and trustvian-ci-comment are then
# built from the commit ../trustvian-run/runtime.env pins, with the toolchain
# it pins, through the run action's own lib.sh — the same digest check, the
# same isolated fetch, the same vcs.revision check. Nothing is read from, or
# built in, the job's workspace: this job may have none, and if a caller
# checked one out anyway, nothing in it is used.
#
# Outputs: bin-dir, head-sha, pull-request, marker-id, artifact-dir,
# runtime-commit.

set -euo pipefail

here="$(CDPATH='' cd -P -- "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd -P)"
run_action="$(CDPATH='' cd -P -- "$here/../trustvian-run" >/dev/null && pwd -P)"
# shellcheck source=../trustvian-run/lib.sh
. "$run_action/lib.sh"
TRUSTVIAN_ACTION=trustvian-comment

refuse_privileged_event
: "${RUNNER_TEMP:?RUNNER_TEMP is not set; this action runs inside a GitHub Actions job}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set; this action runs inside a GitHub Actions job}"
CDPATH='' cd -P -- "$RUNNER_TEMP" >/dev/null || fail "RUNNER_TEMP is not a directory"

for tool in git jq curl tar uname; do
    command -v "$tool" >/dev/null 2>&1 || fail "the runner has no $tool, which this action needs"
done
command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
    fail "the runner has neither sha256sum nor shasum"

# --- Inputs and identity --------------------------------------------------

artifact_name="${INPUT_ARTIFACT_NAME:-trustvian-run}"
marker_id="${INPUT_MARKER_ID:-}"
[ -n "$marker_id" ] || marker_id="$artifact_name"
[[ "$artifact_name" =~ ^[A-Za-z0-9._-]{1,100}$ ]] ||
    fail "input artifact-name must be 1-100 characters of letters, digits, '.', '_' and '-'"
[[ "$marker_id" =~ ^[A-Za-z0-9._-]{1,100}$ ]] ||
    fail "input marker-id must be 1-100 characters of letters, digits, '.', '_' and '-'"

[ "${GITHUB_EVENT_NAME:-}" = pull_request ] ||
    fail "this action comments on a pull request, and runs on pull_request events only (this is ${GITHUB_EVENT_NAME:-unset})"

# The pull request's head commit, never the merge commit (lib.sh).
resolve_head

pull_request="$(jq -r 'if (.pull_request.number | type) == "number" then (.pull_request.number | tostring) else "" end' \
    "$GITHUB_EVENT_PATH" 2>/dev/null)" || pull_request=""
[[ "$pull_request" =~ ^[1-9][0-9]{0,9}$ ]] ||
    fail "cannot name the pull request: event.pull_request.number is not a positive integer"

# --- The renderer and the poster -----------------------------------------

provide_pinned_source "$run_action/runtime.env"

bin_dir="$(mktemp -d "$TRUSTVIAN_RUNNER_TEMP/trustvian-comment-bin.XXXXXX")"
echo "Building the renderer and poster from $TRUSTVIAN_SOURCE_COMMIT with go$GO_VERSION"
build_pinned . ./cmd/trustvian-ci-render "$bin_dir/trustvian-ci-render"
build_pinned . ./cmd/trustvian-ci-comment "$bin_dir/trustvian-ci-comment"
for binary in trustvian-ci-render trustvian-ci-comment; do
    verify_pinned_build "$bin_dir/$binary"
done

# A fresh, empty directory for this invocation's download, so a second
# invocation in the same job can never render the first one's artifact.
artifact_dir="$(mktemp -d "$TRUSTVIAN_RUNNER_TEMP/trustvian-comment-artifact.XXXXXX")"

set_output bin-dir "$bin_dir"
set_output head-sha "$HEAD_SHA"
set_output pull-request "$pull_request"
set_output marker-id "$marker_id"
set_output artifact-dir "$artifact_dir"
set_output runtime-commit "$TRUSTVIAN_SOURCE_COMMIT"
