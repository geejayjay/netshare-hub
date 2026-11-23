package guard

import (
	"fmt"
	"net/netip"
	"testing"
	"time"

	"netshare-hub/internal/sys/sambalog"
)

var pc = netip.MustParseAddr("192.168.1.20")

func rename(at time.Time, from, to string) sambalog.Event {
	return sambalog.Event{Kind: sambalog.KindFile, Time: at, User: "jay", ClientIP: pc, Share: "docs",
		Op: "renameat", OK: true, Path: "/srv/docs/" + from, Path2: "/srv/docs/" + to}
}

func write(at time.Time, p string) sambalog.Event {
	return sambalog.Event{Kind: sambalog.KindFile, Time: at, User: "jay", ClientIP: pc, Share: "docs",
		Op: "openat", Write: true, OK: true, Path: "/srv/docs/" + p}
}

func TestSuspiciousRename(t *testing.T) {
	for from, to := range map[string]string{
		"report.docx": "report.docx.locked",
		"photo.jpg":   "photo.jpg.x7k2q",
		"budget.xlsx": "budget.encrypted",
		"notes.txt":   "notes.txt.ryuk",
		"a.pdf":       "a.pdf.id-8F2C9A1B.[help@mail.to].xtbl",
	} {
		if !SuspiciousRename(from, to) {
			t.Errorf("%s -> %s should be suspicious", from, to)
		}
	}
	for from, to := range map[string]string{
		"IMG_0001.jpg":    "Holiday 2025.jpg", // a new name, same kind of file
		"Projects/a.docx": "Archive/a.docx",   // a move
		"draft.txt":       "draft.md",         // a common extension
		"video.mov":       "video.mp4",
		"Folder":          "Folder 2025", // a folder
		"report.docx":     "report.docx.bak",
		"download.part":   "film.mkv",
		"x":               "y",
	} {
		if SuspiciousRename(from, to) {
			t.Errorf("%s -> %s should not be suspicious", from, to)
		}
	}
}

// The step's "done when", part 1: a mass rename to an encrypted extension
// is caught.
func TestMassRenameTriggers(t *testing.T) {
	d := NewDetector(DefaultThresholds)
	start := time.Now()
	var f Finding
	var fired bool
	for i := 0; i < 25 && !fired; i++ {
		f, fired = d.Observe(rename(start.Add(time.Duration(i)*time.Second), fmt.Sprintf("f%02d.docx", i), fmt.Sprintf("f%02d.docx.locked", i)))
	}
	if !fired || f.Rule != "renames" || f.Count != 20 || f.User != "jay" || f.ClientIP != pc || f.Shares[0] != "docs" {
		t.Fatalf("finding: %+v %v", f, fired)
	}
	// Then quiet for a while: the alert counts up instead of new findings.
	if _, again := d.Observe(rename(start.Add(30*time.Second), "g.docx", "g.docx.locked")); again {
		t.Error("reported twice in a row")
	}
}

func TestRansomNotesInManyFoldersTrigger(t *testing.T) {
	d := NewDetector(DefaultThresholds)
	now := time.Now()
	d.Observe(write(now, "A/HOW_TO_DECRYPT.txt"))
	d.Observe(write(now, "A/HOW_TO_DECRYPT.txt")) // same folder again
	if _, fired := d.Observe(write(now, "B/HOW_TO_DECRYPT.txt")); fired {
		t.Fatal("two folders is not enough")
	}
	f, fired := d.Observe(write(now, "C/HOW_TO_DECRYPT.txt"))
	if !fired || f.Rule != "ransom_notes" || f.Count != 3 {
		t.Errorf("finding: %+v %v", f, fired)
	}
}

// The step's "done when", part 2: normal use does not trigger it.
func TestNormalUseDoesNotTrigger(t *testing.T) {
	d := NewDetector(DefaultThresholds)
	now := time.Now()
	check := func(what string, ev sambalog.Event) {
		t.Helper()
		if f, fired := d.Observe(ev); fired {
			t.Fatalf("%s triggered the guard: %+v", what, f)
		}
	}
	// Copying a big folder: thousands of writes.
	for i := 0; i < 5000; i++ {
		check("a big copy", write(now, fmt.Sprintf("Backup/file-%05d.jpg", i)))
	}
	// Moving 1000 files into another folder: 1000 renames, names kept.
	for i := 0; i < 1000; i++ {
		check("a big move", rename(now, fmt.Sprintf("Inbox/doc-%04d.pdf", i), fmt.Sprintf("Archive/doc-%04d.pdf", i)))
	}
	// Renaming a batch of photos.
	for i := 0; i < 300; i++ {
		check("renaming photos", rename(now, fmt.Sprintf("IMG_%04d.JPG", i), fmt.Sprintf("Holiday %03d.jpg", i)))
	}
	// Converting documents: an extension change, to common types.
	for i := 0; i < 100; i++ {
		check("converting", rename(now, fmt.Sprintf("doc%d.txt", i), fmt.Sprintf("doc%d.md", i)))
	}
	// One README is not a ransom note campaign.
	check("a readme", write(now, "Project/README.md"))
	// Spread out over hours, even unusual renames stay under the rate.
	for i := 0; i < 50; i++ {
		check("slow unusual renames", rename(now.Add(time.Duration(i)*5*time.Minute), fmt.Sprintf("s%d.dat", i), fmt.Sprintf("s%d.dat.sig", i)))
	}
}

// Two people (or one person on two computers) are counted apart.
func TestCountedPerUserAndComputer(t *testing.T) {
	d := NewDetector(DefaultThresholds)
	now := time.Now()
	for i := 0; i < 15; i++ {
		d.Observe(rename(now, fmt.Sprintf("a%d.doc", i), fmt.Sprintf("a%d.doc.locked", i)))
		other := rename(now, fmt.Sprintf("b%d.doc", i), fmt.Sprintf("b%d.doc.locked", i))
		other.ClientIP = netip.MustParseAddr("192.168.1.21")
		if f, fired := d.Observe(other); fired {
			t.Fatalf("15 + 15 from two computers added up: %+v", f)
		}
	}
}

func TestMemoryStaysBounded(t *testing.T) {
	d := NewDetector(DefaultThresholds)
	now := time.Now()
	for i := 0; i < maxTracked+500; i++ {
		ev := rename(now, "a.doc", "a.doc.locked")
		ev.User = fmt.Sprintf("u%d", i)
		d.Observe(ev)
	}
	if len(d.seen) > maxTracked {
		t.Errorf("tracking %d users and computers, limit %d", len(d.seen), maxTracked)
	}
}
