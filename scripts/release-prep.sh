#!/usr/bin/env bash
#
# Prepare a release as a pull request: cut the CHANGELOG section and write the
# release notes. Never tags, never dispatches.
#
#   make release-prep BUMP=patch|minor TITLE="Inspection depth"
#   make release-prep VERSION=v1.0.0 TITLE="…"     # asks you to type the version
#
# From a clean main equal to origin/main:
#   1. derive the version: one BUMP above the newest stable tag
#      (scripts/release-version.sh), or VERSION, which must itself be exactly
#      one bump above it. Leaving 0.x (VERSION=v1.0.0) needs the version typed
#      again at a terminal;
#   2. refuse when CHANGELOG.md's "## Unreleased" section is empty;
#   3. turn "## Unreleased" into "## <version> — <title>", with a new empty
#      "## Unreleased" above it;
#   4. write release-notes.md from the template below, carrying over the
#      previous notes' "## Known limits" section for you to edit;
#   5. on branch release/prep-<version>, commit
#      "docs(release): prepare <version>", push, and open the pull request.
#
# Then a human reviews and merges it, and `make release` releases what it
# declares. See docs/release-runbook.md § 3.

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-version.sh
source ./scripts/release-version.sh
# shellcheck source-path=SCRIPTDIR source=release-mode.sh
source ./scripts/release-mode.sh

# REPO (from $RELEASE_REPO) comes from release-preflight.sh, through
# release-version.sh.

die() {
    echo "release-prep: $*" >&2
    exit 1
}

# The mode, first: it is recorded in the pull request. Agent mode cannot type
# the confirmation leaving 0.x needs, and MODE=manual is refused where it
# cannot be manual, exactly as in scripts/release.sh.
resolved_mode="$(resolve_mode "${MODE:-}" "${CLAUDECODE:-}" "$(is_interactive)" 2>&1)" || die "$resolved_mode"
mode="${resolved_mode%%|*}"
mode_reason="${resolved_mode#*|}"
mode_line release-prep "$mode" "$mode_reason"

bump="${BUMP:-}"
explicit="${VERSION:-}"
title="${TITLE:-}"
[ -n "$title" ] || die 'TITLE is required: make release-prep BUMP=minor TITLE="What this release is about"'
if [ -n "$bump" ] && [ -n "$explicit" ]; then die "give BUMP or VERSION, not both"; fi
[ -n "$bump" ] || [ -n "$explicit" ] || die "give BUMP=patch|minor, or VERSION=vX.Y.Z"

command -v gh >/dev/null || die "gh is required: https://cli.github.com"
gh auth status >/dev/null 2>&1 || die "gh is not authenticated: run gh auth login"

# --- 1. Where we are, and which version -------------------------------------------

git fetch --quiet origin +refs/heads/main:refs/remotes/origin/main --tags
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "check out main first"
[ -z "$(git status --porcelain)" ] || die "the working tree is not clean"
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die "main is not equal to origin/main; pull or push first"

# Captured first, so a failed lookup fails here instead of reading as "no
# tags" and preparing v0.1.0. Not mapfile: macOS ships bash 3.2.
tag_list="$(remote_tags)" || exit 1
tags=()
while IFS= read -r t; do [ -z "$t" ] || tags+=("$t"); done <<<"$tag_list"
latest="$(latest_stable ${tags[@]+"${tags[@]}"})"

if [ -n "$bump" ]; then
    version="$(next_version "$latest" "$bump")"
    how="$bump bump"
else
    is_semver "$explicit" || die "VERSION='$explicit' is not vMAJOR.MINOR.PATCH"
    is_prerelease "$explicit" && die "prepare the stable version; candidates are released with make release PRE=rc"
    check_bump "$explicit" "$latest"
    version="$explicit"
    how="given explicitly"
    if [[ "${latest:-v0.0.0}" == v0.* ]] && [[ "$version" == v1.* ]]; then
        [ "$mode" = manual ] || die "leaving 0.x needs a person to confirm at a terminal, in manual mode"
        echo "release-prep: $version leaves 0.x: from then on the compatibility contract applies (docs/compatibility.md)."
        printf 'release-prep: type %s to confirm: ' "$version"
        answer=""
        read -r answer </dev/tty || true
        [ "$answer" = "$version" ] || die "not confirmed; nothing was changed"
    fi
fi
echo "release-prep: ${latest:-no stable tag} → $version ($how)"

if printf '%s\n' ${tags[@]+"${tags[@]}"} | grep -Fxq -- "$version"; then die "the tag $version already exists"; fi
check_newer "$version" ${tags[@]+"${tags[@]}"}

# --- 2. The Unreleased section ----------------------------------------------------------

unreleased="$(awk '
    /^## Unreleased[[:space:]]*$/ { seen = 1; next }
    seen && /^## / { exit }
    seen && NF { print }
' CHANGELOG.md)"
grep -q '^## Unreleased[[:space:]]*$' CHANGELOG.md || die 'CHANGELOG.md has no "## Unreleased" heading'
[ -n "$unreleased" ] || die "CHANGELOG.md's Unreleased section is empty: there is nothing to release"

branch="release/prep-$version"
if git ls-remote --exit-code --heads origin "$branch" >/dev/null 2>&1; then
    die "the branch $branch already exists on origin; is a prep pull request already open?"
fi

# --- 3. Cut the CHANGELOG section ----------------------------------------------------------

heading="## $version — $title"
awk -v heading="$heading" '
    !done && /^## Unreleased[[:space:]]*$/ { print; print ""; print heading; done = 1; next }
    { print }
' CHANGELOG.md >CHANGELOG.md.new
mv CHANGELOG.md.new CHANGELOG.md

# GitHub's anchor for the heading: lowercase, punctuation dropped, spaces to
# hyphens ("## v0.11.0 — Inspection depth" → "v0110--inspection-depth").
anchor="$(printf '%s' "${heading#\#\# }" | tr '[:upper:]' '[:lower:]' | LC_ALL=C sed 's/[^a-z0-9 _-]//g; s/ /-/g')"

# --- 4. The release notes -------------------------------------------------------------------

limits=""
if [ -f release-notes.md ]; then
    limits="$(awk '
        /^## Known limits[[:space:]]*$/ { seen = 1; next }
        seen && /^## / { exit }
        seen { print }
    ' release-notes.md)"
fi
[ -n "$(printf '%s' "$limits" | tr -d '[:space:]')" ] ||
    limits=$'\n- <!-- Each limit a user could trip over, and what to do instead. -->\n'

{
    echo "# Trustvian $version — $title"
    echo
    echo "<!-- One paragraph: what this release is for, and who should upgrade. -->"
    echo
    echo "Every change is in"
    echo "[CHANGELOG.md](https://github.com/$REPO/blob/$version/CHANGELOG.md#$anchor)."
    echo
    echo "## What's new"
    echo
    echo "<!-- The user-facing changes, in the order a user meets them. -->"
    echo
    echo "## What this release does not include"
    echo
    echo "<!-- What a reader might expect here and will not find. -->"
    echo
    echo "## Known limits"
    # Without trailing blank lines.
    printf '%s\n' "$limits" | awk '{ line[NR] = $0 } NF { last = NR } END { for (i = 1; i <= last; i++) print line[i] }'
    echo
    echo "## Verifying"
    echo
    # Quoted heredoc: the variables are the reader's, filled in when they run
    # it; only the @…@ placeholders are this release's.
    sed -e "s|@V@|$version|g" -e "s|@REPO@|$REPO|g" -e "s|@OWNER@|${REPO%%/*}|g" <<'NOTES'
```bash
V=@V@
A=trustvian_${V}_linux_amd64.tar.gz
SHA=$(git ls-remote https://github.com/@REPO@ "refs/tags/$V^{}" | cut -f1)

sha256sum -c checksums.txt --ignore-missing
gh attestation verify "$A" \
  --repo @REPO@ \
  --signer-workflow @REPO@/.github/workflows/release.yml \
  --source-ref refs/heads/main --source-digest "$SHA"

IMAGE=ghcr.io/@OWNER@/trustvian-collector
DIGEST=$(crane digest "$IMAGE:$V")
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity https://github.com/@REPO@/.github/workflows/release.yml@refs/heads/main \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository @REPO@ \
  --certificate-github-workflow-trigger workflow_dispatch \
  --certificate-github-workflow-sha "$SHA"
```

What each flag pins: [supply-chain.md](https://github.com/@REPO@/blob/@V@/docs/supply-chain.md#verifying-a-published-image).
NOTES
} >release-notes.md

# --- 5. Branch, commit, pull request ------------------------------------------------------------

subject="docs(release): prepare $version"
git switch --quiet -c "$branch"
git add CHANGELOG.md release-notes.md
git commit --quiet -m "$subject" -m "Cut CHANGELOG's $version section from Unreleased ($how from ${latest:-no stable tag}) and write release-notes.md from the template. Prepared by make release-prep; nothing is tagged or dispatched."
git push --quiet -u origin "$branch"
url="$(gh pr create --repo "$REPO" --base main --head "$branch" --title "$subject" --body "$(
    cat <<EOF
Prepares **$version** — $title (${latest:-no stable tag} → $version, $how).

Prepared in **$mode mode** ($mode_reason) by \`make release-prep\`.

- \`CHANGELOG.md\`: \`## Unreleased\` becomes \`$heading\`, with a new empty \`## Unreleased\` above it.
- \`release-notes.md\`: the release body, from the template. The known limits are carried over from the previous release.

**Before merging, edit in this pull request:** the opening paragraph, *What's new*, *What this release does not include*, and *Known limits*. They are what users read first.

After merge: \`make release\` (or \`make release DRY_RUN=1\` first) releases $version. See docs/release-runbook.md.

Nothing is tagged or dispatched by this pull request.
EOF
)")"
echo "release-prep: opened $url"
echo "release-prep: review and merge it, then: git checkout main && git pull --ff-only origin main && make release"
