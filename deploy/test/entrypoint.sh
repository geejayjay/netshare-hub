#!/usr/bin/env bash
set -euo pipefail

mkdir -p /run/samba /var/log/samba /etc/samba /srv/shares
touch /etc/samba/netshare-hub.conf

if ! pgrep -x smbd >/dev/null 2>&1; then
    smbd --daemon --no-process-group
fi

exec "$@"
