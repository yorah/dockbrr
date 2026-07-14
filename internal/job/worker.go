package job

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"dockbrr/internal/compose"
	"dockbrr/internal/detect"
	"dockbrr/internal/docker"
	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// Resolver re-resolves an image reference for the apply precheck. *registry.Resolver satisfies it.
type Resolver interface {
	Resolve(ctx context.Context, ref string, plat registry.Platform) (registry.RemoteImage, error)
}

// Inspector reads a container's status for the snapshot + health gate. *docker.Client satisfies it.
type Inspector interface {
	InspectStatus(ctx context.Context, containerID string) (docker.ContainerStatus, error)
}

// Rediscoverer re-discovers a service's recreated container ids + running digest
// after `compose up` (which assigns new ids). *discovery.Locator satisfies it.
type Rediscoverer interface {
	LocateService(ctx context.Context, projectName, serviceName string) (ids []string, currentDigest string, err error)
}

// Composer parses compose files and hashes them. RealComposer wraps the compose package.
type Composer interface {
	Parse(ctx context.Context, dir string, files []string) (compose.Project, error)
	HashFiles(files []string) (string, error)
}

// RealComposer is the production Composer backed by the compose package.
type RealComposer struct{}

func (RealComposer) Parse(ctx context.Context, dir string, files []string) (compose.Project, error) {
	return compose.Parse(ctx, dir, files)
}
func (RealComposer) HashFiles(files []string) (string, error) { return compose.HashFiles(files) }

// Applier is the Job Engine handler. It is the ONLY component that mutates
// Docker, and it does so only through the whitelisted compose runner, after a
// snapshot, in the strict pull-before-up order.
type Applier struct {
	jobs         *store.Jobs
	updates      *store.Updates
	services     *store.Services
	projects     *store.Projects
	snapshots    *store.Snapshots
	events       *store.Events
	settings     *store.Settings
	runner       compose.Runner
	resolver     Resolver
	inspector    Inspector
	rediscoverer Rediscoverer
	composer     Composer
	emitter      Emitter
	plat         registry.Platform

	healthTimeout func() time.Duration
	healthPoll    func() time.Duration
}

// NewApplier wires the orchestrator. healthTimeout/healthPoll are consulted on
// every health gate poll (not just at construction) so a live settings change
// takes effect without a restart; the <=0-invalid guard lives at the read site
// (healthGate), not here.
func NewApplier(
	jobs *store.Jobs,
	updates *store.Updates,
	services *store.Services,
	projects *store.Projects,
	snapshots *store.Snapshots,
	events *store.Events,
	settings *store.Settings,
	runner compose.Runner,
	resolver Resolver,
	inspector Inspector,
	rediscoverer Rediscoverer,
	composer Composer,
	emitter Emitter,
	plat registry.Platform,
	healthTimeout, healthPoll func() time.Duration,
) *Applier {
	if healthTimeout == nil {
		healthTimeout = func() time.Duration { return 2 * time.Minute }
	}
	if healthPoll == nil {
		healthPoll = func() time.Duration { return 2 * time.Second }
	}
	return &Applier{
		jobs: jobs, updates: updates, services: services, projects: projects,
		snapshots: snapshots, events: events, settings: settings, runner: runner, resolver: resolver,
		inspector: inspector, rediscoverer: rediscoverer, composer: composer, emitter: emitter, plat: plat,
		healthTimeout: healthTimeout, healthPoll: healthPoll,
	}
}

// Handle dispatches a claimed job. It never panics; every path records a
// terminal job result.
func (a *Applier) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "apply":
		a.runApply(ctx, job)
	case "rollback":
		a.runRollback(ctx, job)
	default:
		a.fail(job, "unknown job type: "+job.Type)
	}
}

// --- apply -------------------------------------------------------------------

func (a *Applier) runApply(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "apply job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	proj, err := a.projects.Get(svc.ProjectID)
	if err != nil {
		a.fail(job, "load project: "+err.Error())
		return
	}
	upd, err := a.updates.GetLatestOpenByService(svc.ID)
	if err != nil {
		a.fail(job, "no open update to apply: "+err.Error())
		return
	}
	logger.Infof("apply: service %d (%s) %s -> %s (job %d, scope %s)",
		svc.ID, svc.Name, shortDigest(svc.CurrentDigest), shortDigest(upd.ToDigest), job.ID, job.Scope)

	// --- PRECHECK (no mutation) ---
	parsedProj, err := a.composer.Parse(ctx, proj.WorkingDir, proj.ConfigFiles)
	if err != nil {
		a.fail(job, "precheck: compose unparseable: "+err.Error())
		return
	}
	// Services sharing svc's network/IPC/PID namespace (network_mode/ipc/pid:
	// service:<svc.Name>) are never recreated by compose's own config-hash
	// diff when svc itself is recreated below. Their declared config never
	// changes, only the container id it resolves to. Computed here, applied
	// after the primary up succeeds.
	nsDeps := compose.NamespaceDependents(parsedProj.Services, svc.Name)
	// A cross-tag update (the semver scan suggested a stable tag newer than
	// the one the compose file tracks) must re-resolve THAT tag, not the
	// tracked tag, otherwise the precheck compares against a digest the
	// tracked tag will never serve and every semver-suggested apply aborts as
	// "superseded". upd.Tag is empty for updates recorded before this feature
	// (or same-tag drift), so crossTag is false and behavior is unchanged.
	repo, trackedTag := detect.SplitRef(svc.ImageRef)
	crossTag := upd.Tag != "" && upd.Tag != trackedTag
	targetRef := svc.ImageRef
	if crossTag {
		targetRef = repo + ":" + upd.Tag
	}
	remote, err := a.resolver.Resolve(ctx, targetRef, a.plat)
	if err != nil {
		a.fail(job, "precheck: re-resolve target: "+err.Error())
		return
	}
	if remote.Digest != upd.ToDigest && remote.PlatformDigest != upd.ToDigest {
		_ = a.updates.SetStatus(upd.ID, "superseded")
		a.event(svc.ID, "failed", &job.ID, svc.CurrentDigest, upd.ToDigest, "superseded: remote digest moved before apply")
		a.fail(job, "precheck: target digest moved; marked superseded")
		return
	}

	// --- PLAN THE WRITE-BACK / OVERRIDE DECISION (no mutation yet) ---
	// Branch on the TRACKED tag's class (what the compose file currently
	// declares via svc.ImageRef), NEVER on the suggested update's tag: a
	// floating tag (latest/1/1.31) must never be rewritten to something more
	// specific, even when a cross-tag exact update exists.
	class := detect.ClassifyTag(svc.ImageRef)
	writeBack := a.settings.GetBoolDefault("write_back_compose", true)
	newTag := trackedTag
	if crossTag {
		newTag = upd.Tag
	}

	var (
		preEditBlob *string
		editFile    string
		editContent string
	)
	if writeBack && class == detect.TagExact && newTag != trackedTag {
		if loc, lerr := compose.LocateImageLine(proj.ConfigFiles, svc.Name); lerr == nil && loc.Rewritable && loc.OldRef == svc.ImageRef {
			if raw, rerr := os.ReadFile(loc.File); rerr == nil {
				if newContent, cerr := compose.ReplaceImageLine(string(raw), loc.OldRef, repo+":"+newTag, loc.Line); cerr == nil {
					blob := blobJSON(loc.File, string(raw))
					preEditBlob = &blob
					editFile = loc.File
					editContent = newContent
				}
			}
		}
	}

	// --- SNAPSHOT (must precede any mutation) ---
	if err := a.snapshot(ctx, job, svc, proj, preEditBlob); err != nil {
		a.fail(job, "snapshot: "+err.Error())
		return
	}
	a.event(svc.ID, "apply_started", &job.ID, svc.CurrentDigest, upd.ToDigest, "apply started")

	// --- APPLY THE STAGED EFFECT (the only mutation before pull/up) ---
	files := proj.ConfigFiles
	switch {
	case preEditBlob != nil:
		// Rewritable exact pin: surgically rewrite the user's compose file in
		// place. No runtime override needed: the file itself now names the
		// new tag, so plain pull+up picks it up.
		if werr := compose.WriteFileAtomic(editFile, editContent); werr != nil {
			a.failApply(job, svc, upd, "write compose file: "+werr.Error())
			return
		}
	case crossTag && class != detect.TagFloating:
		// Fallback: non-rewritable exact pin (interpolated/anchor/multi-file),
		// a digest pin, or writeBack disabled. Keeps the user's compose file
		// untouched by pinning the running container to repo:<upd.Tag>@<to_digest>
		// via the same temp-override mechanism rollback uses.
		//
		// Excludes a FLOATING tracked tag on purpose: a floating tag (latest,
		// 1, 1.31) must never be pinned to a more specific version even when a
		// cross-tag exact update exists; that's the caller's job, not apply's.
		overridePath, cleanup, perr := writePinOverride(proj.WorkingDir, svc.Name, repo, upd.Tag, upd.ToDigest)
		if perr != nil {
			a.failApply(job, svc, upd, "write pin override: "+perr.Error())
			return
		}
		defer cleanup()
		files = append(append([]string{}, proj.ConfigFiles...), overridePath)
	default:
		// Floating tracked tag (or same-tag drift): plain pull+up, granularity
		// preserved, nothing to rewrite, nothing to override.
	}

	// --- PULL (must fully succeed before up) ---
	logger.Debugf("apply: pulling images (job %d)", job.ID)
	if code, err := a.runner.Run(ctx, a.pullSpec(files, proj, job.Scope, svc.Name), a.sink(job)); err != nil || code != 0 {
		a.failApply(job, svc, upd, fmt.Sprintf("pull failed (code=%d err=%v)", code, err))
		return // NO up: the running stack is untouched
	}
	// --- UP ---
	logger.Debugf("apply: recreating containers (job %d)", job.ID)
	if code, err := a.runner.Run(ctx, a.upSpec(files, proj, job.Scope, svc.Name), a.sink(job)); err != nil || code != 0 {
		a.failApply(job, svc, upd, fmt.Sprintf("up failed (code=%d err=%v)", code, err))
		return // snapshot enables rollback
	}
	// --- NAMESPACE-DEPENDENT RECREATE (network_mode/ipc/pid: service:<svc.Name>) ---
	extraIDs := a.forceRecreateNamespaceDependents(ctx, job, svc, proj, files, nsDeps)

	// --- RE-DISCOVER (compose up recreates containers with new ids) ---
	pollIDs := svc.ContainerIDs
	newDigest := upd.ToDigest
	if rids, dig, derr := a.rediscoverer.LocateService(ctx, proj.Name, svc.Name); derr != nil {
		a.emit(job, "system", "re-discovery failed, falling back to pre-apply ids: "+derr.Error())
	} else if len(rids) > 0 {
		pollIDs = rids
		if dig != "" {
			newDigest = dig
		}
	}
	pollIDs = append(pollIDs, extraIDs...)
	// --- HEALTH GATE (polls the re-discovered ids, incl. forced dependents) ---
	logger.Debugf("apply: health-gating %d container(s) (job %d)", len(pollIDs), job.ID)
	if err := a.healthGate(ctx, pollIDs); err != nil {
		a.failApply(job, svc, upd, "health gate: "+err.Error())
		return
	}
	// --- SUCCESS ---
	if err := a.services.UpdateRuntime(svc.ID, pollIDs, newDigest); err != nil {
		a.emit(job, "system", "warning: runtime refresh failed: "+err.Error())
	}
	_ = a.updates.MarkApplied(upd.ID)
	a.event(svc.ID, "succeeded", &job.ID, svc.CurrentDigest, upd.ToDigest, "update applied")
	logger.Infof("apply: service %d (%s) applied -> %s (job %d)", svc.ID, svc.Name, shortDigest(newDigest), job.ID)

	// Project scope recreated every service in the stack, so close the siblings'
	// open updates and refresh their runtime too, so the dashboard doesn't show
	// stale "Update available" rows for services this very `up` just moved.
	// A sibling refresh problem is warn-and-continue: it must never flip this
	// already-successful apply to failed.
	if job.Scope == "project" {
		sibs, lerr := a.services.ListByProject(svc.ProjectID)
		if lerr != nil {
			a.emit(job, "system", "warning: sibling refresh failed: "+lerr.Error())
		} else {
			for i := range sibs {
				sib := sibs[i]
				if sib.ID == svc.ID {
					continue
				}
				sids, sdig, derr := a.rediscoverer.LocateService(ctx, proj.Name, sib.Name)
				if derr != nil {
					a.emit(job, "system", "warning: sibling re-discovery failed: "+derr.Error())
					continue
				}
				if len(sids) == 0 {
					continue // service not running, nothing to refresh
				}
				if sdig != "" && sdig != sib.CurrentDigest {
					if uerr := a.services.UpdateRuntime(sib.ID, sids, sdig); uerr != nil {
						a.emit(job, "system", "warning: sibling runtime refresh failed: "+uerr.Error())
					}
					// Only close the sibling's open update when this very `up`
					// delivered it (its ToDigest matches what's now running);
					// a sibling whose registry moved again mid-apply keeps its
					// open row for the next apply/reconcile to handle.
					if sup, uerr := a.updates.GetLatestOpenByService(sib.ID); uerr == nil && sup.ToDigest == sdig {
						_ = a.updates.MarkApplied(sup.ID)
						a.event(sib.ID, "succeeded", &job.ID, sib.CurrentDigest, sdig, "update applied (project scope)")
					}
				}
			}
		}
	}

	a.emit(job, "system", "apply succeeded")
	a.succeed(job)
}

// snapshot captures pre-mutation state. It MUST run (and succeed) before any
// compose command. blob, when non-nil, carries the pre-edit compose file
// content (as {"path","content"} JSON) so a rollback can restore it verbatim
// when apply performed a surgical file rewrite.
func (a *Applier) snapshot(ctx context.Context, job store.Job, svc store.Service, proj store.Project, blob *string) error {
	inspect := "{}"
	if len(svc.ContainerIDs) > 0 {
		if st, err := a.inspector.InspectStatus(ctx, svc.ContainerIDs[0]); err == nil && st.RawJSON != "" {
			inspect = st.RawJSON
		}
	}
	hash, err := a.composer.HashFiles(proj.ConfigFiles)
	if err != nil {
		return err
	}
	repo, _ := detect.SplitRef(svc.ImageRef)
	_, err = a.snapshots.Insert(store.Snapshot{
		ServiceID: svc.ID, JobID: &job.ID,
		PrevRepo: repo, PrevDigest: svc.CurrentDigest, PrevImageID: svc.CurrentImageID,
		PrevContainerInspect: inspect, ComposeFileHash: hash, ComposeBlob: blob,
	})
	return err
}

func (a *Applier) pullSpec(files []string, proj store.Project, scope, service string) compose.RunSpec {
	return compose.PullSpec(files, proj.WorkingDir, proj.Name, scope, service)
}

func (a *Applier) upSpec(files []string, proj store.Project, scope, service string) compose.RunSpec {
	return compose.UpSpec(files, proj.WorkingDir, proj.Name, scope, service)
}

// forceRecreateNamespaceDependents recreates each service in nsDeps (network
// or IPC or PID namespace borrowed from the service that was just recreated)
// and refreshes its stored container ids, returning the newly discovered ids
// so the caller can fold them into its health gate. It is scope-agnostic:
// namespace sharing is a container-runtime relationship independent of
// whether the job targets one service or the whole project, and compose
// never recreates the dependent on its own since the dependent's own
// declared config never changes.
//
// Warn-and-continue throughout: a failure here must never flip an
// already-successful primary apply/rollback to failed, matching the
// sibling-refresh convention used by project-scope applies. It is still
// visible: folded into pollIDs, a genuinely unhealthy forced-recreate DOES
// fail the apply via the health gate, which is the point of this fix.
func (a *Applier) forceRecreateNamespaceDependents(ctx context.Context, job store.Job, svc store.Service, proj store.Project, files []string, nsDeps []string) (extraIDs []string) {
	if len(nsDeps) == 0 {
		return nil
	}
	spec := compose.ForceRecreateSpec(files, proj.WorkingDir, proj.Name, nsDeps)
	if code, err := a.runner.Run(ctx, spec, a.sink(job)); err != nil || code != 0 {
		a.emit(job, "system", fmt.Sprintf(
			"warning: force-recreate of namespace-sharing dependent(s) [%s] failed (code=%d err=%v), they may have lost network/IPC/PID connectivity and need a manual restart",
			strings.Join(nsDeps, ", "), code, err))
		return nil
	}
	sibs, lerr := a.services.ListByProject(svc.ProjectID)
	if lerr != nil {
		a.emit(job, "system", "warning: namespace-dependent runtime refresh failed (list project services): "+lerr.Error())
		return nil
	}
	byName := make(map[string]store.Service, len(sibs))
	for _, s := range sibs {
		byName[s.Name] = s
	}
	for _, dep := range nsDeps {
		depSvc, ok := byName[dep]
		if !ok {
			continue // recreated in Docker, just not a dockbrr-tracked service
		}
		dids, ddig, derr := a.rediscoverer.LocateService(ctx, proj.Name, dep)
		if derr != nil {
			a.emit(job, "system", "warning: namespace-dependent re-discovery failed for "+dep+": "+derr.Error())
			continue
		}
		if len(dids) == 0 {
			continue
		}
		extraIDs = append(extraIDs, dids...)
		digest := ddig
		if digest == "" {
			digest = depSvc.CurrentDigest
		}
		if uerr := a.services.UpdateRuntime(depSvc.ID, dids, digest); uerr != nil {
			a.emit(job, "system", "warning: namespace-dependent runtime refresh failed for "+dep+": "+uerr.Error())
		}
	}
	return extraIDs
}

// healthGate polls the given container ids until all are running-and-healthy, or
// fails fast on exited/unhealthy, or times out. No ids => nothing to poll.
func (a *Applier) healthGate(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	healthTimeout := a.healthTimeout()
	if healthTimeout <= 0 {
		healthTimeout = 2 * time.Minute
	}
	healthPoll := a.healthPoll()
	if healthPoll <= 0 {
		healthPoll = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	for {
		allReady := true
		for _, id := range ids {
			st, err := a.inspector.InspectStatus(ctx, id)
			if err != nil {
				allReady = false
				break
			}
			switch {
			case st.State == "exited" || st.Health == "unhealthy":
				return fmt.Errorf("container %s not healthy (state=%s health=%s)", id, st.State, st.Health)
			case st.State == "running" && (st.Health == "" || st.Health == "healthy"):
				// ready
			default:
				allReady = false
			}
		}
		if allReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("health gate timed out after %s", healthTimeout)
		case <-time.After(healthPoll):
		}
	}
}

// --- rollback ----------------------------------------------------------------

func (a *Applier) runRollback(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "rollback job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	proj, err := a.projects.Get(svc.ProjectID)
	if err != nil {
		a.fail(job, "load project: "+err.Error())
		return
	}
	snap, err := a.snapshots.GetLatestForService(svc.ID)
	if err != nil {
		a.fail(job, "no snapshot to roll back to: "+err.Error())
		return
	}
	if snap.PrevDigest == "" {
		a.fail(job, "snapshot has no previous digest")
		return
	}

	// A snapshot from a file-editing apply (Task 5) carries the pre-edit
	// compose file verbatim. Restore it exactly and do a plain pull+up on the
	// project's own files. The restored file already names the previous
	// image, so no temp pin override is needed (and none must be applied,
	// since the file is the source of truth again).
	if snap.ComposeBlob != nil {
		var b struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(*snap.ComposeBlob), &b); err == nil && b.Path != "" {
			if werr := compose.WriteFileAtomic(b.Path, b.Content); werr != nil {
				a.fail(job, "rollback: restore compose file: "+werr.Error())
				return
			}
			a.rollbackPullUp(ctx, job, svc, proj, snap.PrevDigest, proj.ConfigFiles)
			return
		}
	}

	// Else: no blob (digest pin, floating tag, or non-rewritable fallback at
	// apply time), repin the running container to the previous digest via
	// the same temp-override mechanism apply's fallback path uses.
	overridePath, cleanup, err := writePinOverride(proj.WorkingDir, svc.Name, snap.PrevRepo, "", snap.PrevDigest)
	if err != nil {
		a.fail(job, "write pin override: "+err.Error())
		return
	}
	defer cleanup()

	files := append(append([]string{}, proj.ConfigFiles...), overridePath)
	a.rollbackPullUp(ctx, job, svc, proj, snap.PrevDigest, files)
}

// rollbackPullUp runs the shared rollback tail (pull, up, re-discover, and
// success bookkeeping), shared by both the blob-restore and digest-repin
// branches of runRollback. files is the final config file list for both
// commands (already includes any temp override, when used).
func (a *Applier) rollbackPullUp(ctx context.Context, job store.Job, svc store.Service, proj store.Project, prevDigest string, files []string) {
	// Use the shared PullSpec/UpSpec builders so rollback's pull/up (incl. the
	// service-scope --no-deps guard) stays byte-identical to apply's. files
	// already carries any temp pin override appended by the caller.
	pull := compose.PullSpec(files, proj.WorkingDir, proj.Name, job.Scope, svc.Name)
	if code, err := a.runner.Run(ctx, pull, a.sink(job)); err != nil || code != 0 {
		a.fail(job, fmt.Sprintf("rollback pull failed (code=%d err=%v)", code, err))
		return
	}
	// Same namespace-dependent detection as apply: a service borrowing svc's
	// network/IPC/PID namespace needs forcing too, since rolling svc back
	// also recreates it under a new container id.
	var nsDeps []string
	if parsedProj, perr := a.composer.Parse(ctx, proj.WorkingDir, proj.ConfigFiles); perr != nil {
		a.emit(job, "system", "warning: namespace-dependent detection skipped (compose unparseable): "+perr.Error())
	} else {
		nsDeps = compose.NamespaceDependents(parsedProj.Services, svc.Name)
	}
	up := compose.UpSpec(files, proj.WorkingDir, proj.Name, job.Scope, svc.Name)
	if code, err := a.runner.Run(ctx, up, a.sink(job)); err != nil || code != 0 {
		a.fail(job, fmt.Sprintf("rollback up failed (code=%d err=%v)", code, err))
		return
	}
	// Rollback has no health gate, so the forced-recreate's own ids aren't
	// polled. The call is still needed for its side effect (recreating the
	// dependent + refreshing its stored container ids).
	_ = a.forceRecreateNamespaceDependents(ctx, job, svc, proj, files, nsDeps)
	// --- RE-DISCOVER (rollback up recreates containers with new ids) ---
	// Mirror the apply success tail: refresh the service row so current_digest +
	// container_ids reflect what is now running (the OLD, rolled-back image),
	// instead of the rolled-FORWARD values left over from the apply. A re-discovery
	// error or an UpdateRuntime error is NON-FATAL and must never flip a successful
	// rollback to failed, same non-fatal handling as runApply.
	rbIDs := svc.ContainerIDs
	rbDigest := prevDigest // the digest we rolled back TO
	if rids, rdig, derr := a.rediscoverer.LocateService(ctx, proj.Name, svc.Name); derr != nil {
		a.emit(job, "system", "re-discovery failed, falling back to pre-rollback ids: "+derr.Error())
	} else if len(rids) > 0 {
		rbIDs = rids
		if rdig != "" {
			rbDigest = rdig
		}
	}
	if err := a.services.UpdateRuntime(svc.ID, rbIDs, rbDigest); err != nil {
		a.emit(job, "system", "warning: runtime refresh failed: "+err.Error())
	}
	// Mark the reverted update "rolled_back" (svc was loaded at the start of
	// runRollback, before UpdateRuntime above, so svc.CurrentDigest is still the
	// pre-rollback applied-target digest = the to_digest of the applied update).
	// RecordDrift preserves "rolled_back" on re-detection, so auto-update never
	// silently re-applies the update this rollback just reverted. The
	// rollback-respect invariant now holds via explicit status, not via leaving
	// the update "applied". A mark failure is warn-and-continue: it must never
	// flip an already-successful rollback to failed.
	if err := a.updates.MarkRolledBack(svc.ID, svc.CurrentDigest); err != nil {
		a.emit(job, "system", "warning: mark rolled_back failed: "+err.Error())
	}
	a.event(svc.ID, "rolled_back", &job.ID, svc.CurrentDigest, prevDigest, "rolled back to previous digest")
	a.emit(job, "system", "rollback succeeded")
	a.succeed(job)
}

// writePinOverride writes a temporary compose override in dir pinning the
// service image to repo@digest (or repo:tag@digest when tag is non-empty,
// used by cross-tag apply so the running container shows the suggested tag),
// returning its path and a cleanup func. It uses only literal YAML (no user
// shell); the digest is a content-addressed sha.
func writePinOverride(dir, service, repo, tag, digest string) (string, func(), error) {
	ref := repo + "@" + digest
	if tag != "" {
		ref = repo + ":" + tag + "@" + digest
	}
	content := fmt.Sprintf("services:\n  %s:\n    image: %s\n", service, ref)
	f, err := os.CreateTemp(dir, "dockbrr-rollback-*.yml")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(path)
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, err
	}
	return path, func() { os.Remove(path) }, nil
}

// blobJSON encodes a pre-edit compose file for the snapshot's compose_blob so
// rollback can restore it verbatim.
func blobJSON(path, content string) string {
	b, _ := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{path, content})
	return string(b)
}

// --- shared helpers ----------------------------------------------------------

type emitSink struct {
	emitter Emitter
	jobID   int64
}

func (s emitSink) Write(stream, line string) { s.emitter.Emit(s.jobID, stream, line) }

func (a *Applier) sink(job store.Job) compose.LogSink {
	return emitSink{emitter: a.emitter, jobID: job.ID}
}

func (a *Applier) emit(job store.Job, stream, line string) { a.emitter.Emit(job.ID, stream, line) }

func (a *Applier) event(serviceID int64, kind string, jobID *int64, from, to, msg string) {
	if _, err := a.events.Insert(store.Event{
		ServiceID: serviceID, Kind: kind, RefJobID: jobID,
		FromDigest: from, ToDigest: to, Message: msg,
	}); err != nil {
		// events are history, never fatal to the job outcome
		a.emit(store.Job{ID: derefJob(jobID)}, "system", "event insert failed: "+err.Error())
	}
}

func derefJob(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// fail records a failed job with a system log line.
func (a *Applier) fail(job store.Job, msg string) {
	logger.Warnf("job: %s failed (job %d): %s", job.Type, job.ID, msg)
	a.emit(job, "system", msg)
	_ = a.jobs.Finish(job.ID, "failed", nil, msg)
}

// shortDigest truncates a "sha256:<hex>" digest to a log-friendly prefix.
func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}

// failApply marks the update failed + emits a failed event, then fails the job.
func (a *Applier) failApply(job store.Job, svc store.Service, upd store.Update, msg string) {
	_ = a.updates.SetStatus(upd.ID, "failed")
	a.event(svc.ID, "failed", &job.ID, svc.CurrentDigest, upd.ToDigest, msg)
	a.fail(job, msg)
}

// succeed records a successful job.
func (a *Applier) succeed(job store.Job) {
	code := 0
	_ = a.jobs.Finish(job.ID, "success", &code, "")
}
