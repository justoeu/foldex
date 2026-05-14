# syntax=docker/dockerfile:1
#
# Multi-stage build: the bundle is produced INSIDE the container so prod and
# dev stay in sync. Avoids the recurring "I forgot to run bun run build" class
# of bug where the old dist/ shipped silently.
#
# Stage 1 — install + build with bun.
# Stage 2 — nginx serving the built static files + the mkcert certs.

FROM oven/bun:1.3-alpine AS builder
WORKDIR /app
# Lockfile + package.json first so the install layer caches across edits.
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM nginx:1.27-alpine
COPY --from=builder /app/dist /usr/share/nginx/html
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY certs/cert.pem /etc/nginx/certs/cert.pem
COPY certs/key.pem  /etc/nginx/certs/key.pem
EXPOSE 80 443
