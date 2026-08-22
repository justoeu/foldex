#!/usr/bin/env bash
# Boots the REAL nginx config and asserts the security headers by MAKING THE
# REQUESTS — names, values, and on responses that are not 200.
#
# Why it exists: nginx inherits `add_header` from the enclosing level only while
# the inner level declares none of its own, so one `add_header` inside a
# location silently discards every header above it. Three locations did exactly
# that, and because `location /` reaches index.html through try_files' internal
# redirect, the SPA's own document shipped with 0 of 6 while `/assets/*.js`
# carried 6 of 6 — backwards, since CSP and frame-ancestors govern the document.
# The defect landed on 2026-05-15 inside a service-worker CACHE fix, survived a
# green DAST scan a month later (the ZAP baseline is informational), and lived
# three months. No linter sees it.
#
# The first version of this script checked only header NAMES, on seven paths
# that were all static 200s. Three mutations survived it: stripping `always`
# (every 4xx/5xx loses all six), neutering every VALUE (CSP to `default-src *`,
# X-Frame-Options to ALLOWALL, HSTS to max-age=0 — all still "present"), and
# re-adding an `add_header` to `location /api/` with two-space indentation,
# because the source guard checked indentation rather than nesting. All three
# are covered below.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
# Digest-pinned: CLAUDE.md §4 requires it of every CI image, and nginx's header
# inheritance is exactly the kind of behaviour a silent tag swap could change.
IMAGE=nginx:1.31-alpine@sha256:db35bfc6b2951e7f8a72db5db120288c127ffaeeb4a6d4b95a26fead017d5913
# Per-process name and kernel-assigned ports: a fixed name let one run's cleanup
# destroy a concurrent run's container, and the pre-push gate means developers
# run this beside `make up`.
NAME=foldex-nginx-header-test-$$
PUBLIC_HOST=foldex-header-test.invalid

TMP=$(mktemp -d)
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$TMP"; }
trap cleanup EXIT

fail=0
note() { echo "FAIL $*"; fail=1; }

mkdir -p "$TMP/html/assets" "$TMP/certs" "$TMP/conf.d"
printf '<!doctype html><title>foldex</title>\n' > "$TMP/html/index.html"
printf '// sw\n'            > "$TMP/html/sw.js"
printf '// registerSW\n'    > "$TMP/html/registerSW.js"
printf '// asset\n'         > "$TMP/html/assets/app.js"
printf '{"name":"foldex"}\n' > "$TMP/html/manifest.webmanifest"

openssl req -x509 -newkey rsa:2048 -nodes -keyout "$TMP/certs/key.pem" \
  -out "$TMP/certs/cert.pem" -days 1 -subj "/CN=localhost" >/dev/null 2>&1

cp "$ROOT/web/nginx.conf" "$TMP/conf.d/default.conf"
# Run the SHIPPED substitution rather than a hand-rolled sed, so the config
# under test is the one the container actually produces.
# `|| true`: the substitution reports 1 when the placeholder is absent, and
# under `set -e` that killed the run with no output — the exact silent-failure
# shape this script is supposed to replace. The assertion below is what judges
# the outcome.
( cd "$TMP/conf.d" && WEB_PUBLIC_HOST="$PUBLIC_HOST" \
    sh -c 'f=default.conf; grep -q "__WEB_PUBLIC_HOST__" "$f" && sed "s/__WEB_PUBLIC_HOST__/$WEB_PUBLIC_HOST/g" "$f" > "$f.tmp" && mv "$f.tmp" "$f"' ) || true
if grep -q "__WEB_PUBLIC_HOST__" "$TMP/conf.d/default.conf"; then
  note "host placeholder was not substituted — web/entrypoint-certs.sh's contract changed"
  exit 1
fi
if ! grep -q "$PUBLIC_HOST" "$TMP/conf.d/default.conf"; then
  note "the baked public host is absent from the config — the redirect no longer uses it"
  exit 1
fi

# `backend` must RESOLVE or nginx refuses to start — proxy_pass with a literal
# hostname resolves once at boot. Pointing it at loopback with nothing listening
# is deliberate: the proxied locations then answer 502, which is how this script
# exercises `always` and the proxied locations at the same time.
docker run --rm -d --name "$NAME" -p 127.0.0.1:0:8443 -p 127.0.0.1:0:8080 \
  --add-host backend:127.0.0.1 \
  -v "$ROOT/web/nginx.main.conf:/etc/nginx/nginx.conf:ro" \
  -v "$TMP/conf.d/default.conf:/etc/nginx/conf.d/default.conf:ro" \
  -v "$TMP/html:/usr/share/nginx/html:ro" \
  -v "$TMP/certs:/etc/nginx/certs:ro" \
  "$IMAGE" >/dev/null

TLS_PORT=$(docker port "$NAME" 8443 | head -1 | sed 's/.*://')
HTTP_PORT=$(docker port "$NAME" 8080 | head -1 | sed 's/.*://')
BASE="https://127.0.0.1:$TLS_PORT"

ready=0
for _ in $(seq 1 60); do
  if curl -sk -o /dev/null "$BASE/" 2>/dev/null; then ready=1; break; fi
  sleep 0.5
done
if [[ $ready -eq 0 ]]; then
  # Without this, a config typo or a registry blip presents as a bare non-zero
  # exit with an empty log, under a step named "security headers survive".
  echo "FAIL nginx never became ready — this is an infrastructure failure, not a header regression"
  docker logs "$NAME" 2>&1 | tail -30
  exit 1
fi

# name -> a substring that carries the SECURITY MEANING. Asserting the name
# alone accepts `max-age=0`, `ALLOWALL` and `default-src *`: a header that looks
# configured and protects nothing, with a green step stopping anyone from
# re-reading it.
REQUIRED_NAMES=(
  strict-transport-security x-frame-options x-content-type-options
  referrer-policy permissions-policy content-security-policy
)
expected_for() {
  case $1 in
    strict-transport-security) echo "max-age=31536000" ;;
    x-frame-options)           echo "deny" ;;
    x-content-type-options)    echo "nosniff" ;;
    referrer-policy)           echo "no-referrer" ;;
    permissions-policy)        echo "camera=()" ;;
    content-security-policy)   echo "frame-ancestors 'none'" ;;
  esac
}

check_headers() {
  local path=$1 headers exp
  headers=$(curl -sk -D - -o /dev/null "$BASE$path" 2>/dev/null | tr 'A-Z' 'a-z') || {
    note "$path could not be requested"; return
  }
  for h in "${REQUIRED_NAMES[@]}"; do
    exp=$(expected_for "$h")
    grep -q "^$h:" <<<"$headers" || { note "$path is missing $h"; continue; }
    grep -q "^$h:.*$exp" <<<"$headers" || note "$path has $h but not '$exp' — present and useless"
  done
  # The one CSP property CLAUDE.md §4 states outright.
  if grep -q "^content-security-policy:" <<<"$headers"; then
    grep -qE "^content-security-policy:.*script-src 'self'[;,]" <<<"$headers" \
      || note "$path: script-src is not exactly 'self'"
    if grep -oE "script-src[^;]*" <<<"$headers" | grep -q "unsafe-"; then
      note "$path: script-src allows unsafe- — style-src is the ONLY place that may"
    fi
  fi
}

# Static 200s, plus the PROXIED locations. Nothing listens on the backend, so
# those answer 502 — which is the only way this script observes a non-2xx and
# therefore the only way `always` is under test at all.
for p in / /index.html /sw.js /registerSW.js /assets/app.js /manifest.webmanifest \
         /api/anything /go/x /n/x /healthz; do
  check_headers "$p"
done

# Assert the 502 really happened, or the loop above proved nothing about errors.
code=$(curl -sk -o /dev/null -w '%{http_code}' "$BASE/api/anything" 2>/dev/null || echo 000)
[[ $code == 502 ]] || note "/api/anything answered $code, expected 502 — the non-2xx coverage above is vacuous"

# The map's own job. Exact value, not a substring: `no-store` alone would pass a
# looser check while the documented three-token value quietly shrank.
for p in / /index.html /sw.js /registerSW.js; do
  curl -sk -D - -o /dev/null "$BASE$p" 2>/dev/null | tr 'A-Z' 'a-z' \
    | grep -q '^cache-control: no-cache, no-store, must-revalidate' \
    || note "$p must carry the full no-store Cache-Control"
done
# A revisioned asset must NOT be pinned — the empty map value is what leaves
# default caching alone.
if curl -sk -D - -o /dev/null "$BASE/assets/app.js" 2>/dev/null | tr 'A-Z' 'a-z' \
   | grep -q '^cache-control:'; then
  note "/assets/app.js should keep default caching, not a Cache-Control"
fi

# The :8080 redirect must target the BAKED host, never the request's. The
# config's own comment forbids the request-derived host variables (host-header
# injection), and nothing else in the repo exercised it. Those variable names
# are deliberately NOT spelled out here: Semgrep's nginx request-host-used rule
# is a generic text match, so writing them even in prose raises an alert whose
# only evidence is this sentence — and a scanner that cries wolf is the reason
# the last three months of DAST reports went unread.
loc=$(curl -s -D - -o /dev/null -H "Host: evil.example" "http://127.0.0.1:$HTTP_PORT/x" 2>/dev/null \
      | tr -d '\r' | grep -i '^location:' | awk '{print $2}')
[[ "$loc" == "https://$PUBLIC_HOST/x" ]] \
  || note "the HTTP redirect went to '$loc' — it must ignore the request Host header"

# Plain HTTP on the TLS port must redirect (497 → 301), not dead-end on
# nginx's default 400 — a browser given a bare `host:9444` assumes http://
# and this is the page it lands on. Same rules as the :8080 redirect: baked
# host (the request's is attacker-controlled) and the path preserved.
plain_on_tls=$(curl -s -D - -o /dev/null -H "Host: evil.example" \
      "http://127.0.0.1:$TLS_PORT/some/path?q=1" 2>/dev/null | tr -d '\r')
code=$(head -1 <<<"$plain_on_tls" | awk '{print $2}')
loc=$(grep -i '^location:' <<<"$plain_on_tls" | awk '{print $2}')
[[ "$code" == "301" ]] \
  || note "plain HTTP on the TLS port answered $code — expected the 497→301 redirect, not nginx's dead-end 400"
[[ "$loc" == "https://$PUBLIC_HOST/some/path?q=1" ]] \
  || note "the 497 redirect went to '$loc' — it must use the baked host and keep the path"

# Structural, not typographic: brace depth, so a two-space or column-0
# add_header inside a location is caught the same as a four-space one. The
# previous guard checked indentation and missed all three of those shapes.
awk '
  { line = $0; sub(/#.*/, "", line) }
  line ~ /add_header/ && depth > 1 {
    printf "FAIL %s:%d declares add_header at nesting depth %d (inside a location) — that drops every inherited header\n", FILENAME, FNR, depth
    bad = 1
  }
  { depth += gsub(/{/, "{"); depth -= gsub(/}/, "}") }
  END { exit bad }
' "$ROOT/web/nginx.conf" || fail=1

[[ $fail -eq 0 ]] || exit 1
echo "nginx security headers survive on every response, with their values intact"
