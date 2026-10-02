#!/usr/bin/env bash
#
# trustvian-run, step 4: say what happened, then exit with the CLI's code.
#
# Runs unless the workflow was cancelled — a cancelled run made no claim, so
# this step makes none for it.
#
# The job summary is deliberately minimal: the head commit, a link to this
# run, the CLI's exit code, and whether the result artifact was uploaded. It
# publishes no verdict, no counts and no evidence; rendering the result
# document is the next slice's strict renderer's job, from the artifact. Every
# value is checked against the shape it must have, and is then rendered inert
# and bounded anyway (lib.sh md_inert).
#
# Exit: exactly the CLI's code when the CLI ran — 0, 1, 2 and 3 alike, never
# translated and never suppressed. 1 when it did not run, because then there
# is no code to pass through and the job must not look as though it passed.
# A failed upload or preservation is reported here and by its own step, which
# fails the action on its own; it does not change the CLI's code.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "$here/lib.sh"

cli_exit="${TRUSTVIAN_RUN_EXIT_CODE:-}"
head_sha="${TRUSTVIAN_RUN_HEAD_SHA:-}"
result="${TRUSTVIAN_RUN_RESULT:-}"
upload_outcome="${TRUSTVIAN_RUN_UPLOAD_OUTCOME:-}"
artifact_id="${TRUSTVIAN_RUN_ARTIFACT_ID:-}"
artifact_name="${TRUSTVIAN_RUN_ARTIFACT_NAME:-}"

[[ "$cli_exit" =~ ^[0-9]{1,3}$ ]] && [ "$cli_exit" -le 255 ] || cli_exit=""
[[ "$head_sha" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] || head_sha=""
[[ "$artifact_id" =~ ^[0-9]{1,20}$ ]] || artifact_id=""
[[ "$artifact_name" =~ ^[A-Za-z0-9._-]{1,100}$ ]] || artifact_name=""
case "$result" in present | absent | invalid | oversized) ;; *) result="" ;; esac

run_url=""
if [[ "${GITHUB_SERVER_URL:-}" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$ ]] &&
    [[ "${GITHUB_REPOSITORY:-}" =~ ^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$ ]] &&
    [[ "${GITHUB_RUN_ID:-}" =~ ^[0-9]{1,20}$ ]]; then
    run_url="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"
fi

# Values that reach the summary have each been checked against the exact shape
# they must have. Those rendered as prose also go through md_inert; the code
# spans hold only hex, digits, or the artifact name's own character set, none
# of which can close a code span.
if [ "$upload_outcome" = success ] && [ -n "$artifact_id" ] && [ -n "$artifact_name" ]; then
    artifact_line="uploaded as \`$artifact_name\`"
    case "$result" in
        present) artifact_line="$artifact_line — result document and execution metadata" ;;
        *) artifact_line="$artifact_line — execution metadata only; no result document ($(md_inert "$result"))" ;;
    esac
elif [ -z "$cli_exit" ]; then
    artifact_line="none — trustvian did not run"
else
    artifact_line="**not uploaded**"
fi

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    {
        echo "### Trustvian run"
        echo
        echo "Run-side foundation: no behavioral verdict is rendered here. The"
        echo "result document, when there is one, is in the artifact."
        echo
        echo "| | |"
        echo "|---|---|"
        if [ -n "$head_sha" ]; then
            echo "| Head commit | \`$head_sha\` |"
        else
            echo "| Head commit | unknown |"
        fi
        if [ -n "$run_url" ]; then
            echo "| Run | [$GITHUB_RUN_ID]($run_url) |"
        else
            echo "| Run | unknown |"
        fi
        if [ -n "$cli_exit" ]; then
            echo "| \`trustvian\` exit code | \`$cli_exit\` |"
        else
            echo "| \`trustvian\` exit code | none — it did not run |"
        fi
        echo "| Result artifact | $artifact_line |"
    } >>"$GITHUB_STEP_SUMMARY"
fi

if [ -z "$cli_exit" ]; then
    printf '::error title=trustvian-run::%s\n' \
        "trustvian did not run; an earlier step of this action failed and says why"
    exit 1
fi
if [ "$upload_outcome" != success ]; then
    printf '::error title=trustvian-run::%s\n' \
        "the result artifact was not uploaded; trustvian's exit code $cli_exit is passed through unchanged"
fi

exit "$cli_exit"
