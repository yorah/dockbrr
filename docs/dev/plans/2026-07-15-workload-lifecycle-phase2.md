# Workload Lifecycle Phase 2 (standalone apply/rollback via SDK recreate) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make image apply (and rollback) work for standalone containers by recreating them with the new image via the docker SDK, reusing the existing snapshot schema, so the Apply button that already routes standalone updates stops failing at the compose precheck.

**Architecture:** A new `StandaloneApplier` handles `apply`/`rollback` jobs whose project is standalone. It re-resolves + snapshots (existing schema), pulls the new image, then recreates the container: stop old, rename old aside, create a new container from the captured inspect JSON with the new image, start it, health-gate the new id, and on success remove the old (on failure, restore the old in place). The `Dispatcher` (Phase 1) is extended to branch `apply`/`rollback` on `project.Kind`. Backend-only: the frontend already offers Apply for standalone and posts to `/api/updates/:id/apply`.

**Tech Stack:** Go 1.26 (CGO-free), docker SDK v28.5.2, SQLite.

**Scope note:** Phase 2 of the workload-lifecycle spec (`docs/dev/specs/2026-07-15-workload-lifecycle-design.md`), building on Phase 1 (docker SDK lifecycle methods, `Lifecycle` runner, `Dispatcher`). No frontend changes.

## Global Constraints

- Static binary: `CGO_ENABLED=0 go build ./...` green; no new module deps (docker SDK vendored).
- Invariant 2: only the Job Engine mutates Docker. All recreate SDK calls live in the `StandaloneApplier` / docker wrapper, driven by the engine.
- Invariant 3: apply MUST snapshot before any mutation (standalone included), reusing the existing `store.Snapshot` schema (`PrevRepo`/`PrevDigest`/`PrevImageID`/`PrevContainerInspect`); `ComposeFileHash`/`ComposeBlob` stay empty for standalone.
- Invariant 4: pull-before-create; health-gate the NEW (recreated) container id, never the pre-apply id.
- Invariant 5: per-project mutex (automatic via the Engine).
- Config fidelity: recreate copies `Config` + `HostConfig` + `NetworkSettings.Networks` verbatim except `Config.Image`. Multiple networks: create with the first endpoint, `NetworkConnect` the rest.
- Failure atomicity: a failed apply restores the pre-apply container in place (remove new, rename old back, start old).
- No em-dashes.
- Backend verify: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`

---

### Task 1: docker SDK recreate primitives

**Files:**
- Create: `internal/docker/recreate.go`
- Test: `internal/docker/recreate_test.go`

**Interfaces:**
- Produces (used by Task 2/3):
  - `func (cl *Client) ImagePull(ctx context.Context, ref string) error`
  - `func (cl *Client) ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (id string, err error)`
  - pure helper `func createArgsFromInspect(inspectJSON, newImage string) (*container.Config, *container.HostConfig, *network.NetworkingConfig, map[string]*network.EndpointSettings, error)` (the primary unit under test)

- [ ] **Step 1: Write the failing test**

Create `internal/docker/recreate_test.go`. Build a minimal inspect JSON (the shape `InspectStatus` stores: a marshaled `container.InspectResponse`) and assert the pure helper maps it to create args with the image swapped and networks split (first in `NetworkingConfig`, rest returned as extras).

```go
package docker

import (
	"encoding/json"
	"testing"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

func TestCreateArgsFromInspect(t *testing.T) {
	// Minimal inspect response with two networks and a couple of config fields.
	in := dcontainer.InspectResponse{
		ContainerJSONBase: &dcontainer.ContainerJSONBase{
			Name:       "/adoring_saha",
			HostConfig: &dcontainer.HostConfig{NetworkMode: "bridge"},
		},
		Config: &dcontainer.Config{
			Image: "busybox:1.36",
			Env:   []string{"FOO=bar"},
			Labels: map[string]string{"k": "v"},
		},
		NetworkSettings: &dcontainer.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"net-a": {Aliases: []string{"a"}},
				"net-b": {Aliases: []string{"b"}},
			},
		},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	cfg, host, netCfg, extra, err := createArgsFromInspect(string(raw), "busybox:1.37")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Image != "busybox:1.37" {
		t.Fatalf("Config.Image = %q, want the new image busybox:1.37", cfg.Image)
	}
	if len(cfg.Env) != 1 || cfg.Env[0] != "FOO=bar" || cfg.Labels["k"] != "v" {
		t.Fatalf("config not copied verbatim: %+v", cfg)
	}
	if host == nil || host.NetworkMode != "bridge" {
		t.Fatalf("HostConfig not copied: %+v", host)
	}
	// Exactly one endpoint attached at create; the other returned as an extra.
	if netCfg == nil || len(netCfg.EndpointsConfig) != 1 {
		t.Fatalf("NetworkingConfig should carry exactly one endpoint, got %+v", netCfg)
	}
	if len(extra) != 1 {
		t.Fatalf("expected exactly one extra network to connect after create, got %d", len(extra))
	}
	// The union of the create endpoint + extras must be both original networks.
	seen := map[string]bool{}
	for n := range netCfg.EndpointsConfig {
		seen[n] = true
	}
	for n := range extra {
		seen[n] = true
	}
	if !seen["net-a"] || !seen["net-b"] {
		t.Fatalf("networks lost: %+v", seen)
	}
}

func TestCreateArgsFromInspectRejectsEmpty(t *testing.T) {
	if _, _, _, _, err := createArgsFromInspect("", "img"); err == nil {
		t.Fatal("expected an error for empty inspect JSON")
	}
	if _, _, _, _, err := createArgsFromInspect(`{"Config":null}`, "img"); err == nil {
		t.Fatal("expected an error when Config is missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/docker/ -run TestCreateArgsFromInspect`
Expected: FAIL, `undefined: createArgsFromInspect`.

- [ ] **Step 3: Implement**

Create `internal/docker/recreate.go`:

```go
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	dcontainer "github.com/docker/docker/api/types/container"
	dimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
)

// ImagePull pulls ref and blocks until the pull completes (the SDK returns a
// progress stream that must be drained for the pull to finish). Mutating:
// called only by the Job Engine.
func (cl *Client) ImagePull(ctx context.Context, ref string) error {
	rc, err := cl.c.ImagePull(ctx, ref, dimage.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull %s: %w", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull %s (drain): %w", ref, err)
	}
	return nil
}

// ContainerCreateFromInspect recreates a container from a stored inspect JSON
// (as produced by InspectStatus.RawJSON) with Config.Image swapped to newImage
// and the given name. It creates with the first network endpoint and connects
// any remaining networks afterward. Returns the new container id.
func (cl *Client) ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error) {
	cfg, host, netCfg, extra, err := createArgsFromInspect(inspectJSON, newImage)
	if err != nil {
		return "", err
	}
	resp, err := cl.c.ContainerCreate(ctx, cfg, host, netCfg, nil, name)
	if err != nil {
		return "", fmt.Errorf("docker: create %s: %w", name, err)
	}
	for netName, ep := range extra {
		if err := cl.c.NetworkConnect(ctx, netName, resp.ID, ep); err != nil {
			return "", fmt.Errorf("docker: connect %s to %s: %w", resp.ID, netName, err)
		}
	}
	return resp.ID, nil
}

// createArgsFromInspect parses a container InspectResponse JSON and returns the
// ContainerCreate arguments with Config.Image replaced by newImage. The first
// network endpoint (map iteration order is not stable, so "first" is arbitrary
// but deterministic within one call) goes into the returned NetworkingConfig;
// the remaining endpoints are returned separately to connect after create.
// Pure: no Docker calls, the primary unit under test.
func createArgsFromInspect(inspectJSON, newImage string) (*dcontainer.Config, *dcontainer.HostConfig, *network.NetworkingConfig, map[string]*network.EndpointSettings, error) {
	if strings.TrimSpace(inspectJSON) == "" {
		return nil, nil, nil, nil, errors.New("docker: empty inspect JSON")
	}
	var in dcontainer.InspectResponse
	if err := json.Unmarshal([]byte(inspectJSON), &in); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("docker: parse inspect: %w", err)
	}
	if in.Config == nil {
		return nil, nil, nil, nil, errors.New("docker: inspect has no Config")
	}
	cfg := in.Config
	cfg.Image = newImage

	var host *dcontainer.HostConfig
	if in.ContainerJSONBase != nil {
		host = in.HostConfig
	}

	nets := map[string]*network.EndpointSettings{}
	if in.NetworkSettings != nil {
		for name, ep := range in.NetworkSettings.Networks {
			nets[name] = ep
		}
	}
	netCfg := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{}}
	extra := map[string]*network.EndpointSettings{}
	first := true
	for name, ep := range nets {
		if first {
			netCfg.EndpointsConfig[name] = ep
			first = false
		} else {
			extra[name] = ep
		}
	}
	return cfg, host, netCfg, extra, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/docker/ -run TestCreateArgsFromInspect`
Expected: PASS.

- [ ] **Step 5: Build + commit**

Run: `CGO_ENABLED=0 go build ./... && go vet ./internal/docker/`

```bash
git add internal/docker/recreate.go internal/docker/recreate_test.go
git commit -m "feat(docker): SDK recreate primitives (ImagePull, ContainerCreateFromInspect)"
```

---

### Task 2: StandaloneApplier apply

**Files:**
- Create: `internal/job/standalone.go`
- Test: `internal/job/standalone_test.go`

**Interfaces:**
- Consumes: `store.Jobs/Updates/Services/Projects/Snapshots/Events`, `Resolver` (existing, `Resolve`), a new `Recreator` interface satisfied by `*docker.Client`, `registry.Platform`, `healthTimeout`/`healthPoll` closures.
- Produces: `func NewStandaloneApplier(...) *StandaloneApplier`, `func (a *StandaloneApplier) Handle(ctx, job store.Job)` (dispatches apply/rollback), and the `Recreator` interface.

- [ ] **Step 1: Write the failing test**

Create `internal/job/standalone_test.go`. Use a fake `Recreator` recording the op sequence, and a fake `Resolver` returning a digest matching the update's `ToDigest`. Seed a standalone service with an open update. Assert: pull happens before create; the new id is health-gated; on success the old container is removed and the service runtime updated; the op order is stop -> rename -> create -> start -> remove(old).

```go
package job_test

import (
	"context"
	"testing"

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
```

Add `"time"` to the test imports. Confirm the exact `store.Updates.RecordDrift` / `GetLatestOpenByService` signatures against `internal/store/updates.go` and adjust the seed accordingly (the names are used elsewhere in the job tests).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestStandaloneApply`
Expected: FAIL, `undefined: job.NewStandaloneApplier`.

- [ ] **Step 3: Implement the applier + apply flow**

Create `internal/job/standalone.go`:

```go
package job

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"dockbrr/internal/detect"
	"dockbrr/internal/logger"
	"dockbrr/internal/registry"
	"dockbrr/internal/store"
)

// Recreator is the docker surface the standalone applier needs: the Phase 1
// lifecycle methods plus pull/create-from-inspect and inspect. *docker.Client
// satisfies it. Held only by the Job Engine (invariant 2).
type Recreator interface {
	ImagePull(ctx context.Context, ref string) error
	ContainerStop(ctx context.Context, id string) error
	ContainerStart(ctx context.Context, id string) error
	ContainerRename(ctx context.Context, id, name string) error
	ContainerRemove(ctx context.Context, id string) error
	ContainerCreateFromInspect(ctx context.Context, inspectJSON, newImage, name string) (string, error)
	InspectStatus(ctx context.Context, id string) (ContainerStatus, error)
}

// StandaloneApplier applies (and rolls back) image updates for standalone
// containers by recreating them via the docker SDK. Compose projects use the
// compose Applier; the Dispatcher routes by project kind.
type StandaloneApplier struct {
	jobs      *store.Jobs
	updates   *store.Updates
	services  *store.Services
	projects  *store.Projects
	snapshots *store.Snapshots
	events    *store.Events
	resolver  Resolver
	docker    Recreator
	plat      registry.Platform
	healthTO  func() time.Duration
	healthPoll func() time.Duration
}

func NewStandaloneApplier(
	jobs *store.Jobs, updates *store.Updates, services *store.Services, projects *store.Projects,
	snapshots *store.Snapshots, events *store.Events,
	resolver Resolver, docker Recreator, plat registry.Platform,
	healthTimeout, healthPoll func() time.Duration,
) *StandaloneApplier {
	return &StandaloneApplier{
		jobs: jobs, updates: updates, services: services, projects: projects,
		snapshots: snapshots, events: events, resolver: resolver, docker: docker,
		plat: plat, healthTO: healthTimeout, healthPoll: healthPoll,
	}
}

const oldSuffix = "-dockbrr-old"

// Handle dispatches an apply or rollback job for a standalone service.
func (a *StandaloneApplier) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "apply":
		a.runApply(ctx, job)
	case "rollback":
		a.runRollback(ctx, job)
	default:
		a.fail(job, "standalone: unknown job type: "+job.Type)
	}
}

func (a *StandaloneApplier) runApply(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "apply job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	upd, err := a.updates.GetLatestOpenByService(svc.ID)
	if err != nil {
		a.fail(job, "no open update to apply: "+err.Error())
		return
	}
	if len(svc.ContainerIDs) == 0 {
		a.fail(job, "standalone apply: service has no container")
		return
	}
	oldID := svc.ContainerIDs[0]

	// Precheck: re-resolve the tracked ref and confirm the target digest is
	// still served; else the update was superseded.
	targetRef := svc.ImageRef
	remote, err := a.resolver.Resolve(ctx, targetRef, a.plat)
	if err != nil {
		a.fail(job, "precheck: re-resolve: "+err.Error())
		return
	}
	if remote.Digest != upd.ToDigest && remote.PlatformDigest != upd.ToDigest {
		_ = a.updates.SetStatus(upd.ID, "superseded")
		a.event(svc.ID, "failed", &job.ID, svc.CurrentDigest, upd.ToDigest, "superseded: remote digest moved before apply")
		a.fail(job, "precheck: target digest moved; marked superseded")
		return
	}

	// Snapshot BEFORE any mutation (invariant 3).
	inspect := "{}"
	if st, ierr := a.docker.InspectStatus(ctx, oldID); ierr == nil && st.RawJSON != "" {
		inspect = st.RawJSON
	}
	repo, _ := detect.SplitRef(svc.ImageRef)
	if _, serr := a.snapshots.Insert(store.Snapshot{
		ServiceID: svc.ID, JobID: &job.ID,
		PrevRepo: repo, PrevDigest: svc.CurrentDigest, PrevImageID: svc.CurrentImageID,
		PrevContainerInspect: inspect,
	}); serr != nil {
		a.failApply(job, svc, upd, "snapshot: "+serr.Error())
		return
	}

	// Pull-before-create (invariant 4).
	if err := a.docker.ImagePull(ctx, targetRef); err != nil {
		a.failApply(job, svc, upd, "pull: "+err.Error())
		return
	}

	// Recreate: stop old, rename it aside, create the new from the snapshot
	// inspect with the new image, start it. On any failure, restore old in place.
	newID, rerr := a.recreate(ctx, oldID, inspect, targetRef, svc.Name)
	if rerr != nil {
		a.restoreOld(ctx, oldID, svc.Name, newID)
		a.failApply(job, svc, upd, "recreate: "+rerr.Error())
		return
	}

	// Health-gate the NEW id (invariant 4).
	if err := a.healthGate(ctx, newID); err != nil {
		a.restoreOld(ctx, oldID, svc.Name, newID)
		a.failApply(job, svc, upd, "health gate: "+err.Error())
		return
	}

	// Success: drop the old container, refresh runtime, mark applied.
	if err := a.docker.ContainerRemove(ctx, oldID); err != nil {
		logger.Warnf("standalone apply: remove old %s: %v (continuing)", oldID, err)
	}
	if err := a.services.UpdateRuntime(svc.ID, []string{newID}, upd.ToDigest); err != nil {
		logger.Warnf("standalone apply: runtime refresh: %v", err)
	}
	_ = a.updates.MarkApplied(upd.ID)
	a.event(svc.ID, "succeeded", &job.ID, svc.CurrentDigest, upd.ToDigest, "update applied")
	a.succeed(job)
}

// recreate stops oldID, renames it aside, creates a new container from
// inspectJSON with newImage under name, and starts it. Returns the new id (or
// "" if creation did not happen).
func (a *StandaloneApplier) recreate(ctx context.Context, oldID, inspectJSON, newImage, name string) (string, error) {
	if err := a.docker.ContainerStop(ctx, oldID); err != nil {
		return "", fmt.Errorf("stop old: %w", err)
	}
	if err := a.docker.ContainerRename(ctx, oldID, name+oldSuffix); err != nil {
		return "", fmt.Errorf("rename old: %w", err)
	}
	newID, err := a.docker.ContainerCreateFromInspect(ctx, inspectJSON, newImage, name)
	if err != nil {
		return "", fmt.Errorf("create new: %w", err)
	}
	if err := a.docker.ContainerStart(ctx, newID); err != nil {
		return newID, fmt.Errorf("start new: %w", err)
	}
	return newID, nil
}

// restoreOld undoes a failed recreate: remove the new container (if any), rename
// the old back to its original name, and start it. Best-effort.
func (a *StandaloneApplier) restoreOld(ctx context.Context, oldID, name, newID string) {
	if newID != "" {
		if err := a.docker.ContainerRemove(ctx, newID); err != nil {
			logger.Warnf("standalone restore: remove new %s: %v", newID, err)
		}
	}
	if err := a.docker.ContainerRename(ctx, oldID, name); err != nil {
		logger.Warnf("standalone restore: rename old back: %v", err)
	}
	if err := a.docker.ContainerStart(ctx, oldID); err != nil {
		logger.Warnf("standalone restore: start old: %v", err)
	}
}

// healthGate polls a single recreated container id until running/healthy or the
// timeout elapses. Mirrors the compose Applier's gate (recreated id only).
func (a *StandaloneApplier) healthGate(ctx context.Context, id string) error {
	timeout := a.healthTO()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	poll := a.healthPoll()
	if poll <= 0 {
		poll = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		st, err := a.docker.InspectStatus(ctx, id)
		if err == nil && st.State == "running" && (st.Health == "" || st.Health == "healthy") {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("container %s not healthy within %s", id, timeout)
		case <-time.After(poll):
		}
	}
}
```

Add the terminal-status + event helpers on `*StandaloneApplier` (mirror the `Lifecycle`/`Applier` ones): `succeed(job)`, `fail(job, msg)`, `failApply(job, svc, upd, msg)` (sets update failed + emits a failed event + fails the job), and `event(serviceID, kind, jobID, from, to, msg)`. Check `internal/job/worker.go` for the exact `store.Jobs` finish call and `store.Events.Insert` shape, and reuse them.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/job/ -run TestStandaloneApply`
Expected: PASS.

- [ ] **Step 5: Add a failure-restore test**

Append to `internal/job/standalone_test.go`:

```go
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
```

- [ ] **Step 6: Run tests + commit**

Run: `go test ./internal/job/ -run TestStandaloneApply`
Expected: PASS.

```bash
git add internal/job/standalone.go internal/job/standalone_test.go
git commit -m "feat(job): standalone applier (SDK recreate apply) with in-place restore on failure"
```

---

### Task 3: StandaloneApplier rollback

**Files:**
- Modify: `internal/job/standalone.go` (add `runRollback`)
- Test: `internal/job/standalone_test.go` (append)

**Interfaces:**
- Consumes: `store.Snapshots.GetLatestForService`, the recreate/restore/healthGate helpers from Task 2.

- [ ] **Step 1: Write the failing test**

Append to `internal/job/standalone_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestStandaloneRollback`
Expected: FAIL (rollback not implemented; likely "unknown job type" path or missing behavior).

- [ ] **Step 3: Implement `runRollback`**

Add to `internal/job/standalone.go`:

```go
func (a *StandaloneApplier) runRollback(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		a.fail(job, "rollback job has no service")
		return
	}
	svc, err := a.services.Get(*job.ServiceID)
	if err != nil {
		a.fail(job, "load service: "+err.Error())
		return
	}
	snap, err := a.snapshots.GetLatestForService(svc.ID)
	if err != nil {
		a.fail(job, "no snapshot to roll back to: "+err.Error())
		return
	}
	if snap.PrevDigest == "" || snap.PrevContainerInspect == "" || snap.PrevContainerInspect == "{}" {
		a.fail(job, "snapshot lacks the previous container state")
		return
	}
	if len(svc.ContainerIDs) == 0 {
		a.fail(job, "standalone rollback: service has no current container")
		return
	}
	currentID := svc.ContainerIDs[0]

	// Recreate the snapshot's container with the OLD image. Prefer a
	// digest-pinned ref so rollback is deterministic.
	oldRef := snap.PrevRepo + "@" + snap.PrevDigest

	if err := a.docker.ImagePull(ctx, oldRef); err != nil {
		a.fail(job, "rollback pull: "+err.Error())
		return
	}
	newID, rerr := a.recreate(ctx, currentID, snap.PrevContainerInspect, oldRef, svc.Name)
	if rerr != nil {
		a.restoreOld(ctx, currentID, svc.Name, newID)
		a.fail(job, "rollback recreate: "+rerr.Error())
		return
	}
	if err := a.healthGate(ctx, newID); err != nil {
		a.restoreOld(ctx, currentID, svc.Name, newID)
		a.fail(job, "rollback health gate: "+err.Error())
		return
	}
	if err := a.docker.ContainerRemove(ctx, currentID); err != nil {
		logger.Warnf("standalone rollback: remove current %s: %v", currentID, err)
	}
	if err := a.services.UpdateRuntime(svc.ID, []string{newID}, snap.PrevDigest); err != nil {
		logger.Warnf("standalone rollback: runtime refresh: %v", err)
	}
	// Reopen the update that this rollback reverts, mirroring the compose
	// rollback's event, so the dashboard reflects the reverted state.
	a.event(svc.ID, "rolled_back", &job.ID, svc.CurrentDigest, snap.PrevDigest, "rolled back")
	a.succeed(job)
}
```

Confirm the event kind string used by the compose rollback in `internal/job/worker.go` (e.g. `"rolled_back"`) and match it. If the compose rollback also flips an update row's status, mirror that behavior for parity; otherwise the event is sufficient.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/job/ -run TestStandaloneRollback`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/job/standalone.go internal/job/standalone_test.go
git commit -m "feat(job): standalone rollback (recreate from snapshot inspect with old image)"
```

---

### Task 4: Dispatcher kind-branching + wiring

**Files:**
- Modify: `internal/job/dispatch.go` (branch apply/rollback on project kind)
- Modify: `cmd/dockbrr/main.go` (construct `StandaloneApplier`, pass to the dispatcher)
- Test: `internal/job/dispatch_test.go` (append)

**Interfaces:**
- Consumes: `*Applier`, `*Lifecycle`, `*StandaloneApplier`, `store.Projects`.

- [ ] **Step 1: Write the failing test**

Append to `internal/job/dispatch_test.go`:

```go
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
```

This changes `NewDispatcher`'s signature (adds the standalone applier + projects), so the existing `TestDispatcherRoutesLifecycleAndApply` must be updated to pass `nil, projects` for the new params.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestDispatcher`
Expected: FAIL (signature mismatch / routing not present).

- [ ] **Step 3: Extend the Dispatcher**

Rewrite `internal/job/dispatch.go`:

```go
package job

import (
	"context"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// Dispatcher routes a claimed job to the runner for its kind. Lifecycle kinds go
// to the Lifecycle runner. apply/rollback branch on project kind: standalone
// projects use the SDK-recreate StandaloneApplier, everything else the compose
// Applier.
type Dispatcher struct {
	applier    *Applier
	lifecycle  *Lifecycle
	standalone *StandaloneApplier
	projects   *store.Projects
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle, standalone *StandaloneApplier, projects *store.Projects) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle, standalone: standalone, projects: projects}
}

func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "start", "stop", "restart", "remove":
		d.lifecycle.Handle(ctx, job)
		return
	case "apply", "rollback":
		if d.isStandalone(job) {
			d.standalone.Handle(ctx, job)
			return
		}
	}
	d.applier.Handle(ctx, job)
}

// isStandalone reports whether a job's project is a standalone container. A load
// failure falls back to the compose Applier (its own precheck will surface the
// error), so a transient store error never silently drops the job.
func (d *Dispatcher) isStandalone(job store.Job) bool {
	if job.ProjectID == nil || d.projects == nil {
		return false
	}
	proj, err := d.projects.Get(*job.ProjectID)
	if err != nil {
		logger.Warnf("dispatch: load project %d: %v (routing to compose applier)", *job.ProjectID, err)
		return false
	}
	return proj.Kind == "standalone"
}
```

- [ ] **Step 4: Update the existing dispatcher test**

In `internal/job/dispatch_test.go`, update `TestDispatcherRoutesLifecycleAndApply`'s `job.NewDispatcher(nil, lc)` call to `job.NewDispatcher(nil, lc, nil, store.NewProjects(db))`.

- [ ] **Step 5: Wire main.go**

In `cmd/dockbrr/main.go`, inside `startDockerServices` (where `applier` and `lifecycle` are built), construct the standalone applier and pass all four to the dispatcher. Replace the `engine.SetHandler(job.NewDispatcher(applier, lifecycle))` line:

```go
		standalone := job.NewStandaloneApplier(
			jobs, updates, services, projects, snapshots, events,
			resolver, dc, plat, healthTimeout, healthPoll,
		)
		engine.SetHandler(job.NewDispatcher(applier, lifecycle, standalone, projects))
```

Use the exact variable names in scope in that block: `jobs`, `updates`, `services`, `projects`, `snapshots`, `events`, `resolver`, `dc`, `plat`, `healthTimeout`, `healthPoll` (all are already passed to `NewApplier` a few lines above, so they exist there). If `snapshots` is not a local in that block, construct it via `store.NewSnapshots(db)` as the Applier construction does.

- [ ] **Step 6: Run tests + full backend verify + commit**

Run: `go test ./internal/job/ && CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: all pass.

```bash
git add internal/job/dispatch.go internal/job/dispatch_test.go cmd/dockbrr/main.go
git commit -m "feat(job): dispatcher routes standalone apply/rollback to SDK recreate"
```

---

## Final verification

- [ ] **Backend:** `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` all pass.
- [ ] **Manual smoke (optional):** `mise run build && ./dockbrr` on a host running a standalone container with an available update (e.g. `garethgeorge/backrest:latest`): click Apply; confirm the container is recreated on the new image with the same config (ports, volumes, env), the update clears, and a rollback restores the prior image.

## Notes / gotchas

- SDK types: `image.PullOptions` from `github.com/docker/docker/api/types/image`; `network.NetworkingConfig`/`EndpointSettings` from `github.com/docker/docker/api/types/network`; `container.InspectResponse`/`Config`/`HostConfig` from `github.com/docker/docker/api/types/container`. `ContainerCreate(ctx, config, hostConfig, networkingConfig, *ocispec.Platform, name)` returns `container.CreateResponse{ID}`. All vendored, no `go get`.
- `InspectResponse` embeds `*ContainerJSONBase` (carries `.Name` and `.HostConfig`); guard the nil embed before dereferencing (the pure helper does).
- Config fidelity is the real risk; the pure `createArgsFromInspect` copies `Config`/`HostConfig`/networks verbatim except `Image`. The plan's unit test pins the copy + network split; the manual smoke validates real-container fidelity.
- Rollback uses the digest-pinned old ref (`PrevRepo@PrevDigest`) for determinism; the container comes back on the exact prior image.
- Confirm exact store signatures before coding: `store.Updates.RecordDrift`/`GetLatestOpenByService`/`MarkApplied`/`SetStatus`, `store.Snapshots.Insert`/`GetLatestForService`, `store.Services.UpdateRuntime`, and the `store.Jobs` finish + `store.Events.Insert` shapes used by the existing Applier. Match them.
- The frontend needs no change: Apply for standalone already posts to `/api/updates/:id/apply`; this phase makes that job succeed instead of failing at the compose precheck.
