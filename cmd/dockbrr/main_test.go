package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

// freeAddr reserves an ephemeral port on loopback and returns its address.
// The listener is closed immediately so the caller can bind to it.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeAddr: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// bogusSocket returns a path to a non-existent socket so Docker Ping always
// fails, keeping tests hermetic even on hosts with a real daemon.
func bogusSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "nope.sock")
}

func runTest(t *testing.T, addr string, extraEnv map[string]string) chan error {
	t.Helper()
	env := map[string]string{
		"DOCKBRR_DATA_DIR":     t.TempDir(),
		"DOCKBRR_BIND":         addr,
		"DOCKBRR_DOCKER_SOCKET": bogusSocket(t),
	}
	for k, v := range extraEnv {
		env[k] = v
	}
	getenv := func(k string) string { return env[k] }

	done := make(chan error, 1)
	go func() { done <- run([]string{}, getenv) }()
	t.Cleanup(shutdownTestServer)
	return done
}

func pollHealthz(t *testing.T, addr string) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func assertCleanShutdown(t *testing.T, done chan error) {
	t.Helper()
	shutdownTestServer()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after shutdown signal")
	}
}

// TestRunBootsDockerAbsent starts run() on an ephemeral port with a bogus
// Docker socket, polls /healthz, then asserts a clean shutdown.
// Covers both boot and the docker-absent path (bogusSocket ensures hermetic behaviour
// on hosts with or without a real daemon).
func TestRunBootsDockerAbsent(t *testing.T) {
	addr := freeAddr(t)
	done := runTest(t, addr, nil)

	if !pollHealthz(t, addr) {
		t.Fatal("server never became healthy")
	}
	assertCleanShutdown(t, done)
}
