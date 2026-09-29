#!/bin/bash
# Aggressive local ramps. The EXIT trap puts the stock backend and the write
# quota back even when a phase fails. The throwaway binary only raises the
# compiled write ceiling; the repository constant stays 6000. Logs stay in
# FOLDEX_K6_RUN_DIR (default /tmp).
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=k6-lib.sh
source "$ROOT/scripts/k6-lib.sh"
k6_load_operator_env "$ROOT" || {
  echo "load/k6/env.local tem uma linha inválida" >&2
  exit 2
}
export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
DIR="${FOLDEX_K6_RUN_DIR:-/tmp/foldex-surge}"
LOG="$DIR/runner.log"
SAMPLE="$DIR/samples.tsv"
ORIG="${FOLDEX_K6_BACKEND:-foldex-backend-1}"
SURGE=foldex-backend-surge
PGBOX="${FOLDEX_K6_POSTGRES:-}"
ACTIVE="$ORIG"
orig_stopped=0
surge_started=0
restored=0
SAMPLER=
FLOW_PID=

mkdir -p "$DIR"
chmod 700 "$DIR"
echo $$ > "$DIR/runner.pid"
: > "$LOG"
printf 'ts\tphase\trss_kb\tthreads\tfds\tbackend_cpu\tbackend_mem\tpostgres_cpu\tpostgres_mem\thealth\tdb\n' > "$SAMPLE"

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
  local phase_name="$1" ts proc rss threads fds stats be_cpu be_mem pg_cpu pg_mem db health box
  box="${2:-$ACTIVE}"
  ts="$(date +%H:%M:%S)"
  proc="$(docker exec "$box" sh -c 'awk "/^VmRSS:/{r=\$2} /^Threads:/{t=\$2} END{print r, t}" /proc/1/status; ls /proc/1/fd 2>/dev/null | wc -l' 2>>"$LOG")" || proc=$'0 0\n0'
  rss="$(printf '%s\n' "$proc" | awk 'NR==1{print $1}')"
  threads="$(printf '%s\n' "$proc" | awk 'NR==1{print $2}')"
  fds="$(printf '%s\n' "$proc" | awk 'NR==2{print $1}')"
  stats="$(k6_container_stats "$box" 2>>"$LOG" || true)"
  be_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$box" '$1==n {print $2}')"
  be_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$box" '$1==n {print $3}')"
  pg_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $2}')"
  pg_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $3}')"
  db="$(policy stats 2>>"$LOG" || echo stats_fail)"
  health="$(k6_health_code 3)"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$ts" "$phase_name" "${rss:-0}" "${threads:-0}" "${fds:-0}" \
    "${be_cpu:-na}" "${be_mem:-na}" "${pg_cpu:-na}" "${pg_mem:-na}" "$health" "$db" >>"$SAMPLE"
  if [[ "$phase_name" == surge-* && "$phase_name" != *-end && "$db" != stats_fail ]]; then
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

remove_surge() {
  if docker ps -a --format '{{.Names}}' | grep -qx "$SURGE"; then
    docker inspect "$SURGE" --format 'oom={{.State.OOMKilled}} status={{.State.Status}} exit={{.State.ExitCode}}' >>"$LOG" 2>&1 || true
    docker stop -t 15 "$SURGE" >>"$LOG" 2>&1 || true
    docker rm "$SURGE" >>"$LOG" 2>&1 || true
  fi
  surge_started=0
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
  local code=$? show before now leftover
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
  remove_surge || code=1
  if [[ "$orig_stopped" == 1 ]]; then
    start_orig || code=1
  fi
  if [[ -f "$ROOT/load/k6/.test-user" ]]; then
    # shellcheck source=/dev/null
    ROOT="$ROOT" source "$ROOT/load/k6/test-user.sh"
    unset K6_DB_CONTAINER
    if k6_db_delete "k6-load@foldex.local" >>"$LOG" 2>&1; then
      rm -f "$ROOT/load/k6/.test-user"
      echo "leftover test user removed" >>"$LOG"
    else
      echo "LEFTOVER_USER" >>"$LOG"
      code=1
    fi
  fi
  leftover="$(policy stats 2>>"$LOG" || true)"
  echo "after_cleanup $leftover" >>"$LOG"
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
mkdir -p "$DIR"
if ! bash "$ROOT/scripts/k6-build-overlay.sh" "$ROOT" "$DIR/foldex" 20000 >>"$LOG" 2>&1; then
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
if docker ps -a --format '{{.Names}}' | grep -qx "$SURGE"; then
  echo "surge container already exists" >>"$LOG"
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
before="$(policy show)" || exit 1
printf '%s\n' "${before#writes=}" > "$DIR/quota_before"
echo "quota_before=$before" >>"$LOG"
policy dbinfo >>"$LOG" 2>&1 || echo "dbinfo_fail" >>"$LOG"
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

phase starting_surge
publish="$(k6_publish_spec)" || exit $?
if ! docker run -d --name "$SURGE" \
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
  echo "SURGE_START_FAIL" >>"$LOG"
  exit 1
fi
surge_started=1
ACTIVE="$SURGE"
surge_ok=0
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40; do
  code="$(k6_health_code 3)"
  if [[ "$code" == 200 ]]; then
    surge_ok=1
    break
  fi
  if ! docker ps --format '{{.Names}}' | grep -qx "$SURGE"; then
    echo "surge exited during boot" >>"$LOG"
    docker inspect "$SURGE" --format 'oom={{.State.OOMKilled}} status={{.State.Status}} exit={{.State.ExitCode}}' >>"$LOG" 2>&1 || true
    exit 1
  fi
  sleep 1
done
if [[ "$surge_ok" != 1 ]]; then
  echo "surge health failed" >>"$LOG"
  exit 1
fi
probe="$(policy stats 2>>"$LOG" || true)"
echo "stats_probe $probe" >>"$LOG"
case "$probe" in
  links=*) ;;
  *)
    echo "STATS_PROBE_FAIL" >>"$LOG"
    exit 1
    ;;
esac
sample_once surge-up "$SURGE"
phase surge_up

start_sampler() {
  local name="$1"
  (
    while true; do
      sample_once "$name" "$SURGE"
      sleep 20
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

write_budget_collapsed() {
  local complete newlinks base last
  complete="$(python3 "$ROOT/scripts/k6_budget_complete.py" "$DIR/surge-write.log")"
  base="$(awk -F'\t' '$2=="baseline" {line=$NF} END{print line}' "$SAMPLE")"
  last="$(awk -F'\t' '$2=="surge-write" {line=$NF} END{print line}' "$SAMPLE")"
  newlinks="$(python3 "$ROOT/scripts/k6_budget_links.py" "$base" "$last")"
  echo "budget_check complete=$complete newlinks=$newlinks" >>"$LOG"
  if [[ "$complete" -ge 800 && "$newlinks" -ge 0 && "$newlinks" -lt 250 ]]; then
    return 0
  fi
  return 1
}

run_flow() {
  local flow="$1" delete="$2" pid waited code
  echo "BEGIN $flow $(date +%H:%M:%S)" >>"$LOG"
  phase "$flow"
  start_sampler "$flow"
  (
    cd "$ROOT" || exit 1
    unset K6_VUS K6_DURATION
    export K6_DB_CONTAINER="$SURGE"
    export K6_NO_COLOR=true
    export K6_TEST_USER=1
    export K6_DELETE_TEST_USER="$delete"
    export K6_PROFILE=smoke
    ./load/k6/run.sh "$flow"
  ) >"$DIR/$flow.log" 2>&1 &
  pid=$!
  FLOW_PID=$pid
  waited=0
  while kill -0 "$pid" 2>/dev/null; do
    sleep 10
    waited=$((waited + 10))
    if [[ -f "$DIR/alert" ]]; then
      echo "alert during $flow" >>"$LOG"
      kill "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      FLOW_PID=
      stop_sampler
      exit 1
    fi
    if [[ "$flow" == surge-write && "$waited" == 90 ]]; then
      if write_budget_collapsed; then
        echo "write budget is not tracking the arrival rate" > "$DIR/alert"
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        FLOW_PID=
        stop_sampler
        exit 1
      fi
    fi
    if [[ "$waited" -ge 900 ]]; then
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
  sample_once "${flow}-end" "$SURGE"
  echo "END $flow exit=$code $(date +%H:%M:%S)" >>"$LOG"
  echo "$code" > "$DIR/$flow.exit"
}

run_flow surge-write 0
run_flow surge-read 0
run_flow surge-mixed 1

sample_once after "$SURGE"
echo "runs finished" >>"$LOG"
phase finished
exit 0
