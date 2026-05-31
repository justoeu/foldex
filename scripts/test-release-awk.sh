#!/usr/bin/env bash
# Locks the awk-based "first-match-only" version rewrite in release.sh.
#
# What this proves:
#   1. The first occurrence of `"version": "x.y.z"` is rewritten.
#   2. A SECOND `"version": "x.y.z"` (e.g. inside a dependencies block
#      where another dep happens to be at a real semver) is LEFT ALONE.
#      The original GNU `0,/regex/` form did this; the BSD-portable awk
#      replacement we shipped in PR #10 needs to keep this invariant or
#      release-X would corrupt lockfile-shaped files.
#   3. JSON shape stays valid — no extra whitespace, no broken quoting.
#
# Run with: bash scripts/test-release-awk.sh
# Used by CI's frontend job (see .github/workflows/ci.yml).

set -euo pipefail

# Locate release.sh next to us; both live under scripts/.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE="$SCRIPT_DIR/release.sh"

if [ ! -f "$RELEASE" ]; then
  echo "✗ release.sh not found at $RELEASE" >&2
  exit 1
fi

# Extract update_version() out of release.sh and into a tiny harness.
# Sourcing release.sh directly would trigger its dirty-tree check, so we
# slice the function only. Anchored on `update_version() {` / closing `}`.
HARNESS=$(mktemp)
trap 'rm -f "$HARNESS"' EXIT

awk '
  /^update_version\(\) \{/ { capturing = 1 }
  capturing { print }
  capturing && /^\}$/ { capturing = 0 }
' "$RELEASE" >"$HARNESS"

if ! grep -q "^update_version()" "$HARNESS"; then
  echo "✗ could not extract update_version() from release.sh" >&2
  exit 1
fi

# ─── case 1: first-match-only on a fixture with TWO "version" strings ──

FIXTURE=$(mktemp)
trap 'rm -f "$HARNESS" "$FIXTURE"' EXIT

cat >"$FIXTURE" <<'JSON'
{
  "name": "foldex-web",
  "private": true,
  "version": "1.1.1",
  "dependencies": {
    "react": "^19.2.5",
    "some-pkg": "1.2.3",
    "buggy-tool": {
      "version": "2.3.4",
      "registry": "https://npm.example/"
    }
  }
}
JSON

# Run the extracted function against the fixture.
(
  # shellcheck disable=SC1090
  source "$HARNESS"
  NEW="9.9.9"
  update_version "$FIXTURE"
)

# First "version" must be 9.9.9; the nested one inside buggy-tool MUST
# stay at 2.3.4. If both flipped, the awk lost its `done` flag.
TOP=$(grep -n '"version"' "$FIXTURE" | sed -n '1p')
NESTED=$(grep -n '"version"' "$FIXTURE" | sed -n '2p')

case "$TOP" in
  *'"9.9.9"'*) : ;;
  *) echo "✗ top-level version not bumped: $TOP" >&2; exit 1 ;;
esac

case "$NESTED" in
  *'"2.3.4"'*) : ;;
  *) echo "✗ nested version was rewritten (must stay 2.3.4): $NESTED" >&2; exit 1 ;;
esac

echo "✓ case 1: only the first \"version\" was rewritten"

# ─── case 2: JSON is still valid after the rewrite ─────────────────────

if command -v jq >/dev/null 2>&1; then
  if ! jq -e '.version == "9.9.9"' "$FIXTURE" >/dev/null; then
    echo "✗ JSON shape broke or top-level version mismatch" >&2
    exit 1
  fi
  if ! jq -e '.dependencies."buggy-tool".version == "2.3.4"' "$FIXTURE" >/dev/null; then
    echo "✗ JSON shape broke or nested version mismatch" >&2
    exit 1
  fi
  echo "✓ case 2: JSON shape intact (verified via jq)"
else
  echo "… case 2: jq not available, skipping JSON-shape assertion"
fi

# ─── case 3: idempotent — running twice on the same NEW is a no-op ─────

BEFORE=$(cat "$FIXTURE")
(
  # shellcheck disable=SC1090
  source "$HARNESS"
  NEW="9.9.9"
  update_version "$FIXTURE"
)
AFTER=$(cat "$FIXTURE")

if [ "$BEFORE" != "$AFTER" ]; then
  echo "✗ second invocation with the same NEW changed the file" >&2
  diff <(echo "$BEFORE") <(echo "$AFTER") >&2 || true
  exit 1
fi
echo "✓ case 3: re-running with the same target version is a no-op"

echo
echo "release.sh awk update_version() — all assertions passed."
