#!/usr/bin/env bash
set -euo pipefail

mkdir -p /data /etc/netshare-hub /etc/samba /run/samba /var/log/samba /srv/shares

if [ ! -f /etc/netshare-hub/config.toml ]; then
    cp /usr/share/netshare-hub/config.docker.toml /etc/netshare-hub/config.toml
fi

touch /etc/samba/netshare-hub.conf

if ! pgrep -x smbd >/dev/null 2>&1; then
    smbd --daemon --no-process-group
fi

exec /usr/bin/netshare-hub serve --config /etc/netshare-hub/config.toml
