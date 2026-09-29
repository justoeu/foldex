#!/usr/bin/env bash
# The surge ramps stay wired to 10000 / 30000 / 8000+20000.
# The earlier order ramps stay where they were. No database and no k6.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
js="$ROOT/load/k6/foldex.js"
run="$ROOT/load/k6/run.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

grep -q "surge-write|surge-read|surge-mixed" "$run" || fail "run.sh does not accept the surge flows"
grep -q "orders-write|orders-read|orders-mixed" "$run" || fail "order flows dropped out of run.sh"
grep -q "const SURGE_WRITE_PER_MIN = 10000" "$js" || fail "surge write target drifted"
grep -q "const SURGE_READ_PER_MIN = 30000" "$js" || fail "surge read target drifted"
grep -q "const SURGE_MIXED_WRITE_PER_MIN = 8000" "$js" || fail "surge mixed write target drifted"
grep -q "const SURGE_MIXED_READ_PER_MIN = 20000" "$js" || fail "surge mixed read target drifted"
grep -q "const ORDERS_WRITE_PER_MIN = 1500" "$js" || fail "order write target drifted"
grep -q "const ORDERS_READ_PER_MIN = 3000" "$js" || fail "order read target drifted"
grep -q "ordersOptions('surge-write', orderArrival('orderWrite', SURGE_WRITE_PER_MIN, 200, 2000, 'write'))" "$js" || fail "surge write ramp is not bound"
grep -q "ordersOptions('surge-read', orderArrival('orderRead', SURGE_READ_PER_MIN, 500, 4000, 'read'))" "$js" || fail "surge read ramp is not bound"
grep -q "orderArrival('orderWrite', SURGE_MIXED_WRITE_PER_MIN, 150, 1500, 'write')" "$js" || fail "surge mixed write ramp is not bound"
grep -q "orderArrival('orderRead', SURGE_MIXED_READ_PER_MIN, 400, 3000, 'read')" "$js" || fail "surge mixed read ramp is not bound"

python3 - "$js" <<'PY'
import pathlib, sys
text = pathlib.Path(sys.argv[1]).read_text()
start = text.index("function ordersOptions")
end = text.index("function singleOptions")
body = text[start:end]
if "429" in body:
    sys.exit("orders thresholds must not treat a quota refusal as success")
if "abortOnFail" in body:
    sys.exit("orders thresholds must let the minutes finish")
PY

echo "ok"
