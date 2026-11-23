package sambalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Ingester reads log sources and hands every event to its subscribers.
//
// Ingestion never waits for a subscriber: each has its own buffer, and when
// it is full the event is dropped for that subscriber (and counted) while
// the others still get it. A slow activity log can never stall brute-force
// protection, and nothing a client does can make reading the logs stop.
type Ingester struct {
	mu   sync.RWMutex
	subs []*subscriber

	lines, events, tooLong atomic.Uint64
}

type subscriber struct {
	name    string
	ch      chan Event
	dropped atomic.Uint64
}

// Stats says how ingestion is going.
type Stats struct {
	Lines   uint64            `json:"lines"`
	Events  uint64            `json:"events"`
	TooLong uint64            `json:"tooLong" doc:"Lines longer than MaxLine, skipped."`
	Dropped map[string]uint64 `json:"dropped" doc:"Events a subscriber was too busy to take, by subscriber."`
}

func New() *Ingester { return &Ingester{} }

// Subscribe returns a channel with every event from now on. buffer is how
// many events may wait for the subscriber before new ones are dropped.
// Subscribe before starting the sources.
func (in *Ingester) Subscribe(name string, buffer int) <-chan Event {
	s := &subscriber{name: name, ch: make(chan Event, max(buffer, 1))}
	in.mu.Lock()
	in.subs = append(in.subs, s)
	in.mu.Unlock()
	return s.ch
}

// Unsubscribe ends a subscription and closes its channel.
func (in *Ingester) Unsubscribe(ch <-chan Event) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for i, s := range in.subs {
		if s.ch == ch {
			close(s.ch)
			in.subs = append(in.subs[:i], in.subs[i+1:]...)
			return
		}
	}
}

// Emit hands an event that was parsed elsewhere (received from the agent)
// to the subscribers, with the same never-blocking rule.
func (in *Ingester) Emit(ev Event) { in.emit(ev) }

func (in *Ingester) Stats() Stats {
	st := Stats{Lines: in.lines.Load(), Events: in.events.Load(), TooLong: in.tooLong.Load(), Dropped: map[string]uint64{}}
	in.mu.RLock()
	defer in.mu.RUnlock()
	for _, s := range in.subs {
		st.Dropped[s.name] = s.dropped.Load()
	}
	return st
}

func (in *Ingester) emit(ev Event) {
	in.events.Add(1)
	in.mu.RLock()
	defer in.mu.RUnlock()
	for _, s := range in.subs {
		select {
		case s.ch <- ev:
		default:
			s.dropped.Add(1)
		}
	}
}

// Close ends every subscription (after the sources have stopped).
func (in *Ingester) Close() {
	in.mu.Lock()
	defer in.mu.Unlock()
	for _, s := range in.subs {
		close(s.ch)
	}
	in.subs = nil
}

// lines splits a byte stream into lines of at most MaxLine bytes. A longer
// line is skipped as a whole: its bytes are never held.
type lines struct {
	in       *Ingester
	parser   *Parser
	pending  []byte
	skipping bool
}

func (l *lines) write(chunk []byte) {
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		if i < 0 {
			if !l.skipping {
				l.pending = append(l.pending, chunk...)
				if len(l.pending) > MaxLine {
					l.pending, l.skipping = l.pending[:0], true
				}
			}
			return
		}
		part := chunk[:i]
		chunk = chunk[i+1:]
		if l.skipping {
			l.skipping = false
			l.in.lines.Add(1)
			l.in.tooLong.Add(1)
			continue
		}
		var line []byte
		if len(l.pending) > 0 {
			l.pending = append(l.pending, part...)
			line, l.pending = l.pending, l.pending[:0]
		} else {
			line = part
		}
		l.in.lines.Add(1)
		if len(line) > MaxLine {
			l.in.tooLong.Add(1)
			continue
		}
		if ev, ok := l.parser.Line(string(line)); ok {
			l.in.emit(ev)
		}
	}
}

// Read ingests a stream until it ends or ctx is done (a pipe from a log
// command, a socket). A partial last line is ingested at the end.
func (in *Ingester) Read(ctx context.Context, r io.Reader) error {
	l := &lines{in: in, parser: NewParser()}
	buf := make([]byte, 32<<10)
	for ctx.Err() == nil {
		n, err := r.Read(buf)
		l.write(buf[:n])
		if errors.Is(err, io.EOF) {
			if len(l.pending) > 0 && !l.skipping {
				l.write([]byte{'\n'})
			}
			return nil
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// Tail follows a log file like "tail -F": it starts at the end (old lines
// are not replayed), keeps reading as the file grows, starts over when it
// is truncated, and moves to the new file when it is rotated. A file that
// does not exist yet is waited for. It returns when ctx is done.
func (in *Ingester) Tail(ctx context.Context, path string, poll time.Duration) error {
	return in.tail(ctx, path, poll, false)
}

// tail is Tail; fromStart reads an existing file from its beginning (a log
// file that appeared after the reader started).
func (in *Ingester) tail(ctx context.Context, path string, poll time.Duration, fromStart bool) error {
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	l := &lines{in: in, parser: NewParser()}
	buf := make([]byte, 32<<10)
	var (
		f      *os.File
		offset int64
		first  = !fromStart
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	wait := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(poll):
			return true
		}
	}
	for {
		if f == nil {
			var err error
			f, err = os.Open(path)
			if errors.Is(err, fs.ErrNotExist) {
				first = false // a file that appears later is read from its start
				if !wait() {
					return nil
				}
				continue
			}
			if err != nil {
				return err
			}
			offset = 0
			if first {
				offset, _ = f.Seek(0, io.SeekEnd)
				first = false
			}
		}
		// Read what is there.
		for {
			n, err := f.Read(buf)
			offset += int64(n)
			l.write(buf[:n])
			if n == 0 || err != nil {
				break
			}
		}
		if !wait() {
			return nil
		}
		// Rotated (the name is another file now) or truncated?
		cur, err := f.Stat()
		named, nameErr := os.Stat(path)
		switch {
		case err != nil:
			f.Close()
			f = nil
		case nameErr == nil && !os.SameFile(cur, named):
			// Finish the old file, then the new one from its start.
			for {
				n, _ := f.Read(buf)
				l.write(buf[:n])
				if n == 0 {
					break
				}
			}
			f.Close()
			f = nil
			l.pending, l.skipping = l.pending[:0], false
			slog.DebugContext(ctx, "log rotated, following the new file", "path", path)
		case cur.Size() < offset:
			if _, err := f.Seek(0, io.SeekStart); err == nil {
				offset = 0
				l.pending, l.skipping = l.pending[:0], false
			}
		}
	}
}
