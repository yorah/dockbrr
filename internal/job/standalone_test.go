package job_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"dockbrr/internal/job"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

type recOp struct{ kind, arg string }

type fakeRecreator struct {
	ops        []recOp
	newID      string // id returned by ContainerCreateFromInspect
	status     string // InspectStatus state to report (default "running")
	createErr  error
	inspectErr error             // if set, InspectStatus returns this error for every id
	byName     map[string]string // name -> id, for ContainerIDByName; nil means "nothing by that name"
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

func (f *fakeRecreator) ContainerIDByName(_ context.Context, name string) (string, bool, error) {
	id, ok := f.byName[name]
	return id, ok, nil
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
	if f.inspectErr != nil {
		return job.ContainerStatus{}, f.inspectErr
	}
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
		nil, // nil emitter: allowed, emit() nil-guards
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

// TestStandaloneApplyClearsLeftoverOldContainer proves recreate cleans up a
// leftover "<name>-dockbrr-old" container from a prior crashed attempt before
// stopping/renaming the current one: without this, the resumed run's own
// ContainerRename(oldID, name+oldSuffix) would fail because the name is
// already taken by the stale container. The leftover must be stopped and
// removed BEFORE the create call, and the apply must still succeed.
func TestStandaloneApplyClearsLeftoverOldContainer(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{
		newID:  "new-cid",
		status: "running",
		byName: map[string]string{
			"adoring_saha-dockbrr-old": "stale-old-cid", // leftover, id != current oldID ("old-cid")
			"adoring_saha":             "old-cid",       // normal case: primary name held by the current container
		},
	}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}

	var stopStaleIdx, removeStaleIdx, createIdx = -1, -1, -1
	for i, o := range r.ops {
		switch {
		case o.kind == "stop" && o.arg == "stale-old-cid":
			stopStaleIdx = i
		case o.kind == "remove" && o.arg == "stale-old-cid":
			removeStaleIdx = i
		case o.kind == "create":
			createIdx = i
		}
	}
	if stopStaleIdx == -1 {
		t.Fatalf("leftover old container was never stopped: %+v", r.ops)
	}
	if removeStaleIdx == -1 {
		t.Fatalf("leftover old container was never removed: %+v", r.ops)
	}
	if createIdx == -1 || stopStaleIdx >= createIdx || removeStaleIdx >= createIdx {
		t.Fatalf("leftover cleanup must precede create: %+v", r.ops)
	}
	// The primary name (held by the current container "old-cid") must NOT be
	// touched: no stop/remove op against "old-cid" before the normal
	// stop(old)/rename(old) sequence removes it via the regular apply flow.
	for _, o := range r.ops[:createIdx] {
		if (o.kind == "remove") && o.arg == "old-cid" {
			t.Fatalf("current container old-cid must not be removed as a leftover: %+v", r.ops)
		}
	}
}

func newStandaloneApplierShortHealth(db *store.DB, r *fakeRecreator) *job.StandaloneApplier {
	return job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		fakeResolver2{digest: "sha256:new"}, r, registry.HostPlatform(),
		func() time.Duration { return 20 * time.Millisecond }, func() time.Duration { return time.Millisecond },
		nil, // nil emitter: allowed, emit() nil-guards
	)
}

// newStandaloneApplierLongHealth uses a deliberately LONG health timeout so a
// fail-fast test can distinguish "returned on the first poll" (milliseconds)
// from "looped until the timeout" (seconds) by a wide, race-robust margin,
// instead of racing a tight sub-timeout wall-clock bound.
func newStandaloneApplierLongHealth(db *store.DB, r *fakeRecreator) *job.StandaloneApplier {
	return job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		fakeResolver2{digest: "sha256:new"}, r, registry.HostPlatform(),
		func() time.Duration { return 10 * time.Second }, func() time.Duration { return 50 * time.Millisecond },
		nil, // nil emitter: allowed, emit() nil-guards
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
	// The tracked image ref must be persisted immediately to the new tag
	// (busybox:1.37), not left at the old tracked tag (busybox:1.36) until the
	// next discovery reconcile.
	if got.ImageRef != "busybox:1.37" {
		t.Fatalf("image ref = %q, want busybox:1.37 after cross-tag apply", got.ImageRef)
	}
}

// TestStandaloneApplySameTagDoesNotChangeImageRef proves a same-tag apply
// (upd.Tag equal to the tracked tag, or empty for pre-cross-tag-feature
// updates) leaves svc.ImageRef untouched: only a cross-tag apply needs the
// eager persist, since same-tag applies never change the tracked ref.
func TestStandaloneApplySameTagDoesNotChangeImageRef(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.ImageRef != "busybox:latest" {
		t.Fatalf("image ref = %q, want unchanged busybox:latest after same-tag apply", got.ImageRef)
	}
}

// TestStandaloneApplyFailsBeforeMutationOnInspectError proves runApply
// prechecks the current container via InspectStatus BEFORE any mutation:
// before the fix, an inspect error was swallowed with a fallback snapshot of
// "{}", and apply proceeded to snapshot/pull/stop/rename the live container
// before failing inside create. Now it must fail immediately, write no
// snapshot, and perform zero docker ops.
func TestStandaloneApplyFailsBeforeMutationOnInspectError(t *testing.T) {
	db := openJobDB(t)
	pid, svc, upd := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", inspectErr: errors.New("no such container")}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
	if len(r.ops) != 0 {
		t.Fatalf("no docker mutation should have happened, ops=%+v", r.ops)
	}
	if _, err := store.NewSnapshots(db).GetLatestForService(svc.ID); err == nil {
		t.Fatalf("no snapshot should have been written when the precheck inspect fails")
	}
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:old" {
		t.Fatalf("digest = %q, want unchanged sha256:old", got.CurrentDigest)
	}
	// Nothing was mutated, so the update must stay open for retry (plain fail,
	// not failApply): it must NOT be marked "failed".
	updRow, err := store.NewUpdates(db).Get(upd.ID)
	if err != nil {
		t.Fatalf("load update: %v", err)
	}
	if updRow.Status != "available" {
		t.Fatalf("update status = %q, want available (pre-mutation failure must not close the update)", updRow.Status)
	}
}

// TestStandaloneApplyFailsBeforeMutationOnSnapshotError proves a snapshot
// Insert failure (still pre-mutation: nothing has been stopped/renamed/created
// yet) uses plain fail, not failApply, leaving the update open for retry.
// Renaming the state_snapshots table out from under Insert forces the
// statement itself to fail, without disturbing the jobs/services/updates
// rows the assertions below depend on.
func TestStandaloneApplyFailsBeforeMutationOnSnapshotError(t *testing.T) {
	db := openJobDB(t)
	pid, svc, upd := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	if _, err := db.Exec(`ALTER TABLE state_snapshots RENAME TO state_snapshots_gone`); err != nil {
		t.Fatalf("rename snapshots table: %v", err)
	}

	newStandaloneApplier(db, r).Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
	if len(r.ops) != 0 {
		t.Fatalf("no docker mutation should have happened, ops=%+v", r.ops)
	}
	updRow, err := store.NewUpdates(db).Get(upd.ID)
	if err != nil {
		t.Fatalf("load update: %v", err)
	}
	if updRow.Status != "available" {
		t.Fatalf("update status = %q, want available (pre-mutation failure must not close the update)", updRow.Status)
	}
}

// TestStandaloneApplyHealthGateFailsFastOnExited proves healthGate returns
// immediately when the recreated container reports state "exited", instead of
// polling until the (short) timeout elapses. Before the fix, an
// immediately-exited container (or one flapping exited/running under
// restart:always) could be sampled mid-poll while transiently "running" and
// pass the gate, marking a broken update applied. An always-exited fake is
// sufficient to prove the fail-fast path; it also confirms the job does not
// hang for the full timeout. The applier here uses a LONG (10s) health
// timeout on purpose: fail-fast returns on the FIRST poll (milliseconds),
// while a regression that looped waiting for "running" would take ~10s, so
// the 2s wall-clock bound below separates the two by a wide, race-robust
// margin. The old container must also be restored.
func TestStandaloneApplyHealthGateFailsFastOnExited(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "exited"}
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	start := time.Now()
	newStandaloneApplierLongHealth(db, r).Handle(context.Background(), j)
	elapsed := time.Since(start)

	done, _ := jobs.Get(jid)
	if done.Status != "failed" {
		t.Fatalf("status = %q, want failed", done.Status)
	}
	// Fail-fast: the first poll observes "exited" and returns an error instead
	// of looping to the 10s timeout. The 2s bound is far above any race-detector
	// or CI scheduling overhead in the surrounding Handle, yet far below the 10s
	// a looping regression would take.
	if elapsed >= 2*time.Second {
		t.Fatalf("healthGate took %s, want fail-fast well under the 10s timeout", elapsed)
	}

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
	got, _ := store.NewServices(db).Get(svc.ID)
	if got.CurrentDigest != "sha256:old" {
		t.Fatalf("digest = %q, want unchanged sha256:old after failed apply", got.CurrentDigest)
	}
}

// recEmit is one captured (jobID, stream, line) tuple.
type recEmit struct {
	jobID  int64
	stream string
	line   string
}

// recordingEmitter captures every emitted line so a test can assert the
// live-log panel would not open empty. Not safe for concurrent use; Handle
// runs synchronously in these tests.
type recordingEmitter struct {
	lines []recEmit
}

func (r *recordingEmitter) Emit(jobID int64, stream, line string) {
	r.lines = append(r.lines, recEmit{jobID, stream, line})
}

// TestStandaloneApplyEmitsProgressLines proves a successful standalone apply
// emits at least one "system" progress line through the Emitter, so the
// live-log panel (which streams a job's lines) is not empty (backlog wl-p2).
func TestStandaloneApplyEmitsProgressLines(t *testing.T) {
	db := openJobDB(t)
	pid, svc, _ := seedStandaloneUpdate(t, db)
	r := &fakeRecreator{newID: "new-cid", status: "running"}
	emitter := &recordingEmitter{}
	applier := job.NewStandaloneApplier(
		store.NewJobs(db), store.NewUpdates(db), store.NewServices(db), store.NewProjects(db),
		store.NewSnapshots(db), store.NewEvents(db),
		fakeResolver2{digest: "sha256:new"}, r, registry.HostPlatform(),
		func() time.Duration { return time.Minute }, func() time.Duration { return time.Millisecond },
		emitter,
	)
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "apply", ServiceID: &svc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)

	applier.Handle(context.Background(), j)

	done, _ := jobs.Get(jid)
	if done.Status != "success" {
		t.Fatalf("status = %q, want success", done.Status)
	}
	if len(emitter.lines) == 0 {
		t.Fatalf("no progress lines emitted; live-log panel would be empty")
	}
	for _, l := range emitter.lines {
		if l.jobID != jid {
			t.Fatalf("emitted line for wrong job id %d, want %d: %+v", l.jobID, jid, l)
		}
		if l.stream != "system" {
			t.Fatalf("emitted line on unexpected stream %q: %+v", l.stream, l)
		}
	}
}
