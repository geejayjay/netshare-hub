package sys

import (
	"strings"
	"testing"
)

// Real output of "smbstatus --json" (Samba 4.17), with one open file added.
const smbstatusOutput = `{"timestamp": "2026-11-23T15:18:58.751918+0000", "version": "4.17.12-Debian", "smb_conf": "/etc/samba/smb.conf",
"sessions": {
 "3033177278": {"session_id": "3033177278", "server_id": {"pid": "43", "task_id": "0", "vnn": "4294967295", "unique_id": "5805353434131556653"}, "uid": 1000, "gid": 1000, "username": "alice", "groupname": "alice", "remote_machine": "127.0.0.1", "hostname": "ipv4:127.0.0.1:39150", "session_dialect": "SMB3_11", "encryption": {"cipher": "-", "degree": "none"}, "signing": {"cipher": "AES-128-GMAC", "degree": "partial"}},
 "938749287": {"session_id": "938749287", "server_id": {"pid": "44", "task_id": "0", "vnn": "4294967295", "unique_id": "4388600575420900312"}, "uid": 1001, "gid": 1001, "username": "bob", "groupname": "bob", "remote_machine": "192.168.1.20", "hostname": "ipv4:192.168.1.20:39152", "session_dialect": "SMB3_11", "encryption": {"cipher": "AES-128-GCM", "degree": "full"}, "signing": {"cipher": "AES-128-GMAC", "degree": "partial"}},
 "1": {"session_id": "1", "server_id": {"pid": "x"}, "username": "broken"},
 "2": {"session_id": "2; rm -rf /", "server_id": {"pid": "45"}, "username": "evil"}},
"tcons": {
 "2043756988": {"service": "s", "server_id": {"pid": "43"}, "tcon_id": "2043756988", "session_id": "3033177278", "machine": "127.0.0.1", "connected_at": "2026-11-23T15:18:56.767873+00:00"},
 "3816981781": {"service": "media", "server_id": {"pid": "44"}, "tcon_id": "3816981781", "session_id": "938749287", "machine": "192.168.1.20", "connected_at": "2026-11-23T15:18:56.767953+00:00"}},
"open_files": {"/srv/media/a.mkv": {"service_path": "/srv/media", "filename": "a.mkv", "opens": {"44/1": {"server_id": {"pid": "44"}, "uid": 1001}}}}}`

func TestParseSessions(t *testing.T) {
	got, err := ParseSessions([]byte(smbstatusOutput))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions (the broken ones left out), got %+v", got)
	}
	a, b := got[0], got[1]
	if a.ID != "3033177278" || a.PID != 43 || a.Username != "alice" || a.ClientIP != "127.0.0.1" || a.ClientPort != 39150 ||
		a.Encrypted || !a.Signed || strings.Join(a.Shares, ",") != "s" || a.OpenFiles != 0 || a.ConnectedAt.IsZero() {
		t.Errorf("alice: %+v", a)
	}
	if b.Username != "bob" || !b.Encrypted || b.OpenFiles != 1 || strings.Join(b.Shares, ",") != "media" {
		t.Errorf("bob: %+v", b)
	}
	if _, err := ParseSessions([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
	for _, id := range []string{"", "12a", "1 2", "1;2", "../1", strings.Repeat("9", 21)} {
		if ValidSessionID(id) {
			t.Errorf("session id %q accepted", id)
		}
	}
}
