# Operations: Backup, Restore & Upgrade

What an operator does to keep Trustvian's learned behavioral state safe:
what to back up, how to restore it without making things worse, how to
upgrade without silently resetting what Trustvian has learned, and how to
roll back when an upgrade goes wrong.

Everything here uses PostgreSQL's own tools. There is no Trustvian backup
format, service, or scheduler — two small scripts wrap `pg_dump` and
`pg_restore` to make the dangerous mistakes hard to make.

> **Why this matters more than it looks.** A lost baseline does not make
> Trustvian fail. It makes every actor look brand new: anomaly confidence
> drops to zero, decisions fall back to identity and context alone, and
> behavior an attacker could never have normalized becomes "unknown" rather
> than "unusual". Nothing errors. That is exactly why recovery has to be
> verified by behavior, not by an exit code.

## What Trustvian persists

| State | Where | Backup | If lost |
|---|---|---|---|
| **Learned baselines** — per `{actor, environment}`: fingerprints, timing, hour-of-day activity, transitions, trigrams, delegators | `trustvian_baseline` (PostgreSQL) | **Database backup** | Every actor restarts at cold start. Not reconstructable. |
| **Schema version** | `trustvian_schema_version` (exactly one row) | **Database backup** | Startup fails closed on the remaining data (`ErrAmbiguousSchemaState`) |
| **Policy** (`config.PolicyConfig`) | Your YAML / Collector config | **Your config management** | Decisions change. Never stored in the database. |
| Anomaly, storage, alert, health configuration | Your YAML / Collector config | Your config management | Behavior or wiring changes |
| Database credentials (DSN) | Your secret manager / environment | Your secret manager | — never part of a Trustvian backup |
| Learning freeze flags | Process memory only | Not backed up | Re-apply after restart (per-process by design) |
| `file` store | One JSON file | File copy | As for baselines |
| `memory` store | Process memory | **Impossible** | Lost on every restart |

Trustvian stores no raw events, decisions, alerts, or sessions — so there is
no history to back up beyond the baseline itself
([storage guide](storage-guide.md#what-is-not-stored)).

**A database backup restores what was learned, never how decisions are
made.** Reproducing a decision needs both the restored database *and* the
policy and anomaly configuration that were in force. Keep those in version
control alongside the release version you run.

### The `memory` store has no recovery story

`memory` is non-durable by definition: a process restart loses all learned
state, and there is no way to back it up. That is correct for tests and
experiments, and wrong for anything whose learning you need tomorrow. Use
`postgres` (or `file` for a single process).

### The `file` store

`FileStore` writes by temp-file-plus-atomic-rename, so the file on disk is
always a complete snapshot, never a partial write. A copy is therefore a
valid backup even while the process runs:

```bash
cp /var/lib/trustvian/baseline.json /secure/backups/baseline-20260917.json
(cd /secure/backups && sha256sum baseline-20260917.json > baseline-20260917.json.sha256)
chmod 600 /secure/backups/baseline-20260917.json*
```

Restore by stopping the process, verifying the checksum, and replacing the
file. The file carries its own format version, and a version the binary does
not recognize is refused at startup.

The rest of this document is about PostgreSQL.

## Backup

### What you get

```text
/secure/backups/trustvian-20260917T0400Z/     mode 0700
├── trustvian.dump    pg_dump custom-format archive: schema AND data   0600
├── MANIFEST          what the backup is, with no connection detail    0600
└── SHA256SUMS        checksums of the two files above                 0600
```

`MANIFEST` records the format, UTC creation time, Trustvian schema version,
the Trustvian release if you supply it, and the PostgreSQL server and
`pg_dump` versions. It records **no host, user, database name, DSN, or
password**.

### Taking one

```bash
export PGHOST=db.internal PGPORT=5432 PGUSER=trustvian_backup
export PGDATABASE=trustvian PGSSLMODE=verify-full
export PGPASSFILE=/etc/trustvian/backup.pgpass     # or PGPASSWORD from a secret manager

./scripts/backup-postgres.sh \
  --output /secure/backups/trustvian-$(date -u +%Y%m%dT%H%MZ) \
  --trustvian-version v0.8.0
```

Connection settings come only from the
[standard libpq environment](https://www.postgresql.org/docs/current/libpq-envars.html).
There is deliberately no `--dsn` flag — a connection string on a command line
is visible to anyone who can list processes. Turn a Trustvian DSN
`postgres://user:pw@host:port/db?sslmode=…` into `PGUSER`, `PGPASSWORD` (or a
`.pgpass` entry), `PGHOST`, `PGPORT`, `PGDATABASE`, and `PGSSLMODE`.

The script refuses to:

- write to an output path that already exists — a previous backup is never
  overwritten;
- run without `PGDATABASE` — libpq would otherwise quietly pick a database
  named after the user;
- back up a database without Trustvian's tables, or whose schema-version
  table does not hold exactly one row (use `pg_dump` directly for a forensic
  copy of a damaged database);
- report success unless the archive reads back and its table of contents
  holds both tables' definitions **and data**.

The archive is written into a private staging directory and renamed into
place only when complete, so the output path is either a whole backup or
absent.

A backup role needs only `CONNECT` on the database and `SELECT` on the two
tables. It does not need to be the runtime role.

### Without the script

The script is a convenience, not a requirement. The equivalent by hand:

```bash
umask 077
mkdir /secure/backups/trustvian-20260917T0400Z && cd "$_"
pg_dump -w --format=custom --file=trustvian.dump
pg_restore --list trustvian.dump | grep -E 'TABLE (DATA )?[^ ]+ trustvian_'   # 4 lines
sha256sum trustvian.dump > SHA256SUMS
```

`restore-postgres.sh` expects the script's layout (it requires `MANIFEST`),
so restore a hand-made dump with `pg_restore` directly, using the same
flags and the same new-empty-database discipline described in
[Restore](#restore).

### Backup consistency — no downtime needed

**Take backups online.** `pg_dump` reads the whole database inside one
`REPEATABLE READ` snapshot, and every Trustvian write
(`Store.Observe`) is a single transaction touching a single row, with its
derived columns written in the same statement. There are no cross-row
invariants for a snapshot to split. Every row in the archive is exactly as
of the last commit before the snapshot — never half-written.

This is tested, not inferred: with concurrent writers committing throughout
the backup, the restored database opens, every baseline decodes, every
row's derived `observation_count` agrees with its stored baseline, and each
actor's restored count lies between what had been acknowledged before the
backup began and after it ended.

Observations committed **after** the snapshot are not in the backup. That
is the recovery point, not an inconsistency.

A quiescent backup (runtime stopped) is never *required*. It is useful only
immediately before an upgrade, so that the backup contains every
observation the old release made — see [Upgrade](#upgrade).

### Recovery point and recovery time

Trustvian makes no RPO or RTO promise, because neither is Trustvian's to
make:

- **RPO** is your backup interval. Observations after the last backup are
  lost on restore. Learned state changes gradually, so a day of lost
  observations degrades familiarity somewhat; it does not reset it.
  PostgreSQL continuous archiving / point-in-time recovery narrows RPO
  further and is entirely compatible with Trustvian — it operates below
  the application.
- **RTO** is the time to restore your database size on your infrastructure,
  plus startup. Measure it in a [drill](#recovery-drill).

### Storing backups

A backup holds every actor's learned behavioral profile — what each service
and agent normally does, when, and against what. Treat it as security-
sensitive data:

- **encrypted at rest**, with keys managed separately from the backup;
- **access-controlled** — the script creates `0700`/`0600` artifacts; keep
  that property wherever they are copied;
- **stored separately** from the primary database (different host, account,
  or failure domain);
- **retained and rotated** by your own policy — Trustvian ships no scheduler
  and no retention;
- **restored periodically** — an untested backup is not evidence of
  recoverability.

`SHA256SUMS` detects corruption. It does **not** authenticate the backup:
anyone able to modify the archive can regenerate the checksums. For
tamper evidence, store `SHA256SUMS` somewhere the backup's writer cannot
modify, or sign it.

### PostgreSQL tool versions

- Use a `pg_dump` whose major version is **equal to or newer than** the
  server's. `pg_dump` refuses a newer server itself ("server version
  mismatch") and the script surfaces that error.
- Restore with a `pg_restore` at least as new as the `pg_dump` that wrote the
  archive.
- The custom format is a logical dump, portable across machines and CPU
  architectures. Restoring into a **newer** PostgreSQL major version is the
  normal logical-upgrade path; restoring into an older one is not
  supported by PostgreSQL. Trustvian's storage layer is tested against
  PostgreSQL 17 and expected to work on 13+ — see
  [storage guide § PostgreSQL versions](storage-guide.md#postgresql-versions).

## Restore

Restore is the dangerous half. The safe shape is always:

```text
verified backup
  → a NEW, EMPTY database
  → restore (single transaction)
  → structural verification
  → start Trustvian against it — the schema check runs here
  → /readyz 200
  → verify learned state
  → YOU cut over
```

Nothing in that sequence touches the database currently in use, and nothing
moves traffic on its own.

### 1. Create an empty target

```bash
createdb trustvian_restore_20260917
```

The restore script never creates, drops, or renames databases.

### 2. Restore

```bash
export PGHOST=db.internal PGUSER=trustvian PGSSLMODE=verify-full PGPASSFILE=/etc/trustvian/restore.pgpass
unset PGDATABASE

./scripts/restore-postgres.sh \
  --backup /secure/backups/trustvian-20260917T0400Z \
  --target-db trustvian_restore_20260917
```

Before writing anything, the script:

- verifies `SHA256SUMS` over **both** the dump and the manifest — a
  mismatch, or a checksum file that does not cover exactly those two files,
  is refused, and there is no bypass flag;
- refuses a target that is not a plain database name, that equals
  `PGDATABASE`, that contains any table or extra schema, or that has any
  other session connected. **A refusal changes nothing about the target** —
  it may be a database someone depends on.

It then runs `pg_restore --single-transaction --exit-on-error --no-owner
--no-acl`, so the restore commits entirely or not at all, and checks:

- both Trustvian tables and their primary keys exist;
- `trustvian_schema_version` holds exactly one row, equal to the manifest;
- every stored baseline is a JSON object, and every row's derived
  `schema_version` matches.

Success ends with `RESTORE VERIFIED (structural)` plus the restored baseline
and observation counts.

### If the restore fails

The script exits non-zero, prints `RESTORE FAILED`, and **quarantines the
target** (`ALTER DATABASE … ALLOW_CONNECTIONS false`).

That last step exists because of a real hazard: a failed single-transaction
restore leaves the target *empty*, and an empty database is
indistinguishable from a new deployment. Pointed at it, Trustvian would
initialize a fresh schema and start learning from zero — a silent reset.
Quarantined, it fails to start instead. Read the error, then drop it:

```bash
dropdb trustvian_restore_20260917
```

If the script reports it could not quarantine (it needs ownership of the
target), drop the database yourself before anything else.

### Roles after a restore

`--no-owner --no-acl` makes the restoring role own the tables, so a backup
taken under one role restores cleanly under another. If you run Trustvian
with a separate least-privilege runtime role, re-grant it:

```sql
GRANT SELECT, INSERT, UPDATE ON trustvian_baseline, trustvian_schema_version TO trustvian_runtime;
```

### 3. Start Trustvian against the restored database

Schema compatibility is decided by **Trustvian itself at startup** — the
same `Migrate` step every start runs, with the same fail-closed rules
([storage guide](storage-guide.md#schema-compatibility-case-by-case)). The
restore script deliberately does not re-implement them. A database this
release cannot interpret aborts startup; it never degrades to in-memory
storage.

Start a runtime against the target *without* sending it production traffic —
a second Collector instance, or the CLI:

```bash
cat > restored.yaml <<'EOF'
version: v1
type: postgres
postgres:
  dsn: ${TRUSTVIAN_RESTORED_DSN}
EOF
```

With health enabled (`health:` block in the processor config):

```bash
curl -fsS http://127.0.0.1:13133/livez    # 200 — process functioning
curl -fsS http://127.0.0.1:13133/readyz   # 200 — the restored database is usable
```

`/readyz` is the operational gate. It is 503 while starting, and 503 whenever
the configured PostgreSQL is unusable.

### 4. Verify learned state

A restore is not verified until learned state is shown to be *there and in
use*:

```sql
-- Counts should match the "RESTORE VERIFIED" output and your expectation of
-- the backup time.
SELECT count(*) AS actors, sum(observation_count) AS observations,
       max(last_observed) AS newest
  FROM trustvian_baseline;
```

Then confirm behavior: analyze a few known-routine events for actors you
know well (for example with `trustvian analyze --storage-config
restored.yaml routine.json`). Their anomaly confidence should be high and
their decisions routine. A cold, unfamiliar result for an actor with
hundreds of observations means the runtime is not reading the state you
restored.

### 5. Cut over

Point production at the restored database — your deployment's action, not
Trustvian's: change the DSN in your secret manager and restart, update the
Compose variable, or switch the connection pooler. Afterwards, confirm
`/readyz` is 200 and that `observation_count` for active actors **grows from
the restored values**. Keep the old database until you are satisfied.

In the [reference deployment](../deployments/docker-compose/README.md#restoring-a-backup),
cutover is one variable, `TRUSTVIAN_RUNTIME_POSTGRES_DB`, recorded in `.env`.

## Upgrade

### Before you start

1. **Read the release notes** for every version between yours and the
   target. Pre-`v1.0`, skipping minor versions is not supported unless a
   release says so.
2. **Pin the exact target version** — a `vX.Y.Z` image tag or digest, or a
   `vX.Y.Z` binary. Never `latest`; a floating tag is not an upgrade
   contract.
3. **Verify the artifact** — checksums for binaries
   ([release guide](release-guide.md#verifying-a-published-release)),
   signature and provenance for images
   ([supply chain](supply-chain.md#verifying-a-published-image)).
4. **Have a verified backup** (below). This is mandatory, not advice: it is
   the only rollback path across a schema change.

### The procedure

```text
healthy old runtime (/readyz 200)
  → stop or drain the old runtime            SIGTERM — readiness flips to 503 first
  → backup-postgres.sh                        quiescent: every old-release observation included
  → verify the backup (checksums; restore it somewhere if in doubt)
  → deploy the exact new version
  → start: Migrate verifies the schema        mismatch ⇒ startup fails, database untouched
  → /livez 200, /readyz 200
  → observation_count continues from pre-upgrade values
  → watch trustvian.analyses / trustvian.observations by outcome, and latency
```

Stopping before the backup is the one place a quiescent backup earns its
downtime: an online backup is consistent, but observations committed between
it and the stop would be lost by a rollback. If you cannot stop first, take
the backup, then stop, and accept that window.

### Upgrading into `v1.0`: the schema moves

`v1.0` is the first release that changes the PostgreSQL schema — version 1 to
version 2, adding the learning-scope column
([ADR 0024](adr/0024-learning-scope-is-a-baseline-key-dimension.md)). The
procedure above is unchanged, and `Migrate` performs the upgrade on first
startup, atomically, inside its existing transaction and advisory lock. Every
existing baseline is preserved and lands in the default scope; no learned
state is discarded. The file-store snapshot moves from `version: 1` to
`version: 2` on the same terms.

**The mandatory backup above is load-bearing here, not ceremonial.** There is
no downgrade across this change: a `v0.9.x` binary refuses version-2 state and
fails startup, which is the intended behavior — it cannot see the scope column
and would merge distinct learned profiles if it proceeded. Rolling back to
`v0.9.x` means restoring the pre-upgrade backup, so take it and verify it
before you start.

Expect the first startup after the upgrade to take slightly longer than a
restart: the migration rewrites the baseline table's primary key and restamps
each row's `schema_version`. It is proportional to the number of actors you
track, which is bounded by your deployment, not by traffic.

**Several replicas.** If the target release changes the schema version, stop
**all** old replicas before starting any new one. If it does not (see the
[matrix](#compatibility-matrix)), a rolling replacement is safe: old and new
binaries read and write the same state identically — tested for `v0.8.0` and
this release, alternating on one database. Running replicas of mixed versions
across a schema-version change is never safe.

### Container upgrade

```bash
IMAGE=ghcr.io/trustvian/trustvian-collector
TAG=v0.9.0          # example — substitute a real, released version
DIGEST=$(docker buildx imagetools inspect "$IMAGE:$TAG" --format '{{.Manifest.Digest}}')
cosign verify "$IMAGE@$DIGEST" \
  --certificate-identity-regexp '^https://github.com/trustvian/trustvian/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

docker stop trustvian-collector                      # SIGTERM; bounded graceful shutdown
./scripts/backup-postgres.sh --output /secure/backups/pre-$TAG --trustvian-version v0.8.0
docker run -d --name trustvian-collector-$TAG ... "$IMAGE@$DIGEST" --config=/etc/trustvian/collector.yaml
curl -fsS http://127.0.0.1:13133/readyz
```

> **No official image exists yet.** `ghcr.io/trustvian/trustvian-collector`
> is published from the first release after task 041; `v0.9.0` above is an
> example, not an available tag. `v0.8.0` deployments ran a Collector built
> from source, so the first image-based upgrade is into `v0.9.0`.

### Binary upgrade

The same sequence, without containers:

```bash
sha256sum -c checksums.txt --ignore-missing          # verify the new release
systemctl stop trustvian-collector                   # or your supervisor's stop
./scripts/backup-postgres.sh --output /secure/backups/pre-v0.9.0 --trustvian-version v0.8.0
install -m 0755 trustvian-collector /usr/local/bin/trustvian-collector
systemctl start trustvian-collector
curl -fsS http://127.0.0.1:13133/readyz
```

The `trustvian` CLI is released as a binary; the long-lived Collector is
built as a Collector distribution (see
[processor/README.md](../processor/README.md)). Keep the previous binary
until the upgrade is verified — it is half of the rollback.

### Configuration changes

Configuration is not rewritten automatically. Since `v0.8.0`:

| Change | Kind | Action |
|---|---|---|
| Processor `health:` block (`endpoint`, `readiness_timeout`) | **New, optional** | None required; add it to get `/livez` and `/readyz` |
| Removed or renamed fields | None | — |
| Changed defaults | None | — |

Configuration documents carry their own `version: v1` field, independent of
the database schema version, and an unknown version is rejected at load.

### If the new release will not start

A schema or storage failure at startup is **fail-closed**: the process exits
(or stays not-ready), it never falls back to in-memory storage, and the
database is left exactly as it was — `Migrate` is transactional, so there is
no partially-applied migration to repair and no `force` command to reach for.
The error names the cause:

| Error | Meaning | Action |
|---|---|---|
| `store/postgres: database unavailable` | Cannot reach or authenticate at startup | Fix connectivity/credentials, then start again. (An outage *after* a successful start is different: the runtime stays live, reports not-ready, and becomes ready without a restart.) |
| `database schema version mismatch: database is at version N, this build expects M` | Release and database disagree | Deploy the release matching the database, or [roll back](#rollback) |
| `ambiguous schema metadata` | Version table empty with data present, or several version rows | Restore a consistent backup; do not hand-edit unless you know the correct version |
| permission errors naming a table | Runtime role lacks rights | Grant them; see [storage guide](storage-guide.md#migration-privileges) |

Operational metrics help separate "did not start" from "started but
failing": after an upgrade, `trustvian.analyses` should be `analyzed`,
`trustvian.observations` should be `learned`, and error outcomes and
latency should match pre-upgrade levels ([observability](observability.md)).

## Rollback

### Always safe: restore the pre-upgrade backup

```text
stop the new runtime
  → restore-postgres.sh the pre-upgrade backup into a NEW database
  → deploy the previous exact version against it
  → /readyz 200, verify learned state
  → cut over
```

This works for every release pair, because the previous release reads
exactly the state it wrote. The cost: observations the new release learned
after the backup are lost.

### Binary-only downgrade: only within one schema version

Running the previous binary against the database the new one has used is
safe **only when both releases share the same schema version**:

- **Same schema version** (`v0.8.0` ↔ this release; tested): the previous
  release reads the upgraded database and produces identical analysis.
- **Different schema version**: the previous release refuses to start with
  `database schema version mismatch` (tested). That refusal is the safety
  mechanism — an older binary never mutates state whose layout it does not
  understand — and it means the backup is the only way back.

Do not "fix" a refused downgrade by editing `trustvian_schema_version`. That
is the one action that turns a clean refusal into silent misinterpretation of
learned state.

## Compatibility matrix

Trustvian is pre-`v1.0`: compatibility promises are deliberately narrower
than they will be after it. What is **not** narrowed: no supported upgrade
silently resets or reinterprets learned state.

| Path | Supported | Basis |
|---|---|---|
| `v0.8.0` → this release | **Yes, in place** | Same schema version (1); automated test from the real `v0.8.0` tag |
| Patch → patch, same minor | Yes, if the schema version is unchanged | Startup schema check |
| Minor → next minor | As that release's notes state | No multi-version migration exists yet |
| Skipping minor versions | **No**, unless release notes say so | — |
| Downgrade, same schema version | Binary-only is safe | Tested for `v0.8.0` ↔ this release |
| Downgrade, different schema version | **Restore the pre-upgrade backup** | Older binary refuses newer schema (tested) |
| `memory` store, any version | Nothing survives to upgrade | — |
| PostgreSQL major upgrade | Via dump/restore into the new server, or `pg_upgrade` | PostgreSQL's own mechanisms; tested only on 17 |

The schema version today is `1` and has never changed.

## Recovery drill

A backup that has never been restored is a hope, not a recovery plan. On a
schedule — and after any change to your database, credentials, or
infrastructure — prove the whole chain:

```text
backup → checksum → restore into a new database → Trustvian starts
  → /readyz 200 → learned state verified by behavior → learning continues
```

1. Take a backup with `backup-postgres.sh` (online is fine).
2. `createdb trustvian_drill_$(date +%Y%m%d)` on a non-production server if
   you have one.
3. `restore-postgres.sh` into it; require `RESTORE VERIFIED`.
4. Start a Trustvian instance against it with health enabled; require
   `/readyz` 200.
5. Compare `SELECT count(*), sum(observation_count) FROM trustvian_baseline`
   with production at backup time.
6. Analyze known-routine events for well-known actors; require familiar,
   routine results.
7. Record how long steps 3–4 took. That is your measured RTO.
8. `dropdb` the drill database and destroy the drill copy of the backup.

The repository runs this drill automatically:

- **`deployments/docker-compose/recovery-drill.sh`** — on the real runtime:
  online backup under a running Collector, a corrupt backup and the live
  database both refused, restore into a new database, operator cutover,
  `/livez` and `/readyz`, learning continuing from the restored state (and the
  original database untouched), and readiness tracking a database outage.
  Nightly in CI.
- **`scripts/backup_restore_test.go`** — on every pull request: behavioral
  equivalence after restore, consistency under concurrent writes, every
  failure mode above, and the upgrade/rollback path from the real `v0.8.0`
  release.

```bash
# Locally, with PostgreSQL 17 client tools on PATH:
TRUSTVIAN_TEST_BACKUP_RESTORE=1 \
TRUSTVIAN_TEST_POSTGRES_DSN='postgres://trustvian:…@localhost:5433/trustvian?sslmode=disable' \
  go test -race -run 'Backup|Restore|Upgrade' ./scripts/

# Including the upgrade test, from a CLI built at the previous release tag:
git worktree add --detach /tmp/tv-v0.8.0 v0.8.0
(cd /tmp/tv-v0.8.0 && GOWORK=off go build -o /tmp/trustvian-v0.8.0 ./cmd/trustvian)
TRUSTVIAN_TEST_UPGRADE_FROM_BINARY=/tmp/trustvian-v0.8.0 TRUSTVIAN_TEST_UPGRADE_FROM_VERSION=v0.8.0 \
TRUSTVIAN_TEST_BACKUP_RESTORE=1 TRUSTVIAN_TEST_POSTGRES_DSN='…' \
  go test -race -run Upgrade ./scripts/

# The full drill on the reference deployment (Docker only):
make recovery-drill
```

## What Trustvian deliberately does not do

- No custom backup format, export command, or backup service.
- No scheduler, retention, or upload to any storage vendor.
- No automatic restore into a running database, and no database creation,
  deletion, or renaming.
- No automatic cutover: DNS, load balancers, secrets, and connection strings
  are your deployment's.
- No self-updater and no migration orchestrator.

Each of those belongs to infrastructure you already run, and each is a place
where automation acting on the wrong database would be worse than an operator
taking one deliberate step.

## Related reading

- [Storage Guide](storage-guide.md) — backends, schema, compatibility rules,
  failure semantics
- [Security Model](SECURITY.md#backup-and-restore) — backup confidentiality,
  restore trust, credential handling
- [Observability](observability.md) — metrics to watch during an upgrade
- [Supply chain](supply-chain.md) and [Release guide](release-guide.md) —
  verifying what you upgrade to
- [Reference deployment](../deployments/docker-compose/README.md) — the
  drill, runnable
- [Task 044](archive/tasks/v0.9/044-operations-backup-restore-upgrade.md) — the design
  and evidence behind this document
