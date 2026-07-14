package job

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dockbrr/internal/compose"
	"dockbrr/internal/docker"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// --- fakes -------------------------------------------------------------------

// fakeRunner records every RunSpec and returns programmed results per verb. When
// a spec carries an override file beyond the base config files, it captures the
// override's content (read at call time, before cleanup) for rollback assertions.
type fakeRunner struct {
	calls        []compose.RunSpec
	overrideText []string
	baseFiles    int
	exit         map[string]int
	err          map[string]error

	// forceRecreateExit/forceRecreateErr govern only the `--force-recreate`
	// spec (namespace-dependent recreate), distinguished from the primary
	// up/pull by args rather than verb since both are verb "up".
	forceRecreateExit int
	forceRecreateErr  error
}

func (f *fakeRunner) Run(_ context.Context, spec compose.RunSpec, sink compose.LogSink) (int, error) {
	f.calls = append(f.calls, spec)
	if len(spec.ConfigFiles) > f.baseFiles {
		last := spec.ConfigFiles[len(spec.ConfigFiles)-1]
		if b, err := os.ReadFile(last); err == nil {
			f.overrideText = append(f.overrideText, string(b))
		}
	}
	sink.Write("stdout", spec.Verb+" ran")
	if isForceRecreateSpec(spec) {
		if f.forceRecreateErr != nil {
			return 1, f.forceRecreateErr
		}
		return f.forceRecreateExit, nil
	}
	if e := f.err[spec.Verb]; e != nil {
		return 1, e
	}
	code := 0
	if f.exit != nil {
		code = f.exit[spec.Verb]
	}
	return code, nil
}

func isForceRecreateSpec(spec compose.RunSpec) bool {
	for _, a := range spec.Args {
		if a == "--force-recreate" {
			return true
		}
	}
	return false
}

func (f *fakeRunner) verbs() []string {
	var vs []string
	for _, c := range f.calls {
		vs = append(vs, c.Verb)
	}
	return vs
}

type fakeResolver struct {
	img registry.RemoteImage
	err error
}

func (f fakeResolver) Resolve(_ context.Context, ref string, _ registry.Platform) (registry.RemoteImage, error) {
	if f.err != nil {
		return registry.RemoteImage{}, f.err
	}
	out := f.img
	out.Ref = ref
	return out, nil
}

type fakeInspector struct {
	status docker.ContainerStatus
	err    error
}

func (f fakeInspector) InspectStatus(_ context.Context, _ string) (docker.ContainerStatus, error) {
	return f.status, f.err
}

// fakeRediscoverer stands in for *discovery.Locator. A zero value returns
// (nil, "", nil), "not found", so the gate falls back to the pre-apply ids.
// byService, when non-nil, answers per service name (used by project-scope
// tests that need the locator to distinguish the target service from its
// siblings); a name with no entry falls back to the single-value fields.
type fakeRediscoverer struct {
	ids       []string
	digest    string
	err       error
	byService map[string]rediscoverResult
}

type rediscoverResult struct {
	ids    []string
	digest string
	err    error
}

func (f fakeRediscoverer) LocateService(_ context.Context, _ string, serviceName string) ([]string, string, error) {
	if f.byService != nil {
		if r, ok := f.byService[serviceName]; ok {
			return r.ids, r.digest, r.err
		}
	}
	return f.ids, f.digest, f.err
}

type fakeComposer struct {
	proj     compose.Project
	parseErr error
	hash     string
	hashErr  error
}

func (f fakeComposer) Parse(_ context.Context, _ string, _ []string) (compose.Project, error) {
	return f.proj, f.parseErr
}
func (f fakeComposer) HashFiles(_ []string) (string, error) { return f.hash, f.hashErr }

type nopEmitter struct{}

func (nopEmitter) Emit(int64, string, string) {}

// recordingEmitter captures emitted lines so tests can assert on warn-and-
// continue messages that nopEmitter would otherwise silently discard.
type recordingEmitter struct {
	lines *[]string
}

func (r recordingEmitter) Emit(_ int64, _ string, line string) {
	*r.lines = append(*r.lines, line)
}

// --- harness -----------------------------------------------------------------

type applyFixture struct {
	db        *store.DB
	jobs      *store.Jobs
	updates   *store.Updates
	services  *store.Services
	snapshots *store.Snapshots
	events    *store.Events
	pid, sid  int64
	updateID  int64
	jobID     int64
}

const targetDigest = "sha256:target"

// newApplyFixture seeds a project/service/available-update and an apply job, with
// the project working dir on disk (for rollback override writes).
func newApplyFixture(t *testing.T, scope string) *applyFixture {
	t.Helper()
	db := newEngineDB(t)
	dir := t.TempDir()
	pid, err := store.NewProjects(db).Upsert(store.Project{
		HostID: 1, Kind: "compose", Name: "web", Source: "discovered",
		WorkingDir: dir, ConfigFiles: []string{filepath.Join(dir, "compose.yml")},
	})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := store.NewServices(db).Upsert(store.Service{
		ProjectID: pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.2.3",
		CurrentDigest: "sha256:old", CurrentImageID: "sha256:img-old",
		ContainerIDs: []string{"c1"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	updates := store.NewUpdates(db)
	uid, err := updates.Upsert(store.Update{ServiceID: sid, FromDigest: "sha256:old", ToDigest: targetDigest, Status: "available", Severity: "minor"})
	if err != nil {
		t.Fatal(err)
	}
	jobs := store.NewJobs(db)
	jid, err := jobs.Enqueue(store.Job{Type: "apply", ProjectID: &pid, ServiceID: &sid, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	// Move it to running as the engine would before dispatch.
	if _, _, err := jobs.ClaimNext(); err != nil {
		t.Fatal(err)
	}
	return &applyFixture{
		db: db, jobs: jobs, updates: updates, services: store.NewServices(db),
		snapshots: store.NewSnapshots(db), events: store.NewEvents(db),
		pid: pid, sid: sid, updateID: uid, jobID: jid,
	}
}

func (f *applyFixture) applier(runner compose.Runner, resolver Resolver, inspector Inspector) *Applier {
	return f.applierWithSettings(runner, resolver, inspector, f.settings())
}

// settings returns a *store.Settings backed by the fixture's db. The sealer is
// nil: GetBoolDefault never touches it (only Set/GetSecret would).
func (f *applyFixture) settings() *store.Settings {
	return store.NewSettings(f.db, nil)
}

func (f *applyFixture) applierWithSettings(runner compose.Runner, resolver Resolver, inspector Inspector, settings *store.Settings) *Applier {
	return NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, settings,
		runner, resolver, inspector, fakeRediscoverer{}, fakeComposer{hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)
}

// composeFile returns the project's single config file path.
func (f *applyFixture) composeFile(t *testing.T) string {
	t.Helper()
	proj, err := store.NewProjects(f.db).Get(f.pid)
	if err != nil {
		t.Fatal(err)
	}
	if len(proj.ConfigFiles) == 0 {
		t.Fatal("project has no config files")
	}
	return proj.ConfigFiles[0]
}

func (f *applyFixture) job(t *testing.T) store.Job {
	t.Helper()
	j, err := f.jobs.Get(f.jobID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func matchingRemote() registry.RemoteImage {
	return registry.RemoteImage{Digest: targetDigest, PlatformDigest: targetDigest}
}
func healthyInspector() fakeInspector {
	return fakeInspector{status: docker.ContainerStatus{State: "running", Health: "healthy"}}
}

func (f *applyFixture) snapshotCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM state_snapshots WHERE service_id=?`, f.sid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *applyFixture) updateStatus(t *testing.T) string {
	t.Helper()
	var s string
	if err := f.db.QueryRow(`SELECT status FROM updates WHERE id=?`, f.updateID).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// --- tests -------------------------------------------------------------------

func TestApplySuccess(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("runner verbs = %v, want [pull up]", got)
	}
	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if f.updateStatus(t) != "applied" {
		t.Fatalf("update status = %q, want applied", f.updateStatus(t))
	}
	if f.snapshotCount(t) != 1 {
		t.Fatalf("snapshots = %d, want 1", f.snapshotCount(t))
	}
	// up must carry --no-deps <svc> for service scope.
	up := runner.calls[1]
	if strings.Join(up.Args, " ") != "-d --no-deps app" {
		t.Fatalf("up args = %v, want [-d --no-deps app]", up.Args)
	}
}

func TestApplyPrecheckDigestMovedAbortsBeforeSnapshot(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	moved := registry.RemoteImage{Digest: "sha256:moved", PlatformDigest: "sha256:moved"}
	a := f.applier(runner, fakeResolver{img: moved}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked %d times, want 0 (no mutation on superseded)", len(runner.calls))
	}
	if f.snapshotCount(t) != 0 {
		t.Fatalf("snapshots = %d, want 0 (no snapshot before precheck passes)", f.snapshotCount(t))
	}
	if f.updateStatus(t) != "superseded" {
		t.Fatalf("update status = %q, want superseded", f.updateStatus(t))
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
}

func TestApplyPrecheckParseFailAbortsBeforeSnapshot(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(), fakeRediscoverer{},
		fakeComposer{parseErr: errors.New("bad yaml")}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"}, func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)
	a.Handle(context.Background(), f.job(t))

	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked, want 0 (unparseable compose)")
	}
	if f.snapshotCount(t) != 0 {
		t.Fatalf("snapshots = %d, want 0", f.snapshotCount(t))
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
}

func TestApplyPullFailLeavesStackUntouched(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1, exit: map[string]int{"pull": 1}}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull" {
		t.Fatalf("runner verbs = %v, want [pull] only (no up after pull failure)", got)
	}
	if f.snapshotCount(t) != 1 {
		t.Fatalf("snapshots = %d, want 1 (snapshot taken before pull)", f.snapshotCount(t))
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
	if f.updateStatus(t) != "failed" {
		t.Fatalf("update status = %q, want failed", f.updateStatus(t))
	}
}

func TestApplyHealthFailMarksFailedWithSnapshotForRollback(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	unhealthy := fakeInspector{status: docker.ContainerStatus{State: "running", Health: "unhealthy"}}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, unhealthy)

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("runner verbs = %v, want [pull up]", got)
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
	if f.updateStatus(t) != "failed" {
		t.Fatalf("update status = %q, want failed", f.updateStatus(t))
	}
	if f.snapshotCount(t) != 1 {
		t.Fatalf("snapshots = %d, want 1 (rollback source present)", f.snapshotCount(t))
	}
}

func TestApplyProjectScopeUpHasNoNoDeps(t *testing.T) {
	f := newApplyFixture(t, "project")
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	up := runner.calls[1]
	if strings.Join(up.Args, " ") != "-d" {
		t.Fatalf("project-scope up args = %v, want [-d] (no --no-deps)", up.Args)
	}
}

// TestApplyForceRecreatesNamespaceSharingDependent covers the gluetun/qbit
// case: qbit's network_mode: service:app means compose's own config-hash
// diff never recreates it when app (the target) is recreated. Its own
// declared config never changes, only the container id network_mode
// resolves to. The apply must force-recreate it explicitly and refresh its
// stored container ids.
func TestApplyForceRecreatesNamespaceSharingDependent(t *testing.T) {
	f := newApplyFixture(t, "service")
	depID, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "sidecar", ImageRef: "ghcr.io/acme/sidecar:1.0",
		CurrentDigest: "sha256:sidecar-old", ContainerIDs: []string{"sc-old"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	proj := compose.Project{Services: []compose.Service{
		{Name: "app"},
		{Name: "sidecar", NetworkMode: "service:app"},
	}}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{byService: map[string]rediscoverResult{
			"app":     {ids: []string{"c-new"}, digest: targetDigest},
			"sidecar": {ids: []string{"sc-new"}, digest: "sha256:sidecar-old"},
		}},
		fakeComposer{proj: proj, hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up,up" {
		t.Fatalf("runner verbs = %v, want [pull up up] (primary up + forced sidecar recreate)", got)
	}
	fr := runner.calls[2]
	if strings.Join(fr.Args, " ") != "-d --no-deps --force-recreate sidecar" {
		t.Fatalf("force-recreate args = %v, want [-d --no-deps --force-recreate sidecar]", fr.Args)
	}
	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	dep, err := f.services.Get(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dep.ContainerIDs) != 1 || dep.ContainerIDs[0] != "sc-new" {
		t.Fatalf("sidecar container ids = %v, want [sc-new] (must refresh after forced recreate)", dep.ContainerIDs)
	}
}

// TestApplyNamespaceDependentForceRecreateFailureWarnsAndContinues: the
// primary target update already succeeded by the time the forced dependent
// recreate runs. A failure there must surface as a warning, not flip the
// whole apply to failed (the target's own update is real and should stick).
func TestApplyNamespaceDependentForceRecreateFailureWarnsAndContinues(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1, forceRecreateErr: errors.New("boom")}
	proj := compose.Project{Services: []compose.Service{
		{Name: "app"}, {Name: "sidecar", NetworkMode: "service:app"},
	}}
	var emitted []string
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(), fakeRediscoverer{},
		fakeComposer{proj: proj, hash: "composehash"}, recordingEmitter{lines: &emitted},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success (dependent recreate failure must not fail the primary apply)", f.job(t).Status)
	}
	joined := strings.Join(emitted, "|")
	if !strings.Contains(joined, "sidecar") || !strings.Contains(joined, "warning") {
		t.Fatalf("expected a warning mentioning sidecar, got: %q", joined)
	}
}

// TestApplyProjectScopeClosesSiblingUpdates covers the core fix: a
// project-scope `up` recreates every service in the stack, not just the
// target. The success tail must re-discover each sibling, refresh its
// runtime, and close its open update too, otherwise the dashboard keeps
// showing "Update available" with a stale digest for services this very
// `up` already moved.
func TestApplyProjectScopeClosesSiblingUpdates(t *testing.T) {
	f := newApplyFixture(t, "project")

	sidB, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "worker", ImageRef: "ghcr.io/acme/worker:1.0.0",
		CurrentDigest: "sha256:bold", CurrentImageID: "sha256:bimg-old",
		ContainerIDs: []string{"b1"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: sidB, FromDigest: "sha256:bold", ToDigest: "sha256:bnew",
		Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{byService: map[string]rediscoverResult{
			"app":    {ids: []string{"c1"}, digest: targetDigest},
			"worker": {ids: []string{"b1new"}, digest: "sha256:bnew"},
		}},
		fakeComposer{hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if f.updateStatus(t) != "applied" {
		t.Fatalf("target update status = %q, want applied", f.updateStatus(t))
	}

	// service B's update is closed too:
	if _, err := f.updates.GetLatestOpenByService(sidB); !errors.Is(err, store.ErrNoOpenUpdate) {
		t.Errorf("sibling update must be closed after project-scope apply, got %v", err)
	}
	// and B's runtime was refreshed from re-discovery (fake locator returns sha256:bnew):
	b, err := f.services.Get(sidB)
	if err != nil {
		t.Fatal(err)
	}
	if b.CurrentDigest != "sha256:bnew" {
		t.Errorf("sibling runtime not refreshed: %s", b.CurrentDigest)
	}
	if strings.Join(b.ContainerIDs, ",") != "b1new" {
		t.Errorf("sibling container ids not refreshed: %v", b.ContainerIDs)
	}
}

// TestApplyProjectScopeSiblingRegistryMovedKeepsUpdateOpen guards the safety
// guard called out in the fix: if a sibling's re-discovered digest doesn't
// match its open update's ToDigest (the registry moved again mid-apply, or
// the sibling was never the target of any pending update to begin with),
// the runtime is still refreshed but the open update is NOT closed: this
// `up` didn't necessarily deliver what that update promised.
func TestApplyProjectScopeSiblingRegistryMovedKeepsUpdateOpen(t *testing.T) {
	f := newApplyFixture(t, "project")

	sidB, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "worker", ImageRef: "ghcr.io/acme/worker:1.0.0",
		CurrentDigest: "sha256:bold", CurrentImageID: "sha256:bimg-old",
		ContainerIDs: []string{"b1"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: sidB, FromDigest: "sha256:bold", ToDigest: "sha256:bnew",
		Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{byService: map[string]rediscoverResult{
			"app": {ids: []string{"c1"}, digest: targetDigest},
			// registry moved again mid-apply: this does NOT match updB.ToDigest
			"worker": {ids: []string{"b1new"}, digest: "sha256:bnewer"},
		}},
		fakeComposer{hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	// runtime is still refreshed to whatever is actually running...
	b, err := f.services.Get(sidB)
	if err != nil {
		t.Fatal(err)
	}
	if b.CurrentDigest != "sha256:bnewer" {
		t.Errorf("sibling runtime not refreshed: %s", b.CurrentDigest)
	}
	// ...but the open update, which promised sha256:bnew (not delivered), stays open.
	sup, err := f.updates.GetLatestOpenByService(sidB)
	if err != nil {
		t.Fatalf("sibling update must stay open when re-discovered digest doesn't match ToDigest, got err %v", err)
	}
	if sup.ToDigest != "sha256:bnew" {
		t.Fatalf("unexpected open update: %+v", sup)
	}
}

// TestApplyProjectScopeSiblingRediscoveryErrorWarnsAndContinues covers the
// fix: a genuine LocateService error for a sibling (e.g. a Docker API
// failure) must not be lumped in with the benign "not running" case. It
// must emit a warning and continue, never fail the already-successful
// apply, and never touch the sibling's runtime/update state since nothing
// was actually re-discovered.
func TestApplyProjectScopeSiblingRediscoveryErrorWarnsAndContinues(t *testing.T) {
	f := newApplyFixture(t, "project")

	sidB, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "worker", ImageRef: "ghcr.io/acme/worker:1.0.0",
		CurrentDigest: "sha256:bold", CurrentImageID: "sha256:bimg-old",
		ContainerIDs: []string{"b1"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: sidB, FromDigest: "sha256:bold", ToDigest: "sha256:bnew",
		Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	var emitted []string
	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{byService: map[string]rediscoverResult{
			"app": {ids: []string{"c1"}, digest: targetDigest},
			// sibling's LocateService fails outright (e.g. Docker API error),
			// distinct from the benign "not running" (len==0, no error) case.
			"worker": {err: errors.New("docker api: connection refused")},
		}},
		fakeComposer{hash: "composehash"}, recordingEmitter{lines: &emitted},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	// The apply as a whole still succeeds: sibling refresh is warn-and-continue.
	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	// The target service's own update was still applied.
	if f.updateStatus(t) != "applied" {
		t.Fatalf("target update status = %q, want applied", f.updateStatus(t))
	}

	// The error must be surfaced as a warning, not silently swallowed.
	found := false
	for _, l := range emitted {
		if strings.Contains(l, "warning: sibling re-discovery failed") && strings.Contains(l, "connection refused") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a sibling re-discovery warning to be emitted, got lines: %v", emitted)
	}

	// Nothing was actually re-discovered for the sibling, so its runtime and
	// open update must be left untouched.
	b, err := f.services.Get(sidB)
	if err != nil {
		t.Fatal(err)
	}
	if b.CurrentDigest != "sha256:bold" {
		t.Errorf("sibling runtime must be untouched on rediscovery error: %s", b.CurrentDigest)
	}
	if _, err := f.updates.GetLatestOpenByService(sidB); err != nil {
		t.Errorf("sibling update must stay open on rediscovery error, got %v", err)
	}
}

func TestRollbackRepinsPrevDigest(t *testing.T) {
	f := newApplyFixture(t, "service")
	// A prior apply left a snapshot with the previous digest.
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:prev",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash",
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("rollback verbs = %v, want [pull up]", got)
	}
	if len(runner.overrideText) == 0 {
		t.Fatal("rollback did not pass an override file")
	}
	joined := strings.Join(runner.overrideText, "\n")
	if !strings.Contains(joined, "ghcr.io/acme/web@sha256:prev") {
		t.Fatalf("override does not pin prev digest: %q", joined)
	}
	if got, _ := f.jobs.Get(rbID); got.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", got.Status)
	}
	// The override file must be removed after the job.
	up := runner.calls[len(runner.calls)-1]
	overridePath := up.ConfigFiles[len(up.ConfigFiles)-1]
	if _, err := os.Stat(overridePath); !os.IsNotExist(err) {
		t.Fatalf("override file not cleaned up: %v", err)
	}
}

// TestRollbackForceRecreatesNamespaceSharingDependent mirrors the apply-side
// fix: rolling app back also recreates it under a new container id, so
// sidecar (network_mode: service:app) needs the same forced recreate +
// runtime refresh, even though rollback itself has no health gate.
func TestRollbackForceRecreatesNamespaceSharingDependent(t *testing.T) {
	f := newApplyFixture(t, "service")
	depID, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "sidecar", ImageRef: "ghcr.io/acme/sidecar:1.0",
		CurrentDigest: "sha256:sidecar-cur", ContainerIDs: []string{"sc-old"}, State: "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:prev",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash",
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	proj := compose.Project{Services: []compose.Service{
		{Name: "app"}, {Name: "sidecar", NetworkMode: "service:app"},
	}}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{byService: map[string]rediscoverResult{
			"sidecar": {ids: []string{"sc-new"}, digest: "sha256:sidecar-cur"},
		}},
		fakeComposer{proj: proj, hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"},
		func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up,up" {
		t.Fatalf("rollback verbs = %v, want [pull up up] (rollback up + forced sidecar recreate)", got)
	}
	fr := runner.calls[2]
	if strings.Join(fr.Args, " ") != "-d --no-deps --force-recreate sidecar" {
		t.Fatalf("force-recreate args = %v, want [-d --no-deps --force-recreate sidecar]", fr.Args)
	}
	if got, _ := f.jobs.Get(rbID); got.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", got.Status)
	}
	dep, err := f.services.Get(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dep.ContainerIDs) != 1 || dep.ContainerIDs[0] != "sc-new" {
		t.Fatalf("sidecar container ids = %v, want [sc-new]", dep.ContainerIDs)
	}
}

// TestRollbackMarksRevertedUpdateRolledBack covers the rollback-respect
// invariant restored by Task 2: a successful rollback must EXPLICITLY flip
// the applied update it reverted to "rolled_back" (not leave it "applied").
// RecordDrift (Task 3) preserves "rolled_back" on re-detection, so a
// recreated stack's re-surfaced update never gets silently re-applied by
// auto-update.
func TestRollbackMarksRevertedUpdateRolledBack(t *testing.T) {
	f := newApplyFixture(t, "service")
	// Simulate a prior successful apply: the service is now running at the
	// update's target digest, and the update row is "applied".
	if err := f.services.UpdateRuntime(f.sid, []string{"c1"}, targetDigest); err != nil {
		t.Fatal(err)
	}
	if err := f.updates.SetStatus(f.updateID, "applied"); err != nil {
		t.Fatal(err)
	}
	// The apply's snapshot recorded the pre-apply digest to roll back to.
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:old",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash",
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	if got, _ := f.jobs.Get(rbID); got.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", got.Status)
	}
	if f.updateStatus(t) != "rolled_back" {
		t.Fatalf("update status = %q, want rolled_back (the applied update at the service's current digest)", f.updateStatus(t))
	}
}

// TestRollbackRestoresComposeFileFromBlob covers a snapshot taken from a
// file-editing apply (Task 5): the snapshot's ComposeBlob carries the
// pre-edit file verbatim. Rollback must restore that exact file content and
// then plain pull+up on the project's own config files. NO temp pin
// override, since the restored file already names the previous image.
func TestRollbackRestoresComposeFileFromBlob(t *testing.T) {
	f := newApplyFixture(t, "service")
	composePath := f.composeFile(t)
	preEditContent := "services:\n  app:\n    image: ghcr.io/acme/web:1.2.3\n"
	// Simulate the apply's rewritten (post-edit) file currently on disk.
	if err := os.WriteFile(composePath, []byte("services:\n  app:\n    image: ghcr.io/acme/web:1.3.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blobBytes, err := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{composePath, preEditContent})
	if err != nil {
		t.Fatal(err)
	}
	blob := string(blobBytes)
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:prev",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash", ComposeBlob: &blob,
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("rollback verbs = %v, want [pull up]", got)
	}
	if len(runner.overrideText) != 0 {
		t.Fatalf("rollback passed an override file, want none (blob-restore path restores the file itself): %v", runner.overrideText)
	}
	for _, c := range runner.calls {
		if len(c.ConfigFiles) != runner.baseFiles {
			t.Fatalf("rollback config files = %v, want exactly the project's own files (no override appended)", c.ConfigFiles)
		}
	}
	got, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != preEditContent {
		t.Fatalf("compose file content = %q, want %q (restored from snapshot blob)", got, preEditContent)
	}
	if rbGot, _ := f.jobs.Get(rbID); rbGot.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", rbGot.Status)
	}
}

// TestRollbackEmptyPathBlobFallsBackToOverride covers the edge case where a
// snapshot's ComposeBlob decodes successfully but carries an empty path: the
// blob must fall through to the override/digest-repin path (never restore a
// file, never crash). This test mirrors TestRollbackRestoresComposeFileFromBlob
// but uses a blob with empty path to verify the fallback.
func TestRollbackEmptyPathBlobFallsBackToOverride(t *testing.T) {
	f := newApplyFixture(t, "service")
	composePath := f.composeFile(t)
	preEditContent := "services:\n  app:\n    image: ghcr.io/acme/web:1.2.3\n"
	// Write a "post-edit" file to disk to verify it is NOT modified.
	postEditContent := "services:\n  app:\n    image: ghcr.io/acme/web:1.3.0\n"
	if err := os.WriteFile(composePath, []byte(postEditContent), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a blob with empty path: should decode fine but not restore.
	blobBytes, err := json.Marshal(struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}{"", preEditContent})
	if err != nil {
		t.Fatal(err)
	}
	blob := string(blobBytes)
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:prev",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash", ComposeBlob: &blob,
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	// Rollback must fall back to the override path (same as TestRollbackRepinsPrevDigest).
	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("rollback verbs = %v, want [pull up]", got)
	}
	if len(runner.overrideText) == 0 {
		t.Fatal("empty-path blob must fall back to override path, got no override file")
	}
	joined := strings.Join(runner.overrideText, "\n")
	if !strings.Contains(joined, "ghcr.io/acme/web@sha256:prev") {
		t.Fatalf("override does not pin prev digest: %q", joined)
	}
	if got, _ := f.jobs.Get(rbID); got.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", got.Status)
	}
	// Compose file must NOT be modified (it should remain with post-edit content).
	fileOnDisk, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(fileOnDisk) != postEditContent {
		t.Fatalf("compose file was modified by empty-path blob fallback, want untouched: got %q, want %q", fileOnDisk, postEditContent)
	}
	// The override file must be removed after the job.
	up := runner.calls[len(runner.calls)-1]
	overridePath := up.ConfigFiles[len(up.ConfigFiles)-1]
	if _, err := os.Stat(overridePath); !os.IsNotExist(err) {
		t.Fatalf("override file not cleaned up: %v", err)
	}
}

func TestRollbackNoSnapshotFails(t *testing.T) {
	f := newApplyFixture(t, "service")
	rbID, _ := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	_, _, _ = f.jobs.ClaimNext()
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	rb, _ := f.jobs.Get(rbID)
	a.Handle(context.Background(), rb)

	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked %d times, want 0 (no snapshot to roll back to)", len(runner.calls))
	}
	if got, _ := f.jobs.Get(rbID); got.Status != "failed" {
		t.Fatalf("rollback job status = %q, want failed", got.Status)
	}
}

// TestApplyResolveErrorAbortsBeforeMutation covers the precheck re-resolve
// ERROR path (distinct from a moved-digest precheck failure): the registry
// call itself fails. This must abort before any compose command runs and
// before a snapshot is taken, and because a resolve error is not evidence
// the digest moved, the update must NOT be marked "superseded".
func TestApplyResolveErrorAbortsBeforeMutation(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{err: errors.New("registry down")}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked %d times, want 0 (no mutation with an unverified target)", len(runner.calls))
	}
	if f.snapshotCount(t) != 0 {
		t.Fatalf("snapshots = %d, want 0 (no snapshot before precheck passes)", f.snapshotCount(t))
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
	if f.updateStatus(t) != "available" {
		t.Fatalf("update status = %q, want available (a resolve error must not be treated as a moved digest)", f.updateStatus(t))
	}
}

// TestApplySnapshotFailureAbortsBeforePull covers the snapshot step failing
// after a successful precheck. No compose command may run without a
// successful pre-mutation snapshot.
func TestApplySnapshotFailureAbortsBeforePull(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(), fakeRediscoverer{},
		fakeComposer{hashErr: errors.New("hash boom")}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"}, func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if len(runner.calls) != 0 {
		t.Fatalf("runner invoked %d times, want 0 (no mutation without a snapshot)", len(runner.calls))
	}
	if f.snapshotCount(t) != 0 {
		t.Fatalf("snapshots = %d, want 0 (snapshot insert failed)", f.snapshotCount(t))
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
}

// TestApplyUpFailMarksFailedWithSnapshotForRollback covers pull succeeding
// but up failing: both compose commands must have run, in order, and the
// snapshot taken before pull must still be present so a rollback is possible.
func TestApplyUpFailMarksFailedWithSnapshotForRollback(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1, exit: map[string]int{"up": 1}}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("runner verbs = %v, want [pull up]", got)
	}
	if f.job(t).Status != "failed" {
		t.Fatalf("job status = %q, want failed", f.job(t).Status)
	}
	if f.updateStatus(t) != "failed" {
		t.Fatalf("update status = %q, want failed", f.updateStatus(t))
	}
	if f.snapshotCount(t) != 1 {
		t.Fatalf("snapshots = %d, want 1 (rollback source present)", f.snapshotCount(t))
	}
}

// TestApplyRefreshesServiceRuntimeFromRediscovery covers the core fix: after a
// successful up, the health gate polls the RE-DISCOVERED container ids (compose
// up recreates them with new ids) and, on success, the service row's
// current_digest + container_ids are refreshed to the running values so the next
// detection cycle compares against what is actually running.
func TestApplyRefreshesServiceRuntimeFromRediscovery(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{ids: []string{"c1"}, digest: "sha256:new"},
		fakeComposer{hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"}, func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	svc, err := f.services.Get(f.sid)
	if err != nil {
		t.Fatal(err)
	}
	if svc.CurrentDigest != "sha256:new" {
		t.Fatalf("service current_digest = %q, want sha256:new (refreshed from re-discovery)", svc.CurrentDigest)
	}
	if strings.Join(svc.ContainerIDs, ",") != "c1" {
		t.Fatalf("service container_ids = %v, want [c1]", svc.ContainerIDs)
	}
}

// TestRollbackRefreshesServiceRuntimeFromRediscovery mirrors the apply refresh
// test on the rollback path: after a SUCCESSFUL rollback up (which recreates the
// containers with new ids running the OLD, rolled-back image), the service row's
// current_digest + container_ids must be refreshed to the running values,
// otherwise they keep the rolled-FORWARD digest and stale ids from the apply.
func TestRollbackRefreshesServiceRuntimeFromRediscovery(t *testing.T) {
	f := newApplyFixture(t, "service")
	// A prior apply left a snapshot with the previous digest.
	if _, err := f.snapshots.Insert(store.Snapshot{
		ServiceID: f.sid, PrevRepo: "ghcr.io/acme/web", PrevDigest: "sha256:prev",
		PrevImageID: "sha256:img-old", ComposeFileHash: "composehash",
	}); err != nil {
		t.Fatal(err)
	}
	rbID, err := f.jobs.Enqueue(store.Job{Type: "rollback", ProjectID: &f.pid, ServiceID: &f.sid, Scope: "service"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.jobs.ClaimNext(); err != nil { // claim the rollback job
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := NewApplier(
		f.jobs, f.updates, f.services, store.NewProjects(f.db), f.snapshots, f.events, f.settings(),
		runner, fakeResolver{img: matchingRemote()}, healthyInspector(),
		fakeRediscoverer{ids: []string{"rb1"}, digest: "sha256:prev"},
		fakeComposer{hash: "composehash"}, nopEmitter{},
		registry.Platform{OS: "linux", Arch: "amd64"}, func() time.Duration { return 200 * time.Millisecond }, func() time.Duration { return 5 * time.Millisecond },
	)

	rb, err := f.jobs.Get(rbID)
	if err != nil {
		t.Fatal(err)
	}
	a.Handle(context.Background(), rb)

	if got, _ := f.jobs.Get(rbID); got.Status != "success" {
		t.Fatalf("rollback job status = %q, want success", got.Status)
	}
	svc, err := f.services.Get(f.sid)
	if err != nil {
		t.Fatal(err)
	}
	if svc.CurrentDigest != "sha256:prev" {
		t.Fatalf("service current_digest = %q, want sha256:prev (refreshed to rolled-back digest)", svc.CurrentDigest)
	}
	if strings.Join(svc.ContainerIDs, ",") != "rb1" {
		t.Fatalf("service container_ids = %v, want [rb1] (re-discovered after rollback up)", svc.ContainerIDs)
	}
}

// TestApplyCrossTagWritesPinOverride covers a semver-scan-suggested update
// whose tag (e.g. "1.4.0") differs from the tracked tag in the compose file
// (svc.ImageRef is "ghcr.io/acme/web:1.2.3"). The apply must re-resolve the
// UPDATE's own tag for the precheck (not the tracked tag, see runApply) and,
// because compose pull/up alone won't move the running container to a
// different tag, pin it via a temp override appended to both pull and up
// (same mechanism as rollback), leaving the user's compose file untouched.
func TestApplyCrossTagWritesPinOverride(t *testing.T) {
	f := newApplyFixture(t, "service")
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: f.sid, FromDigest: "sha256:old", ToDigest: targetDigest,
		Tag: "1.4.0", Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if got := runner.verbs(); strings.Join(got, ",") != "pull,up" {
		t.Fatalf("runner verbs = %v, want [pull up]", got)
	}
	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if len(runner.overrideText) == 0 {
		t.Fatal("cross-tag apply did not write a pin override")
	}
	joined := strings.Join(runner.overrideText, "\n")
	if !strings.Contains(joined, "ghcr.io/acme/web:1.4.0@"+targetDigest) {
		t.Fatalf("override does not pin repo:tag@digest: %q", joined)
	}
	pull, up := runner.calls[0], runner.calls[1]
	if len(pull.ConfigFiles) != 2 || len(up.ConfigFiles) != 2 {
		t.Fatalf("pull/up config files = %v / %v, want override appended to both", pull.ConfigFiles, up.ConfigFiles)
	}
	// The override file must be removed after the job.
	overridePath := up.ConfigFiles[len(up.ConfigFiles)-1]
	if _, err := os.Stat(overridePath); !os.IsNotExist(err) {
		t.Fatalf("override file not cleaned up: %v", err)
	}
}

// TestApplySameTagUnaffectedByCrossTagLogic guards against a regression where
// an update with no recorded Tag (pre-feature rows, or ordinary same-tag
// drift) would be misread as "cross-tag" and get a spurious pin override,
// upd.Tag is "" in the fixture's baseline update, matching that legacy shape.
func TestApplySameTagUnaffectedByCrossTagLogic(t *testing.T) {
	f := newApplyFixture(t, "service")
	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if len(runner.overrideText) != 0 {
		t.Fatalf("same-tag apply must not write a pin override, got %v", runner.overrideText)
	}
	pull := runner.calls[0]
	if len(pull.ConfigFiles) != 1 {
		t.Fatalf("pull config files = %v, want just the base compose file", pull.ConfigFiles)
	}
}

// --- Task 5: apply write-back branching ---------------------------------------

// TestApplyExactPinRewritesComposeFile covers the headline case: the tracked
// tag is a full-semver exact pin (1.2.3), write-back is on (default), and the
// compose file declares the image as a rewritable literal. Apply must edit the
// file in place (exact -> exact, no digest ever appended), run pull/up on the
// unmodified ConfigFiles list (no rollback override), and the snapshot row
// must carry the pre-edit file content so rollback can restore it verbatim.
func TestApplyExactPinRewritesComposeFile(t *testing.T) {
	f := newApplyFixture(t, "service")
	composeFile := f.composeFile(t)
	original := "services:\n  app:\n    image: ghcr.io/acme/web:1.2.3\n"
	if err := os.WriteFile(composeFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: f.sid, FromDigest: "sha256:old", ToDigest: targetDigest,
		Tag: "1.4.0", Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	got, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "ghcr.io/acme/web:1.4.0") {
		t.Fatalf("compose file not rewritten to new tag: %s", got)
	}
	if strings.Contains(string(got), "@sha256:") {
		t.Fatalf("exact-pin rewrite must never append a digest: %s", got)
	}
	if len(runner.overrideText) != 0 {
		t.Fatalf("exact-pin rewrite must not use a runtime override, got %v", runner.overrideText)
	}
	for _, c := range runner.calls {
		if len(c.ConfigFiles) != 1 {
			t.Fatalf("runner ConfigFiles = %v, want just the base compose file (no dockbrr-rollback-*.yml)", c.ConfigFiles)
		}
	}

	snap, err := f.snapshots.GetLatestForService(f.sid)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ComposeBlob == nil {
		t.Fatal("expected snapshot ComposeBlob to be set for a surgical file edit")
	}
	var decoded struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(*snap.ComposeBlob), &decoded); err != nil {
		t.Fatalf("ComposeBlob not valid JSON: %v", err)
	}
	if decoded.Path != composeFile {
		t.Fatalf("blob path = %q, want %q", decoded.Path, composeFile)
	}
	if decoded.Content != original {
		t.Fatalf("blob content = %q, want pre-edit content %q", decoded.Content, original)
	}
}

// TestApplyExactPinInterpolatedFallsBackToOverride covers the tracked tag
// being an exact pin, but the compose file declares the image via a variable
// interpolation (${WEB_IMAGE}) rather than a literal. LocateImageLine marks
// this non-rewritable, so apply must fall back to the existing runtime-pin
// override mechanism and leave the compose file byte-for-byte untouched.
func TestApplyExactPinInterpolatedFallsBackToOverride(t *testing.T) {
	f := newApplyFixture(t, "service")
	composeFile := f.composeFile(t)
	original := "services:\n  app:\n    image: ${WEB_IMAGE}\n"
	if err := os.WriteFile(composeFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: f.sid, FromDigest: "sha256:old", ToDigest: targetDigest,
		Tag: "1.4.0", Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if len(runner.overrideText) == 0 {
		t.Fatal("expected fallback to a runtime-pin override, got none")
	}
	joined := strings.Join(runner.overrideText, "\n")
	if !strings.Contains(joined, "ghcr.io/acme/web:1.4.0@"+targetDigest) {
		t.Fatalf("override does not pin repo:tag@digest: %q", joined)
	}
	up := runner.calls[len(runner.calls)-1]
	if len(up.ConfigFiles) != 2 {
		t.Fatalf("up ConfigFiles = %v, want base file + override", up.ConfigFiles)
	}
	got, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("compose file was modified, want untouched: %s", got)
	}
}

// TestApplyFloatingTrackedTagNeverRewritten covers the safety-critical case:
// the tracked tag is floating (1.31, a partial semver stream) even though a
// cross-tag exact update (1.32.0) is available. The floating tag must NEVER
// be rewritten to a more specific version. No file edit, no runtime
// override, plain pull+up on the unmodified ConfigFiles so the running
// container just re-resolves whatever the floating tag currently points to.
func TestApplyFloatingTrackedTagNeverRewritten(t *testing.T) {
	f := newApplyFixture(t, "service")
	composeFile := f.composeFile(t)
	original := "services:\n  app:\n    image: ghcr.io/acme/web:1.31\n"
	if err := os.WriteFile(composeFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.services.Upsert(store.Service{
		ProjectID: f.pid, Name: "app", ImageRef: "ghcr.io/acme/web:1.31",
		CurrentDigest: "sha256:old", CurrentImageID: "sha256:img-old",
		ContainerIDs: []string{"c1"}, State: "running",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: f.sid, FromDigest: "sha256:old", ToDigest: targetDigest,
		Tag: "1.32.0", Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := f.applier(runner, fakeResolver{img: matchingRemote()}, healthyInspector())

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if len(runner.overrideText) != 0 {
		t.Fatalf("floating tracked tag must not get a runtime override, got %v", runner.overrideText)
	}
	for _, c := range runner.calls {
		if len(c.ConfigFiles) != 1 {
			t.Fatalf("runner ConfigFiles = %v, want just the base compose file", c.ConfigFiles)
		}
	}
	got, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("compose file was modified, want untouched (granularity preserved): %s", got)
	}
}

// TestApplyWriteBackOffFallsBackToOverride covers the write_back_compose
// setting turned off: even though the tracked tag is exact and the image is a
// rewritable literal, apply must NOT touch the compose file and must use the
// existing runtime-pin override instead.
func TestApplyWriteBackOffFallsBackToOverride(t *testing.T) {
	f := newApplyFixture(t, "service")
	composeFile := f.composeFile(t)
	original := "services:\n  app:\n    image: ghcr.io/acme/web:1.2.3\n"
	if err := os.WriteFile(composeFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.updates.Upsert(store.Update{
		ServiceID: f.sid, FromDigest: "sha256:old", ToDigest: targetDigest,
		Tag: "1.4.0", Status: "available", Severity: "minor",
	}); err != nil {
		t.Fatal(err)
	}
	settings := f.settings()
	if err := settings.Set("write_back_compose", "false"); err != nil {
		t.Fatal(err)
	}

	runner := &fakeRunner{baseFiles: 1}
	a := f.applierWithSettings(runner, fakeResolver{img: matchingRemote()}, healthyInspector(), settings)

	a.Handle(context.Background(), f.job(t))

	if f.job(t).Status != "success" {
		t.Fatalf("job status = %q, want success", f.job(t).Status)
	}
	if len(runner.overrideText) == 0 {
		t.Fatal("expected fallback to a runtime-pin override when write_back_compose is off")
	}
	joined := strings.Join(runner.overrideText, "\n")
	if !strings.Contains(joined, "ghcr.io/acme/web:1.4.0@"+targetDigest) {
		t.Fatalf("override does not pin repo:tag@digest: %q", joined)
	}
	got, err := os.ReadFile(composeFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Fatalf("compose file was modified, want untouched when write_back_compose is off: %s", got)
	}
}
