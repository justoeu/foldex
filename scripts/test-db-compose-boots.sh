#!/usr/bin/env bash
# The bundled Postgres must actually start.
#
# docker-compose.db.yml pinned postgres:18 from the initial commit while
# mounting the volume at /var/lib/postgresql/data. The 18 image keeps its
# cluster in a major-version subdirectory and refuses to start when it finds the
# legacy path used as a mount — it cannot distinguish a fresh volume in the old
# layout from a 17 cluster nobody pg_upgraded, and starting either way would
# silently serve an empty database. So `make db-up` exited 1 on every clean
# install for three months, and nothing noticed: the DAST never reached the
# database, and the one machine running foldex points at a Postgres on the host.
#
# Reading the compose file cannot answer this. The image decides, and it decides
# at runtime, so the test boots the REAL file.
#
# The overlay below changes identity only — container name, volume, network —
# never the image, the mount or the healthcheck, which are the things under
# test. Without it the probe would collide with an operator's own `foldex-db`.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="foldexdbprobe$$"
PROBE_PASSWORD="probe-only-secret"
OVERLAY="$(mktemp -t foldex-db-probe.XXXXXX.yml)"
ENV_FIXTURE="$(mktemp -t foldex-db-env.XXXXXX)"

cat >"$OVERLAY" <<YML
services:
  db:
    container_name: ${PROJECT}
    restart: "no"
volumes:
  pgdata:
    name: ${PROJECT}_pgdata
networks:
  foldex:
    name: ${PROJECT}_network
    external: false
YML

compose() {
  env -u POSTGRES_PASSWORD -u POSTGRES_USER -u POSTGRES_DB \
    docker compose --env-file "$ENV_FIXTURE" -p "$PROJECT" \
    -f "$ROOT/docker-compose.db.yml" -f "$OVERLAY" "$@"
}

cleanup() {
  compose down -v --remove-orphans >/dev/null 2>&1 || true
  rm -f "$OVERLAY" "$ENV_FIXTURE"
}
trap cleanup EXIT

if ! compose up -d >/dev/null 2>&1; then
  echo "FAIL compose could not create the database container for the missing-password check" >&2
  exit 1
fi
refused=0
for i in $(seq 1 20); do
  state=$(docker inspect "$PROJECT" --format '{{.State.Status}}')
  if [[ "$state" == "exited" ]]; then
    if ! docker logs "$PROJECT" 2>&1 | grep 'superuser password is not specified' >/dev/null; then
      echo "FAIL Postgres exited for a reason other than the missing password:" >&2
      docker logs "$PROJECT" >&2
      exit 1
    fi
    refused=1
    break
  fi
  sleep 1
done
if [[ "$refused" != 1 ]]; then
  echo "FAIL Postgres did not refuse the missing password" >&2
  exit 1
fi
echo "the bundled Postgres refuses a missing password"
printf 'POSTGRES_PASSWORD=%s\n' "$PROBE_PASSWORD" >"$ENV_FIXTURE"

if ! compose up -d >/dev/null 2>&1; then
  echo "FAIL compose could not even create the database container" >&2
  exit 1
fi

for i in $(seq 1 45); do
  if docker exec -e "PGPASSWORD=$PROBE_PASSWORD" "$PROJECT" \
    psql -h 127.0.0.1 -U foldex -d foldex -Atc 'SELECT 1' >/dev/null 2>&1; then
    echo "the bundled Postgres accepts connections (${i} tries)"
    exit 0
  fi
  # A container that exited will never become ready — say so with the image's
  # own words instead of spending the whole budget waiting for it.
  state=$(docker inspect "$PROJECT" --format '{{.State.Status}}' 2>/dev/null || echo missing)
  if [[ "$state" == "exited" || "$state" == "dead" ]]; then
    echo "FAIL the database container exited instead of starting:" >&2
    docker logs "$PROJECT" 2>&1 | tail -20 >&2
    exit 1
  fi
  sleep 2
done

echo "FAIL the bundled Postgres never accepted connections" >&2
docker logs "$PROJECT" 2>&1 | tail -20 >&2
exit 1
