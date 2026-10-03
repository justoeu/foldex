#!/bin/bash
# Full local k6 pass against the stock backend. The EXIT trap restores the
# write quota and removes the test user. Stdout is phase lines only.
# Logs and the database URL stay in FOLDEX_K6_RUN_DIR (default /tmp).
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=k6-lib.sh
source "$ROOT/scripts/k6-lib.sh"
k6_load_operator_env "$ROOT" || {
  echo "load/k6/env.local tem uma linha inválida" >&2
  exit 2
}
export K6_BASE_URL="${K6_BASE_URL:-http://127.0.0.1:9089}"
DIR="${FOLDEX_K6_RUN_DIR:-/tmp/foldex-appcheck}"
LOG="$DIR/runner.log"
SAMPLE="$DIR/samples.tsv"
POLICY="$ROOT/scripts/k6_quota.py"
BOX="${FOLDEX_K6_BACKEND:-foldex-backend-1}"
PGBOX="${FOLDEX_K6_POSTGRES:-}"
FLOW_PID=
SAMPLER=
restored=0
raised=0

mkdir -p "$DIR/flows"
chmod 700 "$DIR"
echo $$ > "$DIR/runner.pid"
: > "$LOG"
rm -f "$DIR/alert" "$DIR/exit_code" "$DIR/quota_raised"
printf 'ts\tphase\trss_kb\tthreads\tfds\tbackend_cpu\tbackend_mem\tpostgres_cpu\tpostgres_mem\thealth\tdb\n' > "$SAMPLE"

phase() {
  printf '%s\n' "$1" > "$DIR/phase"
  echo "PHASE $1 $(date +%H:%M:%S)"
  echo "PHASE $1 $(date +%H:%M:%S)" >>"$LOG"
}

policy() {
  local url
  url="$(cat "$DIR/db_url")"
  FOLDEX_K6_DB_URL="$url" FOLDEX_ORDERS_TARGET="${FOLDEX_ORDERS_TARGET:-6000}" \
    FOLDEX_ORDERS_ORIG="$DIR/abuse_policy.orig" \
    python3 "$POLICY" "$@"
}

sample_once() {
  local name="$1" ts proc rss threads fds stats be_cpu be_mem pg_cpu pg_mem db health oom
  ts="$(date +%H:%M:%S)"
  proc="$(docker exec "$BOX" sh -c 'awk "/^VmRSS:/{r=\$2} /^Threads:/{t=\$2} END{print r, t}" /proc/1/status; ls /proc/1/fd 2>/dev/null | wc -l' 2>>"$LOG")" || proc=$'0 0\n0'
  rss="$(printf '%s\n' "$proc" | awk 'NR==1{print $1}')"
  threads="$(printf '%s\n' "$proc" | awk 'NR==1{print $2}')"
  fds="$(printf '%s\n' "$proc" | awk 'NR==2{print $1}')"
  stats="$(k6_container_stats "$BOX" 2>>"$LOG" || true)"
  be_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$BOX" '$1==n {print $2}')"
  be_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$BOX" '$1==n {print $3}')"
  pg_cpu="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $2}')"
  pg_mem="$(printf '%s\n' "$stats" | awk -F'|' -v n="$PGBOX" '$1==n {print $3}')"
  db="$(policy stats 2>>"$LOG" || echo stats_fail)"
  health="$(k6_health_code 3)"
  oom="$(docker inspect "$BOX" --format '{{.State.OOMKilled}}' 2>>"$LOG" || echo unknown)"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
    "$ts" "$name" "${rss:-0}" "${threads:-0}" "${fds:-0}" \
    "${be_cpu:-na}" "${be_mem:-na}" "${pg_cpu:-na}" "${pg_mem:-na}" "$health" "$db" >>"$SAMPLE"
  if [[ "$oom" == "true" ]]; then
    echo "oom" > "$DIR/alert"
  elif [[ "${rss:-0}" -gt 480000 ]]; then
    echo "rss ${rss}" > "$DIR/alert"
  elif [[ "$health" != "200" ]]; then
    local misses=0
    if [[ -f "$DIR/health_miss" ]]; then
      misses="$(cat "$DIR/health_miss")"
    fi
    misses=$((misses + 1))
    printf '%s\n' "$misses" > "$DIR/health_miss"
    if [[ "$misses" -ge 3 ]]; then
      echo "health ${health}" > "$DIR/alert"
    fi
  else
    rm -f "$DIR/health_miss"
  fi
  if [[ -f "$DIR/quota_raised" ]]; then
    local live
    live="$(policy show 2>>"$LOG" || true)"
    if [[ -n "$live" && "$live" != "writes=6000" ]]; then
      echo "quota drift ${live}" > "$DIR/alert"
    fi
  fi
  if [[ -f "$DIR/alert" ]]; then
    local fpid=""
    fpid="$(cat "$DIR/flow.pid" 2>/dev/null || true)"
    if [[ -n "$fpid" ]]; then
      kill -- -"$fpid" 2>/dev/null || kill "$fpid" 2>/dev/null || true
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

remove_user() {
  if [[ ! -f "$ROOT/load/k6/.test-user" ]]; then
    return 0
  fi
  export K6_TEST_USER=1
  # shellcheck source=/dev/null
  source "$ROOT/load/k6/test-user.sh"
  if k6_db_delete "k6-load@foldex.local" >>"$LOG" 2>&1; then
    rm -f "$ROOT/load/k6/.test-user"
    echo "test user removed" >>"$LOG"
    return 0
  fi
  echo "LEFTOVER_USER" >>"$LOG"
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
    kill -- -"$FLOW_PID" 2>/dev/null || kill "$FLOW_PID" 2>/dev/null || true
    wait "$FLOW_PID" 2>/dev/null || true
    FLOW_PID=
  fi
  restore_quota || code=1
  remove_user || code=1
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
  code_health="$(k6_health_code 5)"
  echo "final_health $code_health" >>"$LOG"
  if [[ "$code_health" != "200" ]]; then
    code=1
  fi
  rm -f "$DIR/db_url"
  echo "$code" > "$DIR/exit_code"
  echo "EXIT:$code" >>"$LOG"
  echo "EXIT:$code"
  exit "$code"
}
trap on_exit EXIT

phase preflight
k6_require_health 5 || {
  echo "health down" >>"$LOG"
  exit 1
}
if pgrep -f 'k6 run' >/dev/null 2>&1; then
  echo "k6 already running" >>"$LOG"
  exit 1
fi
umask 077
docker exec "$BOX" sh -c 'printf %s "$DB_URL"' > "$DIR/db_url"
chmod 600 "$DIR/db_url"
if [[ ! -s "$DIR/db_url" ]]; then
  echo "DB_URL vazio" >>"$LOG"
  exit 1
fi
before="$(policy show)" || exit 1
printf '%s\n' "${before#writes=}" > "$DIR/quota_before"
echo "quota_before=$before" >>"$LOG"
sample_once baseline
if [[ -f "$DIR/alert" ]]; then
  exit 1
fi

start_sampler() {
  if [[ -n "${SAMPLER}" ]]; then
    return 0
  fi
  (
    while true; do
      name="$(cat "$DIR/phase" 2>/dev/null || echo unknown)"
      sample_once "$name"
      sleep 20
    done
  ) &
  SAMPLER=$!
}

run_one() {
  local flow="$1" profile="$2" code
  if [[ -f "$DIR/alert" ]]; then
    echo "stop before $flow: $(cat "$DIR/alert")" >>"$LOG"
    return 1
  fi
  phase "$flow"
  export K6_TEST_USER=1
  export K6_DELETE_TEST_USER=0
  export K6_PROFILE="$profile"
  unset K6_VUS K6_DURATION K6_FLOW FOLDEX_K6_RUN
  set -m
  bash "$ROOT/load/k6/run.sh" "$flow" >"$DIR/flows/${flow}.log" 2>&1 &
  FLOW_PID=$!
  echo "$FLOW_PID" > "$DIR/flow.pid"
  local wcode=0
  wait "$FLOW_PID" || wcode=$?
  code=$wcode
  FLOW_PID=
  rm -f "$DIR/flow.pid"
  echo "$code" > "$DIR/flows/${flow}.exit"
  echo "END $flow exit=$code $(date +%H:%M:%S)" >>"$LOG"
  echo "END $flow exit=$code"
  if [[ -f "$DIR/alert" ]]; then
    return 1
  fi
  if [[ "$code" != 0 && "$code" != 99 ]]; then
    echo "flow $flow failed exit=$code" > "$DIR/alert"
    return 1
  fi
  return 0
}

start_sampler

functional=(
  smoke:smoke
  redirect:smoke
  session:read
  library-read:read
  stats:read
  activity:read
  settings:read
  admin:read
  mixed:read
  library-write:write
  export:smoke
  capacity:smoke
)
for item in "${functional[@]}"; do
  run_one "${item%%:*}" "${item##*:}" || exit 1
done

phase raising_quota
FOLDEX_ORDERS_TARGET=6000 policy raise >>"$LOG" 2>&1 || exit 1
raised_line="$(FOLDEX_ORDERS_TARGET=6000 policy show)" || exit 1
echo "quota_raised=$raised_line" >>"$LOG"
if [[ "$raised_line" != "writes=6000" ]]; then
  echo "RAISE_NOT_VISIBLE" >>"$LOG"
  exit 1
fi
raised=1
echo 1 > "$DIR/quota_raised"
# The running process keeps the previous document until the cache TTL.
sleep 35

ramps=(
  orders-write:smoke
  orders-read:smoke
  orders-mixed:smoke
  surge-write:smoke
  surge-read:smoke
  surge-mixed:smoke
)
for item in "${ramps[@]}"; do
  run_one "${item%%:*}" "${item##*:}" || exit 1
done

phase finished
exit 0
