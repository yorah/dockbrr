# Self-update notification design

## Goal

Show a notification in the sidebar when a newer dockbrr release is published on
GitHub, with a "View Release" button that opens the release page. This is about
dockbrr updating *itself* (the app binary), not the Docker containers it manages.

## Non-goals

- No in-app self-update / auto-download. The button links to GitHub; the operator
  updates the deployment themselves.
- No settings toggle to disable the check (YAGNI; the check is a single best-effort
  GET and never blocks or errors the UI).
- No pre-release / draft notifications. Stable releases only.

## Backend

### New package: `internal/selfupdate`

Single-purpose, isolated. Does **not** reuse `internal/changelog/github.go`'s
internals (that client is built for repo-mapped changelog resolution and carries
cache/TTL/token machinery we don't want to entangle). This is one small GitHub GET.

```
type Result struct {
    Current         string    // internal/version.Version
    Latest          string    // latest stable tag from GitHub (as returned, e.g. "v0.5.0")
    HTMLURL         string    // release page URL
    UpdateAvailable bool
    CheckedAt       time.Time
}

type Checker struct {
    http     *http.Client
    settings *store.Settings
    current  string        // version.Version
    ttl      time.Duration // e.g. 6h
    apiBase  string        // https://api.github.com (overridable for tests)
    tokenFn  func() string // optional GitHub token; may return ""
}
```

`Check(ctx) (Result, error)`:

1. Read cache from settings keys `selfupdate_latest_tag`, `selfupdate_latest_url`,
   `selfupdate_checked_at`.
2. If cache present and `now - checked_at < ttl` → build `Result` from cache and
   return (no network).
3. Otherwise `GET {apiBase}/repos/yorah/dockbrr/releases/latest` with
   `Accept: application/vnd.github+json` and, if `tokenFn()` non-empty,
   `Authorization: Bearer <token>`. GitHub's `/releases/latest` already excludes
   drafts and pre-releases.
   - On success: parse `{ tag_name, html_url }`, write the three settings keys with
     `checked_at = now`, return fresh `Result`.
   - On error (network, non-200, rate-limit 403) **with** a stale cache present:
     return the stale cache (best-effort), log at debug. Do **not** refresh
     `checked_at` (so the next request retries).
   - On error **with no** cache: return `Result{Current: current, UpdateAvailable:
     false}` and a soft error the handler swallows. Never surfaces as a 500.

`UpdateAvailable` computation: parse both tags with `detect.ParseCore`; if both
parse, `UpdateAvailable = detect.CoreLess(currentCore, latestCore)`. If the current
version does not parse (dev build, unusual tag) → `false` (don't nag dev builds).

### Startup warm

During server construction, fire `checker.Check(ctx)` once in a goroutine
(non-blocking) so the cache is warm before the first sidebar render. Failure is
ignored (lazy path will retry on request).

### HTTP endpoint

- Handler `handleSelfUpdate` (new file `internal/httpapi/selfupdate.go`): call
  `s.deps.SelfUpdate.Check(ctx)`, `writeJSON(w, 200, {...})`.
- Response shape:
  ```json
  {
    "current": "0.4.1",
    "latest": "v0.5.0",
    "html_url": "https://github.com/yorah/dockbrr/releases/tag/v0.5.0",
    "update_available": true,
    "checked_at": "2026-07-17T10:00:00Z"
  }
  ```
- Route: `r.Get("/api/updates/self", s.handleSelfUpdate)` inside the authed
  `s.mux.Group` in `server.go` `routes()`.
- Wire the `*selfupdate.Checker` into `Deps` and construct it in `New`, reusing the
  existing GitHub-token accessor (same source `changelog` uses) for `tokenFn`.

## Frontend

- `web/src/api/types.ts`: `SelfUpdate { current, latest, html_url, update_available,
  checked_at }`.
- `web/src/api/keys.ts`: `updates: ["updates", "self"] as const`.
- `web/src/hooks/queries.ts`: `useSelfUpdate` →
  `apiFetch<SelfUpdate>("/api/updates/self")`, `refetchInterval: 6 * 60 * 60 * 1000`.
- **New `web/src/components/layout/UpdateNotice.tsx`**: accent card matching the
  reference — lucide download/arrow-up icon, heading "Update Available", body
  "Version v{latest} is now available", a **View Release** button rendered as
  `<a href={html_url} target="_blank" rel="noopener noreferrer">`, and a **✕**
  dismiss control. Colors use dockbrr's existing accent tokens (not autobrr's).
  - Renders `null` when `!data?.update_available`.
  - Renders `null` when dismissed for the current `latest` — dismiss stores the tag
    in `localStorage["dockbrr_dismissed_update"]`; card shows again only when a newer
    tag ships (stored tag !== latest).
  - **Collapsed sidebar variant**: when the sidebar is collapsed, render an icon-only
    button wrapped in a Tooltip ("Update available") that links to `html_url`,
    mirroring the existing collapsed-Logout pattern in `Sidebar.tsx`.
- `web/src/components/layout/Sidebar.tsx`: render `<UpdateNotice collapsed={...} />`
  directly above the Logout button (~line 48). Pass the same `collapsed` flag the
  sidebar already uses.

## Error handling

- Backend: transient GitHub failure or rate-limit → soft, keep/return stale cache,
  log debug. The endpoint always returns 200 with a valid body.
- Frontend: query error → `useSelfUpdate` returns no data → card renders `null`.
  No error UI in the sidebar.

## Testing

### Go (`internal/selfupdate`)

Table/`httptest`-driven `Checker` tests with an in-memory `store.Settings`:

- Fresh fetch on empty cache → populates settings, returns `update_available` per
  version compare.
- Cache hit within TTL → no HTTP call made (assert server not hit).
- Stale cache past TTL → refetches, updates cache.
- GitHub error with stale cache present → returns stale, leaves `checked_at`
  unchanged.
- GitHub error with no cache → `update_available:false`, no panic.
- Version compare matrix: latest newer → true; equal → false; latest older → false;
  unparsable current → false.

### Vitest (`UpdateNotice`)

Mock `useSelfUpdate`:

- `update_available:true`, not dismissed → card + View Release link (correct href)
  visible.
- `update_available:false` → renders nothing.
- Dismissed tag === latest → renders nothing; clicking ✕ writes localStorage.
- New latest tag after an old dismissal → card shows again.
- Collapsed variant → icon-only link + tooltip.

## Files touched

- `internal/selfupdate/checker.go` (new)
- `internal/selfupdate/checker_test.go` (new)
- `internal/httpapi/selfupdate.go` (new)
- `internal/httpapi/server.go` (route + Deps wiring)
- `web/src/api/types.ts`, `web/src/api/keys.ts`, `web/src/hooks/queries.ts`
- `web/src/components/layout/UpdateNotice.tsx` (new)
- `web/src/components/layout/UpdateNotice.test.tsx` (new)
- `web/src/components/layout/Sidebar.tsx`
