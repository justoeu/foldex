# syntax=docker/dockerfile:1
#
# Multi-stage build: the bundle is produced INSIDE the container so prod and
# dev stay in sync. Avoids the recurring "I forgot to run bun run build" class
# of bug where the old dist/ shipped silently.
#
# Stage 1 — install + build with bun.
# Stage 2 — nginx serving the built static files. TLS material is NOT baked
#           into the image (security: see entrypoint-certs.sh) — mount your
#           own pair at /etc/nginx/certs/{cert,key}.pem or let the entrypoint
#           generate an ephemeral one per container.

FROM oven/bun:1.3-alpine AS builder
WORKDIR /app
# Lockfile + package.json first so the install layer caches across edits.
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM nginx:1.27-alpine

# Patch base packages + add openssl (for the runtime cert generator). Closes
# 30+ HIGH/CRITICAL CVEs sitting in the upstream nginx:alpine pkgs (libcrypto3,
# libssl3, musl, nghttp2, zlib, etc.) without changing the major version.
# Re-runs every build so we always grab the latest patched versions.
RUN apk upgrade --no-cache && apk add --no-cache openssl

COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf

# Cert bootstrap — generates an ephemeral self-signed pair when no volume is
# mounted. Sits in /docker-entrypoint.d which the nginx image runs before
# starting the daemon (sorted alphabetically; "40-" places us after the env
# substitution scripts and before the launcher).
COPY entrypoint-certs.sh /docker-entrypoint.d/40-foldex-certs.sh
RUN chmod +x /docker-entrypoint.d/40-foldex-certs.sh

EXPOSE 80 443
