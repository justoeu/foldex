#!/usr/bin/env bash
# Cut a new foldex release.
#
# Bumps the version across:
#   - web/package.json       (SPA — read by src/version.ts → sidebar footer)
#   - extension/manifest.json (browser extension MV3 manifest)
#   - docker-compose.yml     (backend/web default image tags)
# then commits the bump. After pushing main, dispatch release.yml with the
# strict `vX.Y.Z` target; the validated workflow creates the tag itself.
#
# A manual release.yml dispatch from main publishes Docker images tagged
# `:X.Y.Z` + `:X.Y` + `:X` + `:latest` —
# NOTE: docker/metadata-action strips the leading `v`, so the git tag is
# `vX.Y.Z` but the image tags carry NO `v` (pin FOLDEX_VERSION=X.Y.Z, not
# vX.Y.Z). Applies to both `foldex-backend` and `foldex-web`. (ci.yml is the
# PR gate; branch and tag pushes do not publish.)
#
# Usage:
#   ./scripts/release.sh patch     # 1.0.8 → 1.0.9
#   ./scripts/release.sh minor     # 1.0.8 → 1.1.0
#   ./scripts/release.sh major     # 1.0.8 → 2.0.0
#   ./scripts/release.sh 1.2.3     # bump to an explicit version
#
# Refuses to run with uncommitted changes (a dirty tree means the bump
# commit would also drag along unrelated work).

set -euo pipefail

PART="${1:-patch}"

# Refuse to release from a dirty tree — surprise files would land in the
# bump commit otherwise.
if [ -n "$(git status --porcelain)" ]; then
  echo "✗ working tree is dirty. Commit or stash first." >&2
  git status --short >&2
  exit 1
fi

# Refuse off-main releases — the workflow accepts dispatches from main only.
BRANCH=$(git rev-parse --abbrev-ref HEAD)
if [ "$BRANCH" != "main" ]; then
  echo "✗ refusing to release from '$BRANCH' (expected: main)" >&2
  exit 1
fi

# Make sure local main is up to date — otherwise the tag points at a
# detached state and the next push has unrelated work in front of it.
git fetch origin main --quiet
LOCAL=$(git rev-parse HEAD)
REMOTE=$(git rev-parse origin/main)
if [ "$LOCAL" != "$REMOTE" ]; then
  echo "✗ local main is out of sync with origin/main. Pull/push first." >&2
  exit 1
fi

PKG=web/package.json
EXT=extension/manifest.json
COMPOSE=docker-compose.yml
VERSION_FILES=("$PKG" "$EXT" "$COMPOSE")

# Once rewriting starts, any failure must put every version file back at HEAD.
# The release begins from a clean tree, so this cannot discard unrelated work.
BUMP_STARTED=0
rollback_bump() {
  local status=$?
  trap - EXIT
  rm -f "$PKG.tmp" "$EXT.tmp" "$COMPOSE.tmp"
  if [ "$status" -ne 0 ] && [ "$BUMP_STARTED" -eq 1 ]; then
    if ! git restore --staged --worktree -- "${VERSION_FILES[@]}"; then
      echo "✗ release failed and automatic version rollback also failed" >&2
    fi
  fi
  exit "$status"
}
trap rollback_bump EXIT

# Read current version from web/package.json (source of truth — the
# sidebar footer renders it). Every release-owned version is kept in
# lockstep by this script and its CI regression test.
CUR=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$PKG" \
        | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
if [ -z "$CUR" ]; then
  echo "✗ could not read current version from $PKG" >&2
  exit 1
fi

compose_version_is() {
  local file="$1"
  local expected="$2"
  awk -v expected="$expected" '
    BEGIN {
      backend_prefix = "image: justoeu/foldex-backend:"
      web_prefix = "image: justoeu/foldex-web:"
      backend_expected = backend_prefix "${FOLDEX_VERSION:-" expected "}"
      web_expected = web_prefix "${FOLDEX_VERSION:-" expected "}"
    }
    {
      line = $0
      sub(/^[[:space:]]*/, "", line)
      if (index(line, backend_prefix) == 1) {
        backend++
        if (line != backend_expected) bad = 1
      }
      if (index(line, web_prefix) == 1) {
        web++
        if (line != web_expected) bad = 1
      }
    }
    # At least one of each, and EVERY matching line pinned to `expected`.
    # Not `== 1`: the mailer service reuses the backend image, so the file
    # legitimately carries more than one backend line. Counting exactly one
    # refused the whole release over a second line that was already correct.
    END { exit !(backend >= 1 && web >= 1 && !bad) }
  ' "$file"
}

EXT_CUR=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$EXT" \
            | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
if [ "$EXT_CUR" != "$CUR" ] || ! compose_version_is "$COMPOSE" "$CUR"; then
  echo "✗ release versions are out of sync; expected $CUR in $EXT and every Compose image default" >&2
  exit 1
fi

# Compute next version.
case "$PART" in
  major|minor|patch)
    IFS=. read -r MAJ MIN PAT <<<"$CUR"
    case "$PART" in
      major) NEW="$((MAJ+1)).0.0" ;;
      minor) NEW="$MAJ.$((MIN+1)).0" ;;
      patch) NEW="$MAJ.$MIN.$((PAT+1))" ;;
    esac
    ;;
  *)
    if [[ "$PART" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
      NEW="$PART"
    else
      echo "✗ unknown bump '$PART' — expected: patch | minor | major | X.Y.Z" >&2
      exit 1
    fi
    ;;
esac

echo "  current : v$CUR"
echo "  next    : v$NEW"
echo

# Bump both files in place. Using a strict regex anchored to the first
# occurrence of "version" so we don't accidentally rewrite, e.g.,
# manifest_version or a dependency version.
update_version() {
  local file="$1"
  # Cross-platform first-match-only replace via awk — BSD sed (macOS)
  # rejects the GNU `0,/regex/` range form and there's no portable
  # equivalent without spelunking through `1,/regex/` quirks. awk
  # tracks `done` itself, so the substitution fires once regardless
  # of how many "version" strings appear later (e.g. dep versions).
  awk -v new="$NEW" '
    !done && /"version"[[:space:]]*:[[:space:]]*"[0-9]+\.[0-9]+\.[0-9]+"/ {
      sub(/"version"[[:space:]]*:[[:space:]]*"[0-9]+\.[0-9]+\.[0-9]+"/,
          "\"version\": \"" new "\"")
      done = 1
    }
    { print }
  ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

update_compose_version() {
  local file="$1"
  awk -v current="$CUR" -v new="$NEW" '
    BEGIN {
      backend_prefix = "image: justoeu/foldex-backend:"
      web_prefix = "image: justoeu/foldex-web:"
      backend_current = backend_prefix "${FOLDEX_VERSION:-" current "}"
      web_current = web_prefix "${FOLDEX_VERSION:-" current "}"
      backend_new = backend_prefix "${FOLDEX_VERSION:-" new "}"
      web_new = web_prefix "${FOLDEX_VERSION:-" new "}"
    }
    {
      line = $0
      sub(/^[[:space:]]*/, "", line)
      indent = substr($0, 1, length($0) - length(line))
      if (index(line, backend_prefix) == 1) {
        backend++
        if (line != backend_current) bad = 1
        line = backend_new
        $0 = indent line
      }
      if (index(line, web_prefix) == 1) {
        web++
        if (line != web_current) bad = 1
        line = web_new
        $0 = indent line
      }
      print
    }
    END { if (backend < 1 || web < 1 || bad) exit 1 }
  ' "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

BUMP_STARTED=1
update_version "$PKG"
update_version "$EXT"
update_compose_version "$COMPOSE"

# Sanity check — all release-owned versions should now report NEW.
for f in "$PKG" "$EXT"; do
  got=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$f" | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
  if [ "$got" != "$NEW" ]; then
    echo "✗ bump failed for $f (still $got)" >&2
    exit 1
  fi
done
if ! compose_version_is "$COMPOSE" "$NEW"; then
  echo "✗ bump failed for $COMPOSE" >&2
  exit 1
fi

git add "${VERSION_FILES[@]}"
git commit -m "chore(release): v$NEW"
BUMP_STARTED=0

echo
echo "✓ committed the v$NEW version bump."
echo "  Push with: git push origin main"
echo "  Publish with: gh workflow run release.yml --ref main -f target=v$NEW"
echo "  The workflow creates v$NEW and publishes justoeu/foldex-{backend,web}:$NEW + :latest"
