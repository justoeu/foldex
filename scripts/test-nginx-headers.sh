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
         /api/anything /api/auth/login /api/auth/me /go/x /n/x /healthz; do
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
# `|| true`: with no Location header, grep's exit 1 under pipefail would kill
# the script BEFORE the note below — a bare non-zero with an empty log, the
# silent-failure shape this script exists to prevent. The [[ ]] is the judge.
loc=$(curl -s -D - -o /dev/null -H "Host: evil.example" "http://127.0.0.1:$HTTP_PORT/x?q=1" 2>/dev/null \
      | tr -d '\r' | grep -i '^location:' | awk '{print $2}' || true)
[[ "$loc" == "https://$PUBLIC_HOST/x?q=1" ]] \
  || note "the HTTP redirect went to '$loc' — it must ignore the request Host header and keep the query"

# Plain HTTP on the TLS port must redirect (497 → 301), not dead-end on
# nginx's default 400 — a browser given a bare `host:9444` assumes http://
# and this is the page it lands on. Same rules as the :8080 redirect: baked
# host (the request's is attacker-controlled) and the path preserved.
# `|| true` on both: the old-config 400 carries no Location, and grep's exit 1
# under pipefail would abort the script before either note fired — verified:
# that was a bare exit 1 with an empty log. The [[ ]] below is the judge.
plain_on_tls=$(curl -s -D - -o /dev/null -H "Host: evil.example" \
      "http://127.0.0.1:$TLS_PORT/some/path?q=1" 2>/dev/null | tr -d '\r' || true)
code=$(head -1 <<<"$plain_on_tls" | awk '{print $2}' || true)
loc=$(grep -i '^location:' <<<"$plain_on_tls" | awk '{print $2}' || true)
[[ "$code" == "301" ]] \
  || note "plain HTTP on the TLS port answered $code — expected the 497→301 redirect, not nginx's dead-end 400"
[[ "$loc" == "https://$PUBLIC_HOST/some/path?q=1" ]] \
  || note "the 497 redirect went to '$loc' — it must use the baked host and keep the path"

# ── Rate limiting (docs/SDD-ABUSE-DEFENSE.md §4.4) ─────────────────────────
#
# Asserted by MAKING the requests, for the same reason the headers are: a
# limit_req that never fires and a limit_req that is absent produce identical
# config files and identical logs. The three checks below are one property each
# and none of them is implied by the others.
#
# Requests are fired in PARALLEL. Sequentially, a slow curl lets the bucket
# refill between calls (2r/s means one token every 500 ms), and the test would
# pass on an instance where the zone does nothing — the exact vacuous-green the
# header half of this script already learned to avoid.
burst_codes() {
  local path=$1 n=$2 i pids=() out
  out=$(mktemp -d)
  for ((i=0;i<n;i++)); do
    ( curl -sk -o /dev/null -w '%{http_code}\n' "$BASE$path" 2>/dev/null || echo 000 ) > "$out/$i" &
    pids+=($!)
  done
  wait "${pids[@]}" 2>/dev/null || true
  cat "$out"/* 2>/dev/null
  rm -rf "$out"
}

# 1. The credential zone bites. Nothing listens upstream, so the requests that
#    get through answer 502; the ones the zone rejects answer 429, and a run
#    with no 429 at all means the limit is decorative.
login_codes=$(burst_codes /api/auth/login 60)
throttled=$(grep -c '^429$' <<<"$login_codes" || true)
[[ ${throttled:-0} -gt 0 ]] \
  || note "60 parallel POSTs to /api/auth/login produced no 429 — the fx_login zone is not limiting anything"

# 2. The 429 carries the six headers. This is a response class that did not
#    exist before rate limiting, and it is generated by nginx itself rather than
#    proxied — precisely the shape that loses inherited headers when `always` is
#    missing. A throttled login page with no CSP would be a regression created
#    BY the defence.
if [[ ${throttled:-0} -gt 0 ]]; then
  rl_headers=$(curl -sk -D - -o /dev/null "$BASE/api/auth/login" 2>/dev/null | tr 'A-Z' 'a-z' || true)
  if grep -q '429' <<<"$(head -1 <<<"$rl_headers")"; then
    for h in "${REQUIRED_NAMES[@]}"; do
      grep -q "^$h:" <<<"$rl_headers" || note "the 429 from limit_req is missing $h"
    done
  fi
fi

# 3. The zones are actually SEPARATE. One blanket zone over /api/auth/ would
#    pass check 1 and silently throttle the SPA, whose /api/auth/me and
#    /api/auth/refresh fire on load, on focus and on every 401 retry. Sixty
#    parallel reads is a heavy tab-restore, not abuse, and must pass clean.
me_codes=$(burst_codes /api/auth/me 60)
me_throttled=$(grep -c '^429$' <<<"$me_codes" || true)
[[ ${me_throttled:-0} -eq 0 ]] \
  || note "/api/auth/me was throttled ($me_throttled of 60) — the SPA's own polling shares the credential zone"

# 4. /healthz is never limited. It is what the container orchestrator asks, and
#    a throttled health check restarts the very instance under load.
health_codes=$(burst_codes /healthz 60)
[[ $(grep -c '^429$' <<<"$health_codes" || true) -eq 0 ]] \
  || note "/healthz was rate limited — a throttled health check turns load into a restart loop"

# 5. Every declared zone is used, and every used zone is declared. nginx -t
#    catches a reference to a missing zone; nothing catches a zone that is
#    declared, costs 10 MiB of shared memory, and limits nothing.
declared=$(grep -oE 'zone=[a-z_]+:' "$ROOT/web/nginx.main.conf" | sed 's/zone=//;s/://' | sort -u)
used=$(grep -oE 'limit_req zone=[a-z_]+' "$ROOT/web/nginx.conf" | sed 's/limit_req zone=//' | sort -u)
for z in $declared; do
  grep -qx "$z" <<<"$used" || note "zone $z is declared in nginx.main.conf and used nowhere"
done
for z in $used; do
  grep -qx "$z" <<<"$declared" || note "zone $z is used in nginx.conf and declared nowhere"
done

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
echo "nginx security headers survive on every response, and the rate-limit zones bite where they should"
