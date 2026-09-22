#!/usr/bin/env bash
# The extension zip is a BUILD INPUT of the backend binary (go:embed), and it
# must be rebuildable to the exact same bytes.
#
# Determinism is the whole contract: `backend/internal/addon/dist/extension.zip`
# is committed, and if rebuilding it from the same tree produced different
# bytes, every `make extension` would leave the tree dirty and no diff would
# ever say whether the committed zip is current or silently stale — a stale
# embed is invisible in every check that does not rebuild it.
#
# Check 1: two consecutive `make extension` runs produce identical bytes.
# Check 2: the artifact contract — version.txt mirrors manifest.json, the zip
#          opens, manifest.json sits at its root, and no test file or
#          node_modules entry ever ships inside the extension the admin
#          downloads.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST="$ROOT/backend/internal/addon/dist"
FOLDEX_ADDON_DIR="${FOLDEX_ADDON_DIR:-$ROOT/../foldex-addon}"
if [ ! -f "$FOLDEX_ADDON_DIR/manifest.json" ]; then
  echo "✗ addon project not found at $FOLDEX_ADDON_DIR — clone foldex-addon next to this repo" >&2
  exit 1
fi
fail=0
note() { echo "FAIL $*" >&2; fail=1; }

digest() { shasum -a 256 "$1" | cut -d' ' -f1; }

if ! make -C "$ROOT" --no-print-directory extension; then
  note "make extension did not run"
  exit 1
fi
first=$(digest "$DIST/extension.zip")
make -C "$ROOT" --no-print-directory extension
second=$(digest "$DIST/extension.zip")

if [[ "$first" != "$second" ]]; then
  note "make extension is not deterministic: $first != $second"
fi

manifest_version=$(python3 -c "import json;print(json.load(open('$FOLDEX_ADDON_DIR/manifest.json'))['version'])" 2>/dev/null) \
  || note "addon manifest.json does not parse"
version_txt=$(tr -d '[:space:]' < "$DIST/version.txt" 2>/dev/null) || true
if [[ -z "$manifest_version" || "$manifest_version" != "$version_txt" ]]; then
  note "version.txt ($version_txt) does not mirror manifest.json ($manifest_version)"
fi

if ! python3 - "$DIST/extension.zip" <<'PY'
import sys, zipfile

zf = zipfile.ZipFile(sys.argv[1])
bad = zf.testzip()
if bad is not None:
    sys.stderr.write(f"FAIL corrupt zip entry: {bad}\n")
    sys.exit(1)
names = zf.namelist()
if "manifest.json" not in names:
    sys.stderr.write("FAIL manifest.json is not at the zip root\n")
    sys.exit(1)
leaked = [n for n in names
          if "node_modules" in n or n.endswith((".test.js", ".test.mjs", ".test.cjs"))]
if leaked:
    sys.stderr.write(f"FAIL test/dev entries shipped in the zip: {leaked}\n")
    sys.exit(1)
PY
then
  fail=1
fi

if [[ $fail -ne 0 ]]; then
  echo "extension zip guard: FAIL" >&2
  exit 1
fi
echo "extension zip guard: ok"
