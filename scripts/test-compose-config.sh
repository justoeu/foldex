#!/usr/bin/env bash
# Every docker-compose*.yml in the repo must PARSE. `docker compose config`
# is the only tool that proves it — and no guard ran it, so v2.16.0 shipped
# with a duplicated RUSTFS_* env block in the backup service and the main
# compose file refused to load: `mapping key "RUSTFS_ENDPOINT" already
# defined`. Every other guard that reads this file does so with grep/awk,
# which walks straight past a duplicate YAML key; CI stayed green while
# `make up` was broken for every operator.
#
# --no-interpolate covers syntax without invoking production credential gates.
# A separate render check supplies fixture secrets and an empty env file to
# verify security defaults without reading the operator's configuration.
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

python3 - "$ROOT" <<'PY'
import json
import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import urlsplit

root = Path(sys.argv[1])
base = dict(os.environ)
for key in ('POSTGRES_PASSWORD', 'DB_URL', 'PREVIEW_STRICT_SSRF'):
    base.pop(key, None)
base.update(RUSTFS_ROOT_SECRET_KEY='fixture-root-secret', RUSTFS_SECRET_KEY='fixture-app-secret')
for password in (None, '', 'fixture-custom-secret'):
    env = dict(base)
    if password is not None:
        env['POSTGRES_PASSWORD'] = password
    for name in ('docker-compose.db.yml', 'docker-compose.services.yml', 'docker-compose.yml'):
        rendered = subprocess.check_output(
            ['docker', 'compose', '--env-file', '/dev/null', '-f', str(root / name),
             '--profile', 'backup', 'config', '--format', 'json'], env=env, text=True)
        services = json.loads(rendered)['services']
        if 'db' in services:
            assert services['db']['environment']['POSTGRES_PASSWORD'] == (password or ''), name
        if 'backend' in services:
            assert urlsplit(services['backend']['environment']['DB_URL']).password == (password or ''), name
            assert services['backup']['environment']['POSTGRES_PASSWORD'] == (password or ''), name
            assert services['backend']['environment']['PREVIEW_STRICT_SSRF'] == '1', name
print('ok: no known Postgres fallback, optional services parse, SSRF is strict by default')
PY

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
