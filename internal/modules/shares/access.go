package shares

import (
	"slices"
	"strings"

	"netshare-hub/internal/modules/users"
	"netshare-hub/internal/sys"
)

// Presets are one-click starting points for a share. They only fill in the
// share's settings and the level given to the people added; every setting
// stays editable, and the server applies the settings, not the preset.
//
// Time Machine is a preset too, but needs its own Samba settings (3.5a).
var Presets = []Preset{
	{
		ID: "private", Name: "Private",
		Description:  "Only the people you choose. Each can read, or read and change files.",
		DefaultLevel: LevelWrite, Levels: []Level{LevelRead, LevelWrite},
	},
	{
		ID: "team", Name: "Team",
		Description:  "A folder the whole team reads and works in together.",
		DefaultLevel: LevelWrite, Levels: []Level{LevelRead, LevelWrite},
	},
	{
		ID: "public-read", Name: "Public, read-only",
		Description: "Anyone on the network can open files without a password. Nobody can change them over the network.",
		ReadOnly:    true, GuestAccess: true,
		DefaultLevel: LevelRead, Levels: []Level{LevelRead},
	},
	{
		ID: "dropbox", Name: "Drop box",
		Description:  "People can add files but cannot see what is in the folder. Give \"Read and write\" to whoever collects them.",
		DefaultLevel: LevelUpload, Levels: []Level{LevelUpload, LevelWrite},
	},
	{
		ID: "media", Name: "Media",
		Description:  "Everyone can watch and listen without a password; the people you choose can add and change files.",
		GuestAccess:  true,
		DefaultLevel: LevelWrite, Levels: []Level{LevelRead, LevelWrite},
	},
	{
		ID: "read-only", Name: "Read-only",
		Description:  "The people you choose can open files, but nobody can change them (archives, finished projects).",
		ReadOnly:     true,
		DefaultLevel: LevelRead, Levels: []Level{LevelRead},
	},
}

// PresetCustom is the preset of a share set up by hand.
const PresetCustom = "custom"

// Preset is one entry of Presets.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	ReadOnly    bool   `json:"readOnly"`
	GuestAccess bool   `json:"guestAccess"`
	// DefaultLevel is given to people added while the preset is chosen;
	// Levels are the ones that make sense for it (the UI may hide others).
	DefaultLevel Level   `json:"defaultLevel" enum:"read,upload,write"`
	Levels       []Level `json:"levels" enum:"read,upload,write"`
}

func validPreset(id string) bool {
	return id == PresetCustom || slices.ContainsFunc(Presets, func(p Preset) bool { return p.ID == id })
}

// Effective is what one user can really do on a share, and why. It is the
// single definition of access: the preview shows it, and the Samba config
// and folder permissions are built from it.
type Effective struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	// Given is the level set for the user; Level is what they get.
	Given  Level  `json:"given" enum:"read,upload,write"`
	Level  Level  `json:"level" enum:"none,read,upload,write"`
	Reason string `json:"reason" doc:"Why, in words for the user."`
}

// EffectiveAccess is the preview of a whole share.
type EffectiveAccess struct {
	Users []Effective `json:"users" nullable:"false"`
	// Guests is what someone without a password gets; Others what every
	// other user with SMB access (not listed) gets.
	Guests Level  `json:"guests" enum:"none,read"`
	Others Level  `json:"others" enum:"none,read"`
	Note   string `json:"note" doc:"One sentence about everyone not listed."`
	// Network says from where people can connect, in words.
	Network string `json:"network"`
}

// networkNote describes network rules in one sentence.
func networkNote(rules []NetworkRule) string {
	allow, block := splitRules(rules)
	switch {
	case len(allow) == 0 && len(block) == 0:
		return "From any address on the network."
	case len(allow) == 0:
		return "From any address except " + strings.Join(block, ", ") + "."
	case len(block) == 0:
		return "Only from " + strings.Join(allow, ", ") + "."
	default:
		return "Only from " + strings.Join(allow, ", ") + ", except " + strings.Join(block, ", ") + "."
	}
}

func splitRules(rules []NetworkRule) (allow, block []string) {
	for _, r := range rules {
		if r.Action == RuleAllow {
			allow = append(allow, r.Address)
		} else {
			block = append(block, r.Address)
		}
	}
	return allow, block
}

// configured is the level a share setting leaves of a given level. It is
// what goes into the Samba config and the folder's ACL.
func configured(given Level, r Rules) (Level, string) {
	switch {
	case r.ReadOnly && given == LevelWrite:
		return LevelRead, "Read only: the share is read-only for everyone."
	case r.ReadOnly && given == LevelUpload:
		return LevelNone, "No access: a read-only share takes no uploads."
	case given == LevelWrite:
		return LevelWrite, "Can read and change files."
	case given == LevelUpload:
		return LevelUpload, "Can add files, but cannot see what is in the folder."
	case r.GuestAccess:
		return LevelRead, "Read only (everyone can read this share)."
	default:
		return LevelRead, "Read only."
	}
}

// effective adds what the user's account allows on top of the share rules.
// blocked means the ransomware guard stopped the user writing.
func effective(a AccessInput, u users.User, r Rules, blocked bool) Effective {
	e := Effective{UserID: a.UserID, Username: u.Username, Given: a.Level}
	switch {
	case !u.SMBAccess:
		e.Level, e.Reason = LevelNone, "No access: the user has no SMB access."
	case u.Disabled:
		e.Level, e.Reason = LevelNone, "No access while the account is disabled."
	default:
		e.Level, e.Reason = configured(a.Level, r)
		if blocked {
			e.Level, e.Reason = guardCap(e.Level, e.Reason)
		}
	}
	return e
}

// guardCap is what a level becomes while the ransomware guard blocks the
// user: they can still read, but change nothing.
func guardCap(level Level, reason string) (Level, string) {
	switch level {
	case LevelWrite:
		return LevelRead, "Read only: the ransomware guard stopped this user writing. An admin can restore access under Security."
	case LevelUpload:
		return LevelNone, "No access: the ransomware guard stopped this user writing. An admin can restore access under Security."
	}
	return level, reason
}

// preview computes the effective access of a whole share.
func preview(access []AccessInput, byID map[int64]users.User, r Rules, blocked map[int64]bool) EffectiveAccess {
	out := EffectiveAccess{Users: []Effective{}, Guests: LevelNone, Others: LevelNone,
		Note: "Nobody else can connect."}
	if r.GuestAccess {
		out.Guests, out.Others = LevelRead, LevelRead
		out.Note = "Everyone else on the network can read, without a password."
	}
	for _, a := range access {
		if u, ok := byID[a.UserID]; ok {
			out.Users = append(out.Users, effective(a, u, r, blocked[a.UserID]))
		}
	}
	return out
}

// spec turns a share into what the system layer applies. Disabled users
// stay in it (Samba and the file workers refuse them anyway), so turning an
// account back on does not need the share to be rewritten. Users the
// ransomware guard blocked (blocked, may be nil) can only read.
func spec(sh Share, byID map[int64]users.User, blocked map[int64]bool) sys.ShareSpec {
	r := Rules{ReadOnly: sh.ReadOnly, GuestAccess: sh.GuestAccess}
	s := sys.ShareSpec{Name: sh.Name, Path: sh.Path, Comment: sh.Comment, Guest: r.GuestAccess, HideUnreadable: sh.HideUnreadable}
	s.Allow, s.Block = splitRules(sh.NetworkRules)
	for _, a := range sh.Access {
		u, ok := byID[a.UserID]
		if !ok || !u.SMBAccess {
			continue // no Samba account to give access to
		}
		level, reason := configured(a.Level, r)
		if blocked[a.UserID] {
			level, _ = guardCap(level, reason)
		}
		switch level {
		case LevelWrite:
			s.Readers = append(s.Readers, u.Username)
			s.Writers = append(s.Writers, u.Username)
		case LevelRead:
			s.Readers = append(s.Readers, u.Username)
		case LevelUpload:
			s.Uploaders = append(s.Uploaders, u.Username)
		}
	}
	return s
}
