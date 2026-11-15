#!/usr/bin/env bash
# codeql-config.yml is consumed by the CodeQL runner at analysis time and its
# failure modes are silent: a mistyped key (paths-ignore vs paths_ignore), a
# duplicated mapping key, or a glob that stops matching keeps every job green
# while re-admitting the dismissed Atlas DOM-XSS alerts on the Monday sweep.
# actionlint cannot cover it — it reads only .github/workflows/ and treats the
# init step's `config:` input as an opaque string. This guard proves the file
# keeps its exact shape (stdlib parser, no YAML dependency), excludes exactly
# the generated trees, and stays wired into codeql.yml.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CONFIG="$ROOT/codeql-config.yml"
WORKFLOW="$ROOT/.github/workflows/codeql.yml"

# The guard must be able to fail: feed it the exact defect class it exists for.
self_test() {
  local fixture
  fixture=$(mktemp -d)
  trap 'rm -rf "$fixture"' RETURN
  printf 'name: x\npaths_ignore:\n  - docs/**\npaths_ignore:\n  - other/**\n' > "$fixture/codeql-config.yml"
  if check_shape "$fixture/codeql-config.yml" 2>/dev/null; then
    echo "self-test FAILED: a duplicated mapping key parsed cleanly — this guard can no longer detect its defect" >&2
    return 1
  fi
  echo "ok: self-test — duplicate key is refused"
}

# Shape check: this config is deliberately scalar keys and one sequence of
# globs. Tabs, duplicate keys, nesting, or any other line shape are refused —
# if the config ever needs more structure, this guard must grow with it.
check_shape() {
  python3 - "$1" <<'PY'
import re
import sys

for n, raw in enumerate(open(sys.argv[1]).read().splitlines(), 1):
    if "\t" in raw:
        sys.exit(f"line {n}: tab character — YAML refuses tabs for indentation")
    line = raw.rstrip()
    if not line or line.startswith("#"):
        continue
    if line.startswith("  - "):
        if not getattr(sys, "in_sequence", False):
            sys.exit(f"line {n}: list item outside a sequence key")
        continue
    key, sep, value = line.partition(":")
    if not sep or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_-]*", key):
        sys.exit(f"line {n}: expected 'key:' or '  - item', got {raw!r}")
    sys.in_sequence = not value.strip()
    seen = getattr(sys, "seen_keys", set())
    if key in seen:
        sys.exit(f"line {n}: duplicate mapping key {key!r}")
    seen.add(key)
    sys.seen_keys = seen
PY
}

self_test
check_shape "$CONFIG" || { echo "FAIL: codeql-config.yml shape — see the line above" >&2; exit 1; }
echo "ok: codeql-config.yml keeps a strict shape"

fail=0
expected_globs=('docs/atlas/**' 'docs/audit/**' 'docs/audits/**' 'docs/impl/**')

[ "$(grep -cE '^  - ' "$CONFIG")" -eq "${#expected_globs[@]}" ] || {
  echo "FAIL: paths-ignore has $(grep -cE '^  - ' "$CONFIG") entries, expected ${#expected_globs[@]}" >&2
  fail=1
}
for glob in "${expected_globs[@]}"; do
  grep -qxF "  - $glob" "$CONFIG" || { echo "FAIL: paths_ignore lost '$glob'" >&2; fail=1; }
done
grep -qxE 'paths-ignore:' "$CONFIG" || { echo "FAIL: paths-ignore key missing — CodeQL reads kebab-case only and drops unknown keys silently" >&2; fail=1; }
grep -qxE 'paths_ignore:' "$CONFIG" && { echo "FAIL: snake_case paths_ignore found — the v1 spelling is dropped silently by the runner" >&2; fail=1; }

# The exclusion only works while the init step still loads this file — and
# `config:` (inline YAML) is NOT `config-file:` (path): the wrong input parses
# the path string as the config body.
grep -qE "^[[:space:]]+config-file:[[:space:]]*codeql-config\.yml[[:space:]]*$" "$WORKFLOW" || {
  echo "FAIL: codeql.yml no longer wires config-file: codeql-config.yml — the exclusion is dead config" >&2
  fail=1
}
grep -qE "^[[:space:]]+config:[[:space:]]*codeql-config" "$WORKFLOW" && {
  echo "FAIL: codeql.yml uses config: (inline YAML) where config-file: (path) is required" >&2
  fail=1
}

[ "$fail" -eq 0 ] && echo "ok: codeql-config.yml excludes exactly the generated trees and is wired into codeql.yml"
exit "$fail"
