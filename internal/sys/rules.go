package sys

import (
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This file holds the rules for what may go into the Samba config. They are
// used twice on purpose: by services to give the user a clear error, and by
// the system layer itself (RenderShares, and the root agent) as the real
// enforcement, so no caller can ever get an unchecked value into the config.
//
// The smb.conf format has no escaping. A value runs to the end of its line,
// a trailing backslash joins the next line, and % starts a variable that
// Samba replaces (%U = the connecting user...). So safety comes from
// refusing the characters that carry meaning, not from quoting.

var shareName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$`)

// reservedNames have a special meaning to Samba or Windows.
var reservedNames = map[string]bool{
	"global": true, "homes": true, "printers": true, "netlogon": true, "sysvol": true,
	"ipc": true, "admin": true, "print": true,
}

// ValidShareName reports whether a name is safe and not reserved.
func ValidShareName(name string) bool {
	return shareName.MatchString(name) && !reservedNames[strings.ToLower(name)]
}

// safeValue reports whether free text (a path or comment) can be written
// into the config as it is.
func safeValue(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r == '%' || r == '\\' || unicode.IsControl(r) || r == ' ' || r == ' ' {
			return false
		}
	}
	return true
}

const (
	maxPathLen    = 1024
	maxCommentLen = 100
)

// ValidSharePath reports whether p is a clean, absolute, safe path. Any unicode
// letter is allowed (folders are often named in the owner's language).
func ValidSharePath(p string) bool {
	if len(p) > maxPathLen || !path.IsAbs(p) || path.Clean(p) != p || !safeValue(p) {
		return false
	}
	// Samba trims spaces around a value, so " x" and "x " would silently
	// point somewhere else than intended.
	for _, part := range strings.Split(p, "/") {
		if part != strings.TrimSpace(part) {
			return false
		}
	}
	return true
}

func ValidShareComment(c string) bool {
	return len(c) <= maxCommentLen && safeValue(c) && c == strings.TrimSpace(c)
}

// systemDirs are never shared, and neither is anything inside them: they
// hold the operating system, secrets, or other programs' data. The check
// runs on the real path (symbolic links resolved).
var systemDirs = []string{
	"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32",
	"/proc", "/root", "/run", "/sbin", "/sys", "/tmp", "/usr", "/var",
}

// SharablePath reports whether a real path may be shared.
func SharablePath(real string) bool {
	if real == "/" {
		return false
	}
	for _, dir := range systemDirs {
		if real == dir || strings.HasPrefix(real, dir+"/") {
			return false
		}
	}
	return true
}

// Overlaps reports whether sharing real would expose dir: real is dir itself,
// is inside it, or is a folder that contains it.
func Overlaps(real, dir string) bool {
	return real == dir || strings.HasPrefix(real, dir+"/") || strings.HasPrefix(dir, real+"/")
}
