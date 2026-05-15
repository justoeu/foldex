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
