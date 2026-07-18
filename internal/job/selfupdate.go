package job

import (
	"context"
	"strings"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

// SelfDocker is the Docker surface the self-updater needs. *docker.Client
// satisfies it.
type SelfDocker interface {
	ContainerImageRef(ctx context.Context, id string) (string, error)
	ImagePull(ctx context.Context, ref string) error
	SpawnUpdater(ctx context.Context, image string, cmd []string, socketPath string) (string, error)
}

// SelfChecker reports whether a newer dockbrr release exists. *selfupdate.Checker
// satisfies it.
type SelfChecker interface {
	Check(ctx context.Context) (selfupdate.Result, error)
}

// SelfUpdater runs a self_update job: pull the new dockbrr image in-process (so
// a pull failure never touches the running container), then hand the actual
// container swap to a detached helper that outlives this process.
type SelfUpdater struct {
	jobs    *store.Jobs
	emitter Emitter
	docker  SelfDocker
	checker SelfChecker
	selfID  string // dockbrr's own container id ("" when not in a container)
	socket  string // Docker socket path, bind-mounted into the helper
}

// NewSelfUpdater wires a SelfUpdater.
func NewSelfUpdater(jobs *store.Jobs, emitter Emitter, dc SelfDocker, checker SelfChecker, selfID, socket string) *SelfUpdater {
	return &SelfUpdater{jobs: jobs, emitter: emitter, docker: dc, checker: checker, selfID: selfID, socket: socket}
}

// Handle executes the self_update job.
func (u *SelfUpdater) Handle(ctx context.Context, job store.Job) {
	emit := func(msg string) {
		if u.emitter != nil {
			u.emitter.Emit(job.ID, "system", msg)
		}
	}
	fail := func(msg string) {
		emit(msg)
		logger.Warnf("self-update: job %d failed: %s", job.ID, msg)
		_ = u.jobs.Finish(job.ID, "failed", nil, msg)
	}

	if u.selfID == "" {
		fail("self-update is only available when dockbrr runs in a container")
		return
	}
	res, err := u.checker.Check(ctx)
	if err != nil {
		fail("could not check for a dockbrr update: " + err.Error())
		return
	}
	if !res.UpdateAvailable {
		fail("no dockbrr update is available")
		return
	}
	currentRef, err := u.docker.ContainerImageRef(ctx, u.selfID)
	if err != nil {
		fail("could not resolve dockbrr's own image: " + err.Error())
		return
	}
	newImage := targetSelfImage(currentRef, res.Latest)
	emit("pulling " + newImage)
	if err := u.docker.ImagePull(ctx, newImage); err != nil {
		fail("pull " + newImage + " failed: " + err.Error())
		return
	}
	cmd := []string{"self-update-swap", "--socket", u.socket, "--target", u.selfID, "--image", newImage}
	if _, err := u.docker.SpawnUpdater(ctx, currentRef, cmd, u.socket); err != nil {
		fail("could not start the update helper: " + err.Error())
		return
	}
	emit("pulled " + newImage + "; restarting into the new version (dockbrr will be briefly unavailable)")
	logger.Infof("self-update: job %d spawned helper, restarting into %s", job.ID, newImage)
	_ = u.jobs.Finish(job.ID, "success", nil, "")
}

// targetSelfImage computes the image the self-update should move to. A
// floating tag (latest, or untagged, which detect.SplitRef also reports as
// "latest") is kept as-is: a re-pull moves its digest without changing the
// ref. A pinned tag is swapped to latestTag, with any leading "v" stripped to
// match the image tag convention (GoReleaser publishes "1.2.0", the release
// tag is "v1.2.0").
//
// A digest pins the ref regardless of any tag alongside it (docker pull
// resolves the digest and ignores the tag), so a digest always counts as
// "pinned" and must be swapped to latestTag. detect.SplitRef drops the
// digest before returning repo/tag, so a digest-stripped ref is otherwise
// indistinguishable from a genuinely untagged (floating) repo; the digest
// check below must happen before that distinction is lost.
func targetSelfImage(currentRef, latestTag string) string {
	hasDigest := strings.Contains(currentRef, "@")
	repo, tag := detect.SplitRef(currentRef)
	norm := strings.TrimPrefix(latestTag, "v")
	if norm == "" {
		return currentRef
	}
	if !hasDigest && (tag == "" || tag == "latest") {
		return currentRef
	}
	return repo + ":" + norm
}
