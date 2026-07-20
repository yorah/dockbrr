# Manual "Check for updates" in Settings > Application

**Date**: 2026-07-20
**Status**: Approved, ready for plan

## Problem

Self-update infrastructure already exists end to end: the backend `selfupdate.Checker`
resolves the latest stable release from GitHub (GET `/api/updates/self`, cached in the
settings store with a 6h TTL), the sidebar `UpdateNotice` surfaces an "Update Available"
card when a newer tag ships, and POST `/api/updates/self/apply` enqueues the swap job.

What is missing is a **user-initiated** check. Today the only way an update is discovered
is the passive 6h poll. A user looking at the version number in Settings > Application has
no way to ask "am I on the latest?" on demand, and no visible confirmation of when the last
check ran.

## Goal

Add a "Check for updates" affordance in the Build card of Settings > Application, next to
the version number. Clicking it performs a **fresh** GitHub check (bypassing the cache TTL)
and shows a plain status line under the Version row: either "up to date" or
"{latest} available", with a relative "checked ..." timestamp.

Out of scope: applying the update from this card. The actual update action stays owned by
the sidebar `UpdateNotice` (which already has the container-gated "Update now" button). This
card is status-only, per product decision.

## Non-goals

- No new apply/link buttons in the Build card.
- No change to the passive 6h poll or the sidebar notice behavior.
- No change to the apply endpoint.

## Design

Three thin layers. Each reuses existing plumbing; the only genuinely new behavior is a
force-refresh path on the checker.

### A. Backend: force-refresh path

`internal/selfupdate/checker.go`

`Check(ctx)` currently serves a cached verdict when the cache is younger than the TTL. Add a
way to force a live fetch that ignores the young-cache branch but keeps every other property
(writes cache on success; falls back to stale cache on a GitHub error; never returns an error
when a cache exists to fall back on).

Chosen shape: extract the fetch-and-serve logic and expose a second entry point.

```go
// Check serves a cached verdict within the TTL, else refetches. (unchanged behavior)
func (c *Checker) Check(ctx context.Context) (Result, error) {
    tag, url, checkedAt, haveCache := c.readCache()
    if haveCache && time.Since(checkedAt) < c.ttl {
        return c.result(tag, url, checkedAt), nil
    }
    return c.refresh(ctx, haveCache, tag, url, checkedAt)
}

// CheckFresh always refetches from GitHub, ignoring the cache TTL. Same
// best-effort contract as Check: on a GitHub error it serves a stale cache when
// one exists (nil error), and only errors when there is nothing to fall back on.
func (c *Checker) CheckFresh(ctx context.Context) (Result, error) {
    tag, url, checkedAt, haveCache := c.readCache()
    return c.refresh(ctx, haveCache, tag, url, checkedAt)
}

// refresh performs the GitHub fetch + cache-write + stale-fallback shared by both.
func (c *Checker) refresh(ctx context.Context, haveCache bool, tag, url string, checkedAt time.Time) (Result, error) {
    fTag, fURL, err := c.fetchLatest(ctx)
    if err != nil {
        if haveCache {
            logger.Debugf("selfupdate: github fetch failed, serving stale cache: %v", err)
            return c.result(tag, url, checkedAt), nil
        }
        logger.Debugf("selfupdate: github fetch failed, no cache: %v", err)
        return Result{Current: c.current}, err
    }
    now := time.Now().UTC()
    c.writeCache(fTag, fURL, now)
    return c.result(fTag, fURL, now), nil
}
```

This is a pure refactor of the existing `Check` body plus one new public method; the existing
`Check` behavior and its tests are unchanged.

`internal/httpapi/` (self-update handler)

`handleSelfUpdate` reads a `force` query param. `?force=true` (or `1`) dispatches to
`CheckFresh`; anything else keeps the existing cached `Check`. The response body is identical
in both cases (`current`, `latest`, `html_url`, `update_available`, and `checked_at` when set).
A nil checker still short-circuits to `{"update_available": false}` regardless of `force`.

### B. Frontend: forced fetch + cache write

`web/src/api/keys.ts`, `web/src/hooks/queries.ts`

- `useSelfUpdate()` is unchanged: it remains the passive 6h poll that feeds the sidebar.
- Add a one-shot forced-check action. It calls `apiFetch<SelfUpdate>("/api/updates/self?force=true")`
  and writes the result into the `keys.selfUpdate` query cache via `queryClient.setQueryData`,
  so the sidebar `UpdateNotice` and the settings card stay consistent from a single source of
  truth. Expose it as a small mutation-style hook (e.g. `useCheckForUpdates`) returning
  `{ mutate/mutateAsync, isPending }`, or an inline `useMutation` in the component. Either way
  it must update `keys.selfUpdate` on success.

Rationale for writing to the shared cache rather than keeping a separate local result: a forced
check that finds a new release should immediately light up the sidebar notice too, not just the
settings line.

### C. UI: Build card

`web/src/components/settings/ApplicationSettings.tsx`

- Build card `action` slot: keep the existing "Refresh" button, add a "Check for updates"
  button beside it. While the forced check is pending, disable it and show a spinner/label
  (mirror the existing `isFetching` treatment on Refresh).
- Version `InfoRow` gains a `sub` line derived from the self-update result:
  - `update_available === true` → `"{latest} available (checked {relative})"`
  - otherwise → `"Up to date (checked {relative})"`
  - When no check has run yet this session and there is no `checked_at`, omit the sub line
    (or show nothing) rather than asserting "up to date" with no basis. If `checked_at` is
    present from the passive poll, the line can render immediately on mount.
  - Relative time ("just now", "2m ago") is computed from `checked_at` against the existing
    `useNow` tick already used in this component for uptime.

The self-update data reaches the component by reading the same `keys.selfUpdate` cache
(`useSelfUpdate()` or a lightweight read), so the sub line reflects whatever the latest check
(passive or forced) returned.

## Data flow

```
[Check for updates] click
  -> useCheckForUpdates.mutate()
  -> GET /api/updates/self?force=true
       -> handleSelfUpdate: force=true -> Checker.CheckFresh
            -> fetchLatest (GitHub), writeCache, or stale-fallback
  -> setQueryData(keys.selfUpdate, result)
  -> Build card Version sub-line + sidebar UpdateNotice both re-render
```

## Edge cases

- **Dev build** (unparsable running version): `isNewer` returns false, so `update_available`
  is false → "Up to date". Acceptable; dev builds are never nagged elsewhere either.
- **Checker nil / not configured**: body is `{update_available:false}`, no `checked_at`.
  Button still works, renders "Up to date" only if a `checked_at` basis exists, else no sub line.
- **GitHub error with a warm cache**: stale verdict served, `checked_at` unchanged (old). The
  sub line shows the last successful check time. No error toast required (best-effort contract);
  optionally a subtle "couldn't reach GitHub" toast is a nice-to-have, not required.
- **GitHub error with no cache**: handler still returns 200 `{update_available:false}` (the
  soft-error swallow in `handleSelfUpdate` is preserved). Consistent with today.
- **Rapid double-click**: `isPending` disables the button; forced fetches are idempotent anyway.

## Testing

Backend:
- `CheckFresh` bypasses a young cache (fetches GitHub even when cache is fresh).
- `CheckFresh` writes cache on success and serves stale cache on GitHub error (nil error).
- `CheckFresh` returns an error only when fetch fails and no cache exists.
- Existing `Check` tests unchanged and still pass (refactor is behavior-preserving).
- Handler: `?force=true` triggers a live fetch (assert via test server hit count vs the
  cached path); response shape identical to the non-forced call; nil checker short-circuits.

Frontend:
- `ApplicationSettings` renders the Version sub line for both `update_available` true/false
  given a `checked_at`.
- Clicking "Check for updates" issues `GET /api/updates/self?force=true` and, on success,
  updates the shared cache (sidebar/settings reflect the new verdict).
- Pending state disables the button.
- Relative-time rendering from `checked_at`.

## Files touched

- `internal/selfupdate/checker.go` (refactor + `CheckFresh`)
- `internal/selfupdate/checker_test.go` (force tests)
- `internal/httpapi/` self-update handler + its test (`?force=true`)
- `web/src/hooks/queries.ts` (+ mutations.ts if the hook lands there)
- `web/src/components/settings/ApplicationSettings.tsx`
- `web/src/components/settings/ApplicationSettings.test.tsx`
- `web/src/api/types.ts` only if a field is added (none expected)
