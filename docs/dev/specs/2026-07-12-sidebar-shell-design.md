# Sidebar shell + design-token restyle

Date: 2026-07-12
Status: approved (design)

## Goal

Replace the top-header navigation with a collapsible left sidebar, add a
per-project route reachable from that sidebar, and move the ad-hoc `slate-*` colour
classes onto a CSS design-token layer so the UI reads as one system and a future
theme selector is a pure CSS change.

## Non-goals

- No new HTTP endpoints. Every value the new UI needs is already served
  (`/api/status` carries `version`; `/api/jobs` carries `project_id`, `status`,
  `created_at`).
- No route renames. `/service/$id` is untouched.
- No change to the Job Engine, scan, or any Go package. This is a `web/` change only.

## Layout

```
┌────────────┬──────────────────────────────────────────┐
│  dockbrr   │  [☰]                              [🌙]   │  topbar
├────────────┼──────────────────────────────────────────┤
│ Dashboard  │                                          │
│ Jobs       │                <Outlet />                │
│ Settings   │                                          │
│ ────────── │                                          │
│ PROJECTS   │                                          │
│ media   ②• │                                          │
│ arr       •│                                          │
│ ────────── │                                          │
│ Logout     │                                          │
│ ────────── │                                          │
│ v1.2.0     │                                          │
└────────────┴──────────────────────────────────────────┘
```

`AppLayout` becomes a flex row: `<Sidebar/>` plus a column of `<Topbar/>` and
`<main><Outlet/></main>`.

**Sidebar.** `w-60` expanded, `w-14` collapsed (icon rail), animated with
`transition-[width] duration-200`. In the rail, labels are hidden and each row shows
its icon with a Tooltip carrying the label. Sections top to bottom: nav (Dashboard /
Jobs / Settings), separator, `PROJECTS` heading plus the project list, `mt-auto`
spacer, separator, Logout, separator, version string.

**Topbar.** Thin bar above the content: collapse toggle (hamburger) on the left,
`ThemeToggle` on the far right. Page titles and page-level actions stay in the
content area, as today.

**Collapse state.** `useSidebar()` hook: `collapsed` boolean persisted to
`localStorage` under `dockbrr:sidebar`. A `matchMedia("(max-width: 767px)")`
listener forces the rail on narrow viewports; the user can still expand it, in which
case the expanded sidebar overlays the content rather than squeezing it.

**Version.** Read from the existing `useStatus()` query (`SystemStatus.version`),
rendered as `v{version}`; renders nothing while the query is pending. Hidden in the
rail.

## Project navigation

New route `/project/$id` (`src/routes/project.$id.tsx`), added to `router.tsx`.

The screen renders the project name and working dir as a header, project-scoped
`ScanAllButton` / `ApplyAllButton`, then reuses `DashboardStats` and `DashboardTable`
driven by `useDashboardRows` with `filters.project` pinned to the route param. The
project `<select>` in `Filters` is suppressed there via a new optional
`hideProject?: boolean` prop; the dashboard keeps it. Unknown id renders a "Project
not found" message. A project with no services renders "No services in this project."

The `/` dashboard is unchanged in behaviour: global stats, all rows, project
dropdown intact.

## Sidebar project rows

Each row: project name, an update-count badge (hidden when zero), and a status dot.
All values derive client-side from queries the app already mounts, no new endpoints
and no new fetches beyond `useJobs()`.

New hook `useProjectHealth()` returns, per project id, `{ updates: number; dot:
"red" | "amber" | "green" }`:

- `updates` = count of `Update` rows with `status === "available"` whose `service_id`
  belongs to the project.
- `dot` precedence, highest first:
  1. **red**: the most recent `JobRow` for that `project_id` has `status === "failed"`.
  2. **amber**: `updates > 0`.
  3. **green**: otherwise.

"Most recent job" = the job with the greatest `created_at` among `useJobs()` rows
matching the project. `useJobs()` is capped at 100 rows, so a project whose last job
has aged out of that window shows no red dot; that is acceptable and deliberate.

The SSE stream (`useEventStream`) already invalidates the projects, updates, and jobs
queries, so badges and dots update live with no extra wiring.

## Design tokens

`web/src/index.css` gains a token layer, exposed to Tailwind v4 through
`@theme inline` so `bg-card`, `border-border`, `text-muted-foreground`, `ring-ring`
and friends exist as utilities:

`--background --foreground --card --card-foreground --border --input --muted
--muted-foreground --accent --accent-foreground --primary --primary-foreground
--ring --radius`, plus semantic severity tokens `--success --warning --danger` (and
their `-foreground` pairs).

Light values on `:root`, dark values under `.dark` (the existing `@custom-variant
dark` already targets that class, and `next-themes` sets it). The accent is blue
(`blue-600` light / `blue-500` dark) but is referenced *only* through `--primary` and
`--ring`. A future theme selector is therefore a new CSS block overriding those vars
(e.g. `[data-theme="emerald"] { --primary: … }`) with zero component edits, this is
the reason for the token layer, not incidental to it.

Components migrate off hardcoded `slate-*`/`dark:slate-*` pairs onto the semantic
utilities. Affected: `AppLayout`, `DashboardStats`, `DashboardTable`, `Filters`,
`StatusBadge`, `SeverityDelta`, `ReviewDrawer`, `ApplyPanel`, the `ui/*` primitives
(`button`, `input`, `select`, `table`, `badge`, `dialog`, `drawer`, `tabs`), and the
`routes/*` screens where slate classes leak.

Visual sharpening applied during the migration:

- **Stat tiles**: a muted icon in the top-right corner, value in `tabular-nums`,
  label below in `text-muted-foreground`; hairline `border-border`; selected tile
  uses a `ring-ring` accent ring instead of a darker slate border.
- **Tables**: denser rows, `border-border` hairlines, sticky header, `hover:bg-muted/50`
  row highlight, numeric columns in `tabular-nums`.
- **Nav**: active row is an accent-tinted pill (`bg-primary text-primary-foreground`),
  matching the reference.

## Files

New:

- `src/components/layout/Sidebar.tsx`
- `src/components/layout/SidebarNav.tsx`
- `src/components/layout/SidebarProjects.tsx`
- `src/components/layout/Topbar.tsx`
- `src/components/ui/separator.tsx` (a styled `div`; no new dependency)
- `src/hooks/useSidebar.ts`
- `src/hooks/useProjectHealth.ts`
- `src/routes/project.$id.tsx`

Changed:

- `src/components/AppLayout.tsx`: rewritten as the sidebar shell
- `src/router.tsx`: register `projectRoute`
- `src/components/Filters.tsx`: `hideProject?: boolean`
- `src/index.css`: token layer
- the components listed under **Design tokens**, class migration

No new npm dependencies.

## Testing

Vitest + React Testing Library, matching the existing suite's style (assert on text
and roles, not class names).

- `Sidebar.test.tsx`: renders the three nav links, the project list, Logout, and the
  version from a mocked `/api/status`; the collapse toggle hides labels and exposes
  tooltips; the collapsed flag round-trips through `localStorage`.
- `useProjectHealth.test.tsx`: dot precedence (failed job beats pending updates beats
  healthy); badge count excludes non-`available` updates; badge is absent at zero.
- `project.$id.test.tsx`: renders only the routed project's services; bulk actions are
  project-scoped; the project `<select>` is absent; empty and not-found states.
- `__root.test.tsx`: updated for the new shell.

The existing suite must stay green unmodified except where it asserts on the old
header markup.

Gates: `cd web && npm test`, `./node_modules/.bin/tsc -b --noEmit`, `npm run build`,
plus `mise run check` for the repo-wide vet/test.

## Risks

- **Class-migration churn** touches many components at once. Mitigated by the tests
  asserting behaviour rather than classes, and by `npm run build` catching type
  breakage. The token names follow the shadcn convention the `ui/*` primitives were
  already written against, so most edits are mechanical.
- **`useJobs()` in the sidebar** adds one query to every page. It is already cached by
  TanStack Query and shared with the Jobs screen, so the cost is one extra request per
  cache window, not per navigation.
