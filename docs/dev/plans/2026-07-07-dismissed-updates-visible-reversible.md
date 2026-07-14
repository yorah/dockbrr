# Visible + Reversible Dismissed Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a dismissed update stay visible on the dashboard with a grey "Dismissed" badge + version delta, and let the user restore it back to "available" from the review drawer.

**Architecture:** A dismissed update is already persisted (`status='dismissed'`) and already returned by `GET /api/updates`; it is only dropped client-side by the row join. We (1) join dismissed updates to their service row, (2) add a `"dismissed"` status to the badge, (3) add a `POST /api/updates/{id}/restore` endpoint that flips status to `available`, and (4) surface a Restore action in the drawer. No detection, compose, or Docker-mutation path changes.

**Tech Stack:** Go (chi router, modernc sqlite) backend; React + TypeScript + TanStack Query + Radix/shadcn + Tailwind frontend; vitest + Go `testing`.

## Global Constraints

- Backend must stay CGO-free: build with `CGO_ENABLED=0 go build ./...`.
- UI/API never touch Docker directly (invariant 2). This feature is a read-model change + a status flip; it touches neither Docker nor the Job Engine.
- CSRF header required on mutations (invariant 7); the new `POST /restore` lives inside the CSRF-guarded route group next to `/dismiss`.
- TS typecheck via `./node_modules/.bin/tsc -b --noEmit` (NOT `npx tsc`: rtk proxy masks errors) and `npm run build`.
- Run frontend commands from `web/`. Vitest binary: `./node_modules/.bin/vitest`.
- `npm run build` overwrites the tracked placeholder `internal/httpapi/dist/index.html`; restore it (`git checkout -- internal/httpapi/dist/index.html`) before committing.

---

### Task 1: Restore endpoint (backend)

**Files:**
- Modify: `internal/httpapi/updates.go` (add `handleRestore`, next to `handleDismiss` ~line 89-112)
- Modify: `internal/httpapi/server.go:108` (register route after the `/dismiss` line)
- Test: `internal/httpapi/actions_test.go` (add tests after `TestDismissSetsStatus` ~line 122)

**Interfaces:**
- Consumes: `s.deps.Updates.Get(id) (store.Update, error)`, `store.ErrUpdateNotFound`, `s.deps.Updates.SetStatus(id int64, status string) error`, `s.deps.Events.Insert(store.Event) (int64, error)`, `pathInt64`, `writeJSON`, `writeJSONError`: all already present and used by `handleDismiss`.
- Produces: route `POST /api/updates/{id}/restore` → 200 `{"status":"available"}`; 404 on unknown id; 400 on bad id.

- [ ] **Step 1: Write the failing tests**

Add to `internal/httpapi/actions_test.go` (mirrors `TestDismissSetsStatus`):

```go
func TestRestoreSetsStatusAvailable(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	pid, _ := store.NewProjects(db).Upsert(store.Project{HostID: 1, Kind: "compose", Name: "p", Source: "discovered"})
	sid, _ := store.NewServices(db).Upsert(store.Service{ProjectID: pid, Name: "app"})
	uid, _ := store.NewUpdates(db).Upsert(store.Update{ServiceID: sid, ToDigest: "sha256:new", Status: "dismissed"})

	req := authReq(httptest.NewRequest(http.MethodPost, pathf("/api/updates/%d/restore", uid), nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore = %d", rec.Code)
	}
	// ListOpen returns only status='available' rows, so a restored update reappears.
	open, _ := store.NewUpdates(db).ListOpen()
	if len(open) != 1 {
		t.Fatalf("update not open after restore: %+v", open)
	}
}

func TestRestoreUnknownIDReturns404(t *testing.T) {
	s, db, tok, csrf := authedServer(t, Deps{})
	s.deps = mergeDeps(s.deps, actionDeps(db, &fakeEngine{}, &fakeChecker{}))
	req := authReq(httptest.NewRequest(http.MethodPost, "/api/updates/9999/restore", nil), tok, csrf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("restore unknown id = %d, want 404", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/httpapi/ -run TestRestore -v`
Expected: FAIL: route not registered → 404 for the happy path too (or 405), and `handleRestore` undefined once referenced.

- [ ] **Step 3: Add the handler**

In `internal/httpapi/updates.go`, add directly after `handleDismiss`:

```go
// handleRestore flips a dismissed update back to available, undoing a prior
// dismiss. Mirrors handleDismiss. Records a "restored" service event.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	upd, err := s.deps.Updates.Get(id)
	if err != nil {
		if errors.Is(err, store.ErrUpdateNotFound) {
			writeJSONError(w, http.StatusNotFound, err)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.deps.Updates.SetStatus(id, "available"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err)
		return
	}
	_, _ = s.deps.Events.Insert(store.Event{
		ServiceID: upd.ServiceID, Kind: "restored", ToDigest: upd.ToDigest, Message: "update restored",
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "available"})
}
```

- [ ] **Step 4: Register the route**

In `internal/httpapi/server.go`, immediately after the `/dismiss` line (108):

```go
		r.Post("/api/updates/{id}/dismiss", s.handleDismiss)
		r.Post("/api/updates/{id}/restore", s.handleRestore)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/httpapi/ -run TestRestore -v`
Expected: PASS (both tests).

- [ ] **Step 6: Full backend gate**

Run: `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./internal/httpapi/`
Expected: build ok, vet clean, package tests pass.

- [ ] **Step 7: Commit**

```bash
git add internal/httpapi/updates.go internal/httpapi/server.go internal/httpapi/actions_test.go
git commit -m "feat(api): POST /api/updates/{id}/restore un-dismisses an update"
```

---

### Task 2: Join dismissed updates to their row (frontend)

**Files:**
- Modify: `web/src/hooks/useDashboardRows.ts`
- Test: `web/src/hooks/useDashboardRows.test.tsx`

**Interfaces:**
- Consumes: `Update.status` (`"available" | "dismissed" | ...`), existing `FilterState`, `Row`.
- Produces: `joinRows` now attaches a `dismissed` update to `Row.update` when no `available` update exists for that service; `onlyUpdates` and the `update-available` status filter continue to match **available only**.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/hooks/useDashboardRows.test.tsx` inside the `describe("joinRows", ...)` block. These reuse the existing `projects`/`updates` fixtures (service id 10 has an available update id 99; service id 11 has none):

```tsx
  test("a dismissed update joins to its row so the row is reviewable", () => {
    const dismissed: Update[] = [
      { ...updates[0], id: 77, service_id: 11, status: "dismissed" },
    ];
    const rows = joinRows(projects, dismissed, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    const db = rows.find((r) => r.kind === "service" && r.service.id === 11);
    expect(db && db.kind === "service" && db.update?.id).toBe(77);
  });

  test("onlyUpdates excludes a dismissed-only row (not actionable)", () => {
    const dismissed: Update[] = [
      { ...updates[0], id: 77, service_id: 11, status: "dismissed" },
    ];
    const rows = joinRows(projects, dismissed, { onlyUpdates: true, project: "", status: "", search: "", showRemoved: false });
    expect(rows.some((r) => r.kind === "service" && r.service.id === 11)).toBe(false);
  });

  test("an available update wins over a dismissed one for the same service", () => {
    const mixed: Update[] = [
      updates[0], // available, service 10
      { ...updates[0], id: 78, service_id: 10, status: "dismissed" },
    ];
    const rows = joinRows(projects, mixed, { onlyUpdates: false, project: "", status: "", search: "", showRemoved: false });
    const web = rows.find((r) => r.kind === "service" && r.service.id === 10);
    expect(web && web.kind === "service" && web.update?.status).toBe("available");
  });
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `web/`): `./node_modules/.bin/vitest run src/hooks/useDashboardRows.test.tsx`
Expected: FAIL: the dismissed update is dropped (`byService` only maps `available`), so `db.update` is undefined.

- [ ] **Step 3: Update the join + filters**

In `web/src/hooks/useDashboardRows.ts`, replace the `byService` build loop:

```ts
  const byService = new Map<number, Update>();
  for (const u of updates) if (openStatuses.has(u.status)) byService.set(u.service_id, u);
```

with (available wins; dismissed fills in only when no available update is present, order-independent):

```ts
  const byService = new Map<number, Update>();
  for (const u of updates) {
    if (u.status === "available") byService.set(u.service_id, u);
    else if (u.status === "dismissed" && !byService.has(u.service_id)) byService.set(u.service_id, u);
  }
```

Then update the two filter lines so "actionable update" means `available`:

```ts
      if (f.onlyUpdates && update?.status !== "available") continue;
```
```ts
      if (f.status === "update-available" && update?.status !== "available") continue;
```

Leave the `up-to-date` filter as-is (`if (f.status === "up-to-date" && (update || s.pinned)) continue;`). A dismissed update makes `update` truthy, correctly excluding the row from "up to date".

Note: `openStatuses` (`new Set(["available"])`) is still used by `updatesByService` in the hook below, leave that untouched so any actionable-update consumers stay available-only.

- [ ] **Step 4: Run tests to verify they pass**

Run (from `web/`): `./node_modules/.bin/vitest run src/hooks/useDashboardRows.test.tsx`
Expected: PASS (all cases, including the pre-existing ones).

- [ ] **Step 5: Commit**

```bash
git add web/src/hooks/useDashboardRows.ts web/src/hooks/useDashboardRows.test.tsx
git commit -m "feat(dashboard): join dismissed updates to their service row"
```

---

### Task 3: Dismissed status badge (frontend)

**Files:**
- Modify: `web/src/components/StatusBadge.tsx`
- Modify: `web/src/components/DashboardTable.tsx:146`
- Test: `web/src/components/StatusBadge.test.tsx`

**Interfaces:**
- Consumes: `Row.update` (an `Update | undefined`) from Task 2.
- Produces: `Status` union gains `"dismissed"`; `computeStatus(svc, update, opts)` second param widened to `{ open: boolean; dismissed?: boolean } | undefined` and returns `"dismissed"` when the update is dismissed and no higher-precedence state applies.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/components/StatusBadge.test.tsx`:

```tsx
test("dismissed update yields the dismissed status", () => {
  expect(computeStatus(svc(), { open: false, dismissed: true })).toBe("dismissed");
});

test("pinned wins over a dismissed update", () => {
  expect(computeStatus(svc({ pinned: true }), { open: false, dismissed: true })).toBe("pinned");
});

test("a stopped state wins over a dismissed update", () => {
  expect(computeStatus(svc({ state: "exited" }), { open: false, dismissed: true })).toBe("stopped");
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `web/`): `./node_modules/.bin/vitest run src/components/StatusBadge.test.tsx`
Expected: FAIL: `computeStatus` ignores `dismissed`, returns `"up-to-date"`; and `"dismissed"` is not a valid `Status` (tsc/type error in the test).

- [ ] **Step 3: Extend the Status union, maps, and computeStatus**

In `web/src/components/StatusBadge.tsx`:

Add `"dismissed"` to the union:

```ts
export type Status =
  | "up-to-date"
  | "update-available"
  | "pinned"
  | "error"
  | "updating"
  | "stopped"
  | "restarting"
  | "gone"
  | "dismissed";
```

Add label + variant entries (grey, matching the dismissed-event treatment):

```ts
// in LABEL:
  dismissed: "Dismissed",
```
```ts
// in VARIANT:
  dismissed: "default",
```

Widen `computeStatus` and add the branch after the update-available check:

```ts
export function computeStatus(
  svc: Service,
  update: { open: boolean; dismissed?: boolean } | undefined,
  opts: { updating?: boolean } = {},
): Status {
  if (opts.updating) return "updating";
  if (svc.state === "gone") return "gone";
  if (isStopped(svc.state)) return "stopped";
  if (svc.state === "restarting") return "restarting";
  if (svc.pinned) return "pinned";
  if (svc.state === "error") return "error";
  if (update?.open) return "update-available";
  if (update?.dismissed) return "dismissed";
  return "up-to-date";
}
```

- [ ] **Step 4: Feed the dismissed flag from the table**

In `web/src/components/DashboardTable.tsx:146`, replace:

```ts
        const status = computeStatus(r.service, r.update ? { open: true } : undefined);
```

with:

```ts
        const status = computeStatus(
          r.service,
          r.update ? { open: r.update.status === "available", dismissed: r.update.status === "dismissed" } : undefined,
        );
```

- [ ] **Step 5: Run tests + typecheck to verify pass**

Run (from `web/`): `./node_modules/.bin/vitest run src/components/StatusBadge.test.tsx && ./node_modules/.bin/tsc -b --noEmit`
Expected: PASS, no type errors. (The other `computeStatus` caller `service.$id.tsx` passes `undefined` and is unaffected; existing tests passing `{ open: true|false }` still compile, `dismissed` is optional.)

- [ ] **Step 6: Commit**

```bash
git add web/src/components/StatusBadge.tsx web/src/components/DashboardTable.tsx web/src/components/StatusBadge.test.tsx
git commit -m "feat(dashboard): grey Dismissed status badge"
```

---

### Task 4: Restore action in the review drawer (frontend)

**Files:**
- Modify: `web/src/hooks/mutations.ts` (add `useRestore`, next to `useDismiss`)
- Modify: `web/src/components/ReviewDrawer.tsx`
- Modify: `web/src/components/EventItem.tsx` (add a `restored` kind label)
- Test: `web/src/components/ReviewDrawer.test.tsx`

**Interfaces:**
- Consumes: `POST /api/updates/{id}/restore` (Task 1); `apiFetch`, `invalidate`, `keys.updates`, `keys.projects`, `toastError` (already in `mutations.ts`); `update.status` on the drawer's `Update` prop.
- Produces: `useRestore()` mutation hook; drawer renders **Restore** instead of **Dismiss** when `update.status === "dismissed"`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/components/ReviewDrawer.test.tsx` (fixtures `update`, `service`, `project` already defined at top of the file; import `waitFor`/`screen`/`userEvent`/`http`/`HttpResponse`/`server` already present):

```tsx
test("a dismissed update shows Restore and calls the restore endpoint", async () => {
  let restored = false;
  server.use(
    http.post("/api/updates/7/restore", () => {
      restored = true;
      return HttpResponse.json({ status: "available" });
    }),
  );
  const onClose = vi.fn();
  renderWithClient(
    <ReviewDrawer
      update={{ ...update, status: "dismissed" }}
      service={service}
      project={project}
      onClose={onClose}
      onApplied={vi.fn()}
    />,
  );

  // Dismiss button is replaced by Restore.
  expect(screen.queryByRole("button", { name: /^dismiss$/i })).toBeNull();
  await userEvent.click(screen.getByRole("button", { name: /^restore$/i }));

  await waitFor(() => expect(restored).toBe(true));
  await waitFor(() => expect(onClose).toHaveBeenCalled());
});
```

- [ ] **Step 2: Run test to verify it fails**

Run (from `web/`): `./node_modules/.bin/vitest run src/components/ReviewDrawer.test.tsx`
Expected: FAIL: no Restore button exists; the drawer always renders Dismiss.

- [ ] **Step 3: Add the useRestore mutation**

In `web/src/hooks/mutations.ts`, add directly after `useDismiss`:

```ts
export function useRestore() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => apiFetch(`/api/updates/${id}/restore`, { method: "POST" }),
    onSuccess: () => invalidate(qc, keys.updates, keys.projects),
    onError: toastError,
  });
}
```

- [ ] **Step 4: Wire Restore into the drawer**

In `web/src/components/ReviewDrawer.tsx`:

Update the import:

```ts
import { useApply, useDismiss, useRestore } from "@/hooks/mutations";
```

Add the hook + handler next to the existing `dismiss`:

```ts
  const restore = useRestore();
```
```ts
  function handleRestore() {
    restore.mutate(update.id, { onSuccess: () => onClose() });
  }
```

Replace the Dismiss button in `DrawerFooter` with a status-conditional button:

```tsx
        <DrawerFooter className="flex-row justify-end gap-2">
          {update.status === "dismissed" ? (
            <Button variant="outline" onClick={handleRestore} disabled={restore.isPending}>
              Restore
            </Button>
          ) : (
            <Button variant="outline" onClick={handleDismiss} disabled={dismiss.isPending}>
              Dismiss
            </Button>
          )}
          <Button onClick={handleApply} disabled={apply.isPending}>
            Apply
          </Button>
        </DrawerFooter>
```

- [ ] **Step 5: Add the restored event label**

In `web/src/components/EventItem.tsx`, add `Eye` to the lucide import and a `restored` entry to `KIND_META`:

```ts
import { CheckCircle2, Circle, Eye, EyeOff, Info, PlayCircle, RotateCcw, XCircle, type LucideIcon } from "lucide-react";
```
```ts
  restored: { label: "Restored", icon: Eye, className: "text-emerald-600 dark:text-emerald-400" },
```

- [ ] **Step 6: Run test to verify it passes**

Run (from `web/`): `./node_modules/.bin/vitest run src/components/ReviewDrawer.test.tsx`
Expected: PASS.

- [ ] **Step 7: Full frontend gate**

Run (from `web/`): `./node_modules/.bin/vitest run && ./node_modules/.bin/tsc -b --noEmit && npm run build`
Expected: all tests pass, no type errors, build succeeds.
Then restore the placeholder: `git checkout -- internal/httpapi/dist/index.html`

- [ ] **Step 8: Commit**

```bash
git add web/src/hooks/mutations.ts web/src/components/ReviewDrawer.tsx web/src/components/EventItem.tsx web/src/components/ReviewDrawer.test.tsx
git commit -m "feat(dashboard): Restore action for dismissed updates"
```

---

## Final verification

- [ ] `CGO_ENABLED=0 go build ./... && go vet ./... && go test ./...`, all green.
- [ ] From `web/`: `./node_modules/.bin/vitest run && ./node_modules/.bin/tsc -b --noEmit && npm run build`, all green.
- [ ] `git status` clean except intended files; `internal/httpapi/dist/index.html` NOT modified.
- [ ] Manual (optional, needs running app + a dismissed update): dashboard shows the service with a grey **Dismissed** badge and its version delta; clicking the row's Review (eye) opens the drawer; **Restore** flips it back to **Update available**.
