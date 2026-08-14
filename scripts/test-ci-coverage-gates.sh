#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
WORKFLOW="$ROOT/.github/workflows/ci.yml"
BACKEND_MAKEFILE="$ROOT/backend/Makefile"
WEB_PACKAGE="$ROOT/web/package.json"
VITEST_CONFIG="$ROOT/web/vitest.config.ts"

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

job_block() {
  local job=$1
  awk -v job="$job" '
    $0 == "  " job ":" { in_job = 1 }
    in_job && /^  [^ ]/ && $0 != "  " job ":" { exit }
    in_job { print }
  ' "$WORKFLOW"
}

step_block() {
  local step=$1
  awk -v step="$step" '
    $0 == "      - name: " step { in_step = 1 }
    in_step && /^      - / && $0 != "      - name: " step { exit }
    in_step { print }
  ' "$WORKFLOW"
}

backend_job=$(job_block backend)
frontend_job=$(job_block frontend)
[[ -n "$backend_job" ]] || fail "backend CI job is missing"
[[ -n "$frontend_job" ]] || fail "frontend CI job is missing"

coverage_run_recipe=$(awk '
  /^coverage-run:/ { in_target = 1; next }
  in_target && /^[^[:space:]#][^:]*:/ { exit }
  in_target { print }
' "$BACKEND_MAKEFILE")
grep -Eq 'go test .*\./\.\.\.$' <<<"$coverage_run_recipe" ||
  fail "backend coverage-run must test ./... so cmd packages cannot be skipped"
if grep -Fq './internal/...' <<<"$coverage_run_recipe"; then
  fail "backend coverage-run must not limit test execution to internal packages"
fi
grep -Fq 'go list ./internal/...' "$BACKEND_MAKEFILE" ||
  fail "backend coverpkg must remain limited to internal production packages"
for excluded in 'testdb$$' '/db$$' 'authctxtest$$'; do
  grep -Fq "$excluded" "$BACKEND_MAKEFILE" ||
    fail "backend coverpkg lost helper/boot exclusion: $excluded"
done

backend_run=$(step_block "unit + integration tests (with coverage)")
backend_gate=$(step_block "coverage gate (85% minimum)")
[[ -n "$backend_run" ]] || fail "backend coverage test step is missing"
[[ -n "$backend_gate" ]] || fail "backend blocking coverage gate is missing"
grep -Eq '^[[:space:]]+run: make coverage-run$' <<<"$backend_run" ||
  fail "backend must generate coverage in its only test-suite run"
grep -Eq '^[[:space:]]+run: make coverage-check$' <<<"$backend_gate" ||
  fail "backend must enforce the generated coverage profile"
if grep -Eq '^[[:space:]]+continue-on-error:[[:space:]]*true$' <<<"$backend_gate"; then
  fail "backend coverage gate must block CI"
fi
[[ $(grep -Ec '^[[:space:]]+run: make coverage-run$' <<<"$backend_job") -eq 1 ]] ||
  fail "backend must run its coverage-instrumented test suite exactly once"
grep -Eq '^COVERAGE_MIN[[:space:]]*\?=[[:space:]]*85$' "$BACKEND_MAKEFILE" ||
  fail "backend coverage minimum must remain 85%"

chrome_gate=$(step_block "require live Chrome")
[[ -n "$chrome_gate" ]] || fail "backend CI must require a live Chrome executable"
grep -Fq 'command -v google-chrome' <<<"$chrome_gate" ||
  fail "backend CI must resolve GitHub Ubuntu's google-chrome executable"
# shellcheck disable=SC2016
grep -Eq 'test -x "?\$chrome_path"?' <<<"$chrome_gate" ||
  fail "backend CI must fail when the resolved Chrome path is not executable"
grep -Fq 'CHROME_PATH=' <<<"$chrome_gate" ||
  fail "backend CI must export CHROME_PATH for live browser tests"
grep -Fq 'GITHUB_ENV' <<<"$chrome_gate" ||
  fail "backend CI must persist CHROME_PATH into the test step environment"
chrome_line=$(grep -nF -- '- name: require live Chrome' "$WORKFLOW" | cut -d: -f1)
backend_run_line=$(grep -nF -- '- name: unit + integration tests (with coverage)' "$WORKFLOW" | cut -d: -f1)
[[ "$chrome_line" -lt "$backend_run_line" ]] ||
  fail "the live Chrome prerequisite must run before backend tests"

frontend_gate=$(step_block "tests + coverage gate")
[[ -n "$frontend_gate" ]] || fail "frontend blocking coverage test step is missing"
grep -Eq '^[[:space:]]+run: bun run coverage$' <<<"$frontend_gate" ||
  fail "frontend must use Vitest's native threshold gate"
if grep -Eq '^[[:space:]]+continue-on-error:[[:space:]]*true$' <<<"$frontend_gate"; then
  fail "frontend coverage gate must block CI"
fi
[[ $(grep -Ec '^[[:space:]]+run: bun run coverage$' <<<"$frontend_job") -eq 1 ]] ||
  fail "frontend must run its coverage-instrumented test suite exactly once"
if awk '
  /^      - / { working_directory = "" }
  /^[[:space:]]+working-directory:/ { working_directory = $2 }
  working_directory == "web" && /^[[:space:]]+run: bun run (test|coverage:nogate)$/ { found = 1 }
  END { exit !found }
' <<<"$frontend_job"; then
  fail "frontend CI must not bypass thresholds or rerun the test suite"
fi
grep -Eq '"coverage":[[:space:]]*"vitest run --coverage"' "$WEB_PACKAGE" ||
  fail "frontend coverage command must leave Vitest thresholds enabled"
for threshold in 'lines: 85' 'statements: 85' 'functions: 85' 'branches: 80'; do
  grep -Fq "$threshold" "$VITEST_CONFIG" || fail "missing frontend threshold: $threshold"
done

extension_gate=$(step_block "extension tests")
[[ -n "$extension_gate" ]] || fail "blocking extension tests are missing from CI"
grep -Eq '^[[:space:]]+working-directory: extension$' <<<"$extension_gate" ||
  fail "extension tests must run from the extension package"
grep -Eq '^[[:space:]]+run: bun run test$' <<<"$extension_gate" ||
  fail "extension CI must use its package test script"
if grep -Eq '^[[:space:]]+continue-on-error:[[:space:]]*true$' <<<"$extension_gate"; then
  fail "extension tests must block CI"
fi
[[ $(grep -Ec '^[[:space:]]+run: bun run test$' <<<"$frontend_job") -eq 1 ]] ||
  fail "extension CI must run its test suite exactly once"

while IFS= read -r image; do
  [[ -z "$image" ]] && continue
  if [[ ! "$image" =~ ^[^@[:space:]]+:[^@[:space:]]*[0-9][^@[:space:]]*@sha256:[0-9a-f]{64}$ ]]; then
    fail "workflow service image must have a version and immutable digest: $image"
  fi
done < <(awk '
  /^[[:space:]]+services:[[:space:]]*$/ {
    in_services = 1
    services_indent = match($0, /[^[:space:]]/) - 1
    next
  }
  in_services {
    if ($0 !~ /^[[:space:]]*$/) {
      indent = match($0, /[^[:space:]]/) - 1
      if (indent <= services_indent) {
        in_services = 0
      }
    }
    if (in_services && $0 ~ /^[[:space:]]+image:[[:space:]]*/) {
      sub(/^[[:space:]]+image:[[:space:]]*/, "")
      sub(/[[:space:]]+#.*/, "")
      gsub(/["'\''"]/, "")
      print
    }
  }
' "$WORKFLOW")

grep -Fq 'run: bash scripts/test-ci-coverage-gates.sh' "$WORKFLOW" ||
  fail "CI coverage contract test is not wired into CI"
grep -Fq 'run: bash scripts/test-nginx-access-log.sh' "$WORKFLOW" ||
  fail "nginx access-log security evidence test is not wired into CI"

claude_permission_gate=$(step_block "local Bash approval contract")
[[ -n "$claude_permission_gate" ]] || fail "local Bash approval contract is missing from CI"
grep -Eq '^[[:space:]]+run: bash scripts/test-claude-bash-permissions\.sh$' <<<"$claude_permission_gate" ||
  fail "local Bash approval contract must run its tracked fixture harness"
if grep -Eq '^[[:space:]]+continue-on-error:[[:space:]]*true$' <<<"$claude_permission_gate"; then
  fail "local Bash approval contract must block CI"
fi
[[ $(grep -Ec '^[[:space:]]+run: bash scripts/test-claude-bash-permissions\.sh$' <<<"$frontend_job") -eq 1 ]] ||
  fail "local Bash approval contract must run in the blocking frontend job"
[[ $(grep -Ec '^[[:space:]]+run: bash scripts/test-claude-bash-permissions\.sh$' "$WORKFLOW") -eq 1 ]] ||
  fail "local Bash approval contract must run exactly once"

printf '%s\n' "CI coverage gate contract passed"
