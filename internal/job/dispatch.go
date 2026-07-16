package job

import (
	"context"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// Dispatcher routes a claimed job to the runner for its kind. Lifecycle kinds go
// to the Lifecycle runner. apply/rollback branch on project kind: standalone
// projects use the SDK-recreate StandaloneApplier, everything else the compose
// Applier.
type Dispatcher struct {
	applier    *Applier
	lifecycle  *Lifecycle
	standalone *StandaloneApplier
	projects   *store.Projects
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle, standalone *StandaloneApplier, projects *store.Projects) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle, standalone: standalone, projects: projects}
}

func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "start", "stop", "restart", "remove":
		d.lifecycle.Handle(ctx, job)
		return
	case "apply", "rollback":
		if d.isStandalone(job) {
			d.standalone.Handle(ctx, job)
			return
		}
	}
	d.applier.Handle(ctx, job)
}

// isStandalone reports whether a job's project is a standalone container. A load
// failure falls back to the compose Applier (its own precheck will surface the
// error), so a transient store error never silently drops the job.
func (d *Dispatcher) isStandalone(job store.Job) bool {
	if job.ProjectID == nil || d.projects == nil {
		return false
	}
	proj, err := d.projects.Get(*job.ProjectID)
	if err != nil {
		logger.Warnf("dispatch: load project %d: %v (routing to compose applier)", *job.ProjectID, err)
		return false
	}
	return proj.Kind == "standalone"
}
