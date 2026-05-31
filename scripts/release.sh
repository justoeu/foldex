#!/usr/bin/env bash
# Cut a new foldex release.
#
# Bumps the version across:
#   - web/package.json       (SPA — read by src/version.ts → sidebar footer)
#   - extension/manifest.json (browser extension MV3 manifest)
# then commits, tags `vX.Y.Z`, and (with your confirmation) pushes.
#
# Pushing the tag triggers ci.yml (it watches `tags: ['v*']`), which
# publishes Docker images tagged `:vX.Y.Z` + `:vX.Y` + `:vX` + `:latest`
# for both `foldex-backend` and `foldex-web`.
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

# Refuse off-main releases — we tag from main and CI watches main + tags.
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

# Read current version from web/package.json (source of truth — the
# sidebar footer renders it). Both files are kept in lockstep by this
# script and a couple of CI tests assert it.
CUR=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$PKG" \
        | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
if [ -z "$CUR" ]; then
  echo "✗ could not read current version from $PKG" >&2
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
  [0-9]*.[0-9]*.[0-9]*)
    NEW="$PART"
    ;;
  *)
    echo "✗ unknown bump '$PART' — expected: patch | minor | major | X.Y.Z" >&2
    exit 1
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

update_version "$PKG"
update_version "$EXT"

# Sanity check — both files should now report NEW.
for f in "$PKG" "$EXT"; do
  got=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$f" | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
  if [ "$got" != "$NEW" ]; then
    echo "✗ bump failed for $f (still $got)" >&2
    git checkout -- "$PKG" "$EXT"
    exit 1
  fi
done

git add "$PKG" "$EXT"
git commit -m "chore(release): v$NEW"
git tag -a "v$NEW" -m "v$NEW"

echo
echo "✓ tagged v$NEW locally."
echo "  Push with: git push origin main && git push origin v$NEW"
echo "  CI will publish justoeu/foldex-{backend,web}:v$NEW + :latest"
