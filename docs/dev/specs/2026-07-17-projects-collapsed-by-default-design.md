# Projects collapsed by default (dashboard)

## Problem

On the dashboard, every project loads expanded, so all service rows are on
screen at once. With several projects this is a long, noisy list. Users want
the dashboard to load with all projects collapsed to a header row, expanding
only the ones they care about.

## Scope

- `web/src/components/DashboardTable.tsx` (state + render logic)
- `web/src/routes/dashboard.tsx` (call site: opt in)
- `web/src/routes/project.$id.tsx` (call site: pass filter signal)
- `web/src/components/DashboardTable.test.tsx` (new cases)

No Go / API changes. No persistence: collapse state stays client-side
`useState`, resets on remount. "Load up collapsed" means a new default, not a
saved preference.

The project detail route (`project.$id.tsx`) also renders `DashboardTable`, and
its `rows` include a project-header row. Collapse-by-default must NOT apply
there, or the single project's services would be hidden on load. Collapse
default is therefore dashboard-scoped via an opt-in prop.

## Data model

Keep the existing `collapsed: Set<number>` state (a project id present in the
set = that project is collapsed). Do not invert to an `expanded` set.

## New props (both default `false`)

Defaults of `false` mean the detail route and all existing tests keep today's
expanded-by-default behavior with no changes.

- `defaultCollapsed?: boolean` — dashboard passes `true`. Drives the seed
  effect below.
- `filtersActive?: boolean` — true when a search / status / only-updates filter
  is active. Drives the filter override below.

## Seed effect

Track a `seenProjects` ref (`useRef<Set<number>>`). On each rows change, for
every project id in `rows` not already in `seenProjects`:

- if `defaultCollapsed`, add the id to `collapsed`;
- mark the id seen (regardless of `defaultCollapsed`).

Consequences:

- All projects known at first render start collapsed (when `defaultCollapsed`).
- A project that appears later (SSE refetch) starts collapsed too.
- Once a user expands a project, its id is already in `seenProjects`, so the
  effect never re-collapses it against their action, even across refetches.

## Filter override

Per product decision, an active filter auto-reveals matching services and
re-collapses when the filter clears.

- Service row visibility: `shown = filtersActive || !collapsed.has(projectId)`.
- Chevron `expanded` glyph uses the same expression, so the rotation matches
  what is actually shown.

Because the override reads `filtersActive` and never mutates `collapsed`,
clearing the filter returns to the collapsed set automatically — nothing to
save or restore. Manual toggles made while a filter is active are irrelevant
(filter wins); after it clears, the pre-filter collapsed set is what remains.

## Uniform collapse

Per product decision, no special-casing for projects that have available
updates — they collapse like the rest. The collapsed header row already renders
the health dot and open-update count, so actionable projects stay flagged while
collapsed.

## Call sites

- `dashboard.tsx`: pass `defaultCollapsed` and
  `filtersActive={filters.search !== "" || filters.status !== "" || filters.onlyUpdates}`
  (the same expression already computed for `looseDefaultOpen`).
- `project.$id.tsx`: pass `filtersActive` from its own filter state; do NOT pass
  `defaultCollapsed`, so services stay visible on load while in-project search
  still reveals matches.

`looseDefaultOpen` and the Loose-group logic are unchanged.

## Testing

Existing tests render `DashboardTable` without `defaultCollapsed`, so they keep
passing unchanged. Add cases:

1. `defaultCollapsed` — all projects render collapsed on load (service rows
   absent, header rows present).
2. A project added on a rows refetch renders collapsed.
3. `filtersActive` — a service under a collapsed project is visible.
4. A manually-expanded project stays expanded across a rows refetch.
