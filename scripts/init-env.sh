#!/usr/bin/env bash
set -euo pipefail

ENV_FILE=${FOLDEX_ENV_FILE:-.env}
TEMPLATE=${FOLDEX_ENV_TEMPLATE:-.env.example}

umask 077
if [[ ! -f "$ENV_FILE" ]]; then
  cp "$TEMPLATE" "$ENV_FILE"
fi

random_secret() {
  LC_ALL=C od -An -N32 -tx1 /dev/urandom | tr -d ' \n'
}

root_secret=$(random_secret)
app_secret=$(random_secret)
tmp=$(mktemp "${ENV_FILE}.tmp.XXXXXX")
trap 'rm -f "$tmp"' EXIT

while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in
    RUSTFS_ROOT_SECRET_KEY=|RUSTFS_ROOT_SECRET_KEY=rustfsadmin)
      printf 'RUSTFS_ROOT_SECRET_KEY=%s\n' "$root_secret"
      ;;
    RUSTFS_SECRET_KEY=|RUSTFS_SECRET_KEY=foldex-change-me)
      printf 'RUSTFS_SECRET_KEY=%s\n' "$app_secret"
      ;;
    *)
      printf '%s\n' "$line"
      ;;
  esac
done <"$ENV_FILE" >"$tmp"

chmod 600 "$tmp"
mv "$tmp" "$ENV_FILE"
trap - EXIT
