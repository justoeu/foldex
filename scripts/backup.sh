#!/usr/bin/env bash
# Foldex backup helper — dumps the postgres volume to ./backups/.
#
# Usage:
#   scripts/backup.sh                 # writes backups/foldex-YYYYMMDD-HHMMSS.sql.gz
#
# Schedule (macOS launchd or crontab) every night at 02:30:
#   30 2 * * *  /absolute/path/to/foldex/scripts/backup.sh
#
# Restore:
#   gunzip -c backups/foldex-XXXX.sql.gz | docker compose exec -T db psql -U foldex foldex

set -euo pipefail

cd "$(dirname "$0")/.."

# Load .env so we know POSTGRES_USER/DB.
if [ -f .env ]; then
  set -a; . ./.env; set +a
fi

POSTGRES_USER="${POSTGRES_USER:-foldex}"
POSTGRES_DB="${POSTGRES_DB:-foldex}"

mkdir -p backups
TS=$(date +%Y%m%d-%H%M%S)
OUT="backups/foldex-${TS}.sql.gz"

docker compose exec -T db pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
  | gzip -9 > "$OUT"

# Rotate: keep the 14 most recent.
ls -1t backups/foldex-*.sql.gz 2>/dev/null | tail -n +15 | xargs -I{} rm -f {} || true

echo "Wrote $OUT ($(du -h "$OUT" | cut -f1))"
