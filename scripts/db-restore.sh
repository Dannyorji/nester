#!/usr/bin/env bash
# Restore a Nester Postgres backup (produced by scripts/db-backup.sh) into a
# target database, then verify the restore actually brought the schema to a
# state the API can boot against (nester#795).
#
# Usage:
#   scripts/db-restore.sh <path-to-backup.dump> [target DSN]
#
# If the target DSN is omitted, defaults to the docker-compose dev DSN. The
# target database is expected to already exist and be empty (or at least not
# contain conflicting objects) — this script does not create databases or
# drop the target for you, on purpose: a restore that silently DROPs a
# database is exactly the kind of destructive default this runbook exists to
# avoid. See docs/database-backup-restore.md for the "restore drill"
# checklist, including how to spin up a scratch database/container instead of
# restoring over a live one.
#
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 <path-to-backup.dump> [target DSN]" >&2
  exit 1
fi

DUMP_FILE="$1"
DSN="${2:-${DATABASE_DSN:-postgres://nester:nester_dev_password@localhost:5432/nester_dev?sslmode=disable}}"

if [[ ! -f "$DUMP_FILE" ]]; then
  echo "Backup file not found: ${DUMP_FILE}" >&2
  exit 1
fi

if ! command -v pg_restore >/dev/null 2>&1; then
  echo "pg_restore not found on PATH. Install the postgresql-client package (matching the server's major version) or run this inside the api container." >&2
  exit 1
fi

echo "Validating archive: ${DUMP_FILE} ..."
if ! pg_restore --list "$DUMP_FILE" >/dev/null; then
  echo "Not a valid pg_restore archive: ${DUMP_FILE}" >&2
  exit 1
fi

echo "Restoring ${DUMP_FILE} into target database ..."
# --clean --if-exists: drop conflicting objects before recreating them, so
#   a restore into a database that already has the (possibly stale) schema
#   doesn't fail on "relation already exists".
# --no-owner --no-privileges: mirrors db-backup.sh's dump flags; the target
#   database's roles don't need to match the source's for the restore to
#   succeed.
# --exit-on-error: a partially-applied restore is worse than a loud failure —
#   fail fast rather than leaving the target in an ambiguous half-restored
#   state.
if ! pg_restore \
  --dbname="$DSN" \
  --clean \
  --if-exists \
  --no-owner \
  --no-privileges \
  --exit-on-error \
  "$DUMP_FILE"; then
  echo "Restore failed. Target database may be in a partial state — do not point the API at it. See the restore runbook's rollback guidance." >&2
  exit 1
fi

echo "Restore complete. Verifying schema/migration state ..."

# Ordered migration-state check: the restored schema_migrations table must
# contain every version the API's own migration files expect, or the app can
# boot against a schema it doesn't actually match (nester#795 acceptance
# criterion: "restore runbook covers verification + migration-version
# reconciliation").
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="${SCRIPT_DIR}/../apps/api/migrations"

if ! command -v psql >/dev/null 2>&1; then
  expected_count=0
  for f in "$MIGRATIONS_DIR"/*.up.sql; do [[ -e "$f" ]] && expected_count=$((expected_count + 1)); done
  echo "psql not found on PATH — skipping automated migration-version check. Verify manually: compare SELECT version FROM schema_migrations against the ${expected_count} expected *.up.sql files in ${MIGRATIONS_DIR}." >&2
  exit 0
fi

restored_versions="$(psql "$DSN" -Atc "SELECT version FROM schema_migrations ORDER BY version;" 2>/dev/null || true)"

if [[ -z "$restored_versions" ]]; then
  echo "ERROR: schema_migrations is empty or missing after restore. The backup may predate migration tracking, or the restore did not include this table. Do not point the API at this database until this is resolved." >&2
  exit 1
fi

missing=0
for up_file in "$MIGRATIONS_DIR"/*.up.sql; do
  [[ -e "$up_file" ]] || continue
  version="$(basename "$up_file" .up.sql)"
  if ! grep -qxF "$version" <<<"$restored_versions"; then
    echo "MISSING migration in restored database: ${version}" >&2
    missing=1
  fi
done

if [[ "$missing" -eq 1 ]]; then
  echo "" >&2
  echo "Restored database is missing migrations this codebase expects. This is expected if you restored an OLDER backup on purpose (e.g. point-in-time recovery to before a bad migration) — in that case, run the API's normal migration step (RUN_MIGRATIONS=true, or 'go run ./cmd/migrate up') to bring it forward before pointing the API at it." >&2
  echo "If this is unexpected, the backup or restore is incomplete — do not point the API at this database." >&2
  exit 1
fi

echo "Migration state OK: restored database has all $(basename -a "$MIGRATIONS_DIR"/*.up.sql 2>/dev/null | wc -l | tr -d ' ') expected migrations applied."
echo ""
echo "Next: point APP's DATABASE_DSN at this database and verify /health/detailed reports healthy before serving traffic. See docs/database-backup-restore.md."
