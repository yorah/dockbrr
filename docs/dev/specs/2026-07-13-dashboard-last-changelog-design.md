# Dashboard: last applied changelog

Date: 2026-07-13
Status: approved

## Problem

The changelog of an update is readable on the dashboard only while the update is
pending. `/api/updates` serves `Updates.ListVisible()`, which returns
`available | dismissed | rolled_back`. Applying an update flips its status to
`applied`, so the row's `update` disappears from the dashboard join and the
Changelog column (`web/src/components/DashboardTable.tsx`, column id
`changelog`) renders `: `.

The data is not lost: `Updates.SetStatus` never clears `changelog_url` /
`changelog_text`, and `Events.ListByService` LEFT JOINs `updates` on
`(service_id, to_digest)` so the service detail page still shows the changelog
per history event. But reaching it takes two clicks plus a collapsed disclosure.

Goal: after an apply, the dashboard row keeps showing the changelog of the
update that was just applied.

## Design

### Store

Add `Updates.ListLastAppliedByService() ([]Update, error)`: for every
`service_id`, the newest row with `status='applied'`, ordered
`detected_at DESC, id DESC`, one row per service. Returns the full `Update`
(changelog columns included).

`ListVisible` is unchanged. Status badges, the `up-to-date` filter, and Apply
gating keep their current semantics: they must not start seeing applied rows.

### API

New route `GET /api/updates/last-applied` → `[]updateDTO` (the existing DTO,
`status` will read `applied`). A separate endpoint rather than a flag on
`/api/updates`, so the existing list keeps meaning "visible/actionable".

Auth + CSRF: read-only GET, same middleware as `GET /api/updates`.

### Web

- `useLastApplied()` query, key `["updates","last-applied"]`. It shares the
  `["updates"]` invalidation prefix, so the existing apply/SSE cache busting
  already refreshes it.
- `useDashboardRows` builds `lastAppliedByService: Map<number, Update>` and adds
  `lastApplied?: Update` to the `service` row variant. It is presentational
  only: `joinRows`' filters (`onlyUpdates`, `status`, `search`) keep reading
  `update`, never `lastApplied`.
- Changelog column resolves `r.update ?? r.lastApplied`. If the resolved update
  has `changelog_text` or `changelog_url`, render a button; otherwise `, `. When
  the cell resolved from `lastApplied`, mark it muted with a "last applied"
  tooltip so history is distinguishable from a pending changelog.
- Clicking the cell opens a **read-only changelog drawer**: the `<Changelog>`
  component (react-markdown + rehype-sanitize, per safety invariant 7) plus the
  "View full changelog ↗" external link, with no Apply/Dismiss buttons. The
  Eye/Review action is untouched and still opens the actionable `ReviewDrawer`
  for pending updates.

### Data lifetime

Nothing new is persisted. The applied row already retains its changelog.

Edge case to cover in tests: `Updates.RecordDrift` flips `applied` → `available`
when the same digest drifts again. Such a row then leaves the last-applied set
and reappears as a pending update, correct, and the column falls back to
`r.update` on its own.

## Testing

- Store: newest applied row wins per service; `available` / `dismissed` /
  `superseded` / `rolled_back` rows are excluded; a service with no applied
  update contributes no row; changelog columns survive the status flip.
- API: `GET /api/updates/last-applied` shape + auth.
- Web: Changelog cell falls back to the last-applied update after the pending
  update is gone; renders `: ` when neither update has changelog text or URL;
  the read-only drawer renders markdown and exposes no Apply/Dismiss control.

## Out of scope

- Changing which statuses `/api/updates` returns.
- Any change to the service detail page timeline (it already works).
- Retention/pruning of applied updates.
