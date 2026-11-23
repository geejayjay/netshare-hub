package sambalog

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"
)

// MaxFiles is the most log files TailDir follows at once. Samba writes one
// log file per client name ("log.%m"), and a client chooses its own name:
// without a limit, a client inventing names could make the reader start
// one follower per name, without end.
const MaxFiles = 1024

// TailDir follows every file matching pattern (a filepath.Match pattern,
// e.g. "/var/log/samba/log.*"), like Tail. Files there when it starts are
// read from their end; files that appear later from their start, so the
// first lines of a new client are not lost. Rotated copies ("*.old") are
// left out. It returns when ctx is done.
func (in *Ingester) TailDir(ctx context.Context, pattern string, poll time.Duration) error {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	following := map[string]bool{}
	warned := false
	scan := func(start bool) {
		files, err := filepath.Glob(pattern)
		if err != nil {
			return
		}
		for _, f := range files {
			if following[f] || strings.HasSuffix(f, ".old") {
				continue
			}
			if len(following) >= MaxFiles {
				if !warned {
					slog.WarnContext(ctx, "too many Samba log files; not following more", "limit", MaxFiles, "pattern", pattern)
					warned = true
				}
				return
			}
			following[f] = true
			go in.tail(ctx, f, poll, !start)
		}
	}
	scan(true)
	tick := time.NewTicker(4 * poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			scan(false)
		}
	}
}
