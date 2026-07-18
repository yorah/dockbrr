package job

import (
	"context"
	"fmt"

	"dockbrr/internal/compose"
	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// Mutator is the docker mutation surface the lifecycle runner needs.
// *docker.Client satisfies it. Only the Job Engine holds one (invariant 2).
type Mutator interface {
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string) error
	ContainerRemove(ctx context.Context, id string) error
	InspectStatus(ctx context.Context, id string) (ContainerStatus, error)
}

// Lifecycle handles start/stop/restart/remove jobs. It mutates containers by
// id through the Mutator, ordering start/stop/restart by shared-namespace
// dependents. It takes NO snapshot: lifecycle ops change no image, so there is
// nothing for a rollback to restore (unlike apply, which binds invariant 3).
type Lifecycle struct {
	jobs         *store.Jobs
	services     *store.Services
	projects     *store.Projects
	events       *store.Events
	mutator      Mutator
	composer     Composer
	rediscoverer Rediscoverer
	emitter      Emitter
}

// NewLifecycle wires the lifecycle runner.
func NewLifecycle(
	jobs *store.Jobs,
	services *store.Services,
	projects *store.Projects,
	events *store.Events,
	mutator Mutator,
	composer Composer,
	rediscoverer Rediscoverer,
	emitter Emitter,
) *Lifecycle {
	return &Lifecycle{
		jobs: jobs, services: services, projects: projects, events: events,
		mutator: mutator, composer: composer, rediscoverer: rediscoverer, emitter: emitter,
	}
}

// emit sends a best-effort progress line for the live-log panel. Nil-safe so
// callers (and tests) may pass a nil emitter; never affects job outcome.
func (l *Lifecycle) emit(job store.Job, stream, line string) {
	if l.emitter == nil {
		return
	}
	l.emitter.Emit(job.ID, stream, line)
}

// Handle dispatches a lifecycle job. It records a terminal job status and
// never panics.
func (l *Lifecycle) Handle(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		l.fail(job, "lifecycle job has no service")
		return
	}
	svc, err := l.services.Get(*job.ServiceID)
	if err != nil {
		l.fail(job, "load service: "+err.Error())
		return
	}
	proj, err := l.projects.Get(svc.ProjectID)
	if err != nil {
		l.fail(job, "load project: "+err.Error())
		return
	}

	l.emit(job, "system", job.Type+" "+svc.Name)

	switch job.Type {
	case "start":
		err = l.runOrdered(ctx, svc, proj, "start")
	case "stop":
		err = l.runOrdered(ctx, svc, proj, "stop")
	case "restart":
		// Compute dependent container ids once (parses the compose file) and
		// reuse for both the stop and start phases, instead of re-deriving
		// per phase.
		depIDs := l.dependentContainerIDs(ctx, svc, proj)
		if err = l.runOrderedWithDeps(ctx, svc, depIDs, "stop"); err == nil {
			err = l.runOrderedWithDeps(ctx, svc, depIDs, "start")
		}
	case "remove":
		err = l.runRemove(ctx, svc, proj)
	default:
		l.fail(job, "unknown lifecycle type: "+job.Type)
		return
	}
	if err != nil {
		l.emit(job, "system", job.Type+" failed: "+err.Error())
		l.fail(job, err.Error())
		return
	}
	if job.Type != "remove" {
		// A removed container has no runtime left to refresh.
		l.rediscover(ctx, svc, proj)
	}
	l.emit(job, "system", job.Type+" complete")
	l.succeed(job)
	l.event(svc.ID, eventKind(job.Type), &job.ID)
}

// runOrdered stops or starts the target and its namespace dependents in the
// order dictated by verb: stop = dependents then target; start = target then
// dependents.
func (l *Lifecycle) runOrdered(ctx context.Context, svc store.Service, proj store.Project, verb string) error {
	depIDs := l.dependentContainerIDs(ctx, svc, proj)
	return l.runOrderedWithDeps(ctx, svc, depIDs, verb)
}

// runOrderedWithDeps is runOrdered's implementation, taking the target's
// namespace-dependent container ids (depIDs) as a precomputed input rather
// than deriving them itself. This lets a restart compute depIDs once and
// reuse them for both its stop and start phases.
func (l *Lifecycle) runOrderedWithDeps(ctx context.Context, svc store.Service, depIDs []string, verb string) error {
	targetIDs := svc.ContainerIDs

	var order []string
	switch verb {
	case "stop":
		order = append(append([]string{}, depIDs...), targetIDs...)
	case "start":
		order = append(append([]string{}, targetIDs...), depIDs...)
	default:
		return fmt.Errorf("runOrdered: bad verb %q", verb)
	}
	for _, id := range order {
		var err error
		switch verb {
		case "stop":
			err = l.mutator.ContainerStop(ctx, id)
		case "start":
			err = l.mutator.ContainerStart(ctx, id)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", verb, id, err)
		}
	}
	return nil
}

// runRemove removes the target's containers. Guard: the project must be
// standalone AND every target container must be stopped. This is the backend
// enforcement of the loose+stopped rule. The stored svc.State is only
// refreshed on the event-driven discovery reconcile, so it can be stale; the
// stored-state check below is a cheap first reject, but the authoritative
// check is the live inspect that follows, right before any removal is
// attempted.
func (l *Lifecycle) runRemove(ctx context.Context, svc store.Service, proj store.Project) error {
	if proj.Kind != "standalone" {
		return fmt.Errorf("remove refused: %s is not a standalone container", svc.Name)
	}
	if !store.IsStoppedState(svc.State) {
		return fmt.Errorf("remove refused: %s is not stopped (state=%s)", svc.Name, svc.State)
	}
	for _, id := range svc.ContainerIDs {
		st, err := l.mutator.InspectStatus(ctx, id)
		if err == nil && (st.State == "running" || st.State == "restarting") {
			return fmt.Errorf("remove refused: %s is running (live state=%s)", svc.Name, st.State)
		}
		// An inspect error means the container is already gone (or otherwise
		// unreachable): fall through to the remove attempt and let the daemon
		// backstop it, rather than refusing a removal that already happened.
	}
	for _, id := range svc.ContainerIDs {
		if err := l.mutator.ContainerRemove(ctx, id); err != nil {
			return fmt.Errorf("remove %s: %w", id, err)
		}
	}
	return nil
}

// dependentContainerIDs returns the container ids of svc's namespace
// dependents (compose services sharing svc's netns/ipc/pid). Empty for loose
// or unparseable projects. Best-effort: a parse failure yields no dependents
// (target-only).
func (l *Lifecycle) dependentContainerIDs(ctx context.Context, svc store.Service, proj store.Project) []string {
	if proj.Kind != "compose" || len(proj.ConfigFiles) == 0 {
		return nil
	}
	parsed, err := l.composer.Parse(ctx, proj.WorkingDir, proj.ConfigFiles)
	if err != nil {
		logger.Warnf("lifecycle: parse %s: %v (no dependent ordering)", proj.Name, err)
		return nil
	}
	depNames := compose.NamespaceDependents(parsed.Services, svc.Name)
	if len(depNames) == 0 {
		return nil
	}
	byName := map[string]bool{}
	for _, n := range depNames {
		byName[n] = true
	}
	svcs, err := l.services.ListByProject(proj.ID)
	if err != nil {
		logger.Warnf("lifecycle: list services %s: %v (no dependent ordering)", proj.Name, err)
		return nil
	}
	var ids []string
	for _, s := range svcs {
		if byName[s.Name] {
			ids = append(ids, s.ContainerIDs...)
		}
	}
	return ids
}

// rediscover refreshes svc's stored container ids/digest after a lifecycle op.
// Warn-and-continue: a rediscovery failure must never flip an
// already-successful lifecycle op to failed.
func (l *Lifecycle) rediscover(ctx context.Context, svc store.Service, proj store.Project) {
	if l.rediscoverer == nil {
		return
	}
	ids, digest, err := l.rediscoverer.LocateService(ctx, proj.Name, svc.Name)
	if err != nil {
		logger.Warnf("lifecycle: rediscover service %s: %v", svc.Name, err)
		return
	}
	if len(ids) == 0 {
		return
	}
	if digest == "" {
		digest = svc.CurrentDigest
	}
	if err := l.services.UpdateRuntime(svc.ID, ids, digest); err != nil {
		logger.Warnf("lifecycle: update runtime for %s: %v", svc.Name, err)
	}
	// Stamp the live post-action state. The stored state is otherwise only
	// refreshed by the event-driven discovery reconcile, whose debounce lands
	// AFTER job_finished, so without this the UI's post-job refetch reads the
	// pre-action state (an enabled Stop button for a container that just
	// stopped). Best-effort: the reconcile corrects any miss shortly after.
	if st, ierr := l.mutator.InspectStatus(ctx, ids[0]); ierr == nil && st.State != "" {
		if uerr := l.services.UpdateState(svc.ID, st.State); uerr != nil {
			logger.Warnf("lifecycle: update state for %s: %v", svc.Name, uerr)
		}
	}
}

// eventKind maps a lifecycle job type to its history event kind (past tense,
// matching the succeeded/failed/rolled_back convention used by the Applier).
func eventKind(jobType string) string {
	switch jobType {
	case "start":
		return "started"
	case "stop":
		return "stopped"
	case "restart":
		return "restarted"
	case "remove":
		return "removed"
	default:
		return jobType
	}
}

// succeed records a successful job.
func (l *Lifecycle) succeed(job store.Job) {
	code := 0
	_ = l.jobs.Finish(job.ID, "success", &code, "")
}

// fail records a failed job with a warning log line.
func (l *Lifecycle) fail(job store.Job, msg string) {
	logger.Warnf("job: %s failed (job %d): %s", job.Type, job.ID, msg)
	_ = l.jobs.Finish(job.ID, "failed", nil, msg)
}

// event records a service-history entry for a completed lifecycle op.
func (l *Lifecycle) event(serviceID int64, kind string, jobID *int64) {
	if _, err := l.events.Insert(store.Event{
		ServiceID: serviceID, Kind: kind, RefJobID: jobID,
	}); err != nil {
		// events are history, never fatal to the job outcome
		logger.Warnf("lifecycle: event insert failed: %v", err)
	}
}
