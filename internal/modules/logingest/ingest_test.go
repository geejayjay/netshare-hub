package logingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fileLine = "  nshaudit|jay|192.168.1.20|media|DESKTOP|mkdirat|ok|/srv/media/new\n"

// A subscriber that never reads must not slow down ingestion or the others.
func TestSlowSubscriberNeverBlocks(t *testing.T) {
	in := New()
	fast := in.Subscribe("fast", 20000)
	in.Subscribe("stuck", 1) // never read

	const n = 10000
	done := make(chan error, 1)
	go func() { done <- in.Read(context.Background(), strings.NewReader(strings.Repeat(fileLine, n))) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ingestion blocked on a subscriber that does not read")
	}
	if got := len(fast); got != n {
		t.Errorf("the reading subscriber got %d of %d events", got, n)
	}
	st := in.Stats()
	if st.Events != n || st.Dropped["stuck"] != n-1 || st.Dropped["fast"] != 0 {
		t.Errorf("stats: %+v", st)
	}
}

// Long lines, binary noise and lines split across reads.
func TestReadSurvivesHostileStreams(t *testing.T) {
	in := New()
	out := in.Subscribe("test", 100)
	huge := strings.Repeat("A", 5*MaxLine) // one huge line: skipped, never held
	stream := fileLine + huge + "\n" + "\x00\xff\xfe\n" + fileLine + "no newline at the end " + strings.TrimSuffix(fileLine, "\n")
	if err := in.Read(context.Background(), &chunked{s: stream, n: 7}); err != nil {
		t.Fatal(err)
	}
	// The two good lines, and the last one: it has no newline, but ends the
	// stream, and holds an audit line after some text (as syslog writes it).
	if len(out) != 3 {
		t.Errorf("got %d events, want 3", len(out))
	}
	if st := in.Stats(); st.TooLong != 1 {
		t.Errorf("too-long lines: %+v", st)
	}
}

// chunked returns a string a few bytes at a time, like a slow pipe.
type chunked struct {
	s string
	n int
}

func (c *chunked) Read(p []byte) (int, error) {
	if c.s == "" {
		return 0, os.ErrClosed
	}
	n := copy(p[:min(len(p), c.n)], c.s)
	c.s = c.s[n:]
	if c.s == "" {
		return n, ioEOF
	}
	return n, nil
}

var ioEOF = func() error { _, err := strings.NewReader("").Read(make([]byte, 1)); return err }()

func TestTailFollowsGrowthTruncationAndRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.smbd")
	os.WriteFile(path, []byte(strings.Repeat(fileLine, 3)), 0o644) // old lines: not replayed

	in := New()
	out := in.Subscribe("test", 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go in.Tail(ctx, path, 20*time.Millisecond)

	waitFor := func(n int, what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for len(out) < n && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if len(out) != n {
			t.Fatalf("%s: %d events, want %d", what, len(out), n)
		}
		for len(out) > 0 {
			<-out
		}
	}
	appendLine := func(s string) {
		f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString(s)
		f.Close()
	}

	time.Sleep(100 * time.Millisecond)
	waitFor(0, "old lines")
	appendLine(fileLine + fileLine)
	waitFor(2, "growth")

	// A line written in two parts is one event.
	appendLine(fileLine[:20])
	time.Sleep(60 * time.Millisecond)
	appendLine(fileLine[20:])
	waitFor(1, "split line")

	// Truncated (logrotate copytruncate): start over.
	os.WriteFile(path, nil, 0o644)
	time.Sleep(60 * time.Millisecond)
	appendLine(fileLine)
	waitFor(1, "after truncation")

	// Rotated: the name is a new file now; it is read from its start.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Skipf("this system cannot rename an open file (%v); rotation is covered on Linux", err)
	}
	os.WriteFile(path, []byte(fileLine+fileLine+fileLine), 0o644)
	waitFor(3, "after rotation")
}

func TestTailWaitsForAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "later.log")
	in := New()
	out := in.Subscribe("test", 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go in.Tail(ctx, path, 20*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	os.WriteFile(path, []byte(fileLine), 0o644)
	deadline := time.Now().Add(3 * time.Second)
	for len(out) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(out) != 1 {
		t.Errorf("a file created later is read from its start: %d events", len(out))
	}
}
