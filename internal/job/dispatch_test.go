package job_test

import (
	"context"
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
