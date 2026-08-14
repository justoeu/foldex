#!/bin/sh
# foldex/web — TLS bootstrap for the nginx container.
#
# Runs as part of the nginx image's /docker-entrypoint.d/ chain (executed
# automatically before nginx starts). Decides what TLS material to serve:
#
#   1) Volume-mounted real cert + key at /etc/nginx/certs/{cert,key}.pem
#      → use it as-is. This is the production path: the operator mounts their
#      mkcert (dev) or Let's Encrypt (prod) pair as a read-only volume.
#
#   2) Nothing mounted → generate an EPHEMERAL self-signed pair on the fly,
#      print a loud warning explaining how to override.
#
# The image deliberately ships NO baked-in certificate or key. Keeping a
# private key inside a public Docker image is a HIGH-severity finding
# (Trivy/Scout flag it) AND an actual risk: every operator pulling that
# image would share the same key. Generating per-container makes each
# instance unique and keeps the image clean.

set -eu

CERT_DIR=/etc/nginx/certs
CERT_FILE="$CERT_DIR/cert.pem"
KEY_FILE="$CERT_DIR/key.pem"

# Bake the public host into the HTTP→HTTPS redirect. Never use $host /
# $http_host (attacker-controlled Host header). Operators set WEB_PUBLIC_HOST
# when serving under a LAN name, a custom domain, or a non-443 port — the
# compose default carries :${WEB_HTTPS_PORT} for exactly that reason, since a
# portless redirect sends the browser to 443 where this stack serves nothing.
PUBLIC_HOST=${WEB_PUBLIC_HOST:-localhost}
# Allowlist: hostname / IPv4 / optional :port — reject anything else.
#
# The rejection is ANNOUNCED. It used to fall back in silence, which meant a
# typo'd value produced a redirect that dead-ends with no trace of why — and
# the fallback cannot restore the port, because this container is never told
# which one the host published.
case "$PUBLIC_HOST" in
  ''|*[!A-Za-z0-9._:-]* )
    echo "[foldex/web] WEB_PUBLIC_HOST=$PUBLIC_HOST is not a bare host[:port] — falling back to 'localhost'." >&2
    echo "[foldex/web] The HTTP→HTTPS redirect will target port 443. Set WEB_PUBLIC_HOST=host:port if you serve elsewhere." >&2
    PUBLIC_HOST=localhost
    ;;
esac
NGINX_CONF=/etc/nginx/conf.d/default.conf
if [ -f "$NGINX_CONF" ] && grep -q '__WEB_PUBLIC_HOST__' "$NGINX_CONF" 2>/dev/null; then
  # BusyBox sed -i needs a writable file (Dockerfile chowns conf.d to nginx).
  sed "s/__WEB_PUBLIC_HOST__/${PUBLIC_HOST}/g" "$NGINX_CONF" > "${NGINX_CONF}.tmp"
  mv "${NGINX_CONF}.tmp" "$NGINX_CONF"
  echo "[foldex/web] HTTPS redirect host → ${PUBLIC_HOST}"
fi

mkdir -p "$CERT_DIR"

if [ -f "$CERT_FILE" ] && [ -f "$KEY_FILE" ]; then
  echo "[foldex/web] using mounted TLS pair at $CERT_DIR"
  exit 0
fi

cat <<'WARN' >&2
[foldex/web] ============================================================
[foldex/web]  No TLS pair found at /etc/nginx/certs.
[foldex/web]  Generating an EPHEMERAL self-signed cert for this container.
[foldex/web]  Browsers will show "Not Secure" until you mount a real pair:
[foldex/web]    docker run -v /path/to/certs:/etc/nginx/certs:ro …
[foldex/web]  Or via docker-compose:
[foldex/web]    volumes:
[foldex/web]      - ./web/certs:/etc/nginx/certs:ro
[foldex/web] ============================================================
WARN

openssl req -x509 -newkey rsa:2048 -nodes -days 365 \
  -keyout "$KEY_FILE" \
  -out "$CERT_FILE" \
  -subj "/CN=foldex-ephemeral" \
  -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" 2>/dev/null

chmod 600 "$KEY_FILE"
echo "[foldex/web] ephemeral TLS pair generated (valid 365 days, CN=foldex-ephemeral)"
