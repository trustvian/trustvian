# Trustvian Reference Docker Compose Deployment

A runnable local deployment that demonstrates one thing end to end:
**Trustvian analyzing real telemetry against behavioral state that
survives a restart.**

```text
demo-producer ──OTLP/gRPC──▶ OpenTelemetry Collector
                                    │
                            Trustvian processor
                                    │
                            Trustvian Engine
                                    │
                            PostgreSQL Store
                                    ▼
                               PostgreSQL
```

> **This is a reference deployment, not production orchestration.** The
> credentials are placeholders, TLS is off, and nothing here is hardened.
> See [Security](#security) before borrowing any of it.

## Requirements

Docker with Compose v2. Nothing else — no Go toolchain, no local
PostgreSQL, no build artifacts. Versions this was verified against:

| | Version |
|---|---|
| Docker Engine | 29.8.0 |
| Docker Compose | v5.5.1 |
| PostgreSQL image | `postgres:17-alpine` |
| Go builder image | `golang:1.27-alpine` |
| Runtime base image | `alpine:3.22` |

## Quickstart

```bash
cd deployments/docker-compose
cp .env.example .env          # optional; compose.yaml has the same defaults

docker compose up -d --build  # 1. start PostgreSQL + the Collector
docker compose run --rm demo-producer   # 2. send 60 spans of routine behavior
```

Then look at what happened.

**3. Trustvian's decisions** — every span comes back enriched:

```bash
docker compose logs otel-collector | grep -o 'trustvian\.[a-z.]*: [A-Za-z]*([^)]*)' | tail -5
```

```text
trustvian.anomaly.score: Double(0.30000000000000004)
trustvian.trust.score: Double(0.79)
trustvian.risk.level: Str(low)
trustvian.decision: Str(allow)
trustvian.fingerprint.id: Str(5b035252a6538d34)
```

The anomaly score falls as the run proceeds (0.45 → 0.40 → 0.35 → 0.30):
that is Trustvian learning what this actor normally does.

**4. The learned baseline in PostgreSQL** — no JSONB decoding needed:

```bash
docker compose exec postgres psql -U trustvian -d trustvian -c \
  "SELECT actor_id, environment, fingerprint_count, observation_count, updated_at
     FROM trustvian_baseline ORDER BY updated_at DESC;"
```

```text
   actor_id    | environment | fingerprint_count | observation_count |         updated_at
---------------+-------------+-------------------+-------------------+-----------------------------
 demo-payments | production  |                 4 |                60 | 2026-09-15 18:36:35.4859+00
```

Four fingerprints (the four distinct operations the producer emits), sixty
observations.

**5. The schema version** — the compatibility boundary task 036 added:

```bash
docker compose exec postgres psql -U trustvian -d trustvian -tAc \
  "SELECT version, applied_at FROM trustvian_schema_version;"
```

```text
2|2026-09-15 18:36:04.982887+00
```

The schema is created automatically on first connection. A database
recorded at a version this binary does not understand aborts startup
rather than being migrated blindly — see
[storage-guide.md](../../docs/storage-guide.md#schema-compatibility-case-by-case).

## Proving persistence

This is the point of the deployment, so it is worth doing deliberately.

```bash
# Phase A — learn. (Already done above: 60 observations.)
docker compose exec postgres psql -U trustvian -d trustvian -tAc \
  "SELECT sum(observation_count) FROM trustvian_baseline;"        # → 60

# Phase B — stop everything. NOTE: no -v, so the volume survives.
docker compose down

# Phase C — start again.
docker compose up -d

# Phase D — the same state is still there, before sending anything.
docker compose exec postgres psql -U trustvian -d trustvian -tAc \
  "SELECT sum(observation_count) FROM trustvian_baseline;"        # → 60

# And a second run ADDS to it rather than starting over.
docker compose run --rm demo-producer
docker compose exec postgres psql -U trustvian -d trustvian -tAc \
  "SELECT sum(observation_count) FROM trustvian_baseline;"        # → 120
```

**120, not 60, is the proof.** A stack that silently relearned from
scratch would land back on 60. This is exactly what the automated smoke
test asserts, and it is why it checks for growth rather than merely for
"some state exists".

### Seeing a trained baseline react

With a baseline in place, send one deliberately abnormal span — a slow
call to a target this actor has never touched:

```bash
TRUSTVIAN_DEMO_ANOMALY=true TRUSTVIAN_DEMO_SPAN_COUNT=1 \
  docker compose run --rm demo-producer
docker compose logs otel-collector | grep -A1 'admin/export-all' | tail -3
```

## Restart semantics

The distinction matters, so it is spelled out:

| Command | Containers | Learned baseline |
|---|---|---|
| `docker compose restart` | restarted in place | **kept** |
| `docker compose stop` / `start` | stopped, started | **kept** |
| `docker compose down` | removed | **kept** (the named volume survives) |
| `docker compose down -v` | removed | **destroyed** |

`down -v` is the only one that deletes learned state. It removes the
`postgres-data` named volume belonging to this Compose project
(`trustvian-reference`) and nothing else — it cannot touch unrelated
volumes or anything on your host filesystem.

## Health endpoints

The collector serves `/livez` and `/readyz` (task 042), published on
loopback:

```bash
curl -s http://127.0.0.1:13133/livez     # {"status":"ok"}
curl -s http://127.0.0.1:13133/readyz    # {"status":"ok"} — 503 while PostgreSQL is unusable
```

`/readyz` is the gate to wait on after any restart, restore, or upgrade.
There is still no Compose `healthcheck` for the collector: its image has no
shell or `curl` by design, so probes come from outside.

## Backing up and restoring

The learned baseline is security state. `docker volume` is not a backup.
The repository's scripts take a consistent **online** backup and restore it
into a **new** database; see [Operations](../../docs/operations.md) for the
full procedure and the reasoning behind every refusal. From this directory,
with the PostgreSQL 17 client tools run in a container so nothing needs
installing:

```bash
# Outside the repository, so a backup can never be committed by accident.
BACKUPS="$HOME/trustvian-backups" && mkdir -p -m 700 "$BACKUPS"
tools() {
  docker run --rm --network trustvian-reference_default --user "$(id -u):$(id -g)" \
    -v "$PWD/../../scripts:/scripts:ro" -v "$BACKUPS:/backups" \
    -e PGHOST=postgres -e PGUSER=trustvian -e PGPASSWORD postgres:17-bookworm "$@"
}
export PGPASSWORD=change-me        # the reference placeholder; never a real password on a shared host

# Backup while the stack runs.
tools -e PGDATABASE=trustvian bash /scripts/backup-postgres.sh --output /backups/trustvian-$(date -u +%Y%m%dT%H%MZ)
```

### Restoring a backup

```bash
# 1. A new, empty database. The restore script never creates or drops one.
docker compose exec postgres createdb -U trustvian trustvian_restored

# 2. Restore and verify. Refused if the checksum fails or the target is not empty.
tools bash /scripts/restore-postgres.sh --backup /backups/<backup-dir> --target-db trustvian_restored

# 3. Cut over — your decision, recorded in .env so it persists.
echo 'TRUSTVIAN_RUNTIME_POSTGRES_DB=trustvian_restored' >> .env
docker compose up -d otel-collector
curl -s http://127.0.0.1:13133/readyz
```

**Record the cutover in `.env`, not just on one command.** Compose
re-evaluates the collector whenever a service that depends on it runs.
`TRUSTVIAN_RUNTIME_POSTGRES_DB=… docker compose up -d otel-collector`
followed by a plain `docker compose run --rm demo-producer` silently
recreates the collector on the *original* database — the recovery drill
caught exactly this.

The original `trustvian` database is left untouched throughout; drop it only
once you are satisfied with the restored one.

## Recovery drill

```bash
./recovery-drill.sh        # or: make recovery-drill
```

The whole recovery chain against the running stack, exiting non-zero on the
first failure:

```text
=== 1/8 Stack starts and the runtime becomes ready
=== 2/8 Learn 40 observations
=== 3/8 Online backup while the runtime keeps running
=== 4/8 Restore guards refuse unsafe targets and corrupt backups
=== 5/8 Restore into a new database
=== 6/8 Operator cutover to the restored database
=== 7/8 Learning continues from the restored state
=== 8/8 Readiness tracks a PostgreSQL outage and recovery (task 042)
RECOVERY DRILL PASSED — all 8 checks held.
```

Step 7 is the decisive one, for the same reason as the smoke test's step 5:
the restored database must reach 80 observations (continued from the
backup's 40) while the original stays at 40. It removes its own volume and
backup directory when it finishes. Needs Docker, `curl`, and bash.

## Automated smoke test

```bash
./smoke-test.sh
```

Exits non-zero on the first failure, with a specific message. It starts
from a clean volume and checks, in order:

```text
=== 1/7 Compose startup and PostgreSQL health
=== 2/7 Trustvian runtime on the PostgreSQL backend
=== 3/7 Schema auto-created at the expected version
=== 4/7 Telemetry analyzed and learned into PostgreSQL
=== 5/7 State survives 'down' + 'up' with the volume retained
=== 6/7 Unavailable PostgreSQL fails closed (no in-memory fallback)
=== 7/7 'down -v' destroys the persistent state
SMOKE TEST PASSED — all 7 checks held.
```

It tears down its own volume at the end, so consecutive runs are
independent. It is a deployment smoke test — not CI, not a load test, and
not a security scan.

## Fail-closed behavior

If PostgreSQL is configured but unreachable, **the Collector does not
start.** It never falls back to in-memory storage.

```bash
docker compose run --rm --no-deps \
  -e TRUSTVIAN_POSTGRES_DSN='postgres://trustvian:change-me@127.0.0.1:1/trustvian?sslmode=disable' \
  otel-collector
```

```text
trustvian-collector: run failed: failed to build pipelines: failed to create
"trustvian" processor, in pipeline "traces": trustvianprocessor: storage:
store/postgres: database unavailable: failed to connect to
`user=trustvian database=trustvian`: 127.0.0.1:1 (127.0.0.1): dial error:
dial tcp 127.0.0.1:1: connect: connection refused
```

Non-zero exit, no processor, and — note what the driver reports — the user
and database but **not the password**. A
Collector that fell back would keep enriching spans with trust decisions
derived from state that evaporates on exit — and nothing would tell the
operator their database was never used. See
[docs/SECURITY.md](../../docs/SECURITY.md).

## Reusing this database for integration tests

This PostgreSQL service is the intended environment for the repository's
own storage integration and stress suites. **No test needs modifying** —
they already read `TRUSTVIAN_TEST_POSTGRES_DSN`:

```bash
docker compose -f deployments/docker-compose/compose.yaml up -d postgres

export TRUSTVIAN_TEST_POSTGRES_DSN='postgres://trustvian:change-me@localhost:5433/trustvian?sslmode=disable'
go test -race ./...                       # integration + stress tiers
go test -race -short ./...                # integration only, no stress
```

Or `make integration-postgres` from the repository root, which does both
steps.

The database-restart durability test needs to be told how to restart the
server, and should be run on its own (a restart disrupts every other
connection):

```bash
TRUSTVIAN_TEST_POSTGRES_RESTART_CMD='docker restart trustvian-reference-postgres-1' \
  go test -run TestDatabaseRestartPreservesCommittedBaseline ./internal/store/postgres/
```

See [storage-guide.md § Running the integration
tests](../../docs/storage-guide.md#running-the-integration-tests).

## What is in here

| File | Purpose |
|---|---|
| `compose.yaml` | The one canonical entry point — no `.dev`/`.prod` variants |
| `.env.example` | Local-development defaults; copy to `.env` |
| `collector.yaml` | Collector pipeline + Trustvian storage/policy config |
| `Dockerfile.collector` | Builds the Trustvian-enabled Collector from source |
| `Dockerfile.demo` | Builds the demo OTLP producer |
| `smoke-test.sh` | The automated persistence proof |
| `recovery-drill.sh` | The automated backup → restore → cutover proof |

### Configuration

`collector.yaml` holds the Trustvian configuration, in the **same schema**
the Go SDK and CLI use — `config.StorageConfig` and `config.PolicyConfig`,
with no Collector-specific variants:

```yaml
processors:
  trustvian:
    storage:
      version: v1
      type: postgres
      postgres:
        dsn: ${env:TRUSTVIAN_POSTGRES_DSN}
```

The DSN comes from the environment via the Collector's own `${env:...}`
provider, so credentials never sit in the config file. The processor
decodes this into `config.StorageConfig` and hands it to
`config.CompileStorage`; it contains no database code of its own.

## Troubleshooting

**`Cannot connect to the Docker daemon`** — Docker is not running. Start
Docker Desktop (or your daemon) and retry.

**`port is already allocated`** — something already uses 4317 or 5433.
Change `TRUSTVIAN_OTLP_GRPC_PORT` or `TRUSTVIAN_POSTGRES_HOST_PORT` in
`.env`.

**The collector exits immediately.** Read why:
`docker compose logs otel-collector`. The two common causes are a
storage-initialization failure (the message names `storage:` — the
database is unreachable, or credentials are wrong) and a Collector config
error (a malformed `collector.yaml`; the message names the offending key).
Both are intentional: the Collector fails closed rather than starting in a
degraded state.

**`postgres` never becomes healthy.** `docker compose logs postgres`. A
half-initialized volume from an interrupted first start is the usual
cause; `docker compose down -v` and start again.

**`relation "trustvian_baseline" does not exist`** — the Collector has not
successfully connected yet, so it has not created its schema. That almost
always means it failed to start; see above.

## Security

This deployment is **local and illustrative**. It is not hardened, and it
does not become production-ready by changing the password.

Before anything resembling production:

- **Change the credentials.** `change-me` is a placeholder for a
  throwaway database in a local volume. Real deployments supply
  credentials from a secret manager, not a file in a repository.
- **Turn on PostgreSQL TLS.** This deployment uses `sslmode=disable`
  because both ends are inside one Compose network on one machine. Use
  `sslmode=verify-full` with a pinned root certificate. Trustvian passes
  the DSN to the driver unmodified and never weakens TLS settings
  programmatically.
- **Restrict network access.** Every published port binds to `127.0.0.1`
  here. The PostgreSQL port is published only for local `psql` inspection
  and integration tests; service-to-service traffic uses the Compose
  network and does not need it.
- **Manage secrets externally.** The DSN reaches the Collector as an
  environment variable. That is better than a config file and still not a
  secret-management system.
- **Back up the database.** The learned baseline is real security state.
  A `docker volume` is not a backup strategy — see
  [Backing up and restoring](#backing-up-and-restoring).
- **Do not treat these images as release artifacts.** They are built
  locally from source. The official image is described in
  [supply-chain.md](../../docs/supply-chain.md).

Compose itself provides none of the above. It provides a network and a
volume.

## Related reading

- [Storage Guide](../../docs/storage-guide.md) — backends, durability,
  concurrency, failure semantics, schema compatibility
- [Architecture](../../docs/ARCHITECTURE.md) — where the Store sits
- [Security Model](../../docs/SECURITY.md) — fail-closed guarantees and
  credential handling
- [processor/README.md](../../processor/README.md) — the Collector
  processor itself
- [Task 037](../../docs/archive/tasks/v0.8/037-reference-docker-compose-deployment.md)
  — why this deployment is shaped the way it is
