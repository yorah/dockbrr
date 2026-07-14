package main

import (
	"context"
	"testing"
	"time"
)

// TestWaitForDiscoveryReturnsOnReady guards the fix for the boot race where
// scan_on_start could run before discovery's first reconcile finished,
// checking services against pre-recreate store rows. waitForDiscovery must
// return promptly once ready closes, without waiting out the timeout.
func TestWaitForDiscoveryReturnsOnReady(t *testing.T) {
	ready := make(chan struct{})
	close(ready)

	done := make(chan struct{})
	go func() {
		waitForDiscovery(context.Background(), ready, time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waitForDiscovery did not return promptly when ready was already closed")
	}
}

// TestWaitForDiscoveryTimesOut ensures a down/slow Docker daemon (ready never
// closes) cannot stall scan_on_start forever. It must proceed once the bound
// elapses.
func TestWaitForDiscoveryTimesOut(t *testing.T) {
	ready := make(chan struct{}) // never closed

	start := time.Now()
	waitForDiscovery(context.Background(), ready, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("waitForDiscovery returned before the timeout elapsed: %s", elapsed)
	}
}

// TestWaitForDiscoveryReturnsOnCtxDone ensures shutdown during boot doesn't
// hang waiting on discovery.
func TestWaitForDiscoveryReturnsOnCtxDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{}) // never closed
	cancel()

	done := make(chan struct{})
	go func() {
		waitForDiscovery(ctx, ready, time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waitForDiscovery did not return promptly when ctx was already done")
	}
}

// TestWaitForDiscoveryNilReadyReturnsImmediately covers callers (tests) that
// don't wire a discoveryReady channel at all.
func TestWaitForDiscoveryNilReadyReturnsImmediately(t *testing.T) {
	done := make(chan struct{})
	go func() {
		waitForDiscovery(context.Background(), nil, time.Second)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("waitForDiscovery did not return immediately for a nil ready channel")
	}
}
