#!/usr/bin/env bash
#
# Tests for check-action-refs.sh.
#
#   ./scripts/test-check-action-refs.sh
#
# That script is a release gate — it is what stands between a nonexistent
# action version and a failed release — so it needs tests of its own. It has
# already been wrong twice in ways that mattered: it let
# `sigstore/cosign-installer@v4` reach a tag, and it then reported a
# perfectly valid `aquasecurity/trivy-action@v0.36.0` as nonexistent when the
# lookup itself failed in CI.
#
# Every case below runs against a FAKE resolver supplied through
# TRUSTVIAN_ACTION_REF_RESOLVER and a temporary workflow directory, so the
# suite is deterministic and needs no network. One final case is opt-in and
# does hit the network, because the real resolver deserves one real check:
#
#   TRUSTVIAN_TEST_ACTION_REFS_NETWORK=1 ./scripts/test-check-action-refs.sh

set -euo pipefail

cd "$(dirname "$0")/.."

readonly SCRIPT="./scripts/check-action-refs.sh"

pass_count=0
fail_count=0

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# The fake resolver. Its answers are driven by the ref name alone, so each
# case below is a one-line workflow fixture.
cat >"$work/resolver.sh" <<'EOF'
#!/usr/bin/env bash
repo="$1"; ref="$2"
case "$ref" in
    known-tag)    echo "tag";    exit 0 ;;
    known-branch) echo "branch"; exit 0 ;;
    0123456789abcdef0123456789abcdef01234567) echo "commit"; exit 0 ;;
    missing-ref)  exit 2 ;;
    api-down)     echo "simulated lookup failure for $repo" >&2; exit 1 ;;
esac
exit 2
EOF
chmod +x "$work/resolver.sh"

# run <name> <expected-exit> <expected-substring> <workflow-body...>
run() {
    local name="$1" want_exit="$2" want_text="$3"
    shift 3

    local dir="$work/wf.$$"
    rm -rf "$dir"
    mkdir -p "$dir"
    {
        echo "name: fixture"
        echo "on: push"
        echo "jobs:"
        echo "  j:"
        echo "    runs-on: ubuntu-latest"
        echo "    steps:"
        for line in "$@"; do
            echo "      - uses: $line"
        done
    } >"$dir/fixture.yml"

    local out status
    set +e
    out="$(TRUSTVIAN_WORKFLOW_DIR="$dir" TRUSTVIAN_ACTION_REF_RESOLVER="$work/resolver.sh" \
        bash "$SCRIPT" 2>&1)"
    status=$?
    set -e

    if [ "$status" -ne "$want_exit" ]; then
        printf 'FAIL  %s: exit %d, want %d\n%s\n' "$name" "$status" "$want_exit" "$out" >&2
        fail_count=$((fail_count + 1))
        return
    fi
    if [ -n "$want_text" ] && ! grep -qF -- "$want_text" <<<"$out"; then
        printf 'FAIL  %s: output does not contain %q\n%s\n' "$name" "$want_text" "$out" >&2
        fail_count=$((fail_count + 1))
        return
    fi
    printf 'ok    %s\n' "$name"
    pass_count=$((pass_count + 1))
}

# --- what a ref may resolve to ---------------------------------------
run "existing tag passes"    0 "(tag)"    "owner/action@known-tag"
run "existing branch passes" 0 "(branch)" "owner/action@known-branch"
run "full SHA passes"        0 "(commit)" "owner/action@0123456789abcdef0123456789abcdef01234567"

# --- the rc.1 regression ---------------------------------------------
run "nonexistent ref fails"  1 "has no tag, branch, or commit" "sigstore/cosign-installer@missing-ref"

# --- the CI failure this suite was written after ----------------------
#
# A lookup that cannot be performed must never be reported as a missing
# ref. It still fails the run — it just says something true.
run "lookup failure is UNVERIFIED, not 'does not exist'" 1 "UNVERIFIED" \
    "aquasecurity/trivy-action@api-down"
run "lookup failure does not claim the ref is wrong" 1 "NOT evidence that the ref is wrong" \
    "aquasecurity/trivy-action@api-down"

# --- reference shapes -------------------------------------------------
run "subdirectory action resolves owner/repo" 0 "(tag)" \
    "owner/repo/path/to/action@known-tag"
run "container action is skipped" 0 "(container action)" "docker://alpine:3.22"
run "missing @ref fails"          1 "no @ref"            "owner/action"
run "not owner/repo fails"        1 "not an owner/repo@ref reference" "bare-name@known-tag"

# --- local actions ----------------------------------------------------
#
# A local action path is relative to the repository root — that is how the
# runner resolves it — so the fixture lives there and is removed afterwards.
local_action=".tmp-check-action-refs-fixture"
rm -rf "$local_action"
mkdir -p "$local_action"
{
    echo "name: local fixture"
    echo "runs:"
    echo "  using: composite"
    echo "  steps: []"
} >"$local_action/action.yml"
run "existing local action passes" 0 "(local action)" "./$local_action"
rm -rf "$local_action"

run "missing local action fails" 1 "no action.yml" "./no-such-local-action"

# --- composite actions are scanned too -------------------------------
# A bad ref inside an action fails every workflow that uses the action, so
# TRUSTVIAN_ACTION_DIR is scanned alongside the workflows.
action_wf="$work/action-wf"
action_dir="$work/actions"
mkdir -p "$action_wf" "$action_dir/a"
printf 'name: f\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: actions/checkout@known-tag\n' \
    >"$action_wf/w.yml"
printf 'runs:\n  using: composite\n  steps:\n    - uses: actions/upload-artifact@missing-ref\n' \
    >"$action_dir/a/action.yml"
for want in "1:has no tag, branch, or commit missing-ref" "0:ok:"; do
    set +e
    if [ "${want%%:*}" = 1 ]; then
        out="$(TRUSTVIAN_WORKFLOW_DIR="$action_wf" TRUSTVIAN_ACTION_DIR="$action_dir" \
            TRUSTVIAN_ACTION_REF_RESOLVER="$work/resolver.sh" bash "$SCRIPT" 2>&1)"
    else
        # Without TRUSTVIAN_ACTION_DIR, a test's WORKFLOW_DIR is scanned alone.
        out="$(TRUSTVIAN_WORKFLOW_DIR="$action_wf" \
            TRUSTVIAN_ACTION_REF_RESOLVER="$work/resolver.sh" bash "$SCRIPT" 2>&1)"
    fi
    status=$?
    set -e
    if [ "$status" -eq "${want%%:*}" ] && grep -qF "${want#*:}" <<<"$out"; then
        echo "ok    action directory scanning (exit ${want%%:*})"
        pass_count=$((pass_count + 1))
    else
        printf 'FAIL  action directory scanning: exit %d, want %s\n%s\n' "$status" "$want" "$out" >&2
        fail_count=$((fail_count + 1))
    fi
done

# --- the suite must not pass vacuously --------------------------------
empty="$work/empty"
mkdir -p "$empty"
printf 'name: f\non: push\njobs: {}\n' >"$empty/none.yml"
set +e
out="$(TRUSTVIAN_WORKFLOW_DIR="$empty" bash "$SCRIPT" 2>&1)"
status=$?
set -e
if [ "$status" -eq 1 ] && grep -qF "refusing to pass vacuously" <<<"$out"; then
    echo "ok    no references found is a failure"
    pass_count=$((pass_count + 1))
else
    printf 'FAIL  no references found is a failure: exit %d\n%s\n' "$status" "$out" >&2
    fail_count=$((fail_count + 1))
fi

# --- one real lookup, opt-in ------------------------------------------
if [ "${TRUSTVIAN_TEST_ACTION_REFS_NETWORK:-}" = "1" ]; then
    real="$work/real"
    mkdir -p "$real"
    printf 'name: f\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: sigstore/cosign-installer@v4\n' \
        >"$real/bad.yml"
    set +e
    out="$(TRUSTVIAN_WORKFLOW_DIR="$real" bash "$SCRIPT" 2>&1)"
    status=$?
    set -e
    if [ "$status" -eq 1 ] && grep -qF "has no tag, branch, or commit v4" <<<"$out"; then
        echo "ok    real resolver rejects sigstore/cosign-installer@v4"
        pass_count=$((pass_count + 1))
    else
        printf 'FAIL  real resolver rejects cosign-installer@v4: exit %d\n%s\n' "$status" "$out" >&2
        fail_count=$((fail_count + 1))
    fi

    printf 'name: f\non: push\njobs:\n  j:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: sigstore/cosign-installer@v4.1.2\n' \
        >"$real/good.yml"
    rm "$real/bad.yml"
    set +e
    out="$(TRUSTVIAN_WORKFLOW_DIR="$real" bash "$SCRIPT" 2>&1)"
    status=$?
    set -e
    if [ "$status" -eq 0 ]; then
        echo "ok    real resolver accepts sigstore/cosign-installer@v4.1.2"
        pass_count=$((pass_count + 1))
    else
        printf 'FAIL  real resolver accepts cosign-installer@v4.1.2: exit %d\n%s\n' "$status" "$out" >&2
        fail_count=$((fail_count + 1))
    fi
fi

echo
if [ "$fail_count" -gt 0 ]; then
    printf 'check-action-refs tests: %d passed, %d FAILED\n' "$pass_count" "$fail_count" >&2
    exit 1
fi
printf 'check-action-refs tests: %d passed\n' "$pass_count"
