package job_test

import (
	"context"
	"strings"
	"testing"

	"dockbrr/internal/job"
	"dockbrr/internal/selfupdate"
	"dockbrr/internal/store"
)

// fakeDispatchSelfDocker is a job.SelfDocker spy used to observe whether a
// self_update job actually reached the SelfUpdater's Handle.
type fakeDispatchSelfDocker struct {
	imageRef string
	pulled   string
}

func (f *fakeDispatchSelfDocker) ContainerImageRef(_ context.Context, _ string) (string, error) {
	return f.imageRef, nil
}
func (f *fakeDispatchSelfDocker) ImagePull(_ context.Context, ref string) error {
	f.pulled = ref
	return nil
}
func (f *fakeDispatchSelfDocker) SpawnUpdater(_ context.Context, _ string, _ []string, _ string) (string, error) {
	return "helper123", nil
}

type fakeDispatchChecker struct{ res selfupdate.Result }

func (f fakeDispatchChecker) Check(_ context.Context) (selfupdate.Result, error) { return f.res, nil }

// recordingDispatchEmitter captures emitted lines so dispatch tests can assert
// a live line was sent (dispatch_test.go is package job_test, so it can't
// reuse the internal recordingEmitter defined for package job's own tests).
type recordingDispatchEmitter struct {
	lines *[]string
}

func (r recordingDispatchEmitter) Emit(_ int64, _ string, line string) {
	*r.lines = append(*r.lines, line)
}

func TestDispatcherRoutesLifecycleAndApply(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	lc := newLifecycle(db, m)

	// A lifecycle job routes to the Lifecycle runner (observed via the mutator).
	jobs := store.NewJobs(db)
	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db), jobs) // applier nil: this test only drives a lifecycle kind
	jid, _ := jobs.Enqueue(store.Job{Type: "stop", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)
	if len(m.ops) == 0 {
		t.Fatal("stop job did not reach the lifecycle runner")
	}
}

func TestDispatcherSelfGuardRefusesOwnContainer(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	lc := newLifecycle(db, m)
	jobs := store.NewJobs(db)
	services := store.NewServices(db)

	// dbSvc's container is dockbrr's own container (full id, guard has short id).
	full := "abcdef123456" + "0000000000000000000000000000000000000000000000000000"
	if err := services.UpdateRuntime(dbSvc.ID, []string{full}, dbSvc.CurrentDigest); err != nil {
		t.Fatal(err)
	}

	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db), jobs)
	d.SetSelfGuard("abcdef123456", services, nil)

	// Service-scoped restart on self: refused, mutator never touched.
	jid, _ := jobs.Enqueue(store.Job{Type: "restart", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)
	if len(m.ops) != 0 {
		t.Fatalf("self-targeted restart reached the lifecycle runner: %v", m.ops)
	}
	got, _ := jobs.Get(jid)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if got.Error == "" || !strings.Contains(got.Error, "own container") {
		t.Fatalf("error = %q, want self-guard refusal", got.Error)
	}

	// Project-scoped stop on the project containing self: refused too.
	jid2, _ := jobs.Enqueue(store.Job{Type: "stop", ProjectID: &pid, Scope: "project"})
	j2, _ := jobs.Get(jid2)
	d.Handle(context.Background(), j2)
	if got2, _ := jobs.Get(jid2); got2.Status != "failed" {
		t.Fatalf("project-scoped status = %q, want failed", got2.Status)
	}
}

func TestDispatcherSelfGuardPassesOtherContainers(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	lc := newLifecycle(db, m)
	jobs := store.NewJobs(db)

	// Guard armed, but the target ("db-cid") is not our container.
	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db), jobs)
	d.SetSelfGuard("abcdef123456", store.NewServices(db), nil)

	jid, _ := jobs.Enqueue(store.Job{Type: "stop", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)
	if len(m.ops) == 0 {
		t.Fatal("non-self stop was blocked by the self-guard")
	}
}

func TestDispatcherRoutesStandaloneApplyToSDK(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	sa := newStandaloneApplier(db, r)

	// applier nil + lifecycle nil: this test only drives a standalone apply.
	jobs := store.NewJobs(db)
	d := job.NewDispatcher(nil, nil, sa, store.NewProjects(db), jobs)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)

	if len(r.ops) == 0 {
		t.Fatal("standalone apply did not reach the SDK recreate path")
	}
}

func TestDispatcherRoutesSelfUpdateToUpdater(t *testing.T) {
	db := openJobDB(t)
	jobs := store.NewJobs(db)
	fd := &fakeDispatchSelfDocker{imageRef: "ghcr.io/yorah/dockbrr:1.1.0"}
	ck := fakeDispatchChecker{res: selfupdate.Result{Latest: "v1.2.0", UpdateAvailable: true}}
	u := job.NewSelfUpdater(jobs, nil, fd, ck, "abc123def456", "/var/run/docker.sock")

	d := job.NewDispatcher(nil, nil, nil, nil, jobs)
	d.SetSelfUpdater(u)

	jid, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)

	if fd.pulled != "ghcr.io/yorah/dockbrr:1.2.0" {
		t.Fatalf("self_update job did not reach the SelfUpdater: pulled = %q", fd.pulled)
	}
	got, _ := jobs.Get(jid)
	if got.Status != "success" {
		t.Fatalf("status = %q, want success", got.Status)
	}
}

func TestDispatchSelfUpdateWithoutUpdaterFails(t *testing.T) {
	// A dispatcher with no SelfUpdater wired must fail a self_update job cleanly
	// (not panic, not route it to the compose applier). d.jobs is wired at
	// construction time now, so this stays green regardless of whether
	// SetSelfGuard (the containerized path) is ever called.
	db := openJobDB(t)
	jobs := store.NewJobs(db)
	services := store.NewServices(db)
	var lines []string
	emitter := recordingDispatchEmitter{lines: &lines}
	d := job.NewDispatcher(nil, nil, nil, nil, jobs)
	d.SetSelfGuard("abc123def456", services, emitter) // populates d.emitter; d.jobs already set above

	jid, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)

	got, _ := jobs.Get(jid)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	if len(lines) == 0 {
		t.Fatal("no live line emitted for nil-updater self_update fallback")
	}
	found := false
	for _, l := range lines {
		if strings.Contains(l, "self-update is not available") {
			found = true
		}
	}
	if !found {
		t.Fatalf("lines = %v, want a self-update-unavailable line", lines)
	}
}

func TestDispatchSelfUpdateFallbackFailsOnHostInstall(t *testing.T) {
	// Host installs never call SetSelfGuard: SelfContainerID() returns "" outside
	// a container, so main.go never arms the guard, and previously d.jobs (only
	// ever populated by SetSelfGuard) stayed nil. A resumed/stranded self_update
	// job would then hit the nil-updater fallback, be unable to call
	// d.jobs.Finish, and busy-loop as "running" forever. d.jobs is now wired at
	// construction time (NewDispatcher), independent of containerization, so
	// this must mark the job failed even with SetSelfGuard and SetSelfUpdater
	// both never called.
	db := openJobDB(t)
	jobs := store.NewJobs(db)
	d := job.NewDispatcher(nil, nil, nil, nil, jobs)

	jid, err := jobs.Enqueue(store.Job{Type: "self_update", RequestedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)

	got, _ := jobs.Get(jid)
	if got.Status != "failed" {
		t.Fatalf("status = %q, want failed (host-install nil-updater fallback must still mark the job failed)", got.Status)
	}
	if got.Error == "" || !strings.Contains(got.Error, "self-update is not available") {
		t.Fatalf("error = %q, want self-update-unavailable message", got.Error)
	}
}
