# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

COPY go.mod go.sum* ./
RUN go mod download || true

COPY . .
RUN go build -ldflags="-s -w" -o /out/foldex ./cmd/server

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
