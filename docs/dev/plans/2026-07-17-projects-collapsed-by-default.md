# Projects Collapsed By Default (Dashboard) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On the dashboard, load every top-level project collapsed to its header row; expand on click or when a filter is active.

**Architecture:** Client-only change in `DashboardTable`. Keep the existing `collapsed: Set<number>` state; add two opt-in props (`defaultCollapsed`, `filtersActive`). A seed effect collapses each newly-seen top-level project once; a `filtersActive` override reveals services while any filter is on. The dashboard route opts in; the detail route is left untouched.

**Tech Stack:** React 19, TypeScript, Vite, Vitest + Testing Library + MSW, TanStack Table/Query/Router, Tailwind v4.

## Global Constraints

- CGO-free / static-binary invariants are irrelevant here (no Go changes), but do not add any new runtime dependency: this is pure existing-stack React.
- TS typecheck must pass via `./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc` — the rtk proxy reports a false "No errors found"). `npm run build` is the reliable backstop.
- No persistence: collapse state stays in `useState`, resets per mount. "Load collapsed" means a new default, not a saved preference.
- Loose group (auto-named projects) behavior is unchanged: never seed auto-named projects into `collapsed`.
- Spec: `docs/dev/specs/2026-07-17-projects-collapsed-by-default-design.md`.

---

### Task 1: Add opt-in props + seed/override logic to DashboardTable

Dormant capability: with both new props defaulting to `false`, this task changes NO existing behavior. No route opts in yet, so the full test suite must stay green with zero test edits. TDD here is "prove nothing broke" — behavior tests come in Task 2 once the dashboard opts in.

**Files:**
- Modify: `web/src/components/DashboardTable.tsx`

**Interfaces:**
- Produces: `DashboardTableProps` gains two optional booleans:
  - `defaultCollapsed?: boolean` (default `false`)
  - `filtersActive?: boolean` (default `false`)
  These are consumed by Task 2 (`dashboard.tsx`).

- [ ] **Step 1: Add `useRef` to the React import**

`web/src/components/DashboardTable.tsx:1` currently:

```ts
import { useEffect, useMemo, useState } from "react";
```

Change to:

```ts
import { useEffect, useMemo, useRef, useState } from "react";
```

- [ ] **Step 2: Declare the two new props in the interface**

In `DashboardTableProps` (around `web/src/components/DashboardTable.tsx:73-88`), after the `looseDefaultOpen` prop, add:

```ts
  /** When true (dashboard only), every top-level project loads collapsed. */
  defaultCollapsed?: boolean;
  /** When true, an active filter reveals services regardless of collapse state. */
  filtersActive?: boolean;
```

- [ ] **Step 3: Destructure the new props with `false` defaults**

In the component signature (around `web/src/components/DashboardTable.tsx:530-539`), after `looseDefaultOpen = false,` add:

```ts
  defaultCollapsed = false,
  filtersActive = false,
```

- [ ] **Step 4: Add the `seenProjects` ref and the seed effect**

Immediately after the `collapsed` state declaration (`web/src/components/DashboardTable.tsx:540`), add the ref:

```ts
  // Ids we've already applied the collapse default to. Lets a user-expanded
  // project survive a rows refetch: once seen, the effect never re-collapses it.
  const seenProjects = useRef<Set<number>>(new Set());
```

Then, near the other effects (e.g. after the `removingIds` self-heal effect around `web/src/components/DashboardTable.tsx:570-575`), add:

```ts
  // Collapse-by-default: seed each newly-seen TOP-LEVEL project into `collapsed`
  // exactly once. Auto-named (Loose) projects are skipped — their visibility is
  // gated by looseOpen, not collapsed, so seeding them would keep their services
  // hidden even after the Loose group is opened.
  useEffect(() => {
    if (!defaultCollapsed) return;
    const fresh: number[] = [];
    for (const r of rows) {
      if (r.kind !== "project") continue;
      if (r.project.auto_named) continue;
      if (seenProjects.current.has(r.project.id)) continue;
      fresh.push(r.project.id);
    }
    if (fresh.length === 0) return;
    for (const id of fresh) seenProjects.current.add(id);
    setCollapsed((prev) => {
      const next = new Set(prev);
      for (const id of fresh) next.add(id);
      return next;
    });
  }, [rows, defaultCollapsed]);
```

- [ ] **Step 5: Apply the `filtersActive` override to service visibility**

In `visibleRows`, the `shown` predicate (`web/src/components/DashboardTable.tsx:614`) is currently:

```ts
    const shown = (r: Row) => r.kind !== "service" || !collapsed.has(r.project.id);
```

Change to:

```ts
    const shown = (r: Row) => r.kind !== "service" || filtersActive || !collapsed.has(r.project.id);
```

Then add `filtersActive` to that `useMemo`'s dependency array (`web/src/components/DashboardTable.tsx:627`):

```ts
  }, [rows, collapsed, filtersActive, groupLoose, looseOpen]);
```

- [ ] **Step 6: Sync the chevron glyph to actual visibility**

The project-header render computes `expanded` (`web/src/components/DashboardTable.tsx:725`):

```ts
              const expanded = !collapsed.has(original.project.id);
```

Change to:

```ts
              const expanded = filtersActive || !collapsed.has(original.project.id);
```

- [ ] **Step 7: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output / exit 0. (Do NOT use `npx tsc`.)

- [ ] **Step 8: Run the full web test suite — must be unchanged/green**

Run: `cd web && npm test`
Expected: PASS, same count as before. Both new props default `false`, so no existing behavior changed.

- [ ] **Step 9: Commit**

```bash
git add web/src/components/DashboardTable.tsx
git commit -m "feat(web): add defaultCollapsed + filtersActive props to DashboardTable"
```

---

### Task 2: Opt the dashboard in + migrate and extend tests

Flipping the dashboard is the breaking change: `DashboardTable.test.tsx` renders the real dashboard route, so every test that asserts a service row under a top-level project now sees it collapsed. The flip and its test migration MUST land in one commit (no green state exists between them).

**Files:**
- Modify: `web/src/routes/dashboard.tsx:106-110`
- Modify: `web/src/components/DashboardTable.test.tsx`

**Interfaces:**
- Consumes: `defaultCollapsed`, `filtersActive` from Task 1.

- [ ] **Step 1: Opt the dashboard route in**

In `web/src/routes/dashboard.tsx`, the `<DashboardTable ...>` call (lines 106-110) currently:

```tsx
        <DashboardTable
          rows={rows}
          groupLoose
          looseDefaultOpen={looseDefaultOpen}
          updatesByService={updatesByService}
```

Change to (note: `looseDefaultOpen` is already `filters.search !== "" || filters.status !== "" || filters.onlyUpdates`, see `dashboard.tsx:50` — reuse it for `filtersActive`):

```tsx
        <DashboardTable
          rows={rows}
          groupLoose
          defaultCollapsed
          looseDefaultOpen={looseDefaultOpen}
          filtersActive={looseDefaultOpen}
          updatesByService={updatesByService}
```

- [ ] **Step 2: Run the suite to see the breakage**

Run: `cd web && npm test -- DashboardTable`
Expected: many FAILs — tests that gate on `getByText("web")` (or another service name) time out because the service is now collapsed under its project header. Note the failing test names; Steps 4-5 fix them.

- [ ] **Step 3: Add the `expandProject` test helper**

At the top of `web/src/components/DashboardTable.test.tsx`, after the `renderDashboardWithRouter` helper (around line 18), add:

```ts
// Top-level projects load collapsed on the dashboard now. Waits for the project
// header button to appear (data loaded), then clicks it to reveal the services.
// The accessible name is the bare project name — distinct from "Apply all
// updates in <name>" and "Check all services in <name>".
async function expandProject(name: string) {
  await userEvent.click(await screen.findByRole("button", { name }));
}
```

- [ ] **Step 4: Rewrite the "toggles a project group" test for the new default**

Replace the body of `test("lists services and toggles a project group", ...)` (`web/src/components/DashboardTable.test.tsx:20-51`). The project in its payload is named `"app"` with service `"web"`. New assertions: collapsed on load → expand shows `web` → collapse hides it.

```ts
  renderDashboardWithRouter();
  // Collapsed by default: the header is present, the service is not.
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
  // Expand → service visible.
  await userEvent.click(screen.getByRole("button", { name: "app" }));
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Collapse again → service hidden.
  await userEvent.click(screen.getByRole("button", { name: "app" }));
  await waitFor(() => expect(screen.queryByText("web")).not.toBeInTheDocument());
```

- [ ] **Step 5: Migrate every other failing top-level-project test**

For each remaining test flagged FAIL in Step 2, the fix is mechanical: after `renderDashboardWithRouter();`, expand the project before the first service-row assertion or interaction. The standard transform, applied per failing test:

Before (the load gate that now fails):

```ts
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
```

After:

```ts
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
```

Notes for specific tests:
- Most tests use the project name `"app"` and service `"web"` — expand `"app"`.
- `test("shows an Unmanaged badge on the project header ...")` (`:157`) asserts on the header itself, which is visible while collapsed. It only needs `expandProject("app")` if it also asserts a service row; if it asserts only the badge, leave it — but re-run to confirm.
- `test("project row auto-update switch ... does not collapse the group")` (`:372`) asserts the service stays visible after toggling the switch. Expand `"app"` first, then keep its existing "still visible" assertion.
- `test("hides auto-named projects behind a collapsed Loose group ...")` (`:802`) has BOTH a top-level project (service `"web"`, `auto_named:false`) and a loose project. Its "Named project's service is visible" assertion (`:822`) now requires `expandProject("app")` first. The loose-group half (loose service hidden by default, revealed by opening the Loose group) is unchanged — do not add an expand for the loose project.
- Loose-only tests (`:833`, `:883`, `:903` — every service `auto_named:true`) are NOT affected: auto-named projects are never seeded into `collapsed`. If any still fails, it is unrelated; investigate rather than blanket-inserting an expand.

Apply the transform, re-running `cd web && npm test -- DashboardTable` after each batch until green.

- [ ] **Step 6: Add new-behavior test — collapsed on load**

Append to `web/src/components/DashboardTable.test.tsx`. Reuse the single-project `"app"`/`"web"` payload shape from the top of the file:

```ts
test("dashboard loads every top-level project collapsed", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  // Header present (data loaded) but the service row is hidden.
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
});
```

- [ ] **Step 7: Add new-behavior test — a filter reveals a collapsed project's service**

The dashboard's search box drives `filters.search`, which flows to `filtersActive`. Type into it to reveal the service without clicking the project header. The search input is a plain `Input` with `aria-label="Search"` (`web/src/components/Filters.tsx:57-62`), so select it with `getByLabelText("Search")` (NOT `getByRole("searchbox")` — it has no search role).

```ts
test("an active search reveals a service under an otherwise-collapsed project", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByRole("button", { name: "app" })).toBeInTheDocument());
  expect(screen.queryByText("web")).not.toBeInTheDocument();
  // Filter on → service visible without expanding the header.
  await userEvent.type(screen.getByLabelText("Search"), "web");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
});
```

- [ ] **Step 8: Add new-behavior test — manual expand survives a rows refetch**

Trigger a refetch by dispatching the same SSE/refetch path other tests use, or by updating the MSW handler and invalidating. Simplest reliable approach: expand, then change the `/api/projects` response to add a second project and force a refetch via the existing "Add project" → invalidate flow is heavy; instead assert the invariant directly by re-resolving projects. If the test harness has no simple refetch trigger, drive it through the global "Check all" button (which the suite already uses at `:336`/`:360`) so React Query refetches, then assert the first project is still expanded:

```ts
test("a manually-expanded project stays expanded across a refetch", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, auto_named: false,
          services: [{
            id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a",
            state: "running", pinned: false, healthcheck: false, auto_update_enabled: null,
          }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await expandProject("app");
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
  // Force a projects refetch (mirror the mechanism used by the "Check all" tests).
  await userEvent.click(screen.getByRole("button", { name: /check all/i }));
  // The seed effect must NOT re-collapse a project the user expanded.
  await waitFor(() => expect(screen.getByText("web")).toBeInTheDocument());
});
```

Note for the implementer: verify the exact refetch trigger against the existing "Check all" tests (`:320`, `:336`) — reuse whatever button name / MSW handlers they rely on so the query actually refetches. If `/check all/i` is ambiguous (global vs per-project), scope it the way those tests do.

- [ ] **Step 9: Typecheck + full suite**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: exit 0.

Run: `cd web && npm test`
Expected: PASS (all migrated + new tests green).

- [ ] **Step 10: Commit**

```bash
git add web/src/routes/dashboard.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): collapse dashboard projects by default"
```

---

### Task 3: Manual verification in the running app

Automated tests cover the logic; this task confirms the real dashboard behaves and the detail route is untouched.

**Files:** none (verification only).

- [ ] **Step 1: Build + run**

Run: `mise run run`
Expected: binary builds, server starts on `:3625`.

- [ ] **Step 2: Verify the dashboard**

Open the dashboard with at least two projects.
Expected:
- All top-level projects load collapsed (header rows only; health dot + update count still visible on the header).
- Clicking a project header expands it; clicking again collapses it.
- Typing in the search box reveals matching services under collapsed projects; clearing the search re-collapses them (except any you manually expanded).
- Opening the Loose group still reveals its services (Loose behavior unchanged).

- [ ] **Step 3: Verify the detail route is unaffected**

Navigate to a single project's detail page (`/project/<id>`).
Expected: the project's services are visible on load (not collapsed).

- [ ] **Step 4: Final full check**

Run: `mise run check`
Expected: `go vet` + `go test` + web vitest all PASS. (No Go changed, but this is the repo's standard gate.)

---

## Self-Review

- **Spec coverage:** data model kept as `collapsed` Set (Task 1 Step 4); `defaultCollapsed`/`filtersActive` props (Task 1 Steps 2-3); seed effect skipping auto-named (Task 1 Step 4); filter override on visibility + chevron (Task 1 Steps 5-6); dashboard opt-in (Task 2 Step 1); detail route untouched (no task modifies `project.$id.tsx` — matches spec "Detail route"); uniform collapse (no update-count special-casing anywhere); all four spec test cases (Task 2 Steps 4, 6, 7, 8) plus the migration (Task 2 Step 5). Covered.
- **Placeholder scan:** all code steps carry real code. The only judgment calls (search-box selector in Step 7, refetch trigger in Step 8) are flagged with explicit "verify against existing test X" instructions, not left as TODO.
- **Type consistency:** `defaultCollapsed`/`filtersActive` names and `false` defaults match between the interface (Task 1 Step 2), destructure (Step 3), and dashboard call site (Task 2 Step 1). `seenProjects` ref name consistent across Steps 4. `expandProject` helper name consistent across Task 2 Steps 3-5, 8.
