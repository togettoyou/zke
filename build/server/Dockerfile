# syntax=docker/dockerfile:1.7

# Both builder stages are pinned to the machine doing the building rather than
# to the image's target architecture. Nothing they produce is architecture-bound
# in a way that needs emulation: the Console bundle is static files, and Go
# cross-compiles from one host with CGO disabled. Under the default (target)
# platform a linux/arm64 build runs `pnpm install`, `vite build` and the Go
# compiler through QEMU, which costs more than every other step of this file put
# together. Pinning also makes these stages byte-identical across target
# platforms, so a two-architecture build runs them once instead of twice.
FROM --platform=$BUILDPLATFORM node:24-alpine AS console-build
WORKDIR /src/web/console
RUN corepack enable && corepack prepare pnpm@11.17.0 --activate
COPY web/console/package.json web/console/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/console/ ./
RUN pnpm build

FROM --platform=$BUILDPLATFORM golang:1.26.4-alpine AS server-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
# Declared next to the only command that reads it. The version changes on every
# commit, and a build argument participates in the cache key of the commands
# that use it — keeping it below the module download leaves that layer alone.
#
# The build context excludes `.git`, so the caller supplies the version:
#   docker build --build-arg VERSION="$(git describe --tags --always --dirty)" ...
# An unstamped image still runs; it just reports "development".
ARG VERSION=development
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath \
    -ldflags="-s -w -X github.com/togettoyou/zke/pkg/shared/buildinfo.version=${VERSION}" \
    -o /out/zke-server ./cmd/zke-server

FROM alpine:3.22 AS server
RUN apk add --no-cache ca-certificates tzdata \
    && install -d /data /etc/zke /usr/share/zke/console
COPY --from=server-build /out/zke-server /usr/local/bin/zke-server
COPY --from=console-build /src/web/console/dist/ /usr/share/zke/console/
COPY configs/zke-server.yaml /etc/zke/zke-server.yaml
ENV ZKE_CONSOLE_DIRECTORY=/usr/share/zke/console
WORKDIR /
VOLUME ["/data"]
EXPOSE 8080/tcp 8081/tcp 8443/udp
ENTRYPOINT ["/usr/local/bin/zke-server"]
CMD ["--config", "/etc/zke/zke-server.yaml"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1

FROM postgres:17-alpine AS server-pg
RUN apk add --no-cache ca-certificates tzdata \
    && install -d /data /etc/zke /usr/share/zke/console
COPY --from=server-build /out/zke-server /usr/local/bin/zke-server
COPY --from=console-build /src/web/console/dist/ /usr/share/zke/console/
COPY configs/zke-server.yaml /etc/zke/zke-server.yaml
COPY --chmod=755 build/server/server-pg-entrypoint.sh /usr/local/bin/zke-server-pg-entrypoint
ENV POSTGRES_DB=zke \
    POSTGRES_USER=zke \
    POSTGRES_PASSWORD=zke_change_me \
    PGDATA=/var/lib/postgresql/data/pgdata \
    ZKE_DATABASE_URL=postgres://zke:zke_change_me@127.0.0.1:5432/zke?sslmode=disable \
    ZKE_CONSOLE_DIRECTORY=/usr/share/zke/console
WORKDIR /
VOLUME ["/var/lib/postgresql/data", "/data"]
EXPOSE 8080/tcp 8081/tcp 8443/udp
ENTRYPOINT ["/usr/local/bin/zke-server-pg-entrypoint"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=5 \
    CMD pg_isready -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null \
    && wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1
