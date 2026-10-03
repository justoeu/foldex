#!/usr/bin/env bash
# The three order ramps stay wired to the rates the suite promises.
# No database and no k6.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
js="$ROOT/load/k6/foldex.js"
run="$ROOT/load/k6/run.sh"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

grep -q "orders-write|orders-read|orders-mixed" "$run" || fail "run.sh does not accept the order flows"
grep -q "const ORDERS_WRITE_PER_MIN = 1500" "$js" || fail "write target drifted"
grep -q "const ORDERS_READ_PER_MIN = 3000" "$js" || fail "read target drifted"
grep -q "const ORDERS_MIXED_WRITE_PER_MIN = 1000" "$js" || fail "mixed write target drifted"
grep -q "const ORDERS_MIXED_READ_PER_MIN = 2000" "$js" || fail "mixed read target drifted"
grep -q "orderArrival('orderWrite', ORDERS_WRITE_PER_MIN" "$js" || fail "write ramp is not bound to 1500"
grep -q "orderArrival('orderRead', ORDERS_READ_PER_MIN" "$js" || fail "read ramp is not bound to 3000"
grep -q "orderArrival('orderWrite', ORDERS_MIXED_WRITE_PER_MIN" "$js" || fail "mixed write ramp is not bound to 1000"
grep -q "orderArrival('orderRead', ORDERS_MIXED_READ_PER_MIN" "$js" || fail "mixed read ramp is not bound to 2000"
grep -q "expectStatus(res, \[201\], 'POST /api/links')" "$js" || fail "order write must require 201"
grep -q "expectStatus(res, \[200\], 'GET /api/links')" "$js" || fail "order read must require 200"

python3 - "$js" <<'PY'
import pathlib, sys
text = pathlib.Path(sys.argv[1]).read_text()
start = text.index("function ordersOptions")
end = text.index("function singleOptions")
body = text[start:end]
if "429" in body:
    sys.exit("orders thresholds must not treat 429 as success")
if "abortOnFail" in body:
    sys.exit("orders thresholds must let the six minutes finish")
PY

echo "ok"
