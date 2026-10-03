#!/usr/bin/env bash
# Manual Foldex load. See load/k6/README.md.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck source=../../scripts/k6-lib.sh
source "$ROOT/scripts/k6-lib.sh"
k6_load_operator_env "$ROOT" || {
  echo "load/k6/env.local tem uma linha inválida" >&2
  exit 2
}
FLOW="${1:-smoke}"

case "$FLOW" in
  smoke|session|library-read|library-write|stats|activity|settings|admin|redirect|export|mixed|capacity|orders-write|orders-read|orders-mixed|surge-write|surge-read|surge-mixed) ;;
  *)
    echo "unknown flow: $FLOW" >&2
    echo "flows: smoke session library-read library-write stats activity settings admin redirect export mixed capacity orders-write orders-read orders-mixed surge-write surge-read surge-mixed" >&2
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

export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
export K6_FLOW="$FLOW"
export K6_PROFILE="$PROFILE"
export FOLDEX_K6_VUS
export FOLDEX_K6_DURATION
export FOLDEX_K6_RUN="${FOLDEX_K6_RUN:-${FLOW}-$(date +%Y%m%d%H%M%S)}"

# shellcheck source=test-user.sh
source "$ROOT/load/k6/test-user.sh"
k6_exit_done=0
# shellcheck disable=SC2329 # trap EXIT invokes this
k6_on_exit() {
  local code=$?
  if [[ "$k6_exit_done" == 1 ]]; then
    return
  fi
  k6_exit_done=1
  if [[ "${K6_TEST_USER_MANAGED:-0}" == 1 ]]; then
    if ! k6_finish_test_user; then
      if [[ "$code" == 0 ]]; then
        code=1
      fi
    fi
  fi
  exit "$code"
}
trap k6_on_exit EXIT
k6_prepare_test_user

# Remote write is off until load/k6/env.local or the environment names a
# receiver. There is no built-in address.
k6_prepare_remote_write || exit 2

echo "k6 flow=$FLOW profile=$PROFILE vus=$FOLDEX_K6_VUS duration=$FOLDEX_K6_DURATION run=$FOLDEX_K6_RUN base=${K6_BASE_URL}"
if [[ "$FLOW" == "capacity" ]]; then
  echo "capacity: 5 minutes, one step per minute. reads 20→400 iter/s, writes 12→192 iter/min, side by side"
fi
case "$FLOW" in
  orders-write) echo "orders-write: ramp 150→1500 creates/min over 5 minutes, then hold 2 minutes. Needs the write quota above 1500/min." ;;
  orders-read) echo "orders-read: ramp 300→3000 list GETs/min over 5 minutes, then hold 2 minutes." ;;
  orders-mixed) echo "orders-mixed: ramp to 1000 creates/min and 2000 list GETs/min, then hold 2 minutes. Needs the write quota above 1000/min." ;;
  surge-write) echo "surge-write: ramp 1000→10000 creates/min over 5 minutes, then hold 2 minutes. Needs the write quota above 10000/min." ;;
  surge-read) echo "surge-read: ramp 3000→30000 list GETs/min over 5 minutes, then hold 2 minutes." ;;
  surge-mixed) echo "surge-mixed: ramp to 8000 creates/min and 20000 list GETs/min, then hold 2 minutes. Needs the write quota above 8000/min." ;;
esac
if [[ ${#K6_OUT[@]} -gt 0 ]]; then
  echo "grafana remote write: $K6_PROMETHEUS_RW_SERVER_URL"
  if [[ -n "${K6_GRAFANA_URL:-}" ]]; then
    echo "grafana: $K6_GRAFANA_URL"
  fi
fi
set +e
if [[ ${#K6_OUT[@]} -gt 0 ]]; then
  k6 run "${K6_OUT[@]}" "$ROOT/load/k6/foldex.js"
else
  k6 run "$ROOT/load/k6/foldex.js"
fi
code=$?
set -e
exit "$code"
