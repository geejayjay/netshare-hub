#!/usr/bin/env bash
# The Phase 0b "thin slice", end to end, the way it runs in production:
#
#   root agent  <--unix socket-->  web server (unprivileged user)  <--HTTP--  this script
#
# It sets up the first admin, creates a user and a share through the HTTP API,
# then connects with a real SMB client. Run it ONLY in the disposable test
# container:
#
#   docker compose -f compose.test.yml run --rm test bash deploy/test/thin-slice.sh
set -euo pipefail

WEB_USER=nshweb
CONF=/tmp/nsh-thin-slice.toml
API=http://127.0.0.1:8080/api/v1
JAR=/tmp/nsh-cookies
ADMIN_PASS=thin-slice-admin-1
USER_PASS=thin-slice-user-1

step() { printf '\n== %s\n' "$*"; }
fail() { printf 'FAILED: %s\n' "$*" >&2; exit 1; }
# has TEXT NEEDLE: true when TEXT contains NEEDLE. (No pipes: with pipefail a
# command that exits non-zero would fail the check even when the text matches.)
has() { case "$1" in *"$2"*) return 0 ;; *) return 1 ;; esac; }
smb() { smbclient //127.0.0.1/slice -U "jay%$1" -c "$2" 2>&1 || true; }

step "build"
go build -o /usr/local/bin/netshare-hub ./cmd/netshare-hub

step "prepare the unprivileged web user and its data folder"
id "$WEB_USER" >/dev/null 2>&1 || useradd --no-create-home "$WEB_USER"
install -d -o "$WEB_USER" -g "$WEB_USER" -m 750 /var/lib/netshare-hub
cat > "$CONF" <<EOF
listen = "127.0.0.1:8080"
data_dir = "/var/lib/netshare-hub"
sys = "agent"
agent_user = "$WEB_USER"
secure_cookies = false
log_level = "warn"
EOF

step "start the agent (root) and the web server ($WEB_USER)"
netshare-hub agent --config "$CONF" &
sleep 1
sudo -u "$WEB_USER" netshare-hub serve --config "$CONF" &
for _ in $(seq 1 50); do curl -sf "$API/health" >/dev/null && break; sleep 0.2; done
curl -sf "$API/health" >/dev/null || fail "web server did not start"

procs=$(ps -o user=,args= -C netshare-hub)
echo "$procs"
has "$procs" "root     netshare-hub agent" || fail "agent is not running as root"
has "$procs" "$WEB_USER   netshare-hub serve" || fail "web server is not running as $WEB_USER"

post() { curl -s -b "$JAR" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' -d "$2" "$API$1"; }

step "first-run setup (creates the web-only admin)"
CSRF=$(curl -s -c "$JAR" -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" "$API/setup" \
  | sed -n 's/.*"csrfToken":"\([^"]*\)".*/\1/p')
[ -n "$CSRF" ] || fail "setup did not return a session"
if getent passwd admin >/dev/null; then fail "the web-only admin must not get a Linux account"; fi

step "create user jay (the server asks the agent to create the Linux + Samba account)"
post /users "{\"username\":\"jay\",\"role\":\"user\",\"password\":\"$USER_PASS\"}"; echo
getent passwd jay || fail "Linux account for jay was not created"
has "$(pdbedit -L)" "jay:" || fail "Samba account for jay was not created"

step "a system account name is refused"
out=$(post /users "{\"username\":\"daemon\",\"role\":\"user\",\"password\":\"$USER_PASS\"}")
has "$out" '"code":"reserved"' || fail "creating a user named like a system account was not refused: $out"

step "create share 'slice' with jay as read+write"
post /shares '{"name":"slice","path":"/srv/shares/slice","createDirectory":true,"access":[{"userId":2,"level":"write"}]}'; echo
has "$(testparm -s 2>/dev/null)" "[slice]" || fail "share is not in the Samba config"

step "sharing a system folder is refused"
out=$(post /shares '{"name":"evil","path":"/etc/samba"}')
has "$out" '"code":"not_allowed"' || fail "sharing /etc/samba was not refused: $out"

step "connect over SMB as jay, write and list a file"
out=$(smb "$USER_PASS" 'put /etc/hostname hello.txt; ls')
echo "$out"
has "$out" "hello.txt" || fail "jay could not write to the share"
ls -l /srv/shares/slice

step "the activity log: the agent reads Samba's logs, the web server shows them"
if ls /usr/lib/*/samba/vfs/full_audit.so >/dev/null 2>&1; then
  out=$(curl -s -X PUT -b "$JAR" -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
    -d '{"serverName":"","workgroup":"","minProtocol":"default","maxProtocol":"default","encryption":"default","multichannel":"default","activityLog":true}' \
    "$API/settings/samba")
  has "$out" '"activityLog":true' || fail "could not turn the activity log on: $out"
  smb "$USER_PASS" 'put /etc/hostname audited.txt' >/dev/null
  found=""
  for _ in $(seq 1 50); do
    out=$(curl -s -b "$JAR" "$API/activity?op=write")
    if has "$out" "audited.txt"; then found=1; break; fi
    sleep 0.2
  done
  [ -n "$found" ] || fail "the write over SMB is not in the activity log: $out"
  echo "write over SMB seen in the activity log"
else
  echo "(skipped: samba-vfs-modules is not installed)"
fi

step "a wrong password is refused"
out=$(smb "wrong-password" ls)
has "$out" "NT_STATUS_LOGON_FAILURE" || fail "wrong password was accepted: $out"

step "delete the user: off the share first, then the account is gone"
code=$(curl -s -o /dev/null -w '%{http_code}' -X DELETE -b "$JAR" -H "X-CSRF-Token: $CSRF" "$API/users/2")
[ "$code" = 204 ] || fail "delete failed with status $code"
if getent passwd jay >/dev/null; then fail "Linux account still exists"; fi
has "$(testparm -s 2>/dev/null)" "available = No" || fail "a share without users must be switched off"
out=$(smb "$USER_PASS" ls)
if has "$out" "hello.txt"; then fail "the share is still reachable after its only user was deleted"; fi

printf '\nTHIN SLICE PASSED\n'
