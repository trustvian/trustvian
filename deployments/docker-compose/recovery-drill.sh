#!/usr/bin/env bash
#
# Recovery drill for the Trustvian reference Docker Compose deployment.
# Exits 0 only if every step below actually held.
#
#   ./recovery-drill.sh
#
# This is the procedure in docs/operations.md § Recovery drill, executed
# against the real runtime (the Collector with the Trustvian processor)
# rather than against the storage layer alone:
#
#   1. Start the stack; the runtime becomes ready.
#   2. Learn N observations.
#   3. Take an ONLINE backup with scripts/backup-postgres.sh while the runtime
#      keeps running; check permissions, checksums, and that no credential is
#      in the manifest or output.
#   4. Prove the restore guards: a corrupted copy and the live database are
#      both refused, and the live database is untouched.
#   5. Restore into a NEW database with scripts/restore-postgres.sh.
#   6. Cut the runtime over to the restored database — the one explicit
#      operator action, TRUSTVIAN_RUNTIME_POSTGRES_DB — and require /livez and
#      /readyz to return 200.
#   7. Send N more observations: the restored database must reach 2N (learning
#      CONTINUED from the backup, not restarted) while the original stays at N.
#   8. Task 042 regression on the restored runtime: a PostgreSQL outage makes it
#      not ready but still live, and recovery makes it ready with no restart.
#
# The PostgreSQL client tools run in a postgres:17 container on the Compose
# network, so the host needs only Docker, curl, and bash — and the tools'
# major version matches the server by construction.
#
# It leaves the environment clean: the stack's volume and the backup directory
# are removed at the end.

set -euo pipefail

cd "$(dirname "$0")"
REPO_ROOT="$(cd ../.. && pwd)"

COMPOSE="docker compose"
SPANS=40
DB_USER="${TRUSTVIAN_POSTGRES_USER:-trustvian}"
DB_NAME="${TRUSTVIAN_POSTGRES_DB:-trustvian}"
DB_PASSWORD="${TRUSTVIAN_POSTGRES_PASSWORD:-change-me}"
RESTORED_DB="trustvian_restored"
HEALTH="http://127.0.0.1:${TRUSTVIAN_HEALTH_PORT:-13133}"
TOOLS_IMAGE="postgres:17-bookworm"
NETWORK="trustvian-reference_default"

step() { printf '\n=== %s\n' "$1"; }
ok()   { printf '    ok: %s\n' "$1"; }
fail() { printf '\nFAIL: %s\n' "$1" >&2; exit 1; }

psql_on() {
    $COMPOSE exec -T postgres psql -U "$DB_USER" -d "$1" -tAc "$2" | tr -d '[:space:]'
}

observations_in() {
    psql_on "$1" "SELECT coalesce(sum(observation_count),0) FROM trustvian_baseline"
}

status_of() {
    curl -s -o /dev/null -w '%{http_code}' "$HEALTH$1" 2>/dev/null || true
}

wait_status() {
    # $1 path, $2 wanted status, $3 seconds
    local got=""
    for _ in $(seq 1 "$3"); do
        got="$(status_of "$1")"
        [ "$got" = "$2" ] && return 0
        sleep 1
    done
    fail "$1 returned '${got:-no response}', want $2"
}

# Run a repository script with the PostgreSQL client tools, on the Compose
# network. The password reaches the container by NAME (-e PGPASSWORD), never
# as a literal on a command line.
tools() {
    PGPASSWORD="$DB_PASSWORD" docker run --rm \
        --network "$NETWORK" \
        --user "$(id -u):$(id -g)" \
        -v "$REPO_ROOT/scripts:/scripts:ro" \
        -v "$BACKUP_ROOT:/backups" \
        -e PGHOST=postgres -e PGUSER="$DB_USER" -e PGPASSWORD \
        "$@"
}

BACKUP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/trustvian-drill.XXXXXX")"

finish() {
    status=$?
    if [ $status -ne 0 ]; then
        printf '\n--- collector logs (non-span lines, last 20) ---\n' >&2
        $COMPOSE logs otel-collector 2>&1 | grep -v 'ResourceSpans\|"msg":"Traces"' | tail -n 20 >&2 || true
    fi
    $COMPOSE down -v >/dev/null 2>&1 || true
    rm -rf "$BACKUP_ROOT"
}
trap finish EXIT

# ---------------------------------------------------------------------
step "0/8 Clean slate"
$COMPOSE down -v >/dev/null 2>&1 || true
docker pull -q "$TOOLS_IMAGE" >/dev/null
ok "no pre-existing volume; tools image $TOOLS_IMAGE present"

# ---------------------------------------------------------------------
step "1/8 Stack starts and the runtime becomes ready"
$COMPOSE up -d --build >/dev/null 2>&1 || fail "docker compose up failed"
wait_status /livez 200 60
wait_status /readyz 200 60
ok "/livez 200, /readyz 200"

# ---------------------------------------------------------------------
step "2/8 Learn $SPANS observations"
TRUSTVIAN_DEMO_SPAN_COUNT=$SPANS $COMPOSE run --rm demo-producer >/dev/null 2>&1 \
    || fail "demo-producer failed"
learned="$(observations_in "$DB_NAME")"
[ "$learned" = "$SPANS" ] || fail "observation_count is $learned, want $SPANS"
ok "$learned observations in '$DB_NAME'"

# ---------------------------------------------------------------------
step "3/8 Online backup while the runtime keeps running"
backup_out="$(tools -e PGDATABASE="$DB_NAME" "$TOOLS_IMAGE" \
    bash /scripts/backup-postgres.sh --output /backups/drill 2>&1)" \
    || fail "backup failed: $backup_out"
[ "$(status_of /readyz)" = "200" ] || fail "runtime stopped being ready during the backup"

for f in trustvian.dump MANIFEST SHA256SUMS; do
    [ -f "$BACKUP_ROOT/drill/$f" ] || fail "backup is missing $f"
    mode="$(stat -c '%a' "$BACKUP_ROOT/drill/$f" 2>/dev/null || stat -f '%Lp' "$BACKUP_ROOT/drill/$f")"
    [ "$mode" = "600" ] || fail "$f has mode $mode, want 600"
done
(cd "$BACKUP_ROOT/drill" && { sha256sum -c SHA256SUMS 2>/dev/null || shasum -a 256 -c SHA256SUMS; } >/dev/null) \
    || fail "checksums do not verify"
if grep -q -- "$DB_PASSWORD" "$BACKUP_ROOT/drill/MANIFEST" || grep -q -- "$DB_PASSWORD" <<<"$backup_out"; then
    fail "the database password appears in the backup output or manifest"
fi
# The manifest must record the schema version the live database actually
# carries. This drill used to assert the literal 1, which meant it would fail
# the first time the schema moved — task 051 moved it — and a recovery drill
# that breaks on a schema change breaks exactly when it is most needed.
#
# Comparing the two recorded values instead tests the property that matters:
# the backup describes the database it was taken from. That stays true at
# every future version, and neither side is trusted alone — a manifest
# claiming a version nothing verifies would be as useless as no manifest.
manifest_file="$BACKUP_ROOT/drill/MANIFEST"

# psql_on strips all whitespace, so two rows would concatenate into one
# meaningless number. The row count is asked for separately rather than
# inferred from the value.
schema_rows="$(psql_on "$DB_NAME" "SELECT count(*) FROM trustvian_schema_version")"
[ "$schema_rows" = "1" ] \
    || fail "'$DB_NAME' holds $schema_rows schema-version row(s), want exactly 1"
live_schema="$(psql_on "$DB_NAME" "SELECT version FROM trustvian_schema_version")"

manifest_lines="$(grep -c '^trustvian_schema_version=' "$manifest_file" || true)"
[ "$manifest_lines" = "1" ] \
    || fail "MANIFEST holds $manifest_lines trustvian_schema_version line(s), want exactly 1"
manifest_schema="$(sed -n 's/^trustvian_schema_version=//p' "$manifest_file" | tr -d '[:space:]')"

case "$live_schema" in
    ''|*[!0-9]*) fail "the live database reported a non-numeric schema version: '$live_schema'" ;;
esac
case "$manifest_schema" in
    ''|*[!0-9]*) fail "MANIFEST records a non-numeric schema version: '$manifest_schema'" ;;
esac

[ "$live_schema" = "$manifest_schema" ] \
    || fail "MANIFEST records schema version $manifest_schema but '$DB_NAME' is at $live_schema"

ok "artifacts 0600, checksums verify, schema version $live_schema matches the live database, no credentials"

# ---------------------------------------------------------------------
step "4/8 Restore guards refuse unsafe targets and corrupt backups"
cp -R "$BACKUP_ROOT/drill" "$BACKUP_ROOT/corrupt"
printf 'X' | dd of="$BACKUP_ROOT/corrupt/trustvian.dump" bs=1 seek=100 conv=notrunc 2>/dev/null
out="$(tools "$TOOLS_IMAGE" bash /scripts/restore-postgres.sh \
    --backup /backups/corrupt --target-db "$RESTORED_DB" 2>&1)" && fail "a corrupted backup was restored"
grep -q 'checksum verification FAILED' <<<"$out" || fail "corrupt backup refused for the wrong reason: $out"

out="$(tools "$TOOLS_IMAGE" bash /scripts/restore-postgres.sh \
    --backup /backups/drill --target-db "$DB_NAME" 2>&1)" && fail "restore into the live database was allowed"
grep -Eq 'is not empty|other session' <<<"$out" || fail "live database refused for the wrong reason: $out"
[ "$(observations_in "$DB_NAME")" = "$SPANS" ] || fail "the live database changed after a refused restore"
ok "corrupt backup refused; live database refused and untouched"

# ---------------------------------------------------------------------
step "5/8 Restore into a new database"
$COMPOSE exec -T postgres createdb -U "$DB_USER" "$RESTORED_DB"
out="$(tools "$TOOLS_IMAGE" bash /scripts/restore-postgres.sh \
    --backup /backups/drill --target-db "$RESTORED_DB" 2>&1)" || fail "restore failed: $out"
grep -q 'RESTORE VERIFIED' <<<"$out" || fail "restore did not verify: $out"
[ "$(observations_in "$RESTORED_DB")" = "$SPANS" ] || fail "restored database does not hold $SPANS observations"
ok "restored and structurally verified: $SPANS observations"

# ---------------------------------------------------------------------
step "6/8 Operator cutover to the restored database"
# Exported, not prefixed to one command: the cutover must persist for every
# later compose invocation, exactly as an operator would record it in .env.
# Set only on `up`, the next `docker compose run demo-producer` re-evaluates
# its otel-collector dependency WITHOUT it and silently recreates the runtime
# on the original database — found by this drill, and why step 7 checks both
# databases.
export TRUSTVIAN_RUNTIME_POSTGRES_DB="$RESTORED_DB"
$COMPOSE up -d otel-collector >/dev/null 2>&1 \
    || fail "runtime did not start against the restored database"
# The recreated container briefly has no listener; wait for it to come up on
# the restored database rather than reading the old container's answer.
sleep 2
wait_status /livez 200 60
wait_status /readyz 200 60
logs="$($COMPOSE logs otel-collector 2>&1)"
if grep -q -- "$DB_PASSWORD" <<<"$logs"; then
    fail "the database password appears in the runtime logs"
fi
ok "runtime on '$RESTORED_DB': /livez 200, /readyz 200, no credentials in logs"

# ---------------------------------------------------------------------
step "7/8 Learning continues from the restored state"
TRUSTVIAN_DEMO_SPAN_COUNT=$SPANS $COMPOSE run --rm demo-producer >/dev/null 2>&1 \
    || fail "demo-producer failed after cutover"
expected=$((SPANS * 2))
restored_total="$(observations_in "$RESTORED_DB")"
[ "$restored_total" = "$expected" ] \
    || fail "restored database holds $restored_total observations, want $expected — learning did not continue from the backup"
original_total="$(observations_in "$DB_NAME")"
[ "$original_total" = "$SPANS" ] \
    || fail "original database holds $original_total observations, want $SPANS — the runtime did not cut over"
ok "restored: $restored_total (continued), original: $original_total (untouched)"

# ---------------------------------------------------------------------
step "8/8 Readiness tracks a PostgreSQL outage and recovery (task 042)"
$COMPOSE stop postgres >/dev/null 2>&1
wait_status /readyz 503 30
[ "$(status_of /livez)" = "200" ] || fail "/livez failed during a database outage"
$COMPOSE start postgres >/dev/null 2>&1
wait_status /readyz 200 60
ok "outage: /readyz 503 with /livez 200; recovery: /readyz 200 without restart"

printf '\nRECOVERY DRILL PASSED — all 8 checks held.\n'
