#!/bin/bash
# Silent until a k6 runner directory records an alert or an exit code.
# Usage: scripts/k6-watch.sh /tmp/foldex-appcheck
DIR="${1:-}"
if [[ -z "$DIR" ]]; then
  echo "FAILED: runner directory missing"
  exit 1
fi
for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15; do
  [[ -s "$DIR/runner.pid" ]] && break
  sleep 1
done
pid="$(cat "$DIR/runner.pid" 2>/dev/null || true)"
if [[ -z "$pid" ]]; then
  echo "FAILED: runner pid missing"
  exit 1
fi
while true; do
  if [[ -f "$DIR/alert" || -f "$DIR/exit_code" ]]; then
    for _ in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
      [[ -f "$DIR/exit_code" ]] && break
      sleep 2
    done
    code="$(cat "$DIR/exit_code" 2>/dev/null || echo pending)"
    if [[ -f "$DIR/alert" ]]; then
      echo "FAILED: $(head -1 "$DIR/alert") exit=$code"
      exit 1
    fi
    if [[ "$code" == "0" ]]; then
      echo "DONE: exit 0"
      exit 0
    fi
    echo "FAILED: exit $code"
    exit 1
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    sleep 2
    if [[ -f "$DIR/exit_code" ]]; then
      continue
    fi
    echo "FAILED: runner died without an exit code"
    exit 1
  fi
  sleep 30
done
