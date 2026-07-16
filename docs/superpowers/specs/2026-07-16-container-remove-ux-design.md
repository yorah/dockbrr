# Container remove UX redesign

## Problem

Removing a stopped standalone container today requires a two-step selection flow:
a checkbox appears on each removable row inside the Loose group, and a "Remove
selected" button on the Loose header removes whatever is checked. The flow is
only wired for auto-named (Loose) containers, even though the backend allows
removing any stopped standalone container. The checkbox mechanic is heavier
than the single-container action it usually performs.

## Goal

- A per-row "Remove" button on every stopped standalone container (matching the
  backend guard), so single removals are one click with a confirm.
- A single "Remove stopped containers" button on the Loose header that removes
  all stopped loose containers at once.
- Drop the checkbox selection mechanic entirely.

## Non-goals

- No backend change. The remove endpoint and its guard
  (`kind == "standalone"` and stopped) are already correct and stay as-is.
- No change to running or compose-managed containers: they remain non-removable.

## Current state (reference)

- Endpoint: `POST /api/services/:id/remove`, hook `useRemoveContainer`
  (`web/src/hooks/mutations.ts`).
- Backend guard `internal/httpapi/lifecycle.go` `handleRemove`: 409 unless the
  project `kind == "standalone"` and the state is stopped. Re-checked in the job
  runner (source of truth).
- Frontend `web/src/components/DashboardTable.tsx`:
  - name-column checkbox rendered when `auto_named && isStopped` (buildColumns).
  - `looseSelected` state, `toggleLooseSelect`, `removeSelectedLoose`.
  - Loose header "Remove selected" button, disabled when nothing selected.

## Design

All changes are in `web/src/components/DashboardTable.tsx` plus its test.

### 1. Per-row Remove button (`ActionsCell`)

- Add a `canRemove: boolean` prop to `ActionsCell`.
- Column builder computes it per service row:
  `r.project.kind === "standalone" && isStopped(r.service.state)`.
  This intentionally covers named standalone containers (their own project
  group, outside Loose), which had no remove UI before.
- `ActionsCell` calls `useRemoveContainer()` itself, consistent with its
  existing `useCheck` / `useApply` / `useLifecycle` usage.
- When `canRemove`, render a `Trash2` ghost icon-button (tooltip "Remove") in the
  actions row. Click:
  - `e.stopPropagation()`
  - `window.confirm('Remove stopped container "<name>"? This cannot be undone.')`
  - on confirm: `removeContainer.mutate(service.id)`
  - disabled while `removeContainer.isPending`.

### 2. Loose header bulk button

- Replace the "Remove selected" button with "Remove stopped containers".
- Compute stopped loose services from the existing `looseServices` memo, further
  filtered by `isStopped(r.service.state)`.
- Disabled when that list is empty.
- Click:
  - `e.stopPropagation()`
  - confirm: `Remove <n> stopped container<s>: <names>? This cannot be undone.`
  - on confirm: `removeContainer.mutate(r.service.id)` for each.
- The `removeContainer` mutation stays hoisted in the `DashboardTable` component
  scope for this bulk handler (the per-row button uses its own instance inside
  `ActionsCell`; both point at the same endpoint/cache, which is fine).

### 3. Remove the checkbox mechanic

Delete:
- the name-column checkbox block in `buildColumns` (and the `selectable` calc).
- `looseSelected` state, `toggleLooseSelect`, `removeSelectedLoose`.
- the `looseSelected` / `onToggleLooseSelect` params threaded through
  `buildColumns` and the `useMemo` dependency on `looseSelected`.

Keep `looseServices` (now used only by the bulk handler).

## Testing

Update `web/src/components/DashboardTable.test.tsx`:

- Drop checkbox-selection assertions.
- Per-row Remove button:
  - appears for a standalone stopped service and fires the remove mutation with
    that service id (mock/confirm accepted).
  - absent for a running standalone service.
  - absent for a compose-managed service (running or stopped).
- Loose header "Remove stopped containers":
  - disabled when no stopped loose containers.
  - removes every stopped loose container on confirm.

Full check: `mise run check` (go vet + go test + vitest). TS typecheck via
`./node_modules/.bin/tsc -b --noEmit` (not `npx tsc`).

## Safety invariants

Unaffected. All removal still flows through the Job Engine via the existing
endpoint; UI never touches Docker directly (invariant 2). No compose-verb or
snapshot logic changes.
