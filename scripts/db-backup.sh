#!/usr/bin/env bash
# Take a logical backup of the Nester Postgres database via pg_dump, in the
# custom (-Fc) format so it restores with pg_restore and supports parallel
# restore + selective table/schema restore later (nester#795).
#
# Local/self-hosted path only. For a managed Postgres (RDS/Cloud SQL/etc.)
# use the provider's own automated-snapshot + PITR feature instead — see
# docs/database-backup-restore.md for guidance on wiring that up; this script
# is the self-hosted fallback docker-compose and CI rely on.
#
# Required env (or flag):
#   DATABASE_DSN   — postgres connection string. Defaults to the docker-compose
#                    dev DSN so `make db-backup` works out of the box locally.
#
# Optional env:
#   BACKUP_DIR       — where to write the artifact. Default: ./backups
#   BACKUP_RETENTION_DAYS — prune artifacts older than this many days after a
#                    successful backup. Default: 14. Set to 0 to disable pruning.
#
# Output: ${BACKUP_DIR}/nester_<UTC timestamp>.dump
#
set -euo pipefail

DSN="${DATABASE_DSN:-postgres://nester:nester_dev_password@localhost:5432/nester_dev?sslmode=disable}"
BACKUP_DIR="${BACKUP_DIR:-./backups}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"

if ! command -v pg_dump >/dev/null 2>&1; then
  echo "pg_dump not found on PATH. Install the postgresql-client package (matching the server's major version) or run this inside the api container." >&2
  exit 1
fi

mkdir -p "$BACKUP_DIR"

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
out_file="${BACKUP_DIR}/nester_${timestamp}.dump"
tmp_file="${out_file}.tmp"

echo "Backing up to ${out_file} ..."

# -Fc: custom format — compressed, restorable with pg_restore, supports
#   --jobs for parallel restore and selective restore of individual tables.
# --no-owner / --no-privileges: the artifact must be restorable into a fresh
#   instance whose roles don't necessarily match production's, without a
#   restore failing on missing GRANT/OWNER targets.
# Write to a .tmp path first and rename on success, so a crashed or killed
# backup never leaves a truncated file that looks like a valid artifact.
if ! pg_dump "$DSN" -Fc --no-owner --no-privileges -f "$tmp_file"; then
  echo "pg_dump failed; removing incomplete artifact." >&2
  rm -f "$tmp_file"
  exit 1
fi

mv "$tmp_file" "$out_file"

# Sanity-check the artifact is a readable dump before declaring success —
# catches silent truncation/corruption that a nonzero pg_dump exit wouldn't.
if ! pg_restore --list "$out_file" >/dev/null 2>&1; then
  echo "Backup artifact at ${out_file} failed pg_restore --list validation; not trusting it." >&2
  exit 1
fi

size_human="$(du -h "$out_file" | cut -f1)"
echo "Backup complete: ${out_file} (${size_human})"

# Secrets check: a logical pg_dump of application tables should never contain
# plaintext credentials — the DB itself stores hashed passwords and encrypted
# secrets, never raw ones (see apps/api/internal/domain for how secrets are
# stored). This is a best-effort tripwire, not a guarantee: it greps the dump
# for common secret-shaped prefixes and fails loudly rather than shipping a
# backup that might carry one.
if command -v grep >/dev/null 2>&1; then
  # pg_restore -l gives a TOC; scanning it plus the compressed dump's raw
  # bytes for obvious plaintext secret prefixes is cheap and catches the
  # "someone added a plaintext-secret column" regression class.
  if strings "$out_file" 2>/dev/null | grep -qE '(BEGIN (RSA|EC|OPENSSH) PRIVATE KEY|sntrys_[A-Za-z0-9]{10,})'; then
    echo "WARNING: backup artifact ${out_file} appears to contain a private key or Sentry auth token pattern. Investigate before distributing this backup." >&2
    exit 1
  fi
fi

if [[ "$RETENTION_DAYS" -gt 0 ]]; then
  echo "Pruning backups older than ${RETENTION_DAYS} days in ${BACKUP_DIR} ..."
  find "$BACKUP_DIR" -maxdepth 1 -name 'nester_*.dump' -mtime "+${RETENTION_DAYS}" -print -delete
fi

echo "Done."
