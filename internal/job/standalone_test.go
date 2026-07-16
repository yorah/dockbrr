package job_test

import (
	"context"
	"testing"
	"time"

	"dockbrr/internal/job"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

type recOp struct{ kind, arg string }

type fakeRecreator struct {
	ops       []recOp
	newID     string // id returned by ContainerCreateFromInspect
	status    string // InspectStatus state to report (default "running")
	createErr error
}

func (f *fakeRecreator) ImagePull(_ context.Context, ref string) error {
	f.ops = append(f.ops, recOp{"pull", ref})
	return nil
}
func (f *fakeRecreator) ContainerStop(_ context.Context, id string) error {
	f.ops = append(f.ops, recOp{"stop", id})
	return nil
}
func (f *fakeRecreator) ContainerStart(_ context.Context, id string) error {
	f.ops = append(f.ops, recOp{"start", id})
	return nil
}
func (f *fakeRecreator) ContainerRename(_ context.Context, id, name string) error {
	f.ops = append(f.ops, recOp{"rename", id + "->" + name})
	return nil
}
func (f *fakeRecreator) ContainerRemove(_ context.Context, id string) error {
	f.ops = append(f.ops, recOp{"remove", id})
	return nil
}
func (f *fakeRecreator) ContainerCreateFromInspect(_ context.Context, _, newImage, name string) (string, error) {
	f.ops = append(f.ops, recOp{"create", name + "@" + newImage})
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.newID, nil
}
func (f *fakeRecreator) InspectStatus(_ context.Context, id string) (job.ContainerStatus, error) {
	st := f.status
	if st == "" {
		st = "running"
	}
	return job.ContainerStatus{State: st, RawJSON: `{"Config":{"Image":"busybox:1"},"Name":"/adoring_saha"}`}, nil
}

type fakeResolver2 struct{ digest string }

func (f fakeResolver2) Resolve(_ context.Context, ref string, _ registry.Platform) (registry.RemoteImage, error) {
	return registry.RemoteImage{Ref: ref, Digest: f.digest, PlatformDigest: f.digest}, nil
}
func (f fakeResolver2) ListTags(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (f fakeResolver2) Head(_ context.Context, _ string) (string, error)       { return "", nil }

func seedStandaloneUpdate(t *testing.T, db *store.DB) (pid int64, svc store.Service, upd store.Update) {
	t.Helper()
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	updates := store.NewUpdates(db)
	var err error
	pid, err = projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha", Source: "discovered"})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha",
		ImageRef: "busybox:latest", CurrentDigest: "sha256:old", State: "running", ContainerIDs: []string{"old-cid"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ = services.Get(sid)
	uid, _, err := updates.RecordDrift(store.Update{ServiceID: sid, FromDigest: "sha256:old",
		ToDigest: "sha256:new", Tag: "latest", Severity: "digest-only", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	upd, _ = updates.GetLatestOpenByService(sid)
	_ = uid
	return pid, svc, upd
}

func newStandaloneApplier(db *store.DB, r *fakeRecreator) *job.StandaloneApplier {
	return job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		fakeResolver2{digest: "sha256:new"}, r, registry.HostPlatform(),
		func() time.Duration { return time.Minute }, func() time.Duration { return time.Millisecond },
	)
}

func TestStandaloneApplyRecreates(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	// Pull precedes create; order stop -> rename -> create -> start -> remove(old).
	var order []string
	for _, o := range r.ops {
		order = append(order, o.kind)
	}
	want := []string{"pull", "stop", "rename", "create", "start", "remove"}
	if len(order) != len(want) {
		t.Fatalf("op order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("op order = %v, want %v", order, want)
		}
	}
	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("job status = %q, want success", done.Status)
	}
	// Snapshot was written before the mutation (enables rollback).
	if _, err := store.NewSnapshots(db).GetLatestForService(svc.ID); err != nil {
		t.Fatalf("no snapshot recorded: %v", err)
	}
	// Service runtime updated to the new container id + digest.
	got, _ := store.NewServices(db).Get(svc.ID)
	if len(got.ContainerIDs) != 1 || got.ContainerIDs[0] != "new-cid" || got.CurrentDigest != "sha256:new" {
		t.Fatalf("runtime = %+v, want new-cid @ sha256:new", got)
	}
}

func TestStandaloneApplyRestoresOnCreateFailure(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "", status: "running", createErr: context.DeadlineExceeded}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
	// The old container must have been renamed back and started again.
	var renamedBack, startedOld bool
	for _, o := range r.ops {
		if o.kind == "rename" && o.arg == "old-cid->adoring_saha" {
			renamedBack = true
		}
		if o.kind == "start" && o.arg == "old-cid" {
			startedOld = true
		}
	}
	if !renamedBack || !startedOld {
		t.Fatalf("old container not restored (renamedBack=%v startedOld=%v): %+v", renamedBack, startedOld, r.ops)
	}
	// The service runtime must NOT have moved to a new digest.
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:old" {
		t.Fatalf("digest = %q, want unchanged sha256:old after failed apply", got.CurrentDigest)
	}
}
