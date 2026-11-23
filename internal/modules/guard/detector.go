// Package guard is the ransomware guard: it watches what each user does over
// SMB (from the activity events) and reacts when it looks like ransomware
// encrypting files.
//
// What it looks for is what ransomware does and normal work does not:
//
//   - Renames that change a file's extension to an unusual one
//     ("report.docx" -> "report.docx.locked"). Plain renames and moves keep
//     the extension and are not counted: dragging 500 files into another
//     folder is 500 renames, and perfectly normal.
//   - Ransom notes ("HOW_TO_DECRYPT.txt", "_readme.txt"...) written into
//     several different folders.
//
// Many writes alone are not counted: copying a big folder is exactly that.
package guard

import (
	"net/netip"
	"path"
	"slices"
	"strings"
	"time"

	"netshare-hub/internal/sys/sambalog"
)

// Thresholds say when activity is suspicious, within Window.
type Thresholds struct {
	Window time.Duration
	// Renames is how many suspicious renames by one user from one computer
	// trigger the guard.
	Renames int
	// NoteFolders is in how many different folders a ransom note must
	// appear.
	NoteFolders int
}

// DefaultThresholds are tuned to catch encryption early and stay quiet for
// normal work; learning mode is there to check them on a real server first.
var DefaultThresholds = Thresholds{Window: time.Minute, Renames: 20, NoteFolders: 3}

// Finding is what the detector reports.
type Finding struct {
	User     string
	ClientIP netip.Addr
	Rule     string // "renames" or "ransom_notes"
	Count    int
	Shares   []string
	Samples  []string // some of the paths involved
	At       time.Time
}

// knownBad are extensions ransomware families are known to use.
var knownBad = map[string]bool{
	".locked": true, ".encrypted": true, ".enc": true, ".crypt": true, ".crypted": true, ".cry": true,
	".crypto": true, ".locky": true, ".zepto": true, ".cerber": true, ".wcry": true, ".wncry": true, ".wnry": true,
	".ryk": true, ".ryuk": true, ".conti": true, ".lockbit": true, ".djvu": true, ".stop": true, ".kraken": true,
	".petya": true, ".odin": true, ".thor": true, ".aesir": true, ".zzzzz": true, ".micro": true, ".vvv": true,
	".ecc": true, ".ezz": true, ".exx": true, ".xyz": true, ".ttt": true, ".abc": true, ".aaa": true, ".pays": true,
	".ransom": true, ".hydra": true, ".lock": true, ".crinf": true, ".r5a": true, ".xrnt": true, ".xtbl": true,
}

// common are extensions people rename files to all the time: changing to
// one of these is never suspicious by itself.
var common = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`.txt .md .rtf .doc .docx .odt .pdf .xls .xlsx .ods .csv .ppt .pptx .odp
		.jpg .jpeg .png .gif .bmp .tif .tiff .webp .heic .heif .raw .cr2 .cr3 .nef .arw .dng .svg .psd .ai .xcf
		.mp3 .m4a .aac .wav .flac .ogg .opus .wma .mid .mp4 .m4v .mkv .mov .avi .wmv .webm .mpg .mpeg .3gp .srt .sub .ass
		.zip .7z .rar .tar .gz .tgz .bz2 .xz .zst .iso .img .dmg
		.html .htm .css .js .ts .tsx .jsx .json .xml .yaml .yml .toml .ini .conf .cfg .log .sql .db .sqlite
		.go .py .rb .java .kt .c .h .cpp .hpp .cs .rs .php .sh .bat .ps1 .swift .lua .pl .r
		.bak .old .orig .tmp .part .crdownload .download .backup .eml .msg .pst .ics .vcf .epub .mobi .azw3
		.blend .stl .obj .fbx .dwg .dxf .skp .indd .key .pages .numbers .exe .msi .apk .deb .rpm .appimage`) {
		common[e] = true
	}
}

// ext is a lower-case extension; "" when there is none (or a dotfile).
func ext(name string) string {
	base := path.Base(name)
	i := strings.LastIndex(base, ".")
	if i <= 0 {
		return ""
	}
	return strings.ToLower(base[i:])
}

// SuspiciousRename reports whether a rename looks like encryption: the
// extension changes, to a known-bad one or to one that is not common.
func SuspiciousRename(from, to string) bool {
	oldExt, newExt := ext(from), ext(to)
	if newExt == "" || newExt == oldExt {
		return false
	}
	if knownBad[newExt] {
		return true
	}
	if common[newExt] {
		return false
	}
	// An unknown extension: suspicious when it is added on top of the old
	// name ("a.docx" -> "a.docx.x7k2") or the name became gibberish-like.
	return oldExt != "" || len(newExt) > 5
}

var noteMarkers = []string{
	"decrypt", "ransom", "restore_files", "restore-files", "recover_files", "recover-files", "recovery_key",
	"how_to_back", "how-to-back", "your_files", "your-files", "unlock_files", "_readme.txt", "readme_for",
	"files_encrypted", "files-encrypted", "!!!",
}

// RansomNote reports whether a file name looks like a ransom note.
func RansomNote(p string) bool {
	name := strings.ToLower(path.Base(p))
	for _, m := range noteMarkers {
		if strings.Contains(name, m) {
			return true
		}
	}
	return false
}

// maxTracked bounds memory: who is being watched at once. Beyond it the
// oldest entry is forgotten.
const maxTracked = 10000

type who struct {
	user string
	ip   netip.Addr
}

type state struct {
	renames []time.Time
	notes   map[string]time.Time // folder -> when
	shares  []string
	samples []string
	last    time.Time
	fired   time.Time
}

// Detector watches events per user and computer. It is not safe for
// concurrent use: one goroutine feeds it.
type Detector struct {
	th       Thresholds
	cooldown time.Duration
	seen     map[who]*state
}

func NewDetector(th Thresholds) *Detector {
	if th.Window <= 0 {
		th.Window = DefaultThresholds.Window
	}
	return &Detector{th: th, cooldown: 10 * time.Minute, seen: map[who]*state{}}
}

// Observe takes one event and reports a finding when a threshold is
// crossed. After a finding, the same user and computer are not reported
// again for a while (the alert counts up instead).
func (d *Detector) Observe(ev sambalog.Event) (Finding, bool) {
	if ev.Kind != sambalog.KindFile || !ev.OK || ev.User == "" {
		return Finding{}, false
	}
	var rule string
	switch {
	case ev.Op == "renameat" && SuspiciousRename(ev.Path, ev.Path2):
		rule = "renames"
	case (ev.Op == "openat" && ev.Write || ev.Op == "renameat") && RansomNote(firstNonEmpty(ev.Path2, ev.Path)):
		rule = "ransom_notes"
	default:
		return Finding{}, false
	}
	now := ev.Time
	if now.IsZero() {
		now = time.Now()
	}
	k := who{ev.User, ev.ClientIP}
	st := d.seen[k]
	if st == nil {
		d.forgetOld(now)
		st = &state{notes: map[string]time.Time{}}
		d.seen[k] = st
	}
	st.last = now
	if !slices.Contains(st.shares, ev.Share) {
		st.shares = append(st.shares, ev.Share)
	}
	sample := firstNonEmpty(ev.Path2, ev.Path)
	if len(st.samples) < 20 && !slices.Contains(st.samples, sample) {
		st.samples = append(st.samples, sample)
	}

	cutoff := now.Add(-d.th.Window)
	count := 0
	switch rule {
	case "renames":
		st.renames = append(st.renames, now)
		i := 0
		for i < len(st.renames) && st.renames[i].Before(cutoff) {
			i++
		}
		st.renames = st.renames[i:]
		count = len(st.renames)
		if count < d.th.Renames {
			return Finding{}, false
		}
	case "ransom_notes":
		st.notes[path.Dir(sample)] = now
		for folder, at := range st.notes {
			if at.Before(cutoff) {
				delete(st.notes, folder)
			}
		}
		count = len(st.notes)
		if count < d.th.NoteFolders {
			return Finding{}, false
		}
	}
	if !st.fired.IsZero() && now.Sub(st.fired) < d.cooldown {
		return Finding{}, false
	}
	st.fired = now
	return Finding{User: ev.User, ClientIP: ev.ClientIP, Rule: rule, Count: count,
		Shares: slices.Clone(st.shares), Samples: slices.Clone(st.samples), At: now}, true
}

// forgetOld drops quiet entries, and the oldest one if there are too many.
func (d *Detector) forgetOld(now time.Time) {
	if len(d.seen) < maxTracked {
		return
	}
	var oldest who
	var oldestAt time.Time
	for k, st := range d.seen {
		if now.Sub(st.last) > d.th.Window && now.Sub(st.fired) > d.cooldown {
			delete(d.seen, k)
			continue
		}
		if oldestAt.IsZero() || st.last.Before(oldestAt) {
			oldest, oldestAt = k, st.last
		}
	}
	if len(d.seen) >= maxTracked {
		delete(d.seen, oldest)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
