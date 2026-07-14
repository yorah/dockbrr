# Sidebar Shell + Design-Token Restyle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace dockbrr's top-header nav with a collapsible left sidebar (nav + project list + logout + version), add a per-project route, and move all colour classes onto a CSS design-token layer.

**Architecture:** `web/`-only change. `AppLayout` becomes a flex row: a `Sidebar` (expanded `w-60` / collapsed `w-14` icon rail, state in `localStorage`) plus a column of `Topbar` (collapse toggle left, theme toggle right) and `<main><Outlet/></main>`. A new `/project/$id` route reuses `DashboardStats` + `DashboardTable` with the project filter pinned to the route param. Sidebar project rows show an update-count badge and a red/amber/green health dot derived client-side from the already-mounted `useProjects` / `useUpdates` / `useJobs` queries. Colours move to CSS variables (`--background`, `--card`, `--primary`, …) exposed to Tailwind v4 via `@theme inline`, so a future theme selector is a CSS-only override.

**Tech Stack:** React 19, TypeScript, Vite, Tailwind v4, TanStack Router/Query/Table, Radix primitives, lucide-react, next-themes, Vitest + React Testing Library + MSW.

Spec: `docs/dev/specs/2026-07-12-sidebar-shell-design.md`

## Global Constraints

- **No new npm dependencies.** Every component is built from what's already in `web/package.json`.
- **No Go changes.** No new HTTP endpoints. `/api/status` already returns `version`; `/api/jobs` already returns `project_id`, `status`, `created_at`.
- **No route renames.** `/service/$id`, `/jobs`, `/settings`, `/` keep their paths.
- **Accent colour is referenced only through `--primary` / `--ring`.** Never hardcode `blue-600` in a component. A future theme must be a CSS-var override with zero component edits.
- **Tests assert on text, roles and ARIA, never on class names.**
- **TS typecheck must be run as `./node_modules/.bin/tsc -b --noEmit`**, never `npx tsc` (the rtk hook reports a false "No errors found" for `npx tsc`). `npm run build` also fails on type errors and is the reliable backstop.
- Verification commands, run from `web/`: `npm test`, `./node_modules/.bin/tsc -b --noEmit`, `npm run build`. Repo-wide: `mise run check`.
- Existing tests must stay green. The only existing test allowed to change is `src/routes/__root.test.tsx` (Task 6), which asserts on the old header markup.

---

### Task 1: Design-token layer

Introduce the CSS variables and expose them to Tailwind. No component consumes them yet. This task only proves the utilities exist and the app still builds.

**Files:**
- Modify: `web/src/index.css`
- Create: `web/src/components/ui/separator.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: Tailwind utilities `bg-background`, `text-foreground`, `bg-card`, `text-card-foreground`, `bg-muted`, `text-muted-foreground`, `bg-accent`, `text-accent-foreground`, `bg-primary`, `text-primary-foreground`, `border-border`, `border-input`, `ring-ring`, `text-success`, `text-warning`, `text-danger`, `bg-success`, `bg-warning`, `bg-danger`. Also `Separator` from `@/components/ui/separator` with props `{ className?: string }` rendering an `<div role="separator">`.

- [ ] **Step 1: Add the token layer to `web/src/index.css`**

Insert immediately after the existing `@custom-variant dark (&:where(.dark, .dark *));` line and **replace** the existing `:root { --font-sans … }` block with the block below (it keeps the two font vars and adds the colour tokens). Leave the `html { … }`, `code, pre, .font-mono { … }` rules and the entire `.changelog-body` section untouched.

```css
:root {
  --font-sans: "Inter Variable", ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto,
    Helvetica, Arial, sans-serif;
  --font-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;

  /* Surfaces */
  --background: #ffffff;
  --foreground: #0f172a;
  --card: #ffffff;
  --card-foreground: #0f172a;
  --muted: #f1f5f9;
  --muted-foreground: #64748b;
  --accent: #f1f5f9;
  --accent-foreground: #0f172a;
  --border: #e2e8f0;
  --input: #e2e8f0;

  /* Accent: the ONLY place the brand colour is named. A future theme
     overrides just these two (plus their foreground) and nothing else. */
  --primary: #2563eb;
  --primary-foreground: #f8fafc;
  --ring: #3b82f6;

  /* Semantic severity */
  --success: #16a34a;
  --warning: #d97706;
  --danger: #dc2626;

  --radius: 0.5rem;
}

.dark {
  --background: #020617;
  --foreground: #f8fafc;
  --card: #0f172a;
  --card-foreground: #f8fafc;
  --muted: #1e293b;
  --muted-foreground: #94a3b8;
  --accent: #1e293b;
  --accent-foreground: #f8fafc;
  --border: #1e293b;
  --input: #334155;

  --primary: #3b82f6;
  --primary-foreground: #f8fafc;
  --ring: #60a5fa;

  --success: #4ade80;
  --warning: #fbbf24;
  --danger: #f87171;
}

@theme inline {
  --color-background: var(--background);
  --color-foreground: var(--foreground);
  --color-card: var(--card);
  --color-card-foreground: var(--card-foreground);
  --color-muted: var(--muted);
  --color-muted-foreground: var(--muted-foreground);
  --color-accent: var(--accent);
  --color-accent-foreground: var(--accent-foreground);
  --color-border: var(--border);
  --color-input: var(--input);
  --color-primary: var(--primary);
  --color-primary-foreground: var(--primary-foreground);
  --color-ring: var(--ring);
  --color-success: var(--success);
  --color-warning: var(--warning);
  --color-danger: var(--danger);
}
```

- [ ] **Step 2: Create `web/src/components/ui/separator.tsx`**

```tsx
import { cn } from "@/lib/cn";

/** A 1px horizontal rule. `role="separator"` so tests and AT can find it. */
export function Separator({ className }: { className?: string }) {
  return <div role="separator" aria-orientation="horizontal" className={cn("h-px w-full bg-border", className)} />;
}
```

- [ ] **Step 3: Prove the utilities compile**

The Tailwind v4 build errors on unknown utilities, so a build that succeeds while a token utility is used is the test. Temporarily append `<div className="bg-card text-muted-foreground border-border ring-ring bg-primary text-primary-foreground text-warning text-danger text-success" />` inside the returned tree of `web/src/components/AppLayout.tsx` (just above `<Toaster />`).

Run from `web/`:

```bash
npm run build
```

Expected: build succeeds (`✓ built in …`). If a token utility were missing, Tailwind would fail with `Cannot apply unknown utility class`.

- [ ] **Step 4: Remove the temporary div**

Delete the `<div className="bg-card …" />` you added in Step 3 from `web/src/components/AppLayout.tsx`.

Run from `web/`:

```bash
npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: all vitest suites pass, no type errors, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/src/index.css web/src/components/ui/separator.tsx
git commit -m "feat(web): CSS design-token layer + Separator primitive"
```

---

### Task 2: `useSidebar` hook

**Files:**
- Create: `web/src/hooks/useSidebar.ts`
- Test: `web/src/hooks/useSidebar.test.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: `useSidebar(): { collapsed: boolean; toggle: () => void }` from `@/hooks/useSidebar`, and the exported constant `SIDEBAR_STORAGE_KEY = "dockbrr:sidebar"`. `collapsed === true` means the icon rail.

Behaviour: initial value is `localStorage[SIDEBAR_STORAGE_KEY] === "collapsed"`, OR forced `true` when the viewport matches `(max-width: 767px)` on mount. `toggle()` flips it and writes `"collapsed"` / `"expanded"` back to `localStorage`. A viewport that *becomes* narrow collapses the sidebar; widening does not force it back open (the user's explicit choice wins).

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/useSidebar.test.tsx`:

```tsx
import { beforeEach, describe, expect, test, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { SIDEBAR_STORAGE_KEY, useSidebar } from "@/hooks/useSidebar";

// jsdom has no matchMedia. Install a controllable stub.
let listeners: Array<(e: { matches: boolean }) => void> = [];
function stubMatchMedia(matches: boolean) {
  listeners = [];
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches,
    media: query,
    addEventListener: (_: string, cb: (e: { matches: boolean }) => void) => listeners.push(cb),
    removeEventListener: (_: string, cb: (e: { matches: boolean }) => void) => {
      listeners = listeners.filter((l) => l !== cb);
    },
  }));
}

describe("useSidebar", () => {
  beforeEach(() => {
    localStorage.clear();
    stubMatchMedia(false);
  });

  test("defaults to expanded on a wide viewport", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(false);
  });

  test("toggle flips the state and persists it", () => {
    const { result } = renderHook(() => useSidebar());
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(true);
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("collapsed");
    act(() => result.current.toggle());
    expect(result.current.collapsed).toBe(false);
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("expanded");
  });

  test("restores the persisted collapsed state", () => {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, "collapsed");
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(true);
  });

  test("a narrow viewport forces the rail on mount", () => {
    localStorage.setItem(SIDEBAR_STORAGE_KEY, "expanded");
    stubMatchMedia(true);
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(true);
  });

  test("shrinking the viewport collapses, widening does not re-expand", () => {
    const { result } = renderHook(() => useSidebar());
    expect(result.current.collapsed).toBe(false);
    act(() => listeners.forEach((cb) => cb({ matches: true })));
    expect(result.current.collapsed).toBe(true);
    act(() => listeners.forEach((cb) => cb({ matches: false })));
    expect(result.current.collapsed).toBe(true);
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run from `web/`:

```bash
npm test -- src/hooks/useSidebar.test.tsx
```

Expected: FAIL: `Failed to resolve import "@/hooks/useSidebar"`.

- [ ] **Step 3: Write the implementation**

Create `web/src/hooks/useSidebar.ts`:

```ts
import { useCallback, useEffect, useState } from "react";

export const SIDEBAR_STORAGE_KEY = "dockbrr:sidebar";

const NARROW = "(max-width: 767px)";

function initial(): boolean {
  if (typeof window === "undefined") return false;
  if (window.matchMedia?.(NARROW).matches) return true;
  return window.localStorage.getItem(SIDEBAR_STORAGE_KEY) === "collapsed";
}

/**
 * Collapsed = icon rail. Persisted to localStorage so it survives reloads,
 * like the theme preference. A viewport that becomes narrow forces the rail;
 * widening again leaves the user's choice alone.
 */
export function useSidebar(): { collapsed: boolean; toggle: () => void } {
  const [collapsed, setCollapsed] = useState<boolean>(initial);

  useEffect(() => {
    const mql = window.matchMedia?.(NARROW);
    if (!mql) return;
    const onChange = (e: { matches: boolean }) => {
      if (e.matches) setCollapsed(true);
    };
    mql.addEventListener("change", onChange as (e: MediaQueryListEvent) => void);
    return () => mql.removeEventListener("change", onChange as (e: MediaQueryListEvent) => void);
  }, []);

  const toggle = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      window.localStorage.setItem(SIDEBAR_STORAGE_KEY, next ? "collapsed" : "expanded");
      return next;
    });
  }, []);

  return { collapsed, toggle };
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run from `web/`:

```bash
npm test -- src/hooks/useSidebar.test.tsx && ./node_modules/.bin/tsc -b --noEmit
```

Expected: 5 tests pass, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useSidebar.ts web/src/hooks/useSidebar.test.tsx
git commit -m "feat(web): useSidebar hook, collapse state, persisted + mobile-aware"
```

---

### Task 3: `useProjectHealth` hook

**Files:**
- Create: `web/src/hooks/useProjectHealth.ts`
- Test: `web/src/hooks/useProjectHealth.test.tsx`

**Interfaces:**
- Consumes: `useProjects()`, `useUpdates()`, `useJobs()` from `@/hooks/queries`; types `Project`, `Update`, `JobRow` from `@/api/types`.
- Produces:
  - `export type Dot = "red" | "amber" | "green";`
  - `export interface ProjectHealth { updates: number; dot: Dot }`
  - `export function projectHealth(projects: Project[], updates: Update[], jobs: JobRow[]): Map<number, ProjectHealth>`: the pure function, unit-tested directly.
  - `export function useProjectHealth(): { projects: Project[]; health: Map<number, ProjectHealth> }`. The hook wrapper the sidebar uses.

Rules (highest precedence first): **red** if the most recent `JobRow` for that `project_id` has `status === "failed"`; **amber** if the project has ≥1 `Update` with `status === "available"` on one of its services; **green** otherwise. "Most recent" = greatest `created_at` string (RFC3339, lexicographically sortable). `updates` is the count of `available` updates.

- [ ] **Step 1: Write the failing test**

Create `web/src/hooks/useProjectHealth.test.tsx`:

```tsx
import { describe, expect, test } from "vitest";
import { projectHealth } from "@/hooks/useProjectHealth";
import type { JobRow, Project, Service, Update } from "@/api/types";

const svc = (id: number, name: string): Service => ({
  id, name, image_ref: "nginx:1", current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null,
  check_status: "ok", last_checked: "2026-07-12T10:00:00Z",
});
const proj = (id: number, name: string, services: Service[]): Project => ({
  id, name, kind: "compose", working_dir: `/srv/${name}`,
  auto_update_enabled: false, unmanaged: false, services,
});
const upd = (id: number, service_id: number, status: string): Update => ({
  id, service_id, from_digest: "sha256:a", to_digest: "sha256:b",
  from_version: "1.0.0", to_version: "1.1.0", tag: "1.1.0", severity: "minor",
  changelog_url: "", changelog_text: "", status, detected_at: "2026-07-12T10:00:00Z",
});
const job = (id: number, project_id: number | null, status: string, created_at: string): JobRow => ({
  id, type: "apply", status, scope: "project", exit_code: null, error: "",
  project_id, service_id: null, requested_by: "admin", created_at, finished_at: created_at,
});

describe("projectHealth", () => {
  test("green when there are no updates and no failed job", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const h = projectHealth([p], [], [])!.get(1)!;
    expect(h).toEqual({ updates: 0, dot: "green" });
  });

  test("amber with a pending update; count excludes non-available updates", () => {
    const p = proj(1, "media", [svc(10, "plex"), svc(11, "sonarr")]);
    const updates = [upd(1, 10, "available"), upd(2, 11, "dismissed"), upd(3, 11, "applied")];
    const h = projectHealth([p], updates, [])!.get(1)!;
    expect(h).toEqual({ updates: 1, dot: "amber" });
  });

  test("red when the project's most recent job failed, beats a pending update", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [
      job(1, 1, "success", "2026-07-12T09:00:00Z"),
      job(2, 1, "failed", "2026-07-12T11:00:00Z"),
    ];
    const h = projectHealth([p], [upd(1, 10, "available")], jobs)!.get(1)!;
    expect(h).toEqual({ updates: 1, dot: "red" });
  });

  test("an older failed job is superseded by a newer success", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [
      job(1, 1, "failed", "2026-07-12T09:00:00Z"),
      job(2, 1, "success", "2026-07-12T11:00:00Z"),
    ];
    expect(projectHealth([p], [], jobs)!.get(1)!.dot).toBe("green");
  });

  test("jobs belonging to another project (or to none) do not colour this one", () => {
    const p = proj(1, "media", [svc(10, "plex")]);
    const jobs = [job(1, 2, "failed", "2026-07-12T11:00:00Z"), job(2, null, "failed", "2026-07-12T12:00:00Z")];
    expect(projectHealth([p], [], jobs)!.get(1)!.dot).toBe("green");
  });

  test("an update on a service of another project does not colour this one", () => {
    const a = proj(1, "media", [svc(10, "plex")]);
    const b = proj(2, "arr", [svc(20, "radarr")]);
    const map = projectHealth([a, b], [upd(1, 20, "available")], []);
    expect(map.get(1)).toEqual({ updates: 0, dot: "green" });
    expect(map.get(2)).toEqual({ updates: 1, dot: "amber" });
  });
});
```

- [ ] **Step 2: Run the test to verify it fails**

Run from `web/`:

```bash
npm test -- src/hooks/useProjectHealth.test.tsx
```

Expected: FAIL: `Failed to resolve import "@/hooks/useProjectHealth"`.

- [ ] **Step 3: Write the implementation**

Create `web/src/hooks/useProjectHealth.ts`:

```ts
import { useMemo } from "react";
import { useJobs, useProjects, useUpdates } from "./queries";
import type { JobRow, Project, Update } from "@/api/types";

export type Dot = "red" | "amber" | "green";
export interface ProjectHealth {
  updates: number;
  dot: Dot;
}

/**
 * Sidebar health per project, derived from data the app already has.
 *
 * red: the project's most recent job failed
 * amber: the project has open updates
 * green: otherwise
 *
 * `useJobs()` is capped at 100 rows, so a project whose last job has aged out
 * of that window shows no red dot. Deliberate: the alternative is a per-project
 * endpoint, and a stale failure is not worth one.
 */
export function projectHealth(projects: Project[], updates: Update[], jobs: JobRow[]): Map<number, ProjectHealth> {
  const open = new Map<number, number>(); // service id -> 1 if it has an open update
  for (const u of updates) {
    if (u.status === "available") open.set(u.service_id, 1);
  }

  // created_at is RFC3339 from the API, so string compare orders it correctly.
  const latest = new Map<number, JobRow>();
  for (const j of jobs) {
    if (j.project_id == null) continue;
    const prev = latest.get(j.project_id);
    if (!prev || j.created_at > prev.created_at) latest.set(j.project_id, j);
  }

  const out = new Map<number, ProjectHealth>();
  for (const p of projects) {
    const count = p.services.reduce((n, s) => n + (open.has(s.id) ? 1 : 0), 0);
    const failed = latest.get(p.id)?.status === "failed";
    out.set(p.id, { updates: count, dot: failed ? "red" : count > 0 ? "amber" : "green" });
  }
  return out;
}

export function useProjectHealth(): { projects: Project[]; health: Map<number, ProjectHealth> } {
  const projects = useProjects();
  const updates = useUpdates();
  const jobs = useJobs();
  const health = useMemo(
    () => projectHealth(projects.data ?? [], updates.data ?? [], jobs.data ?? []),
    [projects.data, updates.data, jobs.data],
  );
  return { projects: projects.data ?? [], health };
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run from `web/`:

```bash
npm test -- src/hooks/useProjectHealth.test.tsx && ./node_modules/.bin/tsc -b --noEmit
```

Expected: 6 tests pass, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useProjectHealth.ts web/src/hooks/useProjectHealth.test.tsx
git commit -m "feat(web): useProjectHealth: per-project update count + status dot"
```

---

### Task 4: Test harness for full-app rendering

The sidebar and the project route can only be tested inside a router. Give the suite a `renderApp(path)` helper backed by a **fresh** router per test (the singleton in `router.tsx` carries state between tests), and give MSW defaults for the queries the shell mounts on every page.

**Files:**
- Modify: `web/src/router.tsx`
- Modify: `web/src/test/utils.tsx`
- Modify: `web/src/test/msw.ts`

**Interfaces:**
- Consumes: `rootRoute` and the route objects already in `router.tsx`.
- Produces:
  - `export const routeTree` from `@/router` (in addition to the existing `router` export).
  - `export function renderApp(initialPath?: string): { client: QueryClient } & RenderResult` from `@/test/utils`: renders the whole app tree at `initialPath` (default `"/"`) with a fresh `QueryClient` and a fresh memory-history router.
  - MSW default handlers for `/api/projects` → `[]`, `/api/updates` → `[]`, `/api/jobs` → `[]`, `/api/status` → `{ last_check_all: "", poll_interval_seconds: 300, docker_reachable: true, version: "0.0.0-test" }`. Individual tests override these with `server.use(...)`.

- [ ] **Step 1: Export the route tree from `web/src/router.tsx`**

Change the single line

```ts
const routeTree = rootRoute.addChildren([dashboardRoute, serviceRoute, settingsRoute, jobsRoute]);
```

to

```ts
// Exported so tests can build a fresh router (with memory history) per test, // the `router` singleton below carries navigation state across test cases.
export const routeTree = rootRoute.addChildren([dashboardRoute, serviceRoute, settingsRoute, jobsRoute]);
```

- [ ] **Step 2: Add `renderApp` to `web/src/test/utils.tsx`**

Replace the whole file with:

```tsx
import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { render } from "@testing-library/react";
import { makeQueryClient } from "@/api/queryClient";
import { routeTree } from "@/router";

export function renderWithClient(ui: ReactNode) {
  const client = makeQueryClient();
  const result = render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
  return { client, ...result };
}

/**
 * Render the whole app (AuthGate + AppLayout shell + routes) at `initialPath`.
 * A fresh router per call keeps navigation state from leaking between tests.
 */
export function renderApp(initialPath = "/") {
  const client = makeQueryClient();
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [initialPath] }),
  });
  const result = render(
    <QueryClientProvider client={client}>
      {/* The app's `Register` interface types the singleton router; a per-test
          router has the same route tree but is structurally a distinct type. */}
      <RouterProvider router={router as never} />
    </QueryClientProvider>,
  );
  return { client, ...result };
}
```

- [ ] **Step 3: Add MSW defaults in `web/src/test/msw.ts`**

Replace the `handlers` array with:

```ts
// Default handlers other tests override with server.use(...). The shell
// (sidebar + topbar) mounts projects/updates/jobs/status on every route, so
// these need a default or every app-tree test 404s on them.
export const handlers = [
  http.get("/api/setup/status", () => HttpResponse.json({ needs_setup: false })),
  http.get("/api/auth/me", () => HttpResponse.json({ username: "admin" })),
  http.get("/api/projects", () => HttpResponse.json([])),
  http.get("/api/updates", () => HttpResponse.json([])),
  http.get("/api/jobs", () => HttpResponse.json([])),
  http.get("/api/status", () =>
    HttpResponse.json({
      last_check_all: "",
      poll_interval_seconds: 300,
      docker_reachable: true,
      version: "0.0.0-test",
    })),
];
```

- [ ] **Step 4: Verify the existing suite still passes**

The new defaults are additive, and every existing test that cares overrides them.

Run from `web/`:

```bash
npm test && ./node_modules/.bin/tsc -b --noEmit
```

Expected: the full suite passes, no type errors.

- [ ] **Step 5: Commit**

```bash
git add web/src/router.tsx web/src/test/utils.tsx web/src/test/msw.ts
git commit -m "test(web): renderApp harness + MSW defaults for shell queries"
```

---

### Task 5: Sidebar and Topbar components

Build the shell's pieces in isolation. `AppLayout` still uses the old header after this task, Task 6 swaps it in. Nothing renders these yet, so the gate here is the type check plus the component test from Task 6; keep this task's verification to typecheck + build.

**Files:**
- Create: `web/src/components/layout/SidebarNav.tsx`
- Create: `web/src/components/layout/SidebarProjects.tsx`
- Create: `web/src/components/layout/Sidebar.tsx`
- Create: `web/src/components/layout/Topbar.tsx`

**Interfaces:**
- Consumes: `useSidebar` (Task 2), `useProjectHealth` (Task 3), `Separator` (Task 1), `useStatus` from `@/hooks/queries`, `useLogout` from `@/hooks/mutations`, `Button`, `Tooltip`/`TooltipContent`/`TooltipTrigger`, `ThemeToggle`, `cn`.
- Produces:
  - `<SidebarNav collapsed={boolean} />`
  - `<SidebarProjects collapsed={boolean} />`
  - `<Sidebar collapsed={boolean} />`
  - `<Topbar collapsed={boolean} onToggle={() => void} />`

- [ ] **Step 1: Create `web/src/components/layout/SidebarNav.tsx`**

```tsx
import { Link } from "@tanstack/react-router";
import { LayoutDashboard, ListChecks, Settings } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { cn } from "@/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";

const ITEMS: Array<{ to: string; label: string; icon: LucideIcon; exact?: boolean }> = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, exact: true },
  { to: "/jobs", label: "Jobs", icon: ListChecks },
  { to: "/settings", label: "Settings", icon: Settings },
];

/** Shared row styling for every sidebar entry (nav items and project rows). */
export const rowClass =
  "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground";
export const rowActiveClass =
  "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm font-medium bg-primary text-primary-foreground hover:bg-primary";

export function SidebarNav({ collapsed }: { collapsed: boolean }) {
  return (
    <nav className="flex flex-col gap-1 px-2" aria-label="Main">
      {ITEMS.map(({ to, label, icon: Icon, exact }) => {
        const link = (
          <Link
            key={to}
            to={to}
            className={cn(rowClass, collapsed && "justify-center px-0")}
            activeProps={{ className: cn(rowActiveClass, collapsed && "justify-center px-0") }}
            activeOptions={exact ? { exact: true } : undefined}
            aria-label={collapsed ? label : undefined}
          >
            <Icon className="h-4 w-4 shrink-0" />
            {!collapsed && <span className="truncate">{label}</span>}
          </Link>
        );
        if (!collapsed) return link;
        return (
          <Tooltip key={to}>
            <TooltipTrigger asChild>{link}</TooltipTrigger>
            <TooltipContent side="right">{label}</TooltipContent>
          </Tooltip>
        );
      })}
    </nav>
  );
}
```

- [ ] **Step 2: Create `web/src/components/layout/SidebarProjects.tsx`**

```tsx
import { Link } from "@tanstack/react-router";
import { cn } from "@/lib/cn";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { useProjectHealth, type Dot } from "@/hooks/useProjectHealth";
import { rowActiveClass, rowClass } from "@/components/layout/SidebarNav";

const DOT_CLASS: Record<Dot, string> = {
  red: "bg-danger",
  amber: "bg-warning",
  green: "bg-success",
};
const DOT_LABEL: Record<Dot, string> = {
  red: "last job failed",
  amber: "updates available",
  green: "healthy",
};

export function SidebarProjects({ collapsed }: { collapsed: boolean }) {
  const { projects, health } = useProjectHealth();
  if (projects.length === 0) return null;

  return (
    <div className="flex min-h-0 flex-col">
      {!collapsed && (
        <span className="px-3 py-2 text-xs font-medium tracking-wider text-muted-foreground uppercase">
          Projects
        </span>
      )}
      <nav className="flex flex-col gap-1 overflow-y-auto px-2" aria-label="Projects">
        {projects.map((p) => {
          const h = health.get(p.id) ?? { updates: 0, dot: "green" as Dot };
          const label = collapsed
            ? `${p.name}: ${DOT_LABEL[h.dot]}`
            : `${p.name}, ${DOT_LABEL[h.dot]}`;
          const link = (
            <Link
              to="/project/$id"
              params={{ id: String(p.id) }}
              className={cn(rowClass, collapsed && "justify-center px-0")}
              activeProps={{ className: cn(rowActiveClass, collapsed && "justify-center px-0") }}
              aria-label={label}
            >
              {collapsed ? (
                <span aria-hidden className="text-xs font-semibold uppercase">
                  {p.name.slice(0, 2)}
                </span>
              ) : (
                <span className="truncate">{p.name}</span>
              )}
              {!collapsed && h.updates > 0 && (
                <span className="ml-auto rounded-full bg-warning/15 px-1.5 py-0.5 text-xs font-medium text-warning tabular-nums">
                  {h.updates}
                </span>
              )}
              <span
                aria-hidden
                className={cn(
                  "h-2 w-2 shrink-0 rounded-full",
                  DOT_CLASS[h.dot],
                  !collapsed && h.updates === 0 && "ml-auto",
                  collapsed && "absolute top-1 right-1",
                )}
              />
            </Link>
          );
          if (!collapsed) return <div key={p.id}>{link}</div>;
          return (
            <Tooltip key={p.id}>
              <TooltipTrigger asChild>
                <div className="relative">{link}</div>
              </TooltipTrigger>
              <TooltipContent side="right">
                {p.name}
                {h.updates > 0 && `: ${h.updates} update${h.updates === 1 ? "" : "s"}`}
              </TooltipContent>
            </Tooltip>
          );
        })}
      </nav>
    </div>
  );
}
```

- [ ] **Step 3: Create `web/src/components/layout/Sidebar.tsx`**

```tsx
import { LogOut } from "lucide-react";
import { cn } from "@/lib/cn";
import { Separator } from "@/components/ui/separator";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { SidebarNav, rowClass } from "@/components/layout/SidebarNav";
import { SidebarProjects } from "@/components/layout/SidebarProjects";
import { useStatus } from "@/hooks/queries";
import { useLogout } from "@/hooks/mutations";

export function Sidebar({ collapsed }: { collapsed: boolean }) {
  const logout = useLogout();
  const status = useStatus();

  const logoutButton = (
    <button
      type="button"
      onClick={() => logout.mutate()}
      disabled={logout.isPending}
      className={cn(rowClass, "disabled:opacity-50", collapsed && "justify-center px-0")}
    >
      <LogOut className="h-4 w-4 shrink-0" />
      {!collapsed && <span>Logout</span>}
      {collapsed && <span className="sr-only">Logout</span>}
    </button>
  );

  return (
    <aside
      className={cn(
        "flex shrink-0 flex-col gap-2 border-r border-border bg-card py-3 transition-[width] duration-200",
        collapsed ? "w-14" : "w-60",
      )}
    >
      <div className={cn("flex items-center gap-2 px-4 pb-1", collapsed && "justify-center px-0")}>
        <img src="/favicon.svg" alt="" className="h-5 w-5 shrink-0" />
        {!collapsed && <span className="text-base font-semibold">dockbrr</span>}
      </div>

      <SidebarNav collapsed={collapsed} />

      <Separator className="my-1" />

      <SidebarProjects collapsed={collapsed} />

      <div className="mt-auto flex flex-col gap-2">
        <Separator />
        <div className="px-2">
          {collapsed ? (
            <Tooltip>
              <TooltipTrigger asChild>{logoutButton}</TooltipTrigger>
              <TooltipContent side="right">Logout</TooltipContent>
            </Tooltip>
          ) : (
            logoutButton
          )}
        </div>
        <Separator />
        {status.data && (
          <span className={cn("px-4 text-xs text-muted-foreground", collapsed && "px-0 text-center text-[10px]")}>
            v{status.data.version}
          </span>
        )}
      </div>
    </aside>
  );
}
```

- [ ] **Step 4: Create `web/src/components/layout/Topbar.tsx`**

```tsx
import { PanelLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { ThemeToggle } from "@/components/ThemeToggle";

export function Topbar({ collapsed, onToggle }: { collapsed: boolean; onToggle: () => void }) {
  return (
    <header className="flex items-center justify-between border-b border-border px-3 py-2">
      <Button
        type="button"
        variant="ghost"
        size="icon"
        onClick={onToggle}
        aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
        aria-expanded={!collapsed}
      >
        <PanelLeft className="h-4 w-4" />
      </Button>
      <ThemeToggle />
    </header>
  );
}
```

- [ ] **Step 5: Typecheck and build**

Run from `web/`:

```bash
./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: no type errors, build succeeds. (`/project/$id` does not exist yet as a route, TanStack Router's `Link to` is typed against the registered tree, so **if this errors on `to="/project/$id"`, that is expected**: do Task 6 next and re-run. To keep this task independently green, Task 6 registers the route; if you are running tasks strictly in order, accept a red typecheck here only for that specific `Link` and confirm it goes green after Task 6 Step 2.)

- [ ] **Step 6: Commit**

```bash
git add web/src/components/layout
git commit -m "feat(web): Sidebar, SidebarNav, SidebarProjects, Topbar components"
```

---

### Task 6: Wire the shell into `AppLayout` + register the project route

**Files:**
- Modify: `web/src/components/AppLayout.tsx`
- Create: `web/src/routes/project.$id.tsx`
- Modify: `web/src/router.tsx`
- Modify: `web/src/components/Filters.tsx`
- Modify: `web/src/routes/__root.test.tsx`
- Test: `web/src/components/layout/Sidebar.test.tsx`
- Test: `web/src/routes/project.$id.test.tsx`

**Interfaces:**
- Consumes: `Sidebar`, `Topbar` (Task 5); `renderApp` (Task 4); `useDashboardRows`, `DashboardStats`, `DashboardTable`, `Filters`, `ScanAllButton`, `ApplyAllButton`, `ReviewDrawer`, `ApplyPanel` (existing).
- Produces:
  - `ProjectScreen` from `@/routes/project.$id`, registered at path `/project/$id`.
  - `Filters` gains `hideProject?: boolean` (default `false`).

Existing signatures this task depends on (do not change them):
`ScanAllButton({ label?, ariaLabel? })`; `ApplyAllButton({ updates, onApplied, scopeNoun, label?, ariaLabel? })`; `DashboardStats({ projects, updates, rows, status, activeStatus, onFilter })`; `DashboardTable({ rows, updatesByService, onApplied, onReview })`; `useDashboardRows(filters) -> { rows, projects, updates, updatesByService, isLoading, isError }`; `FilterState = { onlyUpdates, project, status, search, showRemoved }`.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/layout/Sidebar.test.tsx`:

```tsx
import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderApp } from "@/test/utils";
import { SIDEBAR_STORAGE_KEY } from "@/hooks/useSidebar";

const service = { id: 10, name: "plex", image_ref: "plex:1", current_digest: "sha256:a", state: "running", pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null, check_status: "ok", last_checked: "2026-07-12T10:00:00Z" };
const project = { id: 1, name: "media", kind: "compose", working_dir: "/srv/media", auto_update_enabled: false, unmanaged: false, services: [service] };

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
  }));
  server.use(http.get("/api/projects", () => HttpResponse.json([project])));
});

describe("Sidebar", () => {
  test("renders nav, projects, logout and the version", async () => {
    server.use(http.get("/api/status", () => HttpResponse.json({
      last_check_all: "", poll_interval_seconds: 300, docker_reachable: true, version: "1.4.2",
    })));
    renderApp("/");

    expect(await screen.findByRole("link", { name: "Dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Jobs" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings" })).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /^media,/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /logout/i })).toBeInTheDocument();
    expect(await screen.findByText("v1.4.2")).toBeInTheDocument();
  });

  test("shows an update-count badge only when the project has open updates", async () => {
    server.use(http.get("/api/updates", () => HttpResponse.json([
      { id: 1, service_id: 10, from_digest: "sha256:a", to_digest: "sha256:b", from_version: "1.0.0", to_version: "1.1.0", tag: "1.1.0", severity: "minor", changelog_url: "", changelog_text: "", status: "available", detected_at: "2026-07-12T10:00:00Z" },
    ])));
    renderApp("/");
    const link = await screen.findByRole("link", { name: /^media, updates available/ });
    await waitFor(() => expect(link).toHaveTextContent("1"));
  });

  test("collapsing hides the labels and persists the state", async () => {
    const user = userEvent.setup();
    renderApp("/");
    expect(await screen.findByRole("link", { name: "Dashboard" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /collapse sidebar/i }));

    await waitFor(() => expect(screen.queryByText("Projects")).not.toBeInTheDocument());
    expect(localStorage.getItem(SIDEBAR_STORAGE_KEY)).toBe("collapsed");
    // The nav link survives as an icon with its accessible name intact.
    expect(screen.getByRole("link", { name: "Dashboard" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /expand sidebar/i })).toBeInTheDocument();
  });

  test("clicking a project navigates to its page", async () => {
    const user = userEvent.setup();
    renderApp("/");
    await user.click(await screen.findByRole("link", { name: /^media,/ }));
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
  });
});
```

Create `web/src/routes/project.$id.test.tsx`:

```tsx
import { beforeEach, describe, expect, test, vi } from "vitest";
import { screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { server } from "@/test/msw";
import { renderApp } from "@/test/utils";

const svc = (id: number, name: string) => ({
  id, name, image_ref: `${name}:1`, current_digest: "sha256:a", state: "running",
  pinned: false, drifted: false, healthcheck: true, auto_update_enabled: null,
  check_status: "ok", last_checked: "2026-07-12T10:00:00Z",
});
const projects = [
  { id: 1, name: "media", kind: "compose", working_dir: "/srv/media", auto_update_enabled: false, unmanaged: false, services: [svc(10, "plex")] },
  { id: 2, name: "arr", kind: "compose", working_dir: "/srv/arr", auto_update_enabled: false, unmanaged: false, services: [svc(20, "radarr")] },
];

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({
    matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
  }));
  server.use(http.get("/api/projects", () => HttpResponse.json(projects)));
});

describe("project route", () => {
  test("shows only the routed project's services", async () => {
    renderApp("/project/1");
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
    expect(await screen.findByText("plex")).toBeInTheDocument();
    expect(screen.queryByText("radarr")).not.toBeInTheDocument();
  });

  test("hides the project filter (the sidebar is the project switcher)", async () => {
    renderApp("/project/1");
    expect(await screen.findByRole("heading", { name: "media" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Filter by project")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Filter by status")).toBeInTheDocument();
  });

  test("bulk actions are scoped to the project", async () => {
    renderApp("/project/1");
    expect(await screen.findByRole("button", { name: /check all services in media/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /apply all available updates in media/i })).toBeInTheDocument();
  });

  test("an unknown project id renders a not-found message", async () => {
    renderApp("/project/99");
    expect(await screen.findByText(/project not found/i)).toBeInTheDocument();
  });

  test("a project with no services renders an empty state", async () => {
    server.use(http.get("/api/projects", () => HttpResponse.json([
      { id: 3, name: "empty", kind: "compose", working_dir: "/srv/empty", auto_update_enabled: false, unmanaged: false, services: [] },
    ])));
    renderApp("/project/3");
    expect(await screen.findByText(/no services in this project/i)).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run from `web/`:

```bash
npm test -- src/components/layout/Sidebar.test.tsx src/routes/project.$id.test.tsx
```

Expected: FAIL: `Failed to resolve import "@/routes/project.$id"` / no Dashboard link (the old header renders a Dashboard link too, so the Sidebar tests may partly pass; the version, project rows and collapse assertions fail).

- [ ] **Step 3: Add `hideProject` to `web/src/components/Filters.tsx`**

Add the prop to the interface:

```tsx
export interface FiltersProps {
  value: FilterState;
  onChange: (next: FilterState) => void;
  projects: Project[];
  /** Right-aligned action buttons (e.g. global Check all / Apply all). */
  actions?: ReactNode;
  /** Suppress the project <select>: the project route pins the project already. */
  hideProject?: boolean;
}
```

Change the signature:

```tsx
export function Filters({ value, onChange, projects, actions, hideProject = false }: FiltersProps) {
```

and wrap the project `<Select>` (the one whose trigger has `aria-label="Filter by project"`) in the guard, leaving its body byte-for-byte as it is today:

```tsx
      {!hideProject && (
        <Select
          value={value.project || "any"}
          onValueChange={(v) => onChange({ ...value, project: v === "any" ? "" : v })}
        >
          <SelectTrigger className="w-40" aria-label="Filter by project">
            <SelectValue placeholder="Any project" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="any">Any project</SelectItem>
            {projects.map((p) => (
              <SelectItem key={p.id} value={String(p.id)}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
```

- [ ] **Step 4: Create `web/src/routes/project.$id.tsx`**

```tsx
import { useState } from "react";
import { useParams } from "@tanstack/react-router";
import { Filters } from "@/components/Filters";
import { ApplyAllButton, ScanAllButton } from "@/components/BulkActions";
import { DashboardStats } from "@/components/DashboardStats";
import { DashboardTable } from "@/components/DashboardTable";
import { ReviewDrawer } from "@/components/ReviewDrawer";
import { ApplyPanel } from "@/components/ApplyPanel";
import { useDashboardRows, type FilterState } from "@/hooks/useDashboardRows";
import { useStatus } from "@/hooks/queries";
import type { Project, Service, Update } from "@/api/types";

interface Selected {
  update: Update;
  service: Service;
  project: Project;
}

export function ProjectRoute() {
  const { id } = useParams({ from: "/project/$id" });
  const [filters, setFilters] = useState<FilterState>({
    onlyUpdates: false,
    project: id,
    status: "",
    search: "",
    showRemoved: false,
  });
  const [selected, setSelected] = useState<Selected | null>(null);
  const [appliedJobId, setAppliedJobId] = useState<number | null>(null);

  // The project filter is pinned to the route param: a status/search change
  // must never widen the scope back to every project.
  const scoped = { ...filters, project: id };
  const { rows, projects, updates, updatesByService, isLoading, isError } = useDashboardRows(scoped);
  const statusQuery = useStatus();

  const project = projects.find((p) => String(p.id) === id);
  const projectUpdates = project
    ? updates.filter((u) => project.services.some((s) => s.id === u.service_id))
    : [];

  if (isLoading) {
    return (
      <div className="space-y-2" role="status" aria-label="Loading project">
        {Array.from({ length: 6 }).map((_, i) => (
          <div key={i} className="h-9 animate-pulse rounded-md bg-muted" />
        ))}
      </div>
    );
  }
  if (isError) return <p className="text-sm text-danger">Failed to load project data.</p>;
  if (!project) return <p className="text-sm text-muted-foreground">Project not found.</p>;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="mb-4">
        <h1 className="text-xl font-semibold">{project.name}</h1>
        <p className="font-mono text-xs text-muted-foreground">{project.working_dir}</p>
      </div>

      <DashboardStats
        projects={[project]}
        updates={projectUpdates}
        rows={rows}
        status={statusQuery.data}
        activeStatus={filters.status}
        onFilter={(patch) => setFilters({ ...scoped, status: "", ...patch })}
      />
      <Filters
        value={scoped}
        onChange={setFilters}
        projects={projects}
        hideProject
        actions={
          <>
            <ScanAllButton ariaLabel={`Check all services in ${project.name}`} />
            <ApplyAllButton
              updates={projectUpdates}
              onApplied={setAppliedJobId}
              scopeNoun={`in "${project.name}"`}
              ariaLabel={`Apply all available updates in ${project.name}`}
            />
          </>
        }
      />

      {project.services.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          No services in this project.
        </div>
      )}
      {project.services.length > 0 && rows.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-8 text-center text-sm text-muted-foreground">
          No services match the current filters.
        </div>
      )}
      {rows.length > 0 && (
        <DashboardTable
          rows={rows}
          updatesByService={updatesByService}
          onApplied={setAppliedJobId}
          onReview={(update, service, proj) => {
            if (!update) return;
            setSelected({ update, service, project: proj });
          }}
        />
      )}

      {selected && (
        <ReviewDrawer
          update={selected.update}
          service={selected.service}
          project={selected.project}
          onClose={() => setSelected(null)}
          onApplied={(jobId) => {
            setAppliedJobId(jobId);
            setSelected(null);
          }}
        />
      )}

      {appliedJobId !== null && (
        <ApplyPanel key={appliedJobId} jobId={appliedJobId} onClose={() => setAppliedJobId(null)} />
      )}
    </div>
  );
}

// Stable named export for web/src/router.tsx.
export const ProjectScreen = ProjectRoute;
```

- [ ] **Step 5: Register the route in `web/src/router.tsx`**

Add the import next to the existing route imports:

```ts
import { ProjectScreen } from "@/routes/project.$id";
```

Add the route definition below `serviceRoute`:

```ts
const projectRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/project/$id",
  component: ProjectScreen,
});
```

and add it to the tree:

```ts
export const routeTree = rootRoute.addChildren([dashboardRoute, serviceRoute, projectRoute, settingsRoute, jobsRoute]);
```

- [ ] **Step 6: Rewrite `web/src/components/AppLayout.tsx`**

Replace the whole file with:

```tsx
import { Outlet } from "@tanstack/react-router";
import { Toaster } from "sonner";
import { Sidebar } from "@/components/layout/Sidebar";
import { Topbar } from "@/components/layout/Topbar";
import { TooltipProvider } from "@/components/ui/tooltip";
import { useSidebar } from "@/hooks/useSidebar";
import { useEventStream } from "@/hooks/useEventStream";

export function AppLayout() {
  const { collapsed, toggle } = useSidebar();
  // Global SSE refresh stream: maps push events to query invalidations. Mounted
  // here since AppLayout renders only when authenticated (the cookie exists).
  useEventStream();
  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex min-h-screen bg-background text-foreground">
        <Sidebar collapsed={collapsed} />
        <div className="flex min-w-0 flex-1 flex-col">
          <Topbar collapsed={collapsed} onToggle={toggle} />
          <main className="flex min-h-0 w-full flex-1 flex-col p-4">
            <Outlet />
          </main>
        </div>
        <Toaster />
      </div>
    </TooltipProvider>
  );
}
```

- [ ] **Step 7: Update `web/src/routes/__root.test.tsx` for the new shell**

The test drives logout, which still exists, but it renders the singleton router and now needs `matchMedia`. Add the stub inside the `describe`, immediately above the `test(...)`:

```tsx
  beforeEach(() => {
    localStorage.clear();
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: false, media: q, addEventListener: () => {}, removeEventListener: () => {},
    }));
  });
```

and extend the import line at the top of the file to `import { beforeEach, describe, expect, test, vi } from "vitest";`. Everything else in the file stays: Logout is still a `button` with the accessible name "Logout" (it now lives in the sidebar), so the assertions hold unchanged.

- [ ] **Step 8: Run the tests to verify they pass**

Run from `web/`:

```bash
npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: the whole suite passes (including the new `Sidebar.test.tsx` (4 tests) and `project.$id.test.tsx` (5 tests)) no type errors, build succeeds.

- [ ] **Step 9: Commit**

```bash
git add web/src/components/AppLayout.tsx web/src/routes/project.\$id.tsx web/src/routes/project.\$id.test.tsx web/src/router.tsx web/src/components/Filters.tsx web/src/routes/__root.test.tsx web/src/components/layout/Sidebar.test.tsx
git commit -m "feat(web): sidebar shell + /project/\$id route"
```

---

### Task 7: Migrate the `ui/*` primitives onto the tokens

Mechanical class swap. Behaviour must not change, so the existing suite is the test.

**Files:**
- Modify: `web/src/components/ui/button.tsx`, `input.tsx`, `select.tsx`, `table.tsx`, `badge.tsx`, `dialog.tsx`, `drawer.tsx`, `tabs.tsx`

**Interfaces:**
- Consumes: the token utilities from Task 1.
- Produces: no API change: same exports, same props.

Substitution table (apply throughout, deleting the now-redundant `dark:` twin of each pair):

| Old | New |
| --- | --- |
| `bg-white` / `dark:bg-slate-950` (surface) | `bg-card` |
| `bg-white` / `dark:bg-slate-950` (page) | `bg-background` |
| `text-slate-900` / `dark:text-slate-50` | `text-foreground` |
| `text-slate-500` / `dark:text-slate-400` | `text-muted-foreground` |
| `border-slate-200` / `dark:border-slate-800` | `border-border` |
| `border-slate-300` / `dark:border-slate-700` (inputs) | `border-input` |
| `bg-slate-100` / `dark:bg-slate-800` (hover, subtle fill) | `bg-accent` (hover: `hover:bg-accent hover:text-accent-foreground`) |
| `bg-slate-100` / `dark:bg-slate-800` (muted fill) | `bg-muted` |
| `ring-slate-400` / `dark:ring-slate-500` | `ring-ring` |
| `bg-slate-900 text-slate-50` / `dark:bg-slate-50 dark:text-slate-900` (default button) | `bg-primary text-primary-foreground hover:bg-primary/90` |
| `bg-red-600` / `bg-red-700` (destructive) | `bg-danger text-primary-foreground hover:bg-danger/90` |
| `text-red-600` / `dark:text-red-400` | `text-danger` |
| `text-amber-600` / `dark:text-amber-400` | `text-warning` |
| `text-green-600` / `dark:text-green-400` | `text-success` |

- [ ] **Step 1: Rewrite `web/src/components/ui/button.tsx`'s variants**

Replace the `cva` call (keep the rest of the file (`ButtonProps`, `forwardRef`, exports) exactly as is):

```tsx
const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default: "bg-primary text-primary-foreground hover:bg-primary/90",
        destructive: "bg-danger text-primary-foreground hover:bg-danger/90",
        outline: "border border-border bg-transparent hover:bg-accent hover:text-accent-foreground",
        secondary: "bg-muted text-foreground hover:bg-accent",
        ghost: "hover:bg-accent hover:text-accent-foreground",
        link: "text-foreground underline-offset-4 hover:underline",
      },
      size: {
        default: "h-9 px-4 py-2",
        sm: "h-8 rounded-md px-3 text-xs",
        lg: "h-10 rounded-md px-8",
        icon: "h-9 w-9",
      },
    },
    defaultVariants: { variant: "default", size: "default" },
  },
);
```

- [ ] **Step 2: Apply the substitution table to the other seven primitives**

Work through `input.tsx`, `select.tsx`, `table.tsx`, `badge.tsx`, `dialog.tsx`, `drawer.tsx`, `tabs.tsx`. Replace each `slate-*`/`dark:slate-*` pair per the table above. Two rules:

- Every `dark:` variant that existed *only* to pair a slate colour with its dark twin is deleted, the token already carries both.
- Overlay/scrim classes (e.g. `bg-black/50`, `bg-slate-950/50` on dialog and drawer overlays) become `bg-foreground/20`: do not leave a raw slate there.

Sanity check afterwards, from `web/`:

```bash
grep -rn "slate-" src/components/ui/
```

Expected: no output.

- [ ] **Step 3: Sharpen the table primitive while you're in it**

In `web/src/components/ui/table.tsx`, tighten the row/cell defaults (this is the "denser, sharper" pass from the spec, apply to the existing `TableHead`, `TableRow`, `TableCell` class strings, keeping their structure):

- `TableRow`: `border-b border-border transition-colors hover:bg-muted/50`
- `TableHead`: `h-9 px-3 text-left align-middle text-xs font-medium tracking-wide text-muted-foreground uppercase`
- `TableCell`: `px-3 py-2 align-middle`

- [ ] **Step 4: Verify nothing regressed**

Run from `web/`:

```bash
npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: the whole suite passes (the tests assert text and roles, not classes), no type errors, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui
git commit -m "refactor(web): move ui primitives onto design tokens"
```

---

### Task 8: Migrate + sharpen the app components

**Files:**
- Modify: `web/src/components/DashboardStats.tsx`, `DashboardTable.tsx`, `Filters.tsx`, `StatusBadge.tsx`, `SeverityDelta.tsx`, `ReviewDrawer.tsx`, `ApplyPanel.tsx`, `CommandPreview.tsx`, `Changelog.tsx`, `HistoryTimeline.tsx`, `EventItem.tsx`, `DigestShort.tsx`, `BulkActions.tsx`, `RollbackButton.tsx`, `ComposeModal.tsx`
- Modify: `web/src/routes/dashboard.tsx`, `jobs.tsx`, `settings.tsx`, `service.$id.tsx`, `login.tsx`, `setup.tsx`, `project.$id.tsx`
- Modify: `web/src/components/settings/*.tsx`
- Modify: `web/src/auth/AuthGate.tsx`

**Interfaces:**
- Consumes: token utilities (Task 1), `Separator` (Task 1).
- Produces: no API change to any component.

Apply the same substitution table as Task 7. Severity/status colours (`amber`, `red`, `green` in `StatusBadge` / `SeverityDelta`) map onto `text-warning` / `text-danger` / `text-success` and their `/15` tinted backgrounds (e.g. `bg-warning/15 text-warning`).

- [ ] **Step 1: Sharpen `DashboardStats.tsx`'s `Tile`**

Give the tile an icon slot and token classes. Replace the `Tile` component and add the icon props at each call site:

```tsx
import type { LucideIcon } from "lucide-react";

function Tile({
  label,
  value,
  icon: Icon,
  tone,
  onClick,
  selected,
}: {
  label: string;
  value: string | number;
  icon?: LucideIcon;
  tone?: string;
  onClick?: () => void;
  selected?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      aria-pressed={onClick ? selected : undefined}
      className={cn(
        "relative flex min-w-36 flex-col items-start rounded-lg border border-border bg-card px-4 py-3 text-left transition-colors enabled:hover:bg-accent",
        selected && "ring-2 ring-ring",
      )}
    >
      {Icon && <Icon className="absolute top-3 right-3 h-4 w-4 text-muted-foreground" aria-hidden />}
      <span className={cn("text-2xl font-semibold tabular-nums", tone)}>{value}</span>
      <span className="mt-0.5 text-xs text-muted-foreground">{label}</span>
    </button>
  );
}
```

Then, in the returned JSX, add `icon={…}` to each tile and swap the tones: Services: `icon={Boxes}`; Updates available: `icon={ArrowUpCircle}` with `tone={open.length > 0 ? "text-warning" : undefined}`; Pinned: `icon={Pin}`; Stopped: `icon={CircleStop}` with `tone={stopped.length > 0 ? "text-danger" : undefined}`; Last scan: `icon={Clock}`; Docker: `icon={AlertTriangle}` with `tone="text-danger"`. Import them: `import { AlertTriangle, ArrowUpCircle, Boxes, CircleStop, Clock, Pin } from "lucide-react";`

- [ ] **Step 2: Migrate the remaining components and routes**

Walk the file list above, applying the substitution table. Specific sharpening beyond the swap:

- `DashboardTable.tsx`: the `EMPTY` constant becomes `<span className="text-muted-foreground">, </span>`; project header rows get `bg-muted/40 font-medium`; wrap the table in `<div className="min-h-0 flex-1 overflow-auto rounded-lg border border-border">` and drop any per-table border it had, so the header can be `sticky top-0 z-10 bg-card`.
- `Filters.tsx`, `settings/*`, `login.tsx`, `setup.tsx`: swap only; no layout change.
- `dashboard.tsx` / `project.$id.tsx`: loading skeleton `bg-slate-100 dark:bg-slate-800` → `bg-muted`; error text → `text-danger`; empty-state border → `border-border`.

- [ ] **Step 3: Prove no hardcoded palette survives**

Run from `web/`:

```bash
grep -rn "slate-\|dark:bg-\|dark:text-\|dark:border-" src/ --include=*.tsx | grep -v ".test.tsx"
```

Expected: **no output.** (`index.css`'s `.changelog-body` rules keep their literal hex colours: that block is out of scope and is not a `.tsx` file.)

- [ ] **Step 4: Verify**

Run from `web/`:

```bash
npm test && ./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: the whole suite passes, no type errors, build succeeds.

- [ ] **Step 5: Look at it**

Run from the repo root:

```bash
mise run build && ./dockbrr
```

Open the app, then check, in both light and dark mode: the sidebar collapses and expands with an animation; the rail shows tooltips; a project click lands on `/project/$id`; the version renders at the bottom; the stat tiles and table read as one system.

- [ ] **Step 6: Commit**

```bash
git add web/src
git commit -m "refactor(web): move app components onto design tokens; sharpen tiles + table"
```

---

### Task 9: Full verification

- [ ] **Step 1: Run every gate**

```bash
mise run check
cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build
```

Expected: `go vet` clean, Go tests pass, vitest passes, no type errors, SPA build succeeds.

- [ ] **Step 2: Confirm the static-binary invariant holds**

```bash
CGO_ENABLED=0 go build ./... && git status --short web/dist
```

Expected: build succeeds; `web/dist/index.html` is the tracked placeholder, unmodified (`mise run build` restores it).

- [ ] **Step 3: Commit anything outstanding, then open the PR**

```bash
git add -A && git commit -m "chore(web): sidebar shell verification pass" || true
git push -u origin worktree-rm-airgap
gh pr create --draft --title "Sidebar shell + design-token restyle" --body "$(cat <<'EOF'
## Summary
- Replaces the top-header nav with a collapsible left sidebar: Dashboard / Jobs / Settings, the project list with update badges and health dots, Logout, and the version.
- Adds `/project/$id`: a per-project view reusing the dashboard's stats and table, scoped to one project.
- Moves every colour onto a CSS design-token layer (`--background`, `--card`, `--primary`, …) so a future theme selector is a CSS-only override.

## Test plan
- `mise run check` (go vet + go test + vitest)
- `cd web && ./node_modules/.bin/tsc -b --noEmit && npm run build`
- Manual: collapse/expand in both themes, rail tooltips, project navigation, version render.

Spec: `docs/dev/specs/2026-07-12-sidebar-shell-design.md`
Plan: `docs/dev/plans/2026-07-12-sidebar-shell.md`

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-review

**Spec coverage:** layout shell → Tasks 5–6; collapse + localStorage + mobile → Task 2; version from `/api/status` → Task 5; `/project/$id` + `hideProject` → Task 6; badge + dot precedence → Task 3; token layer + `@theme inline` + accent-behind-`--primary` → Task 1; component migration + tile/table sharpening → Tasks 7–8; the four test files the spec names → Tasks 2, 3, 6; gates → Tasks 8–9. No spec section is unclaimed.

**Known ordering wart:** Task 5's `Link to="/project/$id"` does not typecheck until Task 6 Step 5 registers the route. Called out inline in Task 5 Step 5.
