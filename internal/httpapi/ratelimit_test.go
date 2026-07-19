package httpapi

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLoginLimiterBlocksAfterLimit(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	for i := 0; i < failLimit; i++ {
		if _, blocked := l.blocked("10.0.0.1"); blocked {
			t.Fatalf("blocked after %d failures, want unblocked below limit", i)
		}
		l.fail("10.0.0.1")
	}
	retry, blocked := l.blocked("10.0.0.1")
	if !blocked {
		t.Fatal("not blocked after failLimit failures")
	}
	if retry <= 0 || retry > int(failWindow/time.Second) {
		t.Fatalf("retry-after = %d, want within (0, %d]", retry, int(failWindow/time.Second))
	}
}

func TestLoginLimiterWindowExpiryUnblocks(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit; i++ {
		l.fail("10.0.0.1")
	}
	now = now.Add(failWindow + time.Second)
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("still blocked after window expired")
	}
	// Expired entry must not linger: next failure starts a fresh count of 1.
	l.fail("10.0.0.1")
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("blocked after a single post-expiry failure")
	}
}

func TestLoginLimiterSuccessResets(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit-1; i++ {
		l.fail("10.0.0.1")
	}
	l.success("10.0.0.1")
	l.fail("10.0.0.1")
	if _, blocked := l.blocked("10.0.0.1"); blocked {
		t.Fatal("success did not reset the failure count")
	}
}

func TestLoginLimiterPerIPIsolation(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < failLimit; i++ {
		l.fail("10.0.0.1")
	}
	if _, blocked := l.blocked("10.0.0.2"); blocked {
		t.Fatal("unrelated IP blocked")
	}
}

func TestLoginLimiterEvictsAtCap(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })
	for i := 0; i < maxTrackedIPs+10; i++ {
		l.fail(fmt.Sprintf("10.0.%d.%d", i/256, i%256))
	}
	if n := len(l.m); n > maxTrackedIPs {
		t.Fatalf("map grew to %d entries, cap is %d", n, maxTrackedIPs)
	}
}

// TestLoginLimiterEvictionDropsExpiredFirst drives the cap-overflow path with
// a mix of expired and live entries: eviction must clear every expired entry
// and keep the live ones (no oldest-live eviction needed once pruning made
// room).
func TestLoginLimiterEvictionDropsExpiredFirst(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	// 100 entries that will be expired by the time the map hits the cap.
	for i := 0; i < 100; i++ {
		l.fail(fmt.Sprintf("old-%d", i))
	}
	now = now.Add(failWindow + time.Second)
	// Fill to exactly the cap with live entries.
	for i := 0; len(l.m) < maxTrackedIPs; i++ {
		l.fail(fmt.Sprintf("live-%d", i))
	}

	l.fail("trigger") // at cap -> evictLocked runs

	if _, ok := l.m["old-0"]; ok {
		t.Fatal("expired entry survived eviction")
	}
	if _, ok := l.m["old-99"]; ok {
		t.Fatal("expired entry survived eviction")
	}
	if _, ok := l.m["live-0"]; !ok {
		t.Fatal("live entry evicted although expired pruning made room")
	}
	if _, ok := l.m["trigger"]; !ok {
		t.Fatal("triggering entry not recorded")
	}
	if want := maxTrackedIPs - 100 + 1; len(l.m) != want {
		t.Fatalf("map size = %d after eviction, want %d", len(l.m), want)
	}
}

// TestLoginLimiterEvictionDropsOldestWhenNoneExpired pins the tie-breaker:
// with the map at cap and nothing expired, exactly the entry with the oldest
// window start is dropped.
func TestLoginLimiterEvictionDropsOldestWhenNoneExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLoginLimiter(func() time.Time { return now })

	// Staggered first-failure times, all inside the window; ip-0 is oldest.
	for i := 0; i < maxTrackedIPs; i++ {
		l.fail(fmt.Sprintf("ip-%d", i))
		now = now.Add(time.Millisecond)
	}

	l.fail("trigger")

	if _, ok := l.m["ip-0"]; ok {
		t.Fatal("oldest entry survived eviction with the map at cap")
	}
	if _, ok := l.m["ip-1"]; !ok {
		t.Fatal("second-oldest entry evicted; only the oldest should be")
	}
	if _, ok := l.m["trigger"]; !ok {
		t.Fatal("triggering entry not recorded")
	}
	if len(l.m) != maxTrackedIPs {
		t.Fatalf("map size = %d, want exactly the cap %d", len(l.m), maxTrackedIPs)
	}
}

func TestClientIPStripsPort(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "192.0.2.7:51234"
	if got := clientIP(r); got != "192.0.2.7" {
		t.Fatalf("clientIP = %q, want 192.0.2.7", got)
	}
	// Malformed RemoteAddr (no port) degrades to the raw string, never "".
	r.RemoteAddr = "192.0.2.7"
	if got := clientIP(r); got != "192.0.2.7" {
		t.Fatalf("clientIP(no port) = %q, want 192.0.2.7", got)
	}
}
