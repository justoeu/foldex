#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SETTINGS=${1:-"$ROOT/.claude/settings.local.json"}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

readonly -a ALLOWED_BASH_COMMANDS=(
  'go version'
  'command -v graphify'
  'graphify update .'
  'git check-ignore .claude/settings.local.json'
  'git --no-pager diff --stat'
  '/tmp/gobin/actionlint .github/workflows/ci.yml'
  '/tmp/gobin/actionlint .github/workflows/codeql.yml .github/workflows/sast.yml .github/workflows/dast.yml'
  '/tmp/gobin/actionlint -color .github/workflows/codeql.yml .github/workflows/sast.yml .github/workflows/dast.yml'
  'curl -fsS http://127.0.0.1:9089/healthz'
  'curl -fsSk https://127.0.0.1:9444/'
  'curl -s http://127.0.0.1:9089/healthz -o /dev/null -w "backend:%{http_code}\n"'
  'curl -sk https://127.0.0.1:9444/healthz -o /dev/null -w "web:%{http_code}\n"'
  'curl -sk https://127.0.0.1:9444/healthz -o /dev/null -w "web:%{http_code} "'
  'curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:semgrep/semgrep:pull"'
  'curl -s "https://auth.docker.io/token?service=registry.docker.io&scope=repository:rhysd/actionlint:pull"'
  'curl -s "https://ghcr.io/token?scope=repository:zaproxy/zaproxy:pull"'
  'curl -s "https://hub.docker.com/v2/repositories/semgrep/semgrep/tags?page_size=5"'
  'curl -s "https://hub.docker.com/v2/repositories/semgrep/semgrep/tags?page_size=15&name=1.166"'
  'curl -s "https://hub.docker.com/v2/repositories/rhysd/actionlint/tags?page_size=6"'
)

check_command() {
  local command=$1
  local allowed
  for allowed in "${ALLOWED_BASH_COMMANDS[@]}"; do
    [[ "$command" == "$allowed" ]] && return 0
  done
  POLICY_REASON="command is not in the strict exact allowlist"
  return 1
}

validate_permissions() {
  local file=$1
  local command

  jq -e '
    (.permissions.allow // []) as $allow |
    ($allow | type == "array") and
    ($allow | all(.[]; type == "string")) and
    ($allow | all(.[];
      if startswith("Bash(") then
        test("^Bash\\([^\\r\\n]*\\)$")
      else
        true
      end
    ))
  ' "$file" >/dev/null || {
    printf 'invalid Claude permission settings: %s\n' "$file" >&2
    return 1
  }

  while IFS= read -r command; do
    POLICY_REASON=
    if ! check_command "$command"; then
      printf 'dangerous Bash approval (%s): %s\n' "$POLICY_REASON" "$file" >&2
      return 1
    fi
  done < <(jq -r '
    (.permissions.allow // [])[]
    | select(startswith("Bash("))
    | .[5:-1]
  ' "$file")
}

cat >"$TMP/safe.json" <<'JSON'
{
  "permissions": {
    "allow": [
      "Read(//private/tmp/**)",
      "Bash(go version)",
      "Bash(command -v graphify)",
      "Bash(graphify update .)",
      "Bash(git check-ignore .claude/settings.local.json)",
      "Bash(git --no-pager diff --stat)",
      "Bash(/tmp/gobin/actionlint .github/workflows/ci.yml)"
    ]
  }
}
JSON

cat >"$TMP/original-vulnerable.json" <<'JSON'
{
  "permissions": {
    "allow": [
      "Bash(python3 *)"
    ]
  }
}
JSON

validate_permissions "$TMP/safe.json" || fail "safe exact-command fixture was rejected"
if validate_permissions "$TMP/original-vulnerable.json" >/dev/null 2>&1; then
  fail "original vulnerable Bash(python3 *) fixture was accepted"
fi

# shellcheck disable=SC2016
dangerous_approvals=(
  'Bash(curl https://example.test/*)'
  'Bash(git)'
  'Bash(git *)'
  'Bash(npm run test)'
  'Bash(python3 scripts/check.py)'
  'Bash(node scripts/check.js)'
  'Bash(bun run test)'
  'Bash(deno run scripts/check.ts)'
  'Bash(ruby scripts/check.rb)'
  'Bash(perl scripts/check.pl)'
  'Bash(bash scripts/check.sh)'
  'Bash(sh scripts/check.sh)'
  'Bash(zsh scripts/check.sh)'
  'Bash(git status && git diff)'
  'Bash(git status | tee /tmp/status)'
  'Bash(git status; git diff)'
  'Bash(git status $(touch /tmp/claude-contract-bypass))'
  'Bash(git status `touch /tmp/claude-contract-bypass`)'
  'Bash((python3 scripts/untrusted.py))'
  'Bash(source scripts/untrusted.sh)'
  'Bash(. scripts/untrusted.sh)'
  'Bash((git --no-pager diff --stat))'
  'Bash(git --no-pager diff --stat > /tmp/diff)'
)

for approval in "${dangerous_approvals[@]}"; do
  jq -n --arg approval "$approval" \
    '{permissions: {allow: [$approval]}}' >"$TMP/dangerous.json"
  if validate_permissions "$TMP/dangerous.json" >/dev/null 2>&1; then
    fail "dangerous fixture was accepted: $approval"
  fi
done

if [[ -f "$SETTINGS" ]]; then
  validate_permissions "$SETTINGS"
  printf 'validated local Claude Bash approvals: %s\n' "$SETTINGS"
else
  printf 'local Claude settings absent; fixture policy validated\n'
fi

printf '%s\n' "Claude Bash permission contract passed"
