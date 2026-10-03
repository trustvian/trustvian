#!/usr/bin/env bash
#
# The release preflight: every check that must pass before anything is built.
#
#   ./scripts/release-preflight.sh v0.10.0 <full commit sha>
#
# One implementation, two callers. release.yml's preflight job runs it as the
# release's first gate, and scripts/release.sh runs it on the maintainer's
# machine before dispatching, so a release that cannot pass fails in seconds
# rather than after a workflow has queued. See
# docs/adr/0059-releases-are-dispatched-verified-then-published.md.
#
# Checks, in order:
#   1. the version is SemVer (vMAJOR.MINOR.PATCH[-prerelease], no build
#      metadata: `+` is not valid in an OCI image tag);
#   2. the version is greater than every existing v* tag, and its base
#      (the version without a prerelease suffix) is exactly one bump — patch,
#      minor or major — above the newest stable tag;
#   3. the version's tag does not exist yet;
#   4. the commit is a full SHA reachable from main;
#   5. the latest CI and Nightly runs for exactly that commit succeeded;
#   6. for a stable version, CHANGELOG.md at that commit has a "## <version>"
#      section;
#   7. release-notes.md at that commit exists and names the version (for a
#      prerelease, the version or its base: one set of notes serves a
#      version's candidates and its stable release).
#
# Needs git, with the repository's main branch fetched as $RELEASE_MAIN_REF
# (default origin/main), and an authenticated gh for check 5. Reads nothing
# from the working tree: CHANGELOG.md and release-notes.md are read from the
# commit itself, so what is checked is what will be released.
#
# Writes `prerelease=true|false` to $GITHUB_OUTPUT when it is set.
#
# Exit status: 0 passes; 1 fails. With RELEASE_PREFLIGHT_MISSING_RUNS set to
# a file path, a workflow that has *no run at all* for the commit does not
# fail check 5: its file name is appended to that file, every other check
# still runs, and the script exits 3 when everything else passed. A run that
# failed, or has not finished, still fails, because starting another would
# hide it. scripts/release.sh uses this to offer starting Nightly.

set -euo pipefail
export LC_ALL=C

readonly REPO="${RELEASE_REPO:-trustvian/trustvian}"
readonly MAIN_REF="${RELEASE_MAIN_REF:-origin/main}"
readonly REMOTE="${RELEASE_REMOTE:-origin}"

readonly SEMVER_RE='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*))?$'

fail() {
    echo "release preflight: $*" >&2
    exit 1
}

# is_semver VERSION: exit 0 for a release-shaped SemVer tag.
is_semver() {
    [[ "$1" =~ $SEMVER_RE ]]
}

# is_prerelease VERSION: exit 0 when the version carries a suffix.
is_prerelease() {
    [[ "$1" == *-* ]]
}

# compare_identifiers A B: SemVer § 11.4.1-3 for one prerelease identifier.
# Prints -1, 0 or 1.
compare_identifiers() {
    local a="$1" b="$2"
    local a_num=0 b_num=0
    [[ "$a" =~ ^[0-9]+$ ]] && a_num=1
    [[ "$b" =~ ^[0-9]+$ ]] && b_num=1
    if ((a_num && b_num)); then
        if ((10#$a < 10#$b)); then echo -1; elif ((10#$a > 10#$b)); then echo 1; else echo 0; fi
    elif ((a_num)); then
        echo -1 # numeric identifiers sort before alphanumeric ones
    elif ((b_num)); then
        echo 1
    elif [[ "$a" < "$b" ]]; then
        echo -1
    elif [[ "$a" > "$b" ]]; then
        echo 1
    else
        echo 0
    fi
}

# semver_cmp A B: SemVer precedence of two valid versions. Prints -1, 0 or 1.
#
# Not `sort -V`: it orders v1.0.0-rc.1 after v1.0.0, which is exactly the
# comparison a release preflight must get right.
semver_cmp() {
    local a="$1" b="$2"
    [[ "$a" =~ $SEMVER_RE ]] || fail "not SemVer: $a"
    local a_core=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}") a_pre="${BASH_REMATCH[5]}"
    [[ "$b" =~ $SEMVER_RE ]] || fail "not SemVer: $b"
    local b_core=("${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}") b_pre="${BASH_REMATCH[5]}"

    local i
    for i in 0 1 2; do
        if ((10#${a_core[i]} < 10#${b_core[i]})); then echo -1; return; fi
        if ((10#${a_core[i]} > 10#${b_core[i]})); then echo 1; return; fi
    done

    # A version without a prerelease outranks one with it.
    if [ -z "$a_pre" ] && [ -z "$b_pre" ]; then echo 0; return; fi
    if [ -z "$a_pre" ]; then echo 1; return; fi
    if [ -z "$b_pre" ]; then echo -1; return; fi

    local a_ids b_ids
    IFS=. read -r -a a_ids <<<"$a_pre"
    IFS=. read -r -a b_ids <<<"$b_pre"
    local n=${#a_ids[@]}
    ((${#b_ids[@]} < n)) && n=${#b_ids[@]}
    local c
    for ((i = 0; i < n; i++)); do
        c="$(compare_identifiers "${a_ids[i]}" "${b_ids[i]}")"
        if [ "$c" != 0 ]; then echo "$c"; return; fi
    done
    if ((${#a_ids[@]} < ${#b_ids[@]})); then echo -1; elif ((${#a_ids[@]} > ${#b_ids[@]})); then echo 1; else echo 0; fi
}

# base_version VERSION: the version without its prerelease suffix.
base_version() {
    printf '%s\n' "${1%%-*}"
}

# latest_stable TAG...: the highest stable SemVer tag, or nothing.
latest_stable() {
    local best="" tag
    for tag in "$@"; do
        is_semver "$tag" || continue
        is_prerelease "$tag" && continue
        if [ -z "$best" ] || [ "$(semver_cmp "$tag" "$best")" = 1 ]; then
            best="$tag"
        fi
    done
    [ -z "$best" ] || printf '%s\n' "$best"
}

# bumps LATEST: the patch, minor and major versions one step above LATEST,
# space-separated. The major step from 0.x is v1.0.0.
bumps() {
    [[ "$1" =~ $SEMVER_RE ]] || fail "not SemVer: $1"
    local x="${BASH_REMATCH[1]}" y="${BASH_REMATCH[2]}" z="${BASH_REMATCH[3]}"
    echo "v$x.$y.$((z + 1)) v$x.$((y + 1)).0 v$((x + 1)).0.0"
}

# check_bump VERSION LATEST: VERSION's base is exactly one bump above LATEST,
# the newest stable tag. With no stable tag yet, any version is a first
# release.
check_bump() {
    local version="$1" latest="$2" base
    [ -n "$latest" ] || return 0
    base="$(base_version "$version")"
    local p m M
    read -r p m M <<<"$(bumps "$latest")"
    case "$base" in
        "$p" | "$m" | "$M") ;;
        *) fail "$version is not a valid single bump above $latest: want $p (patch), $m (minor) or $M (major)" ;;
    esac
}

# check_newer VERSION TAG...: VERSION outranks every SemVer tag given. Tags
# that are not SemVer (none exist today) are ignored rather than compared.
check_newer() {
    local version="$1" tag
    shift
    for tag in "$@"; do
        is_semver "$tag" || continue
        if [ "$(semver_cmp "$version" "$tag")" != 1 ]; then
            fail "$version is not greater than the existing tag $tag"
        fi
    done
}

# check_changelog VERSION FILE: a stable version has its own section.
check_changelog() {
    local version="$1" file="$2"
    is_prerelease "$version" && return 0
    grep -Eq "^## ${version//./\\.}( |$)" "$file" ||
        fail "CHANGELOG.md has no \"## $version\" section"
}

# check_notes VERSION FILE: the release body exists and names the version.
# A prerelease may instead name its base version: the notes written for
# v0.11.0 serve v0.11.0-rc.1, rc.2 and v0.11.0 itself.
check_notes() {
    local version="$1" file="$2" base
    [ -s "$file" ] || fail "release-notes.md is missing or empty"
    base="$(base_version "$version")"
    if grep -Fq -- "$version" "$file"; then return 0; fi
    if is_prerelease "$version" && grep -Eq -- "${base//./\\.}([^.0-9-]|$)" "$file"; then return 0; fi
    fail "release-notes.md does not name $version — it would publish another release's notes"
}

# latest_run_succeeded WORKFLOW COMMIT: the newest run of WORKFLOW for
# exactly COMMIT concluded success.
latest_run_succeeded() {
    local workflow="$1" commit="$2" result
    result="$(gh api "repos/$REPO/actions/workflows/$workflow/runs?head_sha=$commit&per_page=1" \
        --jq '.workflow_runs[0] | if . == null then "none" else "\(.status) \(.conclusion) \(.html_url)" end')" ||
        fail "could not read $workflow runs for $commit"
    case "$result" in
        "completed success "*) echo "  $workflow: success (${result##* })" ;;
        none)
            if [ -n "${RELEASE_PREFLIGHT_MISSING_RUNS:-}" ]; then
                echo "$workflow" >>"$RELEASE_PREFLIGHT_MISSING_RUNS"
                echo "  $workflow: no run for $commit"
                return 0
            fi
            fail "$workflow has no run for $commit — start one: gh workflow run $workflow --ref main"
            ;;
        *) fail "the latest $workflow run for $commit is not a success: $result" ;;
    esac
}

main() {
    [ $# -eq 2 ] || fail "usage: release-preflight.sh <version> <commit>"
    local version="$1" commit="$2"

    is_semver "$version" ||
        fail "'$version' is not vMAJOR.MINOR.PATCH[-prerelease] (no build metadata)"
    [[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail "commit must be a full 40-character SHA, got '$commit'"

    local tags
    tags="$(git ls-remote --tags --refs "$REMOTE" 'v*' | sed 's|.*refs/tags/||')" ||
        fail "could not list the remote's tags"
    if grep -Fxq -- "$version" <<<"$tags"; then
        fail "the tag $version already exists"
    fi
    # shellcheck disable=SC2086 # one tag per word, by construction
    check_newer "$version" $tags
    # shellcheck disable=SC2086
    check_bump "$version" "$(latest_stable $tags)"

    git cat-file -e "$commit^{commit}" 2>/dev/null || fail "commit $commit is not in this clone"
    git merge-base --is-ancestor "$commit" "$MAIN_REF" ||
        fail "commit $commit is not reachable from $MAIN_REF"

    latest_run_succeeded ci.yml "$commit"
    latest_run_succeeded nightly.yml "$commit"

    local tmp
    tmp="$(mktemp -d)"
    # shellcheck disable=SC2064 # expand now: tmp is local
    trap "rm -rf '$tmp'" EXIT
    git show "$commit:CHANGELOG.md" >"$tmp/CHANGELOG.md" 2>/dev/null || fail "CHANGELOG.md is missing at $commit"
    git show "$commit:release-notes.md" >"$tmp/release-notes.md" 2>/dev/null || : >"$tmp/release-notes.md"
    check_changelog "$version" "$tmp/CHANGELOG.md"
    check_notes "$version" "$tmp/release-notes.md"

    if [ -n "${RELEASE_PREFLIGHT_MISSING_RUNS:-}" ] && [ -s "$RELEASE_PREFLIGHT_MISSING_RUNS" ]; then
        echo "release preflight: everything else passes; no run yet for $commit: $(tr '\n' ' ' <"$RELEASE_PREFLIGHT_MISSING_RUNS")" >&2
        exit 3
    fi

    local prerelease=false
    is_prerelease "$version" && prerelease=true
    if [ -n "${GITHUB_OUTPUT:-}" ]; then
        echo "prerelease=$prerelease" >>"$GITHUB_OUTPUT"
    fi
    echo "release preflight: $version at $commit passes (prerelease=$prerelease)"
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then
    main "$@"
fi
