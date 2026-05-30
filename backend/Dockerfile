# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src
# Cross-compile target arch comes from buildkit via $TARGETARCH (set when the
# build runs with `--platform linux/arm64` or `linux/amd64`). Hardcoding
# GOARCH=amd64 here used to silently produce amd64 binaries even for the
# arm64 CI matrix cell, breaking arm64 deployments.
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .
RUN GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/foldex ./cmd/server

# Use alpine as the final image so we can install chromium for go-rod.
# distroless/static does not include the necessary system libs.
FROM alpine:3.21
RUN apk add --no-cache \
    chromium \
    ca-certificates \
    tzdata
ENV CHROME_PATH=/usr/bin/chromium-browser
COPY --from=build /out/foldex /foldex
COPY db/migrations /migrations
EXPOSE 9089
# Run as non-root for security (chromium requires --no-sandbox when running as root).
RUN addgroup -S foldex && adduser -S foldex -G foldex
USER foldex:foldex
ENTRYPOINT ["/foldex"]
