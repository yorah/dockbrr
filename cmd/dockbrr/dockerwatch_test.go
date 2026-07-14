package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePinger struct {
	mu  sync.Mutex
	err error
	n   int
}

func (f *fakePinger) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return f.err
}

func (f *fakePinger) set(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func TestWaitForDockerRetriesUntilDialSucceeds(t *testing.T) {
	var attempts atomic.Int32
	dial := func(context.Context) (string, error) {
		if attempts.Add(1) < 3 {
			return "", errors.New("connection refused")
		}
		return "client", nil
	}

	got, ok := waitForDocker(t.Context(), time.Millisecond, dial)
	if !ok {
		t.Fatal("waitForDocker gave up, want success")
	}
	if got != "client" {
		t.Fatalf("client = %q, want %q", got, "client")
	}
	if n := attempts.Load(); n != 3 {
		t.Fatalf("dial attempts = %d, want 3", n)
	}
}

func TestWaitForDockerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dial := func(context.Context) (string, error) { return "", errors.New("connection refused") }

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := waitForDocker(ctx, time.Millisecond, dial); ok {
			t.Error("waitForDocker reported success after cancel, want failure")
		}
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForDocker did not return after context cancel")
	}
}

func TestWatchDockerLivenessLogsTransitionsOnly(t *testing.T) {
	p := &fakePinger{}
	var (
		mu    sync.Mutex
		calls []string
	)
	record := func(s string) func(error) {
		return func(error) {
			mu.Lock()
			calls = append(calls, s)
			mu.Unlock()
		}
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchDockerLiveness(ctx, p, time.Millisecond, true, record("up"), record("down"))
	}()

	// Healthy: repeated successful pings must stay silent (no transition).
	waitFor(t, func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.n >= 3 })
	if got := seen(); len(got) != 0 {
		t.Fatalf("transitions while healthy = %v, want none", got)
	}

	p.set(errors.New("daemon down"))
	waitFor(t, func() bool { got := seen(); return len(got) == 1 && got[0] == "down" })

	p.set(nil)
	waitFor(t, func() bool { got := seen(); return len(got) == 2 && got[1] == "up" })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchDockerLiveness did not return after context cancel")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
