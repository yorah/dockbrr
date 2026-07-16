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
	ContainerIDByName(ctx context.Context, name string) (string, bool, error)
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
	emitter    Emitter
}

func NewStandaloneApplier(
	jobs *store.Jobs, updates *store.Updates, services *store.Services, projects *store.Projects,
	snapshots *store.Snapshots, events *store.Events,
	resolver Resolver, docker Recreator, plat registry.Platform,
	healthTimeout, healthPoll func() time.Duration,
	emitter Emitter,
) *StandaloneApplier {
	return &StandaloneApplier{
		jobs: jobs, updates: updates, services: services, projects: projects,
		snapshots: snapshots, events: events, resolver: resolver, docker: docker,
		plat: plat, healthTO: healthTimeout, healthPoll: healthPoll, emitter: emitter,
	}
}

// emit sends a best-effort progress line for the live-log panel. Nil-safe so
// callers (and tests) may pass a nil emitter; never affects job outcome.
func (a *StandaloneApplier) emit(job store.Job, stream, line string) {
	if a.emitter == nil {
		return
	}
	a.emitter.Emit(job.ID, stream, line)
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
	a.emit(job, "system", "resolving target")

	// Precheck: re-resolve the tracked ref and confirm the target digest is
	// still served; else the update was superseded. A cross-tag update (the
	// semver scan suggested a newer tag than the one tracked) must re-resolve
	// THAT tag, not the tracked tag, mirroring the compose Applier's crossTag
	// handling (worker.go): otherwise the precheck compares against a digest
	// the tracked tag will never serve and every semver-suggested apply aborts
	// as "superseded". upd.Tag is empty for updates recorded before this
	// feature (or same-tag drift), so targetRef falls back to svc.ImageRef.
	repo, trackedTag := detect.SplitRef(svc.ImageRef)
	targetRef := svc.ImageRef
	if upd.Tag != "" && upd.Tag != trackedTag {
		targetRef = repo + ":" + upd.Tag
	}
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

	// Precheck: confirm the current container still exists BEFORE any mutation
	// (design spec section 6, step 1). An inspect error or empty payload means
	// the container is gone or the daemon hiccupped; fail cleanly rather than
	// snapshot "{}" and proceed to stop/rename the live container.
	st, ierr := a.docker.InspectStatus(ctx, oldID)
	if ierr != nil || st.RawJSON == "" {
		a.fail(job, "precheck: inspect current container: "+inspectErrMsg(ierr))
		return
	}
	inspect := st.RawJSON

	// Snapshot BEFORE any mutation (invariant 3).
	if _, serr := a.snapshots.Insert(store.Snapshot{
		ServiceID: svc.ID, JobID: &job.ID,
		PrevRepo: repo, PrevDigest: svc.CurrentDigest, PrevImageID: svc.CurrentImageID,
		PrevContainerInspect: inspect,
	}); serr != nil {
		a.fail(job, "snapshot: "+serr.Error())
		return
	}
	a.emit(job, "system", "snapshot taken")

	// Pull-before-create (invariant 4).
	a.emit(job, "system", "pulling "+targetRef)
	if err := a.docker.ImagePull(ctx, targetRef); err != nil {
		a.failApply(job, svc, upd, "pull: "+err.Error())
		return
	}

	// Recreate: stop old, rename it aside, create the new from the snapshot
	// inspect with the new image, start it. On any failure, restore old in place.
	a.emit(job, "system", "recreating container")
	newID, rerr := a.recreate(ctx, oldID, inspect, targetRef, svc.Name)
	if rerr != nil {
		a.restoreOld(ctx, oldID, svc.Name, newID)
		a.failApply(job, svc, upd, "recreate: "+rerr.Error())
		return
	}

	// Health-gate the NEW id (invariant 4).
	a.emit(job, "system", "health-gating")
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
	if targetRef != svc.ImageRef {
		if err := a.services.UpdateImageRef(svc.ID, targetRef); err != nil {
			logger.Warnf("standalone apply: image ref refresh: %v", err)
		}
	}
	_ = a.updates.MarkApplied(upd.ID)
	a.event(svc.ID, "succeeded", &job.ID, svc.CurrentDigest, upd.ToDigest, "update applied")
	a.emit(job, "system", "apply succeeded")
	a.succeed(job)
}

// recreate stops oldID, renames it aside, creates a new container from
// inspectJSON with newImage under name, and starts it. Returns the new id (or
// "" if creation did not happen).
func (a *StandaloneApplier) recreate(ctx context.Context, oldID, inspectJSON, newImage, name string) (string, error) {
	// Idempotency: a prior crashed attempt may have left a renamed-aside old
	// container or a half-created new one holding our names. Remove leftovers
	// (never the current oldID) so a resumed recreate does not collide.
	a.clearNameConflict(ctx, name+oldSuffix, oldID)
	a.clearNameConflict(ctx, name, oldID)

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

// clearNameConflict removes a container currently holding wantName, unless it is
// keepID (the container we are about to operate on). Best-effort: a leftover is
// stopped then removed, ignoring errors (a leftover that fails to clear just
// makes the following rename/create fail naturally, surfaced via failApply). On
// a normal (non-crash)
// run, ContainerIDByName(name) returns keepID (the current container still
// holds its primary name until this recreate renames it aside), so this is a
// no-op; only a genuinely stale container from a prior interrupted attempt is
// ever removed.
func (a *StandaloneApplier) clearNameConflict(ctx context.Context, wantName, keepID string) {
	id, ok, err := a.docker.ContainerIDByName(ctx, wantName)
	if err != nil || !ok || id == keepID {
		return
	}
	logger.Warnf("standalone: removing leftover container %q (id %s) from a prior interrupted recreate", wantName, id)
	_ = a.docker.ContainerStop(ctx, id)
	_ = a.docker.ContainerRemove(ctx, id)
}

// restoreOld undoes a failed recreate: remove the new container (if any), rename
// the old back to its original name, and start it. Best-effort.
func (a *StandaloneApplier) restoreOld(ctx context.Context, oldID, name, newID string) {
	if newID != "" {
		if err := a.docker.ContainerStop(ctx, newID); err != nil {
			logger.Warnf("standalone restore: stop new %s: %v", newID, err)
		}
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
		if err == nil {
			if st.State == "exited" || st.Health == "unhealthy" {
				return fmt.Errorf("container %s not healthy (state=%s health=%s)", id, st.State, st.Health)
			}
			if st.State == "running" && (st.Health == "" || st.Health == "healthy") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s not healthy within %s", id, timeout)
		case <-time.After(poll):
		}
	}
}

// inspectErrMsg renders the precheck inspect failure reason: the inspect
// error if there was one, or "empty inspect" if the call succeeded but
// returned no payload.
func inspectErrMsg(err error) string {
	if err != nil {
		return err.Error()
	}
	return "empty inspect"
}

// --- rollback ------------------------------------------------------------

// runRollback reverts a standalone service to the image identity recorded in
// its latest snapshot: pulls the old (digest-pinned) ref, recreates the
// current container from the snapshot's inspect JSON with that old image,
// health-gates it, and on success drops the current container and refreshes
// runtime + update bookkeeping to the old digest. On any failure the current
// container is restored in place (mirrors runApply's recreate/restore shape).
func (a *StandaloneApplier) runRollback(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "rollback job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	snap, err := a.snapshots.GetLatestForService(svc.ID)
	if err != nil {
		a.fail(job, "no snapshot to roll back to: "+err.Error())
		return
	}
	if snap.PrevDigest == "" || snap.PrevContainerInspect == "" || snap.PrevContainerInspect == "{}" {
		a.fail(job, "snapshot lacks the previous container state")
		return
	}
	if len(svc.ContainerIDs) == 0 {
		a.fail(job, "standalone rollback: service has no current container")
		return
	}
	currentID := svc.ContainerIDs[0]
	a.emit(job, "system", "rolling back")

	// Recreate the snapshot's container with the OLD image. Prefer a
	// digest-pinned ref so rollback is deterministic.
	oldRef := snap.PrevRepo + "@" + snap.PrevDigest

	if err := a.docker.ImagePull(ctx, oldRef); err != nil {
		a.fail(job, "rollback pull: "+err.Error())
		return
	}
	newID, rerr := a.recreate(ctx, currentID, snap.PrevContainerInspect, oldRef, svc.Name)
	if rerr != nil {
		a.restoreOld(ctx, currentID, svc.Name, newID)
		a.fail(job, "rollback recreate: "+rerr.Error())
		return
	}
	if err := a.healthGate(ctx, newID); err != nil {
		a.restoreOld(ctx, currentID, svc.Name, newID)
		a.fail(job, "rollback health gate: "+err.Error())
		return
	}
	if err := a.docker.ContainerRemove(ctx, currentID); err != nil {
		logger.Warnf("standalone rollback: remove current %s: %v", currentID, err)
	}
	if err := a.services.UpdateRuntime(svc.ID, []string{newID}, snap.PrevDigest); err != nil {
		logger.Warnf("standalone rollback: runtime refresh: %v", err)
	}
	// Mark the reverted update "rolled_back", mirroring the compose rollback
	// (worker.go runRollback/rollbackPullUp): svc.CurrentDigest is the
	// pre-rollback digest, i.e. the to_digest of the applied update this
	// rollback reverts. Warn-and-continue: must never flip a successful
	// rollback to failed.
	if err := a.updates.MarkRolledBack(svc.ID, svc.CurrentDigest); err != nil {
		logger.Warnf("standalone rollback: mark rolled_back: %v", err)
	}
	a.event(svc.ID, "rolled_back", &job.ID, svc.CurrentDigest, snap.PrevDigest, "rolled back")
	a.emit(job, "system", "rolled back")
	a.succeed(job)
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
	a.emit(job, "system", job.Type+" failed: "+msg)
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
