# Container Remove UX Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the checkbox+"Remove selected" flow with a per-row Remove button on every stopped standalone container and a single "Remove stopped containers" bulk button on the Loose header.

**Architecture:** All changes are in one React component, `web/src/components/DashboardTable.tsx`, plus its test. The `ActionsCell` gains a per-row Remove button gated on a new `canRemove` prop; the Loose header swaps its bulk button; the checkbox selection mechanic is deleted. Backend is untouched — the existing `POST /api/services/:id/remove` endpoint and its guard (`kind == "standalone"` and stopped) already cover this.

**Tech Stack:** React + TypeScript, TanStack Table/Query, Tailwind, Radix/shadcn UI, lucide-react icons. Tests: vitest + @testing-library/react + msw.

## Global Constraints

- CGO-free / single-binary invariant is unaffected (no Go change).
- UI never touches Docker directly; all removal flows through the Job Engine via the existing endpoint (safety invariant 2).
- TS typecheck must be run as `cd web && ./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc` — the rtk hook masks real errors). `npm run build` also fails on type errors as a backstop.
- Full check: `mise run check` (go vet + go test + vitest).
- Confirm copy for a single removal: `Remove stopped container "<name>"? This cannot be undone.`
- Confirm copy for the bulk removal: `Remove <n> stopped container<s>: <names>? This cannot be undone.` (`<s>` = "s" when n>1).
- Per-row button aria-label: `Remove <service.name>`. Bulk button visible text: `Remove stopped containers`.

---

## File Structure

- Modify: `web/src/components/DashboardTable.tsx`
  - `ActionsCell` — add `canRemove` prop + Remove button (Task 1).
  - `buildColumns` — pass `canRemove` into `ActionsCell` (Task 1); drop checkbox params + name-column checkbox (Task 2).
  - `DashboardTable` component — swap Loose header button, add `removeStoppedLoose`, delete `looseSelected`/`toggleLooseSelect`/`removeSelectedLoose` (Task 2).
- Modify: `web/src/components/DashboardTable.test.tsx`
  - Add per-row Remove tests (Task 1); rewrite the bulk-remove test, drop checkbox assertions (Task 2).

No files created. No backend files touched.

---

## Task 1: Per-row Remove button in `ActionsCell`

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (`ActionsCell` ~lines 96-277, `buildColumns` actions cell ~lines 465-482)
- Test: `web/src/components/DashboardTable.test.tsx`

**Interfaces:**
- Consumes: `useRemoveContainer()` from `@/hooks/mutations` (already imported at line 44) — returns a mutation whose `.mutate(serviceId: number)` POSTs `/api/services/:id/remove` and whose `.isPending` is a boolean. `isStopped(state: string)` from `@/components/StatusBadge` (already imported line 36). `Trash2` icon (already imported line 20).
- Produces: `ActionsCell` now requires a `canRemove: boolean` prop. Its only caller is `buildColumns` (this task wires it).

- [ ] **Step 1: Write the failing tests**

Add these two tests at the end of `web/src/components/DashboardTable.test.tsx`:

```tsx
test("offers a per-row Remove button for a stopped standalone container and removes it on confirm", async () => {
  const removed: string[] = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 5, name: "my-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/remove", ({ params }) => {
      removed.push(String(params.id));
      return HttpResponse.json({ job_id: 999 });
    }),
  );
  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    renderDashboardWithRouter();
    await waitFor(() => expect(screen.getByText("grafana")).toBeInTheDocument());
    const row = screen.getByText("grafana").closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: /^remove grafana$/i }));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(removed).toEqual(["30"]));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("no per-row Remove button for a running standalone or a stopped compose service", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 5, name: "run-standalone", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 30, name: "grafana", image_ref: "grafana:11", current_digest: "sha256:g", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 1, name: "app", kind: "compose", working_dir: "/srv",
          auto_update_enabled: false, unmanaged: false, auto_named: false,
          services: [{ id: 10, name: "web", image_ref: "nginx:1.27", current_digest: "sha256:a", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  await waitFor(() => expect(screen.getByText("grafana")).toBeInTheDocument());
  const runRow = screen.getByText("grafana").closest("tr")!;
  const composeRow = screen.getByText("web").closest("tr")!;
  expect(within(runRow).queryByRole("button", { name: /^remove grafana$/i })).not.toBeInTheDocument();
  expect(within(composeRow).queryByRole("button", { name: /^remove web$/i })).not.toBeInTheDocument();
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/DashboardTable.test.tsx -t "per-row Remove"`
Expected: FAIL — the first test cannot find a `Remove grafana` button.

- [ ] **Step 3: Add the `canRemove` prop and Remove button to `ActionsCell`**

In `web/src/components/DashboardTable.tsx`, extend the `ActionsCell` prop list (the object type ending around line 112) to include `canRemove`:

```tsx
  onApplied,
  onChangelog,
  onLogs,
  canRemove,
}: {
  service: Service;
  update: Update | undefined;
  /** Update whose cached changelog the eye opens: pending, else last applied. */
  changelog: Update | undefined;
  onApplied: DashboardTableProps["onApplied"];
  onChangelog: DashboardTableProps["onChangelog"];
  onLogs: (service: Service) => void;
  /** True only for stopped standalone containers (backend guard mirror). */
  canRemove: boolean;
}) {
```

Add the mutation hook next to the existing ones near the top of `ActionsCell` (after `const lifecycle = useLifecycle();`, ~line 115):

```tsx
  const removeContainer = useRemoveContainer();
```

Add the Remove button as the LAST child inside the `<div className="flex items-center gap-1">`, immediately after the Logs `</Tooltip>` and before the closing `</div>` (~line 274):

```tsx
        {canRemove && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                size="sm"
                variant="ghost"
                className="h-7 w-7 p-0"
                disabled={removeContainer.isPending}
                aria-label={`Remove ${service.name}`}
                onClick={(e) => {
                  e.stopPropagation();
                  if (!window.confirm(`Remove stopped container "${service.name}"? This cannot be undone.`)) return;
                  removeContainer.mutate(service.id);
                }}
              >
                <Trash2 className="h-4 w-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Remove</TooltipContent>
          </Tooltip>
        )}
```

- [ ] **Step 4: Pass `canRemove` from `buildColumns`**

In `buildColumns`, the actions column cell (~lines 471-479) renders `<ActionsCell .../>`. Add the `canRemove` prop:

```tsx
          <ActionsCell
            service={r.service}
            update={r.update}
            changelog={changelogUpdate(r)}
            canRemove={r.project.kind === "standalone" && isStopped(r.service.state)}
            onApplied={onApplied}
            onChangelog={onChangelog}
            onLogs={onLogs}
          />
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/DashboardTable.test.tsx -t "per-row Remove"`
Expected: PASS (both tests).

- [ ] **Step 6: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output (exit 0).

- [ ] **Step 7: Commit**

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): per-row Remove button on stopped standalone containers"
```

---

## Task 2: Loose header bulk button + delete checkbox mechanic

**Files:**
- Modify: `web/src/components/DashboardTable.tsx` (`buildColumns` name column + signature ~lines 343-383, `DashboardTable` component state/handlers ~lines 496-551, Loose header render ~lines 592-633)
- Test: `web/src/components/DashboardTable.test.tsx`

**Interfaces:**
- Consumes: `looseServices` memo (already present, ~line 508) — array of `{ kind: "service"; project: Project; service: Service; ... }` for every auto-named row. `removeContainer` (`useRemoveContainer()`, already at ~line 500). `isStopped` (imported).
- Produces: `buildColumns(onApplied, onChangelog, onLogs)` — the `looseSelected`/`onToggleLooseSelect` params are removed; any caller must pass only the three remaining args.

- [ ] **Step 1: Rewrite the bulk-remove test**

In `web/src/components/DashboardTable.test.tsx`, REPLACE the existing test `"bulk-removes selected stopped loose containers after a confirm listing their names, and never offers a checkbox for a running one"` (the last test, ~lines 727-785) with these two tests:

```tsx
test("Loose header 'Remove stopped containers' removes every stopped loose container on confirm, skipping running ones", async () => {
  const removed: string[] = [];
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 2, name: "adoring_saha", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 20, name: "adoring_saha", image_ref: "busybox:latest", current_digest: "sha256:b", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 3, name: "sleepy_lamarr", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 21, name: "sleepy_lamarr", image_ref: "redis:8.8", current_digest: "sha256:c", state: "exited", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
        {
          id: 4, name: "brave_turing", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 22, name: "brave_turing", image_ref: "alpine:3.20", current_digest: "sha256:d", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
    http.post("/api/services/:id/remove", ({ params }) => {
      removed.push(String(params.id));
      return HttpResponse.json({ job_id: 999 });
    }),
  );
  renderDashboardWithRouter();

  await waitFor(() => expect(screen.getByRole("main")).toBeInTheDocument());
  const main = within(screen.getByRole("main"));
  const bulk = await waitFor(() => main.getByRole("button", { name: /remove stopped containers/i }));
  expect(bulk).not.toBeDisabled(); // two stopped loose containers exist

  const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
  try {
    await userEvent.click(bulk);
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    const message = confirmSpy.mock.calls[0][0] as string;
    expect(message).toContain("adoring_saha");
    expect(message).toContain("sleepy_lamarr");
    expect(message).not.toContain("brave_turing"); // running loose is skipped

    await waitFor(() => expect(new Set(removed)).toEqual(new Set(["20", "21"])));
  } finally {
    confirmSpy.mockRestore();
  }
});

test("Loose header 'Remove stopped containers' is disabled when every loose container is running", async () => {
  server.use(
    http.get("/api/projects", () =>
      HttpResponse.json([
        {
          id: 4, name: "brave_turing", kind: "standalone", working_dir: "",
          auto_update_enabled: false, unmanaged: false, auto_named: true,
          services: [{ id: 22, name: "brave_turing", image_ref: "alpine:3.20", current_digest: "sha256:d", state: "running", pinned: false, healthcheck: false, auto_update_enabled: null }],
        },
      ]),
    ),
    http.get("/api/updates", () => HttpResponse.json([])),
  );
  renderDashboardWithRouter();
  const main = within(screen.getByRole("main"));
  const bulk = await waitFor(() => main.getByRole("button", { name: /remove stopped containers/i }));
  expect(bulk).toBeDisabled();
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/DashboardTable.test.tsx -t "Remove stopped containers"`
Expected: FAIL — no `Remove stopped containers` button exists yet (the header still says "Remove selected").

- [ ] **Step 3: Delete the checkbox name-column block and its params**

In `buildColumns` (`web/src/components/DashboardTable.tsx`), change the signature to drop the last two params:

```tsx
function buildColumns(
  onApplied: DashboardTableProps["onApplied"],
  onChangelog: DashboardTableProps["onChangelog"],
  onLogs: (service: Service) => void,
): ColumnDef<Row>[] {
```

Replace the `name` column cell (~lines 354-382) with the checkbox-free version:

```tsx
      cell: ({ row }) => {
        const r = row.original;
        if (r.kind !== "service") return null;
        return (
          <div className="flex items-center gap-2 pl-6">
            <Link
              to="/service/$id"
              params={{ id: String(r.service.id) }}
              className="hover:underline"
              onClick={(e) => e.stopPropagation()}
            >
              {r.service.name}
            </Link>
          </div>
        );
      },
```

- [ ] **Step 4: Replace component state/handlers**

In the `DashboardTable` component, DELETE these three pieces:
- `const [looseSelected, setLooseSelected] = useState<Set<number>>(() => new Set());` (~line 499)
- the `toggleLooseSelect` function (~lines 513-520)
- the `removeSelectedLoose` function (~lines 522-530)

Keep `const removeContainer = useRemoveContainer();` and the `looseServices` memo.

Add a stopped-loose memo (right after the `looseServices` memo, ~line 511) and a bulk handler:

```tsx
  const stoppedLooseServices = useMemo(
    () => looseServices.filter((r) => isStopped(r.service.state)),
    [looseServices],
  );

  function removeStoppedLoose() {
    if (stoppedLooseServices.length === 0) return;
    const names = stoppedLooseServices.map((r) => r.service.name).join(", ");
    const n = stoppedLooseServices.length;
    if (!window.confirm(`Remove ${n} stopped container${n > 1 ? "s" : ""}: ${names}? This cannot be undone.`)) return;
    for (const r of stoppedLooseServices) removeContainer.mutate(r.service.id);
  }
```

Update the `columns` memo (~lines 548-551) to drop the removed args and dep:

```tsx
  const columns = useMemo(
    () => buildColumns(onApplied, onChangelog, onLogs),
    [onApplied, onChangelog, onLogs],
  );
```

- [ ] **Step 5: Swap the Loose header button**

In the Loose header render (~lines 617-629), replace the "Remove selected" `<Button>` with:

```tsx
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 gap-1 px-2 text-xs"
                        disabled={stoppedLooseServices.length === 0}
                        onClick={(e) => {
                          e.stopPropagation();
                          removeStoppedLoose();
                        }}
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Remove stopped containers
                      </Button>
```

- [ ] **Step 6: Run the full DashboardTable test file**

Run: `cd web && npx vitest run src/components/DashboardTable.test.tsx`
Expected: PASS — all tests, including the two new Task-2 tests and the Task-1 tests. No leftover references to "Remove selected" or checkbox labels.

- [ ] **Step 7: Typecheck**

Run: `cd web && ./node_modules/.bin/tsc -b --noEmit`
Expected: no output (exit 0). Confirms `looseSelected`/`toggleLooseSelect` removal left no dangling references.

- [ ] **Step 8: Commit**

```bash
git add web/src/components/DashboardTable.tsx web/src/components/DashboardTable.test.tsx
git commit -m "feat(web): Loose 'Remove stopped containers' bulk button, drop checkbox selection"
```

---

## Task 3: Full-suite verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full project check**

Run: `mise run check`
Expected: go vet clean, go tests pass, vitest all green.

- [ ] **Step 2: Build (TS backstop + binary)**

Run: `mise run build`
Expected: SPA build succeeds (fails on any TS error), `./dockbrr` produced.

- [ ] **Step 3: Manual smoke (optional, if a Docker host is available)**

Start the app (`mise run run`), open the dashboard:
- A stopped standalone (non-loose) container shows a trash icon in its Actions; clicking it prompts a confirm and enqueues a remove job.
- The Loose header shows "Remove stopped containers", disabled when no loose container is stopped; clicking with stopped ones present prompts a confirm listing their names and removes them.
- Running or compose containers show no Remove control.

---

## Self-Review

**Spec coverage:**
- Per-row Remove button for all standalone stopped containers → Task 1. ✓
- Loose header "Remove stopped containers" bulk button → Task 2, Steps 4-5. ✓
- Drop checkbox mechanic (`looseSelected`, `toggleLooseSelect`, `removeSelectedLoose`, name-column checkbox, buildColumns params) → Task 2, Steps 3-4. ✓
- Tests: per-row appears/fires + absent for running/compose; bulk removes all stopped, disabled when none → Tasks 1-2. ✓
- Backend unchanged → confirmed, no Go files in file list. ✓

**Placeholder scan:** No TBD/TODO/"handle edge cases" — every code step shows full code. ✓

**Type consistency:** `canRemove: boolean` defined in Task 1 Step 3, consumed in Task 1 Step 4. `buildColumns` 3-arg signature set in Task 2 Step 3, its lone caller updated in Task 2 Step 4. `stoppedLooseServices`/`removeStoppedLoose` defined and referenced within Task 2. `removeContainer.mutate(serviceId: number)` matches the hook signature. ✓
