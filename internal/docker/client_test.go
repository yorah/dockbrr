package docker

import (
	"context"
	"os"
	"testing"
)

const defaultSocket = "/var/run/docker.sock"

// TestClientIntegration pings the Docker daemon and runs Collect.
// It is skipped when the Docker socket is absent or the daemon is unreachable,
// so it is a no-op in CI environments without Docker.
func TestClientIntegration(t *testing.T) {
	if _, err := os.Stat(defaultSocket); err != nil {
		t.Skipf("docker socket not found (%v): skipping integration test", err)
	}

	cl, err := New(defaultSocket)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer cl.Close()

	ctx := context.Background()

	if err := cl.Ping(ctx); err != nil {
		t.Skipf("docker daemon unreachable (%v): skipping integration test", err)
	}

	containers, err := cl.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	t.Logf("Collect returned %d containers", len(containers))
}
