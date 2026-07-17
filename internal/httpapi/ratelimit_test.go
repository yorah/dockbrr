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
