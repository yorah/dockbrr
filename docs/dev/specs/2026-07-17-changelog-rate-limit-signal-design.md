# Changelog rate-limit signal

Date: 2026-07-17
Status: approved, pending plan

## Problem

A GitHub-hosted image (e.g. `ghcr.io/autobrr/autobrr`) resolves its version fine
but shows "No changelog available." Root cause: the GitHub Releases API is
throttled to 60 requests/hour when no token is set, and a dashboard with several
containers exhausts that quickly. The resolver treats the resulting 403 as a
plain miss and persists nothing, so the UI cannot tell a throttled changelog
apart from an image that genuinely has no changelog. The user has no signal that
setting a GitHub token would fix it.

## Goal

When a changelog is empty because GitHub throttled the resolve attempt, record
that fact per-update and surface it in the UI with a pointer to the token fix.
Add docs (README section + token tooltip) covering how to create the token and
where to set it.

Non-goals: surfacing auth failures in the UI (log-only, see below); live
rate-limit state; retry/backoff logic.

## Approach

Propagate a real 403 from the resolve attempt through to a persisted per-update
marker, then render it. Precise: the marker reflects the actual resolve for that
update, not a heuristic guess. Same staleness model as the changelog text itself
(cleared on the next successful resolve, which happens on a fresh update or a
manual Check).

### 1. Detect the 403 (internal/changelog)

`github.go` gains a sentinel:

```go
var ErrRateLimited = errors.New("changelog: github rate limited")
```

`fetchReleasesPage` (currently the `default` case returns a generic status
error): when status is **403 or 429** and header `X-RateLimit-Remaining` is
`"0"`, return `ErrRateLimited`. Every other non-2xx keeps the generic
`github releases: status %d` error. The error propagates up through
`fetchReleases` and `GitHubSource.Resolve` unchanged (both already return the
first error they see).

Auth failure is distinct and stays log-only: a bad or expired token makes GitHub
return **401** (`Bad credentials`), not 403, so it never matches the rate-limit
gate. It falls to the default branch as `github releases: status 401`, which the
resolver logs at Error level (see step 2). No UI marker.

### 2. Resolver reports it only on a full-chain miss (resolver.go)

`Resolver.Resolve` walks its ordered sources and returns the first non-empty
(sanitized) text/url pair. Change: track a `sawRateLimit` flag in the loop,
setting it when a source error `errors.Is(rerr, ErrRateLimited)`. Source errors
are still logged and the chain still continues (an OCI-label or CHANGELOG.md link
from a later source is real content and wins, in which case no rate-limit signal
is emitted). Only when the loop ends with no content **and** `sawRateLimit` is
set does `Resolve` return `("", "", ErrRateLimited)`.

Signature stays `(text, url string, err error)`. Today `Resolve` always returns
a nil error; after this change the only non-nil error it can return is
`ErrRateLimited`.

### 3. Persist the marker (internal/store)

- Migration `migrations/0010_updates_changelog_status.sql`:
  `ALTER TABLE updates ADD COLUMN changelog_status TEXT NOT NULL DEFAULT '';`
- `Update` struct: add `ChangelogStatus string`.
- Add `changelog_status` to every SELECT column list and its matching `rows.Scan`
  in `updates.go` (ListOpen, ListVisible, and the other row-hydrating queries)
  and to the changelog SELECT in `store/events.go`.
- `SetChangelog(id, url, text)`: extend the UPDATE to also set
  `changelog_status=''`, so a later successful resolve clears a prior
  `rate_limited`.
- New `SetChangelogStatus(id int64, status string) error`: `UPDATE updates SET
  changelog_status=? WHERE id=?`, used for the empty + rate-limited case (text
  and url stay empty).

Column values: `''` (resolved normally, or genuine miss) or `'rate_limited'`.

### 4. Scan branch (internal/scan/scan.go)

Replace the current resolve-then-store block:

```go
text, url, err := s.changelog.Resolve(ctx, *upd, remote)
switch {
case errors.Is(err, changelog.ErrRateLimited):
    if e := s.updates.SetChangelogStatus(upd.ID, "rate_limited"); e != nil {
        logger.Errorf("scan: persist changelog status (update %d): %v", upd.ID, e)
    }
case err != nil:
    logger.Errorf("scan: changelog resolve (service %d (%s)): %v", serviceID, svc.Name, err)
case text != "" || url != "":
    if e := s.updates.SetChangelog(upd.ID, url, text); e != nil {
        logger.Errorf("scan: persist changelog (update %d): %v", upd.ID, e)
    }
}
```

A genuine miss (no content, no rate-limit) falls through all cases: nothing
written, `changelog_status` stays `''`.

### 5. API surface (internal/httpapi)

- `updates.go` update DTO: add `changelog_status string` (`json:"changelog_status"`),
  mapped from `store.Update.ChangelogStatus`.
- `events.go` SSE update DTO: same field, same mapping.

### 6. Frontend (web/src)

- `api/types.ts` `Update`: add `changelog_status?: string`.
- `components/Changelog.tsx`: the empty branch takes an optional `status` prop.
  `status === "rate_limited"` renders "GitHub rate limit reached. Changelog
  unavailable until the limit resets. Add a GitHub token in Settings to raise the
  limit." with a TanStack Router `Link` to `/settings/registries` (where the
  GitHub token field lives). Otherwise the
  plain "No changelog available." Keep the component a pure renderer: it takes
  `status` as a prop, it does not call a settings hook.
- `components/ReviewDrawer.tsx` and `components/ChangelogDrawer.tsx`: pass
  `status={update.changelog_status}` to `<Changelog>`, guarded so the hint shows
  only when there is also no `changelog_url` (a link is content). `HistoryTimeline`
  keeps calling `<Changelog>` without a status (applied updates, not relevant).

### 7. Docs

- Token tooltip (`components/settings/RegistriesSettings.tsx`, the GitHub token
  `HelpTooltip`): add the create steps (github.com/settings/tokens, classic
  token, no scopes needed for public release notes, paste here) and that it
  lifts the changelog rate limit from 60 to 5000 requests/hour.
- README: a short "GitHub token and changelogs" section with the same steps and
  the reason (unauthenticated GitHub API is throttled to 60 req/hour, which hides
  changelogs).

## Testing

- changelog: 403 with `X-RateLimit-Remaining: 0` yields `ErrRateLimited`; a 403
  without that header, and a 401, yield the generic status error. Resolver:
  empty chain + a rate-limited source returns `ErrRateLimited`; a chain that
  finds content (including an OCI-label link after a rate-limited GitHub source)
  returns that content with a nil error.
- store: migration applies; `SetChangelogStatus` sets `rate_limited`;
  `SetChangelog` clears it back to `''`; the new column round-trips through the
  list queries.
- scan: a rate-limited resolve persists `rate_limited` and no changelog; a
  successful resolve persists content and clears the status; a genuine miss
  leaves `''`.
- frontend: `Changelog` renders the rate-limit hint (with the Settings link) for
  `status="rate_limited"` and the plain message otherwise; the drawers pass the
  status through and suppress the hint when a `changelog_url` is present.

## Files touched

- `internal/changelog/github.go`, `resolver.go` (+ tests)
- `internal/store/migrations/0010_updates_changelog_status.sql`,
  `internal/store/updates.go`, `internal/store/events.go` (+ tests)
- `internal/scan/scan.go` (+ tests)
- `internal/httpapi/updates.go`, `internal/httpapi/events.go` (+ tests)
- `web/src/api/types.ts`, `web/src/components/Changelog.tsx`,
  `web/src/components/ReviewDrawer.tsx`, `web/src/components/ChangelogDrawer.tsx`,
  `web/src/components/settings/RegistriesSettings.tsx` (+ tests)
- `README.md`
