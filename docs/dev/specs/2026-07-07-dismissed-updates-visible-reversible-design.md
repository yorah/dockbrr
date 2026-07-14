# Design: visible + reversible dismissed updates

**Date:** 2026-07-07
**Status:** approved (brainstorm), pending implementation plan

## Problem

Dismissing an update is a dead end. `useDashboardRows` joins only `available`
updates to services (`openStatuses = new Set(["available"])`), so a dismissed
update is dropped from the join and the service silently renders as
**Up to date**. There is no visual trace that a newer version exists and was
deliberately ignored, and there is no un-dismiss path anywhere (no API route, no
UI action). Because the row shows no update, the review drawer is unreachable, so the user cannot even get back to the update they dismissed.

Confirmed sticky behavior (batch2 correctness, intentional): `RecordDrift`
upsert on `(service_id, to_digest)` preserves `status`, so a re-check of the
same digest never resurrects a dismissed update. That part stays. What changes
is visibility and reversibility.

## Goal

1. A dismissed update stays **visible** on the dashboard with its version delta
   and a grey **Dismissed** status badge.
2. The dismissal is **reversible** from the UI (Restore → back to available).

## Non-goals (YAGNI)

- No new status filter option or "show dismissed" toggle (always visible).
- No bulk dismiss/restore.
- Dismiss stays per-digest: when drift moves to a *new* `to_digest`, a fresh
  `available` row is created as today (dismissal does not carry forward).
- The `available`-only Docker/mutation and detection paths are untouched.

## Design

### 1. Join: `web/src/hooks/useDashboardRows.ts`

Attach both `available` and `dismissed` updates to their service row, with
`available` taking precedence when a service somehow has both (it should not,
given the per-digest uniqueness, but precedence keeps the row deterministic).

- Replace the single `openStatuses` set usage: build `byService` preferring an
  `available` update, falling back to a `dismissed` one for the same service.
- `Row`'s `update?: Update` now carries the dismissed update too, so the row
  renders the version delta and is clickable (opens the drawer).
- **`onlyUpdates` filter**: unchanged semantics: shows only rows with an
  **available** update. A dismissed row is not actionable, so it is excluded
  when `onlyUpdates` is on.
- **Empty-header drop** logic unchanged.
- `updatesByService` (used elsewhere) keeps mapping **available** only, so any
  "actionable update" consumers are unaffected.

### 2. Status: `web/src/components/StatusBadge.tsx`

- Add `"dismissed"` to the `Status` union.
- `LABEL.dismissed = "Dismissed"`, `VARIANT.dismissed = "default"` (grey, same
  slate treatment `EventItem` already uses for dismissed events).
- `computeStatus(svc, update, opts)` currently takes `update: { open: boolean }`.
  Widen it to also convey a dismissed update. Precedence, unchanged where it
  matters:
  1. `opts.updating` → updating
  2. `svc.state === "gone"` → gone
  3. stopped / restarting (state-based)
  4. pinned
  5. `svc.state === "error"` → error
  6. **available update → update-available**
  7. **dismissed update → dismissed**  ← new
  8. else up-to-date

  State-based statuses and pinned still win over an update badge, matching
  today's behavior. Callers pass the joined update (or its status) instead of a
  bare `{ open }`.

### 3. Restore endpoint: backend

- Route: `POST /api/updates/{id}/restore` (CSRF-guarded, mirrors
  `handleDismiss`).
- Handler: `Updates.SetStatus(id, "available")`; 404 if the id is unknown,
  same error shape as dismiss.
- No new store method needed, `SetStatus` already exists.

### 4. Restore mutation + drawer, frontend

- `web/src/hooks/mutations.ts`: add `useRestore()`: `POST
  /api/updates/{id}/restore`, on success invalidate the updates query (and any
  updates-derived keys the dismiss mutation already invalidates).
- `web/src/components/ReviewDrawer.tsx`: when `update.status === "dismissed"`,
  render **Restore** in place of **Dismiss** (Apply stays available, a
  dismissed update can be applied directly). Restore closes the drawer on
  success; the row's badge returns to "Update available".

### Data flow

```
detect → RecordDrift (status preserved: dismissed stays dismissed)
        → GET /api/updates returns the dismissed row (already does)
        → useDashboardRows joins it to the service (NEW)
        → StatusBadge shows grey "Dismissed" + delta (NEW)
        → row click → ReviewDrawer → Restore (NEW)
        → POST /api/updates/{id}/restore → SetStatus available
        → invalidate updates → badge flips to "Update available"
```

## Testing

- **useDashboardRows** (vitest): a dismissed update joins to its row and yields a
  row with `update` set; `onlyUpdates` excludes the dismissed row; a service with
  both available+dismissed prefers available.
- **StatusBadge / computeStatus** (vitest): dismissed update → `"dismissed"`;
  precedence: gone/stopped/pinned still win over dismissed.
- **Restore endpoint** (Go httpapi test): `POST /restore` flips status to
  available; requires auth (401) and CSRF; unknown id → 404.
- **ReviewDrawer** (vitest): dismissed update renders Restore (not Dismiss);
  clicking Restore calls the mutation and closes.

## Safety invariants

Unaffected. This is read-model + a status flip only. No Docker mutation, no
compose, no snapshot path touched. CSRF on the new mutation per invariant 7.
