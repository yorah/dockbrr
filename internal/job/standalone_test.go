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

// pullRefs returns every ref passed to ImagePull, in order.
func (f *fakeRecreator) pullRefs() []string {
	var refs []string
	for _, o := range f.ops {
		if o.kind == "pull" {
			refs = append(refs, o.arg)
		}
	}
	return refs
}
func (f *fakeRecreator) InspectStatus(_ context.Context, id string) (job.ContainerStatus, error) {
	st := f.status
	if st == "" {
		st = "running"
	}
	return job.ContainerStatus{State: st, RawJSON: `{"Config":{"Image":"busybox:1"},"Name":"/adoring_saha"}`}, nil
}

// fakeResolver2 returns digest for any ref by default; byRef overrides that
// per-ref when set, so a test can give two tags of the same repo distinct
// remote digests (needed to prove a cross-tag resolve used the right tag).
type fakeResolver2 struct {
	digest string
	byRef  map[string]string
}

func (f fakeResolver2) Resolve(_ context.Context, ref string, _ registry.Platform) (registry.RemoteImage, error) {
	d := f.digest
	if v, ok := f.byRef[ref]; ok {
		d = v
	}
	return registry.RemoteImage{Ref: ref, Digest: d, PlatformDigest: d}, nil
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

// seedStandaloneCrossTagUpdate seeds a standalone service tracking an exact
// tag (busybox:1.36) with an open update suggested by the semver scan: a
// newer tag (1.37) whose remote digest (sha256:new) differs from what the
// tracked tag itself serves. Mirrors the compose crossTag scenario
// (worker.go) but for the standalone applier.
func seedStandaloneCrossTagUpdate(t *testing.T, db *store.DB) (pid int64, svc store.Service, upd store.Update) {
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
		ImageRef: "busybox:1.36", CurrentDigest: "sha256:old", State: "running", ContainerIDs: []string{"old-cid"}})
	if err != nil {
		t.Fatal(err)
	}
	svc, _ = services.Get(sid)
	uid, _, err := updates.RecordDrift(store.Update{ServiceID: sid, FromDigest: "sha256:old",
		ToDigest: "sha256:new", Tag: "1.37", Severity: "minor", Status: "available"})
	if err != nil {
		t.Fatal(err)
	}
	upd, _ = updates.GetLatestOpenByService(sid)
	_ = uid
	return pid, svc, upd
}

func newStandaloneApplier(db *store.DB, r *fakeRecreator) *job.StandaloneApplier {
	return newStandaloneApplierWithResolver(db, r, fakeResolver2{digest: "sha256:new"})
}

func newStandaloneApplierWithResolver(db *store.DB, r *fakeRecreator, resolver fakeResolver2) *job.StandaloneApplier {
	return job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		resolver, r, registry.HostPlatform(),
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

func newStandaloneApplierShortHealth(db *store.DB, r *fakeRecreator) *job.StandaloneApplier {
	return job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		fakeResolver2{digest: "sha256:new"}, r, registry.HostPlatform(),
		func() time.Duration { return 20 * time.Millisecond }, func() time.Duration { return time.Millisecond },
	)
}

func TestStandaloneApplyRestoresOnHealthGateFailure(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	// The new container is created and started successfully, but its inspected
	// status is "exited" (not running), so healthGate times out and fails.
	r := &fakeRecreator{newID: "new-cid", status: "exited"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplierShortHealth(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}

	// The new container must be stopped BEFORE it is removed (ContainerRemove
	// is no-force and errors on a running container), then the old container
	// renamed back and restarted.
	var stopNewIdx, removeNewIdx, renameBackIdx, startOldIdx = -1, -1, -1, -1
	for i, o := range r.ops {
		switch {
		case o.kind == "stop" && o.arg == "new-cid":
			stopNewIdx = i
		case o.kind == "remove" && o.arg == "new-cid":
			removeNewIdx = i
		case o.kind == "rename" && o.arg == "old-cid->adoring_saha":
			renameBackIdx = i
		case o.kind == "start" && o.arg == "old-cid":
			startOldIdx = i
		}
	}
	if stopNewIdx == -1 {
		t.Fatalf("new container was never stopped: %+v", r.ops)
	}
	if removeNewIdx == -1 {
		t.Fatalf("new container was never removed: %+v", r.ops)
	}
	if stopNewIdx >= removeNewIdx {
		t.Fatalf("stop(new) must precede remove(new): %+v", r.ops)
	}
	if renameBackIdx == -1 || renameBackIdx <= removeNewIdx {
		t.Fatalf("rename(old back) must follow remove(new): %+v", r.ops)
	}
	if startOldIdx == -1 || startOldIdx <= renameBackIdx {
		t.Fatalf("start(old) must follow rename(old back): %+v", r.ops)
	}

	// The service runtime/digest must NOT have moved.
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:old" {
		t.Fatalf("digest = %q, want unchanged sha256:old after failed apply", got.CurrentDigest)
	}
}

func TestStandaloneRollbackRecreatesFromSnapshot(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	// Simulate a prior apply: a snapshot exists with the old image identity.
	_, _ = store.NewSnapshots(db).Insert(store.Snapshot{
		ServiceID: svc.ID, PrevRepo: "busybox", PrevDigest: "sha256:old", PrevImageID: "img-old",
		PrevContainerInspect: `{"Config":{"Image":"busybox:latest"},"Name":"/adoring_saha"}`,
	})
	// The service is now running the new image (post-apply state).
	_ = store.NewServices(db).UpdateRuntime(svc.ID, []string{"new-cid"}, "sha256:new")

	r := &fakeRecreator{newID: "restored-cid", status: "running"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "rollback", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}
	// Recreated with the OLD image ref (busybox@sha256:old or busybox:latest);
	// assert a create happened and the service digest returned to old.
	var created bool
	for _, o := range r.ops {
		if o.kind == "create" {
			created = true
		}
	}
	if !created {
		t.Fatalf("no recreate happened on rollback: %+v", r.ops)
	}
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:old" {
		t.Fatalf("digest = %q, want sha256:old after rollback", got.CurrentDigest)
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

// TestStandaloneApplyHonorsCrossTagUpdate proves runApply resolves/pulls/
// creates against the update's suggested tag (upd.Tag), not the tracked tag
// (svc.ImageRef), when the semver scan proposed a newer tag. Before the fix,
// targetRef stayed pinned to svc.ImageRef ("busybox:1.36"), whose remote
// digest ("sha256:old-remote") never matches upd.ToDigest ("sha256:new"), so
// the precheck always marked the update "superseded" and apply never ran.
func TestStandaloneApplyHonorsCrossTagUpdate(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneCrossTagUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	resolver := fakeResolver2{
		digest: "sha256:old-remote", // wrong-tag resolves would hit this and fail precheck
		byRef:  map[string]string{"busybox:1.37": "sha256:new"},
	}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplierWithResolver(db, r, resolver).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success (cross-tag update must not be marked superseded)", done.Status)
	}
	upd, err := store.NewUpdates(db).GetLatestOpenByService(svc.ID)
	if err == nil {
		t.Fatalf("expected no open update after successful apply, got %+v", upd)
	}

	// Pull and create must have used the suggested tag "busybox:1.37", not the
	// tracked tag "busybox:1.36".
	refs := r.pullRefs()
	if len(refs) != 1 || refs[0] != "busybox:1.37" {
		t.Fatalf("pull refs = %v, want [busybox:1.37]", refs)
	}
	var createArg string
	for _, o := range r.ops {
		if o.kind == "create" {
			createArg = o.arg
		}
	}
	if createArg != "adoring_saha@busybox:1.37" {
		t.Fatalf("create arg = %q, want adoring_saha@busybox:1.37", createArg)
	}

	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:new" {
		t.Fatalf("digest = %q, want sha256:new after cross-tag apply", got.CurrentDigest)
	}
}
