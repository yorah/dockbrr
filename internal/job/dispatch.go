package job

import (
	"context"
	"os"
	"regexp"
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

	// jobs is required and wired at construction time (not gated on
	// containerization), so it is always available to mark a job failed: both
	// refuseSelfTarget's guard and the self_update nil-updater fallback below
	// depend on it, and the fallback in particular must work on a host install
	// (SelfContainerID() == "", so SetSelfGuard is never called there).
	jobs *store.Jobs

	// Self-guard wiring (SetSelfGuard). Empty selfID disables the guard.
	selfID   string
	services *store.Services
	emitter  Emitter

	selfUpdater *SelfUpdater
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle, standalone *StandaloneApplier, projects *store.Projects, jobs *store.Jobs) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle, standalone: standalone, projects: projects, jobs: jobs}
}

// SetSelfGuard arms the guard that refuses mutating jobs whose target includes
// dockbrr's own container: the mutation would kill this process mid-job, leave
// the job stuck at "running", and ResumeInterrupted would re-run it against
// ourselves on every boot. Call before the engine starts.
func (d *Dispatcher) SetSelfGuard(selfID string, services *store.Services, emitter Emitter) {
	d.selfID = selfID
	d.services = services
	d.emitter = emitter
}

// SetSelfUpdater wires the self-update runner. Call before the engine starts.
// Without it, self_update jobs fail cleanly instead of reaching a nil runner.
func (d *Dispatcher) SetSelfUpdater(u *SelfUpdater) { d.selfUpdater = u }

func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	if job.Type == "self_update" {
		if d.selfUpdater == nil {
			const msg = "self-update is not available: not running in a container"
			logger.Warnf("job: self_update (job %d) with no updater wired; failing", job.ID)
			if d.emitter != nil {
				d.emitter.Emit(job.ID, "system", msg)
			}
			if d.jobs != nil {
				_ = d.jobs.Finish(job.ID, "failed", nil, msg)
			} else {
				logger.Errorf("job: self_update (job %d) fallback: no jobs store wired, cannot mark failed", job.ID)
			}
			return
		}
		d.selfUpdater.Handle(ctx, job)
		return
	}
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
			// Stored ids may be full 64-char or short; selfID may be either
			// too (64-char from mountinfo/cgroup, 12-char from hostname).
			// Prefix-match either way round.
			if strings.HasPrefix(cid, d.selfID) || strings.HasPrefix(d.selfID, cid) {
				return true
			}
		}
	}
	return false
}

// containerIDRe matches a full 64-hex Docker container id.
var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// parseContainerIDFromMountinfo extracts dockbrr's container id from
// /proc/self/mountinfo. Docker bind-mounts /etc/hostname, /etc/hosts and
// /etc/resolv.conf from /var/lib/docker/containers/<id>/, so the id is the
// 64-hex segment immediately after "/containers/". Overlay layer hashes also
// appear in mountinfo, so anchoring on "/containers/" avoids matching those.
func parseContainerIDFromMountinfo(content string) string {
	const marker = "/containers/"
	for _, line := range strings.Split(content, "\n") {
		i := strings.Index(line, marker)
		if i < 0 {
			continue
		}
		rest := line[i+len(marker):]
		if id := containerIDRe.FindString(rest); id != "" && strings.HasPrefix(rest, id) {
			return id
		}
	}
	return ""
}

// parseContainerIDFromCgroup extracts the id from a cgroup v1 /proc/self/cgroup,
// whose lines carry ".../docker/<id>" or ".../docker-<id>.scope". Only
// container-related 64-hex ids appear here, so the first match wins. Returns ""
// for a host (cgroup v2 root "0::/") where no container id is present.
func parseContainerIDFromCgroup(content string) string {
	for _, line := range strings.Split(content, "\n") {
		if id := containerIDRe.FindString(line); id != "" {
			return id
		}
	}
	return ""
}

// parseContainerIDFromHostname returns the hostname when it is Docker's default
// short container id (exactly 12 hex chars), else "". Under `docker run` the
// hostname is the short id; under Compose it is the service name, so this is the
// last-resort probe.
func parseContainerIDFromHostname(hostname string) string {
	if len(hostname) != 12 {
		return ""
	}
	for _, c := range hostname {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return hostname
}

// SelfContainerID returns dockbrr's own container id when running inside a
// container, or "" on a host install (guard + self-updater stay disabled).
// Probes in order of reliability: the containers bind-mount path in
// /proc/self/mountinfo, then cgroup v1, then the hostname short-id convention.
// A file that cannot be read is treated as "no match" and falls through.
func SelfContainerID() string {
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		if id := parseContainerIDFromMountinfo(string(b)); id != "" {
			return id
		}
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if id := parseContainerIDFromCgroup(string(b)); id != "" {
			return id
		}
	}
	if h, err := os.Hostname(); err == nil {
		if id := parseContainerIDFromHostname(h); id != "" {
			return id
		}
	}
	return ""
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
