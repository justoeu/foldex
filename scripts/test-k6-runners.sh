#!/usr/bin/env bash
# The local k6 runners live in scripts/ and keep secrets out of the tree.
# No database, no k6, no docker, and no go build.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

for script in \
  k6-appcheck.sh k6-surge-run.sh k6-gate-run.sh k6-watch.sh k6-build-overlay.sh k6-lib.sh \
  k6_quota.py k6_metrics.py k6_gate.js k6_budget_complete.py k6_budget_links.py
do
  [[ -f "$ROOT/scripts/$script" ]] || fail "missing scripts/$script"
done

bash -n "$ROOT/scripts/k6-appcheck.sh"
bash -n "$ROOT/scripts/k6-surge-run.sh"
bash -n "$ROOT/scripts/k6-gate-run.sh"
bash -n "$ROOT/scripts/k6-watch.sh"
bash -n "$ROOT/scripts/k6-build-overlay.sh"
bash -n "$ROOT/scripts/k6-lib.sh"
bash -n "$ROOT/load/k6/run.sh"
python3 -m py_compile \
  "$ROOT/scripts/k6_quota.py" \
  "$ROOT/scripts/k6_metrics.py" \
  "$ROOT/scripts/k6_budget_complete.py" \
  "$ROOT/scripts/k6_budget_links.py"

if grep -n '/Users/' "$ROOT/scripts"/k6* >/dev/null; then
  fail "a runner hardcodes a home directory"
fi
if grep -n 'MaxAPIWritesPerMinute = 20000' "$ROOT/scripts"/k6* >/dev/null; then
  fail "a runner bakes the throwaway ceiling into source text"
fi

[[ -f "$ROOT/load/k6/env.example" ]] || fail "missing load/k6/env.example"
git -C "$ROOT" check-ignore -q -- load/k6/env.local || fail "env.local is not ignored"

ip_a=192.168
ip_b=68
pg_a=genfin
pg_b=postgres
machine_address="${ip_a}.${ip_b}|${pg_a}-${pg_b}"
for surface in \
  "$ROOT/scripts"/k6* \
  "$ROOT/load/k6/run.sh" \
  "$ROOT/load/k6/README.md" \
  "$ROOT/load/k6/env.example" \
  "$ROOT/scripts/test-k6-runners.sh" \
  "$ROOT/scripts/test-k6-test-user.sh"
do
  if grep -nE "$machine_address" $surface >/dev/null; then
    grep -nE "$machine_address" $surface >&2
    fail "machine address in $surface"
  fi
done
grep -q 'MaxAPIWritesPerMinute = 6000' "$ROOT/backend/internal/abusepolicy/abusepolicy.go" \
  || fail "repository write ceiling drifted"

python3 - "$ROOT/scripts/k6_quota.py" <<'PY'
import importlib.util
import os
import sys

path = sys.argv[1]
spec = importlib.util.spec_from_file_location("k6_quota", path)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

def rejects(host):
    try:
        mod.dial_plan(host)
    except SystemExit:
        return
    sys.exit(f"dial_plan accepted {host}")

sys.stderr = open(os.devnull, "w")
for host in ("127.0.0.1", "localhost", "::1", "db", "foldex-db", "host.docker.internal"):
    try:
        mod.dial_plan(host)
    except SystemExit:
        sys.exit(f"dial_plan refused {host}")
rejects("203.0.113.10")
rejects("10.1.1.1")
os.environ["FOLDEX_ORDERS_TARGET"] = "20001"
try:
    mod.target_writes()
except SystemExit:
    pass
else:
    sys.exit("target 20001 was accepted")
os.environ["FOLDEX_ORDERS_TARGET"] = "6000"
if mod.target_writes() != 6000:
    sys.exit("target 6000 was refused")
for raw in ("30", "20000"):
    os.environ["FOLDEX_ORDERS_TARGET"] = raw
    if mod.target_writes() != int(raw):
        sys.exit(f"target {raw} was refused")
for raw in ("29", "abc"):
    os.environ["FOLDEX_ORDERS_TARGET"] = raw
    try:
        mod.target_writes()
    except SystemExit:
        pass
    else:
        sys.exit(f"target {raw} was accepted")
PY

python3 - "$ROOT/scripts/k6_metrics.py" <<'PY'
import importlib.util
import os
import sys

path = sys.argv[1]
spec = importlib.util.spec_from_file_location("k6_metrics", path)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)
os.environ.pop("FOLDEX_K6_METRICS_URL", None)
os.environ["K6_BASE_URL"] = "http://127.0.0.1:9095/"
if mod.metrics_url() != "http://127.0.0.1:9095/metrics":
    sys.exit("metrics url ignored K6_BASE_URL")
os.environ["FOLDEX_K6_METRICS_URL"] = "http://127.0.0.1:9096/metrics"
if mod.metrics_url() != "http://127.0.0.1:9096/metrics":
    sys.exit("metrics url ignored FOLDEX_K6_METRICS_URL")
PY

# shellcheck source=k6-lib.sh
source "$ROOT/scripts/k6-lib.sh"
envdir="$(mktemp -d)"
mkdir -p "$envdir/load/k6"
(
  unset K6_OPERATOR_ENV_FILE
  k6_load_operator_env "$envdir"
)
printf '%s\n' 'K6_FROM_FILE=from-file' '# comment' '' 'export K6_ALREADY=from-file' \
  'K6_QUOTED="http://127.0.0.1:9090/api/v1/write"' > "$envdir/load/k6/env.local"
(
  unset K6_FROM_FILE K6_QUOTED
  export K6_ALREADY=preset
  export K6_OPERATOR_ENV_FILE="$envdir/load/k6/env.local"
  k6_load_operator_env "$envdir"
  [[ "$K6_FROM_FILE" == "from-file" ]] || exit 1
  [[ "$K6_ALREADY" == "preset" ]] || exit 1
  [[ "$K6_QUOTED" == "http://127.0.0.1:9090/api/v1/write" ]] || exit 1
) || fail "operator env did not fill only unset names"
printf '%s\n' 'not an assignment' > "$envdir/load/k6/env.local"
if (
  export K6_OPERATOR_ENV_FILE="$envdir/load/k6/env.local"
  k6_load_operator_env "$envdir"
); then
  fail "operator env accepted a bad line"
fi
rm -rf "$envdir"
if [[ -f "$ROOT/load/k6/env.local" ]]; then
  ( unset K6_OPERATOR_ENV_FILE; k6_load_operator_env "$ROOT" ) >/dev/null
fi

(
  unset K6_GRAFANA K6_PROMETHEUS_RW_SERVER_URL K6_PROMETHEUS_RW_TREND_STATS K6_PROMETHEUS_RW_PUSH_INTERVAL
  k6_prepare_remote_write
  [[ ${#K6_OUT[@]} -eq 0 ]]
) || fail "remote write turned on with no receiver"
(
  unset K6_GRAFANA K6_PROMETHEUS_RW_TREND_STATS K6_PROMETHEUS_RW_PUSH_INTERVAL
  export K6_PROMETHEUS_RW_SERVER_URL=http://127.0.0.1:9090/api/v1/write
  k6_prepare_remote_write
  [[ ${#K6_OUT[@]} -eq 2 ]]
  [[ "${K6_OUT[0]}" == "-o" && "${K6_OUT[1]}" == "experimental-prometheus-rw" ]]
) || fail "remote write stayed off when a receiver was set"
(
  export K6_GRAFANA=1
  unset K6_PROMETHEUS_RW_SERVER_URL
  if k6_prepare_remote_write 2>/dev/null; then
    exit 1
  fi
) || fail "K6_GRAFANA=1 without a receiver was accepted"
(
  export K6_GRAFANA=0
  export K6_PROMETHEUS_RW_SERVER_URL=http://127.0.0.1:9090/api/v1/write
  k6_prepare_remote_write
  [[ ${#K6_OUT[@]} -eq 0 ]]
) || fail "K6_GRAFANA=0 still enabled remote write"
(
  unset K6_BASE_URL
  [[ "$(k6_origin)" == "http://127.0.0.1:9089" ]]
  export K6_BASE_URL="http://127.0.0.1:9095/"
  [[ "$(k6_origin)" == "http://127.0.0.1:9095" ]]
  [[ "$(k6_publish_spec)" == "127.0.0.1:9095:9089" ]]
  export K6_BASE_URL="http://203.0.113.10:9089"
  if k6_publish_spec >/dev/null 2>&1; then
    exit 1
  fi
) || fail "origin or publish spec drifted"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/module/backend/internal/abusepolicy" "$work/module/backend/cmd/server" "$work/out"
printf '%s\n' 'package abusepolicy' 'const MaxAPIWritesPerMinute = 6000' \
  > "$work/module/backend/internal/abusepolicy/abusepolicy.go"
before="$(shasum -a 256 "$work/module/backend/internal/abusepolicy/abusepolicy.go")"
bash "$ROOT/scripts/k6-build-overlay.sh" --prepare-only "$work/module" "$work/out/foldex" 20000
after="$(shasum -a 256 "$work/module/backend/internal/abusepolicy/abusepolicy.go")"
[[ "$before" == "$after" ]] || fail "prepare rewrote the module source"
grep -q 'MaxAPIWritesPerMinute = 20000' "$work/out/abusepolicy.go" || fail "overlay copy kept 6000"
grep -q 'MaxAPIWritesPerMinute = 6000' "$work/module/backend/internal/abusepolicy/abusepolicy.go" \
  || fail "module source lost 6000"
python3 - "$work/out/overlay.json" "$work/module/backend/internal/abusepolicy/abusepolicy.go" <<'PY'
import json, pathlib, sys
overlay, src = sys.argv[1:]
replace = json.loads(pathlib.Path(overlay).read_text())["Replace"]
key = str(pathlib.Path(src).resolve())
if key not in replace:
    sys.exit("overlay does not replace the module file")
if pathlib.Path(replace[key]).resolve() == pathlib.Path(src).resolve():
    sys.exit("overlay replace target is the module file itself")
PY

# Every flow the suite runner accepts is in the full local pass.
line="$(grep -E '^[[:space:]]*smoke\|session\|' "$ROOT/load/k6/run.sh")"
line="${line%%)*}"
line="$(printf '%s' "$line" | tr -d '[:space:]')"
IFS='|' read -r -a expected <<< "$line"
for flow in "${expected[@]}"; do
  grep -q "$flow" "$ROOT/scripts/k6-appcheck.sh" || fail "appcheck omits $flow"
done
grep -q 'FOLDEX_K6_RUN_DIR:-/tmp/foldex-appcheck' "$ROOT/scripts/k6-appcheck.sh" \
  || fail "appcheck stores its run inside the repository"

echo "ok"
