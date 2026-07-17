# Current-version changelog for up-to-date services

Date: 2026-07-17
Status: approved

## Problem

The dashboard shows a changelog for a service only when there is an update row
carrying one:

- **Pending update** (`available`): `/api/updates` (`ListVisible`) serves it.
- **Previously applied**: the last-applied fallback
  (`ListLastAppliedByService`, `GET /api/updates/last-applied`, see
  `2026-07-13-dashboard-last-changelog-design.md`) keeps the newest `applied`
  row's changelog on the row after an apply.

A service discovered **already up-to-date, with no update history**, has no
update row at all. `scan.CheckService` sees `detect` return nil and returns
early without resolving anything, so the Changelog column renders empty. This is
the common case on first run / when adopting an already-current fleet.

Goal: a service that is up-to-date with no history still shows the changelog of
its **current running version**. The previously-applied case is already covered
by the last-applied fallback and is unchanged.

## Design

### Data model: a synthetic `current` update row

Introduce a new `status` value, `current`, on the `updates` table. A `current`
row is a synthetic record that carries the current version's changelog for an
otherwise historyless, up-to-date service:

- `from_digest == to_digest ==` the service's current digest.
- `from_version == to_version ==` the current **resolved** version (see below).
- `tag ==` the ref's tag, `severity == "current"`, `status == "current"`.

`current` is inert everywhere pending/actionable logic lives: every filter,
apply-gate, notify, and badge path keys on `available` (and the last-applied
path keys on `applied`), so a `current` row is never pending, never applied "by
dockbrr", never counted in the up-to-date filter's update check. `Upsert`'s
`ON CONFLICT(service_id, to_digest) DO UPDATE` preserves `status`, exactly as
for every other non-`available` row, so a re-scan never resurrects or mutates
its status.

### Current version resolution

The version used for the synthetic row (and thus for changelog resolution) is,
in order:

1. `images.GetByDigest(repo, svc.CurrentDigest).ResolvedVersion`: the
   reverse-looked release version, the same value the UI already shows for
   up-to-date floating tags (`60b3a35`, detect.go:335).
2. `svc.ImageVersion`: the running image's `org.opencontainers.image.version`
   label.
3. the ref's tag (`detect.SplitRef`).

The changelog resolver already handles `from == to`: with no span to walk,
`github.go:159` returns the target release's own notes, i.e. the current
version's changelog. A floating tag with no resolvable version (`latest`, no
label) degrades gracefully to a link or an empty result, same as any digest-only
update today.

### Creation trigger (scan)

In `scan.CheckService`, when `detect` returns nil (up-to-date):

- If the service has **no existing update rows at all**, create the synthetic
  `current` row for the resolved current version, then resolve + persist its
  changelog through the existing `changelog.Resolve` path (rate-limit → 
  `SetChangelogStatus("rate_limited")`, miss → nothing, hit → `SetChangelog`),
  identical to the fresh-update branch.
- If the service already has any update row (open, applied, dismissed,
  superseded, rolled_back, or a prior `current`), do nothing: the existing rows
  already provide the dashboard changelog, and the last-applied fallback prefers
  `applied`.

This needs a store helper to answer "does this service have any update row?"
(e.g. `Updates.CountByService` or `HasAny`). Creation reuses `Upsert` (status
defaulted via the passed `Update.Status = "current"`).

The up-to-date branch still clears the notify dedup (`delete(notifiedTo, ...)`)
as it does today.

### API / web surface

Generalize the last-applied fallback rather than add a parallel path:

- `Updates.ListLastAppliedByService` → selects the newest row per service among
  `status IN ('applied','current')`, with `applied` winning ties over `current`
  (an applied service should show what dockbrr applied, not a stale baseline).
  Ordering stays `COALESCE(applied_at, detected_at) DESC, id DESC` within the
  status-priority tier.
- The DTO, `GET /api/updates/last-applied`, `useLastApplied()`, and the
  `lastApplied` decoration in `useDashboardRows` are **unchanged**: a `current`
  row flows through the existing `lastApplied` map and Changelog-column fallback
  (`r.update ?? r.lastApplied`) the same way an `applied` row does.
- The read-only changelog drawer renders identically. The muted-cell tooltip
  currently says "last applied"; for a `current`-sourced cell it should read as
  the current version instead (exact wording deferred to the plan).

Consider renaming the helper/endpoint to reflect its broadened meaning
("latest changelog-bearing non-open update per service"); a rename is optional
and can stay out of scope to keep the diff focused.

### Data lifetime

The `current` row persists once created. If a real update later appears and is
applied, the `available`/`applied` rows take over the dashboard fallback
(`applied` outranks `current`); the `current` row lingers harmlessly.

## Testing

- **store**: `current` row upsert + status preserved on conflict; generalized
  `ListLastAppliedByService` priority (applied beats current for the same
  service; a service with only a `current` row returns it; a service with
  neither returns nothing); the "has any update row" helper.
- **scan**: up-to-date + no history → creates one `current` row and resolves its
  changelog; up-to-date + existing history (each of open / applied / prior
  `current`) → no new row; rate-limit path stamps `changelog_status`; the
  resolved current version follows the ResolvedVersion → ImageVersion → tag
  fallback.
- **web**: `joinRows` / Changelog column shows the `current` row's changelog for
  an up-to-date, historyless service; renders empty when it has no text/URL; the
  drawer stays read-only (no Apply/Dismiss).

## Out of scope

- **Case A cumulative history** ("stitch every applied changelog"): rejected in
  brainstorming. The newest applied row already spans its own
  from→to jump; single-row display stands.
- Changing which statuses `/api/updates` (`ListVisible`) returns: `current`
  must not appear there.
- Refreshing a stale `current` row when the running version changes without a
  dockbrr apply (deferred; a follow-up may upsert-refresh the row's version).
- Retention/pruning of `current` (or applied) rows.
- Any change to the service detail page timeline.

## Open micro-decisions (resolve in plan)

- Drawer caption wording for a `current`-sourced cell ("current version" vs the
  existing "last applied").
- Whether the store helper is `CountByService` vs a boolean `HasAny`, and
  whether to rename `ListLastAppliedByService`.
