package job

import (
	"context"
	"os"
	"strings"

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

	// Self-guard wiring (SetSelfGuard). Empty selfID disables the guard.
	selfID   string
	services *store.Services
	jobs     *store.Jobs
	emitter  Emitter
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle, standalone *StandaloneApplier, projects *store.Projects) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle, standalone: standalone, projects: projects}
}

// SetSelfGuard arms the guard that refuses mutating jobs whose target includes
// dockbrr's own container: the mutation would kill this process mid-job, leave
// the job stuck at "running", and ResumeInterrupted would re-run it against
// ourselves on every boot. Call before the engine starts.
func (d *Dispatcher) SetSelfGuard(selfID string, services *store.Services, jobs *store.Jobs, emitter Emitter) {
	d.selfID = selfID
	d.services = services
	d.jobs = jobs
	d.emitter = emitter
}

func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	if d.refuseSelfTarget(job) {
		return
	}
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

// mutatingTypes are the job types that mutate Docker state and are therefore
// subject to the self-guard. check/sync are read-only.
var mutatingTypes = map[string]bool{
	"apply": true, "rollback": true, "start": true, "stop": true, "restart": true, "remove": true,
}

// refuseSelfTarget fails a mutating job whose target containers include our
// own, before any runner touches Docker. Returns true when the job was
// refused (already marked failed). Fail-open on store errors: the runners'
// own prechecks handle broken state, and a transient store error must not
// block unrelated jobs.
func (d *Dispatcher) refuseSelfTarget(job store.Job) bool {
	if d.selfID == "" || d.services == nil || d.jobs == nil || !mutatingTypes[job.Type] {
		return false
	}
	if !d.targetsSelf(job) {
		return false
	}
	const msg = "refused: this job targets dockbrr's own container. dockbrr cannot update or restart itself " +
		"(the operation would kill dockbrr mid-job); manage this container from the host instead"
	if d.emitter != nil {
		d.emitter.Emit(job.ID, "system", msg)
	}
	logger.Warnf("job: %s (job %d) refused: targets dockbrr's own container %s", job.Type, job.ID, d.selfID)
	_ = d.jobs.Finish(job.ID, "failed", nil, msg)
	return true
}

// targetsSelf reports whether the job's target container ids include our own
// container. Service scope checks that service; project scope checks every
// service of the project.
func (d *Dispatcher) targetsSelf(job store.Job) bool {
	var svcs []store.Service
	switch {
	case job.ServiceID != nil:
		svc, err := d.services.Get(*job.ServiceID)
		if err != nil {
			logger.Warnf("dispatch: self-guard load service %d: %v (guard skipped)", *job.ServiceID, err)
			return false
		}
		svcs = []store.Service{svc}
	case job.ProjectID != nil:
		list, err := d.services.ListByProject(*job.ProjectID)
		if err != nil {
			logger.Warnf("dispatch: self-guard list project %d: %v (guard skipped)", *job.ProjectID, err)
			return false
		}
		svcs = list
	default:
		return false
	}
	for _, s := range svcs {
		for _, cid := range s.ContainerIDs {
			// Stored ids may be full 64-char or short; selfID is the 12-char
			// short id. Prefix-match either way round.
			if strings.HasPrefix(cid, d.selfID) || strings.HasPrefix(d.selfID, cid) {
				return true
			}
		}
	}
	return false
}

// SelfContainerID returns dockbrr's own container id when running inside a
// container, or "" on a host install (guard disabled). Docker sets a
// container's hostname to its short id (12 hex chars) unless the user
// overrides it; a hostname that doesn't look like a short id yields "" rather
// than risking false matches against container ids.
func SelfContainerID() string {
	h, err := os.Hostname()
	if err != nil || len(h) != 12 {
		return ""
	}
	for _, c := range h {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return h
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
