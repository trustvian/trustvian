#!/usr/bin/env bash
#
# Back up Trustvian's learned behavioral state from PostgreSQL.
#
#   PGHOST=db.internal PGUSER=trustvian PGDATABASE=trustvian \
#   PGPASSFILE=~/.pgpass PGSSLMODE=verify-full \
#     ./scripts/backup-postgres.sh --output /secure/backups/trustvian-20260917T0400Z \
#                                  [--trustvian-version v0.8.0]
#
# A thin wrapper around pg_dump, not a backup engine. It exists to make the
# mistakes that matter hard to make: backing up the wrong database, silently
# overwriting a previous backup, leaving a half-written artifact where a good
# one is expected, writing a world-readable copy of security state, and
# having no way to tell later whether the file is still intact.
#
# Output is a NEW directory holding three files:
#
#   trustvian.dump   pg_dump custom-format archive: schema + data
#   MANIFEST         key=value metadata — no host, user, database, or secret
#   SHA256SUMS       checksums of the two files above (sha256sum -c format)
#
# Connection settings come ONLY from the standard libpq environment
# (PGHOST, PGPORT, PGUSER, PGDATABASE, PGPASSFILE / PGPASSWORD, PGSSLMODE,
# PGSERVICE). There is deliberately no --dsn flag: a connection string on a
# command line is visible to every user who can list processes.
#
# The backup is taken ONLINE. pg_dump reads the whole database in one
# REPEATABLE READ snapshot, and every Trustvian write is a single-row
# transaction, so the archive is consistent without stopping Trustvian. See
# docs/operations.md § Backup consistency.
#
# Exit status is 0 only when the archive was written, its table of contents
# was confirmed to hold both Trustvian tables with their data, and its
# checksums were recorded.

set -euo pipefail

readonly DUMP_FILE="trustvian.dump"
readonly MANIFEST_FILE="MANIFEST"
readonly SUMS_FILE="SHA256SUMS"
readonly MANIFEST_FORMAT="trustvian-postgres-backup"
readonly MANIFEST_FORMAT_VERSION="1"
# Table names mirror internal/store/postgres/schema.go (BaselineTable,
# SchemaVersionTable). The runtime owns the schema; this script only checks
# that the tables it is about to archive are the ones the runtime created.
readonly BASELINE_TABLE="trustvian_baseline"
readonly VERSION_TABLE="trustvian_schema_version"

usage() {
    cat >&2 <<'EOF'
usage: backup-postgres.sh --output <new-directory> [--trustvian-version <vX.Y.Z>]

Connection comes from the libpq environment (PGHOST, PGPORT, PGUSER,
PGDATABASE, PGPASSFILE/PGPASSWORD, PGSSLMODE, PGSERVICE). PGDATABASE is
required. See docs/operations.md.
EOF
    exit 2
}

fail() {
    printf 'backup-postgres: ERROR: %s\n' "$1" >&2
    exit 1
}

log() {
    printf 'backup-postgres: %s\n' "$1"
}

output=""
trustvian_version=""

while [ $# -gt 0 ]; do
    case "$1" in
        --output)
            [ $# -ge 2 ] || usage
            output="$2"
            shift 2
            ;;
        --trustvian-version)
            [ $# -ge 2 ] || usage
            trustvian_version="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            printf 'backup-postgres: unknown argument: %s\n' "$1" >&2
            usage
            ;;
    esac
done

[ -n "$output" ] || usage

# ---------------------------------------------------------------------
# Preconditions that need no database. Checked first, so a mistake is
# reported before anything connects anywhere.
# ---------------------------------------------------------------------

if [ -n "$trustvian_version" ] &&
    ! [[ "$trustvian_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.+-]+)?$ ]]; then
    fail "--trustvian-version must be an exact release version such as v0.8.0"
fi

# Never overwrite. An existing path — file, directory, or dangling link — is
# most likely a previous backup, and replacing it silently destroys the one
# thing a backup is for.
if [ -e "$output" ] || [ -L "$output" ]; then
    fail "output '$output' already exists; refusing to overwrite a previous backup"
fi

output="${output%/}"
parent="$(dirname "$output")"
[ -d "$parent" ] || fail "parent directory '$parent' does not exist"
[ -w "$parent" ] || fail "parent directory '$parent' is not writable"

# Unset libpq defaults PGDATABASE to the user name — a surprising database to
# back up. Uncertainty about WHICH database is exactly what must fail loudly.
if [ -z "${PGDATABASE:-}" ] && [ -z "${PGSERVICE:-}" ]; then
    fail "PGDATABASE is not set; name the Trustvian database explicitly"
fi

for tool in pg_dump pg_restore psql; do
    command -v "$tool" >/dev/null 2>&1 || fail "required tool '$tool' not found on PATH"
done

if command -v sha256sum >/dev/null 2>&1; then
    sha256() { sha256sum "$@"; }
elif command -v shasum >/dev/null 2>&1; then
    sha256() { shasum -a 256 "$@"; }
else
    fail "required tool 'sha256sum' (or 'shasum') not found on PATH"
fi

# ---------------------------------------------------------------------
# Private staging directory, created before anything is written anywhere.
# Every intermediate file lives inside it, so nothing lands in a shared,
# predictable /tmp path, and a failure at any point removes all of it.
# ---------------------------------------------------------------------

umask 077

staging="$(mktemp -d "$parent/.trustvian-backup.XXXXXX")"
cleanup() {
    # Only reached with staging still present on failure: success renames it.
    if [ -n "${staging:-}" ] && [ -d "$staging" ]; then
        rm -rf "$staging"
    fi
}
trap cleanup EXIT

# ---------------------------------------------------------------------
# Confirm this is a Trustvian database in a state worth archiving.
# ---------------------------------------------------------------------

# -w: never prompt for a password — a backup script that hangs waiting on a
# terminal is a backup that silently never happens. -X: ignore psqlrc.
psql_query() {
    psql -w -X -q -A -t -v ON_ERROR_STOP=1 -c "$1"
}

if ! server_version="$(psql_query 'SHOW server_version' 2>"$staging/connect.err")"; then
    # libpq connection errors name host, user, and database but never the
    # password, so the last lines are safe to surface and useful to read.
    detail="$(tail -n 3 "$staging/connect.err" 2>/dev/null || true)"
    fail "cannot connect to the source database: ${detail:-no detail}"
fi
rm -f "$staging/connect.err"
server_version="$(printf '%s' "$server_version" | tr -d '[:space:]')"

has_tables="$(psql_query "SELECT (to_regclass('$VERSION_TABLE') IS NOT NULL)::int + (to_regclass('$BASELINE_TABLE') IS NOT NULL)::int")"
has_tables="$(printf '%s' "$has_tables" | tr -d '[:space:]')"
[ "$has_tables" = "2" ] ||
    fail "the source database does not contain Trustvian's tables ($BASELINE_TABLE, $VERSION_TABLE); refusing to back up what may be the wrong database"

version_rows="$(psql_query "SELECT count(*) FROM $VERSION_TABLE" | tr -d '[:space:]')"
[ "$version_rows" = "1" ] ||
    fail "$VERSION_TABLE holds $version_rows row(s), expected exactly 1 — the schema metadata is ambiguous (Trustvian itself refuses to start on it). Use pg_dump directly for a forensic copy."

schema_version="$(psql_query "SELECT version FROM $VERSION_TABLE" | tr -d '[:space:]')"

# ---------------------------------------------------------------------
# Dump, verify, and rename into place.
# ---------------------------------------------------------------------

log "starting online backup to $output"

# The bare version: `pg_dump (PostgreSQL) 17.6`, or on a Debian build
# `pg_dump (PostgreSQL) 17.6 (Debian 17.6-1.pgdg120+1)`, whose last field is
# not a version at all.
pg_dump_version="$(pg_dump --version | sed -n 's/^pg_dump (PostgreSQL) \([0-9][0-9.]*\).*/\1/p')"
pg_dump_version="${pg_dump_version:-unknown}"

# pg_dump itself refuses a server newer than itself ("server version
# mismatch"), which is the dangerous direction; that error is surfaced as-is.
if ! pg_dump -w --format=custom --file="$staging/$DUMP_FILE" 2>"$staging/pg_dump.err"; then
    detail="$(tail -n 5 "$staging/pg_dump.err" 2>/dev/null || true)"
    fail "pg_dump failed: ${detail:-no detail}"
fi
rm -f "$staging/pg_dump.err"

# Prove the archive is readable and holds what a recovery needs: both
# tables' definitions AND their data. A dump that lacks TABLE DATA for the
# baseline table would restore an empty schema — a silent reset.
toc="$(pg_restore --list "$staging/$DUMP_FILE")" || fail "the written archive cannot be read back by pg_restore"
for entry in "TABLE [^ ]+ $BASELINE_TABLE( |\$)" "TABLE [^ ]+ $VERSION_TABLE( |\$)" \
    "TABLE DATA [^ ]+ $BASELINE_TABLE( |\$)" "TABLE DATA [^ ]+ $VERSION_TABLE( |\$)"; do
    grep -Eq "$entry" <<<"$toc" || fail "the archive is missing an expected entry matching '$entry'"
done

created_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
    printf 'format=%s\n' "$MANIFEST_FORMAT"
    printf 'format_version=%s\n' "$MANIFEST_FORMAT_VERSION"
    printf 'created_at=%s\n' "$created_at"
    printf 'dump_file=%s\n' "$DUMP_FILE"
    printf 'dump_format=custom\n'
    printf 'trustvian_schema_version=%s\n' "$schema_version"
    printf 'trustvian_version=%s\n' "${trustvian_version:-unknown}"
    printf 'postgres_server_version=%s\n' "$server_version"
    printf 'pg_dump_version=%s\n' "$pg_dump_version"
} >"$staging/$MANIFEST_FILE"

(cd "$staging" && sha256 "$DUMP_FILE" "$MANIFEST_FILE" >"$SUMS_FILE")
(cd "$staging" && sha256 -c "$SUMS_FILE" >/dev/null) || fail "checksum self-verification failed"

chmod 0600 "$staging/$DUMP_FILE" "$staging/$MANIFEST_FILE" "$staging/$SUMS_FILE"
chmod 0700 "$staging"

# Same filesystem, so the rename is atomic: $output either does not exist or
# is a complete backup. Re-check first in case it appeared meanwhile.
[ -e "$output" ] && fail "output '$output' appeared during the backup; refusing to overwrite it"
mv "$staging" "$output"
staging=""
trap - EXIT

dump_sha="$(awk -v f="$DUMP_FILE" '$2 == f || $2 == "*" f {print $1}' "$output/$SUMS_FILE")"

log "backup complete"
log "  directory:      $output"
log "  schema version: $schema_version"
log "  dump sha256:    $dump_sha"
log "treat this directory as sensitive: it contains learned behavioral state"
