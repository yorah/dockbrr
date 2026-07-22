# Scan abort + scheduled-through-runner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user abort an in-flight "Check all", and route scheduled scans through the same `ScanRunner` so they show progress + disable the check buttons; then delete the now-dead detect cache.

**Architecture:** The manual "Check all" already runs through `httpapi.ScanRunner` (in-memory single-flight scan-run, SSE progress, `useScanRun` button-disable). Piece 1 adds an abort (a stored `context.CancelFunc` + `DELETE /api/scan`) and a synchronous `RunScheduled` entry point the scheduler calls instead of `scanner.CheckAll`, which also makes scheduled scans fresh. Piece 2 removes the digest-only detect cache, which becomes dead code once the scheduler is fresh.

**Tech Stack:** Go 1.26 (CGO-free), chi router, SQLite via `internal/store`; React + TS + Vite + TanStack Query, vitest + MSW.

## Global Constraints

- `CGO_ENABLED=0` must stay: no cgo-requiring imports (static-binary invariant #1).
- Detection is read-only; the scan-run must never go through the Job Engine (invariant #2). Abort cancels a read-only sweep, nothing to roll back.
- Frontend mutations use `apiFetch` (sets `X-CSRF-Token` on non-GET, `credentials: include`); GET stays CSRF-free (invariant #7).
- One scan-run at a time (single-flight). Manual-vs-scheduled is **blocked, not preempting**: whoever holds the guard wins, the other is skipped/`409`.
- Verify TS with `cd web && npm run typecheck` (NOT `npx tsc`). Backend check: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`.
- No em-dashes in text/comments. No Claude attribution in commits.

---

# PIECE 1: abort + scheduled-through-runner

## Task 1: `scan.CheckServicesFresh` honors context cancellation

**Files:**
- Modify: `internal/scan/scan.go:308-330` (the `CheckServicesFresh` loop)
- Test: `internal/scan/scan_test.go` (new test, reuse `newScannerWithServices` helper at `:62`)

**Interfaces:**
- Consumes: `func newScannerWithServices(t *testing.T, n int) (*scan.Scanner, []int64)` (existing helper).
- Produces: unchanged signature `CheckServicesFresh(ctx context.Context, ids []int64, reopen bool, onDone func(done, total int)) error`; new behavior is that it stops early when `ctx` is cancelled.

- [ ] **Step 1: Write the failing test**

Add to `internal/scan/scan_test.go`:

```go
func TestCheckServicesFreshStopsOnCancelledContext(t *testing.T) {
	sc, svcIDs := newScannerWithServices(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before the sweep starts

	var calls int
	if err := sc.CheckServicesFresh(ctx, svcIDs, false, func(done, total int) {
		calls++
	}); err != nil {
		t.Fatalf("CheckServicesFresh: %v", err)
	}
	if calls != 0 {
		t.Fatalf("onDone called %d time(s), want 0 (cancelled ctx must stop the sweep before any service)", calls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -run TestCheckServicesFreshStopsOnCancelledContext -v`
Expected: FAIL (onDone fires 3 times; the loop ignores ctx today).

- [ ] **Step 3: Add the cancellation check**

In `internal/scan/scan.go`, at the very top of the `for i, id := range ids {` loop inside `CheckServicesFresh`, add:

```go
	for i, id := range ids {
		if ctx.Err() != nil {
			break // aborted or timed out: stop the sweep, keep partial results
		}
		if reopen {
```

(Leave the rest of the loop body unchanged.)

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/scan/ -run TestCheckServicesFresh -v`
Expected: PASS (both the new test and the existing `TestCheckServicesFreshReportsProgressPerService`).

- [ ] **Step 5: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go
git commit -m "feat(scan): stop CheckServicesFresh sweep on context cancellation"
```

---

## Task 2: `ScanRunner` gains abort + synchronous scheduled entry point

**Files:**
- Modify: `internal/httpapi/scanrun.go` (whole run core)
- Test: `internal/httpapi/scanrun_test.go` (add tests + one test-only checker)

**Interfaces:**
- Consumes: `Checker.CheckServicesFresh` (existing), `Bus`, `*store.Services`, `*store.Settings`.
- Produces:
  - `func (sr *ScanRunner) RunScheduled(ctx context.Context) bool` — runs an all-scope sweep synchronously; returns `false` (without running) if a scan is already in flight.
  - `func (sr *ScanRunner) Abort()` — cancels the in-flight run; no-op when idle.
  - `Start` keeps its signature `Start(scope string, projectID, serviceID int64) (scanState, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/httpapi/scanrun_test.go`. First a checker that blocks until the test releases it OR its context is cancelled, reporting which happened:

```go
// abortableChecker blocks in CheckServicesFresh until either release is closed
// or ctx is cancelled. cancelled records that the context path won (i.e. Abort
// reached the sweep).
type abortableChecker struct {
	started   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func newAbortableChecker() *abortableChecker {
	return &abortableChecker{
		started:   make(chan struct{}),
		release:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (a *abortableChecker) CheckServiceFresh(context.Context, int64) error { return nil }
func (a *abortableChecker) CheckAllFresh(context.Context) error            { return nil }
func (a *abortableChecker) CheckServicesFresh(ctx context.Context, _ []int64, _ bool, _ func(done, total int)) error {
	close(a.started)
	select {
	case <-a.release:
	case <-ctx.Done():
		close(a.cancelled)
	}
	return nil
}

func TestScanRunnerAbortCancelsRunAndSkipsStamp(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	bus := NewBus()
	ch, cancelSub := bus.Subscribe()
	defer cancelSub()
	settings := storeSettings(db)
	ac := newAbortableChecker()
	sr := NewScanRunner(ac, storeServices(db), settings, bus)

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-ac.started
	sr.Abort()

	select {
	case <-ac.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("Abort did not cancel the sweep context")
	}

	// Run should wind down: not running, scan_finished published, NO scanned,
	// last_check_all left blank.
	types := drainEventTypes(t, ch, 300*time.Millisecond)
	if !contains(types, "scan_finished") {
		t.Fatalf("want scan_finished after abort, got %v", types)
	}
	if contains(types, "scanned") {
		t.Fatalf("aborted run must not publish scanned, got %v", types)
	}
	if v, _ := settings.Get("last_check_all"); v != "" {
		t.Fatalf("aborted run must not stamp last_check_all, got %q", v)
	}
	if sr.Snapshot().Running {
		t.Fatal("runner still marked running after abort")
	}
}

func TestScanRunnerAbortIdleIsNoOp(t *testing.T) {
	db, _, _ := seedProjectServices(t, 1)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), storeSettings(db), NewBus())
	sr.Abort() // must not panic
}

func TestRunScheduledRunsSynchronouslyAndStamps(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	settings := storeSettings(db)
	sr := NewScanRunner(&fakeChecker{}, storeServices(db), settings, NewBus())

	if ran := sr.RunScheduled(context.Background()); !ran {
		t.Fatal("RunScheduled returned false, want true")
	}
	// Synchronous: by return, the run is done and last_check_all is stamped.
	if sr.Snapshot().Running {
		t.Fatal("RunScheduled returned while still running (must be synchronous)")
	}
	if v, _ := settings.Get("last_check_all"); v == "" {
		t.Fatal("RunScheduled did not stamp last_check_all")
	}
}

func TestRunScheduledSkipsWhenBusy(t *testing.T) {
	db, _, _ := seedProjectServices(t, 2)
	bc := &blockingChecker{release: make(chan struct{}), started: make(chan struct{})}
	sr := NewScanRunner(bc, storeServices(db), storeSettings(db), NewBus())

	if _, err := sr.Start("all", 0, 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-bc.started
	if ran := sr.RunScheduled(context.Background()); ran {
		t.Fatal("RunScheduled ran while a manual scan held the guard, want skip")
	}
	close(bc.release)
	deadline := time.Now().Add(2 * time.Second)
	for sr.Snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run 'ScanRunnerAbort|RunScheduled' -v`
Expected: FAIL to compile (`RunScheduled` / `Abort` undefined).

- [ ] **Step 3: Rewrite the run core in `scanrun.go`**

Add a `cancel` field to the struct:

```go
type ScanRunner struct {
	checker  Checker
	services *store.Services
	settings *store.Settings
	bus      *Bus

	mu     sync.Mutex
	state  scanState
	cancel context.CancelFunc // non-nil while a run is in flight; Abort() calls it
}
```

Replace `Start` and `run` (currently `scanrun.go:44-123`) with:

```go
// tryBegin flips the runner to running with the given total under the guard.
// Returns false if a run is already in flight (single-flight).
func (sr *ScanRunner) tryBegin(total int) bool {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	if sr.state.Running {
		return false
	}
	sr.state = scanState{Running: true, Done: 0, Total: total}
	return true
}

// Start begins an asynchronous scan-run over scope ("all" | "project" |
// "service"). Returns the started snapshot, or ErrScanBusy if one is already
// running. The run executes on a process-lifetime context so it survives the
// originating HTTP request.
func (sr *ScanRunner) Start(scope string, projectID, serviceID int64) (scanState, error) {
	ids, err := sr.resolve(scope, projectID, serviceID)
	if err != nil {
		return scanState{}, err
	}
	if !sr.tryBegin(len(ids)) {
		return sr.Snapshot(), ErrScanBusy
	}
	st := sr.Snapshot()
	go sr.execute(context.Background(), scope, ids)
	return st, nil
}

// RunScheduled runs an all-scope sweep synchronously (the scheduler's path):
// it returns only when the sweep completes, so the caller can auto-apply after.
// Returns false without running if a scan is already in flight (blocked, not
// preempting). The passed ctx is the scheduler's context, so a shutdown cancels
// the sweep too.
func (sr *ScanRunner) RunScheduled(ctx context.Context) bool {
	ids, err := sr.resolve("all", 0, 0)
	if err != nil {
		logger.Errorf("scan: scheduled resolve: %v", err)
		return false
	}
	if !sr.tryBegin(len(ids)) {
		logger.Infof("scan: scheduled tick skipped, a scan is already running")
		return false
	}
	sr.execute(ctx, "all", ids)
	return true
}

// Abort cancels the in-flight scan-run, if any. Idempotent: a no-op when idle.
func (sr *ScanRunner) Abort() {
	sr.mu.Lock()
	c := sr.cancel
	sr.mu.Unlock()
	if c != nil {
		c()
	}
}

// execute drives one sweep (shared by Start's goroutine and RunScheduled's
// inline call). It stores its cancel for Abort, reports progress, and on a
// COMPLETED all-scope sweep stamps last_check_all + publishes "scanned". An
// aborted run (ctx cancelled) leaves the "Last scan" tile untouched. It always
// publishes "scan_finished" so the UI clears the bar and re-enables buttons.
func (sr *ScanRunner) execute(parent context.Context, scope string, ids []int64) {
	ctx, cancel := context.WithTimeout(parent, scanRunTimeout)
	sr.mu.Lock()
	sr.cancel = cancel
	sr.mu.Unlock()
	defer cancel()

	sr.publish(Event{Type: "scan_progress", Done: 0, Total: len(ids)})

	// Scoped (service/project) runs lift the rolled_back suppression; an
	// all-services sweep must never reopen (see the original comment).
	reopen := scope != "all" && scope != ""
	_ = sr.checker.CheckServicesFresh(ctx, ids, reopen, func(done, total int) {
		sr.mu.Lock()
		sr.state.Done = done
		sr.mu.Unlock()
		sr.publish(Event{Type: "scan_progress", Done: done, Total: total})
	})

	if ctx.Err() == nil && (scope == "all" || scope == "") {
		now := time.Now().UTC().Format(time.RFC3339)
		if err := sr.settings.Set("last_check_all", now); err != nil {
			logger.Errorf("scan: record last_check_all: %v", err)
		}
		sr.publish(Event{Type: "scanned"})
	}

	sr.mu.Lock()
	sr.cancel = nil
	sr.state = scanState{Running: false}
	sr.mu.Unlock()
	sr.publish(Event{Type: "scan_finished"})
}
```

Keep `Snapshot`, `resolve`, `publish`, `idsOf` unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run 'ScanRunner|RunScheduled' -v`
Expected: PASS (new tests + the existing single-flight / stamp tests).

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/scanrun.go internal/httpapi/scanrun_test.go
git commit -m "feat(scan): add ScanRunner abort + synchronous RunScheduled"
```

---

## Task 3: `DELETE /api/scan` abort endpoint

**Files:**
- Modify: `internal/httpapi/scan.go` (add `handleScanAbort`)
- Modify: `internal/httpapi/server.go:184-185` (register route)
- Test: `internal/httpapi/scan_test.go`

**Interfaces:**
- Consumes: `ScanRunner.Abort()` (Task 2), `s.scan` field.
- Produces: `DELETE /api/scan` -> `204 No Content` (idempotent).

- [ ] **Step 1: Write the failing test**

Add to `internal/httpapi/scan_test.go` (mirror the auth/CSRF setup other handler tests in this file use; the assertion is the status code):

```go
func TestScanAbortReturns204WhenIdle(t *testing.T) {
	srv, tok, csrf := newTestServerWithAuth(t) // existing helper used by other scan_test cases
	req := httptest.NewRequest(http.MethodDelete, "/api/scan", nil)
	authAndCSRF(req, tok, csrf) // existing helper: sets cookie + X-CSRF-Token
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/scan (idle) = %d, want 204", rec.Code)
	}
}
```

Note: if the exact helper names differ, copy the request-construction pattern from an existing test in `scan_test.go` (e.g. `TestScanAllStartsAndReports202`). The behavior asserted is `204`.

- [ ] **Step 2: Run test to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run TestScanAbort -v`
Expected: FAIL (route not registered -> SPA fallback / 404 or 405).

- [ ] **Step 3: Add handler + route**

In `internal/httpapi/scan.go`, add:

```go
// handleScanAbort cancels the in-flight scan-run. Idempotent: 204 whether or
// not a scan was running. Read-only in effect (detection never mutates Docker),
// but a non-GET call so it carries the CSRF header like other mutations.
func (s *Server) handleScanAbort(w http.ResponseWriter, r *http.Request) {
	s.scan.Abort()
	w.WriteHeader(http.StatusNoContent)
}
```

In `internal/httpapi/server.go`, in the authenticated group next to the existing scan routes (`:184-185`), add:

```go
		r.Post("/api/scan", s.handleScanAll)
		r.Get("/api/scan", s.handleScanStatus)
		r.Delete("/api/scan", s.handleScanAbort)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run TestScanAbort -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/scan.go internal/httpapi/server.go internal/httpapi/scan_test.go
git commit -m "feat(api): DELETE /api/scan aborts the in-flight scan-run"
```

---

## Task 4: Scheduler runs through the `ScanRunner`

**Files:**
- Modify: `internal/httpapi/server.go` (expose the ScanRunner)
- Modify: `cmd/dockbrr/main.go` (reorder server construction before the scheduler goroutine; change `schedulerLoop` + `runCheck`)

**Interfaces:**
- Consumes: `ScanRunner.RunScheduled(ctx) bool` (Task 2).
- Produces: `func (s *Server) ScanRunner() *ScanRunner`.

Note: there is no `schedulerLoop` unit test in the repo; this task is verified by `go build` + `go vet` + the RunScheduled unit tests from Task 2 (which cover the behavior), plus the whole-suite run in Step 4.

- [ ] **Step 1: Expose the ScanRunner accessor**

In `internal/httpapi/server.go`, after `Handler()` (~`:126`), add:

```go
// ScanRunner exposes the process-wide scan-run so the scheduler can drive
// periodic sweeps through the same single-flight runner the API uses (shared
// progress, button-disable, and abort).
func (s *Server) ScanRunner() *ScanRunner { return s.scan }
```

- [ ] **Step 2: Reorder main.go so the server exists before the scheduler**

In `cmd/dockbrr/main.go`, the scheduler goroutine currently launches at `:331-335`, but `srv := httpapi.New(...)` is built later at `:384`. Move the `deps := httpapi.Deps{...}` block (`:352-383`) and `srv := httpapi.New(cfg, db, deps)` (`:384`) to **above** the scheduler goroutine (before `:331 wg.Add(1)`). The `httpServer := &http.Server{...}` block and everything after it stays where it is (it already references `srv`).

Verify no variable used by the `deps` block is defined between the old and new positions (it references `sealer`, `users`, ..., `selfUpdateChecker`, `dockerProbe`, `nextScan` — all defined earlier than `:331`). If `go build` reports an undefined symbol, that variable's definition must also move up; otherwise no other change is needed.

- [ ] **Step 3: Change `schedulerLoop` to take the ScanRunner**

Change the signature (`main.go:541-543`) from taking `scanner *scan.Scanner` + `bus *httpapi.Bus` to taking the runner. New signature:

```go
func schedulerLoop(ctx context.Context, settings *store.Settings, scanRunner *httpapi.ScanRunner,
	services *store.Services, projects *store.Projects, updates *store.Updates, engine *job.Engine,
	nextScan *atomic.Int64, discoveryReady <-chan struct{}) {
```

Update the boot-scan and tick blocks to gate `autoApply` on whether the sweep actually ran:

```go
	if settings.GetBoolDefault("scan_on_start", true) {
		waitForDiscovery(ctx, discoveryReady, discoveryReadyTimeout)
		logger.Infof("scheduler: running startup check (scan_on_start)")
		if runCheck(ctx, scanRunner) {
			autoApply(services, projects, updates, engine)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Infof("scheduler: running scheduled check")
			if runCheck(ctx, scanRunner) {
				autoApply(services, projects, updates, engine)
			}
			if next := settingDuration(settings, "poll_interval_seconds", 15*time.Minute); next != interval {
				interval = next
				ticker.Reset(interval)
				logger.Infof("scheduler: poll interval now %s", interval)
			}
			nextScan.Store(time.Now().Add(interval).Unix())
		}
	}
```

- [ ] **Step 4: Replace `runCheck`**

Replace `runCheck` (`main.go:604-614`) with a thin wrapper that delegates to the runner (which now owns the `last_check_all` stamp + `scanned` publish for the all scope):

```go
// runCheck runs one scheduled read-only sweep through the shared ScanRunner
// (fresh detection, progress + abort + button-disable for free). Returns
// whether the sweep ran; a false means a manual scan held the single-flight
// guard, so the caller skips this tick's auto-apply. The runner stamps
// last_check_all and publishes "scanned" on completion, so runCheck no longer
// does either.
func runCheck(ctx context.Context, scanRunner *httpapi.ScanRunner) bool {
	return scanRunner.RunScheduled(ctx)
}
```

- [ ] **Step 5: Update the scheduler goroutine call site**

At the (now relocated) scheduler goroutine, pass `srv.ScanRunner()` and drop `scanner` + `bus`:

```go
	go func() {
		defer wg.Done()
		schedulerLoop(ctx, settings, srv.ScanRunner(), services, projects, updates, engine, &nextScan, discoveryReady)
	}()
```

If `scanner` or `bus` become unused in `main()` after this, remove the now-dead locals only if the compiler flags them (they are still used elsewhere: `bus` by Deps, `scanner` by `deps.Checker`, so both stay).

- [ ] **Step 6: Build, vet, and run the suite**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && CGO_ENABLED=0 go test ./...`
Expected: PASS. (`main.go` compiles with the reordered construction; RunScheduled behavior is covered by Task 2's tests.)

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/server.go cmd/dockbrr/main.go
git commit -m "feat(scheduler): run periodic sweeps through the shared ScanRunner"
```

---

## Task 5: Cancel button in the scan progress UI

**Files:**
- Modify: `web/src/hooks/mutations.ts` (add `useScanAbort`)
- Modify: `web/src/components/ScanProgress.tsx` (add the Cancel button)
- Test: `web/src/components/ScanProgress.test.tsx`
- Reference (no change needed): `web/src/test/msw.ts` (already has `GET /api/scan`; add a `DELETE` handler for the test)

**Interfaces:**
- Consumes: `apiFetch("/api/scan", { method: "DELETE" })` -> `204`; `useScanRun()` store (`setScanRun`, `__resetScanRun`).
- Produces: `useScanAbort()` mutation hook; a Cancel button rendered by `ScanProgress` while `running`.

- [ ] **Step 1: Write the failing test**

In `web/src/components/ScanProgress.test.tsx`, add a case (the file already imports `setScanRun`, `__resetScanRun`):

```tsx
import { server } from "@/test/msw";
import { http, HttpResponse } from "msw";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
// ...existing imports (ScanProgress, setScanRun, __resetScanRun, wrapper/QueryClient as used by other cases)...

test("Cancel button aborts the scan via DELETE /api/scan and disables itself", async () => {
  let deleted = false;
  server.use(
    http.delete("/api/scan", () => {
      deleted = true;
      return new HttpResponse(null, { status: 204 });
    }),
  );
  setScanRun({ running: true, done: 2, total: 10 });
  render(<ScanProgress />, { wrapper: makeWrapper() }); // makeWrapper mirrors the QueryClientProvider other tests in this file use

  const cancel = screen.getByRole("button", { name: /cancel/i });
  fireEvent.click(cancel);

  await waitFor(() => expect(deleted).toBe(true));
  expect(cancel).toBeDisabled();
});
```

If `ScanProgress.test.tsx` currently renders without a QueryClient wrapper, add one (the mutation needs a `QueryClientProvider`); copy the wrapper pattern from `DashboardTable.test.tsx`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && npm test -- ScanProgress`
Expected: FAIL (no Cancel button).

- [ ] **Step 3: Add the `useScanAbort` mutation**

In `web/src/hooks/mutations.ts`, next to `useScanAll` (`:82`), add:

```ts
// Abort the in-flight scan-run (DELETE /api/scan -> 204). The scan-run store
// clears from the SSE scan_finished the abort triggers, so there is no
// onSuccess state change here.
export function useScanAbort() {
  return useMutation({
    mutationFn: () => apiFetch<void>("/api/scan", { method: "DELETE" }),
    onError: scanError,
  });
}
```

(`scanError` is the shared error handler already used by `useScanAll` in this file.)

- [ ] **Step 4: Add the Cancel button to `ScanProgress`**

Rewrite `web/src/components/ScanProgress.tsx`:

```tsx
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useScanRun } from "@/hooks/useScanRun";
import { useScanAbort } from "@/hooks/mutations";

// A determinate, self-contained progress indicator for the in-flight scan-run.
// Renders nothing when idle. Shared, server-authoritative state (useScanRun)
// means it reflects a scan started on any page (manual OR scheduled), including
// after navigation. The Cancel button aborts the run (DELETE /api/scan); the
// bar then clears on the scan_finished the abort produces.
export function ScanProgress() {
  const { running, done, total } = useScanRun();
  const abort = useScanAbort();
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
      <Button
        size="sm"
        variant="ghost"
        className="h-6 gap-1 px-1.5"
        aria-label="Cancel scan"
        disabled={abort.isPending}
        onClick={() => abort.mutate()}
      >
        <X className="h-3.5 w-3.5" />
        Cancel
      </Button>
    </div>
  );
}
```

The button disables on `abort.isPending` so it cannot be double-fired before `scan_finished` clears the whole bar.

- [ ] **Step 5: Run test + typecheck**

Run: `cd web && npm test -- ScanProgress && npm run typecheck`
Expected: PASS + no type errors.

- [ ] **Step 6: Commit**

```bash
git add web/src/hooks/mutations.ts web/src/components/ScanProgress.tsx web/src/components/ScanProgress.test.tsx
git commit -m "feat(web): cancel button aborts the in-flight scan-run"
```

---

# PIECE 2: remove the detect cache

> Staged after Piece 1, which makes the scheduler fresh, leaving no non-fresh caller. These tasks delete dead code + the now-useless setting. No further detection-behavior change beyond "every scan full-resolves" (which Piece 1 already established for scheduled scans).

## Task 6: Remove the digest-only cache path in `detect`

**Files:**
- Modify: `internal/detect/detect.go` (remove the digest-only branch, `freshCachedDigest`, the `cacheTTL` field + constructor param)
- Modify: `internal/detect/detect_test.go` (remove cache-specific tests)
- Modify: `cmd/dockbrr/main.go:174-175` (drop the `cacheTTL` closure + arg)

**Interfaces:**
- Produces: `NewDetector(resolver, updates, images, states, events, tagCache, plat)` (drops the trailing `cacheTTL func() time.Duration` param).

- [ ] **Step 1: Remove the digest-only branch in `Detect`**

In `internal/detect/detect.go`, delete the block at `:78-94` (the `// 1. Try a fresh cache hit ...` comment through the `return d.record(svc, tag, remoteDigest, "", "", "digest-only")` line). Detection now always falls through to `// 2. Resolve from the network.`

Behavior note for the reviewer: the deleted branch called `closeReachedUpdates` on a same-digest cache hit and deliberately avoided supersede-all to prevent flapping "on every cache-window re-check". With no cache there are no cache-window re-checks; the up-to-date case on the full-resolve path (`closeStaleUpdates`, ~`:177`) is now the only path and is correct.

- [ ] **Step 2: Remove `freshCachedDigest` + the `cacheTTL` field/param**

- Delete `freshCachedDigest` (`:452-465`).
- In the `Detector` struct, remove the `cacheTTL func() time.Duration` field (`:33`).
- In `NewDetector`, remove the `cacheTTL func() time.Duration` parameter (`:47`), the `if cacheTTL == nil { ... }` default (`:49-51`), and the `cacheTTL: cacheTTL` struct field (`:55`).

- [ ] **Step 3: Update the constructor call in main.go**

In `cmd/dockbrr/main.go`, delete the `cacheTTL := func() ...` line (`:174`) and drop the trailing arg:

```go
	detector := detect.NewDetector(resolver, updates, images, states, events, tagCache, plat)
```

- [ ] **Step 4: Remove cache-specific detect tests**

In `internal/detect/detect_test.go`, remove `TestDetectCacheTTLReadPerCall` (`:597`) and any other test that asserts the digest-only short-circuit (search the file for `digest-only`, `cacheTTL`, `freshCachedDigest`, and `ResolvedAt`-based reuse; delete tests whose whole point is the cache hit). Tests that pass a `cacheTTL` arg to `NewDetector` must drop that argument.

- [ ] **Step 5: Build + test**

Run: `CGO_ENABLED=0 go build ./... && CGO_ENABLED=0 go test ./internal/detect/ ./cmd/... -v`
Expected: PASS (compiles without `cacheTTL`; remaining detect tests green).

- [ ] **Step 6: Commit**

```bash
git add internal/detect/detect.go internal/detect/detect_test.go cmd/dockbrr/main.go
git commit -m "refactor(detect): remove digest-only cache short-circuit"
```

---

## Task 7: Remove the `cache_ttl_seconds` setting

**Files:**
- Modify: `internal/httpapi/settings.go:26,46` (default + whitelist)
- Modify: `internal/httpapi/settings_test.go` (drop cache_ttl assertions)
- Modify: `web/src/api/types.ts:50` (drop field)
- Modify: `web/src/components/settings/ScanningSettings.tsx` (drop the field + KEYS entry)
- Modify: web test fixtures that include `cache_ttl_seconds`

**Interfaces:** none (removing a setting key).

- [ ] **Step 1: Remove from backend settings**

In `internal/httpapi/settings.go`:
- Delete `"cache_ttl_seconds": "600",` from `settingDefaults` (`:26`).
- Delete `{"cache_ttl_seconds", false},` from `settingKeys` (`:46`).

The comment above `settingDefaults` warns it is guarded by `TestSettingDefaultsMatchConsumers`; since Task 6 already removed the only consumer (the `cacheTTL` closure in main.go), that test stays green. Confirm in Step 4.

- [ ] **Step 2: Remove backend test references**

In `internal/httpapi/settings_test.go`, remove the `cache_ttl_seconds` entries at `:41,55-56,491,557` (the round-trip assertion and the two full-map fixtures). Where a map literal listed it, delete just that key line.

- [ ] **Step 3: Remove from the frontend**

- `web/src/api/types.ts`: delete `cache_ttl_seconds: string;` (`:50`).
- `web/src/components/settings/ScanningSettings.tsx`: remove `"cache_ttl_seconds"` from `KEYS` (`:9`) and delete the `cache_ttl_seconds` object from `NUMBER_FIELDS` (`:22-26`).
- Remove `cache_ttl_seconds` from these test fixtures: `web/src/routes/settings.test.tsx:32`, `web/src/components/settings/UpdatesSettings.test.tsx:15`, `web/src/components/settings/RegistriesSettings.test.tsx:15`, `web/src/components/settings/ScanningSettings.test.tsx:20` (and its comment at `:29`), `web/src/hooks/useSettingsForm.test.tsx:16`.

- [ ] **Step 4: Build + full test**

Run: `CGO_ENABLED=0 go test ./internal/httpapi/ -run TestSetting -v && cd web && npm run typecheck && npm test -- settings Scanning`
Expected: PASS (`TestSettingDefaultsMatchConsumers` green; no TS errors; settings tests pass without the field).

- [ ] **Step 5: Commit**

```bash
git add internal/httpapi/settings.go internal/httpapi/settings_test.go web/src/api/types.ts web/src/components/settings/ScanningSettings.tsx web/src/routes/settings.test.tsx web/src/components/settings/UpdatesSettings.test.tsx web/src/components/settings/RegistriesSettings.test.tsx web/src/components/settings/ScanningSettings.test.tsx web/src/hooks/useSettingsForm.test.tsx
git commit -m "refactor(settings): drop the now-unused cache_ttl_seconds setting"
```

---

## Task 8: Collapse the dead scan/cache API surface

**Files:**
- Modify: `internal/scan/scan.go` (remove `CheckAll`, `checkAll`, `CheckAllFresh`, `invalidateFor`, the `stateInvalidator` seam)
- Modify: `internal/scan/scan_test.go` (remove `CheckAll*` tests + `spyInvalidator`)
- Modify: `internal/discovery/discovery.go:333` (remove the `Invalidate` call)
- Modify: `internal/httpapi/server.go:34-42` (trim the `Checker` interface)
- Modify: `internal/httpapi/scanrun_test.go:76-77`, `internal/httpapi/actions_test.go:47-58` (drop fake methods no longer in the interface)

**Interfaces:**
- Produces: `Checker` interface reduced to `CheckServicesFresh(ctx, ids, reopen, onDone) error` only.
- `scan.Scanner.CheckServiceFresh` and `CheckServicesFresh` remain (used by the runner + the reopen path).

Ordering caveat: removing `stateInvalidator` / `Invalidate` requires `NewDetector` to already not depend on the cache (Task 6). Do Task 8 after Tasks 6-7.

- [ ] **Step 1: Verify remaining callers before deleting**

Run: `grep -rn "CheckAll\b\|CheckAllFresh\|invalidateFor\|stateInvalidator\|\.Invalidate(" internal cmd | grep -v _test | grep -v dist`
Expected: only definitions in `scan.go`, the `Checker` interface in `server.go`, and `discovery.go:333`. If any non-test production caller of `CheckAll`/`CheckAllFresh` remains, STOP and reconcile (the scheduler switch in Task 4 should have removed the last one).

- [ ] **Step 2: Trim `scan.go`**

- Delete `CheckAll` (`:270-275`), `CheckAllFresh` (`:277-293`), and the `checkAll` helper (`:332-348`).
- Delete `invalidateFor` (`:96-105`) and its call inside `CheckServicesFresh` (the `if s.states != nil { s.invalidateFor(svc) }` branch, ~`:318-320`). With no cache, the non-reopen branch is now just `services.Get` + `CheckService`.
- Delete the `stateInvalidator` interface (`:31-37`), the `states` field on `Scanner` (`:48`), and the `states` parameter of `New` (`:66`). Update `New` to drop `states`.
- `CheckServiceFresh` (`:75-94`): remove the `if s.states != nil { ... invalidateFor }` block; keep the `ReopenRolledBack` logic (that is the manual "look again" gesture, unrelated to the cache).

- [ ] **Step 3: Update `scan.New` call in main.go**

`cmd/dockbrr/main.go:196` calls `scan.New(detector, clResolver, services, updates, images, states, ...)`. Remove the `states` argument. (`states` is still used by `NewDetector` and `httpapi.Deps.RemoteStates`, so the local stays; only the `scan.New` arg goes.)

- [ ] **Step 4: Remove the discovery Invalidate call**

In `internal/discovery/discovery.go`, delete the line at `:333`:
`_ = r.states.Invalidate(repo, tag) // best-effort: never fail the reconcile`
If `r.states` becomes unused in discovery after this, remove the field + its constructor wiring; if it is still used elsewhere, leave it. Run `grep -n "states" internal/discovery/discovery.go` to decide.

- [ ] **Step 5: Trim the `Checker` interface + fakes**

In `internal/httpapi/server.go`, reduce the `Checker` interface (`:34-42`) to:

```go
type Checker interface {
	// CheckServicesFresh checks each id fresh, reporting progress via onDone.
	// reopen lifts the rolled_back auto-apply suppression per service (the
	// manual "look again" gesture) and must only be true for scoped
	// (service/project) runs, never for an all-services sweep.
	CheckServicesFresh(ctx context.Context, ids []int64, reopen bool, onDone func(done, total int)) error
}
```

Remove the now-unused fake methods:
- `internal/httpapi/scanrun_test.go:76-77` (`blockingChecker.CheckServiceFresh`, `.CheckAllFresh`) and the same two on `abortableChecker` (added in Task 2).
- `internal/httpapi/actions_test.go:47-58` (`fakeChecker.CheckServiceFresh`, `.CheckAllFresh`).

(`*scan.Scanner` still satisfies the trimmed interface via `CheckServicesFresh`.)

- [ ] **Step 6: Remove dead scan tests**

In `internal/scan/scan_test.go`, delete `TestCheckAllSweepsAllServices` (`:542`), `TestCheckAllFreshInvalidatesEveryService` (`:554`), `TestCheckAllKeepsCache` (`:569`), and the `spyInvalidator` type. Update every remaining `scan.New(...)` call in the file to drop the `states`/invalidator argument (they currently pass `nil` or `spy`).

- [ ] **Step 7: Build, vet, full suite**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && CGO_ENABLED=0 go test ./... && cd web && npm run typecheck && npm test`
Expected: PASS across Go + web.

- [ ] **Step 8: Commit**

```bash
git add internal/scan/scan.go internal/scan/scan_test.go internal/discovery/discovery.go internal/httpapi/server.go internal/httpapi/scanrun_test.go internal/httpapi/actions_test.go cmd/dockbrr/main.go
git commit -m "refactor(scan): collapse dead cache-keeping check API after cache removal"
```

---

## Final verification

- [ ] **Full check via mise task**

Run: `mise run check`
Expected: `go vet` + `go test` + web vitest all green.

- [ ] **Manual smoke (optional, needs Docker)**

Run: `mise run run`, open the dashboard, click "Check all", confirm the progress bar + Cancel appear; click Cancel and confirm the bar clears and buttons re-enable. Let a scheduled scan fire (or shorten `poll_interval`) and confirm it drives the same progress bar and disables the buttons.

---

## Self-Review notes (addressed)

- **Spec coverage:** Abort (Tasks 2,3,5), scheduled-through-runner + fresh (Tasks 2,4), progress/button-disable for scheduled (Task 4 wiring + existing `useScanRun`), cache removal (Tasks 6,7,8), `image_remote_state` preserved (Tasks 6/8 remove only the TTL read path, not the store writes/reads), `reopen` flag preserved (Task 8 Step 2/5). All spec sections map to a task.
- **Type consistency:** `RunScheduled(ctx) bool`, `Abort()`, `ScanRunner()` accessor, `execute(parent, scope, ids)`, `NewDetector(...)` minus `cacheTTL`, `scan.New(...)` minus `states`, used consistently across tasks.
- **Ordering:** Piece 2 (Tasks 6-8) strictly follows Piece 1 (Task 4 removes the last non-fresh caller); Task 8 follows Tasks 6-7 (detector no longer needs the invalidator).
