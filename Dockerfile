# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS web
WORKDIR /src/web
RUN corepack enable && corepack prepare pnpm@10 --activate
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

FROM golang:1.25-bookworm AS builder
ARG VERSION=dev
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X netshare-hub/internal/app.Version=${VERSION}" \
    -o /out/netshare-hub ./cmd/netshare-hub

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        acl \
        ca-certificates \
        curl \
        openssh-client \
        passwd \
        procps \
        rsync \
        samba \
        samba-common-bin \
        samba-vfs-modules \
        util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /etc/netshare-hub /etc/samba /run/samba /var/log/samba /srv/shares

COPY --from=builder /out/netshare-hub /usr/bin/netshare-hub
COPY deploy/docker/config.toml /usr/share/netshare-hub/config.docker.toml
COPY deploy/docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/bin/netshare-hub /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080 445
VOLUME ["/data", "/etc/netshare-hub", "/srv/shares"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
