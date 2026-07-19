package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Login brute-force lockout: an IP accumulating failLimit failed logins inside
// failWindow is rejected with 429 until the window (measured from the FIRST
// failure) expires. A successful login clears the counter. In-memory only:
// a restart forgets history, which is fine; the goal is slowing online
// guessing, not durable audit.
const (
	failLimit  = 5
	failWindow = 15 * time.Minute
	// maxTrackedIPs bounds the map so a spray of spoofed source addresses
	// cannot grow memory without limit.
	maxTrackedIPs = 4096
)

type failEntry struct {
	count int
	first time.Time // start of this entry's window
}

// loginLimiter is a per-IP fixed-window failure counter. now is injected for
// tests. All methods are safe for concurrent use.
type loginLimiter struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]*failEntry
}

func newLoginLimiter(now func() time.Time) *loginLimiter {
	return &loginLimiter{now: now, m: make(map[string]*failEntry)}
}

// blocked reports whether ip is locked out, and if so for how many more
// seconds (for Retry-After). Expired entries are pruned on sight.
func (l *loginLimiter) blocked(ip string) (retryAfterSeconds int, blocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok {
		return 0, false
	}
	remaining := failWindow - l.now().Sub(e.first)
	if remaining <= 0 {
		delete(l.m, ip)
		return 0, false
	}
	if e.count < failLimit {
		return 0, false
	}
	secs := int(remaining / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, true
}

// fail records a failed login for ip.
func (l *loginLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if e, ok := l.m[ip]; ok && now.Sub(e.first) < failWindow {
		e.count++
		return
	}
	if len(l.m) >= maxTrackedIPs {
		l.evictLocked(now)
	}
	l.m[ip] = &failEntry{count: 1, first: now}
}

// success clears ip's failure history.
func (l *loginLimiter) success(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

// evictLocked drops expired entries; if none were expired it drops the oldest
// entry so the map never exceeds maxTrackedIPs. Caller holds mu.
func (l *loginLimiter) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range l.m {
		if now.Sub(e.first) >= failWindow {
			delete(l.m, k)
			continue
		}
		if oldestKey == "" || e.first.Before(oldest) {
			oldestKey, oldest = k, e.first
		}
	}
	if len(l.m) >= maxTrackedIPs && oldestKey != "" {
		delete(l.m, oldestKey)
	}
}

// clientIP extracts the peer address from RemoteAddr. Deliberately NOT
// X-Forwarded-For: that header is attacker-controlled and would let a client
// dodge the lockout by rotating it. Behind a reverse proxy every request
// shares the proxy's address, so the lockout degrades to a global one,
// acceptable for a single-user app, and documented in the README.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
