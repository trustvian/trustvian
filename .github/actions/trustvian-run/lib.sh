# shellcheck shell=bash
#
# Shared functions for the trustvian-run action's steps, and for the
# trustvian-comment action's, which sources this file through
# $GITHUB_ACTION_PATH/../trustvian-run/lib.sh. Sourced, never run.
#
# TRUSTVIAN_ACTION names the action in every annotation. It is set here, never
# inherited from the job's environment, and the comment action's scripts set
# it to trustvian-comment after sourcing.
#
# Written for bash 3.2 as well as current bash, because a macOS runner's
# /bin/bash is 3.2: no associative arrays, no mapfile, no ${var,,}.
#
# scripts/trustvian_run_action_test.go sources this file and exercises every
# function below directly.

# The largest stdout this action accepts as a result document: the CLI's own
# 32 MiB cap on a suite document (ADR 0055 § 5), plus 64 KiB for its framing.
# Anything larger is refused whole — never truncated, because a truncated
# document is a different document.
readonly TRUSTVIAN_RUN_MAX_RESULT_BYTES=$((32 * 1024 * 1024 + 64 * 1024))

# A summary value is capped at this many bytes before it is rendered.
readonly TRUSTVIAN_RUN_MAX_SUMMARY_VALUE=256

TRUSTVIAN_ACTION=trustvian-run

# fail prints a workflow error annotation and exits 1.
#
# Exit 1 here is the action's own failure — setup, input, or preservation —
# and it is never the CLI's code: whenever the CLI ran, its exact code is in
# the exit-code output and the finish step exits with it.
fail() {
    printf '::error title=%s::%s\n' "$TRUSTVIAN_ACTION" "$(annotation_text "$*")"
    exit 1
}

warn() {
    printf '::warning title=%s::%s\n' "$TRUSTVIAN_ACTION" "$(annotation_text "$*")"
}

# annotation_text makes a message safe to place in a workflow command: a
# newline would end the command early, and '%' starts an escape.
annotation_text() {
    printf '%s' "$1" | LC_ALL=C tr -d '\000-\011\013-\037\177' |
        sed -e 's/%/%25/g' | awk 'BEGIN { ORS = "%0A" } { print }' | sed -e 's/%0A$//'
}

# set_output appends one name=value line to $GITHUB_OUTPUT. Every value this
# action outputs is single-line by construction; a value that is not is
# refused rather than written, because a newline would let it forge a second
# output.
set_output() {
    case "$2" in
        *$'\n'* | *$'\r'*) fail "internal: output $1 is not a single line" ;;
    esac
    printf '%s=%s\n' "$1" "$2" >>"$GITHUB_OUTPUT"
}

# load_runtime_pin reads runtime.env into TRUSTVIAN_SOURCE_REPOSITORY,
# TRUSTVIAN_SOURCE_COMMIT, GO_VERSION and GO_SHA256_<OS>_<ARCH>.
#
# A strict reader rather than `source`: the file is data, and every key and
# value shape is checked, so a malformed pin fails before anything is fetched.
load_runtime_pin() {
    local file="$1" line key value seen=""
    [ -f "$file" ] || fail "runtime pin $file is missing"
    while IFS= read -r line || [ -n "$line" ]; do
        case "$line" in '' | '#'*) continue ;; esac
        key="${line%%=*}"
        value="${line#*=}"
        [ "$key" != "$line" ] || fail "runtime pin: malformed line"
        case "$key" in
            TRUSTVIAN_SOURCE_REPOSITORY)
                [[ "$value" =~ ^https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+\.git$ ]] ||
                    fail "runtime pin: $key must be an https github.com repository URL"
                ;;
            TRUSTVIAN_SOURCE_COMMIT)
                [[ "$value" =~ ^[0-9a-f]{40}$ ]] ||
                    fail "runtime pin: $key must be a full 40-character commit"
                ;;
            GO_VERSION)
                [[ "$value" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
                    fail "runtime pin: $key must be an exact version such as 1.27.1"
                ;;
            GO_SHA256_LINUX_AMD64 | GO_SHA256_LINUX_ARM64 | GO_SHA256_DARWIN_AMD64 | GO_SHA256_DARWIN_ARM64)
                [[ "$value" =~ ^[0-9a-f]{64}$ ]] ||
                    fail "runtime pin: $key must be a SHA-256 digest"
                ;;
            *) fail "runtime pin: unknown key $key" ;;
        esac
        case " $seen " in *" $key "*) fail "runtime pin: $key is set twice" ;; esac
        seen="$seen $key"
        printf -v "$key" '%s' "$value"
    done <"$file"
    for key in TRUSTVIAN_SOURCE_REPOSITORY TRUSTVIAN_SOURCE_COMMIT GO_VERSION \
        GO_SHA256_LINUX_AMD64 GO_SHA256_LINUX_ARM64 GO_SHA256_DARWIN_AMD64 GO_SHA256_DARWIN_ARM64; do
        case " $seen " in *" $key "*) ;; *) fail "runtime pin: $key is missing" ;; esac
    done
}

# go_platform sets GO_OS and GO_ARCH for this runner, or fails. Windows is
# refused: `trustvian dev` refuses to start there, so the run action has
# nothing to run, and the comment action follows it rather than pinning a
# third toolchain archive for a job that would only ever post a no-verdict.
#
# Like every function here that can fail, it sets variables rather than
# printing: fail inside a command substitution would exit only the subshell.
# shellcheck disable=SC2034 # GO_OS and GO_ARCH are read by the caller.
go_platform() {
    case "$(uname -s)" in
        Linux) GO_OS=linux ;;
        Darwin) GO_OS=darwin ;;
        *) fail "unsupported runner OS $(uname -s): $TRUSTVIAN_ACTION runs on Linux and macOS runners only" ;;
    esac
    case "$(uname -m)" in
        x86_64 | amd64) GO_ARCH=amd64 ;;
        aarch64 | arm64) GO_ARCH=arm64 ;;
        *) fail "unsupported runner architecture $(uname -m)" ;;
    esac
}

# sha256_of prints a file's SHA-256 digest.
sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{ print $1 }'
    else
        shasum -a 256 "$1" | awk '{ print $1 }'
    fi
}

# verify_sha256 fails unless a file's digest is exactly the expected one.
verify_sha256() {
    local actual
    actual="$(sha256_of "$1")"
    [ "$actual" = "$2" ] || fail "checksum mismatch for $(basename "$1"): expected $2, got $actual"
}

# refuse_privileged_event fails on an event that runs in the base repository's
# context. Those events hand the job a write-scoped token and repository
# secrets. The run action executes the workload — the pull request's own code;
# the comment action does not, but it belongs to the same workflow as a run
# job that does, so under either event that run job would hold the same
# privileges.
refuse_privileged_event() {
    local why="this action executes the workload"
    [ "$TRUSTVIAN_ACTION" = trustvian-run ] ||
        why="the run job of the same workflow executes the pull request's code"
    case "${GITHUB_EVENT_NAME:-}" in
        pull_request_target | workflow_run)
            fail "refusing to run on ${GITHUB_EVENT_NAME}: that event runs with base-repository privileges, and $why. Use pull_request — see docs/ci-github-action.md"
            ;;
    esac
}

# resolve_head sets HEAD_SHA and HEAD_SOURCE: the commit this run describes,
# read from the event context, never computed.
#
# On a pull_request event that is the pull request's head commit. github.sha
# on that event is the ephemeral merge commit, which names nothing the author
# can check out, so it is never used there — a pull_request event without a
# well-formed head commit is an error, not a fallback. On any other event
# github.sha is the commit the run is for.
resolve_head() {
    HEAD_SHA=""
    case "${GITHUB_EVENT_NAME:-}" in
        pull_request)
            HEAD_SOURCE="event.pull_request.head.sha"
            { [ -n "${GITHUB_EVENT_PATH:-}" ] && [ -f "$GITHUB_EVENT_PATH" ]; } ||
                fail "pull_request event without an event payload; cannot name the head commit"
            HEAD_SHA="$(jq -r 'if (.pull_request.head.sha | type) == "string" then .pull_request.head.sha else "" end' \
                "$GITHUB_EVENT_PATH" 2>/dev/null)" || HEAD_SHA=""
            ;;
        '')
            fail "GITHUB_EVENT_NAME is not set; this action runs inside a GitHub Actions job"
            ;;
        *)
            HEAD_SOURCE="github.sha"
            HEAD_SHA="${GITHUB_SHA:-}"
            ;;
    esac
    [[ "$HEAD_SHA" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] ||
        fail "cannot name the head commit: $HEAD_SOURCE is not a full commit"
}

# classify_result prints present, absent, invalid or oversized for a captured
# stdout file. present means exactly one JSON object, within the size bound.
#
# Syntax only. The result document's fields are the next reporting slice's
# strict decoder's concern; this step decides whether there is a document to
# preserve at all, and never repairs one.
classify_result() {
    local file="$1" bytes
    [ -f "$file" ] || {
        echo absent
        return
    }
    bytes="$(wc -c <"$file" | tr -d ' ')"
    if [ "$bytes" -eq 0 ]; then
        echo absent
    elif [ "$bytes" -gt "$TRUSTVIAN_RUN_MAX_RESULT_BYTES" ]; then
        echo oversized
    elif jq -e -s 'length == 1 and (.[0] | type) == "object"' "$file" >/dev/null 2>&1; then
        echo present
    else
        echo invalid
    fi
}

# md_inert renders an untrusted string inert for a markdown job summary, and
# bounded: control characters (newlines included) are dropped, the value is
# cut at max bytes on a character boundary with the cut stated, and every
# ASCII punctuation character is backslash-escaped, so no link, image, HTML
# tag, table cell, code span or @mention can form.
md_inert() {
    local value="$1" max="${2:-$TRUSTVIAN_RUN_MAX_SUMMARY_VALUE}" cut
    value="$(printf '%s' "$value" | LC_ALL=C tr -d '\000-\037\177')"
    if [ "$(printf '%s' "$value" | LC_ALL=C wc -c | tr -d ' ')" -gt "$max" ]; then
        # Byte cut, then drop a trailing partial UTF-8 sequence: iconv -c
        # discards what does not decode.
        cut="$(printf '%s' "$value" | LC_ALL=C head -c "$max" | iconv -c -f UTF-8 -t UTF-8 2>/dev/null)" ||
            cut="$(printf '%s' "$value" | LC_ALL=C head -c "$max" | LC_ALL=C tr -cd '\040-\176')"
        value="$cut … (truncated)"
    fi
    printf '%s' "$value" | LC_ALL=C sed -e 's/[][!"#$%&'\''()*+,./:;<=>?@\\^_`{|}~-]/\\&/g'
}

# git_isolated runs git with every inherited GIT_* variable removed and no
# user or system configuration.
#
# The job's environment belongs to the consumer: a GIT_DIR or GIT_WORK_TREE
# there would point init, fetch and checkout at the consumer's own repository,
# and GIT_CONFIG_PARAMETERS, GIT_CONFIG_COUNT or a url.insteadOf rewrite would
# change what is fetched. Callers name the repository explicitly
# (--git-dir/--work-tree), so nothing is found by discovery either.
git_isolated() {
    (
        for name in $(env | sed -n 's/^\(GIT_[A-Za-z0-9_]*\)=.*/\1/p'); do
            unset "$name" 2>/dev/null || true
        done
        export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0
        exec git "$@"
    )
}

# source_git DIR ARGS... runs git_isolated against the checkout in DIR alone.
source_git() {
    local dir="$1"
    shift
    git_isolated --git-dir="$dir/.git" --work-tree="$dir" "$@"
}

# fetch_source URL COMMIT DIR replaces DIR with a checkout of exactly COMMIT,
# fetched at depth one from URL. Returns non-zero on any failure; the caller
# reports it.
fetch_source() {
    local url="$1" commit="$2" dir="$3"
    rm -rf -- "$dir"
    git_isolated init -q --template= -- "$dir" || return 1
    source_git "$dir" fetch -q --depth 1 --no-tags "$url" "$commit" || return 1
    source_git "$dir" -c advice.detachedHead=false checkout -q --detach FETCH_HEAD || return 1
}

# source_is_pinned DIR COMMIT succeeds when DIR is a checkout of exactly
# COMMIT, whose repository is DIR/.git itself, with nothing modified or added.
source_is_pinned() {
    local dir="$1" commit="$2"
    [ -d "$dir/.git" ] &&
        [ "$(source_git "$dir" rev-parse --absolute-git-dir 2>/dev/null)" = \
            "$(CDPATH='' cd -P -- "$dir/.git" >/dev/null 2>&1 && pwd -P)" ] &&
        [ "$(source_git "$dir" rev-parse HEAD 2>/dev/null)" = "$commit" ] &&
        [ -z "$(source_git "$dir" status --porcelain --untracked-files=all 2>/dev/null)" ]
}

# go_isolated DIR ARGS... runs the pinned toolchain, $GO_ISOLATED_BIN, in DIR
# with an environment built from nothing, caches under $GO_ISOLATED_TOOLING.
#
# Every Go invocation goes through here — the version checks, the builds and
# the build-information inspection — because each reads the environment: a
# GOTOOLCHAIN the job set (go1.27.0+path, say) makes even `go env` look for a
# different toolchain, and GOFLAGS, GOENV, GOWORK, GOPROXY or GONOSUMDB would
# change a build. GOTOOLCHAIN=local means the pinned toolchain runs itself and
# never switches, whatever a go.mod or go.env says. Nothing here touches the
# job's own environment or PATH.
go_isolated() {
    local dir="$1" goroot git_bin
    shift
    goroot="$(dirname "$(dirname "$GO_ISOLATED_BIN")")"
    git_bin="$(command -v git)" || git_bin=/usr/bin/git
    (
        CDPATH='' cd -P -- "$dir" >/dev/null || exit 1
        exec env -i \
            HOME="$GO_ISOLATED_TOOLING/home" \
            PATH="$(dirname "$git_bin"):/usr/bin:/bin" \
            GOROOT="$goroot" \
            GOPATH="$GO_ISOLATED_TOOLING/gopath" \
            GOMODCACHE="$GO_ISOLATED_TOOLING/gomodcache" \
            GOCACHE="$GO_ISOLATED_TOOLING/gocache" \
            GOENV=off \
            GOTOOLCHAIN=local \
            GOWORK=off \
            GOFLAGS="-trimpath -mod=readonly" \
            GOPROXY="https://proxy.golang.org,direct" \
            GOSUMDB="sum.golang.org" \
            CGO_ENABLED=0 \
            GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_TERMINAL_PROMPT=0 \
            "$GO_ISOLATED_BIN" "$@"
    )
}

# provide_pinned_source PIN_FILE provides the pinned toolchain and the pinned
# source, both under $RUNNER_TEMP, and sets:
#
#   TRUSTVIAN_RUNNER_TEMP $RUNNER_TEMP, resolved to an absolute physical path
#   TRUSTVIAN_TOOLING    the shared tooling directory
#   TRUSTVIAN_SOURCE_DIR the checkout of exactly $TRUSTVIAN_SOURCE_COMMIT
#   GO_ISOLATED_BIN, GO_ISOLATED_TOOLING   for go_isolated
#
# plus everything load_runtime_pin and go_platform set. Fails on any problem.
#
# The tooling directory is shared by every invocation of either action in one
# job, so a second invocation reuses the toolchain, the source and the build
# cache. It is keyed by what was pinned, so two pins never share a directory,
# and resolved physically once, so every path is absolute and under
# $RUNNER_TEMP.
provide_pinned_source() {
    local pin="$1" digest_key go_digest goroot_parent archive
    load_runtime_pin "$pin"
    go_platform
    digest_key="GO_SHA256_$(printf '%s_%s' "$GO_OS" "$GO_ARCH" | tr '[:lower:]' '[:upper:]')"
    go_digest="${!digest_key}"

    TRUSTVIAN_RUNNER_TEMP="$(CDPATH='' cd -P -- "$RUNNER_TEMP" >/dev/null && pwd -P)" ||
        fail "RUNNER_TEMP is not a directory"
    TRUSTVIAN_TOOLING="$TRUSTVIAN_RUNNER_TEMP/trustvian-run-tooling"
    goroot_parent="$TRUSTVIAN_TOOLING/go-$GO_VERSION-$GO_OS-$GO_ARCH"
    TRUSTVIAN_SOURCE_DIR="$TRUSTVIAN_TOOLING/source-$TRUSTVIAN_SOURCE_COMMIT"
    mkdir -p "$TRUSTVIAN_TOOLING/home"

    # Every Go invocation runs through go_isolated: the pinned binary, a
    # cleared environment, GOTOOLCHAIN=local, and a working directory of its
    # own.
    GO_ISOLATED_BIN="$goroot_parent/go/bin/go"
    GO_ISOLATED_TOOLING="$TRUSTVIAN_TOOLING"

    # --- The toolchain, checked against go.dev's published digest.
    if ! pinned_go_present; then
        archive="go$GO_VERSION.$GO_OS-$GO_ARCH.tar.gz"
        echo "Downloading $archive from go.dev"
        rm -rf "$goroot_parent"
        mkdir -p "$goroot_parent"
        # -q: no .curlrc from the job's HOME; TAR_OPTIONS unset: the archive
        # is extracted with exactly these options.
        curl -q -fsSL --retry 3 --proto '=https' --tlsv1.2 \
            -o "$TRUSTVIAN_TOOLING/$archive" "https://go.dev/dl/$archive" ||
            fail "could not download $archive from go.dev"
        verify_sha256 "$TRUSTVIAN_TOOLING/$archive" "$go_digest"
        env -u TAR_OPTIONS tar -xzf "$TRUSTVIAN_TOOLING/$archive" -C "$goroot_parent"
        rm -f "$TRUSTVIAN_TOOLING/$archive"
    fi
    pinned_go_present || fail "the downloaded toolchain does not report go$GO_VERSION"

    # --- The source. Every git command runs through git_isolated, against
    # the source's own .git by name: an inherited GIT_DIR, GIT_WORK_TREE or
    # Git configuration override can neither redirect it into the consumer's
    # repository nor change what is fetched.
    if ! source_is_pinned "$TRUSTVIAN_SOURCE_DIR" "$TRUSTVIAN_SOURCE_COMMIT"; then
        echo "Fetching $TRUSTVIAN_SOURCE_COMMIT from $TRUSTVIAN_SOURCE_REPOSITORY"
        fetch_source "$TRUSTVIAN_SOURCE_REPOSITORY" "$TRUSTVIAN_SOURCE_COMMIT" "$TRUSTVIAN_SOURCE_DIR" ||
            fail "could not fetch $TRUSTVIAN_SOURCE_COMMIT from $TRUSTVIAN_SOURCE_REPOSITORY"
    fi
    source_is_pinned "$TRUSTVIAN_SOURCE_DIR" "$TRUSTVIAN_SOURCE_COMMIT" ||
        fail "the fetched source is not exactly $TRUSTVIAN_SOURCE_COMMIT"
}

# pinned_go_present succeeds when the pinned toolchain is installed and
# reports the pinned version.
pinned_go_present() {
    [ -x "$GO_ISOLATED_BIN" ] &&
        [ "$(go_isolated "$TRUSTVIAN_TOOLING" env GOVERSION 2>/dev/null)" = "go$GO_VERSION" ]
}

# build_pinned MODULE_DIR PACKAGE OUTPUT builds one package of the pinned
# source with the pinned toolchain, or fails.
#
# go_isolated's cleared environment is what makes this build the pinned one: a
# GOFLAGS, GOPROXY, GONOSUMDB or GOTOOLCHAIN the job set for its own Go code
# cannot change it. Checksums are verified against the public checksum
# database. CGO_ENABLED=0 and -trimpath match the release build
# (docs/release-guide.md). There is no -ldflags version injection: the binary
# carries Go's own build information, which records the commit it was built
# from.
build_pinned() {
    local module_dir="$1" package="$2" output="$3"
    go_isolated "$TRUSTVIAN_SOURCE_DIR/$module_dir" build -o "$output" "$package" ||
        fail "building $package from $TRUSTVIAN_SOURCE_COMMIT failed"
}

# verify_pinned_build BINARY fails unless the binary says, through its own
# build information, that it was built from the pinned commit with no local
# modification. This reads what Go recorded; it writes nothing.
verify_pinned_build() {
    local binary="$1" info
    info="$(go_isolated "$TRUSTVIAN_TOOLING" version -m "$binary")" ||
        fail "cannot read the build information of $(basename "$binary")"
    printf '%s\n' "$info" | grep -qx "[[:space:]]*build[[:space:]]*vcs.revision=$TRUSTVIAN_SOURCE_COMMIT" ||
        fail "$(basename "$binary") does not record vcs.revision=$TRUSTVIAN_SOURCE_COMMIT"
    printf '%s\n' "$info" | grep -qx "[[:space:]]*build[[:space:]]*vcs.modified=false" ||
        fail "$(basename "$binary") does not record an unmodified source tree"
}
