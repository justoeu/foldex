#!/bin/bash
# Local admission-gate run. The EXIT trap puts the stock backend, the write
# quota and the test account back. The LAN API is not touched. The binary is
# built from the admission worktree with a throwaway write ceiling; that
# source file is not modified. Logs stay in FOLDEX_K6_RUN_DIR (default /tmp).
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=k6-lib.sh
source "$ROOT/scripts/k6-lib.sh"
k6_load_operator_env "$ROOT" || {
  echo "load/k6/env.local tem uma linha inválida" >&2
  exit 2
}
export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
DIR="${FOLDEX_K6_RUN_DIR:-/tmp/foldex-gate}"
LOG="$DIR/runner.log"
SAMPLE="$DIR/samples.tsv"
ORIG="${FOLDEX_K6_BACKEND:-foldex-backend-1}"
GATE=foldex-backend-gate
PGBOX="${FOLDEX_K6_POSTGRES:-}"
ADMISSION="${FOLDEX_ADMISSION_ROOT:-$(cd "$ROOT/../foldex-admission" 2>/dev/null && pwd || true)}"
ACTIVE="$ORIG"
orig_stopped=0
gate_started=0
restored=0
SAMPLER=
FLOW_PID=
STAMP="$(date +%Y%m%d%H%M%S)"

mkdir -p "$DIR"
chmod 700 "$DIR"
echo $$ > "$DIR/runner.pid"
: > "$LOG"
printf 'ts\tphase\trss_kb\tthreads\tfds\tbackend_cpu\tbackend_mem\tpostgres_cpu\tpostgres_mem\thealth\tgoroutines\tin_flight\tacquired\tidle\tempty_acquire\tempty_wait_s\tcanceled\thttp503\thttp200\tdb\n' > "$SAMPLE"

phase() {
  printf '%s\n' "$1" > "$DIR/phase"
  echo "PHASE:$1"
  echo "PHASE:$1 $(date +%H:%M:%S)" >>"$LOG"
}

policy() {
  local url target="${FOLDEX_ORDERS_TARGET:-6000}"
  url="$(cat "$DIR/db_url")"
  FOLDEX_K6_NETWORK=foldex FOLDEX_K6_DB_URL="$url" FOLDEX_ORDERS_TARGET="$target" \
    FOLDEX_ORDERS_ORIG="$DIR/abuse_policy.orig" \
    python3 "$ROOT/scripts/k6_quota.py" "$@"
  local code=$?
  unset FOLDEX_K6_DB_URL
  return "$code"
}

sample_once() {
  local phase_name="$1" ts proc rss threads fds stats be_cpu be_mem pg_cpu pg_mem db health box metrics
  box="${2:-$ACTIVE}"
  ts="$(date +%H:%M:%S)"
  if [[ -n "$box" ]] && docker ps --format '{{.Names}}' | grep -qx "$box"; then
    proc="$(docker exec "$box" sh -c 'awk "/^VmRSS:/{r=\$2} /^Threads:/{t=\$2} END{print r, t}" /proc/1/status; ls /proc/1/fd 2>/dev/null | wc -l' 2>>"$LOG")" || proc=$'0 0\n0'
    stats="$(k6_container_stats "$box" 2>>"$LOG" || true)"
  else
    proc=$'0 0\n0'
    stats=""
  fi
  rss="$(printf '%s\n' "$proc" | awk 'NR==1{print $1}')"
  threads="$(printf '%s\n' "$proc" | awk 'NR==1{print $2}')"
  fds="$(printf '%s\n' "$proc" | awk 'NR==2{print $1}')"
  be_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$box" '$1==n {print $2}')"
  be_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$box" '$1==n {print $3}')"
  pg_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $2}')"
  pg_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $3}')"
  db="$(policy stats 2>>"$LOG" || echo stats_fail)"
  health="$(k6_health_code 3)"
  if [[ -s "$DIR/env" ]]; then
    metrics="$(FOLDEX_GATE_ENV="$DIR/env" FOLDEX_GATE_ALERT="$DIR/alert" python3 "$ROOT/scripts/k6_metrics.py" 2>>"$LOG" || echo na)"
  else
    metrics="na"
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$ts" "$phase_name" "${rss:-0}" "${threads:-0}" "${fds:-0}" \
    "${be_cpu:-na}" "${be_mem:-na}" "${pg_cpu:-na}" "${pg_mem:-na}" "$health" \
    "${metrics:-na}" "$db" >>"$SAMPLE"
  if [[ "$rss" =~ ^[0-9]+$ && "$rss" -gt 400000 ]]; then
    echo "rss_kb=$rss" > "$DIR/alert"
  fi
  if [[ "$phase_name" == gate-* && "$phase_name" != *-end && "$db" != stats_fail ]]; then
    local live
    live="$(policy show 2>>"$LOG" || true)"
    if [[ -n "$live" && "$live" != "writes=20000" ]]; then
      echo "quota drift $live" > "$DIR/alert"
    fi
  fi
}

restore_quota() {
  if [[ "$restored" == 1 ]]; then
    return 0
  fi
  restored=1
  if [[ ! -s "$DIR/db_url" ]]; then
    echo "RESTORE_SKIP no db url" >>"$LOG"
    return 1
  fi
  if ! policy restore >>"$LOG" 2>&1; then
    echo "RESTORE_FAIL" >>"$LOG"
    return 1
  fi
  return 0
}

remove_gate() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$GATE"; then
    docker inspect "$GATE" --format 'oom={{.State.OOMKilled}} status={{.State.Status}} exit={{.State.ExitCode}}' >>"$LOG" 2>&1 || true
    docker stop -t 15 "$GATE" >>"$LOG" 2>&1 || true
    docker rm "$GATE" >>"$LOG" 2>&1 || true
  fi
  gate_started=0
}

start_orig() {
  local i code
  docker start "$ORIG" >>"$LOG" 2>&1 || return 1
  ACTIVE="$ORIG"
  for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
    code="$(k6_health_code 3)"
    if [[ "$code" == 200 ]]; then
      echo "original health 200" >>"$LOG"
      return 0
    fi
    sleep 1
  done
  echo "original health failed" >>"$LOG"
  return 1
}

on_exit() {
  local code=$? show before now
  if [[ -n "${SAMPLER}" ]]; then
    kill "$SAMPLER" 2>/dev/null || true
    wait "$SAMPLER" 2>/dev/null || true
    SAMPLER=
  fi
  if [[ -n "${FLOW_PID}" ]]; then
    kill "$FLOW_PID" 2>/dev/null || true
    wait "$FLOW_PID" 2>/dev/null || true
    FLOW_PID=
  fi
  restore_quota || code=1
  remove_gate || code=1
  if [[ "$orig_stopped" == 1 ]]; then
    start_orig || code=1
  fi
  if [[ -f "$ROOT/load/k6/.test-user" ]]; then
    # shellcheck source=/dev/null
    source "$ROOT/load/k6/test-user.sh"
    unset K6_DB_CONTAINER
    if k6_db_delete "k6-load@foldex.local" >>"$LOG" 2>&1; then
      rm -f "$ROOT/load/k6/.test-user"
      echo "test user removed" >>"$LOG"
    else
      echo "LEFTOVER_USER" >>"$LOG"
      code=1
    fi
  fi
  show="$(policy show 2>>"$LOG" || true)"
  echo "final $show" >>"$LOG"
  if [[ -f "$DIR/quota_before" && -n "$show" ]]; then
    before="$(cat "$DIR/quota_before")"
    now="${show#writes=}"
    if [[ "$now" != "$before" ]]; then
      echo "QUOTA_MISMATCH before=$before now=$now" >>"$LOG"
      code=1
    else
      echo "QUOTA_OK before=$before now=$now" >>"$LOG"
      rm -f "$DIR/abuse_policy.orig"
    fi
  fi
  rm -f "$DIR/env" "$DIR/db_url" "$DIR/foldex" "$DIR/abusepolicy.go" "$DIR/overlay.json"
  echo "$code" > "$DIR/exit_code"
  echo "EXIT:$code" >>"$LOG"
  echo "EXIT:$code"
  exit "$code"
}
trap on_exit EXIT

phase preflight
if [[ -z "$ADMISSION" || ! -d "$ADMISSION/backend" ]]; then
  echo "admission worktree missing" >>"$LOG"
  exit 1
fi
mkdir -p "$DIR"
if ! bash "$ROOT/scripts/k6-build-overlay.sh" "$ADMISSION" "$DIR/foldex" 20000 >>"$LOG" 2>&1; then
  echo "overlay build failed" >>"$LOG"
  exit 1
fi
chmod 755 "$DIR/foldex"
IMAGE="$(k6_running_image "$ORIG")"
CERTS="$(k6_certs_source "$ORIG")"
if [[ -z "$IMAGE" || -z "$CERTS" || ! -d "$CERTS" ]]; then
  echo "stock container image or certs missing" >>"$LOG"
  exit 1
fi
if [[ ! -x "$DIR/foldex" ]]; then
  echo "binary missing" >>"$LOG"
  exit 1
fi
if docker ps -a --format '{{.Names}}' | grep -qx "$GATE"; then
  echo "gate container already exists" >>"$LOG"
  exit 1
fi
k6_require_health 5 || {
  echo "health down before swap" >>"$LOG"
  exit 1
}
umask 077
docker exec "$ORIG" sh -c 'printf %s "$DB_URL"' > "$DIR/db_url"
chmod 600 "$DIR/db_url"
if [[ ! -s "$DIR/db_url" ]]; then
  echo "DB_URL vazio" >>"$LOG"
  exit 1
fi
docker inspect "$ORIG" --format '{{range .Config.Env}}{{println .}}{{end}}' > "$DIR/env"
chmod 600 "$DIR/env"
FOLDEX_GATE_ENV="$DIR/env" python3 - <<'PY'
import os
import secrets
from pathlib import Path
path = Path(os.environ["FOLDEX_GATE_ENV"])
lines = [ln for ln in path.read_text().splitlines() if ln and not ln.startswith("METRICS_TOKEN=")]
lines.append("METRICS_TOKEN=" + secrets.token_urlsafe(24))
path.write_text("\n".join(lines) + "\n")
print("metrics_token_set")
PY
before="$(policy show)" || exit 1
printf '%s\n' "${before#writes=}" > "$DIR/quota_before"
echo "quota_before=$before" >>"$LOG"
sample_once baseline "$ORIG"

phase stopping_original
docker stop -t 20 "$ORIG" >>"$LOG" 2>&1 || {
  echo "stop original failed" >>"$LOG"
  exit 1
}
orig_stopped=1
ACTIVE=
port_busy=1
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  code="$(k6_health_code 2)"
  if [[ "$code" != 200 ]]; then
    port_busy=0
    break
  fi
  sleep 1
done
if [[ "$port_busy" == 1 ]]; then
  echo "port still answering after stop" >>"$LOG"
  exit 1
fi

phase raising_quota
FOLDEX_ORDERS_TARGET=20000 policy raise >>"$LOG" 2>&1 || exit 1
raised="$(FOLDEX_ORDERS_TARGET=20000 policy show)" || exit 1
echo "quota_raised=$raised" >>"$LOG"
if [[ "$raised" != "writes=20000" ]]; then
  echo "RAISE_NOT_VISIBLE" >>"$LOG"
  exit 1
fi

phase starting_gate
publish="$(k6_publish_spec)" || exit $?
if ! docker run -d --name "$GATE" \
  --network foldex \
  --network-alias backend \
  --add-host localhost:host-gateway \
  --add-host host.docker.internal:host-gateway \
  --memory 512m \
  --restart no \
  --pull never \
  --user foldex:foldex \
  --env-file "$DIR/env" \
  -p "$publish" \
  -v "$CERTS:/etc/foldex/certs" \
  -v foldex-data:/data \
  -v "$DIR/foldex:/foldex:ro" \
  "$IMAGE" >>"$LOG" 2>&1; then
  echo "GATE_START_FAIL" >>"$LOG"
  exit 1
fi
gate_started=1
ACTIVE="$GATE"
gate_ok=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40; do
  code="$(k6_health_code 3)"
  if [[ "$code" == 200 ]]; then
    gate_ok=1
    break
  fi
  if ! docker ps --format '{{.Names}}' | grep -qx "$GATE"; then
    echo "gate exited during boot" >>"$LOG"
    docker logs "$GATE" 2>&1 | tail -n 40 >>"$LOG" || true
    exit 1
  fi
  sleep 1
done
if [[ "$gate_ok" != 1 ]]; then
  echo "gate health failed" >>"$LOG"
  docker logs "$GATE" 2>&1 | tail -n 40 >>"$LOG" || true
  exit 1
fi
# Metrics must answer before the ramp, or the pool series stays blind.
if ! FOLDEX_GATE_ENV="$DIR/env" python3 "$ROOT/scripts/k6_metrics.py" | awk -F'\t' 'NR==1 && $1 ~ /^[0-9]+$/ {ok=1} END{exit ok?0:1}'; then
  echo "METRICS_BLIND" >>"$LOG"
  exit 1
fi
# A prefix assignment does not survive `source` on this bash, so export first.
export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
export K6_TEST_USER=1
export K6_DB_CONTAINER="$GATE"
# shellcheck source=/dev/null
source "$ROOT/load/k6/test-user.sh"
k6_prepare_test_user >>"$LOG" 2>&1 || exit 1
sample_once gate-up "$GATE"
phase gate_up

start_sampler() {
  local name="$1"
  (
    while true; do
      sample_once "$name" "$GATE"
      sleep 5
    done
  ) &
  SAMPLER=$!
}

stop_sampler() {
  if [[ -n "${SAMPLER}" ]]; then
    kill "$SAMPLER" 2>/dev/null || true
    wait "$SAMPLER" 2>/dev/null || true
    SAMPLER=
  fi
}

run_flow() {
  local flow="$1" pid waited code cap
  echo "BEGIN $flow $(date +%H:%M:%S)" >>"$LOG"
  phase "$flow"
  start_sampler "$flow"
  (
    cd "$ROOT" || exit 1
    unset K6_VUS K6_DURATION
    export K6_NO_COLOR=true
    export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
    export K6_FLOW="$flow"
    export FOLDEX_K6_RUN="${flow}-${STAMP}"
    export FOLDEX_K6_TIMEOUT=30s
    k6_prepare_remote_write || exit 2
    set -e
    # shellcheck source=/dev/null
    source "$ROOT/load/k6/test-user.sh"
    k6_export_saved_credentials
    if [[ ${#K6_OUT[@]} -gt 0 ]]; then
      exec k6 run --include-system-env-vars \
        "${K6_OUT[@]}" \
        --summary-export "$DIR/${flow}-summary.json" \
        "$ROOT/scripts/k6_gate.js"
    fi
    exec k6 run --include-system-env-vars \
      --summary-export "$DIR/${flow}-summary.json" \
      "$ROOT/scripts/k6_gate.js"
  ) >"$DIR/$flow.log" 2>&1 &
  pid=$!
  FLOW_PID=$pid
  waited=0
  cap=900
  if [[ "$flow" == gate-climb ]]; then
    cap=180
  fi
  while kill -0 "$pid" 2>/dev/null; do
    sleep 10
    waited=$((waited + 10))
    if [[ -f "$DIR/alert" ]]; then
      echo "alert during $flow: $(cat "$DIR/alert")" >>"$LOG"
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      FLOW_PID=
      stop_sampler
      exit 1
    fi
    if [[ "$waited" -ge "$cap" ]]; then
      echo "TIMEOUT $flow" >>"$LOG"
      kill "$pid" 2>/dev/null || true
      sleep 2
      kill -9 "$pid" 2>/dev/null || true
      break
    fi
  done
  wait "$pid"
  code=$?
  FLOW_PID=
  stop_sampler
  sample_once "${flow}-end" "$GATE"
  echo "END $flow exit=$code $(date +%H:%M:%S)" >>"$LOG"
  echo "$code" > "$DIR/$flow.exit"
  if [[ "$code" != 0 && "$code" != 99 ]]; then
    exit 1
  fi
}

run_flow gate-write
run_flow gate-read
run_flow gate-mixed
run_flow gate-climb

sample_once after "$GATE"
echo "runs finished" >>"$LOG"
phase finished
exit 0
