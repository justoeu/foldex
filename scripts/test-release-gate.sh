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
# flavor latest=auto would mint :latest on every semver cell, duplicating the
# explicit raw latest (gated on publish_latest) and applying it when the
# target is no longer the tip of main. latest=false leaves that knob as the
# only source. onlatest=true is load-bearing for the backup-agent cell:
# without it the suffix skips latest and a bare :latest from that cell
# would overwrite the backend's moving tag.
grep -Eq '^[[:space:]]+latest=false$' <<<"$manifest_job" || fail "flavor must not auto-add latest; publish_latest is the only source"
grep -Fq 'onlatest=true' <<<"$manifest_job" || fail "suffix must apply to latest or backup-agent would stamp the backend tag"
grep -Fq 'suffix: -backup-agent' <<<"$manifest_job" || fail "backup-agent matrix cell must declare its tag suffix"

# backup-agent publishes :sha-<short>-backup-agent into the SAME repository
# as backend. Inspecting the unsuffixed :sha-<short> looks up the backend
# tag — which the sibling cell may not have written yet, and is the wrong
# image even when it has. Both halves are the contract: IMAGE_REF must
# carry the suffix, and inspect must consume IMAGE_REF rather than a
# second unsuffixed interpolation in run:.
# shellcheck disable=SC2016
grep -Fq 'IMAGE_REF: ${{ matrix.image.repo }}:sha-${{ needs.validate-release-ref.outputs.short_sha }}${{ matrix.image.suffix || '"''"' }}' <<<"$manifest_job" ||
  fail "inspect must look up sha-<short> WITH the cell suffix, not the unsuffixed backend tag"
# shellcheck disable=SC2016
grep -Fq 'imagetools inspect "$IMAGE_REF"' <<<"$manifest_job" ||
  fail "inspect must consume IMAGE_REF; interpolating the unsuffixed sha- tag into run: is the production failure"
if grep -Fq 'imagetools inspect ${{ matrix.image.repo }}:sha-' <<<"$manifest_job"; then
  fail "inspect still interpolates the unsuffixed sha- tag into run:"
fi

# A job-level concurrency group that does not include the matrix key is
# shared by every cell of the same run. GitHub then cancels previously
# pending siblings when the next cell queues ("Canceling since a higher
# priority waiting request for <group> exists"). Per-image keeps the
# cross-run serialization of :latest without the three cells of one run
# fighting each other.
# shellcheck disable=SC2016
grep -Fq 'github.workflow }}-manifest-${{ matrix.image.name }}' <<<"$manifest_job" ||
  fail "publish-manifest concurrency must be per image, not one group for the whole matrix"

# write_compose builds the fixture. The optional third argument adds a second
# service running the backend image, which is what the real file looks like:
# the mailer is the backend binary with a different entrypoint. Callers that
# omit it get the single-service shape the older cases assert against.
#
# `-` in a version slot omits that service entirely. That is how the "at least
# one of each" half of the predicate gets exercised against THIS gate — the one
# that decides publication — rather than only against release.sh's private copy
# of the same awk. The unrelated `db` line keeps the services map non-empty in
# that case and doubles as a check that a foreign image is never counted.
write_compose() {
  local backend_version=$1 web_version=$2 mailer_version=${3:-}
  {
    printf 'services:\n'
    if [[ "$backend_version" != - ]]; then
      # shellcheck disable=SC2016
      printf '  backend:\n    image: justoeu/foldex-backend:${FOLDEX_VERSION:-%s}\n' \
        "$backend_version"
    fi
    if [[ -n "$mailer_version" && "$mailer_version" != - ]]; then
      # shellcheck disable=SC2016
      printf '  mailer:\n    image: justoeu/foldex-backend:${FOLDEX_VERSION:-%s}\n' \
        "$mailer_version"
    fi
    if [[ "$web_version" != - ]]; then
      # shellcheck disable=SC2016
      printf '  web:\n    image: justoeu/foldex-web:${FOLDEX_VERSION:-%s}\n' "$web_version"
    fi
    printf '  db:\n    image: postgres:18.4-alpine\n'
  } >"$TMP/repo/docker-compose.yml"
}

# commit_compose publishes the current fixture and echoes its SHA, so a case
# can hand the validator a commit that origin/main actually contains.
commit_compose() {
  git -C "$TMP/repo" commit -qam "$1"
  git -C "$TMP/repo" push -qu origin main
  git -C "$TMP/repo" rev-parse HEAD
}

git init --bare -q "$TMP/origin.git"
git init -q "$TMP/repo"
git -C "$TMP/repo" config user.name test
git -C "$TMP/repo" config user.email test@foldex.invalid
printf 'main\n' >"$TMP/repo/state"
mkdir -p "$TMP/repo/web" "$TMP/repo/extension"
printf '{"version":"1.2.3"}\n' >"$TMP/repo/web/package.json"
printf '{"version":"1.2.3"}\n' >"$TMP/repo/extension/manifest.json"
write_compose 1.2.3 1.2.3
git -C "$TMP/repo" add state web/package.json extension/manifest.json docker-compose.yml
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

for image in backend web; do
  if [[ "$image" == backend ]]; then
    write_compose 9.9.9 1.2.3
  else
    write_compose 1.2.3 9.9.9
  fi
  git -C "$TMP/repo" commit -qam "$image Compose mismatch"
  git -C "$TMP/repo" push -qu origin main
  mismatch_sha=$(git -C "$TMP/repo" rev-parse HEAD)
  if run_validator v1.2.3 refs/heads/main "$mismatch_sha" "$TMP/$image-compose-mismatch-output" >/dev/null 2>&1; then
    fail "release tag was accepted when the $image Compose default did not match"
  fi
  write_compose 1.2.3 1.2.3
  git -C "$TMP/repo" commit -qam "restore $image Compose version"
  git -C "$TMP/repo" push -qu origin main
done

# A service reusing the backend image must not read as drift. The gate used to
# require EXACTLY one line per image, so the mailer landing in docker-compose.yml
# made the validator refuse every tag — a publish gate blocking on a file with
# nothing wrong in it. What must hold is that EVERY matching line carries the
# release version, whatever their count.
#
# This case lives here rather than beside the release.sh cases because this is
# the harness that RUNS the validator. Asserting the rule against the script's
# source text would stay green while the gate itself was broken.
reused_sha=$(write_compose 1.2.3 1.2.3 1.2.3; commit_compose "mailer reuses the backend image")
run_validator v1.2.3 refs/heads/main "$reused_sha" "$TMP/reused-output" ||
  fail "release tag was refused when a second service reused the backend image"
grep -q "^target_sha=$reused_sha$" "$TMP/reused-output" ||
  fail "reused-image commit was not resolved as the release target"

stale_sha=$(write_compose 1.2.3 1.2.3 1.2.2; commit_compose "mailer left on an older version")
if run_validator v1.2.3 refs/heads/main "$stale_sha" "$TMP/stale-output" >/dev/null 2>&1; then
  fail "release tag was accepted when a reused backend line stayed on an older version"
fi

# The other half of the relaxed predicate: "at least one of each". Nothing was
# asserting it HERE, and that is the half whose failure is worst — a validator
# simplified to `exit !(!bad)` would publish a tag whose docker-compose.yml
# pins no foldex image at all, so the stack the operator pulls does not come up.
# Both mutants (`exit !(!bad)`, and dropping either count) survive every other
# case in both suites, so these two are the only thing standing behind them.
nobackend_sha=$(write_compose - 1.2.3; commit_compose "compose without the backend image")
if run_validator v1.2.3 refs/heads/main "$nobackend_sha" "$TMP/nobackend-output" >/dev/null 2>&1; then
  fail "release tag was accepted when no backend image was pinned at all"
fi

noweb_sha=$(write_compose 1.2.3 -; commit_compose "compose without the web image")
if run_validator v1.2.3 refs/heads/main "$noweb_sha" "$TMP/noweb-output" >/dev/null 2>&1; then
  fail "release tag was accepted when no web image was pinned at all"
fi

# Shapes the old prefix matcher could not see, asserted against the publish
# gate rather than only against release.sh. Each one used to pass in silence.
raw_compose() { printf '%s' "$1" >"$TMP/repo/docker-compose.yml"; }

decoy_sha=$(raw_compose 'services:
  backend:
    image: attacker/evil:latest
  decoy:
    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.2.3}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.2.3}
'; commit_compose "decoy service carries the pinned line")
if run_validator v1.2.3 refs/heads/main "$decoy_sha" "$TMP/decoy-output" >/dev/null 2>&1; then
  fail "release tag was accepted when the backend service ran a foreign image"
fi

quoted_sha=$(raw_compose 'services:
  backend:
    image: "justoeu/foldex-backend:${FOLDEX_VERSION:-1.0.9}"
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.2.3}
'; commit_compose "quoted value left on an older version")
if run_validator v1.2.3 refs/heads/main "$quoted_sha" "$TMP/quoted-output" >/dev/null 2>&1; then
  fail "release tag was accepted when a quoted image line was stale"
fi

registry_sha=$(raw_compose 'services:
  backend:
    image: docker.io/justoeu/foldex-backend:${FOLDEX_VERSION:-1.2.3}
  web:
    image: justoeu/foldex-web:${FOLDEX_VERSION:-1.2.3}
'; commit_compose "registry-prefixed reference")
if run_validator v1.2.3 refs/heads/main "$registry_sha" "$TMP/registry-output" >/dev/null 2>&1; then
  fail "release tag was accepted with an image reference the gate cannot verify"
fi

# A second Compose file is gated too, and does not have to define the services.
extra_sha=$({ write_compose 1.2.3 1.2.3
  # shellcheck disable=SC2016
  printf 'services:\n  worker:\n    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.0.9}\n' \
    >"$TMP/repo/docker-compose.extra.yml"
  git -C "$TMP/repo" add docker-compose.extra.yml
  commit_compose "a second compose file left behind"; })
if run_validator v1.2.3 refs/heads/main "$extra_sha" "$TMP/extra-output" >/dev/null 2>&1; then
  fail "release tag was accepted when a secondary Compose file was stale"
fi
# shellcheck disable=SC2016
printf 'services:\n  worker:\n    image: justoeu/foldex-backend:${FOLDEX_VERSION:-1.2.3}\n' \
  >"$TMP/repo/docker-compose.extra.yml"
extra_ok_sha=$(commit_compose "second compose file pinned")
run_validator v1.2.3 refs/heads/main "$extra_ok_sha" "$TMP/extra-ok-output" ||
  fail "release tag was refused when every Compose file was correctly pinned"
git -C "$TMP/repo" rm -q docker-compose.extra.yml

write_compose 1.2.3 1.2.3
git -C "$TMP/repo" commit -qam "restore single-service Compose"
git -C "$TMP/repo" push -qu origin main

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
write_compose 1.2.4 1.2.4
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
