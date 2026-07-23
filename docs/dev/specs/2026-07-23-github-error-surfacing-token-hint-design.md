# GitHub error surfacing with token hint

Date: 2026-07-23

## Problem

dockbrr calls the GitHub API on two paths, and both degrade silently when GitHub
fails (most commonly the unauthenticated 60-req/hour rate limit):

1. **Self-update check** ("Check for updates" button + the background
   `/api/updates/self` poll). `handleSelfUpdate` discards the checker error
   (`res, _ = ...`). On a rate-limit with no cache to fall back on, the response
   is `update_available:false` with no `checked_at`, so the Settings Build card
   renders nothing and the button spins then quietly does nothing. The user
   cannot tell the check failed. The checker does not even classify a
   rate-limit: `fetchLatest` returns a generic `status %d`.

2. **Scan / check (single + check-all)** changelog enrichment. A rate-limit is
   already surfaced per update (`changelog_status="rate_limited"`, rendered by
   `Changelog.tsx` with a token hint), but that is only visible after opening a
   drawer. There is no signal at the end of a manual scan telling the user the
   run hit GitHub limits.

The fix the request points at (add a GitHub token) directly addresses the
rate-limit, which is the dominant failure. A token raises the limit to 5000/hr.

## Goals

- Surface a GitHub error to the user on both paths.
- Only a **rate-limit** (HTTP 403/429 with `X-RateLimit-Remaining: 0`) carries
  the "add a GitHub token" hint. A network error or a GitHub 5xx is not fixed by
  a token, so it shows a generic "GitHub unreachable" message.
- Self-update check: toast on the manual button + a persistent inline line in
  the Settings Build card.
- Scan: an aggregate toast after a **manual** check/check-all that hit a
  rate-limit.

## Non-goals

- No `github_token_set` signal. The hint is shown on every rate-limit, not gated
  on whether a token is already configured, matching the existing `Changelog.tsx`
  precedent. (If a token is set and the limit is still hit, the wording is
  slightly off, same as today's changelog cell.)
- Scheduled sweeps never toast.
- No change to the per-update `changelog_status` rendering; it stays the
  authoritative per-update signal.

## Design

### 1. Rate-limit classification (backend)

- `internal/selfupdate`: add an `ErrRateLimited` sentinel. `fetchLatest` returns
  it when the response status is `403` or `429` **and**
  `X-RateLimit-Remaining: 0`, mirroring `changelog.fetchReleasesPage`. Every
  other non-200 stays a generic `github releases/latest: status %d`.
- `changelog.ErrRateLimited` already exists and is already detected in
  `scan.go`. Reuse it; no changelog change.

### 2. Self-update check path

**Backend**

- `selfupdate.Result` gains `FetchErr error` (`json:"-"`). `refresh()` stamps it
  on any fetch failure, whether it then serves a stale cache (top-level error
  stays `nil`) or errors out (no cache). This preserves the existing
  `(Result, error)` contract: `Check()` still returns a `nil` error when it
  serves stale, so `enqueueSelfUpdate` keeps enqueuing off a cached "update
  available" verdict during a transient outage (no regression). `FetchErr` is
  the softer, always-populated signal the read endpoint uses.
- `handleSelfUpdate` reads `res.FetchErr` (response stays HTTP 200, best-effort
  contract). When non-nil it adds to the JSON body:
  - `error_kind`: `"rate_limited"` when `errors.Is(res.FetchErr,
    selfupdate.ErrRateLimited)`, else `"unreachable"`.
  - `error`: the raw error string (debugging / fallback copy).

**Frontend**

- `SelfUpdate` type gains `error?: string` and
  `error_kind?: "rate_limited" | "unreachable"`. The user-facing copy lives on
  the frontend (consistent with `Changelog.tsx`).
- `useCheckForUpdates` onSuccess: if `data.error_kind` is set, `notify.error(...)`.
  The `rate_limited` variant reads "GitHub rate limit reached. Add a GitHub
  token in Settings to raise the limit."; `unreachable` reads "Couldn't reach
  GitHub to check for updates." The verdict is still written to the cache so the
  inline line renders.
- `ApplicationSettings` Build card: when `su?.error_kind` is set, render an
  inline warning line (`text-warning`) with the message and a `Link` to
  `/settings/registries` ("Add a GitHub token"). This covers the no-cache case
  where `checked_at` is absent and nothing renders today. When a stale cache was
  served alongside the error, the verdict line and the warning line both show.
- `UpdateNotice` is untouched: the background poll updates the cache (feeding the
  inline line) but does not toast.

### 3. Aggregate scan toast

- `CheckServicesFresh`, `CheckService`, and `CheckServiceFresh` return
  `(rateLimited bool, err error)`. `rateLimited` is `true` when any changelog
  resolve during the call returned `changelog.ErrRateLimited`. Update the
  `Checker` interface in `httpapi/server.go`, the single caller in `scanrun.go`,
  and the test doubles.
- `ScanRunner.execute` gains a `manual bool` parameter (`Start` passes `true`,
  `RunScheduled` passes `false`). It captures the sweep's `rateLimited` result;
  when `manual && rateLimited`, it publishes the `scan_finished` event with a new
  `Event.RateLimited bool` field (`json:"rate_limited,omitempty"`).
- `useEventStream`'s `scan_finished` handler: when `rate_limited` is true,
  `notify.error(...)` with the token hint.

**Known limitation:** the SSE bus is process-global, so the aggregate toast
reaches every connected browser, not only the one that triggered the scan, and a
dropped `scan_finished` frame drops the toast. Accepted: dockbrr is single-user,
the toast is a best-effort hint, and the per-update changelog cell hint remains
authoritative.

## Error-source matrix

| Path | Trigger | Rate-limit | Other GitHub error |
| --- | --- | --- | --- |
| Self-update check | manual button | toast (token hint) + inline line | toast (generic) + inline line |
| Self-update check | background poll | inline line only | inline line only |
| Scan | manual single / check-all | aggregate toast (token hint) + per-update cell | per-update behavior unchanged (logged, non-fatal) |
| Scan | scheduled | per-update cell only (no toast) | unchanged |

## Testing

- selfupdate checker: `fetchLatest` classifies 403/429 + `X-RateLimit-Remaining:0`
  as `ErrRateLimited`; other statuses stay generic. `refresh()` stamps `FetchErr`
  on both stale-serve and no-cache failures; top-level error contract unchanged.
- `handleSelfUpdate`: body carries `error_kind:"rate_limited"` / `"unreachable"`
  / absent, HTTP 200 throughout.
- scan: `CheckServicesFresh` bubbles `rateLimited` when a changelog resolve hits
  the limit; `execute` publishes `scan_finished.rate_limited` only when `manual`.
- frontend: `useCheckForUpdates` toasts per `error_kind`; `ApplicationSettings`
  renders the inline warning line + settings link; `useEventStream` toasts on
  `scan_finished` with `rate_limited`.
