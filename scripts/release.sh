#!/usr/bin/env bash
#
# Release Trustvian with one command, in one of two modes.
#
#   make release                     # the version CHANGELOG.md declares
#   make release PRE=rc              # its next release candidate
#   make release VERSION=vX.Y.Z      # explicit, validated the same way
#   make release DRY_RUN=1           # build, sign, verify; publish nothing
#   make release NO_WAIT=1           # dispatch, print the run URL, return
#   make release MODE=agent          # never approve here; approve on GitHub
#   ./scripts/release.sh [--no-wait]
#
# The mode (scripts/release-mode.sh) is the first line it prints:
#   manual  a person at an interactive terminal, outside Claude Code. When
#           publish waits for the `release` environment, it asks
#           [y]es / [n]o / [l]ater and, on y or n, reviews the deployment as
#           that person.
#   agent   Claude Code (CLAUDECODE is set), no interactive terminal, or
#           MODE=agent. It never approves anything: it prints where to approve
#           on GitHub and keeps watching.
# MODE=manual inside Claude Code or without a terminal is refused before
# anything is dispatched.
#
# It needs only an authenticated `gh` and git. It builds nothing, holds no
# key, and never creates or pushes a tag: the workflow's publish job creates
# it, after the approval. See docs/release-runbook.md (manual) and
# docs/releasing-with-claude-code.md (agent).

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-mode.sh
source ./scripts/release-mode.sh

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly WORKFLOW=release.yml
readonly POLL_SECONDS=20

die() {
    echo "release: $*" >&2
    exit 1
}

# --- The one place a deployment is reviewed ------------------------------------
#
# manual_review_deployment STATE COMMENT: approve or reject the run's pending
# `release` deployment as the person running this, through GitHub's
# pending_deployments API. Reached only from manual_prompt, which is reached
# only in manual mode; it checks again, immediately before the call, that this
# is manual mode, at an interactive terminal, outside Claude Code.
manual_review_deployment() {
    local state="$1" comment="$2" env_id
    [ "$mode" = manual ] || die "refusing to review a deployment outside manual mode"
    [ -z "${CLAUDECODE:-}" ] || die "refusing to review a deployment inside Claude Code"
    [ "$(is_interactive)" = yes ] || die "refusing to review a deployment without an interactive terminal"
    case "$state" in approved | rejected) ;; *) die "unknown review state '$state'" ;; esac
    env_id="$(gh api "repos/$REPO/actions/runs/$run_id/pending_deployments" \
        --jq '.[] | select(.environment.name == "release") | .environment.id')"
    [ -n "$env_id" ] || die "the run has no pending release deployment to review: $run_url"
    gh api -X POST "repos/$REPO/actions/runs/$run_id/pending_deployments" \
        -F "environment_ids[]=$env_id" -f state="$state" -f comment="$comment" >/dev/null
}

# manual_prompt: ask the person at the terminal what to do with the waiting
# deployment. Returns 0 after a review, exits 0 for "later".
manual_prompt() {
    local answer
    while :; do
        printf '%s ' "release: approve the release deployment for $version? [y]es / [n]o, reject / [l]ater, on GitHub"
        answer=""
        read -r answer </dev/tty || answer=l
        case "$answer" in
            y | Y)
                manual_review_deployment approved "approved at the make release prompt (manual mode)"
                echo "release: approved as $(gh api user --jq .login); publishing"
                return 0
                ;;
            n | N)
                manual_review_deployment rejected "rejected at the make release prompt (manual mode)"
                echo "release: rejected; nothing will be published"
                return 0
                ;;
            l | L)
                echo "release: left pending; approve or reject it on GitHub, web or mobile: $run_url"
                exit 0
                ;;
        esac
    done
}

# start_nightly: dispatch nightly.yml on main for $commit, wait for it to
# succeed, or die with its link.
#
# Only for a commit with *no* Nightly run: preflight reports a failed or
# unfinished run as a failure, and this is never reached for one. Nightly runs
# on main's head, so the run is checked to be at $commit, in case main moved.
start_nightly() {
    local known ids id sha nightly_id="" nightly_url
    if [ "$mode" = manual ] && ! $dry_run; then
        printf 'release: Nightly has no run for %s. Start it on main and wait for it? [y/N] ' "$commit"
        local answer=""
        read -r answer </dev/tty || true
        if [ "$answer" != y ] && [ "$answer" != Y ]; then
            die "not started; start it yourself with gh workflow run nightly.yml --ref main, then run make release again"
        fi
    else
        echo "release: Nightly has no run for $commit; starting it on main"
    fi

    known="$(gh run list --repo "$REPO" --workflow nightly.yml --event workflow_dispatch --limit 50 \
        --json databaseId --jq 'map(.databaseId) | join(" ")')"
    gh workflow run nightly.yml --repo "$REPO" --ref main
    for _ in $(seq 1 30); do
        ids="$(gh run list --repo "$REPO" --workflow nightly.yml --event workflow_dispatch --limit 50 \
            --json databaseId,headSha --jq 'map("\(.databaseId):\(.headSha)") | join(" ")')"
        for id in $ids; do
            sha="${id#*:}"
            id="${id%%:*}"
            if [[ " $known " != *" $id "* ]]; then
                [ "$sha" = "$commit" ] ||
                    die "main moved to $sha before Nightly started; run make release again"
                nightly_id="$id"
                break
            fi
        done
        [ -n "$nightly_id" ] && break
        sleep 2
    done
    [ -n "$nightly_id" ] || die "the Nightly run did not appear within a minute; see gh run list --workflow nightly.yml"
    nightly_url="https://github.com/$REPO/actions/runs/$nightly_id"
    echo "release: waiting for Nightly: $nightly_url"
    gh run watch "$nightly_id" --repo "$REPO" --interval 30 --exit-status >/dev/null ||
        die "Nightly failed for $commit: $nightly_url"
    echo "release: Nightly passed"
}

report_failure() {
    echo "release: $1" >&2
    gh run view "$run_id" --repo "$REPO" --json jobs \
        --jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out")
              | "  \(.name): \(.conclusion) — \(.url)"' >&2 || true
    echo "  run: $run_url" >&2
    exit 1
}

main() {
    # --- 0. The mode, before anything else -----------------------------------

    local resolved_mode
    resolved_mode="$(resolve_mode "${MODE:-}" "${CLAUDECODE:-}" "$(is_interactive)" 2>&1)" ||
        die "$resolved_mode"
    mode="${resolved_mode%%|*}"
    mode_line release "$mode" "${resolved_mode#*|}"

    no_wait=false
    case "${1:-}" in
        "") ;;
        --no-wait) no_wait=true ;;
        *) die "usage: release.sh [--no-wait]   (MODE, VERSION, PRE, DRY_RUN in the environment)" ;;
    esac
    case "${NO_WAIT:-}" in "" | 0 | false) ;; *) no_wait=true ;; esac
    dry_run=false
    case "${DRY_RUN:-}" in "" | 0 | false) ;; *) dry_run=true ;; esac

    command -v gh >/dev/null || die "gh is required: https://cli.github.com"
    gh auth status >/dev/null 2>&1 || die "gh is not authenticated: run gh auth login"

    # --- 1. Local checks -----------------------------------------------------

    git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main --tags
    [ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "check out main first"
    [ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
    commit="$(git rev-parse origin/main)"
    [ "$(git rev-parse HEAD)" = "$commit" ] || die "main is not equal to origin/main ($commit); pull or push first"

    # The version, and how it was derived, before anything is dispatched.
    local resolved derivation
    resolved="$(RELEASE_REPO="$REPO" ./scripts/release-version.sh resolve "$commit")"
    version="$(sed -n 1p <<<"$resolved")"
    derivation="$(sed -n 2p <<<"$resolved")"
    echo "release: $version — $derivation"
    if $dry_run; then echo "release: dry run: build, sign and verify; nothing will be published"; fi

    # One release at a time. GitHub's concurrency group would otherwise cancel
    # an older *queued* run when this one is dispatched, even with
    # cancel-in-progress off, so refuse rather than displace it.
    local active
    active="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --limit 20 --json status,url \
        --jq '[.[] | select(.status != "completed")] | map(.url) | join(" ")')"
    [ -z "$active" ] || die "another release run is not finished: $active"

    # Preflight, reporting a workflow with no run for this commit separately
    # (exit 3) from any other failure.
    local missing_runs preflight
    missing_runs="$(mktemp)"
    # shellcheck disable=SC2064 # expand now: missing_runs is local
    trap "rm -f '$missing_runs'" EXIT
    set +e
    RELEASE_REPO="$REPO" RELEASE_PREFLIGHT_MISSING_RUNS="$missing_runs" \
        ./scripts/release-preflight.sh "$version" "$commit"
    preflight=$?
    set -e
    case "$preflight" in
        0) ;;
        3)
            # CI runs on the push to main and cannot be dispatched; it is only
            # ever waited for, never started by this script.
            if grep -qx ci.yml "$missing_runs"; then
                die "CI has no run for $commit yet. It starts on the merge to main; wait for it to finish, then run make release again"
            fi
            start_nightly
            RELEASE_REPO="$REPO" ./scripts/release-preflight.sh "$version" "$commit"
            ;;
        *) exit 1 ;;
    esac

    # --- 2. Dispatch -----------------------------------------------------------

    # Matches release.yml's run-name exactly, so the run can be found.
    local title known ids id
    title="Release $version at $commit"
    if $dry_run; then title="$title (dry run)"; fi
    title="$title [$mode]"
    # The runs that already carry this title, so the new one is found by
    # elimination rather than by comparing this machine's clock with GitHub's.
    known="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
        --json databaseId,displayTitle \
        --jq "map(select(.displayTitle == \"$title\") | .databaseId) | join(\" \")")"
    gh workflow run "$WORKFLOW" --repo "$REPO" --ref main \
        -f version="$version" -f commit="$commit" -f dry_run="$dry_run" \
        -f operator="$mode" -f derivation="$derivation"
    echo "release: dispatched $title"

    run_id=""
    for _ in $(seq 1 30); do
        ids="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
            --json databaseId,displayTitle \
            --jq "map(select(.displayTitle == \"$title\") | .databaseId) | join(\" \")")"
        for id in $ids; do
            if [[ " $known " != *" $id "* ]]; then
                run_id="$id"
                break
            fi
        done
        [ -n "$run_id" ] && break
        sleep 2
    done
    [ -n "$run_id" ] || die "the dispatched run did not appear within a minute; see gh run list --workflow $WORKFLOW"
    run_url="https://github.com/$REPO/actions/runs/$run_id"
    echo "release: run $run_url"

    if $no_wait; then
        if $dry_run; then
            echo "release: not waiting; follow it at $run_url"
        else
            echo "release: not waiting; when verification passes, approve on GitHub, web or mobile: $run_url"
        fi
        exit 0
    fi

    # --- 3. Watch, and the approval ----------------------------------------------

    # The run's status is "waiting" while publish waits for a reviewer of the
    # `release` environment. Read from the run itself: it needs no Deployments
    # permission, which an agent's token deliberately lacks.
    local announced=false lookup_failures=0 state status conclusion
    while :; do
        # A gh that keeps failing (token expired, access revoked) must not
        # look like a run that is still waiting: ten failed lookups in a row
        # stop the watch. The run itself is unaffected.
        if ! state="$(gh run view "$run_id" --repo "$REPO" --json status,conclusion \
            --jq '"\(.status) \(.conclusion // "-")"')" || [ -z "$state" ]; then
            lookup_failures=$((lookup_failures + 1))
            [ "$lookup_failures" -lt 10 ] ||
                die "lost contact with the run after $lookup_failures failed lookups; it continues on GitHub: $run_url"
            sleep "$POLL_SECONDS"
            continue
        fi
        lookup_failures=0
        read -r status conclusion <<<"$state"
        case "$status" in
            completed) break ;;
            waiting)
                # A dry run skips publish, so it never waits for approval.
                if ! $dry_run && ! $announced; then
                    announced=true
                    echo "release: every check passed for $version at $commit"
                    if [ "$mode" = manual ]; then
                        manual_prompt
                    else
                        echo "release: waiting for approval — approve on GitHub, web or mobile: $run_url"
                        echo "release: an Organization Admin reads the run's Approval summary and approves the 'release' deployment"
                    fi
                fi
                ;;
        esac
        sleep "$POLL_SECONDS"
    done

    if [ "$conclusion" != success ]; then
        # A publish job that never ran a step was rejected, or not approved
        # before GitHub gave up: nothing was tagged or published.
        if $announced && [ "$(gh run view "$run_id" --repo "$REPO" --json jobs \
            --jq '[.jobs[] | select(.name == "Publish" and .conclusion != "success" and ((.steps // []) | length) == 0)] | length')" != 0 ]; then
            report_failure "the release deployment was rejected or not approved in time; nothing was published"
        fi
        report_failure "the run ended $conclusion; nothing is published unless the Publish job started (see docs/release-runbook.md § 4)"
    fi

    if $dry_run; then
        echo "release: dry run of $version passed: built, signed and verified; nothing published"
        echo "  run: $run_url"
        exit 0
    fi
    echo "release: $version is published"
    echo "  $(gh release view "$version" --repo "$REPO" --json url --jq .url)"
    echo "  run: $run_url"
}

# Globals shared by main and the functions above.
mode=""
version=""
commit=""
run_id=""
run_url=""
dry_run=false
no_wait=false

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
