#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VALIDATOR="$ROOT/scripts/validate-release-ref.sh"
WORKFLOW="$ROOT/.github/workflows/release.yml"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

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

# Current publication is dispatch-only. A pushed historical tag can still load
# its old workflow, so Docker credentials must also be environment-only.
grep -q '^  workflow_dispatch:' "$WORKFLOW" || fail "release must use workflow_dispatch"
trigger_block=$(awk '
  /^on:/ { in_on = 1 }
  in_on && /^[^ ]/ && !/^on:/ { exit }
  in_on { print }
' "$WORKFLOW")
if grep -Eq '^  push:|^[[:space:]]+tags:' <<<"$trigger_block"; then
  fail "release must not publish from a push or tag trigger"
fi
grep -q '^      target:' "$WORKFLOW" || fail "release dispatch must require a target input"
grep -q 'Delete any' "$WORKFLOW" || fail "workflow must require historical-path credentials to be environment-only"
pre_jobs=$(awk '/^jobs:/ { exit } { print }' "$WORKFLOW")
if grep -q 'secrets\.' <<<"$pre_jobs"; then
  fail "workflow-level configuration must not expose publishing secrets"
fi
if awk '/^[[:space:]]*-? uses:/ && $0 !~ /@[0-9a-f]{40}([[:space:]]|$)/ { print; bad = 1 } END { exit bad }' "$WORKFLOW"; then
  :
else
  fail "every release action must remain pinned to a full commit SHA"
fi

gate_job=$(job_block validate-release-ref)
[[ -n "$gate_job" ]] || fail "validate-release-ref job is missing"
if grep -Eq 'secrets\.|docker/login-action|docker/build-push-action|push=true' <<<"$gate_job"; then
  fail "release validation job must not receive publishing secrets or push images"
fi
grep -q 'persist-credentials: false' <<<"$gate_job" || fail "gate checkout must not persist credentials"
grep -q 'contents: read' <<<"$gate_job" || fail "gate must use read-only contents permission"
if grep -q 'contents: write' <<<"$gate_job"; then
  fail "validation job must not have write permission"
fi
if grep -Eq '^[[:space:]]+(id-token|packages|actions):' <<<"$gate_job"; then
  fail "validation job has unnecessary permissions"
fi
# shellcheck disable=SC2016
grep -Fq 'bash scripts/validate-release-ref.sh "$RELEASE_TARGET" "$GITHUB_REF" "$GITHUB_SHA"' <<<"$gate_job" ||
  fail "gate must validate the dispatch input, workflow ref, and workflow SHA"

tag_job=$(job_block create-release-tag)
[[ -n "$tag_job" ]] || fail "validated tag creation job is missing"
grep -q 'validate-release-ref' <<<"$tag_job" || fail "tag creation must depend on validation"
grep -q 'contents: write' <<<"$tag_job" || fail "only tag creation needs contents write"
grep -q 'publish-manifest' <<<"$tag_job" || fail "release tag must be created only after manifest publication"
if grep -Eq 'secrets\.|docker/login-action|docker/build-push-action|push=true' <<<"$tag_job"; then
  fail "tag creation must not receive Docker publishing secrets"
fi
if grep -Eq '^[[:space:]]+(id-token|packages|actions):' <<<"$tag_job"; then
  fail "tag creation has unnecessary permissions"
fi
# shellcheck disable=SC2016
grep -Fq 'repos/$GITHUB_REPOSITORY/git/tags' <<<"$tag_job" || fail "gate must create annotated tags after validation"
grep -Fq 'tagger[name]=github-actions[bot]' <<<"$tag_job" || fail "gate must set the authorized tagger explicitly"
grep -Fq 'release tag already exists' "$VALIDATOR" || fail "validator must reject every pre-existing semver tag"

publish_job=$(job_block publish)
manifest_job=$(job_block publish-manifest)
for block in "$publish_job" "$manifest_job"; do
  [[ -n "$block" ]] || fail "publishing job is missing"
  grep -q 'validate-release-ref' <<<"$block" || fail "every publisher must depend on the release gate"
  grep -q 'environment: release' <<<"$block" || fail "every publisher must use the protected release environment"
  grep -q 'secrets\.DOCKERHUB_USERNAME' <<<"$block" || fail "publisher is missing the Docker Hub username"
  grep -q 'secrets\.DOCKERHUB_TOKEN' <<<"$block" || fail "publisher is missing the Docker Hub token"
  if grep -Eq '^[[:space:]]+(id-token|packages|actions):|contents: write' <<<"$block"; then
    fail "publisher has unnecessary permissions"
  fi
done
grep -q 'publish]' <<<"$manifest_job" || fail "manifest publication must depend on digest publication"
grep -Fq '^(amd64|arm64)-[a-f0-9]{64}$' <<<"$manifest_job" || fail "manifest must identify both architecture digests"
# shellcheck disable=SC2016
grep -Fq '${#digest_files[@]} -ne 2' <<<"$manifest_job" || fail "manifest must require exactly two digests"
grep -Fq 'seen_digest' <<<"$manifest_job" || fail "manifest must require distinct architecture digests"
# shellcheck disable=SC2016
grep -Fq 'ref: ${{ needs.validate-release-ref.outputs.target_sha }}' <<<"$publish_job" ||
  fail "publishers must build the validated target SHA, not the workflow SHA"
grep -Fq "type=raw,value=latest,enable=" "$WORKFLOW" || fail "historical targets must not move latest"

git init --bare -q "$TMP/origin.git"
git init -q "$TMP/repo"
git -C "$TMP/repo" config user.name test
git -C "$TMP/repo" config user.email test@foldex.invalid
printf 'main\n' >"$TMP/repo/state"
mkdir -p "$TMP/repo/web" "$TMP/repo/extension"
printf '{"version":"1.2.3"}\n' >"$TMP/repo/web/package.json"
printf '{"version":"1.2.3"}\n' >"$TMP/repo/extension/manifest.json"
git -C "$TMP/repo" add state web/package.json extension/manifest.json
git -C "$TMP/repo" commit -qm main
git -C "$TMP/repo" branch -M main
git -C "$TMP/repo" remote add origin "$TMP/origin.git"
git -C "$TMP/repo" push -qu origin main
main_sha=$(git -C "$TMP/repo" rev-parse HEAD)

run_validator() {
  local target=$1 ref=$2 workflow_sha=$3 output=$4
  (
    cd "$TMP/repo"
    GITHUB_OUTPUT="$output" bash "$VALIDATOR" "$target" "$ref" "$workflow_sha"
  )
}

sha_output="$TMP/sha-output"
run_validator "$main_sha" refs/heads/main "$main_sha" "$sha_output"
grep -q "^target_sha=$main_sha$" "$sha_output" || fail "full SHA target was not resolved"
grep -q '^create_tag=false$' "$sha_output" || fail "SHA releases must not create a tag"

tag_output="$TMP/tag-output"
run_validator v1.2.3 refs/heads/main "$main_sha" "$tag_output"
grep -q "^target_sha=$main_sha$" "$tag_output" || fail "new semver tag must target the dispatched main SHA"
grep -q '^create_tag=true$' "$tag_output" || fail "missing semver tag was not scheduled for creation"
grep -q '^publish_latest=true$' "$tag_output" || fail "main-tip semver must publish latest"
if run_validator v9.9.9 refs/heads/main "$main_sha" "$TMP/mismatch-output" >/dev/null 2>&1; then
  fail "release tag was accepted when commit versions did not match"
fi

for invalid in 1.2.3 v1 v1.2 v1.2.3-rc.1 v1.2.3+build v01.2.3 v1.02.3 v1.2.03 "${main_sha:0:12}"; do
  if run_validator "$invalid" refs/heads/main "$main_sha" "$TMP/invalid-output" >/dev/null 2>&1; then
    fail "invalid release target accepted: $invalid"
  fi
done
if run_validator 0000000000000000000000000000000000000000 refs/heads/main "$main_sha" "$TMP/missing-sha-output" >/dev/null 2>&1; then
  fail "nonexistent full SHA was accepted"
fi

for invalid_ref in refs/heads/release refs/tags/v1.2.3; do
  if run_validator "$main_sha" "$invalid_ref" "$main_sha" "$TMP/invalid-ref-output" >/dev/null 2>&1; then
    fail "non-default workflow ref accepted: $invalid_ref"
  fi
done

# Even an ancestor must not pass when a tag selected a historical workflow.
if run_validator "$main_sha" refs/tags/v1.2.3 "$main_sha" "$TMP/historical-output" >/dev/null 2>&1; then
  fail "historical tag workflow path was accepted"
fi

git -C "$TMP/repo" switch -qc unmerged
printf 'unmerged\n' >"$TMP/repo/state"
git -C "$TMP/repo" commit -qam unmerged
unmerged_sha=$(git -C "$TMP/repo" rev-parse HEAD)
if run_validator "$unmerged_sha" refs/heads/main "$main_sha" "$TMP/unmerged-output" >/dev/null 2>&1; then
  fail "release validator accepted a target outside origin/main"
fi

if run_validator "$main_sha" refs/heads/main "$unmerged_sha" "$TMP/unmerged-workflow-output" >/dev/null 2>&1; then
  fail "release validator accepted a workflow SHA outside origin/main"
fi

git -C "$TMP/repo" switch -q main
printf '{"version":"1.2.4"}\n' >"$TMP/repo/web/package.json"
printf '{"version":"1.2.4"}\n' >"$TMP/repo/extension/manifest.json"
git -C "$TMP/repo" commit -qam 'version 1.2.4'
git -C "$TMP/repo" push -qu origin main
release_sha=$(git -C "$TMP/repo" rev-parse HEAD)
historical_output="$TMP/historical-sha-output"
run_validator "$main_sha" refs/heads/main "$release_sha" "$historical_output"
grep -q '^publish_latest=false$' "$historical_output" || fail "historical SHA release would move latest backward"
git -C "$TMP/repo" config user.name 'github-actions[bot]'
git -C "$TMP/repo" config user.email '41898282+github-actions[bot]@users.noreply.github.com'
git -C "$TMP/repo" tag -a v1.2.4 "$release_sha" -m 'Foldex release workflow run 12345'
git -C "$TMP/repo" push -q origin refs/tags/v1.2.4
if run_validator v1.2.4 refs/heads/main "$release_sha" "$TMP/preexisting-output" >/dev/null 2>&1; then
  fail "pre-existing tag with forgeable workflow identity was accepted"
fi

git -C "$TMP/repo" config user.name outsider
git -C "$TMP/repo" config user.email outsider@foldex.invalid
git -C "$TMP/repo" tag -a v1.2.5 "$release_sha" -m 'manual tag'
git -C "$TMP/repo" push -q origin refs/tags/v1.2.5
if run_validator v1.2.5 refs/heads/main "$release_sha" "$TMP/untrusted-output" >/dev/null 2>&1; then
  fail "tag from an unrecognized identity was accepted"
fi

printf '%s\n' "release ref gate contract passed"
