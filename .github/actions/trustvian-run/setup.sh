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

load_runtime_pin "$here/runtime.env"
go_platform
digest_key="GO_SHA256_$(printf '%s_%s' "$GO_OS" "$GO_ARCH" | tr '[:lower:]' '[:upper:]')"
go_digest="${!digest_key}"

# Shared by every invocation of this action in the job, so a second invocation
# reuses the toolchain, the source and the build cache. Keyed by what was
# pinned, so two pins never share a directory. Resolved physically once, so
# every path below is absolute and under $RUNNER_TEMP.
runner_temp="$(CDPATH='' cd -P -- "$RUNNER_TEMP" >/dev/null && pwd -P)" || fail "RUNNER_TEMP is not a directory"
tooling="$runner_temp/trustvian-run-tooling"
goroot_parent="$tooling/go-$GO_VERSION-$GO_OS-$GO_ARCH"
source_dir="$tooling/source-$TRUSTVIAN_SOURCE_COMMIT"
mkdir -p "$tooling/home"

# Every Go invocation runs through go_isolated (lib.sh): the pinned binary, a
# cleared environment, GOTOOLCHAIN=local, and a working directory of its own.
GO_ISOLATED_BIN="$goroot_parent/go/bin/go"
GO_ISOLATED_TOOLING="$tooling"

# --- The toolchain --------------------------------------------------------

pinned_go_present() {
    [ -x "$GO_ISOLATED_BIN" ] && [ "$(go_isolated "$tooling" env GOVERSION 2>/dev/null)" = "go$GO_VERSION" ]
}

if ! pinned_go_present; then
    archive="go$GO_VERSION.$GO_OS-$GO_ARCH.tar.gz"
    echo "Downloading $archive from go.dev"
    rm -rf "$goroot_parent"
    mkdir -p "$goroot_parent"
    # -q: no .curlrc from the job's HOME; TAR_OPTIONS unset: the archive is
    # extracted with exactly these options.
    curl -q -fsSL --retry 3 --proto '=https' --tlsv1.2 \
        -o "$tooling/$archive" "https://go.dev/dl/$archive" ||
        fail "could not download $archive from go.dev"
    verify_sha256 "$tooling/$archive" "$go_digest"
    env -u TAR_OPTIONS tar -xzf "$tooling/$archive" -C "$goroot_parent"
    rm -f "$tooling/$archive"
fi
pinned_go_present || fail "the downloaded toolchain does not report go$GO_VERSION"

# --- The source -----------------------------------------------------------

# Every git command runs through git_isolated (lib.sh), against
# $source_dir/.git by name: an inherited GIT_DIR, GIT_WORK_TREE or Git
# configuration override can neither redirect it into the consumer's
# repository nor change what is fetched.
if ! source_is_pinned "$source_dir" "$TRUSTVIAN_SOURCE_COMMIT"; then
    echo "Fetching $TRUSTVIAN_SOURCE_COMMIT from $TRUSTVIAN_SOURCE_REPOSITORY"
    fetch_source "$TRUSTVIAN_SOURCE_REPOSITORY" "$TRUSTVIAN_SOURCE_COMMIT" "$source_dir" ||
        fail "could not fetch $TRUSTVIAN_SOURCE_COMMIT from $TRUSTVIAN_SOURCE_REPOSITORY"
fi
source_is_pinned "$source_dir" "$TRUSTVIAN_SOURCE_COMMIT" ||
    fail "the fetched source is not exactly $TRUSTVIAN_SOURCE_COMMIT"

# --- The build ------------------------------------------------------------

bin_dir="$(mktemp -d "$runner_temp/trustvian-run-bin.XXXXXX")"

# go_isolated's cleared environment is what makes this build the pinned one: a
# GOFLAGS, GOPROXY, GONOSUMDB or GOTOOLCHAIN the job set for its own Go code
# cannot change it. Checksums are verified against the public checksum
# database.
#
# CGO_ENABLED=0 and -trimpath match the release build (docs/release-guide.md).
# There is no -ldflags version injection: the binaries carry Go's own build
# information, which records the commit they were built from.
build() {
    local module_dir="$1" package="$2" output="$3"
    go_isolated "$source_dir/$module_dir" build -o "$output" "$package" ||
        fail "building $package from $TRUSTVIAN_SOURCE_COMMIT failed"
}

echo "Building the Trustvian runtime from $TRUSTVIAN_SOURCE_COMMIT with go$GO_VERSION"
build . ./cmd/trustvian "$bin_dir/trustvian"
build platform ./cmd/trustvian-local "$bin_dir/trustvian-local"
build processor ./cmd/trustvian-collector "$bin_dir/trustvian-collector"

# --- Verification ---------------------------------------------------------

# Every binary must say, through its own build information, that it was built
# from the pinned commit with no local modification. This reads what Go
# recorded; it writes nothing into the binaries.
for binary in trustvian trustvian-local trustvian-collector; do
    info="$(go_isolated "$tooling" version -m "$bin_dir/$binary")" || fail "cannot read the build information of $binary"
    printf '%s\n' "$info" | grep -qx "[[:space:]]*build[[:space:]]*vcs.revision=$TRUSTVIAN_SOURCE_COMMIT" ||
        fail "$binary does not record vcs.revision=$TRUSTVIAN_SOURCE_COMMIT"
    printf '%s\n' "$info" | grep -qx "[[:space:]]*build[[:space:]]*vcs.modified=false" ||
        fail "$binary does not record an unmodified source tree"
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
