package job

import (
	"context"
	"fmt"
	"time"

	"dockbrr/internal/detect"
	"dockbrr/internal/docker"
	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// ContainerStatus mirrors docker.ContainerStatus so the Recreator interface (and
// its test fakes) can reference it without every caller importing
// dockbrr/internal/docker directly. Identical to the Inspector interface's
// return type (worker.go): both describe the same *docker.Client method.
type ContainerStatus = docker.ContainerStatus

// Recreator is the docker surface the standalone applier needs: the Phase 1
// lifecycle methods plus pull/create-from-inspect and inspect. *docker.Client
// satisfies it. Held only by the Job Engine (invariant 2).
type Recreator interface {
	ImagePull(ctx context.Context, ref string) error
	ContainerStop(ctx context.Context, id string) error
	ContainerStart(ctx context.Context, id string) error
	ContainerRename(ctx context.Context, id, name string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error)
	InspectStatus(ctx context.Context, id string) (ContainerStatus, error)
}

// StandaloneApplier applies (and rolls back) image updates for standalone
// containers by recreating them via the docker SDK. Compose projects use the
// compose Applier; the Dispatcher routes by project kind.
type StandaloneApplier struct {
	jobs       *store.Jobs
	updates    *store.Updates
	services   *store.Services
	projects   *store.Projects
	snapshots  *store.Snapshots
	events     *store.Events
	resolver   Resolver
	docker     Recreator
	plat       registry.Platform
	healthTO   func() time.Duration
	healthPoll func() time.Duration
}

func NewStandaloneApplier(
	jobs *store.Jobs, updates *store.Updates, services *store.Services, projects *store.Projects,
	snapshots *store.Snapshots, events *store.Events,
	resolver Resolver, docker Recreator, plat registry.Platform,
	healthTimeout, healthPoll func() time.Duration,
) *StandaloneApplier {
	return &StandaloneApplier{
		jobs: jobs, updates: updates, services: services, projects: projects,
		snapshots: snapshots, events: events, resolver: resolver, docker: docker,
		plat: plat, healthTO: healthTimeout, healthPoll: healthPoll,
	}
}

const oldSuffix = "-dockbrr-old"

// Handle dispatches an apply or rollback job for a standalone service.
func (a *StandaloneApplier) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "apply":
		a.runApply(ctx, job)
	case "rollback":
		a.runRollback(ctx, job)
	default:
		a.fail(job, "standalone: unknown job type: "+job.Type)
	}
}

func (a *StandaloneApplier) runApply(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "apply job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	upd, err := a.updates.GetLatestOpenByService(svc.ID)
	if err != nil {
		a.fail(job, "no open update to apply: "+err.Error())
		return
	}
	if len(svc.ContainerIDs) == 0 {
		a.fail(job, "standalone apply: service has no container")
		return
	}
	oldID := svc.ContainerIDs[0]

	// Precheck: re-resolve the tracked ref and confirm the target digest is
	// still served; else the update was superseded.
	targetRef := svc.ImageRef
	remote, err := a.resolver.Resolve(ctx, targetRef, a.plat)
	if err != nil {
		a.fail(job, "precheck: re-resolve: "+err.Error())
		return
	}
	if remote.Digest != upd.ToDigest && remote.PlatformDigest != upd.ToDigest {
		_ = a.updates.SetStatus(upd.ID, "superseded")
		a.event(svc.ID, "failed", &job.ID, svc.CurrentDigest, upd.ToDigest, "superseded: remote digest moved before apply")
		a.fail(job, "precheck: target digest moved; marked superseded")
		return
	}

	// Snapshot BEFORE any mutation (invariant 3).
	inspect := "{}"
	if st, ierr := a.docker.InspectStatus(ctx, oldID); ierr == nil && st.RawJSON != "" {
		inspect = st.RawJSON
	}
	repo, _ := detect.SplitRef(svc.ImageRef)
	if _, serr := a.snapshots.Insert(store.Snapshot{
		ServiceID: svc.ID, JobID: &job.ID,
		PrevRepo: repo, PrevDigest: svc.CurrentDigest, PrevImageID: svc.CurrentImageID,
		PrevContainerInspect: inspect,
	}); serr != nil {
		a.failApply(job, svc, upd, "snapshot: "+serr.Error())
		return
	}

	// Pull-before-create (invariant 4).
	if err := a.docker.ImagePull(ctx, targetRef); err != nil {
		a.failApply(job, svc, upd, "pull: "+err.Error())
		return
	}

	// Recreate: stop old, rename it aside, create the new from the snapshot
	// inspect with the new image, start it. On any failure, restore old in place.
	newID, rerr := a.recreate(ctx, oldID, inspect, targetRef, svc.Name)
	if rerr != nil {
		a.restoreOld(ctx, oldID, svc.Name, newID)
		a.failApply(job, svc, upd, "recreate: "+rerr.Error())
		return
	}

	// Health-gate the NEW id (invariant 4).
	if err := a.healthGate(ctx, newID); err != nil {
		a.restoreOld(ctx, oldID, svc.Name, newID)
		a.failApply(job, svc, upd, "health gate: "+err.Error())
		return
	}

	// Success: drop the old container, refresh runtime, mark applied.
	if err := a.docker.ContainerRemove(ctx, oldID); err != nil {
		logger.Warnf("standalone apply: remove old %s: %v (continuing)", oldID, err)
	}
	if err := a.services.UpdateRuntime(svc.ID, []string{newID}, upd.ToDigest); err != nil {
		logger.Warnf("standalone apply: runtime refresh: %v", err)
	}
	_ = a.updates.MarkApplied(upd.ID)
	a.event(svc.ID, "succeeded", &job.ID, svc.CurrentDigest, upd.ToDigest, "update applied")
	a.succeed(job)
}

// recreate stops oldID, renames it aside, creates a new container from
// inspectJSON with newImage under name, and starts it. Returns the new id (or
// "" if creation did not happen).
func (a *StandaloneApplier) recreate(ctx context.Context, oldID, inspectJSON, newImage, name string) (string, error) {
	if err := a.docker.ContainerStop(ctx, oldID); err != nil {
		return "", fmt.Errorf("stop old: %w", err)
	}
	if err := a.docker.ContainerRename(ctx, oldID, name+oldSuffix); err != nil {
		return "", fmt.Errorf("rename old: %w", err)
	}
	newID, err := a.docker.ContainerCreateFromInspect(ctx, inspectJSON, newImage, name)
	if err != nil {
		return "", fmt.Errorf("create new: %w", err)
	}
	if err := a.docker.ContainerStart(ctx, newID); err != nil {
		return newID, fmt.Errorf("start new: %w", err)
	}
	return newID, nil
}

// restoreOld undoes a failed recreate: remove the new container (if any), rename
// the old back to its original name, and start it. Best-effort.
func (a *StandaloneApplier) restoreOld(ctx context.Context, oldID, name, newID string) {
	if newID != "" {
		if err := a.docker.ContainerRemove(ctx, newID); err != nil {
			logger.Warnf("standalone restore: remove new %s: %v", newID, err)
		}
	}
	if err := a.docker.ContainerRename(ctx, oldID, name); err != nil {
		logger.Warnf("standalone restore: rename old back: %v", err)
	}
	if err := a.docker.ContainerStart(ctx, oldID); err != nil {
		logger.Warnf("standalone restore: start old: %v", err)
	}
}

// healthGate polls a single recreated container id until running/healthy or the
// timeout elapses. Mirrors the compose Applier's gate (recreated id only).
func (a *StandaloneApplier) healthGate(ctx context.Context, id string) error {
	timeout := a.healthTO()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	poll := a.healthPoll()
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		st, err := a.docker.InspectStatus(ctx, id)
		if err == nil && st.State == "running" && (st.Health == "" || st.Health == "healthy") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s not healthy within %s", id, timeout)
		case <-time.After(poll):
		}
	}
}

// --- rollback ------------------------------------------------------------

// runRollback is filled in by Task 3. Until then, a rollback job for a
// standalone service fails cleanly rather than panicking.
func (a *StandaloneApplier) runRollback(ctx context.Context, job store.Job) {
	a.fail(job, "standalone: rollback not yet implemented")
}

// --- shared helpers --------------------------------------------------------

func (a *StandaloneApplier) event(serviceID int64, kind string, jobID *int64, from, to, msg string) {
	if _, err := a.events.Insert(store.Event{
		ServiceID: serviceID, Kind: kind, RefJobID: jobID,
		FromDigest: from, ToDigest: to, Message: msg,
	}); err != nil {
		// events are history, never fatal to the job outcome
		logger.Warnf("standalone: event insert failed: %v", err)
	}
}

// fail records a failed job with a warning log line.
func (a *StandaloneApplier) fail(job store.Job, msg string) {
	logger.Warnf("job: %s failed (job %d): %s", job.Type, job.ID, msg)
	_ = a.jobs.Finish(job.ID, "failed", nil, msg)
}

// succeed records a successful job.
func (a *StandaloneApplier) succeed(job store.Job) {
	code := 0
	_ = a.jobs.Finish(job.ID, "success", &code, "")
}

// failApply marks the update failed + emits a failed event, then fails the job.
func (a *StandaloneApplier) failApply(job store.Job, svc store.Service, upd store.Update, msg string) {
	_ = a.updates.SetStatus(upd.ID, "failed")
	a.event(svc.ID, "failed", &job.ID, svc.CurrentDigest, upd.ToDigest, msg)
	a.fail(job, msg)
}
