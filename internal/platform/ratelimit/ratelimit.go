// Package ratelimit counts failed attempts per key and blocks a key that
// fails too often. It is used for login now and SMB brute-force protection
// later, so there is one implementation.
package ratelimit

import (
	"sync"
	"time"
)

type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	entries map[string]*entry
}

type entry struct {
	failures int
	resetAt  time.Time
}

// New allows max failures per window for each key.
func New(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, entries: map[string]*entry{}}
}

// Blocked reports whether key is locked out, and for how many more seconds.
func (l *Limiter) Blocked(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.live(key)
	if e == nil || e.failures < l.max {
		return false, 0
	}
	return true, int(e.resetAt.Sub(l.now()).Seconds()) + 1
}

// Fail records one failed attempt.
func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.live(key)
	if e == nil {
		e = &entry{resetAt: l.now().Add(l.window)}
		l.entries[key] = e
	}
	e.failures++
}

// Reset clears the counter after a success.
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// live returns the entry for key, dropping it first if its window has passed.
func (l *Limiter) live(key string) *entry {
	e := l.entries[key]
	if e != nil && !l.now().Before(e.resetAt) {
		delete(l.entries, key)
		return nil
	}
	return e
}
