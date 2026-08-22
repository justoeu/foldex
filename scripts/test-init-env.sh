#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
INIT="$ROOT/scripts/init-env.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

grep -qx 'RUSTFS_ROOT_SECRET_KEY=' "$ROOT/.env.example"
grep -qx 'RUSTFS_SECRET_KEY=' "$ROOT/.env.example"
# The object store lives in docker-compose.services.yml, not in the app file.
# These two assertions are the ONLY thing in the tree stopping someone from
# giving RUSTFS_ROOT_SECRET_KEY the treatment RUSTFS_ROOT_ACCESS_KEY already
# has (${RUSTFS_ROOT_ACCESS_KEY:-rustfsadmin}) — validateSecureDefaults refuses
# the placeholder on the BACKEND's credential and has no visibility at all into
# the credential the RustFS server itself boots with. If this file moves again,
# repoint these; deleting them is how a predictable root password ships.
grep -Fq "RUSTFS_ROOT_SECRET_KEY: \${RUSTFS_ROOT_SECRET_KEY:?" "$ROOT/docker-compose.services.yml"
grep -Fq "RUSTFS_SECRET_KEY: \${RUSTFS_SECRET_KEY:?" "$ROOT/docker-compose.services.yml"
# The APP's copy is deliberately relaxed: the backend treats the object store as
# optional and degrades, so a `:?` there refused the whole compose file — mailer
# and web included — over a credential neither reads. Asserted so the relaxation
# stays legible as a decision instead of reading like an omission.
grep -Fq 'RUSTFS_SECRET_KEY: ${RUSTFS_SECRET_KEY:-}' "$ROOT/docker-compose.yml"
grep -Fq 'bash scripts/init-env.sh' "$ROOT/Makefile"

cat >"$TMP/template" <<'EOF'
RUSTFS_ROOT_ACCESS_KEY=rustfsadmin
RUSTFS_ROOT_SECRET_KEY=rustfsadmin
RUSTFS_ACCESS_KEY=foldex
RUSTFS_SECRET_KEY=foldex-change-me
UNCHANGED=value
EOF

output=$(FOLDEX_ENV_FILE="$TMP/.env" FOLDEX_ENV_TEMPLATE="$TMP/template" bash "$INIT" 2>&1)
[[ -z "$output" ]]

root_secret=$(awk -F= '$1 == "RUSTFS_ROOT_SECRET_KEY" { print $2 }' "$TMP/.env")
app_secret=$(awk -F= '$1 == "RUSTFS_SECRET_KEY" { print $2 }' "$TMP/.env")

[[ "$root_secret" =~ ^[a-f0-9]{64}$ ]]
[[ "$app_secret" =~ ^[a-f0-9]{64}$ ]]
[[ "$root_secret" != "$app_secret" ]]
[[ "$root_secret" != rustfsadmin ]]
[[ "$app_secret" != foldex-change-me ]]
grep -qx 'UNCHANGED=value' "$TMP/.env"
mode=$(stat -c '%a' "$TMP/.env" 2>/dev/null || stat -f '%Lp' "$TMP/.env")
[[ "$mode" == 600 ]]

cp "$TMP/.env" "$TMP/before"
output=$(FOLDEX_ENV_FILE="$TMP/.env" FOLDEX_ENV_TEMPLATE="$TMP/template" bash "$INIT" 2>&1)
[[ -z "$output" ]]
cmp "$TMP/before" "$TMP/.env"

echo "init-env credential generation contract passed"
