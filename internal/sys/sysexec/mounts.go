package sysexec

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"netshare-hub/internal/sys"
)

// InspectMounts reports mount health for share folders. It reads this
// process's mount table, so it must run where the real mounts are visible:
// in the root agent, never in the sandboxed web server (systemd's
// ProtectSystem=strict remounts everything read-only for that process).
func (f *Files) InspectMounts(_ context.Context, paths []string) (sys.MountReport, error) {
	if len(paths) > sys.MaxMountPaths {
		return sys.MountReport{}, fmt.Errorf("%w: at most %d paths", sys.ErrInvalid, sys.MaxMountPaths)
	}
	return inspectMounts(paths), nil
}

type mountInfoEntry struct {
	MountPoint string
	FSType     string
	ReadOnly   bool
}

// parseMountInfo parses Linux /proc/self/mountinfo lines.
func parseMountInfo(r io.Reader) []mountInfoEntry {
	var out []mountInfoEntry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		left := strings.Fields(line[:sep])
		right := strings.Fields(line[sep+3:])
		if len(left) < 6 || len(right) < 1 {
			continue
		}
		mp := unescapeMountPath(left[4])
		ro := hasOption(left[5], "ro")
		if len(right) >= 3 && hasOption(right[2], "ro") {
			ro = true
		}
		out = append(out, mountInfoEntry{
			MountPoint: mp,
			FSType:     right[0],
			ReadOnly:   ro,
		})
	}
	return out
}

func hasOption(opts, want string) bool {
	for _, o := range strings.Split(opts, ",") {
		if o == want {
			return true
		}
	}
	return false
}

func unescapeMountPath(s string) string {
	s = strings.ReplaceAll(s, `\040`, " ")
	s = strings.ReplaceAll(s, `\011`, "\t")
	s = strings.ReplaceAll(s, `\012`, "\n")
	s = strings.ReplaceAll(s, `\134`, `\`)
	return s
}

// bestMountFor returns the most specific mount that contains target.
func bestMountFor(target string, mounts []mountInfoEntry) (mountInfoEntry, bool) {
	clean := filepath.ToSlash(filepath.Clean(target))
	var best mountInfoEntry
	found := false
	for _, m := range mounts {
		mp := filepath.ToSlash(filepath.Clean(m.MountPoint))
		if mp == "/" {
			if !found || len(mp) > len(best.MountPoint) {
				best = m
				found = true
			}
			continue
		}
		if clean == mp || strings.HasPrefix(clean, mp+"/") {
			if !found || len(mp) > len(best.MountPoint) {
				best = m
				found = true
			}
		}
	}
	return best, found
}
