package sys

import (
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Session is one SMB connection: a user logged in from a computer.
type Session struct {
	ID          string    `json:"id" doc:"Samba's session id."`
	PID         int       `json:"pid" doc:"The smbd process serving it."`
	Username    string    `json:"username"`
	ClientIP    string    `json:"clientIp"`
	ClientPort  int       `json:"clientPort,omitempty"`
	Machine     string    `json:"machine,omitempty" doc:"The client's name or address as Samba knows it."`
	Dialect     string    `json:"dialect" doc:"The SMB version, e.g. SMB3_11."`
	Encrypted   bool      `json:"encrypted"`
	Signed      bool      `json:"signed"`
	Shares      []string  `json:"shares" nullable:"false"`
	OpenFiles   int       `json:"openFiles"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
}

var sessionID = regexp.MustCompile(`^[0-9]{1,20}$`)

// ValidSessionID reports whether id looks like a Samba session id.
func ValidSessionID(id string) bool { return sessionID.MatchString(id) }

// smbstatusJSON is the part of "smbstatus --json" that is used.
type smbstatusJSON struct {
	Sessions map[string]struct {
		SessionID string `json:"session_id"`
		ServerID  struct {
			PID string `json:"pid"`
		} `json:"server_id"`
		Username   string `json:"username"`
		Machine    string `json:"remote_machine"`
		Hostname   string `json:"hostname"`
		Dialect    string `json:"session_dialect"`
		Encryption struct {
			Degree string `json:"degree"`
		} `json:"encryption"`
		Signing struct {
			Degree string `json:"degree"`
		} `json:"signing"`
	} `json:"sessions"`
	Tcons map[string]struct {
		Service     string `json:"service"`
		SessionID   string `json:"session_id"`
		ConnectedAt string `json:"connected_at"`
	} `json:"tcons"`
	OpenFiles map[string]struct {
		Opens map[string]struct {
			ServerID struct {
				PID string `json:"pid"`
			} `json:"server_id"`
		} `json:"opens"`
	} `json:"open_files"`
}

// ParseSessions reads "smbstatus --json". Sessions with a missing or
// unusable process id are left out: they could not be disconnected safely.
func ParseSessions(raw []byte) ([]Session, error) {
	var st smbstatusJSON
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	openByPID := map[int]int{}
	for _, f := range st.OpenFiles {
		for _, o := range f.Opens {
			if pid, err := strconv.Atoi(o.ServerID.PID); err == nil {
				openByPID[pid]++
			}
		}
	}
	var out []Session
	for _, s := range st.Sessions {
		pid, err := strconv.Atoi(s.ServerID.PID)
		if err != nil || pid <= 1 || !ValidSessionID(s.SessionID) {
			continue
		}
		sess := Session{ID: s.SessionID, PID: pid, Username: s.Username, Machine: s.Machine, Dialect: s.Dialect,
			Encrypted: s.Encryption.Degree != "" && s.Encryption.Degree != "none",
			Signed:    s.Signing.Degree != "" && s.Signing.Degree != "none",
			Shares:    []string{}, OpenFiles: openByPID[pid]}
		sess.ClientIP, sess.ClientPort = parseHostname(s.Hostname)
		for _, t := range st.Tcons {
			if t.SessionID != s.SessionID {
				continue
			}
			sess.Shares = append(sess.Shares, t.Service)
			if at, err := time.Parse(time.RFC3339Nano, t.ConnectedAt); err == nil && (sess.ConnectedAt.IsZero() || at.Before(sess.ConnectedAt)) {
				sess.ConnectedAt = at.UTC()
			}
		}
		sort.Strings(sess.Shares)
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username+out[i].ID < out[j].Username+out[j].ID })
	return out, nil
}

// parseHostname reads Samba's "ipv4:1.2.3.4:5678" / "ipv6:[::1]:5678".
func parseHostname(h string) (string, int) {
	h = strings.TrimPrefix(strings.TrimPrefix(h, "ipv4:"), "ipv6:")
	i := strings.LastIndex(h, ":")
	if i < 0 {
		return strings.Trim(h, "[]"), 0
	}
	port, _ := strconv.Atoi(h[i+1:])
	return strings.Trim(h[:i], "[]"), port
}
