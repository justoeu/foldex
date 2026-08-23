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

# ── The HOST the migrate container dials ────────────────────────────────────
#
# `POSTGRES_HOST=localhost` is supported: compose aliases `localhost` to the
# host gateway for the backend, so a Postgres on the developer's own machine
# works. The migrate CLI runs in its own container with no such alias, where
# `localhost` is the container itself — so `make migrate-up` failed with
# "connection refused" on an instance whose backend was connected and healthy,
# and the operator's next move (per README) was the command that had just
# failed.
#
# `--add-host=localhost:host-gateway` does not fix it — Docker appends to
# /etc/hosts and the pre-existing `127.0.0.1 localhost` line wins the lookup —
# so the URL is rewritten instead. Both halves are asserted: the rewrite HAPPENS
# for a local host, and it does NOT happen for any other, or a genuinely remote
# Postgres would be redirected at the developer's own machine.
db_flag() { sed -n 's/.*-database "\([^"]*\)".*/\1/p' <<<"$1"; }
# NEVER echo a DSN. The first version of this guard printed the expansion on
# failure and put the instance's real Postgres password on the terminal — the
# same defect logsafe exists to prevent, in the one file whose job is to read
# the expansion. Both probes therefore use a SYNTHETIC DB_URL passed on the
# command line (which outranks .env), so no real credential is ever expanded,
# and the messages name the host only.
host_of() { sed -E 's#^[^@]*@([^/:]+).*#\1#' <<<"$1"; }
expand_db() { (cd "$ROOT/backend" && make -n migrate-up DB_URL="$1" 2>/dev/null | { read -r l; db_flag "$l"; }); }

FAKE_LOCAL='postgres://u:p@localhost:5432/x?sslmode=disable'
FAKE_REMOTE='postgres://u:p@db.example.internal:5432/x?sslmode=disable'

got=$(host_of "$(expand_db "$FAKE_LOCAL")")
if [[ "$got" == "localhost" || "$got" == "127.0.0.1" ]]; then
  note "a local POSTGRES_HOST reaches the migrate container unrewritten; inside it that is the container itself, and migrate dials nothing"
elif [[ "$got" != "host.docker.internal" ]]; then
  note "a local host did not become host.docker.internal (got host '$got')"
fi

got=$(host_of "$(expand_db "$FAKE_REMOTE")")
if [[ "$got" != "db.example.internal" ]]; then
  note "a non-local host must pass through untouched, or a remote Postgres is silently redirected at the developer's machine (got host '$got')"
fi

if [[ "$root_cmd" != *"--add-host=host.docker.internal:host-gateway"* ]]; then
  note "the migrate container has no host.docker.internal alias, so the rewritten name resolves to nothing"
fi

[[ $fail -eq 0 ]] || exit 1
echo "make migrate-up mounts $from_root and dials the right host from either entry point"
