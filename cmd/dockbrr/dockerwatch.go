package main

import (
	"context"
	"time"

	"dockbrr/internal/docker"
)

const (
	dockerDialTimeout   = 2 * time.Second  // per-attempt Ping budget
	dockerRetryInterval = 5 * time.Second  // reconnect cadence while Docker is down at boot
	dockerProbeInterval = 15 * time.Second // liveness cadence once the daemon has been seen
)

// pinger is the liveness surface of a Docker client (satisfied by *docker.Client).
type pinger interface {
	Ping(context.Context) error
}

// dialDocker opens a client and proves the daemon answers. docker.New does not
// dial, so the Ping is what decides reachability; a client that fails the Ping
// is closed rather than handed back.
func dialDocker(ctx context.Context, socket string) (*docker.Client, error) {
	c, err := docker.New(socket)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, dockerDialTimeout)
	defer cancel()
	if err := c.Ping(pingCtx); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

// waitForDocker retries dial every interval until it succeeds, returning the
// client and true. It returns false when ctx ends first, meaning the daemon
// never came back, so the caller should shut down instead of starting
// Docker-backed services.
func waitForDocker[T any](ctx context.Context, interval time.Duration, dial func(context.Context) (T, error)) (T, bool) {
	var zero T
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return zero, false
		case <-ticker.C:
			c, err := dial(ctx)
			if err != nil {
				continue
			}
			return c, true
		}
	}
}

// watchDockerLiveness pings p every interval and reports only edges: down fires
// on the first failure after a healthy stretch, up on the first success after a
// failing one. reachable seeds the state so a boot with Docker present does not
// announce a "restored" it never lost.
func watchDockerLiveness(ctx context.Context, p pinger, interval time.Duration, reachable bool, up, down func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, dockerDialTimeout)
			err := p.Ping(pingCtx)
			cancel()
			switch {
			case err != nil && reachable:
				reachable = false
				down(err)
			case err == nil && !reachable:
				reachable = true
				up(nil)
			}
		}
	}
}
