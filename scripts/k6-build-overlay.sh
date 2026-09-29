#!/usr/bin/env bash
# Build a linux/arm64 backend whose compiled write ceiling is the third
# argument. The module tree is only read: Sanitize turns a stored value above
# the compiled max back into the default 120, so a surge above 6000/min needs
# a throwaway binary. The repository file stays at 6000.
set -euo pipefail

prepare_overlay() {
  local module="$1" out="$2" maximum="$3"
  local src dest dir overlay
  src="$module/backend/internal/abusepolicy/abusepolicy.go"
  dir="$(cd "$(dirname "$out")" && pwd)"
  dest="$dir/abusepolicy.go"
  overlay="$dir/overlay.json"
  python3 - "$src" "$dest" "$overlay" "$maximum" <<'PY'
import json
import pathlib
import sys

src, dest, overlay, maximum = sys.argv[1:]
if not maximum.isdigit() or not (30 <= int(maximum) <= 20000):
    sys.exit("teto compilado fora da janela")
text = pathlib.Path(src).read_text()
needle = "MaxAPIWritesPerMinute = 6000"
if text.count(needle) != 1:
    sys.exit("o teto compilado no fonte não é a atribuição única 6000")
pathlib.Path(dest).write_text(text.replace(needle, f"MaxAPIWritesPerMinute = {maximum}", 1))
pathlib.Path(overlay).write_text(
    json.dumps({"Replace": {str(pathlib.Path(src).resolve()): str(pathlib.Path(dest).resolve())}}) + "\n"
)
PY
  printf '%s\n' "$overlay"
}

if [[ "${1:-}" == "--prepare-only" ]]; then
  shift
  prepare_overlay "$@" >/dev/null
  exit 0
fi

if [[ $# -ne 3 ]]; then
  echo "uso: k6-build-overlay.sh <raiz-do-modulo> <binario> <teto>" >&2
  exit 2
fi

module="$1"
out="$2"
maximum="$3"
src="$module/backend/internal/abusepolicy/abusepolicy.go"
before="$(shasum -a 256 "$src")"
overlay="$(prepare_overlay "$module" "$out" "$maximum")"
dir="$(dirname "$overlay")"
(
  cd "$module/backend"
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -overlay "$overlay" -o "$out" ./cmd/server
)
after="$(shasum -a 256 "$src")"
rm -f "$dir/abusepolicy.go" "$overlay"
if [[ "$before" != "$after" ]]; then
  echo "o fonte da cota mudou durante o build" >&2
  exit 1
fi
