#!/usr/bin/env bash
# Bring the bundled object store up and do not return until its bucket exists.
#
# rustfs-init is a ONE-SHOT: it provisions the bucket and the app IAM user, then
# exits. `docker compose up -d --wait` calls that a FAILURE — it waits for
# running|healthy, and a container that has finished is neither. The CI log says
# it plainly: "container foldex-rustfs-init exited (0)" followed by exit 1.
#
# That line lived in two places (`make storage-up` and the DAST workflow), which
# is why it is one script now. Waiting for the bootstrap to EXIT 0 is also
# strictly stronger than waiting for it to be running, and the guarantee is the
# point: the store moved out of docker-compose.yml, `depends_on` cannot cross
# compose projects, and storage.New does a single BucketExists at boot with no
# retry — lose that race and screenshots are off and /api/backup/* is unmounted
# for the whole process lifetime, on a stack that otherwise looks healthy.
#
# STORAGE_FILE / STORAGE_PROJECT let the guard drive THIS script against a
# fixture instead of testing a copy of its logic. It cannot drive the real file:
# docker-compose.services.yml names its volumes globally (`name: foldex_*`), so
# a "isolated" project would still mount the operator's actual object store —
# discovered the hard way, when a probe's `down -v` was one compose safety check
# away from deleting it. The real file is proven by the DAST, on a disposable
# runner; what runs at PR time is the branch logic below.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
compose=(docker compose -f "${STORAGE_FILE:-$ROOT/docker-compose.services.yml}")
[[ -n "${STORAGE_PROJECT:-}" ]] && compose+=(-p "$STORAGE_PROJECT")

"${compose[@]}" up -d --wait rustfs

"${compose[@]}" up -d rustfs-init
cid=$("${compose[@]}" ps -q rustfs-init)
if [[ -z "$cid" ]]; then
  echo "rustfs-init did not start" >&2
  exit 1
fi

# Polled rather than `docker wait`, which blocks forever on a hung bootstrap.
# `timeout` is not portable to a stock macOS, and this runs on both.
for _ in $(seq 1 60); do
  status=$(docker inspect "$cid" --format '{{.State.Status}}' 2>/dev/null || echo missing)
  if [[ "$status" == "exited" || "$status" == "dead" ]]; then
    code=$(docker inspect "$cid" --format '{{.State.ExitCode}}' 2>/dev/null || echo 1)
    if [[ "$code" != "0" ]]; then
      echo "rustfs bootstrap failed (exit $code)" >&2
      docker logs "$cid" 2>&1 | tail -20 >&2
      exit 1
    fi
    echo "object store ready"
    exit 0
  fi
  sleep 2
done

echo "rustfs bootstrap did not finish in 120s" >&2
docker logs "$cid" 2>&1 | tail -20 >&2
exit 1
