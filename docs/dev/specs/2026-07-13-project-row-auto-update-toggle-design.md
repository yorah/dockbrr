# Project-row auto-update toggle

## Problem

Auto-update can only be turned on or off from Settings → Auto-update. Flipping it for a
project you are already looking at on the dashboard costs a navigation round-trip.

## Design

Add an "Auto" switch to the project header row of `DashboardTable`, in the right-hand
action cluster, left of the Check-all / Apply-all / Compose buttons.

**Component.** `ProjectAutoToggle`, colocated with `ProjectBulkActions` in
`web/src/components/DashboardTable.tsx`.

- `Switch` (`@/components/ui/switch`) plus a small "Auto" label.
- Wrapped in `Tooltip`: "Apply this project's updates without review, on each poll
  interval. Genuinely pinned services are never auto-updated."
- `checked={project.auto_update_enabled}`, `aria-label={`Auto-update ${project.name}`}`.

**Wiring.** Reuses `useToggleProjectAuto()` from `@/hooks/mutations`: the same mutation
Settings uses (`PUT /api/projects/:id/auto-update`, invalidates the projects query).
No backend, store, or API change.

**Event containment.** The project row's `onClick`/`onKeyDown` collapses and expands the
group. The switch container stops propagation on both, so toggling auto-update never
collapses the project.

**Scope.** Project-level only. The per-service tri-state (on / off / inherit) stays in
Settings, where a three-value control has room; a row switch cannot express "inherit".

**No confirmation.** Parity with the Settings switch: the flag is reversible and does not
mutate anything until the next poll.

**Surfaces.** Both the dashboard and `/project/$id` render `DashboardTable`, so both get
the toggle for free.

## Testing

`DashboardTable.test.tsx`:

- switch reflects `project.auto_update_enabled`;
- clicking it calls the toggle mutation with the negated value;
- clicking it does not collapse the project group.
