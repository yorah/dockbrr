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
	ImageVersion(ctx context.Context, ref string) (string, error)
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
		// Reachable only on a rare race: the HTTP endpoint pre-checks update
		// availability before enqueuing, but the answer can change in the gap.
		// Not an error: nothing to do, so the job succeeds as a no-op.
		emit("dockbrr is already up to date, nothing to do")
		_ = u.jobs.Finish(job.ID, "success", nil, "")
		return
	}
	emit("this is dockbrr updating itself: the new image is pulled, then a short-lived helper swaps the container. A normal compose apply is not used here, and dockbrr will restart.")
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
	// A release's GitHub tag (which drives res.UpdateAvailable) is published
	// BEFORE GoReleaser pushes the image to the registry. In that window a
	// floating tag like :latest still resolves to the OLD image, so the pull
	// above succeeds without advancing the version. Confirm the pulled image
	// really is the release we intend to install; otherwise dockbrr would
	// "successfully" restart into the same old version and the update would keep
	// reappearing. The update stays available (correctly) for a later retry.
	if msg := verifyPulledVersion(ctx, u.docker, newImage, res.Latest); msg != "" {
		fail(msg)
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

// verifyPulledVersion returns "" when the just-pulled newImage carries a
// version (its org.opencontainers.image.version label) at least as new as the
// intended release `latest`, and a user-facing failure message otherwise. It is
// deliberately fail-closed: an unreadable, missing, or unparsable label aborts
// the swap rather than restarting into an unknown image, since every genuine
// dockbrr release image is stamped with a clean semver label.
func verifyPulledVersion(ctx context.Context, dc SelfDocker, newImage, latest string) string {
	got, err := dc.ImageVersion(ctx, newImage)
	if err != nil {
		return "could not read the pulled image's version: " + err.Error()
	}
	gotCore, okGot := detect.ParseCore(got)
	wantCore, okWant := detect.ParseCore(latest)
	if okGot && okWant && !detect.CoreLess(gotCore, wantCore) {
		return ""
	}
	pulled := got
	if pulled == "" {
		pulled = "an image with no version label"
	}
	return "the " + latest + " image is not published to the registry yet (pulled " + pulled + "); the update stays available, try again in a few minutes"
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
	// Degenerate guard: only reachable if latestTag is literally "v" or ""
	// (GitHub never publishes either); without it, the fallthrough below would
	// produce a malformed "repo:" ref, so keep currentRef unchanged instead.
	if norm == "" {
		return currentRef
	}
	if !hasDigest && tag == "latest" {
		return currentRef
	}
	return repo + ":" + norm
}
