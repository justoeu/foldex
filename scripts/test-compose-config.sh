#!/usr/bin/env bash
# Every docker-compose*.yml in the repo must PARSE. `docker compose config`
# is the only tool that proves it — and no guard ran it, so v2.16.0 shipped
# with a duplicated RUSTFS_* env block in the backup service and the main
# compose file refused to load: `mapping key "RUSTFS_ENDPOINT" already
# defined`. Every other guard that reads this file does so with grep/awk,
# which walks straight past a duplicate YAML key; CI stayed green while
# `make up` was broken for every operator.
#
# --no-interpolate keeps this a pure parse check: duplicate-key detection
# happens in the YAML parser, BEFORE interpolation, while interpolation is
# exactly what cannot run here — docker-compose.services.yml carries
# ${RUSTFS_ROOT_SECRET_KEY:?} gates that must keep failing for real users
# without an .env (INV-099) and must not fail this guard on a bare runner.
#
# The loop globs docker-compose*.yml so a sixth file is born covered.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

# The guard must be able to fail: feed it the exact defect it exists for.
self_test() {
  local fixture
  fixture=$(mktemp -d)
  trap 'rm -rf "$fixture"' RETURN
  cat > "$fixture/docker-compose.yml" <<'YAML'
services:
  app:
    image: alpine
    environment:
      RUSTFS_ENDPOINT: a
      RUSTFS_ENDPOINT: b
YAML
  if docker compose -f "$fixture/docker-compose.yml" config --no-interpolate --quiet 2>/dev/null; then
    echo "self-test FAILED: a duplicated mapping key parsed cleanly — this guard can no longer detect the v2.16.0 defect" >&2
    return 1
  fi
  echo "ok: self-test — duplicate key is refused"
}

self_test

fail=0
for f in "$ROOT"/docker-compose*.yml; do
  # An operator's local docker-compose.override.yml never reaches CI (it is
  # host-local, like .env); the glob covers it on dev machines for free.
  if docker compose -f "$f" config --no-interpolate --quiet; then
    echo "ok: $(basename "$f") parses"
  else
    echo "FAIL: $(basename "$f") does not parse — see the compose error above" >&2
    fail=1
  fi
done
exit "$fail"
