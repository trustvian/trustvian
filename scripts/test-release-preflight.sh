#!/usr/bin/env bash
#
# Tests for scripts/release-preflight.sh's offline checks: SemVer shape and
# precedence, "newer than every tag", the CHANGELOG and release-notes
# checks, and the actions' runtime pin against a scratch repository. The network checks (tags on the remote, CI and Nightly runs) are
# thin wrappers over git ls-remote and gh api and are not faked here.
#
#   ./scripts/test-release-preflight.sh

set -euo pipefail

cd "$(dirname "$0")/.."

# shellcheck source-path=SCRIPTDIR source=release-preflight.sh
source ./scripts/release-preflight.sh

pass_count=0
fail_count=0

ok() {
    printf 'ok    %s\n' "$1"
    pass_count=$((pass_count + 1))
}

bad() {
    printf 'FAIL  %s\n' "$1" >&2
    fail_count=$((fail_count + 1))
}

# succeeds DESCRIPTION CMD...: CMD exits 0 (in a subshell, so `fail`'s exit
# cannot end this script).
succeeds() {
    local what="$1"
    shift
    if ("$@") >/dev/null 2>&1; then ok "$what"; else bad "$what"; fi
}

refuses() {
    local what="$1"
    shift
    if ("$@") >/dev/null 2>&1; then bad "$what"; else ok "$what"; fi
}

# env_run_check: run_check with missing runs recorded in $missing.
env_run_check() { RELEASE_PREFLIGHT_MISSING_RUNS="$missing" run_check; }

cmp_is() {
    local a="$1" b="$2" want="$3" got
    got="$(semver_cmp "$a" "$b")"
    if [ "$got" = "$want" ]; then ok "cmp $a $b = $want"; else bad "cmp $a $b = $got, want $want"; fi
}

# --- shape ---------------------------------------------------------------
for v in v0.10.0 v1.0.0 v10.20.30 v1.0.0-rc.1 v1.0.0-alpha v1.0.0-0.3.7 v1.0.0-x-y.1; do
    succeeds "is_semver $v" is_semver "$v"
done
for v in 0.10.0 v0.10 v01.0.0 v1.00.0 v1.0.0+build.1 v1.0.0-rc.1+b v1.0.0- v1.0.0-rc..1 "v1.0.0 " ""; do
    refuses "is_semver '$v'" is_semver "$v"
done
succeeds "v1.0.0-rc.1 is a prerelease" is_prerelease v1.0.0-rc.1
refuses "v1.0.0 is not a prerelease" is_prerelease v1.0.0

# --- precedence: the SemVer 2.0.0 § 11 example chain, and the core -------
chain=(v1.0.0-alpha v1.0.0-alpha.1 v1.0.0-alpha.beta v1.0.0-beta v1.0.0-beta.2
    v1.0.0-beta.11 v1.0.0-rc.1 v1.0.0 v1.0.1 v1.1.0 v2.0.0)
for ((i = 0; i + 1 < ${#chain[@]}; i++)); do
    cmp_is "${chain[i]}" "${chain[i + 1]}" -1
    cmp_is "${chain[i + 1]}" "${chain[i]}" 1
done
cmp_is v0.10.0 v0.9.0 1
cmp_is v0.10.0 v0.10.0 0
cmp_is v0.10.0 v0.10.0-rc.3 1
cmp_is v0.9.0-rc.10 v0.9.0-rc.9 1

# --- newer than every tag --------------------------------------------------
succeeds "v0.10.0 after v0.9.0 and its candidates" check_newer v0.10.0 v0.8.0 v0.9.0 v0.9.0-rc.1 v0.9.0-rc.3
refuses "v0.9.1 after v0.10.0" check_newer v0.9.1 v0.9.0 v0.10.0
refuses "v0.10.0-rc.1 after v0.10.0" check_newer v0.10.0-rc.1 v0.10.0
refuses "an equal version" check_newer v0.10.0 v0.10.0

# --- CHANGELOG and release notes -------------------------------------------
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf '# Changelog\n\n## Unreleased\n\n## v0.10.0 — Developer Preview\n\n## v0.9.0 — Operational Readiness\n' >"$tmp/CHANGELOG.md"
succeeds "CHANGELOG has the stable section" check_changelog v0.10.0 "$tmp/CHANGELOG.md"
refuses "CHANGELOG lacks v0.11.0" check_changelog v0.11.0 "$tmp/CHANGELOG.md"
refuses "v0.1.0 does not match the v0.10.0 heading" check_changelog v0.1.0 "$tmp/CHANGELOG.md"
succeeds "a prerelease needs no section" check_changelog v0.11.0-rc.1 "$tmp/CHANGELOG.md"
printf '# Trustvian v0.10.0 — Developer Preview\n' >"$tmp/notes.md"
succeeds "notes name v0.10.0" check_notes v0.10.0 "$tmp/notes.md"
refuses "notes do not name v0.11.0" check_notes v0.11.0 "$tmp/notes.md"
: >"$tmp/empty.md"
refuses "empty notes" check_notes v0.10.0 "$tmp/empty.md"
refuses "missing notes" check_notes v0.10.0 "$tmp/absent.md"

# --- the actions' runtime pin ----------------------------------------------
# A scratch history: c0 → c1 (the previous release) → c2, and a side branch
# s1 from c0 that never contains c1.
repo="$tmp/repo"
git init -q "$repo"
commit_in() { # MESSAGE: an empty commit in $repo; prints its id
    git -C "$repo" -c user.name=test -c user.email=test@invalid \
        commit -q --allow-empty -m "$1"
    git -C "$repo" rev-parse HEAD
}
c0="$(commit_in c0)"
c1="$(commit_in c1)"
c2="$(commit_in c2)"
git -C "$repo" checkout -q -b side "$c0"
s1="$(commit_in s1)"
in_repo() { (cd "$repo" && "$@"); }
succeeds "a pin at the previous release passes" in_repo check_runtime_pin "$c1" v0.1.0 "$c1"
succeeds "a pin after the previous release passes" in_repo check_runtime_pin "$c2" v0.1.0 "$c1"
refuses "a pin before the previous release fails" in_repo check_runtime_pin "$c0" v0.1.0 "$c1"
refuses "a pin beside the previous release fails" in_repo check_runtime_pin "$s1" v0.1.0 "$c1"
refuses "a pin not in the clone fails" in_repo check_runtime_pin 0123456789abcdef0123456789abcdef01234567 v0.1.0 "$c1"
msg="$( (in_repo check_runtime_pin "$c0" v0.1.0 "$c1") 2>&1 || true)"
if [[ "$msg" == *"predates v0.1.0"* ]]; then ok "the refusal names the release it predates"; else bad "refusal: $msg"; fi

printf 'TRUSTVIAN_SOURCE_REPOSITORY=https://github.com/trustvian/trustvian.git\nTRUSTVIAN_SOURCE_COMMIT=%s\n' "$c1" >"$tmp/runtime.env"
if [ "$(runtime_pin "$tmp/runtime.env")" = "$c1" ]; then ok "runtime_pin reads the pin"; else bad "runtime_pin read the wrong pin"; fi
printf 'TRUSTVIAN_SOURCE_COMMIT=%s\n' "${c1:0:7}" >"$tmp/short.env"
refuses "a short pin is refused" runtime_pin "$tmp/short.env"
printf 'GO_VERSION=1.27.2\n' >"$tmp/nopin.env"
refuses "a file with no pin is refused" runtime_pin "$tmp/nopin.env"
refuses "a missing file is refused" runtime_pin "$tmp/absent.env"

# --- CI and Nightly runs, through a stubbed gh ------------------------------
# gh is replaced by a function that answers with $GH_ANSWER, the shape the real
# call's --jq produces: "none", or "<status> <conclusion> <url>".
# Older shellcheck reports this as SC2317, newer as SC2329.
# shellcheck disable=SC2317,SC2329 # invoked by latest_run_succeeded, in place of gh
gh() { echo "$GH_ANSWER"; }
missing="$tmp/missing"
run_check() { latest_run_succeeded nightly.yml 0123456789abcdef0123456789abcdef01234567; }

GH_ANSWER="completed success https://example.invalid/run/1"
succeeds "a successful run passes" run_check
GH_ANSWER="none"
refuses "no run fails by default" run_check
: >"$missing"
succeeds "no run is recorded, not failed, when asked" env_run_check
GH_ANSWER="completed failure https://example.invalid/run/2"
refuses "a failed run fails even when missing runs are recorded" env_run_check
GH_ANSWER="in_progress null https://example.invalid/run/3"
refuses "a running run fails even when missing runs are recorded" env_run_check
if [ "$(cat "$missing")" = nightly.yml ]; then ok "only the missing workflow is recorded"; else bad "recorded: $(cat "$missing")"; fi
unset -f gh

echo
echo "$pass_count passed, $fail_count failed"
[ "$fail_count" -eq 0 ]
