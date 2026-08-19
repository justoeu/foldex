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
PIN_AWK="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/compose-image-pin.awk"
if [ ! -f "$PIN_AWK" ]; then
  echo "✗ missing $PIN_AWK" >&2
  exit 1
fi

# Every Compose file, not just the primary one. The gate used to read
# docker-compose.yml alone, so a Foldex image moved into one of the others —
# the mailer into docker-compose.mail.yml, say — would leave the release
# unversioned with nothing to say so. The others carry no Foldex image today;
# the point is that the day one does, it is already covered.
COMPOSE_FILES=()
while IFS= read -r compose_file; do
  COMPOSE_FILES+=("$compose_file")
done < <(git ls-files 'docker-compose*.yml' | sort)
if [ "${#COMPOSE_FILES[@]}" -eq 0 ]; then
  echo "✗ no tracked docker-compose*.yml to version" >&2
  exit 1
fi
VERSION_FILES=("$PKG" "$EXT" "${COMPOSE_FILES[@]}")

# Once rewriting starts, any failure must put every version file back at HEAD.
# The release begins from a clean tree, so this cannot discard unrelated work.
BUMP_STARTED=0
rollback_bump() {
  local status=$?
  local leftover
  trap - EXIT
  rm -f "$PKG.tmp" "$EXT.tmp"
  for leftover in "${COMPOSE_FILES[@]}"; do
    rm -f "$leftover.tmp"
  done
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

# The version is read with `[^"]+`, which admits `$`, `(` and `[`, and it ends
# up in `$((PAT+1))` below. Bash resolves command substitution inside an array
# subscript in arithmetic context, so an unvalidated string from a committed
# file would execute here — on the machine that holds push rights on main and
# dispatch rights on the release workflow. The explicit-target branch already
# demands strict semver from what the operator TYPES; a version read off disk
# deserves no more trust than one typed at the prompt.
if ! [[ "$CUR" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "✗ current version in $PKG is not strict semver: $CUR" >&2
  exit 1
fi

compose_version_is() {
  awk -f "$PIN_AWK" -v expected="$2" -v require_services="${3:-1}" "$1"
}

EXT_CUR=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$EXT" \
            | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
# Validated on its own rather than left safe by the equality check below. It is
# safe today only BECAUSE it must equal CUR to get past that check — a property
# of the comparison, not of the value — and the next edit that reads the
# manifest earlier, or uses this in arithmetic, silently reopens the hole the
# guard above closes.
if ! [[ "$EXT_CUR" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "✗ current version in $EXT is not strict semver: $EXT_CUR" >&2
  exit 1
fi
if [ "$EXT_CUR" != "$CUR" ]; then
  echo "✗ release versions are out of sync; expected $CUR in $EXT" >&2
  exit 1
fi
for compose_file in "${COMPOSE_FILES[@]}"; do
  if [ "$compose_file" = "$COMPOSE" ]; then require=1; else require=0; fi
  if ! compose_version_is "$compose_file" "$CUR" "$require"; then
    echo "✗ $compose_file does not pin every Foldex image to $CUR" >&2
    exit 1
  fi
done

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

# Same program, same rules, `new` set: awk prints the rewritten file and still
# exits non-zero if anything was off, so the `&&` leaves the original in place.
update_compose_version() {
  local file="$1"
  awk -f "$PIN_AWK" -v expected="$CUR" -v new="$NEW" -v require_services="${2:-1}" \
    "$file" > "$file.tmp" && mv "$file.tmp" "$file"
}

BUMP_STARTED=1
update_version "$PKG"
update_version "$EXT"
for compose_file in "${COMPOSE_FILES[@]}"; do
  if [ "$compose_file" = "$COMPOSE" ]; then require=1; else require=0; fi
  update_compose_version "$compose_file" "$require"
done

# Sanity check — all release-owned versions should now report NEW.
for f in "$PKG" "$EXT"; do
  got=$(grep -oE '"version"[[:space:]]*:[[:space:]]*"[^"]+"' "$f" | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
  if [ "$got" != "$NEW" ]; then
    echo "✗ bump failed for $f (still $got)" >&2
    exit 1
  fi
done
for compose_file in "${COMPOSE_FILES[@]}"; do
  if [ "$compose_file" = "$COMPOSE" ]; then require=1; else require=0; fi
  if ! compose_version_is "$compose_file" "$NEW" "$require"; then
    echo "✗ bump failed for $compose_file" >&2
    exit 1
  fi
done

git add "${VERSION_FILES[@]}"
git commit -m "chore(release): v$NEW"
BUMP_STARTED=0

echo
echo "✓ committed the v$NEW version bump."
echo "  Push with: git push origin main"
echo "  Publish with: gh workflow run release.yml --ref main -f target=v$NEW"
echo "  The workflow creates v$NEW and publishes justoeu/foldex-{backend,web}:$NEW + :latest"
