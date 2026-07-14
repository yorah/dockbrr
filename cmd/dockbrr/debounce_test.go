package main

import (
	"context"
	"testing"
	"time"
)

func TestDebounceCoalescesBursts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan struct{}, 10)
	out := debounce(ctx, in, 50*time.Millisecond)
	for i := 0; i < 5; i++ {
		in <- struct{}{}
	}
	select {
	case <-out:
	case <-time.After(time.Second):
		t.Fatal("no debounced signal")
	}
	select {
	case <-out:
		t.Fatal("burst must coalesce to one signal")
	case <-time.After(120 * time.Millisecond):
	}
}

// TestDebounceReturnsWhenInputClosed guards against the goroutine busy-looping
// forever when in is closed (e.g. docker.ContainerEvents closes its channel
// after the daemon event stream errors/ends). The correct behavior is for the
// debounce goroutine to return, which closes out via its deferred close.
func TestDebounceReturnsWhenInputClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan struct{})
	out := debounce(ctx, in, 50*time.Millisecond)
	close(in)

	select {
	case _, ok := <-out:
		if ok {
			t.Fatal("expected out to be closed, got a value")
		}
	case <-time.After(time.Second):
		t.Fatal("out was not closed after in was closed (goroutine likely busy-looping)")
	}
}
