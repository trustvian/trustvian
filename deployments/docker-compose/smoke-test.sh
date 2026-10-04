#!/usr/bin/env bash
#
# Deterministic smoke test for the Trustvian reference Docker Compose
# deployment. Exits 0 only if every step below actually held; exits
# non-zero with a specific message on the first failure.
#
#   ./smoke-test.sh
#
# What it proves, in order:
#
#   1. The stack starts and PostgreSQL becomes healthy.
#   2. The Trustvian runtime starts on the PostgreSQL backend — and logs
#      the backend type without leaking the DSN.
#   3. Trustvian auto-created its schema at the expected version.
#   4. Telemetry sent over OTLP is analyzed and produces learned state in
#      PostgreSQL.
#   5. That state SURVIVES a full `down` + `up` with the volume retained,
#      and is *added to* rather than relearned from scratch.
#   6. PostgreSQL configured but unavailable makes the runtime FAIL, never
#      silently fall back to in-memory storage.
#   7. `down -v` destroys the persistent state, as documented.
#
# Step 5 is the one that matters most, and it is deliberately written to
# distinguish persistence from relearning: it asserts the observation count
# GREW to 2N rather than merely being non-zero. A stack that silently
# started from scratch after the restart would land back on N and fail
# here — which "is there state?" would not catch.
#
# It leaves the environment clean: the volume is destroyed at the end, so
# consecutive runs are independent.

set -euo pipefail

cd "$(dirname "$0")"

COMPOSE="docker compose"
SPANS=60
# The core baseline schema version a fresh database is created at:
# internal/store/postgres.SchemaVersion (not the platform's own schema).
# scripts/smoke_test_schema_test.go fails when the two disagree, so a schema
# bump breaks a pull request's CI rather than the next Nightly.
EXPECTED_SCHEMA_VERSION=2
PSQL="$COMPOSE exec -T postgres psql -U ${TRUSTVIAN_POSTGRES_USER:-trustvian} -d ${TRUSTVIAN_POSTGRES_DB:-trustvian} -tAc"

step()  { printf '\n=== %s\n' "$1"; }
ok()    { printf '    ok: %s\n' "$1"; }
fail()  { printf '\nFAIL: %s\n' "$1" >&2; exit 1; }

cleanup_on_error() {
    if [ $? -ne 0 ]; then
        printf '\n--- collector logs (last 30 lines) ---\n' >&2
        $COMPOSE logs --tail=30 otel-collector >&2 2>&1 || true
    fi
}
trap cleanup_on_error EXIT

# ---------------------------------------------------------------------
step "0/7 Starting from a clean slate"
# -v so a previous run's learned state cannot make this one pass.
$COMPOSE down -v >/dev/null 2>&1 || true
ok "no pre-existing volume"

# ---------------------------------------------------------------------
step "1/7 Compose startup and PostgreSQL health"
$COMPOSE up -d >/dev/null
# `depends_on: service_healthy` already waited, but assert it rather than
# assume: every later step depends on this being true.
for _ in $(seq 1 60); do
    health=$($COMPOSE ps postgres --format '{{.Health}}' 2>/dev/null || true)
    [ "$health" = "healthy" ] && break
    sleep 1
done
[ "$health" = "healthy" ] || fail "PostgreSQL did not become healthy (last state: ${health:-unknown})"
ok "postgres healthy"

# ---------------------------------------------------------------------
step "2/7 Trustvian runtime on the PostgreSQL backend"
for _ in $(seq 1 30); do
    state=$($COMPOSE ps otel-collector --format '{{.State}}' 2>/dev/null || true)
    [ "$state" = "running" ] && break
    sleep 1
done
[ "$state" = "running" ] || fail "collector is not running (state: ${state:-unknown})"

logs=$($COMPOSE logs otel-collector 2>&1)
grep -q 'storage backend initialized' <<<"$logs" \
    || fail "collector did not report initializing a storage backend"
grep -q '"type":"postgres"' <<<"$logs" \
    || fail "collector did not initialize the postgres backend"
ok "collector running on the postgres backend"

# The DSN carries a password. It must not be in the logs.
password="${TRUSTVIAN_POSTGRES_PASSWORD:-change-me}"
if grep -q "$password" <<<"$logs"; then
    fail "the database password appears in the collector logs"
fi
ok "no credentials in logs"

# ---------------------------------------------------------------------
step "3/7 Schema auto-created at the expected version"
version=$($PSQL "SELECT version FROM trustvian_schema_version" | tr -d '[:space:]')
[ "$version" = "$EXPECTED_SCHEMA_VERSION" ] || fail "schema version is '${version}', want ${EXPECTED_SCHEMA_VERSION}"
tables=$($PSQL "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'trustvian_%'" | tr -d '[:space:]')
[ "$tables" = "2" ] || fail "found ${tables} trustvian tables, want exactly 2 (no event warehouse)"
ok "schema version ${version}, ${tables} tables"

# ---------------------------------------------------------------------
step "4/7 Telemetry analyzed and learned into PostgreSQL"
baseline_before=$($PSQL "SELECT count(*) FROM trustvian_baseline" | tr -d '[:space:]')
[ "$baseline_before" = "0" ] || fail "expected an empty baseline table, found ${baseline_before} row(s)"

TRUSTVIAN_DEMO_SPAN_COUNT=$SPANS $COMPOSE run --rm demo-producer >/dev/null 2>&1 \
    || fail "demo-producer failed to send telemetry"

observations=$($PSQL "SELECT coalesce(sum(observation_count),0) FROM trustvian_baseline" | tr -d '[:space:]')
[ "$observations" = "$SPANS" ] \
    || fail "observation_count is ${observations} after ${SPANS} spans, want ${SPANS}"

# Analysis actually ran, not just persistence: enriched attributes appear.
# Captured to a variable first, deliberately: piping into `grep -q` under
# `set -o pipefail` fails the script, because grep exits on its first match
# and the upstream `docker compose logs` then dies on SIGPIPE.
analysis_logs=$($COMPOSE logs otel-collector 2>&1)
grep -q 'trustvian.decision' <<<"$analysis_logs" \
    || fail "no span was enriched with trustvian.decision — analysis did not run"
ok "${observations} observations learned; spans enriched with decisions"

# ---------------------------------------------------------------------
step "5/7 State survives 'down' + 'up' with the volume retained"
$COMPOSE down >/dev/null 2>&1   # NO -v: the volume must survive
$COMPOSE up -d >/dev/null
for _ in $(seq 1 60); do
    state=$($COMPOSE ps otel-collector --format '{{.State}}' 2>/dev/null || true)
    [ "$state" = "running" ] && break
    sleep 1
done
[ "$state" = "running" ] || fail "collector did not come back up after down/up"

retained=$($PSQL "SELECT coalesce(sum(observation_count),0) FROM trustvian_baseline" | tr -d '[:space:]')
[ "$retained" = "$SPANS" ] \
    || fail "observation_count is ${retained} after restart, want ${SPANS} — learned state did not survive"
ok "${retained} observations retained across down/up"

# The decisive assertion: the second run ADDS to the persisted baseline
# rather than starting over. Relearning from scratch would land on $SPANS.
TRUSTVIAN_DEMO_SPAN_COUNT=$SPANS $COMPOSE run --rm demo-producer >/dev/null 2>&1 \
    || fail "demo-producer failed on the second run"

expected=$((SPANS * 2))
total=$($PSQL "SELECT coalesce(sum(observation_count),0) FROM trustvian_baseline" | tr -d '[:space:]')
[ "$total" = "$expected" ] \
    || fail "observation_count is ${total} after a second ${SPANS}-span run, want ${expected} — the baseline was relearned, not continued"
ok "${total} observations — the second run continued the persisted baseline"

# ---------------------------------------------------------------------
step "6/7 Unavailable PostgreSQL fails closed (no in-memory fallback)"
# Point the collector at a port nothing listens on, leaving everything else
# identical. It must exit rather than start on a substituted store.
TRUSTVIAN_POSTGRES_DSN='postgres://trustvian:change-me@127.0.0.1:1/trustvian?sslmode=disable' \
    $COMPOSE run --rm --no-deps \
    -e TRUSTVIAN_POSTGRES_DSN='postgres://trustvian:change-me@127.0.0.1:1/trustvian?sslmode=disable' \
    otel-collector >/tmp/tv-failclosed.log 2>&1 && failed=0 || failed=1

[ "$failed" = "1" ] \
    || fail "the collector started successfully with an unreachable database — it must fail closed"
grep -qi 'storage' /tmp/tv-failclosed.log \
    || fail "the failure did not mention storage; got: $(tail -3 /tmp/tv-failclosed.log)"
if grep -q 'change-me' /tmp/tv-failclosed.log; then
    fail "the database password appears in the failure output"
fi
ok "collector refused to start, reported storage, leaked no credentials"
rm -f /tmp/tv-failclosed.log

# ---------------------------------------------------------------------
step "7/7 'down -v' destroys the persistent state"
$COMPOSE down -v >/dev/null 2>&1
volume_name="$($COMPOSE config --format json 2>/dev/null | grep -o '"trustvian-reference_postgres-data"' | head -1 || true)"
if docker volume inspect trustvian-reference_postgres-data >/dev/null 2>&1; then
    fail "the postgres-data volume still exists after 'down -v'"
fi
ok "volume removed"

trap - EXIT
printf '\nSMOKE TEST PASSED — all 7 checks held.\n'
