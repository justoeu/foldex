# syntax=docker/dockerfile:1
# Base tags are minor-pinned (golang:1.26-alpine, alpine:3.24). Digest pins are
# refreshed on intentional base upgrades via `docker buildx imagetools inspect`.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
# Cross-compile target arch comes from buildkit via $TARGETARCH (set when the
# build runs with `--platform linux/arm64` or `linux/amd64`). Hardcoding
# GOARCH=amd64 here used to silently produce amd64 binaries even for the
# arm64 CI matrix cell, breaking arm64 deployments.
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/foldex ./cmd/server
RUN GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/rustfs-bootstrap ./cmd/rustfs-bootstrap
RUN GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/foldex-mailer ./cmd/mailer
RUN GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/foldex-backup-agent ./cmd/backup-agent

# One-shot init image: create app IAM user + bucket-scoped policy on RustFS.
# Used by docker-compose.services.yml `rustfs-init` (target: rustfs-bootstrap).
FROM alpine:3.24 AS rustfs-bootstrap
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates \
    && addgroup -S bootstrap && adduser -S -G bootstrap -H bootstrap
COPY --from=build /out/rustfs-bootstrap /rustfs-bootstrap
USER bootstrap:bootstrap
ENTRYPOINT ["/rustfs-bootstrap"]

# Operational backup agent (ADR-43). Derives from postgres:18.4-alpine, NOT
# from the backend image: the drill needs initdb/pg_ctl/postgres — a whole
# ephemeral server — and postgresql*-client packages ship only the clients.
# The base also keeps pg_dump version-matched with foldex-db; bump this pin in
# the same change as docker-compose.db.yml/services.yml and internal/testdb
# (docs/STACK.md tracks all four). Published as an extra tag of the SAME
# repository (justoeu/foldex-backend:<ver>-backup-agent) — same reasoning as
# the mailer: a third Docker Hub repository is worse than a second tag.
FROM postgres:18.4-alpine AS backup-agent
# Accepted wart: the postgres base declares VOLUME for its data dir, so each
# recreate of this container leaves an unused anonymous volume behind (the
# agent never runs the bundled server outside the drill's /tmp cluster).
# `docker volume prune` clears them; re-basing to strip the VOLUME is not
# worth diverging from the version-matched official image.
COPY --from=build /out/foldex-backup-agent /foldex-backup-agent
# The postgres base image already ships a non-root `postgres` user (uid 70 on
# alpine); the agent neither needs nor wants the entrypoint machinery of the
# base image — it execs pg_dump/pg_restore itself.
USER postgres
ENTRYPOINT ["/foldex-backup-agent"]

# Use alpine as the final image so we can install chromium for go-rod.
# distroless/static does not include the necessary system libs.
FROM alpine:3.24
# `apk upgrade` before `apk add` closes HIGH/CRITICAL CVEs that ship with the
# base alpine packages (musl, libssl, etc.) between alpine minor releases.
# Same pattern as web/Dockerfile.
RUN apk upgrade --no-cache && apk add --no-cache \
    chromium \
    ca-certificates \
    tzdata
ENV CHROME_PATH=/usr/bin/chromium-browser
COPY --from=build /out/foldex /foldex
# Ship bootstrap alongside the server so operators can re-run it from the
# backend image without a separate pull (`docker compose run …`).
COPY --from=build /out/rustfs-bootstrap /rustfs-bootstrap
# The AMQP mail worker (MAIL_TRANSPORT=amqp) rides in this image too, started by
# overriding the entrypoint — docker-compose.yml's `mailer` service does exactly
# that. A separate, slimmer image would suit it better (it needs neither
# Chromium nor the migrations), but publishing a third image means a third
# Docker Hub repository and a third arm on the release matrix, and a compose
# service pointing at a tag the release workflow never pushes is worse than a
# fat image. The isolation that actually matters is preserved regardless: the
# mailer container gets no DB_URL, and cmd/mailer cannot ask for one.
COPY --from=build /out/foldex-mailer /foldex-mailer
COPY db/migrations /migrations
EXPOSE 9089
# Run as non-root so Chromium can keep its process sandbox enabled.
RUN addgroup -S foldex && adduser -S foldex -G foldex
# /data holds operator-persisted state: the autogen VAPID keypair lands at
# /data/vapid.json (config.VAPID_STATE_PATH default). Docker named volumes
# inherit ownership of the underlying directory on first mount, so creating
# it pre-owned by foldex:foldex is what keeps the LoadOrGenerate write path
# working without a privileged init container. The /data directory ships
# 0o700 so even if a future mount widens read access on the volume, the
# private key file (0o600) plus the parent's narrow mode keeps it locked.
# 0700 is intentional — the only resident is the VAPID private key
# (push.LoadOrGenerate writes 0600 vapid.json here). If a future feature
# adds a world-readable file, widen the dir then.
RUN mkdir -p /data && chown foldex:foldex /data && chmod 0700 /data
USER foldex:foldex
ENTRYPOINT ["/foldex"]
