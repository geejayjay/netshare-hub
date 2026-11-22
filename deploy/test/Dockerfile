FROM golang:1.27-bookworm AS go-toolchain

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive \
    PATH="/usr/local/go/bin:/go/bin:${PATH}" \
    GOPATH="/go"

RUN apt-get update && apt-get install -y --no-install-recommends \
        acl \
        ca-certificates \
        curl \
        git \
        passwd \
        samba \
        samba-common-bin \
        samba-vfs-modules \
        smbclient \
        sudo \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-toolchain /usr/local/go /usr/local/go

RUN mkdir -p /var/lib/netshare-hub /srv/shares /etc/samba \
    && touch /etc/samba/netshare-hub.conf

COPY deploy/test/smb.conf /etc/samba/smb.conf
COPY deploy/test/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

WORKDIR /workspace

EXPOSE 445 8080

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["go", "test", "-p", "1", "-tags", "integration", "./internal/..."]
