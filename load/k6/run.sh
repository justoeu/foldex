#!/usr/bin/env bash
# Manual Foldex load. See load/k6/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
FLOW="${1:-smoke}"

case "$FLOW" in
  smoke|session|library-read|library-write|stats|activity|settings|admin|redirect|export|mixed) ;;
  *)
    echo "unknown flow: $FLOW" >&2
    echo "flows: smoke session library-read library-write stats activity settings admin redirect export mixed" >&2
    exit 2
    ;;
esac

PROFILE="${K6_PROFILE:-smoke}"
case "$PROFILE" in
  smoke)
    : "${FOLDEX_K6_VUS:=1}"
    : "${FOLDEX_K6_DURATION:=15s}"
    ;;
  read)
    : "${FOLDEX_K6_VUS:=8}"
    : "${FOLDEX_K6_DURATION:=1m}"
    ;;
  write)
    : "${FOLDEX_K6_VUS:=1}"
    : "${FOLDEX_K6_DURATION:=30s}"
    ;;
  stress)
    if [[ "${K6_I_MEAN_IT:-}" != "1" ]]; then
      echo "stress trips rate limits on purpose. Re-run with K6_I_MEAN_IT=1." >&2
      exit 2
    fi
    : "${FOLDEX_K6_VUS:=20}"
    : "${FOLDEX_K6_DURATION:=2m}"
    ;;
  *)
    echo "unknown K6_PROFILE: $PROFILE (smoke, read, write, stress)" >&2
    exit 2
    ;;
esac

if [[ "$FLOW" == "export" ]]; then
  FOLDEX_K6_VUS=1
  FOLDEX_K6_DURATION=1s
fi

if ! command -v k6 >/dev/null 2>&1; then
  echo "k6 is not installed. brew install k6" >&2
  exit 1
fi

export K6_FLOW="$FLOW"
export K6_PROFILE="$PROFILE"
export FOLDEX_K6_VUS
export FOLDEX_K6_DURATION

echo "k6 flow=$FLOW profile=$PROFILE vus=$FOLDEX_K6_VUS duration=$FOLDEX_K6_DURATION base=${K6_BASE_URL:-http://127.0.0.1:9089}"
exec k6 run "$ROOT/load/k6/foldex.js"
