package docker

import (
	"context"

	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"

	"dockbrr/internal/logger"
)

// ContainerEvents emits one signal per container lifecycle event (start, die,
// stop, destroy). The channel closes when ctx is cancelled or the daemon
// stream ends; the caller decides whether to resubscribe (reconcileLoop falls
// back to timer-only via a nil channel).
func (c *Client) ContainerEvents(ctx context.Context) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		f := filters.NewArgs()
		f.Add("type", "container")
		for _, action := range []string{"start", "die", "stop", "destroy"} {
			f.Add("event", action)
		}
		msgs, errs := c.c.Events(ctx, events.ListOptions{Filters: f})
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-errs:
				if err != nil {
					logger.Errorf("docker: event stream ended: %v", err)
				}
				return
			case <-msgs:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
