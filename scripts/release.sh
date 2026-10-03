#!/usr/bin/env bash
#
# Release Trustvian with one command. Safe for an AI agent to run.
#
#   make release                     # the version CHANGELOG.md declares
#   make release PRE=rc              # its next release candidate
#   make release VERSION=vX.Y.Z      # explicit, validated the same way
#   make release DRY_RUN=1           # build, sign, verify; publish nothing
#   make release NO_WAIT=1           # dispatch, print the run URL, return
#   ./scripts/release.sh [--no-wait]
#
# It needs only an authenticated `gh` and git. It builds nothing, holds no
# key, never creates or pushes a tag, and never approves a deployment.
# Publishing waits on GitHub for a required reviewer of the `release`
# environment (an Organization Admin) to approve it, in the web UI or the
# mobile app. The workflow's publish job then creates the tag itself.
#
# Steps:
#   1. Local checks: on main, clean, equal to origin/main. Resolve the version
#      (scripts/release-version.sh) and print it, with how it was derived,
#      before anything else. Run the same preflight release.yml runs. When
#      Nightly has no run for the commit, start one on main and wait for it:
#      ask first in an interactive shell, just do it under DRY_RUN or without
#      a terminal. A failed or unfinished CI or Nightly run is reported,
#      never started over; a missing CI run is waited for, never dispatched.
#   2. Dispatch release.yml with commit = origin/main's head.
#   3. Watch it. When publish is waiting for approval, print where to approve
#      and keep watching until it is published, rejected or failed.
#
# See docs/release-runbook.md (the procedure) and
# docs/adr/0060-agent-operated-releases-with-environment-approval.md.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly WORKFLOW=release.yml
readonly POLL_SECONDS=20

die() {
    echo "release: $*" >&2
    exit 1
}

no_wait=false
case "${1:-}" in
    "") ;;
    --no-wait) no_wait=true ;;
    *) die "usage: release.sh [--no-wait]   (version from VERSION, PRE, DRY_RUN in the environment)" ;;
esac
case "${NO_WAIT:-}" in "" | 0 | false) ;; *) no_wait=true ;; esac
dry_run=false
case "${DRY_RUN:-}" in "" | 0 | false) ;; *) dry_run=true ;; esac

command -v gh >/dev/null || die "gh is required: https://cli.github.com"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated: run gh auth login"

# --- 1. Local checks ---------------------------------------------------------

git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main --tags
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "check out main first"
[ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
commit="$(git rev-parse origin/main)"
[ "$(git rev-parse HEAD)" = "$commit" ] || die "main is not equal to origin/main ($commit); pull or push first"

# The version, and how it was derived, before anything else happens.
resolved="$(RELEASE_REPO="$REPO" ./scripts/release-version.sh resolve "$commit")"
version="$(sed -n 1p <<<"$resolved")"
derivation="$(sed -n 2p <<<"$resolved")"
echo "release: $version — $derivation"
$dry_run && echo "release: dry run: build, sign and verify; nothing will be published"

# One release at a time. GitHub's concurrency group would otherwise cancel an
# older *queued* run when this one is dispatched, even with cancel-in-progress
# off, so refuse rather than displace it.
active="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --limit 20 --json status,url \
    --jq '[.[] | select(.status != "completed")] | map(.url) | join(" ")')"
[ -z "$active" ] || die "another release run is not finished: $active"

# start_nightly: dispatch nightly.yml on main for $commit, wait for it to
# succeed, or die with its link.
#
# Only for a commit with *no* Nightly run: preflight reports a failed or
# unfinished run as a failure, and this is never reached for one. Nightly runs
# on main's head, so the run is checked to be at $commit, in case main moved.
start_nightly() {
    local known ids id sha nightly_id="" nightly_url
    if ! $dry_run && [ -t 0 ]; then
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

# Preflight, reporting a workflow with no run for this commit separately
# (exit 3) from any other failure.
missing_runs="$(mktemp)"
trap 'rm -f "$missing_runs"' EXIT
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

# --- 2. Dispatch ---------------------------------------------------------------

title="Release $version at $commit"
$dry_run && title="$title (dry run)"
# The runs that already carry this title, so the new one is found by
# elimination rather than by comparing this machine's clock with GitHub's.
known="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
    --json databaseId,displayTitle \
    --jq "map(select(.displayTitle == \"$title\") | .databaseId) | join(\" \")")"
gh workflow run "$WORKFLOW" --repo "$REPO" --ref main \
    -f version="$version" -f commit="$commit" -f dry_run="$dry_run" -f derivation="$derivation"
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
        echo "release: not waiting; when verification passes, approve at $run_url (GitHub web or mobile)"
    fi
    exit 0
fi

report_failure() {
    echo "release: $1" >&2
    gh run view "$run_id" --repo "$REPO" --json jobs \
        --jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out")
              | "  \(.name): \(.conclusion) — \(.url)"' >&2 || true
    echo "  run: $run_url" >&2
    exit 1
}

# --- 3. Watch ---------------------------------------------------------------------

# The run's status is "waiting" while publish waits for a reviewer of the
# `release` environment. Read from the run itself: it needs no Deployments
# permission, which an agent's token deliberately lacks.
announced=false
lookup_failures=0
while :; do
    # A gh that keeps failing (token expired, access revoked) must not look
    # like a run that is still waiting: ten failed lookups in a row stop the
    # watch. The run itself is unaffected; approval can take as long as it
    # takes.
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
                echo "release: every check passed for $version at $commit"
                echo "release: approve at $run_url (GitHub web or mobile)"
                echo "release: an Organization Admin reviews the run's Approval summary and approves the 'release' deployment; waiting"
                announced=true
            fi
            ;;
    esac
    sleep "$POLL_SECONDS"
done

if [ "$conclusion" != success ]; then
    # A publish job that never ran a step was rejected, or not approved before
    # GitHub gave up: nothing was tagged or published.
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
