#!/usr/bin/env bash
set -euo pipefail

target=${1:-${RELEASE_TARGET:-}}
workflow_ref=${2:-${GITHUB_REF:-}}
workflow_sha=${3:-${GITHUB_SHA:-}}

if [[ -z "$target" || -z "$workflow_ref" || -z "$workflow_sha" ]]; then
  printf '%s\n' "release validation requires a target, workflow ref, and workflow SHA" >&2
  exit 2
fi
if [[ "$workflow_ref" != refs/heads/main ]]; then
  printf '%s\n' "release workflow must run from refs/heads/main" >&2
  exit 1
fi

git fetch --quiet --no-tags origin '+refs/heads/main:refs/remotes/origin/main'
if ! git merge-base --is-ancestor "$workflow_sha" origin/main; then
  printf '%s\n' "release workflow SHA is not an ancestor of origin/main" >&2
  exit 1
fi

release_tag=
create_tag=false
is_semver=false

if [[ "$target" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  release_tag=$target
  is_semver=true

  if git ls-remote --exit-code --tags origin "refs/tags/$release_tag" >/dev/null 2>&1; then
    printf '%s\n' "release tag already exists; dispatch its full commit SHA instead" >&2
    exit 1
  fi
  target_sha=$(git rev-parse "$workflow_sha^{commit}")
  create_tag=true
elif [[ "$target" =~ ^[0-9a-fA-F]{40}$ ]]; then
  if ! target_sha=$(git rev-parse "$target^{commit}" 2>/dev/null); then
    printf '%s\n' "release target SHA does not exist" >&2
    exit 1
  fi
else
  printf '%s\n' "release target must be strict vMAJOR.MINOR.PATCH or a full 40-character SHA" >&2
  exit 1
fi

if ! git merge-base --is-ancestor "$target_sha" origin/main; then
  printf '%s\n' "release target SHA is not an ancestor of origin/main" >&2
  exit 1
fi

if [[ "$is_semver" == true ]]; then
  version=${release_tag#v}
  for file in web/package.json extension/manifest.json; do
    file_version=$(git show "$target_sha:$file" | jq -er '.version') || {
      printf 'cannot read version from %s at release target\n' "$file" >&2
      exit 1
    }
    if [[ "$file_version" != "$version" ]]; then
      printf 'release tag %s does not match %s version %s\n' "$release_tag" "$file" "$file_version" >&2
      exit 1
    fi
  done
  # Same awk program release.sh uses. It was a hand-copied duplicate, and that
  # is how one wrong assumption ended up in both — including in this file,
  # where the consequence is refusing to publish rather than refusing to bump.
  pin_awk="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib/compose-image-pin.awk"
  if [[ ! -f "$pin_awk" ]]; then
    printf 'missing %s\n' "$pin_awk" >&2
    exit 2
  fi
  # Filtered with grep rather than a pathspec: `git ls-tree` takes paths, not
  # globs, and silently returns nothing for `docker-compose*.yml` — which would
  # read as "no Compose file to check" and skip the gate entirely.
  compose_files=$(git ls-tree -r --name-only "$target_sha" |
                    grep -E '^docker-compose[^/]*\.yml$' || true)
  if [[ -z "$compose_files" ]]; then
    printf 'release target has no Compose file to verify\n' >&2
    exit 1
  fi
  # Every Compose file at the target, not only the primary one: an image that
  # moves into another file must not fall out of the gate unnoticed.
  while IFS= read -r compose_file; do
    if [[ "$compose_file" == docker-compose.yml ]]; then require=1; else require=0; fi
    if ! git show "$target_sha:$compose_file" |
         awk -f "$pin_awk" -v expected="$version" -v require_services="$require"; then
      printf 'release tag %s does not match %s\n' "$release_tag" "$compose_file" >&2
      exit 1
    fi
  done <<<"$compose_files"
fi

short_sha=$(git rev-parse --short=7 "$target_sha")
publish_latest=false
if [[ "$target_sha" == "$(git rev-parse origin/main)" ]]; then
  publish_latest=true
fi
if [[ -n ${GITHUB_OUTPUT:-} ]]; then
  {
    printf 'create_tag=%s\n' "$create_tag"
    printf 'is_semver=%s\n' "$is_semver"
    printf 'publish_latest=%s\n' "$publish_latest"
    printf 'release_tag=%s\n' "$release_tag"
    printf 'short_sha=%s\n' "$short_sha"
    printf 'target_sha=%s\n' "$target_sha"
  } >>"$GITHUB_OUTPUT"
fi
