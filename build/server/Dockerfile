# syntax=docker/dockerfile:1.7

FROM node:24-alpine AS console-build
WORKDIR /src/web/console
RUN corepack enable && corepack prepare pnpm@11.17.0 --activate
COPY web/console/package.json web/console/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/console/ ./
RUN pnpm build

FROM golang:1.26.4-alpine AS server-build
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ ./api/
COPY cmd/ ./cmd/
COPY pkg/ ./pkg/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/zke-server ./cmd/zke-server

FROM alpine:3.22 AS server
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 1000 zke \
    && adduser -S -D -H -u 1000 -G zke zke \
    && install -d -o zke -g zke /etc/zke /var/lib/zke /usr/share/zke/console
COPY --from=server-build /out/zke-server /usr/local/bin/zke-server
COPY --from=console-build /src/web/console/dist/ /usr/share/zke/console/
COPY configs/zke-server.yaml /etc/zke/zke-server.yaml
USER 1000:1000
EXPOSE 8080/tcp 8081/tcp 8443/udp
ENTRYPOINT ["/usr/local/bin/zke-server"]
CMD ["--config", "/etc/zke/zke-server.yaml"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1

FROM postgres:17-alpine AS server-pg
RUN apk add --no-cache ca-certificates su-exec tzdata \
    && addgroup -S -g 1000 zke \
    && adduser -S -D -H -u 1000 -G zke zke \
    && install -d -o zke -g zke /etc/zke /var/lib/zke /usr/share/zke/console
COPY --from=server-build /out/zke-server /usr/local/bin/zke-server
COPY --from=console-build /src/web/console/dist/ /usr/share/zke/console/
COPY configs/zke-server.yaml /etc/zke/zke-server.yaml
COPY --chmod=755 build/server/server-pg-entrypoint.sh /usr/local/bin/zke-server-pg-entrypoint
ENV POSTGRES_DB=zke \
    POSTGRES_USER=zke \
    POSTGRES_PASSWORD=zke_change_me \
    PGDATA=/var/lib/postgresql/data/pgdata \
    ZKE_DATABASE_URL=postgres://zke:zke_change_me@127.0.0.1:5432/zke?sslmode=disable
VOLUME ["/var/lib/postgresql/data", "/var/lib/zke"]
EXPOSE 8080/tcp 8081/tcp 8443/udp
ENTRYPOINT ["/usr/local/bin/zke-server-pg-entrypoint"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=5 \
    CMD pg_isready -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null \
    && wget -qO- http://127.0.0.1:8080/readyz >/dev/null || exit 1
