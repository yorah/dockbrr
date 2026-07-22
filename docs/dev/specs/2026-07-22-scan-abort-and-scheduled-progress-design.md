# Abort a scan-run + scheduled scans share the scan-run

**Date:** 2026-07-22
**Status:** Approved, ready for planning

## Problem

Two gaps in the current scan-run (built in `2026-07-20-scan-progress`):

1. **No way to abort a running "Check all".** Once started, a sweep runs to
   completion (or the 5m timeout). `2026-07-20-scan-progress` listed cancelling
   as explicitly out of scope; this closes it.
2. **Automated (scheduled) scans are invisible.** The scheduler runs
   `scanner.CheckAll` directly (`cmd/dockbrr/main.go` `runCheck`), bypassing the
   `ScanRunner` entirely. So a scheduled sweep shows no progress bar, does not
   set the shared `running` state, and does not disable the check buttons. A
   user cannot tell a scan is in flight and can start a redundant one.

Goal: abort any in-flight scan-run, and make scheduled scans flow through the
same `ScanRunner` so they get progress, button-disable, and abort for free.

## Decisions (from brainstorming)

- **Manual vs scheduled: blocked, not preempting.** One scan-run at a time,
  first-come. If a scheduled scan is running, the manual "Check all" is disabled
  (busy). If a manual scan is running, the scheduler tick is skipped (logged).
  The existing single-flight guard already enforces this; the scheduler just
  becomes another caller of it.
- **Scheduled scans become fresh.** Routing them through the `ScanRunner` means
  they use the invalidate-then-check path (`CheckServicesFresh`), same as the
  manual button. This is a deliberate behavior change: every scheduled tick now
  full-resolves each service instead of taking the digest-only cache path.
- **Remove the detect cache (Piece 2).** Once the scheduler is the last
  non-fresh caller and it goes fresh, the digest-only TTL short-circuit is dead
  code. Delete it and the `cache_ttl_seconds` setting. This is pure cleanup with
  no further behavior change, staged after Piece 1 in the same branch.
- **Abort keeps partial results.** Detection is read-only and each service
  commits as it is checked, so an aborted sweep simply stops early; nothing to
  roll back. An aborted run does **not** stamp `last_check_all` (the sweep did
  not complete).

## Piece 1: abort + scheduled-through-runner

### `internal/httpapi/scanrun.go`

Refactor the run core so both the async (API) and synchronous (scheduler) entry
points share it, and add abort.

- Extract `execute(parent context.Context, scope string, ids []int64)`: derives
  `ctx, cancel := context.WithTimeout(parent, scanRunTimeout)`, stores `cancel`
  on the `ScanRunner` under `mu` (this is what abort trips), runs the sweep via
  `CheckServicesFresh`, and on exit clears `state` and the stored `cancel`.
- `Start(scope, projectID, serviceID) (scanState, error)` (API path): unchanged
  contract. Acquires the busy-guard (returns `ErrScanBusy` -> `409` if running),
  then `go execute(context.Background(), scope, ids)`. Base context is the
  process, not a request, so the run survives the originating request.
- `RunScheduled(ctx context.Context) (ran bool)` (scheduler path): synchronous.
  Acquires the busy-guard; if already running, returns `false` (tick skipped).
  Otherwise resolves scope **all**, runs `execute(ctx, "all", ids)` inline, and
  returns `true` when the sweep finishes. Synchronous so the caller can run
  `autoApply` strictly after the sweep. Parent ctx is the scheduler's context,
  so a shutdown cancels the sweep too.
- `Abort()`: under `mu`, if a run is active, call the stored `cancel()`. No-op
  when idle (idempotent).

Completion vs abort in `execute` (currently `run`): the sweep loop must honor
cancellation (see `internal/scan` below). After the loop:

- If the run completed (`ctx.Err() == nil`) **and** scope is **all**: stamp
  `last_check_all` and publish `scanned`, as today.
- If aborted (`ctx.Err() != nil`): do **not** stamp `last_check_all`, do **not**
  publish `scanned`.
- Always: set `state` back to idle and publish `scan_finished` so the UI clears
  the bar and re-enables buttons.

### `internal/scan`

`CheckServicesFresh` currently loops over all IDs regardless of context. Make it
honor cancellation so abort stops the sweep promptly:

- At the top of each iteration, `if ctx.Err() != nil { break }`.

This benefits manual runs too (they become abortable by the same mechanism). No
signature change.

### `cmd/dockbrr/main.go`

- `runCheck` stops calling `scanner.CheckAll`. It calls
  `scanRunner.RunScheduled(ctx)` instead, then runs `autoApply` only when the
  sweep actually ran (skipped ticks do nothing, matching today's no-op on an
  empty service set).
- The scheduler needs the `*ScanRunner` handle. It is constructed inside
  `httpapi.New`; expose it (a field or accessor on the server/deps) so
  `main.go` can pass it into `schedulerLoop`.
- `runCheck` no longer stamps `last_check_all` or publishes `scanned` itself:
  `execute` now owns that for scope **all** (both the manual and scheduled
  paths), removing the duplicate stamp.

### API

- `DELETE /api/scan` -> `handleScanAbort` -> `sr.Abort()` -> `204`. Idempotent:
  `204` whether or not a scan was running. It is a mutation-ish action but
  read-only in effect; still requires the CSRF header + auth like other
  non-GET calls.

### Frontend

- `ScanProgress.tsx`: add a Cancel button next to the bar. onClick issues
  `DELETE /api/scan` (via `apiFetch`, so CSRF header + `credentials: include`,
  per safety invariant #7). Disable the button once clicked until the next
  `scan_finished` so it cannot be double-fired.
- Check-all buttons need **no change**: they already disable on
  `useScanRun().running`, and scheduled scans now emit `scan_progress`, so they
  auto-disable during automated scans across every page.
- `useEventStream` / `useScanRun` need no new event types: abort surfaces as the
  existing `scan_finished`.

## Piece 2: remove the detect cache

Staged after Piece 1 (which makes the scheduler fresh, leaving no non-fresh
caller). Pure cleanup, no further behavior change.

- `internal/detect/detect.go`: delete the digest-only branch (the
  `freshCachedDigest` short-circuit), the `freshCachedDigest` helper, and the
  `cacheTTL` constructor parameter. `Detect` always does the full network
  resolve + semver scan.
- Remove the `cache_ttl_seconds` setting: its read/wiring in `main.go`, its
  default, and any settings-UI field/validation.
- `Invalidate` becomes a no-op once the short-circuit is gone. Remove
  `scanner.invalidateFor` and its calls, and `discovery.go`'s `Invalidate`
  call. Drop the `stateInvalidator` seam if nothing else uses it.
- Collapse the redundant scan API: remove `Scanner.CheckAll` / `checkAll` (the
  scheduler was the only caller) and `CheckAllFresh` if no caller remains after
  Piece 1. **Keep** `CheckServicesFresh`'s `reopen` flag: that encodes the
  manual-scoped vs all-sweep rolled-back-suppression semantics, which is
  unrelated to the cache.
- **Preserve** `image_remote_state`: the dashboard and changelog read its
  status / labels / digest without a network call. Only the TTL *read*
  short-circuit is removed; the `ResolvedAt` column stays (written, unused) to
  avoid a migration.
- Method names keep the `Fresh` suffix to bound churn, even though "fresh" no
  longer means "invalidate cache". A rename is out of scope.

Tradeoff being accepted: every scan full-resolves every service; there is no
longer any dedup between poll frequency and registry load. Acceptable for the
target (self-hosted, modest service counts, >= 10m polls); it matters only if a
user polls faster than the old TTL against many rate-limited images.

## Tests

**Go**

- `Abort()` cancels an in-flight run: the sweep loop breaks early, `state` goes
  idle, `scan_finished` is published, and `last_check_all` is **not** stamped.
- `Abort()` on an idle runner is a no-op (no panic, no events).
- `DELETE /api/scan` returns `204` whether running or idle.
- `RunScheduled` runs the sweep synchronously and returns `true`; a second
  concurrent call (or one while a manual `Start` holds the guard) returns
  `false` without running.
- `CheckServicesFresh` stops early when `ctx` is cancelled mid-sweep (`onDone`
  fires for the services checked before cancellation, not after).
- Scheduled path stamps `last_check_all` + publishes `scanned` on completion
  exactly once (via `execute`, not a duplicate in `runCheck`).
- Piece 2: `Detect` always full-resolves (no digest-only path); the removed
  `cache_ttl_seconds` setting is gone; `image_remote_state` status/labels are
  still upserted and read.

**Web**

- `ScanProgress` renders a Cancel button while running; clicking it issues
  `DELETE /api/scan` and disables the button until `scan_finished`.
- Check buttons disable during a scheduled scan (driven by `scan_progress`) the
  same as a manual one.

## Out of scope

- Preempting a running scan (blocked-not-preempt was chosen).
- Concurrent scan-runs (single-flight stays).
- Per-service abort granularity (abort cancels the whole run).
- Renaming the `Fresh` scan methods after the cache is gone.
