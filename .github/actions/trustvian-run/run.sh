#!/usr/bin/env bash
#
# trustvian-run, step 2: run the scenario or suite, and preserve its result.
#
#   1. Name the head commit from the event context, before anything runs.
#   2. Start a control plane under $RUNNER_TEMP, unless api-url names one.
#   3. Run `trustvian eval run ... --json` once, with every input passed as a
#      separate argument. stdout is captured as the result document; stderr
#      (progress, and the workload's own output) streams to the job log.
#   4. Stop the control plane this step started — that process, nothing else.
#   5. Write the artifact directory: result.json when there is a document, and
#      trustvian-run.json, the execution metadata, always.
#
# This step computes nothing about the result. It does not read a verdict,
# count a behavior, or compare a number with a limit; the CLI's exit code and
# its document are passed on exactly as they came.
#
# Exit: 0 once the CLI has run and its result is preserved as far as the
# contract allows, whatever the CLI's own code was. 1 for this action's own
# failures, which the annotation names. The CLI's code is never this step's
# exit status: it is the exit-code output, and the finish step exits with it.
#
# Outputs: exit-code, result, head-sha, artifact-dir.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=SCRIPTDIR/lib.sh
. "$here/lib.sh"

refuse_privileged_event
: "${RUNNER_TEMP:?RUNNER_TEMP is not set; this action runs inside a GitHub Actions job}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is not set; this action runs inside a GitHub Actions job}"
: "${TRUSTVIAN_RUN_BIN_DIR:?the setup step did not provide a runtime}"

bin_dir="$TRUSTVIAN_RUN_BIN_DIR"
for binary in trustvian trustvian-local trustvian-collector; do
    [ -x "$bin_dir/$binary" ] || fail "the runtime has no $binary"
done

# --- Inputs ---------------------------------------------------------------

scenario="${INPUT_SCENARIO:-}"
suite="${INPUT_SUITE:-}"
reference="${INPUT_REFERENCE:-}"
scenario_timeout="${INPUT_SCENARIO_TIMEOUT:-}"
fail_fast="${INPUT_FAIL_FAST:-false}"
api_url="${INPUT_API_URL:-}"
working_directory="${INPUT_WORKING_DIRECTORY:-.}"
artifact_name="${INPUT_ARTIFACT_NAME:-trustvian-run}"

# Only the inputs this action itself must interpret are checked here. Every
# other value goes to the CLI unchanged, and the CLI's own usage error — exit
# 2 — is the answer, so the action holds no second copy of its rules.
case "$fail_fast" in
    true | false) ;;
    *) fail "input fail-fast must be true or false" ;;
esac
[[ "$artifact_name" =~ ^[A-Za-z0-9._-]{1,100}$ ]] ||
    fail "input artifact-name must be 1-100 characters of letters, digits, '.', '_' and '-'"
[ -d "$working_directory" ] || fail "input working-directory is not a directory"

if [ -n "$scenario" ] && [ -n "$suite" ]; then
    mode=both
elif [ -n "$suite" ]; then
    mode=suite
elif [ -n "$scenario" ]; then
    mode=scenario
else
    mode=none
fi

# --- Identity -------------------------------------------------------------

resolve_head
set_output head-sha "$HEAD_SHA"

# --- Arguments ------------------------------------------------------------

# One array element per argument, and every value attached to its flag with
# '=': a value that begins with '-' stays that flag's value, and no value is
# ever re-split, globbed or evaluated by a shell.
args=(eval run --json "--collector-bin=$bin_dir/trustvian-collector")
[ -z "$scenario" ] || args+=("--scenario=$scenario")
[ -z "$suite" ] || args+=("--suite=$suite")
[ -z "$reference" ] || args+=("--reference=$reference")
[ -z "$scenario_timeout" ] || args+=("--scenario-timeout=$scenario_timeout")
[ "$fail_fast" != true ] || args+=(--fail-fast)

invocation="$(mktemp -d "$RUNNER_TEMP/trustvian-run.XXXXXX")"
artifact_dir="$invocation/artifact"
mkdir -p "$artifact_dir"

# --- The control plane ----------------------------------------------------

control_plane_pid=""

# stop_control_plane stops the control plane this step started, by the PID
# this step recorded: SIGTERM, which it handles as a clean shutdown, then
# SIGKILL if it is still running after 10s. Not SIGINT: a non-interactive
# shell starts a background process with SIGINT ignored. It touches no other
# process — the Collector each repetition starts is `trustvian dev`'s, and dev
# stops it.
stop_control_plane() {
    [ -n "$control_plane_pid" ] || return 0
    local pid="$control_plane_pid" waited=0
    control_plane_pid=""
    kill -0 "$pid" 2>/dev/null || return 0
    kill -TERM "$pid" 2>/dev/null || true
    while kill -0 "$pid" 2>/dev/null && [ "$waited" -lt 100 ]; do
        sleep 0.1
        waited=$((waited + 1))
    done
    if kill -0 "$pid" 2>/dev/null; then
        warn "the control plane did not stop within 10s of SIGTERM; killing it"
        kill -KILL "$pid" 2>/dev/null || true
    fi
    wait "$pid" 2>/dev/null || true
}
trap stop_control_plane EXIT
trap 'stop_control_plane; exit 130' INT
trap 'stop_control_plane; exit 143' TERM

if [ -n "$api_url" ]; then
    # Attach. The CLI validates the URL and owns the meaning of a bad one.
    control_plane=attached
    args+=("--api-url=$api_url")
else
    control_plane=started
    state_dir="$invocation/control-plane"
    log="$invocation/control-plane.log"
    mkdir -p "$state_dir"
    "$bin_dir/trustvian-local" --state-dir "$state_dir" --listen 127.0.0.1:0 >"$log" 2>&1 &
    control_plane_pid=$!

    # The control plane publishes the endpoint it bound in its state
    # directory, exactly as `make local` does, once it is serving.
    discovered=""
    for _ in $(seq 1 600); do
        if [ -f "$state_dir/runtime.json" ]; then
            discovered="$(jq -r 'if .version == "1" and (.api_url | type) == "string" then .api_url else "" end' \
                "$state_dir/runtime.json" 2>/dev/null || true)"
            [ -z "$discovered" ] || break
        fi
        kill -0 "$control_plane_pid" 2>/dev/null || break
        sleep 0.1
    done
    if ! [[ "$discovered" =~ ^http://127\.0\.0\.1:[0-9]+$ ]]; then
        echo "--- control plane log ---" >&2
        tail -n 50 "$log" >&2 || true
        fail "the control plane did not publish a loopback endpoint within 60s"
    fi
    args+=("--api-url=$discovered")
fi

# --- The run --------------------------------------------------------------

stdout_file="$invocation/stdout"
echo "Running trustvian eval run ($mode) in $working_directory"
set +e
(cd "$working_directory" && exec "$bin_dir/trustvian" "${args[@]}") >"$stdout_file"
cli_exit=$?
set -e
set_output exit-code "$cli_exit"
echo "trustvian exited $cli_exit"

stop_control_plane

# --- Preservation ---------------------------------------------------------

result="$(classify_result "$stdout_file")"
set_output result "$result"

result_bytes=0
result_sha256=""
if [ "$result" = present ]; then
    cp "$stdout_file" "$artifact_dir/result.json"
    result_bytes="$(wc -c <"$artifact_dir/result.json" | tr -d ' ')"
    result_sha256="$(sha256_of "$artifact_dir/result.json")"
fi

jq -n \
    --arg head_sha "$HEAD_SHA" \
    --arg head_source "$HEAD_SOURCE" \
    --arg event "${GITHUB_EVENT_NAME:-}" \
    --arg repository "${GITHUB_REPOSITORY:-}" \
    --arg run_id "${GITHUB_RUN_ID:-}" \
    --arg run_attempt "${GITHUB_RUN_ATTEMPT:-}" \
    --arg mode "$mode" \
    --arg control_plane "$control_plane" \
    --argjson exit_code "$cli_exit" \
    --arg result "$result" \
    --argjson result_bytes "$result_bytes" \
    --arg result_sha256 "$result_sha256" \
    --arg runtime_commit "${TRUSTVIAN_RUN_RUNTIME_COMMIT:-}" \
    --arg go_version "${TRUSTVIAN_RUN_GO_VERSION:-}" \
    '{
      version: "1",
      head: {sha: $head_sha, source: $head_source},
      event: $event,
      repository: $repository,
      run: {id: $run_id, attempt: $run_attempt},
      mode: $mode,
      control_plane: $control_plane,
      cli: {exit_code: $exit_code},
      result: (
        if $result == "present"
        then {status: $result, file: "result.json", bytes: $result_bytes, sha256: $result_sha256}
        else {status: $result}
        end
      ),
      runtime: {source_commit: $runtime_commit, go_version: $go_version}
    }' >"$artifact_dir/trustvian-run.json"

set_output artifact-dir "$artifact_dir"

# A PASS or a FAIL is a verdict, and the CLI always writes its document with
# one (ADR 0033 § 11). Without a document there is no result to preserve, so
# this step fails rather than let the job report what the CLI said with
# nothing behind it. A usage or operational code may come with no document —
# a single scenario writes none — but one that came and cannot be preserved
# is reported, not dropped.
case "$result:$cli_exit" in
    present:*) ;;
    absent:0 | absent:1)
        fail "trustvian exited $cli_exit but wrote no result document"
        ;;
    absent:*) echo "No result document: trustvian exited $cli_exit before producing one" ;;
    oversized:*)
        fail "trustvian exited $cli_exit with a result larger than $TRUSTVIAN_RUN_MAX_RESULT_BYTES bytes; it was not preserved, and was not truncated"
        ;;
    invalid:*)
        fail "trustvian exited $cli_exit with stdout that is not one JSON object; it was not preserved"
        ;;
esac

case "$cli_exit" in
    0 | 1 | 2 | 3) ;;
    *) warn "trustvian exited $cli_exit, outside its documented codes 0-3; it is passed through unchanged" ;;
esac
