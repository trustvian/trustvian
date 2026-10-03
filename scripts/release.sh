#!/usr/bin/env bash
#
# Release Trustvian with one command.
#
#   make release VERSION=v0.10.0              # or: ./scripts/release.sh v0.10.0
#   make release VERSION=v0.10.0 DRY_RUN=1    # build, sign, verify; publish nothing
#
# Run by a human Organization Admin, from a clean main that equals origin/main.
# It needs only an authenticated `gh` and git. It builds nothing and holds no
# key. Its one write is the release tag: the v* ruleset lets only an
# Organization Admin create one (docs/governance/releases.md). This script
# creates it, through the API, after the workflow has built, signed and
# verified everything, and only once you confirm.
#
# An AI agent must never run this script except with DRY_RUN=1: creating a
# v* tag is reserved to a human, and an agent may not use the admin bypass
# even when its credential holds it (docs/governance/agents.md).
#
# Steps:
#   1. Local checks: on main, clean, equal to origin/main, then the same
#      preflight release.yml runs (scripts/release-preflight.sh).
#   2. Dispatch release.yml with commit = origin/main's head, and find the run.
#   3. Wait for every job up to `verify` to pass. A failure stops here, with
#      the failed job's log link, and nothing has been published.
#   4. Unless DRY_RUN: confirm, create the annotated tag at that commit, and
#      let `publish` finish.
#   5. Print the release URL, or which job failed and its log link.
#
# The confirmation in step 4 reads from a terminal and cannot be skipped: a
# shell with no terminal (CI, an agent's tool call) can dry-run, never release.
# See docs/release-guide.md and
# docs/adr/0059-releases-are-dispatched-verified-then-published.md.

set -euo pipefail

cd "$(dirname "$0")/.."

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly WORKFLOW=release.yml
readonly POLL_SECONDS=20
# The jobs that must pass before a tag may exist, as a jq array literal: gh's
# --jq takes no --arg, and these names are constants.
readonly VERIFIED_JOBS='["Preflight","Gates","Build archives","Container image","Verify (ubuntu-latest)","Verify (macos-latest)"]'

die() {
    echo "release: $*" >&2
    exit 1
}

version="${1:-${VERSION:-}}"
[ -n "$version" ] || die "usage: make release VERSION=vX.Y.Z [DRY_RUN=1]"
dry_run=false
case "${DRY_RUN:-}" in
    "" | 0 | false) ;;
    *) dry_run=true ;;
esac

command -v gh >/dev/null || die "gh is required: https://cli.github.com"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated: run gh auth login"

# --- 1. Local checks ---------------------------------------------------------

git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main --tags
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "check out main first"
[ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
commit="$(git rev-parse origin/main)"
[ "$(git rev-parse HEAD)" = "$commit" ] || die "main is not equal to origin/main ($commit); pull or push first"

RELEASE_REPO="$REPO" ./scripts/release-preflight.sh "$version" "$commit"

# A real release needs a person at a terminal for step 4. Refuse now, before
# anything is dispatched, rather than after verification.
if ! $dry_run && ! [ -t 0 ]; then
    die "a release needs an interactive terminal to confirm the tag; DRY_RUN=1 runs without one"
fi

# One release at a time. GitHub's concurrency group would otherwise cancel an
# older *queued* run when this one is dispatched, even with cancel-in-progress
# off, so refuse rather than displace it.
active="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --limit 20 --json status,url \
    --jq '[.[] | select(.status != "completed")] | map(.url) | join(" ")')"
[ -z "$active" ] || die "another release run is not finished: $active"

# --- 2. Dispatch ---------------------------------------------------------------

title="Release $version at $commit"
$dry_run && title="$title (dry run)"
# The runs that already carry this title, so the new one is found by
# elimination rather than by comparing this machine's clock with GitHub's.
known="$(gh run list --repo "$REPO" --workflow "$WORKFLOW" --event workflow_dispatch --limit 50 \
    --json databaseId,displayTitle \
    --jq "map(select(.displayTitle == \"$title\") | .databaseId) | join(\" \")")"
gh workflow run "$WORKFLOW" --repo "$REPO" --ref main \
    -f version="$version" -f commit="$commit" -f dry_run="$dry_run"
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

report_failure() {
    echo "release: $1" >&2
    gh run view "$run_id" --repo "$REPO" --json jobs \
        --jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "cancelled" or .conclusion == "timed_out")
              | "  \(.name): \(.conclusion) — \(.url)"' >&2 || true
    echo "  run: $run_url" >&2
    exit 1
}

if $dry_run; then
    gh run watch "$run_id" --repo "$REPO" --interval "$POLL_SECONDS" --exit-status >/dev/null ||
        report_failure "the dry run failed; nothing was published"
    echo "release: dry run of $version passed: built, signed and verified; nothing published"
    echo "  run: $run_url"
    exit 0
fi

# --- 3. Wait for verification -------------------------------------------------

echo "release: waiting for preflight, gates, build, image and verify"
while :; do
    # "<failed jobs> <run status> <verified jobs that succeeded>"
    state="$(gh run view "$run_id" --repo "$REPO" --json status,jobs --jq "
        ([.jobs[] | select(.conclusion == \"failure\" or .conclusion == \"cancelled\" or .conclusion == \"timed_out\")] | length) as \$failed
        | ([.jobs[] | select(.conclusion == \"success\" and (.name as \$n | $VERIFIED_JOBS | index(\$n)))] | length) as \$ok
        | \"\\(\$failed) \\(.status) \\(\$ok)\"")"
    read -r failed status ok <<<"$state"
    if [ "$failed" != 0 ] || [ "$status" = completed ]; then
        report_failure "verification did not pass; nothing was published and no tag was created"
    fi
    [ "$ok" = 6 ] && break
    sleep "$POLL_SECONDS"
done
echo "release: every check passed for $version at $commit"

# --- 4. The tag ---------------------------------------------------------------

me="$(gh api user --jq .login)"
printf 'release: create the annotated tag %s at %s as %s, and publish? [y/N] ' "$version" "$commit" "$me"
answer=""
read -r answer </dev/tty || true
if [ "$answer" != y ] && [ "$answer" != Y ]; then
    gh run cancel "$run_id" --repo "$REPO" >/dev/null || true
    die "not released; the run was cancelled and nothing was published"
fi

if existing="$(gh api "repos/$REPO/git/ref/tags/$version" --jq '"\(.object.type) \(.object.sha)"' 2>/dev/null)"; then
    # A re-run after the tag was created: it must be the same tag.
    read -r type object <<<"$existing"
    [ "$type" = tag ] || die "$version exists and is not an annotated tag"
    target="$(gh api "repos/$REPO/git/tags/$object" --jq .object.sha)"
    [ "$target" = "$commit" ] || die "$version already exists at $target, not $commit"
    echo "release: the tag $version already exists at $commit"
else
    tag_object="$(gh api "repos/$REPO/git/tags" \
        -f tag="$version" -f message="Trustvian $version" -f object="$commit" -f type=commit --jq .sha)"
    gh api "repos/$REPO/git/refs" -f ref="refs/tags/$version" -f sha="$tag_object" >/dev/null
    echo "release: created the annotated tag $version at $commit"
fi

# --- 5. Publish -----------------------------------------------------------------

gh run watch "$run_id" --repo "$REPO" --interval "$POLL_SECONDS" --exit-status >/dev/null ||
    report_failure "publishing failed; re-run the failed jobs to finish it (each publish step is idempotent)"
echo "release: $version is published"
echo "  $(gh release view "$version" --repo "$REPO" --json url --jq .url)"
echo "  run: $run_url"
