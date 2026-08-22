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

# `|| true` on the capture, not decoration: under `set -e` a pipeline whose grep
# matches nothing kills the assignment, so the script exited 1 printing NOTHING
# — a silent failure inside the script whose whole job is to make one loud.
expand_from() { (cd "$1" && make -n migrate-up 2>/dev/null) || true; }

# -v <host>:<container>
mount_host()      { grep -o -- '-v [^ ]*:[^ ]*' <<<"$1" | head -1 | sed 's|^-v ||; s|:[^:]*$||' || true; }
mount_target()    { grep -o -- '-v [^ ]*:[^ ]*' <<<"$1" | head -1 | sed 's|.*:||'              || true; }
path_flag()       { grep -o -- '-path [^ ]*'    <<<"$1" | head -1 | sed 's|^-path ||'          || true; }

root_cmd=$(expand_from "$ROOT")
backend_cmd=$(expand_from "$ROOT/backend")

from_root=$(mount_host "$root_cmd")
from_backend=$(mount_host "$backend_cmd")

if [[ -z "$root_cmd" ]]; then
  note "make -n migrate-up produced nothing from the repo root — the target is gone or does not delegate"
elif [[ -z "$from_root" ]]; then
  note "could not find a -v mount in the expanded command — the recipe changed shape: $root_cmd"
fi

# The mount target and -path must name the SAME directory. They are two
# independent strings that only happen to agree, so a rename of either alone
# leaves migrate pointed at an empty path inside a container that mounted the
# files somewhere else — the same "exit 0, no change" as the original bug, from
# the other end. Mutation testing found this gap: changing -path alone survived
# every earlier assertion.
target=$(mount_target "$root_cmd")
pflag=$(path_flag "$root_cmd")
if [[ -z "$pflag" ]]; then
  note "the expanded command carries no -path flag"
elif [[ "$target" != "$pflag" ]]; then
  note "the mount lands at '$target' but migrate reads '$pflag' — it would find no migrations there"
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
