# Scan progress + cross-navigation button disabling — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn "Check all" into a determinate, background scan-run so the UI shows a progress bar and every check button stays disabled while a check runs, across page navigation.

**Architecture:** A single global in-flight *scan-run* lives server-side in memory (`ScanRunner` in httpapi), driven on a background context so it survives the request. It broadcasts `scan_progress` / `scan_finished` over the existing SSE bus and exposes an authoritative `GET /api/scan` snapshot. The frontend holds the run in a `useSyncExternalStore` module store (`useScanRun`), seeded on mount + SSE reconnect and updated live from events. Detection stays read-only and entirely outside the Job Engine.

**Tech Stack:** Go 1.26 (CGO_ENABLED=0), chi router, SQLite store; web = Vite + TS + React + TanStack Query + Tailwind v4, vitest + MSW.

## Global Constraints

- `CGO_ENABLED=0 go build ./...` must stay green (static-binary invariant #1).
- Detection never mutates Docker and never goes through the Job Engine (invariant #2).
- Compose/Docker untouched by this change (read-only feature).
- TS typecheck via `npm run typecheck` (NOT `npx tsc` — rtk masks errors). `npm run build` is the backstop.
- No em-dashes in code comments or docs.
- Frontend: CSRF header on mutations only (handled by `apiFetch`); GET stays plain.
- Spec: `docs/dev/specs/2026-07-20-scan-progress-design.md`.

---

## Task summary table

| # | Task | Files | Deliverable |
|---|------|-------|-------------|
| 1 | `scan.CheckServicesFresh` | `internal/scan/scan.go` (+test) | scoped fresh sweep with per-service `onDone` callback |
| 2 | Event fields + Checker iface | `internal/httpapi/events_stream.go`, `server.go` (+test) | `Done`/`Total` on `Event`; `CheckServicesFresh` on `Checker` |
| 3 | `ScanRunner` | `internal/httpapi/scanrun.go` (+test) | single-flight in-memory scan-run, progress/finished/scanned events, `last_check_all` stamp on all-scope |
| 4 | Endpoints + wiring | `internal/httpapi/server.go`, `scan.go`, `updates.go` (+tests) | async `POST /api/scan`, `POST /check` routed via runner, `GET /api/scan` |
| 5 | `useScanRun` store | `web/src/api/types.ts`, `web/src/hooks/useScanRun.ts` (+test) | server-authoritative `{running,done,total}` store |
| 6 | Event stream wiring | `web/src/hooks/useEventStream.ts`, `web/src/test/msw.ts` (+test) | `scan_progress`/`scan_finished` handling + on-open resync |
| 7 | Mutations | `web/src/hooks/mutations.ts` (+test) | `useScanAll` / `useProjectScan` / `useServiceCheck` (no fan-out) |
| 8 | Buttons + progress UI | `web/src/components/BulkActions.tsx`, `ScanProgress.tsx`, `DashboardTable.tsx`, `routes/dashboard.tsx`, `routes/project.$id.tsx` (+tests) | disabled-while-running buttons + determinate progress bar |

Commands: `CGO_ENABLED=0 go test ./...` (Go), `cd web && npm test` (vitest), `cd web && npm run typecheck` (types).

---

## Task 1: `scan.CheckServicesFresh`

**Files:**
- Modify: `internal/scan/scan.go` (add method near `CheckAllFresh` ~L257; reimplement `CheckAllFresh`)
- Test: `internal/scan/scan_test.go`

**Interfaces:**
- Produces: `func (s *Scanner) CheckServicesFresh(ctx context.Context, ids []int64, onDone func(done, total int)) error`
- Consumes: existing `s.services.Get`, `s.invalidateFor`, `s.CheckService`.

- [ ] **Step 1: Write the failing test**

Add to `internal/scan/scan_test.go` (reuse the package's existing scanner test harness/fakes — grep an existing test like `TestCheckAllFresh`/`TestCheckService` for the setup helper and copy its construction):

```go
func TestCheckServicesFreshReportsProgressPerService(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 3) // helper mirrors existing scan_test setup; returns 3 seeded service ids
	var got [][2]int
	err := sc.CheckServicesFresh(context.Background(), svcIDs, func(done, total int) {
		got = append(got, [2]int{done, total})
	})
	if err != nil {
		t.Fatalf("CheckServicesFresh: %v", err)
	}
	want := [][2]int{{1, 3}, {2, 3}, {3, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %v, want %v", got, want)
	}
}

func TestCheckServicesFreshContinuesPastMissingService(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 2)
	ids := append([]int64{999999}, svcIDs...) // 999999 does not exist
	var calls int
	err := sc.CheckServicesFresh(context.Background(), ids, func(done, total int) { calls++ })
	if err != nil {
		t.Fatalf("want nil error (per-service errors are logged, not returned), got %v", err)
	}
	if calls != len(ids) {
		t.Fatalf("onDone calls = %d, want %d (fires even for the missing id)", calls, len(ids))
	}
}
```

If no reusable helper exists, adapt the construction block from the nearest existing `Scanner` test verbatim rather than inventing new fakes.

- [ ] **Step 2: Run to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -run CheckServicesFresh -v`
Expected: FAIL (`CheckServicesFresh` undefined).

- [ ] **Step 3: Implement**

In `internal/scan/scan.go`, add:

```go
// CheckServicesFresh invalidates each service's detect cache and checks it,
// invoking onDone(done, total) after every service completes (whether it
// detected drift, found nothing, or errored). Per-service errors are logged
// and the sweep continues, matching checkAll. onDone may be nil.
func (s *Scanner) CheckServicesFresh(ctx context.Context, ids []int64, onDone func(done, total int)) error {
	total := len(ids)
	for i, id := range ids {
		if svc, err := s.services.Get(id); err != nil {
			logger.Errorf("scan: load service %d: %v", id, err)
		} else {
			if s.states != nil {
				s.invalidateFor(svc)
			}
			if err := s.CheckService(ctx, id); err != nil {
				logger.Errorf("scan: check service %d (%s): %v", id, svc.Name, err)
			}
		}
		if onDone != nil {
			onDone(i+1, total)
		}
	}
	return nil
}
```

Reimplement `CheckAllFresh` over it (replaces the `checkAll(ctx, true)` call):

```go
// CheckAllFresh runs a fresh (cache-invalidated) check of every service.
func (s *Scanner) CheckAllFresh(ctx context.Context) error {
	svcs, err := s.services.List()
	if err != nil {
		return err
	}
	ids := make([]int64, len(svcs))
	for i, sv := range svcs {
		ids[i] = sv.ID
	}
	logger.Infof("scan: checking %d service(s)", len(ids))
	return s.CheckServicesFresh(ctx, ids, nil)
}
```

Leave `checkAll` (non-fresh, used by `CheckAll` for the scheduler) and `CheckServiceFresh` untouched. If `reflect` is not yet imported in the test file, add it.

- [ ] **Step 4: Run to verify pass**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -v`
Expected: PASS (all scan tests, including the existing `CheckAllFresh` ones).

- [ ] **Step 5: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go
git commit -m "feat(scan): scoped CheckServicesFresh with per-service progress callback"
```

---

## Task 2: Event progress fields + `Checker` interface extension

**Files:**
- Modify: `internal/httpapi/events_stream.go:19-24` (Event struct)
- Modify: `internal/httpapi/server.go:34-37` (Checker interface)
- Test: covered by Task 3/4 tests (this task is a compile-level change; no standalone test).

**Interfaces:**
- Produces: `Event{ ..., Done int, Total int }`; `Checker` gains `CheckServicesFresh(ctx, ids []int64, onDone func(done, total int)) error`.
- Consumes: Task 1's `*scan.Scanner` method (satisfies the extended interface).

- [ ] **Step 1: Extend the Event struct**

In `internal/httpapi/events_stream.go`, replace the `Event` struct:

```go
type Event struct {
	Type      string `json:"type"` // detected|job_finished|reconciled|scanned|scan_progress|scan_finished
	ServiceID int64  `json:"service_id,omitempty"`
	JobID     int64  `json:"job_id,omitempty"`
	// Done/Total carry scan-run progress. Exception to the payload-free hint
	// rule: progress is ephemeral with no query to refetch. The authoritative
	// GET /api/scan snapshot self-heals dropped events on mount/reconnect.
	Done  int `json:"done,omitempty"`
	Total int `json:"total,omitempty"`
}
```

- [ ] **Step 2: Extend the Checker interface**

In `internal/httpapi/server.go`, add the method to `Checker`:

```go
type Checker interface {
	CheckServiceFresh(ctx context.Context, serviceID int64) error
	CheckAllFresh(ctx context.Context) error
	CheckServicesFresh(ctx context.Context, ids []int64, onDone func(done, total int)) error
}
```

- [ ] **Step 3: Update the test fake**

Find the fake Checker used in httpapi tests (grep `CheckAllFresh` under `internal/httpapi/*_test.go`) and add a `CheckServicesFresh` method to it that, by default, iterates ids and calls `onDone(i+1, len(ids))`:

```go
func (f *fakeChecker) CheckServicesFresh(ctx context.Context, ids []int64, onDone func(done, total int)) error {
	f.servicesFreshCalls = append(f.servicesFreshCalls, ids) // add this field to the fake
	for i := range ids {
		if onDone != nil {
			onDone(i+1, len(ids))
		}
	}
	return f.servicesFreshErr // add this field; zero value nil
}
```

- [ ] **Step 4: Verify it compiles**

Run: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./internal/httpapi/ -run TestNothing`
Expected: builds clean (no test selected is fine).

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/events_stream.go internal/httpapi/server.go internal/httpapi/*_test.go
git commit -m "feat(httpapi): add scan progress fields to Event and CheckServicesFresh to Checker"
```

---

## Task 3: `ScanRunner`

**Files:**
- Create: `internal/httpapi/scanrun.go`
- Test: `internal/httpapi/scanrun_test.go`

**Interfaces:**
- Consumes: `Checker` (Task 2), `*store.Services` (`List`, `ListByProject`), `*store.Settings` (`Set`), `*Bus` (`Publish`).
- Produces:
  - `type scanState struct { Running bool; Done int; Total int }` with JSON tags `running`/`done`/`total`
  - `var ErrScanBusy error`
  - `func NewScanRunner(checker Checker, services *store.Services, settings *store.Settings, bus *Bus) *ScanRunner`
  - `func (sr *ScanRunner) Start(scope string, projectID, serviceID int64) (scanState, error)` — scope in `"all"|"project"|"service"`
  - `func (sr *ScanRunner) Snapshot() scanState`

- [ ] **Step 1: Write the failing test**

Create `internal/httpapi/scanrun_test.go`. Use the same store test helper the other httpapi/store tests use to get a `*store.DB` with a seeded project + services (grep `store.NewServices` / `openTestDB` in existing tests for the exact helper):

```go
package httpapi

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// blockingChecker lets the test hold a run "in flight" to exercise single-flight.
type blockingChecker struct {
	release chan struct{}
	started chan struct{}
}

func (b *blockingChecker) CheckServiceFresh(context.Context, int64) error { return nil }
func (b *blockingChecker) CheckAllFresh(context.Context) error            { return nil }
func (b *blockingChecker) CheckServicesFresh(_ context.Context, ids []int64, onDone func(done, total int)) error {
	close(b.started)
	<-b.release
	for i := range ids {
		if onDone != nil {
			onDone(i+1, len(ids))
		}
	}
	return nil
}

func TestScanRunnerSingleFlight(t *testing.T) {
	db, projectID, svcIDs := seedProjectServices(t, 2) // helper: returns db + one project + 2 service ids
	bus := NewBus()
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	sr := NewScanRunner(bc, storeServices(db), storeSettings(db), bus)

	_, err := sr.Start("all", 0, 0)
	if err != nil {
		t.Fatalf("first Start: %v", err)
	}
	<-bc.started
	if _, err := sr.Start("all", 0, 0); !errors.Is(err, ErrScanBusy) {
		t.Fatalf("second Start err = %v, want ErrScanBusy", err)
	}
	if snap := sr.Snapshot(); !snap.Running || snap.Total != 2 {
		t.Fatalf("snapshot = %+v, want running total=2", snap)
	}
	close(bc.release)
	_ = projectID
	_ = svcIDs
}

func TestScanRunnerAllScopeStampsLastCheckAllAndPublishes(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, bus) // fakeChecker from Task 2 auto-completes

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	types := drainEventTypes(t, ch, 300*time.Millisecond) // helper: collect event types until quiet
	if !contains(types, "scan_finished") || !contains(types, "scanned") {
		t.Fatalf("event types = %v, want scanned + scan_finished", types)
	}
	if v, _ := settings.Get("last_check_all"); v == "" {
		t.Fatalf("last_check_all not stamped for all scope")
	}
}

func TestScanRunnerServiceScopeDoesNotStampLastCheckAll(t *testing.T) {
	db, _, svcIDs := seedProjectServices(t, 1)
	bus := NewBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, bus)

	if _, err := sr.Start("service", 0, svcIDs[0]); err != nil {
		t.Fatalf("Start: %v", err)
	}
	types := drainEventTypes(t, ch, 300*time.Millisecond)
	if contains(types, "scanned") {
		t.Fatalf("service scope must NOT publish scanned; got %v", types)
	}
	if v, _ := settings.Get("last_check_all"); v != "" {
		t.Fatalf("service scope must not stamp last_check_all, got %q", v)
	}
}
```

Provide the small helpers (`seedProjectServices`, `storeServices`, `storeSettings`, `drainEventTypes`, `contains`, and a `sync` import if used) in `scanrun_test.go` or a shared `testutil_test.go`. Model `seedProjectServices` on the existing store-seeding used by `projects_test.go`/`scan_test.go` in httpapi.

- [ ] **Step 2: Run to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run ScanRunner -v`
Expected: FAIL (`NewScanRunner`/`ScanRunner` undefined).

- [ ] **Step 3: Implement `scanrun.go`**

```go
package httpapi

import (
	"context"
	"errors"
	"sync"
	"time"

	"dockbrr/internal/logger"
	"dockbrr/internal/store"
)

// scanRunTimeout bounds an unattended background sweep. Generous versus the
// old 60s request cap since the run no longer blocks an HTTP request.
const scanRunTimeout = 5 * time.Minute

// ErrScanBusy is returned by Start when a scan-run is already in flight.
var ErrScanBusy = errors.New("a scan is already running")

// scanState is the authoritative snapshot of the single in-flight scan-run.
type scanState struct {
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`
}

// ScanRunner owns the process-wide single scan-run: a read-only detection
// sweep tracked in memory and broadcast over the SSE bus. It is NOT a Job
// Engine job (detection never mutates Docker; invariant #2).
type ScanRunner struct {
	checker  Checker
	services *store.Services
	settings *store.Settings
	bus      *Bus

	mu    sync.Mutex
	state scanState
}

func NewScanRunner(checker Checker, services *store.Services, settings *store.Settings, bus *Bus) *ScanRunner {
	return &ScanRunner{checker: checker, services: services, settings: settings, bus: bus}
}

// Start begins a scan-run over scope ("all" | "project" | "service"). It
// returns the started snapshot, or ErrScanBusy if one is already running.
func (sr *ScanRunner) Start(scope string, projectID, serviceID int64) (scanState, error) {
	ids, err := sr.resolve(scope, projectID, serviceID)
	if err != nil {
		return scanState{}, err
	}
	sr.mu.Lock()
	if sr.state.Running {
		st := sr.state
		sr.mu.Unlock()
		return st, ErrScanBusy
	}
	sr.state = scanState{Running: true, Done: 0, Total: len(ids)}
	st := sr.state
	sr.mu.Unlock()

	sr.publish(Event{Type: "scan_progress", Done: 0, Total: len(ids)})
	go sr.run(scope, ids)
	return st, nil
}

// Snapshot returns the current scan-run state.
func (sr *ScanRunner) Snapshot() scanState {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	return sr.state
}

func (sr *ScanRunner) resolve(scope string, projectID, serviceID int64) ([]int64, error) {
	switch scope {
	case "service":
		return []int64{serviceID}, nil
	case "project":
		svcs, err := sr.services.ListByProject(projectID)
		if err != nil {
			return nil, err
		}
		return idsOf(svcs), nil
	default: // "all"
		svcs, err := sr.services.List()
		if err != nil {
			return nil, err
		}
		return idsOf(svcs), nil
	}
}

func (sr *ScanRunner) run(scope string, ids []int64) {
	ctx, cancel := context.WithTimeout(context.Background(), scanRunTimeout)
	defer cancel()

	_ = sr.checker.CheckServicesFresh(ctx, ids, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})

	if scope == "all" || scope == "" {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := sr.settings.Set("last_check_all", now); err != nil {
			logger.Errorf("scan: record last_check_all: %v", err)
		}
		sr.publish(Event{Type: "scanned"})
	}

	sr.mu.Lock()
	sr.state = scanState{Running: false}
	sr.mu.Unlock()
	sr.publish(Event{Type: "scan_finished"})
}

func (sr *ScanRunner) publish(e Event) {
	if sr.bus != nil {
		sr.bus.Publish(e)
	}
}

func idsOf(svcs []store.Service) []int64 {
	ids := make([]int64, len(svcs))
	for i, sv := range svcs {
		ids[i] = sv.ID
	}
	return ids
}
```

- [ ] **Step 4: Run to verify pass**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run ScanRunner -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/scanrun.go internal/httpapi/scanrun_test.go internal/httpapi/testutil_test.go
git commit -m "feat(httpapi): ScanRunner drives single-flight background scan-run"
```

---

## Task 4: Endpoints + server wiring

**Files:**
- Modify: `internal/httpapi/server.go` (Server struct add `scan *ScanRunner`; construct in `New`; add `GET /api/scan` route)
- Modify: `internal/httpapi/scan.go` (rewrite `handleScanAll` async + add `handleScanStatus`)
- Modify: `internal/httpapi/updates.go:174` (`handleCheck` routes via runner)
- Test: `internal/httpapi/scan_test.go`, `internal/httpapi/updates_test.go`

**Interfaces:**
- Consumes: `ScanRunner.Start`, `ScanRunner.Snapshot`, `ErrScanBusy` (Task 3).
- Produces: `POST /api/scan` → `202 {running,total}` / `409`; `POST /api/services/{id}/check` → `202 {running,total}` / `409`; `GET /api/scan` → `200 {running,done,total}`.

- [ ] **Step 1: Write failing handler tests**

Add to `internal/httpapi/scan_test.go` (reuse the existing test server builder in that file / `server_test.go`; grep for how a `*Server` + `Deps` with a fake Checker is constructed):

```go
func TestScanAllStartsAndReports202(t *testing.T) {
	srv := newTestServer(t) // existing helper; wires Deps incl. Bus + fake Checker + real store
	rr := doJSON(t, srv, "POST", "/api/scan", nil)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
}

func TestScanStatusSnapshot(t *testing.T) {
	srv := newTestServer(t)
	rr := doJSON(t, srv, "GET", "/api/scan", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Running bool `json:"running"`
	}
	mustUnmarshal(t, rr.Body.Bytes(), &got)
	if got.Running {
		t.Fatalf("fresh server should report running=false")
	}
}
```

For the busy-409 path, drive `srv`'s `ScanRunner` with a blocking fake Checker (as in Task 3) so a run is in flight, then assert a second `POST /api/scan` returns `409`. If `newTestServer` does not expose the Checker, add an option to inject one.

- [ ] **Step 2: Run to verify fail**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run 'ScanAll|ScanStatus' -v`
Expected: FAIL (routes/handlers not updated).

- [ ] **Step 3: Wire the Server**

In `internal/httpapi/server.go`:

1. Add field to `Server`:
```go
type Server struct {
	cfg     config.Config
	db      *store.DB
	deps    Deps
	mux     *chi.Mux
	limiter *loginLimiter
	scan    *ScanRunner
}
```
2. In `New(...)`, after `deps` is stored on the server and before routes are registered, construct it:
```go
s.scan = NewScanRunner(deps.Checker, deps.Services, deps.Settings, deps.Bus)
```
(Place this next to the existing `deps` assignment; grep `s.deps = ` / `&Server{` in `New` for the exact spot.)
3. Add the status route beside the existing scan route (after `server.go:177`):
```go
r.Post("/api/scan", s.handleScanAll)
r.Get("/api/scan", s.handleScanStatus)
```

- [ ] **Step 4: Rewrite `handleScanAll` + add `handleScanStatus`**

Replace the body of `internal/httpapi/scan.go`'s `handleScanAll` and append `handleScanStatus`:

```go
// handleScanAll starts a background scan-run. With no body it sweeps every
// service (scope "all", which stamps last_check_all + publishes "scanned").
// With {"project_id": N} it sweeps that project only. Returns 202 immediately;
// progress streams over SSE. A scan already in flight returns 409.
func (s *Server) handleScanAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectID int64 `json:"project_id"`
	}
	_ = decodeJSON(r, &body) // optional

	scope, projectID := "all", int64(0)
	if body.ProjectID > 0 {
		scope, projectID = "project", body.ProjectID
	}
	logger.Infof("scan: manual check-all requested (scope=%s project=%d)", scope, projectID)

	st, err := s.scan.Start(scope, projectID, 0)
	if errors.Is(err, ErrScanBusy) {
		writeJSONError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// handleScanStatus returns the authoritative scan-run snapshot so a freshly
// mounted (or reconnected) client can seed/resync its progress state.
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scan.Snapshot())
}
```

Update imports in `scan.go`: drop the now-unused `context`/`time` if no longer referenced (the compiler will tell you), add `errors`. Keep `logger`.

- [ ] **Step 5: Route `handleCheck` through the runner**

Replace `internal/httpapi/updates.go`'s `handleCheck`:

```go
// handleCheck starts a scan-run scoped to a single service. It returns 202
// immediately (progress + completion arrive over SSE); a scan already in
// flight returns 409.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	logger.Infof("check: manual check requested (service %d (%s))", id, s.serviceName(id))
	st, err := s.scan.Start("service", 0, id)
	if errors.Is(err, ErrScanBusy) {
		writeJSONError(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}
```

Drop now-unused `context`/`time` imports from `updates.go` only if nothing else there uses them (check `handleApply` etc. first).

- [ ] **Step 6: Run to verify pass + full package**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -v`
Expected: PASS. Update any existing `handleScanAll`/`handleCheck` tests that asserted `200 {"status":"checked"}` to expect `202` + the snapshot shape (grep `"checked"` in httpapi tests).

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/server.go internal/httpapi/scan.go internal/httpapi/updates.go internal/httpapi/scan_test.go internal/httpapi/updates_test.go
git commit -m "feat(httpapi): async scan endpoints backed by ScanRunner (202/409 + GET snapshot)"
```

---

## Task 5: `useScanRun` store

**Files:**
- Modify: `web/src/api/types.ts` (add `ScanRun`)
- Create: `web/src/hooks/useScanRun.ts`
- Test: `web/src/hooks/useScanRun.test.tsx`

**Interfaces:**
- Produces: `interface ScanRun { running: boolean; done: number; total: number }`; `setScanRun(next: ScanRun)`, `useScanRun(): ScanRun`, `__resetScanRun()`.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/useScanRun.test.tsx`:

```tsx
import { renderHook, act } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { useScanRun, setScanRun, __resetScanRun } from "./useScanRun";

afterEach(() => __resetScanRun());

describe("useScanRun", () => {
  it("defaults to not running", () => {
    const { result } = renderHook(() => useScanRun());
    expect(result.current).toEqual({ running: false, done: 0, total: 0 });
  });

  it("reflects setScanRun updates", () => {
    const { result } = renderHook(() => useScanRun());
    act(() => setScanRun({ running: true, done: 2, total: 5 }));
    expect(result.current).toEqual({ running: true, done: 2, total: 5 });
  });

  it("keeps a stable snapshot reference when value is unchanged", () => {
    const { result, rerender } = renderHook(() => useScanRun());
    const first = result.current;
    act(() => setScanRun({ running: false, done: 0, total: 0 }));
    rerender();
    expect(result.current).toBe(first);
  });
});
```

- [ ] **Step 2: Run to verify fail**

Run: `cd web && npm test -- useScanRun`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement**

Add to `web/src/api/types.ts`:

```ts
export interface ScanRun {
  running: boolean;
  done: number;
  total: number;
}
```

Create `web/src/hooks/useScanRun.ts`:

```ts
import { useSyncExternalStore } from "react";
import type { ScanRun } from "@/api/types";

// Server-authoritative scan-run state, external to any component so every
// check button (dashboard, project, per-row) and the progress bar share one
// source of truth that survives navigation. Mirrors useBusyServices.
let state: ScanRun = { running: false, done: 0, total: 0 };
const listeners = new Set<() => void>();

function emit() {
  for (const l of listeners) l();
}

export function setScanRun(next: ScanRun) {
  if (next.running === state.running && next.done === state.done && next.total === state.total) return;
  state = next;
  emit();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

function getSnapshot(): ScanRun {
  return state;
}

export function useScanRun(): ScanRun {
  return useSyncExternalStore(subscribe, getSnapshot);
}

// Test seam: reset module state between cases.
export function __resetScanRun() {
  state = { running: false, done: 0, total: 0 };
  emit();
}
```

- [ ] **Step 4: Run to verify pass + typecheck**

Run: `cd web && npm test -- useScanRun && npm run typecheck`
Expected: PASS, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/api/types.ts web/src/hooks/useScanRun.ts web/src/hooks/useScanRun.test.tsx
git commit -m "feat(web): useScanRun store for server-authoritative scan-run state"
```

---

## Task 6: Event stream wiring + resync

**Files:**
- Modify: `web/src/hooks/useEventStream.ts` (new event cases + on-open resync)
- Modify: `web/src/test/msw.ts` (add `GET /api/scan` handler; keep `POST /api/scan` and `POST /check`)
- Test: `web/src/hooks/useEventStream.test.tsx`

**Interfaces:**
- Consumes: `setScanRun` (Task 5), `apiFetch`, `ScanRun`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/hooks/useEventStream.test.tsx` (this file already installs an EventSource factory via `__setEventSourceFactory`; follow its existing pattern):

```tsx
it("updates the scan-run store on scan_progress and clears on scan_finished", async () => {
  // render the hook with the test EventSource, emit events, assert store.
  emit({ type: "scan_progress", done: 3, total: 10 });
  expect(getScanRunForTest().running).toBe(true);
  expect(getScanRunForTest().done).toBe(3);

  emit({ type: "scan_finished" });
  expect(getScanRunForTest().running).toBe(false);
});
```

Read `useScanRun`'s current value in-test via `import { useScanRun, __resetScanRun }` rendered in a probe hook, or import the module's `getSnapshot` equivalent by reading through `renderHook(() => useScanRun())`. Reset with `__resetScanRun()` in `afterEach`. Match the existing test's mechanics for emitting a message (it constructs a fake `EventSource` and calls `onmessage`).

- [ ] **Step 2: Run to verify fail**

Run: `cd web && npm test -- useEventStream`
Expected: FAIL (no scan_progress handling yet).

- [ ] **Step 3: Implement the event cases**

In `web/src/hooks/useEventStream.ts`:

1. Imports:
```ts
import { apiFetch } from "@/api/client";
import { setScanRun } from "@/hooks/useScanRun";
import type { ScanRun } from "@/api/types";
```
2. Widen the parsed shape:
```ts
const ev = JSON.parse(e.data as string) as {
  type: string; service_id?: number; job_id?: number; done?: number; total?: number;
};
```
3. Add cases inside the `switch (ev.type)`:
```ts
case "scan_progress":
  setScanRun({ running: true, done: ev.done ?? 0, total: ev.total ?? 0 });
  break;
case "scan_finished":
  setScanRun({ running: false, done: 0, total: 0 });
  void qc.invalidateQueries({ queryKey: keys.updates });
  void qc.invalidateQueries({ queryKey: keys.projects });
  void qc.invalidateQueries({ queryKey: keys.status });
  break;
```
4. Resync on (re)connect: replace the `es.onopen` handler:
```ts
es.onopen = () => {
  attempts = 0; // healthy connection → reset backoff
  // Authoritative resync: a page mounted mid-scan, or one whose stream blipped,
  // learns the true running state (dropped progress events self-heal here).
  void apiFetch<ScanRun>("/api/scan").then(setScanRun).catch(() => {});
};
```

- [ ] **Step 4: Add the MSW handler**

In `web/src/test/msw.ts`, add a handler for the snapshot (and confirm `POST /api/scan` still returns an accepting response, e.g. `202`/`200` with `{running:false,total:0}`):

```ts
http.get("/api/scan", () => HttpResponse.json({ running: false, done: 0, total: 0 })),
```

- [ ] **Step 5: Run to verify pass + typecheck**

Run: `cd web && npm test -- useEventStream && npm run typecheck`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/hooks/useEventStream.ts web/src/hooks/useEventStream.test.tsx web/src/test/msw.ts
git commit -m "feat(web): drive scan-run store from SSE + resync GET /api/scan on connect"
```

---

## Task 7: Mutations

**Files:**
- Modify: `web/src/hooks/mutations.ts` (replace `useScanAll`, `useCheckAll`, `useCheck`)
- Test: `web/src/hooks/mutations.test.tsx` if present, else covered by Task 8 component tests.

**Interfaces:**
- Produces: `useScanAll()` → POST `/api/scan` (no body); `useProjectScan()` → POST `/api/scan` `{project_id}`; `useServiceCheck()` → POST `/api/services/:id/check`.
- Consumes: `apiFetch`, `ApiError` (for 409 suppression).

- [ ] **Step 1: Implement the three mutations**

In `web/src/hooks/mutations.ts`, import the error type (grep the export in `web/src/api/client.ts` — it is `ApiError`):

```ts
import { apiFetch, ApiError } from "@/api/client";
```

Replace `useCheck`, `useCheckAll`, `useScanAll` with:

```ts
// A 409 means a scan-run is already in flight (buttons are disabled, so this
// is only a race). Swallow it; surface anything else.
const scanError = (e: unknown) => {
  if (e instanceof ApiError && e.status === 409) return;
  notify.error(e instanceof Error ? e.message : "Check failed");
};

// Global sweep: scope "all". Progress + completion arrive over SSE; the
// scan-run store drives the UI, so there is no onSuccess refetch here.
export function useScanAll() {
  return useMutation({
    mutationFn: () => apiFetch<{ running: boolean; total: number }>("/api/scan", { method: "POST" }),
    onError: scanError,
  });
}

// Scoped sweep of one project (single request, not a per-service fan-out).
export function useProjectScan() {
  return useMutation({
    mutationFn: (projectId: number) =>
      apiFetch<{ running: boolean; total: number }>("/api/scan", { method: "POST", body: { project_id: projectId } }),
    onError: scanError,
  });
}

// Single-service check, routed through the same scan-run.
export function useServiceCheck() {
  return useMutation({
    mutationFn: (serviceId: number) =>
      apiFetch<{ running: boolean; total: number }>(`/api/services/${serviceId}/check`, { method: "POST" }),
    onError: scanError,
  });
}
```

Remove the old `useCheck` / `useCheckAll` definitions. (Leave `useApply`, `useDismiss`, etc. untouched.)

- [ ] **Step 2: Verify types**

Run: `cd web && npm run typecheck`
Expected: fails only where old hooks are still referenced (Task 8 fixes call sites). If `mutations.test.tsx` exists and references the removed hooks, update it now to the new names.

- [ ] **Step 3: Commit**

```bash
git add web/src/hooks/mutations.ts web/src/hooks/mutations.test.tsx
git commit -m "feat(web): replace check mutations with scan-run starters (no fan-out)"
```

(Commit even though call sites are updated in Task 8; the two tasks land back-to-back.)

---

## Task 8: Buttons + progress UI

**Files:**
- Create: `web/src/components/ScanProgress.tsx`
- Modify: `web/src/components/BulkActions.tsx` (`CheckAllButton`, `ScanAllButton`)
- Modify: `web/src/components/DashboardTable.tsx` (row check button ~L131,229-236; `CheckAllButton` usage ~L409)
- Modify: `web/src/routes/dashboard.tsx:67` (add `ScanProgress`)
- Modify: `web/src/routes/project.$id.tsx:83` (add `ScanProgress`, pass `projectId`)
- Test: `web/src/components/ScanProgress.test.tsx`, `web/src/components/BulkActions.test.tsx` (if present), and existing `DashboardTable.test.tsx` / route tests updated.

**Interfaces:**
- Consumes: `useScanRun` (Task 5), `useScanAll`/`useProjectScan`/`useServiceCheck` (Task 7).
- Produces: `ScanProgress` component; `CheckAllButton({ projectId, serviceIds, label?, ariaLabel? })`.

- [ ] **Step 1: Write the failing ScanProgress test**

Create `web/src/components/ScanProgress.test.tsx`:

```tsx
import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { ScanProgress } from "./ScanProgress";
import { setScanRun, __resetScanRun } from "@/hooks/useScanRun";

afterEach(() => __resetScanRun());

describe("ScanProgress", () => {
  it("renders nothing when idle", () => {
    const { container } = render(<ScanProgress />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows a determinate count while running", () => {
    setScanRun({ running: true, done: 4, total: 12 });
    render(<ScanProgress />);
    expect(screen.getByText(/4\s*\/\s*12/)).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "4");
  });
});
```

- [ ] **Step 2: Run to verify fail**

Run: `cd web && npm test -- ScanProgress`
Expected: FAIL (module not found).

- [ ] **Step 3: Implement `ScanProgress`**

Create `web/src/components/ScanProgress.tsx` (plain Tailwind bar, no new dependency):

```tsx
import { useScanRun } from "@/hooks/useScanRun";

// A determinate, self-contained progress indicator for the in-flight scan-run.
// Renders nothing when idle. Shared, server-authoritative state (useScanRun)
// means it reflects a scan started on any page, including after navigation.
export function ScanProgress() {
  const { running, done, total } = useScanRun();
  if (!running) return null;
  const pct = total > 0 ? Math.round((done / total) * 100) : 0;
  return (
    <div className="flex items-center gap-2 text-xs text-muted-foreground">
      <div
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={total}
        aria-valuenow={done}
        aria-label="Checking services"
        className="h-1.5 w-28 overflow-hidden rounded-full bg-muted"
      >
        <div className="h-full bg-primary transition-[width]" style={{ width: `${pct}%` }} />
      </div>
      <span className="tabular-nums">
        Checking {done} / {total}
      </span>
    </div>
  );
}
```

- [ ] **Step 4: Update the buttons**

In `web/src/components/BulkActions.tsx`:

1. Imports:
```ts
import { useApply, useProjectScan, useScanAll } from "@/hooks/mutations";
import { useScanRun } from "@/hooks/useScanRun";
```
2. Rewrite `CheckAllButton` to take `projectId` and disable on any running scan:
```tsx
export function CheckAllButton({
  projectId,
  serviceIds,
  label = "Check all",
  ariaLabel,
}: {
  projectId: number;
  serviceIds: number[];
  label?: string;
  ariaLabel?: string;
}) {
  const scan = useProjectScan();
  const { running } = useScanRun();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={serviceIds.length === 0 || running}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        if (serviceIds.length === 0) return;
        scan.mutate(projectId);
      }}
    >
      <RefreshCw className={running ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
      {label}
    </Button>
  );
}
```
3. Rewrite `ScanAllButton` to disable on any running scan:
```tsx
export function ScanAllButton({ label = "Check all", ariaLabel }: { label?: string; ariaLabel?: string }) {
  const scan = useScanAll();
  const { running } = useScanRun();
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 gap-1 px-2 text-xs"
      disabled={running}
      aria-label={ariaLabel ?? label}
      onClick={(e) => {
        e.stopPropagation();
        scan.mutate();
      }}
    >
      <RefreshCw className={running ? "h-3.5 w-3.5 animate-spin" : "h-3.5 w-3.5"} />
      {label}
    </Button>
  );
}
```

- [ ] **Step 5: Update the row check button (DashboardTable)**

In `web/src/components/DashboardTable.tsx`:

1. Replace the `useCheck` import (L45) with `useServiceCheck`, and add `useScanRun`:
```ts
import { useServiceCheck } from "@/hooks/mutations";
import { useScanRun } from "@/hooks/useScanRun";
```
2. In the row component: replace `const check = useCheck();` (L131) with:
```ts
const check = useServiceCheck();
const { running: scanRunning } = useScanRun();
```
3. Update the button (~L229-236): `disabled={scanRunning}` and `check.mutate(service.id)`:
```tsx
disabled={scanRunning}
// ...
onClick={() => check.mutate(service.id)}
// ...
<RefreshCw className={scanRunning ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
```
4. Fix the `CheckAllButton` usage (~L409) to pass `projectId`. The row has `project` in scope, so add `projectId={project.id}` to the existing element:
```tsx
<CheckAllButton
  projectId={project.id}
  serviceIds={project.services.map((s) => s.id)}
  ariaLabel={`Check all services in ${project.name}`}
/>
```

- [ ] **Step 6: Mount `ScanProgress` + pass projectId in routes**

- `web/src/routes/dashboard.tsx` (near L67, beside `ScanAllButton`):
```tsx
import { ScanProgress } from "@/components/ScanProgress";
// ...
<ScanProgress />
<ScanAllButton ariaLabel="Check all services" />
```
- `web/src/routes/project.$id.tsx` (near L83): import + render `<ScanProgress />` in the `Filters` `actions`, and add `projectId={project.id}` to the existing `CheckAllButton`:
```tsx
<CheckAllButton
  projectId={project.id}
  serviceIds={project.services.map((s) => s.id)}
  ariaLabel={`Check all services in "${project.name}"`}
/>
```

- [ ] **Step 7: Run tests + typecheck + build**

Run: `cd web && npm test && npm run typecheck && npm run build`
Expected: PASS. Update existing tests that referenced `useCheck`/`useCheckAll` or asserted per-row check spinners / toast text (grep `Check complete`, `useCheck`, `Checked ` in `web/src`). Route tests (`dashboard.test.tsx`, `project.$id.test.tsx`) may need the `GET /api/scan` MSW handler (added in Task 6) and adjusted expectations for disabled buttons.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/ScanProgress.tsx web/src/components/ScanProgress.test.tsx web/src/components/BulkActions.tsx web/src/components/DashboardTable.tsx web/src/routes/dashboard.tsx web/src/routes/project.$id.tsx
git commit -m "feat(web): determinate scan progress bar + disable check buttons while running"
```

---

## Final verification

- [ ] `CGO_ENABLED=0 go build ./...` — clean (invariant #1).
- [ ] `CGO_ENABLED=0 go test ./...` — green.
- [ ] `cd web && npm run typecheck` — no errors (NOT `npx tsc`).
- [ ] `cd web && npm test` — green.
- [ ] `cd web && npm run build` — succeeds (type backstop + embeds `dist/`).
- [ ] Manual smoke (`mise run run`): trigger global "Check all" → bar advances `n / N`, all check buttons disabled; navigate away and back mid-scan → bar + disabled state persist (resync); finish → buttons re-enable, "Last scan" tile updates. Scoped project "Check all" → single `POST /api/scan {project_id}`, no `last_check_all` bump.

## Notes for the implementer

- Any-scan-disables-all is intentional: while a scan-run is in flight, every check trigger (global, project, per-row) disables, matching the user's request. Single-service checks (total 1) resolve near-instantly.
- The progress bar is one authoritative `ScanProgress` per header, not per-button; buttons only reflect disabled + spinner. This avoids threading "which button started it" through shared state.
- Do not add restart-survival, a Jobs-list entry, or cancellation (explicitly out of scope in the spec).
