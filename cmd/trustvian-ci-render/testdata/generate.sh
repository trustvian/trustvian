#!/usr/bin/env bash
#
# Regenerates testdata/artifacts: real trustvian-run artifacts, produced by the
# action's own run.sh driving the real CLI, control plane and Collector against
# the model-free agent-producer workload. Nothing here is hand-written; the
# renderer's tests read these files as the producers wrote them.
#
# Run from the repository root, with the four binaries built from this
# checkout:
#
#   go build -o "$BIN/trustvian" ./cmd/trustvian
#   (cd platform && GOWORK=off go build -o "$BIN/trustvian-local" ./cmd/trustvian-local)
#   (cd processor && GOWORK=off go build -o "$BIN/trustvian-collector" ./cmd/trustvian-collector)
#   (cd processor && GOWORK=off go build -o "$RUNNER_TEMP/agent-producer" ./cmd/agent-producer)
#   TRUSTVIAN_RUN_BIN_DIR="$BIN" RUNNER_TEMP=... cmd/trustvian-ci-render/testdata/generate.sh
#
# Every case shares one job identity — expectedContext in the tests.

set -euo pipefail

: "${TRUSTVIAN_RUN_BIN_DIR:?the directory holding trustvian, trustvian-local and trustvian-collector}"
: "${RUNNER_TEMP:?a scratch directory holding agent-producer}"
[ -x "$RUNNER_TEMP/agent-producer" ] || { echo "no $RUNNER_TEMP/agent-producer" >&2; exit 1; }

here=cmd/trustvian-ci-render/testdata
out="$here/artifacts"
scenarios="$here/scenarios"
[ -d "$scenarios" ] || { echo "run from the repository root" >&2; exit 1; }

readonly head_sha=0123456789abcdef0123456789abcdef01234567
event="$RUNNER_TEMP/event.json"
printf '{"pull_request":{"number":7,"head":{"sha":"%s"}}}\n' "$head_sha" >"$event"

go_version="$(go env GOVERSION)"
export GITHUB_EVENT_NAME=pull_request GITHUB_EVENT_PATH="$event" \
    GITHUB_SHA=fedcba9876543210fedcba9876543210fedcba98 \
    GITHUB_REPOSITORY=trustvian/trustvian GITHUB_RUN_ID=1000000001 GITHUB_RUN_ATTEMPT=1 \
    TRUSTVIAN_RUN_RUNTIME_COMMIT=3e86f9cead2586e7ee90e14185a8f34074b54240 \
    TRUSTVIAN_RUN_GO_VERSION="$go_version" TRUSTVIAN_RUN_BIN_DIR RUNNER_TEMP

# One control plane for every attached case, so a later case can reuse an
# earlier one's recorded reference side.
state="$(mktemp -d "$RUNNER_TEMP/control-plane.XXXXXX")"
"$TRUSTVIAN_RUN_BIN_DIR/trustvian-local" --state-dir "$state" --listen 127.0.0.1:0 >"$state.log" 2>&1 &
cp_pid=$!
trap 'kill "$cp_pid" 2>/dev/null || true; wait "$cp_pid" 2>/dev/null || true' EXIT
api_url=""
for _ in $(seq 1 600); do
    api_url="$(jq -r '.api_url // empty' "$state/runtime.json" 2>/dev/null || true)"
    [ -z "$api_url" ] || break
    sleep 0.1
done
[ -n "$api_url" ] || { echo "the control plane did not start" >&2; exit 1; }

# run_case NAME [INPUT=VALUE...] runs run.sh once and keeps its artifact
# directory as $out/NAME.
run_case() {
    local name="$1" output artifact_dir
    shift
    output="$(mktemp "$RUNNER_TEMP/output.XXXXXX")"
    echo "== $name" >&2
    env GITHUB_OUTPUT="$output" INPUT_WORKING_DIRECTORY=. INPUT_FAIL_FAST=false \
        INPUT_ARTIFACT_NAME="$name" "$@" bash .github/actions/trustvian-run/run.sh >&2 || true
    artifact_dir="$(sed -n 's/^artifact-dir=//p' "$output")"
    [ -n "$artifact_dir" ] || { echo "$name produced no artifact" >&2; exit 1; }
    rm -rf "${out:?}/$name"
    mkdir -p "$out/$name"
    cp "$artifact_dir"/* "$out/$name/"
}

run_case pass-started INPUT_SCENARIO="$scenarios/pass.yaml"
run_case pass INPUT_SCENARIO="$scenarios/pass.yaml" INPUT_API_URL="$api_url"
run_case pass-reused INPUT_SCENARIO="$scenarios/pass.yaml" INPUT_REFERENCE=last INPUT_API_URL="$api_url"
run_case fail INPUT_SCENARIO="$scenarios/fail.yaml" INPUT_API_URL="$api_url"
run_case removed INPUT_SCENARIO="$scenarios/removed.yaml" INPUT_API_URL="$api_url"
run_case single-run INPUT_SCENARIO="$scenarios/single-run.yaml" INPUT_API_URL="$api_url"
run_case operational INPUT_SCENARIO=scripts/testdata/trustvian-run/operational.yaml INPUT_API_URL="$api_url"
run_case usage INPUT_SCENARIO="$scenarios/does-not-exist.yaml" INPUT_API_URL="$api_url"
run_case suite INPUT_SUITE="$scenarios/suite" INPUT_SCENARIO_TIMEOUT=5m INPUT_API_URL="$api_url"
run_case suite-fail-fast INPUT_SUITE="$scenarios/suite-fail-fast" INPUT_SCENARIO_TIMEOUT=5m \
    INPUT_FAIL_FAST=true INPUT_API_URL="$api_url"
run_case suite-error INPUT_SUITE="$scenarios/suite-error" INPUT_SCENARIO_TIMEOUT=5m INPUT_API_URL="$api_url"

echo "artifacts written to $out" >&2
