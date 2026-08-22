#!/usr/bin/env bash
# The migrate mount path must be the same from either entry point.
#
# `make migrate-up` from the repo root delegates with `$(MAKE) -C backend`, and
# `make -C` does NOT update the PWD environment variable — only CURDIR. So a
# recipe written with $(PWD) resolved to <root>/db/migrations, a directory that
# does not exist. Docker creates a missing bind-mount source as an empty
# directory, golang-migrate then finds zero files and exits 0 with "no change".
#
# The failure is therefore silent AND self-cancelling: the backend refuses to
# boot against an un-migrated database (db.CheckSchemaVersion) and tells the
# operator to run `make migrate-up` — which is the command that just did
# nothing. README.md line 59 puts that command in the quickstart, so this was
# the first thing a new install was told to do.
#
# Asserted on the EXPANSION (`make -n`), not by running docker: the defect is in
# the path, and expanding it is both exact and free.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0
note() { echo "FAIL $*" >&2; fail=1; }

mount_path_from() {
  # -v <host>:<container> — take the host side of the /migrations mount.
  (cd "$1" && make -n migrate-up 2>/dev/null) \
    | grep -o -- '-v [^ ]*:/migrations' | head -1 | sed 's|^-v ||; s|:/migrations$||'
}

from_root=$(mount_path_from "$ROOT")
from_backend=$(mount_path_from "$ROOT/backend")

if [[ -z "$from_root" ]]; then
  note "could not expand the migrate mount from the repo root — the recipe changed shape"
fi
if [[ "$from_root" != "$from_backend" ]]; then
  note "entry point decides the migrations directory: root gives '$from_root', backend gives '$from_backend'"
fi
# The stronger half. Equal-and-both-wrong would pass the check above, and a
# non-existent source is exactly what Docker papers over with an empty dir.
if [[ ! -d "$from_root" ]]; then
  note "'$from_root' does not exist — docker would create it empty and migrate would report 'no change'"
fi
if [[ -d "$from_root" ]] && ! compgen -G "$from_root/*.up.sql" >/dev/null; then
  note "'$from_root' holds no .up.sql files — it is not the migrations directory"
fi

[[ $fail -eq 0 ]] || exit 1
echo "make migrate-up mounts $from_root from either entry point"
