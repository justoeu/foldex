#!/usr/bin/env bash
# scripts/storage-up.sh must succeed when the one-shot bootstrap exits 0 and
# fail — loudly — when it does not.
#
# `docker compose up -d --wait` cannot express the first case: it waits for
# running|healthy, and a container that has finished its job is neither, so it
# reports failure for a bootstrap that worked. That is what took the DAST down
# ("container foldex-rustfs-init exited (0)" → exit 1) after the database was
# fixed, and the identical line was in `make storage-up`.
#
# Driven against a FIXTURE, not docker-compose.services.yml: that file names its
# volumes globally, so there is no such thing as an isolated run of it — a probe
# would mount the operator's real object store. The real file is exercised by
# the DAST on a disposable runner.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURE="$(mktemp -t fx-storage-fixture.XXXXXX.yml)"
PROJECT="fxstorageprobe$$"
fail=0

cleanup() {
  docker compose -f "$FIXTURE" -p "$PROJECT" down -v --remove-orphans >/dev/null 2>&1 || true
  rm -f "$FIXTURE"
}
trap cleanup EXIT

# `rustfs` stands in for the long-running store (healthcheck, stays up);
# `rustfs-init` for the one-shot. Service NAMES must match — they are what
# storage-up.sh addresses.
write_fixture() { # $1 = exit code for the one-shot
  cat >"$FIXTURE" <<YML
services:
  rustfs:
    image: alpine:3
    command: ["sleep", "300"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      retries: 3
  rustfs-init:
    image: alpine:3
    restart: "no"
    command: ["sh", "-c", "echo bootstrapping; exit $1"]
YML
}

run_it() { STORAGE_FILE="$FIXTURE" STORAGE_PROJECT="$PROJECT" bash "$ROOT/scripts/storage-up.sh" 2>&1; }

write_fixture 0
if out=$(run_it); then
  [[ "$out" == *"object store ready"* ]] || { echo "FAIL success path said: $out" >&2; fail=1; }
else
  echo "FAIL a bootstrap that exited 0 was reported as a failure: $out" >&2
  fail=1
fi

docker compose -f "$FIXTURE" -p "$PROJECT" down -v --remove-orphans >/dev/null 2>&1 || true

# The negative case matters as much: without it, a script that always returned 0
# would pass the check above and hide every broken bootstrap behind a green run.
write_fixture 3
if out=$(run_it); then
  echo "FAIL a bootstrap that exited 3 was reported as success" >&2
  fail=1
else
  [[ "$out" == *"exit 3"* ]] || { echo "FAIL failure path did not name the exit code: $out" >&2; fail=1; }
  [[ "$out" == *"bootstrapping"* ]] || { echo "FAIL failure path did not surface the container log" >&2; fail=1; }
fi

docker compose -f "$FIXTURE" -p "$PROJECT" down -v --remove-orphans >/dev/null 2>&1 || true

# Running it twice must still succeed — re-running `make up` is ordinary.
#
# This case does NOT reproduce the `ps -q` race that CI caught, and an earlier
# comment here claimed it did: the second `up -d` restarts the exited one-shot,
# so the id is findable again either way. That race is a genuine race, only
# visible on a machine fast enough to finish the bootstrap before the script
# looks, and no assertion here forces that ordering. What guards it is `ps -aq`
# in storage-up.sh plus the CI run that failed on it; saying otherwise would be
# a test whose comment is stronger than the test.
write_fixture 0
run_it >/dev/null || { echo "FAIL first run failed" >&2; fail=1; }
if out=$(run_it); then
  [[ "$out" == *"object store ready"* ]] || { echo "FAIL second run said: $out" >&2; fail=1; }
else
  echo "FAIL an already-finished bootstrap was reported as not started: $out" >&2
  fail=1
fi

[[ $fail -eq 0 ]] || exit 1
echo "storage-up judges a finished bootstrap by its exit code, before and after it exits"
