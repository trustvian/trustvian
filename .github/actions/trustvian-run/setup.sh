#!/usr/bin/env bash
#
# trustvian-run, step 1: provide the runtime.
#
# Builds `trustvian`, `trustvian-local` and `trustvian-collector` from the one
# reviewed commit runtime.env pins, with the Go toolchain it pins, entirely
# under $RUNNER_TEMP. Nothing is written to the workload's checkout, nothing is
# added to the job's PATH, and no Go installation the job already has is used
# or changed — see docs/adr/0056-the-run-action-builds-a-pinned-source-commit.md.
#
# Outputs: bin-dir, runtime-commit, runtime-version, go-version.

set -euo pipefail

here="$(CDPATH='' cd -P -- "$(dirname "${BASH_SOURCE[0]}")" >/dev/null && pwd -P)"
# shellcheck source=SCRIPTDIR/lib.sh
. "$here/lib.sh"

refuse_privileged_event
: "${RUNNER_TEMP:?RUNNER_TEMP is not set; this action runs inside a GitHub Actions job}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set; this action runs inside a GitHub Actions job}"

for tool in git jq curl tar uname; do
    command -v "$tool" >/dev/null 2>&1 || fail "the runner has no $tool, which this action needs"
done
command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 ||
    fail "the runner has neither sha256sum nor shasum"

# The pinned toolchain and source, under $RUNNER_TEMP (lib.sh): the toolchain
# checked against go.dev's digest, the source fetched by commit into its own
# directory and refused if it differs from the pin.
provide_pinned_source "$here/runtime.env"

# --- The build ------------------------------------------------------------

bin_dir="$(mktemp -d "$TRUSTVIAN_RUNNER_TEMP/trustvian-run-bin.XXXXXX")"

echo "Building the Trustvian runtime from $TRUSTVIAN_SOURCE_COMMIT with go$GO_VERSION"
build_pinned . ./cmd/trustvian "$bin_dir/trustvian"
build_pinned platform ./cmd/trustvian-local "$bin_dir/trustvian-local"
build_pinned processor ./cmd/trustvian-collector "$bin_dir/trustvian-collector"

# --- Verification ---------------------------------------------------------

for binary in trustvian trustvian-local trustvian-collector; do
    verify_pinned_build "$bin_dir/$binary"
done

# The commands this action runs must exist in the binary it built. A binary
# without them answers "unknown command" — exit 2, like a usage error — so the
# check reads the answer rather than the exit status.
eval_run_usage="$("$bin_dir/trustvian" eval run 2>&1 >/dev/null || true)"
case "$eval_run_usage" in
    *'unknown command'* | *'unknown subcommand'*) fail "the pinned trustvian has no 'eval run' command" ;;
esac
case "$eval_run_usage" in
    *'--suite'*'--scenario-timeout'*) ;;
    *) fail "the pinned trustvian's 'eval run' does not offer --suite and --scenario-timeout" ;;
esac

runtime_version="$("$bin_dir/trustvian" version)" || fail "the pinned trustvian cannot report its version"
runtime_version="${runtime_version%%$'\n'*}"
echo "Runtime: $runtime_version ($TRUSTVIAN_SOURCE_COMMIT)"

set_output bin-dir "$bin_dir"
set_output runtime-commit "$TRUSTVIAN_SOURCE_COMMIT"
set_output runtime-version "$runtime_version"
set_output go-version "go$GO_VERSION"
