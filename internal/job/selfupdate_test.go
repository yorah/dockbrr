package job

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

func TestTargetSelfImage(t *testing.T) {
	cases := []struct{ ref, latest, want string }{
		{"ghcr.io/yorah/dockbrr:latest", "v1.2.0", "ghcr.io/yorah/dockbrr:latest"}, // floating kept
		{"ghcr.io/yorah/dockbrr:1.1.0", "v1.2.0", "ghcr.io/yorah/dockbrr:1.2.0"},   // pinned swapped, v stripped
		{"ghcr.io/yorah/dockbrr", "v1.2.0", "ghcr.io/yorah/dockbrr"},               // untagged == floating, kept
		{"ghcr.io/yorah/dockbrr@sha256:deadbeef", "v1.2.0", "ghcr.io/yorah/dockbrr:1.2.0"},        // pure digest, moved off pin
		{"ghcr.io/yorah/dockbrr:latest@sha256:deadbeef", "v1.2.0", "ghcr.io/yorah/dockbrr:1.2.0"}, // tag+digest, latest was pinned
		{"ghcr.io/yorah/dockbrr:1.1.0@sha256:deadbeef", "v1.2.0", "ghcr.io/yorah/dockbrr:1.2.0"},  // tag+digest, semver pinned
		{"ghcr.io/yorah/dockbrr:1.1.0", "v", "ghcr.io/yorah/dockbrr:1.1.0"},                       // degenerate latestTag "v" normalizes to "", currentRef kept
	}
	for _, c := range cases {
		if got := targetSelfImage(c.ref, c.latest); got != c.want {
			t.Errorf("targetSelfImage(%q,%q) = %q, want %q", c.ref, c.latest, got, c.want)
		}
	}
}

type fakeSelfDocker struct {
	imageRef     string
	pulled       string
	pullErr      error
	imageVersion string
	versionErr   error
	spawnedCmd   []string
	spawnErr     error
}

func (f *fakeSelfDocker) ContainerImageRef(ctx context.Context, id string) (string, error) {
	return f.imageRef, nil
}
func (f *fakeSelfDocker) ImagePull(ctx context.Context, ref string) error {
	f.pulled = ref
	return f.pullErr
}
func (f *fakeSelfDocker) ImageVersion(ctx context.Context, ref string) (string, error) {
	return f.imageVersion, f.versionErr
}
func (f *fakeSelfDocker) SpawnUpdater(ctx context.Context, image string, cmd []string, socket string) (string, error) {
	f.spawnedCmd = cmd
	return "helper123", f.spawnErr
}

type fakeChecker struct {
	res selfupdate.Result
	err error
}

func (f fakeChecker) Check(ctx context.Context) (selfupdate.Result, error) { return f.res, f.err }

// newJobFixture opens a store and returns a *store.Jobs plus a no-op Emitter.
func newJobFixture(t *testing.T) (*store.Jobs, Emitter) {
	t.Helper()
	db := newEngineDB(t)
	return store.NewJobs(db), nopEmitter{}
}

// enqueueSelfUpdate enqueues and claims one self_update job, returning it.
func enqueueSelfUpdate(t *testing.T, jobs *store.Jobs) store.Job {
	t.Helper()
	if _, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	j, ok, err := jobs.ClaimNext()
	if err != nil || !ok {
		t.Fatalf("claim self_update job: ok=%v err=%v", ok, err)
	}
	return j
}

func TestSelfUpdaterNoUpdateAvailable(t *testing.T) {
	jobs, _ := newJobFixture(t)
	var lines []string
	emitter := recordingEmitter{lines: &lines}
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0"}
	ck := fakeChecker{res: selfupdate.Result{Latest: "1.1.0", UpdateAvailable: false}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.pulled != "" {
		t.Errorf("pulled despite no update available: %q", fd.pulled)
	}
	if fd.spawnedCmd != nil {
		t.Errorf("helper spawned despite no update available: %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "success" {
		t.Errorf("job status = %q, want success (no-op)", got.Status)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "already up to date") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %v, want an explanatory already-up-to-date line", lines)
	}
}

func TestSelfUpdaterHappyPath(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0", imageVersion: "1.2.0"}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.pulled != "ghcr.io/yorah/dockbrr:1.2.0" {
		t.Errorf("pulled = %q, want ...:1.2.0", fd.pulled)
	}
	if len(fd.spawnedCmd) == 0 || fd.spawnedCmd[0] != "self-update-swap" {
		t.Errorf("helper cmd = %v", fd.spawnedCmd)
	}
	wantCmd := []string{"self-update-swap", "--socket", "/var/run/docker.sock", "--target", "abc123def456", "--image", "ghcr.io/yorah/dockbrr:1.2.0"}
	if len(fd.spawnedCmd) != len(wantCmd) {
		t.Fatalf("helper cmd = %v, want %v", fd.spawnedCmd, wantCmd)
	}
	for i := range wantCmd {
		if fd.spawnedCmd[i] != wantCmd[i] {
			t.Errorf("helper cmd[%d] = %q, want %q", i, fd.spawnedCmd[i], wantCmd[i])
		}
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "success" {
		t.Errorf("job status = %q, want success", got.Status)
	}
}

func TestSelfUpdaterPullFailureKeepsRunning(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0", pullErr: errors.New("network down")}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.spawnedCmd != nil {
		t.Errorf("helper spawned despite pull failure: %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}

// TestSelfUpdaterStaleRegistryImageAborts covers the release-window race: the
// GitHub tag for v1.2.0 exists (update available) but GoReleaser has not pushed
// the image yet, so pulling the floating :latest tag resolves to the OLD 1.1.0
// image. The pull succeeds, but the swap must NOT proceed and the job must fail
// (not falsely report success), leaving the update available for a later retry.
func TestSelfUpdaterStaleRegistryImageAborts(t *testing.T) {
	jobs, _ := newJobFixture(t)
	var lines []string
	emitter := recordingEmitter{lines: &lines}
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:latest", imageVersion: "1.1.0"}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.pulled != "ghcr.io/yorah/dockbrr:latest" {
		t.Errorf("pulled = %q, want floating latest", fd.pulled)
	}
	if fd.spawnedCmd != nil {
		t.Errorf("helper spawned despite stale registry image: %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed (no false success)", got.Status)
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "not published to the registry yet") {
			found = true
		}
	}
	if !found {
		t.Errorf("lines = %v, want a not-published-yet explanation", lines)
	}
}

// TestSelfUpdaterUnlabeledImageAborts: an image with no version label cannot be
// verified as the intended release, so the updater fails closed rather than
// restarting into an unknown image.
func TestSelfUpdaterUnlabeledImageAborts(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0", imageVersion: ""}
	ck := fakeChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "abc123def456", "/var/run/docker.sock")

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.spawnedCmd != nil {
		t.Errorf("helper spawned despite unverifiable image: %v", fd.spawnedCmd)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}

func TestSelfUpdaterNotInContainer(t *testing.T) {
	jobs, emitter := newJobFixture(t)
	fd := &fakeSelfDocker{}
	ck := fakeChecker{res: selfupdate.Result{UpdateAvailable: true}}
	u := NewSelfUpdater(jobs, emitter, fd, ck, "", "/var/run/docker.sock") // empty selfID

	j := enqueueSelfUpdate(t, jobs)
	u.Handle(context.Background(), j)

	if fd.pulled != "" {
		t.Errorf("pulled despite not-in-container: %q", fd.pulled)
	}
	got, _ := jobs.Get(j.ID)
	if got.Status != "failed" {
		t.Errorf("job status = %q, want failed", got.Status)
	}
}
