# Dashboard & Lifecycle UX: Design Spec

**Date:** 2026-07-11
**Status:** Approved (brainstorm complete; ready for planning)

## Goal

A batch of seven dashboard / settings / lifecycle improvements surfaced during
live smoke-testing: three quick fixes, a counter/filter alignment, and three
small features (auto-remove of dead services, a compose-file viewer, and
changelog access after an update is applied).

## Items

### 1. Pinned digest truncation (quick fix)

**Problem:** when a service is Pinned its `@sha256:…` digest is shown in full,
eating row width.

**Fix:** render the digest through the existing `DigestShort` presenter
(`web/src/components/presenters`: 12 hex chars + full value in a `title`
tooltip) wherever the pinned ref is displayed on the dashboard row. Frontend
only.

### 2. Hide gone-only projects when "show removed" is off (quick fix)

**Problem:** a project whose only services are Gone still shows its header when
"show removed" is off (empty expand).

**Fix:** in `joinRows` (`web/src/hooks/useDashboardRows.ts`), a project header is
dropped when *all* its services are filtered out. Extend/verify this covers the
case where every service is Gone and show-removed is off. Frontend only; covered
by a `useDashboardRows` test.

### 5. Settings show effective defaults (quick fix)

**Problem:** `handleGetSettings` returns the raw stored value for numeric keys,
which is empty when the key was never set, so Poll interval, Concurrency, Health
timeout/poll, and Cache TTL render blank in the form.

**Fix:** add a single `settingDefaults` map in `internal/httpapi/settings.go`
(key → default string) and, in `handleGetSettings`, fall back to it when a
numeric key is absent (mirrors how `write_back_compose` already falls back via
`GetBoolDefault`). Values must match the defaults the consumers actually use at
their `settingDuration(settings, key, default)` / equivalent call sites. A Go
test asserts every editable numeric key has a default and that the map's values
equal the consumer-side defaults (single source of truth, guards drift).

Defaults to surface (confirm exact values against the call sites during
implementation): `poll_interval_seconds`, `concurrency`, `health_timeout_seconds`
(120), `health_poll_seconds` (2), `cache_ttl_seconds`.

### 6. "Updates available" card counts only visible services

**Problem:** the top "N updates available" tile counts updates on services that
are hidden by the current filter (e.g. an update on a Gone service while
"show removed" is off), so clicking the tile shows "no services match".

**Fix:** the tile counts only currently-visible services, the same filter the
table applies (excludes Gone/removed when show-removed is off). The number always
matches what clicking reveals. Implemented in the dashboard stats computation
(`web/src/components/DashboardStats` + the rows/stats source), covered by a test.
Once #3 auto-remove (below) is on, Gone-service updates disappear anyway; this
keeps the count honest in every filter state.

### 3. Auto-remove gone services & empty projects (feature)

**Settings (both new):**
- `auto_remove_gone`: bool, default **true**.
- `gone_grace_seconds`: int, default **3600** (1 hour), configurable.

Both join the `settingKeys` whitelist + the GET/PUT surface and get a Settings UI
control (a Switch for the bool, a number field for the grace, both ride the
existing GeneralSettings dirty-indicator + diff-save).

**Schema:** migration `0005_service_gone_since.sql` adds
`services.gone_since TIMESTAMP` (nullable). It is set to "now" when a service
transitions **into** the Gone state and cleared (set NULL) whenever the service
is seen present again. Only the transition sets it, a service already Gone keeps
its original `gone_since` so the grace is measured from when it first went Gone.

**Reconcile (`internal/discovery/discovery.go`):** after the existing mark-gone
pass, when `auto_remove_gone` is true:
1. Hard-delete services where `state = 'gone'` AND `gone_since` is non-null AND
   `gone_since < now − gone_grace_seconds`. FK cascade removes each deleted
   service's `updates`, `state_snapshots`, and `service_events` rows, the
   accepted retention trade-off (a torn-down stack's history is disposable).
2. Delete **discovered** projects that now have zero services. **Manual** projects
   are never auto-deleted (their compose services may legitimately not be running,
   an existing invariant in discovery).

**Safety of the grace:** only containers actually **removed** (`docker rm`) go
Gone: a merely *stopped* container stays `stopped` and is never touched. The
grace additionally protects the brief Gone window during an apply's recreate
(`docker rm` old → create new) and normal redeploys. `gone_grace_seconds` is read
live per reconcile cycle (hot, not restart-gated).

The Reconciler needs read access to the two settings; inject the settings store
(or a small accessor) into the Reconciler, mirroring how other components read
settings.

### 4. Compose-file viewer modal

**Backend:** new `GET /api/projects/{id}/compose` (auth-guarded, read-only).
Returns `{ files: [{ path, content }] }`, each of the project's
`proj.ConfigFiles` read from disk via `os.ReadFile`. `ConfigFiles` already
excludes dockbrr's temporary rollback overrides (stripped at collection). A file
that fails to read yields an entry with an `error` string rather than failing the
whole response. No shell, no compose exec, plain file reads of paths dockbrr
already tracks.

**Frontend:** a button/link on the project header opens a **centered modal**
(the existing `Dialog` primitive: not the side `Drawer`) titled with the project
name, showing each file as a labelled, scrollable monospace block (`overflow`
contained, no horizontal page scroll). Read-only. New query hook +
`api/types.ts` DTO.

### 7. Changelog accessible after an update is applied

**Backend:** the service-events data path attaches the changelog of the update
matching each event. The events query (`internal/store` events read behind
`GET /api/services/{id}/events`) LEFT JOINs `updates` on
`(service_id, to_digest)` and selects `changelog_url` + `changelog_text`. An
`applied`/`succeeded`/`detected` event's `to_digest` matches its update row,
which persists (status `applied`) with the changelog intact; the
`UNIQUE(service_id, to_digest)` constraint makes the join 1:1. Events with no
matching update (or no changelog) carry empty changelog fields. Extend the
events DTO with `changelog_url` / `changelog_text`.

**Frontend:** in the service **history timeline**, a history entry that carries a
changelog shows a **Changelog** affordance. Activating it renders the existing
`Changelog` component (react-markdown + `rehype-sanitize`, no
`dangerouslySetInnerHTML`): rendered text when `changelog_text` is present,
otherwise the sanitized `changelog_url` as a `rel="noopener"` link. This reuses
the exact sanitized render path already used in the review drawer.

## Architecture notes

- Backend touches: `internal/store` (migration 0005, `gone_since` in services
  read/write, events changelog join, discovered-empty-project delete + gone-past-
  grace delete helpers), `internal/discovery` (prune pass + settings access),
  `internal/httpapi` (settings defaults + two new keys, `GET /projects/{id}/compose`,
  events DTO changelog fields).
- Frontend touches: `web/src/components` (DigestShort in the pinned cell,
  DashboardStats count, GeneralSettings two controls + populated defaults, a
  ComposeModal, history-timeline changelog), `web/src/hooks/useDashboardRows.ts`
  (gone-only header drop), `web/src/api/types.ts` + hooks (compose endpoint,
  events changelog fields, two settings).

## Safety invariants (unchanged, must still hold)

- Single static binary, CGO-free; SPA via embed.FS (only the dist placeholder
  tracked). The new `GET /projects/{id}/compose` reads files but adds no shell
  path (invariant 6 intact).
- UI/API never mutate Docker; the prune runs in discovery (read-only detection
  orchestrator boundary: it already reconciles store state, and delete is a
  store operation, not a Docker mutation).
- Frontend: changelog still rendered via react-markdown + rehype-sanitize, no
  `dangerouslySetInnerHTML`, no CDN; compose modal content is plain text in a
  `<pre>`, not HTML.

## Testing

- **#1/#2/#6:** frontend unit tests: pinned cell renders short digest; joinRows
  drops a gone-only project header when show-removed off; stats count matches the
  filtered visible set.
- **#3-fix:** Go test: GET settings returns each numeric default when unset;
  defaults map matches consumer-side defaults.
- **#3-feature:** Go tests: `gone_since` set on transition to gone / cleared on
  return; reconcile deletes services gone past grace (and NOT within grace);
  discovered empty project deleted, manual project preserved; `auto_remove_gone`
  off → nothing deleted.
- **#4:** Go test: `GET /projects/{id}/compose` returns file contents (+ auth
  401); frontend: modal renders file blocks.
- **#7:** Go test: events endpoint attaches changelog from the matching update
  (incl. an applied update); frontend: history entry shows the Changelog
  affordance and renders sanitized.

## Out of scope

- Any change to how updates are detected/applied (the write-back feature is
  done).
- A per-service "remove now" manual button (the auto-remove + grace covers the
  clutter; a manual override can be a later follow-up).
- Configurable retention of history for removed services (cascade delete is the
  accepted model).
