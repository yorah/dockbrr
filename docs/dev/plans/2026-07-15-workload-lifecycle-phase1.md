# Workload Lifecycle Phase 1 (start/stop/restart, remove, logs) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give dockbrr per-container lifecycle control (start/stop/restart), loose-container removal (stopped-only, bulk), and a read-only logs tail, all mutations routed through the Job Engine via a new docker-SDK channel.

**Architecture:** Add mutation + logs methods to the `docker.Client` SDK wrapper. Add lifecycle job kinds (`start`/`stop`/`restart`/`remove`) handled by a new `Lifecycle` runner that orders operations by shared-namespace dependents. Introduce a `Dispatcher` that routes job kinds to the `Lifecycle` runner or the existing compose `Applier`. Add REST endpoints (lifecycle + remove enqueue jobs; logs is a read-only GET) and dashboard UI (state-aware action menu, logs drawer, loose-group bulk remove).

**Tech Stack:** Go 1.26 (CGO-free), docker SDK `github.com/docker/docker` v28.5.2, chi router, SQLite, React + TypeScript + TanStack, Vitest + MSW.

**Scope note:** This is Phase 1 of the workload-lifecycle spec (`docs/dev/specs/2026-07-15-workload-lifecycle-design.md`). Phase 2 (standalone apply/rollback via SDK recreate) is a separate plan that builds on the SDK methods and Dispatcher produced here. This phase ships working software on its own: lifecycle + remove + logs.

## Global Constraints

- Static binary: `CGO_ENABLED=0 go build ./...` must stay green; no new module deps (docker SDK already vendored).
- Invariant 2: only the Job Engine mutates Docker. Lifecycle SDK mutation methods are called ONLY from the `Lifecycle` runner, never from an API handler. Logs is read-only and may be called from the API handler.
- Invariant 5: lifecycle jobs run under the existing per-project keyed mutex (automatic: the Engine locks by project for every claimed job).
- Invariant 3 refinement: lifecycle ops (start/stop/restart/remove) take NO snapshot (they mutate no image). Document this in code comments.
- No em-dashes in code, comments, or docs.
- Remove guard is enforced on the BACKEND: target project `kind == "standalone"` AND every target container stopped. UI gating is not sufficient.
- Namespace ordering: stop = dependents then target; start = target then dependents; restart = orchestrated-stop then orchestrated-start. Direct dependents only (reuse `compose.NamespaceDependents`).
- TS typecheck via `./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc`). `npm run build` is the backstop; if run, restore `internal/httpapi/dist/index.html` with `git checkout -- internal/httpapi/dist/index.html`.
- Backend verify: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
- Frontend verify (from `web/`): `./node_modules/.bin/tsc -b --noEmit && npm test`

---

### Task 1: Docker SDK mutation + logs methods

**Files:**
- Create: `internal/docker/lifecycle.go`
- Test: `internal/docker/lifecycle_test.go`

**Interfaces:**
- Produces (used by Tasks 2, 5, and Phase 2):
  - `func (cl *Client) ContainerStart(ctx context.Context, id string) error`
  - `func (cl *Client) ContainerStop(ctx context.Context, id string) error`
  - `func (cl *Client) ContainerRestart(ctx context.Context, id string) error`
  - `func (cl *Client) ContainerRemove(ctx context.Context, id string) error`
  - `func (cl *Client) ContainerRename(ctx context.Context, id, newName string) error`
  - `func (cl *Client) ContainerLogsTail(ctx context.Context, id string, tail int) (string, error)`

- [ ] **Step 1: Write the failing test**

Create `internal/docker/lifecycle_test.go`. The mutation methods wrap the SDK directly, so the meaningful unit test targets the logs demux helper (the pure part). Split the tail formatting + stream demux into a pure helper `decodeLogStream(r io.Reader) (string, error)` and `tailArg(n int) string`, and test those:

```go
package docker

import (
	"bytes"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
)

func TestTailArg(t *testing.T) {
	if got := tailArg(0); got != "all" {
		t.Fatalf("tailArg(0) = %q, want all", got)
	}
	if got := tailArg(500); got != "500" {
		t.Fatalf("tailArg(500) = %q, want 500", got)
	}
	if got := tailArg(-5); got != "all" {
		t.Fatalf("tailArg(-5) = %q, want all (non-positive clamps to all)", got)
	}
}

func TestDecodeLogStream(t *testing.T) {
	// Build a multiplexed docker log stream (stdout + stderr framed).
	var raw bytes.Buffer
	w := stdcopy.NewStdWriter(&raw, stdcopy.Stdout)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	e := stdcopy.NewStdWriter(&raw, stdcopy.Stderr)
	if _, err := e.Write([]byte("oops\n")); err != nil {
		t.Fatal(err)
	}
	got, err := decodeLogStream(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello\noops\n" {
		t.Fatalf("decodeLogStream = %q, want interleaved stdout+stderr", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/docker/ -run 'TestTailArg|TestDecodeLogStream'`
Expected: FAIL, `undefined: tailArg` / `undefined: decodeLogStream`.

- [ ] **Step 3: Implement the methods**

Create `internal/docker/lifecycle.go`:

```go
package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	dcontainer "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ContainerStart starts a stopped container. Mutating: called only by the Job
// Engine (invariant 2).
func (cl *Client) ContainerStart(ctx context.Context, id string) error {
	if err := cl.c.ContainerStart(ctx, id, dcontainer.StartOptions{}); err != nil {
		return fmt.Errorf("docker: start %s: %w", id, err)
	}
	return nil
}

// ContainerStop stops a running container with the daemon default timeout.
func (cl *Client) ContainerStop(ctx context.Context, id string) error {
	if err := cl.c.ContainerStop(ctx, id, dcontainer.StopOptions{}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", id, err)
	}
	return nil
}

// ContainerRestart restarts a container (stop then start) with the daemon
// default timeout.
func (cl *Client) ContainerRestart(ctx context.Context, id string) error {
	if err := cl.c.ContainerRestart(ctx, id, dcontainer.StopOptions{}); err != nil {
		return fmt.Errorf("docker: restart %s: %w", id, err)
	}
	return nil
}

// ContainerRemove removes a container. The caller guarantees it is stopped
// (no force), so a running container is a caller bug surfaced as an error.
func (cl *Client) ContainerRemove(ctx context.Context, id string) error {
	if err := cl.c.ContainerRemove(ctx, id, dcontainer.RemoveOptions{}); err != nil {
		return fmt.Errorf("docker: remove %s: %w", id, err)
	}
	return nil
}

// ContainerRename renames a container (used by the Phase 2 recreate path to free
// a name, and available to lifecycle callers).
func (cl *Client) ContainerRename(ctx context.Context, id, newName string) error {
	if err := cl.c.ContainerRename(ctx, id, newName); err != nil {
		return fmt.Errorf("docker: rename %s -> %s: %w", id, newName, err)
	}
	return nil
}

// ContainerLogsTail returns the last tail lines of a container's combined
// stdout+stderr as text. Read-only: callable from the API handler. tail <= 0
// returns all lines.
func (cl *Client) ContainerLogsTail(ctx context.Context, id string, tail int) (string, error) {
	rc, err := cl.c.ContainerLogs(ctx, id, dcontainer.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tailArg(tail),
	})
	if err != nil {
		return "", fmt.Errorf("docker: logs %s: %w", id, err)
	}
	defer rc.Close()
	return decodeLogStream(rc)
}

// tailArg maps a line count to the docker Tail option ("all" for non-positive).
func tailArg(n int) string {
	if n <= 0 {
		return "all"
	}
	return strconv.Itoa(n)
}

// decodeLogStream demultiplexes docker's framed stdout/stderr log stream into a
// single text blob. Non-tty containers multiplex both streams with 8-byte
// headers; stdcopy.StdCopy splits them, and we interleave into one buffer.
func decodeLogStream(r io.Reader) (string, error) {
	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, r); err != nil {
		return "", fmt.Errorf("docker: decode log stream: %w", err)
	}
	return buf.String(), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/docker/ -run 'TestTailArg|TestDecodeLogStream'`
Expected: PASS.

- [ ] **Step 5: Build check + commit**

Run: `CGO_ENABLED=0 go build ./... && go vet ./internal/docker/`
Expected: clean.

```bash
git add internal/docker/lifecycle.go internal/docker/lifecycle_test.go
git commit -m "feat(docker): add container lifecycle + logs SDK methods"
```

---

### Task 2: Lifecycle runner (start/stop/restart, namespace-ordered)

**Files:**
- Create: `internal/job/lifecycle.go`
- Test: `internal/job/lifecycle_test.go`

**Interfaces:**
- Consumes: `store.Services`, `store.Projects`, `store.Events`, `compose.NamespaceDependents`, `Composer` (Task existing), `Rediscoverer` (existing), and a new `Mutator` interface satisfied by `*docker.Client` (Task 1 methods).
- Produces (used by Task 3):
  - `type Mutator interface { ContainerStart/Stop/Restart/Remove(ctx, id) error }`
  - `func NewLifecycle(...) *Lifecycle`
  - `func (l *Lifecycle) Handle(ctx context.Context, job store.Job)`

- [ ] **Step 1: Write the failing test**

Create `internal/job/lifecycle_test.go`. Use a fake mutator that records the order of container operations, and seed a compose project where service `web` shares `db`'s namespace so `db` has a namespace dependent `web`.

```go
package job_test

import (
	"context"
	"path/filepath"
	"testing"

	"dockbrr/internal/compose"
	"dockbrr/internal/job"
	"dockbrr/internal/store"
)

type opRec struct{ kind, id string }

type fakeMutator struct{ ops []opRec }

func (f *fakeMutator) ContainerStart(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"start", id})
	return nil
}
func (f *fakeMutator) ContainerStop(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"stop", id})
	return nil
}
func (f *fakeMutator) ContainerRestart(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"restart", id})
	return nil
}
func (f *fakeMutator) ContainerRemove(_ context.Context, id string) error {
	f.ops = append(f.ops, opRec{"remove", id})
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
		store.NewServices(db), store.NewProjects(db), store.NewEvents(db),
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
	if done.Status != "succeeded" {
		t.Fatalf("job status = %q, want succeeded", done.Status)
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
}
```

Note: confirm `store.Jobs` has a `Get(id)` returning `store.Job`; the store package already has it (used by httpapi). If the setter for status is named differently than the Engine uses, use the same completion helpers the Applier uses (see `internal/job/worker.go` `succeed`/`fail`). This runner records terminal status itself.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestLifecycle`
Expected: FAIL, `undefined: job.NewLifecycle` / `job.Mutator`.

- [ ] **Step 3: Implement the Lifecycle runner**

Create `internal/job/lifecycle.go`:

```go
package job

import (
	"context"
	"fmt"

	"dockbrr/internal/compose"
	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// Mutator is the docker mutation surface the lifecycle runner needs.
// *docker.Client satisfies it. Only the Job Engine holds one (invariant 2).
type Mutator interface {
	ContainerStart(ctx context.Context, id string) error
	ContainerStop(ctx context.Context, id string) error
	ContainerRestart(ctx context.Context, id string) error
	ContainerRemove(ctx context.Context, id string) error
}

// Lifecycle handles start/stop/restart/remove jobs. It mutates containers by id
// through the Mutator, ordering start/stop/restart by shared-namespace
// dependents. It takes NO snapshot: lifecycle ops change no image (refinement of
// invariant 3, which binds image apply).
type Lifecycle struct {
	services     *store.Services
	projects     *store.Projects
	events       *store.Events
	mutator      Mutator
	composer     Composer
	rediscoverer Rediscoverer
}

func NewLifecycle(
	services *store.Services,
	projects *store.Projects,
	events *store.Events,
	mutator Mutator,
	composer Composer,
	rediscoverer Rediscoverer,
) *Lifecycle {
	return &Lifecycle{
		services: services, projects: projects, events: events,
		mutator: mutator, composer: composer, rediscoverer: rediscoverer,
	}
}

// Handle dispatches a lifecycle job. It records a terminal job status and never
// panics.
func (l *Lifecycle) Handle(ctx context.Context, job store.Job) {
	if job.ServiceID == nil {
		l.fail(job, "lifecycle job has no service")
		return
	}
	svc, err := l.services.Get(*job.ServiceID)
	if err != nil {
		l.fail(job, "load service: "+err.Error())
		return
	}
	proj, err := l.projects.Get(svc.ProjectID)
	if err != nil {
		l.fail(job, "load project: "+err.Error())
		return
	}

	switch job.Type {
	case "start":
		err = l.runOrdered(ctx, svc, proj, "start")
	case "stop":
		err = l.runOrdered(ctx, svc, proj, "stop")
	case "restart":
		if err = l.runOrdered(ctx, svc, proj, "stop"); err == nil {
			err = l.runOrdered(ctx, svc, proj, "start")
		}
	case "remove":
		err = l.runRemove(ctx, svc, proj)
	default:
		l.fail(job, "unknown lifecycle type: "+job.Type)
		return
	}
	if err != nil {
		l.fail(job, err.Error())
		return
	}
	l.rediscover(ctx, svc)
	l.succeed(job)
	l.event(svc.ID, job.Type, &job.ID)
}

// runOrdered stops or starts the target and its namespace dependents in the
// order dictated by verb: stop = dependents then target; start = target then
// dependents.
func (l *Lifecycle) runOrdered(ctx context.Context, svc store.Service, proj store.Project, verb string) error {
	depIDs := l.dependentContainerIDs(ctx, svc, proj)
	targetIDs := svc.ContainerIDs

	var order []string
	switch verb {
	case "stop":
		order = append(append([]string{}, depIDs...), targetIDs...)
	case "start":
		order = append(append([]string{}, targetIDs...), depIDs...)
	default:
		return fmt.Errorf("runOrdered: bad verb %q", verb)
	}
	for _, id := range order {
		var err error
		switch verb {
		case "stop":
			err = l.mutator.ContainerStop(ctx, id)
		case "start":
			err = l.mutator.ContainerStart(ctx, id)
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", verb, id, err)
		}
	}
	return nil
}

// runRemove removes the target's containers. Guard: the project must be
// standalone AND every target container must be stopped. This is the backend
// enforcement of the loose+stopped rule.
func (l *Lifecycle) runRemove(ctx context.Context, svc store.Service, proj store.Project) error {
	if proj.Kind != "standalone" {
		return fmt.Errorf("remove refused: %s is not a standalone container", svc.Name)
	}
	if !isStoppedState(svc.State) {
		return fmt.Errorf("remove refused: %s is not stopped (state=%s)", svc.Name, svc.State)
	}
	for _, id := range svc.ContainerIDs {
		if err := l.mutator.ContainerRemove(ctx, id); err != nil {
			return fmt.Errorf("remove %s: %w", id, err)
		}
	}
	return nil
}

// dependentContainerIDs returns the container ids of svc's namespace dependents
// (compose services sharing svc's netns/ipc/pid). Empty for loose or unparseable
// projects. Best-effort: a parse failure yields no dependents (target-only).
func (l *Lifecycle) dependentContainerIDs(ctx context.Context, svc store.Service, proj store.Project) []string {
	if proj.Kind != "compose" || len(proj.ConfigFiles) == 0 {
		return nil
	}
	parsed, err := l.composer.Parse(ctx, proj.WorkingDir, proj.ConfigFiles)
	if err != nil {
		logger.Warnf("lifecycle: parse %s: %v (no dependent ordering)", proj.Name, err)
		return nil
	}
	depNames := compose.NamespaceDependents(parsed.Services, svc.Name)
	if len(depNames) == 0 {
		return nil
	}
	byName := map[string]bool{}
	for _, n := range depNames {
		byName[n] = true
	}
	svcs, err := l.services.ListByProject(proj.ID)
	if err != nil {
		logger.Warnf("lifecycle: list services %s: %v (no dependent ordering)", proj.Name, err)
		return nil
	}
	var ids []string
	for _, s := range svcs {
		if byName[s.Name] {
			ids = append(ids, s.ContainerIDs...)
		}
	}
	return ids
}

func (l *Lifecycle) rediscover(ctx context.Context, svc store.Service) {
	if l.rediscoverer == nil {
		return
	}
	if err := l.rediscoverer.Rediscover(ctx, svc.ProjectID); err != nil {
		logger.Warnf("lifecycle: rediscover project %d: %v", svc.ProjectID, err)
	}
}

// isStoppedState reports whether a service state is a non-running state that
// permits removal.
func isStoppedState(state string) bool {
	switch state {
	case "exited", "dead", "created":
		return true
	default:
		return false
	}
}
```

Note on helpers: reuse the Applier's terminal-status + event helpers if they are exported/shared within the package; if `succeed`, `fail`, and `event` are methods on `*Applier` only, add small equivalents on `*Lifecycle` that call the same `store.Jobs`/`store.Events` methods. Check `internal/job/worker.go` for the exact `store.Jobs` status-setter name (e.g. `Complete`/`Fail`/`SetStatus`) and the `store.Events.Insert` shape, and mirror them. Confirm `Rediscoverer` interface method name (`Rediscover`) against `internal/job/worker.go:31`; use the real name.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/job/ -run TestLifecycle`
Expected: PASS.

- [ ] **Step 5: Add the remove-guard tests**

Append to `internal/job/lifecycle_test.go`:

```go
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
	if done.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded", done.Status)
	}
}
```

- [ ] **Step 6: Run all lifecycle tests + commit**

Run: `go test ./internal/job/ -run TestLifecycle`
Expected: PASS.

```bash
git add internal/job/lifecycle.go internal/job/lifecycle_test.go
git commit -m "feat(job): lifecycle runner (start/stop/restart/remove) with namespace ordering"
```

---

### Task 3: Dispatcher + engine wiring

**Files:**
- Create: `internal/job/dispatch.go`
- Modify: `cmd/dockbrr/main.go` (build Lifecycle + Dispatcher, set as engine handler)
- Test: `internal/job/dispatch_test.go`

**Interfaces:**
- Consumes: `*Applier` (existing), `*Lifecycle` (Task 2).
- Produces: `func NewDispatcher(applier *Applier, lifecycle *Lifecycle) *Dispatcher` and `func (d *Dispatcher) Handle(ctx, job store.Job)` implementing the `Handler` interface.

- [ ] **Step 1: Write the failing test**

Create `internal/job/dispatch_test.go`:

```go
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
	d := job.NewDispatcher(nil, lc) // applier nil: this test only drives a lifecycle kind
	jobs := store.NewJobs(db)
	jid, _ := jobs.Enqueue(store.Job{Type: "stop", ServiceID: &dbSvc.ID, ProjectID: &pid, Scope: "service"})
	j, _ := jobs.Get(jid)
	d.Handle(context.Background(), j)
	if len(m.ops) == 0 {
		t.Fatal("stop job did not reach the lifecycle runner")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/job/ -run TestDispatcher`
Expected: FAIL, `undefined: job.NewDispatcher`.

- [ ] **Step 3: Implement the Dispatcher**

Create `internal/job/dispatch.go`:

```go
package job

import (
	"context"

	"dockbrr/internal/store"
)

// Dispatcher routes a claimed job to the runner for its kind. It is the Engine's
// single Handler. Lifecycle kinds go to the Lifecycle runner; apply/rollback go
// to the compose Applier. Phase 2 extends this to branch apply/rollback on
// project kind (standalone vs compose).
type Dispatcher struct {
	applier   *Applier
	lifecycle *Lifecycle
}

func NewDispatcher(applier *Applier, lifecycle *Lifecycle) *Dispatcher {
	return &Dispatcher{applier: applier, lifecycle: lifecycle}
}

// Handle implements Handler. It never panics; the delegated runner records the
// terminal job status.
func (d *Dispatcher) Handle(ctx context.Context, job store.Job) {
	switch job.Type {
	case "start", "stop", "restart", "remove":
		d.lifecycle.Handle(ctx, job)
	default:
		// apply, rollback, and any other compose kinds.
		d.applier.Handle(ctx, job)
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/job/ -run TestDispatcher`
Expected: PASS.

- [ ] **Step 5: Wire it in main.go**

In `cmd/dockbrr/main.go`, after the `applier := job.NewApplier(...)` block (around line 217) and before `engine.SetHandler(applier)`, build the lifecycle runner and dispatcher, and set the dispatcher as the handler. Replace the `engine.SetHandler(applier)` line:

```go
		lifecycle := job.NewLifecycle(services, projects, events, dc, job.RealComposer{}, locator)
		engine.SetHandler(job.NewDispatcher(applier, lifecycle))
```

Use the same variable names already in scope in that block: `services`, `projects`, `events`, `dc` (the `*docker.Client`), and `locator` (the `Rediscoverer`; confirm its variable name matches what `NewApplier` was passed as the rediscoverer argument). If `events` is not already a local there, construct it with `store.NewEvents(db)` as the Applier construction does.

- [ ] **Step 6: Build + full backend test + commit**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: all pass.

```bash
git add internal/job/dispatch.go internal/job/dispatch_test.go cmd/dockbrr/main.go
git commit -m "feat(job): dispatcher routes lifecycle vs apply jobs; wire engine handler"
```

---

### Task 4: Lifecycle + remove API endpoints

**Files:**
- Create: `internal/httpapi/lifecycle.go`
- Modify: `internal/httpapi/server.go` (register 2 routes)
- Test: `internal/httpapi/lifecycle_test.go`

**Interfaces:**
- Consumes: `s.deps.Engine.Enqueue`, `store.Services`, `store.Projects`.
- Produces: `POST /api/services/{id}/lifecycle` `{action}` and `POST /api/services/{id}/remove`, both returning `{"job_id": N}`.

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/lifecycle_test.go`. Mirror the harness in `internal/httpapi/projects_test.go` (`authedServer(t, Deps{})`, `authReq`). Deps must carry an Engine; check how `projects_test.go`/`updates_test.go` inject a fake Engine (search for a `fakeEngine`/`stubEngine` implementing `Enqueue`). Use that same fake.

```go
package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dockbrr/internal/store"
)

func TestLifecycleEndpointEnqueues(t *testing.T) {
	eng := &fakeEngine{} // implements Enqueue, records the last job; reuse the existing test fake
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "web", ImageRef: "nginx:1", State: "running"})

	body := strings.NewReader(`{"action":"restart"}`)
	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/lifecycle", sid), body), tok, csrf)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.JobID == 0 {
		t.Fatalf("body = %s, want a job_id", rec.Body.String())
	}
	if eng.lastJob.Type != "restart" || eng.lastJob.ServiceID == nil || *eng.lastJob.ServiceID != sid {
		t.Fatalf("enqueued job = %+v, want restart for service %d", eng.lastJob, sid)
	}
}

func TestLifecycleEndpointRejectsBadAction(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "compose", Name: "app", WorkingDir: "/srv"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "web", ImageRef: "nginx:1", State: "running"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/lifecycle", sid), strings.NewReader(`{"action":"nuke"}`)), tok, csrf)
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for bad action", rec.Code)
	}
}

func TestRemoveEndpointGuardsLooseStopped(t *testing.T) {
	eng := &fakeEngine{}
	srv, db, tok, csrf := authedServer(t, Deps{Engine: eng})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	// Running standalone: must be rejected (409).
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "running"})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/services/%d/remove", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a running container", rec.Code)
	}
	if eng.lastJob.Type == "remove" {
		t.Fatal("remove job must not be enqueued for a running container")
	}
}
```

Note: `pathf` is the existing test helper in the httpapi test package (used by `projects_test.go`). Confirm the `Deps` struct field for the engine is named `Engine` and its type is the `JobService` interface (server.go:19). Reuse the existing test-package `fakeEngine`; if none exists, add a minimal one implementing `Enqueue(store.Job) (int64, error)` recording `lastJob`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run 'TestLifecycleEndpoint|TestRemoveEndpoint'`
Expected: FAIL (routes 404 / handlers undefined).

- [ ] **Step 3: Implement the handlers**

Create `internal/httpapi/lifecycle.go`:

```go
package httpapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"dockbrr/internal/store"
)

var lifecycleActions = map[string]bool{"start": true, "stop": true, "restart": true}

// handleLifecycle enqueues a start/stop/restart job for a service.
func (s *Server) handleLifecycle(w http.ResponseWriter, r *http.Request) {
	svc, proj, ok := s.loadServiceProject(w, r)
	if !ok {
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &body); err != nil || !lifecycleActions[body.Action] {
		writeJSONError(w, http.StatusBadRequest, errInvalidAction)
		return
	}
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: body.Action, ServiceID: &svc.ID, ProjectID: &proj.ID, Scope: "service", RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"job_id": jobID})
}

// handleRemove enqueues a remove job for a loose, stopped container. The guard
// is enforced here AND re-checked in the runner (the runner is the source of
// truth; this returns a friendly 409 before enqueue).
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	svc, proj, ok := s.loadServiceProject(w, r)
	if !ok {
		return
	}
	if proj.Kind != "standalone" {
		writeJSONError(w, http.StatusConflict, errRemoveNotStandalone)
		return
	}
	if !store.IsStoppedState(svc.State) {
		writeJSONError(w, http.StatusConflict, errRemoveNotStopped)
		return
	}
	jobID, err := s.deps.Engine.Enqueue(store.Job{
		Type: "remove", ServiceID: &svc.ID, ProjectID: &proj.ID, Scope: "service", RequestedBy: "user",
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"job_id": jobID})
}

// loadServiceProject resolves the {id} path param to a service + its project,
// writing a 404 and returning ok=false when either is missing.
func (s *Server) loadServiceProject(w http.ResponseWriter, r *http.Request) (store.Service, store.Project, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return store.Service{}, store.Project{}, false
	}
	svc, err := store.NewServices(s.db).Get(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return store.Service{}, store.Project{}, false
	}
	proj, err := store.NewProjects(s.db).Get(svc.ProjectID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err)
		return store.Service{}, store.Project{}, false
	}
	return svc, proj, true
}
```

Add the error values near the other package errors (e.g. top of `lifecycle.go`):

```go
var (
	errInvalidAction       = errString("action must be start, stop, or restart")
	errRemoveNotStandalone = errString("remove is only allowed for standalone containers")
	errRemoveNotStopped    = errString("remove is only allowed for a stopped container")
)
```

Use the package's existing error-construction idiom instead of `errString` if one exists (check how other handlers build `writeJSONError` messages, e.g. `errors.New`). Confirm `decodeJSON`, `writeJSON`, `writeJSONError` helper names against `internal/httpapi/settings.go` and reuse them verbatim.

Move `isStoppedState` from Task 2 into `store` as exported `store.IsStoppedState(state string) bool` so both the runner and this handler share one definition (update Task 2's runner call site to `store.IsStoppedState`). Add it to `internal/store/services.go`:

```go
// IsStoppedState reports whether a service state permits removal (any
// non-running, non-transitional state).
func IsStoppedState(state string) bool {
	switch state {
	case "exited", "dead", "created":
		return true
	default:
		return false
	}
}
```

- [ ] **Step 4: Register the routes**

In `internal/httpapi/server.go`, in the authenticated route group near `r.Post("/api/updates/{id}/apply", ...)` (line 147), add:

```go
		r.Post("/api/services/{id}/lifecycle", s.handleLifecycle)
		r.Post("/api/services/{id}/remove", s.handleRemove)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -run 'TestLifecycleEndpoint|TestRemoveEndpoint'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpapi/lifecycle.go internal/httpapi/server.go internal/httpapi/lifecycle_test.go internal/store/services.go internal/job/lifecycle.go
git commit -m "feat(api): lifecycle + remove endpoints; share IsStoppedState in store"
```

---

### Task 5: Logs endpoint (read-only tail)

**Files:**
- Modify: `internal/httpapi/lifecycle.go` (add `handleLogs`)
- Modify: `internal/httpapi/server.go` (register route + confirm Deps has a Docker reader)
- Modify: `internal/httpapi/deps.go` (or wherever `Deps` is defined) to add a read-only `DockerLogs` dependency
- Modify: `cmd/dockbrr/main.go` (inject the docker client into Deps)
- Test: `internal/httpapi/lifecycle_test.go` (append)

**Interfaces:**
- Consumes: a read-only logs reader `interface { ContainerLogsTail(ctx, id string, tail int) (string, error) }` satisfied by `*docker.Client` (Task 1).
- Produces: `GET /api/services/{id}/logs?tail=N` returning `{"logs": "..."}`.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpapi/lifecycle_test.go`:

```go
type fakeLogs struct{ out string }

func (f fakeLogs) ContainerLogsTail(_ context.Context, _ string, _ int) (string, error) {
	return f.out, nil
}

func TestLogsEndpointReturnsTail(t *testing.T) {
	srv, db, tok, csrf := authedServer(t, Deps{DockerLogs: fakeLogs{out: "line1\nline2\n"}})
	projects := store.NewProjects(db)
	services := store.NewServices(db)
	pid, _ := projects.Upsert(store.Project{HostID: 1, Kind: "standalone", Name: "adoring_saha"})
	sid, _ := services.Upsert(store.Service{ProjectID: pid, Name: "adoring_saha", ImageRef: "busybox", State: "exited", ContainerIDs: []string{"c1"}})

	rec := httptest.NewRecorder()
	req := authReq(httptest.NewRequest(http.MethodGet, pathf("/api/services/%d/logs?tail=100", sid), nil), tok, csrf)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Logs string `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Logs != "line1\nline2\n" {
		t.Fatalf("logs = %q, want the fake tail", out.Logs)
	}
}
```

Add `"context"` to the test file imports if not present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestLogsEndpoint`
Expected: FAIL (`Deps.DockerLogs` undefined / route 404).

- [ ] **Step 3: Add the Deps field + interface**

In the file defining `Deps` (search for `type Deps struct`), add:

```go
	// DockerLogs is the read-only container-logs reader. Nil disables the logs
	// endpoint (returns 503). Read-only: this is the sole API->docker read path.
	DockerLogs DockerLogsReader
```

and define the interface in the same file:

```go
// DockerLogsReader reads a bounded tail of a container's logs. *docker.Client
// satisfies it. Read-only, so it may be called directly from an API handler.
type DockerLogsReader interface {
	ContainerLogsTail(ctx context.Context, id string, tail int) (string, error)
}
```

- [ ] **Step 4: Implement the handler**

Add to `internal/httpapi/lifecycle.go`:

```go
const (
	defaultLogTail = 500
	maxLogTail     = 2000
)

// handleLogs returns a bounded tail of a service's first container logs.
// Read-only (invariant 2 permits API->docker reads; only mutation is forbidden).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.loadServiceProject(w, r)
	if !ok {
		return
	}
	if s.deps.DockerLogs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, errLogsUnavailable)
		return
	}
	if len(svc.ContainerIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"logs": ""})
		return
	}
	tail := defaultLogTail
	if q := r.URL.Query().Get("tail"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > maxLogTail {
			writeJSONError(w, http.StatusBadRequest, errBadTail)
			return
		}
		tail = n
	}
	out, err := s.deps.DockerLogs.ContainerLogsTail(r.Context(), svc.ContainerIDs[0], tail)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": out})
}
```

Add the error values to the error block from Task 4:

```go
	errLogsUnavailable = errString("logs are unavailable")
	errBadTail         = errString("tail must be between 1 and 2000")
```

- [ ] **Step 5: Register the route + inject the client**

In `internal/httpapi/server.go` add (GET, still inside the authenticated group):

```go
		r.Get("/api/services/{id}/logs", s.handleLogs)
```

In `cmd/dockbrr/main.go`, where the `Deps` struct is populated for the server (search `Deps{`), set `DockerLogs: dc` (the `*docker.Client`).

- [ ] **Step 6: Run tests + full backend verify + commit**

Run: `go test ./internal/httpapi/ -run TestLogsEndpoint && CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`
Expected: all pass.

```bash
git add internal/httpapi/ cmd/dockbrr/main.go
git commit -m "feat(api): read-only container logs tail endpoint"
```

---

### Task 6: Frontend mutations + types

**Files:**
- Modify: `web/src/hooks/mutations.ts` (add `useLifecycle`, `useRemoveContainer`)
- Modify: `web/src/hooks/queries.ts` (add a `useLogs` fetch, or a plain fetch helper)
- Modify: `web/src/api/types.ts` if a shared type is needed
- Test: `web/src/hooks/mutations.test.ts` (append, if the file exists; else fold assertions into the component tests in Tasks 7-9)

**Interfaces:**
- Produces: `useLifecycle()` -> mutate `{serviceId, action}`; `useRemoveContainer()` -> mutate `serviceId`; a `fetchLogs(serviceId, tail)` helper returning `{logs: string}`.

- [ ] **Step 1: Add the mutations**

In `web/src/hooks/mutations.ts`, mirror `useApply` (which posts and invalidates `keys.updates, keys.projects`):

```ts
export function useLifecycle() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { serviceId: number; action: "start" | "stop" | "restart" }) =>
      apiFetch<{ job_id: number }>(`/api/services/${v.serviceId}/lifecycle`, {
        method: "POST",
        body: { action: v.action },
      }),
    onSuccess: () => invalidate(qc, keys.projects),
  });
}

export function useRemoveContainer() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (serviceId: number) =>
      apiFetch<{ job_id: number }>(`/api/services/${serviceId}/remove`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.projects),
  });
}
```

- [ ] **Step 2: Add the logs fetch helper**

In `web/src/hooks/queries.ts` (or a small `web/src/api/logs.ts`), add:

```ts
export function fetchServiceLogs(serviceId: number, tail = 500): Promise<{ logs: string }> {
  return apiFetch<{ logs: string }>(`/api/services/${serviceId}/logs?tail=${tail}`);
}
```

Confirm `apiFetch` is imported from `@/api/client` in that file.

- [ ] **Step 3: Typecheck + commit**

Run (from `web/`): `./node_modules/.bin/tsc -b --noEmit`
Expected: clean.

```bash
git add web/src/hooks/mutations.ts web/src/hooks/queries.ts
git commit -m "feat(web): lifecycle + remove mutations and logs fetch helper"
```

---

### Task 7: State-aware action menu (Start/Stop/Restart/Logs)

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (`ActionsCell` gains a lifecycle menu)
- Test: `web/src/components/DashboardTable.test.tsx` (append)

**Interfaces:**
- Consumes: `useLifecycle` (Task 6), `service.state`, `service.id`.
- Produces: a per-service menu whose visible items depend on state (Start when stopped; Stop + Restart when running; Logs always) and a Logs callback prop `onLogs(service)`.

- [ ] **Step 1: Write the failing test**

Append to `web/src/components/DashboardTable.test.tsx` a test that renders a running service and a stopped service and asserts the correct menu items. Use the same `renderDashboardWithRouter()` + `server.use(http.get("/api/projects", ...))` pattern already in the file. Assertions:
- running service row exposes Stop and Restart controls, not Start;
- stopped service row exposes Start, not Stop;
- both expose a Logs control;
- clicking Stop calls `POST /api/services/:id/lifecycle` with `{action:"stop"}` (assert via an `http.post` MSW handler capturing the body).

(Write the concrete test mirroring the existing "toggles a project group" test's structure and the MSW body-capture pattern used elsewhere in the suite.)

- [ ] **Step 2: Run test to verify it fails**

Run (from `web/`): `npm test -- DashboardTable`
Expected: FAIL (no lifecycle controls).

- [ ] **Step 3: Implement the menu**

Extend `ActionsCell` in `web/src/components/DashboardTable.tsx`. Add a dropdown (reuse the existing `@/components/ui/dropdown-menu` if present; otherwise inline buttons) driven by `isStopped(service.state)` (import the existing helper from `@/components/StatusBadge`). Wire `useLifecycle()` for start/stop/restart and an `onLogs(service)` callback prop threaded from `DashboardTable` down to `ActionsCell`. Start shown when stopped; Stop + Restart when running; Logs always. On a lifecycle success, call `onApplied(job_id)` so the existing live-log panel opens (parity with apply).

(Provide the full JSX for the menu in the implementation, matching the file's existing button/dropdown idioms.)

- [ ] **Step 4: Run test + typecheck + commit**

Run (from `web/`): `npm test -- DashboardTable && ./node_modules/.bin/tsc -b --noEmit`
Expected: PASS + clean.

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): state-aware lifecycle action menu on service rows"
```

---

### Task 8: Logs drawer

**Files:**
- Create: `web/src/components/LogsDrawer.tsx`
- Modify: `web/src/routes/dashboard.tsx` and `web/src/routes/project.$id.tsx` (mount the drawer, pass `onLogs`)
- Test: `web/src/components/LogsDrawer.test.tsx`

**Interfaces:**
- Consumes: `fetchServiceLogs` (Task 6), the `onLogs(service)` callback (Task 7).
- Produces: a drawer showing the tail with a Refresh button.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/LogsDrawer.test.tsx`: render the drawer for a service with an MSW `GET /api/services/:id/logs` returning known text; assert the text renders; click Refresh; assert a second fetch occurs (bump the MSW response and assert the new text). Mirror `ChangelogDrawer.test.tsx` structure.

- [ ] **Step 2: Run test to verify it fails**

Run (from `web/`): `npm test -- LogsDrawer`
Expected: FAIL (component does not exist).

- [ ] **Step 3: Implement the drawer**

Create `web/src/components/LogsDrawer.tsx` mirroring `ChangelogDrawer.tsx`: a controlled drawer (`open`, `onOpenChange`) that on open (and on Refresh) calls `fetchServiceLogs(service.id)` and renders the text in a scrollable `<pre>` (monospace, `overflow-auto`). Show a loading state and an error state. No streaming.

(Provide the full component in the implementation, matching `ChangelogDrawer.tsx` conventions.)

- [ ] **Step 4: Mount it**

In `web/src/routes/dashboard.tsx` and `web/src/routes/project.$id.tsx`, add local state `const [logsFor, setLogsFor] = useState<Service | null>(null)`, pass `onLogs={setLogsFor}` into `DashboardTable`, and render `<LogsDrawer service={logsFor} open={logsFor != null} onOpenChange={(o) => !o && setLogsFor(null)} />`. Thread the `onLogs` prop type through `DashboardTableProps` and `ActionsCell` (added in Task 7).

- [ ] **Step 5: Run test + typecheck + commit**

Run (from `web/`): `npm test -- LogsDrawer && ./node_modules/.bin/tsc -b --noEmit`
Expected: PASS + clean.

```bash
git add web/src/components/LogsDrawer.tsx web/src/routes/dashboard.tsx "web/src/routes/project.\$id.tsx" web/src/components/LogsDrawer.test.tsx
git commit -m "feat(web): container logs drawer (tail + refresh)"
```

---

### Task 9: Loose-group bulk remove

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (Loose group gains per-row Remove + multi-select bulk Remove with confirm)
- Test: `web/src/components/DashboardTable.test.tsx` (append)

**Interfaces:**
- Consumes: `useRemoveContainer` (Task 6), the loose-group rendering (from the loose-container-grouping feature already on this branch).

- [ ] **Step 1: Write the failing test**

Append to `web/src/components/DashboardTable.test.tsx`: render the dashboard with two stopped loose (auto_named) standalone services; expand the Loose group; select both via checkboxes; click bulk Remove; assert a confirm dialog lists both container names; confirm; assert two `POST /api/services/:id/remove` calls fire (capture via MSW). Also assert a running loose service does NOT offer Remove.

- [ ] **Step 2: Run test to verify it fails**

Run (from `web/`): `npm test -- DashboardTable`
Expected: FAIL (no remove controls in the loose group).

- [ ] **Step 3: Implement**

In `DashboardTable.tsx`, within the Loose group (added earlier on this branch): add a checkbox per stopped loose service row and a bulk "Remove selected" button on the Loose header that opens a confirm dialog (reuse the existing dialog primitive, e.g. `@/components/ui/alert-dialog` or the pattern used for other destructive confirms) listing the selected container names. On confirm, call `useRemoveContainer().mutate(serviceId)` for each selected id. Only stopped (`isStopped(state)`) loose services are selectable/removable. Track selection in local `useState<Set<number>>`.

(Provide the full JSX + handlers in the implementation.)

- [ ] **Step 4: Run test + full frontend verify + commit**

Run (from `web/`): `./node_modules/.bin/tsc -b --noEmit && npm test`
Expected: clean + all pass.

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): bulk remove for stopped loose containers"
```

---

## Final verification

- [ ] **Backend:** `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...` all pass.
- [ ] **Frontend (from `web/`):** `./node_modules/.bin/tsc -b --noEmit && npm test` clean.
- [ ] **Manual smoke (optional):** `mise run build && ./dockbrr`; on a host with a stopped loose container, confirm: Logs drawer shows output; Start/Stop/Restart enqueue jobs and the live-log panel opens; a stopped loose container can be removed (single + bulk) and disappears after reconcile; a running or compose container is refused removal.

## Notes / gotchas

- The `docker` SDK option types (`StartOptions`, `StopOptions`, `RemoveOptions`, `LogsOptions`) live in `github.com/docker/docker/api/types/container` for v28.5.2. `stdcopy` is `github.com/docker/docker/pkg/stdcopy`. Both are already in the module graph (no `go get`).
- Tty containers emit un-multiplexed logs; `stdcopy.StdCopy` handles the common non-tty case. Tty log fidelity is a known minor limitation for the tail MVP; do not block on it.
- `isStoppedState` is defined once in `store` (Task 4) and used by both the runner (Task 2) and the handler; the runner's local copy from Task 2 Step 3 must be replaced by `store.IsStoppedState` when Task 4 lands (same-branch follow-up within Task 4's commit).
- Confirm exact names before coding each backend task: `store.Jobs` status setter, `store.Events.Insert` shape, `Rediscoverer` method name, the httpapi `Deps` engine field + test `fakeEngine`, and the `decodeJSON`/`writeJSON`/`writeJSONError` helpers. These exist; match them rather than inventing.
