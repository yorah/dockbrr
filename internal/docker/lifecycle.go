package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerStart starts a stopped container. Mutating: called only by the Job
// Engine (invariant 2).
func (cl *Client) ContainerStart(ctx context.Context, id string) error {
	if err := cl.c.ContainerStart(ctx, id, dcontainer.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start %s: %w", id, err)
	}
	return nil
}

// ContainerStop stops a running container with the daemon default timeout.
func (cl *Client) ContainerStop(ctx context.Context, id string) error {
	if err := cl.c.ContainerStop(ctx, id, dcontainer.StopOptions{}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", id, err)
	}
	return nil
}

// ContainerRestart restarts a container (stop then start) with the daemon
// default timeout.
func (cl *Client) ContainerRestart(ctx context.Context, id string) error {
	if err := cl.c.ContainerRestart(ctx, id, dcontainer.StopOptions{}); err != nil {
		return fmt.Errorf("docker: restart %s: %w", id, err)
	}
	return nil
}

// ContainerRemove removes a container. The caller guarantees it is stopped
// (no force), so a running container is a caller bug surfaced as an error.
func (cl *Client) ContainerRemove(ctx context.Context, id string) error {
	if err := cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{}); err != nil {
		return fmt.Errorf("docker: remove %s: %w", id, err)
	}
	return nil
}

// ContainerRename renames a container (used by the Phase 2 recreate path to free
// a name, and available to lifecycle callers).
func (cl *Client) ContainerRename(ctx context.Context, id, newName string) error {
	if err := cl.c.ContainerRename(ctx, id, newName); err != nil {
		return fmt.Errorf("docker: rename %s -> %s: %w", id, newName, err)
	}
	return nil
}

// ContainerLogsTail returns the last tail lines of a container's combined
// stdout+stderr as text. Read-only: callable from the API handler. tail <= 0
// returns all lines.
func (cl *Client) ContainerLogsTail(ctx context.Context, id string, tail int) (string, error) {
	rc, err := cl.c.ContainerLogs(ctx, id, dcontainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tailArg(tail),
	})
	if err != nil {
		return "", fmt.Errorf("docker: logs %s: %w", id, err)
	}
	defer rc.Close()
	return decodeLogStream(rc)
}

// tailArg maps a line count to the docker Tail option ("all" for non-positive).
func tailArg(n int) string {
	if n <= 0 {
		return "all"
	}
	return strconv.Itoa(n)
}

// decodeLogStream demultiplexes docker's framed stdout/stderr log stream into a
// single text blob. Non-tty containers multiplex both streams with 8-byte
// headers; stdcopy.StdCopy splits them, and we interleave into one buffer.
func decodeLogStream(r io.Reader) (string, error) {
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, r); err != nil {
		return "", fmt.Errorf("docker: decode log stream: %w", err)
	}
	return buf.String(), nil
}
