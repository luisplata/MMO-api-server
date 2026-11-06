# syntax=docker/dockerfile:1
# MMO API Server — multi-stage build.
#
# Builder pins the Go toolchain to the exact go.mod version (1.25.4).
# go.mod/go.sum are copied FIRST so `go mod download` layer-caches.
# The generated protobuf code under proto/v1/gen is COMMITTED, so no
# protoc / buf / protobuf tooling is needed at build time.

FROM golang:1.25.4-alpine AS builder
WORKDIR /src

# Cache dependencies first (invalidates only when go.mod/go.sum change).
COPY go.mod go.sum ./
RUN go mod download

# Then the source (invalidates the dep layer above on code changes).
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mmo-server ./cmd/server

# Runtime: minimal alpine. busybox provides nc used by the
# docker-compose healthcheck. The binary is static (CGO_ENABLED=0) and
# serves plain ws:// on the private upstream network; TLS and
# certificates belong to the reverse proxy, never to this image.
FROM alpine:3.21
RUN adduser -D -u 10001 mmo
COPY --from=builder /out/mmo-server /usr/local/bin/mmo-server
# The boot flags default to data/templates.json and data/characters.json;
# ship the committed seed files so the fail-fast loader succeeds inside the
# container. mmo owns the directory because the character store writes there.
WORKDIR /app
COPY --from=builder --chown=10001:10001 /src/data ./data
USER mmo
EXPOSE 8080/tcp
ENTRYPOINT ["/usr/local/bin/mmo-server"]
CMD ["-wss", ":8080", "-dev-auth", "true"]
