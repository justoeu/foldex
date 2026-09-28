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
export FOLDEX_K6_RUN="${FOLDEX_K6_RUN:-${FLOW}-$(date +%Y%m%d%H%M%S)}"

# The LAN Prometheus (Grafana at :3001) only stores a remote write when its
# receiver is on. K6_GRAFANA=0 keeps the terminal summary and skips the push.
GRAFANA="${K6_GRAFANA:-1}"
OUT=()
if [[ "$GRAFANA" != "0" ]]; then
  export K6_PROMETHEUS_RW_SERVER_URL="${K6_PROMETHEUS_RW_SERVER_URL:-http://192.168.68.65:9091/api/v1/write}"
  export K6_PROMETHEUS_RW_TREND_STATS="${K6_PROMETHEUS_RW_TREND_STATS:-p(95),p(99),max}"
  export K6_PROMETHEUS_RW_PUSH_INTERVAL="${K6_PROMETHEUS_RW_PUSH_INTERVAL:-5s}"
  OUT=(-o experimental-prometheus-rw)
fi

echo "k6 flow=$FLOW profile=$PROFILE vus=$FOLDEX_K6_VUS duration=$FOLDEX_K6_DURATION run=$FOLDEX_K6_RUN base=${K6_BASE_URL:-http://127.0.0.1:9089}"
if [[ ${#OUT[@]} -gt 0 ]]; then
  echo "grafana remote write: $K6_PROMETHEUS_RW_SERVER_URL"
fi
exec k6 run "${OUT[@]}" "$ROOT/load/k6/foldex.js"
