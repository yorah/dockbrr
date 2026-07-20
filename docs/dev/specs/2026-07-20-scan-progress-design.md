# Scan progress + cross-navigation button disabling

**Date:** 2026-07-20
**Status:** Approved, ready for planning

## Problem

Triggering "Check all" gives no visible progress and no durable running state:

1. No progress indicator. The global sweep (`POST /api/scan`) blocks the request
   for up to 60s with only a button spinner. The scoped per-project "Check all"
   fans out N per-service requests with the same opaque spinner.
2. Running state is per-component and dies on navigation. Leaving the page and
   coming back loses any sense that a check is in flight, and the buttons
   re-enable, inviting a duplicate sweep.

Goal: a determinate progress bar ("Checking 4 / 12"), and check buttons that
stay disabled while a check runs, even across page navigation.

## Decisions (from brainstorming)

- **Determinate** progress (done / total), not an indeterminate spinner.
- **Lightweight** server-side scan-run tracked in memory and broadcast over the
  existing SSE bus. It is NOT modeled as a Job Engine job: the Job Engine is the
  mutation-only path (safety invariant #2) with per-project keyed mutex,
  snapshot, and rollback, none of which fit a cross-project, read-only sweep.
  No restart-survival and no Jobs-list entry, both low value for a read-only
  sweep that finishes in seconds.
- **Unified** concept: the global "Check all", the scoped per-project
  "Check all", and an individual per-service check all become the same thing, a
  *scan-run* with a scope. One mechanism, one running state. Any scan-run in
  flight disables every check button.

## Concept: the scan-run

A single global in-flight scan-run. Its scope is one of:

- **all**: every service (the dashboard-wide sweep)
- **project**: the service IDs of one project
- **service**: a single service ID

Single-flight: while a scan-run is active, starting another returns `409` and is
ignored (the buttons are already disabled, so this is a race backstop, not a
normal path).

The scan-run stays read-only and entirely outside the Job Engine. Safety
invariant #2 ("UI/API never touches Docker directly, all mutation goes through
the Job Engine") is unaffected: detection never mutates Docker.

## Backend

### `internal/scan`

Add a scoped, progress-reporting sweep. The package stays pure (no httpapi
import; progress is surfaced via a plain callback, the same seam already used
for `notify`).

```go
// CheckServicesFresh invalidates each service's detect cache and checks it,
// invoking onDone(done, total) after each service completes (whether it
// detected drift, found nothing, or errored; the loop logs per-service
// errors and continues, matching checkAll today). onDone may be nil.
func (s *Scanner) CheckServicesFresh(ctx context.Context, ids []int64, onDone func(done, total int)) error
```

`CheckAllFresh` is reimplemented over (or wraps) `CheckServicesFresh` across all
service IDs. `CheckServiceFresh` remains for the single-service path. Behavior of
the existing methods is unchanged.

### `internal/httpapi/scanrun.go` (new)

A `ScanRunner` owns the in-flight scan-run state and drives it on a background
context so it survives the originating HTTP request (and thus page navigation).

State (mutex-guarded): `{running bool, done int, total int, scope string}`.

Dependencies: the `Checker` (the scan.Scanner behind the existing `Checker`
interface, which is extended with `CheckServicesFresh`), `Bus`, `Settings`,
`Services` (to resolve scope to service IDs and count totals), and a base
context captured at construction (the server lifetime context, not a request
context).

- `Start(scope) (snapshot, error)`:
  - If already running, return `ErrScanBusy` (handler maps to `409`).
  - Resolve scope to a concrete `[]int64` and `total`:
    - **all**: all service IDs
    - **project**: the project's service IDs
    - **service**: `[id]`, total 1
  - Set `running=true, done=0, total`, publish `scan_progress{done:0,total}`.
  - Spawn a goroutine on `context.WithTimeout(baseCtx, ~5m)` that calls
    `CheckServicesFresh(ctx, ids, onDone)`, where `onDone` updates state and
    publishes `scan_progress{done,total}` after each service.
  - On completion: if scope == **all**, stamp `last_check_all` and publish the
    existing `scanned` event (preserving the dashboard "Last scan" tile
    refresh). Scoped and single runs do NOT stamp `last_check_all`. Then set
    `running=false` and publish `scan_finished`.
- `Snapshot() snapshot`: current `{running, done, total}` for the GET endpoint.

The ~5m timeout is generous versus the prior 60s blocking cap, since a large
sweep now runs unattended in the background rather than under a request
deadline.

### Events

`Event` gains two fields:

```go
type Event struct {
    Type      string `json:"type"` // …|scan_progress|scan_finished
    ServiceID int64  `json:"service_id,omitempty"`
    JobID     int64  `json:"job_id,omitempty"`
    Done      int    `json:"done,omitempty"`
    Total     int    `json:"total,omitempty"`
}
```

Carrying `done`/`total` is a deliberate exception to the "payload-free hint,
refetch the authoritative query" rule: progress is ephemeral and has no
persisted query resource to refetch, and a per-service refetch would be chatty.
Best-effort drops (a full subscriber buffer) are tolerable for a live bar, and
the authoritative `GET /api/scan` snapshot self-heals the count on mount and on
SSE reconnect. `scan_finished` carries no counts; it triggers the normal
refetches.

### Endpoints

- `POST /api/scan`: optional JSON body `{ "project_id": <id> }`; absent means
  scope **all**. Returns `202 { running, total }` on start, `409` if a scan-run
  is already in flight. **Now asynchronous** (previously blocked up to 60s).
  Replaces the scoped per-project fan-out (one request instead of N).
- `POST /api/services/:id/check`: routes through `ScanRunner.Start` with a
  **service** scope for `:id`. Still returns promptly; `409` if busy.
- `GET /api/scan`: authoritative `{ running, done, total }` snapshot (no
  mutation, no CSRF). Read on mount and on SSE (re)connect to seed/resync the
  client store.

CSRF/auth unchanged: the POSTs are mutations (CSRF header via `apiFetch`); the
GET is a plain authenticated read.

## Frontend

### `useScanRun` store

A `useSyncExternalStore`-backed module store mirroring `useBusyServices`,
holding the server-authoritative scan-run: `{ running, done, total }`.

- Seeded by `GET /api/scan` on app mount and on every SSE (re)connect (so a
  page loaded mid-scan, or one whose stream blipped, shows the right state).
- Updated live from `scan_progress` event payloads (set `done`/`total`
  directly, no refetch, so the bar advances smoothly).
- On `scan_finished`: clear `running`, then refetch `updates` / `projects` /
  `status` (the authoritative queries), matching how `job_finished` is handled
  today.

`useEventStream` gains the new cases and an on-open resync (`GET /api/scan`).

### Buttons

`CheckAllButton`, `ScanAllButton`, and the individual per-service check button:

- onClick issues a single POST (start the scan-run). No client-side fan-out.
- `disabled` when `useScanRun().running` (any scan-run disables all three,
  server-authoritative, so it holds across navigation).
- While running, the active trigger shows a determinate bar plus
  `Checking {done} / {total}`.

`useScanAll` / `useCheckAll` / `useCheck` mutations collapse toward the single
`POST /api/scan` (plus `POST /api/services/:id/check` for the row button). The
scoped per-project fan-out is removed.

## Tests

**Go**

- `ScanRunner.Start` single-flight: a second `Start` while running returns
  `ErrScanBusy`; the handler returns `409`.
- `onDone` fires once per service with monotonic `done` up to `total`.
- `last_check_all` is stamped for scope **all** only; scoped and single runs
  leave it untouched.
- `scan_progress` and `scan_finished` events are published (and `scanned` for
  scope all).
- `scan.CheckServicesFresh` iterates the given IDs, invalidates per service,
  continues past a per-service error, and calls `onDone` each time.
- `GET /api/scan` snapshot reflects running vs idle.

**Web**

- `useScanRun`: seeds from the GET snapshot, applies `scan_progress` /
  `scan_finished` events, and resyncs on reconnect.
- Buttons are disabled and show the bar while `running`; re-enable on
  `scan_finished`.
- Scoped "Check all" issues a single `POST /api/scan { project_id }`, not a
  per-service fan-out.

## Out of scope

- Restart-survival of an in-flight scan and any Jobs-list entry for scans
  (explicitly rejected: read-only, seconds-long).
- Cancelling an in-flight scan-run.
- Concurrent scan-runs (single-flight by design).
