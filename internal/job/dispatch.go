package job

import (
	"context"

	"dockbrr/internal/store"
)

// Dispatcher routes a claimed job to the runner for its kind. It is the Engine's
// single Handler. Lifecycle kinds go to the Lifecycle runner; apply/rollback go
// to the compose Applier. Phase 2 extends this to branch apply/rollback on
// project kind (standalone vs compose).
type Dispatcher struct {
	applier   *Applier
	lifecycle *Lifecycle
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle}
}

// Handle implements Handler. It never panics; the delegated runner records the
// terminal job status.
func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "start", "stop", "restart", "remove":
		d.lifecycle.Handle(ctx, job)
	default:
		// apply, rollback, and any other compose kinds.
		d.applier.Handle(ctx, job)
	}
}
