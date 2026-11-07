// Package ratelimit provides per-key rate limiting and failure lockouts, kept in memory
// with automatic eviction of idle keys.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const gcEvery = time.Minute

// Limiter is a set of token buckets, one per key.
type Limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	m      map[string]*bucket
	lastGC time.Time
}

type bucket struct {
	lim  *rate.Limiter
	per  int
	seen time.Time
}

func NewLimiter() *Limiter { return &Limiter{now: time.Now, m: map[string]*bucket{}} }

// Allow consumes one event for key, permitting perMinute events per minute (burst = perMinute).
func (l *Limiter) Allow(key string, perMinute int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastGC) > gcEvery {
		for k, b := range l.m {
			if now.Sub(b.seen) > 2*time.Minute { // a bucket is full again after one minute
				delete(l.m, k)
			}
		}
		l.lastGC = now
	}
	b := l.m[key]
	if b == nil || b.per != perMinute {
		b = &bucket{lim: rate.NewLimiter(rate.Limit(float64(perMinute)/60), perMinute), per: perMinute}
		l.m[key] = b
	}
	b.seen = now
	return b.lim.AllowN(now, 1)
}

// Available reports whether Allow would currently succeed for key, without using up an event.
func (l *Limiter) Available(key string, perMinute int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.m[key]
	if b == nil || b.per != perMinute {
		return true // a fresh bucket starts full
	}
	return b.lim.TokensAt(now) >= 1
}

// Lockout locks a key out after repeated failures.
type Lockout struct {
	mu     sync.Mutex
	now    func() time.Time
	m      map[string]*lockEntry
	lastGC time.Time
}

type lockEntry struct {
	fails   int
	start   time.Time
	until   time.Time // locked while now < until
	expires time.Time // entry can be dropped after this
}

func NewLockout() *Lockout { return &Lockout{now: time.Now, m: map[string]*lockEntry{}} }

// Locked reports whether key is currently locked out.
func (l *Lockout) Locked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.m[key]
	return e != nil && l.now().Before(e.until)
}

// Fail records a failure. After threshold failures within one lock duration the key is locked
// for that duration; Fail then returns true.
func (l *Lockout) Fail(key string, threshold int, lock time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastGC) > gcEvery {
		for k, e := range l.m {
			if now.After(e.expires) {
				delete(l.m, k)
			}
		}
		l.lastGC = now
	}
	e := l.m[key]
	if e == nil {
		e = &lockEntry{start: now}
		l.m[key] = e
	}
	if now.Sub(e.start) > lock { // old failures age out
		e.fails, e.start = 0, now
	}
	e.fails++
	e.expires = e.start.Add(lock)
	if e.fails >= threshold {
		e.until = now.Add(lock)
		e.fails, e.start = 0, now
		e.expires = e.until
		return true
	}
	return false
}
