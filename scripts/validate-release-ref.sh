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
  if ! git show "$target_sha:docker-compose.yml" | awk -v expected="$version" '
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
    END { exit !(backend == 1 && web == 1 && !bad) }
  '; then
    printf 'release tag %s does not match both Compose image defaults\n' "$release_tag" >&2
    exit 1
  fi
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
