#!/usr/bin/env bash
# Locks the two branches of `make coverage-check`:
#
#   1. coverage.out missing → exit non-zero with an actionable message.
#   2. coverage.out present, total ≥ COVERAGE_MIN → exit 0.
#   3. coverage.out present, total < COVERAGE_MIN → exit non-zero with
#      a FAIL line that names both numbers.
#
# The recipe is short but easy to break — a future awk rewrite or a
# Makefile cleanup that drops `-f coverage.out` would silently always-pass
# or always-fail without anyone noticing.
#
# Run with: bash backend/scripts/test-coverage-check.sh
# Wired into CI's backend job (see .github/workflows/ci.yml).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKEND_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$BACKEND_DIR"

TMPDIR=$(mktemp -d)
# Re-anchor so we don't trample a real coverage.out at the backend root.
COVER_OUT="$TMPDIR/coverage.out"
trap 'rm -rf "$TMPDIR"; rm -f coverage.out' EXIT

# Capture an existing coverage.out (if a contributor was mid-run) so we
# can restore it at exit. Test pollution avoidance.
ORIG_COVER=""
if [ -f coverage.out ]; then
  ORIG_COVER=$(mktemp)
  cp coverage.out "$ORIG_COVER"
  trap 'rm -rf "$TMPDIR"; mv "$ORIG_COVER" coverage.out 2>/dev/null || rm -f coverage.out' EXIT
fi

# ─── case 1: missing file → fail ──────────────────────────────────────

rm -f coverage.out
if make coverage-check >/dev/null 2>&1; then
  echo "✗ case 1: coverage-check passed without coverage.out — must fail" >&2
  exit 1
fi
out=$(make coverage-check 2>&1 || true)
if ! grep -q "coverage.out missing" <<<"$out"; then
  echo "✗ case 1: expected 'coverage.out missing' in stderr, got:" >&2
  echo "$out" >&2
  exit 1
fi
echo "✓ case 1: missing coverage.out fails with actionable message"

# ─── helper: generate a real coverage.out by exercising a tiny backend
# package. `go tool cover -func` needs to resolve the source files
# referenced inside the profile, so synthesising a profile in a temp
# dir doesn't work — pkg paths land outside the module. Use
# internal/pkg/cssvalid which has fast unit tests + a known small
# surface; the actual percentage doesn't matter, we drive pass/fail by
# moving COVERAGE_MIN above/below whatever total it reports.

generate_real_profile() {
  ( cd "$BACKEND_DIR" && go test -covermode=atomic -coverprofile=coverage.out ./internal/pkg/cssvalid/... >/dev/null 2>&1 )
}

extract_total_pct() {
  ( cd "$BACKEND_DIR" && go tool cover -func=coverage.out | tail -1 | awk '{print $3}' | tr -d '%' )
}

generate_real_profile
REAL_PCT=$(extract_total_pct)
if [ -z "$REAL_PCT" ]; then
  echo "✗ could not extract a coverage percentage from real profile" >&2
  exit 1
fi
LOW_MIN=1     # well below any non-zero coverage
HIGH_MIN=$(awk -v p="$REAL_PCT" 'BEGIN{print int(p)+5}')  # well above

# ─── case 2: above-threshold → pass ───────────────────────────────────

if ! out=$(COVERAGE_MIN=$LOW_MIN make coverage-check 2>&1); then
  echo "✗ case 2: real coverage ($REAL_PCT%) failed against COVERAGE_MIN=$LOW_MIN" >&2
  echo "$out" >&2
  exit 1
fi
if ! grep -q "Total coverage:" <<<"$out"; then
  echo "✗ case 2: expected 'Total coverage:' in stdout, got:" >&2
  echo "$out" >&2
  exit 1
fi
echo "✓ case 2: above-threshold passes and reports the total ($REAL_PCT% ≥ $LOW_MIN%)"

# ─── case 3: below-threshold → fail with named numbers ────────────────

if COVERAGE_MIN=$HIGH_MIN make coverage-check >/dev/null 2>&1; then
  echo "✗ case 3: real coverage ($REAL_PCT%) passed against COVERAGE_MIN=$HIGH_MIN — must fail" >&2
  exit 1
fi
out=$(COVERAGE_MIN=$HIGH_MIN make coverage-check 2>&1 || true)
if ! grep -qE "FAIL: coverage.*< $HIGH_MIN" <<<"$out"; then
  echo "✗ case 3: expected 'FAIL: coverage … < $HIGH_MIN' line, got:" >&2
  echo "$out" >&2
  exit 1
fi
echo "✓ case 3: below-threshold fails with both numbers named ($REAL_PCT% < $HIGH_MIN%)"

echo
echo "make coverage-check — all branches asserted."
