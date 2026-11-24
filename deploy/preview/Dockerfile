FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /etc/netshare-hub /srv/shares/media /srv/shares/documents /srv/shares/timemachine \
    && printf '# Welcome to NetShare Hub\n\nThis is a sample markdown file for testing inline preview in the File Manager.\n' > /srv/shares/documents/README.md \
    && printf '{\n  "service": "netshare-hub",\n  "mode": "preview",\n  "protocols": ["SMB3_11", "tus-1.0.0"]\n}\n' > /srv/shares/documents/sample.json \
    && printf 'Sample media notes for testing downloads and previews.\n' > /srv/shares/media/notes.txt

COPY bin/netshare-hub-linux /usr/local/bin/netshare-hub
COPY deploy/preview/config.toml /etc/netshare-hub/config.toml
RUN chmod +x /usr/local/bin/netshare-hub

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/usr/local/bin/netshare-hub"]
CMD ["serve", "--config", "/etc/netshare-hub/config.toml"]
