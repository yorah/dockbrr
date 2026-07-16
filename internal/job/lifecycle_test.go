package job_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"dockbrr/internal/compose"
	"dockbrr/internal/job"
	"dockbrr/internal/store"
)

type opRec struct{ kind, id string }

type fakeMutator struct {
	ops    []opRec
	failOn string // if set, return error when op kind matches
}

func (f *fakeMutator) ContainerStart(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"start", id})
	if f.failOn == "start" {
		return fmt.Errorf("injected error for start")
	}
	return nil
}
func (f *fakeMutator) ContainerStop(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"stop", id})
	if f.failOn == "stop" {
		return fmt.Errorf("injected error for stop")
	}
	return nil
}
func (f *fakeMutator) ContainerRemove(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"remove", id})
	if f.failOn == "remove" {
		return fmt.Errorf("injected error for remove")
	}
	return nil
}

// fakeComposer returns a fixed parsed project (web shares db's namespace).
type fakeComposer struct{}

func (fakeComposer) Parse(_ context.Context, _ string, _ []string) (compose.Project, error) {
	return compose.Project{Services: []compose.Service{
		{Name: "db"},
		{Name: "web", NetworkMode: "service:db"},
	}}, nil
}
func (fakeComposer) HashFiles(_ []string) (string, error) { return "", nil }

func openJobDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// seedComposeProject creates a compose project with db + web services, each
// carrying one container id, and returns the project id and the two services.
func seedComposeProject(t *testing.T, db *store.DB) (pid int64, db2, web store.Service) {
	t.Helper()
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	var err error
	pid, err = projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app",
		WorkingDir: "/srv/app", ConfigFiles: []string{"docker-compose.yml"}, Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	dbID, err := services.Upsert(store.Service{ProjectID: pid, Name: "db",
		ImageRef: "postgres:16", CurrentDigest: "sha256:d", State: "running", ContainerIDs: []string{"db-cid"}})
	if err != nil {
		t.Fatal(err)
	}
	webID, err := services.Upsert(store.Service{ProjectID: pid, Name: "web",
		ImageRef: "nginx:1", CurrentDigest: "sha256:w", State: "running", ContainerIDs: []string{"web-cid"}})
	if err != nil {
		t.Fatal(err)
	}
	db2, _ = services.Get(dbID)
	web, _ = services.Get(webID)
	return pid, db2, web
}

func newLifecycle(db *store.DB, m job.Mutator) *job.Lifecycle {
	return job.NewLifecycle(
		store.NewJobs(db), store.NewServices(db), store.NewProjects(db), store.NewEvents(db),
		m, fakeComposer{}, nil, // nil rediscoverer: allowed, skipped when nil
	)
}

func TestLifecycleStopOrdersDependentsFirst(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "stop", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	// Stopping db must stop its namespace dependent web first, then db.
	if len(m.ops) != 2 || m.ops[0] != (opRec{"stop", "web-cid"}) || m.ops[1] != (opRec{"stop", "db-cid"}) {
		t.Fatalf("stop order = %+v, want [stop web-cid, stop db-cid]", m.ops)
	}
	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("job status = %q, want success", done.Status)
	}
}

func TestLifecycleStartOrdersTargetFirst(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "start", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	if len(m.ops) != 2 || m.ops[0] != (opRec{"start", "db-cid"}) || m.ops[1] != (opRec{"start", "web-cid"}) {
		t.Fatalf("start order = %+v, want [start db-cid, start web-cid]", m.ops)
	}
	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("job status = %q, want success", done.Status)
	}
}

func TestLifecycleRemoveRefusesRunning(t *testing.T) {
	db := openJobDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha", Source: "discovered"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha",
		ImageRef: "busybox:latest", CurrentDigest: "sha256:b", State: "running", ContainerIDs: []string{"c1"}})
	svc, _ := services.Get(sid)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "remove", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	if len(m.ops) != 0 {
		t.Fatalf("running container must not be removed, ops=%+v", m.ops)
	}
	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
}

func TestLifecycleRemoveRefusesCompose(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db) // compose project, state running
	// Force stopped so only the kind guard can reject it.
	services := store.NewServices(db)
	stopped := dbSvc
	stopped.State = "exited"
	_, _ = services.Upsert(stopped)
	svc, _ := services.Get(dbSvc.ID)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "remove", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	if len(m.ops) != 0 {
		t.Fatalf("compose service must not be removed, ops=%+v", m.ops)
	}
	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
}

func TestLifecycleRemoveStoppedStandalone(t *testing.T) {
	db := openJobDB(t)
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha", Source: "discovered"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha",
		ImageRef: "busybox:latest", CurrentDigest: "sha256:b", State: "exited", ContainerIDs: []string{"c1"}})
	svc, _ := services.Get(sid)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "remove", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	if len(m.ops) != 1 || m.ops[0] != (opRec{"remove", "c1"}) {
		t.Fatalf("ops = %+v, want [remove c1]", m.ops)
	}
	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}
}

func TestLifecycleRestartOrdersStopThenStart(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "restart", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	want := []opRec{{"stop", "web-cid"}, {"stop", "db-cid"}, {"start", "db-cid"}, {"start", "web-cid"}}
	if len(m.ops) != len(want) {
		t.Fatalf("restart ops = %+v, want %+v", m.ops, want)
	}
	for i := range want {
		if m.ops[i] != want[i] {
			t.Fatalf("restart ops = %+v, want %+v", m.ops, want)
		}
	}
	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}
}

func TestLifecycleMutatorErrorFailsJob(t *testing.T) {
	db := openJobDB(t)
	pid, dbSvc, _ := seedComposeProject(t, db)
	m := &fakeMutator{failOn: "stop"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "stop", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newLifecycle(db, m).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
}
