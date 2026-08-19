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

# Extract the rewrite functions out of release.sh and into a tiny harness.
# Sourcing release.sh directly would trigger its dirty-tree check, so we
# slice the functions only. Each closing brace is anchored at column zero.
HARNESS=$(mktemp)
trap 'rm -f "$HARNESS"' EXIT

for function_name in update_version update_compose_version compose_version_is; do
  awk -v function_name="$function_name" '
    $0 == function_name "() {" { capturing = 1 }
    capturing { print }
    capturing && /^\}$/ { exit }
  ' "$RELEASE" >>"$HARNESS"
done

for function_name in update_version update_compose_version compose_version_is; do
  grep -q "^$function_name()" "$HARNESS" || {
    echo "✗ could not extract $function_name() from release.sh" >&2
    exit 1
  }
done

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
  # shellcheck disable=SC2034
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
  # shellcheck disable=SC2034
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

# The explicit version accepted by release.sh must match the workflow's strict
# semver core: no prerelease/build suffix and no leading zero.
release_case=$(awk '
  /^# Compute next version\./ { capturing = 1 }
  capturing { print }
  capturing && /^esac$/ { exit }
' "$RELEASE")
grep -Fq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$' <<<"$release_case" || {
  echo "✗ explicit release version is not strict semver" >&2
  exit 1
}
for invalid in 01.2.3 1.02.3 1.2.03 1.2.3-rc.1 1x2x3; do
  if [[ "$invalid" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
    echo "✗ invalid explicit release version accepted: $invalid" >&2
    exit 1
  fi
done
echo "✓ case 4: explicit versions use strict semver"

# ─── case 5: every Compose image default moves in lockstep ─────────────

COMPOSE_FIXTURE=$(mktemp)
trap 'rm -f "$HARNESS" "$FIXTURE" "$COMPOSE_FIXTURE"' EXIT
cat >"$COMPOSE_FIXTURE" <<'YAML'
services:
  backend:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.1.1}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.1.1}
  unrelated:
    image: example/tool:1.1.1
YAML

(
  # shellcheck disable=SC1090
  source "$HARNESS"
  CUR="1.1.1"
  NEW="9.9.9"
  update_compose_version "$COMPOSE_FIXTURE"
) || {
  # Without this guard `set -e` aborts the whole suite here with no output at
  # all, and the last thing printed is the PREVIOUS case's ✓ — sending whoever
  # reads the CI log to investigate a case that is fine.
  echo "✗ rewrite refused a well-formed Compose file" >&2
  exit 1
}

grep -Fq 'justoeu/foldex-backend:${FOLDEX_VERSION:-9.9.9}' "$COMPOSE_FIXTURE" || {
  echo "✗ backend Compose default was not bumped" >&2
  exit 1
}
grep -Fq 'justoeu/foldex-web:${FOLDEX_VERSION:-9.9.9}' "$COMPOSE_FIXTURE" || {
  echo "✗ web Compose default was not bumped" >&2
  exit 1
}
grep -Fq 'example/tool:1.1.1' "$COMPOSE_FIXTURE" || {
  echo "✗ unrelated Compose image was rewritten" >&2
  exit 1
}
if grep -Eq 'justoeu/foldex-(backend|web):.*latest' "$COMPOSE_FIXTURE"; then
  echo "✗ Foldex Compose defaults must never use latest" >&2
  exit 1
fi

ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
CURRENT=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$ROOT/web/package.json" |
            sed -n '1s/.*"\([^"]*\)"$/\1/p')
for image in backend web; do
  grep -Fq "justoeu/foldex-$image:\${FOLDEX_VERSION:-$CURRENT}" "$ROOT/docker-compose.yml" || {
    echo "✗ current $image Compose default is not locked to release $CURRENT" >&2
    exit 1
  }
done
if grep -Eq 'justoeu/foldex-(backend|web):.*latest' "$ROOT/docker-compose.yml"; then
  echo "✗ current Foldex Compose defaults must never use latest" >&2
  exit 1
fi
echo "✓ case 5: backend/web Compose defaults move together without :latest"

# ─── case 6: dirty/off-main gates and failed-commit rollback ───────────

GATE_ROOT=$(mktemp -d)
trap 'rm -f "$HARNESS" "$FIXTURE" "$COMPOSE_FIXTURE"; rm -rf "$GATE_ROOT"' EXIT
mkdir -p "$GATE_ROOT/repo/scripts" "$GATE_ROOT/repo/web" "$GATE_ROOT/repo/extension"
cp "$RELEASE" "$GATE_ROOT/repo/scripts/release.sh"
cat >"$GATE_ROOT/repo/web/package.json" <<'JSON'
{"version":"1.2.3"}
JSON
cat >"$GATE_ROOT/repo/extension/manifest.json" <<'JSON'
{"version":"1.2.3"}
JSON
cat >"$GATE_ROOT/repo/docker-compose.yml" <<'YAML'
services:
  backend:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.2.3}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.2.3}
YAML
printf 'clean\n' >"$GATE_ROOT/repo/state"
git init --bare -q "$GATE_ROOT/origin.git"
git -C "$GATE_ROOT/repo" init -q
git -C "$GATE_ROOT/repo" config user.name test
git -C "$GATE_ROOT/repo" config user.email test@foldex.invalid
git -C "$GATE_ROOT/repo" add .
git -C "$GATE_ROOT/repo" commit -qm initial
git -C "$GATE_ROOT/repo" branch -M main
git -C "$GATE_ROOT/repo" remote add origin "$GATE_ROOT/origin.git"
git -C "$GATE_ROOT/repo" push -qu origin main
INITIAL_SHA=$(git -C "$GATE_ROOT/repo" rev-parse HEAD)

printf 'dirty\n' >"$GATE_ROOT/repo/state"
if (cd "$GATE_ROOT/repo" && bash scripts/release.sh patch) >"$GATE_ROOT/dirty.out" 2>&1; then
  echo "✗ release accepted a dirty worktree" >&2
  exit 1
fi
grep -q 'working tree is dirty' "$GATE_ROOT/dirty.out" || {
  echo "✗ dirty-worktree refusal changed unexpectedly" >&2
  exit 1
}
git -C "$GATE_ROOT/repo" restore state

git -C "$GATE_ROOT/repo" switch -qc topic
if (cd "$GATE_ROOT/repo" && bash scripts/release.sh patch) >"$GATE_ROOT/branch.out" 2>&1; then
  echo "✗ release accepted an off-main branch" >&2
  exit 1
fi
grep -q "refusing to release from 'topic'" "$GATE_ROOT/branch.out" || {
  echo "✗ off-main refusal changed unexpectedly" >&2
  exit 1
}
git -C "$GATE_ROOT/repo" switch -q main

mkdir -p "$GATE_ROOT/bin"
REAL_GIT=$(command -v git)
cat >"$GATE_ROOT/bin/git" <<'SH'
#!/usr/bin/env bash
if [ "${1:-}" = commit ]; then
  exit 97
fi
exec "$REAL_GIT" "$@"
SH
chmod +x "$GATE_ROOT/bin/git"
if (cd "$GATE_ROOT/repo" && REAL_GIT="$REAL_GIT" PATH="$GATE_ROOT/bin:$PATH" bash scripts/release.sh patch) >"$GATE_ROOT/rollback.out" 2>&1; then
  echo "✗ release unexpectedly survived a failed commit" >&2
  exit 1
fi
if [ "$(git -C "$GATE_ROOT/repo" rev-parse HEAD)" != "$INITIAL_SHA" ] ||
   [ -n "$(git -C "$GATE_ROOT/repo" status --porcelain)" ]; then
  echo "✗ failed release did not atomically roll back all version files" >&2
  exit 1
fi
grep -Fq '"version":"1.2.3"' "$GATE_ROOT/repo/web/package.json" || exit 1
grep -Fq '"version":"1.2.3"' "$GATE_ROOT/repo/extension/manifest.json" || exit 1
grep -Fq 'foldex-backend:${FOLDEX_VERSION:-1.2.3}' "$GATE_ROOT/repo/docker-compose.yml" || exit 1
grep -Fq 'foldex-web:${FOLDEX_VERSION:-1.2.3}' "$GATE_ROOT/repo/docker-compose.yml" || exit 1
echo "✓ case 6: dirty/off-main gates remain closed and failed commits roll back"

# ─── case 7: a service reusing the backend image is in scope, not extra ──
#
# The mailer runs the backend image, so docker-compose.yml carries more than
# one `foldex-backend:` line. The gate used to require EXACTLY one of each and
# refused every release the moment that service landed — a version-drift guard
# failing on a file with no drift. What must hold is that EVERY matching line
# is pinned, whatever their count; a second line left behind at the old version
# is the actual failure this guard exists to catch.

MULTI_FIXTURE=$(mktemp)
DRIFT_FIXTURE=$(mktemp)
WEBDRIFT_FIXTURE=$(mktemp)
NOBACKEND_FIXTURE=$(mktemp)
# The `.tmp` siblings are listed because a REFUSED rewrite leaves one behind:
# update_compose_version only `mv`s when awk exits 0, and the refusal path is
# exercised twice below.
trap 'rm -f "$HARNESS" "$FIXTURE" "$COMPOSE_FIXTURE" "$MULTI_FIXTURE" "$DRIFT_FIXTURE" \
  "$WEBDRIFT_FIXTURE" "$NOBACKEND_FIXTURE" "$DRIFT_FIXTURE.tmp" "$WEBDRIFT_FIXTURE.tmp" \
  "$NOBACKEND_FIXTURE.tmp"; rm -rf "$GATE_ROOT"' EXIT

cat >"$MULTI_FIXTURE" <<'YAML'
services:
  backend:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.1.1}
  mailer:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.1.1}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.1.1}
YAML

(
  # shellcheck disable=SC1090
  source "$HARNESS"
  compose_version_is "$MULTI_FIXTURE" "1.1.1"
) || {
  echo "✗ read gate refused a Compose file whose backend image is reused" >&2
  exit 1
}

(
  # shellcheck disable=SC1090
  source "$HARNESS"
  CUR="1.1.1"
  NEW="9.9.9"
  update_compose_version "$MULTI_FIXTURE"
) || {
  echo "✗ rewrite refused a Compose file whose backend image is reused" >&2
  exit 1
}

if [ "$(grep -Fc 'justoeu/foldex-backend:${FOLDEX_VERSION:-9.9.9}' "$MULTI_FIXTURE")" != 2 ]; then
  echo "✗ every backend Compose default must be bumped, not just the first" >&2
  exit 1
fi

# One line left behind must still be refused — both on read and on rewrite.
cat >"$DRIFT_FIXTURE" <<'YAML'
services:
  backend:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.1.1}
  mailer:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.0.9}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.1.1}
YAML
DRIFT_BEFORE=$(cat "$DRIFT_FIXTURE")

if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  compose_version_is "$DRIFT_FIXTURE" "1.1.1"
); then
  echo "✗ read gate accepted a stale second backend default" >&2
  exit 1
fi

if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  CUR="1.1.1"
  NEW="9.9.9"
  update_compose_version "$DRIFT_FIXTURE"
); then
  echo "✗ rewrite accepted a stale second backend default" >&2
  exit 1
fi
if [ "$(cat "$DRIFT_FIXTURE")" != "$DRIFT_BEFORE" ]; then
  echo "✗ refused rewrite still modified the Compose file" >&2
  exit 1
fi
echo "✓ case 7: reused images are bumped together and drift is still refused"

# ─── case 8: the OTHER half of the relaxed predicate ────────────────────
#
# `>= 1` says two things, and the case above only exercises one of them. The
# other is "at least one", and nothing was testing it: `END { exit !(!bad) }`
# looks like a harmless simplification and would make the gate accept a
# docker-compose.yml carrying NO foldex image at all — a release published
# with nothing pinned, which is the exact opposite of what this gate is for.
#
# The web side is relaxed identically, and today's file has one web line only
# by accident rather than by design, so the reuse case is asserted there too.

cat >"$NOBACKEND_FIXTURE" <<'YAML'
services:
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.1.1}
YAML

if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  compose_version_is "$NOBACKEND_FIXTURE" "1.1.1"
); then
  echo "✗ read gate accepted a Compose file with no backend image at all" >&2
  exit 1
fi
if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  CUR="1.1.1"
  NEW="9.9.9"
  update_compose_version "$NOBACKEND_FIXTURE"
); then
  echo "✗ rewrite accepted a Compose file with no backend image at all" >&2
  exit 1
fi

cat >"$WEBDRIFT_FIXTURE" <<'YAML'
services:
  backend:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.1.1}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.1.1}
  web-canary:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.0.9}
YAML
WEBDRIFT_BEFORE=$(cat "$WEBDRIFT_FIXTURE")

if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  compose_version_is "$WEBDRIFT_FIXTURE" "1.1.1"
); then
  echo "✗ read gate accepted a stale second web default" >&2
  exit 1
fi
if (
  # shellcheck disable=SC1090
  source "$HARNESS"
  CUR="1.1.1"
  NEW="9.9.9"
  update_compose_version "$WEBDRIFT_FIXTURE"
); then
  echo "✗ rewrite accepted a stale second web default" >&2
  exit 1
fi
if [ "$(cat "$WEBDRIFT_FIXTURE")" != "$WEBDRIFT_BEFORE" ]; then
  echo "✗ refused rewrite still modified the Compose file" >&2
  exit 1
fi
echo "✓ case 8: a missing image and a stale reused web line are still refused"

# ─── case 9: a version read from disk is not trusted as a number ────────
#
# CUR comes out of web/package.json via `[^"]+`, which admits `$`, `(` and `[`,
# and lands in `$((PAT+1))`. Bash resolves command substitution inside an array
# subscript in arithmetic context, so a crafted "version" string in a commit
# executes on the maintainer's machine the moment they run `make release-patch`
# — the one machine holding push rights on main and dispatch rights on the
# release workflow. The strict semver regex already guarded what the operator
# TYPES; this asserts it also guards what the script READS.

# Three things the fixture must get right, each of which would otherwise turn
# this into a green that proves nothing:
#   - the payload is COMMITTED, or release.sh stops at its dirty-tree check;
#   - the SAME string goes into all three version files, or the sync check
#     refuses first and the arithmetic is never reached — which is exactly how
#     an earlier draft of this case passed while asserting nothing;
#   - the path is relative and dot-free, since `IFS=. read` splits on `.`;
#   - the array base is PATH, not an arbitrary name. `set -u` aborts on an
#     UNDEFINED base before the subscript runs, so `x[$(…)]` would be refused
#     by the shell rather than by the guard and prove nothing. With a defined
#     base the substitution runs first and the unbound/syntax error arrives
#     only afterwards — the payload has already executed by then.
# shellcheck disable=SC2016
printf '{"version":"1.0.PATH[$(touch canary)]"}\n' >"$GATE_ROOT/repo/web/package.json"
# shellcheck disable=SC2016
printf '{"version":"1.0.PATH[$(touch canary)]"}\n' >"$GATE_ROOT/repo/extension/manifest.json"
# shellcheck disable=SC2016
printf 'services:\n  backend:\n    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.0.PATH[$(touch canary)]}\n  web:\n    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.0.PATH[$(touch canary)]}\n' \
  >"$GATE_ROOT/repo/docker-compose.yml"
git -C "$GATE_ROOT/repo" commit -qam "poisoned version"
git -C "$GATE_ROOT/repo" push -qu origin main

if (cd "$GATE_ROOT/repo" && bash scripts/release.sh patch) >"$GATE_ROOT/inject.out" 2>&1; then
  echo "✗ release accepted a non-semver version read from disk" >&2
  exit 1
fi
if [ -e "$GATE_ROOT/repo/canary" ]; then
  echo "✗ a version string read from disk reached arithmetic evaluation" >&2
  exit 1
fi
grep -q 'not strict semver' "$GATE_ROOT/inject.out" || {
  echo "✗ release refused the poisoned version for the wrong reason" >&2
  cat "$GATE_ROOT/inject.out" >&2
  exit 1
}
echo "✓ case 9: versions read from disk are validated before they are evaluated"

echo
echo "release.sh version transaction — all assertions passed."
