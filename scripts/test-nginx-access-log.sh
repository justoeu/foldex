#!/bin/sh
set -eu

config="${1:-web/nginx.main.conf}"

# shellcheck disable=SC2016
if ! grep -Fq '$request_method $uri $server_protocol' "$config"; then
  printf '%s\n' 'nginx access log must use the normalized request path' >&2
  exit 1
fi

# shellcheck disable=SC2016
for forbidden in '"$request"' '$args' '$query_string' '$http_referer'; do
  if grep -Fq "$forbidden" "$config"; then
    printf 'nginx access log contains forbidden field: %s\n' "$forbidden" >&2
    exit 1
  fi
done
