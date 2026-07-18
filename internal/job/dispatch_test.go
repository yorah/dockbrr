package job_test

import (
	"context"
	"strings"
	"testing"

	"dockbrr/internal/job"
	"dockbrr/internal/store"
)

func TestDispatcherRoutesLifecycleAndApply(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	lc := newLifecycle(db, m)

	// A lifecycle job routes to the Lifecycle runner (observed via the mutator).
	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db)) // applier nil: this test only drives a lifecycle kind
	jobs := store.NewJobs(db)
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

	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db))
	d.SetSelfGuard("abcdef123456", services, jobs, nil)

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
	d := job.NewDispatcher(nil, lc, nil, store.NewProjects(db))
	d.SetSelfGuard("abcdef123456", store.NewServices(db), jobs, nil)

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
	d := job.NewDispatcher(nil, nil, sa, store.NewProjects(db))
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)

	if len(r.ops) == 0 {
		t.Fatal("standalone apply did not reach the SDK recreate path")
	}
}
