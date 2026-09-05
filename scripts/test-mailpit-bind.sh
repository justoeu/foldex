#!/usr/bin/env bash
# Mailpit is a development SMTP sink with a web inbox and no authentication.
# Publishing :1025/:8025 on 0.0.0.0 exposes that inbox to the LAN. Bind the
# published ports to loopback, matching BIND_HOST / rustfs.
#
# Do NOT use ${VAR:?} here or in docker-compose.mail.yml — that is a
# whole-file gate (INV-032), not a per-service one.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
FILE="$ROOT/docker-compose.mail.yml"

check_file() {
  local f=$1
  local fail=0
  local maps
  maps=$(grep -E '^[[:space:]]+-[[:space:]]+' "$f" | grep -E ':[0-9]+' || true)
  if [[ -z "$maps" ]]; then
    echo "FAIL: $f has no published port mappings to check" >&2
    return 1
  fi
  while IFS= read -r line; do
    stripped=$(printf '%s' "$line" | sed "s/^[[:space:]]*-[[:space:]]*//; s/[\"']//g")
    case "$stripped" in
      127.0.0.1:*)
        ;;
      *)
        echo "FAIL: published port is not loopback-prefixed: $line" >&2
        fail=1
        ;;
    esac
  done <<<"$maps"
  return "$fail"
}

self_test() {
  local fixture
  fixture=$(mktemp)
  trap 'rm -f "$fixture"' RETURN
  cat > "$fixture" <<'YAML'
services:
  mailpit:
    ports:
      - '${MAILPIT_SMTP_PORT:-1025}:1025'
      - '${MAILPIT_WEB_PORT:-8025}:8025'
YAML
  if check_file "$fixture" 2>/dev/null; then
    echo "self-test FAILED: unbound ports were accepted" >&2
    return 1
  fi
  echo "ok: self-test — unbound ports are refused"
}

self_test
if check_file "$FILE"; then
  echo "ok: docker-compose.mail.yml publishes Mailpit on loopback"
else
  echo "FAIL: docker-compose.mail.yml publishes Mailpit without 127.0.0.1" >&2
  exit 1
fi
