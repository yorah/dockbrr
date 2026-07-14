# Recreate Re-Surfaces Updates: Design Spec

**Date:** 2026-07-11
**Status:** Approved (brainstorm complete; ready for planning)

## Goal

When a user tears down and recreates a stack on an older image, dockbrr must
re-surface the available update instead of showing "up to date." Today an
`applied` update row is preserved forever (to protect a rollback), which also
hides a legitimately-pending update after an external recreate; and the detect
cache short-circuit delays re-evaluation for up to the TTL. Fix both while
keeping a dockbrr rollback protected from auto-re-application.

## Background: the two root causes

1. **`RecordDrift` preserves `applied`.** `internal/store/updates.go RecordDrift`
   re-opens `failed`/`superseded` rows to `available` on re-detection but
   deliberately preserves `applied` (and `dismissed`). The preservation exists
   for the **rollback-respect invariant** (`internal/job/worker.go` ~line 492):
   after a rollback the rolled-back-from update must not flip back to
   `available`, or auto-update would immediately re-apply it. But a dockbrr
   rollback and an external recreate-on-old-image produce the *same* state, an
   `applied` update whose `to_digest` ≠ the running digest, so protecting the
   rollback also hides the recreate's pending update.

2. **Detect cache short-circuit.** `internal/detect/detect.go` Detect step 1: a
   fresh same-tag digest cache hit (`freshCachedDigest`) returns "up to date"
   and skips the semver tag scan until the ~10-min TTL expires. Right after a
   recreate the image's cache is fresh (discovery just resolved it), so the
   cross-tag update is not even re-evaluated. A manual "Check now" can still
   say "up to date."

## Core idea

Make the rollback-vs-recreate distinction **explicit at write time**: a rollback
is the only thing that writes a new `rolled_back` status. Then `RecordDrift` can
safely re-open `applied` (recreate re-surfaces the update) while preserving
`rolled_back` (rollback stays suppressed). Separately, invalidate an image's
detect cache when discovery observes its running digest change, so a recreate is
re-evaluated promptly instead of after the TTL.

## A. `rolled_back` status

- New update status value: `rolled_back` (alongside
  `available|dismissed|applied|failed|superseded`).
- `runRollback` (`internal/job/worker.go`, the user-initiated rollback job)
  sets the rolled-back update's status to `rolled_back` (replacing the current
  "leave it `applied`, do NOT reset" behavior at ~line 492). Update that comment
  to describe the new explicit status.
- **Scope:** only a user-initiated rollback (the rollback job / `runRollback`)
  writes `rolled_back`. An auto-rollback after a *failed* apply keeps `failed`
  (already retryable via RecordDrift's re-open): do not change that path.
- `RecordDrift` CASE (the `e == nil` existing-row branch): re-open
  `failed`/`superseded` **and `applied`** to `available`; **preserve
  `rolled_back` and `dismissed`**. The status CASE becomes:
  `status = CASE WHEN status IN ('failed','superseded','applied') THEN 'available' ELSE status END`
  (was `('failed','superseded')`). `dismissed` and `rolled_back` fall through
  the ELSE and are preserved.
- Right after a successful apply the service runs `to_digest`, so detect finds
  target==current and returns early (no RecordDrift call), the `applied` row
  stays `applied` and hidden. Re-open only fires once the service diverges from
  its applied target (recreate / manual image change).

## B. Cache invalidation on running-digest change

- In `internal/discovery/discovery.go Reconcile`, compare each present service's
  newly-collected running digest against its stored `current_digest`. When they
  differ (a recreate/redeploy/update changed the running image), invalidate that
  service's image cache so the next detect does a full network resolve + semver
  scan rather than the digest-only short-circuit.
- Add `Invalidate(repo, tag string) error` to the remote-state store
  (`DELETE FROM image_remote_state WHERE repo=? AND tag=?`). Derive `repo,tag`
  from the service's `ImageRef` via `detect.SplitRef` (or the discovery-local
  equivalent: avoid a new cross-package dependency if one exists; a small local
  split is fine).
- Inject the remote-state store into the `Reconciler` (constructor +
  `cmd/dockbrr/main.go` wiring), mirroring how other stores are injected. A nil
  states store disables invalidation (tests that don't care pass nil).
- Blast radius: a sibling service sharing the same `repo:tag` also re-scans on
  the next cycle. Acceptable (one extra resolve).
- Only invalidate when the stored digest was non-empty AND differs, a brand-new
  service (no stored digest) does not invalidate (its first detect already runs
  fresh).

## C. Dashboard visibility + restore

- Extend the dashboard list query `ListOpenAndDismissed`
  (`internal/store/updates.go`) to also include `rolled_back`
  (`WHERE status IN ('available','dismissed','rolled_back')`). Keep `ListOpen`
  (available-only) as-is. Rename to reflect the third state if it reads cleanly
  (e.g. `ListVisible`), or keep the name and update its comment/SQL, implementer's
  call, but every caller must be updated consistently.
- Frontend `StatusBadge.tsx`: add `"rolled-back"` to the `Status` union, `LABEL`
  ("Rolled back"), `VARIANT` ("default"/grey: same as dismissed). `computeStatus`
  gains a `rolled_back` branch after the update-available check (mirror the
  `dismissed` branch): when `update.status === "rolled_back"` → `"rolled-back"`.
- The dashboard row's update carries `status`; `DashboardTable` passes it to
  `computeStatus` (extend the `{ open, dismissed }` shape to also carry
  `rolledBack`, or pass the raw status: match the existing dismissed wiring).
- **Restore reuses existing machinery:** `POST /api/updates/{id}/restore`
  (`handleRestore` → `SetStatus available`) already works for any status; show
  the drawer's **Restore** button for `rolled_back` too (currently gated on
  `dismissed`). `useRestore` is unchanged.
- The rollback already emits a `rolled_back`/rollback event in history; no change
  needed there beyond the status.

## D. Testing

**Go:**
- `RecordDrift`: re-detecting an `applied` row whose service diverged re-opens it
  to `available`; re-detecting a `rolled_back` row preserves `rolled_back`; a
  `dismissed` row stays `dismissed`; `failed`/`superseded` still re-open.
- `runRollback` sets the update to `rolled_back` (not `applied`).
- The dashboard list query returns `available` + `dismissed` + `rolled_back`,
  excludes `applied`/`failed`/`superseded`.
- Discovery: a present service whose collected digest differs from its stored
  digest triggers `states.Invalidate(repo, tag)`; an unchanged digest does not;
  a brand-new service (empty stored digest) does not. Use a fake/spy states store
  to assert the Invalidate call (no sleeps).

**Web:**
- `computeStatus` returns `"rolled-back"` for a `rolled_back` update; the badge
  renders grey "Rolled back".
- The drawer shows the **Restore** button for a `rolled_back` update and restore
  flips it to available (reuse the dismissed-restore tests as the template).

## Interplay / edge cases (must hold)

- **Rollback then recreate:** a `rolled_back` update stays `rolled_back` (visible,
  greyed, restorable) even if the stack is recreated, user rejected it, keep it
  suppressed until an explicit Restore. Consistent with `dismissed`.
- **Recreate on old image (the bug):** the `applied` update re-opens to
  `available` on the next (cache-invalidated) scan → visible + auto-updatable.
- **Auto-update safety:** `rolled_back` is not `available`, so `autoApply` never
  re-applies it (the rollback stays honored). Confirm `EffectiveAutoUpdate`/the
  open-update gate only act on `available`.
- **Restore of a `rolled_back` update → `available`** → then eligible for apply /
  auto-update again (user explicitly asked for it).

## Safety invariants (unchanged, must still hold)

- Single static binary, CGO-free; SPA via embed.FS (only the dist placeholder).
- All Docker mutation via the Job Engine; discovery/detect stay read-only except
  the store writes they already own (RemoteState, updates, services). Cache
  invalidation is a store DELETE, not a Docker action.
- Frontend: no `dangerouslySetInnerHTML`, no CDN; CSRF on mutations only.

## Out of scope

- Changing how `applied` (successfully-applied, still-running) updates are
  displayed: they stay hidden.
- Surfacing `applied` history beyond the existing service history / changelog.
- Any change to the semver scan or apply write-back behavior.
